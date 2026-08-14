package git

// Detach-only regression tests (B-class workstream↔git design): EVERY run
// worktree is a detached checkout at the repo HEAD, so N runs of one
// workstream never collide on a branch ref (the M11c "already used by
// worktree" failure class is gone by construction), and nothing but accepts
// moves refs. ApplyDiff/CommitPaths pin the path-scoped accept contract
// (P0: an accept must never sweep unrelated main-checkout changes into its
// commit).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCommand returns trimmed git stdout, failing the test on error.
func runCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

func TestCreateWorktreeDetached(t *testing.T) {
	repo := t.TempDir()
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "odo@test")
	mustRun(t, repo, "config", "user.name", "odo")
	writeAndCommit(t, repo, "base.txt", "base")
	c1 := mustRun(t, repo, "rev-parse", "HEAD")

	// N worktrees from one workstream's runs: no branch, no collision —
	// the M11c "already used by worktree" class cannot occur.
	w1 := filepath.Join(t.TempDir(), "w1")
	if err := CreateWorktree(repo, w1); err != nil {
		t.Fatalf("first create: %v", err)
	}
	w2 := filepath.Join(t.TempDir(), "w2")
	if err := CreateWorktree(repo, w2); err != nil {
		t.Fatalf("second create: %v", err)
	}
	for _, w := range []string{w1, w2} {
		if got := mustRun(t, w, "rev-parse", "HEAD"); got != c1 {
			t.Errorf("%s HEAD = %s, want base %s", w, got, c1)
		}
		if _, err := run(w, "symbolic-ref", "HEAD"); err == nil {
			t.Errorf("%s is on a branch, want detached HEAD", w)
		}
	}
	// Run worktrees create NO refs at all: refs/heads must still be just main.
	if got := runCommand(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads"); got != "refs/heads/main" {
		t.Errorf("refs/heads = %q, want only refs/heads/main", got)
	}
	// Cleanup.
	for _, w := range []string{w1, w2} {
		if err := RemoveWorktree(repo, w); err != nil {
			t.Errorf("remove %s: %v", w, err)
		}
	}
}

func writeAndCommit(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "add", name)
	mustRun(t, dir, "commit", "-m", "add "+name)
}

// mustRun runs git in dir and fails the test on error.
func mustRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

// newPatchRepo builds a repo with committed base.txt + tracked.txt and
// returns its root. It mirrors what initRepo does for the ipc suite.
func newPatchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "odo@test")
	mustRun(t, repo, "config", "user.name", "odo")
	writeAndCommit(t, repo, "base.txt", "base\n")
	writeAndCommit(t, repo, "tracked.txt", "user file\n")
	return repo
}

// generatePatch diffs base.txt's current working-tree content against the
// index, mimicking ExtractDiff: the modification is staged first so the
// post-image blob lands in the object store (--3way can always build it),
// then everything is restored.
func generatePatch(t *testing.T, repo, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "add", "base.txt")
	// Untrimmed: stripping the final newline would corrupt the last hunk line
	// (git apply demands the terminator or a "\ No newline" marker).
	patch, err := run(repo, "diff", "--cached", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "reset", "-q")
	mustRun(t, repo, "checkout", "--", "base.txt")
	path := filepath.Join(t.TempDir(), "change.diff")
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestApplyDiffPathScopedCommit pins P0: accepting a diff stages and commits
// ONLY the patch's paths. Unrelated user state in the main checkout — dirty
// tracked files, staged-but-uncommitted edits, untracked scratch — survives
// exactly as it was, and never rides into the accept commit.
func TestApplyDiffPathScopedCommit(t *testing.T) {
	repo := newPatchRepo(t)
	patch := generatePatch(t, repo, "patched\n")

	// User state the accept must not touch: an unstaged edit, a staged edit,
	// and an untracked scratch file.
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, repo, "staged.txt", "v1\n")
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ApplyDiff(repo, patch); err != nil {
		t.Fatalf("ApplyDiff: %v", err)
	}
	if got := mustRun(t, repo, "cat-file", "-p", ":base.txt"); got != "patched" {
		t.Errorf("index base.txt = %q, want patched (apply + path-scoped stage)", got)
	}
	status := mustRun(t, repo, "status", "--porcelain")
	for _, want := range []string{"M  base.txt", "M  staged.txt", " M tracked.txt", "?? scratch.txt"} {
		if !strings.Contains(status, want) {
			t.Errorf("status missing %q after apply:\n%s", want, status)
		}
	}

	if err := CommitPaths(repo, "odo: accept diff #1", []string{"base.txt"}); err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if got := mustRun(t, repo, "show", "--format=", "--name-only", "HEAD"); got != "base.txt" {
		t.Errorf("accept commit files = %q, want exactly base.txt", got)
	}
	// The user's staged and dirty files were not swept into the commit.
	status = mustRun(t, repo, "status", "--porcelain")
	for _, want := range []string{"M  staged.txt", " M tracked.txt", "?? scratch.txt"} {
		if !strings.Contains(status, want) {
			t.Errorf("status missing %q after commit:\n%s", want, status)
		}
	}
	if strings.Contains(status, "base.txt") {
		t.Errorf("base.txt left uncommitted after CommitPaths:\n%s", status)
	}
}

// TestApplyDiffRefusesUnmerged pins the P1 retry guardrail: ApplyDiff
// refuses to run against an index with unresolved merge conflicts, and the
// refusal names the problem so the user can resolve and retry.
func TestApplyDiffRefusesUnmerged(t *testing.T) {
	repo := t.TempDir()
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "odo@test")
	mustRun(t, repo, "config", "user.name", "odo")
	writeAndCommit(t, repo, "foo.txt", "base\n")

	mustRun(t, repo, "checkout", "-b", "side")
	writeAndCommit(t, repo, "foo.txt", "side\n")
	mustRun(t, repo, "checkout", "main")
	writeAndCommit(t, repo, "foo.txt", "main\n")
	if _, err := run(repo, "merge", "side"); err == nil {
		t.Fatal("merge unexpectedly clean; test needs a conflict")
	}
	if conflicts, err := HasUnmergedEntries(repo); err != nil || !conflicts {
		t.Fatalf("HasUnmergedEntries = %v, %v; want true after conflicted merge", conflicts, err)
	}

	patch := filepath.Join(t.TempDir(), "noop.diff")
	if err := os.WriteFile(patch, []byte("diff --git a/x b/x\nnew file mode 100644\n--- /dev/null\n+++ b/x\n@@ -0,0 +1 @@\n+x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyDiff(repo, patch); err == nil || !strings.Contains(err.Error(), "unmerged") {
		t.Fatalf("ApplyDiff on conflicted index = %v, want an unmerged-entries refusal", err)
	}

	mustRun(t, repo, "merge", "--abort")
	if conflicts, err := HasUnmergedEntries(repo); err != nil || conflicts {
		t.Fatalf("HasUnmergedEntries = %v, %v; want false after merge --abort", conflicts, err)
	}
}

// TestDiffPaths pins the patch-path parser: which side carries a path for
// adds/deletes/modifies, header fallback for mode-only and pure renames,
// and C-quote resolution into real filesystem names.
func TestDiffPaths(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		wantA []string
		wantB []string
	}{
		{
			name:  "modify carries both sides",
			patch: "diff --git a/f.txt b/f.txt\nindex 1111111..2222222 100644\n--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-a\n+b\n",
			wantA: []string{"f.txt"}, wantB: []string{"f.txt"},
		},
		{
			name:  "new file is b-side only",
			patch: "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+x\n",
			wantA: nil, wantB: []string{"new.txt"},
		},
		{
			name:  "delete is a-side only",
			patch: "diff --git a/old.txt b/old.txt\ndeleted file mode 100644\n--- a/old.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n",
			wantA: []string{"old.txt"}, wantB: nil,
		},
		{
			name:  "pure rename falls back to the header",
			patch: "diff --git a/before.txt b/after.txt\nsimilarity index 100%\nrename from before.txt\nrename to after.txt\n",
			wantA: []string{"before.txt"}, wantB: []string{"after.txt"},
		},
		{
			name:  "mode-only falls back to the header",
			patch: "diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n",
			wantA: []string{"run.sh"}, wantB: []string{"run.sh"},
		},
		{
			name:  "quoted paths unquote to real names",
			patch: "diff --git \"a/src/f\\303\\251.go\" \"b/src/f\\303\\251.go\"\nindex 1111111..2222222 100644\n--- \"a/src/f\\303\\251.go\"\n+++ \"b/src/f\\303\\251.go\"\n@@ -1 +1 @@\n-a\n+b\n",
			wantA: []string{"src/fé.go"}, wantB: []string{"src/fé.go"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.diff")
			if err := os.WriteFile(path, []byte(tc.patch), 0o644); err != nil {
				t.Fatal(err)
			}
			a, b, err := DiffPaths(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(a, ",") != strings.Join(tc.wantA, ",") {
				t.Errorf("a-side = %v, want %v", a, tc.wantA)
			}
			if strings.Join(b, ",") != strings.Join(tc.wantB, ",") {
				t.Errorf("b-side = %v, want %v", b, tc.wantB)
			}
		})
	}
}

// TestTestAssertionDelta pins the weakened-tests gate's counting contract:
// assertion tokens are counted per side ONLY inside *_test.go blocks
// (global across files, so a move nets zero), comment lines never count
// on either side (commenting out an assertion nets a removal), and
// /dev/null sides arm independently. The auto-land gate blocks when
// removed > added.
func TestTestAssertionDelta(t *testing.T) {
	cases := []struct {
		name       string
		patch      string
		wantAdd    int
		wantRemove int
	}{
		{
			name: "modify test file counts both sides",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1,2 +1,3 @@\n" +
				" func TestX(t *testing.T) {\n" +
				"-\tassert.Equal(t, 1, got)\n" +
				"+\tassert.Equal(t, 2, got)\n" +
				"+\trequire.NoError(t, err)\n",
			wantAdd:    2, // assert.Equal + require.NoError
			wantRemove: 1,
		},
		{
			name: "assertion tokens in non-test files are ignored",
			patch: "diff --git a/main.go b/main.go\n" +
				"--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n" +
				"-\tassert.Equal(t, 0, x)\n" +
				"+\tassert.Equal(t, 1, x)\n",
		},
		{
			name: "per-block reset: tokens after a test block in a main.go block are ignored",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1 +1,2 @@\n" +
				" func TestX(t *testing.T) {}\n" +
				"+\tt.Error(\"one\")\n" +
				"diff --git a/main.go b/main.go\n" +
				"--- a/main.go\n+++ b/main.go\n@@ -1 +1,2 @@\n" +
				" package main\n" +
				"+\tt.Error(\"two\")\n+\trequire.True(t, true)\n",
			wantAdd: 1, // only the _test.go line
		},
		{
			name: "new test file counts added only",
			patch: "diff --git a/new_test.go b/new_test.go\n" +
				"new file mode 100644\n--- /dev/null\n+++ b/new_test.go\n@@ -0,0 +1,2 @@\n" +
				"+\trequire.True(t, ok)\n" +
				"+\tt.Fatal(\"boom\")\n",
			wantAdd: 2, // require.True + t.Fatal
		},
		{
			name: "deleted test file counts removed only",
			patch: "diff --git a/old_test.go b/old_test.go\n" +
				"deleted file mode 100644\n--- a/old_test.go\n+++ /dev/null\n@@ -1,2 +0,0 @@\n" +
				"-\trequire.True(t, ok)\n" +
				"-\tt.Errorf(\"boom\")\n",
			wantRemove: 2, // t.Error matches t.Errorf
		},
		{
			name: "multiple tokens on one line add up",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1 +1 @@\n" +
				"-\tt.Fail()\n" +
				"+\tassert.NoError(t, require.True(t, ok))\n",
			wantAdd:    2, // assert. + require. on the same line
			wantRemove: 1,
		},
		{
			name: "rename test file nets zero",
			patch: "diff --git a/x_test.go b/y_test.go\n" +
				"--- a/x_test.go\n+++ b/y_test.go\n@@ -1 +1 @@\n" +
				"-\tassert.True(t, ok)\n" +
				"+\tassert.True(t, ok)\n",
			wantAdd:    1,
			wantRemove: 1,
		},
		{
			// M16 panel evasion: `+// assert.X` used to count as added and
			// netted zero against the removed live assertion.
			name: "commenting out an assertion nets a removal",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1 +1 @@\n" +
				"-\tassert.Equal(t, 3, got)\n" +
				"+\t// assert.Equal(t, 3, got)\n",
			wantAdd:    0,
			wantRemove: 1,
		},
		{
			name: "added comment mentioning tokens does not count",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1 +1,2 @@\n" +
				" func TestX(t *testing.T) {}\n" +
				"+\t// TODO: add require.NoError and t.Fatal coverage\n",
			wantAdd: 0,
		},
		{
			name: "removed comment mentioning tokens was never proof",
			patch: "diff --git a/x_test.go b/x_test.go\n" +
				"--- a/x_test.go\n+++ b/x_test.go\n@@ -1,2 +1 @@\n" +
				"-\t// stale: assert.True used to live here\n" +
				" func TestX(t *testing.T) {}\n",
			wantRemove: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "d.diff")
			if err := os.WriteFile(path, []byte(tc.patch), 0o644); err != nil {
				t.Fatal(err)
			}
			added, removed, err := TestAssertionDelta(path)
			if err != nil {
				t.Fatal(err)
			}
			if added != tc.wantAdd || removed != tc.wantRemove {
				t.Errorf("delta = +%d/-%d, want +%d/-%d", added, removed, tc.wantAdd, tc.wantRemove)
			}
		})
	}
}

// ------------------------------------------- P0a: ProbeApplyClean (stale-diff refresh probe)

// probeLeaks counts leftover probe worktrees: registered .git/worktrees
// entries beyond the main checkout and odo-probe-* dirs in the OS temp dir.
func probeLeaks(t *testing.T, repo string) (worktrees int, dirs int) {
	t.Helper()
	worktrees = strings.Count(mustRun(t, repo, "worktree", "list", "--porcelain"), "worktree ")
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "odo-probe-*"))
	if err != nil {
		t.Fatal(err)
	}
	return worktrees, len(matches)
}

// TestProbeApplyClean_CleanOnDrift (P0a): HEAD moved past the diff's base
// but on a DISJOINT path — the diff's embedded base blobs let --3way merge
// it onto the new HEAD, so the probe reports clean. The probe runs entirely
// inside a throwaway worktree: main's HEAD, files, and index are untouched.
func TestProbeApplyClean_CleanOnDrift(t *testing.T) {
	repo := newPatchRepo(t)
	patch := generatePatch(t, repo, "patched\n")
	headBefore := mustRun(t, repo, "rev-parse", "HEAD")
	writeAndCommit(t, repo, "drift.txt", "drift\n") // disjoint drift
	headDrifted := mustRun(t, repo, "rev-parse", "HEAD")
	if headDrifted == headBefore {
		t.Fatal("setup bug: drift commit did not move HEAD")
	}

	clean, detail, err := ProbeApplyClean(repo, patch)
	if err != nil || !clean || detail != "" {
		t.Fatalf("ProbeApplyClean = (%v, %q, %v), want (true, \"\", nil)", clean, detail, err)
	}
	// Main untouched: same HEAD, original file content, pristine status.
	if got := mustRun(t, repo, "rev-parse", "HEAD"); got != headDrifted {
		t.Errorf("main HEAD = %s after the probe, want %s (probe must not move main)", got, headDrifted)
	}
	if got := readFile(repo, "base.txt"); got != "base\n" {
		t.Errorf("main base.txt = %q, want the pre-probe content (probe leaked into main)", got)
	}
	if status := mustRun(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("main status = %q after the probe, want clean", status)
	}
}

// TestProbeApplyClean_ConflictOnOverlap (P0a): HEAD moved by editing the
// SAME file the patch rewrites — the 3-way merge conflicts, the probe
// reports it (non-empty detail carrying git's diagnostics, no error), and
// main is untouched.
func TestProbeApplyClean_ConflictOnOverlap(t *testing.T) {
	repo := newPatchRepo(t)
	patch := generatePatch(t, repo, "patched\n")
	writeAndCommit(t, repo, "base.txt", "user drift\n") // overlapping drift
	headDrifted := mustRun(t, repo, "rev-parse", "HEAD")

	clean, detail, err := ProbeApplyClean(repo, patch)
	if err != nil || clean {
		t.Fatalf("ProbeApplyClean = (%v, %q, %v), want (false, detail, nil)", clean, detail, err)
	}
	if !strings.Contains(detail, "base.txt") {
		t.Errorf("detail = %q, want git's conflict diagnostics naming base.txt", detail)
	}
	if got := mustRun(t, repo, "rev-parse", "HEAD"); got != headDrifted {
		t.Errorf("main HEAD = %s after the probe, want %s", got, headDrifted)
	}
	if got := readFile(repo, "base.txt"); got != "user drift\n" {
		t.Errorf("main base.txt = %q, want the drifted content (probe leaked into main)", got)
	}
	if status := mustRun(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("main status = %q after the probe, want clean", status)
	}
}

// TestProbeApplyClean_CleansUp (P0a): the throwaway worktree is removed
// unconditionally — neither a clean nor a conflicting probe leaves a
// registered worktree entry or an odo-probe-* dir behind.
func TestProbeApplyClean_CleansUp(t *testing.T) {
	repo := newPatchRepo(t)
	conflictPatch := generatePatch(t, repo, "patched again\n")
	writeAndCommit(t, repo, "base.txt", "user drift\n") // makes conflictPatch conflict
	wtBefore, dirsBefore := probeLeaks(t, repo)

	// The clean leg needs a patch that still merges onto HEAD after one
	// more drift commit: a new-file patch cut from the CURRENT (drifted)
	// HEAD, then drift again on a disjoint path.

	disjoint := t.TempDir()
	mustRun(t, repo, "worktree", "add", "--detach", filepath.Join(disjoint, "scratch"), "HEAD")
	scratch := filepath.Join(disjoint, "scratch")
	if err := os.WriteFile(filepath.Join(scratch, "added.txt"), []byte("added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, scratch, "add", "-A")
	disjointPatch, err := run(scratch, "diff", "--cached", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "worktree", "remove", "--force", scratch)
	patchPath := filepath.Join(disjoint, "d.diff")
	if err := os.WriteFile(patchPath, []byte(disjointPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, repo, "second-drift.txt", "drift\n") // disjoint: probe is clean

	if clean, _, err := ProbeApplyClean(repo, patchPath); err != nil || !clean {
		t.Fatalf("disjoint probe = (%v, _, %v), want clean", clean, err)
	}
	conflictPath := filepath.Join(disjoint, "c.diff")
	if data, err := os.ReadFile(conflictPatch); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(conflictPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if clean, _, err := ProbeApplyClean(repo, conflictPath); err != nil || clean {
		t.Fatalf("overlap probe = (%v, _, %v), want conflict", clean, err)
	}

	wtAfter, dirsAfter := probeLeaks(t, repo)
	if wtAfter != wtBefore {
		t.Errorf("registered worktrees = %d, want %d (a probe leaked a worktree entry)", wtAfter, wtBefore)
	}
	if dirsAfter != dirsBefore {
		t.Errorf("odo-probe-* dirs = %d, want %d (a probe leaked its checkout)", dirsAfter, dirsBefore)
	}
}

// readFile returns the working-tree content of one repo file.
func readFile(repo, name string) string {
	data, err := os.ReadFile(filepath.Join(repo, name))
	if err != nil {
		return ""
	}
	return string(data)
}
