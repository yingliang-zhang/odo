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
