// Package git shells out to the git binary for all repository operations.
// The daemon never links libgit2 and never parses .git internals.
package git

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// run executes git with -C dir and returns combined-stdout on success.
// The error includes the stderr tail so IPC callers can surface git's own
// diagnostics (e.g. "error: patch failed: hello.txt:1").
func run(dir string, args ...string) (string, error) {
	argv := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		tail := strings.TrimSpace(stderr.String())
		if tail == "" {
			tail = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), tail)
	}
	return stdout.String(), nil
}

// CreateWorktree adds a detached worktree of repoPath at HEAD.
func CreateWorktree(repoPath, worktreePath string) error {
	_, err := run(repoPath, "worktree", "add", "--detach", worktreePath, "HEAD")
	return err
}

// CreateWorktreeOnBranch adds a worktree of repoPath at HEAD checked out on
// branch (M11c). -B creates the branch when missing or resets it to HEAD when
// it exists — safe because the worktree is a fresh checkout and accepted
// diffs always land on the main working tree first (see AdvanceBranch).
//
// Git refuses -B when branch is already checked out in another worktree, so
// concurrent runs of one workstream (fan-out lanes, cross-conversation runs)
// fall back to checking the existing ref out in place (-f), which never moves
// the ref out from under a live worktree.
func CreateWorktreeOnBranch(repoPath, worktreePath, branch string) error {
	if _, err := run(repoPath, "worktree", "add", "-B", branch, worktreePath, "HEAD"); err != nil {
		if !strings.Contains(err.Error(), "already used by worktree") {
			return err
		}
		if _, ferr := run(repoPath, "worktree", "add", "--force", worktreePath, branch); ferr != nil {
			return fmt.Errorf("%w (branch fallback: %w)", err, ferr)
		}
	}
	return nil
}

// AdvanceBranch force-points branch at HEAD. Called after an accept so a
// workstream branch accumulates the newly committed change. It fails when
// branch is checked out in a live worktree — callers must run it after the
// run's worktree is retired and treat any error as non-fatal (the next run's
// -B checkout resets the branch forward regardless).
func AdvanceBranch(repoPath, branch string) error {
	_, err := run(repoPath, "branch", "-f", branch, "HEAD")
	return err
}

// RemoveWorktree force-removes a worktree. The worktree index may be dirty
// (ExtractDiff stages everything), so --force is required. A missing path is
// not an error.
func RemoveWorktree(repoPath, worktreePath string) error {
	if _, err := run(repoPath, "worktree", "remove", "--force", worktreePath); err != nil {
		// Tolerate worktrees deleted out from under us.
		if strings.Contains(err.Error(), "is not a working tree") ||
			strings.Contains(err.Error(), "No such file or directory") {
			return nil
		}
		return err
	}
	return nil
}

// ExtractDiff returns the full diff of the worktree against its HEAD,
// including new (untracked) files: everything is staged first, then diffed
// --cached. Staging writes only to the worktree-private index, never to the
// user's repository index. Returns "" when the worktree is unchanged.
func ExtractDiff(worktreePath string) (string, error) {
	if _, err := run(worktreePath, "add", "-A"); err != nil {
		return "", err
	}
	return run(worktreePath, "diff", "--cached", "HEAD")
}

// ApplyDiff applies a unified diff file to the working tree of repoPath.
// It stages all current files first (so untracked-but-applied files from
// previous accepts are in the index), then uses --3way to handle cases
// where the target file already exists with different content.
func ApplyDiff(repoPath, diffPath string) error {
	if _, err := run(repoPath, "add", "-A"); err != nil {
		return fmt.Errorf("stage before apply: %w", err)
	}
	_, err := run(repoPath, "apply", "--3way", diffPath)
	return err
}

// CommitAll stages all changes and creates a commit with the given message.
// Used after applying a diff so the next worktree (created from HEAD) includes
// the accepted files. Requires git user.name and user.email to be configured.
func CommitAll(repoPath, message string) error {
	if _, err := run(repoPath, "add", "-A"); err != nil {
		return err
	}
	_, err := run(repoPath, "commit", "-m", message, "--no-verify")
	return err
}

// CurrentSHA returns the full HEAD commit SHA of repoPath.
func CurrentSHA(repoPath string) (string, error) {
	out, err := run(repoPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
