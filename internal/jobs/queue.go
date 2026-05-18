// Package jobs is the in-memory RPC broker that lets the CLI ask the
// browser extension to do things in a live tab (dump HTML, execute JS,
// list tabs). Jobs are opaque to the daemon — it just routes a JSON
// payload from a submitter (CLI) to a consumer (extension) and shuttles
// the result back. State is fully ephemeral; nothing is persisted.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Job is what the daemon hands to the extension. Payload is opaque JSON
// passed through unchanged.
type Job struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// Result is what the extension posts back. Exactly one of Value or Error
// is meaningful: Ok=true means Value, Ok=false means Error.
type Result struct {
	Ok    bool            `json:"ok"`
	Value json.RawMessage `json:"value,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Queue is the broker. Safe for concurrent use.
type Queue struct {
	pending chan Job

	mu      sync.Mutex
	results map[string]chan Result
}

// New returns a queue with a bounded pending buffer. Submitters block when
// the buffer is full (back-pressure to the CLI).
func New(pendingBuffer int) *Queue {
	if pendingBuffer <= 0 {
		pendingBuffer = 16
	}
	return &Queue{
		pending: make(chan Job, pendingBuffer),
		results: make(map[string]chan Result),
	}
}

// Submit enqueues a job and blocks until the consumer posts a result or
// ctx is cancelled. The job's ID is assigned here; callers don't pre-set it.
func (q *Queue) Submit(ctx context.Context, kind string, payload json.RawMessage) (Result, error) {
	if strings.TrimSpace(kind) == "" {
		return Result{}, fmt.Errorf("kind is required")
	}
	id := "job_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	resultCh := make(chan Result, 1)

	q.mu.Lock()
	q.results[id] = resultCh
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.results, id)
		q.mu.Unlock()
	}()

	job := Job{ID: id, Kind: kind, Payload: payload}

	select {
	case q.pending <- job:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}

	select {
	case r := <-resultCh:
		return r, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Next blocks until a job is available or ctx is cancelled. The second
// return is false on cancellation.
func (q *Queue) Next(ctx context.Context) (Job, bool) {
	select {
	case j := <-q.pending:
		return j, true
	case <-ctx.Done():
		return Job{}, false
	}
}

// PostResult delivers a result to the waiting submitter. Returns false if
// no submitter is waiting (already timed out, or unknown id).
func (q *Queue) PostResult(id string, r Result) bool {
	q.mu.Lock()
	ch, ok := q.results[id]
	q.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- r:
		return true
	default:
		return false
	}
}
