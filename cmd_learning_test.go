package main

// D9-W6: `odo learning` CLI — validation matrix (arg parsing exit 2),
// drop/apply/promote --global roundtrips against a store fixture (rows
// journaled on main, files behave as the action contracts demand), and
// the stall listing surface (list --stalled advisory-only).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// cliLearningFixture seeds a project + main-lane conversation + candidate
// artifacts, journals the given stage rows and stall advisories, and
// closes the store so the CLI exercises its own open
// (cmd_unretract_test.go's shape). stages maps artifact hash → target
// stage; stalls maps artifact hash → its advisory's stage (reason text
// fixtures the W5 emitter's shape).
func cliLearningFixture(t *testing.T, cands []ipc.LearningCandidate, stages map[string]string, stalls map[string]string) (root string) {
	t.Helper()
	root = t.TempDir()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, cand := range cands {
		if _, _, err := ipc.AppendLearningCandidate(root, cand); err != nil {
			t.Fatalf("append candidate: %v", err)
		}
	}
	for hash, to := range stages {
		payload := fmt.Sprintf(`{"action":"learning_stage","artifact_hash":%q,"from":"x","to":%q,"cause":"cli_fixture","epoch":1}`, hash, to)
		if _, err := st.AppendEvent(ctx, c.ID, store.EventReviewAction, payload); err != nil {
			t.Fatalf("stage %s: %v", to, err)
		}
	}
	for hash, stage := range stalls {
		payload := fmt.Sprintf(`{"layer":"learning","cause":"learning_stall","artifact_hash":%q,"stage":%q,"epoch":14,"reason":"%s aged past its floor"}`, hash, stage, stage)
		if _, err := st.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, payload); err != nil {
			t.Fatalf("stall: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

// cliCandidate builds one candidate artifact (constant base sha — the
// hash covers it; uniqueness rides the delta/content), its
// ArtifactHash computed with the production constructor (the appender
// recomputes identically on write).
func cliCandidate(content string, adds []ipc.LearningRuleAdd, retract []string) ipc.LearningCandidate {
	c := ipc.LearningCandidate{
		Version: 1, Scope: "project:memory", BaseSHA16: "0123456789abcdef",
		Delta:   ipc.LearningCandidateDelta{Add: adds, Retract: retract},
		Content: content,
		Provenance: ipc.LearningCandidateProvenance{
			CreatedBy: "human", SourceSeq: []int{1}, ProposeEpoch: 1,
			Uses: 0, Cost: map[string]interface{}{"usage_available": false},
		},
	}
	c.ArtifactHash = ipc.LearningArtifactHash(c)
	return c
}

// cliMainEvents reopens the journal read-only and returns main's rows.
func cliMainEvents(t *testing.T, root string) []store.Event {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenReadOnly(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.GetProjectByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWorkstreamByName(ctx, p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetActiveConversation(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := st.ListEvents(ctx, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// cliActionSeqs returns the journaled seqs of review_action rows carrying
// one action name, in seq order.
func cliActionSeqs(events []store.Event, action string) []int {
	var out []int
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Action == action {
			out = append(out, ev.Seq)
		}
	}
	return out
}

// cliMemReceipts counts memory_update{layer:"memory"} rows per cause.
func cliMemReceipts(events []store.Event) map[string]int {
	out := map[string]int{}
	for _, ev := range events {
		if ev.Type != store.EventMemoryUpdate {
			continue
		}
		var u struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
		}
		if json.Unmarshal(ev.Payload, &u) == nil && u.Layer == "memory" {
			out[u.Cause]++
		}
	}
	return out
}

func cliRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// TestLearningCLIValidation: subcommand arity/flags refuse at exit 2;
// resolvable-arg failures at exit 1 (command registration + arg parsing).
func TestLearningCLIValidation(t *testing.T) {
	root := cliLearningFixture(t, nil, nil, nil)
	t.Chdir(root)

	for _, args := range [][]string{
		{},                       // no subcommand
		{"frobnicate"},           // unknown subcommand
		{"status", "extra"},      // arity
		{"list", "--bogus"},      // unknown list flag
		{"drop"},                 // missing hash
		{"drop", "a", "b"},       // arity
		{"apply"},                // missing hash
		{"promote"},              // the only human promote is --global
		{"promote", "--project"}, // no other human promote scope
		{"promote", "--global"},  // missing hash
		{"promote", "0123abcd"},  // promote without --global
	} {
		_, _, code := captureCLI(t, func() int { return runLearningCLI(args) })
		if code != 2 {
			t.Errorf("args %v: exit %d, want 2 (usage)", args, code)
		}
	}

	// A well-formed but unresolvable action fails 1, naming the lookup.
	_, stderr, code := captureCLI(t, func() int { return runLearningCLI([]string{"drop", "deadbeefdeadbeef"}) })
	if code != 1 || !strings.Contains(stderr, "no learning candidate") {
		t.Errorf("unknown hash: exit %d stderr %q, want 1 + resolution error", code, stderr)
	}
}

// TestLearningCLIDropRoundtrip: a staged shadow candidate drops —
// marker-first journal rows land on main, memory.md is byte-untouched,
// and the fold immediately reads dropped (list surfaces it).
func TestLearningCLIDropRoundtrip(t *testing.T) {
	mem := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	cand := cliCandidate(mem+"- CLI drop rule — cites: main-epoch-1; reaffirmed: 0\n",
		[]ipc.LearningRuleAdd{{Rule: "CLI drop rule", Evidence: "main-epoch-1"}}, nil)
	root := cliLearningFixture(t, []ipc.LearningCandidate{cand}, map[string]string{cand.ArtifactHash: "shadow"}, nil)
	if err := os.WriteFile(filepath.Join(root, ".odo", "memory.md"), []byte(mem), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int { return runLearningCLI([]string{"drop", cand.ArtifactHash[:16]}) })
	if code != 0 {
		t.Fatalf("drop: exit %d, stdout %q", code, stdout)
	}
	if !strings.Contains(stdout, "was shadow") || !strings.Contains(stdout, "dropped") {
		t.Errorf("stdout = %q, want the drop confirmation", stdout)
	}
	events := cliMainEvents(t, root)
	if got := cliActionSeqs(events, "learning_drop"); len(got) != 1 {
		t.Errorf("learning_drop seqs = %v, want exactly 1 marker", got)
	}
	if got := cliActionSeqs(events, "learning_stage"); len(got) != 2 {
		t.Errorf("learning_stage seqs = %v, want fixture row + drop transition", got)
	}
	if got := cliRead(t, filepath.Join(root, ".odo", "memory.md")); got != mem {
		t.Errorf("memory.md after CLI drop = %q, want byte-unchanged %q (candidate-layer only)", got, mem)
	}

	// The shared fold immediately sees the terminal stage.
	out2, _, code2 := captureCLI(t, func() int { return runLearningCLI([]string{"list"}) })
	if code2 != 0 || !strings.Contains(out2, "dropped") {
		t.Errorf("list after drop: exit %d stdout %q, want the dropped stage", code2, out2)
	}
	// Terminal refusal: re-drop exits 1 naming the terminal stage.
	_, stderr, code3 := captureCLI(t, func() int { return runLearningCLI([]string{"drop", cand.ArtifactHash[:16]}) })
	if code3 != 1 || !strings.Contains(stderr, "terminal stage") {
		t.Errorf("re-drop: exit %d stderr %q, want 1 + terminal refusal", code3, stderr)
	}
}

// TestLearningCLIApplyRoundtrip: a held candidate with an add + a
// retraction applies with the receipted path — memory.md updated,
// archive records the retracted line, journal carries the marker +
// receipts + stage row.
func TestLearningCLIApplyRoundtrip(t *testing.T) {
	base := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	old := base + "- CLI old rule — cites: main-epoch-1; reaffirmed: 0\n"
	cand := cliCandidate(old+"- CLI new rule — cites: main-epoch-2; reaffirmed: 0\n",
		[]ipc.LearningRuleAdd{{Rule: "CLI new rule", Evidence: "main-epoch-2"}}, []string{"CLI old rule"})
	root := cliLearningFixture(t, []ipc.LearningCandidate{cand}, map[string]string{cand.ArtifactHash: "held_for_human"}, nil)
	if err := os.WriteFile(filepath.Join(root, ".odo", "memory.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int { return runLearningCLI([]string{"apply", cand.ArtifactHash[:12]}) })
	if code != 0 {
		t.Fatalf("apply: exit %d, stdout %q", code, stdout)
	}
	if !strings.Contains(stdout, "→ project_active") || !strings.Contains(stdout, "memory_apply marker") {
		t.Errorf("stdout = %q, want the apply confirmation", stdout)
	}
	got := cliRead(t, filepath.Join(root, ".odo", "memory.md"))
	if strings.Contains(got, "CLI old rule") || !strings.Contains(got, "CLI new rule") || !strings.Contains(got, "Base rule") {
		t.Errorf("memory.md after apply = %q, want old retracted + new present + base kept", got)
	}
	if archive := cliRead(t, filepath.Join(root, ".odo", "memory-archive.md")); !strings.Contains(archive, "CLI old rule") {
		t.Errorf("archive = %q, want the retraction record", archive)
	}
	events := cliMainEvents(t, root)
	if got := cliActionSeqs(events, "memory_apply"); len(got) != 1 {
		t.Errorf("memory_apply markers = %v, want exactly 1", got)
	}
	receipts := cliMemReceipts(events)
	if receipts["apply"] != 1 || receipts["retract"] != 1 {
		t.Errorf("receipts = %v, want apply 1 + retract 1", receipts)
	}
}

// TestLearningCLIPromoteGlobalRoundtrip: the global promotion prints the
// rule lines + the D4 notice, journals marker + stage row, and NEVER
// writes user.md.
func TestLearningCLIPromoteGlobalRoundtrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userBefore := "- Human global rule\n"
	if err := os.MkdirAll(filepath.Join(home, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".odo", "user.md"), []byte(userBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	mem := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	cand := cliCandidate(mem+"- Global CLI rule — cites: main-epoch-1; reaffirmed: 1\n",
		[]ipc.LearningRuleAdd{{Rule: "Global CLI rule", Evidence: "main-epoch-1"}}, nil)
	root := cliLearningFixture(t, []ipc.LearningCandidate{cand}, map[string]string{cand.ArtifactHash: "project_active"}, nil)
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int { return runLearningCLI([]string{"promote", "--global", cand.ArtifactHash[:12]}) })
	if code != 0 {
		t.Fatalf("promote --global: exit %d, stdout %q", code, stdout)
	}
	for _, want := range []string{"→ global_active", "user.md is human-owned", "- Global CLI rule"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want substring %q", stdout, want)
		}
	}
	events := cliMainEvents(t, root)
	if got := cliActionSeqs(events, "learning_promote"); len(got) != 1 {
		t.Errorf("learning_promote markers = %v, want exactly 1", got)
	}
	if got := cliRead(t, filepath.Join(home, ".odo", "user.md")); got != userBefore {
		t.Errorf("user.md after CLI promote --global = %q, want byte-unchanged %q (NEVER written)", got, userBefore)
	}
	// Already global: the fold reads it; a re-promote refuses cleanly.
	_, stderr, code2 := captureCLI(t, func() int { return runLearningCLI([]string{"promote", "--global", cand.ArtifactHash[:12]}) })
	if code2 != 1 || !strings.Contains(stderr, "already global_active") {
		t.Errorf("re-promote: exit %d stderr %q, want 1 + terminal refusal", code2, stderr)
	}
}

// TestLearningCLIListStalled: the stall closeout surface — list marks
// stalled candidates, --stalled filters to them with the advisory
// reason, and the --json shape carries candidates + stalls.
func TestLearningCLIListStalled(t *testing.T) {
	mem := "- Base rule — cites: main-epoch-1; reaffirmed: 1\n"
	stalled := cliCandidate(mem+"- Stall rule — cites: main-epoch-1; reaffirmed: 0\n",
		[]ipc.LearningRuleAdd{{Rule: "Stall rule", Evidence: "main-epoch-1"}}, nil)
	free := cliCandidate(mem+"- Free rule — cites: main-epoch-1; reaffirmed: 0\n",
		[]ipc.LearningRuleAdd{{Rule: "Free rule", Evidence: "main-epoch-1"}}, nil)
	root := cliLearningFixture(t,
		[]ipc.LearningCandidate{stalled, free},
		map[string]string{stalled.ArtifactHash: "shadow", free.ArtifactHash: "canary"},
		map[string]string{stalled.ArtifactHash: "shadow"})
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int { return runLearningCLI([]string{"list"}) })
	if code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.Contains(stdout, trimHash(stalled.ArtifactHash)) || !strings.Contains(stdout, trimHash(free.ArtifactHash)) {
		t.Errorf("list stdout = %q, want both candidates", stdout)
	}
	if !strings.Contains(stdout, "STALLED") {
		t.Errorf("list stdout = %q, want the STALLED marker", stdout)
	}

	// --stalled: only the advisory row, with its reason named.
	stdout, _, code = captureCLI(t, func() int { return runLearningCLI([]string{"list", "--stalled"}) })
	if code != 0 {
		t.Fatalf("list --stalled: exit %d", code)
	}
	if !strings.Contains(stdout, trimHash(stalled.ArtifactHash)) || strings.Contains(stdout, trimHash(free.ArtifactHash)) {
		t.Errorf("--stalled stdout = %q, want only the stalled hash", stdout)
	}
	if !strings.Contains(stdout, "aged past its floor") {
		t.Errorf("--stalled stdout = %q, want the advisory reason", stdout)
	}

	// --json carries the two sections; stages untouched (advisory-only).
	stdout, _, code = captureCLI(t, func() int { return runLearningCLI([]string{"list", "--stalled", "--json"}) })
	if code != 0 {
		t.Fatalf("list --stalled --json: exit %d", code)
	}
	var payload struct {
		Candidates []ipc.LearningCandidateRow `json:"candidates"`
		Stalls     []ipc.LearningStallRow     `json:"stalls"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("--json parse: %v (stdout %q)", err, stdout)
	}
	if len(payload.Candidates) != 1 || payload.Candidates[0].ArtifactHash != stalled.ArtifactHash {
		t.Errorf("json candidates = %+v, want only the stalled one", payload.Candidates)
	}
	if len(payload.Stalls) != 1 || payload.Stalls[0].Stage != "shadow" {
		t.Errorf("json stalls = %+v, want the shadow stall row", payload.Stalls)
	}
	out2, _, _ := captureCLI(t, func() int { return runLearningCLI([]string{"list"}) })
	if !strings.Contains(out2, "shadow") || !strings.Contains(out2, "canary") {
		t.Errorf("list after --stalled = %q, stages must be untouched (advisory-only)", out2)
	}
}
