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

	"github.com/yingliang-zhang/odo/internal/store"
)

// TestSweeperReconcilesLoopArtifacts pins the .odo/loop/<id> reconciliation
// (P1, 2026-08-22 panel): a non-terminal loop (active OR suspended) holds
// its spill dir even past the grace window (journaled *_path rows point at
// those bytes); a terminal loop's dir and a dir with no journal rows at
// all are reclaimed once past grace, kept inside it; a foreign entry the
// engine can't attribute is never deleted; and .odo/diffs — the only copy
// of reviewable work (epoch-8 evidence) — is provably untouched.
func TestSweeperReconcilesLoopArtifacts(t *testing.T) {
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	ctx := context.Background()

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	mainConv := boot.Conversation.ID
	p, err := rig.store.GetProjectByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	journalLoop := func(convID int64, payload map[string]interface{}) store.Event {
		t.Helper()
		ev, err := rig.store.AppendEvent(ctx, convID, store.EventLoopEvent, mustJSON(payload))
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}

	// main: an active loop (no terminal row) and a completed one.
	startA := journalLoop(mainConv, map[string]interface{}{"kind": "loop_started", "mode": "audit"})
	startB := journalLoop(mainConv, map[string]interface{}{"kind": "loop_started", "mode": "audit"})
	journalLoop(mainConv, map[string]interface{}{"kind": "loop_completed", "loop_id": startB.Seq, "rounds": 1})

	// exp: a SUSPENDED loop — still holds (it can be resumed). Spill ids
	// are conversation-local seqs, so pad the conversation past main's ids
	// to keep the cases disjoint.
	w, err := rig.store.CreateOrGetWorkstream(ctx, p.ID, "exp")
	if err != nil {
		t.Fatal(err)
	}
	expConv, err := rig.store.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	last := 0
	for last < int(startB.Seq) {
		ev, err := rig.store.AppendEvent(ctx, expConv.ID, store.EventUserMessage, `{"text":"pad"}`)
		if err != nil {
			t.Fatal(err)
		}
		last = ev.Seq
	}
	startD := journalLoop(expConv.ID, map[string]interface{}{"kind": "loop_started", "mode": "tasks", "tasks": []string{"x"}})
	journalLoop(expConv.ID, map[string]interface{}{"kind": "loop_suspended", "loop_id": startD.Seq, "cause": "human_interleave"})

	// Ghost ids: no journal rows anywhere (a rotated-away journal's
	// orphan spill, or a crash between file-write and row-insert).
	staleGhost, freshGhost := int64(startD.Seq)+100, int64(startD.Seq)+101

	plant := func(loopID int64) string {
		t.Helper()
		rel, _, err := rig.server.loopSpillBody(loopID, "findings-1.json", `{"findings":1}`)
		if err != nil {
			t.Fatal(err)
		}
		return filepath.Dir(filepath.Join(root, rel))
	}
	old := time.Now().Add(-2 * sweepOrphanGrace)
	ageDir := func(dir string, mt time.Time) {
		t.Helper()
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if err := os.Chtimes(filepath.Join(dir, e.Name()), mt, mt); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(dir, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	dirA := plant(int64(startA.Seq)) // active — held despite age
	dirB := plant(int64(startB.Seq)) // completed — reclaimable
	dirD := plant(int64(startD.Seq)) // suspended — held despite age
	dirStale := plant(staleGhost)    // rows gone — reclaimable
	dirFresh := plant(freshGhost)    // rows gone but inside grace — kept
	for _, d := range []string{dirA, dirB, dirD, dirStale} {
		ageDir(d, old)
	}

	// Foreign entry the engine can't attribute + the standing exclusions.
	stateDir := rig.server.mgr.StateDir()
	stray := filepath.Join(stateDir, "loop", "stray-notes.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stray, old, old); err != nil {
		t.Fatal(err)
	}
	diffFile := filepath.Join(stateDir, "diffs", "orphan.patch")
	if err := os.MkdirAll(filepath.Dir(diffFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diffFile, []byte("unsubmitted work"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(diffFile, old, old); err != nil {
		t.Fatal(err)
	}

	rig.server.SweepOrphanWorktrees(ctx)

	for _, pth := range []string{dirB, dirStale} {
		if _, err := os.Stat(pth); !os.IsNotExist(err) {
			t.Errorf("reclaimable loop dir survived the sweep: %s", pth)
		}
	}
	for _, pth := range []string{dirA, dirD, dirFresh, stray, diffFile} {
		if _, err := os.Stat(pth); err != nil {
			t.Errorf("held artifact was swept: %s: %v", pth, err)
		}
	}
	// The held dirs kept their bytes, not just the empty dir.
	if _, err := os.Stat(filepath.Join(dirA, "findings-1.json")); err != nil {
		t.Errorf("active loop lost its spill body: %v", err)
	}
}

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
