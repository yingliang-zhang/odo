package ipc

// Boot-time convergence tests for the run-artifact janitor (B-class work
// lifecycle). The normal-path cleanup is retireRun's; the sweeper reaps
// what crashed daemons left behind in .odo/sessions and .odo/prompts.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSweeperAgesSessionsAndPrompts pins the boot-time GC for per-run
// artifacts: a past-grace session dir or prompt capture — crash residue
// from a daemon lifetime that never came back — is reclaimed, while a
// fresh one survives (a just-orphaned wrapper may still be draining into
// it during the boot window).
func TestSweeperAgesSessionsAndPrompts(t *testing.T) {
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)

	stateDir := rig.server.mgr.StateDir()
	old := time.Now().Add(-2 * sweepOrphanGrace)

	writeAt := func(path string, mt time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	// Past grace: reclaimed.
	writeAt(filepath.Join(stateDir, "sessions", "dead-run", "output.txt"), old)
	// The dir's own mtime refreshed when output.txt landed inside it —
	// roll it back too, matching a dir abandoned hours ago.
	if err := os.Chtimes(filepath.Join(stateDir, "sessions", "dead-run"), old, old); err != nil {
		t.Fatal(err)
	}
	writeAt(filepath.Join(stateDir, "prompts", "dead-run.txt"), old)

	// Inside grace: kept (dir mtime fresh because the file just landed —
	// the exact shape of a wrapper orphaned mid-drain).
	writeAt(filepath.Join(stateDir, "sessions", "fresh-run", "output.txt"), time.Now())
	writeAt(filepath.Join(stateDir, "prompts", "fresh-run.txt"), time.Now())

	rig.server.SweepOrphanWorktrees(context.Background())

	for _, p := range []string{
		filepath.Join(stateDir, "sessions", "dead-run"),
		filepath.Join(stateDir, "prompts", "dead-run.txt"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("past-grace artifact survived the sweep: %s", p)
		}
	}
	for _, p := range []string{
		filepath.Join(stateDir, "sessions", "fresh-run"),
		filepath.Join(stateDir, "prompts", "fresh-run.txt"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("in-grace artifact reaped: %s: %v", p, err)
		}
	}
}
