package server

import (
	"io"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlediLami/kubetective/internal/model"
	"github.com/GlediLami/kubetective/internal/record"
	"github.com/GlediLami/kubetective/pkg/api"
)

// scriptedInvestigator returns a fixed result for any request.
type scriptedInvestigator struct{ res *api.InvestigationResult }

func (s *scriptedInvestigator) Investigate(_ context.Context, _ *api.InvestigationRequest) (*api.InvestigationResult, error) {
	return s.res, nil
}

func testResult() *api.InvestigationResult {
	return &api.InvestigationResult{
		Incident: &model.IncidentSummary{
			Target:     model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"},
			Status:     "OOMKILLED",
			Severity:   model.SevHigh,
			Confidence: 0.94,
		},
		Meta: model.ResultMeta{RecordID: "inc-test", EngineVersion: "test"},
	}
}

func TestRESTInvestigate(t *testing.T) {
	s := &REST{Inv: &scriptedInvestigator{res: testResult()}, Store: record.NewStore(t.TempDir())}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	body := strings.NewReader(`{"target":"deployment/checkout","namespace":"prod","since_minutes":30}`)
	resp, err := http.Post(srv.URL+"/v1/investigate", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out api.InvestigationResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Incident.Status != "OOMKILLED" || out.Incident.Target.Name != "checkout" {
		t.Errorf("result = %+v", out.Incident)
	}
}

func TestRESTInvestigateErrors(t *testing.T) {
	s := &REST{Inv: &scriptedInvestigator{res: testResult()}}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// Malformed JSON.
	resp, _ := http.Post(srv.URL+"/v1/investigate", "application/json", strings.NewReader(`{bad`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed JSON: status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	// Bad target.
	resp, _ = http.Post(srv.URL+"/v1/investigate", "application/json", strings.NewReader(`{"target":"bogus"}`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad target: status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRESTIncidents(t *testing.T) {
	dir := t.TempDir()
	store := record.NewStore(dir)
	// Save one incident so the store has something.
	inc := &model.Incident{ID: "inc-1", Meta: model.IncidentMeta{Target: "deployment/prod/checkout"}, Observations: nil}
	if _, err := store.Save(inc); err != nil {
		t.Fatal(err)
	}
	s := &REST{Inv: &scriptedInvestigator{res: testResult()}, Store: store}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/incidents")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Incidents []string `json:"incidents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(list.Incidents) != 1 || list.Incidents[0] != "inc-1" {
		t.Errorf("incidents = %v", list.Incidents)
	}

	resp, err = http.Get(srv.URL + "/v1/incidents/inc-1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get incident: status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp, _ = http.Get(srv.URL + "/v1/incidents/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing incident: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: status = %d", resp.StatusCode)
	}
}

func TestRESTInvestigateWritesRecord(t *testing.T) {
	dir := t.TempDir()
	store := record.NewStore(dir)
	s := &REST{Inv: &scriptedInvestigator{res: testResult()}, Store: store}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/investigate", "application/json", strings.NewReader(`{"target":"deployment/checkout"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 incident record, got %d", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), ".jsonl") {
		t.Errorf("record file = %q", entries[0].Name())
	}
}

func TestParseRef(t *testing.T) {
	ref, err := parseRef("deployment/checkout", "prod")
	if err != nil || ref.Kind != "deployment" || ref.Name != "checkout" || ref.Namespace != "prod" {
		t.Errorf("parseRef = %+v, %v", ref, err)
	}
	ref, err = parseRef("prod/deployment/checkout", "")
	if err != nil || ref.Namespace != "prod" {
		t.Errorf("parseRef 3-part = %+v, %v", ref, err)
	}
	if _, err := parseRef("bogus", ""); err == nil {
		t.Error("bogus target must fail")
	}
}

var _ = filepath.Join

func TestRESTWebUIAndMetrics(t *testing.T) {
	store := record.NewStore(t.TempDir())
	if _, err := store.Save(&model.Incident{ID: "inc-1", Meta: model.IncidentMeta{Target: "deployment/prod/checkout"}}); err != nil {
		t.Fatal(err)
	}
	s := &REST{Inv: &scriptedInvestigator{res: testResult()}, Store: store}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// Index page.
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "KubeTective") {
		t.Errorf("index: status=%d body=%q", resp.StatusCode, string(body)[:80])
	}
	// Incident page (the id is filled client-side from the URL).
	resp, err = http.Get(srv.URL + "/incidents/inc-1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "KubeTective") {
		t.Errorf("incident page: status=%d", resp.StatusCode)
	}
	// Metrics after one investigation.
	resp, _ = http.Post(srv.URL+"/v1/investigate", "application/json", strings.NewReader(`{"target":"deployment/checkout"}`))
	resp.Body.Close()
	resp, err = http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "kubetective_investigations_total") {
		t.Errorf("metrics missing counters: %q", string(body)[:200])
	}
}
