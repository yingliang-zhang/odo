package ipc

// M16 (O-1 v2): the auto-land pipeline's observable contracts. Pure-gate
// boundaries first (autoLandCheck over real patch text against a real base
// tree), the verify gate's config and runner, prompt assembly, then the
// journaled blocked-path integrations: every blocked attempt must leave
// exactly one review_action{auto_land_blocked} row naming its reason, and
// the pipeline must never land without a panel (no_review_models pins that
// fail-closed path without network).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// autolandRepo builds a git repo whose base tree has a top-level file and
// one top directory (src/), returning (root, HEAD sha) — the base a diff
// under review is judged against.
func autolandRepo(t *testing.T) (string, string) {
	t.Helper()
	root := initRepo(t) // README.md commit
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "src")
	return root, gitOut(t, root, "rev-parse", "HEAD")
}

func TestAutoLandCheck(t *testing.T) {
	root, sha := autolandRepo(t)
	s := &Server{projectRoot: root}

	bigTestPatch := func(adds, removes int) string {
		var b strings.Builder
		b.WriteString("diff --git a/src/x_test.go b/src/x_test.go\n--- a/src/x_test.go\n+++ b/src/x_test.go\n@@ -1,1 +1,1 @@\n")
		for i := range removes {
			fmt.Fprintf(&b, "-\tassert.Equal(t, %d, got)\n", i)
		}
		for i := range adds {
			fmt.Fprintf(&b, "+\tassert.Equal(t, %d, got)\n", i)
		}
		return b.String()
	}

	cases := []struct {
		name       string
		patch      string
		base       string // empty means nil BaseSHA
		nonexist   bool   // PathOnDisk points at a missing file
		wantReason string
		wantDetail string // substring; "" = don't check
	}{
		{"modify in existing top dir", patchSrc("src/a.go", 1, 1, false), sha, false, "", ""},
		{"top-level file", patchSrc("README.md", 1, 1, false), sha, false, "", ""},
		{"new subdir under existing top", patchSrc("src/sub/f.go", 2, 0, true), sha, false, "", ""},
		{"protected .odo path", patchSrc(".odo/memory.md", 1, 1, false), sha, false, "protected_path", ".odo/"},
		{"protected wiki path", patchSrc("wiki/guide.md", 1, 0, true), sha, false, "protected_path", "wiki/"},
		{"protected .odo path uppercase", patchSrc(".ODO/memory.md", 1, 1, false), sha, false, "protected_path", ".ODO/"},
		{"protected wiki path mixed case", patchSrc("Wiki/guide.md", 1, 0, true), sha, false, "protected_path", "Wiki/"},
		{"lockfile at top level", patchSrc("go.sum", 1, 1, false), sha, false, "supply_chain_path", "go.sum"},
		{"nested manifest, case-insensitive", patchSrc("gui/Package-Lock.JSON", 1, 1, false), sha, false, "supply_chain_path", "Package-Lock.JSON"},
		{"verify config is self-protected", patchSrc(".odo-verify", 1, 1, false), sha, false, "supply_chain_path", ".odo-verify"},
		{"new top-level directory", patchSrc("newdir/f.go", 2, 0, true), sha, false, "new_top_dir", "newdir/"},
		{"tests weakened", bigTestPatch(1, 2), sha, false, "test_assertions_decreased", "+1 added / -2 removed"},
		{"tests strengthened pass", bigTestPatch(2, 1), sha, false, "", ""},
		{"nil base sha", patchSrc("src/a.go", 1, 1, false), "", false, "base_unresolvable", ""},
		{"missing patch file", "", sha, true, "unparseable_diff", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			patchPath := filepath.Join(t.TempDir(), "d.diff")
			if !tc.nonexist {
				if err := os.WriteFile(patchPath, []byte(tc.patch), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			d := store.Diff{PathOnDisk: patchPath}
			if tc.base != "" {
				d.BaseSHA = &tc.base
			}
			reason, detail := s.autoLandCheck(d)
			if reason != tc.wantReason {
				t.Errorf("reason = %q (detail %q), want %q", reason, detail, tc.wantReason)
			}
			if tc.wantDetail != "" && !strings.Contains(detail, tc.wantDetail) {
				t.Errorf("detail = %q, want substring %q", detail, tc.wantDetail)
			}
		})
	}
}

func TestVerifyCommand(t *testing.T) {
	t.Run("missing file is fail-closed", func(t *testing.T) {
		if _, err := verifyCommand(t.TempDir()); err == nil {
			t.Fatal("no .odo-verify: want error")
		}
	})
	t.Run("comments only is fail-closed", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".odo-verify"), []byte("# nothing\n\n  \n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := verifyCommand(dir); err == nil {
			t.Fatal("comment-only .odo-verify: want error")
		}
	})
	t.Run("first non-comment line wins", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".odo-verify"), []byte("# comment\n\ngo test ./...\n# later\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd, err := verifyCommand(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cmd != "go test ./..." {
			t.Errorf("cmd = %q, want %q", cmd, "go test ./...")
		}
	})
}

func TestRunVerify(t *testing.T) {
	ctx := context.Background()
	t.Run("success returns output", func(t *testing.T) {
		out, err := runVerify(ctx, t.TempDir(), "printf hello")
		if err != nil {
			t.Fatal(err)
		}
		if out != "hello" {
			t.Errorf("out = %q, want hello", out)
		}
	})
	t.Run("failure returns error and output", func(t *testing.T) {
		out, err := runVerify(ctx, t.TempDir(), "printf doomed; exit 3")
		if err == nil {
			t.Fatal("exit 3: want error")
		}
		if !strings.Contains(out, "doomed") {
			t.Errorf("out = %q, want it to carry the failing output", out)
		}
	})
	t.Run("output truncated to the tail", func(t *testing.T) {
		out, err := runVerify(ctx, t.TempDir(), "i=0; while [ $i -lt 3000 ]; do echo line$i; i=$((i+1)); done")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) > autoLandVerifyTailBytes {
			t.Errorf("out len = %d, want <= %d", len(out), autoLandVerifyTailBytes)
		}
		if !strings.HasSuffix(out, "line2999\n") {
			t.Errorf("out tail = %q, want the END of the output (diagnostics sit at the end)", out[len(out)-40:])
		}
	})
}

// TestVerifyEnviron pins the allowlist contract: the verify child sees
// shell/toolchain vars and GO*/GIT_* passthrough, never the daemon's
// credential-shaped vars (the panel's API keys are process env).
func TestVerifyEnviron(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HOME=/u", "TMPDIR=/tmp", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8",
		"GOPATH=/u/go", "GOMODCACHE=/u/go/pkg/mod", "GOFLAGS=-mod=mod", "GIT_EDITOR=vim", "CGO_ENABLED=1",
		"SUDO_CODING_KEY=sk-secret", "ANTHROPIC_API_KEY=sk-secret", "AWS_SECRET_ACCESS_KEY=secret",
		"SSH_AUTH_SOCK=/tmp/sock", "ODO_DB=/private",
	}
	out := verifyEnviron(in)
	joined := "\n" + strings.Join(out, "\n") + "\n"
	for _, want := range []string{"PATH=/usr/bin", "GOPATH=/u/go", "GOMODCACHE=/u/go/pkg/mod", "GIT_EDITOR=vim", "LC_ALL=en_US.UTF-8"} {
		if !strings.Contains(joined, "\n"+want+"\n") {
			t.Errorf("env missing %q", want)
		}
	}
	for _, drop := range []string{"SUDO_CODING_KEY", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK", "ODO_DB"} {
		if strings.Contains(joined, "\n"+drop+"=") {
			t.Errorf("env leaked %s — an allowlist miss costs the daemon's keys", drop)
		}
	}
}

// blockedReasons returns every auto_land_blocked reason journaled for the
// conversation, in order.
func blockedReasons(t *testing.T, st *store.Store, convID int64) []string {
	t.Helper()
	events, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for _, e := range events {
		if e.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("event %d: %v", e.ID, err)
		}
		if p.Action == "auto_land_blocked" {
			reasons = append(reasons, p.Reason)
		}
	}
	return reasons
}

func TestAutoLandBlockedPaths(t *testing.T) {
	// Each case drives s.autoLand to one blocked exit and asserts the
	// journal row. Cases needing the panel without network isolate HOME so
	// prefs.md has no review: line (empty models = blocked, never landed).
	newServer := func(t *testing.T) (autonomyFixture, *Server, string, string) {
		f := newAutonomyFixture(t)
		root, sha := autolandRepo(t)
		return f, &Server{store: f.st, projectRoot: root}, root, sha
	}

	t.Run("run errored", func(t *testing.T) {
		f, s, _, _ := newServer(t)
		d := f.addDiff(t, "p.diff", patchSrc("src/a.go", 1, 1, false))
		s.autoLand(context.Background(), d, t.TempDir(), "goal", true, "")
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "run_errored" {
			t.Errorf("reasons = %v, want [run_errored]", got)
		}
	})

	t.Run("verify gate missing config", func(t *testing.T) {
		f, s, root, sha := newServer(t)
		d := f.addDiff(t, "p.diff", patchSrc("README.md", 1, 1, false))
		d.BaseSHA = &sha
		s.autoLand(context.Background(), d, root, "goal", false, "")
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "verify_unconfigured" {
			t.Errorf("reasons = %v, want [verify_unconfigured]", got)
		}
	})

	t.Run("prompt cost breaker", func(t *testing.T) {
		f, s, root, sha := newServer(t)
		// The verify must emit pass evidence (M18 batch B, B4: an
		// output-less "exit 0" would now stop at verify_no_evidence
		// before the cost breaker).
		if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo PASS\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// ~45 chars/line × 9000 ≈ 400KB > 87K-token×4 estimate.
		var b strings.Builder
		b.WriteString("diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1,9001 @@\n")
		for range 9000 {
			b.WriteString("+padding line for the cost breaker gate ........\n")
		}
		big := b.String()
		d := f.addDiff(t, "big.diff", big)
		d.BaseSHA = &sha
		s.autoLand(context.Background(), d, root, "goal", false, "")
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "prompt_too_large" {
			t.Errorf("reasons = %v, want [prompt_too_large]", got)
		}
	})

	t.Run("no review models is fail-closed", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir()) // no prefs.md → no review: models
		f, s, root, sha := newServer(t)
		// Pass evidence required (B4): an evidence-less verify blocks
		// before the panel-model check.
		if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo PASS\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		d := f.addDiff(t, "p.diff", patchSrc("README.md", 1, 1, false))
		d.BaseSHA = &sha
		s.autoLand(context.Background(), d, root, "goal", false, "")
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "no_review_models" {
			t.Errorf("reasons = %v, want [no_review_models]", got)
		}
	})
}

// TestMaybeAutoLandPrefOffSilent: a disabled auto_apply leaves NO journal
// trace — a turned-off feature deserves no noise (blocked attempts are the
// evidence, silence is the off state).
func TestMaybeAutoLandPrefOffSilent(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // default auto_apply = off
	f := newAutonomyFixture(t)
	s := &Server{store: f.st, projectRoot: f.dir}
	d := f.addDiff(t, "p.diff", patchSrc("src/a.go", 1, 1, false))
	s.maybeAutoLand(d, f.dir, "goal", false, "")
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 0 {
		t.Errorf("reasons = %v, want none (pref off must be silent)", got)
	}
}

// TestAutoLandVerifyNoEvidence (B4): an exit-0 verify whose output tail
// shows zero test evidence blocks before ANY panel spend — and, flipped
// on, pass evidence lets the same config proceed to the panel-model gate.
func TestAutoLandVerifyNoEvidence(t *testing.T) {
	t.Run("zero evidence blocks", func(t *testing.T) {
		f := newAutonomyFixture(t)
		root, sha := autolandRepo(t)
		if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo build-only-done\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := &Server{store: f.st, projectRoot: root}
		d := f.addDiff(t, "p.diff", patchSrc("src/a.go", 1, 1, false))
		d.BaseSHA = &sha
		s.autoLand(context.Background(), d, root, "goal", false, "")
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "verify_no_evidence" {
			t.Errorf("reasons = %v, want [verify_no_evidence]", got)
		}
		// Fail closed: the diff stays pending for the human.
		got, err := f.st.GetDiff(context.Background(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.DiffPending {
			t.Errorf("diff status = %q, want pending", got.Status)
		}
	})

	t.Run("go-test-shaped tail passes the gate", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir()) // no prefs → the next gate (no_review_models) proves we got PAST verify
		f := newAutonomyFixture(t)
		root, sha := autolandRepo(t)
		if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("printf 'ok  \\tpkg/mod\\t0.1s\\n'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := &Server{store: f.st, projectRoot: root}
		d := f.addDiff(t, "p.diff", patchSrc("src/a.go", 1, 1, false))
		d.BaseSHA = &sha
		s.autoLand(context.Background(), d, root, "goal", false, "")
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "no_review_models" {
			t.Errorf("reasons = %v, want [no_review_models] (an ok line satisfies the evidence gate)", got)
		}
	})
}

// TestAutoLandVisualGate (B5, m16 gate 12): a diff touching gui/src/**
// never auto-lands regardless of panel outcome — blocked
// human_gate_visual with the completed panel riding the row as advisory
// evidence; the ladder never ticks; the diff stays pending for human
// visual acceptance.
func TestAutoLandVisualGate(t *testing.T) {
	// visualRepo commits gui/src/ + the verify config so the new-top-dir
	// and base-freshness gates pass for a gui diff.
	visualRepo := func(t *testing.T) (root, sha string) {
		root, _ = autolandRepo(t)
		if err := os.MkdirAll(filepath.Join(root, "gui", "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "gui", "src", "app.ts"), []byte("export const x = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo PASS\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, root, "add", ".")
		gitIn(t, root, "commit", "-m", "gui + verify")
		return root, gitOut(t, root, "rev-parse", "HEAD")
	}
	setup := func(t *testing.T, reply func(call int64, model string) (int, string)) (autonomyFixture, *Server, store.Diff, string) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writePrefs(t, home, "review: rm1@test\n")
		startPanelStub(t, reply)
		f := newAutonomyFixture(t)
		root, sha := visualRepo(t)
		s := &Server{store: f.st, projectRoot: root}
		d := f.addDiff(t, "p.diff", patchSrc("gui/src/app.ts", 3, 1, false))
		d.BaseSHA = &sha
		return f, s, d, root
	}

	t.Run("unanimous accept still blocks (advisory only)", func(t *testing.T) {
		f, s, d, root := setup(t, func(call int64, model string) (int, string) {
			return 200, "ACCEPT\nlooks correct"
		})
		s.autoLand(context.Background(), d, root, "goal", false, "")
		sc := scanSettle(t, f.st, f.c.ID)
		if got := sc.blockedReasons(); len(got) != 1 || got[0] != "human_gate_visual" {
			t.Fatalf("blocked reasons = %v, want [human_gate_visual]", got)
		}
		row := sc.blocked[0]
		if row["consensus_verdict"] != "accept" {
			t.Errorf("consensus_verdict = %v, want accept riding as advisory evidence", row["consensus_verdict"])
		}
		reviews, _ := row["reviews"].([]interface{})
		if len(reviews) != 1 {
			t.Errorf("reviews = %v, want the 1-model panel attached", row["reviews"])
		}
		if detail, _ := row["detail"].(string); !strings.Contains(detail, "gui/src/app.ts") {
			t.Errorf("detail = %q, want the visual path named", detail)
		}
		if len(sc.moaRows) != 0 || len(sc.accepts) != 0 {
			t.Errorf("landing rows = %v moa %v accepts, want none (the blocked row is the only evidence)", sc.moaRows, sc.accepts)
		}
		got, err := f.st.GetDiff(context.Background(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.DiffPending {
			t.Errorf("diff status = %q, want pending (human visual acceptance is the accept path)", got.Status)
		}
	})

	t.Run("needs_fixes blocks visually, never spawns a revise round", func(t *testing.T) {
		f, s, d, root := setup(t, func(call int64, model string) (int, string) {
			return 200, "NEEDS_FIXES\nthe layout regressed on narrow viewports"
		})
		s.autoLand(context.Background(), d, root, "goal", false, "")
		sc := scanSettle(t, f.st, f.c.ID)
		if got := sc.blockedReasons(); len(got) != 1 || got[0] != "human_gate_visual" {
			t.Fatalf("blocked reasons = %v, want [human_gate_visual] (needs_fixes never reaches the ladder on a visual diff)", got)
		}
		if len(sc.rounds) != 0 || len(sc.markers) != 0 {
			t.Errorf("ladder fired on a visual diff: rounds=%v markers=%v", sc.rounds, sc.markers)
		}
	})
}

// TestAutoLandFinalRefreshClean (P0a; supersedes fix-INT's
// TestAutoLandBaseStaleAtLand): HEAD drifts MID-PIPELINE — the entry probe
// passed fresh, verify and the panel ran, and only then a racing commit
// moved main on a DISJOINT path. The FINAL gate inside handleDiffAction
// (checkAndRefreshBase) rebases the diff onto current HEAD and lands it:
// refresh_attempted{clean, accept_apply} rides between the moa_review and
// accept rows, base_stale_at_land is NEVER journaled, and the accept row
// carries refreshed_from_sha naming the base the panel judged. The diff's
// base must ride the STORE row — handleDiffAction re-reads it.
func TestAutoLandFinalRefreshClean(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test\n")
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: f.st, projectRoot: root}
	// realPatch, not patchSrc: the FINAL gate must actually APPLY this
	// patch onto main, and git apply --3way adjudicates real content.
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "landed.go"), []byte("package src // landed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	oldBase := *d.BaseSHA

	// The panel stub drifts main HEAD inside its handler — a racing human
	// commit while the verdict is in flight — then replies ACCEPT for
	// every leg anyway: the verdict genuinely arrived, just under a HEAD
	// the land no longer applies to without a rebase.
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		if werr := os.WriteFile(filepath.Join(root, "drift.txt"), []byte("racing human commit\n"), 0o644); werr != nil {
			t.Errorf("drift write: %v", werr)
		}
		for _, args := range [][]string{{"add", "drift.txt"}, {"commit", "-m", "drift"}} {
			argv := append([]string{"-C", root, "-c", "user.email=odo@test", "-c", "user.name=odo"}, args...)
			if out, gerr := exec.Command("git", argv...).CombinedOutput(); gerr != nil {
				t.Errorf("drift %v: %v: %s", args, gerr, out)
			}
		}
		return 200, "ACCEPT\nlooks correct"
	})

	s.autoLand(context.Background(), d, root, "goal", false, "")
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Fatalf("panel calls = %d, want 1", n)
	}
	sc := scanSettle(t, f.st, f.c.ID)
	if got := sc.blockedReasons(); len(got) != 0 {
		t.Fatalf("blocked reasons = %v, want none — a disjoint drift refreshes clean and lands", got)
	}
	if len(sc.moaRows) != 1 {
		t.Errorf("moa_review rows = %d, want 1 (evidence before action)", len(sc.moaRows))
	}
	if len(sc.accepts) != 1 {
		t.Fatalf("accept rows = %v, want exactly one (the refreshed land)", sc.accepts)
	}
	// Journal order: moa_review → refresh_attempted → accept.
	driftHead := gitOut(t, root, "rev-parse", "HEAD~1") // accept commit sits on top
	var moaIdx, refreshIdx, acceptIdx = -1, -1, -1
	for i, p := range sc.reviewSeq {
		switch p["action"] {
		case "moa_review":
			moaIdx = i
		case "refresh_attempted":
			refreshIdx = i
		case "accept":
			acceptIdx = i
		}
	}
	if moaIdx < 0 || refreshIdx < 0 || acceptIdx < 0 || !(moaIdx < refreshIdx && refreshIdx < acceptIdx) {
		t.Errorf("reviewSeq order moa=%d refresh=%d accept=%d, want moa < refresh < accept", moaIdx, refreshIdx, acceptIdx)
	}
	r := sc.reviewSeq[refreshIdx]
	if r["outcome"] != "clean" || r["phase"] != "accept_apply" {
		t.Errorf("refresh row = %v, want {clean, accept_apply}", r)
	}
	if r["base_sha"] != oldBase || r["target_sha"] != driftHead {
		t.Errorf("refresh shas = %v→%v, want %s→%s", r["base_sha"], r["target_sha"], oldBase, driftHead)
	}
	a := sc.accepts[0]
	if a["actor"] != autoActor {
		t.Errorf("accept actor = %v, want %s", a["actor"], autoActor)
	}
	if a["refreshed_from_sha"] != oldBase {
		t.Errorf("accept refreshed_from_sha = %v, want the panel-judged base %s", a["refreshed_from_sha"], oldBase)
	}
	if a["base_sha"] != driftHead || a["head_sha"] != driftHead {
		t.Errorf("accept base/head = %v/%v, want the refreshed HEAD %s for both", a["base_sha"], a["head_sha"], driftHead)
	}
	// M18 W2 item 4: the auto-land moa_review attests the exact diff bytes
	// the fanout judged (the file read at pipeline entry) — unchanged by
	// the refresh (bounded attestation gap, stated in the lock).
	diffBytes, rerr := os.ReadFile(d.PathOnDisk)
	if rerr != nil {
		t.Fatalf("read judged diff: %v", rerr)
	}
	if got := sc.moaRows[0]["patch_sha16"]; got != sha16(diffBytes) {
		t.Errorf("moa_review patch_sha16 = %v, want sha16 of the judged diff bytes", got)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffAccepted {
		t.Errorf("diff status = %q, want accepted (the refreshed land)", got.Status)
	}
	if got.BaseSHA == nil || *got.BaseSHA != driftHead {
		t.Errorf("store base_sha = %v, want refreshed to %s", got.BaseSHA, driftHead)
	}
	// Both the racing commit's and the diff's content are in main.
	if _, serr := os.Stat(filepath.Join(root, "drift.txt")); serr != nil {
		t.Errorf("drift.txt missing: %v — the racing commit stays", serr)
	}
	if _, serr := os.Stat(filepath.Join(root, "src", "landed.go")); serr != nil {
		t.Errorf("src/landed.go missing: %v — the refreshed land must apply", serr)
	}
}

// TestHandleDiffActionStaleRefusalIsSentinel (fix-INT D3, P0a-updated):
// when the final gate's automatic refresh CONFLICTS, the refusal still
// wraps errBaseStale so the auto-land caller's errors.Is branch fires,
// and the handler journals ONLY its refresh_attempted{conflict} row —
// the pipeline caller owns the blocked row (it alone has the completed
// panel evidence to attach). Main is rolled back and the diff pending.
func TestHandleDiffActionStaleRefusalIsSentinel(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	oldBase := gitOut(t, root, "rev-parse", "HEAD")
	// realPatch + overlapping drift: the refresh attempt must produce a
	// REAL 3-way conflict (a hand-shaped patch would degrade to the
	// "error" outcome and never exercise the conflict classification).
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // agent edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // user drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "src/a.go")
	gitIn(t, root, "commit", "-m", "user drift")
	head := gitOut(t, root, "rev-parse", "HEAD")

	_, err := s.handleDiffAction(context.Background(), d.ID, "accept", autoActor)
	if !errors.Is(err, errBaseStale) {
		t.Fatalf("err = %v, want errors.Is(err, errBaseStale)", err)
	}
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 0 {
		t.Errorf("blocked reasons = %v, want none — the handler owns no auto_land_blocked row", got)
	}
	events, err := f.st.ListEvents(context.Background(), f.c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var refreshRows int
	for _, e := range events {
		if e.Type != store.EventReviewAction {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("event %d: %v", e.ID, err)
		}
		if p["action"] != "refresh_attempted" {
			t.Errorf("sentinel refusal journaled %s, want ONLY the refresh_attempted row", e.Payload)
			continue
		}
		refreshRows++
		if p["outcome"] != "conflict" || p["phase"] != "accept_apply" {
			t.Errorf("refresh row = %v, want {conflict, accept_apply}", p)
		}
		if p["base_sha"] != oldBase || p["target_sha"] != head {
			t.Errorf("refresh shas = %v→%v, want %s→%s", p["base_sha"], p["target_sha"], oldBase, head)
		}
	}
	if refreshRows != 1 {
		t.Errorf("refresh_attempted rows = %d, want 1 (one attempt per gate encounter)", refreshRows)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending", got.Status)
	}
	// Rolled back: main carries only the drifted edit, cleanly.
	if data, rerr := os.ReadFile(filepath.Join(root, "src", "a.go")); rerr != nil || string(data) != "package src // user drift\n" {
		t.Errorf("src/a.go = %q, %v — rollback must restore the drifted content", data, rerr)
	}
	if unmerged := gitOut(t, root, "ls-files", "-u"); unmerged != "" {
		t.Errorf("unmerged index entries after rollback:\n%s", unmerged)
	}
}

// TestAutoLandPreSpendRefreshClean (P0a): main drifted BEFORE the pipeline
// started, on a path the diff doesn't touch. The pre-spend probe merges
// cleanly in its throwaway worktree, the diff's base pointer moves to the
// drifted HEAD, and the pipeline proceeds exactly as if the base had been
// fresh: verify + panel run, the diff lands. refresh_attempted{clean,
// pre_spend_probe} is the only refresh row — the final gate re-reads the
// (already corrected) store row and sees a fresh base.
func TestAutoLandPreSpendRefreshClean(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test\n")
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "landed.go"), []byte("package src // landed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	oldBase := *d.BaseSHA
	head := driftMain(t, root, "src/drift.go") // disjoint drift, before pipeline entry

	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	s.autoLand(context.Background(), d, root, "goal", false, "")
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Errorf("panel calls = %d, want 1 (a clean probe proceeds to the panel)", n)
	}
	sc := scanSettle(t, f.st, f.c.ID)
	if got := sc.blockedReasons(); len(got) != 0 {
		t.Fatalf("blocked reasons = %v, want none — the clean probe cleared entry", got)
	}
	if len(sc.accepts) != 1 {
		t.Fatalf("accept rows = %v, want exactly one (the probe preceded a normal land)", sc.accepts)
	}
	// refresh_attempted{clean, pre_spend_probe} precedes moa_review and
	// the accept; the final gate saw a fresh base and refreshed nothing
	// itself (the accept row carries no refreshed_from_sha).
	var refreshIdx, moaIdx, acceptIdx = -1, -1, -1
	for i, p := range sc.reviewSeq {
		switch p["action"] {
		case "refresh_attempted":
			refreshIdx = i
		case "moa_review":
			moaIdx = i
		case "accept":
			acceptIdx = i
		}
	}
	if !(refreshIdx >= 0 && refreshIdx < moaIdx && moaIdx < acceptIdx) {
		t.Errorf("reviewSeq order refresh=%d moa=%d accept=%d, want refresh < moa < accept", refreshIdx, moaIdx, acceptIdx)
	}
	r := sc.reviewSeq[refreshIdx]
	if r["outcome"] != "clean" || r["phase"] != "pre_spend_probe" {
		t.Errorf("refresh row = %v, want {clean, pre_spend_probe}", r)
	}
	if r["base_sha"] != oldBase || r["target_sha"] != head {
		t.Errorf("refresh shas = %v→%v, want %s→%s", r["base_sha"], r["target_sha"], oldBase, head)
	}
	if r["actor"] != autoActor {
		t.Errorf("probe refresh actor = %v, want %s (probes only run inside the pipeline)", r["actor"], autoActor)
	}
	if _, has := sc.accepts[0]["refreshed_from_sha"]; has {
		t.Errorf("accept carries refreshed_from_sha = %v — the final gate saw a fresh base", sc.accepts[0]["refreshed_from_sha"])
	}
	if sc.accepts[0]["base_sha"] != head {
		t.Errorf("accept base_sha = %v, want the probe-refreshed %s", sc.accepts[0]["base_sha"], head)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffAccepted {
		t.Errorf("diff status = %q, want accepted", got.Status)
	}
	if got.BaseSHA == nil || *got.BaseSHA != head {
		t.Errorf("store base_sha = %v, want %s (the probe moved the pointer)", got.BaseSHA, head)
	}
	// The probe never touched main: the drift commit and the landed diff
	// are the only tree changes.
	for _, rel := range []string{"src/drift.go", "src/landed.go"} {
		if _, serr := os.Stat(filepath.Join(root, rel)); serr != nil {
			t.Errorf("%s missing after the refreshed land: %v", rel, serr)
		}
	}
}

// TestAutoLandPreSpendRefreshConflict (P0a): entry drift that OVERLAPS the
// diff — the pre-spend probe's 3-way merge conflicts in its throwaway
// worktree. The pipeline blocks exactly where the old hard refusal did
// (base_stale, before ANY verify/panel spend), one refresh_attempted
// {conflict, pre_spend_probe} row precedes the blocked row, the diff stays
// pending, and main was never touched by the probe.
func TestAutoLandPreSpendRefreshConflict(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	oldBase := gitOut(t, root, "rev-parse", "HEAD")
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nshould never be consulted"
	})
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // agent edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	// Drift main by editing the SAME line the patch rewrites.
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // user drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "src/a.go")
	gitIn(t, root, "commit", "-m", "user drift")
	head := gitOut(t, root, "rev-parse", "HEAD")

	s.autoLand(context.Background(), d, root, "goal", false, "")
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "base_stale" {
		t.Fatalf("reasons = %v, want [base_stale]", got)
	}
	sc := scanSettle(t, f.st, f.c.ID)
	if detail, _ := sc.blocked[0]["detail"].(string); !strings.Contains(detail, "refresh probe: conflict") {
		t.Errorf("blocked detail = %q, want the refresh-probe conflict named", detail)
	}
	if n := atomic.LoadInt64(calls); n != 0 {
		t.Errorf("panel calls = %d, want 0 (the entry probe precedes all spend)", n)
	}
	// refresh_attempted{conflict, pre_spend_probe} immediately precedes the
	// blocked row (journal-first, hard rule 6).
	if len(sc.reviewSeq) != 2 || sc.reviewSeq[0]["action"] != "refresh_attempted" || sc.reviewSeq[1]["action"] != "auto_land_blocked" {
		t.Fatalf("reviewSeq = %v, want [refresh_attempted, auto_land_blocked]", sc.reviewSeq)
	}
	r := sc.reviewSeq[0]
	if r["outcome"] != "conflict" || r["phase"] != "pre_spend_probe" {
		t.Errorf("refresh row = %v, want {conflict, pre_spend_probe}", r)
	}
	if r["base_sha"] != oldBase || r["target_sha"] != head {
		t.Errorf("refresh shas = %v→%v, want %s→%s", r["base_sha"], r["target_sha"], oldBase, head)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending", got.Status)
	}
	if got.BaseSHA == nil || *got.BaseSHA != oldBase {
		t.Errorf("store base_sha = %v, want the ORIGINAL %s (a failed probe moves nothing)", got.BaseSHA, oldBase)
	}
	// The probe ran in a throwaway worktree: main carries only the drift
	// commit, the checkout is clean, and no probe worktree leaked.
	if data, rerr := os.ReadFile(filepath.Join(root, "src", "a.go")); rerr != nil || string(data) != "package src // user drift\n" {
		t.Errorf("src/a.go = %q, %v — a probe must never touch main", data, rerr)
	}
	if status := gitOut(t, root, "status", "--porcelain"); status != "" {
		t.Errorf("main status = %q after the probe, want clean", status)
	}
	if n := strings.Count(gitOut(t, root, "worktree", "list", "--porcelain"), "worktree "); n != 1 {
		t.Errorf("worktree count = %d, want 1 (the probe's worktree was removed)", n)
	}
}

// TestAutoLandFinalRefreshConflict (P0a): the entry probe passed (fresh
// base), verify + the panel ran — and THEN a racing commit drifted main
// ONTO the diff's own path. The final gate's rebase conflicts: main rolls
// back, one refresh_attempted{conflict, accept_apply} row rides between
// the moa_review and the base_stale_at_land blocked row (which carries
// the completed panel as advisory evidence), and the diff stays pending.
func TestAutoLandFinalRefreshConflict(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test\n")
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: f.st, projectRoot: root}
	oldBase := gitOut(t, root, "rev-parse", "HEAD")
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // agent edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))

	// The panel stub drifts main ONTO src/a.go mid-verdict — a racing
	// human edit of the very file the diff rewrites — then replies ACCEPT:
	// the evidence is real, the rebase genuinely cannot merge them.
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		if werr := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // racing human edit\n"), 0o644); werr != nil {
			t.Errorf("drift write: %v", werr)
		}
		for _, args := range [][]string{{"add", "src/a.go"}, {"commit", "-m", "racing edit"}} {
			argv := append([]string{"-C", root, "-c", "user.email=odo@test", "-c", "user.name=odo"}, args...)
			if out, gerr := exec.Command("git", argv...).CombinedOutput(); gerr != nil {
				t.Errorf("drift %v: %v: %s", args, gerr, out)
			}
		}
		return 200, "ACCEPT\nlooks correct"
	})

	s.autoLand(context.Background(), d, root, "goal", false, "")
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Fatalf("panel calls = %d, want 1 (the panel RAN — the block rides its evidence)", n)
	}
	sc := scanSettle(t, f.st, f.c.ID)
	if got := sc.blockedReasons(); len(got) != 1 || got[0] != "base_stale_at_land" {
		t.Fatalf("blocked reasons = %v, want [base_stale_at_land]", got)
	}
	row := sc.blocked[0]
	if row["consensus_verdict"] != "accept" {
		t.Errorf("consensus_verdict = %v, want accept riding as advisory evidence", row["consensus_verdict"])
	}
	reviews, _ := row["reviews"].([]interface{})
	if len(reviews) != 1 {
		t.Errorf("reviews = %v, want the 1-model panel attached", row["reviews"])
	}
	if detail, _ := row["detail"].(string); !strings.Contains(detail, "the verify and panel attested the pre-drift tree") {
		t.Errorf("detail = %q, want the pre-drift attestation advisory", detail)
	}
	// Journal order: moa_review → refresh_attempted{conflict} →
	// base_stale_at_land, and NO accept row.
	var moaIdx, refreshIdx, blockedIdx = -1, -1, -1
	for i, p := range sc.reviewSeq {
		switch p["action"] {
		case "moa_review":
			moaIdx = i
		case "refresh_attempted":
			refreshIdx = i
		case "auto_land_blocked":
			blockedIdx = i
		}
	}
	if !(moaIdx >= 0 && moaIdx < refreshIdx && refreshIdx < blockedIdx) {
		t.Errorf("reviewSeq order moa=%d refresh=%d blocked=%d, want moa < refresh < blocked", moaIdx, refreshIdx, blockedIdx)
	}
	r := sc.reviewSeq[refreshIdx]
	if r["outcome"] != "conflict" || r["phase"] != "accept_apply" {
		t.Errorf("refresh row = %v, want {conflict, accept_apply}", r)
	}
	if r["base_sha"] != oldBase {
		t.Errorf("refresh base_sha = %v, want the panel-judged %s", r["base_sha"], oldBase)
	}
	if len(sc.accepts) != 0 {
		t.Errorf("accept rows = %v, want none (nothing landed)", sc.accepts)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending (NOT conflict — that's reserved for fresh-base apply failures)", got.Status)
	}
	// Rolled back: main carries only the racing commit's edit, cleanly.
	if data, rerr := os.ReadFile(filepath.Join(root, "src", "a.go")); rerr != nil || string(data) != "package src // racing human edit\n" {
		t.Errorf("src/a.go = %q, %v — rollback must restore the racing edit", data, rerr)
	}
	if unmerged := gitOut(t, root, "ls-files", "-u"); unmerged != "" {
		t.Errorf("unmerged index entries after rollback:\n%s", unmerged)
	}
}
