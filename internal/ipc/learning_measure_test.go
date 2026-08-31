package ipc

// D9-W5 tests (K3 spec §3.4 promotion predicate + lock R1/§5.3): the
// promotion gate's boundary integers (paired floors, 1.25× reject leg,
// +5pp taint leg, harmful drop, retract hold), the rollback-target fold,
// the never-score exclusions inside the paired cohorts (W5 completion:
// canary/auto/other-canary/scoring-excluded traffic never grades a
// candidate), the double-execution determinism pin, and the structural
// evidence→measure→gate separation pin (gate signatures carry the
// measure struct, never []store.Event — the shortcut is unrepresentable).

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
// floor, zero rejects, zero taint, no harmful rows) for mutation.
func w5Measure() learningCohortMeasure {
	return learningCohortMeasure{
		ArtifactHash: "abc",
		Kind:         "canary",
		Epoch:        5,
		Canary:       learningCohortStats{Outcomes: 10, Accepts: 10, Sends: 10},
		Live:         learningCohortStats{Outcomes: 10, Accepts: 10, Sends: 10},
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
	// 10 outcomes, 0/10 live rejects: any canary reject breaks 0×1.25.
	m = w5Measure()
	m.Canary.Rejects = 1
	if got, _ := learningPromotionVerdict(m, cand); got != "" {
		t.Errorf("zero live rejects: verdict = %q, want keep-measuring", got)
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
	if got, d := learningPromotionVerdict(m, cand); got != "drop" || d["harmful_rule"] == nil {
		t.Errorf("harmful row: verdict = %q detail %v, want drop with the harmful rule cited", got, d)
	}
}

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
