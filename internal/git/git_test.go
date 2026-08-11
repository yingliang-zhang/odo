package git

// M11c regression tests: CreateWorktreeOnBranch must handle a branch that is
// already checked out by another live worktree (fan-out lanes, concurrent
// conversations on one workstream) without letting git move the ref out from
// under the existing worktree — by falling back to a DETACHED checkout at
// the repo HEAD, never the stale ref. The "already used by worktree" error
// string is the load-bearing fallback trigger, so it gets pinned here.
// ApplyDiff/CommitPaths pin the path-scoped accept contract (P0: an accept
// must never sweep unrelated main-checkout changes into its commit).

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

func TestCreateWorktreeOnBranchFallback(t *testing.T) {
	repo := t.TempDir()
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "odo@test")
	mustRun(t, repo, "config", "user.name", "odo")
	writeAndCommit(t, repo, "base.txt", "base")

	// Two worktrees on the SAME branch — the second must take the detached
	// fallback (git refuses -B when the branch is already checked out).
	w1 := filepath.Join(t.TempDir(), "w1")
	if err := CreateWorktreeOnBranch(repo, w1, "odo/main"); err != nil {
		t.Fatalf("first create on branch: %v", err)
	}

	// Advance the repo HEAD beyond the branch: odo/main now lags, and any
	// fallback that checks the REF out in place would bake this stale base
	// into the run (the P0 bug this test pins).
	c1 := mustRun(t, repo, "rev-parse", "HEAD")
	writeAndCommit(t, repo, "advanced.txt", "advanced")
	c2 := mustRun(t, repo, "rev-parse", "HEAD")
	if c1 == c2 {
		t.Fatal("repo HEAD did not advance")
	}

	w2 := filepath.Join(t.TempDir(), "w2")
	if err := CreateWorktreeOnBranch(repo, w2, "odo/main"); err != nil {
		t.Fatalf("second create on branch (fallback): %v", err)
	}

	// The fallback worktree must be DETACHED AT THE REPO HEAD (fresh base) —
	// not on the stale branch ref.
	if got := mustRun(t, w2, "rev-parse", "HEAD"); got != c2 {
		t.Errorf("fallback worktree HEAD = %s, want repo HEAD %s (detached-at-HEAD fallback)", got, c2)
	}
	if _, err := run(w2, "symbolic-ref", "HEAD"); err == nil {
		t.Error("fallback worktree is on a branch, want detached HEAD")
	}
	// The branch ref must not have moved: it stays where the live worktree
	// holds it.
	if ref := runCommand(t, repo, "rev-parse", "refs/heads/odo/main"); ref != c1 {
		t.Errorf("odo/main = %s, want %s: fallback moved the ref", ref, c1)
	}
	if got := runCommand(t, w1, "symbolic-ref", "HEAD"); got != "refs/heads/odo/main" {
		t.Errorf("w1 symbolic-ref = %q, want refs/heads/odo/main", got)
	}
	// Cleanup: removing both worktrees (--force tolerates the shared branch).
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
