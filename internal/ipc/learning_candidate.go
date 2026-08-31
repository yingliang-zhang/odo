package ipc

// D9-W4 (lock stage machine "— → candidate"; K3 spec §1.2, §6 wave W4):
// candidate creation behind the `learning_stages:` pref (DEFAULT ON — the
// wave IS the feature; off = legacy direct-apply behavior, kill-switch
// posture mirroring auto_apply). With the pref on, accepted learner-batch
// memory.md adds (the auto consumer call sites, memory_autogate.go) that
// previously wrote memory.md now produce ONE candidate per batch:
//
//   delta   = {add: [...accepted rules], retract: []} — retract deltas
//             stay human-originated ONLY (lock: retract_candidate rows
//             resolve through the existing D4 path; the W4 auto path
//             never carries them),
//   content = the FULL projected injected block bytes via planMemoryApply
//             — the same pure function the write path and the frozen
//             replay use (one projection rule, zero second convention),
//   hash    = LearningArtifactHash over the truth fields (learning_store.go
//             W3) — identical delta on identical base = idempotent no-op.
//
// The batch itself is STILL consumed through the shared apply core, with
// the diverted proposals journaled as `diverted` on the memory_apply
// marker (never faked as accepted or rejected); reaffirms, skill writes,
// user.md holds, and retract intents ride the UNCHANGED legacy lanes.
//
// Gate run at creation (all three, always — evidence completeness): lint,
// security (learning_lint.go), frozen replay (learning_replay.go). Any
// gate fail ⇒ learning_stage candidate→dropped with per-check evidence;
// all pass ⇒ candidate→shadow. The fold is the only stage state
// (candidates.jsonl rows are immutable).

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
)

// learningCandidateScope is the only candidate scope built in waves 3–6
// (K3 §0 scope note: policy candidates need a separate lock).
const learningCandidateScope = "project:memory"

// learningStagesEnabled resolves prefs.md `learning_stages:` (default ON;
// off/false/0/no/never = legacy W3 behavior — zero candidates, the auto
// apply path writes memory.md directly). Resolved per call, the
// resolveMaxConcurrent pattern: a prefs edit takes effect on the next
// fold, never on a package global.
func learningStagesEnabled() bool {
	switch strings.ToLower(adapter.LoadPrefsRaw("learning_stages")) {
	case "off", "false", "0", "no", "never":
		return false
	}
	return true
}

// learningCandidateFromAccepted builds the candidate row (pure — no I/O):
// the FULL projected memory.md block under the added rules. epoch 0 stamps
// reaffirmed:0 in the projected lines — a projection-only marker, stable
// across replays (the projected cohort hash intersects nothing live).
func learningCandidateFromAccepted(base, baseSHA string, baseSourceSeq int, adds []LearningRuleAdd, prov LearningCandidateProvenance) LearningCandidate {
	accepted := make([]acceptedRule, 0, len(adds))
	for _, a := range adds {
		accepted = append(accepted, acceptedRule{rule: a.Rule, evidence: a.Evidence})
	}
	plan := planMemoryApply(base, accepted, nil, 0)
	cand := LearningCandidate{
		Version:       1,
		Scope:         learningCandidateScope,
		BaseSHA16:     baseSHA,
		BaseSourceSeq: baseSourceSeq,
		Delta: LearningCandidateDelta{
			Add:     adds,
			Retract: []string{},
		},
		Content:    plan.content,
		Provenance: prov,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	cand.ArtifactHash = LearningArtifactHash(cand)
	return cand
}

// learningMainWorkstream returns the project's "main" workstream (nil
// when absent) — the audit-sink lane the candidate aging clock keys on
// (RulesAuditMainWorkstream conventions).
func (s *Server) learningMainWorkstream(ctx context.Context, projectID int64) *store.Workstream {
	wss, err := s.store.ListWorkstreams(ctx, projectID)
	if err != nil {
		return nil
	}
	for _, w := range wss {
		if w.Name == RulesAuditMainWorkstream {
			w := w
			return &w
		}
	}
	return nil
}

// learningMainEvents returns the main lane's full journal (nil on any
// lookup failure — callers degrade to empty folds, never stall).
func (s *Server) learningMainEvents(ctx context.Context, projectID int64) []store.Event {
	mainWS := s.learningMainWorkstream(ctx, projectID)
	if mainWS == nil {
		return nil
	}
	c, err := s.store.GetActiveConversation(ctx, mainWS.ID)
	if err != nil {
		return nil
	}
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return nil
	}
	return events
}

// learningMainEpoch resolves the newest MAIN-lane distill marker epoch —
// the project-scoped aging clock for candidates (R2 freeze + shadow aging
// count in main epochs). 0 when main has never distilled.
func learningMainEpochFrom(events []store.Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		if !isDistillMarkerEvent(events[i]) {
			continue
		}
		var p struct {
			Epoch int `json:"epoch"`
		}
		if jsonUnmarshalOK(events[i].Payload, &p) {
			return p.Epoch
		}
	}
	return 0
}

// divertAcceptedAddsToCandidate is the W4 hook shared by the two
// auto-consumer call sites (post-fold apply + legacy sweep). It returns
// the proposal indexes that ride the candidate lane instead of the
// memory.md write lane — the caller passes them to applyResolvedBatch,
// which journals them as `diverted` and skips their file writes.
//
// Returns nil fast (legacy behavior, zero journal noise) when the pref is
// off, the batch has no accepted memory.md adds, or creation inputs are
// unresolvable. All journal/jsonl/gate failures are fail-closed but never
// propagate: a candidate whose bookkeeping half-landed stays un-staged
// (visible in `odo learning status`), and the accepted rules are NOT
// applied — infrastructure failure must not leak staged rules into the
// live prompt.
func (s *Server) divertAcceptedAddsToCandidate(ctx context.Context, c store.Conversation, batch pendingBatch, accepted []bool, events []store.Event) []int {
	if !learningStagesEnabled() {
		return nil
	}
	var addIdx []int
	adds := []LearningRuleAdd{}
	for i, p := range batch.proposals {
		if !accepted[i] || p.Target != "memory.md" || p.Intent == "retract" {
			continue
		}
		addIdx = append(addIdx, i)
		adds = append(adds, LearningRuleAdd{Rule: p.Rule, Evidence: p.Evidence, FlagSeq: p.FlagSeq})
	}
	if len(addIdx) == 0 {
		return nil
	}

	// Base: the pre-apply memory.md (FULL uncapped — the write-path basis,
	// applyResolvedBatch precedent) + the journal head seq grounding it.
	base := readFileFull(filepath.Join(s.projectRoot, ".odo", memoryFileName))
	baseSourceSeq := 0
	if len(events) > 0 {
		baseSourceSeq = events[len(events)-1].Seq
	}
	w, werr := s.store.GetWorkstream(ctx, c.WorkstreamID)
	var mainEvents []store.Event
	mainEpoch := 0
	if werr == nil {
		mainEvents = s.learningMainEvents(ctx, w.ProjectID)
		mainEpoch = learningMainEpochFrom(mainEvents)
	}
	cand := learningCandidateFromAccepted(base, sha16([]byte(base)), baseSourceSeq, adds, LearningCandidateProvenance{
		CreatedBy:       "learner_batch",
		SourceSeq:       []int{batch.seq},
		ProposeEpoch:    batch.epoch,
		PanelReceiptSeq: batch.seq,
		Uses:            0,
		Cost:            map[string]interface{}{"usage_available": false},
		Supersedes:      nil,
	})

	// Idempotence: an identical delta on an identical base re-derives the
	// same artifact_hash. An existing artifact WITH a stage row makes the
	// whole creation an inert no-op (crash-retry convergence — the batch
	// still diverts; re-inviting the rules into memory.md is the only
	// wrong exit). An artifact WITHOUT any stage row is the pre-gates
	// crash window: run the gates now, journal no second creation row.
	exists := false
	if prior, rerr := ReadLearningCandidates(s.projectRoot); rerr != nil {
		log.Printf("learning: read candidates: %v (batch stays diverted, un-staged)", rerr)
		return addIdx
	} else {
		for _, p := range prior {
			if p.ArtifactHash == cand.ArtifactHash {
				exists = true
				break
			}
		}
	}
	row := cand
	if !exists {
		// Journal the lineage row FIRST so the jsonl row carries the real
		// created_seq (the hash never covers it — learning_store.go truth
		// fields). Crash order: jsonl missing ⇒ the next batch simply
		// creates again (a moved head seq changes the base anyway).
		createEv, jerr := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action":          "learning_candidate",
			"artifact_hash":   cand.ArtifactHash,
			"version":         cand.Version,
			"scope":           cand.Scope,
			"base_sha16":      cand.BaseSHA16,
			"base_source_seq": cand.BaseSourceSeq,
			"delta":           cand.Delta,
			"propose_epoch":   batch.epoch,
			"source_seq":      cand.Provenance.SourceSeq,
			"main_epoch":      mainEpoch,
			"created_by":      "learner_batch",
		}))
		if jerr != nil {
			log.Printf("learning: journal learning_candidate: %v (batch stays diverted)", jerr)
			return addIdx
		}
		row.CreatedSeq = createEv.Seq
		var aerr error
		row, _, aerr = AppendLearningCandidate(s.projectRoot, row)
		if aerr != nil {
			log.Printf("learning: append candidate: %v (batch stays diverted, un-staged)", aerr)
			return addIdx
		}
	} else if werr == nil {
		if _, staged := s.learningStageOf(ctx, w.ProjectID, row.ArtifactHash); staged {
			return addIdx // fully journaled before; nothing new to journal
		}
	}
	// (exists && !staged) falls through: the pre-gates crash window — run
	// the gates against the existing artifact now.

	frozen := learningCandidateFreezeSet(mainEvents, mainEpoch)
	lintRep := lintLearningCandidate(s.projectRoot, base, row, frozen)
	secRep := securityLearningCandidate(row)
	replayRep, freezeSeq := computeLearningReplayGate(ctx, s, c.ID, row)

	lintSeq := s.journalLearningGate(ctx, c.ID, row.ArtifactHash, lintRep)
	secSeq := s.journalLearningGate(ctx, c.ID, row.ArtifactHash, secRep)
	replaySeq := s.journalLearningGate(ctx, c.ID, row.ArtifactHash, replayRep.base(freezeSeq))

	to, cause := "shadow", "gates_passed"
	var failures []string
	if !lintRep.passed() {
		failures = append(failures, learningGateLint)
	}
	if !secRep.passed() {
		failures = append(failures, learningGateSecurity)
	}
	if !replayRep.passed() {
		failures = append(failures, learningGateReplay)
	}
	if len(failures) > 0 {
		to, cause = "dropped", "gates_failed"
	}
	s.journalLearningStage(ctx, c.ID, row.ArtifactHash, "candidate", to, cause, map[string]interface{}{
		"gates": map[string]string{
			learningGateLint:     lintRep.Verdict,
			learningGateSecurity: secRep.Verdict,
			learningGateReplay:   replayRep.Verdict,
		},
		"report_seqs": map[string]int{
			learningGateLint:     lintSeq,
			learningGateSecurity: secSeq,
			learningGateReplay:   replaySeq,
		},
		"failures": failures,
	})
	return addIdx
}
