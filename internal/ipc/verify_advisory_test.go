package ipc

// Pins the verify-setup advisory contract (verify_advisory.go): an
// unconfigured (or scope-missing) project's FIRST verify_unconfigured
// block journals one daemon-authored agent_error row into the diff's
// conversation, telling the user the one-time manual step; subsequent
// diffs stay silent (one row per project per daemon boot, never a
// per-diff flood) — and blocks the advice cannot fix (reclaimed
// worktree, or a HEAD whose committed config covers the diff) never
// produce it. Panel findings the current revision pins:
//
//   - "Configured" is judged on HEAD's committed copy, never the
//     working file: setup that never reached a commit arms nothing
//     (run worktrees see tracked files only), and a TRACKED file with
//     uncommitted edits is judged by its HEAD content — a stub HEAD
//     plus a real uncommitted edit still gets the COMMIT advice.
//   - A committed scoped-only config whose globs miss the blocked
//     diff's paths is NOT a suppress condition — the gate keeps
//     blocking every such diff, so the advisory fires with the
//     scope-shaped fix (add a fallback line or a covering glob).
//   - Concurrent blocked diffs land EXACTLY ONE advisory row even when
//     one append fails — claim+journal+release run under a mutex, so a
//     failed append's release strictly precedes the retry's claim.
//   - The starter command is only ever one that will actually run (no
//     bare `npm test` without scripts.test).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// verifyAdvisories returns the daemon-authored advisory rows
// (journalRunAdvisory shape: agent_error with odo:true) in order.
func verifyAdvisories(t *testing.T, st *store.Store, convID int64) []string {
	t.Helper()
	var out []string
	for _, ev := range mustListEvents(t, st, convID) {
		if ev.Type != store.EventAgentError {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if odo, _ := p["odo"].(bool); odo {
			out = append(out, p["error"].(string))
		}
	}
	return out
}

// armedVerifyServer mirrors TestAutoLandBlockedPaths' fixture: armed
// panel prefs + a store/server pair on a fresh repo, so autoLand reaches
// the verify gate and stops there. The repo deliberately has NO
// .odo-verify — the state the advisory exists for.
func armedVerifyServer(t *testing.T) (autonomyFixture, *Server, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\nauto_apply: main\n")
	f := newAutonomyFixture(t)
	root, sha := autolandRepo(t)
	return f, &Server{store: f.st, projectRoot: root}, root, sha
}

// emptyWorktree is what a real run worktree looks like to the gate when
// the base lacks the .odo-verify commit: a directory without the file.
// (The gate reads the worktree path on disk, so tests driving the
// blocked path must NOT pass the holding checkout as the worktree.)
func emptyWorktree(t *testing.T) string { return t.TempDir() }

// runBlocked drives one diff (patch touching path) into the verify gate
// through an empty worktree and returns the blocked reasons. Committing
// config first advances HEAD; pass the sha the diff is based on.
func runBlocked(t *testing.T, f autonomyFixture, s *Server, sha, path string) []string {
	t.Helper()
	d := f.addDiff(t, "p.diff", patchSrc(path, 1, 1, false))
	d.BaseSHA = &sha
	s.autoLand(context.Background(), d, emptyWorktree(t), "goal", false, "")
	return blockedReasons(t, f.st, f.c.ID)
}

func TestVerifyAdvisoryFiresOncePerProject(t *testing.T) {
	f, s, root, sha := armedVerifyServer(t)

	// Two consecutive diffs both trip verify_unconfigured; the advice
	// must appear EXACTLY once — debounce is keyed on the project, so
	// the second autoLand (even a second conversation's diff would)
	// stays silent.
	for _, name := range []string{"a.diff", "b.diff"} {
		d := f.addDiff(t, name, patchSrc("README.md", 1, 1, false))
		d.BaseSHA = &sha
		s.autoLand(context.Background(), d, root, "goal", false, "")
	}
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 2 || got[0] != "verify_unconfigured" || got[1] != "verify_unconfigured" {
		t.Fatalf("reasons = %v, want two verify_unconfigured", got)
	}
	adv := verifyAdvisories(t, f.st, f.c.ID)
	if len(adv) != 1 {
		t.Fatalf("advisories = %d, want exactly 1 (once per project per boot)", len(adv))
	}
	// The contract the row must carry: it IS a setup checklist — the
	// file name, a runnable command, the evidence requirement, and the
	// commit requirement (worktrees see tracked files only).
	// (the toolchain-hint content itself is pinned in
	// TestVerifySetupAdviceToolchainHint — the autolandRepo fixture
	// carries no manifest, so the generic hint is what appears here).
	for _, want := range []string{".odo-verify", "COMMIT", "tracked files", "test evidence"} {
		if !strings.Contains(adv[0], want) {
			t.Errorf("advisory missing %q:\n%s", want, adv[0])
		}
	}
}

func TestVerifyAdvisorySuppressedWhenCheckoutConfigured(t *testing.T) {
	f, s, root, _ := armedVerifyServer(t)
	// HEAD commits a usable fallback, but the diff's worktree (cut at a
	// base without that commit, here an empty dir on disk) lacks the
	// file: the gate honestly blocks, yet "create .odo-verify" would be
	// wrong advice — the fix is a fresh run. Blocked row stays, advisory
	// must NOT fire. BaseSHA tracks the fresh HEAD so the base-freshness
	// ladder stays out of this path.
	gitWriteVerify(t, root, "echo PASS\n")
	sha := gitOut(t, root, "rev-parse", "HEAD")
	if got := runBlocked(t, f, s, sha, "README.md"); len(got) != 1 || got[0] != "verify_unconfigured" {
		t.Fatalf("reasons = %v, want [verify_unconfigured]", got)
	}
	if adv := verifyAdvisories(t, f.st, f.c.ID); len(adv) != 0 {
		t.Errorf("advisories = %v, want none (checkout already configured — re-run is the fix)", adv)
	}
}

func TestVerifyAdvisoryScopedConfigCoverage(t *testing.T) {
	// Panel finding #2, both directions of a committed scoped-only
	// config (no fallback):
	t.Run("covered paths suppress", func(t *testing.T) {
		f, s, root, _ := armedVerifyServer(t)
		gitWriteVerify(t, root, "src/**: echo PASS\n")
		sha := gitOut(t, root, "rev-parse", "HEAD")
		// The diff lives INSIDE the glob: a fresh worktree would carry
		// the committed config and pass, so this block is stale-base —
		// the fix is a re-run. No advisory.
		if got := runBlocked(t, f, s, sha, "src/x.go"); len(got) != 1 || got[0] != "verify_unconfigured" {
			t.Fatalf("reasons = %v, want [verify_unconfigured]", got)
		}
		if adv := verifyAdvisories(t, f.st, f.c.ID); len(adv) != 0 {
			t.Errorf("advisories = %v, want none (committed config covers this diff)", adv)
		}
	})
	t.Run("uncovered paths advise scope fix", func(t *testing.T) {
		f, s, root, _ := armedVerifyServer(t)
		gitWriteVerify(t, root, "src/**: echo PASS\n")
		sha := gitOut(t, root, "rev-parse", "HEAD")
		// The diff lives OUTSIDE every glob: the gate found no command
		// for it and NEVER will for diffs like this one — suppressing
		// here leaves zero signal (the finding). Advisory fires with the
		// scope-shaped fix, not the create-from-scratch text.
		if got := runBlocked(t, f, s, sha, "README.md"); len(got) != 1 || got[0] != "verify_unconfigured" {
			t.Fatalf("reasons = %v, want [verify_unconfigured]", got)
		}
		adv := verifyAdvisories(t, f.st, f.c.ID)
		if len(adv) != 1 {
			t.Fatalf("advisories = %v, want exactly 1 (scope-missing config still blocks)", adv)
		}
		for _, want := range []string{"src/**", "fallback", "COMMIT", "tracked files", "test evidence"} {
			if !strings.Contains(adv[0], want) {
				t.Errorf("scope advisory missing %q:\n%s", want, adv[0])
			}
		}
		if strings.Contains(adv[0], "no usable command line") {
			t.Errorf("scope advisory must not claim the committed config is missing:\n%s", adv[0])
		}
	})
}

func TestVerifyAdvisoryFiresWhenConfigUncommitted(t *testing.T) {
	// Panel finding #1, all shapes of "setup exists only outside HEAD":
	// setup that exists only on the checkout's disk — untracked, staged
	// but never committed, or a tracked file EDITED past its committed
	// stub — arms NOTHING: run worktrees materialize HEAD's tracked
	// content, so every diff still blocks verify_unconfigured and the
	// author's next step is exactly the advisory's COMMIT sentence.
	// Suppressing here re-created the original silence.
	t.Run("untracked", func(t *testing.T) {
		f, s, root, sha := armedVerifyServer(t)
		if err := os.WriteFile(filepath.Join(root, verifyCmdFile), []byte("echo PASS\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := runBlocked(t, f, s, sha, "README.md"); len(got) != 1 || got[0] != "verify_unconfigured" {
			t.Fatalf("reasons = %v, want [verify_unconfigured]", got)
		}
		if adv := verifyAdvisories(t, f.st, f.c.ID); len(adv) != 1 {
			t.Fatalf("advisories = %v, want exactly 1 (uncommitted config still needs the COMMIT advice)", adv)
		}
	})
	t.Run("staged-only", func(t *testing.T) {
		f, s, root, sha := armedVerifyServer(t)
		if err := os.WriteFile(filepath.Join(root, verifyCmdFile), []byte("echo PASS\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, root, "add", verifyCmdFile)
		if got := runBlocked(t, f, s, sha, "README.md"); len(got) != 1 || got[0] != "verify_unconfigured" {
			t.Fatalf("reasons = %v, want [verify_unconfigured]", got)
		}
		if adv := verifyAdvisories(t, f.st, f.c.ID); len(adv) != 1 {
			t.Fatalf("advisories = %v, want exactly 1 (staged-only config still needs the COMMIT advice)", adv)
		}
	})
	t.Run("tracked dirty past a stub HEAD", func(t *testing.T) {
		// The exact case the finding names: HEAD commits a comment-only
		// stub; the user edited the working file to a real command but
		// never committed. Judging the DISK copy would see a command and
		// suppress — yet every worktree materializes the stub and blocks.
		f, s, root, _ := armedVerifyServer(t)
		gitWriteVerify(t, root, "# TODO: real verify command\n")
		sha := gitOut(t, root, "rev-parse", "HEAD")
		if err := os.WriteFile(filepath.Join(root, verifyCmdFile), []byte("echo PASS\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := runBlocked(t, f, s, sha, "README.md"); len(got) != 1 || got[0] != "verify_unconfigured" {
			t.Fatalf("reasons = %v, want [verify_unconfigured]", got)
		}
		adv := verifyAdvisories(t, f.st, f.c.ID)
		if len(adv) != 1 {
			t.Fatalf("advisories = %v, want exactly 1 (uncommitted edit past a stub HEAD arms nothing)", adv)
		}
		if !strings.Contains(adv[0], "COMMIT") {
			t.Errorf("advisory must tell the author to commit:\n%s", adv[0])
		}
	})
}

// gitWriteVerify writes .odo-verify content and commits it (HEAD now
// carries the config). Callers refresh the diff BaseSHA after this.
func gitWriteVerify(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, verifyCmdFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", verifyCmdFile)
	gitIn(t, root, "commit", "-m", "verify config")
}

func TestVerifyAdvisoryReleasedOnJournalFailure(t *testing.T) {
	// The debounce may never swallow a failed append — losing the boot's
	// one reminder to a transient store error would re-create the
	// silence the advisory exists to end. A failed journal leaves the
	// key UNCLAIMED; the next blocked diff retries.
	f, s, root, _ := armedVerifyServer(t)
	// FK-forced append failure: no such conversation, so the insert
	// violates events.conversation_id REFERENCES.
	s.adviseVerifyUnconfigured(context.Background(), 1<<40, root, []string{"README.md"})
	if s.hasVerifyAdvised(root) {
		t.Error("debounce claimed after a failed journal append — the boot's one advisory would be lost")
	}
	// Retry path: the next blocked diff (working conversation) journals
	// the row and claims the key.
	s.adviseVerifyUnconfigured(context.Background(), f.c.ID, root, []string{"README.md"})
	if adv := verifyAdvisories(t, f.st, f.c.ID); len(adv) != 1 {
		t.Fatalf("advisories after retry = %d, want 1", len(adv))
	}
	if !s.hasVerifyAdvised(root) {
		t.Error("debounce key missing after a journaled advisory — later diffs would re-advise")
	}
}

func TestVerifyAdvisoryConcurrentSingleRow(t *testing.T) {
	// Panel finding #3: pipelines run concurrently. With ONE FK-broken
	// caller racing seven valid ones, claim+journal+release must still
	// leave exactly one row — the mutex orders any release strictly
	// before a retry's claim. (Sequential release/retry is pinned by
	// TestVerifyAdvisoryReleasedOnJournalFailure; this pins the race.)
	f, s, root, _ := armedVerifyServer(t)
	var wg sync.WaitGroup
	for i := range 8 {
		convID := f.c.ID
		if i == 0 {
			convID = 1 << 40 // append fails on the FK constraint
		}
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			s.adviseVerifyUnconfigured(context.Background(), id, root, []string{"README.md"})
		}(convID)
	}
	wg.Wait()
	if adv := verifyAdvisories(t, f.st, f.c.ID); len(adv) != 1 {
		t.Fatalf("advisories after concurrent blocks = %d, want exactly 1", len(adv))
	}
}

func TestVerifyAdvisorySuppressedForReclaimedWorktree(t *testing.T) {
	f, s, _, sha := armedVerifyServer(t)
	// recoverPendingDiffs re-drives autoLand with an empty worktree
	// path (the sweeper got there first) → the gate reports
	// verify_unconfigured, but the fix is "re-run or reject", not
	// project setup. No advisory.
	d := f.addDiff(t, "p.diff", patchSrc("README.md", 1, 1, false))
	d.BaseSHA = &sha
	s.autoLand(context.Background(), d, "", "goal", false, "")
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "verify_unconfigured" {
		t.Fatalf("reasons = %v, want [verify_unconfigured]", got)
	}
	if adv := verifyAdvisories(t, f.st, f.c.ID); len(adv) != 0 {
		t.Errorf("advisories = %v, want none (reclaimed worktree is not a setup problem)", adv)
	}
}

func TestVerifySetupAdviceToolchainHint(t *testing.T) {
	mk := func(t *testing.T, manifest, content string) string {
		t.Helper()
		dir := t.TempDir()
		if manifest != "" {
			if err := os.WriteFile(filepath.Join(dir, manifest), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return verifySetupAdvice(dir, verifyConfigNone, nil)
	}
	if got := mk(t, "go.mod", "module x\n"); !strings.Contains(got, "go build ./... && go vet ./... && go test ./...") {
		t.Errorf("go.mod hint = %s", got)
	}
	// Every hint must state the evidence contract (verify_no_evidence
	// is the very next block a build-only file would hit).
	if got := mk(t, "go.mod", "module x\n"); !strings.Contains(got, "test evidence") {
		t.Errorf("advice must state the evidence contract, got:\n%s", got)
	}
	if got := mk(t, "Cargo.toml", "[package]\n"); !strings.Contains(got, "`cargo test`") {
		t.Errorf("Cargo.toml hint = %s", got)
	}
	// `npm test` is advice ONLY when the script exists — npm without
	// scripts.test exits "Missing script: test", so that suggestion
	// would just trade verify_unconfigured for verify_failed.
	if got := mk(t, "package.json", `{"scripts":{"test":"vitest run"}}`); !strings.Contains(got, "`npm test`") {
		t.Errorf("package.json with a test script hint = %s", got)
	}
	if got := mk(t, "package.json", `{"scripts":{"build":"tsc"}}`); strings.Contains(got, "npm test") || !strings.Contains(got, "runs the project's tests") {
		t.Errorf("package.json without a test script must fall back to the generic hint, got: %s", got)
	}
	if got := mk(t, "package.json", "not json"); !strings.Contains(got, "runs the project's tests") {
		t.Errorf("unparseable package.json must fall back to the generic hint, got: %s", got)
	}
	if got := mk(t, "", ""); !strings.Contains(got, "runs the project's tests") {
		t.Errorf("no-manifest hint = %s", got)
	}
	// The scope-shaped (verifyConfigPartial) text names the committed
	// scopes and the fallback fix, and still carries the by-design
	// blocks: evidence contract + commit/tracked + supply-chain.
	got := verifySetupAdvice(t.TempDir(), verifyConfigPartial, []string{"gui/**", "docs/**"})
	for _, want := range []string{"gui/**, docs/**", "fallback", "test evidence", "COMMIT", "tracked files", "supply-chain"} {
		if !strings.Contains(got, want) {
			t.Errorf("partial advice missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "no usable command line") {
		t.Errorf("partial advice must not claim the committed config is missing:\n%s", got)
	}
	// Long scope lists are capped so the transcript row stays readable.
	many := verifySetupAdvice(t.TempDir(), verifyConfigPartial, []string{"a/**", "b/**", "c/**", "d/**", "e/**"})
	if !strings.Contains(many, "(+1 more)") || strings.Contains(many, "e/**") {
		t.Errorf("scope list must cap at 4 with a +N suffix, got:\n%s", many)
	}
}

func TestVerifyCommitConfig(t *testing.T) {
	writeDisk := func(t *testing.T, content string) string {
		t.Helper()
		root := initRepo(t)
		if err := os.WriteFile(filepath.Join(root, verifyCmdFile), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}

	if state, _ := verifyCommitConfig(initRepo(t), []string{"README.md"}); state != verifyConfigNone {
		t.Error("absent file: want verifyConfigNone")
	}
	// Finding #1: presence on disk ≠ setup. Until the file is committed,
	// run worktrees never carry it, so the gate keeps blocking and the
	// author still needs the COMMIT advice.
	if state, _ := verifyCommitConfig(writeDisk(t, "go test ./...\n"), []string{"README.md"}); state != verifyConfigNone {
		t.Error("untracked file: want verifyConfigNone (worktrees never see it)")
	}
	staged := writeDisk(t, "go test ./...\n")
	gitIn(t, staged, "add", verifyCmdFile)
	if state, _ := verifyCommitConfig(staged, []string{"README.md"}); state != verifyConfigNone {
		t.Error("staged-only file: want verifyConfigNone (HEAD still lacks it)")
	}
	// Finding #1's named case: committed comment-only stub, uncommitted
	// real command on disk. The DISK read would find a command; HEAD
	// judges correctly.
	dirty := writeDisk(t, "# stub\n")
	gitIn(t, dirty, "add", verifyCmdFile)
	gitIn(t, dirty, "commit", "-m", "stub")
	if err := os.WriteFile(filepath.Join(dirty, verifyCmdFile), []byte("go test ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if state, _ := verifyCommitConfig(dirty, []string{"README.md"}); state != verifyConfigNone {
		t.Error("tracked dirty past a stub HEAD: want verifyConfigNone (HEAD content judges)")
	}

	commit := func(t *testing.T, content string) string {
		t.Helper()
		root := writeDisk(t, content)
		gitIn(t, root, "add", verifyCmdFile)
		gitIn(t, root, "commit", "-m", "verify config")
		return root
	}

	commentOnly := commit(t, "# comments only\n\n")
	if state, _ := verifyCommitConfig(commentOnly, []string{"README.md"}); state != verifyConfigNone {
		t.Error("committed comment-only file: want verifyConfigNone (gate fails on it too)")
	}
	fallback := commit(t, "# head\ngo test ./...\n")
	if state, _ := verifyCommitConfig(fallback, []string{"README.md"}); state != verifyConfigReady {
		t.Error("committed fallback: want verifyConfigReady")
	}
	// A fallback covers every diff, including a path-less one.
	if state, _ := verifyCommitConfig(fallback, nil); state != verifyConfigReady {
		t.Error("committed fallback, nil paths: want verifyConfigReady")
	}
	// Finding #2: a scoped-only config is configured RELATIVE to the
	// blocked diff's paths — covered paths suppress (the stale-worktree
	// fix), uncovered paths keep blocking (the scope advisory fires).
	scoped := commit(t, "gui/**: cd gui && npm test\n")
	if state, _ := verifyCommitConfig(scoped, []string{"gui/app.ts"}); state != verifyConfigReady {
		t.Error("scoped config covering the diff: want verifyConfigReady")
	}
	state, globs := verifyCommitConfig(scoped, []string{"README.md"})
	if state != verifyConfigPartial {
		t.Errorf("scoped config missing the diff: state = %v, want verifyConfigPartial", state)
	}
	if len(globs) != 1 || globs[0] != "gui/**" {
		t.Errorf("scoped globs = %v, want [gui/**]", globs)
	}
	if state, _ := verifyCommitConfig(scoped, nil); state != verifyConfigPartial {
		t.Error("scoped-only config, nil paths: want verifyConfigPartial (gate resolves no command)")
	}
	// Mixed config: the fallback covers what the globs miss.
	mixed := commit(t, "gui/**: cd gui && npm test\ngo test ./...\n")
	if state, _ := verifyCommitConfig(mixed, []string{"README.md"}); state != verifyConfigReady {
		t.Error("scoped + fallback: want verifyConfigReady for out-of-scope paths")
	}
}
