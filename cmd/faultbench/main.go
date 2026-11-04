package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hiroshi-os/hookrelay/internal/migrate"
	"github.com/hiroshi-os/hookrelay/internal/store"
	"github.com/hiroshi-os/hookrelay/internal/testpg"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	n := flag.Int("n", 10000, "events to push")
	restarts := flag.Int("restarts", 5, "hookrelay process kill/restarts during run")
	databaseURL := flag.String("database-url", getenv("DATABASE_URL", ""), "postgres url (empty = embedded Postgres)")
	chaosAddr := flag.String("chaos-addr", "", "chaosrecv listen (empty = ephemeral)")
	adminAddr := flag.String("admin-addr", "", "hookrelay admin listen (empty = ephemeral)")
	secret := flag.String("secret", "fault-secret", "shared secret")
	outPath := flag.String("out", "bench/RESULTS.md", "results markdown path")
	binDir := flag.String("bin-dir", "bin", "directory with built binaries")
	flag.Parse()

	if *chaosAddr == "" {
		*chaosAddr = freeLocalAddr()
	}
	if *adminAddr == "" {
		*adminAddr = freeLocalAddr()
	}
	fmt.Printf("chaos=%s admin=%s\n", *chaosAddr, *adminAddr)

	ctx := context.Background()
	dsn := *databaseURL
	var stopDB func()
	if dsn == "" {
		var err error
		dsn, stopDB, err = testpg.EnsureURL()
		must(err)
		defer stopDB()
		fmt.Printf("using embedded postgres: %s\n", dsn)
	}
	pool, err := pgxpool.New(ctx, dsn)
	must(err)
	defer pool.Close()
	must(migrate.Up(ctx, pool))
	_, _ = pool.Exec(ctx, `TRUNCATE dead_letters, deliveries, outbox_cursors, events, endpoints RESTART IDENTITY CASCADE`)

	chaosBin := filepath.Join(*binDir, exeName("chaosrecv"))
	hookBin := filepath.Join(*binDir, exeName("hookrelay"))

	chaosCmd := exec.Command(chaosBin,
		"-addr", *chaosAddr,
		"-secret", *secret,
		"-fail-5xx", "0.10",
		"-fail-timeout", "0.05",
		"-fail-slow", "0.05",
		"-fail-reset", "0.10",
		"-hang-for", "8s",
		"-slow-for", "1s",
		"-seed", "42",
	)
	chaosCmd.Stdout = os.Stdout
	chaosCmd.Stderr = os.Stderr
	must(chaosCmd.Start())
	defer func() { _ = chaosCmd.Process.Kill() }()
	waitHTTP("http://"+*chaosAddr+"/stats", 30*time.Second)

	st := store.New(pool)
	ep, err := st.CreateEndpoint(ctx, "http://"+*chaosAddr+"/", *secret, 32, 10_000)
	must(err)
	_ = ep

	startHook := func() *exec.Cmd {
		cmd := exec.Command(hookBin)
		cmd.Env = append(os.Environ(),
			"DATABASE_URL="+dsn,
			"HTTP_ADDR="+*adminAddr,
			"ADMIN_TOKEN=fault-token",
			"WORKERS=4",
			"POLL_INTERVAL=50ms",
			"DELIVERY_TIMEOUT=2s",
			"BACKOFF_BASE=50ms",
			"BACKOFF_CAP=2s",
			"MAX_ATTEMPTS=20",
			"MAX_AGE=1h",
			"DISABLE_AFTER_FAILURES=100000",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		must(cmd.Start())
		return cmd
	}

	hook := startHook()
	defer func() {
		if hook != nil && hook.Process != nil {
			_ = hook.Process.Kill()
		}
	}()
	waitHTTP("http://"+*adminAddr+"/health", 30*time.Second)

	createdAt := make([]time.Time, *n)
	ids := make([]uuid.UUID, *n)
	t0 := time.Now()
	for i := 0; i < *n; i++ {
		id := uuid.New()
		ids[i] = id
		payload, _ := json.Marshal(map[string]any{"i": i})
		ev, err := st.InsertEventID(ctx, id, "fault.test", payload)
		must(err)
		createdAt[i] = ev.CreatedAt
		must(st.EnsureDeliveries(ctx, ev.ID))
		if (i+1)%1000 == 0 {
			fmt.Printf("inserted %d/%d\n", i+1, *n)
		}
	}
	insertDone := time.Now()

	// Kill/restart hookrelay at random-ish intervals while backlog drains.
	restartDone := 0
	deadline := time.Now().Add(15 * time.Minute)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		stats := fetchChaosStats("http://" + *chaosAddr + "/stats")
		dlq, _ := st.CountDeadLetters(ctx)
		by, _ := st.CountByStatus(ctx)
		pending := by["pending"] + by["in_flight"]
		fmt.Printf("progress unique=%d dlq=%d pending=%d restarts=%d\n", stats.Unique, dlq, pending, restartDone)

		if int(stats.Unique)+int(dlq) >= *n && pending == 0 {
			break
		}

		if restartDone < *restarts && time.Since(t0) > time.Duration(restartDone+1)*3*time.Second {
			fmt.Printf("killing hookrelay (restart %d)\n", restartDone+1)
			_ = hook.Process.Kill()
			_, _ = hook.Process.Wait()
			time.Sleep(300 * time.Millisecond)
			hook = startHook()
			waitHTTP("http://"+*adminAddr+"/health", 30*time.Second)
			restartDone++
		}
		<-ticker.C
	}

	stats := fetchChaosStats("http://" + *chaosAddr + "/stats")
	dlq, _ := st.CountDeadLetters(ctx)
	delivered := int(stats.Unique)
	lost := *n - delivered - int(dlq)
	if lost < 0 {
		lost = 0
	}

	// p50/p99 from first successful receipt vs created_at
	var delays []float64
	for i, id := range ids {
		ts, ok := stats.FirstOK[id.String()]
		if !ok {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			continue
		}
		delays = append(delays, parsed.Sub(createdAt[i]).Seconds())
	}
	sort.Float64s(delays)
	p50 := percentile(delays, 50)
	p99 := percentile(delays, 99)

	hw := hardwareLine()
	sha := gitSHA()
	cmdLine := strings.Join(os.Args, " ")
	deliveredPct := 100 * float64(delivered) / float64(*n)

	body := fmt.Sprintf(`# Faultbench results

- **Date (UTC):** %s
- **Commit:** %s
- **Hardware:** %s
- **Command:** `+"`"+`%s`+"`"+`

## Run parameters

| Param | Value |
| --- | --- |
| Events | %d |
| Chaos mix | 30%% failure (10%% 5xx + 5%% timeout + 5%% slow + 10%% reset) |
| Restarts requested | %d |
| Restarts performed | %d |
| Insert wall time | %s |
| Total wall time | %s |

## Measured

| Metric | Value |
| --- | --- |
| Delivered %% (unique / N) | %.4f%% (%d / %d) |
| Lost events (neither delivered nor DLQ) | %d |
| Duplicate deliveries | %d |
| DLQ count | %d |
| End-to-end delay p50 (s) | %.4f |
| End-to-end delay p99 (s) | %.4f |
| Chaos accepted | %d |
| Chaos bad signatures | %d |

`,
		time.Now().UTC().Format(time.RFC3339),
		sha,
		hw,
		cmdLine,
		*n,
		*restarts,
		restartDone,
		insertDone.Sub(t0).String(),
		time.Since(t0).String(),
		deliveredPct,
		delivered,
		*n,
		lost,
		stats.Duplicates,
		dlq,
		p50,
		p99,
		stats.Accepted,
		stats.BadSigs,
	)

	must(os.MkdirAll(filepath.Dir(*outPath), 0o755))
	must(os.WriteFile(*outPath, []byte(body), 0o644))
	fmt.Println(body)
	if lost != 0 {
		os.Exit(2)
	}
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

type chaosStats struct {
	Unique     int64             `json:"unique_events"`
	Duplicates int64             `json:"duplicate_deliveries"`
	BadSigs    int64             `json:"bad_signatures"`
	Accepted   int64             `json:"accepted"`
	FirstOK    map[string]string `json:"first_ok_at"`
}

func fetchChaosStats(url string) chaosStats {
	resp, err := http.Get(url)
	if err != nil {
		return chaosStats{FirstOK: map[string]string{}}
	}
	defer resp.Body.Close()
	var st chaosStats
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if st.FirstOK == nil {
		st.FirstOK = map[string]string{}
	}
	return st
}

func waitHTTP(url string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	panic("timeout waiting for " + url)
}

func freeLocalAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func hardwareLine() string {
	cpu := runtime.GOARCH
	cores := runtime.NumCPU()
	var memMiB uint64
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("powershell", "-NoProfile", "-Command",
			"(Get-CimInstance Win32_Processor | Select-Object -First 1 -ExpandProperty Name).Trim()").Output(); err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				cpu = s
			}
		}
		if out, err := exec.Command("powershell", "-NoProfile", "-Command",
			"[math]::Round((Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory/1MB)").Output(); err == nil {
			fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &memMiB)
		}
	} else if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					cpu = strings.TrimSpace(parts[1])
					break
				}
			}
		}
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					var kb uint64
					fmt.Sscanf(line, "MemTotal: %d", &kb)
					memMiB = kb / 1024
					break
				}
			}
		}
	}
	return fmt.Sprintf("%s, %d logical CPUs, %d MiB RAM, %s/%s", cpu, cores, memMiB, runtime.GOOS, runtime.GOARCH)
}

func gitSHA() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "UNKNOWN"
	}
	return strings.TrimSpace(string(out))
}
