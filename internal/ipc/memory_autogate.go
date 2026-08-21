package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
		noteText := ""
		if data, rerr := os.ReadFile(filepath.Join(s.projectRoot, "wiki",
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
