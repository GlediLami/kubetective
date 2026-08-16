package alert

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GlediLami/kubetective/internal/model"
)

func TestParsePagerDuty(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    model.ResourceRef
		wantErr error
	}{
		{
			name: "v2 webhook firehose - title carries pod",
			body: `{
				"event": "incident.triggered",
				"data": {
					"incident": {
						"id": "P123456",
						"title": "[P1] pod checkout-7f84c9 CrashLoopBackOff",
						"trigger_summary_data": {"subject": "CrashLoopBackOff"}
					},
					"service": {"summary": "kubernetes"}
				}
			}`,
			want: model.ResourceRef{Kind: "pod", Name: "checkout-7f84c9"},
		},
		{
			name: "v2 webhook - deployment slash form in title",
			body: `{
				"event": "incident.triggered",
				"data": {"incident": {"title": "deployment/checkout down", "impacted_services": [{"summary": "orders"}]}}
			}`,
			want: model.ResourceRef{Kind: "deployment", Name: "checkout"},
		},
		{
			name: "events API v2 - k8s details fields",
			body: `{
				"event": {
					"id": "evt_1",
					"data": {"details": {"pod": "checkout-7f84c9", "namespace": "production"}}
				}
			}`,
			want: model.ResourceRef{Kind: "pod", Name: "checkout-7f84c9"},
		},
		{
			name:    "no target anywhere",
			body:    `{"event": "incident.resolved", "data": {"incident": {"title": "back to normal"}}}`,
			wantErr: ErrNoTarget,
		},
		{
			name:    "not json",
			body:    `not json`,
			wantErr: errors.New("pagerduty payload"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(PagerDuty, []byte(tc.body))
			if tc.wantErr != nil {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr.Error()) {
					t.Fatalf("Parse error = %v, want containing %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if p.Target != tc.want {
				t.Errorf("Target = %+v, want %+v", p.Target, tc.want)
			}
		})
	}
}

func TestParseGrafana(t *testing.T) {
	cases := []struct {
		name string
		body string
		want model.ResourceRef
	}{
		{
			name: "legacy - evalMatches tags",
			body: `{
				"title": "[Alerting] High CPU on checkout",
				"state": "alerting",
				"evalMatches": [
					{"tags": {"kubernetes_namespace_name": "production", "kubernetes_pod_name": "checkout-7f84c9"}, "value": 91.2}
				]
			}`,
			want: model.ResourceRef{Kind: "pod", Namespace: "production", Name: "checkout-7f84c9"},
		},
		{
			name: "legacy - bare pod label",
			body: `{
				"title": "OOMKilled",
				"state": "alerting",
				"evalMatches": [{"tags": {"pod": "payments"}, "value": 1}]
			}`,
			want: model.ResourceRef{Kind: "pod", Name: "payments"},
		},
		{
			name: "unified - alerts labels, deployment",
			body: `{
				"title": "[FIRING:2] workers down",
				"state": "alerting",
				"ruleId": "abc123",
				"alerts": [
					{"status": "firing", "labels": {"kubernetes_namespace_name": "prod", "kubernetes_deployment_name": "queue"}}
				]
			}`,
			want: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "queue"},
		},
		{
			name: "unified - only namespace label",
			body: `{
				"state": "alerting",
				"alerts": [{"labels": {"namespace": "payments"}}]
			}`,
			want: model.ResourceRef{Kind: "namespace", Namespace: "payments", Name: "payments"},
		},
		{
			name: "title fallback",
			body: `{"title": "investigate deployment/checkout now", "state": "alerting"}`,
			want: model.ResourceRef{Kind: "deployment", Name: "checkout"},
		},
		{
			name: "conservative bare fallback - hyphenated name",
			body: `{"state": "alerting", "message": "storefront-7f9c2 is failing"}`,
			want: model.ResourceRef{Kind: "pod", Name: "storefront-7f9c2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(Grafana, []byte(tc.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if p.Target != tc.want {
				t.Errorf("Target = %+v, want %+v", p.Target, tc.want)
			}
		})
	}
}

func TestParseGrafanaDegradesWithoutTarget(t *testing.T) {
	p, err := Parse(Grafana, []byte(`{"state": "alerting", "message": "the storefront is failing"}`))
	if p != nil {
		t.Fatalf("Parse payload = %+v, want nil", p)
	}
	if !errors.Is(err, ErrNoTarget) {
		t.Fatalf("error = %v, want ErrNoTarget (prose must not be guessed)", err)
	}
}

func TestParseGrafanaRejectsUnknown(t *testing.T) {
	p, err := Parse(Grafana, []byte(`{"state": "ok"}`))
	if p != nil {
		t.Fatalf("Parse payload = %+v, want nil", p)
	}
	if !errors.Is(err, ErrNoTarget) {
		t.Fatalf("error = %v, want ErrNoTarget", err)
	}
}

func TestParseSlack(t *testing.T) {
	p, err := Parse(Slack, []byte(`{
		"token": "xoxb-test",
		"user_name": "alice",
		"command": "/kubetective",
		"text": "deployment/checkout since=2h"
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Target != (model.ResourceRef{Kind: "deployment", Name: "checkout"}) {
		t.Errorf("Target = %+v, want deployment/checkout", p.Target)
	}
	if p.Window != 2*time.Hour {
		t.Errorf("Window = %v, want 2h", p.Window)
	}
	if _, err := Parse(Slack, []byte(`{"text": "no since here"}`)); err == nil {
		t.Error("got target for text with no workload, want ErrNoTarget")
	}
	if p, err := Parse(Slack, []byte(`{"text": "checkout"}`)); err != nil {
		t.Errorf("single-token command must parse, got: %v", err)
	} else if p.Target != (model.ResourceRef{Kind: "pod", Name: "checkout"}) {
		t.Errorf("Target = %+v, want pod/checkout", p.Target)
	}
	if _, err := Parse(Slack, []byte(`{}`)); err == nil {
		t.Error("empty payload parsed, want error")
	}
}

func TestParseUnknownKind(t *testing.T) {
	if _, err := Parse(Kind("email"), []byte(`{}`)); err == nil {
		t.Fatal("unknown kind must error")
	}
}
