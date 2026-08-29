package modelspec

import "testing"

func TestLookupTable(t *testing.T) {
	cases := []struct {
		model               string
		ctx, maxOut, maxTok int
		ratio               float64
	}{
		{"t9s/kimi-k3", 350000, 65536, 32768, 0.90},
		{"kimi-k3", 350000, 65536, 32768, 0.90}, // bare id resolves too
		{"t9s/deepseek-v4-flash", 1000000, 65536, 32768, 0.60},
		{"glm-5.2", 1000000, 65536, 16384, 0.35},
		// glm-5.3 (user ruling, 2026-08-29): 1M window, CompactRatio 0.5
		// → 500K trigger; thinking budget aligned with the other thinking
		// models.
		{"glm-5.3", 1000000, 65536, 32768, 0.50},
		{"t9s/glm-5.3", 1000000, 65536, 32768, 0.50}, // prefixed id resolves too
	}
	for _, tc := range cases {
		s := Lookup(tc.model)
		if s.ContextWindow != tc.ctx || s.MaxOutput != tc.maxOut || s.MaxTokens != tc.maxTok || s.CompactRatio != tc.ratio {
			t.Errorf("%s: got %+v", tc.model, s)
		}
	}
}

func TestLookupFallback(t *testing.T) {
	for _, model := range []string{"", "acme/pi-9", "pm1"} {
		if got := Lookup(model); got != fallback {
			t.Errorf("%q: got %+v, want fallback %+v", model, got, fallback)
		}
	}
}

// The generated compact thresholds must equal the profile's own numbers
// (k3 0.90×350K, dsf 0.60×1M, glm 0.35×1M at the template's 1M window).
func TestCompactThresholdTokens(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"t9s/kimi-k3", 315000},
		{"t9s/deepseek-v4-flash", 600000},
		{"glm-5.2", 350000},
		// glm-5.3: 0.5 × 1M (user ruling, 2026-08-29) = 500K.
		{"glm-5.3", 500000},
	}
	for _, tc := range cases {
		got, ok := CompactThresholdTokens(tc.model)
		if !ok || got != tc.want {
			t.Errorf("%s: got (%d, %v), want (%d, true)", tc.model, got, ok, tc.want)
		}
	}
	// Unknown models refuse: the caller must inherit the global omp config
	// instead of fabricating a trigger off the fallback window.
	if _, ok := CompactThresholdTokens("acme/pi-9"); ok {
		t.Error("unknown model must report ok=false")
	}
}

// TestFamily pins the D7/D6 model-family identity: basename prefix before
// the first "-", provider labels and case folded away, unknown models
// kept raw. Label diversity is NOT model diversity.
func TestFamily(t *testing.T) {
	cases := []struct{ model, want string }{
		{"t9s/kimi-k3", "kimi"},
		{"kimi-k3@test", "kimi"}, // review-row label shape (model@provider)
		{"T9S/Kimi-K3", "kimi"},  // case-folded
		{"deepseek-v4-flash", "deepseek"},
		{"gpt-5.6", "gpt"},
		{"acme/pi-9", "pi"},
		{"pm1", "pm1"},   // no dash: the raw basename is the family
		{"-odd", "-odd"}, // leading dash: no prefix exists — raw basename
		{"", ""},
	}
	for _, tc := range cases {
		if got := Family(tc.model); got != tc.want {
			t.Errorf("Family(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
	// Same model under two provider labels is ONE family (the diversity
	// gate must not count label diversity as model diversity).
	if Family("kimi-k3@alpha") != Family("kimi-k3@beta") {
		t.Error("provider labels must not split a family")
	}
}
