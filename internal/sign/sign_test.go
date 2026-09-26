package sign_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hiroshi-os/hookrelay/internal/sign"
	"github.com/hiroshi-os/hookrelay/pkg/verify"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	body := []byte(`{"id":"evt_1","hello":"world"}`)
	ts := time.Unix(1_700_000_000, 0).UTC()
	hdr := sign.Sign("sec-current", "", ts, body)
	if !strings.Contains(hdr, "t=1700000000") || !strings.Contains(hdr, "v1=") {
		t.Fatalf("unexpected header: %s", hdr)
	}
	err := verify.Verify(hdr, body, []string{"sec-current"}, verify.Options{
		Now: func() time.Time { return ts },
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTamperedBodyRejected(t *testing.T) {
	body := []byte(`{"ok":true}`)
	ts := time.Unix(1_700_000_000, 0).UTC()
	hdr := sign.Sign("sec", "", ts, body)
	err := verify.Verify(hdr, []byte(`{"ok":false}`), []string{"sec"}, verify.Options{
		Now: func() time.Time { return ts },
	})
	if err != verify.ErrNoMatchingSig {
		t.Fatalf("want ErrNoMatchingSig, got %v", err)
	}
}

func TestStaleTimestampRejected(t *testing.T) {
	body := []byte(`{}`)
	ts := time.Unix(1_700_000_000, 0).UTC()
	hdr := sign.Sign("sec", "", ts, body)
	err := verify.Verify(hdr, body, []string{"sec"}, verify.Options{
		Tolerance: 300 * time.Second,
		Now:       func() time.Time { return ts.Add(10 * time.Minute) },
	})
	if err != verify.ErrStaleTimestamp {
		t.Fatalf("want ErrStaleTimestamp, got %v", err)
	}
}

func TestFutureTimestampRejected(t *testing.T) {
	body := []byte(`{}`)
	ts := time.Unix(1_700_000_000, 0).UTC()
	hdr := sign.Sign("sec", "", ts, body)
	err := verify.Verify(hdr, body, []string{"sec"}, verify.Options{
		Tolerance: 300 * time.Second,
		Now:       func() time.Time { return ts.Add(-10 * time.Minute) },
	})
	if err != verify.ErrStaleTimestamp {
		t.Fatalf("want ErrStaleTimestamp, got %v", err)
	}
}

func TestRotatedSecretAccepted(t *testing.T) {
	body := []byte(`{"x":1}`)
	ts := time.Unix(1_700_000_000, 0).UTC()
	hdr := sign.Sign("new-secret", "old-secret", ts, body)
	// Receiver still has only the previous secret.
	if err := verify.Verify(hdr, body, []string{"old-secret"}, verify.Options{Now: func() time.Time { return ts }}); err != nil {
		t.Fatalf("previous secret should match: %v", err)
	}
	if err := verify.Verify(hdr, body, []string{"new-secret"}, verify.Options{Now: func() time.Time { return ts }}); err != nil {
		t.Fatalf("current secret should match: %v", err)
	}
	v1Count := strings.Count(hdr, "v1=")
	if v1Count != 2 {
		t.Fatalf("want 2 v1 signatures, got %d in %q", v1Count, hdr)
	}
}

func TestMalformedHeader(t *testing.T) {
	cases := []string{"", "v1=abc", "t=notint,v1=abc", "t=1"}
	for _, c := range cases {
		err := verify.Verify(c, []byte(`{}`), []string{"sec"}, verify.Options{
			Now: func() time.Time { return time.Unix(1, 0) },
		})
		if err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}
