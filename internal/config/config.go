package config

import (
	"os"
	"strconv"
	"time"
)

// Config is process-wide runtime configuration.
type Config struct {
	DatabaseURL          string
	HTTPAddr             string
	AdminToken           string
	WorkerID             string
	Workers              int
	PollInterval         time.Duration
	DeliveryTimeout      time.Duration
	BackoffBase          time.Duration
	BackoffCap           time.Duration
	MaxAttempts          int
	MaxAge               time.Duration
	DisableAfterFailures int
	OutboxPollInterval   time.Duration
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func getenvDur(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// Load reads configuration from the environment.
func Load() Config {
	return Config{
		DatabaseURL:          getenv("DATABASE_URL", "postgres://hookrelay:hookrelay@127.0.0.1:5432/hookrelay?sslmode=disable"),
		HTTPAddr:             getenv("HTTP_ADDR", ":8080"),
		AdminToken:           getenv("ADMIN_TOKEN", "dev-admin-token"),
		WorkerID:             getenv("WORKER_ID", "worker-1"),
		Workers:              getenvInt("WORKERS", 4),
		PollInterval:         getenvDur("POLL_INTERVAL", 200*time.Millisecond),
		DeliveryTimeout:      getenvDur("DELIVERY_TIMEOUT", 5*time.Second),
		BackoffBase:          getenvDur("BACKOFF_BASE", 1*time.Second),
		BackoffCap:           getenvDur("BACKOFF_CAP", 5*time.Minute),
		MaxAttempts:          getenvInt("MAX_ATTEMPTS", 12),
		MaxAge:               getenvDur("MAX_AGE", 24*time.Hour),
		DisableAfterFailures: getenvInt("DISABLE_AFTER_FAILURES", 20),
		OutboxPollInterval:   getenvDur("OUTBOX_POLL_INTERVAL", 500*time.Millisecond),
	}
}
