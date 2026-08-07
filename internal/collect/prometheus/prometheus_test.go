package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kubedoctor/kubedoctor/internal/analyze"
	"github.com/kubedoctor/kubedoctor/internal/collect"
	"github.com/kubedoctor/kubedoctor/internal/model"
	"github.com/kubedoctor/kubedoctor/pkg/api"
)

func fixtureResponse(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query().Get("query")
		switch {
		case query == `sum by (container) (container_memory_working_set_bytes{namespace="prod", pod="checkout-7f84c9"})`:
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{
				"metric":{"container":"checkout"},
				"values":[[1754582400,"4.10e+08"],[1754582430,"7.00e+08"],[1754582460,"1.02e+09"],[1754582490,"1.02e+09"]]
			}]}}`)
		case query == `sum by (container) (container_cpu_cfs_throttled_periods_total{namespace="prod", pod="checkout-7f84c9"})`:
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[{
				"metric":{"container":"checkout"},
				"values":[[1754582400,"0"],[1754582460,"0"]]
			}]}}`)
		case query == `sum by (container) (container_cpu_usage_seconds_total{namespace="prod", pod="checkout-7f84c9"})`:
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
		default:
			http.Error(w, `{"status":"error","errorType":"bad_data","error":"unexpected query"}`, 400)
		}
	}
}

func TestCollectEmitsMetricSeries(t *testing.T) {
	srv := httptest.NewServer(fixtureResponse(t))
	defer srv.Close()

	c := New(srv.URL)
	start := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-7f84c9"}
	obs, refs, err := c.Collect(context.Background(), &collect.ScopePlan{
		Prior: []model.Observation{{
			ID:       "obs-pod",
			Kind:     "pod.state",
			Resource: pod,
		}},
		Window: api.Window{Start: start, End: start.Add(10 * time.Minute)},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("missing source refs (auditability)")
	}

	var memory *model.Observation
	for i := range obs {
		if obs[i].Payload["metric"] == MetricMemory {
			memory = &obs[i]
		}
	}
	if memory == nil {
		t.Fatalf("memory series missing: %v", obs)
	}
	if memory.Payload["container"] != "checkout" {
		t.Errorf("container = %v, want checkout", memory.Payload["container"])
	}
	// 410Mi → 1.02Gi growth, max = last value.
	if got := memory.Payload["first"]; got != 4.10e+08 {
		t.Errorf("first = %v, want 4.10e+08", got)
	}
	if got := memory.Payload["max"]; got != 1.02e+09 {
		t.Errorf("max = %v, want 1.02e+09", got)
	}
	if got, ok := analyze.PayloadInt64(memory.Payload, "count"); !ok || got != 4 {
		t.Errorf("count = %v, want 4", memory.Payload["count"])
	}
	if memory.Payload["unit"] != "bytes" {
		t.Errorf("unit = %v, want bytes", memory.Payload["unit"])
	}
	// Summary timestamp = last sample.
	if memory.Timestamp.IsZero() {
		t.Error("series observation timestamp missing")
	}
}

func TestCollectWithoutPriorIsGap(t *testing.T) {
	c := New("http://localhost:1")
	_, _, err := c.Collect(context.Background(), &collect.ScopePlan{})
	if err == nil {
		t.Fatal("expected an error (no pod targets in prior observations)")
	}
}

func TestCollectUnreachablePrometheusReturnsError(t *testing.T) {
	// A closed server port → connection error surfaces as collector-down gap.
	c := New("http://127.0.0.1:1")
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "p1"}
	_, _, err := c.Collect(context.Background(), &collect.ScopePlan{
		Prior: []model.Observation{{ID: "obs-pod", Kind: "pod.state", Resource: pod}},
		Window: api.Window{Start: time.Now().Add(-10 * time.Minute), End: time.Now()},
	})
	if err == nil {
		t.Fatal("expected connection error from unreachable Prometheus")
	}
}
