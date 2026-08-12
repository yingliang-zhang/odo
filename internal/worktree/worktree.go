// Package worktree manages per-run git worktrees and diff files under
// <project>/.odo/. Worktrees persist until the user accepts or rejects the
// run's diff; ORPHANED worktrees (no pending diff row, no live run) are
// reclaimed by the daemon's startup sweeper (I8 — disk converges to the
// journal's truth; see internal/ipc/sweeper.go).
package worktree

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yingliang-zhang/odo/internal/git"
)

// Manager binds worktree + diff paths for one project.
type Manager struct {
	// mu serializes Create and Remove (M11 P0): with goroutine-per-connection
	// serving, concurrent `git worktree add/remove` on the same repo could
	// race on .git/worktrees bookkeeping. ExtractDiff stays unlocked — it
	// operates on an existing worktree directory and touches no shared state.
	mu          sync.Mutex
	projectRoot string
	stateDir    string // <project>/.odo
}

// NewManager returns a Manager for the project at projectRoot. State lives in
// projectRoot/.odo (worktrees/, diffs/, sessions/, prompts/).
func NewManager(projectRoot string) *Manager {
	return &Manager{
		projectRoot: projectRoot,
		stateDir:    filepath.Join(projectRoot, ".odo"),
	}
}

// StateDir returns the project's .odo directory.
func (m *Manager) StateDir() string { return m.stateDir }

// NewRunID returns a sortable, filesystem- and branch-safe run identifier.
func NewRunID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return fmt.Sprintf("%x-%s", time.Now().Unix(), hex.EncodeToString(b[:]))
}

// WorktreePath returns the path a worktree for runID would live at.
func (m *Manager) WorktreePath(runID string) string {
	return filepath.Join(m.stateDir, "worktrees", runID)
}

// DiffPath returns the path of the diff file for runID.
func (m *Manager) DiffPath(runID string) string {
	return filepath.Join(m.stateDir, "diffs", runID+".diff")
}

// EnsureDirs creates the .odo state directories. Idempotent.
func (m *Manager) EnsureDirs() error {
	for _, d := range []string{"worktrees", "diffs", "sessions", "prompts"} {
		if err := os.MkdirAll(filepath.Join(m.stateDir, d), 0o755); err != nil {
			return fmt.Errorf("worktree: create state dir %s: %w", d, err)
		}
	}
	return nil
}

// Create adds a DETACHED worktree at HEAD for runID and returns its path.
// Detach-only (B-class design): workstreams never name branches, so there
// is no checkout target besides the base commit. The caller must tolerate
// this failing on repositories with no commits.
func (m *Manager) Create(runID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := m.WorktreePath(runID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("worktree: create worktrees dir: %w", err)
	}
	if err := git.CreateWorktree(m.projectRoot, path); err != nil {
		return "", fmt.Errorf("worktree: create: %w", err)
	}
	return path, nil
}

// ExtractDiff extracts the worktree's full diff vs HEAD and, when non-empty,
// writes it to <project>/.odo/diffs/<runID>.diff. Returns the diff file path,
// or "" when the worktree has no changes.
func (m *Manager) ExtractDiff(worktreePath, runID string) (string, error) {
	diff, err := git.ExtractDiff(worktreePath)
	if err != nil {
		return "", fmt.Errorf("worktree: extract diff: %w", err)
	}
	if diff == "" {
		return "", nil
	}
	path := m.DiffPath(runID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("worktree: create diffs dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(diff), 0o644); err != nil {
		return "", fmt.Errorf("worktree: write diff: %w", err)
	}
	return path, nil
}

// Remove deletes the worktree at path. Force semantics: a dirty worktree
// index is normal (ExtractDiff stages everything). Convergence-first (I8):
// when git can't remove it (metadata already pruned, dir half-deleted), the
// directory is still erased — a lingering scratch dir is worse than a stale
// .git/worktrees entry, and `git worktree prune` (sweeper) collects the
// entry afterwards. Called on accept/reject and by the startup sweeper.
func (m *Manager) Remove(worktreePath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	gitErr := git.RemoveWorktree(m.projectRoot, worktreePath)
	if _, statErr := os.Stat(worktreePath); statErr != nil {
		return gitErr // dir gone: report whatever git thought
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		if gitErr != nil {
			return fmt.Errorf("worktree: remove: %v (fallback rm: %w)", gitErr, err)
		}
		return fmt.Errorf("worktree: remove fallback: %w", err)
	}
	return nil
}
