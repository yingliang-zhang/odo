package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"

	"github.com/yingliang-zhang/odo/internal/store"
)

// Panel-gated memory apply (the user's standing directive: panel review
// decides, no human gate in the normal case): two distill-side passes.
//
//   - autoApplyProposals runs POST-fold (after the marker): the batch this
//     distill journaled is the pending one, its proposals already carry
//     the riding reviews — decide and consume immediately.
//   - sweepPendingBatch runs PRE-learner: an older unconsumed batch still
//     deserves its decision. Fresh gate when the batch predates the panel
//     path (verdicts journaled as a memory_gate receipt); riding reviews
//     reused when a crash between propose journal and auto-apply left a
//     gated batch behind. A batch refused by the apply core (user.md
//     overflow) stays pending for human salvage and is never re-gated —
//     re-charging the panel every distill for a decision the files still
//     can't take is pure spend.
//
// Both fail soft: errors journal and leave the batch pending; the distill
// itself never fails on a memory-pipeline fault (learner/ledger
// discipline).
//
// D4 (2026-08-28, ruling ④): user.md proposals NEVER take these paths —
// the scope hold flips them to not-accepted and journals
// memory_update{layer:"apply", cause:"scope_held_for_human"} BEFORE any
// consume; an all-user.md batch stays pending forever (human territory,
// never re-gated — the journaled hold suppresses the sweep), and the
// apply core fails closed should an auto caller ever bypass the hold.
// Accepted retract intents (intent:"retract") never write either — the
// apply core emits retract_candidate rows for human resolution.

// autoApplyProposals decides a freshly journaled batch from the reviews
// riding each proposal and consumes it via the shared apply core.
// batchProposals is the exact slice journaled this distill (indexes of
// the decision vector align with the journal row).
func (s *Server) autoApplyProposals(ctx context.Context, c store.Conversation, batchProposals []MemoryProposal, numModels int) {
	if len(batchProposals) == 0 {
		return
	}
	accepted := make([]bool, len(batchProposals))
	for i, p := range batchProposals {
		accepted[i] = panelAccepts(p.Reviews, numModels)
	}
	// The apply core consumes the JOURNAL's batch (reaffirm list included)
	// — re-read it rather than trusting the in-memory copy.
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		s.journalAutoApplyFailed(ctx, c.ID, 0, fmt.Errorf("list events: %w", err))
		return
	}
	batch := findPendingBatch(events)
	if !batch.exists || batch.consumed || len(batch.proposals) != len(batchProposals) {
		return
	}
	// D4 scope check, BEFORE any consume: user.md stays strictly
	// human-written no matter what the panel said (ruling ④).
	if s.holdUserScopeBatch(ctx, c.ID, batch, accepted) {
		return
	}
	if _, err := s.applyResolvedBatch(ctx, c, batch, accepted, autoActor); err != nil {
		s.journalAutoApplyFailed(ctx, c.ID, batch.epoch, err)
	}
}

// sweepPendingBatch decides an older unconsumed batch through the panel.
// Called from distillCore before the learner — its rows are this fold's
// own bookkeeping (owned by unownedFoldGrowth's attributed set). A batch
// already CONSUMED is out of the sweep's decision remit but not out of
// crash exposure: the consumed branch below routes its marker through the
// replay engine so a failed write is repaired (or conflicted for review)
// at sweep time, not deferred to the next apply or restart (round-4
// FIX 1).
func (s *Server) sweepPendingBatch(ctx context.Context, c store.Conversation, w store.Workstream, models []reviewModel) {
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return
	}

	batch := findPendingBatch(events)
	if !batch.exists {
		return
	}
	if batch.consumed {
		// The consumed marker may have outlived a crash before its file
		// writes: the batch is decided and gone from the sweep's remit,
		// but its recovery block is still the only replayable intent for
		// the files it lagged. Route it through the SAME engine pass the
		// apply core retries with (marker-first doctrine, round-4 FIX 1) —
		// newest-per-layer discipline over this lane's receipts — so the
		// sweep repairs or journals NOW instead of stranding the files
		// until the next manual apply or daemon restart. memMu + a fresh
		// re-read mirror handleApplyMemory's consumed branch: an unlocked
		// snapshot may lag a landed racer.
		s.memMu.Lock()
		if fresh, rerr := s.store.ListEvents(ctx, c.ID, 0); rerr == nil {
			s.replayLaneMemReceipts(ctx, c.ID, fresh, replayApply)
		}
		s.memMu.Unlock()
		return
	}
	if autoApplyRefused(events, batch.epoch) {
		return // refused batch: human salvage territory (user.md overflow)
	}
	// D4 scope check (ruling ④): an all-user.md batch never enters the
	// gate at all — hold it for the human (rows journaled once, deduped),
	// then leave; the journaled hold makes every later sweep pass return
	// here too, so a held batch never re-charges the panel (the
	// autoApplyRefused discipline for a decision the files can't take).
	if allUserScopeProposals(batch.proposals) {
		if !userScopeHeldReported(events, batch.epoch) {
			s.holdUserScopeBatch(ctx, c.ID, batch, nil)
		}
		return
	}
	accepted := make([]bool, len(batch.proposals))
	if reviewsRideOn(batch.proposals, len(models)) {
		// Gated at distill, never applied (crash before the post-fold
		// apply): decide from the journaled verdicts themselves.
		for i, p := range batch.proposals {
			accepted[i] = panelAccepts(p.Reviews, len(models))
		}
	} else {
		// Pre-panel batch: gate fresh. Receipt discipline — the verdicts
		// this decision acts on must land in the journal BEFORE the apply.
		// (2026-08-24 tri-review P0) the note text below builds the panel
		// review prompt; a planted symlink in the committable wiki/ tree
		// pointing at external bytes must degrade to "" like an unreadable
		// note, never inject them.
		noteText := ""
		if data, rerr := readWithinDir(s.projectRoot, filepath.Join(s.projectRoot, "wiki"), filepath.Join(s.projectRoot, "wiki",
			fmt.Sprintf("%s-epoch-%d.md", w.Name, batch.epoch))); rerr == nil {
			noteText = string(data)
		}
		allReviews := s.reviewProposals(ctx, batch.proposals, models, func(p MemoryProposal) string {
			return proposalReviewPrompt(p, noteText)
		})
		if _, jerr := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action":    "memory_gate",
			"epoch":     batch.epoch,
			"batch_seq": batch.seq,
			"reviews":   allReviews,
		})); jerr != nil {
			return // unjournaled verdicts never act (receipt discipline)
		}
		for i := range accepted {
			accepted[i] = panelAccepts(allReviews[i], len(models))
		}
	}
	// D4 scope check (mixed batch): the user.md proposals peel off for
	// the human; the rest apply. A fully held batch ends here, pending.
	if s.holdUserScopeBatch(ctx, c.ID, batch, accepted) {
		return
	}
	if _, err := s.applyResolvedBatch(ctx, c, batch, accepted, autoActor); err != nil {
		s.journalAutoApplyFailed(ctx, c.ID, batch.epoch, err)
	}
}

// reviewsRideOn reports whether every proposal already carries a complete
// panel fan-out (one leg per configured model) — the journaled batch is
// its own verdict receipt and no fresh gate is needed.
func reviewsRideOn(proposals []MemoryProposal, numModels int) bool {
	if len(proposals) == 0 {
		return false
	}
	for _, p := range proposals {
		if len(p.Reviews) != numModels {
			return false
		}
	}
	return true
}

// autoApplyRefused reports whether the epoch's batch already failed an
// auto-apply — the marker the sweep honors to leave the batch for human
// salvage instead of re-gating it on every distill.
func autoApplyRefused(events []store.Event, epoch int) bool {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Cause string `json:"cause"`
			Epoch int    `json:"epoch"`
		}
		if json.Unmarshal(events[i].Payload, &p) == nil && p.Cause == "auto_apply_failed" && p.Epoch == epoch {
			return true
		}
	}
	return false
}

// allUserScopeProposals reports whether every proposal in the batch
// targets user.md — the D4 human-only layer (ruling ④).
func allUserScopeProposals(proposals []MemoryProposal) bool {
	if len(proposals) == 0 {
		return false
	}
	for _, p := range proposals {
		if p.Target != "user.md" {
			return false
		}
	}
	return true
}

// userScopeHeldReported folds the hold rows for one epoch: a journaled
// scope_held_for_human marks the batch as already diverted to the human,
// so the sweep never re-gates it.
func userScopeHeldReported(events []store.Event, epoch int) bool {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
			Epoch int    `json:"epoch"`
		}
		if json.Unmarshal(events[i].Payload, &p) == nil &&
			p.Layer == "apply" && p.Cause == "scope_held_for_human" && p.Epoch == epoch {
			return true
		}
	}
	return false
}

// holdUserScopeBatch diverts user.md proposals out of the auto path (D4,
// ruling ④): each flips to not-accepted and journals
// memory_update{layer:"apply", cause:"scope_held_for_human", target,
// epoch, proposal_index, rule} — the rule text rides so the human can
// salvage it into user.md by hand. Returns true when EVERY proposal was
// held (the batch stays pending, human-only). Rows dedup per
// (epoch, proposal_index) so a crash-and-retry never double-journals;
// journal errors log and leave the batch pending for the next pass.
func (s *Server) holdUserScopeBatch(ctx context.Context, conversationID int64, batch pendingBatch, accepted []bool) bool {
	var held []int
	for i, p := range batch.proposals {
		if p.Target != "user.md" {
			continue
		}
		held = append(held, i)
		if accepted != nil {
			accepted[i] = false
		}
	}
	if len(held) == 0 {
		return false
	}
	reported := map[int]bool{}
	if events, err := s.store.ListEvents(ctx, conversationID, 0); err == nil {
		for _, ev := range events {
			if ev.Type != store.EventMemoryUpdate {
				continue
			}
			var p struct {
				Layer         string `json:"layer"`
				Cause         string `json:"cause"`
				Epoch         int    `json:"epoch"`
				ProposalIndex int    `json:"proposal_index"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && p.Layer == "apply" &&
				p.Cause == "scope_held_for_human" && p.Epoch == batch.epoch {
				reported[p.ProposalIndex] = true
			}
		}
	}
	for _, i := range held {
		if reported[i] {
			continue
		}
		if _, err := s.store.AppendEvent(ctx, conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":          "apply",
			"cause":          "scope_held_for_human",
			"target":         "user.md",
			"epoch":          batch.epoch,
			"proposal_index": i,
			"rule":           batch.proposals[i].Rule,
		})); err != nil {
			log.Printf("distill: journal scope_held_for_human (epoch %d, proposal %d): %v", batch.epoch, i, err)
		}
	}
	return len(held) == len(batch.proposals)
}

// journalAutoApplyFailed records a failed auto-decision consume (user.md
// overflow refusal, a failed write, …). Cancel-free: the caller's ctx may
// be the reason the fold is unwinding. The epoch key lets the sweep skip
// re-gating this batch — a refusal is a file-state fact, not a verdict.
func (s *Server) journalAutoApplyFailed(ctx context.Context, conversationID int64, epoch int, cause error) {
	_, _ = s.store.AppendEvent(context.WithoutCancel(ctx), conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":  "apply",
		"cause":  "auto_apply_failed",
		"epoch":  epoch,
		"detail": cause.Error(),
	}))
}

// batchSupersededReported folds existing batch_superseded rows for epoch:
// the supersede journal is idempotent — one row per superseded batch per
// journal, so a replay (a hand-restored journal, a test re-fold) never
// double-counts the orphan.
func batchSupersededReported(events []store.Event, epoch int) bool {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
			Epoch int    `json:"epoch"`
		}
		if json.Unmarshal(events[i].Payload, &p) == nil &&
			p.Layer == "learner" && p.Cause == "batch_superseded" && p.Epoch == epoch {
			return true
		}
	}
	return false
}

// journalBatchSuperseded closes the ledger on an orphaned proposal batch:
// the just-journaled distill marker (newEpoch) re-pinned the pending epoch
// to newEpoch−1, so an OLDER unconsumed batch — left by a crash between
// propose journal and apply, or sweeper-refused via user.md overflow —
// falls out of every future findPendingBatch scan: its proposals, learner
// spend, and panel reviews would vanish journal-silent (ADR-0003
// gate-theater blind spot, P0-4). prev is the PRE-marker batch, snapshotted
// before the marker appended (the old pin stops resolving the moment the
// marker lands). A consumed batch never reports — its memory_apply row IS
// the close-out. A refused-then-superseded batch still gets this one row:
// the refusal explains why it pended, the supersede closes it (the detail
// names the auto_apply_failed row). The dedup folds the CURRENT journal,
// never the caller's snapshot, so the row stays idempotent under any
// replay (crash-recovery, a hand-restored journal); best-effort with a
// log on failure and cancel-free, same discipline as
// journalAutoApplyFailed: the fold committed at the marker, so the row
// must not die with a dropped client.
func (s *Server) journalBatchSuperseded(ctx context.Context, conversationID int64, prev pendingBatch, newEpoch int) {
	if !prev.exists || prev.consumed || prev.epoch == newEpoch-1 {
		// consumed: the apply row closed the batch; epoch match: the
		// marker kept the same pin (defensive — IncrementEpoch moves past
		// the old marker in every reachable path).
		return
	}
	events, err := s.store.ListEvents(context.WithoutCancel(ctx), conversationID, 0)
	if err != nil {
		log.Printf("distill: batch_superseded dedup scan: list events: %v", err)
		return
	}
	if batchSupersededReported(events, prev.epoch) {
		return
	}
	detail := fmt.Sprintf("%d proposal(s) from epoch %d superseded by distill epoch %d",
		len(prev.proposals), prev.epoch, newEpoch)
	if autoApplyRefused(events, prev.epoch) {
		detail += "; auto_apply_failed recorded"
	}
	if _, err := s.store.AppendEvent(context.WithoutCancel(ctx), conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":  "learner",
		"cause":  "batch_superseded",
		"epoch":  prev.epoch,
		"detail": detail,
	})); err != nil {
		log.Printf("distill: journal batch_superseded (epoch %d): %v", prev.epoch, err)
	}
}
