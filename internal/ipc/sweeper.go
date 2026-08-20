package ipc

// Startup worktree sweeper (B-class workstream↔git lifecycle, I8/I10).
//
// Truth lives in the journal; disk converges to it within one sweep window.
// For every dir under <project>/.odo/worktrees/:
//
//	live (an in-memory run owns it)            → keep
//	pending diff row binds it                  → keep (review queue owes it)
//	only concluded rows bind it                → reclaim (retire path failed
//	                                             or crashed mid-accept)
//	unreferenced AND younger than the grace    → keep one window (a run whose
//	                                             daemon died between worktree
//	                                             create and diff-row insert —
//	                                             its dir is fresh evidence)
//	unreferenced AND older than the grace      → reclaim (F1 leak; the run is
//	                                             long dead, the diff explicitly
//	                                             disposable per the design
//	                                             review's I8)
//
// Every decision logs an audit line (sweeper: ...). The sweeper runs once at
// daemon startup: single-daemon-per-project means "no in-memory runs yet" is
// exact, and the grace covers the create→insert crash window against a
// daemon booting right after its own crash. The same boot pass then ages
// out .odo/sessions/<runID> and .odo/prompts/<runID>.txt. Normal-path
// cleanup drops a run's transcript dir with it (retireRun); prompt
// captures deliberately survive until boot (they are the audit surface
// for what the agent was shown), so everything the sweeper finds is
// dead-run residue from a previous lifetime — kept one grace window (a
// just-orphaned wrapper may still be draining), reclaimed after. Legacy
// odo/*
// branches are retired at the end (merged-only delete): they only ever
// accumulated accepted content, so -d loses nothing and the "cannot force
// update" accept noise (F2) dies with them.
import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/yingliang-zhang/odo/internal/git"
)

// sweepOrphanGrace is how long an unreferenced worktree dir may be younger
// than before the sweeper reclaims it — one daemon-lifetime window covers
// the create→InsertDiff crash race.
const sweepOrphanGrace = 30 * time.Minute

// SweepOrphanWorktrees reclaims orphan worktree dirs and retires legacy
// odo/* refs. Best-effort: failures log and the boot continues (the next
// boot sweeps again).
func (s *Server) SweepOrphanWorktrees(ctx context.Context) {
	wtRoot := filepath.Join(s.mgr.StateDir(), "worktrees")
	entries, err := os.ReadDir(wtRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("sweeper: read %s: %v", wtRoot, err)
		}
		entries = nil
	}

	referenced, pending, err := s.store.WorktreeRefs(ctx)
	if err != nil {
		// Without DB truth no reclaim is safe — bail entirely.
		log.Printf("sweeper: worktree refs: %v (skipping sweep)", err)
		return
	}

	s.mu.Lock()
	live := map[string]bool{}
	for _, meta := range s.runs {
		live[meta.worktreePath] = true
	}
	s.mu.Unlock()

	reclaimed, kept := 0, 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(wtRoot, e.Name())
		switch {
		case live[path]:
			log.Printf("sweeper: keep %s (live run)", path)
			kept++
		case pending[path]:
			log.Printf("sweeper: keep %s (pending review)", path)
			kept++
		case !referenced[path]:
			info, statErr := e.Info()
			if statErr == nil && time.Since(info.ModTime()) < sweepOrphanGrace {
				log.Printf("sweeper: keep %s (unreferenced but fresh: inside grace window)", path)
				kept++
				break
			}
			log.Printf("sweeper: reclaim %s (orphan: no diff row, past grace)", path)
			if err := s.mgr.Remove(path); err != nil {
				log.Printf("sweeper: reclaim %s: %v", path, err)
			}
			reclaimed++
		default:
			log.Printf("sweeper: reclaim %s (bound only to concluded diff rows)", path)
			if err := s.mgr.Remove(path); err != nil {
				log.Printf("sweeper: reclaim %s: %v", path, err)
			}
			reclaimed++
		}
	}
	if reclaimed > 0 || kept > 0 {
		log.Printf("sweeper: worktrees — %d reclaimed, %d kept", reclaimed, kept)
	}

	// Collapse .git/worktrees metadata for dirs deleted out from under git.
	if err := git.PruneWorktrees(s.projectRoot); err != nil {
		log.Printf("sweeper: worktree prune: %v", err)
	}

	// Sessions/prompts: retireRun drops these at accept/reject, so what's
	// left at boot is crash residue of dead runs (their journal story is
	// complete). Session dir mtimes lie — writes touch files INSIDE the
	// dir, so age is taken from the newest entry within. The grace window
	// covers a wrapper still draining into a fresh dir as the daemon
	// comes back up.
	s.sweepRunArtifactDir("sessions")
	s.sweepRunArtifactDir("prompts")

	// Retire legacy M11c workstream refs (F2). Merged-only: a divergent ref
	// would be the only copy of unaccepted work — refuse and leave it.
	branches, err := git.ListOdoBranches(s.projectRoot)
	if err != nil {
		log.Printf("sweeper: list odo/* branches: %v", err)
		return
	}
	for _, b := range branches {
		if err := git.DeleteBranchMerged(s.projectRoot, b); err != nil {
			log.Printf("sweeper: keep %s (not merged into HEAD: %v)", b, err)
			continue
		}
		log.Printf("sweeper: retired legacy branch %s", b)
	}
}

// sweepRunArtifactDir ages out one per-run artifact dir (.odo/sessions or
// .odo/prompts) at daemon boot. Sessions normally die with their run via
// retireRun — boot-time leftovers are crash residue. Prompts ride a whole
// daemon lifetime as the "what the agent was shown" audit surface; boot
// is where their retention ends. Either way: everything found here is at
// least one lifetime old, so the grace window (not liveness) is the keep
// rule, and age = newest mtime found one level deep so an appended
// output.txt keeps its session dir fresh.
func (s *Server) sweepRunArtifactDir(name string) {
	root := filepath.Join(s.mgr.StateDir(), name)
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("sweeper: read %s: %v", root, err)
		}
		return
	}
	reclaimed, kept := 0, 0
	for _, e := range entries {
		age := time.Since(newestMtime(filepath.Join(root, e.Name()), e))
		if age < sweepOrphanGrace {
			log.Printf("sweeper: keep %s/%s (inside grace window)", name, e.Name())
			kept++
			continue
		}
		log.Printf("sweeper: reclaim %s/%s (crash residue, past grace)", name, e.Name())
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
			log.Printf("sweeper: reclaim %s/%s: %v", name, e.Name(), err)
		}
		reclaimed++
	}
	if reclaimed > 0 || kept > 0 {
		log.Printf("sweeper: %s — %d reclaimed, %d kept", name, reclaimed, kept)
	}
}

// newestMtime walks path one level deep and returns the newest mtime seen
// (the entry itself plus, for dirs, its immediate children).
func newestMtime(path string, e os.DirEntry) time.Time {
	newest, err := e.Info()
	if err != nil {
		return time.Time{}
	}
	t := newest.ModTime()
	if !e.IsDir() {
		return t
	}
	children, err := os.ReadDir(path)
	if err != nil {
		return t
	}
	for _, c := range children {
		if ci, err := c.Info(); err == nil && ci.ModTime().After(t) {
			t = ci.ModTime()
		}
	}
	return t
}
