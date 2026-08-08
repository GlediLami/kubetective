package dns

import (
	"context"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/analyze"
	"github.com/GlediLami/kubetective/internal/model"
)

func obs(kind string, res model.ResourceRef, payload map[string]any, ts time.Time) model.Observation {
	return model.Observation{Kind: kind, Resource: res, Payload: payload, Timestamp: ts}
}

func TestDNSAnalyzerCoreDNSDown(t *testing.T) {
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("deployment.state", model.ResourceRef{Kind: "deployment", Namespace: "kube-system", Name: "coredns"},
			map[string]any{"replicas": int64(2), "available_replicas": int64(0)}, time.Now()),
		obs("event.recorded", pod, map[string]any{"reason": "FailedToCreatePodSandBox", "message": "network is not ready: failed to setup network for sandbox"}, time.Now()),
		obs("event.recorded", pod, map[string]any{"reason": "BackOff", "message": "no such host: checkout.default.svc"}, time.Now()),
		obs("container.terminated", pod, map[string]any{"reason": "Error", "exit_code": int64(1)}, time.Now()),
		obs("container.waiting", pod, map[string]any{"reason": "CrashLoopBackOff"}, time.Now()),
	}}
	findings, hyps, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Analyzer != "dns" || findings[0].Severity != model.SevHigh {
		t.Fatalf("findings = %+v", findings)
	}
	if len(hyps) != 1 || hyps[0].Category != model.CatDNS {
		t.Fatalf("hyps = %+v", hyps)
	}
	if hyps[0].Score == nil || hyps[0].Score.Score < 0.9 {
		t.Errorf("score = %+v, want ≥ 0.9 (coredns down + events + symptom)", hyps[0].Score)
	}
}

func TestDNSAnalyzerEventsOnly(t *testing.T) {
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("event.recorded", pod, map[string]any{"reason": "BackOff", "message": "no such host: checkout.default.svc"}, time.Now()),
		obs("container.waiting", pod, map[string]any{"reason": "CrashLoopBackOff"}, time.Now()),
	}}
	findings, hyps, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Severity != model.SevWarning {
		t.Fatalf("findings = %+v, want WARNING (no coredns evidence)", findings)
	}
	if len(hyps) != 1 {
		t.Fatalf("hyps = %d", len(hyps))
	}
}

func TestDNSAnalyzerSilentWithoutDNS(t *testing.T) {
	pod := model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-abc"}
	in := &analyze.AnalysisInput{Observations: []model.Observation{
		obs("container.terminated", pod, map[string]any{"reason": "OOMKilled", "exit_code": int64(137)}, time.Now()),
		obs("event.recorded", pod, map[string]any{"reason": "OOMKilling"}, time.Now()),
	}}
	findings, hyps, _, err := New().Analyze(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(hyps) != 0 {
		t.Fatalf("must stay silent on non-DNS incidents: %+v %+v", findings, hyps)
	}
}
