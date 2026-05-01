// Package client is a thin Go SDK over the attendd HTTP API. The CLI uses it
// directly; tests use httptest to simulate the daemon.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ketan0/attend/internal/rules"
	"github.com/ketan0/attend/internal/server"
)

// Client talks to attendd over HTTP.
type Client struct {
	BaseURL string // e.g. "http://127.0.0.1:7723"
	HTTP    *http.Client
}

// New constructs a Client with sensible defaults.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

// APIError is what we return when the server replies with a structured error.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("attendd: %s (%s, status %d)", e.Message, e.Code, e.Status)
}

// IsConflict reports whether err is a 409 conflict from attendd.
func IsConflict(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == http.StatusConflict
	}
	return false
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("attendd unreachable at %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &env)
		if env.Error.Code == "" {
			env.Error.Code = "unknown"
			env.Error.Message = string(raw)
		}
		return &APIError{Status: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode response: %w (body: %s)", err, raw)
		}
	}
	return nil
}

// Status fetches /v1/status.
func (c *Client) Status(ctx context.Context) (server.StatusResponse, error) {
	var s server.StatusResponse
	return s, c.doJSON(ctx, "GET", "/v1/status", nil, &s)
}

// ListRules fetches all rules.
func (c *Client) ListRules(ctx context.Context) ([]rules.Rule, error) {
	var out []rules.Rule
	return out, c.doJSON(ctx, "GET", "/v1/rules", nil, &out)
}

// CreateRule creates a rule, returning the saved rule (with assigned ID).
func (c *Client) CreateRule(ctx context.Context, req server.CreateRuleRequest) (rules.Rule, error) {
	var out rules.Rule
	return out, c.doJSON(ctx, "POST", "/v1/rules", req, &out)
}

// GetRule fetches one rule by ID.
func (c *Client) GetRule(ctx context.Context, id string) (rules.Rule, error) {
	var out rules.Rule
	return out, c.doJSON(ctx, "GET", "/v1/rules/"+id, nil, &out)
}

// UpdateRule patches an existing rule.
func (c *Client) UpdateRule(ctx context.Context, id string, req server.UpdateRuleRequest) (rules.Rule, error) {
	var out rules.Rule
	return out, c.doJSON(ctx, "PATCH", "/v1/rules/"+id, req, &out)
}

// DeleteRule deletes by ID.
func (c *Client) DeleteRule(ctx context.Context, id string) error {
	return c.doJSON(ctx, "DELETE", "/v1/rules/"+id, nil, nil)
}

// Pause suppresses enforcement.
func (c *Client) Pause(ctx context.Context, req server.PauseRequest) (rules.Settings, error) {
	var out rules.Settings
	return out, c.doJSON(ctx, "POST", "/v1/pause", req, &out)
}

// Resume re-enables enforcement.
func (c *Client) Resume(ctx context.Context) (rules.Settings, error) {
	var out rules.Settings
	return out, c.doJSON(ctx, "POST", "/v1/resume", nil, &out)
}
