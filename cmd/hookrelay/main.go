package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hiroshi-os/hookrelay/internal/api"
	"github.com/hiroshi-os/hookrelay/internal/backoff"
	"github.com/hiroshi-os/hookrelay/internal/config"
	"github.com/hiroshi-os/hookrelay/internal/migrate"
	"github.com/hiroshi-os/hookrelay/internal/store"
	"github.com/hiroshi-os/hookrelay/internal/worker"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	cfg := config.Load()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	if err := migrate.Up(ctx, pool); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	st := store.New(pool)
	srv := api.New(st, cfg.AdminToken)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("hookrelay admin listening on %s", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	runner := &worker.Runner{
		Store:        st,
		WorkerID:     cfg.WorkerID,
		Workers:      cfg.Workers,
		PollInterval: cfg.PollInterval,
		Timeout:      cfg.DeliveryTimeout,
		Backoff: backoff.Config{
			Base:        cfg.BackoffBase,
			Cap:         cfg.BackoffCap,
			MaxAttempts: cfg.MaxAttempts,
			MaxAge:      cfg.MaxAge,
		},
		DisableAfter: cfg.DisableAfterFailures,
		HTTPClient:   &http.Client{Timeout: cfg.DeliveryTimeout},
	}
	go runner.Run(ctx)

	<-ctx.Done()
	shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	_ = httpServer.Shutdown(shutdownCtx)
	log.Printf("hookrelay stopped")
}
