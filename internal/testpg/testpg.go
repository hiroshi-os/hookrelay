package testpg

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/hiroshi-os/hookrelay/internal/migrate"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	mu      sync.Mutex
	started bool
	pg      *embeddedpostgres.EmbeddedPostgres
	url     string
	dir     string
)

// Start launches a shared embedded Postgres (once per process) and returns DATABASE_URL.
// The process keeps it alive until exit — per-test teardown would race parallel packages.
func Start(t testing.TB) string {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if started {
		return url
	}
	port, err := freePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	dir, err = os.MkdirTemp("", "hookrelay-pg-*")
	if err != nil {
		t.Fatal(err)
	}
	cfg := embeddedpostgres.DefaultConfig().
		Username("hookrelay").
		Password("hookrelay").
		Database("hookrelay").
		Version(embeddedpostgres.V16).
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		Port(uint32(port)).
		StartTimeout(90 * time.Second)
	pg = embeddedpostgres.NewDatabase(cfg)
	if err := pg.Start(); err != nil {
		t.Fatalf("embedded postgres start: %v", err)
	}
	url = fmt.Sprintf("postgres://hookrelay:hookrelay@127.0.0.1:%d/hookrelay?sslmode=disable", port)
	started = true
	return url
}

// Pool returns a migrated pgx pool. Prefer DATABASE_URL when set.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = Start(t)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `TRUNCATE dead_letters, deliveries, outbox_cursors, events, endpoints RESTART IDENTITY CASCADE`)
	return pool
}

// EnsureURL returns DATABASE_URL, starting embedded Postgres if needed (for CLI tools).
func EnsureURL() (string, func(), error) {
	if u := os.Getenv("DATABASE_URL"); u != "" {
		return u, func() {}, nil
	}
	port, err := freePort()
	if err != nil {
		return "", nil, err
	}
	d, err := os.MkdirTemp("", "hookrelay-pg-*")
	if err != nil {
		return "", nil, err
	}
	cfg := embeddedpostgres.DefaultConfig().
		Username("hookrelay").
		Password("hookrelay").
		Database("hookrelay").
		Version(embeddedpostgres.V16).
		RuntimePath(filepath.Join(d, "runtime")).
		DataPath(filepath.Join(d, "data")).
		Port(uint32(port)).
		StartTimeout(120 * time.Second)
	db := embeddedpostgres.NewDatabase(cfg)
	if err := db.Start(); err != nil {
		_ = os.RemoveAll(d)
		return "", nil, err
	}
	u := fmt.Sprintf("postgres://hookrelay:hookrelay@127.0.0.1:%d/hookrelay?sslmode=disable", port)
	stop := func() {
		_ = db.Stop()
		_ = os.RemoveAll(d)
	}
	return u, stop, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
