package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testReceiver is the receiving side of the contract (and the pattern to
// copy when wiring an app in): read the RAW body, verify the signature
// against the shared secret BEFORE parsing, then parse the payload.
func testReceiver(t *testing.T, secret string) (*httptest.Server, chan Notification, chan struct{}) {
	t.Helper()
	got := make(chan Notification, 1)
	rejected := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !Verify(secret, body, []byte(r.Header.Get(SignatureHeader))) {
			select {
			case rejected <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var n Notification
		if err := json.Unmarshal(body, &n); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got <- n
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, got, rejected
}

func TestSendSignedAndVerified(t *testing.T) {
	srv, got, rejected := testReceiver(t, "secret3")
	err := Send(context.Background(), srv.URL, "secret3", Notification{
		IncidentID:    "incident-1235356396-checkout",
		Target:        "deployment/prod/checkout",
		ClusterID:     "c1",
		EngineVersion: "v0.9.0",
		DurationMs:    1234,
		Status:        "CRASHLOOPBACKOFF",
		Findings:      []Finding{{Analyzer: "crashloop", Severity: "HIGH", Title: "CrashLoopBackOff"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case n := <-got:
		if n.Target != "deployment/prod/checkout" {
			t.Errorf("target = %q, want deployment/prod/checkout", n.Target)
		}
		if len(n.Findings) != 1 || n.Findings[0].Analyzer != "crashloop" {
			t.Errorf("findings = %+v, want one crashloop finding", n.Findings)
		}
	case <-rejected:
		t.Fatal("receiver rejected the notification")
	case <-time.After(5 * time.Second):
		t.Fatal("notification not received")
	}
}

func TestSendWrongSecretRejected(t *testing.T) {
	srv, got, rejected := testReceiver(t, "right-secret")
	err := Send(context.Background(), srv.URL, "wrong-secret", Notification{IncidentID: "x"})
	if err == nil {
		t.Fatal("Send with a wrong secret must error (receiver verifies and 401s)")
	}
	select {
	case <-got:
		t.Fatal("receiver accepted a wrongly-signed notification")
	case <-rejected:
		// expected
	case <-time.After(5 * time.Second):
		t.Fatal("no rejection observed")
	}
}

func TestSendWithoutSecret(t *testing.T) {
	srv, got, rejected := testReceiver(t, "")
	if err := Send(context.Background(), srv.URL, "", Notification{IncidentID: "x", Status: "OK"}); err != nil {
		t.Fatalf("unsigned send: %v", err)
	}
	select {
	case n := <-got:
		if n.IncidentID != "x" {
			t.Errorf("incident id = %q, want x", n.IncidentID)
		}
	case <-rejected:
		t.Fatal("rejected an unsigned notification (no secret configured)")
	case <-time.After(5 * time.Second):
		t.Fatal("notification not received")
	}
}

func TestSendServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := Send(context.Background(), srv.URL, "s", Notification{}); err == nil {
		t.Fatal("5xx must surface as an error")
	}
}

func TestVerifyGarbage(t *testing.T) {
	if Verify("s", []byte("body"), []byte("deadbeef")) {
		t.Error("garbage signature must not verify")
	}
	if Verify("s", []byte("body"), []byte("f00d")) {
		t.Error("malformed signature must not verify")
	}
	if !Verify("s", []byte("body"), []byte(sign("s", []byte("body")))) {
		t.Error("correct signature must verify")
	}
	// A receiver with a secret configured must reject unsigned bodies.
	if Verify("s", []byte("body"), nil) {
		t.Error("unsigned body must not verify against a configured secret")
	}
}
