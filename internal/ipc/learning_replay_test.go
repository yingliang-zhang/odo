package ipc

// D9-W4 tests: the frozen replay's per-criterion fixtures (a–h +
// provenance + unverifiable + determinism + friction boundary), built as
// synthetic journal slices (computeLearningReplay is pure given its
// gathered input — never a store).
//
// Structure note (pinned by TestLearningReplayCheckAUniform): for an
// adds-only candidate every covered cohort counterfactually carries the
// add, so the add's row IS the baseline pool and the rate-ratio leg of
// the harmful tuple can never fire — check-a is structurally vacuous in
// W4. It stays journaled as the invariant guard for future scopes; the
// hostile-slice fixture pins the structural property explicitly rather
// than faking an impossible failure.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// w4Send builds a non-slash chain-root user_message with an optional
// memory receipt entry.
func w4Send(seq int, memHash string) store.Event {
	p := map[string]interface{}{"text": fmt.Sprintf("task at %d", seq)}
	if memHash != "" {
		p["receipt"] = map[string]string{rulesAuditMemoryReceipt: memHash}
	}
	return w4Event(seq, store.EventUserMessage, p)
}

// w4Snapshot builds one snapshot pin row for a rule layer.
func w4Snapshot(seq int, layer, sha, content string) store.Event {
	return w4Event(seq, store.EventMemoryUpdate, map[string]interface{}{
		"layer": layer, "cause": "snapshot", "source": ".odo/memory.md", "sha": sha, "content": content,
	})
}

// w4Done builds a clean run terminal.
func w4Done(seq int) store.Event {
	return w4Event(seq, store.EventAgentDone, map[string]interface{}{})
}

// w4Patch writes a minimal one-line-change patch per file and returns the
// patch path (learningScoringClassify parses real patch bytes).
func w4Patch(t *testing.T, dir, name string, files ...string) string {
	t.Helper()
	var sb strings.Builder
	for _, f := range files {
		fmt.Fprintf(&sb, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1,2 @@\n line\n+added line\n", f, f, f, f)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// w4LaneLayout is the seq plan of one covered outcome inside a lane:
// [snapshot] send, terminal, review, all below the lane's marker.
type w4OutcomeSpec struct {
	hash string // cohort snapshot sha ("" = memory-free)
	kind string // "accept" | "reject"
	weak bool   // moa weak reject instead of a human action
	auto bool   // actor auto_panel
}

// w4BuildLane assembles one lane: optional snapshot row, outcomes in
// order (their own patch file each), then a trailing distill marker
// (head). Returns the lane.
func w4BuildLane(t *testing.T, convID int64, snapshotContent map[string]string, specs []w4OutcomeSpec, extra []store.Event) learningReplayLane {
	t.Helper()
	dir := t.TempDir()
	var events []store.Event
	var diffs []store.Diff
	hashesSeen := map[string]bool{}
	seq := 1
	for _, spec := range specs {
		if content, ok := snapshotContent[spec.hash]; ok && !hashesSeen[spec.hash] {
			events = append(events, w4Snapshot(seq, "memory", spec.hash, content))
			hashesSeen[spec.hash] = true
			seq++
		}
		events = append(events, w4Send(seq, spec.hash), w4Done(seq+1))
		diffPath := w4Patch(t, dir, fmt.Sprintf("d%d-%d.diff", convID, seq), "src/feature.go")
		diffID := convID*1000 + int64(seq)
		diffs = append(diffs, store.Diff{ID: diffID, ConversationID: convID, PathOnDisk: diffPath})
		actor := ""
		if spec.auto {
			actor = autoActor
		}
		kind := spec.kind
		if actor != "" {
			kind = strings.TrimPrefix(kind, "auto_")
		}
		if spec.weak {
			events = append(events, w4Review(seq+2, "moa_review", map[string]interface{}{
				"diff_id": diffID, "consensus_verdict": "reject",
			}))
		} else {
			p := map[string]interface{}{"diff_id": diffID}
			if actor != "" {
				p["actor"] = actor
			}
			events = append(events, w4Review(seq+2, kind, p))
		}
		seq += 3
	}
	events = append(events, extra...)
	head := seq
	for _, ev := range events {
		if ev.Seq >= head {
			head = ev.Seq + 1 // extras may sit above the outcome plan
		}
	}
	events = append(events, w4Marker(head, 1))
	return learningReplayLane{convID: convID, events: events, diffs: diffs}
}

// w4Base is the small live block every pass fixture shares.
const w4Base = "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"

// w4PassInput: 1 covered reject (lane 1, with the provenance propose row),
// 2 covered accepts (lane 2), an empty lane 3 (no markers), and one
// memory-free accept (lane 4 marker but unreceipted send).
func w4PassInput(t *testing.T, proposeSeq int) learningReplayInput {
	t.Helper()
	baseHash := sha16([]byte(w4Base))
	lane1 := w4BuildLane(t, 1, map[string]string{baseHash: w4Base},
		[]w4OutcomeSpec{{hash: baseHash, kind: "reject"}},
		[]store.Event{w4Review(proposeSeq, "memory_propose", map[string]interface{}{
			"epoch":     1,
			"proposals": []MemoryProposal{{Target: "memory.md", Rule: "Candidate rule", Evidence: "main-epoch-1"}},
		})})
	lane2 := w4BuildLane(t, 2, map[string]string{baseHash: w4Base},
		[]w4OutcomeSpec{{hash: baseHash, kind: "accept"}, {hash: baseHash, kind: "accept"}}, nil)
	empty := learningReplayLane{convID: 3, events: []store.Event{w4Send(1, "")}}
	lane4 := w4BuildLane(t, 4, map[string]string{baseHash: w4Base},
		[]w4OutcomeSpec{{hash: "", kind: "accept"}}, nil)
	return learningReplayInput{lanes: []learningReplayLane{lane1, lane2, empty, lane4}}
}

// w4PassCandidate: adds one novel rule on top of w4Base, provenance
// pointing at the propose row in lane 1.
func w4PassCandidate(proposeSeq int) LearningCandidate {
	return w4ProjCandidate(w4Base, []LearningRuleAdd{{Rule: "Candidate rule", Evidence: "main-epoch-1"}}, proposeSeq)
}

func TestLearningReplayPassAllChecks(t *testing.T) {
	const proposeSeq = 10
	in := w4PassInput(t, proposeSeq)
	rep := computeLearningReplay(in, w4PassCandidate(proposeSeq))
	if rep.Verdict != "pass" {
		t.Fatalf("verdict = %q, want pass; violations %+v checks %v", rep.Verdict, rep.Violations, rep.Checks)
	}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "provenance"} {
		if !rep.Checks[name] {
			t.Errorf("check %s = false in a passing replay", name)
		}
	}
	if rep.PreventedHarm != 1 || rep.Friction != 2 {
		t.Errorf("prevented/friction = %d/%d, want 1/2", rep.PreventedHarm, rep.Friction)
	}
	if rep.Outcomes != 4 || rep.MemoryFree != 1 {
		t.Errorf("outcomes = %d memfree %d, want 4/1 (lane 3 marker-free contributes nothing)", rep.Outcomes, rep.MemoryFree)
	}
	if len(rep.Freeze.Bounds) != 3 { // lanes 1,2,4 have markers
		t.Errorf("bounds lanes = %d, want 3 (marker-less lane excluded)", len(rep.Freeze.Bounds))
	}
	if rep.Freeze.InputSHA256 == "" {
		t.Error("input_sha256 empty — the manifest must pin the consulted inputs")
	}
	// Determinism beyond the built-in e pin: two full runs marshal
	// byte-identical, including the manifest hash.
	b1, _ := json.Marshal(rep)
	b2, _ := json.Marshal(computeLearningReplay(in, w4PassCandidate(proposeSeq)))
	if string(b1) != string(b2) {
		t.Error("two computeLearningReplay runs diverged (Sol pin)")
	}
}

// Check-a pin: even a maximally hostile slice cannot fail the tuple check
// for an adds-only candidate — every counterfactual cohort carries the
// add, so its row equals the baseline pool (rate-ratio leg inert). The
// check remains as the invariant guard; this fixture proves the join
// RUNS (rows computed) and the structural property holds.
func TestLearningReplayCheckAUniform(t *testing.T) {
	dirHash := sha16([]byte(w4Base))
	mk := func(conv int64, kind string, n int) learningReplayLane {
		specs := make([]w4OutcomeSpec, n)
		for i := range specs {
			specs[i] = w4OutcomeSpec{hash: dirHash, kind: kind}
		}
		return w4BuildLane(t, conv, map[string]string{dirHash: w4Base}, specs, nil)
	}
	in := learningReplayInput{lanes: []learningReplayLane{
		mk(11, "reject", 4), mk(12, "reject", 4), mk(13, "reject", 4), mk(14, "accept", 4),
	}}
	rep := computeLearningReplay(in, w4PassCandidate(0))
	rep.Checks["provenance"] = true // source_seq 0 unused for this pin
	if !rep.Checks["a"] {
		t.Error("check-a fired on a uniform adds-only candidate — the cohort-join changed shape, audit the replay")
	}
	if rep.PreventedHarm != 12 || rep.Friction != 4 {
		t.Errorf("hostile slice counted prevented/friction = %d/%d, want 12/4", rep.PreventedHarm, rep.Friction)
	}
}

// Check-b: re-adding a text that was retracted after a harmful flag fails.
func TestLearningReplayCheckBRetractedAfterHarmful(t *testing.T) {
	rule := "Harmful rule that was retracted"
	flagEv := w4Review(1, rulesAuditFlagAction, map[string]interface{}{
		"verdict": "harmful", "rule": rule, "injections": 12, "rejects": 4, "reject_conversations": 3,
	})
	proposeEv := w4Review(2, "memory_propose", map[string]interface{}{
		"epoch":     1,
		"proposals": []MemoryProposal{{Target: "memory.md", Rule: "Replacement", Evidence: "main-epoch-1", Contradicts: rule}},
	})
	applyEv := w4Review(3, "memory_apply", map[string]interface{}{
		"epoch": 1, "accepted": []MemoryAccept{{Target: "memory.md", Index: 0}},
		"rejected": []MemoryAccept{}, "metrics": map[string]int{"accepted": 1, "rejected": 0},
	})
	snapEv := w4Snapshot(4, "memory", sha16([]byte(w4Base)), w4Base)
	sendEv := w4Send(5, sha16([]byte(w4Base)))
	lane := learningReplayLane{
		convID: 21,
		events: []store.Event{flagEv, proposeEv, applyEv, snapEv, sendEv, w4Done(6), w4Marker(7, 2)},
	}
	cand := w4ProjCandidate(w4Base, []LearningRuleAdd{{Rule: rule, Evidence: "main-epoch-1"}}, 2)
	rep := computeLearningReplay(learningReplayInput{lanes: []learningReplayLane{lane}}, cand)
	if rep.Checks["b"] || rep.Verdict != "fail" {
		t.Errorf("b = %v verdict %q, want false/fail (harmful text re-proposed verbatim)", rep.Checks["b"], rep.Verdict)
	}
	found := false
	for _, v := range rep.Violations {
		if strings.Contains(v.Reason, "retracted after a harmful flag") {
			found = true
		}
	}
	if !found {
		t.Errorf("violations missing the retracted-after-harmful reason: %+v", rep.Violations)
	}
}

// w4SizedContent returns exactly total bytes of valid rule lines.
func w4SizedContent(total int) string {
	suffix := " — cites: main-epoch-1; reaffirmed: 1"
	lineLen := total/2 - 1
	mk := func(n int, ch byte) string {
		return "- " + strings.Repeat(string(ch), n-len(suffix)-2) + suffix
	}
	l1 := mk(lineLen, 'a')
	rem := total - (len(l1) + 1)
	l2 := mk(rem-1, 'b')
	out := l1 + "\n" + l2 + "\n"
	if len(out) != total {
		panic(fmt.Sprintf("w4SizedContent(%d) = %d", total, len(out)))
	}
	return out
}

// Check-c: a projection that must evict an existing rule to fit the cap
// fails (no silent third-party rotation).
func TestLearningReplayCheckCRotationEviction(t *testing.T) {
	full := w4SizedContent(memoryCap)
	fullHash := sha16([]byte(full))
	lane := w4BuildLane(t, 31, map[string]string{fullHash: full},
		[]w4OutcomeSpec{{hash: fullHash, kind: "reject"}}, nil)
	cand := w4ProjCandidate(w4Base, []LearningRuleAdd{{Rule: "New rule", Evidence: "main-epoch-1"}}, 0)
	rep := computeLearningReplay(learningReplayInput{lanes: []learningReplayLane{lane}}, cand)
	if rep.Checks["c"] || rep.Verdict != "fail" {
		t.Errorf("c = %v verdict %q, want false/fail (projection rotated a live rule)", rep.Checks["c"], rep.Verdict)
	}
	found := false
	for _, v := range rep.Violations {
		if strings.Contains(v.Reason, "third-party rotation") {
			found = true
		}
	}
	if !found {
		t.Errorf("violations missing the rotation reason: %+v", rep.Violations)
	}
}

// Check-d: growth beyond +512B fails (cap respected — no rotation).
func TestLearningReplayCheckDGrowthBudget(t *testing.T) {
	baseHash := sha16([]byte(w4Base))
	lane := w4BuildLane(t, 41, map[string]string{baseHash: w4Base},
		[]w4OutcomeSpec{{hash: baseHash, kind: "reject"}}, nil)
	bigRule := strings.Repeat("x", 600)
	cand := w4ProjCandidate(w4Base, []LearningRuleAdd{{Rule: bigRule, Evidence: "main-epoch-1"}}, 0)
	rep := computeLearningReplay(learningReplayInput{lanes: []learningReplayLane{lane}}, cand)
	if rep.Checks["d"] || rep.Verdict != "fail" {
		t.Errorf("d = %v verdict %q, want false/fail (+%dB growth)", rep.Checks["d"], rep.Verdict, rep.GrowthMax)
	}
	if rep.GrowthMax <= learningReplayGrowthCap {
		t.Errorf("growth_max = %d, want > %d", rep.GrowthMax, learningReplayGrowthCap)
	}
	if !rep.Checks["c"] {
		t.Error("a small projection must NOT rotate — c should stay true in the d fixture")
	}
}

// Checks f/g: anti-vacuity and the friction budget (integer boundary).
func TestLearningReplayCheckFGVacuityAndFriction(t *testing.T) {
	baseHash := sha16([]byte(w4Base))
	mk := func(specs []w4OutcomeSpec) learningReplayInput {
		return learningReplayInput{lanes: []learningReplayLane{
			w4BuildLane(t, 51, map[string]string{baseHash: w4Base}, specs, nil),
		}}
	}
	cand := w4PassCandidate(0)
	// Vacuous: accepts only — zero preventable harm.
	rep := computeLearningReplay(mk([]w4OutcomeSpec{{hash: baseHash, kind: "accept"}, {hash: baseHash, kind: "accept"}}), cand)
	if rep.Checks["f"] || rep.Verdict != "fail" {
		t.Errorf("vacuous slice: f = %v verdict %q, want false/fail", rep.Checks["f"], rep.Verdict)
	}
	// Boundary: prevented 2, friction exactly 3×2 = 6 passes; 7 fails.
	pass := make([]w4OutcomeSpec, 0, 8)
	pass = append(pass, w4OutcomeSpec{hash: baseHash, kind: "reject"}, w4OutcomeSpec{hash: baseHash, kind: "reject"})
	for i := 0; i < 6; i++ {
		pass = append(pass, w4OutcomeSpec{hash: baseHash, kind: "accept"})
	}
	rep = computeLearningReplay(mk(pass), cand)
	if !rep.Checks["g"] {
		t.Errorf("friction 6 at prevented 2 must pass (6 ≤ 3×2): %+v", rep.Violations)
	}
	fail := append(append([]w4OutcomeSpec{}, pass...), w4OutcomeSpec{hash: baseHash, kind: "accept"})
	rep = computeLearningReplay(mk(fail), cand)
	if rep.Checks["g"] {
		t.Error("friction 7 at prevented 2 must fail (7 > 3×2)")
	}
	// Auto rejects count toward prevented harm (GLM's class list).
	rep = computeLearningReplay(mk([]w4OutcomeSpec{{hash: baseHash, kind: "reject", auto: true}}), cand)
	if rep.PreventedHarm != 1 || !rep.Checks["f"] {
		t.Errorf("auto_reject coverage: prevented = %d f = %v, want 1/true", rep.PreventedHarm, rep.Checks["f"])
	}
}

// Check-h: retract without harmful-flag evidence is a loosening.
func TestLearningReplayCheckHLoosening(t *testing.T) {
	baseHash := sha16([]byte(w4Base))
	lane := w4BuildLane(t, 61, map[string]string{baseHash: w4Base},
		[]w4OutcomeSpec{{hash: baseHash, kind: "reject"}}, nil)
	cand := w4ProjCandidate(w4Base, nil, 0)
	cand.Delta.Retract = []string{"Base rule"}
	rep := computeLearningReplay(learningReplayInput{lanes: []learningReplayLane{lane}}, cand)
	if rep.Checks["h"] || rep.Loosened != 1 {
		t.Errorf("retract without flag evidence: h = %v loosened %d, want false/1", rep.Checks["h"], rep.Loosened)
	}
	// Same retraction with the harmful flag journaled is conservative.
	lane2 := w4BuildLane(t, 62, map[string]string{baseHash: w4Base},
		[]w4OutcomeSpec{{hash: baseHash, kind: "reject"}},
		[]store.Event{w4Review(1, rulesAuditFlagAction, map[string]interface{}{
			"verdict": "harmful", "rule": "Base rule", "injections": 12, "rejects": 4, "reject_conversations": 3,
		})})
	rep = computeLearningReplay(learningReplayInput{lanes: []learningReplayLane{lane2}}, cand)
	if !rep.Checks["h"] || rep.Loosened != 0 {
		t.Errorf("flagged retraction: h = %v loosened %d, want true/0", rep.Checks["h"], rep.Loosened)
	}
}

// Unverifiable: a covered cohort with no resolvable snapshot is FAIL with
// the missing key named.
func TestLearningReplayUnverifiable(t *testing.T) {
	ghost := "deadbeefcafe0001"
	lane := w4BuildLane(t, 71, map[string]string{}, // no snapshot content at all
		[]w4OutcomeSpec{{hash: ghost, kind: "reject"}}, nil)
	// The builder's snapshot row is only written for hashes WITH content;
	// with an empty map the receipt hash is unresolvable.
	cand := w4PassCandidate(0)
	rep := computeLearningReplay(learningReplayInput{lanes: []learningReplayLane{lane}}, cand)
	if rep.Verdict != "unverifiable" {
		t.Fatalf("verdict = %q, want unverifiable", rep.Verdict)
	}
	found := false
	for _, v := range rep.Violations {
		if strings.Contains(v.Reason, ghost) {
			found = true
		}
	}
	if !found {
		t.Errorf("violations must name the missing snapshot hash: %+v", rep.Violations)
	}
	for name, ok := range rep.Checks {
		if name == "e" {
			continue // the determinism pin is orthogonal to the gate verdict
		}
		if ok {
			t.Errorf("check %s = true under unverifiable (fail-closed means every gate check fails)", name)
		}
	}
}

// Provenance: source_seqs must resolve to the claimed row kinds.
func TestLearningReplayProvenance(t *testing.T) {
	baseHash := sha16([]byte(w4Base))
	lane := w4BuildLane(t, 81, map[string]string{baseHash: w4Base},
		[]w4OutcomeSpec{{hash: baseHash, kind: "reject"}}, nil)
	in := learningReplayInput{lanes: []learningReplayLane{lane}}
	// Ghost source_seq.
	cand := w4ProjCandidate(w4Base, []LearningRuleAdd{{Rule: "Rule x", Evidence: "main-epoch-1"}}, 999)
	rep := computeLearningReplay(in, cand)
	if rep.Checks["provenance"] {
		t.Error("ghost source_seq must fail provenance")
	}
	// Ghost flag ref.
	cand = w4ProjCandidate(w4Base, []LearningRuleAdd{{Rule: "Rule x", Evidence: "main-epoch-1", FlagSeq: 777}}, 0)
	rep = computeLearningReplay(in, cand)
	if rep.Checks["provenance"] {
		t.Error("ghost flag_seq must fail provenance")
	}
}

// Hash idempotence at the creation layer: same delta + same base ⇒ the
// same artifact, jsonl append dedupes to one row.
func TestLearningCandidateHashIdempotence(t *testing.T) {
	adds := []LearningRuleAdd{{Rule: "Same rule", Evidence: "main-epoch-1"}}
	c1 := learningCandidateFromAccepted(w4Base, sha16([]byte(w4Base)), 12, adds, LearningCandidateProvenance{})
	c2 := learningCandidateFromAccepted(w4Base, sha16([]byte(w4Base)), 12, adds, LearningCandidateProvenance{
		CreatedBy: "learner_batch", Uses: 42,
	})
	if c1.ArtifactHash != c2.ArtifactHash {
		t.Error("provenance and created_at must not perturb the artifact hash")
	}
	root := t.TempDir()
	if _, created, err := AppendLearningCandidate(root, c1); err != nil || !created {
		t.Fatalf("first append = created %v err %v", created, err)
	}
	row, created, err := AppendLearningCandidate(root, c2)
	if err != nil || created {
		t.Fatalf("second identical append = created %v err %v, want idempotent no-op", created, err)
	}
	if row.ArtifactHash != c1.ArtifactHash {
		t.Error("dedupe returned a different artifact")
	}
	rows, err := ReadLearningCandidates(root)
	if err != nil || len(rows) != 1 {
		t.Fatalf("candidates.jsonl rows = %d err %v, want exactly 1", len(rows), err)
	}
}
