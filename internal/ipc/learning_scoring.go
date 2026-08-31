package ipc

// D9-W4 (lock §5 "Never-score-own-changes"; K3 spec §5): the ONE shared
// scoring-exclusion predicate, consumed by both the learning gates (frozen
// replay's counterfactual join, learning_replay.go) and the rules audit's
// live baseline (rules_audit.go — mirrored, never a second convention).
//
// An outcome whose resolving diff falls in the excluded set never feeds any
// gate metric (rule rows, baselines, prevented/friction tallies): the gate
// must not grade traffic produced by changes to the gate itself (circular),
// and .odo/-family work is un-steerable human-heavy input the candidate
// cannot be responsible for (K3 §5.1). The union, deliberately all
// structural path checks (gatepolicy/autonomy predicates, zero file reads):
//
//   - gate source: Tier-0 files (gatepolicy.go, gate_manifest.json) ∪ the
//     Tier-1 directory boundary (internal/ipc/, internal/store/,
//     internal/git/, internal/moa/, internal/adapter/) — the learning
//     plane's own source lives under internal/ipc/ and self-excludes.
//   - memory paths: .odo/, wiki/ (isMemoryPath, server.go) — this covers
//     .odo/learning/ journal artifacts for free (K3 appendix: the
//     structural coverage row).
//   - size legs of C0: >5 files OR >300 changed lines — applied at the
//     DIFF level below (needs patch stats); the path predicate alone
//     cannot see them.
//
// The new-top-dir C0 leg needs a base-commit listing (git) and is NOT part
// of this predicate (structural path checks only — risk noted in the wave
// report; such diffs still classify C0 in the autonomy report).

import (
	"github.com/yingliang-zhang/odo/internal/git"
)

// excludedFromScoring reports whether ANY touched path is gate source or
// memory-scoped — the shared never-score path predicate (lock §5).
func excludedFromScoring(paths []string) bool {
	for _, p := range paths {
		if isGateSourcePath(p) || isMemoryPath(p) {
			return true
		}
	}
	return false
}

// learningScoringClassify resolves one diff's scoring exclusion from its
// journaled patch file. readable=false means the patch could not be read
// or parsed; per lock §5.1 ("patch file missing ⇒ class unknown ⇒
// EXCLUDED", fail-closed, the unreadable_diffs honesty precedent) an
// unreadable diff is excluded from scoring — callers keep the unreadable
// bodycount honest in the report.
func learningScoringClassify(pathOnDisk string) (excluded, readable bool) {
	if pathOnDisk == "" {
		return true, false
	}
	stat, err := git.PatchStats(pathOnDisk)
	if err != nil {
		return true, false
	}
	if len(stat.Files) == 0 {
		return false, true // pure rename/mode change: nothing reviewable
	}
	if len(stat.Files) > autonomyMaxC0Files || stat.Added+stat.Removed > autonomyMaxC0Lines {
		return true, true
	}
	paths := make([]string, len(stat.Files))
	for i, f := range stat.Files {
		paths[i] = f.Path
	}
	return excludedFromScoring(paths), true
}
