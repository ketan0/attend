// Package server implements the attendd HTTP API. The server is a thin
// shell around store + a clock; nothing here depends on macOS specifics so
// it's fully testable via httptest.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ketan0/attend/internal/rules"
	"github.com/ketan0/attend/internal/store"
)

// Clock returns the current time. Production uses time.Now; tests inject a
// frozen clock for deterministic IDs/timestamps.
type Clock func() time.Time

// IDGen returns the next rule ID. Production uses random UUIDs; tests inject
// a deterministic generator.
type IDGen func() string

// Server is the HTTP API.
type Server struct {
	Store          store.Store
	Now            Clock
	NewID          IDGen
	NewInjectionID IDGen
	// Version is reported by /v1/status; harmless if empty.
	Version string
}

// New constructs a Server with sensible defaults.
func New(s store.Store) *Server {
	return &Server{
		Store:          s,
		Now:            time.Now,
		NewID:          defaultID,
		NewInjectionID: defaultInjectionID,
	}
}

func defaultID() string {
	// Short, prefixed, URL-safe-ish.
	return "r_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
}

func defaultInjectionID() string {
	return "inj_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
}

// Handler returns an http.Handler that mounts every API route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/status", s.handleStatus)
	mux.HandleFunc("GET /v1/rules", s.handleListRules)
	mux.HandleFunc("POST /v1/rules", s.handleCreateRule)
	mux.HandleFunc("GET /v1/rules/{id}", s.handleGetRule)
	mux.HandleFunc("PATCH /v1/rules/{id}", s.handleUpdateRule)
	mux.HandleFunc("DELETE /v1/rules/{id}", s.handleDeleteRule)
	mux.HandleFunc("POST /v1/pause", s.handlePause)
	mux.HandleFunc("POST /v1/resume", s.handleResume)
	mux.HandleFunc("POST /v1/friction/result", s.handleFrictionResult)

	mux.HandleFunc("GET /v1/injections", s.handleListInjections)
	mux.HandleFunc("POST /v1/injections", s.handleCreateInjection)
	mux.HandleFunc("GET /v1/injections/{id}", s.handleGetInjection)
	mux.HandleFunc("DELETE /v1/injections/{id}", s.handleDeleteInjection)
	return mux
}

// --- response helpers --------------------------------------------------------

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiErrorEnvelope struct {
	Error apiError `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiErrorEnvelope{Error: apiError{Code: code, Message: msg}})
}

// --- /v1/status --------------------------------------------------------------

// StatusResponse is the JSON shape returned by GET /v1/status.
type StatusResponse struct {
	Version     string            `json:"version"`
	Now         time.Time         `json:"now"`
	Paused      bool              `json:"paused"`
	PausedUntil *time.Time        `json:"paused_until,omitempty"`
	Rules       []rules.Rule      `json:"rules"`
	ActiveNow   []string          `json:"active_now"` // IDs of rules currently active
	Settings    rules.Settings    `json:"settings"`
	Injections  []rules.Injection `json:"injections"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	settings := s.Store.Settings()
	all := s.Store.List()
	active := make([]string, 0, len(all))
	for _, rl := range all {
		if rl.Schedule.IsActive(now) {
			active = append(active, rl.ID)
		}
	}
	writeJSON(w, http.StatusOK, StatusResponse{
		Version:     s.Version,
		Now:         now,
		Paused:      settings.IsPaused(now),
		PausedUntil: settings.PausedUntil,
		Rules:       all,
		ActiveNow:   active,
		Settings:    settings,
		Injections:  s.Store.ListInjections(),
	})
}

// --- /v1/rules ---------------------------------------------------------------

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.List())
}

// CreateRuleRequest is the body for POST /v1/rules. Schedule is provided in
// one of three flavors via convenience fields. The server normalizes to a
// fully-specified rules.Schedule.
type CreateRuleRequest struct {
	ID       string                `json:"id,omitempty"` // optional; for idempotent retries
	Action   rules.Action          `json:"action"`
	Target   rules.Target          `json:"target"`
	Friction *rules.FrictionConfig `json:"friction,omitempty"`
	Message  string                `json:"message,omitempty"`

	// Schedule fields. Specify exactly one of:
	For       string                   `json:"for,omitempty"`   // duration, e.g. "2h" — converted to Until = now + d
	Until     *time.Time               `json:"until,omitempty"` // RFC3339 hard end
	Recurring *rules.RecurringSchedule `json:"recurring,omitempty"`
	// (none of the three) → Schedule.Always

	// If a rule already targets the same canonical target, return a 409
	// unless Replace is true (in which case the existing rule is overwritten).
	Replace bool `json:"replace,omitempty"`
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var req CreateRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}

	now := s.Now()
	rule, err := s.buildRuleFromCreate(req, now)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_rule", err.Error())
		return
	}

	replacing := ""
	if req.Replace {
		// Replace targets the existing rule with the same canonical target.
		for _, e := range s.Store.List() {
			if e.Target.Canonical() == rule.Target.Canonical() {
				replacing = e.ID
				rule.ID = e.ID // overwrite in place
				rule.CreatedAt = e.CreatedAt
				break
			}
		}
	}

	if c := rules.FindConflict(rule, s.Store.List(), replacing); c != nil {
		writeErr(w, http.StatusConflict, "conflict",
			fmt.Sprintf("%s (existing rule: %s). Pass replace=true to overwrite.",
				c.Reason, c.ExistingID))
		return
	}

	if err := s.Store.Put(rule); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_write", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (s *Server) buildRuleFromCreate(req CreateRuleRequest, now time.Time) (rules.Rule, error) {
	id := req.ID
	if id == "" {
		id = s.NewID()
	}

	sched, err := buildSchedule(req.For, req.Until, req.Recurring, now)
	if err != nil {
		return rules.Rule{}, err
	}

	rule := rules.Rule{
		ID:        id,
		Action:    req.Action,
		Target:    req.Target,
		Schedule:  sched,
		Friction:  req.Friction,
		Message:   req.Message,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if rule.Target.Kind == rules.TargetDomain || rule.Target.Kind == rules.TargetPath {
		rule.Target.Value = rules.NormalizeWebTargetValue(rule.Target.Value)
	}
	// Default friction cooldown (5min) if unset on a friction rule.
	if rule.Action == rules.ActionFriction && rule.Friction != nil && rule.Friction.Cooldown == 0 {
		rule.Friction.Cooldown = rules.Duration(5 * time.Minute)
	}
	if err := rule.Validate(); err != nil {
		return rules.Rule{}, err
	}
	return rule, nil
}

func buildSchedule(forStr string, until *time.Time, rec *rules.RecurringSchedule, now time.Time) (rules.Schedule, error) {
	count := 0
	if forStr != "" {
		count++
	}
	if until != nil {
		count++
	}
	if rec != nil {
		count++
	}
	if count == 0 {
		return rules.Schedule{Kind: rules.ScheduleAlways}, nil
	}
	if count > 1 {
		return rules.Schedule{}, errors.New("specify exactly one of for/until/recurring")
	}
	if forStr != "" {
		d, err := time.ParseDuration(forStr)
		if err != nil {
			return rules.Schedule{}, fmt.Errorf("invalid for duration %q: %w", forStr, err)
		}
		end := now.Add(d)
		return rules.Schedule{Kind: rules.ScheduleUntil, Until: &end}, nil
	}
	if until != nil {
		return rules.Schedule{Kind: rules.ScheduleUntil, Until: until}, nil
	}
	return rules.Schedule{Kind: rules.ScheduleRecurring, Recurring: rec}, nil
}

func (s *Server) handleGetRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rl, ok := s.Store.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no rule with id "+id)
		return
	}
	writeJSON(w, http.StatusOK, rl)
}

// UpdateRuleRequest is a partial update; only non-nil/non-empty fields are
// applied.
type UpdateRuleRequest struct {
	Friction  *rules.FrictionConfig    `json:"friction,omitempty"`
	Message   *string                  `json:"message,omitempty"`
	For       *string                  `json:"for,omitempty"`
	Until     *time.Time               `json:"until,omitempty"`
	Recurring *rules.RecurringSchedule `json:"recurring,omitempty"`
	Always    *bool                    `json:"always,omitempty"` // set to revert to always-on
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rl, ok := s.Store.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no rule with id "+id)
		return
	}

	var req UpdateRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}

	if req.Message != nil {
		rl.Message = *req.Message
	}
	if req.Friction != nil {
		rl.Friction = req.Friction
	}

	now := s.Now()
	scheduleSpecified := req.For != nil || req.Until != nil || req.Recurring != nil || req.Always != nil
	if scheduleSpecified {
		if req.Always != nil && *req.Always {
			rl.Schedule = rules.Schedule{Kind: rules.ScheduleAlways}
		} else {
			forStr := ""
			if req.For != nil {
				forStr = *req.For
			}
			sched, err := buildSchedule(forStr, req.Until, req.Recurring, now)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid_schedule", err.Error())
				return
			}
			rl.Schedule = sched
		}
	}
	rl.UpdatedAt = now

	if err := rl.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_rule", err.Error())
		return
	}
	if err := s.Store.Put(rl); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_write", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rl)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ok, err := s.Store.Delete(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_write", err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no rule with id "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// --- /v1/pause and /v1/resume -----------------------------------------------

// PauseRequest controls how long enforcement is suppressed.
type PauseRequest struct {
	For   string     `json:"for,omitempty"`   // duration, e.g. "30m"
	Until *time.Time `json:"until,omitempty"` // RFC3339
	// (none) means: pause indefinitely (until manually resumed).
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	var req PauseRequest
	// Empty body is fine.
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
	}

	settings := s.Store.Settings()
	now := s.Now()

	if req.For == "" && req.Until == nil {
		// Indefinite: a long way away.
		far := now.AddDate(100, 0, 0)
		settings.PausedUntil = &far
	} else if req.For != "" {
		d, err := time.ParseDuration(req.For)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_duration", err.Error())
			return
		}
		end := now.Add(d)
		settings.PausedUntil = &end
	} else {
		settings.PausedUntil = req.Until
	}

	if err := s.Store.PutSettings(settings); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_write", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// FrictionResultRequest is posted by the friction GUI helper after a user
// passes (or cancels) a challenge.
type FrictionResultRequest struct {
	ChallengeID string `json:"challenge_id"` // rule ID
	Target      string `json:"target"`       // app name or domain (informational)
	Passed      bool   `json:"passed"`
	Intent      string `json:"intent,omitempty"` // for level=intent
}

// FrictionResultResponse echoes the resulting cooldown.
type FrictionResultResponse struct {
	Passed        bool       `json:"passed"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
}

func (s *Server) handleFrictionResult(w http.ResponseWriter, r *http.Request) {
	var req FrictionResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.ChallengeID == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "challenge_id is required")
		return
	}
	if !req.Passed {
		// Failed / cancelled — no cooldown set, app stays gated.
		writeJSON(w, http.StatusOK, FrictionResultResponse{Passed: false})
		return
	}

	rule, ok := s.Store.Get(req.ChallengeID)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found",
			"no rule with id "+req.ChallengeID)
		return
	}
	if rule.Action != rules.ActionFriction || rule.Friction == nil {
		writeErr(w, http.StatusBadRequest, "invalid_rule",
			"rule "+rule.ID+" is not a friction rule")
		return
	}

	cd := rule.Friction.Cooldown
	if cd == 0 {
		cd = rules.Duration(5 * time.Minute)
	}
	now := s.Now()
	expiry := now.Add(cd.Std())

	settings := s.Store.Settings()
	if settings.Cooldowns == nil {
		settings.Cooldowns = map[string]time.Time{}
	}
	settings.Cooldowns[rule.ID] = expiry
	if err := s.Store.PutSettings(settings); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_write", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, FrictionResultResponse{
		Passed:        true,
		CooldownUntil: &expiry,
	})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	settings := s.Store.Settings()
	settings.PausedUntil = nil
	if err := s.Store.PutSettings(settings); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_write", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// --- /v1/injections ----------------------------------------------------------

// CreateInjectionRequest is the body for POST /v1/injections. If ID names an
// existing injection it is overwritten (upsert); if ID is empty, a new ID is
// assigned.
type CreateInjectionRequest struct {
	ID        string               `json:"id,omitempty"`
	Name      string               `json:"name,omitempty"`
	Match     []rules.MatchPattern `json:"match"`
	Exclude   []rules.MatchPattern `json:"exclude,omitempty"`
	RunAt     rules.RunAt          `json:"run_at,omitempty"`
	World     rules.World          `json:"world,omitempty"`
	AllFrames bool                 `json:"all_frames,omitempty"`
	JS        string               `json:"js,omitempty"`
	CSS       string               `json:"css,omitempty"`
}

func (s *Server) handleListInjections(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.ListInjections())
}

func (s *Server) handleCreateInjection(w http.ResponseWriter, r *http.Request) {
	var req CreateInjectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}

	now := s.Now()
	id := req.ID
	createdAt := now
	if id == "" {
		id = s.NewInjectionID()
	} else if existing, ok := s.Store.GetInjection(id); ok {
		createdAt = existing.CreatedAt
	}

	runAt := req.RunAt
	if runAt == "" {
		runAt = rules.RunAtIdle
	}
	world := req.World
	if world == "" {
		world = rules.WorldMain
	}

	inj := rules.Injection{
		ID:        id,
		Name:      req.Name,
		Match:     req.Match,
		Exclude:   req.Exclude,
		RunAt:     runAt,
		World:     world,
		AllFrames: req.AllFrames,
		JS:        req.JS,
		CSS:       req.CSS,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}
	if err := inj.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_injection", err.Error())
		return
	}
	if err := s.Store.PutInjection(inj); err != nil {
		writeErr(w, http.StatusInternalServerError, "store_write", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, inj)
}

func (s *Server) handleGetInjection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inj, ok := s.Store.GetInjection(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no injection with id "+id)
		return
	}
	writeJSON(w, http.StatusOK, inj)
}

func (s *Server) handleDeleteInjection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ok, err := s.Store.DeleteInjection(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store_write", err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no injection with id "+id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}
