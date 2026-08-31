package ipc

// D9-W4 tests: the canary seam — pref resolution, deterministic interleave,
// chain inheritance (stage-flip-proof), receipt substitution under the
// existing .odo/memory.md key, snapshot pinning, and audit isolation.
// Production can never stage a candidate to canary in W4 (promotion is
// W5); these fixtures force-stage one, per the task's seam contract.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// TestLearningCanaryFractionAndM pins R3: default 0.25, ceiling 0.5,
// 0 = disabled, malformed = default.
func TestLearningCanaryFractionAndM(t *testing.T) {
	type tc struct {
		raw   string
		wantF float64
		wantM int
	}
	cases := []tc{
		{"", 0.25, 4},
		{"0.25", 0.25, 4},
		{"0.5", 0.5, 2},
		{"0.3", 0.3, 3},
		{"0", 0, 0},
		{"-0.2", 0, 0},
		{"0.9", 0.5, 2}, // ceiling clamps
		{"bogus", 0.25, 4},
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, c := range cases {
		if c.raw != "" {
			writePrefs(t, home, "learning_canary_fraction: "+c.raw+"\n")
		} else {
			if err := os.Remove(filepath.Join(home, ".odo", "prefs.md")); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		if f := learningCanaryFraction(); f != c.wantF {
			t.Errorf("fraction(%q) = %v, want %v", c.raw, f, c.wantF)
		}
		if m := learningCanaryM(c.wantF); m != c.wantM {
			t.Errorf("M(%v) = %d, want %d", c.wantF, m, c.wantM)
		}
	}
}

// TestLearningChainRoots pins the anchor fold: only non-slash human
// sends count (steers, parked goals, slash payloads, auto_revise
// machine prompts excluded).
func TestLearningChainRoots(t *testing.T) {
	events := []store.Event{
		w4Event(1, store.EventUserMessage, map[string]interface{}{"text": "root one"}),
		w4Event(2, store.EventUserMessage, map[string]interface{}{"text": "nudge", "steer": true}),
		w4Event(3, store.EventUserMessage, map[string]interface{}{"text": "goal", "park": true}),
		w4Event(4, store.EventUserMessage, map[string]interface{}{"text": "/panel status", "context_scope": "full"}),
		w4Event(5, store.EventUserMessage, map[string]interface{}{"text": "repair", "auto_revise": map[string]interface{}{"round": 1}}),
		w4Event(6, store.EventUserMessage, map[string]interface{}{"text": "root two"}),
	}
	roots := learningChainRoots(events)
	if len(roots) != 2 || roots[0] != 1 || roots[1] != 6 {
		t.Errorf("chain roots = %v, want [1 6]", roots)
	}
}

// w4SeedCanaryCandidate materializes one candidate into jsonl and journals
// its canary stage row (force-staged — the W4 promotion path is unlanded).
func w4SeedCanaryCandidate(t *testing.T, rig *testRig, convID int64, content string) LearningCandidate {
	t.Helper()
	cand := w4ProjCandidate(content, []LearningRuleAdd{{Rule: "Canary rule", Evidence: "main-epoch-1"}}, 1)
	row, _, err := AppendLearningCandidate(rig.root, cand)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action": "learning_stage", "artifact_hash": row.ArtifactHash,
		"from": "shadow", "to": "canary", "cause": "shadow_passed",
	})); err != nil {
		t.Fatalf("stage: %v", err)
	}
	return row
}

// w4JournalSend appends a chain-root user_message (what handleSendMessage
// would journal after assembly).
func w4JournalSend(t *testing.T, rig *testRig, convID int64, text string) {
	t.Helper()
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventUserMessage,
		mustJSON(map[string]interface{}{"text": text})); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// TestLearningCanaryInterleave: with f = 0.25 (M = 4), ordinals 4 and 8
// assign; 5–7 ride live. The substitution rides the existing receipt key
// and pins the block bytes exactly once.
func TestLearningCanaryInterleave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "learning_canary_fraction: 0.25\n")
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	content := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n- Canary rule — cites: main-epoch-1; reaffirmed: 0\n"
	cand := w4SeedCanaryCandidate(t, rig, convID, content)

	// Three sends journal live (ordinals 1..3 stay unassigned).
	for i := 1; i <= 3; i++ {
		if block := rig.server.learningCanaryBlock(ctx, convID, allEvents(t, rig, convID), learningCohortRoot); block != "" {
			t.Fatalf("ordinal %d assigned unexpectedly", i)
		}
		w4JournalSend(t, rig, convID, "task")
	}
	// Ordinal 4: assigned, journaled BEFORE the run's user_message.
	ml, err := rig.server.runMemoryLayers(ctx, "main", convID, "task", learningCohortRoot)
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	if ml.project != content {
		t.Errorf("assigned run did not inject the candidate block:\n%s", ml.project)
	}
	if ml.receipt[".odo/memory.md"] != sha16([]byte(content)) {
		t.Errorf("receipt cohort = %q, want sha16(candidate block) under the EXISTING key",
			ml.receipt[".odo/memory.md"])
	}
	rows := allEvents(t, rig, convID)
	cohorts := payloadsByAction(t, rows, "learning_cohort")
	if len(cohorts) != 1 || cohorts[0]["artifact_hash"] != cand.ArtifactHash || cohorts[0]["conv_seq"] != float64(4) || cohorts[0]["run"] != "send" {
		t.Fatalf("learning_cohort rows = %+v, want one ordinal-4 assignment", cohorts)
	}
	snaps := 0
	for _, mu := range rows {
		if mu.Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
		}
		if jsonUnmarshalOK(mu.Payload, &p) && p.Layer == "learning_canary" && p.Cause == "snapshot" {
			snaps++
		}
	}
	if snaps != 1 {
		t.Errorf("learning_canary snapshot pins = %d, want 1", snaps)
	}
	w4JournalSend(t, rig, convID, "task") // complete chain root 4

	// Ordinals 5..7 live; 8 assigned; snapshot NOT re-pinned.
	for i := 5; i <= 8; i++ {
		block := rig.server.learningCanaryBlock(ctx, convID, allEvents(t, rig, convID), learningCohortRoot)
		want := i == 8
		if (block != "") != want {
			t.Errorf("ordinal %d assigned = %v, want %v", i, block != "", want)
		}
		w4JournalSend(t, rig, convID, "task")
	}
	rows = allEvents(t, rig, convID)
	snaps = 0
	for _, mu := range rows {
		if mu.Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
		}
		if jsonUnmarshalOK(mu.Payload, &p) && p.Layer == "learning_canary" && p.Cause == "snapshot" {
			snaps++
		}
	}
	if snaps != 1 {
		t.Errorf("snapshot pins after second assignment = %d, want 1 (idempotent)", snaps)
	}
	if got := len(payloadsByAction(t, rows, "learning_cohort")); got != 2 {
		t.Errorf("learning_cohort rows = %d, want 2 (ordinals 4 and 8)", got)
	}
}

// TestLearningCanaryInheritFlip: continuations inherit the chain's bound
// cohort even after the artifact's stage flips mid-chain.
func TestLearningCanaryInheritFlip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "learning_canary_fraction: 0.25\n")
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	content := "- Canary rule — cites: main-epoch-1; reaffirmed: 0\n"
	cand := w4SeedCanaryCandidate(t, rig, convID, content)
	for i := 1; i <= 3; i++ {
		rig.server.learningCanaryBlock(ctx, convID, allEvents(t, rig, convID), learningCohortRoot)
		w4JournalSend(t, rig, convID, "task")
	}
	ml4, _ := rig.server.runMemoryLayers(ctx, "main", convID, "task", learningCohortRoot)
	if ml4.project != content {
		t.Fatal("setup: ordinal 4 must assign the canary block")
	}
	w4JournalSend(t, rig, convID, "task") // root 4 completes
	// Steer lands; the continuation must inherit WITHOUT re-rolling.
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventUserMessage,
		mustJSON(map[string]interface{}{"text": "steer along", "steer": true})); err != nil {
		t.Fatal(err)
	}
	// Stage flips mid-chain: the candidate is gone from the slot…
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action": "learning_stage", "artifact_hash": cand.ArtifactHash,
		"from": "canary", "to": "dropped", "cause": "dropped_by_human",
	})); err != nil {
		t.Fatal(err)
	}
	ml, err := rig.server.runMemoryLayers(ctx, "main", convID, "steer along", learningCohortInherit)
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	if ml.project != content {
		t.Errorf("continuation after stage flip must keep the chain's bound block (pin: first hash wins):\n%s", ml.project)
	}
	if ml.receipt[".odo/memory.md"] != sha16([]byte(content)) {
		t.Error("continuation receipt must carry the chain's original block hash")
	}
	// …and the NEXT chain root rides live (ordinal 5 ∉ the interleave,
	// and the slot is empty anyway).
	ml5, _ := rig.server.runMemoryLayers(ctx, "main", convID, "fresh task", learningCohortRoot)
	if ml5.project == content {
		t.Error("a new chain after the flip must NOT adopt the dropped cohort")
	}
	if got := len(payloadsByAction(t, allEvents(t, rig, convID), "learning_cohort")); got != 1 {
		t.Errorf("continuations journal no new assignment rows: got %d, want 1", got)
	}
}

// TestLearningCanaryAuditIsolation: canary-cohorted outcomes are excluded
// from live rows AND the baseline, reported as canary_outcomes (the K3
// 100%-canary fixture pin), and gate-source/C0 diffs are never scored.
func TestLearningCanaryAuditIsolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	live := "- Live rule — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", live) // audit's current rules read the file
	liveHash := sha16([]byte(live))
	canary := "- Live rule — cites: main-epoch-1; reaffirmed: 1\n- Canary rule — cites: main-epoch-1; reaffirmed: 0\n"
	canaryHash := sha16([]byte(canary))
	dir := t.TempDir()
	mk := func(text, receiptHash, patchPath string, action string) {
		for _, ev := range []struct {
			typ  string
			body map[string]interface{}
		}{
			{store.EventUserMessage, map[string]interface{}{"text": text, "receipt": map[string]string{".odo/memory.md": receiptHash}}},
			{store.EventAgentDone, map[string]interface{}{}},
		} {
			if _, err := rig.store.AppendEvent(ctx, convID, ev.typ, mustJSON(ev.body)); err != nil {
				t.Fatalf("append: %v", err)
			}
		}
		d, err := rig.store.InsertDiff(ctx, convID, patchPath, "", "", "")
		if err != nil {
			t.Fatalf("diff: %v", err)
		}
		if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(
			map[string]interface{}{"action": action, "diff_id": d.ID})); err != nil {
			t.Fatalf("review: %v", err)
		}
	}
	// Snapshots (an earlier empty live snapshot makes the live rule
	// window-eligible), then one live accept, one canary reject.
	for _, ev := range []struct{ layer, sha, content string }{
		{"memory", sha16(nil), ""},
		{"memory", liveHash, live},
		{"learning_canary", canaryHash, canary},
	} {
		if _, err := rig.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer": ev.layer, "cause": "snapshot", "sha": ev.sha, "content": ev.content,
			"artifact_hash": "cand1",
		})); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
	}
	mk("live run", liveHash, w4Patch(t, dir, "live.diff", "src/feature.go"), "accept")
	mk("canary run", canaryHash, w4Patch(t, dir, "canary.diff", "src/feature.go"), "reject")
	// A gate-source-diff outcome (never scored) and an unreadable-patch
	// one (legacy truth posture: still counted in the live audit).
	mk("gate-source run", liveHash, w4Patch(t, dir, "gate.diff", "internal/ipc/foo.go"), "reject")
	mk("unreadable run", liveHash, filepath.Join(dir, "pruned.diff"), "reject")

	project, err := rig.store.GetProjectByRoot(ctx, root)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	report, err := ComputeRulesAudit(ctx, rig.store, project)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if report.CanaryOutcomes != 1 {
		t.Errorf("canary_outcomes = %d, want 1 (the canary reject is isolated)", report.CanaryOutcomes)
	}
	if report.ScoringExcluded != 1 {
		t.Errorf("scoring_excluded = %d, want 1 (the gate-source reject)", report.ScoringExcluded)
	}
	if report.Resolutions != 2 || report.Accepts != 1 || report.Rejects != 1 {
		t.Errorf("resolutions = %d (%d/%d), want 2 (accept + unreadable-patch reject; canary + gate-source never count)",
			report.Resolutions, report.Accepts, report.Rejects)
	}
	if report.Baseline.Outcomes != 2 {
		t.Errorf("baseline outcomes = %d, want 2 (canary + excluded never grade the baseline)",
			report.Baseline.Outcomes)
	}
	if len(report.Rules) != 1 || report.Rules[0].Rule != "Live rule" || report.Rules[0].Accepts != 1 || report.Rules[0].Rejects != 1 {
		t.Errorf("rows = %+v, want one Live rule row with 1 accept + 1 reject — canary rule must not leak in", report.Rules)
	}
}
