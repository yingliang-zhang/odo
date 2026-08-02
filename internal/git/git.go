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
func ApplyDiff(repoPath, diffPath string) error {
	_, err := run(repoPath, "apply", diffPath)
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
