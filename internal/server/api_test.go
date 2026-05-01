package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ketan0/attend/internal/rules"
	"github.com/ketan0/attend/internal/store"
)

// fixedTime / fixedID make the server deterministic for tests.
var fixedTime = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rules.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	idCounter := 0
	srv := &Server{
		Store: st,
		Now:   func() time.Time { return fixedTime },
		NewID: func() string {
			idCounter++
			return "r_test" + string(rune('0'+idCounter))
		},
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs
}

func doJSON(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out := new(bytes.Buffer)
	_, _ = out.ReadFrom(resp.Body)
	return resp, out.Bytes()
}

func TestCreateAndGetRule(t *testing.T) {
	_, hs := newTestServer(t)

	body := CreateRuleRequest{
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "twitter.com"},
		For:    "2h",
	}
	resp, raw := doJSON(t, "POST", hs.URL+"/v1/rules", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}

	var created rules.Rule
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("missing ID")
	}
	if created.Schedule.Kind != rules.ScheduleUntil {
		t.Errorf("schedule kind = %q, want until", created.Schedule.Kind)
	}
	if created.Schedule.Until == nil || !created.Schedule.Until.Equal(fixedTime.Add(2*time.Hour)) {
		t.Errorf("expected until = fixedTime+2h, got %v", created.Schedule.Until)
	}

	// GET it back.
	resp, raw = doJSON(t, "GET", hs.URL+"/v1/rules/"+created.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", resp.StatusCode, raw)
	}
	var got rules.Rule
	_ = json.Unmarshal(raw, &got)
	if got.ID != created.ID {
		t.Errorf("ID = %q, want %q", got.ID, created.ID)
	}
}

func TestCreateRuleInvalid(t *testing.T) {
	_, hs := newTestServer(t)

	cases := []struct {
		name string
		body CreateRuleRequest
		code string
	}{
		{
			"missing action",
			CreateRuleRequest{Target: rules.Target{Kind: rules.TargetDomain, Value: "x.com"}},
			"invalid_rule",
		},
		{
			"two schedule modes",
			CreateRuleRequest{
				Action: rules.ActionBlock,
				Target: rules.Target{Kind: rules.TargetDomain, Value: "x.com"},
				For:    "1h",
				Until:  &fixedTime,
			},
			"invalid_rule",
		},
		{
			"friction without config",
			CreateRuleRequest{
				Action: rules.ActionFriction,
				Target: rules.Target{Kind: rules.TargetDomain, Value: "x.com"},
			},
			"invalid_rule",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, raw := doJSON(t, "POST", hs.URL+"/v1/rules", c.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
			}
			var env apiErrorEnvelope
			_ = json.Unmarshal(raw, &env)
			if env.Error.Code != c.code {
				t.Errorf("code = %q, want %q (full: %+v)", env.Error.Code, c.code, env.Error)
			}
		})
	}
}

func TestCreateRuleConflictAndReplace(t *testing.T) {
	_, hs := newTestServer(t)

	body := CreateRuleRequest{
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "twitter.com"},
	}
	resp, _ := doJSON(t, "POST", hs.URL+"/v1/rules", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create: %d", resp.StatusCode)
	}

	// Same target → conflict.
	resp, raw := doJSON(t, "POST", hs.URL+"/v1/rules", body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "replace=true") {
		t.Errorf("error should hint at replace=true, got %s", raw)
	}

	// Replace=true overwrites the existing rule (action change).
	body.Action = rules.ActionFriction
	body.Friction = &rules.FrictionConfig{Level: rules.FrictionTimer, TimerSeconds: 10}
	body.Replace = true
	resp, raw = doJSON(t, "POST", hs.URL+"/v1/rules", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 on replace, got %d body=%s", resp.StatusCode, raw)
	}
	var replaced rules.Rule
	_ = json.Unmarshal(raw, &replaced)
	if replaced.Action != rules.ActionFriction {
		t.Errorf("action = %q, want friction", replaced.Action)
	}

	// Only one rule should exist.
	resp, raw = doJSON(t, "GET", hs.URL+"/v1/rules", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	var list []rules.Rule
	_ = json.Unmarshal(raw, &list)
	if len(list) != 1 {
		t.Errorf("list len = %d, want 1: %+v", len(list), list)
	}
}

func TestUpdateRule(t *testing.T) {
	_, hs := newTestServer(t)

	resp, raw := doJSON(t, "POST", hs.URL+"/v1/rules", CreateRuleRequest{
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "twitter.com"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	var created rules.Rule
	_ = json.Unmarshal(raw, &created)

	dur := "30m"
	body := UpdateRuleRequest{For: &dur}
	resp, raw = doJSON(t, "PATCH", hs.URL+"/v1/rules/"+created.ID, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: %d body=%s", resp.StatusCode, raw)
	}
	var updated rules.Rule
	_ = json.Unmarshal(raw, &updated)
	if updated.Schedule.Kind != rules.ScheduleUntil {
		t.Errorf("kind = %q", updated.Schedule.Kind)
	}
	if updated.Schedule.Until == nil ||
		!updated.Schedule.Until.Equal(fixedTime.Add(30*time.Minute)) {
		t.Errorf("until wrong: %v", updated.Schedule.Until)
	}

	// Revert to always.
	always := true
	body = UpdateRuleRequest{Always: &always}
	resp, _ = doJSON(t, "PATCH", hs.URL+"/v1/rules/"+created.ID, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch always: %d", resp.StatusCode)
	}
}

func TestDeleteRule(t *testing.T) {
	_, hs := newTestServer(t)
	resp, raw := doJSON(t, "POST", hs.URL+"/v1/rules", CreateRuleRequest{
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "x.com"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	var rl rules.Rule
	_ = json.Unmarshal(raw, &rl)

	resp, _ = doJSON(t, "DELETE", hs.URL+"/v1/rules/"+rl.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, "GET", hs.URL+"/v1/rules/"+rl.ID, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", resp.StatusCode)
	}
}

func TestStatusActiveNow(t *testing.T) {
	_, hs := newTestServer(t)

	// Active rule (always)
	_, _ = doJSON(t, "POST", hs.URL+"/v1/rules", CreateRuleRequest{
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "active.example"},
	})

	// Inactive rule (until in past)
	past := fixedTime.Add(-time.Hour)
	_, _ = doJSON(t, "POST", hs.URL+"/v1/rules", CreateRuleRequest{
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "expired.example"},
		Until:  &past,
	})

	resp, raw := doJSON(t, "GET", hs.URL+"/v1/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var s StatusResponse
	_ = json.Unmarshal(raw, &s)
	if len(s.Rules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(s.Rules))
	}
	if len(s.ActiveNow) != 1 {
		t.Errorf("expected 1 active, got %d (%+v)", len(s.ActiveNow), s.ActiveNow)
	}
	if s.Paused {
		t.Errorf("should not be paused")
	}
}

func TestPauseAndResume(t *testing.T) {
	_, hs := newTestServer(t)

	resp, raw := doJSON(t, "POST", hs.URL+"/v1/pause", PauseRequest{For: "30m"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pause: %d body=%s", resp.StatusCode, raw)
	}
	var settings rules.Settings
	_ = json.Unmarshal(raw, &settings)
	if settings.PausedUntil == nil ||
		!settings.PausedUntil.Equal(fixedTime.Add(30*time.Minute)) {
		t.Errorf("paused_until wrong: %v", settings.PausedUntil)
	}

	resp, raw = doJSON(t, "GET", hs.URL+"/v1/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var s StatusResponse
	_ = json.Unmarshal(raw, &s)
	if !s.Paused {
		t.Errorf("status should report paused")
	}

	resp, raw = doJSON(t, "POST", hs.URL+"/v1/resume", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume: %d body=%s", resp.StatusCode, raw)
	}
	var resumed rules.Settings
	_ = json.Unmarshal(raw, &resumed)
	if resumed.PausedUntil != nil {
		t.Errorf("expected paused_until=nil, got %v", resumed.PausedUntil)
	}

	// Also verify via /status that we are no longer paused.
	resp, raw = doJSON(t, "GET", hs.URL+"/v1/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var post StatusResponse
	_ = json.Unmarshal(raw, &post)
	if post.Paused {
		t.Errorf("expected not paused after resume")
	}
}

func TestIdempotentCreateWithExplicitID(t *testing.T) {
	_, hs := newTestServer(t)

	body := CreateRuleRequest{
		ID:     "r_explicit",
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetDomain, Value: "twitter.com"},
	}
	resp, raw := doJSON(t, "POST", hs.URL+"/v1/rules", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d body=%s", resp.StatusCode, raw)
	}
	var rl rules.Rule
	_ = json.Unmarshal(raw, &rl)
	if rl.ID != "r_explicit" {
		t.Errorf("ID = %q, want r_explicit", rl.ID)
	}
}

func TestFrictionResultSetsCooldown(t *testing.T) {
	_, hs := newTestServer(t)

	// Create a friction rule first.
	body := CreateRuleRequest{
		Action: rules.ActionFriction,
		Target: rules.Target{Kind: rules.TargetApp, Value: "Discord"},
		Friction: &rules.FrictionConfig{
			Level:    rules.FrictionIntent,
			Cooldown: rules.Duration(10 * time.Minute),
		},
	}
	resp, raw := doJSON(t, "POST", hs.URL+"/v1/rules", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d body=%s", resp.StatusCode, raw)
	}
	var rule rules.Rule
	_ = json.Unmarshal(raw, &rule)

	// Pass the challenge.
	resp, raw = doJSON(t, "POST", hs.URL+"/v1/friction/result", FrictionResultRequest{
		ChallengeID: rule.ID,
		Target:      "Discord",
		Passed:      true,
		Intent:      "checking team chat for an actual reason",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("friction/result: %d body=%s", resp.StatusCode, raw)
	}
	var fr FrictionResultResponse
	_ = json.Unmarshal(raw, &fr)
	if !fr.Passed || fr.CooldownUntil == nil {
		t.Fatalf("expected passed + cooldown_until, got %+v", fr)
	}
	if !fr.CooldownUntil.Equal(fixedTime.Add(10 * time.Minute)) {
		t.Errorf("cooldown_until = %v, want fixedTime+10m", fr.CooldownUntil)
	}

	// Status should reflect the cooldown.
	resp, raw = doJSON(t, "GET", hs.URL+"/v1/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var st StatusResponse
	_ = json.Unmarshal(raw, &st)
	if got, ok := st.Settings.Cooldowns[rule.ID]; !ok ||
		!got.Equal(fixedTime.Add(10*time.Minute)) {
		t.Errorf("settings.cooldowns[rule.ID] = %v, want fixedTime+10m", got)
	}
}

func TestFrictionResultFailedDoesNotSetCooldown(t *testing.T) {
	_, hs := newTestServer(t)
	body := CreateRuleRequest{
		Action:   rules.ActionFriction,
		Target:   rules.Target{Kind: rules.TargetApp, Value: "Discord"},
		Friction: &rules.FrictionConfig{Level: rules.FrictionIntent},
	}
	resp, raw := doJSON(t, "POST", hs.URL+"/v1/rules", body)
	var rule rules.Rule
	_ = json.Unmarshal(raw, &rule)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}

	resp, _ = doJSON(t, "POST", hs.URL+"/v1/friction/result", FrictionResultRequest{
		ChallengeID: rule.ID,
		Passed:      false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 even on fail, got %d", resp.StatusCode)
	}
	resp, raw = doJSON(t, "GET", hs.URL+"/v1/status", nil)
	var st StatusResponse
	_ = json.Unmarshal(raw, &st)
	if _, ok := st.Settings.Cooldowns[rule.ID]; ok {
		t.Errorf("expected no cooldown after failed challenge")
	}
}

func TestFrictionResultUnknownRule(t *testing.T) {
	_, hs := newTestServer(t)
	resp, _ := doJSON(t, "POST", hs.URL+"/v1/friction/result", FrictionResultRequest{
		ChallengeID: "r_nope",
		Passed:      true,
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown rule, got %d", resp.StatusCode)
	}
}

func TestFrictionResultRejectsNonFrictionRule(t *testing.T) {
	_, hs := newTestServer(t)
	resp, raw := doJSON(t, "POST", hs.URL+"/v1/rules", CreateRuleRequest{
		Action: rules.ActionBlock,
		Target: rules.Target{Kind: rules.TargetApp, Value: "Slack"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	var rule rules.Rule
	_ = json.Unmarshal(raw, &rule)

	resp, _ = doJSON(t, "POST", hs.URL+"/v1/friction/result", FrictionResultRequest{
		ChallengeID: rule.ID,
		Passed:      true,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for non-friction rule, got %d", resp.StatusCode)
	}
}

func TestNotFound(t *testing.T) {
	_, hs := newTestServer(t)
	resp, _ := doJSON(t, "GET", hs.URL+"/v1/rules/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, "DELETE", hs.URL+"/v1/rules/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}
