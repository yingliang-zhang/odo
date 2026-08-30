package ipc

// D9-W3 (lock docs/design/d9-learning-control-plane-lock.md; detailed spec
// docs/design/learning-control-plane-d9.md §1.1): pure observability.
// One review_action{action:"learning_episode"} row per lane per distill,
// journaled at the distillCore tail (after journalDistillLedger), computed
// as a PURE fold over the marker-pinned window [first_seq, last_seq] — the
// fold consumes the same pin the note rendered, never re-derives it.
//
// Zero behavior change: no existing decision path reads these rows. The
// rules-audit attribution machinery (rulesScanConversation /
// rulesMapDiffTerminal / rulesConvOutcomes) is reused VERBATIM for the
// weak-reject map and the cohort join — second conventions are
// prohibited. panel_infra blocks are context counts only (D7: infra is
// not a verdict); unblockable/infra blocked reasons are never outcome
// evidence.
//
// Determinism pin: same (events, diffs, params) ⇒ byte-identical row.
// The fold never reads a clock: distill_ms arrives as a parameter (the
// same value the distill marker journals), timestamps never enter the
// payload. Fixture tests recompute and pin the bytes.

import (
	"context"
	"log"
	"sort"

	"github.com/yingliang-zhang/odo/internal/store"
)

// learningEpisodeAction is the journaled review_action discriminator.
const learningEpisodeAction = "learning_episode"

// learningEpisodeParams carries the seam-supplied values the fold cannot
// (and must not) derive from the journal window itself: the distill's
// epoch/workstream attribution and the JUST-JOURNALED marker's duration.
// Passing them in keeps the fold clock-free.
type learningEpisodeParams struct {
	epoch      int
	workstream string
	firstSeq   int
	lastSeq    int
	distillMS  int64
}

// learningEpisodeOutcomeKeys is the FIXED key set of the row's outcomes
// map — every key is always emitted (zero-valued included) so consumers
// never branch on presence.
var learningEpisodeOutcomeKeys = []string{
	"accepted", "rejected", "weak_rejected",
	"auto_accepted", "auto_rejected",
	"verify_failed", "panel_mixed", "panel_minority_reject",
	"revise_rounds_spawned", "revise_landed", "ladder_suspended", "revise_no_progress",
	"agent_errors", "false_stops", "no_texts", "human_reverts",
}

// foldLearningEpisode computes one learning_episode row over the pinned
// window. events is the conversation's seq-ascending journal slice from
// which the note's window was pinned (the fold filters to [first,last]
// itself so the pin can never drift from a caller's slicing); diffs is the
// conversation's full diff table (needed by the verbatim cohort join —
// attribution partner of the send/terminal scans).
func foldLearningEpisode(events []store.Event, diffs []store.Diff, p learningEpisodeParams) map[string]interface{} {
	// Window slice: pinned bounds only; distill markers and everything
	// outside [firstSeq,lastSeq] are not content (windowEvents doctrine —
	// marker rows are bookkeeping, never note content, and land above
	// their own last_seq, so a LATER window can cover this row without
	// this fold ever counting it).
	var win []store.Event
	for _, ev := range events {
		if ev.Seq < p.firstSeq || ev.Seq > p.lastSeq {
			continue
		}
		if isDistillMarkerEvent(ev) {
			continue
		}
		win = append(win, ev)
	}

	out := map[string]int{}
	for _, k := range learningEpisodeOutcomeKeys {
		out[k] = 0
	}
	ctxCounts := map[string]int{"panel_infra": 0, "blocked_other": 0, "diff_less_terminals": 0, "attribution_lost": 0}
	var flagsEmitted []int
	var verifyMsTotal int64
	usage := map[string]interface{}{
		"available": false, "input": 0, "output": 0,
		"cache_read": 0, "cache_write": 0, "cost_usd": float64(0),
	}
	addUsage := func(avail bool, in, outT, cr, cw int, cost float64) {
		if !avail {
			return
		}
		usage["available"] = true
		usage["input"] = usage["input"].(int) + in
		usage["output"] = usage["output"].(int) + outT
		usage["cache_read"] = usage["cache_read"].(int) + cr
		usage["cache_write"] = usage["cache_write"].(int) + cw
		usage["cost_usd"] = usage["cost_usd"].(float64) + cost
	}

	// Pass 1: revise-chain product ids (a later accept of one of these is
	// ladder convergence). product rows journal at the drain, accepts
	// after — but seq order within a window is not a promise the fold
	// makes, so the set is gathered before outcome counting.
	productDiffs := map[int64]bool{}
	for _, ev := range win {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var rp struct {
			Action    string `json:"action"`
			ProductID int64  `json:"product_diff_id"`
		}
		if jsonUnmarshalOK(ev.Payload, &rp) && rp.Action == "auto_revise_product" && rp.ProductID != 0 {
			productDiffs[rp.ProductID] = true
		}
	}

	// rules audit verbatim: the window's parsed scan feeds BOTH the weak
	// map (rulesConvOutcomes' own rule — moa consensus "reject" with no
	// later human accept/reject on the same diff, only AutoActor excluded
	// from "human") and the cohort join below.
	cs := rulesScanConversation(win)
	// Verbatim weak-map (rulesConvOutcomes, rules_audit.go): humanSeq is
	// the latest accept/reject seq per diff from a NON-AutoActor actor.
	humanSeq := map[int64]int{}
	for _, r := range cs.reviews {
		if (r.action == "accept" || r.action == "reject") && r.actor != AutoActor {
			if r.seq > humanSeq[r.diffID] {
				humanSeq[r.diffID] = r.seq
			}
		}
	}
	weak := map[int]bool{}
	for _, r := range cs.reviews {
		if r.action == "moa_review" && r.consensus == "reject" && humanSeq[r.diffID] <= r.seq {
			weak[r.seq] = true
		}
	}

	// Pass 2: outcome + context counts.
	for _, ev := range win {
		switch ev.Type {
		case store.EventReviewAction:
			var rp struct {
				Action   string `json:"action"`
				Actor    string `json:"actor"`
				Reason   string `json:"reason"`
				DiffID   int64  `json:"diff_id"`
				VerifyMS int64  `json:"verify_ms"`
			}
			if !jsonUnmarshalOK(ev.Payload, &rp) {
				continue
			}
			switch rp.Action {
			case "accept":
				// Auto split: the pipeline actors resolve nothing human.
				// auto_panel accepts land via the auto-land path; pre-
				// reroute loop accepts (auto_loop) are the same class —
				// current loops ride auto_panel rows (loop_journal.go),
				// so auto_loop is the legacy twin. NOT the rules-audit
				// pool: this is a factual actor ledger, no verdicts move.
				if rp.Actor == AutoActor || rp.Actor == loopActor {
					out["auto_accepted"]++
				} else {
					out["accepted"]++
				}
				if productDiffs[rp.DiffID] {
					out["revise_landed"]++
				}
			case "reject":
				// Auto split as above (verbatim pair).
				if rp.Actor == AutoActor || rp.Actor == loopActor {
					out["auto_rejected"]++
				} else {
					out["rejected"]++
				}
			case "moa_review":
				if weak[ev.Seq] {
					out["weak_rejected"]++
				}
			case "auto_revise_round":
				out["revise_rounds_spawned"]++
			case "memory_audit_flag":
				flagsEmitted = append(flagsEmitted, ev.Seq)
			case "auto_land_blocked":
				switch rp.Reason {
				case "verify_failed":
					out["verify_failed"]++
				case "panel_mixed":
					out["panel_mixed"]++
				case "panel_minority_reject":
					out["panel_minority_reject"]++
				case "revise_no_progress":
					out["revise_no_progress"]++
				case "ladder_suspended":
					// Demotion ledger lives on the memory_update
					// (layer:auto_land, cause:ladder_suspended) row and
					// is counted below; the blocked row is its paired
					// duplicate — counting both would double the tally.
				case "panel_infra":
					// D7: infra is not a verdict — context only.
					ctxCounts["panel_infra"]++
				default:
					ctxCounts["blocked_other"]++
				}
			}
			verifyMsTotal += rp.VerifyMS
		case store.EventMemoryUpdate:
			var mp struct {
				Layer   string  `json:"layer"`
				Cause   string  `json:"cause"`
				Verdict string  `json:"verdict"`
				Avail   bool    `json:"usage_available"`
				Input   int     `json:"input_tokens"`
				Output  int     `json:"output_tokens"`
				CacheR  int     `json:"cache_read_tokens"`
				CacheW  int     `json:"cache_write_tokens"`
				Cost    float64 `json:"cost_usd"`
			}
			if !jsonUnmarshalOK(ev.Payload, &mp) {
				continue
			}
			switch {
			case mp.Layer == "run_verdict" && mp.Verdict == verdictFalseStop:
				out["false_stops"]++
			case mp.Layer == "run_verdict" && mp.Verdict == verdictNoText:
				out["no_texts"]++
			case mp.Layer == "apply" && mp.Cause == "revert":
				// The diff-revert undo rows (memory_revert.go); the
				// streak heuristic's mirror-pair detection
				// (autonomy.go) is computed on the fly with patch
				// bytes — nothing journaled, nothing to fold here.
				out["human_reverts"]++
			case mp.Layer == "auto_land" && mp.Cause == "ladder_suspended":
				out["ladder_suspended"]++
			case mp.Layer == "run_usage":
				addUsage(mp.Avail, mp.Input, mp.Output, mp.CacheR, mp.CacheW, mp.Cost)
			}
		case store.EventLoopEvent:
			var lp struct {
				Kind   string  `json:"kind"`
				Avail  bool    `json:"usage_available"`
				Input  int     `json:"input_tokens"`
				Output int     `json:"output_tokens"`
				CacheR int     `json:"cache_read_tokens"`
				CacheW int     `json:"cache_write_tokens"`
				Cost   float64 `json:"cost_usd"`
			}
			if jsonUnmarshalOK(ev.Payload, &lp) && lp.Kind == loopKindRunUsage {
				addUsage(lp.Avail, lp.Input, lp.Output, lp.CacheR, lp.CacheW, lp.Cost)
			}
		case store.EventAgentError:
			// Errored-run terminals ONLY: journalRunAdvisory rows carry
			// odo:true (transcript-visible advisories, not failures) and
			// panel/vision one-shots carry their markers (rules-audit
			// exclusion precedent — they are panel infra, not lanes).
			var ap struct {
				Odo    bool `json:"odo"`
				Panel  bool `json:"panel"`
				Vision bool `json:"vision"`
			}
			if jsonUnmarshalOK(ev.Payload, &ap) && !ap.Odo && !ap.Panel && !ap.Vision {
				out["agent_errors"]++
			}
		}
	}

	// diff_less_terminals (attribution boundaries per §1.1): in-window
	// agent_done terminals no diff claimed. Errored terminals are already
	// agent_errors (their reviews are boundary noise in the audit); panel/
	// vision one-shots are infra. rulesMapDiffTerminal is the verbatim
	// claim rule over the conversation's diff table.
	termSeq := rulesMapDiffTerminal(cs.terminals, diffs)
	claimed := make(map[int]bool, len(termSeq))
	for _, t := range termSeq {
		claimed[t] = true
	}
	for _, t := range cs.terminals {
		if !t.errored && !claimed[t.seq] {
			ctxCounts["diff_less_terminals"]++
		}
	}

	// Cohort join (verbatim): in-window send → terminal → diff → outcome
	// per the rules audit. Outcomes whose send/terminal predates the
	// window are un-attributable IN THIS WINDOW — raw outcome counts
	// above still record them; attribution_lost reconciles the identity.
	// Auto resolutions never enter cohorts (M17 F5: they grade nothing).
	cohortOutcomes := rulesConvOutcomes(cs, diffs, 0)
	type cohortAgg struct {
		outcomes, accepts, rejects, weak int
	}
	byHash := map[string]*cohortAgg{}
	memFree := 0
	for _, o := range cohortOutcomes {
		switch o.kind {
		case "accept", "reject", "weak_reject":
		default:
			continue // auto_* — excluded from cohorts
		}
		if o.memHash == "" {
			memFree++
			continue
		}
		agg := byHash[o.memHash]
		if agg == nil {
			agg = &cohortAgg{}
			byHash[o.memHash] = agg
		}
		agg.outcomes++
		switch o.kind {
		case "accept":
			agg.accepts++
		case "reject":
			agg.rejects++
		case "weak_reject":
			agg.weak++
		}
	}
	if memFree > 0 {
		ctxCounts["memory_free_outcomes"] = memFree
	}
	resolved := 0
	for _, o := range cohortOutcomes {
		if o.kind == "accept" || o.kind == "reject" || o.kind == "weak_reject" {
			resolved++
		}
	}
	if lost := out["accepted"] + out["rejected"] + out["weak_rejected"] - resolved; lost > 0 {
		ctxCounts["attribution_lost"] = lost
	}
	hashes := make([]string, 0, len(byHash))
	for h := range byHash {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	cohorts := make([]map[string]interface{}, 0, len(hashes))
	for _, h := range hashes {
		agg := byHash[h]
		cohorts = append(cohorts, map[string]interface{}{
			"sha16": h, "outcomes": agg.outcomes,
			"accepts": agg.accepts, "rejects": agg.rejects, "weak": agg.weak,
		})
	}

	if flagsEmitted == nil {
		flagsEmitted = []int{}
	}
	return map[string]interface{}{
		"action":          learningEpisodeAction,
		"epoch":           p.epoch,
		"workstream":      p.workstream,
		"window":          map[string]interface{}{"first_seq": p.firstSeq, "last_seq": p.lastSeq},
		"outcomes":        out,
		"context":         ctxCounts,
		"cohorts":         cohorts,
		"flags_emitted":   flagsEmitted,
		"usage":           usage,
		"verify_ms_total": verifyMsTotal,
		"distill_ms":      p.distillMS,
	}
}

// journalLearningEpisode writes the W3 episode row for one finished
// distill. Best-effort (journalDistillLedger precedent): the fold is
// recomputed each epoch, so a lost row is a display gap, never a state
// corruption — the distill itself must never fail on bookkeeping.
func (s *Server) journalLearningEpisode(ctx context.Context, c store.Conversation, events []store.Event, diffs []store.Diff, p learningEpisodeParams) {
	row := foldLearningEpisode(events, diffs, p)
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(row)); err != nil {
		log.Printf("distill: learning episode for conversation %d: %v", c.ID, err)
	}
}
