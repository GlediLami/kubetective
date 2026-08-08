package cli

import (
	"testing"

	"github.com/GlediLami/kubetective/internal/model"
)

func TestParseResourceRefForms(t *testing.T) {
	tests := []struct {
		raw       string
		namespace string
		want      model.ResourceRef
	}{
		{raw: "pod/checkout-7f84c9", namespace: "prod", want: model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-7f84c9"}},
		{raw: "deployment/checkout", namespace: "prod", want: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}},
		{raw: "checkout", namespace: "prod", want: model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout"}},
		// The fully-qualified form is what incident Meta.Target stores, and
		// the replay/action path must round-trip it (regression: restart-pod
		// applied with Name="prod/name", breaking the delete).
		{raw: "pod/prod/checkout-7f84c9", namespace: "", want: model.ResourceRef{Kind: "pod", Namespace: "prod", Name: "checkout-7f84c9"}},
		{raw: "deployment/prod/checkout", namespace: "ignored", want: model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}},
	}
	for _, tc := range tests {
		got, err := parseResourceRef(tc.raw, tc.namespace)
		if err != nil {
			t.Errorf("parseResourceRef(%q): %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseResourceRef(%q, %q) = %+v, want %+v", tc.raw, tc.namespace, got, tc.want)
		}
	}
}

func TestParseResourceRefRejectsGarbage(t *testing.T) {
	if _, err := parseResourceRef("a/b/c/d", ""); err == nil {
		t.Error("4-part target must be rejected")
	}
	if _, err := parseResourceRef("pod/", ""); err == nil {
		t.Error("empty name must be rejected")
	}
}
