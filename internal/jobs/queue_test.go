package jobs

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSubmitDeliversResult(t *testing.T) {
	q := New(4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Consumer goroutine picks up the job and posts a result.
	go func() {
		job, ok := q.Next(ctx)
		if !ok {
			t.Errorf("Next: cancelled")
			return
		}
		q.PostResult(job.ID, Result{Ok: true, Value: json.RawMessage(`"hi"`)})
	}()

	r, err := q.Submit(ctx, "test.ping", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !r.Ok || string(r.Value) != `"hi"` {
		t.Errorf("got %+v", r)
	}
}

func TestSubmitTimeoutWhenNoConsumer(t *testing.T) {
	q := New(4)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := q.Submit(ctx, "test.timeout", json.RawMessage(`{}`))
	if err == nil {
		t.Errorf("expected ctx error, got nil")
	}
}

func TestPostResultUnknownID(t *testing.T) {
	q := New(4)
	if q.PostResult("nope", Result{Ok: true}) {
		t.Errorf("expected false for unknown id")
	}
}

func TestEmptyKindRejected(t *testing.T) {
	q := New(4)
	_, err := q.Submit(context.Background(), "  ", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Errorf("expected kind error, got %v", err)
	}
}

func TestConcurrentSubmitters(t *testing.T) {
	q := New(4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Single consumer that echoes the payload back.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			job, ok := q.Next(ctx)
			if !ok {
				return
			}
			q.PostResult(job.ID, Result{Ok: true, Value: job.Payload})
		}
	}()

	var wg sync.WaitGroup
	var mu sync.Mutex
	results := map[string]bool{}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := json.RawMessage(`{"i":` + intStr(i) + `}`)
			r, err := q.Submit(ctx, "test.echo", payload)
			if err != nil {
				t.Errorf("Submit %d: %v", i, err)
				return
			}
			mu.Lock()
			results[string(r.Value)] = true
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	<-done

	if len(results) != 20 {
		t.Errorf("expected 20 distinct results, got %d", len(results))
	}
}

func TestNextCancellation(t *testing.T) {
	q := New(4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, ok := q.Next(ctx)
	if ok {
		t.Errorf("expected cancellation, got job")
	}
}

func intStr(i int) string {
	if i == 0 {
		return "0"
	}
	out := []byte{}
	for i > 0 {
		out = append([]byte{byte('0' + i%10)}, out...)
		i /= 10
	}
	return string(out)
}
