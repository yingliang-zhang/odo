package ipc

// D9-W6 tests (lock stage machine's human legs + task acceptance §1-§4):
// drop (candidate-layer only, memory.md byte-untouched, journaled rows,
// terminal refusal, hash/prefix resolution), held-state apply (receipted
// marker-first apply: marker shape, stage row, memory.md + archive
// writes, receipts, converge path), promote --global (evidence-carrying
// marker, stage row, NEVER-writes-user.md pin, harmful-absence gate,
// scope/stage refusals), the stall closeout fold (status payload +
// advisory-only: the fold journals nothing), and the learning_action
// wiring (unknown verb, empty hash).

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// w6ActionRows collects decoded review_action payloads of one action
// from the main conversation.
func w6ActionRows(t *testing.T, rig *testRig, convID int64, action string) []map[string]interface{} {
	t.Helper()
	return w5EventsByAction(t, allEvents(t, rig, convID), action)
}

// w6StageRowShape asserts the W6 human stage row's shared fields.
func w6StageRowShape(t *testing.T, rows []map[string]interface{}, hash, from, to, cause string) map[string]interface{} {
	t.Helper()
	for _, r := range rows {
		if r["artifact_hash"] == hash && r["from"] == from && r["to"] == to {
			if r["actor"] != "human" || r["cause"] != cause {
				t.Errorf("stage row = %+v, want actor human, cause %q", r, cause)
			}
			return r
		}
	}
	t.Fatalf("no learning_stage row %s→%s for %s in %+v", from, to, trimLearningHash(hash), rows)
	return nil
}

// TestLearningDropHuman: a staged shadow candidate drops through the
// learning_action IPC — marker-first learning_drop + actor:"human" stage
// row, the fold reads dropped, memory.md is byte-untouched (candidate-
// layer only), and a transcript advisory lands. project_active drops too
// (landed:true marker, memory.md still byte-untouched — the rules ride
// `odo rules retract`). Re-drops (terminal), unknown hashes, and
// ambiguous prefixes refuse.
func TestLearningDropHuman(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	mem := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", mem)
	adds := []LearningRuleAdd{{Rule: "Drop target rule", Evidence: "main-epoch-1"}}
	cand := w5Candidate(t, root, mem+"- Drop target rule — cites: main-epoch-1; reaffirmed: 0\n", adds, nil)
	w5Lineage(t, rig, convID, cand.ArtifactHash, 1)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "shadow", 1)
	memBefore := readFileStr(t, filepath.Join(root, ".odo", "memory.md"))

	// Hash-PREFIX addressing (the CLI's <hash|prefix> contract).
	resp := rig.call(t, Request{Cmd: CmdLearningAction, Action: "drop", Hash: cand.ArtifactHash[:16]})
	ar := resp.LearningAction
	if ar == nil {
		t.Fatal("learning_action: nil payload")
	}
	if ar.Action != "drop" || ar.FromStage != "shadow" || ar.ToStage != "dropped" || ar.ArtifactHash != cand.ArtifactHash {
		t.Errorf("result = %+v", ar)
	}
	if ar.MarkerSeq <= 0 || ar.StageSeq <= 0 || ar.StageSeq <= ar.MarkerSeq {
		t.Errorf("seqs = marker %d stage %d, want 0 < marker < stage", ar.MarkerSeq, ar.StageSeq)
	}

	// Marker-first: one learning_drop row with the evidence fields, then
	// the stage row citing its seq.
	drops := w6ActionRows(t, rig, convID, "learning_drop")
	if len(drops) != 1 {
		t.Fatalf("learning_drop rows = %d, want 1", len(drops))
	}
	d := drops[0]
	if d["artifact_hash"] != cand.ArtifactHash || d["from_stage"] != "shadow" || d["actor"] != "human" {
		t.Errorf("drop marker = %+v", d)
	}
	if rules, _ := d["rules"].([]interface{}); len(rules) != 1 || rules[0] != "Drop target rule" {
		t.Errorf("drop marker rules = %v, want the delta.add texts", d["rules"])
	}
	stageRows := w6ActionRows(t, rig, convID, "learning_stage")
	sr := w6StageRowShape(t, stageRows, cand.ArtifactHash, "shadow", "dropped", "dropped_by_human")
	if got, _ := sr["marker_seq"].(float64); got != float64(ar.MarkerSeq) {
		t.Errorf("stage marker_seq = %v, want the drop marker's seq %d", sr["marker_seq"], ar.MarkerSeq)
	}

	// Fold: dropped, and candidate-layer ONLY — memory.md byte-untouched.
	if info, _ := rig.server.learningStageOf(context.Background(), 1, cand.ArtifactHash); info.To != "dropped" {
		t.Errorf("stage = %q, want dropped", info.To)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != memBefore {
		t.Errorf("memory.md after drop = %q, want byte-unchanged %q", got, memBefore)
	}
	if !hasRunAdvisory(t, allEvents(t, rig, convID), "dropped by human") {
		t.Error("no transcript advisory for the human drop")
	}

	// project_active drops (candidate-layer): landed:true, memory.md still
	// byte-untouched — landed rules ride `odo rules retract`.
	landed := mem + "- Landed rule gamma — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", landed)
	cand2 := w5Candidate(t, root, landed, []LearningRuleAdd{{Rule: "Landed rule gamma", Evidence: "main-epoch-1"}}, nil)
	w5RollForward(t, rig, convID, cand2.ArtifactHash, "project_active", 1)
	resp2 := rig.call(t, Request{Cmd: CmdLearningAction, Action: "drop", Hash: cand2.ArtifactHash})
	if resp2.LearningAction.FromStage != "project_active" {
		t.Errorf("project_active drop result = %+v", resp2.LearningAction)
	}
	drops = w6ActionRows(t, rig, convID, "learning_drop")
	var d2 map[string]interface{}
	for _, r := range drops {
		if r["artifact_hash"] == cand2.ArtifactHash {
			d2 = r
		}
	}
	if d2 == nil || d2["landed"] != true {
		t.Errorf("project_active drop marker = %+v, want landed:true honesty", d2)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != landed {
		t.Errorf("memory.md after project_active drop = %q, want byte-unchanged %q (D4)", got, landed)
	}

	// Refusals: terminal re-drop, unknown hash, ambiguous prefix.
	if e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "drop", Hash: cand.ArtifactHash}).Error; !strings.Contains(e, "terminal stage") {
		t.Errorf("re-drop error = %q, want the terminal refusal", e)
	}
	if e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "drop", Hash: "deadbeefdeadbeef"}).Error; !strings.Contains(e, "no learning candidate") {
		t.Errorf("unknown-hash error = %q, want the resolution error", e)
	}
	// Ambiguity: deterministic search over rule texts for two candidates
	// sharing one hex char (≤32 distinct-first-char tries).
	seen := map[byte]bool{}
	collided := ""
	for i := 0; ; i++ {
		c := w5Candidate(t, root, mem, []LearningRuleAdd{{Rule: "ambiguity probe " + strconv.Itoa(i), Evidence: "main-epoch-1"}}, nil)
		if seen[c.ArtifactHash[0]] {
			collided = c.ArtifactHash
			break
		}
		seen[c.ArtifactHash[0]] = true
		if i > 64 {
			t.Fatal("no first-char collision in 65 candidates — sha256 broken?")
		}
	}
	if e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "drop", Hash: collided[:1]}).Error; !strings.Contains(e, "ambiguous") {
		t.Errorf("ambiguous-prefix error = %q, want the ambiguity refusal", e)
	}
}

// TestLearningApplyHeld: a held_for_human candidate with adds + retracts
// applies through the IPC — marker-first memory_apply (actor "human",
// epoch −1 sentinel, recovery block), actor:"human" stage row, archive
// record before memory.md, apply/retract receipts. Non-held stages and
// terminal stages refuse; a candidate whose rules already landed
// converges (present:true, zero writes).
func TestLearningApplyHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	old := base + "- Old bad rule — cites: main-epoch-1; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", old)
	adds := []LearningRuleAdd{{Rule: "New good rule", Evidence: "main-epoch-2"}}
	cand := w5Candidate(t, root, old+"- New good rule — cites: main-epoch-2; reaffirmed: 0\n", adds, []string{"Old bad rule"})
	w5RollForward(t, rig, convID, cand.ArtifactHash, "held_for_human", 1)

	resp := rig.call(t, Request{Cmd: CmdLearningAction, Action: "apply", Hash: cand.ArtifactHash[:12]})
	ar := resp.LearningAction
	if ar == nil {
		t.Fatal("learning_action: nil payload")
	}
	if ar.Action != "apply" || ar.FromStage != "held_for_human" || ar.ToStage != "project_active" || ar.Present {
		t.Errorf("result = %+v", ar)
	}
	if ar.MarkerSeq <= 0 || len(ar.Retracted) != 1 || ar.Retracted[0] != "Old bad rule" {
		t.Errorf("marker/retracted = %+v", ar)
	}

	// Marker: memory_apply with the W5 promote family shape (actor human,
	// sentinel epoch, recovery carrying the post-state).
	applies := w6ActionRows(t, rig, convID, "memory_apply")
	if len(applies) != 1 {
		t.Fatalf("memory_apply markers = %d, want 1", len(applies))
	}
	ap := applies[0]
	if ap["actor"] != "human" || ap["epoch"] != float64(learningPromoteEpochKey) || ap["artifact_hash"] != cand.ArtifactHash {
		t.Errorf("apply marker = %+v, want actor human, epoch −1, artifact cited", ap)
	}
	if rec, ok := ap["recovery"].(map[string]interface{}); !ok || rec["memory"] == nil {
		t.Errorf("apply marker recovery = %v, want the memory layer block", ap["recovery"])
	}
	stages := w6ActionRows(t, rig, convID, "learning_stage")
	w6StageRowShape(t, stages, cand.ArtifactHash, "held_for_human", "project_active", "applied_by_human")

	// The write: old rule out, new rule in; the archive got the record.
	got := readFileStr(t, filepath.Join(root, ".odo", "memory.md"))
	if strings.Contains(got, "Old bad rule") {
		t.Errorf("memory.md still carries the retracted line:\n%s", got)
	}
	if !strings.Contains(got, "- New good rule — cites: main-epoch-2; reaffirmed:") {
		t.Errorf("memory.md missing the promoted add:\n%s", got)
	}
	if !strings.Contains(got, "Base rule") {
		t.Errorf("memory.md lost the untouched base line:\n%s", got)
	}
	archive := readFileStr(t, filepath.Join(root, ".odo", "memory-archive.md"))
	if !strings.Contains(archive, "Old bad rule") {
		t.Errorf("memory-archive.md has no retraction record:\n%s", archive)
	}

	// Receipts: apply + the DISTINCT retract cause (ADR's exhaustive cause
	// family).
	memRows := memoryUpdatesByCause(t, allEvents(t, rig, convID), "apply")
	if len(memRows) != 1 || memRows[0]["layer"] != "memory" || memRows[0]["before_sha"] != ar.BeforeSHA || memRows[0]["after_sha"] != ar.AfterSHA {
		t.Errorf("apply receipts = %+v, want one memory apply with the marker's sha pair", memRows)
	}
	if rct := memoryUpdatesByCause(t, allEvents(t, rig, convID), "retract"); len(rct) != 1 {
		t.Errorf("retract receipts = %+v, want exactly one for the held delta's retraction", rct)
	}
	if info, _ := rig.server.learningStageOf(context.Background(), 1, cand.ArtifactHash); info.To != "project_active" {
		t.Errorf("stage = %q, want project_active", info.To)
	}

	// Re-apply: now project_active — refused (held-only).
	if e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "apply", Hash: cand.ArtifactHash}).Error; !strings.Contains(e, "not held_for_human") {
		t.Errorf("re-apply error = %q, want the held-only refusal", e)
	}
	// Canary refuses too.
	cand2 := w5Candidate(t, root, old, []LearningRuleAdd{{Rule: "Canary rule", Evidence: "main-epoch-1"}}, nil)
	w5RollForward(t, rig, convID, cand2.ArtifactHash, "canary", 1)
	if e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "apply", Hash: cand2.ArtifactHash}).Error; !strings.Contains(e, "not held_for_human") {
		t.Errorf("canary apply error = %q, want the held-only refusal", e)
	}

	// Converge: the rules are ALREADY in memory.md verbatim — the apply
	// journals only the stage flip (present:true, no marker, no write).
	conv := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n- Converged rule — cites: main-epoch-2; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", conv)
	cand3 := w5Candidate(t, root, conv, []LearningRuleAdd{{Rule: "Converged rule", Evidence: "main-epoch-2"}}, nil)
	w5RollForward(t, rig, convID, cand3.ArtifactHash, "held_for_human", 1)
	resp3 := rig.call(t, Request{Cmd: CmdLearningAction, Action: "apply", Hash: cand3.ArtifactHash})
	if !resp3.LearningAction.Present || resp3.LearningAction.MarkerSeq != 0 {
		t.Errorf("converge result = %+v, want present:true, marker_seq 0", resp3.LearningAction)
	}
	if rows := w6ActionRows(t, rig, convID, "memory_apply"); len(rows) != 1 {
		t.Errorf("memory_apply rows after converge = %d, want still 1 (no second marker)", len(rows))
	}
	w6StageRowShape(t, w6ActionRows(t, rig, convID, "learning_stage"), cand3.ArtifactHash, "held_for_human", "project_active", "applied_by_human")
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != conv {
		t.Errorf("memory.md after converge = %q, want byte-unchanged %q", got, conv)
	}
}

// TestLearningPromoteGlobal: the human global promotion — project_active
// candidate flips to global_active with the evidence-carrying
// learning_promote{scope:"global"} marker (harmful absence verified NOW),
// user.md byte-untouched (D4 ruling ④ pin), result carrying the rule
// lines for hand-addition. Non-project_active stages, terminal stages,
// wrong scopes, and a candidate whose adds read harmful NOW refuse
// without journaling.
func TestLearningPromoteGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	userBefore := "- Human global rule\n"
	writeUserMD(t, home, userBefore)

	mem := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", mem)
	adds := []LearningRuleAdd{
		{Rule: "Global worthy rule", Evidence: "main-epoch-1"},
		{Rule: "Second global rule", Evidence: "main-epoch-1"},
	}
	cand := w5Candidate(t, root, mem+"- Global worthy rule — cites: main-epoch-1; reaffirmed: 1\n- Second global rule — cites: main-epoch-1; reaffirmed: 1\n", adds, nil)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "project_active", 1)

	resp := rig.call(t, Request{Cmd: CmdLearningAction, Action: "promote_global", Hash: cand.ArtifactHash[:12]})
	ar := resp.LearningAction
	if ar == nil {
		t.Fatal("learning_action: nil payload")
	}
	if ar.Action != "promote_global" || ar.FromStage != "project_active" || ar.ToStage != "global_active" {
		t.Errorf("result = %+v", ar)
	}
	if len(ar.RuleLines) != 2 || ar.RuleLines[0] != "Global worthy rule" || ar.RuleLines[1] != "Second global rule" {
		t.Errorf("rule lines = %v, want the verbatim delta.add texts", ar.RuleLines)
	}

	// Marker: learning_promote with scope global + the measured evidence
	// tuple (harmful absence journaled as verified).
	proms := w6ActionRows(t, rig, convID, "learning_promote")
	if len(proms) != 1 {
		t.Fatalf("learning_promote markers = %d, want 1", len(proms))
	}
	pm := proms[0]
	if pm["scope"] != "global" || pm["actor"] != "human" || pm["artifact_hash"] != cand.ArtifactHash || pm["harmful_absent"] != true {
		t.Errorf("promote marker = %+v", pm)
	}
	if pm["canary"] == nil || pm["live"] == nil || pm["baseline"] == nil || pm["rules"] == nil {
		t.Errorf("promote marker missing the evidence tuple (cohorts/rules): %+v", pm)
	}
	if lines, _ := pm["rule_lines"].([]interface{}); len(lines) != 2 {
		t.Errorf("marker rule_lines = %v, want both delta.add texts", pm["rule_lines"])
	}
	w6StageRowShape(t, w6ActionRows(t, rig, convID, "learning_stage"), cand.ArtifactHash, "project_active", "global_active", "promoted_global")
	if info, _ := rig.server.learningStageOf(context.Background(), 1, cand.ArtifactHash); info.To != "global_active" {
		t.Errorf("stage = %q, want global_active", info.To)
	}

	// D4 ruling ④ pin: user.md is byte-untouched (promotion staging only —
	// the human adds the lines by hand; the tiers are never bypassed).
	if got := readFileStr(t, filepath.Join(home, ".odo", "user.md")); got != userBefore {
		t.Errorf("user.md after promote --global = %q, want byte-unchanged %q (NEVER written)", got, userBefore)
	}
	if !hasRunAdvisory(t, allEvents(t, rig, convID), "global_active") {
		t.Error("no transcript advisory for the global promotion")
	}

	// Refusals: already global_active (idempotence by refusal, no second
	// marker), non-project_active stage, unknown word order at the gate.
	if e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "promote_global", Hash: cand.ArtifactHash}).Error; !strings.Contains(e, "already global_active") {
		t.Errorf("re-promote error = %q, want the terminal refusal", e)
	}
	canary := w5Candidate(t, root, mem, []LearningRuleAdd{{Rule: "Canary only", Evidence: "main-epoch-1"}}, nil)
	w5RollForward(t, rig, convID, canary.ArtifactHash, "canary", 1)
	if e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "promote_global", Hash: canary.ArtifactHash}).Error; !strings.Contains(e, "only project_active") {
		t.Errorf("canary promote error = %q, want the project_active-only refusal", e)
	}
	if proms := w6ActionRows(t, rig, convID, "learning_promote"); len(proms) != 1 {
		t.Errorf("learning_promote markers after refusals = %d, want still 1", len(proms))
	}
}

// TestLearningPromoteGlobalHarmfulRefused: a candidate whose own adds
// meet the harmful tuple NOW refuses global promotion — the evidence
// tuple is verified at promote time, never inherited from the old
// project_active flip; nothing journals.
func TestLearningPromoteGlobalHarmfulRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, lanes := w5RigWithLanes(t, root, "ui", "ops")
	defer rig.stop(t)
	dir := t.TempDir()
	patch := w4Patch(t, dir, "f.diff", "src/feature.go")

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	mem := base + "- Bad global rule — cites: main-epoch-1; reaffirmed: 0\n"
	writeProjFile(t, root, ".odo/memory.md", mem)
	cand := w5Candidate(t, root, mem, []LearningRuleAdd{{Rule: "Bad global rule", Evidence: "main-epoch-1"}}, nil)
	w5RollForward(t, rig, convID, cand.ArtifactHash, "project_active", 1)
	cohortSHA := w5SeedSnapshot(t, rig, convID, mem)
	cleanSHA := w5SeedSnapshot(t, rig, convID, base)
	w5SeedHarmfulRuleTraffic(t, rig, []int64{convID, lanes["ui"], lanes["ops"]}, cohortSHA, cleanSHA, patch)

	e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "promote_global", Hash: cand.ArtifactHash}).Error
	if !strings.Contains(e, "harmful tuple") {
		t.Errorf("promote error = %q, want the harmful-now refusal", e)
	}
	if proms := w6ActionRows(t, rig, convID, "learning_promote"); len(proms) != 0 {
		t.Errorf("learning_promote markers = %d, want 0 (refusal journals nothing)", len(proms))
	}
	if info, _ := rig.server.learningStageOf(context.Background(), 1, cand.ArtifactHash); info.To != "project_active" {
		t.Errorf("stage = %q, want still project_active (no stage flip on refusal)", info.To)
	}
}

// TestLearningStallStatusCloseout: W5's learning_stall rows surface in
// the single status fold (GUI + `odo learning list --stalled` drink this
// payload), candidate rows carry the stalled marker, and the fold is
// advisory-only — listing NEVER journals (no stage rows, no drops, no
// promotions).
func TestLearningStallStatusCloseout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	mem := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	shadow := w5Candidate(t, root, mem+"- Shadow rule — cites: main-epoch-1; reaffirmed: 0\n",
		[]LearningRuleAdd{{Rule: "Shadow rule", Evidence: "main-epoch-1"}}, nil)
	w5RollForward(t, rig, convID, shadow.ArtifactHash, "shadow", 1)
	canary := w5Candidate(t, root, mem+"- Canary rule — cites: main-epoch-1; reaffirmed: 0\n",
		[]LearningRuleAdd{{Rule: "Canary rule", Evidence: "main-epoch-1"}}, nil)
	w5RollForward(t, rig, convID, canary.ArtifactHash, "canary", 1)
	free := w5Candidate(t, root, mem+"- Free rule — cites: main-epoch-1; reaffirmed: 0\n",
		[]LearningRuleAdd{{Rule: "Free rule", Evidence: "main-epoch-1"}}, nil)
	w5RollForward(t, rig, convID, free.ArtifactHash, "shadow", 1)

	// The W5 emitter's exact rows (one per hash+stage, deduped upstream).
	for i, fixture := range []struct {
		cand   LearningCandidate
		stage  string
		reason string
	}{
		{shadow, "shadow", "shadow aged 13 main epochs without reaching canary"},
		{canary, "canary", "canary aged 13 main epochs with 4 resolved outcome(s), short of the 10 floor"},
	} {
		if _, err := rig.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer": "learning", "cause": "learning_stall",
			"artifact_hash": fixture.cand.ArtifactHash, "stage": fixture.stage,
			"epoch": 14, "reason": fixture.reason})); err != nil {
			t.Fatalf("stall fixture %d: %v", i, err)
		}
	}

	before := len(allEvents(t, rig, convID))
	p, err := rig.store.GetProjectByRoot(ctx, root)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	rep, err := ComputeLearningStatus(ctx, rig.store, p)
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if len(rep.Stalls) != 2 {
		t.Fatalf("stalls = %d, want 2, rows %+v", len(rep.Stalls), rep.Stalls)
	}
	byHash := map[string]LearningStallRow{}
	for _, s := range rep.Stalls {
		byHash[s.ArtifactHash] = s
		if s.Seq <= 0 || s.Epoch != 14 || s.Reason == "" {
			t.Errorf("stall row = %+v, want seq>0, epoch 14, reason carried", s)
		}
	}
	if _, ok := byHash[shadow.ArtifactHash]; !ok {
		t.Errorf("shadow stall missing: %+v", rep.Stalls)
	}
	if got := byHash[canary.ArtifactHash]; got.Stage != "canary" {
		t.Errorf("canary stall stage = %q, want canary", got.Stage)
	}
	stalledOf := map[string]bool{}
	for _, c := range rep.Candidates {
		stalledOf[c.ArtifactHash] = c.Stalled
	}
	if !stalledOf[shadow.ArtifactHash] || !stalledOf[canary.ArtifactHash] || stalledOf[free.ArtifactHash] {
		t.Errorf("candidate stalled markers = %v, want shadow+canary stalled, free clean", stalledOf)
	}

	// Advisory-only pin: the listing journaled NOTHING (never promotes,
	// never drops — stages identical).
	if after := len(allEvents(t, rig, convID)); after != before {
		t.Errorf("events after status fold = %d, want unchanged %d (listing is read-only)", after, before)
	}
	if info, _ := rig.server.learningStageOf(ctx, 1, shadow.ArtifactHash); info.To != "shadow" {
		t.Errorf("stalled shadow stage = %q, want shadow (no auto-action)", info.To)
	}
	if info, _ := rig.server.learningStageOf(ctx, 1, canary.ArtifactHash); info.To != "canary" {
		t.Errorf("stalled canary stage = %q, want canary (no auto-action)", info.To)
	}
}

// TestLearningActionWiring: the learning_action command is registered
// with the W6 verbs; unknown verbs and nameless candidates refuse cleanly.
func TestLearningActionWiring(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})

	if e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "demote", Hash: "whatever"}).Error; !strings.Contains(e, "unknown action") {
		t.Errorf("unknown verb error = %q, want the verb refusal", e)
	}
	if e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "drop"}).Error; !strings.Contains(e, "empty candidate reference") {
		t.Errorf("empty hash error = %q, want the reference refusal", e)
	}
	if e := rig.callExpectErr(t, Request{Cmd: CmdLearningAction, Action: "drop", Hash: "0123abcd"}).Error; !strings.Contains(e, "no learning candidate") {
		t.Errorf("no-jsonl error = %q, want the resolution error", e)
	}
}
