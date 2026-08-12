package ipc

import (
	"os"
	"path/filepath"
	"testing"
)

// Registry guardrail tests (phantom-project accident, 2026-08-11): paths
// under an odo worktrees dir must never reach ~/.odo/projects.json. The
// guard is structural, so it must hold even against an empty registry
// (worktree seen before its parent project registers) and against paths
// nested below the worktree root.

func useTempRegistry(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projects.json")
	t.Setenv("ODO_REGISTRY_PATH", path)
	return path
}

func registryRoots(t *testing.T) []string {
	t.Helper()
	var roots []string
	for _, r := range registeredProjects() {
		roots = append(roots, r.Root)
	}
	return roots
}

// resolved mirrors what ensureProjectRegistered stores: the EvalSymlinks
// form of t.TempDir() (macOS maps /var → /private/var).
func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("resolve %s: %v", p, err)
	}
	return r
}

func TestRegistryRefusesWorktreePaths(t *testing.T) {
	useTempRegistry(t)
	project := t.TempDir()
	worktree := filepath.Join(project, ".odo", "worktrees", "6a7ae410")

	// Empty registry: the worktree registration attempt arrives first in the
	// accident shape, and must still be refused.
	ensureProjectRegistered(worktree)
	if got := registryRoots(t); len(got) != 0 {
		t.Fatalf("worktree path registered against empty registry: %v", got)
	}

	// Parent project registers fine afterwards.
	ensureProjectRegistered(project)
	if got := registryRoots(t); len(got) != 1 || got[0] != resolved(t, project) {
		t.Fatalf("project registration failed: %v", got)
	}

	// With the parent registered, worktree + deeper nested paths stay out.
	ensureProjectRegistered(worktree)
	ensureProjectRegistered(filepath.Join(worktree, "sub", "dir"))
	if got := registryRoots(t); len(got) != 1 {
		t.Fatalf("worktree paths registered alongside parent: %v", got)
	}
}

func TestRegistryAllowsOrdinaryProjects(t *testing.T) {
	useTempRegistry(t)
	a := t.TempDir()
	b := t.TempDir()
	ensureProjectRegistered(a)
	ensureProjectRegistered(b)
	// Idempotent: re-registering an existing root appends nothing.
	ensureProjectRegistered(a)
	if got := registryRoots(t); len(got) != 2 || got[0] != resolved(t, a) || got[1] != resolved(t, b) {
		t.Fatalf("unexpected registry: %v", got)
	}
}

func TestIsOdoWorktreePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join("/p", ".odo", "worktrees", "abc"), true},
		{filepath.Join("/p", ".odo", "worktrees", "abc", "nested"), true},
		{filepath.Join("/p", ".odo", "worktrees"), false},         // container dir itself is not a worktree
		{filepath.Join("/p", ".odo"), false},                      // project state dir
		{filepath.Join("/p", ".odoxic", "worktrees", "x"), false}, // near-miss on the state dir name
		{filepath.Join("/p", ".odo", "other", "x"), false},        // near-miss on the subdir name
		{filepath.Join("/p", "worktrees", "x"), false},            // worktrees without .odo parent
		{"/", false},
	}
	for _, c := range cases {
		if got := isOdoWorktreePath(c.path); got != c.want {
			t.Errorf("isOdoWorktreePath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// The refusal must be persistence-level, not just in-memory: after refusing,
// the registry file on disk must not contain the worktree root even when a
// later valid project forces a rewrite.
func TestRegistryRefusalSurvivesRewrite(t *testing.T) {
	path := useTempRegistry(t)
	project := t.TempDir()
	worktree := filepath.Join(project, ".odo", "worktrees", "abc")
	ensureProjectRegistered(worktree) // refused
	ensureProjectRegistered(project)  // rewrites the file
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if got := string(b); got == "" || len(got) == 0 {
		t.Fatalf("registry file empty after valid registration")
	}
	if rows := registryRoots(t); len(rows) != 1 || rows[0] != resolved(t, project) {
		t.Fatalf("registry file holds wrong rows: %v", rows)
	}
}
