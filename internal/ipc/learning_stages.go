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
// seqs journaled). Pass at ≥3 main-epoch age with no harmful tuple ⇒ the
// candidate is promotion-eligible; W4 never occupies the canary slot
// (promotion actuation is W5), so the checkpoint journals
// cause:"shadow_queued" — visible, never silently stuck. Stage rows never
// write the shadow candidate itself (the fold IS the state — a passing
// checkpoint needs no transition).

import (
	"context"
	"log"

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
	var lanes [][]store.Event
	if wss, err := s.store.ListWorkstreams(ctx, projectID); err == nil {
		for _, w := range wss {
			c, cerr := s.store.GetActiveConversation(ctx, w.ID)
			if cerr != nil {
				continue
			}
			if events, lerr := s.store.ListEvents(ctx, c.ID, 0); lerr == nil {
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
// re-failures. Promotion eligibility is surfaced via shadow_queued — W4
// never actuates the canary slot (W5), so an eligible candidate is never
// stage-flipped here; the queued row is the never-stuck signal.
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
		if aged {
			// Promotion-eligible per the W4 criteria (re-pass on grown
			// slice, no harmful tuple — replay-a inside the report). The
			// canary slot stays un-actuated until W5 lands promotion:
			// journal the never-stuck signal instead of a transition.
			s.journalLearningUpdate(ctx, mainConv.ID, "shadow_queued", map[string]interface{}{
				"artifact_hash":  cand.ArtifactHash,
				"epoch":          mainEpoch,
				"checkpoint_seq": checkpointSeq,
				"slot_free":      !slotOccupied,
				"promoted":       false,
			})
		}
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
