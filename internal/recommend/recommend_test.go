package recommend

import (
	"testing"

	"github.com/GlediLami/kubetective/internal/model"
)

func scored(category model.HypothesisCategory) model.Hypothesis {
	return model.Hypothesis{
		ID:       string(category) + ".checkout-7f84c9",
		Claim:    "test",
		Category: category,
		Score:    &model.ScoreBreakdown{Score: 0.9},
		Evidence: []string{"e1", "e2"},
	}
}

func TestForTopKnownCategories(t *testing.T) {
	target := model.ResourceRef{Kind: "deployment", Namespace: "prod", Name: "checkout"}
	cases := []struct {
		cat   model.HypothesisCategory
		risk  model.Risk
		check string
	}{
		{model.CatMemory, model.RiskMedium, "roll back"},
		{model.CatConfig, model.RiskMedium, "roll back"},
		{model.CatCrashLoop, model.RiskMedium, "inspect"},
		{model.CatImage, model.RiskLow, "verify"},
		{model.CatScheduling, model.RiskLow, "right-size"},
		{model.CatNode, model.RiskHigh, "evict"},
		{model.CatProbe, model.RiskLow, "probe"},
		{model.CatPVC, model.RiskMedium, "storage class"},
		{model.CatService, model.RiskMedium, "selector"},
		{model.CatHPA, model.RiskLow, "maxReplicas"},
	}
	for _, tc := range cases {
		h := scored(tc.cat)
		recs := ForTop(&h, target)
		if len(recs) != 1 {
			t.Fatalf("%s: recommendations = %d, want 1", tc.cat, len(recs))
		}
		r := recs[0]
		if r.Risk != tc.risk {
			t.Errorf("%s: risk = %s, want %s", tc.cat, r.Risk, tc.risk)
		}
		if r.Action == "" || r.Reason == "" {
			t.Errorf("%s: action/reason must be non-empty", tc.cat)
		}
		// Evidence-linked: the recommendation must cite the hypothesis evidence.
		if len(r.Evidence) != 2 || r.Evidence[0] != "e1" {
			t.Errorf("%s: evidence links = %v, want [e1 e2]", tc.cat, r.Evidence)
		}
		if !contains(r.Action, tc.check) {
			t.Errorf("%s: action %q missing %q", tc.cat, r.Action, tc.check)
		}
	}
}

func TestForTopUnknownAndNil(t *testing.T) {
	target := model.ResourceRef{Kind: "deployment", Name: "checkout"}
	h := scored(model.CatDNS)
	if recs := ForTop(&h, target); len(recs) != 0 {
		t.Fatalf("unknown category must yield no recommendations, got %v", recs)
	}
	if recs := ForTop(nil, target); recs != nil {
		t.Fatalf("nil hypothesis must yield nil recommendations")
	}
	if recs := ForTop(&model.Hypothesis{Category: model.CatMemory}, target); recs != nil {
		t.Fatalf("unscored hypothesis must yield nil recommendations")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
