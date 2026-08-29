// Package modelspec is odo's per-model policy table — context window,
// compaction ("compact") ratio, and output budgets — read by both consumers
// of model identity:
//
//   - internal/moa (direct API: /panel, /vision, review_diff): the initial
//     max_tokens budget and the hard cap for truncation escalation.
//   - internal/adapter (normal chat via omp): the fixed compaction trigger
//     written as a per-run --config overlay.
//
// One table, no prefs knobs: the numbers are statements about the upstream
// models, not user preference, and a second override convention beside the
// table would just drift. Values mirror the Hermes orchestrator profile so
// odo never argues with the profile about the same model:
//
//   - ContextWindow — profile config.template.yaml custom_providers.<sudo>
//     (+ ~/.omp/agent/models.yml contextWindow, which agrees).
//   - CompactRatio — profile compression.model_thresholds (kimi-k3: 0.9,
//     deepseek-v4: 0.6, gpt-5.6: 0.85) and its global compression.threshold
//     0.35 (applied to glm-5.2; the profile's fixed 315K omp trigger is
//     0.35 × its then-900K window — same ratio, older window).
//   - MaxOutput — ~/.omp/agent/models.yml maxTokens: 65536 (the sudo gateway
//     catalog cap; thinking traces burn the same budget as the answer).
//   - MaxTokens — measured /panel runs (2026-08-09): at 16384 kimi/dsf/glm
//     emitted 7325/8076/8550 output tokens and thinking models truncated at
//     4096; freed headroom grew dsf to 21484. Thinking models start at 32768
//     (reasoning + answer), glm at 16384; moa escalates ×2 up to MaxOutput
//     on stop_reason=max_tokens.
package modelspec

import (
	"math"
	"strings"
)

// Spec is one model's policy entry.
type Spec struct {
	ContextWindow int     // total context in tokens
	CompactRatio  float64 // compaction trigger as a fraction of ContextWindow
	MaxOutput     int     // hard per-response output cap (tokens)
	MaxTokens     int     // initial per-request output budget (tokens)
}

// table is keyed by bare model id (provider prefix stripped, lowercase).
var table = map[string]Spec{
	"kimi-k3":           {ContextWindow: 350000, CompactRatio: 0.90, MaxOutput: 65536, MaxTokens: 32768},
	"deepseek-v4-flash": {ContextWindow: 1000000, CompactRatio: 0.60, MaxOutput: 65536, MaxTokens: 32768},
	"glm-5.2":           {ContextWindow: 1000000, CompactRatio: 0.35, MaxOutput: 65536, MaxTokens: 16384},
	// glm-5.3 mirrors the Hermes orchestrator profile (2026-08-29): window
	// 500K (profile config.template.yaml), compaction trigger 0.35 → 175K
	// (global compression.threshold, same ratio glm-5.2 carries), thinking
	// budget starts at 32768 like the other thinking models (glm-5.2's
	// 16384 start predates the measured thinking-trace burn; moa escalates
	// ×2 up to MaxOutput on stop_reason=max_tokens either way).
	"glm-5.3": {ContextWindow: 500000, CompactRatio: 0.35, MaxOutput: 65536, MaxTokens: 32768},
}

// fallback applies to models absent from the table. CompactRatio is the
// profile's global compression.threshold; MaxOutput is a single conservative
// escalation step over MaxTokens since the true catalog cap is unverified.
var fallback = Spec{ContextWindow: 200000, CompactRatio: 0.35, MaxOutput: 32768, MaxTokens: 16384}

// basename strips one provider prefix segment ("t9s/kimi-k3" → "kimi-k3").
func basename(model string) string {
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		return model[i+1:]
	}
	return model
}

// Family resolves a model id ("t9s/kimi-k3", "kimi-k3@test", "gpt-5.6")
// to its vendor family: the basename's prefix before the first "-", else
// the whole basename — "t9s/kimi-k3" → "kimi", "deepseek-v4-flash" →
// "deepseek", unknown ⇒ the raw basename. Pure, LLM-free, case-folded.
// The D7 settlement classes use it to tell a correlated same-family
// dissent from an independent one (same model under two provider labels
// must still read as ONE family); D6's diversity gate rides the same
// identity. Exported for both.
func Family(model string) string {
	b := basename(strings.ToLower(model))
	if i := strings.IndexByte(b, '-'); i > 0 {
		return b[:i]
	}
	return b
}

// Lookup resolves the spec for a model id ("t9s/kimi-k3" or "kimi-k3"),
// falling back to the conservative default for unknown models.
func Lookup(model string) Spec {
	m := strings.ToLower(model)
	if s, ok := table[m]; ok {
		return s
	}
	if s, ok := table[basename(m)]; ok {
		return s
	}
	return fallback
}

// CompactThresholdTokens resolves the fixed compaction trigger in tokens
// (CompactRatio × ContextWindow) for a KNOWN model. Unknown models report
// ok=false: the caller must fall back to the global omp config rather than
// invent a threshold for a context window it has never verified.
func CompactThresholdTokens(model string) (int, bool) {
	m := strings.ToLower(model)
	s, ok := table[m]
	if !ok {
		s, ok = table[basename(m)]
	}
	if !ok {
		return 0, false
	}
	return int(math.Round(float64(s.ContextWindow) * s.CompactRatio)), true
}
