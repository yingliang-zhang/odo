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
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	t.Run("stale base blocks before any verify spend", func(t *testing.T) {
		f, s, root, sha := newServer(t)
		// Advance main HEAD past the diff's base — verify would attest a
		// tree nobody lands.
		if err := os.WriteFile(filepath.Join(root, "src", "b.go"), []byte("package src\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, root, "add", ".")
		gitIn(t, root, "commit", "-m", "drift")
		d := f.addDiff(t, "p.diff", patchSrc("README.md", 1, 1, false))
		d.BaseSHA = &sha
		s.autoLand(context.Background(), d, root, "goal", false, "")
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "base_stale" {
			t.Errorf("reasons = %v, want [base_stale]", got)
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
