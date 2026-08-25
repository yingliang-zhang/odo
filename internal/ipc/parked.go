package ipc

// W6: goal queue → park-and-switch (ADR-0005, design lock
// docs/design/fix-int-w6-goal-queue-lock.md).
//
// Park = enqueue goal; switch = drain-then-activate. The current run
// drains to completion (no suspend — OMP owns the agent loop; no cancel —
// that already exists as handleCancel); the parked goal starts the instant
// the run finishes. The durable queue is journal-derived:
// user_message{park:true} rows minus seqs consumed by any
// run_prompt{goal_seqs} or parked_goal_dropped{goal_seq} row — per-
// conversation FIFO by journal seq (the atomic ordering domain), capped
// at goalQueueCap. Daemon kill mid-queue → restart → recoverParkedGoals
// re-derives the queue from the journal and the goals resume their wait;
// nothing was ever memory-only (the deepseek-harness durable-inbox
// precedent, P0#3).
//
// Dequeue is fully automatic from three call sites: drainRun's terminal
// tail (runDone), the park branch of handleSendMessage (send to a free
// conversation), and daemon startup. prefs.md "parked_goals: manual"
// disarms all three; the human then drives the queue with
// resume_parked_goal / drop_parked_goal. Steer continuations outrank
// parked goals at each drain (a steer extends the goal thread it was
// typed against), errored runs hold the queue (transcript advisory), and
// panel rejection never blocks the queue — the goal queue and the review
// queue are independent.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

// goalQueueCap bounds one conversation's parked-goal FIFO. Over-cap parks
// fail loud pre-journal (never silently drop a human message).
const goalQueueCap = 8

// parkedGoal is one queued goal: the seq of its user_message{park:true}
// journal row (ordering + consumption identity) and the verbatim text.
type parkedGoal struct {
	seq  int
	text string
}

// deriveParkedGoals is the journal-derived fold: user_message{park:true}
// rows minus seqs consumed by any run_prompt{goal_seqs} or
// parked_goal_dropped{goal_seq} row, in seq order. events is one
// conversation's seq-ascending journal (ListEvents(_, _, 0)). The runtime
// queue and every consumer exclusion (replay) derive from this — the
// journal is the authority, Server.parked only its hot cache.
func deriveParkedGoals(events []store.Event) []parkedGoal {
	var goals []parkedGoal
	consumed := map[int]bool{}
	for _, ev := range events {
		switch ev.Type {
		case store.EventUserMessage:
			var p struct {
				Text string `json:"text"`
				Park bool   `json:"park"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || !p.Park {
				continue
			}
			if strings.TrimSpace(p.Text) == "" {
				continue // nothing runnable; an empty park can never wedge FIFO
			}
			goals = append(goals, parkedGoal{seq: ev.Seq, text: p.Text})
		case store.EventReviewAction:
			var p struct {
				Action   string `json:"action"`
				GoalSeqs []int  `json:"goal_seqs"`
				GoalSeq  int    `json:"goal_seq"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			switch p.Action {
			case "run_prompt":
				for _, seq := range p.GoalSeqs {
					consumed[seq] = true
				}
			case "parked_goal_dropped":
				consumed[p.GoalSeq] = true
			}
		}
	}
	var waiting []parkedGoal
	for _, g := range goals {
		if !consumed[g.seq] {
			waiting = append(waiting, g)
		}
	}
	return waiting
}

// parkedGoalsAuto resolves prefs.md "parked_goals" (auto|manual, default
// auto): manual disarms every automatic dequeue site (drain tail, park on
// a free conversation, startup recovery); resume_parked_goal is the
// manual path and ignores this gate.
func parkedGoalsAuto() bool {
	return !strings.EqualFold(strings.TrimSpace(adapter.LoadPrefsRaw("parked_goals")), "manual")
}

// handleParkGoal is handleSendMessage's req.Park branch: journal the
// durable goal (text verbatim; steer-path journaler — no receipt/recall
// keys), enqueue it, and dequeue immediately when the conversation is
// free (send-to-free-conversation call site). Caller holds s.mu.
func (s *Server) handleParkGoal(ctx context.Context, c store.Conversation, req Request) (Response, error) {
	if len(s.parked[c.ID]) >= goalQueueCap {
		return Response{}, fmt.Errorf("send_message: parked goal queue full (%d)", goalQueueCap)
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(map[string]interface{}{
		"text": req.Text,
		"park": true,
	}))
	if err != nil {
		return Response{}, err
	}
	// M19 (V8): a park is a human send — it suspends an active loop.
	s.suspendLoopOnHumanSendLocked(ctx, c.ID)
	s.parked[c.ID] = append(s.parked[c.ID], parkedGoal{seq: ev.Seq, text: req.Text})
	if parkedGoalsAuto() {
		s.maybeDequeueParkedGoal(ctx, c.ID)
	}
	return Response{Event: &ev, Parked: len(s.parked[c.ID])}, nil
}

// maybeDequeueParkedGoal starts the oldest parked goal when no run is
// active for the conversation — the shared dequeue step behind all three
// automatic call sites (runDone, send to a free conversation, daemon
// startup). The head stays queued unless the run actually starts: the
// consumption receipt (run_prompt{goal_seqs}) is journaled only after
// adapter start, so a failed activation never drops the human's goal.
// Caller holds s.mu.
func (s *Server) maybeDequeueParkedGoal(ctx context.Context, conversationID int64) {
	queue := s.parked[conversationID]
	if len(queue) == 0 {
		return
	}
	// M19 (V8): parked-goal auto-dequeue is suppressed (quietly) while a
	// loop is active for the conversation. The queue survives; the loop's
	// resume/stop re-frees the dequeue.
	if st, _, err := s.loopActiveState(ctx, conversationID); err == nil && st != nil && st.active() {
		log.Printf("parked: dequeue suppressed for conversation %d (loop active)", conversationID)
		return
	}
	c, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		log.Printf("parked: get conversation %d: %v", conversationID, err)
		return
	}
	if s.startParkedGoalRunLocked(ctx, c, queue[0], autoActor) {
		s.removeParkedGoalLocked(conversationID, queue[0].seq)
	}
}

// dequeueParkedGoalOnRunDoneLocked is drainRun's terminal-tail W6 step:
// the caller just finished a run and fired no continuation. Errored runs
// do NOT auto-activate — the queue holds and the transcript advisory says
// so (journalRunAdvisory already prefixes "odo: "). Clean runs delegate
// to maybeDequeueParkedGoal. Returns true when a parked-goal run actually
// started: drainRun fires at most one continuation OR one parked-goal
// activation per finished run (steer continuations outrank parked goals;
// the parked head survives to the next drain), and the M12 auto-distill
// evaluation must skip when the successor slot went to the queue.
// Caller holds s.mu.
func (s *Server) dequeueParkedGoalOnRunDoneLocked(ctx context.Context, meta *runMeta) bool {
	queued := len(s.parked[meta.conversationID])
	if queued == 0 {
		return false
	}
	if meta.errored {
		s.journalRunAdvisory(ctx, meta.conversationID, fmt.Sprintf(
			"%d parked goal(s) remain queued — the last run errored; review it, then resume_parked_goal or wait for the next successful run.",
			queued))
		return false
	}
	if !parkedGoalsAuto() {
		return false
	}
	s.maybeDequeueParkedGoal(ctx, meta.conversationID)
	if runID, ok := s.byConv[meta.conversationID]; ok {
		if m := s.runs[runID]; m != nil && !m.finished {
			return true
		}
	}
	return false
}

// startParkedGoalRunLocked admits and starts one parked goal as a fresh
// run with the caller holding s.mu, returning whether the run started.
// Admission re-checks mirror startFollowupRunLocked (active run,
// concurrency cap, distill in progress); on every failure the goal stays
// queued. actor is autoActor for automatic dequeues (the run_prompt row
// is then fold-excluded pipeline mechanics, foldExcludedReviewAction) and
// "" for resume_parked_goal (a human decision — the row renders a
// harmless one-liner in the fold prompt).
//
// Journal order departs from startFollowupRunLocked deliberately: the
// park row itself is the goal's evidence (journaled at park time), and
// the run_prompt{goal_seqs} consumption receipt lands AFTER adapter
// start — journal-first here would let a failed start consume a goal no
// run ever covers (silently dropping a human message, the exact failure
// the durable inbox exists to prevent).
func (s *Server) startParkedGoalRunLocked(ctx context.Context, c store.Conversation, goal parkedGoal, actor string) (started bool) {
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return false
		}
	}
	if cap := resolveMaxConcurrent(); s.activeRunCount() >= cap {
		log.Printf("parked: skipping dequeue for conversation %d — concurrency cap %d reached", c.ID, cap)
		return false
	}
	if _, ok := s.distilling[c.ID]; ok {
		log.Printf("parked: skipping dequeue for conversation %d — distill in progress", c.ID)
		return false
	}
	ad := s.adapterFor("") // default adapter (continuation precedent)
	if ad == nil || s.mgr == nil {
		log.Printf("parked: no adapter/worktree manager — goal %d stays queued", goal.seq)
		return false
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		log.Printf("parked: get workstream: %v", err)
		return false
	}
	// Mid-delete/deleted lane bar (2026-08-25 review follow-up).
	if err := s.guardLiveWorkstreamLocked(w); err != nil {
		log.Printf("parked: skipping dequeue for conversation %d — %v", c.ID, err)
		return false
	}
	// Full prompt-assembly parity with send/continuation: fresh memory
	// layers (ADR-0003), journal replay, receipt closure. The goal's own
	// park row is still waiting at this point (consumption journals below),
	// so collectReplayTurns excludes it — its text lands verbatim at the
	// prompt's end, the send path's exact shape.
	prompt, receiptPayload, assertErr := s.assembleRunPrompt(ctx, w.Name, c.ID, goal.text)
	if assertErr != nil {
		// M18 W2 item 4 parity: fail closed, no silent drop — the breach
		// is a journaled agent_error, the goal stays queued.
		_ = s.failRun(ctx, c.ID, fmt.Errorf("prompt receipt assertion failed: %w", assertErr))
		return false
	}
	runDirID := worktree.NewRunID()
	wtPath, err := s.mgr.Create(runDirID)
	if err != nil {
		log.Printf("parked: create worktree: %v", err)
		return false
	}
	runID, err := ad.Start(ctx, wtPath, prompt)
	if err != nil {
		_ = s.mgr.Remove(wtPath) // nothing to review; don't orphan a worktree
		log.Printf("parked: start agent: %v", err)
		return false
	}
	// The dequeue receipt: origin:"parked_goal" links this run to the
	// consumed park row(s). goal_seqs is an array by contract (one goal
	// per activation today; the shape survives multi-consumption).
	row := map[string]interface{}{
		"action":    "run_prompt",
		"origin":    "parked_goal",
		"goal_seqs": []int{goal.seq},
	}
	if actor != "" {
		row["actor"] = actor
	}
	for k, v := range receiptPayload {
		row[k] = v
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(row)); err != nil {
		// The receipt cannot be lost: kill the just-started run (nothing
		// drains it — s.mu is held) and leave the goal queued for the
		// next dequeue opportunity.
		_ = ad.Cancel(ctx, runID)
		_ = s.mgr.Remove(wtPath)
		log.Printf("parked: journal run_prompt: %v", err)
		return false
	}
	s.runs[runID] = &runMeta{
		runID:          runID,
		runDirID:       runDirID,
		conversationID: c.ID,
		workstreamID:   c.WorkstreamID,
		worktreePath:   wtPath,
		goal:           goal.text, // the parked goal, verbatim
	}
	s.byConv[c.ID] = runID
	return true
}

// removeParkedGoalLocked drops one goal (by journal seq) from the runtime
// queue. The journal's consumption row is what persists the removal; this
// keeps the hot cache in step. Caller holds s.mu.
func (s *Server) removeParkedGoalLocked(conversationID int64, seq int) {
	queue := s.parked[conversationID]
	for i, g := range queue {
		if g.seq == seq {
			queue = append(queue[:i], queue[i+1:]...)
			break
		}
	}
	if len(queue) == 0 {
		delete(s.parked, conversationID)
	} else {
		s.parked[conversationID] = queue
	}
}

// recoverParkedGoals is the daemon-startup dequeue call site: scan the
// project's active conversations, seed the runtime queue from the journal
// (durable inbox: a daemon kill mid-queue loses nothing), then dequeue the
// oldest parked goal for each free conversation. Called from NewServer —
// after the store is open, before serving (the run table is empty on
// boot, so every conversation is free; the concurrency cap still applies
// via startParkedGoalRunLocked). Best-effort: failures are logged per
// conversation and never stop the daemon from serving.
func (s *Server) recoverParkedGoals(ctx context.Context) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			// An unregistered project (fresh repo, bootstrap pending) is the
			// common case — nothing to recover yet, and no log noise.
			log.Printf("parked: startup scan: %v", err)
		}
		return
	}
	wss, err := s.store.ListWorkstreams(ctx, p.ID)
	if err != nil {
		log.Printf("parked: startup scan: %v", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range wss {
		c, err := s.store.GetActiveConversation(ctx, w.ID)
		if err != nil {
			continue // no active conversation — nothing to recover
		}
		events, err := s.store.ListEvents(ctx, c.ID, 0)
		if err != nil {
			log.Printf("parked: startup scan conversation %d: %v", c.ID, err)
			continue
		}
		if goals := deriveParkedGoals(events); len(goals) > 0 {
			s.parked[c.ID] = goals
			if parkedGoalsAuto() {
				s.maybeDequeueParkedGoal(ctx, c.ID)
			}
		}
	}
}

// handleResumeParkedGoal is the manual resume IPC handler: activate the
// queue head (GoalSeq 0) or one specific parked goal. The manual override
// ignores the parked_goals pref and the errored-run hold — the transcript
// advisory names exactly this command as the way past an errored run.
// Resume rows journal run_prompt WITHOUT an actor (a human decision).
func (s *Server) handleResumeParkedGoal(ctx context.Context, req Request) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	queue := s.parked[c.ID]
	if len(queue) == 0 {
		return Response{}, fmt.Errorf("resume_parked_goal: no parked goals for conversation %d", c.ID)
	}
	goal := queue[0]
	if req.GoalSeq != 0 {
		found := false
		for _, g := range queue {
			if g.seq == req.GoalSeq {
				goal, found = g, true
				break
			}
		}
		if !found {
			return Response{}, fmt.Errorf("resume_parked_goal: no parked goal with seq %d for conversation %d", req.GoalSeq, c.ID)
		}
	}
	if !s.startParkedGoalRunLocked(ctx, c, goal, "") {
		return Response{}, fmt.Errorf("resume_parked_goal: could not start the run (active run, concurrency cap, or distill in progress); the goal stays queued")
	}
	s.removeParkedGoalLocked(c.ID, goal.seq)
	return Response{Parked: len(s.parked[c.ID])}, nil
}

// handleDropParkedGoal is the manual drop IPC handler — the "clean the
// junk drawer" path: journal the human's drop decision
// (parked_goal_dropped{goal_seq}, no actor) and remove the goal from the
// queue. GoalSeq 0 drops the queue head.
func (s *Server) handleDropParkedGoal(ctx context.Context, req Request) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	queue := s.parked[c.ID]
	if len(queue) == 0 {
		return Response{}, fmt.Errorf("drop_parked_goal: no parked goals for conversation %d", c.ID)
	}
	goal := queue[0]
	if req.GoalSeq != 0 {
		found := false
		for _, g := range queue {
			if g.seq == req.GoalSeq {
				goal, found = g, true
				break
			}
		}
		if !found {
			return Response{}, fmt.Errorf("drop_parked_goal: no parked goal with seq %d for conversation %d", req.GoalSeq, c.ID)
		}
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":   "parked_goal_dropped",
		"goal_seq": goal.seq,
	})); err != nil {
		return Response{}, err
	}
	s.removeParkedGoalLocked(c.ID, goal.seq)
	return Response{Parked: len(s.parked[c.ID])}, nil
}
