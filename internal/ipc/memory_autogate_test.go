package ipc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// Panel-gated memory apply tests. Two levels: pure decision functions, and
// end-to-end through the socket (distill with a stubbed learner one-shot +
// a stubbed MoA review gateway per settle_test's startPanelStub pattern).
//
// Every distill-side test pins HOME (prefs.md + user.md hermeticity) and
// writes `review:` prefs so the gate is armed — without review models the
// gate is inert and batches stay pending (the M4-era contract the older
// learner tests pin).

// --- decision functions ---

func TestPanelAcceptsDecision(t *testing.T) {
	accepts := []ReviewResult{
		{Model: "m1", Verdict: "accept"},
		{Model: "m2", Verdict: "accept"},
		{Model: "m3", Verdict: "accept"},
	}
	mixed := func(pos int, v string, extra ...ReviewResult) []ReviewResult {
		out := []ReviewResult{{Model: "m1", Verdict: "accept"}, {Model: "m2", Verdict: "accept"}, {Model: "m3", Verdict: "accept"}}
		out[pos].Verdict = v
		if len(extra) > 0 {
			out[pos] = extra[0]
		}
		return out
	}
	for name, tc := range map[string]struct {
		reviews []ReviewResult
		models  int
		want    bool
	}{
		"unanimous accept":       {accepts, 3, true},
		"no models":              {nil, 0, false},
		"len mismatch":           {accepts[:2], 3, false},
		"one reject fails":       {mixed(1, "reject"), 3, false},
		"needs_fixes fails":      {mixed(1, "needs_fixes"), 3, false},
		"infra leg fails closed": {mixed(1, "", ReviewResult{Model: "m2", Verdict: "accept", Infra: true}), 3, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := panelAccepts(tc.reviews, tc.models); got != tc.want {
				t.Errorf("panelAccepts = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRuleReviewPromptContent(t *testing.T) {
	p := MemoryProposal{Target: "user.md", Rule: "Always run the suite after landing."}
	prompt := ruleReviewPrompt(p, "# Epoch 3\n\nThe note.\n")
	for _, want := range []string{"user.md", "all projects", "The note.", "Always run the suite after landing.", "ACCEPT, REJECT, NEEDS_FIXES"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("rule review prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestReviewsRideOn(t *testing.T) {
	if reviewsRideOn(nil, 3) {
		t.Error("empty proposals must not read as gated")
	}
	gated := []MemoryProposal{{Target: "memory.md", Rule: "r", Reviews: make([]ReviewResult, 3)}}
	if !reviewsRideOn(gated, 3) {
		t.Error("complete fan-out should read as gated")
	}
	if reviewsRideOn(gated, 2) {
		t.Error("fan-out sized for 3 must not satisfy a 2-model panel")
	}
	mixed := append(gated, MemoryProposal{Target: "memory.md", Rule: "r2"})
	if reviewsRideOn(mixed, 3) {
		t.Error("one unreviewed proposal poisons the batch")
	}
}

func TestAutoApplyRefused(t *testing.T) {
	mk := func(seq int, cause string, epoch int) store.Event {
		return store.Event{Seq: seq, Type: store.EventMemoryUpdate, Payload: json.RawMessage(mustJSON(map[string]interface{}{
			"layer": "apply", "cause": cause, "epoch": epoch,
		}))}
	}
	events := []store.Event{mk(1, "auto_apply_failed", 1)}
	if !autoApplyRefused(events, 1) {
		t.Error("refused marker for epoch 1 must be honored")
	}
	if autoApplyRefused(events, 2) {
		t.Error("epoch 1 marker must not suppress epoch 2")
	}
	if autoApplyRefused(nil, 1) {
		t.Error("empty journal is never refused")
	}
}

// --- fold ownership (server-side owned set) ---

// The panel-gated pipeline journals apply-side rows between render and
// marker (the sweep) and right after it (the auto-apply) — both classes
// must read as fold-owned/attributed so an auto-triggered fold never
// self-aborts on its own memory bookkeeping, while real conversation
// growth still aborts.
func TestUnownedFoldGrowthMemoryPipelineRows(t *testing.T) {
	owned := []store.Event{
		{Seq: 1, Type: store.EventReviewAction, Payload: json.RawMessage(mustJSON(map[string]interface{}{"action": "memory_apply"}))},
		{Seq: 2, Type: store.EventReviewAction, Payload: json.RawMessage(mustJSON(map[string]interface{}{"action": "memory_gate"}))},
		{Seq: 3, Type: store.EventMemoryUpdate, Payload: json.RawMessage(mustJSON(map[string]interface{}{"layer": "memory", "cause": "apply"}))},
		{Seq: 4, Type: store.EventMemoryUpdate, Payload: json.RawMessage(mustJSON(map[string]interface{}{"layer": "user", "cause": "apply"}))},
		{Seq: 5, Type: store.EventMemoryUpdate, Payload: json.RawMessage(mustJSON(map[string]interface{}{"layer": "skills", "cause": "applied"}))},
		{Seq: 6, Type: store.EventMemoryUpdate, Payload: json.RawMessage(mustJSON(map[string]interface{}{"layer": "apply", "cause": "auto_apply_failed"}))},
		{Seq: 7, Type: store.EventMemoryUpdate, Payload: json.RawMessage(mustJSON(map[string]interface{}{"layer": "wiki", "cause": "commit"}))},
	}
	if unownedFoldGrowth(owned, 0) {
		t.Error("apply-side memory pipeline rows must not trip the supersession probe")
	}
	human := append(append([]store.Event{}, owned...),
		store.Event{Seq: 8, Type: store.EventUserMessage, Payload: json.RawMessage(mustJSON(map[string]interface{}{"text": "hold on"}))})
	if !unownedFoldGrowth(human, 0) {
		t.Error("a user send mid-fold is still unattributed growth")
	}
	diffAccept := append(append([]store.Event{}, owned...),
		store.Event{Seq: 8, Type: store.EventReviewAction, Payload: json.RawMessage(mustJSON(map[string]interface{}{"action": "accept"}))})
	if !unownedFoldGrowth(diffAccept, 0) {
		t.Error("a diff accept mid-fold is still unattributed growth")
	}
}

// --- end-to-end through distill ---

const (
	testLearnerOneRule  = `{"memory":[{"rule":"Run the full ipc suite after every landing.","evidence":"main-epoch-1","contradicts":""}],"procedures":[],"reaffirm":[]}`
	testLearnerEmpty    = `{"memory":[],"procedures":[],"reaffirm":[]}`
	testLearnerOneSkill = `{"memory":[],"procedures":[{"name":"run-go-tests","description":"Use when verifying Go work.","keywords":["test"],"body":"# Run Go Tests\n\n1. go test ./..."}],"reaffirm":[]}`
)

// armedDistillRig wires the shared fixture: repo + HOME + OMP wrapper +
// review prefs + a scripted panel gateway.
func armedDistillRig(t *testing.T, note, learnerJSON string, reply func(call int64, model string) (int, string)) (*testRig, string, string, *int64) {
	t.Helper()
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", note)
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", learnerJSON)
	// skills_distill: procedure distillation is off by default (P1-12);
	// this suite pins the panel GATE, so its learner output opts back in.
	writePrefs(t, home, "review: rm1@test, rm2@test, rm3@test\nskills_distill: on\n")
	calls := startPanelStub(t, reply)
	rig := startRig(t, root)
	return rig, root, home, calls
}

func acceptAll(int64, string) (int, string) { return 200, "ACCEPT\nLooks right." }

// A unanimous panel lands the rule and consumes the batch inside the
// distill — no human click, actor auto_panel, reviews riding the batch,
// and the wiki note auto-committed.
func TestDistillPanelAutoApplies(t *testing.T) {
	rig, root, _, _ := armedDistillRig(t, "# Epoch 1\n\nDecided to always run the suite after landing.\n", testLearnerOneRule, acceptAll)
	defer rig.stop(t)

	convID, d := runToDistill(t, rig, root)
	if d.MemoryProposals != 1 {
		t.Fatalf("distill MemoryProposals = %d, want 1", d.MemoryProposals)
	}

	mem := readFileStr(t, filepath.Join(root, ".odo", "memory.md"))
	if !strings.Contains(mem, "Run the full ipc suite after every landing.") {
		t.Errorf("memory.md missing panel-accepted rule:\n%s", mem)
	}

	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if !pend.Consumed || pend.ApplyActor != autoActor {
		t.Errorf("batch outcome = consumed %v actor %q, want consumed auto_panel", pend.Consumed, pend.ApplyActor)
	}
	if len(pend.Accepted) != 1 || pend.Accepted[0].Target != "memory.md" || pend.Accepted[0].Index != 0 {
		t.Errorf("accepted refs = %+v, want one memory.md@0", pend.Accepted)
	}
	if len(pend.Proposals) != 1 || len(pend.Proposals[0].Reviews) != 3 {
		t.Fatalf("batch proposals reviews = %+v, want 1 proposal with 3 riding reviews", pend.Proposals)
	}
	for _, r := range pend.Proposals[0].Reviews {
		if r.Verdict != "accept" {
			t.Errorf("riding verdict = %q, want accept", r.Verdict)
		}
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	applies := payloadsByAction(t, events, "memory_apply")
	if len(applies) != 1 || applies[0]["actor"] != autoActor {
		t.Fatalf("memory_apply rows = %+v, want one auto_panel row", applies)
	}
	if metrics, _ := applies[0]["metrics"].(map[string]interface{}); metrics["accepted"] != float64(1) || metrics["rejected"] != float64(0) {
		t.Errorf("apply metrics = %v, want 1/0", metrics)
	}

	// Wiki durability is the pipeline's own job now: one commit row, one
	// real commit covering the note.
	if commits := memoryUpdatesByCause(t, events, "commit"); len(commits) != 1 || commits[0]["layer"] != "wiki" {
		t.Errorf("wiki commit rows = %+v, want one layer:wiki row", commits)
	}
	if log := gitOut(t, root, "log", "--oneline"); !strings.Contains(log, "docs(wiki): distill main/epoch 1") {
		t.Errorf("git log missing the wiki auto-commit:\n%s", log)
	}
}

// A contested panel fails closed: the rule does NOT land, the batch is
// still consumed (the decision exists), and the journal shows why.
func TestDistillPanelRejectsMixed(t *testing.T) {
	var n int64
	rig, root, _, _ := armedDistillRig(t, "# Epoch 1\n\nNote.\n", testLearnerOneRule, func(int64, string) (int, string) {
		if atomic.AddInt64(&n, 1) <= 2 {
			return 200, "ACCEPT\nfine"
		}
		return 200, "REJECT\nrestatement of the obvious"
	})
	defer rig.stop(t)

	convID, _ := runToDistill(t, rig, root)

	// A rejected-all apply writes nothing — memory.md may not even exist.
	memData, _ := os.ReadFile(filepath.Join(root, ".odo", "memory.md"))
	if strings.Contains(string(memData), "Run the full ipc suite after every landing.") {
		t.Errorf("contested rule must not land:\n%s", memData)
	}
	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if !pend.Consumed || len(pend.Rejected) != 1 || len(pend.Accepted) != 0 {
		t.Errorf("batch outcome = consumed %v, %d accepted / %d rejected; want consumed 0/1",
			pend.Consumed, len(pend.Accepted), len(pend.Rejected))
	}
}

// An all-reject skill keeps the M9 pre-batch discard: skill_gate row, no
// batch, no apply, no file.
func TestDistillPanelDiscardsAllRejectSkill(t *testing.T) {
	rig, root, _, calls := armedDistillRig(t, "# Epoch 1\n\nNote.\n", testLearnerOneSkill,
		func(int64, string) (int, string) { return 200, "REJECT\nvague procedure" })
	defer rig.stop(t)

	convID, d := runToDistill(t, rig, root)
	if d.MemoryProposals != 0 {
		t.Errorf("MemoryProposals = %d, want 0 (auto_discard never reaches the batch)", d.MemoryProposals)
	}
	if got := atomic.LoadInt64(calls); got != 3 {
		t.Errorf("panel calls = %d, want 3 (one fan-out)", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".odo", "skills", "run-go-tests.md")); !os.IsNotExist(err) {
		t.Error("discarded skill must not be written")
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	gates := payloadsByAction(t, events, "skill_gate")
	if len(gates) != 1 || gates[0]["tier"] != "auto_discard" {
		t.Errorf("skill_gate rows = %+v, want one auto_discard", gates)
	}
	if applies := payloadsByAction(t, events, "memory_apply"); len(applies) != 0 {
		t.Errorf("memory_apply rows = %d, want 0 (nothing survived the gate)", len(applies))
	}
	if pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID}); pend.Epoch != 0 {
		t.Errorf("memory_proposals = epoch %d, want 0 (no batch)", pend.Epoch)
	}
}

// A unanimous skill lands via the auto-apply like any other target.
func TestDistillPanelAutoAppliesSkill(t *testing.T) {
	rig, root, _, _ := armedDistillRig(t, "# Epoch 1\n\nWe verified Go work by running the suite.\n", testLearnerOneSkill, acceptAll)
	defer rig.stop(t)

	convID, _ := runToDistill(t, rig, root)

	skillPath := filepath.Join(root, ".odo", "skills", "run-go-tests.md")
	content := readFileStr(t, skillPath)
	if !strings.Contains(content, "name: run-go-tests") {
		t.Errorf("skill file missing composed frontmatter:\n%s", content)
	}
	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if !pend.Consumed || len(pend.Accepted) != 1 || pend.Accepted[0].Target != "skills" {
		t.Errorf("batch outcome = consumed %v accepted %+v, want consumed skills@0", pend.Consumed, pend.Accepted)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	applied := 0
	for _, mu := range memoryUpdatesByCause(t, events, "applied") {
		if mu["layer"] == "skills" {
			applied++
		}
	}
	if applied != 1 {
		t.Errorf("memory_update(skills/applied) = %d, want 1", applied)
	}
}

// An ungated batch from before the panel path (or left pending while the
// panel was unarmed) is swept at the next distill: fresh gate, verdict
// receipt (memory_gate), auto apply — nothing drops silently under
// supersession anymore.
func TestDistillSweepsLegacyBatch(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\nFirst note.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", testLearnerOneRule)
	// No review prefs yet: batch one journals ungated and stays pending.
	rig := startRig(t, root)
	defer rig.stop(t)

	convID, _ := runToDistill(t, rig, root)
	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if pend.Consumed || len(pend.Proposals) != 1 || len(pend.Proposals[0].Reviews) != 0 {
		t.Fatalf("pre-arm batch = consumed %v reviews %d, want pending ungated", pend.Consumed, len(pend.Proposals[0].Reviews))
	}

	// Arm the panel; the second distill sweeps batch one and decides its own.
	writePrefs(t, home, "review: rm1@test, rm2@test, rm3@test\n")
	calls := startPanelStub(t, acceptAll)
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 2\n\nSecond note.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[{"rule":"Rebase the later diff before reviewing it.","evidence":"main-epoch-2","contradicts":""}],"procedures":[],"reaffirm":[]}`)
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt again"})
	rig.pollUntilDone(t, convID)
	rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})

	if got := atomic.LoadInt64(calls); got != 6 {
		t.Errorf("panel calls = %d, want 6 (sweep 3 + new batch 3)", got)
	}
	mem := readFileStr(t, filepath.Join(root, ".odo", "memory.md"))
	for _, rule := range []string{"Run the full ipc suite after every landing.", "Rebase the later diff before reviewing it."} {
		if !strings.Contains(mem, rule) {
			t.Errorf("memory.md missing swept/auto-applied rule %q:\n%s", rule, mem)
		}
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	gates := payloadsByAction(t, events, "memory_gate")
	if len(gates) != 1 || gates[0]["epoch"] != float64(1) {
		t.Errorf("memory_gate receipts = %+v, want one epoch-1 row", gates)
	}
	applies := payloadsByAction(t, events, "memory_apply")
	if len(applies) != 2 {
		t.Fatalf("memory_apply rows = %d, want 2 (swept + fresh)", len(applies))
	}
	if applies[0]["epoch"] != float64(1) || applies[1]["epoch"] != float64(2) {
		t.Errorf("apply epochs = %v, %v, want 1, 2", applies[0]["epoch"], applies[1]["epoch"])
	}
	for _, a := range applies {
		if a["actor"] != autoActor {
			t.Errorf("apply actor = %v, want auto_panel on both", a["actor"])
		}
	}
	pend = rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if !pend.Consumed || pend.Epoch != 2 || pend.ApplyActor != autoActor {
		t.Errorf("final batch = epoch %d consumed %v actor %q, want epoch 2 consumed auto_panel", pend.Epoch, pend.Consumed, pend.ApplyActor)
	}
}

// A refused auto-apply (user.md overflow) leaves the batch pending, marks
// the epoch, and the next distill's sweep skips it instead of re-charging
// the panel for a decision the files still can't take.
func TestDistillSweepSkipsRefusedBatch(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\nNote.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", testLearnerEmpty)
	writePrefs(t, home, "review: rm1@test, rm2@test, rm3@test\n")
	calls := startPanelStub(t, acceptAll)
	if err := os.MkdirAll(filepath.Join(home, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(home, ".odo", "user.md")
	if err := os.WriteFile(userPath, []byte("- Short durable line.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	oversized := strings.Repeat("r", 4100)
	seedProposeBatch(t, rig, convID, 1, []MemoryProposal{
		{Target: "user.md", Rule: oversized, Projects: []string{"odo", "ananke"}},
	}, nil)

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.pollUntilDone(t, convID)
	rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})

	if got := atomic.LoadInt64(calls); got != 3 {
		t.Fatalf("panel calls after distill 1 = %d, want 3 (the sweep's fan-out)", got)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	refused := memoryUpdatesByCause(t, events, "auto_apply_failed")
	if len(refused) != 1 || refused[0]["epoch"] != float64(1) {
		t.Fatalf("auto_apply_failed rows = %+v, want one epoch-1 row", refused)
	}
	if applies := payloadsByAction(t, events, "memory_apply"); len(applies) != 0 {
		t.Fatalf("refused apply must leave no memory_apply marker, got %d", len(applies))
	}
	if got := readFileStr(t, userPath); got != "- Short durable line.\n" {
		t.Errorf("user.md after refusal = %q, want unchanged", got)
	}
	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if pend.Epoch != 1 || pend.Consumed {
		t.Errorf("batch after refusal = epoch %d consumed %v, want epoch 1 pending", pend.Epoch, pend.Consumed)
	}

	// Second distill: the refused marker holds the sweep off — zero new
	// panel spend, batch left for human salvage.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt again"})
	rig.pollUntilDone(t, convID)
	rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if got := atomic.LoadInt64(calls); got != 3 {
		t.Errorf("panel calls after distill 2 = %d, want still 3 (refused batch never re-gated)", got)
	}
	if pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID}); pend.Epoch != 0 {
		t.Errorf("batch visibility after supersession = epoch %d, want 0", pend.Epoch)
	}
}

// The distilled decode of a memory_gate row keeps per-proposal reviews
// aligned (the receipt's only consumer is an auditor reading the journal;
// catch a shape regression here).
func TestMemoryGateReceiptShape(t *testing.T) {
	reviews := [][]ReviewResult{
		{{Model: "m1", Verdict: "accept"}, {Model: "m2", Verdict: "reject"}},
		{{Model: "m1", Verdict: "accept"}, {Model: "m2", Verdict: "accept"}},
	}
	payload := mustJSON(map[string]interface{}{"action": "memory_gate", "epoch": 4, "batch_seq": 9, "reviews": reviews})
	var decoded struct {
		Reviews [][]ReviewResult `json:"reviews"`
	}
	if err := json.Unmarshal(json.RawMessage(payload), &decoded); err != nil {
		t.Fatalf("memory_gate payload: %v", err)
	}
	if len(decoded.Reviews) != 2 || decoded.Reviews[0][1].Verdict != "reject" {
		t.Errorf("decoded reviews = %+v, want aligned per-proposal legs", decoded.Reviews)
	}
}
