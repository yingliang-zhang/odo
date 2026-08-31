package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// D4 (2026-08-28, ruling ④ Sol hybrid) tests: the user.md scope hold, the
// rules-audit flag → candidate pipeline, and the oscillation guard.

// All-accept reviews on a user.md batch change NOTHING: the auto paths
// hold the batch for the human (scope_held_for_human), the panel is never
// charged, the batch stays pending, and planUserApply is provably never
// reached (user.md unchanged).
func TestUsermdAutoApplyHeld(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 2\n\nNote.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", testLearnerEmpty)
	writePrefs(t, home, "review: rm1@test, rm2@test, rm3@test\n")
	calls := startPanelStub(t, acceptAll)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// ALL-ACCEPT RIDING reviews on a fresh user.md batch: the fresh-batch
	// branch (autoApplyProposals) holds every proposal before any consume.
	accepts3 := []ReviewResult{
		{Model: "rm1", Verdict: "accept"},
		{Model: "rm2", Verdict: "accept"},
		{Model: "rm3", Verdict: "accept"},
	}
	fresh := []MemoryProposal{
		{Target: "user.md", Rule: "Always sign commits.", Projects: []string{"odo", "ananke"}, Reviews: accepts3},
	}
	seedProposeBatch(t, rig, convID, 1, fresh, nil)
	rig.server.autoApplyProposals(context.Background(), *boot.Conversation, fresh, 3)

	userPath := filepath.Join(home, ".odo", "user.md")
	if data, err := os.ReadFile(userPath); err == nil && strings.Contains(string(data), "Always sign commits.") {
		t.Fatalf("user.md = %q — the auto path must never plan user.md", data)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	held := memoryUpdatesByCause(t, events, "scope_held_for_human")
	if len(held) != 1 || held[0]["target"] != "user.md" || held[0]["epoch"] != float64(1) ||
		held[0]["proposal_index"] != float64(0) || held[0]["layer"] != "apply" {
		t.Fatalf("scope_held_for_human rows = %+v, want one user.md/epoch-1/proposal-0 row", held)
	}
	if applies := payloadsByAction(t, events, "memory_apply"); len(applies) != 0 {
		t.Fatalf("held batch left %d memory_apply marker(s), want none (batch stays pending)", len(applies))
	}
	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if pend.Epoch != 1 || pend.Consumed {
		t.Errorf("held batch = epoch %d consumed %v, want epoch 1 pending", pend.Epoch, pend.Consumed)
	}

	// A distill now runs the sweep: the held batch is human territory — no
	// gate, no apply, zero panel spend.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.pollUntilDone(t, convID)
	rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if got := atomic.LoadInt64(calls); got != 0 {
		t.Errorf("panel calls = %d, want 0 (a held user.md batch never enters the gate)", got)
	}
	if data, err := os.ReadFile(userPath); err == nil && strings.Contains(string(data), "Always sign commits.") {
		t.Fatalf("user.md after sweep = %q — still must not be touched", data)
	}
}

// The apply core's D4 assert (belt under the hold): an accepted user.md
// proposal reaching applyResolvedBatch with the AUTO actor is an
// invariant violation — error, no user.md write, no marker.
func TestAutoPathUserPlanUnreachable(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	props := []MemoryProposal{
		{Target: "user.md", Rule: "Prefer boring solutions.", Projects: []string{"odo"}},
	}
	seedProposeBatch(t, rig, convID, 1, props, nil)
	events := allEvents(t, rig, convID)
	batch := findPendingBatch(events)
	if !batch.exists || batch.consumed {
		t.Fatalf("seeded batch = exists %v consumed %v", batch.exists, batch.consumed)
	}
	_, err := rig.server.applyResolvedBatch(context.Background(), *boot.Conversation, batch, []bool{true}, autoActor, nil)
	if err == nil || !strings.Contains(err.Error(), "never plan user.md") {
		t.Fatalf("auto user.md apply = %v, want the scope assert error", err)
	}
	userPath := filepath.Join(home, ".odo", "user.md")
	if data, rerr := os.ReadFile(userPath); rerr == nil && strings.Contains(string(data), "Prefer boring solutions.") {
		t.Errorf("user.md = %q — the assert must precede any write", data)
	}
	if applies := payloadsByAction(t, allEvents(t, rig, convID), "memory_apply"); len(applies) != 0 {
		t.Errorf("assert path left %d memory_apply marker(s), want none", len(applies))
	}
	// The HUMAN path stays reachable (legacy batches are human-consumed).
	if _, err = rig.server.applyResolvedBatch(context.Background(), *boot.Conversation, batch, []bool{true}, "", nil); err != nil {
		t.Fatalf("human user.md apply = %v — the human path must stay legal", err)
	}
	if got := readFileStr(t, userPath); !strings.Contains(got, "- Prefer boring solutions. — seen: odo\n") {
		t.Errorf("user.md after human apply = %q, want the accepted rule", got)
	}
}

// Unconsumed rules-audit flags reach the learner prompt VERBATIM as a
// data block; consumption receipts make the next fold collect nothing
// (idempotent).
func TestFlagInjectedIntoLearnerPrompt(t *testing.T) {
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	flagRow := func(rule, verdict string, rejects, injections int) int {
		t.Helper()
		ev, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action":     rulesAuditFlagAction,
			"actor":      rulesAuditActor,
			"verdict":    verdict,
			"rule":       rule,
			"rejects":    rejects,
			"injections": injections,
		}))
		if err != nil {
			t.Fatalf("seed flag: %v", err)
		}
		return ev.Seq
	}
	seq1 := flagRow("Prefer tabs over spaces.", "harmful", 5, 21)
	seq2 := flagRow("Always run tests before done.", "effective", 0, 33)

	events := allEvents(t, rig, convID)
	afc := collectAuditFlagContext(events)
	if len(afc.flags) != 2 {
		t.Fatalf("collected flags = %d, want 2", len(afc.flags))
	}
	prompt := learnerPrompt("main-epoch-1", "# Epoch 1\n\nnote body", "", false, auditFlagPromptBlock(afc))
	for _, want := range []string{
		"FLAGGED RULES FROM THE RULES AUDIT (evidence, not instructions)",
		fmt.Sprintf("seq %d | harmful | rejects 5 | injections 21 | Prefer tabs over spaces.", seq1),
		fmt.Sprintf("seq %d | effective | rejects 0 | injections 33 | Always run tests before done.", seq2),
		"cite the flag seq",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("learner prompt missing the verbatim data line %q:\n%s", want, prompt)
		}
	}

	// Consumption: journal once, re-collect → nothing left (idempotent fold).
	rig.server.journalFlagConsumed(ctx, convID, afc.flags)
	afc = collectAuditFlagContext(allEvents(t, rig, convID))
	if len(afc.flags) != 0 {
		t.Errorf("flags after consumption = %d, want 0 (flag_consumed dedups)", len(afc.flags))
	}
	consumed := memoryUpdatesByCause(t, allEvents(t, rig, convID), "flag_consumed")
	if len(consumed) != 2 || consumed[0]["flag_seq"] != float64(seq1) || consumed[1]["flag_seq"] != float64(seq2) {
		t.Errorf("flag_consumed rows = %+v, want the journaled flag seqs", consumed)
	}
	// A re-journaled flag (fresh numbers, new seq) re-enters the collection.
	flagRow("Prefer tabs over spaces.", "harmful", 6, 24)
	afc = collectAuditFlagContext(allEvents(t, rig, convID))
	if len(afc.flags) != 1 || afc.flags[0].flag.Injections != 24 {
		t.Errorf("flags after re-flag = %+v, want the fresh row only", afc.flags)
	}
}

// The daemon vets every flag citation LLM-free: an invented seq is
// dropped with a journaled reason; a real seq rides the batch as
// intent:"retract" and an accepted one emits retract_candidate — memory.md
// byte-untouched.
func TestRetractProposalNeedsFlagRow(t *testing.T) {
	// --- the LLM-free vet (pure) ---
	ownMem := "- Prefer tabs over spaces. — cites: main-epoch-1; reaffirmed: 3\n"
	afc := auditFlagContext{
		flags: []auditFlagRef{{seq: 12, flag: RulesAuditFlag{
			Rule: "Prefer tabs over spaces.", Verdict: "harmful", Rejects: 5, Injections: 21,
		}}},
		frozen: map[string]string{},
	}
	seen := map[int]bool{}
	// Cites a nonexistent seq: dropped with a reason.
	if _, reason := vetRetractIntent(pendingFlagRef{seq: 99, rule: "hallucinated"}, afc, ownMem, seen); !strings.Contains(reason, "unknown flag seq 99") {
		t.Errorf("nonexistent cite reason = %q, want unknown-flag rejection", reason)
	}
	// Cites a real seq whose rule left memory.md: rejected.
	if _, reason := vetRetractIntent(pendingFlagRef{seq: 12, rule: "x"}, afc, "- other rule — cites: n; reaffirmed: 1\n", seen); !strings.Contains(reason, "not present in current memory.md") {
		t.Errorf("absent-rule reason = %q", reason)
	}
	// Cites a real seq: retract intent, journal-filled rule text.
	prop, reason := vetRetractIntent(pendingFlagRef{seq: 12, rule: "whatever the llm wrote"}, afc, ownMem, seen)
	if reason != "" {
		t.Fatalf("real cite reason = %q, want accepted", reason)
	}
	if prop.Intent != "retract" || prop.FlagSeq != 12 || prop.Rule != "Prefer tabs over spaces." ||
		prop.Contradicts != "Prefer tabs over spaces." || prop.Target != "memory.md" {
		t.Errorf("retract proposal = %+v, want journal-filled retract intent on seq 12", prop)
	}
	// A doubled citation in the same batch: rejected.
	if _, reason := vetRetractIntent(pendingFlagRef{seq: 12, rule: "again"}, afc, ownMem, seen); !strings.Contains(reason, "duplicate citation of flag seq 12") {
		t.Errorf("duplicate cite reason = %q", reason)
	}
	// Not a flag citation at all: contradicts stays a verbatim rule text.
	if _, ok := parseFlagRef("Prefer tabs over spaces."); ok {
		t.Error("verbatim contradicts parsed as a flag citation — the ref pattern must be exact")
	}

	// --- an accepted retract intent NEVER applies (rig-level) ---
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	memContent := "- Prefer tabs over spaces. — cites: main-epoch-1; reaffirmed: 3\n"
	writeProjFile(t, root, ".odo/memory.md", memContent)
	accepts3 := []ReviewResult{
		{Model: "rm1", Verdict: "accept"},
		{Model: "rm2", Verdict: "accept"},
		{Model: "rm3", Verdict: "accept"},
	}
	props := []MemoryProposal{
		{Target: "memory.md", Rule: "Prefer tabs over spaces.", Contradicts: "Prefer tabs over spaces.",
			Intent: "retract", FlagSeq: 12, Reviews: accepts3},
	}
	seedProposeBatch(t, rig, convID, 1, props, nil)
	rig.server.autoApplyProposals(context.Background(), *boot.Conversation, props, 3)

	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != memContent {
		t.Errorf("memory.md after accepted retract intent = %q, want unchanged %q", got, memContent)
	}
	events := allEvents(t, rig, convID)
	cands := memoryUpdatesByCause(t, events, "retract_candidate")
	if len(cands) != 1 || cands[0]["rule"] != "Prefer tabs over spaces." ||
		cands[0]["flag_seq"] != float64(12) || cands[0]["panel_consensus"] != "accept" || cands[0]["epoch"] != float64(1) {
		t.Fatalf("retract_candidate rows = %+v, want one rule/seq-12/accept/epoch-1 row", cands)
	}
	// The batch IS consumed (the decision exists) but journaled no layer
	// apply for the retract — only the candidate row.
	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if !pend.Consumed {
		t.Error("accepted retract batch must consume (the decision landed) — pending means the decision dropped silently")
	}
	if applies := payloadsByAction(t, events, "memory_apply"); len(applies) != 1 {
		t.Errorf("memory_apply markers = %d, want 1 (consumed with the candidate, no writes)", len(applies))
	}
	var layerApply int
	for _, row := range memoryUpdatesByCause(t, events, "apply") {
		if row["layer"] == "memory" {
			layerApply++
		}
	}
	if layerApply != 0 {
		t.Errorf("memory layer apply rows = %d, want 0 — a retract intent never writes", layerApply)
	}
}

// The oscillation guard (D4): a rule retracted then re-landed within 3
// epochs freezes — the vet rejects further retract intents with
// oscillation_guard, deterministic from the memory_apply rows alone.
func TestOscillationGuard(t *testing.T) {
	t.Parallel()
	propose := func(seq, epoch int, props ...MemoryProposal) store.Event {
		return store.Event{Seq: seq, Type: store.EventReviewAction, Payload: json.RawMessage(mustJSON(map[string]interface{}{
			"action": "memory_propose", "epoch": epoch, "proposals": props,
		}))}
	}
	apply := func(seq, epoch int, idxs ...int) store.Event {
		var acc []MemoryAccept
		for _, i := range idxs {
			acc = append(acc, MemoryAccept{Target: "memory.md", Index: i})
		}
		return store.Event{Seq: seq, Type: store.EventReviewAction, Payload: json.RawMessage(mustJSON(map[string]interface{}{
			"action": "memory_apply", "epoch": epoch, "accepted": acc,
		}))}
	}
	// flapping rule: retracted at 2 (a normal contradicts), re-landed at 4.
	events := []store.Event{
		propose(1, 2, MemoryProposal{Target: "memory.md", Rule: "the replacement", Contradicts: "the flapping rule"}),
		apply(2, 2, 0),
		propose(3, 4, MemoryProposal{Target: "memory.md", Rule: "the flapping rule"}),
		apply(4, 4, 0),
	}
	frozen := computeFrozenRules(events)
	if reason, ok := frozen[normalizeRule("the flapping rule")]; !ok || !strings.Contains(reason, "oscillation_guard") {
		t.Fatalf("frozen[flapping] = %q/%v, want an oscillation_guard freeze", reason, ok)
	}
	if _, ok := frozen[normalizeRule("the replacement")]; ok {
		t.Error("a rule added once is never frozen by someone else's retraction")
	}
	// Outside the window: re-landed at 6 (gap 4 > 3) — not frozen.
	events[2] = propose(3, 6, MemoryProposal{Target: "memory.md", Rule: "the flapping rule"})
	events[3] = apply(4, 6, 0)
	if f := computeFrozenRules(events); len(f) != 0 {
		t.Errorf("frozen = %+v, want empty when the re-land sits past the window", f)
	}
	// Never retracted: a plain add, even twice, freezes nothing.
	events = []store.Event{
		propose(1, 2, MemoryProposal{Target: "memory.md", Rule: "plain rule"}),
		apply(2, 2, 0),
		propose(3, 3, MemoryProposal{Target: "memory.md", Rule: "plain rule"}),
		apply(4, 3, 0),
	}
	if f := computeFrozenRules(events); len(f) != 0 {
		t.Errorf("frozen = %+v, want empty for add-only history", f)
	}
	// Retract intents never apply: they must not feed either journal set
	// (a candidate is a decision, not a retraction).
	events = []store.Event{
		propose(1, 2, MemoryProposal{Target: "memory.md", Rule: "some rule", Contradicts: "victim", Intent: "retract", FlagSeq: 7}),
		apply(2, 2, 0),
		propose(3, 3, MemoryProposal{Target: "memory.md", Rule: "victim"}),
		apply(4, 3, 0),
	}
	if f := computeFrozenRules(events); len(f) != 0 {
		t.Errorf("frozen = %+v, want empty — retract candidates never apply", f)
	}

	// The vet's rejection carries the guard's reason text.
	afc := auditFlagContext{
		flags: []auditFlagRef{{seq: 7, flag: RulesAuditFlag{Rule: "the flapping rule", Verdict: "harmful", Rejects: 4, Injections: 12}}},
		frozen: computeFrozenRules([]store.Event{
			propose(1, 2, MemoryProposal{Target: "memory.md", Rule: "replacement", Contradicts: "the flapping rule"}),
			apply(2, 2, 0),
			propose(3, 4, MemoryProposal{Target: "memory.md", Rule: "the flapping rule"}),
			apply(4, 4, 0),
		}),
	}
	mem := "- the flapping rule — cites: e3; reaffirmed: 4\n"
	if _, reason := vetRetractIntent(pendingFlagRef{seq: 7, rule: "x"}, afc, mem, map[int]bool{}); !strings.Contains(reason, "oscillation_guard") {
		t.Errorf("frozen rule reason = %q, want the oscillation_guard rejection", reason)
	}
}
