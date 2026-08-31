package ipc

// D9-W6 (lock stage machine's human legs + wave slicing; K3 spec §6 W6):
// the HUMAN learning actions — drop, held-state apply, promote --global —
// shared by the daemon's learning_action IPC handler and the
// `odo learning drop|apply|promote --global` CLIs (one core per action,
// never two actuation paths; the unretract/rules-retract write-CLI
// convention means the CLI rides the same exported free functions with
// store.Open — read-write, WAL coexistence).
//
// Stage legs and journal shapes (all additive, all journaled on MAIN —
// the audit-sink lane, rules-audit sink precedent):
//
//	drop            any non-terminal stage → dropped. Candidate-layer ONLY:
//	                review_action{action:"learning_drop", from_stage, actor:
//	                "human"} FIRST (marker-first, the learning_rollback
//	                precedent), then learning_stage{cause:"dropped_by_human",
//	                actor:"human"}. NEVER touches memory.md — lines a
//	                prior promotion landed ride `odo rules retract` (D4).
//	                A human drop is voluntary (not harmful evidence), so it
//	                feeds NO freeze (the R2 fold reads rollback/frozen rows
//	                only, learning_lint.go).
//	apply           held_for_human → project_active (K3 §1.3: apply_memory /
//	                `odo rules retract` / `odo learning apply` — the human
//	                resolution of a retract-carrying delta whose stats
//	                passed). The receipted apply path: memory_apply marker
//	                (epoch −1 sentinel, actor "human", recovery block) →
//	                stage row → archive-first writes → apply/rotate/retract
//	                receipts — the exact learningPromoteApply convention,
//	                with retractions riding planMemoryApply's retraction-
//	                with-record arm (rule "" + contradicts target).
//	promote_global  project_active → global_active. `odo learning promote
//	                --global` ONLY (locked: human, never daemon-initiated).
//	                Marker-first: review_action{action:"learning_promote",
//	                scope:"global"} carrying the measured evidence tuple
//	                (cohort counts, epochs, harmful-tuple ABSENCE verified
//	                at promote time through the same never-score measure
//	                machinery — a candidate whose adds read harmful NOW is
//	                refused, the rollback path owns it), then the stage
//	                row. NEVER writes user.md — zero file I/O; the result
//	                carries the rule lines for the human to add by hand
//	                (D4 ruling ④ absolute: the memory_autogate tiers
//	                govern user.md writes, and no tier is bypassed here
//	                because nothing writes).
//
// Terminal stages (dropped / rolled_back / frozen / global_active) refuse
// every action. global_active re-promote refuses too — idempotence by
// refusal, never a silently re-journaled marker.

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/yingliang-zhang/odo/internal/store"
)

// LearningActionResult is the daemon/CLI-shared outcome of one human
// action (the learning_action IPC payload; the CLI renders the same
// fields).
type LearningActionResult struct {
	Action       string `json:"action"` // "drop" | "apply" | "promote_global"
	ArtifactHash string `json:"artifact_hash"`
	FromStage    string `json:"from_stage"`
	ToStage      string `json:"to_stage"`
	Epoch        int    `json:"epoch"` // main-lane epoch the rows stamp (0 = main never distilled)
	// MarkerSeq is the action marker's journal seq (learning_drop /
	// memory_apply / learning_promote row); StageSeq the transition row's.
	// Converge-path applies (rules already landed) carry MarkerSeq 0.
	MarkerSeq int `json:"marker_seq"`
	StageSeq  int `json:"stage_seq"`
	// promote_global: the verbatim rule lines for the human to add to
	// ~/.odo/user.md by hand (NEVER written daemon/CLI-side, D4 ruling ④).
	RuleLines []string `json:"rule_lines,omitempty"`
	// apply: the receipt pair + retraction outcomes (archive records ride
	// the marker's recovery block).
	BeforeSHA         string   `json:"before_sha,omitempty"`
	AfterSHA          string   `json:"after_sha,omitempty"`
	Retracted         []string `json:"retracted,omitempty"`
	UnmatchedRetracts []string `json:"unmatched_retracts,omitempty"`
	// Present marks the converge path: the candidate's rules were already
	// in memory.md (crash-retry or identical human add) — the stage row
	// journaled present:true with no marker and no write.
	Present bool `json:"present,omitempty"`
}

// learningTerminalStages refuse every human action (the fold's terminal
// set, K3 §1.3).
var learningTerminalStages = map[string]bool{
	"dropped":       true,
	"rolled_back":   true,
	"frozen":        true,
	"global_active": true,
}

// resolveLearningCandidate finds one candidate by full artifact hash or a
// UNIQUE hash prefix (the git-ref convention). Zero or multiple prefix
// matches refuse with the count named.
func resolveLearningCandidate(cands []LearningCandidate, id string) (LearningCandidate, error) {
	if id == "" {
		return LearningCandidate{}, fmt.Errorf("empty candidate reference — pass a hash or unique prefix")
	}
	var matched []LearningCandidate
	for _, c := range cands {
		if c.ArtifactHash == id {
			return c, nil
		}
		if len(id) <= len(c.ArtifactHash) && c.ArtifactHash[:len(id)] == id {
			matched = append(matched, c)
		}
	}
	switch len(matched) {
	case 0:
		return LearningCandidate{}, fmt.Errorf("no learning candidate matches %q", id)
	case 1:
		return matched[0], nil
	default:
		return LearningCandidate{}, fmt.Errorf("%q is ambiguous — %d candidates share the prefix (pass more characters)", id, len(matched))
	}
}

// learningActionMainLane resolves the audit-sink lane (main's active
// conversation) + its full event journal — the human-action rows' journal
// target and the epoch clock's source.
func learningActionMainLane(ctx context.Context, st *store.Store, p store.Project) (store.Conversation, []store.Event, error) {
	w, err := st.GetWorkstreamByName(ctx, p.ID, RulesAuditMainWorkstream)
	if err != nil {
		return store.Conversation{}, nil, fmt.Errorf("main workstream: %w", err)
	}
	c, err := st.GetActiveConversation(ctx, w.ID)
	if err != nil {
		return store.Conversation{}, nil, fmt.Errorf("main conversation: %w", err)
	}
	events, err := st.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return store.Conversation{}, nil, fmt.Errorf("main events: %w", err)
	}
	return c, events, nil
}

// learningActionStage folds one candidate's stage (the "candidate"
// default covers a pre-gates crash window: a jsonl row with no stage row
// yet reads as the initial stage, the status fold's convention).
func learningActionStage(ctx context.Context, st *store.Store, p store.Project, hash string) string {
	info, _ := learningStageOfStore(ctx, st, p.ID, hash)
	if info.To == "" {
		return "candidate"
	}
	return info.To
}

// learningCandidateAddTexts returns the candidate's verbatim delta.add
// texts (journal evidence + the promote result's rule lines).
func learningCandidateAddTexts(cand LearningCandidate) []string {
	out := make([]string, 0, len(cand.Delta.Add))
	for _, a := range cand.Delta.Add {
		out = append(out, a.Rule)
	}
	return out
}

// learningHumanStageRow journals the transition row for a human action
// (journalLearningStage's CLI-side twin — one payload family). Exported-
// core callers (drop/apply/promote) share it; the seq is the row's.
func learningHumanStageRow(ctx context.Context, st *store.Store, convID int64, hash, from, to, cause string, epoch, seqRef int, extra map[string]interface{}) (int, error) {
	payload := map[string]interface{}{
		"action":        "learning_stage",
		"artifact_hash": hash,
		"from":          from,
		"to":            to,
		"cause":         cause,
		"actor":         "human",
		"epoch":         epoch,
	}
	if seqRef > 0 {
		payload["marker_seq"] = seqRef
	}
	for k, v := range extra {
		payload[k] = v
	}
	ev, err := st.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(payload))
	if err != nil {
		return 0, err
	}
	return ev.Seq, nil
}

// LearningDropCandidate implements `odo learning drop` / learning_action
// drop (any non-terminal stage → dropped; candidate-layer ONLY).
func LearningDropCandidate(ctx context.Context, st *store.Store, p store.Project, id string) (LearningActionResult, error) {
	cands, err := ReadLearningCandidates(p.RootPath)
	if err != nil {
		return LearningActionResult{}, err
	}
	cand, err := resolveLearningCandidate(cands, id)
	if err != nil {
		return LearningActionResult{}, err
	}
	from := learningActionStage(ctx, st, p, cand.ArtifactHash)
	if learningTerminalStages[from] {
		return LearningActionResult{}, fmt.Errorf("candidate %s is already in terminal stage %q", trimLearningHash(cand.ArtifactHash), from)
	}
	conv, events, err := learningActionMainLane(ctx, st, p)
	if err != nil {
		return LearningActionResult{}, fmt.Errorf("learning drop: %w", err)
	}
	epoch := learningMainEpochFrom(events)

	// Marker-first (the learning_rollback precedent): an unjournaled drop
	// never happens.
	marker := map[string]interface{}{
		"action":        "learning_drop",
		"artifact_hash": cand.ArtifactHash,
		"from_stage":    from,
		"epoch":         epoch,
		"actor":         "human",
		"rules":         learningCandidateAddTexts(cand),
	}
	if from == "project_active" {
		// R1 honesty: the candidate's rules may already live in memory.md
		// through the receipted path. The drop leaves them untouched
		// (D4) — `odo rules retract` owns them.
		marker["landed"] = true
	}
	markerEv, err := st.AppendEvent(ctx, conv.ID, store.EventReviewAction, mustJSON(marker))
	if err != nil {
		return LearningActionResult{}, fmt.Errorf("learning drop: journal learning_drop: %w", err)
	}
	stageSeq, err := learningHumanStageRow(ctx, st, conv.ID, cand.ArtifactHash, from, "dropped", "dropped_by_human", epoch, markerEv.Seq, nil)
	if err != nil {
		return LearningActionResult{}, fmt.Errorf("learning drop: journal stage row: %w", err)
	}
	return LearningActionResult{
		Action: "drop", ArtifactHash: cand.ArtifactHash, FromStage: from, ToStage: "dropped",
		Epoch: epoch, MarkerSeq: markerEv.Seq, StageSeq: stageSeq,
	}, nil
}

// LearningApplyCandidate implements `odo learning apply` / learning_action
// apply: the human-held receipted apply (held_for_human → project_active).
// The marker-first order mirrors learningPromoteApply: marker → stage row →
// archive-first writes → receipts. Callers needing cross-write exclusion
// (the daemon handler) hold memMu around the call; CLIs ride the store's
// WAL coexistence (rules-retract precedent).
func LearningApplyCandidate(ctx context.Context, st *store.Store, p store.Project, id string) (LearningActionResult, error) {
	cands, err := ReadLearningCandidates(p.RootPath)
	if err != nil {
		return LearningActionResult{}, err
	}
	cand, err := resolveLearningCandidate(cands, id)
	if err != nil {
		return LearningActionResult{}, err
	}
	from := learningActionStage(ctx, st, p, cand.ArtifactHash)
	if learningTerminalStages[from] {
		return LearningActionResult{}, fmt.Errorf("candidate %s is in terminal stage %q — nothing left to apply", trimLearningHash(cand.ArtifactHash), from)
	}
	if from != "held_for_human" {
		return LearningActionResult{}, fmt.Errorf("candidate %s is %q, not held_for_human — `odo learning apply` resolves held candidates only (evidence-owned stages promote measured; a canary waits for its cohorts)", trimLearningHash(cand.ArtifactHash), from)
	}
	conv, events, err := learningActionMainLane(ctx, st, p)
	if err != nil {
		return LearningActionResult{}, fmt.Errorf("learning apply: %w", err)
	}
	epoch := learningMainEpochFrom(events)

	accepted := make([]acceptedRule, 0, len(cand.Delta.Add)+len(cand.Delta.Retract))
	for _, a := range cand.Delta.Add {
		accepted = append(accepted, acceptedRule{rule: a.Rule, evidence: a.Evidence})
	}
	// Retract targets ride the planner's retraction-with-record arm
	// (rule "" + contradicts — the replay projection's own convention,
	// learning_replay.go).
	for _, t := range cand.Delta.Retract {
		accepted = append(accepted, acceptedRule{rule: "", contradicts: t})
	}

	memPath := filepath.Join(p.RootPath, ".odo", memoryFileName)
	oldMem := readFileFull(memPath)
	plan := planMemoryApply(oldMem, accepted, nil, epoch)

	if plan.content == oldMem && plan.archiveAppend == "" {
		// Converge (crash-retry / identical human add): the rules already
		// landed — journal ONLY the stage flip with present:true (the
		// learningPromoteApply converge convention; no marker, no write).
		stageSeq, serr := learningHumanStageRow(ctx, st, conv.ID, cand.ArtifactHash, from, "project_active", "applied_by_human", epoch, 0, map[string]interface{}{"present": true})
		if serr != nil {
			return LearningActionResult{}, fmt.Errorf("learning apply: journal stage row: %w", serr)
		}
		return LearningActionResult{
			Action: "apply", ArtifactHash: cand.ArtifactHash, FromStage: from, ToStage: "project_active",
			Epoch: epoch, StageSeq: stageSeq, Present: true,
		}, nil
	}

	// Marker-first: the journaled intent + recovery block precede every
	// file write (boot replayer repairs the crash window; planMemoryApply's
	// dedup-skip converges a retry whose write landed without a receipt).
	beforeSHA := sha16([]byte(oldMem))
	afterSHA := sha16([]byte(plan.content))
	recovery := applyRecovery{
		Memory: &applyRecoveryLayer{
			BeforeSHA: beforeSHA, AfterSHA: afterSHA,
			Body: plan.content, Entries: plan.addedEntries,
		},
	}
	var oldArchive string
	if plan.archiveAppend != "" {
		oldArchive = readArchive(p.RootPath)
		recovery.Archive = &applyRecoveryLayer{
			BeforeSHA: sha16([]byte(oldArchive)),
			AfterSHA:  sha16([]byte(oldArchive + plan.archiveAppend)),
			Body:      plan.archiveAppend, // append chunk only (batch convention)
		}
	}
	markerEv, err := st.AppendEvent(ctx, conv.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":        "memory_apply",
		"epoch":         learningPromoteEpochKey,
		"actor":         "human",
		"artifact_hash": cand.ArtifactHash,
		"metrics":       map[string]int{"promoted": len(cand.Delta.Add), "retracted": len(plan.retracted)},
		"recovery":      recovery,
	}))
	if err != nil {
		return LearningActionResult{}, fmt.Errorf("learning apply: journal memory_apply marker: %w", err)
	}
	markerSeq := markerEv.Seq
	if _, err := learningHumanStageRow(ctx, st, conv.ID, cand.ArtifactHash, from, "project_active", "applied_by_human", epoch, markerSeq, nil); err != nil {
		return LearningActionResult{}, fmt.Errorf("learning apply: journal stage row: %w", err)
	}

	// Writes: archive record FIRST, memory.md last (the batch order — a
	// mid-sequence archive failure leaves the previous memory.md intact;
	// the marker owns convergence either way).
	if plan.archiveAppend != "" {
		arcPath := filepath.Join(p.RootPath, ".odo", archiveFileName)
		if err := writeFileWithin(p.RootPath, arcPath, oldArchive+plan.archiveAppend, 0o644); err != nil {
			return LearningActionResult{}, fmt.Errorf("learning apply: append archive: %w", err)
		}
	}
	if err := writeFileWithin(p.RootPath, memPath, plan.content, 0o644); err != nil {
		return LearningActionResult{}, fmt.Errorf("learning apply: write memory.md: %w (marker-first: the boot replayer repairs the file)", err)
	}

	// Receipts (the applyResolvedBatch journal family): apply, plus
	// rotate / retract as DISTINCT causes, plus unmatched-retract rows —
	// honest per-text outcomes, never silent.
	detail := "learning apply " + cand.ArtifactHash + ": promoted " + strconv.Itoa(len(cand.Delta.Add)) + " rule(s)"
	if _, err := st.AppendEvent(ctx, conv.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":      "memory",
		"cause":      "apply",
		"before_sha": beforeSHA,
		"after_sha":  afterSHA,
		"detail":     detail,
	})); err != nil {
		return LearningActionResult{}, fmt.Errorf("learning apply: journal apply receipt: %w", err)
	}
	if len(plan.rotated) > 0 {
		if _, err := st.AppendEvent(ctx, conv.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":      "memory",
			"cause":      "rotate",
			"before_sha": beforeSHA,
			"after_sha":  afterSHA,
			"detail":     fmt.Sprintf("rotated %d to memory-archive.md (overflow)", len(plan.rotated)),
		})); err != nil {
			return LearningActionResult{}, fmt.Errorf("learning apply: journal rotate receipt: %w", err)
		}
	}
	if len(plan.retracted) > 0 {
		if _, err := st.AppendEvent(ctx, conv.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":      "memory",
			"cause":      "retract",
			"before_sha": beforeSHA,
			"after_sha":  afterSHA,
			"detail":     fmt.Sprintf("retracted %d (learning apply)", len(plan.retracted)),
		})); err != nil {
			return LearningActionResult{}, fmt.Errorf("learning apply: journal retract receipt: %w", err)
		}
	}
	for _, unmatched := range plan.unmatchedContradicts {
		if _, err := st.AppendEvent(ctx, conv.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "memory",
			"cause":  "retract",
			"detail": fmt.Sprintf("no match for contradicts: %q", unmatched),
		})); err != nil {
			return LearningActionResult{}, fmt.Errorf("learning apply: journal unmatched-retract row: %w", err)
		}
	}

	return LearningActionResult{
		Action: "apply", ArtifactHash: cand.ArtifactHash, FromStage: from, ToStage: "project_active",
		Epoch: epoch, MarkerSeq: markerSeq, BeforeSHA: beforeSHA, AfterSHA: afterSHA,
		Retracted: plan.retracted, UnmatchedRetracts: plan.unmatchedContradicts,
	}, nil
}

// LearningPromoteGlobal implements `odo learning promote --global` /
// learning_action promote_global (project_active → global_active, HUMAN
// ONLY). Marker-first; NEVER writes user.md (zero file I/O — the rule
// lines return for hand-addition).
func LearningPromoteGlobal(ctx context.Context, st *store.Store, p store.Project, id string) (LearningActionResult, error) {
	cands, err := ReadLearningCandidates(p.RootPath)
	if err != nil {
		return LearningActionResult{}, err
	}
	cand, err := resolveLearningCandidate(cands, id)
	if err != nil {
		return LearningActionResult{}, err
	}
	if cand.Scope != "project:memory" {
		return LearningActionResult{}, fmt.Errorf("candidate %s has scope %q — policy-scope candidates need a separate lock (waves 3–6 build project:memory only)", trimLearningHash(cand.ArtifactHash), cand.Scope)
	}
	from := learningActionStage(ctx, st, p, cand.ArtifactHash)
	switch from {
	case "project_active":
		// the one promotable stage (K3 §1.3)
	case "global_active":
		return LearningActionResult{}, fmt.Errorf("candidate %s is already global_active — nothing to do", trimLearningHash(cand.ArtifactHash))
	default:
		if learningTerminalStages[from] {
			return LearningActionResult{}, fmt.Errorf("candidate %s is in terminal stage %q — it never reached project_active", trimLearningHash(cand.ArtifactHash), from)
		}
		return LearningActionResult{}, fmt.Errorf("candidate %s is %q — only project_active candidates promote globally (the paired-cohort promotion runs first)", trimLearningHash(cand.ArtifactHash), from)
	}
	conv, events, err := learningActionMainLane(ctx, st, p)
	if err != nil {
		return LearningActionResult{}, fmt.Errorf("learning promote --global: %w", err)
	}
	epoch := learningMainEpochFrom(events)

	// Evidence tuple (the task's "same evidence tuple as the project-level
	// path"): the measure fold over the project_active window — same never-
	// score machinery as the daemon's tick; harmful-tuple absence is
	// VERIFIED now, not inherited from the old promotion (a rule that
	// turned harmful since must not go global).
	in := gatherLearningReplayInputStore(ctx, st, p.ID)
	since := learningStageSince(in.laneEvents(), cand.ArtifactHash, "project_active")
	m := computeLearningMeasure(in, cand, since, epoch)
	m.Kind = "global_promote"
	for _, r := range m.Rules {
		if r.Harmful {
			return LearningActionResult{}, fmt.Errorf("candidate %s: rule %q meets the harmful tuple now — global promotion refused; the rollback/retract paths own it (checkpoint re-measure will fold it rolled_back)", trimLearningHash(cand.ArtifactHash), r.Rule)
		}
	}

	// Marker-first: the evidence-carrying promote row precedes the stage
	// flip; an unjournaled promotion never happens.
	markerEv, err := st.AppendEvent(ctx, conv.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":         "learning_promote",
		"artifact_hash":  cand.ArtifactHash,
		"scope":          "global",
		"epoch":          epoch,
		"actor":          "human",
		"window_from":    since,
		"canary":         m.Canary,
		"live":           m.Live,
		"baseline":       m.Baseline,
		"rules":          m.Rules,
		"excluded":       m.Excluded,
		"harmful_absent": true,
		"rule_lines":     learningCandidateAddTexts(cand),
	}))
	if err != nil {
		return LearningActionResult{}, fmt.Errorf("learning promote --global: journal learning_promote: %w", err)
	}
	stageSeq, err := learningHumanStageRow(ctx, st, conv.ID, cand.ArtifactHash, from, "global_active", "promoted_global", epoch, markerEv.Seq, nil)
	if err != nil {
		return LearningActionResult{}, fmt.Errorf("learning promote --global: journal stage row: %w", err)
	}
	return LearningActionResult{
		Action: "promote_global", ArtifactHash: cand.ArtifactHash, FromStage: from, ToStage: "global_active",
		Epoch: epoch, MarkerSeq: markerEv.Seq, StageSeq: stageSeq,
		RuleLines: learningCandidateAddTexts(cand),
	}, nil
}

// trimLearningHash renders a hash's first 8 chars for errors/advisories.
func trimLearningHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// learningActionAdvisory renders a daemon-side transcript row for one
// action (the journalRunAdvisory precedent — GUI-initiated human actions
// surface where the user reads; CLI runs print to stdout instead).
func learningActionAdvisory(res LearningActionResult) string {
	short := trimLearningHash(res.ArtifactHash)
	switch res.Action {
	case "drop":
		return "learning: candidate " + short + " dropped by human (was " + res.FromStage + ") — candidate-layer only; any landed memory.md lines stay until `odo rules retract` (D4)"
	case "apply":
		if res.Present {
			return "learning: candidate " + short + " confirmed project_active by human (rules already present — converged, no write)"
		}
		return "learning: candidate " + short + " applied by human (held_for_human → project_active; receipted memory.md apply)"
	case "promote_global":
		return "learning: candidate " + short + " promoted to global_active by human — user.md is human-owned forever; add the rule line(s) to ~/.odo/user.md by hand (never written by odo)"
	}
	return "learning: candidate " + short + " action " + res.Action
}

// handleLearningAction implements learning_action: the daemon exposure of
// the W6 human actions (GUI-proof contract; the CLI rides the same cores
// by direct call). apply takes memMu — the apply's archive+memory.md
// writes race the daemon's other memory writers (2026-08-25 audit P1)
// inside this process.
func (s *Server) handleLearningAction(ctx context.Context, req Request) (Response, error) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		return Response{}, err
	}
	var res LearningActionResult
	switch req.Action {
	case "drop":
		res, err = LearningDropCandidate(ctx, s.store, p, req.Hash)
	case "apply":
		s.memMu.Lock()
		res, err = LearningApplyCandidate(ctx, s.store, p, req.Hash)
		s.memMu.Unlock()
	case "promote_global":
		res, err = LearningPromoteGlobal(ctx, s.store, p, req.Hash)
	default:
		return Response{}, fmt.Errorf("learning_action: unknown action %q (want drop|apply|promote_global)", req.Action)
	}
	if err != nil {
		return Response{}, err
	}
	// Transcript advisory (best-effort; the rollback convention —
	// journalRunAdvisory logs its own failures).
	if conv, _, cerr := learningActionMainLane(ctx, s.store, p); cerr == nil {
		_ = s.journalRunAdvisory(ctx, conv.ID, learningActionAdvisory(res))
	}
	return Response{LearningAction: &res}, nil
}
