package ipc

// D9-W5 tests (K3 spec §3.4 promotion predicate + lock R1/§5.3 + D9 Lock
// Amendment A1): the promotion gate's boundary integers (paired floors,
// 1.25× reject leg, +5pp taint leg, harmful drop, retract hold), the A1
// legs (f′ live-exercise with its grace window, the efficacy_vacuity /
// canary_starved drop exits and their boundaries), the rollback-target
// fold, the never-score exclusions inside the paired cohorts (W5
// completion: canary/auto/other-canary/scoring-excluded traffic never
// grades a candidate), the shared attribution-join pin (Amendment 5:
// replay and measure classify through ONE predicate, both folds double-
// execute byte-identically), and the structural evidence→measure→gate
// separation pin (gate signatures carry the measure struct, never
// []store.Event — the shortcut is unrepresentable).

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// w5Measure returns a well-formed paired measure (both cohorts at the
// floor, zero taint, no harmful rows) for mutation. A1: one live reject
// keeps liveHarm ≥ 1 (f′ satisfied — vacuity drops fire otherwise at
// age ≥ 3), and StageEpoch 2 arms the grace clock (age 3 at epoch 5) so
// f′ is evaluated, never silently bypassed on an unknown entry epoch.
func w5Measure() learningCohortMeasure {
	return learningCohortMeasure{
		ArtifactHash: "abc",
		Kind:         "canary",
		Epoch:        5,
		StageEpoch:   2,
		Canary:       learningCohortStats{Outcomes: 10, Accepts: 10, Sends: 10},
		Live:         learningCohortStats{Outcomes: 10, Accepts: 9, Rejects: 1, Sends: 10},
	}
}

func w5Additive() LearningCandidate {
	return LearningCandidate{
		ArtifactHash: "abc",
		Delta: LearningCandidateDelta{
			Add:     []LearningRuleAdd{{Rule: "Good rule", Evidence: "main-epoch-1"}},
			Retract: []string{},
		},
	}
}

func TestLearningPromotionVerdictBoundaries(t *testing.T) {
	t.Parallel()
	cand := w5Additive()

	// Paired floors: 10/10 promotes; either leg at 9 never does
	// (self-reinforcing cohort guard — a canary with no live contrast
	// starves, it never promotes).
	for _, tc := range []struct {
		name         string
		canary, live int
		want         string
	}{
		{"both at floor", 10, 10, "promote"},
		{"canary below", 9, 10, ""},
		{"live below", 10, 9, ""},
		{"both below", 9, 9, ""},
	} {
		m := w5Measure()
		m.Canary.Outcomes, m.Live.Outcomes = tc.canary, tc.live
		m.Canary.Accepts, m.Live.Accepts = tc.canary, tc.live
		if got, _ := learningPromotionVerdict(m, cand); got != tc.want {
			t.Errorf("%s: verdict = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Reject leg, exact 1.25× boundary via integer cross-multiplication:
	// canary (2r+w)·4·liveN vs live (2rl+wl)·5·canaryN. live: 1 reject of
	// 10 (rate .1); canary at 10: rejects 1 (rate .1 ≤ .125 ✓), rejects…
	// rate 2r/20: r=1 ⇒ .1; boundary leg computed exactly.
	m := w5Measure()
	m.Canary.Rejects, m.Live.Rejects = 1, 1 // .1 vs .1×1.25: pass
	if got, _ := learningPromotionVerdict(m, cand); got != "promote" {
		t.Errorf("reject parity: verdict = %q, want promote", got)
	}
	// Over the 1.25× line keeps measuring: live 1 reject of 10 (lr=2);
	// canary 2 rejects of 10 (cr=4): 4·4·10 = 160 > 5·2·10 = 100. (A1:
	// f′ stays satisfied — liveHarm = 1 — so the miss is attributable to
	// the reject leg, not the vacuity branch.)
	m = w5Measure()
	m.Canary.Rejects, m.Canary.Accepts = 2, 8
	if got, _ := learningPromotionVerdict(m, cand); got != "" {
		t.Errorf("over the 1.25× line: verdict = %q, want keep-measuring", got)
	}
	// Exact-boundary equality must pass (integer math): live 4 rejects
	// of 32 vs canary 5 rejects of 32: (10/64) == (8/64)×5/4 exactly.
	m = w5Measure()
	m.Canary = learningCohortStats{Outcomes: 32, Accepts: 27, Rejects: 5, Sends: 32}
	m.Live = learningCohortStats{Outcomes: 32, Accepts: 28, Rejects: 4, Sends: 32}
	if got, _ := learningPromotionVerdict(m, cand); got != "promote" {
		t.Errorf("exact 1.25× boundary: verdict = %q, want promote (locked side)", got)
	}
	m.Canary.Rejects = 6 // 12/64 > 10/64
	if got, _ := learningPromotionVerdict(m, cand); got != "" {
		t.Errorf("past 1.25× boundary: verdict = %q, want keep-measuring", got)
	}

	// Taint leg, exact +5pp boundary: canary errored-share ≤ live + 5pp.
	// live 100 sends, 5 errored (5%); canary 100 sends, 10 errored
	// (10%) — exactly +5pp: passes on the locked side.
	m = w5Measure()
	m.Canary.Sends, m.Canary.ErroredSends = 100, 10
	m.Live.Sends, m.Live.ErroredSends = 100, 5
	if got, _ := learningPromotionVerdict(m, cand); got != "promote" {
		t.Errorf("exact +5pp taint boundary: verdict = %q, want promote", got)
	}
	m.Canary.ErroredSends = 11 // +6pp
	if got, _ := learningPromotionVerdict(m, cand); got != "" {
		t.Errorf("past +5pp taint boundary: verdict = %q, want keep-measuring", got)
	}

	// Retract-carrying delta with passing stats: held_for_human (D4
	// preserved — the daemon never auto-retracts).
	m = w5Measure()
	held := w5Additive()
	held.Delta.Retract = []string{"Old rule"}
	if got, _ := learningPromotionVerdict(m, held); got != "hold" {
		t.Errorf("retract delta: verdict = %q, want hold", got)
	}

	// Harmful tuple on the candidate's own adds: drop (gate fail).
	m = w5Measure()
	m.Rules = []learningRuleMeasure{{
		Rule: "Good rule", Injections: 10, Rejects: 3, RejectConversations: 3, Harmful: true,
	}}
	if got, d := learningPromotionVerdict(m, cand); got != "drop" || d["harmful_rule"] == nil || d["drop_cause"] != "harmful_tuple" {
		t.Errorf("harmful row: verdict = %q detail %v, want drop (drop_cause harmful_tuple) with the harmful rule cited", got, d)
	}
}

// TestLearningPromotionVerdictFPrime pins the A1 legs (Amendments 2+3):
// f′ = liveHarm ≥ 1 evaluated ONLY after the paired floors, the 3-epoch
// grace, the drop exits (efficacy_vacuity / canary_starved) with their
// boundary ages, StageEpoch 0 read as "not yet", and the harmful leg
// staying first. The per-check boolean rides the verdict detail map
// (replay checks convention), drop_cause drives the tick's exit
// dispatch, and the starved row carries the exclusion counters.
func TestLearningPromotionVerdictFPrime(t *testing.T) {
	t.Parallel()
	cand := w5Additive()
	cases := []struct {
		name      string
		mutate    func(m *learningCohortMeasure)
		want      string
		wantCause string
		fPrime    *bool // nil: checks key must be ABSENT (f′ not evaluated)
		excluded  bool  // starved rows carry the exclusion counters
	}{
		{
			name: "floors met, liveHarm ≥ 1 — f′ satisfied",
			want: "promote", fPrime: ptr(true),
		},
		{
			name:   "weak reject counts as live harm",
			mutate: func(m *learningCohortMeasure) { m.Live.Rejects, m.Live.WeakRejects, m.Live.Accepts = 0, 1, 9 },
			want:   "promote", fPrime: ptr(true),
		},
		{
			name: "vacuous inside the grace window (age 2 < 3) — keep measuring",
			mutate: func(m *learningCohortMeasure) {
				m.Live.Rejects, m.Live.Accepts = 0, 10
				m.StageEpoch = 3 // age 2 at epoch 5
			},
			want: "",
		},
		{
			name: "vacuous with unknown entry (StageEpoch 0, pre-A1 rows) — keep measuring",
			mutate: func(m *learningCohortMeasure) {
				m.Live.Rejects, m.Live.Accepts = 0, 10
				m.StageEpoch = 0
			},
			want: "",
		},
		{
			name: "vacuous at the grace boundary (age 3) — efficacy_vacuity drop",
			mutate: func(m *learningCohortMeasure) {
				m.Live.Rejects, m.Live.Accepts = 0, 10 // age 3 at epoch 5 (StageEpoch 2)
			},
			want: "drop", wantCause: "efficacy_vacuity", fPrime: ptr(false),
		},
		{
			name: "floors unmet at the starve boundary (age 24) — keep measuring",
			mutate: func(m *learningCohortMeasure) {
				m.Canary.Outcomes, m.Canary.Accepts = 9, 9
				m.StageEpoch, m.Epoch = 1, 25 // age 24: the drop needs > 24
			},
			want: "",
		},
		{
			name: "floors unmet past the starve floor (age 25) — canary_starved drop",
			mutate: func(m *learningCohortMeasure) {
				m.Canary.Outcomes, m.Canary.Accepts = 9, 9
				m.StageEpoch, m.Epoch = 1, 26
			},
			want: "drop", wantCause: "canary_starved", excluded: true,
		},
		{
			name: "harmful leg stays first (liveHarm 0, age ≥ 3)",
			mutate: func(m *learningCohortMeasure) {
				m.Live.Rejects, m.Live.Accepts = 0, 10
				m.Rules = []learningRuleMeasure{{Rule: "Good rule", Injections: 10, Rejects: 3, RejectConversations: 3, Harmful: true}}
			},
			want: "drop", wantCause: "harmful_tuple",
		},
	}
	for _, tc := range cases {
		m := w5Measure()
		if tc.mutate != nil {
			tc.mutate(&m)
		}
		got, d := learningPromotionVerdict(m, cand)
		if got != tc.want {
			t.Errorf("%s: verdict = %q, want %q (detail %v)", tc.name, got, tc.want, d)
			continue
		}
		if got == "" {
			continue // keep-measuring verdicts carry no detail (the measure row is the journal)
		}
		if tc.wantCause != "" && d["drop_cause"] != tc.wantCause {
			t.Errorf("%s: drop_cause = %v, want %s", tc.name, d["drop_cause"], tc.wantCause)
		}
		checks, _ := d["checks"].(map[string]bool)
		if tc.fPrime == nil {
			if _, ok := checks["f_prime"]; ok {
				t.Errorf("%s: f_prime present (%v) — f′ is evaluated ONLY after the paired floors", tc.name, checks)
			}
		} else if gotFP, ok := checks["f_prime"]; !ok || gotFP != *tc.fPrime {
			t.Errorf("%s: f_prime = %v ok %v, want %v (per-check boolean rides the detail map)", tc.name, gotFP, ok, *tc.fPrime)
		}
		if tc.excluded {
			if ex, ok := d["excluded"]; !ok || ex != m.Excluded {
				t.Errorf("%s: excluded = %v ok %v, want the measure's exclusion counters", tc.name, ex, ok)
			}
		}
	}
}

// ptr boxes a bool literal for table expectations.
func ptr(b bool) *bool { return &b }

func TestLearningRollbackTargets(t *testing.T) {
	t.Parallel()
	m := w5Measure()
	m.Rules = []learningRuleMeasure{
		{Rule: "Benign", Injections: 30, Accepts: 30},
		{Rule: "Bad one", Injections: 10, Rejects: 3, RejectConversations: 3, Harmful: true},
	}
	targets := learningRollbackTargets(m)
	if len(targets) != 1 || targets[0].Rule != "Bad one" {
		t.Errorf("targets = %+v, want only the harmful row", targets)
	}
}

// w5SeedLaneOutcome journals one resolved outcome over cohort memHash:
// send carrying the receipt, done, diff, review verdict. verdict is the
// review action ("accept"|"reject"); actor "" = the human click,
// AutoActor = the auto-land pipeline (the M17 F5 exclusion class).
func w5SeedLaneOutcome(t *testing.T, rig *testRig, convID int64, cohortSHA, patchPath, verdict, actor string) {
	t.Helper()
	ctx := context.Background()
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventUserMessage, mustJSON(map[string]interface{}{
		"text": "task " + patchPath, "receipt": map[string]string{".odo/memory.md": cohortSHA}})); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventAgentDone, mustJSON(map[string]interface{}{})); err != nil {
		t.Fatalf("done: %v", err)
	}
	d, err := rig.store.InsertDiff(ctx, convID, patchPath, "", "", "")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	body := map[string]interface{}{"action": verdict, "diff_id": d.ID}
	if actor != "" {
		body["actor"] = actor
	}
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(body)); err != nil {
		t.Fatalf("review: %v", err)
	}
}

// w5SeedSnapshot pins a layer:"memory" snapshot row (idempotent content
// per sha — the fold's first-writer-wins discipline).
func w5SeedSnapshot(t *testing.T, rig *testRig, convID int64, content string) string {
	t.Helper()
	sha := sha16([]byte(content))
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer": "memory", "cause": "snapshot", "source": ".odo/memory.md", "sha": sha, "content": content})); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return sha
}

// w5Candidate builds + appends one candidate artifact (content explicit —
// fixtures control the projected block).
func w5Candidate(t *testing.T, root, content string, adds []LearningRuleAdd, retract []string) LearningCandidate {
	t.Helper()
	cand := LearningCandidate{
		Version: 1, Scope: learningCandidateScope,
		BaseSHA16: sha16([]byte(content)),
		Delta:     LearningCandidateDelta{Add: adds, Retract: retract},
		Content:   content,
	}
	cand.ArtifactHash = LearningArtifactHash(cand)
	row, _, err := AppendLearningCandidate(root, cand)
	if err != nil {
		t.Fatalf("append candidate: %v", err)
	}
	return row
}

// w5Stage journals one learning_stage transition row.
func w5Stage(t *testing.T, rig *testRig, convID int64, hash, from, to string, epoch int) {
	t.Helper()
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action": "learning_stage", "artifact_hash": hash, "from": from, "to": to, "cause": "w5_fixture", "epoch": epoch})); err != nil {
		t.Fatalf("stage %s→%s: %v", from, to, err)
	}
}

// w5PinCanarySnapshot journals the candidate's pinned canary block row
// (pinLearningCanarySnapshot's shape).
func w5PinCanarySnapshot(t *testing.T, rig *testRig, convID int64, cand LearningCandidate) string {
	t.Helper()
	sha := sha16([]byte(cand.Content))
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer": "learning_canary", "cause": "snapshot",
		"source": "learning/" + cand.ArtifactHash, "artifact_hash": cand.ArtifactHash,
		"sha": sha, "content": cand.Content})); err != nil {
		t.Fatalf("pin canary: %v", err)
	}
	return sha
}

// w5RigWithLanes boots a rig and adds named sibling workstreams; returns
// main's conv and the sibling convs (all under the rig's project).
func w5RigWithLanes(t *testing.T, root string, siblings ...string) (*testRig, int64, map[string]int64) {
	t.Helper()
	rig := startRig(t, root)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	ctx := context.Background()
	p, err := rig.store.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		rig.stop(t)
		t.Fatalf("project: %v", err)
	}
	convs := map[string]int64{}
	for _, name := range siblings {
		w, err := rig.store.CreateOrGetWorkstream(ctx, p.ID, name)
		if err != nil {
			rig.stop(t)
			t.Fatalf("workstream %s: %v", name, err)
		}
		c, err := rig.store.CreateConversation(ctx, w.ID, "")
		if err != nil {
			rig.stop(t)
			t.Fatalf("conversation %s: %v", name, err)
		}
		convs[name] = c.ID
	}
	return rig, boot.Conversation.ID, convs
}

func TestLearningMeasureNeverScoreExclusions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := initRepo(t)
	rig, convID, lanes := w5RigWithLanes(t, root, "ui")
	defer rig.stop(t)
	ctx := context.Background()
	dir := t.TempDir()
	defUIPath := filepath.Join(dir, "code.diff") // feature-code patch: scoreable
	w4Patch(t, dir, "code.diff", "src/feature.go")
	gatePath := w4Patch(t, dir, "gate.diff", "internal/ipc/server.go") // gate source: excluded

	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	block := base + "- Candidate rule — cites: main-epoch-1; reaffirmed: 0\n"
	cand := w5Candidate(t, root, block, []LearningRuleAdd{{Rule: "Candidate rule", Evidence: "main-epoch-1"}}, nil)
	otherBlock := base + "- Other candidate — cites: main-epoch-1; reaffirmed: 0\n"
	other := w5Candidate(t, root, otherBlock, []LearningRuleAdd{{Rule: "Other candidate", Evidence: "main-epoch-1"}}, nil)

	// Cohorts: candidate's canary block, the OTHER artifact's canary
	// block, and the live memory snapshot.
	canSHA := w5PinCanarySnapshot(t, rig, convID, cand)
	othSHA := w5PinCanarySnapshot(t, rig, convID, other)
	liveSHA := w5SeedSnapshot(t, rig, convID, base)

	// Canary outcomes: 10 (8 accepts, 2 rejects) — one riding a
	// gate-source diff (excluded from metrics, counted honest).
	for i := 0; i < 8; i++ {
		w5SeedLaneOutcome(t, rig, convID, canSHA, defUIPath, "accept", "")
	}
	w5SeedLaneOutcome(t, rig, convID, canSHA, defUIPath, "reject", "")
	w5SeedLaneOutcome(t, rig, convID, canSHA, defUIPath, "reject", "")
	w5SeedLaneOutcome(t, rig, lanes["ui"], canSHA, gatePath, "accept", "") // gate source: excluded

	// Live outcomes: 10 accepts (one on another lane); one OTHER-canary
	// and one auto outcome ride along (both must isolate).
	for i := 0; i < 9; i++ {
		w5SeedLaneOutcome(t, rig, convID, liveSHA, defUIPath, "accept", "")
	}
	w5SeedLaneOutcome(t, rig, lanes["ui"], liveSHA, defUIPath, "accept", "")
	w5SeedLaneOutcome(t, rig, convID, othSHA, defUIPath, "accept", "")         // other canary: excluded
	w5SeedLaneOutcome(t, rig, convID, liveSHA, defUIPath, "reject", AutoActor) // auto: excluded

	w, _ := rig.store.GetWorkstream(ctx, 1)
	in := rig.server.gatherLearningReplayInput(ctx, w.ProjectID)
	m := computeLearningMeasure(in, cand, "", 5)

	if m.Canary.Outcomes != 10 || m.Canary.Accepts != 8 || m.Canary.Rejects != 2 {
		t.Errorf("canary tally = %+v, want 10 outcomes (8a/2r; the gate-source one excluded)", m.Canary)
	}
	if m.Live.Outcomes != 10 || m.Live.Accepts != 10 {
		t.Errorf("live tally = %+v, want 10 accepts (other-canary + auto isolated)", m.Live)
	}
	if m.Excluded.OtherCanary != 1 || m.Excluded.Auto != 1 || m.Excluded.ScoringExcluded != 1 {
		t.Errorf("exclusions = %+v, want other_canary 1, auto 1, scoring_excluded 1", m.Excluded)
	}
	// Per-rule rows: the candidate's rule was in play for the 10
	// canary outcomes only (its block).
	var row *learningRuleMeasure
	for i := range m.Rules {
		if m.Rules[i].Rule == "Candidate rule" {
			row = &m.Rules[i]
		}
	}
	if row == nil || row.Injections != 10 || row.Rejects != 2 {
		t.Errorf("candidate rule row = %+v, want 10 injections / 2 rejects from the canary cohort", row)
	}
	// Not harmful: the rejects leg needs ≥3 (this row carries 2).
	if row != nil && row.Harmful {
		t.Errorf("rule row harmful = true, want false (rejects below the ≥3 leg)")
	}
	// Determinism (double-execution pin, the replay-e precedent).
	b1, _ := json.Marshal(computeLearningMeasure(in, cand, "", 5))
	b2, _ := json.Marshal(computeLearningMeasure(in, cand, "", 5))
	if string(b1) != string(b2) {
		t.Error("double execution diverged — the measure fold must be byte-identical")
	}
}

// TestLearningSharedAttributionJoin pins A1 Amendment 5: the frozen
// replay's covered-outcome join and the measure fold's cohort bucketing
// share ONE predicate — over the SAME gathered input both consumers
// attribute identically per class, and both folds double-execute
// byte-identically (the replay-era double-execution fixture extended to
// the measure fold). One lane, one marker: the replay slice and the
// unbounded measure window coincide, so every per-class count is
// directly comparable.
func TestLearningSharedAttributionJoin(t *testing.T) {
	dir := t.TempDir()
	baseHash := sha16([]byte(w4Base))
	ownBlock := w4Base + "- Candidate rule — cites: main-epoch-1; reaffirmed: 0\n"
	ownSha := sha16([]byte(ownBlock))
	otherBlock := w4Base + "- Other rule — cites: main-epoch-1; reaffirmed: 0\n"
	otherSha := sha16([]byte(otherBlock))
	otherOwner := "ffffffffffffffff"

	canaryPin := func(seq int, owner, sha, content string) store.Event {
		return w4Event(seq, store.EventMemoryUpdate, map[string]interface{}{
			"layer": "learning_canary", "cause": "snapshot", "source": "learning/" + owner,
			"artifact_hash": owner, "sha": sha, "content": content})
	}
	events := []store.Event{
		w4Snapshot(1, "memory", baseHash, w4Base),
		canaryPin(2, "placeholder", ownSha, ownBlock), // owner replaced below once the hash is known
		canaryPin(3, otherOwner, otherSha, otherBlock),
	}

	seq := 4
	var diffs []store.Diff
	outcome := func(memHash, kind, patchFile, patchTarget string, id int64) {
		events = append(events, w4Send(seq, memHash), w4Done(seq+1))
		diffPath := w4Patch(t, dir, patchFile, patchTarget)
		diffs = append(diffs, store.Diff{ID: id, ConversationID: 91, PathOnDisk: diffPath})
		events = append(events, w4Review(seq+2, kind, map[string]interface{}{"diff_id": id}))
		seq += 3
	}
	// One outcome per attribution class: live harm, live on a gate-source
	// diff (scoring-excluded), own-canary, other-canary, memory-free.
	outcome(baseHash, "reject", "live.diff", "src/feature.go", 9101)
	outcome(baseHash, "accept", "gate.diff", "internal/ipc/server.go", 9102)
	outcome(ownSha, "reject", "own.diff", "src/feature.go", 9103)
	outcome(otherSha, "accept", "other.diff", "src/feature.go", 9104)
	outcome("", "accept", "free.diff", "src/feature.go", 9105)

	proposeSeq := seq
	events = append(events, w4Review(proposeSeq, "memory_propose", map[string]interface{}{
		"epoch":     1,
		"proposals": []MemoryProposal{{Target: "memory.md", Rule: "Candidate rule", Evidence: "main-epoch-1"}},
	}))
	events = append(events, w4Marker(proposeSeq+1, 1))
	in := learningReplayInput{lanes: []learningReplayLane{{convID: 91, events: events, diffs: diffs}}}
	cand := w4ProjCandidate(w4Base, []LearningRuleAdd{{Rule: "Candidate rule", Evidence: "main-epoch-1"}}, proposeSeq)
	events[1] = canaryPin(2, cand.ArtifactHash, ownSha, ownBlock) // real owner now resolvable

	rep := computeLearningReplay(in, cand)
	m := computeLearningMeasure(in, cand, "", 5)
	if rep.Verdict != "pass" {
		t.Fatalf("verdict = %q, want pass; violations %+v", rep.Verdict, rep.Violations)
	}
	// Per-class agreement across the two folds (one implementation,
	// no drift):
	if rep.CanaryExcluded != m.Canary.Outcomes+m.Excluded.OtherCanary {
		t.Errorf("canary-class split: replay excluded %d vs measure own %d + other %d — the joins drifted",
			rep.CanaryExcluded, m.Canary.Outcomes, m.Excluded.OtherCanary)
	}
	if rep.ScoringExcluded != m.Excluded.ScoringExcluded {
		t.Errorf("scoring exclusion: replay %d vs measure %d — the joins drifted", rep.ScoringExcluded, m.Excluded.ScoringExcluded)
	}
	if want := rep.Outcomes - rep.CanaryExcluded - rep.ScoringExcluded; m.Live.Outcomes != want {
		t.Errorf("live leg: measure %d vs replay-covered+memfree %d — the joins drifted", m.Live.Outcomes, want)
	}
	if liveHarm := m.Live.Rejects + m.Live.WeakRejects; rep.PreventedHarm != liveHarm {
		t.Errorf("live harm: replay telemetry %d vs measure liveHarm %d (f′'s attribution) — the joins drifted", rep.PreventedHarm, liveHarm)
	}
	// The predicate's leaf semantics, pinned directly (both consumers
	// call THIS function — no shadow implementations).
	canaryOf := map[string]string{ownSha: cand.ArtifactHash, otherSha: otherOwner}
	excluded := map[int64]bool{9102: true}
	for _, c := range []struct {
		name string
		o    rulesOutcome
		want learningOutcomeClass
	}{
		{"live reject", rulesOutcome{kind: "reject", memHash: baseHash, diffID: 9101}, learningClassLive},
		{"gate-source diff", rulesOutcome{kind: "accept", memHash: baseHash, diffID: 9102}, learningClassScoringExcluded},
		{"own canary", rulesOutcome{kind: "reject", memHash: ownSha, diffID: 9103}, learningClassOwnCanary},
		{"other canary", rulesOutcome{kind: "accept", memHash: otherSha, diffID: 9104}, learningClassOtherCanary},
		{"memory-free", rulesOutcome{kind: "accept", memHash: "", diffID: 9105}, learningClassLive},
		{"scoring class gates first (canary + excluded diff)", rulesOutcome{kind: "accept", memHash: ownSha, diffID: 9102}, learningClassScoringExcluded},
	} {
		if got := learningClassifyOutcome(cand.ArtifactHash, canaryOf, excluded, c.o); got != c.want {
			t.Errorf("%s: class = %d, want %d", c.name, got, c.want)
		}
	}
	// Double execution, BOTH folds byte-identical (the replay-e pin
	// extended to the measure fold).
	r1, _ := json.Marshal(computeLearningReplay(in, cand))
	r2, _ := json.Marshal(computeLearningReplay(in, cand))
	if string(r1) != string(r2) {
		t.Error("replay double execution diverged")
	}
	m1, _ := json.Marshal(computeLearningMeasure(in, cand, "", 5))
	m2, _ := json.Marshal(computeLearningMeasure(in, cand, "", 5))
	if string(m1) != string(m2) {
		t.Error("measure double execution diverged")
	}
}

// TestLearningGateSignaturesSeparation is the §5.3 structural pin: the
// gate predicates consume the measure struct — never raw evidence. AST-
// level: no gate predicate declares a []store.Event (or any store.Event
// container) in its signature.
func TestLearningGateSignaturesSeparation(t *testing.T) {
	t.Parallel()
	gates := map[string]bool{
		"learningPromotionVerdict": false,
		"learningRollbackTargets":  false,
	}
	fset := token.NewFileSet()
	for _, file := range []string{"learning_measure.go", "learning_rollback.go", "learning_stages.go"} {
		f, err := parser.ParseFile(fset, filepath.Join(".", file), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			if _, isGate := gates[fn.Name.Name]; !isGate {
				return true
			}
			gates[fn.Name.Name] = true
			check := func(fl *ast.FieldList) {
				if fl == nil {
					return
				}
				for _, field := range fl.List {
					text := exprString(fset, field.Type)
					if containsEventType(text) {
						t.Errorf("%s: gate signature carries evidence (%s) — §5.3: gates read the measure only", fn.Name.Name, text)
					}
				}
			}
			check(fn.Type.Params)
			check(fn.Type.Results)
			return true
		})
	}
	for name, seen := range gates {
		if !seen {
			t.Errorf("gate predicate %s not found — the separation pin must not silently lose its target", name)
		}
	}
}

// containsEventType reports whether a rendered type expression names the
// journal's evidence row type.
func containsEventType(text string) bool {
	return strings.Contains(text, "store.Event") || strings.Contains(text, "[]Event")
}

// exprString renders an AST type expression (go/printer discipline,
// minimal: identifiers, selectors, slices, pointers).
func exprString(fset *token.FileSet, e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return exprString(fset, t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprString(fset, t.Elt)
	case *ast.StarExpr:
		return "*" + exprString(fset, t.X)
	case *ast.MapType:
		return "map[" + exprString(fset, t.Key) + "]" + exprString(fset, t.Value)
	case *ast.FuncType:
		return "func"
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.Ellipsis:
		return "..." + exprString(fset, t.Elt)
	}
	return "unknown"
}
