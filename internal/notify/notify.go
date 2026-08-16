// Package notify delivers opt-in completion webhooks for investigations
// (roadmap v1.0 integration surfaces). The payload is HMAC-SHA256 signed
// with a shared secret; the receiver MUST re-compute and compare the
// signature before trusting the body (see notify_test.go for the receiving
// side).
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SignatureHeader carries the HMAC-SHA256 hex digest of the raw body.
const SignatureHeader = "X-Kubetective-Signature"

// Notification is the completed-investigation payload.
type Notification struct {
	IncidentID    string    `json:"incident_id"`
	Target        string    `json:"target"`
	ClusterID     string    `json:"cluster_id,omitempty"`
	EngineVersion string    `json:"engine_version"`
	RecordID      string    `json:"record_id,omitempty"`
	DurationMs    int64     `json:"duration_ms"`
	Status        string    `json:"status,omitempty"`
	Findings      []Finding `json:"findings,omitempty"` // top findings only
}

// Finding is one compact finding in the notification payload.
type Finding struct {
	Analyzer string `json:"analyzer"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
}

// Send posts the notification with a 10s timeout. A non-2xx response is an
// error; a missing secret sends an unsigned request (still opt-in; prefer a
// secret wherever the target is not localhost).
func Send(ctx context.Context, url, secret string, n Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kubetective/"+n.EngineVersion)
	if secret != "" {
		req.Header.Set(SignatureHeader, sign(secret, body))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook %s: unexpected status %s", url, resp.Status)
	}
	return nil
}

// sign returns the hex HMAC-SHA256 of body keyed with secret.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether sig is a valid signature for body under secret.
// Receivers MUST call this before trusting a notification. With no secret
// configured on both sides, an absent signature is accepted (unsigned mode).
func Verify(secret string, body, sig []byte) bool {
	if len(sig) == 0 {
		return secret == ""
	}
	expected, err := hex.DecodeString(string(sig))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), expected)
}
