package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/hiroshi-os/hookrelay/internal/backoff"
	"github.com/hiroshi-os/hookrelay/internal/sign"
	"github.com/hiroshi-os/hookrelay/internal/store"
)

// Runner polls for due deliveries and POSTs them.
type Runner struct {
	Store        *store.Store
	WorkerID     string
	Workers      int
	PollInterval time.Duration
	Timeout      time.Duration
	Backoff      backoff.Config
	DisableAfter int
	HTTPClient   *http.Client
	rng          *rand.Rand
	mu           sync.Mutex
}

func (r *Runner) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: r.Timeout}
}

func (r *Runner) rand() *rand.Rand {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rng == nil {
		r.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return r.rng
}

// Run blocks until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	if r.Workers <= 0 {
		r.Workers = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < r.Workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := fmt.Sprintf("%s-%d", r.WorkerID, idx)
			t := time.NewTicker(r.PollInterval)
			defer t.Stop()
			for {
				r.tick(ctx, id)
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}(i)
	}

	// Crash recovery for stale in-flight claims.
	go func() {
		age := r.Timeout + 2*time.Second
		if age < 5*time.Second {
			age = 5 * time.Second
		}
		t := time.NewTicker(age / 2)
		defer t.Stop()
		for {
			_, _ = r.Store.RecoverStaleInFlight(ctx, age)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// Outbox fan-out.
	go func() {
		t := time.NewTicker(r.PollInterval)
		defer t.Stop()
		for {
			_, _ = r.Store.FanOutNewEvents(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	wg.Wait()
}

func (r *Runner) tick(ctx context.Context, workerID string) {
	claimed, err := r.Store.ClaimDue(ctx, workerID, 16)
	if err != nil || len(claimed) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, d := range claimed {
		d := d
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.deliver(ctx, d)
		}()
	}
	wg.Wait()
}

func (r *Runner) deliver(ctx context.Context, d store.Delivery) {
	body, err := json.Marshal(map[string]any{
		"id":         d.EventID.String(),
		"type":       d.EventType,
		"payload":    json.RawMessage(d.EventPayload),
		"created_at": d.EventCreated.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		r.fail(ctx, d, err.Error())
		return
	}

	now := time.Now().UTC()
	sig := sign.Sign(d.Endpoint.SecretCurrent, d.Endpoint.SecretPrevious, now, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Endpoint.URL, bytes.NewReader(body))
	if err != nil {
		r.fail(ctx, d, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(sign.HeaderName, sig)
	req.Header.Set("Hookrelay-Event-Id", d.EventID.String())
	req.Header.Set("User-Agent", "hookrelay/1.0")

	resp, err := r.client().Do(req)
	if err != nil {
		r.fail(ctx, d, err.Error())
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = r.Store.MarkSucceeded(ctx, d.ID)
		return
	}
	r.fail(ctx, d, fmt.Sprintf("http %d", resp.StatusCode))
}

func (r *Runner) fail(ctx context.Context, d store.Delivery, errMsg string) {
	if r.Backoff.Exhausted(d.Attempt, d.CreatedAt, time.Now()) {
		_ = r.Store.MoveToDeadLetter(ctx, d, errMsg)
		return
	}
	delay := r.Backoff.Delay(d.Attempt-1, r.rand())
	next := time.Now().Add(delay)
	disableAfter := r.DisableAfter
	if d.Endpoint.DisableAfterFailures > 0 {
		disableAfter = d.Endpoint.DisableAfterFailures
	}
	_, _ = r.Store.MarkFailed(ctx, d.ID, next, errMsg, disableAfter)
}
