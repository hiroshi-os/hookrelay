package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HeaderName is the HTTP header carrying Hookrelay signatures.
const HeaderName = "Hookrelay-Signature"

// Sign returns Hookrelay-Signature header value for the given secrets.
// During rotation, pass previous as a non-empty string to include a second v1.
func Sign(secretCurrent, secretPrevious string, t time.Time, body []byte) string {
	ts := t.Unix()
	payload := signedPayload(ts, body)
	parts := []string{
		fmt.Sprintf("t=%d", ts),
		"v1=" + hexHMAC(secretCurrent, payload),
	}
	if secretPrevious != "" {
		parts = append(parts, "v1="+hexHMAC(secretPrevious, payload))
	}
	return strings.Join(parts, ",")
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
