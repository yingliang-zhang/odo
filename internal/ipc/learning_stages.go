package ipc

// D9-W4 (lock stage machine; K3 spec §1.3, §3.1): the journaled stage
// fold — the ONLY stage state (candidates.jsonl rows are immutable, never
// carry status) — plus the shadow checkpoint machinery.
//
// Fold: the latest learning_stage row per artifact_hash wins; ordering
// across lanes uses the store's GLOBAL event id (per-lane seqs are
// incomparable across conversations — the W3 status fold's per-lane seq
// compare is superseded here and learning_status now shares this fold).
// Stage rows citing a hash absent from candidates.jsonl are INVALID
// (tamper/drift — surfaced, and all transitions refuse, fail-closed).
//
// Shadow checkpoints (K3 §3.1): at each MAIN-lane distill tail, every
// shadow candidate re-runs the frozen replay against the grown slice.
// Re-fail ⇒ learning_stage shadow→dropped (cause shadow_failed, evidence
// seqs journaled). Pass at ≥3 main-epoch age with no harmful tuple ⇒
// W5 actuates shadow→canary when the single canary slot is free (one
// promotion per tick — cohort purity, lock R3's single-slot rule); a
// slot-blocked or frozen candidate journals shadow_queued / the R2
// stage-interrupt (learning_frozen) instead — never silently stuck.
// Stage rows never write the shadow candidate itself (the fold IS the
// state — a passing checkpoint needs no transition).
//
// W5 additions below: the per-epoch measure tick (canary promotion /
// hold / drop + stall advisories, learning_measure.go's fold feeding
// learning_rollback.go and this file's actuation), the R2 stage-
// interrupt (learning_frozen: a held/shadow candidate carrying frozen
// text stalls — journaled once per hash+stage), and the marker-first
// promotion apply (memory_apply{actor:"learning_promote",
// epoch:learningPromoteEpochKey} — the sentinel keeps the batch fold
// collision-free, see learningPromoteApply).

import (
	"context"
	"log"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/yingliang-zhang/odo/internal/store"
)

// learningStageInfo is one artifact's folded stage state.
type learningStageInfo struct {
	To    string `json:"to"`
	From  string `json:"from"`
	Cause string `json:"cause"`
	seq   int
	id    int64 // global journal id: the cross-lane ordering key
}

// foldLearningStages merges learning_stage rows across any number of lane
// event slices into the latest-stage table, ordered by global event id.
func foldLearningStages(lanes ...[]store.Event) map[string]learningStageInfo {
	out := map[string]learningStageInfo{}
	for _, events := range lanes {
		for _, ev := range events {
			if ev.Type != store.EventReviewAction {
				continue
			}
			var p struct {
				Action string `json:"action"`
				Hash   string `json:"artifact_hash"`
				From   string `json:"from"`
				To     string `json:"to"`
				Cause  string `json:"cause"`
			}
			if !jsonUnmarshalOK(ev.Payload, &p) || p.Action != "learning_stage" || p.Hash == "" || p.To == "" {
				continue
			}
			cur, ok := out[p.Hash]
			if !ok || ev.ID > cur.id {
				out[p.Hash] = learningStageInfo{To: p.To, From: p.From, Cause: p.Cause, seq: ev.Seq, id: ev.ID}
			}
		}
	}
	return out
}

// learningStageOf resolves one artifact's folded stage across the
// project's active conversations. Absent = (zero value, false).
func (s *Server) learningStageOf(ctx context.Context, projectID int64, hash string) (learningStageInfo, bool) {
	return learningStageOfStore(ctx, s.store, projectID, hash)
}

// learningStageOfStore is the store-keyed twin of the method above (one
// fold, one cross-lane walk): daemon gates ride the method, the W6
// human-action cores (learning_actions.go) ride this free form from CLI
// processes.
func learningStageOfStore(ctx context.Context, st *store.Store, projectID int64, hash string) (learningStageInfo, bool) {
	var lanes [][]store.Event
	if wss, err := st.ListWorkstreams(ctx, projectID); err == nil {
		for _, w := range wss {
			c, cerr := st.GetActiveConversation(ctx, w.ID)
			if cerr != nil {
				continue
			}
			if events, lerr := st.ListEvents(ctx, c.ID, 0); lerr == nil {
				lanes = append(lanes, events)
			}
		}
	}
	info, ok := foldLearningStages(lanes...)[hash]
	return info, ok
}

// journalLearningGate appends one learning_gate row and returns its seq
// (0 on failure — best-effort like the episode row; a missing gate row
// leaves the candidate un-staged, visible, never silently promoted).
func (s *Server) journalLearningGate(ctx context.Context, convID int64, hash string, rep learningGateReport) int {
	payload := map[string]interface{}{
		"action":        "learning_gate",
		"artifact_hash": hash,
		"gate":          rep.Gate,
		"verdict":       rep.Verdict,
	}
	if len(rep.Violations) > 0 {
		payload["violations"] = rep.Violations
	}
	for k, v := range rep.Detail {
		payload[k] = v
	}
	ev, err := s.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(payload))
	if err != nil {
		log.Printf("learning: journal learning_gate %s/%s: %v", rep.Gate, rep.Verdict, err)
		return 0
	}
	return ev.Seq
}

// journalLearningStage appends one learning_stage transition row. extra
// merges gate verdicts / report seqs / failure lists / evidence refs.
// Best-effort (the fold recomputes from rows; a lost row is a stage gap,
// never a file write).
func (s *Server) journalLearningStage(ctx context.Context, convID int64, hash, from, to, cause string, extra map[string]interface{}) int {
	payload := map[string]interface{}{
		"action":        "learning_stage",
		"artifact_hash": hash,
		"from":          from,
		"to":            to,
		"cause":         cause,
	}
	for k, v := range extra {
		payload[k] = v
	}
	ev, err := s.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(payload))
	if err != nil {
		log.Printf("learning: journal learning_stage %s→%s: %v", from, to, err)
		return 0
	}
	return ev.Seq
}

// learningShadowAgingEpochs is the locked shadow exposure-free observation
// window: promotion may only be considered after ≥3 main-lane epochs.
const learningShadowAgingEpochs = 3

// learningShadowCheckpoints is the distill-tail driver (MAIN lane only):
// re-run the frozen replay for every shadow candidate against the grown
// slice, journal memory_update{layer:"learning"} rows, and demote
// re-failures. W5 actuation: an aged passing candidate flips
// shadow→canary when the single canary slot is free (one flip per
// tick — cohort purity, R3); slot-blocked keeps the W4 shadow_queued
// signal; a frozen candidate stalls on the R2 stage-interrupt
// (learning_frozen, journaled once per hash+stage).
//
// Best-effort per candidate: one artifact's failure never blocks the rest
// and never fails the distill (the W3 episode-bookkeeping precedent).
func (s *Server) learningShadowCheckpoints(ctx context.Context, mainConv store.Conversation, newEpoch int) {
	if !learningStagesEnabled() {
		return
	}
	cands, rerr := ReadLearningCandidates(s.projectRoot)
	if rerr != nil {
		log.Printf("learning: checkpoint: read candidates: %v", rerr)
		return
	}
	if len(cands) == 0 {
		return
	}
	w, werr := s.store.GetWorkstream(ctx, mainConv.WorkstreamID)
	if werr != nil {
		return
	}
	gathered := s.gatherLearningReplayInput(ctx, w.ProjectID)
	table := foldLearningStages(gathered.laneEvents()...)
	mainEpoch := newEpoch // this distill IS the main-lane clock tick
	slotOccupied := false
	for _, c := range cands {
		if table[c.ArtifactHash].To == "canary" {
			slotOccupied = true
			break
		}
	}
	for _, cand := range cands {
		if table[cand.ArtifactHash].To != "shadow" {
			continue
		}
		rep := computeLearningReplay(gathered, cand)
		freezeSeq := s.journalLearningFreeze(ctx, mainConv.ID, rep)
		metrics := map[string]interface{}{
			"verdict":        rep.Verdict,
			"prevented_harm": rep.PreventedHarm,
			"friction":       rep.Friction,
			"loosened":       rep.Loosened,
			"outcomes":       rep.Outcomes,
			"cohorts":        rep.Cohorts,
			"slice_events":   rep.Freeze.SliceEvents,
			"input_sha256":   rep.Freeze.InputSHA256,
		}
		if len(rep.Violations) > 0 {
			metrics["violations"] = rep.Violations
		}
		checkpointSeq := s.journalLearningUpdate(ctx, mainConv.ID, "shadow_checkpoint", map[string]interface{}{
			"artifact_hash": cand.ArtifactHash,
			"epoch":         mainEpoch,
			"freeze_seq":    freezeSeq,
			"metrics":       metrics,
		})
		if !rep.passed() {
			s.journalLearningStage(ctx, mainConv.ID, cand.ArtifactHash, "shadow", "dropped", "shadow_failed", map[string]interface{}{
				"evidence_seqs": []int{freezeSeq, checkpointSeq},
				"verdict":       rep.Verdict,
			})
			continue
		}
		aged := mainEpoch-learningCandidateMainEpoch(gathered.laneEvents(), cand.ArtifactHash) >= learningShadowAgingEpochs
		if !aged {
			continue
		}
		// R2 stage-interrupt: an eligible candidate whose texts are frozen
		// (rolled back / flagged within the window AFTER its own creation)
		// never actuates — journaled once per hash+stage.
		if hits := learningFrozenHits(cand, learningFreezeSetForStage(gathered, mainEpoch)); len(hits) > 0 {
			s.journalLearningFrozen(ctx, mainConv.ID, cand.ArtifactHash, "shadow", hits, mainEpoch)
			continue
		}
		// Seam-disabled gate (D9 Lock Amendment A1, Amendment 4 — GLM
		// finding): a project-wide disabled canary seam
		// (learning_canary_fraction ≤ 0, R3) must NEVER flip
		// shadow→canary — learningCurrentCanary returns nil at fraction
		// 0, so a flipped candidate could never inject a cohort: a
		// structurally permanent squatter in the single slot. The
		// checkpoints and the frozen interrupt above stay live; aging
		// surfaces through the stall advisory.
		if learningCanaryFraction() <= 0 {
			continue
		}
		if slotOccupied {
			// Promotion-eligible but the single canary slot is taken:
			// the W4 never-stuck signal stays the honest surface.
			s.journalLearningUpdate(ctx, mainConv.ID, "shadow_queued", map[string]interface{}{
				"artifact_hash":  cand.ArtifactHash,
				"epoch":          mainEpoch,
				"checkpoint_seq": checkpointSeq,
				"slot_free":      false,
				"promoted":       false,
			})
			continue
		}
		// W5 actuation: shadow→canary (checkpoint evidence rides the
		// transition row; epoch keys the stall clock and freeze windows).
		s.journalLearningStage(ctx, mainConv.ID, cand.ArtifactHash, "shadow", "canary", "checkpoint_promoted", map[string]interface{}{
			"epoch":         mainEpoch,
			"evidence_seqs": []int{freezeSeq, checkpointSeq},
		})
		slotOccupied = true
	}
}

// learningFrozenHits intersects the candidate's delta texts (add ∪
// retract, normalized — one identity rule) with the R2 freeze set:
// the texts and their freeze reasons, verbatim-sorted (deterministic
// journal output).
func learningFrozenHits(cand LearningCandidate, frozen map[string]string) []string {
	var hits []string
	check := func(text string) {
		if reason, froz := frozen[normalizeRule(text)]; froz {
			hits = append(hits, text+" ("+reason+")")
		}
	}
	for _, a := range cand.Delta.Add {
		check(a.Rule)
	}
	for _, t := range cand.Delta.Retract {
		check(t)
	}
	sort.Strings(hits)
	return hits
}

// journalLearningFrozen journals the R2 stage-interrupt row
// (review_action{action:"learning_frozen"}) ONCE per (hash, stage) —
// the stall signal for a held/frozen candidate; the repeated-section
// dedupe keeps each discovery epoch's window from re-arming on every
// checkpoint (the text's own freeze windows, journaled separately by
// rollback rows, govern re-entry).
func (s *Server) journalLearningFrozen(ctx context.Context, convID int64, hash, stage string, hits []string, epoch int) {
	have := false
	if events, err := s.store.ListEvents(ctx, convID, 0); err == nil {
		for _, ev := range events {
			if ev.Type != store.EventReviewAction {
				continue
			}
			var p struct {
				Action string `json:"action"`
				Hash   string `json:"artifact_hash"`
				Stage  string `json:"stage"`
			}
			if jsonUnmarshalOK(ev.Payload, &p) && p.Action == "learning_frozen" && p.Hash == hash && p.Stage == stage {
				have = true
				break
			}
		}
	}
	if have {
		return
	}
	texts := make([]string, len(hits))
	copy(texts, hits)
	if _, err := s.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":        "learning_frozen",
		"artifact_hash": hash,
		"stage":         stage,
		"epoch":         epoch,
		"reason":        "stage_interrupt",
		"texts":         texts,
	})); err != nil {
		log.Printf("learning: journal learning_frozen: %v", err)
	}
}

// learningCandidateMainEpoch folds the main_epoch lineage key of one
// artifact's learning_candidate row (0 when the row is unreadable or
// pre-W4 — conservative: the candidate ages from the next checkpoint).
func learningCandidateMainEpoch(lanes [][]store.Event, hash string) int {
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
				Epoch  int    `json:"main_epoch"`
			}
			if !jsonUnmarshalOK(ev.Payload, &p) || p.Action != "learning_candidate" || p.Hash != hash {
				continue
			}
			if ev.ID > bestID {
				bestID, epoch = ev.ID, p.Epoch
			}
		}
	}
	return epoch
}

// journalLearningUpdate appends a memory_update{layer:"learning"} row —
// the W4 measure/checkpoint journal family (K3 §1.3 memory_update usage).
func (s *Server) journalLearningUpdate(ctx context.Context, convID int64, cause string, fields map[string]interface{}) int {
	payload := map[string]interface{}{
		"layer": "learning",
		"cause": cause,
	}
	for k, v := range fields {
		payload[k] = v
	}
	ev, err := s.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(payload))
	if err != nil {
		log.Printf("learning: journal learning/%s: %v", cause, err)
		return 0
	}
	return ev.Seq
}

// learningPromoteEpochKey is the promotion apply marker's epoch sentinel
// (-1): findPendingBatch pairs memory_apply rows with distill batches by
// epoch and pending epochs are ≥ 0, so the sentinel keeps a promotion
// from ever consuming a learner batch; the replay fold's propose prune
// and pairing treat a propose-less negative-epoch receipt as the
// documented nil-propose path (safe conflict, never a wrong merge —
// memory_replay.go).
const learningPromoteEpochKey = -1

// learningMeasureTick is the W5 per-epoch measure driver (MAIN lane,
// distill tail, after the shadow checkpoints): the per-stage cadence of
// the evidence→measure→gate pipeline.
//
//	canary → journal the measure row (cadence); run the promotion gate:
//	         "promote" ⇒ marker-first apply + canary→project_active,
//	         "hold"    ⇒ canary→held_for_human (D4: retractions stay
//	         human-resolved), "drop" ⇒ canary→dropped (its own canary
//	         cohort met the harmful tuple), "" ⇒ keep measuring.
//	         Frozen texts stall the promotion (R2 stage-interrupt).
//	project_active → learningRollbackCheck (learning_rollback.go, R1).
//	held_for_human → R2 stage-interrupt signal only (human resolution
//	         pending; the frozen stall rows keep it visible).
//	shadow → stall advisory when aged past learningStallMainEpochs
//	         (aging without promotion-worthy evidence — surfaced, NEVER
//	         auto-promoted, never auto-dropped; the lock's promotion-
//	         starvation pin). Canary aging past the floor without its
//	         cohort minimums advises the same way.
//
// Best-effort per candidate (the checkpoint precedent): one artifact's
// failure never fails the distill.
func (s *Server) learningMeasureTick(ctx context.Context, mainConv store.Conversation, newEpoch int) {
	if !learningStagesEnabled() {
		return
	}
	cands, rerr := ReadLearningCandidates(s.projectRoot)
	if rerr != nil || len(cands) == 0 {
		if rerr != nil {
			log.Printf("learning: measure tick: read candidates: %v", rerr)
		}
		return
	}
	w, werr := s.store.GetWorkstream(ctx, mainConv.WorkstreamID)
	if werr != nil {
		return
	}
	in := s.gatherLearningReplayInput(ctx, w.ProjectID)
	lanes := in.laneEvents()
	table := foldLearningStages(lanes...)
	// Pass 1 — destructive/held stages first: a rollback journaled HERE
	// must freeze its texts before any canary/shadow promotes below
	// (same-tick re-entry through a sibling candidate carrying the same
	// text is the R2 hole this ordering closes).
	frozen := learningFreezeSetForStage(in, newEpoch)
	for _, cand := range cands {
		switch table[cand.ArtifactHash].To {
		case "project_active":
			s.learningRollbackCheck(ctx, mainConv, in, cand, newEpoch)
		case "held_for_human":
			if hits := learningFrozenHits(cand, frozen); len(hits) > 0 {
				s.journalLearningFrozen(ctx, mainConv.ID, cand.ArtifactHash, "held_for_human", hits, newEpoch)
			}
		}
	}
	// Pass 2 — re-gather so the freeze set sees pass-1 rollback rows.
	in = s.gatherLearningReplayInput(ctx, w.ProjectID)
	lanes = in.laneEvents()
	frozen = learningFreezeSetForStage(in, newEpoch)
	for _, cand := range cands {
		switch table[cand.ArtifactHash].To {
		case "canary":
			s.learningCanaryMeasure(ctx, mainConv, in, cand, newEpoch, frozen)
		case "shadow":
			entryEpoch := learningCandidateMainEpoch(lanes, cand.ArtifactHash)
			age := newEpoch - entryEpoch
			if age > learningStallMainEpochs {
				s.journalLearningStall(ctx, mainConv.ID, cand.ArtifactHash, "shadow",
					"shadow aged "+strconv.Itoa(age)+" main epochs without reaching canary (replay re-pass failing, frozen, or canary slot occupied)", newEpoch, entryEpoch)
			}
		}
	}
}

// learningCanaryMeasure is the canary arm of the tick: measure (journaled
// every epoch — the cadence, A1: stage_epoch rides the row), gate,
// actuate. Below the paired minimums the gate returns "" (keep
// measuring) — unless the candidate squats past 2× the stall floor
// (canary_starved drop); at full floors with zero live harm past the
// grace window it drops efficacy_vacuity (A1 Amendment 3; both vacuous
// exits write ZERO freeze-set entries). Promotions still never ride age
// or vacuity (promotion-starvation pin's operative half stands).
func (s *Server) learningCanaryMeasure(ctx context.Context, mainConv store.Conversation, in learningReplayInput, cand LearningCandidate, epoch int, frozen map[string]string) {
	lanes := in.laneEvents()
	since := learningStageSince(lanes, cand.ArtifactHash, "canary")
	m := computeLearningMeasure(in, cand, since, epoch)
	m.Kind = "canary"
	m.StageEpoch = learningStageMainEpochAt(lanes, cand.ArtifactHash, "canary")
	measureSeq := s.journalLearningUpdate(ctx, mainConv.ID, "measure", map[string]interface{}{
		"artifact_hash": m.ArtifactHash,
		"kind":          m.Kind,
		"epoch":         epoch,
		"stage_epoch":   m.StageEpoch,
		"window_from":   m.WindowFrom,
		"canary":        m.Canary,
		"live":          m.Live,
		"baseline":      m.Baseline,
		"rules":         m.Rules,
		"excluded":      m.Excluded,
	})
	// R2 stage-interrupt: a canary whose texts froze mid-experiment (a
	// sibling's rollback) never promotes — stall, journaled once.
	if hits := learningFrozenHits(cand, frozen); len(hits) > 0 {
		s.journalLearningFrozen(ctx, mainConv.ID, cand.ArtifactHash, "canary", hits, epoch)
		return
	}
	verdict, detail := learningPromotionVerdict(m, cand)
	if detail == nil {
		detail = map[string]interface{}{} // keep-measuring verdicts carry no gate detail
	}
	detail["measure_seq"] = measureSeq
	detail["epoch"] = epoch
	switch verdict {
	case "promote":
		s.learningPromoteApply(ctx, mainConv, cand, m)
	case "hold":
		s.journalLearningStage(ctx, mainConv.ID, cand.ArtifactHash, "canary", "held_for_human", "retract_delta_held", detail)
		s.journalLearningAdvisory(ctx, mainConv.ID, cand, "held for human: stats pass but the delta carries retractions (D4 preserved) — resolve via apply_memory / `odo rules retract`")
	case "drop":
		// A1 Amendment 3: three drop exits. cause rides the verdict
		// detail (drop_cause — never "cause", which the stage row owns);
		// the two vacuous-drop causes write ZERO freeze-set entries by
		// construction (no rollback, R2 untouched — vacuity ≠ harmful).
		cause, _ := detail["drop_cause"].(string)
		if cause == "" {
			cause = "harmful_tuple" // defensive: the verdict always sets modern drops
		}
		s.journalLearningStage(ctx, mainConv.ID, cand.ArtifactHash, "canary", "dropped", cause, detail)
		switch cause {
		case "efficacy_vacuity":
			s.journalLearningAdvisory(ctx, mainConv.ID, cand, "dropped (efficacy_vacuity): paired floors met but zero live harm across the window — the rule's harm class is absent from live traffic (vacuity ≠ harmful; the freeze set is untouched)")
		case "canary_starved":
			s.journalLearningAdvisory(ctx, mainConv.ID, cand, "dropped (canary_starved): squatting the single slot past 2× the stall floor with the paired minimums still unmet (the exclusion counters ride the drop row; the freeze set is untouched)")
		default:
			s.journalLearningAdvisory(ctx, mainConv.ID, cand, "dropped: its own canary cohort met the harmful tuple — the experiment hurt")
		}
	default:
		// Keep measuring. Stall advisory only: aging without the cohort
		// minimums is surfaced, never auto-promoted. The A1 drop exits
		// (efficacy_vacuity / canary_starved) subsume the busy-but-
		// vacuous hole; the stall dedupe is epoch-keyed so a re-cycled
		// candidate re-advises honestly (Sol fix, Amendment 3).
		age := 0
		if m.StageEpoch > 0 { // pre-W5 stage rows carry no epoch: the stall clock reads unknown as "not yet", never as ancient
			age = epoch - m.StageEpoch
		}
		if age > learningStallMainEpochs && m.Canary.Outcomes < learningPromotionMinOutcomes {
			s.journalLearningStall(ctx, mainConv.ID, cand.ArtifactHash, "canary",
				"canary aged "+strconv.Itoa(age)+" main epochs with "+strconv.Itoa(m.Canary.Outcomes)+" resolved outcome(s), short of the "+strconv.Itoa(learningPromotionMinOutcomes)+" floor", epoch, m.StageEpoch)
		}
	}
}

// learningPromoteApply lands an additive-only candidate into memory.md
// under the marker-first doctrine (the applyResolvedBatch convention):
//
//  1. under memMu + the stranded-receipt repair pass,
//  2. memory_apply marker (actor "learning_promote", sentinel epoch,
//     recovery block) BEFORE any write — a crash after it is repaired
//     by the boot replayer (the one place memory_replay serves D9),
//  3. the canary→project_active stage row (the control flip),
//  4. the file write + memory-layer receipt (before/after shas).
//
// A candidate whose rules are ALREADY present verbatim (crash between
// steps 2-4 of an earlier tick, or a human/curated add identical to the
// delta) converges without a second marker: the stage row journals with
// present:true.
func (s *Server) learningPromoteApply(ctx context.Context, mainConv store.Conversation, cand LearningCandidate, m learningCohortMeasure) {
	s.memMu.Lock()
	defer s.memMu.Unlock()

	// Repair pass (the convention): a stranded marker from any earlier
	// crash is restored before planning, and its landing makes THIS plan
	// a no-op — idempotent re-entry after a mid-apply crash.
	if events, err := s.store.ListEvents(ctx, mainConv.ID, 0); err == nil {
		s.replayLaneMemReceipts(ctx, mainConv.ID, events, replayApply)
	}
	memPath := filepath.Join(s.projectRoot, ".odo", memoryFileName)
	oldMem := readFileFull(memPath)
	accepted := make([]acceptedRule, 0, len(cand.Delta.Add))
	for _, a := range cand.Delta.Add {
		accepted = append(accepted, acceptedRule{rule: a.Rule, evidence: a.Evidence})
	}
	plan := planMemoryApply(oldMem, accepted, nil, m.Epoch)
	if plan.content == oldMem {
		// Nothing to write (already landed) — converge the stage.
		s.journalLearningStage(ctx, mainConv.ID, cand.ArtifactHash, "canary", "project_active", "measured_promote", map[string]interface{}{
			"epoch":     m.Epoch,
			"present":   true,
			"measure_c": m.Canary.Outcomes,
		})
		return
	}

	// Marker-first: the journaled intent + recovery block precede every
	// file write (2026-08-25 doctrine; boot replayer repairs the rest).
	beforeSHA := sha16([]byte(oldMem))
	afterSHA := sha16([]byte(plan.content))
	markSeq := 0
	if ev, err := s.store.AppendEvent(ctx, mainConv.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":        "memory_apply",
		"epoch":         learningPromoteEpochKey,
		"actor":         "learning_promote",
		"artifact_hash": cand.ArtifactHash,
		"metrics":       map[string]int{"promoted": len(cand.Delta.Add)},
		"recovery": applyRecovery{
			Memory: &applyRecoveryLayer{
				BeforeSHA: beforeSHA, AfterSHA: afterSHA,
				Body: plan.content, Entries: plan.addedEntries,
			},
		},
	})); err != nil {
		log.Printf("learning: promote marker: %v (promotion deferred: no marker, no write)", err)
		return
	} else {
		markSeq = ev.Seq
	}
	s.journalLearningStage(ctx, mainConv.ID, cand.ArtifactHash, "canary", "project_active", "measured_promote", map[string]interface{}{
		"epoch":      m.Epoch,
		"marker_seq": markSeq,
		"measure_c":  m.Canary.Outcomes,
	})
	if err := writeFileWithin(s.projectRoot, memPath, plan.content, 0o644); err != nil {
		// Marker + stage landed; the replayer repairs the file (a failed
		// write is exactly the crash window the doctrine covers).
		log.Printf("learning: promote write memory.md: %v (marker-first: replay repairs)", err)
		return
	}
	if _, err := s.store.AppendEvent(ctx, mainConv.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":      "memory",
		"cause":      "apply",
		"before_sha": beforeSHA,
		"after_sha":  afterSHA,
		"detail":     "learning promote " + cand.ArtifactHash + ": accepted " + strconv.Itoa(len(cand.Delta.Add)) + " rule(s)",
	})); err != nil {
		log.Printf("learning: promote apply receipt: %v", err)
	}
	s.journalLearningAdvisory(ctx, mainConv.ID, cand, "promoted to project_active: paired cohorts passed the promotion gate (measured)")
}

// journalLearningStall journals ONE learning_stall advisory per
// (hash, stage, stage_epoch) — the epoch key is the candidate's CURRENT
// stage-cycle entry epoch (A1 Amendment 3, Sol fix for the re-cycle
// blind spot): within a cycle the row dedupes; a re-cycled artifact
// (re-entering the same stage at a new epoch) re-advises honestly.
// Aging without cohort minimums is surfaced for the LearningPanel (GUI
// render rides W6; the journal row is the truth), and NEVER
// auto-promotes, never auto-drops. Pre-A1 stall rows carry no
// stage_epoch (decode 0): one honest re-advice across the A1 boundary.
func (s *Server) journalLearningStall(ctx context.Context, convID int64, hash, stage, reason string, epoch, stageEpoch int) {
	if events, err := s.store.ListEvents(ctx, convID, 0); err == nil {
		for _, ev := range events {
			if ev.Type != store.EventMemoryUpdate {
				continue
			}
			var p struct {
				Layer      string `json:"layer"`
				Cause      string `json:"cause"`
				Hash       string `json:"artifact_hash"`
				Stage      string `json:"stage"`
				StageEpoch int    `json:"stage_epoch"`
			}
			if jsonUnmarshalOK(ev.Payload, &p) && p.Layer == "learning" && p.Cause == "learning_stall" &&
				p.Hash == hash && p.Stage == stage && p.StageEpoch == stageEpoch {
				return // already surfaced for this hash+stage+cycle
			}
		}
	}
	s.journalLearningUpdate(ctx, convID, "learning_stall", map[string]interface{}{
		"artifact_hash": hash,
		"stage":         stage,
		"stage_epoch":   stageEpoch,
		"epoch":         epoch,
		"reason":        reason,
	})
}

// journalLearningAdvisory renders a learning actuation into the
// transcript (the journalRunAdvisory precedent, learning-prefixed).
func (s *Server) journalLearningAdvisory(ctx context.Context, convID int64, cand LearningCandidate, what string) {
	short := cand.ArtifactHash
	if len(short) > 8 {
		short = short[:8]
	}
	if err := s.journalRunAdvisory(ctx, convID, "learning: candidate "+short+": "+what); err != nil {
		log.Printf("learning: advisory: %v", err)
	}
}
