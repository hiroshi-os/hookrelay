package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

func main() {
	dir := filepath.Join(".tmp", "embedded-postgres")
	_ = os.MkdirAll(dir, 0o755)
	port := uint32(55432)
	if v := os.Getenv("EMBEDDED_PG_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &port)
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Username("hookrelay").
		Password("hookrelay").
		Database("hookrelay").
		Version(embeddedpostgres.V16).
		Port(port).
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		StartTimeout(90 * time.Second))
	if err := pg.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start embedded postgres: %v\n", err)
		os.Exit(1)
	}
	url := fmt.Sprintf("postgres://hookrelay:hookrelay@127.0.0.1:%d/hookrelay?sslmode=disable", port)
	fmt.Println(url)
	_ = os.WriteFile(filepath.Join(".tmp", "database_url"), []byte(url), 0o644)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	_ = pg.Stop()
}
