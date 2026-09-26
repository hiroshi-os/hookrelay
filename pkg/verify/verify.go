package verify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HeaderName matches internal/sign.
const HeaderName = "Hookrelay-Signature"

var (
	ErrMalformedHeader = errors.New("verify: malformed Hookrelay-Signature header")
	ErrNoMatchingSig   = errors.New("verify: no matching v1 signature")
	ErrStaleTimestamp  = errors.New("verify: timestamp outside tolerance window")
	ErrMissingSecret   = errors.New("verify: at least one secret is required")
)

// Options controls verification behaviour.
type Options struct {
	// Tolerance is the max |now - t| allowed. Default 300s.
	Tolerance time.Duration
	// Now overrides time.Now for tests.
	Now func() time.Time
}

// Verify parses header and accepts any matching v1 against the provided secrets
// (current and optional previous). Comparison is constant-time via hmac.Equal.
func Verify(header string, body []byte, secrets []string, opts Options) error {
	if len(secrets) == 0 {
		return ErrMissingSecret
	}
	tol := opts.Tolerance
	if tol == 0 {
		tol = 300 * time.Second
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	ts, sigs, err := parseHeader(header)
	if err != nil {
		return err
	}
	if len(sigs) == 0 {
		return ErrMalformedHeader
	}

	delta := nowFn().Unix() - ts
	if delta < 0 {
		delta = -delta
	}
	if delta > int64(tol.Seconds()) {
		return ErrStaleTimestamp
	}

	payload := signedPayload(ts, body)
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		expected := hexHMAC(secret, payload)
		expBytes, err := hex.DecodeString(expected)
		if err != nil {
			continue
		}
		for _, got := range sigs {
			gotBytes, err := hex.DecodeString(got)
			if err != nil {
				continue
			}
			if hmac.Equal(expBytes, gotBytes) {
				return nil
			}
		}
	}
	return ErrNoMatchingSig
}

func parseHeader(header string) (int64, []string, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, nil, ErrMalformedHeader
	}
	var ts int64
	var haveTS bool
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return 0, nil, ErrMalformedHeader
		}
		k, v := kv[0], kv[1]
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return 0, nil, fmt.Errorf("%w: bad t", ErrMalformedHeader)
			}
			ts = n
			haveTS = true
		case "v1":
			if v == "" {
				return 0, nil, ErrMalformedHeader
			}
			sigs = append(sigs, v)
		default:
			// ignore unknown scheme versions
		}
	}
	if !haveTS {
		return 0, nil, ErrMalformedHeader
	}
	return ts, sigs, nil
}

func signedPayload(ts int64, body []byte) []byte {
	buf := make([]byte, 0, 20+1+len(body))
	buf = strconv.AppendInt(buf, ts, 10)
	buf = append(buf, '.')
	buf = append(buf, body...)
	return buf
}

func hexHMAC(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
