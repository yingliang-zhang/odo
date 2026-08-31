package ipc

import (
	"slices"
	"testing"
)

// M12 D-budget bound test: per injection path, the registry's Σ defaults
// must stay under the soft bound and the Σ clamp-maxes under the 128 KB
// hard bound (ADR-0003's additive prompt budget, with the slash path as
// the send path's alternative — they never co-inject, so summing both
// paths into one Σ would charge bytes no prompt carries).
//
// Why both sums: a defaults-only test would bless 47 KB today while a
// single prefs edit (replay_total_kb: 64 + replay_turn_kb: 16) ships
// ~119 KB — the hard bound is what makes a prefs-clamped blowup a test
// failure instead of a prod surprise.
//
// Soft bound: 48 KB when the registry landed (M12 D-budget), re-based to
// 50 KB with the M12 D-todo layer (+1.5 KB/send, landed in the same
// milestone), re-based to 55 KB with the M12 Batch 3a D-cross layer
// (+5 KB/send: cross_topics 3 KB + cross_sibling 2 KB): the bound tracks
// the current effective send stack by definition, and the registry Σ bills
// replay_turn nested inside replay_total (the pessimistic convention
// below) — 47 KB + 1.5 KB + 5 KB = 53.5 KB against ~44.5 KB physically
// injected. The 128 KB hard bound — the actual model-context guard — is
// unchanged.
func TestPromptBudgetSumsWithinBounds(t *testing.T) {
	t.Parallel()
	const (
		softBound = 55 * 1024 // re-based for the D-cross rows (see header)
		hardBound = 128 * 1024
	)
	for _, path := range []string{budgetPathSend, budgetPathSlash} {
		var defSum, maxSum int
		for _, b := range PromptBudgets {
			if !slices.Contains(b.Paths, path) {
				continue
			}
			defSum += b.DefaultBytes
			maxSum += b.ClampMaxBytes
		}
		if defSum > softBound {
			t.Errorf("%s path Σdefaults = %d bytes (%d KB), want ≤ %d KB", path, defSum, defSum/1024, softBound/1024)
		}
		if maxSum > hardBound {
			t.Errorf("%s path Σclamp-max = %d bytes (%d KB), want ≤ %d KB", path, maxSum, maxSum/1024, hardBound/1024)
		}
	}
}

// Registry hygiene: every row is well-formed (non-empty metadata, both
// paths exist somewhere, clamp-max never below default) so the ledger can
// never silently undercount.
func TestPromptBudgetRegistryShape(t *testing.T) {
	t.Parallel()
	seenPath := map[string]bool{}
	for _, b := range PromptBudgets {
		if b.Name == "" || b.Constant == "" || b.Layer == "" {
			t.Errorf("row %+v: name/constant/layer must be set", b)
		}
		if len(b.Paths) == 0 {
			t.Errorf("%s: no injection path", b.Name)
		}
		if b.DefaultBytes <= 0 {
			t.Errorf("%s: default %d not positive", b.Name, b.DefaultBytes)
		}
		if b.ClampMaxBytes < b.DefaultBytes {
			t.Errorf("%s: clamp-max %d below default %d", b.Name, b.ClampMaxBytes, b.DefaultBytes)
		}
		for _, p := range b.Paths {
			seenPath[p] = true
		}
	}
	for _, p := range []string{budgetPathSend, budgetPathSlash} {
		if !seenPath[p] {
			t.Errorf("no registry row covers the %s path", p)
		}
	}
}
