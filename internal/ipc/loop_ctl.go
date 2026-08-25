package ipc

// M19 (/loop): the human-facing control surface — /loop status, /loop
// stop, /loop resume, and the GUI's loop_ctl command (design gate +
// chip buttons + notification receipts).

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

// loopStatus renders a fold dump into the chat (slash answer shape: a
// slash user_message, then agent_text + agent_done so the GUI completes).
func (s *Server) loopStatus(ctx context.Context, c *store.Conversation) (Response, error) {
	if err := s.journalLoopSlash(ctx, c, "/loop status"); err != nil {
		return Response{}, err
	}
	states, _, err := s.loopAllStates(ctx, c.ID)
	if err != nil {
		return Response{}, err
	}
	text := renderLoopStatus(states)
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentText, mustJSON(map[string]interface{}{
		"text": text,
		"loop": true,
	})); err != nil {
		return Response{}, err
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentDone, mustJSON(map[string]interface{}{
		"loop": true,
	}))
	if err != nil {
		return Response{}, err
	}
	return Response{Event: &ev}, nil
}

// loopAllStates folds the conversation and returns every loop (even
// terminal — status shows history).
func (s *Server) loopAllStates(ctx context.Context, conversationID int64) ([]*loopState, []store.Event, error) {
	events, err := s.store.ListEvents(ctx, conversationID, 0)
	if err != nil {
		return nil, nil, err
	}
	return deriveLoopStates(events), events, nil
}

// renderLoopStatus builds the status dump: one block per loop, newest
// last, with the fold's own numbers (re-derivable, never cached).
func renderLoopStatus(states []*loopState) string {
	if len(states) == 0 {
		return "No loops on this conversation."
	}
	var b strings.Builder
	for _, st := range states {
		fmt.Fprintf(&b, "## Loop #%d — %s, %s%s\n\n", st.id, st.mode, st.status, loopCauseSuffix(st))
		fmt.Fprintf(&b, "- base: %s\n", st.base)
		fmt.Fprintf(&b, "- rounds: %d/%d, spent: %d/%d tokens\n", len(st.rounds), st.maxRounds, st.spentTokens, st.budgetTokens)
		if v := st.latestVerdict(); v != "" {
			fmt.Fprintf(&b, "- latest verdict: %s (fix phase: %s)\n", v, loopFixPhase(st))
		}
		if len(st.tasks) > 0 {
			b.WriteString("- tasks:\n")
			for _, t := range st.tasks {
				status := "pending"
				if t.done {
					status = t.doneStatus
				} else if t.spawned {
					status = "implementing"
				} else if t.designLockSeq > 0 {
					status = "awaiting design approval"
				}
				fmt.Fprintf(&b, "    %d. [%s] %s\n", t.n, status, truncateLine(t.text, 80))
			}
		}
		if st.status == "active" && st.lastKind != "" {
			fmt.Fprintf(&b, "- last row: %s (seq %d)\n", st.lastKind, st.lastSeq)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// loopFixPhase renders the fix pipeline's fold state for status.
func loopFixPhase(st *loopState) string {
	switch {
	case st.awaitingFixSpawn:
		return "spawn pending"
	case st.fixOpen:
		return "fix in flight"
	case st.fixOutcome != "":
		return st.fixOutcome
	}
	return "—"
}

// truncateLine caps a status line.
func truncateLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// loopStopCmd implements /loop stop (C11): terminal; cancels the
// in-flight loop run; landed fix diffs stay landed (journaled).
func (s *Server) loopStopCmd(ctx context.Context, c *store.Conversation, detail, origin string) (Response, error) {
	st, _, err := s.loopActiveState(ctx, c.ID)
	if err != nil {
		return Response{}, err
	}
	if st == nil {
		return Response{}, fmt.Errorf("loop: no active loop for this conversation")
	}
	if origin == "" {
		if err := s.journalLoopSlash(ctx, c, "/loop stop"); err != nil {
			return Response{}, err
		}
	}
	payload := map[string]interface{}{"detail": detail}
	if origin != "" {
		payload["origin"] = origin
	}
	ev, err := s.journalLoop(ctx, c.ID, st.id, fmtLoopMode(st), loopKindStopped, payload, st.spentTokens)
	if err != nil {
		return Response{}, err
	}
	s.cancelLoopRun(c.ID, st.id)
	return Response{Event: &ev}, nil
}

// cancelLoopRun cancels this conversation's in-flight loop-marked run
// (fix or implement). The drain afterwards journals its verdict; the
// fold's stopped status makes the loop's pipeline a no-op.
func (s *Server) cancelLoopRun(conversationID, loopID int64) {
	s.mu.Lock()
	runID, ok := s.byConv[conversationID]
	var meta *runMeta
	if ok {
		meta = s.runs[runID]
	}
	s.mu.Unlock()
	if meta == nil || meta.finished || meta.loopID != loopID {
		return
	}
	s.cancelLoopRunLocked(runID, meta)
}

// cancelLoopRunLocked is cancelLoopRun's critical-section core (caller
// holds s.mu — handleSendMessage's human-interleave path). Adapter
// Cancel is non-blocking (SIGKILL, no reap); OMP.Cancel answers "unknown
// run" only for adapter instances that never started the run, which the
// meta.adapter key prevents (P1). The run's drain is inert for a
// suspended/stopped loop — loopDrainActive's fold check.
func (s *Server) cancelLoopRunLocked(runID string, meta *runMeta) {
	if err := s.adapterFor(meta.adapter).Cancel(context.Background(), runID); err != nil {
		log.Printf("loop: cancel run %s: %v", runID, err)
	}
}

// loopResumeCmd implements /loop resume [budget=T]: journals the resume
// (clears the suspendable cause; optional budget raise) and re-ticks
// (V8: resume refolds and re-ticks — never automatic).
func (s *Server) loopResumeCmd(ctx context.Context, c *store.Conversation, budget int64, origin string) (Response, error) {
	st, _, err := s.loopActiveState(ctx, c.ID)
	if err != nil {
		return Response{}, err
	}
	if st == nil {
		return Response{}, fmt.Errorf("loop: no loop to resume for this conversation")
	}
	if st.status != "suspended" {
		return Response{}, fmt.Errorf("loop: the loop is not suspended (status %s)", st.status)
	}
	if origin == "" {
		if err := s.journalLoopSlash(ctx, c, "/loop resume"); err != nil {
			return Response{}, err
		}
	}
	payload := map[string]interface{}{"cause": st.cause}
	if budget >= 100_000 {
		payload["budget"] = budget
	}
	if origin != "" {
		payload["origin"] = origin
	}
	ev, err := s.journalLoop(ctx, c.ID, st.id, fmtLoopMode(st), loopKindResumed, payload, st.spentTokens)
	if err != nil {
		return Response{}, err
	}
	s.fireLoopTick(c.ID)
	return Response{Event: &ev}, nil
}

// handleLoopCtl is the GUI-only IPC (design gate + chip buttons +
// notification receipts, GUI: LoopChip popover).
func (s *Server) handleLoopCtl(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	switch req.Action {
	case "stop":
		return s.loopStopCmd(ctx, &c, "stopped from the GUI", "loop_ctl")
	case "resume":
		return s.loopResumeCmd(ctx, &c, req.LoopBudget, "loop_ctl")
	case "approve_design", "amend_design", "veto_design":
		return s.loopDesignCtl(ctx, &c, req)
	case "notified":
		return s.loopNotifiedCtl(ctx, &c, req)
	default:
		return Response{}, fmt.Errorf("loop_ctl: unknown action %q", req.Action)
	}
}

// loopDesignCtl drives the Mode B human design gate (loop_design_gate:
// human). approve/amend spawn the implement run (the spawn row is the
// receipt); veto skips the task.
func (s *Server) loopDesignCtl(ctx context.Context, c *store.Conversation, req Request) (Response, error) {
	st, events, err := s.loopActiveState(ctx, c.ID)
	if err != nil {
		return Response{}, err
	}
	if st == nil || st.mode != loopModeTasks {
		return Response{}, fmt.Errorf("loop_ctl: no active tasks loop for this conversation")
	}
	// The gate's pending task: the first not-done task with a design lock.
	var t *loopTask
	for i := range st.tasks {
		if !st.tasks[i].done && st.tasks[i].designLockSeq > 0 {
			t = &st.tasks[i]
			break
		}
	}
	if t == nil {
		return Response{}, fmt.Errorf("loop_ctl: no design lock is awaiting the gate")
	}
	if req.Action == "veto_design" {
		ev, err := s.journalLoop(ctx, c.ID, st.id, loopModeTasks, loopKindTaskDone, map[string]interface{}{
			"task":   t.n,
			"status": loopTaskVetoed,
			"origin": "loop_ctl",
		}, st.spentTokens)
		if err != nil {
			return Response{}, err
		}
		s.fireLoopTick(c.ID)
		return Response{Event: &ev}, nil
	}
	design := ""
	designSHA := t.designSHA16
	amended := false
	if req.Action == "amend_design" {
		if strings.TrimSpace(req.Text) == "" {
			return Response{}, fmt.Errorf("loop_ctl: amend_design requires the amended text")
		}
		design, amended = req.Text, true
		designSHA = sha16([]byte(design))
	} else {
		design = s.loopReadDesign(events, t.designLockSeq)
		if design == "" {
			return Response{}, fmt.Errorf("loop_ctl: the journaled design lock is unreadable")
		}
	}
	if !s.spawnLoopImplement(ctx, c.ID, st.id, *t, design, designSHA, amended, "loop_ctl") {
		return Response{}, fmt.Errorf("loop_ctl: implement run could not start (see the loop's suspended row)")
	}
	return Response{OK: true}, nil
}

// loopReadDesign reads a journaled design lock back (inline or spilled).
func (s *Server) loopReadDesign(events []store.Event, designLockSeq int) string {
	for _, ev := range events {
		if ev.Type != store.EventLoopEvent || ev.Seq != designLockSeq {
			continue
		}
		if text := jsonStr(ev.Payload, "design_lock"); text != "" {
			return text
		}
		if jsonStr(ev.Payload, "design_lock_path") != "" {
			// Contained + sha16-checked (2026-08-25 audit P2): the design
			// lock steers the implement prompt verbatim.
			if data := s.loopArtifactBody(ev.Payload, "design_lock_path"); data != nil {
				return string(data)
			}
		}
	}
	return ""
}

// loopNotifiedCtl journals the GUI's notification receipt (V11): the
// first loop_notified row for a terminal kind prevents re-fires on GUI
// reopen.
func (s *Server) loopNotifiedCtl(ctx context.Context, c *store.Conversation, req Request) (Response, error) {
	if req.LoopID == 0 {
		return Response{}, fmt.Errorf("loop_ctl: notified requires loop_id")
	}
	kind := strings.TrimSpace(req.Text)
	if kind == "" {
		return Response{}, fmt.Errorf("loop_ctl: notified requires the terminal kind")
	}
	states, _, err := s.loopAllStates(ctx, c.ID)
	if err != nil {
		return Response{}, err
	}
	st := findLoopByID(states, req.LoopID)
	if st == nil {
		return Response{}, fmt.Errorf("loop_ctl: no loop %d on this conversation", req.LoopID)
	}
	if st.notifiedKinds[kind] {
		return Response{OK: true}, nil // already journaled — idempotent
	}
	ev, err := s.journalLoop(ctx, c.ID, st.id, fmtLoopMode(st), loopKindNotified, map[string]interface{}{
		"terminal_kind": kind,
		"origin":        "loop_ctl",
	}, st.spentTokens)
	if err != nil {
		return Response{}, err
	}
	return Response{Event: &ev}, nil
}
