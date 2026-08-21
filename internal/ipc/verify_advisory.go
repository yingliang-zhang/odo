package ipc

// Verify-setup advisory (discoverability fix, 2026-08-21): the auto-land
// verify gate is fail-closed — a project whose committed .odo-verify
// can't run for a diff stops that diff at verify_unconfigured, and until
// now the only trace was a per-diff blocked journal row the user had to
// go looking for. Nothing around it can be automated by design:
// .odo-verify sits on the supply-chain list, so an agent-authored diff
// introducing or editing it blocks supply_chain_path — the verify oracle
// may never author itself (m16: "its own land path is human-gated").
// What the pipeline CAN do is say, once and where the user already
// reads, what the one-time manual step is.
//
// Fired from autoLand only, debounced once per project root per daemon
// lifetime (a restart re-arms it — at most one transcript row per boot);
// a failed journal append RELEASES the debounce so the next blocked diff
// retries rather than silently losing the boot's one reminder. The /loop
// Mode A pipeline shares runVerifyGate but surfaces gate outcomes as
// round facts inside the loop's own journal (V6), so it needs no
// transcript advisory.
//
// "Configured" is judged against HEAD's committed copy of .odo-verify,
// never the checkout's working file (panel findings): run worktrees are
// materialized from HEAD's tracked content, so an uncommitted copy —
// untracked, staged-only, or a tracked file with uncommitted edits —
// arms NOTHING, and suppressing on it re-creates the silence this fix
// exists to end. And a scoped-only config whose globs miss the blocked
// diff's paths keeps blocking forever; it gets a scope-shaped advisory
// instead of suppression.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yingliang-zhang/odo/internal/git"
)

// verifyConfigState is what the project's COMMITTED .odo-verify means for
// the blocked diff's paths.
type verifyConfigState int

const (
	// verifyConfigNone: HEAD commits no usable command line — the file
	// is absent from HEAD (uncommitted copies never reach a worktree),
	// or its committed content is comments/blanks only.
	verifyConfigNone verifyConfigState = iota
	// verifyConfigPartial: HEAD commits scoped (glob: command) lines,
	// no fallback, and the blocked diff's paths fall outside every glob
	// — the gate will keep finding no command for diffs like this one.
	verifyConfigPartial
	// verifyConfigReady: HEAD commits a fallback (any diff resolves a
	// command) or scoped lines covering EVERY path of this diff — the
	// block is a stale-worktree artifact whose fix is a re-run, not
	// project setup.
	verifyConfigReady
)

// adviseVerifyUnconfigured journals the one-time setup advisory for a
// project whose diff reached the mandatory verify gate with no usable
// .odo-verify for it. Silent when the gate tripped for a reason the
// advice cannot fix: a reclaimed worktree (empty path — the fix is
// re-running the task, per recoverPendingDiffs semantics) or a HEAD
// whose committed config already covers this diff (the stalled diff's
// worktree simply predates the config commit — again a re-run fix).
func (s *Server) adviseVerifyUnconfigured(ctx context.Context, conversationID int64, worktreePath string, diffPaths []string) {
	if s.projectRoot == "" || worktreePath == "" {
		return
	}
	state, scopes := verifyCommitConfig(s.projectRoot, diffPaths)
	if state == verifyConfigReady {
		return
	}
	// Serialize claim+journal+release (panel finding): without the mutex
	// a racing second blocked diff can observe the claimed key and return
	// silently while the first's journal append fails and releases it —
	// both diffs pass with zero advisory and the boot's one reminder is
	// lost. Under the lock, the release strictly precedes the retry and
	// exactly one row lands.
	s.verifyAdviseMu.Lock()
	defer s.verifyAdviseMu.Unlock()
	if _, done := s.verifyAdvised[s.projectRoot]; done {
		return
	}
	if err := s.journalRunAdvisory(ctx, conversationID, verifySetupAdvice(s.projectRoot, state, scopes)); err != nil {
		return // key never claimed — the next blocked diff retries
	}
	if s.verifyAdvised == nil {
		s.verifyAdvised = map[string]struct{}{}
	}
	s.verifyAdvised[s.projectRoot] = struct{}{}
}

// hasVerifyAdvised reports whether root's advisory was journaled this
// boot. Test surface for the debounce/release contract.
func (s *Server) hasVerifyAdvised(root string) bool {
	s.verifyAdviseMu.Lock()
	defer s.verifyAdviseMu.Unlock()
	_, done := s.verifyAdvised[root]
	return done
}

// verifyCommitConfig judges the project's verify setup the way a future
// run worktree will see it: HEAD's committed copy of .odo-verify, parsed
// by the gate's OWN parser (parseVerifyFile — the two never drift on
// what a line means). Coverage mirrors verifyCommands' rule: a fallback
// line covers every diff; scoped-only configs cover a diff only when
// every one of its paths matches some glob (and a path-less diff is
// never covered). Every doubt (not a repo, file absent from HEAD, git
// failure) fails OPEN toward the advisory: a spurious one-time row costs
// one line, a missed one leaves the user with zero signal.
func verifyCommitConfig(projectRoot string, diffPaths []string) (verifyConfigState, []string) {
	content, err := git.ShowHEADFile(projectRoot, verifyCmdFile)
	if err != nil {
		return verifyConfigNone, nil
	}
	scoped, fallback := parseVerifyFile(content)
	if fallback != "" {
		return verifyConfigReady, nil
	}
	if len(scoped) == 0 {
		return verifyConfigNone, nil
	}
	globs := make([]string, len(scoped))
	for i, sc := range scoped {
		globs[i] = sc.glob
	}
	covered := len(diffPaths) > 0
	for _, p := range diffPaths {
		matched := false
		for _, g := range globs {
			if pathMatch(p, g) {
				matched = true
				break
			}
		}
		if !matched {
			covered = false
			break
		}
	}
	if covered {
		return verifyConfigReady, nil
	}
	return verifyConfigPartial, globs
}

// verifySetupAdvice renders the advisory text. Both shapes restate the
// parts that block BY DESIGN, because prescribing around them just
// trades one blocked reason for the next: the evidence contract
// (m16 gate 7 / M18 B4 — an exit-0 verify whose output shows no real
// test run never counts, so advising `go build` alone would trade
// verify_unconfigured for verify_no_evidence) and the COMMIT
// requirement (run worktrees see tracked files only; the oracle may
// never author itself). The starter command is one that will actually
// RUN (verifyToolchainHint). verifyConfigPartial gets the scope-shaped
// fix: the committed config exists but its globs miss this diff.
func verifySetupAdvice(projectRoot string, state verifyConfigState, scopes []string) string {
	hint := verifyToolchainHint(projectRoot)
	if state == verifyConfigPartial {
		shown := scopes
		extra := ""
		if len(shown) > 4 {
			shown, extra = shown[:4], fmt.Sprintf(" (+%d more)", len(scopes)-4)
		}
		return "auto-land is blocked for this project: the committed .odo-verify only scopes commands to [" +
			strings.Join(shown, ", ") + "]" + extra + " and this diff's paths fall outside every glob — the verify " +
			"gate is mandatory (fail-closed by design). One-time manual fix: add a bare fallback line running the " +
			"project's tests, e.g. `" + hint + "`, or a `glob: command` line covering the diff's paths; the output " +
			"must carry real test evidence (a PASS token, a go \"ok\" line, an N-passed count) — a build-only " +
			"command always blocks. Then COMMIT the file: run worktrees see tracked files only, and an " +
			"odo-authored diff touching .odo-verify is supply-chain-blocked, so the fix stays manual."
	}
	return "auto-land is blocked for this project: no usable command line is committed in .odo-verify at the repo " +
		"root — the verify gate is mandatory (fail-closed by design) and nothing auto-creates it. One-time manual " +
		"setup: create .odo-verify in the project checkout with its first non-comment line running the tests, e.g. `" +
		hint + "`; the output must carry real test evidence (a PASS token, a go \"ok\" line, an N-passed count) — a " +
		"build-only command always blocks. Then COMMIT the file: run worktrees see tracked files only, so an " +
		"uncommitted copy (untracked, staged, or edited-but-not-committed) changes nothing, and an odo-authored " +
		"diff touching .odo-verify is supply-chain-blocked, so setup stays manual. Optional `glob: command` lines " +
		"scope commands per path."
}

// verifyToolchainHint guesses a starter verify command from the
// checkout's manifests — only commands that will actually run: a root
// package.json without scripts.test gets the generic hint, since
// prescribing `npm test` would fail at the shell ("Missing script:
// test") and just trade verify_unconfigured for verify_failed.
func verifyToolchainHint(projectRoot string) string {
	switch {
	case fileExists(filepath.Join(projectRoot, "go.mod")):
		return "go build ./... && go vet ./... && go test ./..."
	case fileExists(filepath.Join(projectRoot, "Cargo.toml")):
		return "cargo test"
	case npmHasTestScript(projectRoot):
		return "npm test"
	}
	return "a command that runs the project's tests"
}

// npmHasTestScript reports whether the checkout's package.json defines a
// test script — the only state in which `npm test` is advice rather than
// a guaranteed verify_failed. An unreadable or unparseable manifest
// fails toward the generic hint.
func npmHasTestScript(projectRoot string) bool {
	data, err := os.ReadFile(filepath.Join(projectRoot, "package.json"))
	if err != nil {
		return false
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false
	}
	return strings.TrimSpace(manifest.Scripts["test"]) != ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
