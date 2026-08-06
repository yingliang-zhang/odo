package git

// M11c regression test: CreateWorktreeOnBranch must handle a branch that is
// already checked out by another live worktree (fan-out lanes, concurrent
// conversations on one workstream) without letting git move the ref out from
// under the existing worktree. The "already used by worktree" error string is
// the load-bearing fallback trigger, so it gets pinned here.

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

	// Two worktrees on the SAME branch — the second must take the fallback
	// (git refuses -B when the branch is already checked out).
	w1 := filepath.Join(t.TempDir(), "w1")
	w2 := filepath.Join(t.TempDir(), "w2")
	if err := CreateWorktreeOnBranch(repo, w1, "odo/main"); err != nil {
		t.Fatalf("first create on branch: %v", err)
	}
	if err := CreateWorktreeOnBranch(repo, w2, "odo/main"); err != nil {
		t.Fatalf("second create on branch (fallback): %v", err)
	}

	// The branch ref must not have moved: both worktrees point at it.
	head := mustRun(t, repo, "rev-parse", "HEAD")
	ref := runCommand(t, repo, "rev-parse", "refs/heads/odo/main")
	if ref != head {
		t.Errorf("odo/main = %s, HEAD = %s: fallback moved the ref", ref, head)
	}
	// And each worktree has the branch checked out (not detached).
	for _, w := range []string{w1, w2} {
		if got := runCommand(t, w, "symbolic-ref", "HEAD"); got != "refs/heads/odo/main" {
			t.Errorf("%s symbolic-ref = %q, want refs/heads/odo/main", w, got)
		}
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