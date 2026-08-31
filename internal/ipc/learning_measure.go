package ipc

// D9-W5 (design lock stage machine canary→project_active + §4.1 rollback
// trigger; K3 spec §3.4 promotion predicate, §5.3 evidence→measure→gate):
// the per-epoch MEASURE fold — the only reader of raw evidence that feeds
// stage transitions — plus the two pure gate predicates (canary promotion,
// project_active rollback targets).
//
// §5.3 separation (structural, review-pinned):
//  1. EVIDENCE: raw journal rows (sends, terminals, reviews, receipts,
//     snapshots) — only computeLearningMeasure reads them.
//  2. MEASURE: this file's pure fold, journaled per epoch as
//     memory_update{layer:"learning", cause:"measure"} (learningMeasureTick).
//  3. GATE: learningPromotionVerdict / learningRollbackTargets take the
//     measure struct, NEVER []store.Event — the evidence→gate shortcut is
//     unrepresentable in code (the AST pin in learning_measure_test.go).
//
// Never-score-own-changes (lock §5 — W5 completion): BOTH cohorts of the
// promotion check exclude auto_* outcomes, OTHER candidates' canary
// traffic, and scoring-excluded diffs (gate-source / C0 / memory-path via
// the shared learningScoringClassify — fail-closed here like the frozen
// replay: this is a gate context, not the live audit's legacy posture).
// A candidate never grades itself against traffic its own plane
// generated, at either leg of the paired contrast.

import (
	"fmt"
	"sort"

	"github.com/yingliang-zhang/odo/internal/store"
)

// learningPromotionMinOutcomes is the paired-cohort resolution floor for
// canary→project_active (K3 §3.4: the threshold mirrors
// rulesFlagMinInjections — one constant family, deliberately).
const learningPromotionMinOutcomes = 10

// learningPromotionRejectFactorN/D bounds the canary reject rate against
// the live contrast: canary ≤ live × 5/4 (the locked 1.25 — integer
// cross-multiplication only, the rules_audit.go:553-556 float-trap
// precedent; N/D keeps the comparison division-free).
const (
	learningPromotionRejectFactorN = 5
	learningPromotionRejectFactorD = 4
)

// learningTaintAllowancePP is the taint gap the canary may carry over the
// live contrast, in percentage points (K3 §3.4 "≤ live + 5pp").
const learningTaintAllowancePP = 5

// learningStallMainEpochs is the W5 stall-advisory age floor (task §3):
// a candidate aging this many MAIN-lane epochs without meeting its
// stage's next-step minimums surfaces one journaled advisory — never an
// auto-promotion, never an auto-drop.
const learningStallMainEpochs = 12

// learningCohortStats is one cohort's outcome/taint tally (the two legs
// of the paired promotion contrast share the shape).
type learningCohortStats struct {
	Outcomes      int     `json:"outcomes"`
	Accepts       int     `json:"accepts"`
	Rejects       int     `json:"rejects"`
	WeakRejects   int     `json:"weak_rejects"`
	Conversations []int64 `json:"conversations"` // sorted, distinct (distinctness by construction)
	Sends         int     `json:"sends"`
	ErroredSends  int     `json:"errored_sends"`
}

// learningRuleMeasure is one candidate-add rule's scored row inside the
// measure window (the harmful legs are pre-computed over the measure's
// own baseline pool so the gate predicates are pure comparisons).
type learningRuleMeasure struct {
	Rule                string `json:"rule"`
	Injections          int    `json:"injections"`
	Accepts             int    `json:"accepts"`
	Rejects             int    `json:"rejects"`
	WeakRejects         int    `json:"weak_rejects"`
	RejectConversations int    `json:"reject_conversations"`
	// Harmful is the exact rules-audit harmful tuple (rules_audit.go:94-97
	// constants, integer cross-multiplication, measure-pool baseline).
	Harmful bool `json:"harmful"`
}

// learningExclusionCounters keeps the never-score drop honest (the
// audit's canary_outcomes/scoring_excluded line convention).
type learningExclusionCounters struct {
	Auto            int `json:"auto"`
	OtherCanary     int `json:"other_canary"`
	ScoringExcluded int `json:"scoring_excluded"`
	Unresolved      int `json:"unresolved_cohort"` // snapshot row absent: skipped, never interpolated
}

// learningCohortMeasure is the journaled epoch measure for one candidate:
// the window, both paired cohorts, per-add rule rows, and the exclusion
// counts. Deterministic: folds the gathered replay input; the double-
// execution pin in the test suite asserts byte-identical marshals.
type learningCohortMeasure struct {
	ArtifactHash string                    `json:"artifact_hash"`
	Kind         string                    `json:"kind"` // "canary" | "project_active"
	Epoch        int                       `json:"epoch"`
	WindowFrom   string                    `json:"window_from"` // created_at lower bound ("" = unbounded)
	Canary       learningCohortStats       `json:"canary"`
	Live         learningCohortStats       `json:"live"`
	Rules        []learningRuleMeasure     `json:"rules"`
	Baseline     learningCohortStats       `json:"baseline"` // canary ∪ live (the harmful legs' rate pool)
	Excluded     learningExclusionCounters `json:"excluded"`
}

// learningOutcomeKind classifies a rulesOutcome for the measure cohorts.
type learningOutcomeKind int

const (
	learningOutcomeCanary      learningOutcomeKind = iota // this candidate's canary block
	learningOutcomeOtherCanary                            // another artifact's canary block (isolated)
	learningOutcomeLive
)

// learningStageSince folds the created_at of the newest learning_stage row
// flipping artifact hash INTO one of the given stages — the cross-lane
// window bound (per-lane seqs are incomparable; created_at is the one
// project-wide clock the journal carries).
func learningStageSince(lanes [][]store.Event, hash string, stages ...string) string {
	want := map[string]bool{}
	for _, s := range stages {
		want[s] = true
	}
	since := ""
	var bestID int64 = -1
	for _, events := range lanes {
		for _, ev := range events {
			if ev.Type != store.EventReviewAction {
				continue
			}
			var p struct {
				Action string `json:"action"`
				Hash   string `json:"artifact_hash"`
				To     string `json:"to"`
			}
			if !jsonUnmarshalOK(ev.Payload, &p) || p.Action != "learning_stage" || p.Hash != hash || !want[p.To] {
				continue
			}
			if ev.ID > bestID {
				bestID, since = ev.ID, ev.CreatedAt
			}
		}
	}
	return since
}

// computeLearningMeasure is the §5.3 measure fold (evidence → measure;
// pure over the gathered replay input — the one sanctioned evidence
// reader feeding learning gates). since bounds the window on every lane
// (lexicographic on the uniform datetime('now') stamps; "" = whole
// journal).
func computeLearningMeasure(in learningReplayInput, cand LearningCandidate, since string, epoch int) learningCohortMeasure {
	m := learningCohortMeasure{
		ArtifactHash: cand.ArtifactHash,
		Epoch:        epoch,
		WindowFrom:   since,
	}

	// Cohort content tables (one snapshot family per provenance):
	// layer:"memory" snapshots give live block rule sets; layer:
	// "learning_canary" snapshots name their artifact per block sha.
	// An add-carrying project_active candidate's rules live inside
	// memory.md blocks; a canary candidate's live only inside its own
	// pinned canary block.
	liveBlocks := map[string]map[string]bool{} // sha -> rule set
	canaryOf := map[string]string{}            // sha -> owning artifact hash
	canaryBlocks := map[string]string{}        // sha -> pinned content (any artifact)
	for _, lane := range in.lanes {
		for _, ev := range lane.events {
			if ev.Type != store.EventMemoryUpdate {
				continue
			}
			var p struct {
				Layer   string `json:"layer"`
				Cause   string `json:"cause"`
				Hash    string `json:"artifact_hash"`
				Sha     string `json:"sha"`
				Content string `json:"content"`
			}
			if !jsonUnmarshalOK(ev.Payload, &p) || p.Cause != "snapshot" || p.Sha == "" {
				continue
			}
			switch p.Layer {
			case "memory":
				if _, seen := liveBlocks[p.Sha]; !seen {
					liveBlocks[p.Sha] = rulesOfContent(p.Content)
				}
			case "learning_canary":
				canaryOf[p.Sha] = p.Hash
				if _, seen := canaryBlocks[p.Sha]; !seen {
					canaryBlocks[p.Sha] = p.Content
				}
			}
		}
	}

	classify := func(memHash string) learningOutcomeKind {
		if owner, ok := canaryOf[memHash]; ok {
			if owner == cand.ArtifactHash {
				return learningOutcomeCanary
			}
			return learningOutcomeOtherCanary
		}
		return learningOutcomeLive
	}

	convSeen := map[string]map[int64]bool{"canary": {}, "live": {}}
	noteConv := func(which string, id int64) {
		if !convSeen[which][id] {
			convSeen[which][id] = true
		}
	}
	// Rule tallies (normalized rule text -> accumulator).
	type ruleAcc struct {
		inj, acc, rej, weak int
		rejConvs            map[int64]bool
	}
	ruleTally := map[string]*ruleAcc{}
	addNorm := map[string]string{} // normalized -> verbatim
	for _, a := range cand.Delta.Add {
		if n := normalizeRule(a.Rule); n != "" {
			addNorm[n] = a.Rule
			ruleTally[n] = &ruleAcc{rejConvs: map[int64]bool{}}
		}
	}

	for _, lane := range in.lanes {
		var window []store.Event
		for _, ev := range lane.events {
			if since == "" || ev.CreatedAt >= since {
				window = append(window, ev)
			}
		}
		cs := rulesScanConversation(window)

		// Taint: each send's terminal is the first terminal after it and
		// before the next send (chain attribution, the rulesConvOutcomes
		// window discipline).
		sends := cs.sends
		for i, sd := range sends {
			end := int(^uint(0) >> 1)
			if i+1 < len(sends) {
				end = sends[i+1].seq
			}
			var term *rulesTerminalInfo
			for ti := range cs.terminals {
				if cs.terminals[ti].seq > sd.seq && cs.terminals[ti].seq < end {
					if term == nil || cs.terminals[ti].seq < term.seq {
						t := cs.terminals[ti]
						term = &t
					}
				}
			}
			if term == nil {
				continue // pending run: no verdict, no taint
			}
			switch classify(sd.memHash) {
			case learningOutcomeCanary:
				m.Canary.Sends++
				if term.errored {
					m.Canary.ErroredSends++
				}
			case learningOutcomeLive:
				m.Live.Sends++
				if term.errored {
					m.Live.ErroredSends++
				}
			case learningOutcomeOtherCanary:
				// another experiment's traffic: isolated from both legs
			}
		}

		// Outcomes: skip auto (M17 F5 posture everywhere), bucket the
		// rest; the producing diff's scoring class gates metric entry
		// (fail-closed: unreadable ⇒ excluded, the frozen-replay posture).
		excludedDiffs := map[int64]bool{}
		for _, d := range lane.diffs {
			if ex, _ := learningScoringClassify(d.PathOnDisk); ex {
				excludedDiffs[d.ID] = true
			}
		}
		for _, o := range rulesConvOutcomes(cs, lane.diffs, lane.convID) {
			human := true
			switch o.kind {
			case "auto_accept", "auto_reject":
				m.Excluded.Auto++
				human = false
			case "accept", "reject", "weak_reject":
			default:
				human = false
			}
			if !human {
				continue
			}
			kind := classify(o.memHash)
			switch kind {
			case learningOutcomeOtherCanary:
				m.Excluded.OtherCanary++
				continue
			case learningOutcomeLive:
				if excludedDiffs[o.diffID] {
					m.Excluded.ScoringExcluded++
					continue
				}
			case learningOutcomeCanary:
				if excludedDiffs[o.diffID] {
					m.Excluded.ScoringExcluded++
					continue
				}
			}
			// Rule-set resolution: the outcome's cohort content must be
			// journaled — skipped and counted otherwise (never
			// interpolated; honesty over coverage). The CANARY cohort
			// resolves against the pinned block the run actually carried
			// (not the artifact's current bytes — tamper-honest).
			var ruleSet map[string]bool
			switch {
			case o.memHash == "":
				// memory-free outcome: no rule signal, still a cohort data
				// point (the audit's memFree convention).
			case kind == learningOutcomeCanary:
				if content, ok := canaryBlocks[o.memHash]; ok {
					ruleSet = rulesOfContent(content)
				} else {
					m.Excluded.Unresolved++
					continue
				}
			default:
				if set, ok := liveBlocks[o.memHash]; ok {
					ruleSet = set
				} else {
					m.Excluded.Unresolved++
					continue
				}
			}
			st := &m.Live
			which := "live"
			if kind == learningOutcomeCanary {
				st = &m.Canary
				which = "canary"
			}
			st.Outcomes++
			noteConv(which, o.convID)
			switch o.kind {
			case "accept":
				st.Accepts++
			case "reject":
				st.Rejects++
			case "weak_reject":
				st.WeakRejects++
			}
			for n := range ruleTally {
				if o.memHash == "" || !containsNormalized(ruleSet, n) {
					continue
				}
				a := ruleTally[n]
				a.inj++
				switch o.kind {
				case "accept":
					a.acc++
				case "reject":
					a.rej++
					a.rejConvs[o.convID] = true
				case "weak_reject":
					a.weak++
				}
			}
		}
	}

	// Distinct sorted conversation ids (deterministic journal output).
	for i, which := range []string{"canary", "live"} {
		st := &m.Canary
		if i == 1 {
			st = &m.Live
		}
		for id := range convSeen[which] {
			st.Conversations = append(st.Conversations, id)
		}
		sort.Slice(st.Conversations, func(a, b int) bool { return st.Conversations[a] < st.Conversations[b] })
	}

	// Baseline pool = canary ∪ live (the harmful legs' rate pool — the
	// only traffic that can carry THIS candidate's rules is already
	// inside; both legs already exclusion-clean).
	m.Baseline = learningCohortStats{
		Outcomes:     m.Canary.Outcomes + m.Live.Outcomes,
		Accepts:      m.Canary.Accepts + m.Live.Accepts,
		Rejects:      m.Canary.Rejects + m.Live.Rejects,
		WeakRejects:  m.Canary.WeakRejects + m.Live.WeakRejects,
		Sends:        m.Canary.Sends + m.Live.Sends,
		ErroredSends: m.Canary.ErroredSends + m.Live.ErroredSends,
	}
	var rulesList []learningRuleMeasure
	for n, a := range ruleTally {
		rm := learningRuleMeasure{
			Rule: addNorm[n], Injections: a.inj, Accepts: a.acc, Rejects: a.rej,
			WeakRejects: a.weak, RejectConversations: len(a.rejConvs),
		}
		bInj := m.Baseline.Outcomes
		if rm.Injections >= rulesFlagMinInjections &&
			rm.Rejects >= rulesFlagMinRejects &&
			rm.RejectConversations >= rulesFlagMinRejectConversations &&
			bInj > 0 &&
			(2*rm.Rejects+rm.WeakRejects)*bInj >= rulesFlagRateFactor*(2*m.Baseline.Rejects+m.Baseline.WeakRejects)*rm.Injections {
			rm.Harmful = true
		}
		rulesList = append(rulesList, rm)
	}
	sort.Slice(rulesList, func(i, j int) bool { return rulesList[i].Rule < rulesList[j].Rule })
	m.Rules = rulesList
	return m
}

// containsNormalized reports whether the cohort rule set carries the
// normalized rule text (rules are stored verbatim; the join normalizes —
// one identity rule, the freeze-set convention).
func containsNormalized(set map[string]bool, normalized string) bool {
	for t := range set {
		if normalizeRule(t) == normalized {
			return true
		}
	}
	return false
}

// learningPromotionVerdict is the canary→project_active GATE (K3 §3.4 +
// lock stage machine; a §5.3 gate — consumes the journaled measure ONLY,
// never raw evidence). Returns:
//
//	"promote" + detail — all legs pass, additive-only delta,
//	"hold"    + detail — all legs pass BUT delta carries retractions (D4
//	             preserved: flips to held_for_human for human resolution),
//	"drop"    + detail — a canary-scoped harmful tuple on the candidate's
//	             own adds (gate fail — the experiment hurts),
//	""        + nil    — minimums unmet: keep measuring (NEVER promotes on
//	             age or vacuity; the stall advisory is the visibility
//	             answer, learning_stages.go).
func learningPromotionVerdict(m learningCohortMeasure, cand LearningCandidate) (string, map[string]interface{}) {
	detail := map[string]interface{}{
		"epoch":         m.Epoch,
		"min_outcomes":  learningPromotionMinOutcomes,
		"reject_factor": "5/4",
	}
	// Destructive leg first: a harmful tuple on the candidate's own adds
	// under its own canary cohort is a gate fail (any gate ⇒ dropped).
	for _, r := range m.Rules {
		if r.Harmful {
			detail["harmful_rule"] = r
			return "drop", detail
		}
	}
	// Paired minimums (both cohorts ≥ floor — a canary with no live
	// contrast never promotes: the self-reinforcing cohort guard).
	if m.Canary.Outcomes < learningPromotionMinOutcomes || m.Live.Outcomes < learningPromotionMinOutcomes {
		return "", nil
	}
	// Reject-rate leg: canary ≤ live × 5/4, integer cross-multiplied:
	// (2r+w)/(2inj) ≤ (5/4)(2rl+wl)/(2injl) ⇔ 4(2r+w)·injl ≤ 5(2rl+wl)·inj.
	cr := 2*m.Canary.Rejects + m.Canary.WeakRejects
	lr := 2*m.Live.Rejects + m.Live.WeakRejects
	if learningPromotionRejectFactorD*cr*m.Live.Outcomes >
		learningPromotionRejectFactorN*lr*m.Canary.Outcomes {
		detail["fail"] = fmt.Sprintf("canary reject rate %d/%d exceeds live %d/%d × 5/4",
			cr, m.Canary.Outcomes, lr, m.Live.Outcomes)
		return "", nil // stats miss: stay canary (never a drop — evidence keeps aging)
	}
	// Taint leg: canary errored-share ≤ live + 5pp (100·ce·ls ≤
	// cs·(100·le + 5·ls)); zero-send legs are unscoreable — the
	// outcomes floor above already fails closed on empty traffic.
	if m.Canary.Sends > 0 && m.Live.Sends > 0 &&
		100*m.Canary.ErroredSends*m.Live.Sends >
			m.Canary.Sends*(100*m.Live.ErroredSends+learningTaintAllowancePP*m.Live.Sends) {
		detail["fail"] = fmt.Sprintf("canary taint %d/%d exceeds live %d/%d + %dpp",
			m.Canary.ErroredSends, m.Canary.Sends, m.Live.ErroredSends, m.Live.Sends, learningTaintAllowancePP)
		return "", nil
	}
	if len(cand.Delta.Retract) > 0 {
		return "hold", detail
	}
	return "promote", detail
}

// learningRollbackTargets is the project_active→rolled_back GATE (lock
// R1 + §4.1; a §5.3 gate — measure in, targets out, zero evidence reads):
// the candidate-add rules whose measure row meets the exact rules-audit
// harmful tuple at this epoch's cadence.
func learningRollbackTargets(m learningCohortMeasure) []learningRuleMeasure {
	var out []learningRuleMeasure
	for _, r := range m.Rules {
		if r.Harmful {
			out = append(out, r)
		}
	}
	return out
}

// learningFreezeSetForStage re-derives the R2 frozen-text set for the
// stage drivers (the lint fold, learning_lint.go — ONE fold, three
// consumers: learner vet, candidate lint, stage interrupt).
func learningFreezeSetForStage(in learningReplayInput, epoch int) map[string]string {
	return learningCandidateFreezeSet(flattenLanes(in), epoch)
}

// flattenLanes returns the gathered events as one slice (main-lane
// freeze rows are what the fold reads; cross-lane inclusion is harmless —
// the fold keys on action names either way).
func flattenLanes(in learningReplayInput) []store.Event {
	var out []store.Event
	for _, lane := range in.lanes {
		out = append(out, lane.events...)
	}
	return out
}

// learningStageMainEpochAt resolves the main-lane epoch journaled on a
// stage row (W5 transitions carry it; absent ⇒ 0, conservative).
func learningStageMainEpochAt(lanes [][]store.Event, hash, to string) int {
	bestID := int64(-1)
	epoch := 0
	for _, events := range lanes {
		for _, ev := range events {
			if ev.Type != store.EventReviewAction {
				continue
			}
			var p struct {
				Action string `json:"action"`
				Hash   string `json:"artifact_hash"`
				To     string `json:"to"`
				Epoch  int    `json:"epoch"`
			}
			if !jsonUnmarshalOK(ev.Payload, &p) || p.Action != "learning_stage" ||
				p.Hash != hash || p.To != to {
				continue
			}
			if ev.ID > bestID {
				bestID, epoch = ev.ID, p.Epoch
			}
		}
	}
	return epoch
}
