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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
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
		wantNotes  string // substring across joined annotations; "" = none expected (when wantReason also "")
	}{
		{"modify in existing top dir", patchSrc("src/a.go", 1, 1, false), sha, false, "", "", ""},
		{"top-level file", patchSrc("README.md", 1, 1, false), sha, false, "", "", ""},
		{"new subdir under existing top", patchSrc("src/sub/f.go", 2, 0, true), sha, false, "", "", ""},
		{"protected .odo path", patchSrc(".odo/memory.md", 1, 1, false), sha, false, "protected_path", ".odo/", ""},
		{"protected wiki path", patchSrc("wiki/guide.md", 1, 0, true), sha, false, "protected_path", "wiki/", ""},
		{"protected .odo path uppercase", patchSrc(".ODO/memory.md", 1, 1, false), sha, false, "protected_path", ".ODO/", ""},
		{"protected wiki path mixed case", patchSrc("Wiki/guide.md", 1, 0, true), sha, false, "protected_path", "Wiki/", ""},
		{"lockfile at top level", patchSrc("go.sum", 1, 1, false), sha, false, "supply_chain_path", "go.sum", ""},
		{"nested manifest, case-insensitive", patchSrc("gui/Package-Lock.JSON", 1, 1, false), sha, false, "supply_chain_path", "Package-Lock.JSON", ""},
		{"verify config is self-protected", patchSrc(".odo-verify", 1, 1, false), sha, false, "supply_chain_path", ".odo-verify", ""},
		// 2026-08-20 doctrine: gate source / new top dir / net assertion
		// loss are panel-weighed annotations, never hard blocks.
		{"gate source annotated not blocked", patchSrc("internal/ipc/autoland.go", 1, 1, false), sha, false, "", "", "gate source touched: internal/ipc/autoland.go"},
		{"new top-level directory annotated", patchSrc("newdir/f.go", 2, 0, true), sha, false, "", "", "new top-level directory: newdir/"},
		{"tests weakened annotated", bigTestPatch(1, 2), sha, false, "", "", "test assertions decreased: +1 added / -2 removed"},
		{"tests strengthened pass", bigTestPatch(2, 1), sha, false, "", "", ""},
		{"nil base sha", patchSrc("src/a.go", 1, 1, false), "", false, "base_unresolvable", "", ""},
		{"missing patch file", "", sha, true, "unparseable_diff", "", ""},
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
			reason, detail, annotations := s.autoLandCheck(d)
			if reason != tc.wantReason {
				t.Errorf("reason = %q (detail %q), want %q", reason, detail, tc.wantReason)
			}
			if tc.wantDetail != "" && !strings.Contains(detail, tc.wantDetail) {
				t.Errorf("detail = %q, want substring %q", detail, tc.wantDetail)
			}
			joined := strings.Join(annotations, "\n")
			if tc.wantNotes != "" && !strings.Contains(joined, tc.wantNotes) {
				t.Errorf("annotations = %q, want substring %q", joined, tc.wantNotes)
			}
			if tc.wantNotes == "" && tc.wantReason == "" && len(annotations) != 0 {
				t.Errorf("annotations = %q, want none", joined)
			}
		})
	}
}

func TestVerifyCommands(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".odo-verify"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	want := func(t *testing.T, dir string, paths []string, want ...string) {
		t.Helper()
		cmds, err := verifyCommands(dir, paths)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(cmds, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("cmds = %q, want %q", cmds, want)
		}
	}
	t.Run("missing file is fail-closed", func(t *testing.T) {
		if _, err := verifyCommands(t.TempDir(), nil); err == nil {
			t.Fatal("no .odo-verify: want error")
		}
	})
	t.Run("comments only is fail-closed", func(t *testing.T) {
		dir := write(t, "# nothing\n\n  \n")
		if _, err := verifyCommands(dir, nil); err == nil {
			t.Fatal("comment-only .odo-verify: want error")
		}
	})
	t.Run("first non-comment line wins", func(t *testing.T) {
		dir := write(t, "# comment\n\ngo test ./...\n# later\n")
		want(t, dir, nil, "go test ./...")
	})
	t.Run("glob-only file with unmatched paths is fail-closed", func(t *testing.T) {
		dir := write(t, "gui/**: cd gui && npx tsc --noEmit\n")
		if _, err := verifyCommands(dir, []string{"internal/ipc/x.go"}); err == nil {
			t.Fatal("no fallback and no scope touched: want error")
		}
	})

	// Scope-union selection (panel diff #9 finding 3): mixed diffs run
	// BOTH their touched scope and the fallback — the old all-paths-match
	// rule dropped the gui command for exactly this shape.
	const scoped = "gui/**: cd gui && npx tsc --noEmit\ngo build ./... && go test ./...\n"
	guiCmd := "cd gui && npx tsc --noEmit"
	goCmd := "go build ./... && go test ./..."
	t.Run("pure-gui diff runs the scoped command only", func(t *testing.T) {
		want(t, write(t, scoped), []string{"gui/src/a.ts", "gui/e2e/b.spec.ts"}, guiCmd)
	})
	t.Run("pure-go diff runs the fallback only", func(t *testing.T) {
		want(t, write(t, scoped), []string{"internal/ipc/x.go", "go.mod"}, goCmd)
	})
	t.Run("mixed diff runs scope then fallback", func(t *testing.T) {
		want(t, write(t, scoped), []string{"internal/ipc/server.go", "gui/src/App.tsx"}, guiCmd, goCmd)
	})
	t.Run("no scoped lines always falls back", func(t *testing.T) {
		want(t, write(t, goCmd+"\n"), []string{"gui/src/a.ts"}, goCmd)
	})
	t.Run("nil paths with glob line present falls back", func(t *testing.T) {
		want(t, write(t, scoped), nil, goCmd)
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

// TestRunVerifyGateProvisionsGuiDeps pins the diff-#8 incident (2026-08-19):
// a GUI diff verified in a fresh worktree blocked verify_failed within
// seconds — per-run worktrees carry tracked files only, so gui/node_modules
// was absent and npx found no tsc. The gate now links the project
// checkout's install before choosing/running the command.
func TestRunVerifyGateProvisionsGuiDeps(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (root, wt string) {
		t.Helper()
		root, wt = t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(wt, verifyCmdFile), []byte("echo PASS\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "gui", "node_modules"), 0o755); err != nil {
			t.Fatal(err)
		}
		return root, wt
	}
	t.Run("gui diff symlinks the project install", func(t *testing.T) {
		root, wt := setup(t)
		if gate := runVerifyGate(ctx, root, wt, []string{"gui/src/App.tsx"}); !gate.ok {
			t.Fatalf("gate = %+v", gate)
		}
		fi, err := os.Lstat(filepath.Join(wt, "gui", "node_modules"))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("want gui/node_modules symlink in the worktree, fi=%v err=%v", fi, err)
		}
	})
	t.Run("go-only diff provisions nothing", func(t *testing.T) {
		root, wt := setup(t)
		if gate := runVerifyGate(ctx, root, wt, []string{"internal/ipc/server.go"}); !gate.ok {
			t.Fatalf("gate = %+v", gate)
		}
		if _, err := os.Lstat(filepath.Join(wt, "gui", "node_modules")); !os.IsNotExist(err) {
			t.Fatalf("want no gui/node_modules for a go-only diff, err=%v", err)
		}
	})
	t.Run("existing node_modules is left alone", func(t *testing.T) {
		root, wt := setup(t)
		if err := os.MkdirAll(filepath.Join(wt, "gui", "node_modules", "sentinel"), 0o755); err != nil {
			t.Fatal(err)
		}
		if gate := runVerifyGate(ctx, root, wt, []string{"gui/src/App.tsx"}); !gate.ok {
			t.Fatalf("gate = %+v", gate)
		}
		fi, err := os.Lstat(filepath.Join(wt, "gui", "node_modules"))
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("pre-existing real dir must not be replaced by a symlink, fi=%v err=%v", fi, err)
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

// blockedDetails returns every auto_land_blocked detail journaled for the
// conversation, in order (parallel to blockedReasons).
func blockedDetails(t *testing.T, st *store.Store, convID int64) []string {
	t.Helper()
	events, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, e := range events {
		if e.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("event %d: %v", e.ID, err)
		}
		if p.Action == "auto_land_blocked" {
			details = append(details, p.Detail)
		}
	}
	return details
}

func TestAutoLandBlockedPaths(t *testing.T) {
	// Each case drives s.autoLand to one blocked exit and asserts the
	// journal row. M20: the models arming gate runs FIRST — every gate
	// case must isolate HOME and arm the panel (a review: line parses
	// without network; these cases never reach the fan-out). The one
	// no-prefs case pins the OPPOSITE contract: unarmed = silent.
	newServer := func(t *testing.T) (autonomyFixture, *Server, string, string) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writePrefs(t, home, "review: rm1@test, rm2@test\nauto_apply: main\n")
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

	t.Run("no review models is silent (M20 unarmed)", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir()) // no prefs.md → default-on but UNARMED (no review: line)
		f := newAutonomyFixture(t)
		root, sha := autolandRepo(t)
		// Even a verify config and a perfectly-landable diff must not
		// make a sound: the arming gate precedes every gate and row.
		if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo PASS\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := &Server{store: f.st, projectRoot: root}
		d := f.addDiff(t, "p.diff", patchSrc("README.md", 1, 1, false))
		d.BaseSHA = &sha
		s.autoLand(context.Background(), d, root, "goal", false, "")
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 0 {
			t.Errorf("reasons = %v, want NONE (unarmed pipelines journal nothing)", got)
		}
		got, err := f.st.GetDiff(context.Background(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.DiffPending {
			t.Errorf("diff status = %q, want pending (unarmed never resolves)", got.Status)
		}
	})
}

// TestAutoLandSingleJudgeUnarmed (P1 #8): a review: line with ONE model
// leaves the pipeline UNARMED — N=1 "unanimity" is a single judge with no
// dissent channel. The FIRST attempt per daemon lifetime journals exactly
// one single_judge_panel advisory row, zero panel calls fire, and later
// diffs pend silent; advisory surfaces (/panel, review_diff) stay
// N-unrestricted, proven by the review_diff leg below.
func TestAutoLandSingleJudgeUnarmed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test\nauto_apply: main\n")
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	root, sha := autolandRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: f.st, projectRoot: root}

	diffIDs := make([]int64, 0, 2)
	for _, name := range []string{"a.diff", "b.diff"} {
		d := f.addDiff(t, name, patchSrc("src/a.go", 1, 1, false))
		d.BaseSHA = &sha
		s.autoLand(context.Background(), d, root, "goal", false, "")
		diffIDs = append(diffIDs, d.ID)
	}
	// Exactly ONE advisory across both attempts; zero panel spend.
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "single_judge_panel" {
		t.Fatalf("blocked reasons = %v, want exactly [single_judge_panel] (one advisory per daemon lifetime)", got)
	}
	if n := atomic.LoadInt64(calls); n != 0 {
		t.Errorf("panel calls = %d, want 0 (the unarmed pipeline spends nothing)", n)
	}
	if got := blockedDetails(t, f.st, f.c.ID); len(got) != 1 || !strings.Contains(got[0], "single model") {
		t.Errorf("advisory details = %v, want one row naming the single-model cause", got)
	}
	for _, id := range diffIDs {
		got, err := f.st.GetDiff(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.DiffPending {
			t.Errorf("diff %d status = %q, want pending (unarmed never resolves)", id, got.Status)
		}
	}

	// Advisory carve-out: review_diff consults the single model normally —
	// it only advises, it never lands.
	resp, err := s.handleReviewDiff(context.Background(), Request{DiffID: diffIDs[0]})
	if err != nil {
		t.Fatalf("review_diff on a 1-model config: %v (advisory surfaces are N-unrestricted)", err)
	}
	if len(resp.Reviews) != 1 || resp.Reviews[0].Verdict != "accept" {
		t.Errorf("advisory reviews = %+v, want the single model's accept", resp.Reviews)
	}
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Errorf("panel calls after review_diff = %d, want 1 (only the advisory consult ran)", n)
	}
}

// TestReviewLegOuterDeadline (P1 #9): a hung review leg dies at the
// leg's outer deadline as an Infra result (needs_fixes — never an
// accidental accept) instead of wedging the auto-land pipeline on a dead
// gateway (autoLand's Background ctx carries no deadline of its own).
func TestReviewLegOuterDeadline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bounded hang: the client's leg deadline (50ms) must fire long
		// before this 2s answer; the bound keeps srv.Close deterministic.
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "too-late-answer"}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	s := &Server{legTimeoutForTest: 50 * time.Millisecond}
	start := time.Now()
	rr := s.reviewWithModel(context.Background(), reviewModel{model: "rm1", provider: "test"}, "review this diff")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("review leg held %v — the leg deadline must fire", elapsed)
	}
	if !rr.Infra || rr.Verdict != "needs_fixes" || !strings.Contains(rr.Comments, "review failed") {
		t.Errorf("result = %+v, want an Infra needs_fixes review-failed leg", rr)
	}
}

// TestMaybeAutoLandPrefOffSilent (M20): the off kill switch leaves NO
// journal trace even when the panel is fully armed — a disabled feature
// deserves no noise (blocked attempts are the evidence, silence is the
// off state). Companion contract: an armed pref with the pipeline
// default-on (no auto_apply line at all) DOES run.
func TestMaybeAutoLandPrefOffSilent(t *testing.T) {
	t.Run("explicit off silences an armed pipeline", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writePrefs(t, home, "review: rm1@test\nauto_apply: off\n")
		f := newAutonomyFixture(t)
		s := &Server{store: f.st, projectRoot: f.dir}
		d := f.addDiff(t, "p.diff", patchSrc("src/a.go", 1, 1, false))
		s.maybeAutoLand(d, f.dir, "goal", false, "")
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 0 {
			t.Errorf("reasons = %v, want none (kill switch must be silent)", got)
		}
	})

	t.Run("absent auto_apply arms (M20 default-on)", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		// review: line but NO auto_apply line — the M20 default must arm
		// the pipeline; reaching the verify gate proves it (blocked row).
		writePrefs(t, home, "review: rm1@test, rm2@test\n")
		f := newAutonomyFixture(t)
		root, sha := autolandRepo(t)
		s := &Server{store: f.st, projectRoot: root}
		d := f.addDiff(t, "p.diff", patchSrc("src/a.go", 1, 1, false))
		d.BaseSHA = &sha
		s.maybeAutoLand(d, root, "goal", false, "")
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "verify_unconfigured" {
			t.Errorf("reasons = %v, want [verify_unconfigured] (absent pref must arm)", got)
		}
	})
}

// TestAutoLandVerifyNoEvidence (B4): an exit-0 verify whose output tail
// shows zero test evidence blocks before ANY panel spend — and, flipped
// on, pass evidence lets the same config proceed THROUGH the panel to a
// verdict (M20: the panel is armed by stub; the M16 no_review_models
// progress-proof is gone with the arming gate).
func TestAutoLandVerifyNoEvidence(t *testing.T) {
	arm := func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writePrefs(t, home, "review: rm1@test, rm2@test\n")
	}
	t.Run("zero evidence blocks", func(t *testing.T) {
		arm(t)
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
		// Run/verify log: the blocked detail carries the verify output
		// tail (previously message-only) — the fixture echoes build-only-done.
		if got := blockedDetails(t, f.st, f.c.ID); len(got) != 1 || !strings.Contains(got[0], "build-only-done") {
			t.Errorf("details = %v, want one row containing the verify tail (build-only-done)", got)
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
		arm(t)
		startPanelStub(t, func(call int64, model string) (int, string) {
			return 200, "ACCEPT\nlooks correct"
		})
		f := newAutonomyFixture(t)
		root, _ := autolandRepo(t)
		if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("printf 'ok  \\tpkg/mod\\t0.1s\\n'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := &Server{store: f.st, projectRoot: root}
		d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
			if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // landed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}))
		s.autoLand(context.Background(), d, root, "goal", false, "")
		// An ok line satisfies the evidence gate → panel consulted →
		// unanimous accept lands (the live proof the gate passed).
		got, err := f.st.GetDiff(context.Background(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.DiffAccepted {
			t.Errorf("diff status = %q, want accepted (verify evidence gate passed to a landed panel)", got.Status)
		}
		if got := blockedReasons(t, f.st, f.c.ID); len(got) != 0 {
			t.Errorf("reasons = %v, want none on a landed pipeline", got)
		}
		content, err := os.ReadFile(filepath.Join(root, "src", "a.go"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "// landed") {
			t.Errorf("landed tree content = %q, want the patch's post-image", content)
		}
	})
}

// visualAutolandRepo commits gui/src/ + the verify config so the
// new-top-dir and base-freshness gates pass for a gui diff.
func visualAutolandRepo(t *testing.T) (root, sha string) {
	t.Helper()
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

// TestAutoLandVisualDiffUnanimousAcceptLands (visual-gate removal): a diff
// touching gui/src/** with a unanimous ACCEPT panel lands through the same
// pipeline as any daemon diff — one accept row, the diff DiffAccepted, zero
// blocked rows, and no human_gate_visual anywhere in the journal.
func TestAutoLandVisualDiffUnanimousAcceptLands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	root, _ := visualAutolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	// realPatch, not patchSrc: the accept path's git apply --3way
	// adjudicates real content against the committed gui/src/app.ts.
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "gui", "src", "app.ts"), []byte("export const x = 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))

	s.autoLand(context.Background(), d, root, "goal", false, "")
	if n := atomic.LoadInt64(calls); n != 2 {
		t.Fatalf("panel calls = %d, want 2 (one per model leg)", n)
	}
	sc := scanSettle(t, f.st, f.c.ID)
	if got := sc.blockedReasons(); len(got) != 0 {
		t.Fatalf("blocked reasons = %v, want none — visual diffs land on a unanimous accept", got)
	}
	for _, p := range sc.reviewSeq {
		if fmt.Sprint(p["reason"]) == "human_gate_visual" {
			t.Fatalf("human_gate_visual journaled %v — the visual gate is removed", p)
		}
	}
	if len(sc.moaRows) != 1 || sc.moaRows[0]["consensus_verdict"] != "accept" {
		t.Errorf("moa_review rows = %v, want one unanimous-accept evidence row", sc.moaRows)
	}
	// Run/verify log (tri-model right sidebar gap): the landed moa_review
	// journals the verify that attested it — command + capped output tail.
	// The fixture's .odo-verify is `echo PASS`, so the tail is "PASS\n".
	if sc.moaRows[0]["verify_cmd"] != "echo PASS" {
		t.Errorf("moa_review verify_cmd = %v, want 'echo PASS'", sc.moaRows[0]["verify_cmd"])
	}
	if !strings.Contains(fmt.Sprint(sc.moaRows[0]["verify_tail"]), "PASS") {
		t.Errorf("moa_review verify_tail = %v, want the verify output containing PASS", sc.moaRows[0]["verify_tail"])
	}
	if len(sc.accepts) != 1 || sc.accepts[0]["actor"] != autoActor || sc.accepts[0]["diff_id"] != float64(d.ID) {
		t.Errorf("accepts = %v, want exactly one auto_panel accept of diff %d", sc.accepts, d.ID)
	}
	if len(sc.rounds) != 0 || len(sc.markers) != 0 {
		t.Errorf("ladder fired on a unanimous accept: rounds=%v markers=%v", sc.rounds, sc.markers)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffAccepted {
		t.Errorf("diff status = %q, want accepted", got.Status)
	}
	if data, rerr := os.ReadFile(filepath.Join(root, "gui", "src", "app.ts")); rerr != nil || string(data) != "export const x = 2\n" {
		t.Errorf("gui/src/app.ts = %q, %v — the visual diff must land in main", data, rerr)
	}
}

// TestAutoLandStartedRowsPinStageBoundaries (indicator-lock Phase 2): the
// happy path journals one auto_land_started breadcrumb per silent stage —
// "verify" before the gate, "panel" before the fan-out — ordered strictly
// verify < panel < moa_review < accept, so the GUI chip never labels a
// minutes-long running stage "queued". Breadcrumbs carry the judged bytes'
// patch_sha16 (audit identity, same as every outcome row) and NEVER a risk
// receipt (nothing resolved — a rated liveness row would corrupt the
// autonomy audit's Unrated bucket).
func TestAutoLandStartedRowsPinStageBoundaries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	root, _ := visualAutolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "gui", "src", "app.ts"), []byte("export const x = 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))

	s.autoLand(context.Background(), d, root, "goal", false, "")

	sc := scanSettle(t, f.st, f.c.ID)
	var order []string
	var stages []string
	for _, p := range sc.reviewSeq {
		action := fmt.Sprint(p["action"])
		order = append(order, action)
		if action != "auto_land_started" {
			continue
		}
		stages = append(stages, fmt.Sprint(p["stage"]))
		if p["actor"] != autoActor {
			t.Errorf("started row actor = %v, want %q", p["actor"], autoActor)
		}
		if diffBytes, rerr := os.ReadFile(d.PathOnDisk); rerr != nil {
			t.Fatal(rerr)
		} else if p["patch_sha16"] != sha16(diffBytes) {
			t.Errorf("started row patch_sha16 = %v, want sha16 of the judged diff bytes", p["patch_sha16"])
		}
		if _, rated := p["risk_class"]; rated {
			t.Errorf("started row %v carries a risk receipt — liveness breadcrumbs never rate", p)
		}
	}
	want := []string{"auto_land_started", "auto_land_started", "moa_review", "accept"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("journal actions = %v, want %v (one breadcrumb per silent stage, then verdict, then land)", order, want)
	}
	if fmt.Sprint(stages) != fmt.Sprint([]string{"verify", "panel"}) {
		t.Errorf("started stages = %v, want [verify panel]", stages)
	}
}

// TestAutoLandStartedRowsAbsentBeforeSpend: entry/mechanical gates that
// block before the first silent stage (here: the supply-chain gate) leave
// ZERO started rows — the chip's honest transition is queued → blocked,
// never queued → running → blocked for a pipeline that spent nothing.
func TestAutoLandStartedRowsAbsentBeforeSpend(t *testing.T) {
	// M20 arming gate runs FIRST (file convention, L352): without an
	// isolated+armed HOME the case depends on ambient prefs — green on a
	// configured dev box, zero blocked rows under verify's scratch HOME.
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	f := newAutonomyFixture(t)
	root, sha := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := f.addDiff(t, "p.diff", patchSrc("go.mod", 1, 1, false))
	d.BaseSHA = &sha

	s.autoLand(context.Background(), d, root, "goal", false, "")

	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "supply_chain_path" {
		t.Fatalf("blocked reasons = %v, want [supply_chain_path]", got)
	}
	for _, p := range scanSettle(t, f.st, f.c.ID).reviewSeq {
		if p["action"] == "auto_land_started" {
			t.Errorf("started row %v journaled for a pipeline blocked before any spend", p)
		}
	}
}

// TestAutoLandVisualDiffNeedsFixesEntersLadder (visual-gate removal): a
// GUI diff judged needs_fixes takes the same revise ladder a daemon diff
// would — one round-1 marker + round row spawn, zero blocked rows.
func TestAutoLandVisualDiffNeedsFixesEntersLadder(t *testing.T) {
	root := settleRigRepo(t)
	// A tracked GUI file so the stub run's diff touches gui/src/** and the
	// new-top-dir gate passes (the base tree carries gui/).
	if err := os.MkdirAll(filepath.Join(root, "gui", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gui", "src", "app.ts"), []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "gui fixture")
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test, rm3@test\nauto_apply: main\n")
	startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "NEEDS_FIXES\nthe accent token should come from the theme"
	})
	// guiStubWrapper modifies a tracked GUI file: the run's diff touches
	// gui/src/**, exercising the post-removal pipeline stance on visual work.
	const guiStubWrapper = `#!/bin/sh
output_file="$3"
sleep 1
printf 'export const x = 99\n' > gui/src/app.ts
printf 'updated the GUI accent color\n' > "$output_file"
exit 0
`
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, guiStubWrapper))
	rig := startRig(t, root)
	t.Cleanup(func() { rig.stop(t) })

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: rig.root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "update the GUI accent color"})
	done := pollDone(t, rig, convID)
	if done.Diff == nil {
		t.Fatal("the GUI run produced no diff")
	}
	d0 := done.Diff.ID

	// The needs_fixes GUI diff spawns revise round 1 exactly like a daemon
	// diff would; the repair run is never polled (its drain would evaluate
	// round 2's panel and muddy the round-1 assertions).
	sc := waitSettle(t, rig.store, convID, "revise round 1 for the GUI diff", func(sc settleScan) bool {
		return len(sc.markers) == 1 && len(sc.rounds) == 1
	})
	if got := sc.blockedReasons(); len(got) != 0 {
		t.Fatalf("blocked reasons = %v, want none — a needs_fixes GUI diff enters the ladder", got)
	}
	if r := sc.rounds[0]; r["round"] != float64(1) || r["diff_id"] != float64(d0) {
		t.Errorf("round row = %v, want round:1 on the GUI diff %d", r, d0)
	}
	for _, p := range sc.reviewSeq {
		if fmt.Sprint(p["reason"]) == "human_gate_visual" {
			t.Fatalf("human_gate_visual journaled %v — the visual gate is removed", p)
		}
	}
	got, err := rig.store.GetDiff(context.Background(), d0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending (needs_fixes never lands)", got.Status)
	}
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
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
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
	// the land no longer applies to without a rebase. (First leg only:
	// the drifted commit can't repeat under a 2-model panel.)
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		if call == 1 {
			if werr := os.WriteFile(filepath.Join(root, "drift.txt"), []byte("racing human commit\n"), 0o644); werr != nil {
				t.Errorf("drift write: %v", werr)
			}
			for _, args := range [][]string{{"add", "drift.txt"}, {"commit", "-m", "drift"}} {
				argv := append([]string{"-C", root, "-c", "user.email=odo@test", "-c", "user.name=odo"}, args...)
				if out, gerr := exec.Command("git", argv...).CombinedOutput(); gerr != nil {
					t.Errorf("drift %v: %v: %s", args, gerr, out)
				}
			}
		}
		return 200, "ACCEPT\nlooks correct"
	})

	s.autoLand(context.Background(), d, root, "goal", false, "")
	if n := atomic.LoadInt64(calls); n != 2 {
		t.Fatalf("panel calls = %d, want 2 (one per model leg)", n)
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

	_, err := s.handleDiffAction(context.Background(), d.ID, "accept", autoActor, "")
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
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
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
	if n := atomic.LoadInt64(calls); n != 2 {
		t.Errorf("panel calls = %d, want 2 — one per model leg (a clean probe proceeds to the panel)", n)
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

// TestAutoLandPreSpendRefreshConflict (P0a → M20): entry drift that
// OVERLAPS the diff — the pre-spend probe's 3-way merge conflicts in its
// throwaway worktree. M20 rewires the old base_stale block into the settle
// ladder: refresh_attempted{conflict, pre_spend_probe} journals first,
// then the ladder's read-decide — failing closed at revise_ambiguous
// because this fixture conversation has no origin goal — spawns nothing,
// consults no panel, runs no verify, and leaves the diff pending with its
// base unmoved. Main was never touched by the probe.
func TestAutoLandPreSpendRefreshConflict(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n") // M20: the arming gate precedes every gate
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
	sc := scanSettle(t, f.st, f.c.ID)
	if got := sc.blockedReasons(); len(got) != 1 || got[0] != "revise_ambiguous" {
		t.Fatalf("reasons = %v, want [revise_ambiguous] (entry conflict now enters the ladder, not a base_stale block)", got)
	}
	if n := atomic.LoadInt64(calls); n != 0 {
		t.Errorf("panel calls = %d, want 0 (the entry probe precedes all spend)", n)
	}
	// refresh_attempted{conflict, pre_spend_probe} immediately precedes the
	// blocked row (journal-first, hard rule 6), and the verify/panel
	// breadcrumbs never fired.
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

// TestAutoLandFinalRefreshConflict (P0a → M20): the entry probe passed
// (fresh base), verify + the panel ran — and THEN a racing commit drifted
// main ONTO the diff's own path. The final gate's rebase conflicts: main
// rolls back, one refresh_attempted{conflict, accept_apply} row rides
// between the moa_review and the base_stale_at_land blocked row (which
// carries the completed panel as advisory evidence), and M20 hands the
// diff to the settle ladder — here failing closed at revise_ambiguous
// (no origin goal in the fixture conversation), the diff still pending.
func TestAutoLandFinalRefreshConflict(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
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
	// (First leg only: the drifted commit can't repeat under a 2-model
	// panel.)
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		if call == 1 {
			if werr := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // racing human edit\n"), 0o644); werr != nil {
				t.Errorf("drift write: %v", werr)
			}
			for _, args := range [][]string{{"add", "src/a.go"}, {"commit", "-m", "racing edit"}} {
				argv := append([]string{"-C", root, "-c", "user.email=odo@test", "-c", "user.name=odo"}, args...)
				if out, gerr := exec.Command("git", argv...).CombinedOutput(); gerr != nil {
					t.Errorf("drift %v: %v: %s", args, gerr, out)
				}
			}
		}
		return 200, "ACCEPT\nlooks correct"
	})

	s.autoLand(context.Background(), d, root, "goal", false, "")
	if n := atomic.LoadInt64(calls); n != 2 {
		t.Fatalf("panel calls = %d, want 2 — one per model leg (the panel RAN — the block rides its evidence)", n)
	}
	sc := scanSettle(t, f.st, f.c.ID)
	// M20: the blocked evidence row now rides INTO the settle ladder —
	// this fixture conversation has no human user_message (no origin
	// goal), so the ladder fails closed at revise_ambiguous instead of
	// spawning. The old contract's run ends at base_stale_at_land.
	if got := sc.blockedReasons(); len(got) != 2 || got[0] != "base_stale_at_land" || got[1] != "revise_ambiguous" {
		t.Fatalf("blocked reasons = %v, want [base_stale_at_land revise_ambiguous]", got)
	}
	row := sc.blocked[0]
	if row["consensus_verdict"] != "accept" {
		t.Errorf("consensus_verdict = %v, want accept riding as advisory evidence", row["consensus_verdict"])
	}
	reviews, _ := row["reviews"].([]interface{})
	if len(reviews) != 2 {
		t.Errorf("reviews = %v, want the full 2-model panel attached", row["reviews"])
	}
	if detail, _ := row["detail"].(string); !strings.Contains(detail, "the verify and panel attested the pre-drift tree") {
		t.Errorf("detail = %q, want the pre-drift attestation advisory", detail)
	}
	if detail, _ := row["detail"].(string); !strings.Contains(detail, "regenerating on current HEAD") {
		t.Errorf("detail = %q, want the M20 regeneration note", detail)
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

// TestAutoLandAlreadyLandedFastPath (M20 reconcile): the diff's content
// reached main through a side path (a manual commit of identical content)
// while the diff sat pending. Entry detection short-circuits everything —
// NO verify run, NO panel spend, NO base probe — and the final gate's own
// roundtrip ledgers the no-op land: diff accepted, accept row carries
// already_landed:true with actor auto_panel, base pointer rides to HEAD,
// refresh_attempted{already_landed} rows mark both gates, and NO new
// commit is created (the content was already committed — an empty
// path-scoped commit would be a git error and ledger noise).
func TestAutoLandAlreadyLandedFastPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nshould never be consulted"
	})
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // landed out-of-band\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	// The side-channel landing: identical content, committed by hand.
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // landed out-of-band\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "src/a.go")
	gitIn(t, root, "commit", "-m", "manual merge of the same change")
	landedHEAD := gitOut(t, root, "rev-parse", "HEAD")

	s.autoLand(context.Background(), d, root, "goal", false, "")
	if n := atomic.LoadInt64(calls); n != 0 {
		t.Errorf("panel calls = %d, want 0 (bookkeeping spends nothing)", n)
	}
	sc := scanSettle(t, f.st, f.c.ID)
	if got := sc.blockedReasons(); len(got) != 0 {
		t.Fatalf("blocked reasons = %v, want none on a roundtripted land", got)
	}
	if len(sc.accepts) != 1 || sc.accepts[0]["diff_id"] != float64(d.ID) || sc.accepts[0]["actor"] != autoActor {
		t.Fatalf("accepts = %v, want one auto_panel accept of diff %d", sc.accepts, d.ID)
	}
	if sc.accepts[0]["already_landed"] != true {
		t.Errorf("accept row already_landed = %v, want true", sc.accepts[0]["already_landed"])
	}
	// Gate breadcrumbs: entry roundtrip then the final gate's own roundtrip,
	// newest order, and NO started/moa rows (nothing attested by verify or panel).
	var outcomes []string
	for _, p := range sc.reviewSeq {
		switch p["action"] {
		case "auto_land_started", "moa_review":
			t.Errorf("spend row %v on a reconcile land — verify/panel must never run", p)
		case "refresh_attempted":
			outcomes = append(outcomes, fmt.Sprintf("%v/%v", p["outcome"], p["phase"]))
		}
	}
	if len(outcomes) != 2 || outcomes[0] != "already_landed/pre_spend_probe" || outcomes[1] != "already_landed/accept_apply" {
		t.Errorf("refresh rows = %v, want [already_landed/pre_spend_probe, already_landed/accept_apply]", outcomes)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffAccepted {
		t.Errorf("diff status = %q, want accepted", got.Status)
	}
	if got.BaseSHA == nil || *got.BaseSHA != landedHEAD {
		t.Errorf("store base_sha = %v, want %s (the pointer rode to HEAD)", got.BaseSHA, landedHEAD)
	}
	// No bookkeeping commit: HEAD is exactly the manual landing commit.
	if head := gitOut(t, root, "rev-parse", "HEAD"); head != landedHEAD {
		t.Errorf("HEAD = %s, want %s (an already-committed landing earns no empty commit)", head, landedHEAD)
	}
	if status := gitOut(t, root, "status", "--porcelain"); status != "" {
		t.Errorf("main status = %q, want clean", status)
	}
}

// TestAcceptAlreadyLandedFreshBase (M20 rescue): head == base, but the
// post-image sits in main UNCOMMITTED (an identical out-of-band edit) —
// the forward apply fails yet this is no conflict. The fresh-base
// roundtrip fires BEFORE the apply attempt (the I7 rollback would
// otherwise restore HEAD bytes and destroy the edit), the accept commits
// the content as the accept commit, and the tree ends clean.
func TestAcceptAlreadyLandedFreshBase(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	patch := realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // identical out-of-band edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	d := baseBoundDiff(t, f, root, "p.diff", patch)
	headBefore := gitOut(t, root, "rev-parse", "HEAD")
	// The out-of-band identical edit, left uncommitted.
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // identical out-of-band edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.handleDiffAction(context.Background(), d.ID, "accept", autoActor, ""); err != nil {
		t.Fatalf("accept: %v", err)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffAccepted {
		t.Errorf("diff status = %q, want accepted", got.Status)
	}
	row := resolutionRow(t, f, d, "accept")
	if row["already_landed"] != true {
		t.Errorf("accept row already_landed = %v, want true", row["already_landed"])
	}
	if row["refreshed_from_sha"] != nil {
		t.Errorf("accept row refreshed_from_sha = %v, want absent (no base refresh happened)", row["refreshed_from_sha"])
	}
	// The uncommitted post-image content was recorded as the accept commit.
	if head := gitOut(t, root, "rev-parse", "HEAD"); head == headBefore {
		t.Error("HEAD did not advance — the uncommitted post-image must be committed")
	}
	if msg := gitOut(t, root, "log", "-1", "--format=%s"); !strings.Contains(msg, "odo: accept diff #") {
		t.Errorf("HEAD subject = %q, want the accept commit", msg)
	}
	if data, rerr := os.ReadFile(filepath.Join(root, "src", "a.go")); rerr != nil || string(data) != "package src // identical out-of-band edit\n" {
		t.Errorf("src/a.go = %q, %v — the identical edit survives, never rolled back", data, rerr)
	}
	if status := gitOut(t, root, "status", "--porcelain"); status != "" {
		t.Errorf("main status = %q, want clean (the accept commit swept the patch paths)", status)
	}
}

// TestAutoLandParallelPipelines (P2): with autoLandMu gone, two pipelines
// run concurrently — the panel stub's in-flight counter proves both legs
// are live at once (serialization would cap it at 1). Both panels accept;
// the acceptMu race resolves at land: the winner applies on a fresh base,
// the loser re-adjudicates the moved HEAD at the final gate and lands via
// a clean accept_apply refresh. Two accept rows, exactly one refresh row,
// zero blocked, and no double-apply interleave.
func TestAutoLandParallelPipelines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\nauto_apply: main\n")
	// Rendezvous: a leg waits until BOTH pipelines' legs have arrived
	// (2 models × 2 pipelines = 4 legs). A bare counter latch lets the
	// last arriver miss the peak (an early leg decrements before the
	// last's first load) and pins the wait a whole deadline — a channel
	// closed at 4 releases all deterministically.
	// The 30s safety net releases a permanently serialized pipeline so a
	// regression (autoLandMu restored) fails rather than hangs.
	var inFlight, maxFlight int64
	both := make(chan struct{})
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		cur := atomic.AddInt64(&inFlight, 1)
		for {
			old := atomic.LoadInt64(&maxFlight)
			if cur <= old || atomic.CompareAndSwapInt64(&maxFlight, old, cur) {
				break
			}
		}
		if cur == 4 {
			close(both)
		} else {
			select {
			case <-both:
			case <-time.After(30 * time.Second):
			}
		}
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: f.st, projectRoot: root}
	preHead := gitOut(t, root, "rev-parse", "HEAD")
	// Disjoint real patches: the loser's rebase against the moved HEAD
	// must merge cleanly.
	d1 := baseBoundDiff(t, f, root, "p1.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "first.go"), []byte("package src // first\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	d2 := baseBoundDiff(t, f, root, "p2.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "second.go"), []byte("package src // second\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.maybeAutoLand(d1, root, "goal", false, "") }()
	go func() { defer wg.Done(); s.maybeAutoLand(d2, root, "goal", false, "") }()
	wg.Wait()

	if n := atomic.LoadInt64(calls); n != 4 {
		t.Fatalf("panel calls = %d, want 4 (two legs per pipeline)", n)
	}
	if got := atomic.LoadInt64(&maxFlight); got != 4 {
		t.Fatalf("max in-flight panel legs = %d, want 4 — both pipelines' legs must overlap (autoLandMu is gone)", got)
	}
	sc := scanSettle(t, f.st, f.c.ID)
	if got := sc.blockedReasons(); len(got) != 0 {
		t.Fatalf("blocked reasons = %v, want none — both lands adjudicate cleanly", got)
	}
	if len(sc.moaRows) != 2 {
		t.Errorf("moa_review rows = %d, want 2 (evidence before action, one per pipeline)", len(sc.moaRows))
	}
	if len(sc.accepts) != 2 {
		t.Fatalf("accept rows = %d, want 2 (both pipelines land)", len(sc.accepts))
	}
	// Exactly one refresh_attempted{clean, accept_apply}: the winner saw
	// a fresh base; the loser refreshed onto the moved HEAD.
	var refreshes []map[string]interface{}
	for _, p := range sc.reviewSeq {
		if p["action"] == "refresh_attempted" {
			refreshes = append(refreshes, p)
		}
	}
	if len(refreshes) != 1 || refreshes[0]["outcome"] != "clean" || refreshes[0]["phase"] != "accept_apply" {
		t.Fatalf("refresh rows = %v, want exactly one {clean, accept_apply}", refreshes)
	}
	if refreshes[0]["base_sha"] != preHead {
		t.Errorf("refresh base_sha = %v, want the shared pre-pipeline HEAD %s", refreshes[0]["base_sha"], preHead)
	}
	// The refreshed accept names the panel-judged base; the winner's row
	// carries no refresh marker. Both rows attest the same actor.
	var withRefresh int
	for _, a := range sc.accepts {
		if a["actor"] != autoActor {
			t.Errorf("accept actor = %v, want %s", a["actor"], autoActor)
		}
		if rs, ok := a["refreshed_from_sha"]; ok {
			withRefresh++
			if rs != preHead {
				t.Errorf("refreshed_from_sha = %v, want %s", rs, preHead)
			}
		}
	}
	if withRefresh != 1 {
		t.Errorf("accepts carrying refreshed_from_sha = %d, want exactly 1 (the acceptMu loser)", withRefresh)
	}
	for _, d := range []store.Diff{d1, d2} {
		got, err := f.st.GetDiff(context.Background(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.DiffAccepted {
			t.Errorf("diff %d status = %q, want accepted", d.ID, got.Status)
		}
	}
	for _, rel := range []string{"src/first.go", "src/second.go"} {
		if _, serr := os.Stat(filepath.Join(root, rel)); serr != nil {
			t.Errorf("%s missing after both lands: %v", rel, serr)
		}
	}
}

// TestAutoLandLadderNoFork (P2): two needs_fixes diffs from the SAME
// conversation race the revise ladder. ladderMu serializes the whole
// read-decide-spawn: the winner spawns round 1; the loser re-reads the
// chain AFTER the round row exists and stops — revise_no_progress
// (identical patch bytes) when its diff id is higher, revise_ambiguous
// when the winner's is (the lineage id-order guard). Exactly one marker,
// one round row, one blocked row — forked chains journal two round-1 rows.
func TestAutoLandLadderNoFork(t *testing.T) {
	rig := settleRig(t, func(call int64, model string) (int, string) {
		return 200, "NEEDS_FIXES\ntighten the loop"
	})
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: rig.root})
	convID := boot.Conversation.ID
	// A human ask grounds the chain's origin goal without running a send —
	// a send's own drain would evaluate its diff first and pollute the
	// rounds ledger before the fork scenario is staged.
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventUserMessage, mustJSON(map[string]interface{}{
		"text": "the original instruction",
	})); err != nil {
		t.Fatal(err)
	}
	head := gitOut(t, rig.root, "rev-parse", "HEAD")
	dir := t.TempDir()
	// Identical patch bytes in both diffs: the loser hits the no-progress
	// stop on patch_sha16 (unless the id-order lineage guard fires first).
	patch := patchSrc("src/a.go", 1, 1, false)
	var diffs [2]store.Diff
	for i, name := range []string{"p1.diff", "p2.diff"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
			t.Fatal(err)
		}
		d, err := rig.store.InsertDiff(context.Background(), convID, path, head, "", "")
		if err != nil {
			t.Fatal(err)
		}
		diffs[i] = d
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for _, d := range diffs {
		go func(d store.Diff) { defer wg.Done(); rig.server.maybeAutoLand(d, rig.root, "goal", false, "") }(d)
	}
	wg.Wait()

	sc := scanSettle(t, rig.store, convID)
	if len(sc.markers) != 1 || len(sc.rounds) != 1 {
		t.Fatalf("markers=%d rounds=%d — the rounds chain forked; ladderMu must admit exactly ONE round-1 spawn", len(sc.markers), len(sc.rounds))
	}
	if sc.rounds[0]["round"] != float64(1) {
		t.Errorf("round row = %v, want round:1", sc.rounds[0])
	}
	if got := sc.blockedReasons(); len(got) != 1 {
		t.Fatalf("blocked reasons = %v, want exactly one (the loser pipeline)", got)
	}
	switch got := fmt.Sprint(sc.blocked[0]["reason"]); got {
	case "revise_no_progress", "revise_ambiguous":
	default:
		t.Errorf("loser blocked reason = %q, want revise_no_progress (identical patch) or revise_ambiguous (lineage id guard) — a spawn failure means the loser SAW an empty chain", got)
	}
	for _, d := range diffs {
		got, err := rig.store.GetDiff(context.Background(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.DiffPending {
			t.Errorf("diff %d status = %q, want pending (a needs_fixes evaluation never lands)", d.ID, got.Status)
		}
	}
}

// -------------------------------------------------- restart recovery dedup

// TestPipelineTerminalDiffIDs pins the recovery dedup classes: blocked
// (≠panel_infra), pipeline moa_review, and revise-round rows retire a
// diff; breadcrumbs, panel_infra, human rows, and foreign diff ids never
// do.
func TestPipelineTerminalDiffIDs(t *testing.T) {
	ev := func(payload string) store.Event {
		return store.Event{Type: store.EventReviewAction, Payload: json.RawMessage(payload)}
	}
	events := []store.Event{
		// Terminal — the pipeline's own outcomes.
		ev(`{"action":"auto_land_blocked","actor":"auto_panel","reason":"panel_mixed","diff_id":1}`),
		ev(`{"action":"auto_land_blocked","actor":"auto_loop","reason":"loop_verify_failed","diff_id":2}`), // loop pipeline: same terminal class
		ev(`{"action":"moa_review","actor":"auto_panel","diff_id":3,"consensus_verdict":"accept"}`),
		ev(`{"action":"auto_revise_round","actor":"auto_panel","round":1,"diff_id":4,"origin_diff_id":4}`),
		ev(`{"action":"auto_revise_round","actor":"human","round":1,"diff_id":5}`), // human revise: the chain owns the diff
		// NOT terminal.
		ev(`{"action":"auto_land_blocked","actor":"auto_panel","reason":"panel_infra","diff_id":11}`), // not a verdict — retry is the design
		ev(`{"action":"auto_land_started","actor":"auto_panel","stage":"panel","diff_id":12}`),        // breadcrumb: restart mid-pipeline
		ev(`{"action":"refresh_attempted","actor":"auto_panel","diff_id":13,"outcome":"conflict"}`),   // breadcrumb
		ev(`{"action":"moa_review","diff_id":14,"consensus_verdict":"reject"}`),                       // human panel: no actor, pipeline never ran
		ev(`{"action":"accept","actor":"auto_panel","diff_id":15}`),                                   // crash-mid-bookkeeping edge: not a judgment source
		ev(`{"action":"reject","diff_id":16}`),                                                        // human verdict rows can't coexist with a pending diff
		ev(`{"action":"auto_land_blocked","reason":"panel_mixed","diff_id":0}`),                       // no diff id: names nothing
		ev(`garbage`),
		{Type: store.EventUserMessage, Payload: json.RawMessage(`{"text":"unrelated"}`)},
	}
	terminal := pipelineTerminalDiffIDs(events)
	for _, id := range []int64{1, 2, 3, 4, 5} {
		if !terminal[id] {
			t.Errorf("diff %d not terminal, want terminal", id)
		}
	}
	for _, id := range []int64{11, 12, 13, 14, 15, 16} {
		if terminal[id] {
			t.Errorf("diff %d terminal, want stranded", id)
		}
	}
}

// TestStrandedPendingDiffs runs the recovery filter against a real store:
// two workstreams, seven pending diffs — each diff with a journaled
// pipeline outcome drops out; order and cross-conversation isolation
// survive.
func TestStrandedPendingDiffs(t *testing.T) {
	f := newAutonomyFixture(t)
	ctx := context.Background()
	w2, err := f.st.CreateOrGetWorkstream(ctx, f.p.ID, "side")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := f.st.CreateConversation(ctx, w2.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	add := func(convID int64, name string) store.Diff {
		t.Helper()
		path := filepath.Join(f.dir, name)
		if err := os.WriteFile(path, []byte(patchDoc("README.md", 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		d, err := f.st.InsertDiff(ctx, convID, path, "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	review := func(convID, diffID int64, payloadFmt string) {
		t.Helper()
		if _, err := f.st.AppendEvent(ctx, convID, store.EventReviewAction, fmt.Sprintf(payloadFmt, diffID)); err != nil {
			t.Fatal(err)
		}
	}
	review2 := func(convID int64, a, b int64, payloadFmt string) {
		t.Helper()
		if _, err := f.st.AppendEvent(ctx, convID, store.EventReviewAction, fmt.Sprintf(payloadFmt, a, b)); err != nil {
			t.Fatal(err)
		}
	}

	d1 := add(f.c.ID, "d1.diff") // terminal: settled blocked row
	d2 := add(f.c.ID, "d2.diff") // stranded: breadcrumbs only (restart mid-panel)
	d3 := add(f.c.ID, "d3.diff") // stranded: zero pipeline rows
	d4 := add(c2.ID, "d4.diff")  // terminal: pre-land evidence (land-failure race)
	d5 := add(c2.ID, "d5.diff")  // stranded: panel_infra — retry IS the design
	d6 := add(c2.ID, "d6.diff")  // terminal: ladder owns it (round spawned)
	d7 := add(c2.ID, "d7.diff")  // stranded: human-review rows never dedup
	review(f.c.ID, d1.ID, `{"action":"auto_land_blocked","actor":"auto_panel","reason":"panel_mixed","diff_id":%d}`)
	review(f.c.ID, d2.ID, `{"action":"auto_land_started","actor":"auto_panel","stage":"panel","diff_id":%d}`)
	review(f.c.ID, 999, `{"action":"auto_land_blocked","actor":"auto_panel","reason":"verify_failed","diff_id":%d}`) // foreign id bleeds nowhere
	review(c2.ID, d4.ID, `{"action":"moa_review","actor":"auto_panel","diff_id":%d,"consensus_verdict":"accept"}`)
	review(c2.ID, d5.ID, `{"action":"auto_land_blocked","actor":"auto_panel","reason":"panel_infra","diff_id":%d}`)
	review2(c2.ID, d6.ID, d6.ID, `{"action":"auto_revise_round","actor":"auto_panel","round":1,"diff_id":%d,"origin_diff_id":%d}`)
	review(c2.ID, d7.ID, `{"action":"moa_review","diff_id":%d,"consensus_verdict":"reject"}`)

	rows, err := f.st.ListAllPendingDiffs(ctx, f.p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 7 {
		t.Fatalf("pending rows = %d, want 7 (fixture sanity)", len(rows))
	}
	got, err := strandedPendingDiffs(ctx, f.st, rows)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	want := []int64{d2.ID, d3.ID, d5.ID, d7.ID} // workstream-then-diff order preserved
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("stranded ids = %v, want %v", ids, want)
	}
}

// startPanelCaptureStub is startPanelStub plus prompt capture: every
// request's user content lands in the returned snapshot (mutex-guarded —
// panel legs fan out concurrently, so capture ORDER is nondeterministic).
func startPanelCaptureStub(t *testing.T, reply func(call int64, model string) (int, string)) (*int64, func() []string) {
	t.Helper()
	calls := new(int64)
	var mu sync.Mutex
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var text string
		if len(req.Messages) > 0 {
			_ = json.Unmarshal(req.Messages[len(req.Messages)-1].Content, &text)
		}
		mu.Lock()
		prompts = append(prompts, text)
		mu.Unlock()
		n := atomic.AddInt64(calls, 1)
		status, out := reply(n, req.Model)
		if status != http.StatusOK {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "gateway boom"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": out}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")
	return calls, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), prompts...)
	}
}

// TestRecoverReFireAnchorsStoredGoal (schema v3, the #34 false
// objective-mismatch rejection): a diff re-fired by recoverPendingDiffs
// long after its producing run — a NEWER human message has since landed in
// the same conversation — is judged against the goal stored on its row,
// never the conversation's newest message. #34 was auto-rejected because
// the re-fire anchored "现在coding.sudoai.cc 应该可以访问了" (a connectivity
// note) against an unrelated 2,500-line batch; every judge flagged the
// mismatch.
func TestRecoverReFireAnchorsStoredGoal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\nauto_apply: main\n")
	_, captured := startPanelCaptureStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	root, _ := visualAutolandRepo(t) // HEAD carries .odo-verify (echo PASS)
	// The diff's conversation lives under the ROOT project — the recovery
	// enumerates pending diffs by s.projectRoot's project.
	ctx := context.Background()
	p, err := f.st.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		t.Fatal(err)
	}
	w, err := f.st.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.st.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(f.dir, "p.diff")
	if err := os.WriteFile(patchPath, []byte(realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "gui", "src", "app.ts"), []byte("export const x = 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})), 0o644); err != nil {
		t.Fatal(err)
	}
	// The producing run's worktree (production shape: .odo/worktrees/<id>,
	// NEVER the main checkout — the land retires this dir, not main): a
	// real git worktree so the committed .odo-verify is visible to the
	// gate, exactly like a drained run's dir.
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, root, "worktree", "add", "--detach", wt)
	d, err := f.st.InsertDiff(ctx, c.ID, patchPath,
		gitOut(t, root, "rev-parse", "HEAD"), wt, "BUILD THE ORIGINAL BATCH")
	if err != nil {
		t.Fatal(err)
	}
	// The poisoning message: human chatter that postdates the producing run.
	if _, err := f.st.AppendEvent(ctx, c.ID, store.EventUserMessage,
		`{"text":"现在coding.sudoai.cc 应该可以访问了"}`); err != nil {
		t.Fatal(err)
	}
	// The land retires the diff's bound worktree via s.mgr — the real
	// manager against the repo, same as production.
	s := &Server{store: f.st, projectRoot: root, mgr: worktree.NewManager(root)}
	s.recoverPendingDiffs(ctx)

	// The re-fire fans out in goroutines; wait for both legs.
	deadline := time.Now().Add(30 * time.Second)
	for len(captured()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	prompts := captured()
	if len(prompts) != 2 {
		t.Fatalf("panel prompts = %d, want 2 legs (re-fired gate): %v", len(prompts), prompts)
	}
	for i, p := range prompts {
		if !strings.Contains(p, "BUILD THE ORIGINAL BATCH") {
			t.Errorf("leg %d prompt lacks the diff row's stored objective:\n%s", i, p[:min(600, len(p))])
		}
		if strings.Contains(p, "coding.sudoai.cc") {
			t.Errorf("leg %d prompt anchored the LATER human message — the #34 false-anchor bug:\n%s", i, p[:min(600, len(p))])
		}
	}
	// Proof the pipeline ran to completion on the stored objective: the
	// re-fire lands asynchronously past the panel, so poll for the row's
	// terminal status instead of racing the accept.
	var got store.Diff
	for time.Now().Before(deadline) {
		var gerr error
		got, gerr = f.st.GetDiff(ctx, d.ID)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if got.Status != store.DiffPending {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Status != store.DiffAccepted {
		t.Errorf("diff status = %q, want accepted (unanimous panel on the true objective)", got.Status)
	}
}

// TestReviewDiffAnchorProvenance covers the manual review_diff wiring of
// the same fix, both branches of diffGoal: a stored goal beats the
// conversation's newest message; a legacy NULL-goal row keeps the
// originGoal fallback (newest non-slash human message).
func TestReviewDiffAnchorProvenance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	_, captured := startPanelCaptureStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	s := &Server{store: f.st, projectRoot: f.dir}
	ctx := context.Background()

	d1, err := f.st.InsertDiff(ctx, f.c.ID, writePatch(t, f.dir, "p1.diff", patchSrc("src/a.go", 1, 1, false)), "", "", "BUILD THE ORIGINAL BATCH")
	if err != nil {
		t.Fatal(err)
	}
	// Legacy row: no goal recorded (pre-v3 journal shape).
	d2, err := f.st.InsertDiff(ctx, f.c.ID, writePatch(t, f.dir, "p2.diff", patchSrc("src/b.go", 1, 1, false)), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AppendEvent(ctx, f.c.ID, store.EventUserMessage,
		`{"text":"现在coding.sudoai.cc 应该可以访问了"}`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.handleReviewDiff(ctx, Request{DiffID: d1.ID}); err != nil {
		t.Fatal(err)
	}
	for i, p := range captured() {
		if !strings.Contains(p, "BUILD THE ORIGINAL BATCH") || strings.Contains(p, "coding.sudoai.cc") {
			t.Errorf("stored-goal leg %d prompt = wrong anchor:\n%s", i, p[:min(600, len(p))])
		}
	}
	if _, err := s.handleReviewDiff(ctx, Request{DiffID: d2.ID}); err != nil {
		t.Fatal(err)
	}
	legs := captured()[2:]
	if len(legs) != 2 {
		t.Fatalf("legacy-row legs = %d, want 2", len(legs))
	}
	for i, p := range legs {
		if !strings.Contains(p, "coding.sudoai.cc") {
			t.Errorf("legacy leg %d lost the originGoal fallback:\n%s", i, p[:min(600, len(p))])
		}
	}
}

// writePatch drops patch text at dir/name and returns the path.
func writePatch(t *testing.T, dir, name, patch string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAutoLandBlockPrecedence (2026-08-22 panel review, deepseek/kimi):
// producing-run evidence (run_errored, tainted verdict) outranks the
// once-per-lifetime single_judge_panel advisory — the advisory's one shot
// fires on the first CLEAN diff, and per-diff failure rows stay
// attributable to the run, never masked as a config complaint.
func TestAutoLandBlockPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test\nauto_apply: main\n")
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	root, sha := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}

	// Errored producing run at N=1: run_errored wins, advisory keeps its shot.
	d1 := f.addDiff(t, "a.diff", patchSrc("src/a.go", 1, 1, false))
	d1.BaseSHA = &sha
	s.autoLand(context.Background(), d1, root, "goal", true, "")
	// Tainted verdict at N=1: same class, same precedence.
	d2 := f.addDiff(t, "b.diff", patchSrc("src/a.go", 1, 1, false))
	d2.BaseSHA = &sha
	s.autoLand(context.Background(), d2, root, "goal", false, verdictNoText)
	// First CLEAN diff earns the one advisory.
	d3 := f.addDiff(t, "c.diff", patchSrc("src/a.go", 1, 1, false))
	d3.BaseSHA = &sha
	s.autoLand(context.Background(), d3, root, "goal", false, "")

	want := []string{"run_errored", "run_no_text", "single_judge_panel"}
	if got := blockedReasons(t, f.st, f.c.ID); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("blocked reasons = %v, want %v", got, want)
	}
	if n := atomic.LoadInt64(calls); n != 0 {
		t.Errorf("panel calls = %d, want 0 (every block precedes panel spend)", n)
	}
	for _, d := range []store.Diff{d1, d2, d3} {
		got, err := f.st.GetDiff(context.Background(), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.DiffPending {
			t.Errorf("diff %d status = %q, want pending", got.ID, got.Status)
		}
	}
}

// TestReviewFanoutSharedClientBoundsInflight (P1 #10): every leg used to
// build a FRESH moa client, so the per-client in-flight semaphore
// (defaultMaxInFlight in internal/moa — unexported there; 5 at writing)
// never contended: an N-leg gate batch fired N concurrent requests at the
// gateway (the 8×3=24 distill-gate storm the review flagged). The
// Server's shared client re-arms that semaphore as the daemon-wide cap:
// an 8-leg fan-out must pile up no more than 5 concurrent requests while
// still returning every leg's verdict.
func TestReviewFanoutSharedClientBoundsInflight(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	var inflight, maxSeen atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		for {
			if m := maxSeen.Load(); n <= m || maxSeen.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond) // long enough for all 8 legs to try to pile up
		inflight.Add(-1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "ACCEPT"}},
			"stop_reason": "end_turn",
		})
	}))
	defer srv.Close()
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)

	models := make([]reviewModel, 8)
	for i := range models {
		models[i] = reviewModel{model: fmt.Sprintf("rm%d", i), provider: "test"}
	}
	reviews := rig.server.reviewFanout(context.Background(), models, "prompt")
	if len(reviews) != 8 {
		t.Fatalf("legs = %d, want 8", len(reviews))
	}
	for i, r := range reviews {
		if r.Verdict != "accept" {
			t.Errorf("leg %d verdict = %q (%s), want accept", i, r.Verdict, r.Comments)
		}
	}
	if got := maxSeen.Load(); got > 5 {
		t.Errorf("max in-flight = %d, want ≤ 5 — the shared client's semaphore is not bounding the fan-out", got)
	}
}

// TestRunVerifyScratchHomeShieldsFileCredentials (P1 #11): the env
// allowlist blocks key-shaped leaks, but before the sandbox the verify's
// unreviewed code could still READ file-shaped credentials (~/.ssh,
// ~/.aws) and exfiltrate them (same m16 P0 class, different shape). The
// scratch HOME must hide them byte-completely, and runVerify must clean
// the sandbox up after itself.
func TestRunVerifyScratchHomeShieldsFileCredentials(t *testing.T) {
	home := t.TempDir()
	for _, rel := range []string{".ssh", ".aws", ".config"} {
		if err := os.MkdirAll(filepath.Join(home, rel), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_rsa"), []byte("DECOY-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "credentials"), []byte("DECOY-CREDS"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	tail, err := runVerify(context.Background(), t.TempDir(),
		`printf 'HOME=%s\n' "$HOME"; test ! -e "$HOME/.ssh/id_rsa" && test ! -e "$HOME/.aws/credentials" && echo SHIELDED; cat "$HOME/.ssh/id_rsa" 2>/dev/null || echo PLAINTEXT-MISS; echo PASS`)
	if err != nil {
		t.Fatalf("verify errored: %v (tail %q)", err, tail)
	}
	if !strings.Contains(tail, "SHIELDED") || !strings.Contains(tail, "PLAINTEXT-MISS") || !strings.Contains(tail, "PASS") {
		t.Errorf("shielding markers missing from tail %q", tail)
	}
	if strings.Contains(tail, "DECOY-KEY") || strings.Contains(tail, home) {
		t.Errorf("real HOME or its credentials leaked into the verify child: %q", tail)
	}
	// Sandbox lifecycle: the scratch dir the child saw must be gone.
	scratch := strings.TrimPrefix(strings.SplitN(strings.TrimSpace(tail), "\n", 2)[0], "HOME=")
	if scratch == home || scratch == "" || scratch == os.Getenv("HOME") {
		t.Fatalf("child HOME = %q, want a scratch dir (real home %q)", scratch, home)
	}
	if _, serr := os.Stat(scratch); !os.IsNotExist(serr) {
		t.Errorf("scratch dir %q still on disk after runVerify (stat err %v)", scratch, serr)
	}
}

// TestRunVerifyMountsGoToolchainCaches (P1 #11): the sandbox must not cold-
// start the toolchain — GOCACHE/GOMODCACHE/GOPATH are re-exported from the
// REAL environment's resolution so a per-verify scratch HOME keeps the
// warm caches (without it, every auto-land verify re-downloads modules).
func TestRunVerifyMountsGoToolchainCaches(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	t.Setenv("HOME", t.TempDir())
	// Same telemetry pin as goToolchainCacheEnv: keep the query's async
	// counter writes off the TempDir HOME (cleanup-race guard).
	t.Setenv("GOTELEMETRYDIR", filepath.Join(os.TempDir(), "odo-go-telemetry"))
	out, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE", "GOPATH").Output()
	if err != nil {
		t.Skipf("go env unusable: %v", err)
	}
	want := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(want) != 3 {
		t.Skipf("unexpected go env shape: %q", out)
	}

	tail, err := runVerify(context.Background(), t.TempDir(), `printf '%s\n%s\n%s\n' "$GOCACHE" "$GOMODCACHE" "$GOPATH"`)
	if err != nil {
		t.Fatalf("verify errored: %v (tail %q)", err, tail)
	}
	got := strings.Split(strings.TrimSpace(tail), "\n")
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("child caches = %q, want %q", got, want)
	}
}

// TestPlaywrightBrowsersDir (P1 #11): the gui verify's browser cache is
// the one audited env exception — found when installed, silent when not,
// env override always honored.
func TestPlaywrightBrowsersDir(t *testing.T) {
	// Scrub the daemon-side verify's exported override (P1 #11): on a
	// machine with the cache installed runVerify ALWAYS exports
	// PLAYWRIGHT_BROWSERS_PATH, and playwrightBrowsersDir honors it
	// first — without the scrub the "uninstalled" assertion below reads
	// the host cache and fails deterministically under verify.
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")
	home := t.TempDir()
	if got := playwrightBrowsersDir(home); got != "" {
		t.Fatalf("uninstalled browsers = %q, want \"\"", got)
	}
	var dir string
	if runtime.GOOS == "darwin" {
		dir = filepath.Join(home, "Library", "Caches", "ms-playwright")
	} else {
		dir = filepath.Join(home, ".cache", "ms-playwright")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := playwrightBrowsersDir(home); got != dir {
		t.Errorf("installed browsers = %q, want %q", got, dir)
	}
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "/custom/browsers")
	if got := playwrightBrowsersDir(home); got != "/custom/browsers" {
		t.Errorf("override = %q, want /custom/browsers", got)
	}
}
// TestCapDetailTailBias pins the flipped cap policy (#40 investigate): an
// oversized detail keeps its TAIL — go-test output carries ---
// FAIL/ok-summary lines at the end, and head-trimming journaled 4KB of
// PASS spam while the actual failure vanished from the blocked row.
func TestCapDetailTailBias(t *testing.T) {
	if got := capDetail("short"); got != "short" {
		t.Fatalf("undersized detail = %q, want unchanged", got)
	}
	s := strings.Repeat("PASS ok\n", 1024) + "--- FAIL: TestBoom\nexit status 1"
	got := capDetail(s)
	if len(got) > 4*1024+len("…[earlier truncated]\n") {
		t.Errorf("capped len = %d, want ≤ 4KB + marker", len(got))
	}
	if !strings.HasPrefix(got, "…[earlier truncated]\n") {
		t.Errorf("head marker missing from %q…", got[:40])
	}
	if !strings.HasSuffix(got, "--- FAIL: TestBoom\nexit status 1") {
		t.Errorf("failure tail lost: …%q", got[len(got)-60:])
	}
	if !utf8.ValidString(got) {
		t.Error("capped detail is not valid UTF-8")
	}
	// Rune-safe boundary: multi-byte runes straddling the cut must not
	// bleed invalid bytes into the kept tail.
	cjk := strings.Repeat("中", 4*1024)
	if got := capDetail(cjk); !utf8.ValidString(got) {
		t.Error("CJK-straddled cut is not valid UTF-8")
	}
}

// TestSetEnv pins the single-entry contract: replace-on-name-match only
// (the HOMEBREW-vs-HOME prefix trap), append otherwise.
func TestSetEnv(t *testing.T) {
	env := setEnv([]string{"A=1", "HOME=/old"}, "HOME=/new")
	if n := strings.Count(strings.Join(env, "\n"), "HOME="); n != 1 {
		t.Fatalf("HOME entries = %d, want 1 in %v", n, env)
	}
	if env[1] != "HOME=/new" {
		t.Errorf("replaced = %q, want HOME=/new", env[1])
	}
	env = setEnv(env, "B=2")
	if env[len(env)-1] != "B=2" {
		t.Errorf("append result tail = %q, want B=2", env[len(env)-1])
	}
	env = setEnv([]string{"HOMEBREW=/x"}, "HOME=/n")
	if len(env) != 2 || env[1] != "HOME=/n" {
		t.Errorf("HOMEBREW prefix trap: %v, want [HOMEBREW=/x HOME=/n]", env)
	}
}
