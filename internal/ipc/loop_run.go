package ipc

// M19 (/loop) tick engine + drivers + pipelines (loop.go holds the slash
// route and ctl handlers; loop_journal.go the fold/contract; loop_audit.go
// the leg machinery).
//
// Tick doctrine (C1): no driver goroutine per loop, no supervisor. Ticks
// happen at the seams; every long step is a goroutine under loopWG whose
// completion re-ticks. s.loops[conversationID] is liveness-only — the
// liveness guard against double-driving, never state (s.designing
// precedent). Every row the engine needs is re-folded from the journal.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

// Loop driver ownership (C1/C2's liveness guard): s.loops[conversationID]
// present ⟺ a tick chain or a driver goroutine is actively driving that
// conversation's loop. fireLoopTick acquires; a driver-goroutine launch
// HANDS OFF ownership (the launch func flips handedOff); the driver
// releases then fires a fresh tick at completion. Presence is liveness
// only (the s.designing precedent) — the journal fold is the state.
func (s *Server) acquireLoopOwner(conversationID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.loops[conversationID]; ok {
		return false
	}
	s.loops[conversationID] = struct{}{}
	return true
}

// releaseLoopOwner drops the liveness claim.
func (s *Server) releaseLoopOwner(conversationID int64) {
	s.mu.Lock()
	delete(s.loops, conversationID)
	s.mu.Unlock()
}

// fireLoopTick launches a tick goroutine (the slash/drain/ctl seams). A
// conversation already driven coalesces — the driver's completion seam
// fires the follow-up tick.
func (s *Server) fireLoopTick(conversationID int64) {
	if !s.acquireLoopOwner(conversationID) {
		return
	}
	s.loopWG.Add(1)
	go func() {
		defer s.loopWG.Done()
		s.tickLoopOwned(context.Background(), conversationID)
	}()
}

// tickLoopOwned walks the state machine with ownership held until it
// must wait, releasing on exit unless a driver launch took it over.
// Idempotent: every step is journaled before its side effect, so a tick
// never double-fires an effect across restarts.
func (s *Server) tickLoopOwned(ctx context.Context, conversationID int64) {
	handedOff := false
	defer func() {
		if !handedOff {
			s.releaseLoopOwner(conversationID)
		}
	}()
	for {
		st, events, err := s.loopActiveState(ctx, conversationID)
		if err != nil {
			log.Printf("loop: fold conversation %d: %v", conversationID, err)
			return
		}
		if st == nil || !st.active() {
			return
		}
		progress, handed := s.tickLoopOnce(ctx, conversationID, st, events)
		if handed {
			handedOff = true
			return
		}
		if !progress {
			return
		}
	}
}

// tickResult is one tick step's outcome.
type tickResult int

const (
	tickWait      tickResult = iota // wait for the next seam
	tickProgress                    // journaled without waiting — re-fold and continue
	tickHandedOff                   // a driver goroutine owns the loop now
)

// tickLoopOnce performs one state-machine step.
func (s *Server) tickLoopOnce(ctx context.Context, conversationID int64, st *loopState, events []store.Event) (bool, bool) {
	var r tickResult
	if st.mode == loopModeTasks && !st.finalAudit {
		r = s.tickLoopTasks(ctx, conversationID, st, events)
	} else {
		r = s.tickLoopAuditMode(ctx, conversationID, st, events)
	}
	return r == tickProgress, r == tickHandedOff
}

// --- Mode A (+ tasks_final) audit engine ----------------------------------------

// tickLoopAuditMode advances the audit fixpoint: seed → audit round →
// fix spawn → (fix pipeline at drain) → next round → completed.
func (s *Server) tickLoopAuditMode(ctx context.Context, conversationID int64, st *loopState, events []store.Event) tickResult {
	// SEED (V6): pending diffs from loop_started land through s.autoLand
	// verbatim before round 1. Synchronous inside this tick's goroutine;
	// a still-pending diff after its drive suspends (seed_blocked).
	if st.mode == loopModeAudit && len(st.seedDiffs) > 0 && len(st.rounds) == 0 {
		return s.loopSeed(ctx, conversationID, st)
	}
	if st.fixOpen {
		return tickWait // fix run/pipeline in flight — drain seam drives
	}
	if st.awaitingFixSpawn {
		return s.loopSpawnFix(ctx, conversationID, st, events)
	}
	switch st.latestVerdict() {
	case loopVerdictClean:
		// The fixpoint closed: subject audited clean by every leg.
		s.journalLoopBestEffort(ctx, conversationID, st.id, fmtLoopMode(st), loopKindCompleted, map[string]interface{}{
			"rounds":       len(st.rounds),
			"fixes_landed": st.fixesLanded,
		}, st.spentTokens)
		return tickProgress // the fold flips to completed; next pass exits
	case loopVerdictFix:
		if st.fixOutcome != "landed" && st.fixOutcome != "unlanded" {
			return tickWait // fix phase mid-flight without an open run
		}
	case loopVerdictAuditInfra:
		if st.infraStreak >= 2 {
			// C4: one automatic re-issue happened; the second consecutive
			// infra round suspends (never a verdict, never clean).
			s.journalLoopBestEffort(ctx, conversationID, st.id, fmtLoopMode(st), loopKindSuspended, map[string]interface{}{
				"cause":  "audit_infra",
				"detail": "auditor legs keep failing on transport/truncation/parse — the round cannot close clean",
			}, st.spentTokens)
			return tickProgress
		}
	}
	// Launch the next audit round (rounds cap first, V4).
	next := len(st.rounds) + 1
	if next > st.maxRounds {
		s.journalLoopBestEffort(ctx, conversationID, st.id, fmtLoopMode(st), loopKindVerdict, map[string]interface{}{
			"round":   len(st.rounds),
			"verdict": loopVerdictRoundCap,
			"reason":  fmt.Sprintf("round %d exceeds loop_max_rounds %d", next, st.maxRounds),
		}, st.spentTokens)
		s.journalLoopBestEffort(ctx, conversationID, st.id, fmtLoopMode(st), loopKindSuspended, map[string]interface{}{
			"cause":  "round_cap",
			"detail": fmt.Sprintf("audit round cap %d reached without a clean verdict", st.maxRounds),
		}, st.spentTokens)
		return tickProgress
	}
	return s.launchAuditRound(conversationID, st, events, next)
}

// loopSeed drives every pending diff journaled at loop_started through
// s.autoLand verbatim (V6). A diff still pending after its drive suspends
// the loop (seed_blocked) — the human resolves it and resumes. On full
// success the SEED handed itself to the first audit round directly (the
// tick ownership carries over).
func (s *Server) loopSeed(ctx context.Context, conversationID int64, st *loopState) tickResult {
	for _, diffID := range st.seedDiffs {
		// Re-fold between drives (P2): autoLand below is synchronous and
		// long (verify + panel per diff) — a human /loop stop or suspend
		// journal-landing mid-SEED must take effect on the NEXT diff, not
		// after the last one. A terminal loop also ends the loop this st
		// copy describes, so bail without launching round 1.
		fresh, _, ferr := s.loopActiveState(ctx, conversationID)
		if ferr != nil || fresh == nil || fresh.id != st.id || !fresh.active() {
			return tickWait
		}
		st = fresh
		d, err := s.store.GetDiff(ctx, diffID)
		if err != nil {
			log.Printf("loop: seed diff %d: %v", diffID, err)
			continue
		}
		if d.Status != store.DiffPending {
			continue // landed/rejected between listing and seed — nothing to drive
		}
		wtPath := ""
		if d.WorktreePath != nil {
			wtPath = *d.WorktreePath
		}
		s.autoLand(ctx, d, wtPath, s.diffGoal(ctx, d), false, "")
		if after, err := s.store.GetDiff(ctx, diffID); err == nil && after.Status == store.DiffPending {
			s.journalLoopBestEffort(ctx, conversationID, st.id, loopModeAudit, loopKindSuspended, map[string]interface{}{
				"cause":  "seed_blocked",
				"detail": fmt.Sprintf("pending diff #%d survived the auto-land pipeline; resolve it, then /loop resume", diffID),
			}, st.spentTokens)
			return tickProgress
		}
	}
	// Final re-fold before launching round 1: a stop/suspend landing
	// between the last drive and this launch must not spawn an audit.
	fresh, _, ferr := s.loopActiveState(ctx, conversationID)
	if ferr != nil || fresh == nil || fresh.id != st.id || !fresh.active() {
		return tickWait
	}
	return s.launchAuditRound(conversationID, fresh, nil, 1)
}

// launchAuditRound starts the audit goroutine for one round (the
// audit-goroutine seam — its completion releases ownership and fires the
// follow-up tick under loopWG). The caller (a tick) owns the loop; the
// launch hands that ownership to the goroutine.
func (s *Server) launchAuditRound(conversationID int64, st *loopState, events []store.Event, round int) tickResult {
	s.loopWG.Add(1)
	go func() {
		defer s.loopWG.Done()
		defer func() {
			s.releaseLoopOwner(conversationID)
			s.fireLoopTick(conversationID) // the audit-goroutine completion seam (C1)
		}()
		s.runAuditRound(st.id, conversationID, st, events, round)
	}()
	return tickHandedOff
}

// 256KB admits the real squashed-land shapes (M19 impl 233,533B; GUI wave
// 80,889B). Convergence guards are UNCHANGED: the 16KB findings-feed
// breaker, round cap, budget projection, and C5 stall. >256KB is a hard
// wall (a 500KB death-loop stays physically inadmissible).
const loopAuditSubjectCapBytes = 256 * 1024

// runAuditRound executes one audit round end to end and journals
// loop_audit_round + loop_verdict (evidence before action). Never
// suspends directly except the subject-too-large breaker (C12) — every
// other verdict defers to the tick.
func (s *Server) runAuditRound(loopID, conversationID int64, st *loopState, events []store.Event, round int) {
	ctx := context.Background()
	mode := fmtLoopMode(st)

	subject, err := git.DiffRange(s.projectRoot, st.base, "HEAD")
	if err != nil {
		s.journalLoopBestEffort(ctx, conversationID, loopID, mode, loopKindSuspended, map[string]interface{}{
			"cause":  "audit_infra",
			"detail": "cannot resolve the audit subject: " + err.Error(),
		}, st.spentTokens)
		return
	}
	subjectSHA, subjectBytes := sha16([]byte(subject)), len(subject)
	if subjectBytes > loopAuditSubjectCapBytes {
		// C12 subject breaker: the loop-owned 256KB cap admits real
		// squashed-land shapes; anything larger is a hard wall.
		s.journalLoopBestEffort(ctx, conversationID, loopID, mode, loopKindSuspended, map[string]interface{}{
			"cause":  "subject_too_large",
			"detail": fmt.Sprintf("audit subject %dB exceeds the %dB loop audit cap — land pending diffs first or narrow the base= range", subjectBytes, loopAuditSubjectCapBytes),
		}, st.spentTokens)
		return
	}

	// Closure pass input (C6): the previous round's union findings,
	// verbatim. prev_findings_sha16 attests exactly what the prompt carried.
	prev, prevRound := s.loopPrevBlockingFindings(events, loopID)
	// Prior facts (V6): an unlanded fix's verify/land failure rides the
	// next round as advisory evidence, never a suspend.
	priorFacts := s.loopPriorFacts(events, st)

	hold := loopHoldSeverity()
	models := loopAuditorModels()
	prompt := auditPrompt(subject, prev, prevRound, priorFacts)

	// Budget projection (C12): spend is charged as Σ journaled
	// output_tokens + prompt chars/4 estimates; projection over cap
	// suspends as loop_budget_exceeded (resume budget=N raises).
	est := 0
	if len(models) > 0 {
		est = len(prompt) / 4 * len(models)
	}
	if st.spentTokens+est > st.budgetTokens {
		s.journalLoopBestEffort(ctx, conversationID, loopID, mode, loopKindBudgetExceeded, map[string]interface{}{
			"budget_tokens": st.budgetTokens,
			"projected":     st.spentTokens + est,
		}, st.spentTokens)
		return
	}

	client := s.sharedMoa()
	legs := auditFanout(ctx, client, models, auditSystem, prompt)
	var perLeg [][]finding
	completeLegs := 0
	for _, l := range legs {
		if l.Verdict == "complete" {
			completeLegs++
			perLeg = append(perLeg, l.Findings)
		}
	}
	union := unionFindings(perLeg)
	blocking := blockingFindings(union, hold)
	blockingFPS := findingFPs(blocking)

	// Journal the round (spill the findings JSON over 32KB).
	roundPayload := map[string]interface{}{
		"round":          round,
		"subject_sha16":  subjectSHA,
		"subject_bytes":  subjectBytes,
		"findings_count": len(union),
		"blocking_count": len(blocking),
		"legs":           legs,
		"hold_severity":  hold,
	}
	if prevRound > 0 {
		roundPayload["prev_findings_sha16"] = findingsSHA16(prev)
	}
	if len(priorFacts) > 0 {
		roundPayload["prior_facts"] = priorFacts
	}
	findingsJSON := mustJSON(union)
	if len(findingsJSON) > loopBodyCapBytes {
		if rel, sha, serr := s.loopSpillBody(loopID, fmt.Sprintf("findings-%d.json", round), findingsJSON); serr == nil {
			roundPayload["findings_path"] = rel
			roundPayload["findings_sha16"] = sha
		} else {
			log.Printf("loop: spill findings round %d: %v", round, serr)
			roundPayload["findings"] = json.RawMessage(findingsJSON)
		}
	} else {
		roundPayload["findings"] = json.RawMessage(findingsJSON)
	}
	roundSpent := st.spentTokens + loopRowSpend(roundPayload)
	rev, rerr := s.journalLoop(ctx, conversationID, loopID, mode, loopKindAuditRound, roundPayload, roundSpent)
	if rerr != nil {
		log.Printf("loop: journal audit round %d: %v (round lost; tick will re-fold)", round, rerr)
		return
	}
	// Re-fold so verdict math sees the journaled round (restart-proof
	// even when the verdict journal itself fails).
	events, _ = s.store.ListEvents(ctx, conversationID, 0)
	cur := findLoopByID(deriveLoopStates(events), loopID)
	if cur != nil {
		st = cur
	}

	// Verdict (C3/C4/C5), journaled right after its evidence.
	var verdict, reason string
	switch {
	case completeLegs == 0:
		verdict, reason = loopVerdictAuditInfra, "every auditor leg failed (transport/timeout/truncated/parse) — infra is never a verdict"
	case len(blocking) > 0:
		// Stall (C5): the blocking fingerprint set is equal across two
		// consecutive rounds after an intervening landed fix, OR the
		// subject sha16 didn't move across that fix.
		if stall, why := s.loopStallCheck(events, st, subjectSHA, blockingFPS, rev.Seq); stall {
			verdict, reason = loopVerdictStall, why
		} else {
			verdict = loopVerdictFix
		}
	default:
		// Zero blocking findings: clean only when EVERY leg closed
		// readable (C4 — a bad leg may hide a P0).
		if completeLegs < len(models) {
			verdict, reason = loopVerdictAuditInfra, "some auditor legs unreadable — the round cannot close clean"
		} else {
			verdict = loopVerdictClean
		}
	}
	newFPS, carried := splitNewCarriedFPs(blockingFPS, st.rounds[:max(0, len(st.rounds)-1)])
	vp := map[string]interface{}{
		"round":        round,
		"verdict":      verdict,
		"blocking_fps": blockingFPS,
		"new_fps":      newFPS,
		"carried_fps":  carried,
	}
	if reason != "" {
		vp["reason"] = reason
	}
	s.journalLoopBestEffort(ctx, conversationID, loopID, mode, loopKindVerdict, vp, roundSpent)
	if verdict == loopVerdictStall {
		s.journalLoopBestEffort(ctx, conversationID, loopID, mode, loopKindSuspended, map[string]interface{}{
			"cause":  "stall",
			"detail": reason,
		}, roundSpent)
		return
	}
}

// loopStallCheck implements C5's two tripwires, armed only when a fix
// LANDED between the previous round's verdict and this round (a verify-
// failed round that never landed must not read as stall, V6).
func (s *Server) loopStallCheck(events []store.Event, st *loopState, subjectSHA string, blockingFPS []string, thisRoundSeq int) (bool, string) {
	if len(st.rounds) < 2 {
		return false, ""
	}
	prev := st.rounds[len(st.rounds)-2]
	fixLanded := false
	for _, ev := range events {
		if ev.Seq <= prev.seq || ev.Seq >= thisRoundSeq || ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
			Actor  string `json:"actor"`
		}
		if jsonUnmarshalOK(ev.Payload, &p) && p.Action == "accept" && p.Actor == loopActor {
			fixLanded = true
		}
	}
	if !fixLanded {
		return false, ""
	}
	if equalStringSets(prev.blockingFPS, blockingFPS) && len(blockingFPS) > 0 {
		return true, fmt.Sprintf("the blocking fingerprint set is unchanged after round %d's landed fix — the fixpoint is not converging (P? %d fps)", prev.round, len(blockingFPS))
	}
	if prev.subjectSHA16 == subjectSHA {
		return true, fmt.Sprintf("the audit subject did not change after round %d's landed fix (sha16 %s)", prev.round, subjectSHA)
	}
	return false, ""
}

// loopSpawnFix spawns the Mode A fix run for the latest fix verdict
// (BYOF — blocking findings verbatim behind the demotion directive).
func (s *Server) loopSpawnFix(ctx context.Context, conversationID int64, st *loopState, events []store.Event) tickResult {
	round := st.rounds[len(st.rounds)-1]
	blocking := s.loopRoundBlockingFindings(events, st.id, round)
	if len(blocking) == 0 {
		// Corrupt/unreadable findings row: fail closed, not a silent clean.
		s.journalLoopBestEffort(ctx, conversationID, st.id, fmtLoopMode(st), loopKindSuspended, map[string]interface{}{
			"cause":  "audit_infra",
			"detail": fmt.Sprintf("round %d's findings payload is unreadable", round.round),
		}, st.spentTokens)
		return tickProgress
	}
	prompt := fixPrompt(blocking)
	if len(prompt) > settleCommentsCapBytes {
		s.journalLoopBestEffort(ctx, conversationID, st.id, fmtLoopMode(st), loopKindSuspended, map[string]interface{}{
			"cause":  "subject_too_large",
			"detail": fmt.Sprintf("findings feed %dB exceeds the %dB cap — land pending diffs first", len(prompt), settleCommentsCapBytes),
		}, st.spentTokens)
		return tickProgress
	}
	est := len(prompt) / 4
	if st.spentTokens+est > st.budgetTokens {
		s.journalLoopBestEffort(ctx, conversationID, st.id, fmtLoopMode(st), loopKindBudgetExceeded, map[string]interface{}{
			"budget_tokens": st.budgetTokens,
			"projected":     st.spentTokens + est,
		}, st.spentTokens)
		return tickProgress
	}
	spawnRow := map[string]interface{}{
		"round":          round.round,
		"findings_count": len(blocking),
		"findings_sha16": findingsSHA16(blocking),
	}
	admitted, reason := s.startLoopRunLocked(ctx, conversationID, st.id, "fix", round.round, 0, prompt, spawnRow, nil, st.spentTokens)
	if !admitted {
		s.journalLoopBestEffort(ctx, conversationID, st.id, fmtLoopMode(st), loopKindSuspended, map[string]interface{}{
			"cause":  "spawn_failed",
			"detail": reason,
		}, st.spentTokens)
	}
	return tickProgress
}

// --- fix/implement run spawning (startReviseRun parity) --------------------------

// startLoopRunLocked admits, journals, and starts a loop fix/implement run
// under s.mu — startReviseRun's synchronous-admission shape: the
// user_message (with the loop_fix marker) and the spawn row land BEFORE
// the adapter starts (evidence before action). goalSeqs (queue source,
// V9) journal their run_prompt consumption receipt AFTER adapter start
// (the parked contract). prevSpent is the loop's cumulative spend so the
// spawn row stamps the new cumulative (prompt chars/4 estimate — C12).
func (s *Server) startLoopRunLocked(ctx context.Context, conversationID, loopID int64, kind string, round, task int, prompt string, spawnRow map[string]interface{}, goalSeqs []int, prevSpent int) (admitted bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if runID, ok := s.byConv[conversationID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return false, "active_run"
		}
	}
	if cap := resolveMaxConcurrent(); s.activeRunCount() >= cap {
		return false, "concurrency_cap"
	}
	if _, ok := s.distilling[conversationID]; ok {
		return false, "distill_active"
	}
	c, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return false, "conversation_lookup"
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return false, "workstream_lookup"
	}

	fullPrompt, receiptPayload, assertErr := s.assembleRunPrompt(ctx, w.Name, conversationID, prompt)
	if assertErr != nil {
		return false, "receipt_assert_failed"
	}
	est := len(fullPrompt) / 4

	marker := loopFixMarker{LoopID: int(loopID)}
	if round > 0 {
		marker.Round = round
	}
	if task > 0 {
		marker.Task = task
	}
	msgPayload := map[string]interface{}{}
	for k, v := range receiptPayload {
		msgPayload[k] = v
	}
	msgPayload["text"] = prompt
	msgPayload["loop_fix"] = marker
	if _, err := s.store.AppendEvent(ctx, conversationID, store.EventUserMessage, mustJSON(msgPayload)); err != nil {
		return false, "journal_user_message: " + err.Error()
	}

	spent := prevSpent + est
	spawnRow["prompt_tokens_est"] = est
	rowKind, mode := loopKindFixSpawn, loopModeAudit
	if task > 0 {
		rowKind, mode = loopKindTaskSpawn, loopModeTasks
	}
	if _, err := s.journalLoop(ctx, conversationID, loopID, mode, rowKind, spawnRow, spent); err != nil {
		return false, "journal_spawn: " + err.Error()
	}

	runDirID := worktree.NewRunID()
	wtPath, err := s.mgr.Create(runDirID)
	if err != nil {
		return false, "worktree_create: " + err.Error()
	}
	ad, adName := s.loopRunAdapterLocked()
	runID, err := ad.Start(ctx, wtPath, fullPrompt)
	if err != nil {
		_ = s.mgr.Remove(wtPath)
		return false, "agent_start: " + err.Error()
	}
	// The queue-source consumption receipt lands only after adapter start
	// (V9, the parked contract): a failed start never drops a human goal.
	if len(goalSeqs) > 0 {
		row := map[string]interface{}{
			"action":    "run_prompt",
			"origin":    "loop_queue",
			"goal_seqs": goalSeqs,
			"actor":     loopActor,
		}
		for k, v := range receiptPayload {
			row[k] = v
		}
		if _, err := s.store.AppendEvent(ctx, conversationID, store.EventReviewAction, mustJSON(row)); err != nil {
			_ = ad.Cancel(ctx, runID)
			_ = s.mgr.Remove(wtPath)
			return false, "journal_queue_receipt: " + err.Error()
		}
		for _, seq := range goalSeqs {
			s.removeParkedGoalLocked(conversationID, seq)
		}
	}
	s.runs[runID] = &runMeta{
		runID:          runID,
		runDirID:       runDirID,
		adapter:        adName,
		conversationID: conversationID,
		workstreamID:   c.WorkstreamID,
		worktreePath:   wtPath,
		goal:           prompt,
		loopID:         loopID,
		loopKind:       kind,
		loopRound:      round,
		loopTask:       task,
	}
	s.byConv[conversationID] = runID
	return true, ""
}

// --- drain seams: the loop-owned pipelines after a loop run finishes --------------

// journalLoopDiffBound writes the loop⇄diff binding row (P1 #13 — the
// spawn row's old diff_id contract was dead: the diff is inserted at
// drain, not spawn, so the binding lands here). loopAdjudicateTask
// attributes the task's accept/blocked rows by it, and
// loopOwnedSeedDiffIDs excludes the diff from the boot recovery's
// pending-diff re-fire.
func (s *Server) journalLoopDiffBound(ctx context.Context, meta *runMeta, diffID int64) {
	payload := map[string]interface{}{"diff_id": diffID}
	mode := loopModeAudit
	if meta.loopKind == "implement" {
		mode = loopModeTasks
		payload["task"] = meta.loopTask
	} else if meta.loopRound > 0 {
		payload["round"] = meta.loopRound
	}
	s.journalLoopBestEffort(ctx, meta.conversationID, meta.loopID, mode, loopKindDiffBound, payload, s.loopSpent(ctx, meta.conversationID, meta.loopID))
}

// loopPipelineAfterRun is drainRun's terminal-tail branch for
// loop-provenance runs (C1: the marker skips maybeAutoLand; the loop's
// own pipeline runs instead, under loopWG).
func (s *Server) loopPipelineAfterRun(meta *runMeta, d store.Diff, verdict string) {
	ctx := context.Background()
	defer s.fireLoopTick(meta.conversationID)
	// Bind the drained diff to its loop phase FIRST (P1 #13). Journaled
	// even when the fold moved on (a stale drain): the binding is pure
	// provenance — attribution gates and the boot recovery's exclusion
	// read it regardless of the phase's liveness (a suspended loop's
	// late diff still belongs to the loop, a stopped loop's orphans are
	// filtered by the exclusion's terminal check).
	s.journalLoopDiffBound(ctx, meta, d.ID)
	if !s.loopDrainActive(ctx, meta) {
		return
	}
	if meta.errored || verdict != verdictNone {
		// Failure matrix: a tainted run (no_text/false_stop/error) never
		// feeds the loop — suspend, one re-spawn on resume.
		detail := "the fix run ended with agent_error"
		if verdict != verdictNone {
			detail = "the fix run's verdict is " + verdict + " (no reliable output)"
		}
		mode := loopModeAudit
		if meta.loopKind == "implement" {
			mode = loopModeTasks
		}
		s.journalLoopBestEffort(ctx, meta.conversationID, meta.loopID, mode, loopKindSuspended, map[string]interface{}{
			"cause":  "run_tainted",
			"detail": detail,
		}, s.loopSpent(ctx, meta.conversationID, meta.loopID))
		return
	}
	if meta.loopKind == "implement" {
		s.loopImplementPipeline(ctx, meta, d)
		return
	}
	s.loopFixPipeline(ctx, meta, d)
}

// loopDrainActive folds meta's loop: a drained loop run drives the
// loop's pipeline (journals suspension rows, auto-lands) ONLY while the
// fold still has that exact phase open. A loop that suspended (human
// interleave mid-run, P1) or stopped (cancelLoopRun) while the run was
// in flight has already journal-authored its next step — the stale
// drain must journal and land NOTHING. Without this check the cancelled
// run's taint row silently rewrites the cause (and on a stopped loop
// would flip terminal → suspended).
func (s *Server) loopDrainActive(ctx context.Context, meta *runMeta) bool {
	st, _, err := s.loopActiveState(ctx, meta.conversationID)
	if err != nil || st == nil || st.id != meta.loopID || !st.active() {
		return false
	}
	if meta.loopKind == "implement" {
		for _, t := range st.tasks {
			if t.n == meta.loopTask {
				return t.spawned && !t.done
			}
		}
		return false
	}
	// A fix run's drain is live only while the fold has the fix phase
	// open (the spawn row sans accept/blocked/suspend).
	return st.fixOpen
}

// loopNoDiffAfterRun is drainRun's no-diff branch for loop-provenance
// runs (failure matrix: no-diff fix run ⇒ suspend fix_no_diff; one
// automatic re-spawn rides the next /loop resume). The caller holds s.mu;
// journaling is store-only, same posture as journalRunVerdict.
func (s *Server) loopNoDiffAfterRun(ctx context.Context, meta *runMeta, verdict string) {
	if !s.loopDrainActive(ctx, meta) {
		return
	}
	mode := loopModeAudit
	if meta.loopKind == "implement" {
		mode = loopModeTasks
	}
	cause, detail := "fix_no_diff", "the fix run produced no diff — nothing changed to land"
	if meta.errored || verdict != verdictNone {
		cause, detail = "run_tainted", "the run ended with agent_error"
		if verdict != verdictNone {
			detail = "the run's verdict is " + verdict + " (no reliable output)"
		}
	}
	// A refusal at registration carries the most specific truth (the
	// diff existed but is structurally unlandable): it wins over both
	// default branches.
	if meta.refusalDetail != "" {
		cause, detail = "run_tainted", meta.refusalDetail
	}
	s.journalLoopBestEffort(ctx, meta.conversationID, meta.loopID, mode, loopKindSuspended, map[string]interface{}{
		"cause":  cause,
		"detail": detail,
	}, s.loopSpent(ctx, meta.conversationID, meta.loopID))
}

// loopFixPipeline is the Mode A fix round's landing path (V6): risk gate
// (protected paths / supply-chain class → suspend, the only coherent
// state for an unabsorbable fix) → runVerifyGate verbatim → journaled
// autoActor land. Verify/land failures journal blocked evidence rows
// (never suspends) and the next audit round re-audits.
func (s *Server) loopFixPipeline(ctx context.Context, meta *runMeta, d store.Diff) {
	spent := s.loopSpent(ctx, meta.conversationID, meta.loopID)
	diffText := ""
	if data, err := os.ReadFile(d.PathOnDisk); err == nil {
		diffText = string(data)
	}
	paths, perr := git.PatchPaths(d.PathOnDisk)
	// V5 protected-path gate.
	if perr == nil {
		for _, p := range paths {
			if isProtectedPath(p) {
				payload := map[string]interface{}{
					"cause":  "risk:protected_path",
					"detail": "the fix diff touches protected path " + p + " — land it manually, then /loop resume",
				}
				if diffText != "" {
					mountRiskReceipt(payload, riskReceiptKeys(diffText))
				}
				s.journalLoopBestEffort(ctx, meta.conversationID, meta.loopID, loopModeAudit, loopKindSuspended, payload, spent)
				return
			}
		}
	}
	// V5 supply-chain class gate.
	if diffText != "" {
		classes, _ := classifyRisk(diffText)
		for _, cl := range classes {
			if cl == "supply_chain" {
				payload := map[string]interface{}{
					"cause":  "risk:supply_chain",
					"detail": "the risk classifier rated the fix diff supply_chain — land it manually, then /loop resume",
				}
				mountRiskReceipt(payload, riskReceiptKeys(diffText))
				s.journalLoopBestEffort(ctx, meta.conversationID, meta.loopID, loopModeAudit, loopKindSuspended, payload, spent)
				return
			}
		}
	}
	if perr != nil {
		s.journalFixBlocked(ctx, meta, d.ID, "loop_unparseable_diff", perr.Error())
		return
	}
	gate := runVerifyGate(ctx, s.projectRoot, meta.worktreePath, paths)
	if !gate.ok {
		// Advisory, never land-blocking (V6): the blocked row is the
		// round fact the next audit prompt reads.
		s.journalFixBlocked(ctx, meta, d.ID, "loop_"+gate.reason, gate.detail)
		return
	}
	if _, err := s.handleDiffAction(ctx, d.ID, "accept", loopActor, ""); err != nil {
		s.journalFixBlocked(ctx, meta, d.ID, "loop_land_failed", err.Error())
		return
	}
}

// journalFixBlocked journals the fix pipeline's evidence row: the fix
// stays pending (the human's inbox still sees it), the fold closes the
// fix phase, and the next audit round carries the failure as a prior fact.
func (s *Server) journalFixBlocked(ctx context.Context, meta *runMeta, diffID int64, reason, detail string) {
	payload := map[string]interface{}{
		"action":  "auto_land_blocked",
		"diff_id": diffID,
		"actor":   loopActor,
		"reason":  reason,
		"detail":  capDetail(detail),
	}
	if d, err := s.store.GetDiff(ctx, diffID); err == nil {
		if data, err := os.ReadFile(d.PathOnDisk); err == nil {
			payload["patch_sha16"] = sha16(data)
			mountRiskReceipt(payload, riskReceiptKeys(string(data)))
		}
	}
	if _, err := s.store.AppendEvent(ctx, meta.conversationID, store.EventReviewAction, mustJSON(payload)); err != nil {
		log.Printf("loop: journal fix blocked (%s) for diff %d: %v", reason, diffID, err)
	}
}

// loopImplementPipeline is the Mode B task's review stage: s.autoLand
// VERBATIM (C8 — inherit, never fork), then the outcome fold.
func (s *Server) loopImplementPipeline(ctx context.Context, meta *runMeta, d store.Diff) {
	s.autoLand(ctx, d, meta.worktreePath, meta.goal, false, "")
	s.loopAdjudicateTask(ctx, meta.conversationID, meta.loopID, meta.loopTask)
}

// loopAdjudicateTask folds settle's terminal rows for one task (C8: the
// loop learns outcomes from the journal, it never duplicates the ladder):
// landed on the task's diff's accept; settle_blocked on its terminal
// blocked row with no later accept; nothing while a repair round is in
// flight. Also the rescue path for a pipeline lost to a restart (the
// recovery tick calls it when the spawn has no live run).
//
// Attribution (P1 #13) keys on the loop_diff_bound chain, never on
// wall-clock: a review row counts toward the task only when its diff
// chain-roots at a bound diff (a revise ladder's product chains via
// origin_diff_id) — or, on a pre-binding journal (no loop_diff_bound
// row anywhere for this loop), when the row passes the legacy lane:
// pipeline actor (auto_loop/auto_panel) for accept/blocked, any actor
// for reject/revise (the P2-g human-reject rescue survives). A human
// accept of an unrelated inbox diff NEVER closes the task.
func (s *Server) loopAdjudicateTask(ctx context.Context, conversationID, loopID int64, task int) {
	events, err := s.store.ListEvents(ctx, conversationID, 0)
	if err != nil {
		return
	}
	spawnSeq := 0
	taskBound := map[int64]bool{}
	hasBindings := false
	for _, ev := range events {
		if ev.Type != store.EventLoopEvent {
			continue
		}
		if int64(jsonInt(ev.Payload, "loop_id")) != loopID {
			continue
		}
		switch jsonStr(ev.Payload, "kind") {
		case loopKindTaskSpawn:
			if jsonInt(ev.Payload, "task") == task {
				spawnSeq = ev.Seq
			}
		case loopKindTaskDone:
			// An existing terminal row makes adjudication a no-op (P2):
			// re-adjudication after a restart/resume/human gate must never
			// double-journal loop_task_done for one task.
			if jsonInt(ev.Payload, "task") == task {
				return
			}
		case loopKindDiffBound:
			// P1 #13: one binding row anywhere for this loop switches
			// attribution from pre-binding (wall-clock + actor) to
			// bound-diff-only.
			hasBindings = true
			if jsonInt(ev.Payload, "task") == task {
				if id := int64(jsonInt(ev.Payload, "diff_id")); id > 0 {
					taskBound[id] = true
				}
			}
		}
	}
	if spawnSeq == 0 {
		return
	}
	// Revise-ladder edges (a ladder round's/product's diff → its origin):
	// the task's own ladder settles under the PRODUCT's id, so its
	// accept/blocked rows attribute only through the chain root.
	chainParent := map[int64]int64{}
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action        string `json:"action"`
			DiffID        int64  `json:"diff_id"`
			ProductDiffID int64  `json:"product_diff_id"`
			OriginDiffID  int64  `json:"origin_diff_id"`
		}
		if !jsonUnmarshalOK(ev.Payload, &p) {
			continue
		}
		switch p.Action {
		case "auto_revise_round":
			if p.DiffID > 0 && p.OriginDiffID > 0 && p.DiffID != p.OriginDiffID {
				chainParent[p.DiffID] = p.OriginDiffID
			}
		case "auto_revise_product":
			if p.ProductDiffID > 0 && p.OriginDiffID > 0 {
				chainParent[p.ProductDiffID] = p.OriginDiffID
			}
		}
	}
	// attributedToTask gates a settle row onto this task (P1 #13): the
	// row's diff must chain-root at one of the task's bound diffs — or,
	// on a pre-binding journal, fall back to wall-clock order.
	// humanOK=false lanes (accept, blocked) additionally require a
	// pipeline actor in the fallback; humanOK=true lanes (reject,
	// revise) preserve the pre-binding behavior for either actor (the
	// P2-g human-reject rescue).
	attributedToTask := func(diffID int64, actor string, humanOK bool) bool {
		for id, depth := diffID, 0; id > 0 && depth < 64; depth++ {
			if taskBound[id] {
				return true
			}
			next, ok := chainParent[id]
			if !ok {
				break
			}
			id = next
		}
		if hasBindings {
			return false
		}
		if humanOK {
			return true
		}
		return actor == loopActor || actor == autoActor
	}
	var landedDiff int64
	var blockedReason, blockedDetail string
	rejected := false
	reviseInFlight := false
	for _, ev := range events {
		if ev.Seq <= spawnSeq || ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action        string `json:"action"`
			Actor         string `json:"actor"`
			DiffID        int64  `json:"diff_id"`
			ProductDiffID int64  `json:"product_diff_id"`
			OriginDiffID  int64  `json:"origin_diff_id"`
			Reason        string `json:"reason"`
			Detail        string `json:"detail"`
		}
		if !jsonUnmarshalOK(ev.Payload, &p) {
			continue
		}
		switch p.Action {
		case "accept":
			// Bound chain or the pre-binding pipeline-actor fallback only
			// (P1 #13): an accept of an unrelated diff — human or auto —
			// must never resolve the task.
			if !attributedToTask(p.DiffID, p.Actor, false) {
				continue
			}
			landedDiff = p.DiffID
			blockedReason = ""
			reviseInFlight = false
		case "reject":
			// The human reject of the task's pending diff — its
			// resolution lands in the default branch (the P2-g rescue;
			// bound lane keys on the chain root).
			if !attributedToTask(p.DiffID, p.Actor, true) {
				continue
			}
			rejected = true
		case "auto_revise_round":
			if !attributedToTask(p.DiffID, p.Actor, true) {
				continue
			}
			reviseInFlight = true
			blockedReason = ""
		case "auto_revise_product":
			// The product's own settle rows arrive under product_diff_id;
			// they chain to the task through origin_diff_id.
			pid := p.ProductDiffID
			if pid == 0 {
				pid = p.DiffID
			}
			if !attributedToTask(pid, p.Actor, true) {
				continue
			}
			reviseInFlight = false // the product arrived; its own pipeline decides
		case "auto_land_blocked":
			// Same discipline as accept: the blocked row must name a diff
			// in the task's bound chain (or the journal is pre-binding
			// and the actor is pipeline automation).
			if !attributedToTask(p.DiffID, p.Actor, false) {
				continue
			}
			blockedReason, blockedDetail = p.Reason, p.Detail
			reviseInFlight = false
		}
	}
	spent := s.loopSpent(ctx, conversationID, loopID)
	switch {
	case landedDiff > 0:
		s.journalLoopBestEffort(ctx, conversationID, loopID, loopModeTasks, loopKindTaskDone, map[string]interface{}{
			"task":    task,
			"status":  loopTaskLanded,
			"diff_id": landedDiff,
		}, spent)
	case reviseInFlight:
		// The repair ladder is driving; its drains re-adjudicate via the
		// tick seam. Nothing journaled.
	case blockedReason != "":
		s.journalLoopBestEffort(ctx, conversationID, loopID, loopModeTasks, loopKindTaskDone, map[string]interface{}{
			"task":   task,
			"status": loopTaskSettleBlocked,
			"detail": capDetail(blockedReason + ": " + blockedDetail),
		}, spent)
		s.journalLoopBestEffort(ctx, conversationID, loopID, loopModeTasks, loopKindSuspended, map[string]interface{}{
			"cause":  "settle_blocked",
			"detail": fmt.Sprintf("task %d's diff was blocked in the auto-land pipeline (%s); land or reject it manually, then /loop resume", task, blockedReason),
		}, spent)
	default:
		if rejected {
			// The human inspected the orphaned pending diff after a
			// restart_mid_run suspend and REJECTED it (P2): resolve the
			// task as skipped instead of dead-ending — the next resume
			// moves to the next task, never re-suspends on this one.
			s.journalLoopBestEffort(ctx, conversationID, loopID, loopModeTasks, loopKindTaskDone, map[string]interface{}{
				"task":   task,
				"status": loopTaskSkipped,
				"detail": "the task's pending diff was rejected by the human after a restart",
			}, spent)
			return
		}
		// No rows at all after the spawn (pipeline lost mid-flight —
		// restart rescue): the task's diff may still sit pending. Treat
		// as settle-blocked evidence-free: suspend for the human.
		s.journalLoopBestEffort(ctx, conversationID, loopID, loopModeTasks, loopKindSuspended, map[string]interface{}{
			"cause":  "restart_mid_run",
			"detail": fmt.Sprintf("task %d's implement pipeline left no settle rows after a restart; inspect the pending diff, then /loop resume", task),
		}, spent)
	}
}

// --- Mode B task engine ------------------------------------------------------------

// tickLoopTasks advances the task list: design (goroutine) → gate →
// implement → adjudicate → next task. finalAudit flips when the list
// drains; from there the audit engine takes over.
func (s *Server) tickLoopTasks(ctx context.Context, conversationID int64, st *loopState, events []store.Event) tickResult {
	var t *loopTask
	for i := range st.tasks {
		if !st.tasks[i].done {
			t = &st.tasks[i]
			break
		}
	}
	if t == nil {
		return tickWait // finalAudit flips at the fold; the next pass audits
	}
	if t.designLockSeq > 0 {
		return tickWait // human design gate (loop_design_gate auto never parks here)
	}
	if t.spawned {
		// Implement run or its ladder is in flight — OR the pipeline was
		// lost (restart, or settle rows arrived without a loop drain).
		// Adjudicate inline only when NO run is live for the conversation.
		s.mu.Lock()
		live := false
		if runID, ok := s.byConv[conversationID]; ok {
			if meta := s.runs[runID]; meta != nil && !meta.finished {
				live = true
			}
		}
		s.mu.Unlock()
		if live {
			return tickWait
		}
		s.loopAdjudicateTask(ctx, conversationID, st.id, t.n)
		return tickProgress
	}
	return s.launchLoopDesign(conversationID, st, *t)
}

// launchLoopDesign starts the design goroutine for one task (design =
// runDesignMoa; its completion journals loop_design_lock and either
// spawns the implement run (gate auto) or parks for the human gate).
// Ownership hands off from the calling tick to the goroutine.
func (s *Server) launchLoopDesign(conversationID int64, st *loopState, t loopTask) tickResult {
	s.loopWG.Add(1)
	go func() {
		defer s.loopWG.Done()
		defer func() {
			s.releaseLoopOwner(conversationID)
			s.fireLoopTick(conversationID)
		}()
		s.runLoopDesign(st.id, conversationID, st, t)
	}()
	return tickHandedOff
}

// runLoopDesign executes the design stage for one task. Fail-closed
// (design_infra): every proposal leg failing or a consolidator error/
// truncation suspends the loop.
func (s *Server) runLoopDesign(loopID, conversationID int64, st *loopState, t loopTask) {
	ctx := context.Background()
	goal := "# Task\n\n" + t.text + "\n\n(This task is one step of a sequential task pipeline; design for this step only, against the repository as it stands.)"
	est := len(goal) / 4
	if st.spentTokens+est > st.budgetTokens {
		s.journalLoopBestEffort(ctx, conversationID, loopID, loopModeTasks, loopKindBudgetExceeded, map[string]interface{}{
			"budget_tokens": st.budgetTokens,
			"projected":     st.spentTokens + est,
		}, st.spentTokens)
		return
	}
	out, err := runDesignMoa(ctx, "loop_design", s.projectRoot, goal, nil, loopConsolidatorModel(), s.sharedMoa())
	if err != nil {
		s.journalLoopBestEffort(ctx, conversationID, loopID, loopModeTasks, loopKindSuspended, map[string]interface{}{
			"cause":  "design_infra",
			"detail": err.Error(),
		}, st.spentTokens)
		return
	}
	payload := map[string]interface{}{
		"task":         t.n,
		"goal":         t.text,
		"goal_sha16":   sha16([]byte(t.text)),
		"design_lock":  out.lock,
		"design_sha16": sha16([]byte(out.lock)),
		"proposals":    out.proposals,
		"consolidator": out.consolidator,
	}
	if out.droppedLegs > 0 {
		payload["dropped_legs"] = out.droppedLegs
	}
	s.spillField(payload, loopID, "design_lock", fmt.Sprintf("design-%d.md", t.n))
	spent := st.spentTokens + loopRowSpend(payload)
	s.journalLoopBestEffort(ctx, conversationID, loopID, loopModeTasks, loopKindDesignLock, payload, spent)
	if loopDesignGateAuto() {
		s.spawnLoopImplement(ctx, conversationID, loopID, t, out.lock, sha16([]byte(out.lock)), false, "")
	}
}

// spawnLoopImplement spawns a task's implement run with the approved (or
// amended) design lock. Called by the auto gate, and by loop_ctl's
// approve/amend actions. Returns whether the run started.
func (s *Server) spawnLoopImplement(ctx context.Context, conversationID, loopID int64, t loopTask, design, designSHA string, amended bool, origin string) bool {
	prompt := "# Task\n\n" + t.text + "\n\n# Approved design lock\n\nFollow this design lock; where it is silent, make the boring choice.\n\n" + design
	st, events, _ := s.loopActiveState(ctx, conversationID)
	prevSpent := 0
	if st != nil {
		prevSpent = st.spentTokens
	}
	est := len(prompt) / 4
	budget := loopBudgetTokens()
	if st != nil && st.budgetTokens > 0 {
		budget = st.budgetTokens
	}
	if prevSpent+est > budget {
		s.journalLoopBestEffort(ctx, conversationID, loopID, loopModeTasks, loopKindBudgetExceeded, map[string]interface{}{
			"budget_tokens": budget,
			"projected":     prevSpent + est,
		}, prevSpent)
		return false
	}
	spawnRow := map[string]interface{}{
		"task":         t.n,
		"task_sha16":   sha16([]byte(t.text)),
		"design_sha16": designSHA,
	}
	if amended {
		spawnRow["amended"] = true
		spawnRow["design_lock"] = design
		s.spillField(spawnRow, loopID, "design_lock", fmt.Sprintf("design-%d-amended.md", t.n))
	}
	if origin != "" {
		spawnRow["origin"] = origin
	}
	goalSeqs := loopTaskGoalSeqs(events, loopID, t.n)
	admitted, reason := s.startLoopRunLocked(ctx, conversationID, loopID, "implement", 0, t.n, prompt, spawnRow, goalSeqs, prevSpent)
	if !admitted {
		s.journalLoopBestEffort(ctx, conversationID, loopID, loopModeTasks, loopKindSuspended, map[string]interface{}{
			"cause":  "spawn_failed",
			"detail": reason,
		}, prevSpent)
		return false
	}
	return true
}

// loopTaskGoalSeqs maps a queue-source task to its parked-goal seq (V9:
// run_prompt{goal_seqs} receipts per task).
func loopTaskGoalSeqs(events []store.Event, loopID int64, task int) []int {
	for _, ev := range events {
		if ev.Type != store.EventLoopEvent || ev.Seq != int(loopID) {
			continue
		}
		seqs := jsonIntList(ev.Payload, "task_goal_seqs")
		if len(seqs) >= task && task >= 1 {
			return []int{seqs[task-1]}
		}
	}
	return nil
}

// --- recovery (V7) --------------------------------------------------------------

// recoverLoops runs in NewServer after the store opens (the
// recoverParkedGoals precedent): mid-audit/design loops (no side
// effects) re-run idempotently; loops interrupted with a fix/implement
// or design/implement phase open suspend as restart_mid_run for the
// human (the worktree may hold partial side effects).
func (s *Server) recoverLoops(ctx context.Context) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		return
	}
	wss, err := s.store.ListWorkstreams(ctx, p.ID)
	if err != nil {
		return
	}
	for _, w := range wss {
		c, err := s.store.GetActiveConversation(ctx, w.ID)
		if err != nil {
			continue
		}
		events, err := s.store.ListEvents(ctx, c.ID, 0)
		if err != nil {
			continue
		}
		st := newestFoldLoop(deriveLoopStates(events))
		if st == nil || st.terminal() || !st.active() {
			continue
		}
		// Mid-run kill (side effects possible): fix open, or a task
		// implement/design phase open without its terminal row.
		if st.fixOpen {
			s.journalLoopBestEffort(ctx, c.ID, st.id, fmtLoopMode(st), loopKindSuspended, map[string]interface{}{
				"cause":  "restart_mid_run",
				"detail": "the daemon restarted with a fix run in flight — its worktree may hold partial side effects; inspect, then /loop resume",
			}, st.spentTokens)
			continue
		}
		if st.mode == loopModeTasks && !st.finalAudit {
			openTask := false
			for _, t := range st.tasks {
				if t.spawned && !t.done {
					openTask = true
					break
				}
			}
			if openTask {
				s.journalLoopBestEffort(ctx, c.ID, st.id, loopModeTasks, loopKindSuspended, map[string]interface{}{
					"cause":  "restart_mid_run",
					"detail": "the daemon restarted with an implement run in flight — inspect its worktree/pending diff, then /loop resume",
				}, st.spentTokens)
				continue
			}
			if st.designInFlightOpen() {
				// A design goroutine is read-only (side-effect-free):
				// re-run it idempotently.
				s.journalLoopBestEffort(ctx, c.ID, st.id, loopModeTasks, loopKindRecovered, map[string]interface{}{
					"action": "reran_design",
				}, st.spentTokens)
				s.fireLoopTick(c.ID)
				continue
			}
			continue // awaiting a human design gate — no recovery work
		}
		// Audit-mode active with no live goroutine: every pending step is
		// either side-effect-free (audit legs) or journaled-before-start
		// (fix spawn — the tick's awaitingFixSpawn respawns it). Idempotent
		// re-tick.
		s.journalLoopBestEffort(ctx, c.ID, st.id, fmtLoopMode(st), loopKindRecovered, map[string]interface{}{
			"action": "reran_audit",
		}, st.spentTokens)
		s.fireLoopTick(c.ID)
	}
}

// designInFlightOpen reports whether a Mode B task needs its design
// goroutine re-run (no lock row, no spawn, not done).
func (st *loopState) designInFlightOpen() bool {
	for _, t := range st.tasks {
		if !t.done && !t.spawned && t.designLockSeq == 0 {
			return true
		}
	}
	return false
}

// --- journal read-back helpers ---------------------------------------------------

// loopSpent re-derives the loop's cumulative spend (the rows carry it;
// the fold's value is authoritative).
func (s *Server) loopSpent(ctx context.Context, conversationID, loopID int64) int {
	events, err := s.store.ListEvents(ctx, conversationID, 0)
	if err != nil {
		return 0
	}
	if st := findLoopByID(deriveLoopStates(events), loopID); st != nil {
		return st.spentTokens
	}
	return 0
}

// findLoopByID picks one loop out of the fold order.
func findLoopByID(states []*loopState, id int64) *loopState {
	for _, st := range states {
		if st.id == id {
			return st
		}
	}
	return nil
}

// loopPrevBlockingFindings reads back the previous round's blocking
// union for the closure-pass prompt (C6). Returns the findings and their
// round number (0 when none — round 1 runs without a closure section).
func (s *Server) loopPrevBlockingFindings(events []store.Event, loopID int64) ([]finding, int) {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != store.EventLoopEvent || int64(jsonInt(ev.Payload, "loop_id")) != loopID || jsonStr(ev.Payload, "kind") != loopKindAuditRound {
			continue
		}
		return s.loopRoundBlockingFindings(events, loopID, loopRound{seq: ev.Seq, round: jsonInt(ev.Payload, "round")}), jsonInt(ev.Payload, "round")
	}
	return nil, 0
}

// loopRoundBlockingFindings reconstructs one round's blocking findings
// from its journaled row (inline findings, or the spill file), then the
// row's hold_severity filter.
func (s *Server) loopRoundBlockingFindings(events []store.Event, loopID int64, r loopRound) []finding {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != store.EventLoopEvent || ev.Seq != r.seq || jsonStr(ev.Payload, "kind") != loopKindAuditRound {
			continue
		}
		union := s.loopRoundUnion(ev)
		return blockingFindings(union, jsonStr(ev.Payload, "hold_severity"))
	}
	return nil
}

// loopRoundUnion decodes a round row's union findings (inline or spilled).
func (s *Server) loopRoundUnion(ev store.Event) []finding {
	if path := jsonStr(ev.Payload, "findings_path"); path != "" {
		data, err := os.ReadFile(strings.TrimSuffix(s.projectRoot, "/") + "/" + path)
		if err != nil {
			return nil
		}
		var out []finding
		if jsonUnmarshalOK(data, &out) {
			return out
		}
		return nil
	}
	var p struct {
		Findings []finding `json:"findings"`
	}
	if jsonUnmarshalOK(ev.Payload, &p) {
		return p.Findings
	}
	return nil
}

// loopPriorFacts collects the advisory facts for the next audit prompt
// (V6): the last fix pipeline's verify/land failures.
func (s *Server) loopPriorFacts(events []store.Event, st *loopState) []string {
	if st.fixSpawnSeq == 0 {
		return nil
	}
	var facts []string
	for _, ev := range events {
		if ev.Seq <= st.fixSpawnSeq || ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
			Actor  string `json:"actor"`
			Reason string `json:"reason"`
			Detail string `json:"detail"`
		}
		if !jsonUnmarshalOK(ev.Payload, &p) || p.Action != "auto_land_blocked" || p.Actor != loopActor || !strings.HasPrefix(p.Reason, "loop_") {
			continue
		}
		facts = append(facts, fmt.Sprintf("the previous fix did not land (%s): %s", p.Reason, capDetail(p.Detail)))
	}
	return facts
}

// findingsSHA16 attests a finding set's exact content (spawn rows, the
// closure input receipt).
func findingsSHA16(fs []finding) string {
	return sha16([]byte(mustJSON(fs)))
}

// equalStringSets reports set equality (both sorted or not).
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	am := map[string]int{}
	for _, s := range a {
		am[s]++
	}
	for _, s := range b {
		am[s]--
		if am[s] < 0 {
			return false
		}
	}
	return true
}
