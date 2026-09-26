package backoff

import (
	"math"
	"math/rand"
	"time"
)

// Config holds exponential backoff with full jitter.
type Config struct {
	Base        time.Duration // delay after first failure
	Cap         time.Duration // maximum delay
	MaxAttempts int           // move to DLQ after this many failed attempts
	MaxAge      time.Duration // optional absolute age cap from first attempt; 0 = disabled
}

// Delay returns a full-jitter delay for the given 0-based failed attempt index n.
// The delay is uniformly sampled from [0, min(cap, base*2^n)].
func (c Config) Delay(n int, rng *rand.Rand) time.Duration {
	if n < 0 {
		n = 0
	}
	exp := float64(c.Base) * math.Pow(2, float64(n))
	ceiling := time.Duration(exp)
	if ceiling > c.Cap || exp > float64(math.MaxInt64)/2 {
		ceiling = c.Cap
	}
	if ceiling <= 0 {
		return 0
	}
	if rng == nil {
		return time.Duration(rand.Int63n(int64(ceiling) + 1))
	}
	return time.Duration(rng.Int63n(int64(ceiling) + 1))
}

// Exhausted reports whether attempts or age force a DLQ transition.
func (c Config) Exhausted(attempts int, createdAt time.Time, now time.Time) bool {
	if c.MaxAttempts > 0 && attempts >= c.MaxAttempts {
		return true
	}
	if c.MaxAge > 0 && now.Sub(createdAt) >= c.MaxAge {
		return true
	}
	return false
}

// BoundCeiling returns min(cap, base*2^n) without jitter — used by tests.
func (c Config) BoundCeiling(n int) time.Duration {
	if n < 0 {
		n = 0
	}
	exp := float64(c.Base) * math.Pow(2, float64(n))
	ceiling := time.Duration(exp)
	if ceiling > c.Cap || exp > float64(math.MaxInt64)/2 {
		ceiling = c.Cap
	}
	return ceiling
}
