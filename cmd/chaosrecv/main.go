package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hiroshi-os/hookrelay/pkg/verify"
)

type stats struct {
	UniqueEvents        int64 `json:"unique_events"`
	DuplicateDeliveries int64 `json:"duplicate_deliveries"`
	BadSignatures       int64 `json:"bad_signatures"`
	Accepted            int64 `json:"accepted"`
	Rejected5xx         int64 `json:"rejected_5xx"`
	Timeouts            int64 `json:"timeouts"`
	Slow                int64 `json:"slow"`
	Resets              int64 `json:"resets"`
	TotalRequests       int64 `json:"total_requests"`
}

func main() {
	addr := flag.String("addr", ":9090", "listen address")
	secret := flag.String("secret", "test-secret", "HMAC secret (current)")
	secretPrev := flag.String("secret-prev", "", "previous HMAC secret")
	fail5xx := flag.Float64("fail-5xx", 0.10, "probability of 500")
	failTimeout := flag.Float64("fail-timeout", 0.05, "probability of hanging past client timeout")
	failSlow := flag.Float64("fail-slow", 0.05, "probability of slow response")
	failReset := flag.Float64("fail-reset", 0.10, "probability of connection reset")
	slowFor := flag.Duration("slow-for", 2*time.Second, "slow response delay")
	hangFor := flag.Duration("hang-for", 30*time.Second, "hang duration for timeout faults")
	seed := flag.Int64("seed", 1, "rng seed")
	flag.Parse()

	rng := rand.New(rand.NewSource(*seed))
	var mu sync.Mutex
	seen := map[string]struct{}{}
	var st stats
	firstOK := sync.Map{} // event_id -> time.Time of first success

	secrets := []string{*secret}
	if *secretPrev != "" {
		secrets = append(secrets, *secretPrev)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]any{
			"unique_events":        atomic.LoadInt64(&st.UniqueEvents),
			"duplicate_deliveries": atomic.LoadInt64(&st.DuplicateDeliveries),
			"bad_signatures":       atomic.LoadInt64(&st.BadSignatures),
			"accepted":             atomic.LoadInt64(&st.Accepted),
			"rejected_5xx":         atomic.LoadInt64(&st.Rejected5xx),
			"timeouts":             atomic.LoadInt64(&st.Timeouts),
			"slow":                 atomic.LoadInt64(&st.Slow),
			"resets":               atomic.LoadInt64(&st.Resets),
			"total_requests":       atomic.LoadInt64(&st.TotalRequests),
		}
		firsts := map[string]string{}
		firstOK.Range(func(k, v any) bool {
			firsts[k.(string)] = v.(time.Time).UTC().Format(time.RFC3339Nano)
			return true
		})
		out["first_ok_at"] = firsts
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&st.TotalRequests, 1)
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		hdr := r.Header.Get(verify.HeaderName)
		if err := verify.Verify(hdr, body, secrets, verify.Options{}); err != nil {
			atomic.AddInt64(&st.BadSignatures, 1)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}

		var envelope struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(body, &envelope)
		eventID := envelope.ID
		if eventID == "" {
			eventID = r.Header.Get("Hookrelay-Event-Id")
		}

		mu.Lock()
		roll := rng.Float64()
		mu.Unlock()

		switch {
		case roll < *failReset:
			atomic.AddInt64(&st.Resets, 1)
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "no hijack", http.StatusInternalServerError)
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				return
			}
			_ = conn.Close()
			return
		case roll < *failReset+*failTimeout:
			atomic.AddInt64(&st.Timeouts, 1)
			time.Sleep(*hangFor)
			return
		case roll < *failReset+*failTimeout+*failSlow:
			atomic.AddInt64(&st.Slow, 1)
			time.Sleep(*slowFor)
		case roll < *failReset+*failTimeout+*failSlow+*fail5xx:
			atomic.AddInt64(&st.Rejected5xx, 1)
			http.Error(w, "chaos 500", http.StatusInternalServerError)
			return
		}

		mu.Lock()
		_, dup := seen[eventID]
		if !dup {
			seen[eventID] = struct{}{}
			atomic.AddInt64(&st.UniqueEvents, 1)
			firstOK.Store(eventID, time.Now().UTC())
		} else {
			atomic.AddInt64(&st.DuplicateDeliveries, 1)
		}
		mu.Unlock()
		atomic.AddInt64(&st.Accepted, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	log.Printf("chaosrecv listening on %s (5xx=%.2f timeout=%.2f slow=%.2f reset=%.2f)",
		*addr, *fail5xx, *failTimeout, *failSlow, *failReset)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
