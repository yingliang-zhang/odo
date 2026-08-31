package ipc

// D9-W5 (lock R1 — USER RULING, binding; K3 spec §4 amended by R1): the
// project_active→rolled_back path. R1 supersedes K3 §4.2's auto-restore:
// rollback operates on the CANDIDATE LAYER ONLY — the stage demotion is
// instant and fold-derived; the daemon NEVER writes memory.md.
//
// Two layers, structurally separated:
//
//  1. CANDIDATE layer (always): marker-first
//     review_action{action:"learning_rollback"} carrying the full
//     harmful evidence tuple (epoch, retracted texts — the R2 freeze set
//     reads this row — per-rule measure rows, and per-text presence
//     outcomes), then learning_stage project_active→rolled_back. The
//     fold is the state; the candidate stops injecting/targeting
//     immediately.
//  2. MEMORY layer (only for texts that actually LANDED in memory.md —
//     the receipted canary→project_active apply): one D4 receipt per
//     present add text, memory_update{layer:"memory",
//     cause:"retract_candidate", rule, flag_seq, candidate, epoch} —
//     the exact R1 field set — flagging the line for HUMAN resolution
//     (apply_memory contradicts / `odo rules retract`). The rollback
//     row's own seq is flag_seq (the evidence citation). Texts absent
//     from memory.md get a per-text outcome on the rollback row
//     (present:false) — honest, never fabricated.
//
// Restore is bounded to the candidate's OWN delta.add by construction:
// receipts name this artifact hash; opaque lines (never in a delta),
// other candidates' adds (own hashes, own lifecycle), and human/curated
// lines are unreachable. Trigger cadence (DSF amendment): per-epoch
// measure re-measure at each main-lane distill — no second faster path.
// Novel-detection idempotence (K3 §4.1): the demoted stage removes the
// candidate from the trigger set; a re-check can never double-fire.

import (
	"context"
	"log"
	"path/filepath"
	"strconv"

	"github.com/yingliang-zhang/odo/internal/store"
)

// learningRollbackCheck is the project_active arm of the per-epoch
// measure tick: re-measure the candidate's adds against the grown
// journal, journal the measure row (cadence), and — on the first harmful
// measure — execute the R1 two-layer rollback. Zero memory.md writes at
// every exit (R1).
func (s *Server) learningRollbackCheck(ctx context.Context, mainConv store.Conversation, in learningReplayInput, cand LearningCandidate, epoch int) {
	lanes := in.laneEvents()
	since := learningStageSince(lanes, cand.ArtifactHash, "project_active")
	m := computeLearningMeasure(in, cand, since, epoch)
	m.Kind = "project_active"
	measureSeq := s.journalLearningUpdate(ctx, mainConv.ID, "measure", map[string]interface{}{
		"artifact_hash": m.ArtifactHash,
		"kind":          m.Kind,
		"epoch":         epoch,
		"window_from":   m.WindowFrom,
		"canary":        m.Canary,
		"live":          m.Live,
		"baseline":      m.Baseline,
		"rules":         m.Rules,
		"excluded":      m.Excluded,
	})
	targets := learningRollbackTargets(m)
	if len(targets) == 0 {
		return
	}

	// Presence fold (read-only): which of the candidate's OWN adds
	// actually sit in current memory.md as non-opaque rules. The restore
	// boundary (R1; the over-reach pin): receipts issue only for present
	// add texts — opaque lines, other candidates' rules, and human lines
	// are never named.
	var texts []string
	present := map[string]bool{}
	current := map[string]bool{}
	for _, r := range parseMemoryLines(readFileFull(filepath.Join(s.projectRoot, ".odo", memoryFileName))) {
		if !r.opaque && r.text != "" {
			current[normalizeRule(r.text)] = true
		}
	}
	for _, tgt := range targets {
		texts = append(texts, tgt.Rule)
		present[tgt.Rule] = current[normalizeRule(tgt.Rule)]
	}

	// Layer 1 — candidate layer, marker-first: the rollback row IS the
	// evidence tuple the R2 freeze set (learning_lint.go), the status
	// surfaces, and the receipts' flag_seq citation all key on.
	rollbackSeq := 0
	if ev, err := s.store.AppendEvent(ctx, mainConv.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":        "learning_rollback",
		"artifact_hash": cand.ArtifactHash,
		"epoch":         epoch,
		"reason":        "harmful_tuple",
		"retracted":     texts,
		"rules":         targets,
		"present":       present,
		"measure_seq":   measureSeq,
	})); err != nil {
		log.Printf("learning: journal learning_rollback: %v (rollback deferred: no marker, no demotion)", err)
		return // marker-first: an unjournaled rollback never happens
	} else {
		rollbackSeq = ev.Seq
	}
	s.journalLearningStage(ctx, mainConv.ID, cand.ArtifactHash, "project_active", "rolled_back", "harmful_tuple", map[string]interface{}{
		"epoch":        epoch,
		"rollback_seq": rollbackSeq,
	})

	// Layer 2 — memory layer, D4 receipts for the landed texts ONLY
	// (R1: {rule, flag_seq, candidate, epoch}; a human resolves via
	// apply_memory / `odo rules retract`; the daemon never deletes the
	// memory.md line itself).
	receipts := 0
	for _, tgt := range targets {
		if !present[tgt.Rule] {
			continue // not in memory.md: nothing for a human to resolve
		}
		if _, err := s.store.AppendEvent(ctx, mainConv.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":     "memory",
			"cause":     "retract_candidate",
			"rule":      tgt.Rule,
			"flag_seq":  rollbackSeq,
			"candidate": cand.ArtifactHash,
			"epoch":     epoch,
		})); err != nil {
			log.Printf("learning: journal retract_candidate (%s): %v", tgt.Rule, err)
			continue
		}
		receipts++
	}

	// Transcript advisory (the journalRunAdvisory precedent): the
	// rollback is visible where the user reads.
	short := cand.ArtifactHash
	if len(short) > 8 {
		short = short[:8]
	}
	msg := "learning rollback: candidate " + short + " rolled back (harmful tuple, epoch " +
		strconv.Itoa(epoch) + "): " + joinNonEmpty(texts) + "."
	if receipts > 0 {
		msg += " Its rule(s) landed in memory.md earlier — retract_candidate receipt(s) journaled; resolve with `odo rules retract`."
	}
	if err := s.journalRunAdvisory(ctx, mainConv.ID, msg); err != nil {
		log.Printf("learning: rollback advisory: %v", err)
	}
}

// joinNonEmpty renders the rollback's rule list for the advisory (rules
// are single lines; joined with " · ").
func joinNonEmpty(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += " · "
		}
		out += x
	}
	return out
}
