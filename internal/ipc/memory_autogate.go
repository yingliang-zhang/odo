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
	if _, err := s.applyResolvedBatch(ctx, c, batch, accepted, autoActor); err != nil {
		s.journalAutoApplyFailed(ctx, c.ID, batch.epoch, err)
	}
}

// sweepPendingBatch decides an older unconsumed batch through the panel.
// Called from distillCore before the learner — its rows are this fold's
// own bookkeeping (owned by unownedFoldGrowth's attributed set).
func (s *Server) sweepPendingBatch(ctx context.Context, c store.Conversation, w store.Workstream, models []reviewModel) {
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return
	}
	// Recovery first (2026-08-25 review follow-up P1): a marker-first apply
	// stranded by a crash is restored from its recorded bodies now — waiting
	// past this distill's fold would let a NEW apply marker retire the
	// stranded one with its layers never written (markers claim layers
	// newest-first).
	s.memMu.Lock()
	s.healMemoryFromJournalLocked(ctx, c.ID, events)
	s.memMu.Unlock()
	batch := findPendingBatch(events)
	if !batch.exists || batch.consumed {
		return
	}
	if autoApplyRefused(events, batch.epoch) {
		return // refused batch: human salvage territory (user.md overflow)
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
