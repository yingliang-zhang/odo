package ipc

// D9-W4 tests: candidate creation from accepted learner batches (divert),
// the diverted apply marker, the stage fold, shadow checkpoints, the
// pref-off legacy parity, and the fold-growth whitelist pin. Rig-level
// fixtures use the store directly; one end-to-end distill test drives
// the real pipeline.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// w4SeedActivity journals ONE completed no-panel run transcript
// (send → diff → done) so the distill has conversation content without
// a live agent run — a live run's rows can be REPLAYED by the liveness
// ladder under machine load, scrambling the terminal→diff attribution
// this file's e2e depends on (diff #119 verify_failed root cause).
func w4SeedActivity(t *testing.T, rig *testRig, convID int64, text, summary, patch string) {
	t.Helper()
	ctx := context.Background()
	content := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	h := sha16([]byte(content))
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventUserMessage, mustJSON(map[string]interface{}{
		"text": text, "receipt": map[string]string{".odo/memory.md": h}})); err != nil {
		t.Fatalf("seed send: %v", err)
	}
	if _, err := rig.store.InsertDiff(ctx, convID, patch, "", "", ""); err != nil {
		t.Fatalf("seed diff: %v", err)
	}
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventAgentDone, mustJSON(map[string]interface{}{
		"summary": summary})); err != nil {
		t.Fatalf("seed done: %v", err)
	}
}

// w4SeedCoveredReject journals snapshot→send→done→(diff)→reject so the
// replay has one cohort-covered negative outcome, then returns the
// cohort hash and diff id.
func w4SeedCoveredReject(t *testing.T, rig *testRig, convID int64, patch string) string {
	t.Helper()
	ctx := context.Background()
	content := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	h := sha16([]byte(content))
	for _, ev := range []struct {
		typ  string
		body map[string]interface{}
	}{
		{store.EventMemoryUpdate, map[string]interface{}{
			"layer": "memory", "cause": "snapshot", "source": ".odo/memory.md", "sha": h, "content": content}},
		{store.EventUserMessage, map[string]interface{}{
			"text": "task one", "receipt": map[string]string{".odo/memory.md": h}}},
		{store.EventAgentDone, map[string]interface{}{}},
	} {
		if _, err := rig.store.AppendEvent(ctx, convID, ev.typ, mustJSON(ev.body)); err != nil {
			t.Fatalf("seed %s: %v", ev.typ, err)
		}
	}
	d, err := rig.store.InsertDiff(ctx, convID, patch, "", "", "")
	if err != nil {
		t.Fatalf("seed diff: %v", err)
	}
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action": "reject", "diff_id": d.ID,
	})); err != nil {
		t.Fatalf("seed reject: %v", err)
	}
	return h
}

// w4Proposals returns one accepted memory.md add proposal with riding
// unanimous reviews.
func w4Proposals(rule string) []MemoryProposal {
	return []MemoryProposal{{
		Target: "memory.md", Rule: rule, Evidence: "main-epoch-1",
		Reviews: []ReviewResult{{Model: "rm1", Verdict: "accept"}, {Model: "rm2", Verdict: "accept"}, {Model: "rm3", Verdict: "accept"}},
	}}
}

// --- divert + gates + consume ---------------------------------------------

func TestDivertAcceptedAddsToCandidate(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // default learning_stages: on
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
	w4SeedCoveredReject(t, rig, convID, w4Patch(t, dir, "one.diff", "src/feature.go"))
	seedProposeBatch(t, rig, convID, 1, w4Proposals("Run the full suite after landing"), nil)

	events := allEvents(t, rig, convID)
	batch := findPendingBatch(events)
	if !batch.exists || batch.consumed {
		t.Fatalf("seeded batch = exists %v consumed %v", batch.exists, batch.consumed)
	}
	diverted := rig.server.divertAcceptedAddsToCandidate(ctx, *boot.Conversation, batch, []bool{true}, events)
	if len(diverted) != 1 || diverted[0] != 0 {
		t.Fatalf("diverted = %v, want [0]", diverted)
	}

	// The artifact exists with the full projection and base binding.
	cands, err := ReadLearningCandidates(root)
	if err != nil || len(cands) != 1 {
		t.Fatalf("candidates = %d err %v, want 1", len(cands), err)
	}
	cand := cands[0]
	if !strings.Contains(cand.Content, "Base rule") || !strings.Contains(cand.Content, "Run the full suite after landing") {
		t.Errorf("candidate content must be the FULL projected block:\n%s", cand.Content)
	}
	if cand.BaseSHA16 != sha16([]byte(base)) {
		t.Errorf("base_sha16 = %q, want the seeded memory.md sha", cand.BaseSHA16)
	}
	if cand.Delta.Retract == nil || len(cand.Delta.Retract) != 0 || len(cand.Delta.Add) != 1 {
		t.Errorf("delta = %+v, want adds-only (retract stays human-only)", cand.Delta)
	}
	if cand.CreatedSeq == 0 {
		t.Error("created_seq must be the learning_candidate journal row's seq")
	}

	rows := allEvents(t, rig, convID)
	if lc := payloadsByAction(t, rows, "learning_candidate"); len(lc) != 1 || lc[0]["artifact_hash"] != cand.ArtifactHash {
		t.Fatalf("learning_candidate rows = %+v, want one for %s", lc, cand.ArtifactHash)
	}
	// All three gates ran and passed (1 covered reject ⇒ prevented 1,
	// friction 0; propose row inside the slice ⇒ provenance clean).
	gates := map[string]string{}
	for _, g := range payloadsByAction(t, rows, "learning_gate") {
		gates[g["gate"].(string)] = g["verdict"].(string)
	}
	for _, want := range []string{"lint", "security", "replay"} {
		if gates[want] != "pass" {
			t.Errorf("gate %s = %q, want pass", want, gates[want])
		}
	}
	stages := payloadsByAction(t, rows, "learning_stage")
	if len(stages) != 1 || stages[0]["from"] != "candidate" || stages[0]["to"] != "shadow" || stages[0]["cause"] != "gates_passed" {
		t.Fatalf("learning_stage rows = %+v, want candidate→shadow gates_passed", stages)
	}
	if fr := payloadsByAction(t, rows, "learning_freeze"); len(fr) != 1 || fr[0]["input_sha256"] == "" {
		t.Errorf("learning_freeze rows = %+v, want one with input_sha256", fr)
	}
	// memory.md untouched — the divert owns the accepted adds now.
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != base {
		t.Errorf("memory.md changed (%q), want untouched base", got)
	}

	// Idempotence: the identical batch re-diverted against the ORIGINAL
	// event snapshot (the crash-retry posture — same base grounding) is an
	// inert no-op: the artifact hash matches, nothing new journals.
	diverted = rig.server.divertAcceptedAddsToCandidate(ctx, *boot.Conversation, batch, []bool{true}, events)
	if len(diverted) != 1 {
		t.Fatalf("re-divert = %v, want [0] (batch still diverts)", diverted)
	}
	rows = allEvents(t, rig, convID)
	if lc := payloadsByAction(t, rows, "learning_candidate"); len(lc) != 1 {
		t.Errorf("learning_candidate rows after retry = %d, want 1 (idempotent)", len(lc))
	}
	if g := payloadsByAction(t, rows, "learning_gate"); len(g) != 3 {
		t.Errorf("learning_gate rows after retry = %d, want 3 (no re-run)", len(g))
	}
	if c2, _ := ReadLearningCandidates(root); len(c2) != 1 {
		t.Errorf("candidates.jsonl rows after retry = %d, want 1", len(c2))
	}

	// A re-divert against a GROWN journal ground pins the address rule:
	// base_source_seq differs ⇒ a new artifact (duplicate rule text on a
	// later base is a new artifact, never a silent merge — the runner-up
	// hash is what dedupe keys on).
	diverted = rig.server.divertAcceptedAddsToCandidate(ctx, *boot.Conversation, batch, []bool{true}, allEvents(t, rig, convID))
	if len(diverted) != 1 {
		t.Fatalf("grown-ground divert = %v, want [0]", diverted)
	}
	if c2, _ := ReadLearningCandidates(root); len(c2) != 2 {
		t.Errorf("candidates.jsonl after a moved base_source_seq = %d rows, want 2 (base grounding is part of the address)", len(c2))
	}
}

// The diverted batch still consumes through the apply core: the marker
// carries the diverted refs instead of accepted/rejected, and writes
// nothing for them.
func TestApplyResolvedBatchWithDiversion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	props := w4Proposals("Rule staged away")
	seedProposeBatch(t, rig, convID, 1, props, nil)
	batch := findPendingBatch(allEvents(t, rig, convID))
	if resp, err := rig.server.applyResolvedBatch(ctx, *boot.Conversation, batch, []bool{true}, autoActor, []int{0}); err != nil || !resp.Applied {
		t.Fatalf("apply with diversion = %+v %v", resp, err)
	}
	markers := payloadsByAction(t, allEvents(t, rig, convID), "memory_apply")
	if len(markers) != 1 {
		t.Fatalf("memory_apply markers = %d, want 1", len(markers))
	}
	m := markers[0]
	metrics, _ := m["metrics"].(map[string]interface{})
	if metrics["accepted"] != float64(0) || metrics["rejected"] != float64(0) || metrics["diverted"] != float64(1) {
		t.Errorf("marker metrics = %v, want 0/0 diverted 1", metrics)
	}
	dv, _ := m["diverted"].([]interface{})
	if len(dv) != 1 {
		t.Fatalf("diverted refs = %v, want 1", dv)
	}
	if accepted, _ := m["accepted"].([]interface{}); len(accepted) != 0 {
		t.Errorf("accepted refs = %v, want none (not faked as accepted)", accepted)
	}
	if _, err := os.Stat(filepath.Join(root, ".odo", "memory.md")); !os.IsNotExist(err) {
		t.Error("memory.md must not exist — the only add was diverted")
	}
	// The batch is consumed (a second apply refuses).
	if _, err := rig.server.applyResolvedBatch(ctx, *boot.Conversation, batch, []bool{true}, autoActor, nil); err == nil ||
		!strings.Contains(err.Error(), "already applied") {
		t.Errorf("second apply = %v, want already-applied refusal", err)
	}

	// Legacy nil-diversion marker keeps the byte-identical key shape
	// (no "diverted" key at all).
	seedProposeBatch(t, rig, convID, 2, w4Proposals("Second rule"), nil)
	batch2 := findPendingBatch(allEvents(t, rig, convID))
	if _, err := rig.server.applyResolvedBatch(ctx, *boot.Conversation, batch2, []bool{true}, autoActor, nil); err != nil {
		t.Fatalf("legacy apply: %v", err)
	}
	markers = payloadsByAction(t, allEvents(t, rig, convID), "memory_apply")
	if len(markers) != 2 {
		t.Fatalf("memory_apply markers = %d, want 2", len(markers))
	}
	legacy := markers[1]
	if _, hasDiverted := legacy["diverted"]; hasDiverted {
		t.Error("nil diversion must carry no diverted key (byte-identical legacy marker)")
	}
	keys := map[string]bool{}
	for k := range legacy {
		keys[k] = true
	}
	for _, want := range []string{"action", "epoch", "accepted", "rejected", "metrics", "recovery", "actor"} {
		if !keys[want] {
			t.Errorf("legacy marker missing key %q: %v", want, legacy)
		}
		delete(keys, want)
	}
	if len(keys) != 0 {
		t.Errorf("legacy marker gained unexpected keys: %v", keys)
	}
}

// --- stage fold ------------------------------------------------------------

func TestLearningStageFoldCrossLane(t *testing.T) {
	// Rows on two lanes with interleaved GLOBAL ids: the fold orders by
	// id (per-lane seqs are incomparable — the W3 status fold's flaw this
	// fixes).
	rev := func(id int64, seq int, to string) store.Event {
		e := w4Review(seq, "learning_stage", map[string]interface{}{
			"artifact_hash": "h1", "from": "candidate", "to": to, "cause": "c",
		})
		e.ID = id
		return e
	}
	laneA := []store.Event{rev(10, 2, "shadow"), rev(30, 9, "dropped")}
	laneB := []store.Event{rev(20, 5, "canary")} // id 20 < id 30 ⇒ dropped wins
	table := foldLearningStages(laneA, laneB)
	if table["h1"].To != "dropped" {
		t.Errorf("stage = %q, want dropped (global id ordering wins)", table["h1"].To)
	}
	if _, ok := table["ghost"]; ok {
		t.Error("absent hash must stay absent")
	}
}

// --- legacy pref-off parity -------------------------------------------------

func TestDivertPrefOffParity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "learning_stages: off\n")
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	props := w4Proposals("Rule lands directly")
	seedProposeBatch(t, rig, convID, 1, props, nil)
	events := allEvents(t, rig, convID)
	batch := findPendingBatch(events)
	if d := rig.server.divertAcceptedAddsToCandidate(ctx, *boot.Conversation, batch, []bool{true}, events); len(d) != 0 {
		t.Fatalf("pref off: diverted = %v, want nil (legacy)", d)
	}
	if _, err := rig.server.applyResolvedBatch(ctx, *boot.Conversation, batch, []bool{true}, autoActor,
		rig.server.divertAcceptedAddsToCandidate(ctx, *boot.Conversation, batch, []bool{true}, allEvents(t, rig, convID))); err != nil {
		t.Fatalf("legacy apply: %v", err)
	}
	mem := readFileStr(t, filepath.Join(root, ".odo", "memory.md"))
	if !strings.Contains(mem, "Rule lands directly") {
		t.Errorf("pref off must keep the legacy direct write:\n%s", mem)
	}
	markers := payloadsByAction(t, allEvents(t, rig, convID), "memory_apply")
	if _, hasDiverted := markers[0]["diverted"]; hasDiverted {
		t.Error("legacy marker must carry no diverted key (byte-identical shape)")
	}
	if c := payloadsByAction(t, allEvents(t, rig, convID), "learning_candidate"); len(c) != 0 {
		t.Errorf("pref off journals %d learning_candidate rows, want 0", len(c))
	}
}

// --- fold-growth whitelist pin ----------------------------------------------

// --- shadow checkpoints (main-lane distill tail) -----------------------------

func TestLearningShadowCheckpoints(t *testing.T) {
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

	mk := func(rule string) LearningCandidate {
		cand := w4ProjCandidate(base, []LearningRuleAdd{{Rule: rule, Evidence: "main-epoch-1"}}, proposeSeq)
		row, created, err := AppendLearningCandidate(root, cand)
		if err != nil || !created {
			t.Fatalf("seed jsonl: %v %v", created, err)
		}
		return row
	}
	pass := mk("Aged shadow candidate")
	fail := mk(strings.Repeat("z", 600)) // replay-d: growth past the budget

	// Stage rows + lineage: both shadow, pass ages 3 epochs (5−2).
	for _, cand := range []LearningCandidate{pass, fail} {
		if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action": "learning_candidate", "artifact_hash": cand.ArtifactHash, "main_epoch": 2,
		})); err != nil {
			t.Fatalf("seed lineage: %v", err)
		}
		if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action": "learning_stage", "artifact_hash": cand.ArtifactHash,
			"from": "candidate", "to": "shadow", "cause": "gates_passed",
		})); err != nil {
			t.Fatalf("seed stage: %v", err)
		}
	}

	rig.server.learningShadowCheckpoints(ctx, *boot.Conversation, 5)
	rows := allEvents(t, rig, convID)

	// Passing candidate: checkpoint (pass) + W5 actuation shadow→canary
	// (aged ≥3, slot free) with the checkpoint evidence riding the
	// transition. No shadow_queued row — the slot was free.
	checkpoints := memoryUpdatesByCause(t, rows, "shadow_checkpoint")
	if len(checkpoints) != 2 {
		t.Fatalf("shadow_checkpoint rows = %d, want 2 (one per shadow candidate)", len(checkpoints))
	}
	byHash := map[string]map[string]interface{}{}
	for _, cp := range checkpoints {
		byHash[cp["artifact_hash"].(string)] = cp
	}
	if m, _ := byHash[pass.ArtifactHash]["metrics"].(map[string]interface{}); m["verdict"] != "pass" {
		t.Errorf("pass candidate checkpoint verdict = %v", m["verdict"])
	}
	stageRows := payloadsByAction(t, rows, "learning_stage")
	var promote map[string]interface{}
	for _, s := range stageRows {
		if s["cause"] == "checkpoint_promoted" {
			promote = s
		}
	}
	if promote == nil || promote["artifact_hash"] != pass.ArtifactHash ||
		promote["from"] != "shadow" || promote["to"] != "canary" || promote["epoch"] != float64(5) {
		t.Fatalf("checkpoint_promoted row = %+v, want shadow→canary epoch 5 for the aged candidate", promote)
	}
	if seqs, _ := promote["evidence_seqs"].([]interface{}); len(seqs) != 2 {
		t.Errorf("promote evidence_seqs = %v, want [freeze, checkpoint]", seqs)
	}
	if queued := memoryUpdatesByCause(t, rows, "shadow_queued"); len(queued) != 0 {
		t.Errorf("shadow_queued rows = %+v, want none (the slot was free — W5 actuates)", queued)
	}
	// The replay-failing candidate demotes to dropped with evidence.
	var drop map[string]interface{}
	for _, s := range stageRows {
		if s["cause"] == "shadow_failed" {
			drop = s
		}
	}
	if drop == nil || drop["artifact_hash"] != fail.ArtifactHash || drop["to"] != "dropped" {
		t.Fatalf("shadow_failed drop row = %+v, want one for the growth-budget candidate", drop)
	}
	if seqs, _ := drop["evidence_seqs"].([]interface{}); len(seqs) != 2 {
		t.Errorf("drop evidence_seqs = %v, want [freeze, checkpoint]", seqs)
	}
	if info, ok := rig.server.learningStageOf(ctx, 1, pass.ArtifactHash); !ok || info.To != "canary" {
		t.Errorf("pass stage = %q ok %v, want canary (W5 actuation)", info.To, ok)
	}
	if fr := payloadsByAction(t, rows, "learning_freeze"); len(fr) < 2 {
		t.Errorf("learning_freeze rows = %d, want ≥ 2 (one per checkpointed candidate)", len(fr))
	}
}

// --- end-to-end: distill with learning stages ON -----------------------------

func TestDistillStagesCandidateEndToEnd(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\nDecided to always run the suite after landing.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", testLearnerOneRule)
	writePrefs(t, home, "review: rm1@test, rm2@test, rm3@test\n")
	startPanelStub(t, acceptAll)
	rig := startRig(t, root)
	defer rig.stop(t)
	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", base)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	// Hermetic transcript, NOT a live run: a real "Create hello.txt" run
	// can wedge under machine load (full-suite oversubscription) and the
	// liveness ladder replays it — the replayed send + second terminal +
	// extra diff row scramble the terminal→diff attribution for the
	// seeded reject (same-second claims are FIFO by seq), the replay
	// then counts ZERO covered outcomes (check f: vacuous ⇒ fail) and
	// the creation gates legitimately drop the fresh candidate
	// (diff #119 verify_failed: stage "dropped", want shadow).
	dir := t.TempDir()
	w4SeedActivity(t, rig, convID, "Create hello.txt", "Created hello.txt as requested.", w4Patch(t, dir, "hello.diff", "hello.txt"))
	// Cohort-covered negative evidence so the candidate's replay is
	// non-vacuous (f ≥ 1) before the fold.
	w4SeedCoveredReject(t, rig, convID, w4Patch(t, dir, "e2e.diff", "src/feature.go"))

	_, d := runToDistill(t, rig, root)
	if d.MemoryProposals != 1 {
		t.Fatalf("distill MemoryProposals = %d, want 1", d.MemoryProposals)
	}

	// The accepted rule staged as a candidate; memory.md unchanged.
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != base {
		t.Errorf("memory.md changed (%q) — accepted adds must stage, not write", got)
	}
	cands, err := ReadLearningCandidates(root)
	if err != nil || len(cands) != 1 {
		t.Fatalf("candidates = %d err %v, want 1", len(cands), err)
	}
	if !strings.Contains(cands[0].Content, "Run the full ipc suite after every landing.") {
		t.Errorf("candidate content missing the accepted rule:\n%s", cands[0].Content)
	}

	events := allEvents(t, rig, convID)
	// The batch still consumed, with the add diverted rather than applied.
	applies := payloadsByAction(t, events, "memory_apply")
	if len(applies) != 1 {
		t.Fatalf("memory_apply markers = %d, want 1", len(applies))
	}
	metrics, _ := applies[0]["metrics"].(map[string]interface{})
	if metrics["diverted"] != float64(1) || metrics["accepted"] != float64(0) {
		t.Errorf("apply metrics = %v, want diverted 1 accepted 0", metrics)
	}
	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if !pend.Consumed {
		t.Error("batch must consume even though the add staged")
	}
	// Gates passed; the stage is shadow; the SAME distill's checkpoint ran
	// (main-lane tail seam) and found nothing aged yet.
	stage := ""
	for _, s := range payloadsByAction(t, events, "learning_stage") {
		stage, _ = s["to"].(string)
	}
	if stage != "shadow" {
		t.Errorf("stage = %q, want shadow", stage)
	}
	if cp := memoryUpdatesByCause(t, events, "shadow_checkpoint"); len(cp) < 1 {
		t.Error("main distill must run the shadow checkpoint for the fresh candidate")
	}
	if lc := payloadsByAction(t, events, "learning_candidate"); len(lc) != 1 {
		t.Errorf("learning_candidate rows = %d, want 1", len(lc))
	}
	// The learner lifecycle itself is intact: propose row present with the
	// riding reviews (panel gate pre-diversion).
	proposes := payloadsByAction(t, events, "memory_propose")
	if len(proposes) != 1 {
		t.Errorf("memory_propose rows = %d, want 1", len(proposes))
	}
}

// Every W4 learning-plane row class reads as fold-authored bookkeeping —
// never unowned journal growth (the W3 episode pin extended).
func TestLearningWhitelistPin(t *testing.T) {
	rows := []store.Event{
		w4Review(1, "learning_episode", nil),
		w4Review(2, "learning_candidate", nil),
		w4Review(3, "learning_gate", nil),
		w4Review(4, "learning_stage", nil),
		w4Review(5, "learning_freeze", nil),
		w4Review(6, "learning_cohort", nil),
		w4Event(7, store.EventMemoryUpdate, map[string]interface{}{"layer": "learning", "cause": "shadow_checkpoint"}),
		w4Event(8, store.EventMemoryUpdate, map[string]interface{}{"layer": "learning", "cause": "shadow_queued"}),
		w4Event(9, store.EventMemoryUpdate, map[string]interface{}{"layer": "learning_canary", "cause": "snapshot"}),
	}
	if unownedFoldGrowth(rows, 0) {
		t.Error("learning-plane rows must not trip the supersession probe")
	}
	mixed := append(append([]store.Event{}, rows...),
		w4Event(10, store.EventUserMessage, map[string]interface{}{"text": "incoming"}))
	if !unownedFoldGrowth(mixed, 0) {
		t.Error("a user send mid-fold is still unattributed growth")
	}
}
