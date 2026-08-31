package ipc

// D9-W4 (lock R3; K3 spec §3.2 canary cohort; task §4 canary seam): the
// deterministic assignment + injection-substitution seam. W4 lands ONLY
// the seam — no candidate can REACH canary while promotion is unlanded
// (W5); the fixtures force-stage candidates to exercise it.
//
//   - prefs `learning_canary_fraction:` default 0.25, hard ceiling 0.5, 0
//     = canary disabled project-wide (R3). M = round(1/f): the run is
//     canary iff its lane ordinal % M == 0 — deterministic interleave,
//     zero RNG state.
//   - Ordinal = the lane's 1-based chain-root count: non-slash human
//     user_messages minus steers/parked/machine (auto_revise) rows. The
//     chain ROOT send assigns; steer continuations and retries INHERIT
//     the chain's bound cohort (a stage flip mid-chain cannot swap a
//     running cohort's block hash).
//   - Assignment journals BEFORE the run:
//     review_action{action:"learning_cohort", artifact_hash, conv_seq,
//     run:"send", block_sha}. Live runs journal NOTHING (zero-noise
//     posture — the M20 unarmed precedent).
//   - Injection: memoryLayers substitutes the candidate's projected
//     content for cohort-assigned sends — the EXISTING ".odo/memory.md"
//     receipt key cohorts the run (ZERO new receipt keys; the rules-
//     audit join picks it up unchanged). Block bytes pin on first
//     assignment as memory_update{layer:"learning_canary",
//     cause:"snapshot", source:"learning/<hash>"} — the
//     journalRuleSnapshots discipline, idempotent per artifact.
//   - memory.md itself is never written before project_active: revert =
//     stop injecting; in-flight chains finish on their bound cohort.

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"strconv"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
)

// learningCohortMode tells the injection seam which roll a run takes
// (K3 §3.2). Pipeline runs (parked/loop/revise) ride LIVE in W4 —
// run:"send" cohorts only.
type learningCohortMode int

const (
	learningCohortLive    learningCohortMode = iota // pipeline run: never canary in W4
	learningCohortRoot                              // human send chain root: ordinal roll
	learningCohortInherit                           // continuation/retry: inherit the chain
)

// learningCanaryFraction resolves prefs.md `learning_canary_fraction`:
// default 0.25, hard ceiling 0.5, 0 (or negative) = disabled (R3).
// Malformed reads fail to the default (resolveReplayCaps pattern).
func learningCanaryFraction() float64 {
	raw := adapter.LoadPrefsRaw("learning_canary_fraction")
	if raw == "" {
		return 0.25
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0.25
	}
	if f <= 0 {
		return 0
	}
	if f > 0.5 {
		return 0.5
	}
	return f
}

// learningCanaryM is the interleave modulus: M = round(1/f), ≥ 2 (the
// ceiling makes 2 the tightest meaningful interleave).
func learningCanaryM(f float64) int {
	if f <= 0 {
		return 0
	}
	m := int(math.Round(1 / f))
	if m < 2 {
		return 2
	}
	return m
}

// learningIsChainRootSend reports whether ev is a lane chain-root anchor:
// a non-slash human user_message — never a steer (continuation fodder),
// a parked-goal row (machine dequeue), or an auto_revise repair prompt
// (machine chain).
func learningIsChainRootSend(ev store.Event) bool {
	if ev.Type != store.EventUserMessage {
		return false
	}
	var p map[string]interface{}
	if json.Unmarshal(ev.Payload, &p) != nil {
		return false
	}
	text, _ := p["text"].(string)
	if rulesIsSlash(text) {
		return false
	}
	if p["steer"] == true || p["park"] == true {
		return false
	}
	if _, machine := p["auto_revise"]; machine {
		return false
	}
	return true
}

// learningChainRoots returns the seqs of the lane's chain-root anchors,
// seq-ascending. The upcoming root's ordinal = len(roots)+1.
func learningChainRoots(events []store.Event) []int {
	var roots []int
	for _, ev := range events {
		if learningIsChainRootSend(ev) {
			roots = append(roots, ev.Seq)
		}
	}
	return roots
}

// learningCohortRow is one decoded learning_cohort assignment.
type learningCohortRow struct {
	seq          int
	artifactHash string
	convSeq      int
	blockSHA     string
}

// learningChainCohort resolves the cohort bound to the chain whose latest
// root anchor sits at the end of events' history (inherit mode): the
// newest learning_cohort row journaled between the previous chain root
// and the current one — i.e., the assignment the root send itself made.
// Returns ("", false) when the root ran live.
func learningChainCohort(events []store.Event, roots []int) (learningCohortRow, bool) {
	if len(roots) == 0 {
		return learningCohortRow{}, false
	}
	root := roots[len(roots)-1]
	prev := 0
	if len(roots) > 1 {
		prev = roots[len(roots)-2]
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Seq >= root {
			continue // the root's assignment pre-dates the root message
		}
		if ev.Seq <= prev {
			break // older chains' rows never bind this chain
		}
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
			Hash   string `json:"artifact_hash"`
			Conv   int    `json:"conv_seq"`
			Block  string `json:"block_sha"`
		}
		if !jsonUnmarshalOK(ev.Payload, &p) || p.Action != "learning_cohort" || p.Hash == "" {
			continue
		}
		return learningCohortRow{seq: ev.Seq, artifactHash: p.Hash, convSeq: p.Conv, blockSHA: p.Block}, true
	}
	return learningCohortRow{}, false
}

// learningCurrentCanary folds the project stage table for the single
// canary candidate (the newest learning_stage{to:"canary"} row whose
// artifact resolves in candidates.jsonl). Nil when no candidate occupies
// the slot (always the case in W4 production) — fold tolerates multiple
// canary rows by taking the newest; slot exclusivity enforcement is W5.
func (s *Server) learningCurrentCanary(ctx context.Context, convID int64) *LearningCandidate {
	if !learningStagesEnabled() || learningCanaryFraction() <= 0 {
		return nil
	}
	// Fast path (hot send path): no candidate artifacts at all ⇒ no
	// canary can exist. The full project stage fold below only runs when
	// artifacts exist — candidates are rare; sends are not.
	cands, rerr := ReadLearningCandidates(s.projectRoot)
	if rerr != nil || len(cands) == 0 {
		return nil
	}
	c, err := s.store.GetConversation(ctx, convID)
	if err != nil {
		return nil
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return nil
	}
	var lanes [][]store.Event
	if wss, werr := s.store.ListWorkstreams(ctx, w.ProjectID); werr == nil {
		for _, ws := range wss {
			cc, cerr := s.store.GetActiveConversation(ctx, ws.ID)
			if cerr != nil {
				continue
			}
			if events, lerr := s.store.ListEvents(ctx, cc.ID, 0); lerr == nil {
				lanes = append(lanes, events)
			}
		}
	}
	table := foldLearningStages(lanes...)
	hash := ""
	newestID := int64(-1)
	for h, info := range table {
		if info.To != "canary" {
			continue
		}
		if info.id > newestID {
			hash, newestID = h, info.id
		}
	}
	if hash == "" {
		return nil
	}
	for _, cand := range cands {
		if cand.ArtifactHash == hash {
			c := cand
			return &c
		}
	}
	return nil // stage row without an artifact: tamper surface, never inject
}

// learningCandidateContent resolves a bound cohort's injection bytes:
// the candidates.jsonl row (creation truth), falling back to its pinned
// learning_canary snapshot (tamper-tolerant second source). "" when both
// are missing — never inject what cannot be verified.
func learningCandidateContent(events []store.Event, cands []LearningCandidate, hash string) string {
	for _, c := range cands {
		if c.ArtifactHash == hash {
			return c.Content
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Layer   string `json:"layer"`
			Cause   string `json:"cause"`
			Hash    string `json:"artifact_hash"`
			Content string `json:"content"`
		}
		if !jsonUnmarshalOK(ev.Payload, &p) || p.Layer != "learning_canary" ||
			p.Cause != "snapshot" || p.Hash != hash {
			continue
		}
		return p.Content
	}
	return ""
}

// learningCanaryBlock resolves the block this run must inject instead of
// the live memory.md ("" = ride live). mode root rolls the ordinal and
// journals the assignment (+ snapshot pin) BEFORE the run's user_message;
// inherit follows the chain's bound row and journals nothing.
func (s *Server) learningCanaryBlock(ctx context.Context, convID int64, events []store.Event, mode learningCohortMode) string {
	if mode == learningCohortLive {
		return ""
	}
	roots := learningChainRoots(events)
	if mode == learningCohortRoot {
		cand := s.learningCurrentCanary(ctx, convID)
		if cand == nil {
			return ""
		}
		m := learningCanaryM(learningCanaryFraction())
		ordinal := len(roots) + 1
		if ordinal%m != 0 {
			return ""
		}
		sha := sha16([]byte(cand.Content))
		s.journalLearningCohort(ctx, convID, cand.ArtifactHash, ordinal, sha)
		s.pinLearningCanarySnapshot(ctx, convID, events, *cand)
		return cand.Content
	}
	// Inherit: the chain's bound cohort, stage-flip-proof by construction.
	row, ok := learningChainCohort(events, roots)
	if !ok {
		return ""
	}
	cands, rerr := ReadLearningCandidates(s.projectRoot)
	if rerr != nil {
		return ""
	}
	return learningCandidateContent(events, cands, row.artifactHash)
}

// journalLearningCohort writes the pre-run assignment row (R3: journaled
// BEFORE the run). Fail-soft: a lost row degrades the chain to live on
// continuations — attribution never lies (the receipt hash is the block).
func (s *Server) journalLearningCohort(ctx context.Context, convID int64, hash string, ordinal int, blockSHA string) {
	if _, err := s.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":        "learning_cohort",
		"artifact_hash": hash,
		"conv_seq":      ordinal,
		"run":           "send",
		"block_sha":     blockSHA,
	})); err != nil {
		log.Printf("learning: journal learning_cohort: %v", err)
	}
}

// pinLearningCanarySnapshot materializes the candidate's block bytes as
// memory_update{layer:"learning_canary", cause:"snapshot"} on first use
// (the journalRuleSnapshots discipline: the send/continuation receipt's
// hash resolves to pinned content for the audit join). Idempotent per
// (artifact, sha): an unchanged pin journals nothing.
func (s *Server) pinLearningCanarySnapshot(ctx context.Context, convID int64, events []store.Event, cand LearningCandidate) {
	sha := sha16([]byte(cand.Content))
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
			Hash  string `json:"artifact_hash"`
			Sha   string `json:"sha"`
		}
		if !jsonUnmarshalOK(ev.Payload, &p) || p.Layer != "learning_canary" || p.Cause != "snapshot" || p.Hash != cand.ArtifactHash {
			continue
		}
		if p.Sha == sha {
			return // already pinned with the same bytes
		}
		break
	}
	if _, err := s.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":         "learning_canary",
		"cause":         "snapshot",
		"source":        "learning/" + cand.ArtifactHash,
		"artifact_hash": cand.ArtifactHash,
		"sha":           sha,
		"content":       cand.Content,
	})); err != nil {
		log.Printf("learning: pin canary snapshot: %v", err)
	}
}
