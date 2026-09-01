package ipc

// D9-W5 end-to-end tests (lock R1/R2 + task acceptance): R1 two-layer
// rollback (candidate demote + D4 retract_candidate receipts, memory.md
// byte-untouched, restore bounded, freeze re-entry), marker-first
// promotion (paired cohorts actuation + crash-repair via the replayer),
// pairing guard, retract hold, harmful drop, freeze stage-interrupt
// (shadow + held_for_human, once-only dedup, N+4 boundary free),
// stall advisories (fire once, never promote, never drop), and the
// single-canary-slot actuation exclusivity.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// w5Lineage journals the candidate's learning_candidate lineage row
// (main_epoch keys the shadow aging clock).
func w5Lineage(t *testing.T, rig *testRig, convID int64, hash string, mainEpoch int) {
	t.Helper()
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action": "learning_candidate", "artifact_hash": hash, "main_epoch": mainEpoch})); err != nil {
		t.Fatalf("lineage: %v", err)
	}
}

// w5RollForward journals the stage chain to a target stage, in order,
// with the epoch key (W5 shape).
func w5RollForward(t *testing.T, rig *testRig, convID int64, hash, target string, epoch int) {
	t.Helper()
	chain := []struct{ from, to string }{{"candidate", "shadow"}}
	switch target {
	case "canary":
		chain = append(chain, struct{ from, to string }{"shadow", "canary"})
	case "project_active":
		chain = append(chain, struct{ from, to string }{"shadow", "canary"}, struct{ from, to string }{"canary", "project_active"})
	case "held_for_human":
		chain = append(chain, struct{ from, to string }{"shadow", "canary"}, struct{ from, to string }{"canary", "held_for_human"})
	}
	for _, tr := range chain {
		w5Stage(t, rig, convID, hash, tr.from, tr.to, epoch)
	}
}

// payloadsBySubstring collects decoded payloads whose action matches.
func w5EventsByAction(t *testing.T, events []store.Event, action string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p map[string]interface{}
		if json.Unmarshal(ev.Payload, &p) == nil && p["action"] == action {
			out = append(out, p)
		}
	}
	return out
}

// w5RunTick gathers the rig's project input + runs the measure tick at
// epoch newEpoch.
func w5RunTick(t *testing.T, rig *testRig, convID int64, newEpoch int) {
	t.Helper()
	ctx := context.Background()
	c, err := rig.store.GetConversation(ctx, convID)
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	rig.server.learningMeasureTick(ctx, c, newEpoch)
}

// w5SeedHarmfulRuleTraffic journals the exact harmful tuple for ruleText
// over cohortSHA: 10 injections, 3 rejects spread across the three conv
// ids, plus baselineDilute accepts on a clean cohort so the 2× rate leg
// trips. Requires ≥3 conv ids.
func w5SeedHarmfulRuleTraffic(t *testing.T, rig *testRig, convs []int64, cohortSHA, cleanSHA, patchPath string) {
	t.Helper()
	if len(convs) < 3 {
		t.Fatalf("harmful tuple needs ≥3 reject conversations")
	}
	// 10 injections of the rule cohort: 7 accepts + 3 rejects, rejects in
	// 3 distinct conversations.
	w5SeedLaneOutcome(t, rig, convs[0], cohortSHA, patchPath, "reject", "")
	w5SeedLaneOutcome(t, rig, convs[1], cohortSHA, patchPath, "reject", "")
	w5SeedLaneOutcome(t, rig, convs[2], cohortSHA, patchPath, "reject", "")
	for i := 0; i < 7; i++ {
		w5SeedLaneOutcome(t, rig, convs[0], cohortSHA, patchPath, "accept", "")
	}
	// Baseline dilution: clean cohort accepts (2× rate leg).
	for i := 0; i < 20; i++ {
		w5SeedLaneOutcome(t, rig, convs[0], cleanSHA, patchPath, "accept", "")
	}
}

// TestLearningRollbackR1TwoLayer: a project_active candidate whose own
// add meets the harmful tuple rolls back on the CANDIDATE layer (stage
// demotion, freeze re-entry) and emits D4 retract_candidate receipts for
// the memory-layer texts — memory.md byte-untouched; absent add texts are
// journaled honest (present:false, zero receipts).
func TestLearningRollbackR1TwoLayer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, lanes := w5RigWithLanes(t, root, "ui", "ops")
	defer rig.stop(t)
	ctx := context.Background()
	dir := t.TempDir()
	patch := w4Patch(t, dir, "f.diff", "src/feature.go")

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	mem := base + "- Bad rule alpha — cites: main-epoch-1; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", mem)

	adds := []LearningRuleAdd{
		{Rule: "Bad rule alpha", Evidence: "main-epoch-1"},
		{Rule: "Missing rule gamma", Evidence: "main-epoch-1"}, // never landed: present:false
	}
	cand := w5Candidate(t, root, mem, adds, nil)
	w5Lineage(t, rig, convID, cand.ArtifactHash, 1)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "project_active", 1)

	// A sibling canary carrying the SAME text — the rollback must not
	// touch it (over-reach pin); its own stall/freeze lifecycle applies.
	sibling := w5Candidate(t, root, mem, adds[:1], nil)
	w5Lineage(t, rig, convID, sibling.ArtifactHash, 1)
	w5RollForward(t, rig, convID, sibling.ArtifactHash, "canary", 1)

	cohortSHA := w5SeedSnapshot(t, rig, convID, mem)
	cleanSHA := w5SeedSnapshot(t, rig, convID, base)
	convs := []int64{convID, lanes["ui"], lanes["ops"]}
	w5SeedHarmfulRuleTraffic(t, rig, convs, cohortSHA, cleanSHA, patch)

	memBefore := readFileStr(t, filepath.Join(root, ".odo", "memory.md"))
	w5RunTick(t, rig, convID, 5)
	rows := allEvents(t, rig, convID)

	// Layer 1: the rollback row (marker-first evidence tuple — feeds R2).
	rb := w5EventsByAction(t, rows, "learning_rollback")
	if len(rb) != 1 {
		t.Fatalf("learning_rollback rows = %d, want 1", len(rb))
	}
	if rb[0]["artifact_hash"] != cand.ArtifactHash || rb[0]["epoch"] != float64(5) || rb[0]["reason"] != "harmful_tuple" {
		t.Errorf("rollback row = %+v", rb[0])
	}
	retracted, _ := rb[0]["retracted"].([]interface{})
	if len(retracted) != 1 || retracted[0] != "Bad rule alpha" {
		t.Errorf("retracted = %v, want [Bad rule alpha] (gamma not harmful ⇒ not a target)", retracted)
	}
	present, _ := rb[0]["present"].(map[string]interface{})
	if present["Bad rule alpha"] != true {
		t.Errorf("present[alpha] = %v, want true (landed in memory.md)", present["Bad rule alpha"])
	}

	// Stage demotion: instant, fold-derived.
	if info, ok := rig.server.learningStageOf(ctx, 1, cand.ArtifactHash); !ok || info.To != "rolled_back" {
		t.Fatalf("stage = %q ok %v, want rolled_back", info.To, ok)
	}

	// Layer 2: D4 receipts — ONLY for texts present in memory.md.
	receipts := memoryUpdatesByCause(t, rows, "retract_candidate")
	if len(receipts) != 1 {
		t.Fatalf("retract_candidate receipts = %d, want 1 (gamma absent ⇒ no receipt)", len(receipts))
	}
	rc := receipts[0]
	if rc["layer"] != "memory" || rc["rule"] != "Bad rule alpha" ||
		rc["candidate"] != cand.ArtifactHash || rc["epoch"] != float64(5) {
		t.Errorf("receipt = %+v, want the R1 field set", rc)
	}
	rbSeq := rollbackSeqOf(t, rows)
	if rc["flag_seq"] != float64(rbSeq) {
		t.Errorf("receipt flag_seq = %v, want the rollback row's seq %d", rc["flag_seq"], rbSeq)
	}

	// The daemon NEVER deletes the memory.md line (R1).
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != memBefore {
		t.Errorf("memory.md after rollback = %q, want byte-unchanged %q", got, memBefore)
	}

	// Freeze re-entry (R2): the rolled-back text freezes for 3 main epochs.
	frozen := learningCandidateFreezeSet(rows, 6)
	if reason, ok := frozen[normalizeRule("Bad rule alpha")]; !ok || !strings.Contains(reason, "oscillation_guard") {
		t.Errorf("freeze set = %v, want the rolled-back text frozen (oscillation_guard)", frozen)
	}

	// Over-reach bound: the sibling canary is untouched by the rollback;
	// its own canary lifecycle continues (frozen-text interrupt stalls
	// its promotion — journaled, never a receipt naming it).
	if info, _ := rig.server.learningStageOf(ctx, 1, sibling.ArtifactHash); info.To != "canary" {
		t.Errorf("sibling stage = %q, want canary (rollback never reaches siblings)", info.To)
	}
	for _, r := range receipts {
		if r["candidate"] == sibling.ArtifactHash {
			t.Errorf("sibling got a retract receipt: %+v", r)
		}
	}

	// Novel-detection idempotence: a second tick re-measures but never
	// double-fires (the demoted stage is out of the trigger set).
	w5RunTick(t, rig, convID, 6)
	rows = allEvents(t, rig, convID)
	if rb := w5EventsByAction(t, rows, "learning_rollback"); len(rb) != 1 {
		t.Errorf("learning_rollback rows after re-tick = %d, want still 1", len(rb))
	}
	if r := memoryUpdatesByCause(t, rows, "retract_candidate"); len(r) != 1 {
		t.Errorf("retract_candidate receipts after re-tick = %d, want still 1", len(r))
	}

	// Advisory: the rollback is visible in the transcript.
	if !hasRunAdvisory(t, rows, "learning rollback") {
		t.Error("no transcript advisory journaled for the rollback")
	}
}

// rollbackSeqOf resolves the learning_rollback row's journal seq.
func rollbackSeqOf(t *testing.T, events []store.Event) int {
	t.Helper()
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Action == "learning_rollback" {
			return ev.Seq
		}
	}
	t.Fatal("no learning_rollback row")
	return 0
}

// hasRunAdvisory reports whether an odo advisory transcript row mentions
// the substring.
func hasRunAdvisory(t *testing.T, events []store.Event, sub string) bool {
	t.Helper()
	for _, ev := range events {
		if ev.Type != store.EventAgentError {
			continue
		}
		var p map[string]interface{}
		if json.Unmarshal(ev.Payload, &p) == nil {
			if p["odo"] == true {
				if msg, _ := p["error"].(string); strings.Contains(msg, sub) {
					return true
				}
			}
		}
	}
	return false
}

// TestLearningPromoteFullPassE2E: paired cohorts at the floor, zero
// rejects, zero taint ⇒ marker-first apply (memory_apply{actor:
// learning_promote, epoch −1}), stage canary→project_active, memory.md
// written, and the boot-replayer repairs a post-marker crash exactly.
func TestLearningPromoteFullPassE2E(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, lanes := w5RigWithLanes(t, root, "ui")
	defer rig.stop(t)
	ctx := context.Background()
	dir := t.TempDir()
	patch := w4Patch(t, dir, "f.diff", "src/feature.go")

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	block := base + "- Promotable rule — cites: main-epoch-1; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", base)
	cand := w5Candidate(t, root, block, []LearningRuleAdd{{Rule: "Promotable rule", Evidence: "main-epoch-1"}}, nil)
	w5Lineage(t, rig, convID, cand.ArtifactHash, 1)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "canary", 1)

	canSHA := w5PinCanarySnapshot(t, rig, convID, cand)
	liveSHA := w5SeedSnapshot(t, rig, convID, base)
	for i := 0; i < 6; i++ {
		w5SeedLaneOutcome(t, rig, convID, canSHA, patch, "accept", "")
	}
	for i := 0; i < 4; i++ {
		w5SeedLaneOutcome(t, rig, lanes["ui"], canSHA, patch, "accept", "")
	}
	for i := 0; i < 9; i++ {
		w5SeedLaneOutcome(t, rig, convID, liveSHA, patch, "accept", "")
	}
	// A1: one live reject — liveHarm ≥ 1 satisfies f′ (zero live harm at
	// full floors past the 3-epoch grace is the efficacy_vacuity drop).
	w5SeedLaneOutcome(t, rig, convID, liveSHA, patch, "reject", "")

	w5RunTick(t, rig, convID, 5)
	rows := allEvents(t, rig, convID)

	// The measure row journaled this epoch (cadence); stage_epoch rides
	// it (A1 additive key — the canary-entry epoch the grace/starve
	// clocks read).
	measures := memoryUpdatesByCause(t, rows, "measure")
	if len(measures) != 1 || measures[0]["artifact_hash"] != cand.ArtifactHash || measures[0]["kind"] != "canary" {
		t.Fatalf("measure rows = %+v, want one canary measure", measures)
	}
	if measures[0]["stage_epoch"] != float64(1) {
		t.Errorf("measure stage_epoch = %v, want 1 (canary staged at epoch 1)", measures[0]["stage_epoch"])
	}

	// Stage flipped with the measured cause.
	if info, _ := rig.server.learningStageOf(ctx, 1, cand.ArtifactHash); info.To != "project_active" {
		t.Fatalf("stage = %q, want project_active", info.To)
	}

	// Marker-first apply: actor + sentinel epoch + recovery block.
	applies := w5EventsByAction(t, rows, "memory_apply")
	if len(applies) != 1 {
		t.Fatalf("memory_apply markers = %d, want 1", len(applies))
	}
	ap := applies[0]
	if ap["actor"] != "learning_promote" || ap["epoch"] != float64(learningPromoteEpochKey) ||
		ap["artifact_hash"] != cand.ArtifactHash {
		t.Errorf("promote marker = %+v, want actor learning_promote, epoch −1, artifact cited", ap)
	}

	// The rule landed in memory.md; the memory receipt attests the pair.
	mem := readFileStr(t, filepath.Join(root, ".odo", "memory.md"))
	if !strings.Contains(mem, "Promotable rule") {
		t.Errorf("memory.md missing the promoted rule:\n%s", mem)
	}
	memApplies := memoryUpdatesByCause(t, rows, "apply")
	if len(memApplies) != 1 || memApplies[0]["before_sha"] != sha16([]byte(base)) {
		t.Errorf("memory apply receipts = %+v, want one with the base before_sha", memApplies)
	}

	// findPendingBatch ignores the sentinel (no batch consumption).
	if b := findPendingBatch(rows); b.exists && b.consumed && b.epoch == learningPromoteEpochKey {
		t.Error("sentinel-epoch marker must never fold into a learner batch")
	}

	// Crash-repair: restore the pre-write bytes; the replayer restores
	// the promoted content from the marker's recovery block.
	writeProjFile(t, root, ".odo/memory.md", base)
	if repaired := rig.server.replayLaneMemReceipts(ctx, convID, allEvents(t, rig, convID), replayApply); len(repaired) == 0 {
		t.Fatal("replay repaired nothing from the promote marker")
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != mem {
		t.Errorf("repaired memory.md = %q, want the promoted content", got)
	}

	// Steady state: a second tick keeps the stage (project_active
	// rollback check: one reject is far short of the harmful tuple ⇒
	// no targets).
	w5RunTick(t, rig, convID, 6)
	if info, _ := rig.server.learningStageOf(ctx, 1, cand.ArtifactHash); info.To != "project_active" {
		t.Errorf("stage after re-tick = %q, want project_active", info.To)
	}
}

// TestLearningPromotionPairedGuard: live contrast below the floor ⇒ keep
// measuring — NO promotion, NO apply marker, memory.md untouched
// (self-reinforcing cohort pin).
func TestLearningPromotionPairedGuard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, _ := w5RigWithLanes(t, root)
	defer rig.stop(t)
	dir := t.TempDir()
	patch := w4Patch(t, dir, "f.diff", "src/feature.go")

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	block := base + "- Promotable rule — cites: main-epoch-1; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", base)
	cand := w5Candidate(t, root, block, []LearningRuleAdd{{Rule: "Promotable rule", Evidence: "main-epoch-1"}}, nil)
	w5Lineage(t, rig, convID, cand.ArtifactHash, 1)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "canary", 1)
	canSHA := w5PinCanarySnapshot(t, rig, convID, cand)
	liveSHA := w5SeedSnapshot(t, rig, convID, base)
	for i := 0; i < 10; i++ {
		w5SeedLaneOutcome(t, rig, convID, canSHA, patch, "accept", "")
	}
	for i := 0; i < 9; i++ { // live one short of the floor
		w5SeedLaneOutcome(t, rig, convID, liveSHA, patch, "accept", "")
	}

	w5RunTick(t, rig, convID, 5)
	rows := allEvents(t, rig, convID)
	if info, _ := rig.server.learningStageOf(context.Background(), 1, cand.ArtifactHash); info.To != "canary" {
		t.Errorf("stage = %q, want canary (paired floor unmet)", info.To)
	}
	if applies := w5EventsByAction(t, rows, "memory_apply"); len(applies) != 0 {
		t.Errorf("memory_apply markers = %d, want 0 (paired guard)", len(applies))
	}
	if mem := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); mem != base {
		t.Errorf("memory.md changed under the paired guard:\n%s", mem)
	}
}

// TestLearningPromotionHoldRetractions: passing stats BUT a
// retract-carrying delta ⇒ held_for_human (D4 preserved: only a human
// resolves retractions), zero writes.
func TestLearningPromotionHoldRetractions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, _ := w5RigWithLanes(t, root)
	defer rig.stop(t)
	dir := t.TempDir()
	patch := w4Patch(t, dir, "f.diff", "src/feature.go")

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	block := base + "- Promotable rule — cites: main-epoch-1; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", base)
	cand := w5Candidate(t, root, block,
		[]LearningRuleAdd{{Rule: "Promotable rule", Evidence: "main-epoch-1"}},
		[]string{"Base rule"})
	w5Lineage(t, rig, convID, cand.ArtifactHash, 1)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "canary", 1)
	canSHA := w5PinCanarySnapshot(t, rig, convID, cand)
	liveSHA := w5SeedSnapshot(t, rig, convID, base)
	for i := 0; i < 9; i++ {
		w5SeedLaneOutcome(t, rig, convID, canSHA, patch, "accept", "")
		w5SeedLaneOutcome(t, rig, convID, liveSHA, patch, "accept", "")
	}
	// A1: a 10th canary outcome + one live reject (liveHarm ≥ 1 — f′
	// must pass before the retract hold can fire).
	w5SeedLaneOutcome(t, rig, convID, canSHA, patch, "accept", "")
	w5SeedLaneOutcome(t, rig, convID, liveSHA, patch, "reject", "")

	w5RunTick(t, rig, convID, 5)
	rows := allEvents(t, rig, convID)
	if info, _ := rig.server.learningStageOf(context.Background(), 1, cand.ArtifactHash); info.To != "held_for_human" {
		t.Errorf("stage = %q, want held_for_human (retract delta, stats passing)", info.To)
	}
	if applies := w5EventsByAction(t, rows, "memory_apply"); len(applies) != 0 {
		t.Errorf("memory_apply markers = %d, want 0 (D4: the human resolves)", len(applies))
	}
	if !hasRunAdvisory(t, rows, "held for human") {
		t.Error("no held-for-human advisory journaled")
	}
}

// TestLearningCanaryHarmfulDrop: the candidate's OWN canary cohort meets
// the harmful tuple ⇒ canary→dropped with the harmful rule cited.
func TestLearningCanaryHarmfulDrop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, lanes := w5RigWithLanes(t, root, "ui", "ops")
	defer rig.stop(t)
	dir := t.TempDir()
	patch := w4Patch(t, dir, "f.diff", "src/feature.go")

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	block := base + "- Harmful experiment — cites: main-epoch-1; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", base)
	cand := w5Candidate(t, root, block, []LearningRuleAdd{{Rule: "Harmful experiment", Evidence: "main-epoch-1"}}, nil)
	w5Lineage(t, rig, convID, cand.ArtifactHash, 1)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "canary", 1)
	canSHA := w5PinCanarySnapshot(t, rig, convID, cand)
	liveSHA := w5SeedSnapshot(t, rig, convID, base)
	w5SeedHarmfulRuleTraffic(t, rig, []int64{convID, lanes["ui"], lanes["ops"]}, canSHA, liveSHA, patch)

	w5RunTick(t, rig, convID, 5)
	rows := allEvents(t, rig, convID)
	if info, _ := rig.server.learningStageOf(context.Background(), 1, cand.ArtifactHash); info.To != "dropped" {
		t.Errorf("stage = %q, want dropped (own canary cohort met the harmful tuple)", info.To)
	}
	var drop map[string]interface{}
	for _, s := range w5EventsByAction(t, rows, "learning_stage") {
		if s["to"] == "dropped" {
			drop = s
		}
	}
	if drop == nil || drop["cause"] != "harmful_tuple" {
		t.Fatalf("drop row = %+v, want cause harmful_tuple", drop)
	}
	if hr, _ := drop["harmful_rule"].(map[string]interface{}); hr["rule"] != "Harmful experiment" || hr["harmful"] != true {
		t.Errorf("drop detail harmful_rule = %v, want the experiment's row cited", hr)
	}
	if mem := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); mem != base {
		t.Errorf("memory.md changed under a drop:\n%s", mem)
	}
}

// TestLearningCanaryVacuityDrop (A1 Amendment 3): floors met over
// accept-only traffic — inside the grace window (age 2 < 3) the tick
// keeps measuring; at age 4 ≥ 3 with zero live harm the candidate drops
// efficacy_vacuity (the measured do-nothing class) with f_prime:false
// riding the detail's checks map, ZERO freeze-set entries (vacuity ≠
// harmful — R2 untouched), memory.md byte-untouched, and the advisory
// journaled.
func TestLearningCanaryVacuityDrop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, _ := w5RigWithLanes(t, root)
	defer rig.stop(t)
	ctx := context.Background()
	dir := t.TempDir()
	patch := w4Patch(t, dir, "v.diff", "src/feature.go")

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	block := base + "- Vacuous rule — cites: main-epoch-1; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", base)
	cand := w5Candidate(t, root, block, []LearningRuleAdd{{Rule: "Vacuous rule", Evidence: "main-epoch-1"}}, nil)
	w5Lineage(t, rig, convID, cand.ArtifactHash, 1)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "canary", 1)
	canSHA := w5PinCanarySnapshot(t, rig, convID, cand)
	liveSHA := w5SeedSnapshot(t, rig, convID, base)
	for i := 0; i < 10; i++ {
		w5SeedLaneOutcome(t, rig, convID, canSHA, patch, "accept", "")
		w5SeedLaneOutcome(t, rig, convID, liveSHA, patch, "accept", "")
	}

	// Grace window (age 2 < 3): keep measuring, measure row still
	// journals stage_epoch every epoch (cadence).
	w5RunTick(t, rig, convID, 3)
	if info, _ := rig.server.learningStageOf(ctx, 1, cand.ArtifactHash); info.To != "canary" {
		t.Fatalf("stage at grace = %q, want canary (vacuity drop needs the 3-epoch grace)", info.To)
	}
	// Age 4 ≥ 3 with floors met and liveHarm 0: efficacy_vacuity.
	w5RunTick(t, rig, convID, 5)
	rows := allEvents(t, rig, convID)
	if info, _ := rig.server.learningStageOf(ctx, 1, cand.ArtifactHash); info.To != "dropped" {
		t.Fatalf("stage = %q, want dropped (efficacy_vacuity)", info.To)
	}
	var drop map[string]interface{}
	for _, s := range w5EventsByAction(t, rows, "learning_stage") {
		if s["to"] == "dropped" && s["cause"] == "efficacy_vacuity" {
			drop = s
		}
	}
	if drop == nil {
		t.Fatalf("no efficacy_vacuity drop row in %+v", w5EventsByAction(t, rows, "learning_stage"))
	}
	if drop["drop_cause"] != "efficacy_vacuity" {
		t.Errorf("drop_cause = %v, want efficacy_vacuity", drop["drop_cause"])
	}
	if checks, _ := drop["checks"].(map[string]interface{}); checks["f_prime"] != false {
		t.Errorf("checks = %v, want f_prime false riding the drop detail", checks)
	}
	for _, ms := range memoryUpdatesByCause(t, rows, "measure") {
		if ms["artifact_hash"] == cand.ArtifactHash && ms["stage_epoch"] != float64(1) {
			t.Errorf("measure stage_epoch = %v, want 1 (canary staged at epoch 1)", ms["stage_epoch"])
		}
	}
	// R2 untouched: vacuity ≠ harmful — ZERO freeze-set entries.
	if frozen := learningCandidateFreezeSet(rows, 6); len(frozen) != 0 {
		t.Errorf("freeze set = %v, want EMPTY — vacuous drops write zero freeze entries", frozen)
	}
	if mem := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); mem != base {
		t.Errorf("memory.md changed under a vacuity drop:\n%s", mem)
	}
	if !hasRunAdvisory(t, rows, "efficacy_vacuity") {
		t.Error("no efficacy_vacuity advisory journaled")
	}
	// Never a stall row for a floors-met, full-sample candidate (the
	// busy-but-vacuous hole is subsumed by the drop exit).
	if stalls := memoryUpdatesByCause(t, rows, "learning_stall"); len(stalls) != 0 {
		t.Errorf("learning_stall rows = %d, want 0 (drop exit subsumes the busy-but-vacuous hole)", len(stalls))
	}
}

// TestLearningCanaryStarvedDrop (A1 Amendment 3): floors unmet — at the
// boundary age 24 (NOT > 24) the candidate stays canary with exactly one
// stall advisory; past 2× the stall floor (age 25) it drops
// canary_starved with the exclusion counters riding the detail, ZERO
// freeze-set entries, memory.md byte-untouched.
func TestLearningCanaryStarvedDrop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, _ := w5RigWithLanes(t, root)
	defer rig.stop(t)
	ctx := context.Background()
	dir := t.TempDir()
	patch := w4Patch(t, dir, "s.diff", "src/feature.go")

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	block := base + "- Starved rule — cites: main-epoch-1; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", base)
	cand := w5Candidate(t, root, block, []LearningRuleAdd{{Rule: "Starved rule", Evidence: "main-epoch-1"}}, nil)
	w5Lineage(t, rig, convID, cand.ArtifactHash, 1)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "canary", 1)
	canSHA := w5PinCanarySnapshot(t, rig, convID, cand)
	liveSHA := w5SeedSnapshot(t, rig, convID, base)
	for i := 0; i < 3; i++ { // both cohorts far under the floor
		w5SeedLaneOutcome(t, rig, convID, canSHA, patch, "accept", "")
		w5SeedLaneOutcome(t, rig, convID, liveSHA, patch, "accept", "")
	}

	// Boundary: age 24 = 2× the stall floor exactly — the drop needs
	// age > 24. Stays canary; the stall advisory (age > 12, outcomes
	// short of the floor) fires once for this stage cycle.
	w5RunTick(t, rig, convID, 25)
	rows := allEvents(t, rig, convID)
	if info, _ := rig.server.learningStageOf(ctx, 1, cand.ArtifactHash); info.To != "canary" {
		t.Fatalf("stage at age 24 = %q, want canary (starve drop needs age > 2×12)", info.To)
	}
	stalls := memoryUpdatesByCause(t, rows, "learning_stall")
	if len(stalls) != 1 || stalls[0]["stage"] != "canary" || stalls[0]["stage_epoch"] != float64(1) {
		t.Fatalf("stall rows at boundary = %+v, want one canary row keyed stage_epoch 1", stalls)
	}

	// Age 25 > 24: canary_starved.
	w5RunTick(t, rig, convID, 26)
	rows = allEvents(t, rig, convID)
	if info, _ := rig.server.learningStageOf(ctx, 1, cand.ArtifactHash); info.To != "dropped" {
		t.Fatalf("stage at age 25 = %q, want dropped (canary_starved)", info.To)
	}
	var drop map[string]interface{}
	for _, s := range w5EventsByAction(t, rows, "learning_stage") {
		if s["to"] == "dropped" && s["cause"] == "canary_starved" {
			drop = s
		}
	}
	if drop == nil || drop["drop_cause"] != "canary_starved" {
		t.Fatalf("drop row = %+v, want cause/drop_cause canary_starved", drop)
	}
	if _, ok := drop["excluded"]; !ok {
		t.Errorf("canary_starved drop detail missing the exclusion counters: %+v", drop)
	}
	if frozen := learningCandidateFreezeSet(rows, 27); len(frozen) != 0 {
		t.Errorf("freeze set = %v, want EMPTY — starved drops write zero freeze entries", frozen)
	}
	if mem := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); mem != base {
		t.Errorf("memory.md changed under a starved drop:\n%s", mem)
	}
	if !hasRunAdvisory(t, rows, "canary_starved") {
		t.Error("no canary_starved advisory journaled")
	}
	if stalls := memoryUpdatesByCause(t, rows, "learning_stall"); len(stalls) != 1 {
		t.Errorf("learning_stall rows after drop = %d, want still 1 (the drop is not a stall)", len(stalls))
	}
}

// TestLearningStallEpochKeyedReAdvise (A1 Amendment 3, Sol fix): the
// stall dedupe is epoch-keyed per stage cycle — within a cycle it fires
// once (promotion-starvation pin), and a re-cycled artifact re-entering
// the same stage at a new epoch re-advises honestly (the pre-A1
// (hash,stage)-only dedupe was blind to re-cycles).
func TestLearningStallEpochKeyedReAdvise(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, _ := w5RigWithLanes(t, root)
	defer rig.stop(t)

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	block := base + "- Recycled rule — cites: main-epoch-1; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", base)
	cand := w5Candidate(t, root, block, []LearningRuleAdd{{Rule: "Recycled rule", Evidence: "main-epoch-1"}}, nil)
	w5Lineage(t, rig, convID, cand.ArtifactHash, 1)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "canary", 1)
	// Zero cohort outcomes: the canary stalls under the floor.

	w5RunTick(t, rig, convID, 14) // age 13 > 12
	rows := allEvents(t, rig, convID)
	stalls := memoryUpdatesByCause(t, rows, "learning_stall")
	if len(stalls) != 1 || stalls[0]["stage_epoch"] != float64(1) {
		t.Fatalf("stall rows = %+v, want one keyed stage_epoch 1 (first cycle)", stalls)
	}
	w5RunTick(t, rig, convID, 15)
	if stalls := memoryUpdatesByCause(t, allEvents(t, rig, convID), "learning_stall"); len(stalls) != 1 {
		t.Fatalf("stall rows after re-tick = %d, want still 1 (cycle dedupe)", len(stalls))
	}

	// Re-cycle the SAME artifact into the slot at a new epoch.
	w5Stage(t, rig, convID, cand.ArtifactHash, "canary", "dropped", 20)
	w5Stage(t, rig, convID, cand.ArtifactHash, "dropped", "shadow", 21)
	w5Stage(t, rig, convID, cand.ArtifactHash, "shadow", "canary", 22)

	w5RunTick(t, rig, convID, 36) // age 14 > 12 in the new cycle
	rows = allEvents(t, rig, convID)
	stalls = memoryUpdatesByCause(t, rows, "learning_stall")
	if len(stalls) != 2 {
		t.Fatalf("stall rows after re-cycle = %d, want 2 (one per stage cycle)", len(stalls))
	}
	seen := map[float64]bool{}
	for _, s := range stalls {
		se, _ := s["stage_epoch"].(float64)
		seen[se] = true
	}
	if !seen[1] || !seen[22] {
		t.Errorf("stall stage_epochs = %v, want cycles 1 and 22 keyed distinctly", seen)
	}
	w5RunTick(t, rig, convID, 37)
	if stalls := memoryUpdatesByCause(t, allEvents(t, rig, convID), "learning_stall"); len(stalls) != 2 {
		t.Errorf("stall rows after second-cycle re-tick = %d, want still 2", len(stalls))
	}
	if info, _ := rig.server.learningStageOf(context.Background(), 1, cand.ArtifactHash); info.To != "canary" {
		t.Errorf("stage = %q, want canary (age 14 < the starve drop floor — advisory only, never an auto-drop)", info.To)
	}
}

// TestLearningFreezeStageInterrupt: an eligible shadow carrying a frozen
// text stalls at the checkpoint with ONE journaled learning_frozen (no
// canary flip; deduped on re-run); past the window (rollback epoch 1 at
// checkpoint 5, age 4 > 3) it promotes — the N+4 boundary at actuation.
func TestLearningFreezeStageInterrupt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	writeProjFile(t, root, "wiki/main-epoch-1.md", "# epoch 1\n")
	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", base)
	dir := t.TempDir()
	w4SeedCoveredReject(t, rig, convID, w4Patch(t, dir, "ck.diff", "src/feature.go"))
	proposals := w4Proposals("Aged shadow candidate")
	seedProposeBatch(t, rig, convID, 1, proposals, nil)
	proposeSeq := 0
	for _, ev := range allEvents(t, rig, convID) {
		if ev.Type == store.EventReviewAction {
			var p struct{ Action string }
			if json.Unmarshal(ev.Payload, &p) == nil && p.Action == "memory_propose" {
				proposeSeq = ev.Seq
			}
		}
	}
	cand := w4ProjCandidate(base, []LearningRuleAdd{{Rule: "Aged shadow candidate", Evidence: "main-epoch-1"}}, proposeSeq)
	row, _, err := AppendLearningCandidate(root, cand)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	w5Lineage(t, rig, convID, row.ArtifactHash, 2)
	w5Stage(t, rig, convID, row.ArtifactHash, "candidate", "shadow", 2)

	// Frozen at epoch 4 (inside the window at checkpoint 5).
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action": "learning_rollback", "epoch": 4, "retracted": []string{"Aged shadow candidate"}})); err != nil {
		t.Fatalf("rollback row: %v", err)
	}

	rig.server.learningShadowCheckpoints(ctx, *boot.Conversation, 5)
	rows := allEvents(t, rig, convID)
	frozenRows := w5EventsByAction(t, rows, "learning_frozen")
	if len(frozenRows) != 1 {
		t.Fatalf("learning_frozen rows = %d, want 1 (the stage interrupt)", len(frozenRows))
	}
	fr := frozenRows[0]
	if fr["artifact_hash"] != row.ArtifactHash || fr["stage"] != "shadow" || fr["epoch"] != float64(5) {
		t.Errorf("frozen row = %+v, want the shadow interrupt at epoch 5", fr)
	}
	// #118 panel pin: the journaled text is DECORATED (text + " (" +
	// reason + ")") and the freeze-set fold must still key the bare
	// rule — feed this exact journaled row back through the fold
	// (attributed: it alone is the fold input) and require the
	// candidate's text frozen at the interrupt epoch.
	if texts, _ := fr["texts"].([]interface{}); len(texts) != 1 ||
		!strings.HasPrefix(texts[0].(string), "Aged shadow candidate (oscillation_guard: ") {
		t.Errorf("frozen row texts = %v, want the decorated production form", texts)
	}
	var frozenEv store.Event
	for _, ev := range rows {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct{ Action string }
		if json.Unmarshal(ev.Payload, &p) == nil && p.Action == "learning_frozen" {
			frozenEv = ev
		}
	}
	if set := learningCandidateFreezeSet([]store.Event{frozenEv}, 5); set[normalizeRule("Aged shadow candidate")] == "" {
		t.Error("fold must read the decorated production row (bare key frozen at the interrupt epoch)")
	}
	if info, _ := rig.server.learningStageOf(ctx, 1, row.ArtifactHash); info.To != "shadow" {
		t.Errorf("stage = %q, want shadow (frozen interrupt stalls actuation)", info.To)
	}

	// Dedup: a second checkpoint at the same stage journals nothing new.
	rig.server.learningShadowCheckpoints(ctx, *boot.Conversation, 6)
	if fr := w5EventsByAction(t, allEvents(t, rig, convID), "learning_frozen"); len(fr) != 1 {
		t.Errorf("learning_frozen rows after re-checkpoint = %d, want still 1 (deduped)", len(fr))
	}
}

// TestLearningFreezeStageInterruptBoundaryFree: rollback at epoch 1,
// checkpoint at 5 — age 4 > 3 ⇒ the freeze is over; the candidate
// actuates shadow→canary (N+4 free at actuation, R2 fixture).
func TestLearningFreezeStageInterruptBoundaryFree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	writeProjFile(t, root, "wiki/main-epoch-1.md", "# epoch 1\n")
	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", base)
	dir := t.TempDir()
	w4SeedCoveredReject(t, rig, convID, w4Patch(t, dir, "ck.diff", "src/feature.go"))
	proposals := w4Proposals("Boundary free candidate")
	seedProposeBatch(t, rig, convID, 1, proposals, nil)
	proposeSeq := 0
	for _, ev := range allEvents(t, rig, convID) {
		if ev.Type == store.EventReviewAction {
			var p struct{ Action string }
			if json.Unmarshal(ev.Payload, &p) == nil && p.Action == "memory_propose" {
				proposeSeq = ev.Seq
			}
		}
	}
	cand := w4ProjCandidate(base, []LearningRuleAdd{{Rule: "Boundary free candidate", Evidence: "main-epoch-1"}}, proposeSeq)
	row, _, err := AppendLearningCandidate(root, cand)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	w5Lineage(t, rig, convID, row.ArtifactHash, 1) // aged to 4 at checkpoint 5
	w5Stage(t, rig, convID, row.ArtifactHash, "candidate", "shadow", 1)
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action": "learning_rollback", "epoch": 1, "retracted": []string{"Boundary free candidate"}})); err != nil {
		t.Fatalf("rollback row: %v", err)
	}

	rig.server.learningShadowCheckpoints(ctx, *boot.Conversation, 5)
	if info, _ := rig.server.learningStageOf(ctx, 1, row.ArtifactHash); info.To != "canary" {
		t.Errorf("stage = %q, want canary (freeze window over at N+4)", info.To)
	}
	if fr := w5EventsByAction(t, allEvents(t, rig, convID), "learning_frozen"); len(fr) != 0 {
		t.Errorf("learning_frozen rows = %d, want 0 (past the window)", len(fr))
	}
}

// TestLearningHeldForHumanFrozenInterrupt: a held_for_human candidate
// carrying frozen text surfaces ONE learning_frozen (its stage), stays
// held (human resolution pending — the stall is the visibility answer).
func TestLearningHeldForHumanFrozenInterrupt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, _ := w5RigWithLanes(t, root)
	defer rig.stop(t)

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	block := base + "- Held rule — cites: main-epoch-1; reaffirmed: 0\n"
	cand := w5Candidate(t, root, block,
		[]LearningRuleAdd{{Rule: "Held rule", Evidence: "main-epoch-1"}},
		[]string{"Base rule"})
	w5Lineage(t, rig, convID, cand.ArtifactHash, 1)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "held_for_human", 1)
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action": "learning_rollback", "epoch": 4, "retracted": []string{"Held rule"}})); err != nil {
		t.Fatalf("rollback row: %v", err)
	}

	w5RunTick(t, rig, convID, 5)
	rows := allEvents(t, rig, convID)
	fr := w5EventsByAction(t, rows, "learning_frozen")
	if len(fr) != 1 || fr[0]["stage"] != "held_for_human" {
		t.Fatalf("learning_frozen rows = %+v, want one held_for_human interrupt", fr)
	}
	if info, _ := rig.server.learningStageOf(context.Background(), 1, cand.ArtifactHash); info.To != "held_for_human" {
		t.Errorf("stage = %q, want held_for_human (stall only)", info.To)
	}
	w5RunTick(t, rig, convID, 6)
	if fr := w5EventsByAction(t, allEvents(t, rig, convID), "learning_frozen"); len(fr) != 1 {
		t.Errorf("learning_frozen rows after re-tick = %d, want still 1", len(fr))
	}
}

// TestLearningStallAdvisory: aging without the stage's next-step
// minimums surfaces ONE journaled learning_stall per (hash, stage) —
// NEVER an auto-promotion, NEVER an auto-drop (promotion-starvation
// pin).
func TestLearningStallAdvisory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, _ := w5RigWithLanes(t, root)
	defer rig.stop(t)

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	block := base + "- Slow rule — cites: main-epoch-1; reaffirmed: 0\n"

	// Shadow aged 14 epochs (> 12), never reaching canary.
	shadow := w5Candidate(t, root, block, []LearningRuleAdd{{Rule: "Slow rule", Evidence: "main-epoch-1"}}, nil)
	w5Lineage(t, rig, convID, shadow.ArtifactHash, 1)
	w5Stage(t, rig, convID, shadow.ArtifactHash, "candidate", "shadow", 1)

	// Canary aged 14 epochs with zero cohort outcomes.
	canery := w5Candidate(t, root, block+"x", []LearningRuleAdd{{Rule: "Slower rule", Evidence: "main-epoch-1"}}, nil)
	w5Lineage(t, rig, convID, canery.ArtifactHash, 1)
	w5RollForward(t, rig, convID, canery.ArtifactHash, "canary", 1)

	w5RunTick(t, rig, convID, 15)
	rows := allEvents(t, rig, convID)

	stalls := memoryUpdatesByCause(t, rows, "learning_stall")
	if len(stalls) != 2 {
		t.Fatalf("learning_stall rows = %d, want 2 (shadow + canary)", len(stalls))
	}
	byStage := map[string]bool{}
	for _, s := range stalls {
		if s["layer"] != "learning" || s["epoch"] != float64(15) {
			t.Errorf("stall row shape = %+v", s)
		}
		byStage[s["stage"].(string)] = true
	}
	if !byStage["shadow"] || !byStage["canary"] {
		t.Errorf("stall stages = %v, want both shadow and canary", byStage)
	}

	// Never auto-promote, never auto-drop.
	ctx := context.Background()
	if info, _ := rig.server.learningStageOf(ctx, 1, shadow.ArtifactHash); info.To != "shadow" {
		t.Errorf("stalled shadow stage = %q, want shadow", info.To)
	}
	if info, _ := rig.server.learningStageOf(ctx, 1, canery.ArtifactHash); info.To != "canary" {
		t.Errorf("stalled canary stage = %q, want canary", info.To)
	}

	// Idempotent: re-tick journals nothing new.
	w5RunTick(t, rig, convID, 16)
	if stalls := memoryUpdatesByCause(t, allEvents(t, rig, convID), "learning_stall"); len(stalls) != 2 {
		t.Errorf("learning_stall rows after re-tick = %d, want still 2 (deduped per hash+stage)", len(stalls))
	}
}

// TestLearningShadowActuationSlotExclusive: two eligible shadows at one
// checkpoint — the first actuates shadow→canary (single slot, R3), the
// second stays queued with slot_free:false; the queued row repeats while
// the slot stays occupied.
func TestLearningShadowActuationSlotExclusive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	writeProjFile(t, root, "wiki/main-epoch-1.md", "# epoch 1\n")
	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", base)
	dir := t.TempDir()
	w4SeedCoveredReject(t, rig, convID, w4Patch(t, dir, "ck.diff", "src/feature.go"))
	proposals := w4Proposals("slot candidates")
	seedProposeBatch(t, rig, convID, 1, proposals, nil)
	proposeSeq := 0
	for _, ev := range allEvents(t, rig, convID) {
		if ev.Type == store.EventReviewAction {
			var p struct{ Action string }
			if json.Unmarshal(ev.Payload, &p) == nil && p.Action == "memory_propose" {
				proposeSeq = ev.Seq
			}
		}
	}
	mk := func(rule string) LearningCandidate {
		cand := w4ProjCandidate(base, []LearningRuleAdd{{Rule: rule, Evidence: "main-epoch-1"}}, proposeSeq)
		row, _, err := AppendLearningCandidate(root, cand)
		if err != nil {
			t.Fatalf("append %s: %v", rule, err)
		}
		w5Lineage(t, rig, convID, row.ArtifactHash, 2)
		w5Stage(t, rig, convID, row.ArtifactHash, "candidate", "shadow", 2)
		return row
	}
	first := mk("Slot candidate one")
	second := mk("Slot candidate two")

	rig.server.learningShadowCheckpoints(ctx, *boot.Conversation, 5)
	rows := allEvents(t, rig, convID)

	// jsonl append order: the FIRST eligible candidate takes the slot.
	if info, _ := rig.server.learningStageOf(ctx, 1, first.ArtifactHash); info.To != "canary" {
		t.Errorf("first stage = %q, want canary (slot free)", info.To)
	}
	if info, _ := rig.server.learningStageOf(ctx, 1, second.ArtifactHash); info.To != "shadow" {
		t.Errorf("second stage = %q, want shadow (slot occupied — cohort purity)", info.To)
	}
	queued := memoryUpdatesByCause(t, rows, "shadow_queued")
	if len(queued) != 1 || queued[0]["artifact_hash"] != second.ArtifactHash || queued[0]["slot_free"] != false {
		t.Errorf("shadow_queued rows = %+v, want one for the slot-blocked candidate, slot_free false", queued)
	}
}

// TestLearningFreezeLintBoundaryE2E: the R2 freeze FOLD feeding the lint
// GATE — rollback at epoch N ⇒ re-propose at N+1 rejected with
// oscillation_guard; N+4 free (task boundary fixture, gate level).
func TestLearningFreezeLintBoundaryE2E(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeProjFile(t, root, "wiki/main-epoch-1.md", "# epoch 1\n")
	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	rb := func(epoch int) store.Event {
		ev, _ := json.Marshal(map[string]interface{}{
			"action": "learning_rollback", "epoch": epoch, "retracted": []string{"Boundary rule"}})
		return store.Event{Type: store.EventReviewAction, Payload: ev}
	}
	add := LearningRuleAdd{Rule: "Boundary rule", Evidence: "main-epoch-1"}
	cand := learningCandidateFromAccepted(base, sha16([]byte(base)), 1, []LearningRuleAdd{add}, LearningCandidateProvenance{})

	// N+1: frozen ⇒ lint rejects with the oscillation_guard reason.
	frozen := learningCandidateFreezeSet([]store.Event{rb(2)}, 3)
	rep := lintLearningCandidate(root, base, cand, frozen)
	if rep.passed() {
		t.Fatal("N+1 lint passed — the rolled-back text must stay frozen")
	}
	found := false
	for _, v := range rep.Violations {
		if strings.Contains(v.Reason, "oscillation_guard") {
			found = true
		}
	}
	if !found {
		t.Errorf("N+1 violations = %+v, want an oscillation_guard reject", rep.Violations)
	}
	// N+4: free ⇒ the freeze set reads empty; lint passes the freeze leg.
	free := learningCandidateFreezeSet([]store.Event{rb(2)}, 6)
	if _, ok := free[normalizeRule("Boundary rule")]; ok {
		t.Fatal("N+4 must be free of the freeze set")
	}
	if rep := lintLearningCandidate(root, base, cand, free); !rep.passed() {
		t.Errorf("N+4 lint failed (%+v) — the freeze must be over", rep.Violations)
	}
}
