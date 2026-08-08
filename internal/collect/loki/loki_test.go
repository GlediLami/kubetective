package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/collect"
	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/pkg/api"
)

func fakeLoki(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.RawQuery
		if !strings.Contains(r.URL.Path, "query_range") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &lastQuery
}

func lokiResponse(t time.Time) string {
	ns := fmt.Sprintf("%d", t.UnixNano())
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"streams","result":[
	  {"stream":{"namespace":"prod","pod":"checkout-abc"},"values":[["%s","line1"],["%s","line2 OOMKilled"]]}
	]}}`, ns, ns)
}

func window() api.Window {
	return api.Window{
		Start: time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC),
	}
}

func TestCollectEmitsLogSnippetsWhenLogsWanted(t *testing.T) {
	ts := time.Date(2026, 8, 7, 13, 30, 0, 0, time.UTC)
	srv, lastQuery := fakeLoki(t, lokiResponse(ts))
	c := New(srv.URL)

	scope := &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}},
		Window:  window(),
		Logs:    true,
	}
	obs, _, err := c.Collect(context.Background(), scope)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("observations = %d, want 1 log.snippet", len(obs))
	}
	o := obs[0]
	if o.Kind != "log.snippet" || o.Source.System != "loki" {
		t.Errorf("observation = %+v", o)
	}
	lines, _ := o.Payload["lines"].([]string)
	if len(lines) != 2 || lines[1] != "line2 OOMKilled" {
		t.Errorf("lines = %v", lines)
	}
	if !strings.Contains(*lastQuery, "checkout-abc") || !strings.Contains(*lastQuery, "limit=50") {
		t.Errorf("query = %s", *lastQuery)
	}
	if !o.Timestamp.Equal(ts) {
		t.Errorf("timestamp = %v, want %v", o.Timestamp, ts)
	}
}

func TestCollectHonorsAdaptiveLogRequest(t *testing.T) {
	srv, _ := fakeLoki(t, lokiResponse(time.Now()))
	c := New(srv.URL)
	scope := &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}},
		Window:  window(),
		// Logs off, but an analyzer asked for logs (adaptive loop).
		EvidenceRequests: []model.EvidenceRequest{{HypothesisID: "h", Description: "logs", QueryHint: "logs", Cost: 1}},
	}
	obs, _, err := c.Collect(context.Background(), scope)
	if err != nil || len(obs) != 1 {
		t.Fatalf("adaptive logs: obs=%d err=%v", len(obs), err)
	}
}

func TestCollectSilentWithoutLogsRequest(t *testing.T) {
	srv, _ := fakeLoki(t, lokiResponse(time.Now()))
	c := New(srv.URL)
	obs, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}},
		Window:  window(),
	})
	if err != nil || len(obs) != 0 {
		t.Fatalf("must stay silent without a logs request: obs=%d err=%v", len(obs), err)
	}
}

func TestCollectFailureIsSilent(t *testing.T) {
	c := New("http://127.0.0.1:1") // unreachable
	obs, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Targets: []model.ResourceRef{{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}},
		Window:  window(),
		Logs:    true,
	})
	if err != nil {
		t.Fatalf("collector failure must not fail the investigation: %v", err)
	}
	if len(obs) != 0 {
		t.Errorf("obs = %d, want none", len(obs))
	}
}

func TestParseResponseCapsAndOrders(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"streams","result":[
	  {"stream":{"pod":"p"},"values":[["100","old"],["200","new"]]},
	  {"stream":{"pod":"p2"},"values":[["300","newest"]]}
	]}}`
	lines, ts, err := parseResponse([]byte(body), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "newest" {
		t.Errorf("lines = %v, want capped newest-first", lines)
	}
	if ts.UnixNano() != 300 {
		t.Errorf("newest ts = %d", ts.UnixNano())
	}
}

var _ = json.Valid
