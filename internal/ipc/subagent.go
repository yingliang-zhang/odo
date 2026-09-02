// subagent.go — P1 borrow #7 from the 2026-08-13 tri-model harness audit
// (grok spawn_subagent: report + worktree isolation). spawn_subagent starts
// a scoped OMP run in a dedicated "sub-" worktree, journaled into the
// PARENT conversation: every adapter event carries a subagent_id payload
// marker, and the lifecycle endpoints are subagent_spawned / subagent_done
// rows. The finished run's diff is registered as a subagent diff
// (diffs.subagent_id) — a PROPOSAL for the parent conversation to review
// through the ordinary accept/reject path, never an auto-land candidate
// (recoverPendingDiffs excludes marked rows).
//
// Invariants (design lock):
//   - Worktree isolation is the security boundary: the subagent's bytes
//     cannot appear in any parent worktree until its diff is accepted.
//   - Journal stays in the parent conversation for full auditability;
//     the subagent holds NO conversation of its own.
//   - One level of isolation only: a spawn carrying req.SubagentID is
//     refused ("enforced in the handler") — the `odo spawn` CLI reads
//     the git-dir marker (odo_subagent) and passes it through.
//   - Not auto-landed, not a separate conversation, not recursive.
//   - Lifecycle is poll/tick-driven like regular runs: pollLocked and
//     the liveness drain advance subagents; no dedicated goroutines.
package ipc

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

const (
	// subRunDirPrefix names subagent worktrees and their diff files —
	// "sub-" makes the class readable in .odo/worktrees listings.
	subRunDirPrefix = "sub-"
	// subAgentMaxActive bounds simultaneous subagent runs per
	// conversation (regular runs keep the global concurrency cap; this
	// one keeps a rogue fan-out bounded without touching it).
	subAgentMaxActive = 8
	// subAgentSummaryCap is the spec's 2KB truncation on subagent_done's
	// summary (the OMP run's final agent_text).
	subAgentSummaryCap = 2048
	// subAgentMarkerName is the git-dir marker the `odo spawn` CLI reads
	// to detect it is running INSIDE a subagent worktree (recursion
	// guard half — the daemon refuses the marked request).
	subAgentMarkerName = "odo_subagent"
)

// subAgentRun is one in-flight (or recently finished) subagent run.
// Bookkeeping is in-memory like runMeta; the journal is the lifecycle's
// durable truth, and a daemon restart orphans a live subagent — the boot
// recovery journals a synthetic subagent_done for those ids.
type subAgentRun struct {
	id             string // journaled subagent_id ("sub-" + runDirID)
	runID          string // adapter run id
	runDirID       string
	conversationID int64  // parent conversation — the journal owner
	parentRunID    string // parent's adapter run id when spawned mid-parent-run
	worktreePath   string
	goal           string
	consumed       int
	finished       bool
}

// handleSpawnSubagent admits one isolated subagent run: a fresh "sub-"
// worktree, an OMP run with goal (+optional Context section), all of it
// journaled into the parent conversation. s.mu is held for the handler —
// admission bookkeeping, worktree creation, and the journal row stay one
// serial unit (the handleSendMessage precedent; adapter Start is
// non-blocking so the hold stays short).
func (s *Server) handleSpawnSubagent(ctx context.Context, req Request) (Response, error) {
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		return Response{}, fmt.Errorf("spawn_subagent: goal is required")
	}
	// One-level isolation, enforced in the handler (design lock): the
	// `odo spawn` CLI marks requests issued from inside a subagent
	// worktree; a plain IPC caller setting the field gets the same
	// refusal — the marker is an admission check, not a trust decision.
	if req.SubagentID != "" {
		return Response{}, fmt.Errorf("spawn_subagent: refused — issued from subagent %q: subagents cannot spawn subagents (one level of isolation only)", req.SubagentID)
	}
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("spawn_subagent: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// `odo spawn` inside a run worktree passes NO conversation id: the
	// caller's worktree path (req.Path) resolves the parent conversation
	// from the live run table — the only durable binding a run worktree
	// has to its conversation (in-memory; a stale path refuses with the
	// explicit-flag hint, never a cross-conversation guess).
	convID := req.ConversationID
	if convID == 0 {
		if req.Path == "" {
			return Response{}, fmt.Errorf("spawn_subagent: conversation_id is required (pass the caller's run worktree path in path instead)")
		}
		wt := req.Path
		if resolved, rerr := filepath.EvalSymlinks(wt); rerr == nil {
			wt = resolved
		}
		for _, meta := range s.runs {
			if meta.worktreePath == wt {
				convID = meta.conversationID
				break
			}
		}
		if convID == 0 {
			return Response{}, fmt.Errorf("spawn_subagent: no run owns worktree %s — pass conversation_id explicitly", req.Path)
		}
	}
	c, err := s.checkConversation(ctx, convID)
	if err != nil {
		return Response{}, fmt.Errorf("spawn_subagent: %w", err)
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, fmt.Errorf("spawn_subagent: %w", err)
	}
	if err := s.guardLiveWorkstreamLocked(w); err != nil {
		return Response{}, fmt.Errorf("spawn_subagent: %w", err)
	}
	if s.landSealed {
		return Response{}, fmt.Errorf("spawn_subagent: land admissions sealed (shutting down)")
	}
	active := 0
	for _, sub := range s.subagents {
		if !sub.finished {
			active++
		}
	}
	if active >= subAgentMaxActive {
		return Response{}, fmt.Errorf("spawn_subagent: %d active subagent(s) (cap %d); wait for one to report", active, subAgentMaxActive)
	}

	runDirID := subRunDirPrefix + worktree.NewRunID()
	wtPath, err := s.mgr.Create(runDirID)
	if err != nil {
		return Response{}, fmt.Errorf("spawn_subagent: create worktree: %w", err)
	}
	// Setup failures after this point unwind the worktree exactly like a
	// run admission failure (nothing journaled yet → nothing to fake).
	ad := s.adapterFor("")

	prompt := subAgentPrompt(req.Context, goal)
	runID, err := ad.Start(ctx, wtPath, prompt)
	if err != nil {
		_ = s.mgr.Remove(wtPath)
		return Response{}, fmt.Errorf("spawn_subagent: start agent: %w", err)
	}

	// Journal-first unwind parity with run admissions: if the spawn row
	// cannot land, the run started unlogged — kill it (evidence-before-
	// action; the start side effect must never outlive its receipt).
	parentRunID := ""
	if rid, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[rid]; meta != nil && !meta.finished {
			parentRunID = meta.runID
		}
	}
	payload := map[string]interface{}{
		"subagent_id":   runDirID,
		"goal":          goal,
		"run_dir_id":    runDirID,
		"worktree_path": wtPath,
	}
	if parentRunID != "" {
		payload["parent_run_id"] = parentRunID
	}
	if strings.TrimSpace(req.Context) != "" {
		payload["context"] = req.Context
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventSubagentSpawned, mustJSON(payload)); err != nil {
		_ = ad.Cancel(ctx, runID)
		_ = s.mgr.Remove(wtPath)
		return Response{}, fmt.Errorf("spawn_subagent: journal spawn: %w", err)
	}
	// The git-dir recursion marker: `odo spawn` invoked beneath this
	// worktree finds it and marks the request (stderr note, never fatal —
	// the handler's SubagentID check is the enforcement half).
	if err := writeSubAgentMarker(wtPath, runDirID); err != nil {
		log.Printf("spawn_subagent: write recursion marker in %s: %v", wtPath, err)
	}
	s.subagents[runDirID] = &subAgentRun{
		id:             runDirID,
		runID:          runID,
		runDirID:       runDirID,
		conversationID: c.ID,
		parentRunID:    parentRunID,
		worktreePath:   wtPath,
		goal:           goal,
	}
	return Response{
		Subagent: &SubagentInfo{SubagentID: runDirID, RunID: runID, WorktreePath: wtPath},
	}, nil
}

// subAgentPrompt assembles the subagent's OMP prompt: the caller's
// context (when non-empty) as a "## Context" section ahead of the goal,
// plus the isolation contract tail. Deliberately no memory layers — the
// subagent is work-scoped (its context is the parent's handoff), and it
// must never inherit the parent's durable behavior rules.
func subAgentPrompt(contextText, goal string) string {
	var b strings.Builder
	if t := strings.TrimSpace(contextText); t != "" {
		b.WriteString("## Context\n\n")
		b.WriteString(contextText)
		b.WriteString("\n\n")
	}
	b.WriteString("## Goal\n\n")
	b.WriteString(goal)
	b.WriteString("\n\n---\nYou are an Odo SUBAGENT. This worktree is isolated from the parent's worktree — your changes cannot leak into it. Produce your change set here; when you finish, the daemon extracts a diff and proposes it to the parent conversation (it is never landed automatically). Do not write outside this workspace.")
	return b.String()
}

// writeSubAgentMarker drops the recursion marker into the worktree's git
// metadata dir (<main>/.git/worktrees/<id>/odo_subagent): outside the
// checkout bytes, so it can never ride an ExtractDiff into the proposal.
func writeSubAgentMarker(worktreePath, subID string) error {
	gitDir, err := git.GitDir(worktreePath)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(gitDir, subAgentMarkerName), []byte(subID+"\n"), 0o600)
}

// drainSubAgentsLocked advances every unfinished subagent owned by
// conversationID (0 = all conversations, the liveness sweep). Caller
// holds s.mu; drain order is irrelevant (runs are independent).
func (s *Server) drainSubAgentsLocked(ctx context.Context, conversationID int64) error {
	for _, sub := range s.subagents {
		if sub.finished || (conversationID != 0 && sub.conversationID != conversationID) {
			continue
		}
		if err := s.drainSubAgentLocked(ctx, sub); err != nil {
			// The poll posture next tick (drainRun precedent: errors
			// abort this drain round; the cursor never advanced past the
			// last journaled event).
			return err
		}
	}
	return nil
}

// drainSubAgentLocked pulls new adapter events into the PARENT
// conversation's journal (subagent_id-tagged payloads) and, at the
// terminal event, extracts the diff and journals subagent_done. Caller
// holds s.mu. NOTE: a subagent's agent_text is never fed to the todo
// merge — the parent's plan is not the child's canvas.
func (s *Server) drainSubAgentLocked(ctx context.Context, sub *subAgentRun) error {
	ad := s.adapterFor("")
	evs, err := ad.Events(ctx, sub.runID, sub.consumed)
	if err != nil {
		return err
	}
	// M7 parity with drainRun: a trailing partial event is the adapter's
	// transient preview — never journaled (subagent MVP surfaces no
	// preview bubble; the completed block re-arrives next drain).
	if n := len(evs); n > 0 && evs[n-1].Payload["partial"] == true {
		evs = evs[:n-1]
	}
	summary := ""
	for _, ev := range evs {
		payload := ev.Payload
		if payload == nil {
			payload = map[string]interface{}{}
		}
		payload["subagent_id"] = sub.id
		if _, err := s.store.AppendEvent(ctx, sub.conversationID, ev.Type, mustJSON(payload)); err != nil {
			return err
		}
		sub.consumed++
		if ev.Type == store.EventAgentDone {
			if sum, ok := payload["summary"].(string); ok {
				summary = sum
			}
		}
	}
	if len(evs) == 0 {
		return nil
	}
	terminal := evs[len(evs)-1].Type
	if terminal != store.EventAgentDone && terminal != store.EventAgentError {
		return nil
	}
	sub.finished = true
	s.closeSubAgentLocked(ctx, sub, terminal == store.EventAgentError, summary)
	return nil
}

// closeSubAgentLocked settles a finished subagent: extract the worktree
// diff exactly once, register a subagent diff row when non-empty (never
// auto-landed), journal subagent_done, and retire the adapter state. The
// worktree retires immediately when the diff is empty (drainRun's no-diff
// retire precedent); a registered diff keeps it — the accept/reject path
// retires it through the row's worktree_path binding.
func (s *Server) closeSubAgentLocked(ctx context.Context, sub *subAgentRun, errored bool, summary string) {
	exitCode := 0
	if errored {
		exitCode = 1
	}
	done := map[string]interface{}{
		"subagent_id":   sub.id,
		"goal":          sub.goal,
		"exit_code":     exitCode,
		"worktree_path": sub.worktreePath,
	}
	if summary != "" {
		done["summary"] = truncateRunes(summary, subAgentSummaryCap)
	}

	keepWorktree := false
	baseSHA := ""
	if sha, err := git.CurrentSHA(sub.worktreePath); err == nil {
		baseSHA = sha
	} else {
		log.Printf("subagent: read worktree base sha: %v (diff gets NULL base)", err)
	}
	diffPath, derr := s.mgr.ExtractDiff(sub.worktreePath, sub.runDirID)
	switch {
	case derr != nil:
		done["error"] = "extract diff: " + derr.Error()
	case diffPath == "":
		// No side effect — a report-only subagent (scan/verdict agents
		// land here deliberately).
	default:
		done["diff_path"] = diffPath
		if paths, perr := git.PatchPaths(diffPath); perr != nil {
			log.Printf("subagent: memory-path guard: parse %s: %v", diffPath, perr)
			// fail-closed like the memory-path hit below: accept_diff's
			// parse gate would refuse this patch, so registering it
			// would park a stuck proposal row.
			done["error"] = "this subagent diff was NOT registered: patch unparseable: " + perr.Error()
		} else if merr := rejectMemoryPaths(paths); merr != nil {
			done["error"] = "this subagent diff was NOT registered: " + merr.Error()
		}
		if done["error"] == nil {
			if d, ierr := s.store.InsertSubagentDiff(ctx, sub.conversationID, diffPath, baseSHA, sub.worktreePath, sub.goal, sub.id); ierr != nil {
				done["error"] = "register diff: " + ierr.Error()
			} else {
				done["diff_id"] = d.ID
				keepWorktree = true
			}
		}
	}

	if _, err := s.store.AppendEvent(ctx, sub.conversationID, store.EventSubagentDone, mustJSON(done)); err != nil {
		// Settling without the receipt re-opens the boot recovery's
		// orphan close — log loudly; the run stays finished.
		log.Printf("subagent: journal subagent_done for %s: %v", sub.id, err)
	}
	delete(s.subagents, sub.id)
	ad := s.adapterFor("")
	_ = ad.Close(ctx, sub.runID)
	if err := os.RemoveAll(filepath.Join(s.mgr.StateDir(), "sessions", sub.runID)); err != nil {
		log.Printf("subagent: remove session dir %s: %v", sub.runID, err)
	}
	if !keepWorktree {
		if err := s.mgr.Remove(sub.worktreePath); err != nil {
			log.Printf("subagent: remove worktree %s: %v", sub.worktreePath, err)
		}
	}
}

// recoverOrphanedSubAgents journals a synthetic subagent_done for every
// subagent_spawned id with no done row — the orphan-turn doctrine
// (cold-close, never truncate, never leave dangling): after a daemon
// restart no child process is alive, and a pending diff row (when the
// child's work already settled) survives reviewable either way. Boot-only
// (NewServer); a fresh id set re-derives per boot, so the closure is
// idempotent (done ids are excluded by the fold itself).
func (s *Server) recoverOrphanedSubAgents(ctx context.Context) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		log.Printf("recover-orphan-subagents: get project: %v", err)
		return
	}
	convs, err := s.store.ListActiveConversations(ctx, p.ID)
	if err != nil {
		log.Printf("recover-orphan-subagents: list conversations: %v", err)
		return
	}
	for _, c := range convs {
		events, err := s.store.ListEvents(ctx, c.ID, 0)
		if err != nil {
			log.Printf("recover-orphan-subagents: list events conv %d: %v", c.ID, err)
			continue
		}
		open := map[string]map[string]interface{}{}
		for _, ev := range events {
			switch ev.Type {
			case store.EventSubagentSpawned:
				var spawn struct {
					SubagentID   string `json:"subagent_id"`
					Goal         string `json:"goal"`
					WorktreePath string `json:"worktree_path"`
				}
				if jsonUnmarshalOK(ev.Payload, &spawn) && spawn.SubagentID != "" {
					open[spawn.SubagentID] = map[string]interface{}{
						"goal":          spawn.Goal,
						"worktree_path": spawn.WorktreePath,
					}
				}
			case store.EventSubagentDone:
				var done struct {
					SubagentID string `json:"subagent_id"`
				}
				if jsonUnmarshalOK(ev.Payload, &done) {
					delete(open, done.SubagentID)
				}
			}
		}
		for id, spawnInfo := range open {
			payload := map[string]interface{}{
				"subagent_id": id,
				"goal":        spawnInfo["goal"],
				"exit_code":   1,
				"error":       "daemon restarted while this subagent was in flight — the child process is dead; its worktree (if any) converges with the sweeper",
			}
			if wp, ok := spawnInfo["worktree_path"].(string); ok && wp != "" {
				payload["worktree_path"] = wp
			}
			if _, err := s.store.AppendEvent(ctx, c.ID, store.EventSubagentDone, mustJSON(payload)); err != nil {
				log.Printf("recover-orphan-subagents: close %s: %v", id, err)
				continue
			}
			log.Printf("recover-orphan-subagents: synthetic subagent_done for %s (daemon restart)", id)
		}
	}
}

// truncateRunes clips s to n runes (appending the ellipsis marker), used
// for subagent_done's 2KB summary cap.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
