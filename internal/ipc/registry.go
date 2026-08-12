package ipc

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Global cross-project registry (M4): ~/.odo/projects.json, daemon-owned,
// mode 0600. The learner consults it for sibling projects when checking the
// ≥2-project recurrence gate for user.md proposals. Registration happens once
// at NewServer and is best-effort: a failure degrades the learner to "no
// siblings" instead of failing daemon startup.

// RegistryRow is one registered project.
type RegistryRow struct {
	Root  string `json:"root"`  // absolute, EvalSymlinks-resolved at write time
	Name  string `json:"name"`  // filepath.Base(Root)
	Added string `json:"added"` // RFC3339
}

// registryPath returns the registry file location. ODO_REGISTRY_PATH
// overrides it for tests/smoke scripts (same seam style as ODO_OMP_WRAPPER).
func registryPath() string {
	if p := os.Getenv("ODO_REGISTRY_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".odo", "projects.json")
}

// registeredProjects reads the registry, tolerating a missing or empty file
// (returns an empty slice). A corrupt file also degrades to empty: the
// learner then sees no siblings rather than the daemon failing to boot.
func registeredProjects() []RegistryRow {
	path := registryPath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return nil
	}
	var rows []RegistryRow
	if err := json.Unmarshal(b, &rows); err != nil {
		log.Printf("registry: parse %s: %v (treating as empty)", path, err)
		return nil
	}
	return rows
}

// isOdoWorktreePath reports whether resolved lies inside an odo worktrees
// dir (<project>/.odo/worktrees/<run>). Worktrees are per-run scratch
// spaces, never standalone projects. The 2026-08-11 phantom-project
// accident: a bare `odo conv list` from inside a worktree defaulted
// -project to the worktree cwd, the row outlived the worktree, and the
// GUI then polled the dead path + respawned a stale daemon in a loop.
// The check is structural (path shape), so it holds independent of
// registration order — even with an empty registry.
func isOdoWorktreePath(resolved string) bool {
	for dir := filepath.Clean(resolved); ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		if filepath.Base(parent) == "worktrees" && filepath.Base(filepath.Dir(parent)) == ".odo" {
			return true
		}
		dir = parent
	}
}

// ensureProjectRegistered appends root (EvalSymlinks-resolved) to the
// registry when absent. Best-effort: errors are logged, never returned.
func ensureProjectRegistered(root string) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		log.Printf("registry: resolve %s: %v (using unresolved path)", root, err)
		resolved = root
	}
	if isOdoWorktreePath(resolved) {
		log.Printf("registry: refusing to register worktree path %s", resolved)
		return
	}
	path := registryPath()
	if path == "" {
		return
	}
	rows := registeredProjects()
	for _, r := range rows {
		if r.Root == resolved {
			return // already registered
		}
	}
	rows = append(rows, RegistryRow{
		Root:  resolved,
		Name:  filepath.Base(resolved),
		Added: time.Now().UTC().Format(time.RFC3339),
	})
	b, err := json.Marshal(rows)
	if err != nil {
		log.Printf("registry: marshal: %v", err)
		return
	}
	if err := writeFileAtomic(path, string(b)+"\n", 0o600); err != nil {
		log.Printf("registry: write %s: %v", path, err)
	}
}
