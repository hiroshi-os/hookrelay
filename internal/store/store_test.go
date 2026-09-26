package store_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/hiroshi-os/hookrelay/internal/store"
	"github.com/hiroshi-os/hookrelay/internal/testpg"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testpg.Pool(t)
}

func TestSkipLockedNeverDoubleClaims(t *testing.T) {
	pool := testPool(t)
	st := store.New(pool)
	ctx := context.Background()

	ep, err := st.CreateEndpoint(ctx, "http://127.0.0.1:9/hook", "sec", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	const n = 50
	for i := 0; i < n; i++ {
		ev, err := st.InsertEvent(ctx, "t", json.RawMessage(`{"i":1}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.EnsureDeliveries(ctx, ev.ID); err != nil {
			t.Fatal(err)
		}
	}
	_ = ep

	var mu sync.Mutex
	seen := map[int64]string{}
	var wg sync.WaitGroup
	claim := func(worker string) {
		defer wg.Done()
		for {
			got, err := st.ClaimDue(ctx, worker, 10)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if len(got) == 0 {
				return
			}
			mu.Lock()
			for _, d := range got {
				if prev, ok := seen[d.ID]; ok {
					t.Errorf("delivery %d claimed by %s and %s", d.ID, prev, worker)
				}
				seen[d.ID] = worker
			}
			mu.Unlock()
			for _, d := range got {
				_ = st.MarkSucceeded(ctx, d.ID)
			}
		}
	}
	wg.Add(2)
	go claim("w1")
	go claim("w2")
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("claimed %d want %d", len(seen), n)
	}
}

func TestDLQAfterMaxAttempts(t *testing.T) {
	pool := testPool(t)
	st := store.New(pool)
	ctx := context.Background()
	ep, err := st.CreateEndpoint(ctx, "http://127.0.0.1:9/hook", "sec", 8, 100)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := st.InsertEvent(ctx, "t", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureDeliveries(ctx, ev.ID); err != nil {
		t.Fatal(err)
	}
	_ = ep

	var d store.Delivery
	for attempt := 1; attempt <= 3; attempt++ {
		got, err := st.ClaimDue(ctx, "w", 1)
		if err != nil || len(got) != 1 {
			t.Fatalf("attempt %d: %#v %v", attempt, got, err)
		}
		d = got[0]
		if attempt < 3 {
			_, err := st.MarkFailed(ctx, d.ID, time.Now(), "boom", 100)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.MoveToDeadLetter(ctx, d, "exhausted"); err != nil {
		t.Fatal(err)
	}
	n, err := st.CountDeadLetters(ctx)
	if err != nil || n != 1 {
		t.Fatalf("dlq=%d err=%v", n, err)
	}
}

func TestAutoDisableAndReenable(t *testing.T) {
	pool := testPool(t)
	st := store.New(pool)
	ctx := context.Background()
	ep, err := st.CreateEndpoint(ctx, "http://127.0.0.1:9/hook", "sec", 8, 3)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := st.InsertEvent(ctx, "t", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.EnsureDeliveries(ctx, ev.ID)

	for i := 0; i < 3; i++ {
		got, err := st.ClaimDue(ctx, "w", 1)
		if err != nil || len(got) != 1 {
			t.Fatalf("i=%d got=%v err=%v", i, got, err)
		}
		disabled, err := st.MarkFailed(ctx, got[0].ID, time.Now().Add(-time.Millisecond), "fail", 3)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && disabled {
			t.Fatal("should not disable yet")
		}
		if i == 2 && !disabled {
			t.Fatal("should disable on 3rd failure")
		}
	}
	ep2, err := st.GetEndpoint(ctx, ep.ID)
	if err != nil || ep2.Enabled {
		t.Fatalf("expected disabled, got %#v err=%v", ep2, err)
	}
	ep3, err := st.EnableEndpoint(ctx, ep.ID)
	if err != nil || !ep3.Enabled || ep3.ConsecutiveFailures != 0 {
		t.Fatalf("re-enable failed: %#v %v", ep3, err)
	}
}
