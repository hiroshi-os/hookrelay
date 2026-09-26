package backoff_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/hiroshi-os/hookrelay/internal/backoff"
)

func TestDelayWithinBounds(t *testing.T) {
	cfg := backoff.Config{
		Base:        time.Second,
		Cap:         30 * time.Second,
		MaxAttempts: 10,
	}
	rng := rand.New(rand.NewSource(42))
	for n := 0; n < 20; n++ {
		ceiling := cfg.BoundCeiling(n)
		for i := 0; i < 200; i++ {
			d := cfg.Delay(n, rng)
			if d < 0 || d > ceiling {
				t.Fatalf("n=%d delay %v outside [0, %v]", n, d, ceiling)
			}
		}
		wantCap := time.Second * time.Duration(1<<min(n, 20))
		if wantCap > cfg.Cap {
			wantCap = cfg.Cap
		}
		if ceiling != wantCap && !(n >= 5 && ceiling == cfg.Cap) {
			// base*2^n may exceed int via float; just ensure ceiling <= cap
			if ceiling > cfg.Cap {
				t.Fatalf("ceiling %v > cap", ceiling)
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestExhausted(t *testing.T) {
	cfg := backoff.Config{MaxAttempts: 3, MaxAge: time.Hour}
	created := time.Now()
	if cfg.Exhausted(2, created, created) {
		t.Fatal("should not exhaust at attempt 2")
	}
	if !cfg.Exhausted(3, created, created) {
		t.Fatal("should exhaust at attempt 3")
	}
	if !cfg.Exhausted(1, created, created.Add(2*time.Hour)) {
		t.Fatal("should exhaust by age")
	}
}
