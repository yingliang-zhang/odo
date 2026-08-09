package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// M4 Learning tests. Two levels:
//   - end-to-end through the socket: send -> distill (real learner one-shot
//     via a stub wrapper that returns the JSON contract) -> memory_proposals
//     -> apply_memory;
//   - batch-seeded IPC: the propose batch is appended to the journal
//     directly (same shape runLearner uses) so the apply path is exercised
//     without re-running the distill machinery.
//
// Every test pins HOME to a temp dir (user.md/injection hermeticity); rigs
// without a sibling need leave ODO_REGISTRY_PATH to startRig's temp default.

// learnerFlowWrapper serves every prompt the daemon builds: the plain agent
// run (hello.txt), the distill one-shot (note body from ODO_DISTILL_OUTPUT),
// and the M4 learner one-shot (JSON contract from ODO_LEARNER_OUTPUT). The
// message branch keeps the 1s delay so the first poll still sees the run.
const learnerFlowWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
if grep -q "memory learner pass" "$prompt_file"; then
  cat "$ODO_LEARNER_OUTPUT" > "$output_file"
  exit 0
fi
if grep -q "Summarize the key decisions" "$prompt_file"; then
  cat "$ODO_DISTILL_OUTPUT" > "$output_file"
  exit 0
fi
sleep 1
cp "$prompt_file" hello.txt
printf 'Created hello.txt as requested.\n' > "$output_file"
exit 0
`

// setOneShotEnv pins a one-shot output (distill note / learner JSON) to a
// file and points the wrapper's env seam at it.
func setOneShotEnv(t *testing.T, envKey, content string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "oneshot.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envKey, p)
}

// seedProposeBatch journals a memory_propose review_action in runLearner's
// exact shape, plus the distill marker that makes findPendingBatch resolve
// the batch (pending epoch = latest distill newEpoch − 1).
func seedProposeBatch(t *testing.T, rig *testRig, convID int64, epoch int, proposals []MemoryProposal, reaffirm []string) {
	t.Helper()
	ctx := context.Background()
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":    "memory_propose",
		"epoch":     epoch,
		"proposals": proposals,
		"reaffirm":  reaffirm,
		"stats":     map[string]int{"memory_kept": len(proposals)},
	})); err != nil {
		t.Fatalf("seed propose: %v", err)
	}
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action": "distill",
		"epoch":  epoch + 1,
	})); err != nil {
		t.Fatalf("seed distill marker: %v", err)
	}
}

// payloadsByAction decodes every review_action payload of one conversation
// whose "action" field equals action, in seq order.
func payloadsByAction(t *testing.T, events []store.Event, action string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("review_action payload: %v", err)
		}
		if p["action"] == action {
			out = append(out, p)
		}
	}
	return out
}

// memoryUpdatesByCause decodes the conversation's memory_update payloads of
// one cause, in seq order.
func memoryUpdatesByCause(t *testing.T, events []store.Event, cause string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, ev := range events {
		if ev.Type != store.EventMemoryUpdate {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("memory_update payload: %v", err)
		}
		if p["cause"] == cause {
			out = append(out, p)
		}
	}
	return out
}

// receiptFromEvent extracts the journaled injection receipt map from a
// user_message event payload (nil when the key is absent).
func receiptFromEvent(t *testing.T, ev *store.Event) map[string]string {
	t.Helper()
	if ev == nil {
		t.Fatal("missing user_message event")
	}
	var p map[string]interface{}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	raw, ok := p["receipt"].(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("receipt[%s] not a string: %v", k, v)
		}
		out[k] = s
	}
	return out
}

func readFileStr(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// runToDistill drives bootstrap -> send -> done -> distill and returns the
// conversation ID along with the distill response.
func runToDistill(t *testing.T, rig *testRig, root string) (int64, Response) {
	t.Helper()
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.pollUntilDone(t, convID)
	return convID, rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
}

// TestLearnerProposesJournaled covers Demo A steps 1-2: after a real distill
// the learner one-shot's proposals ride a single review_action
// memory_propose event (per-target entries), and the distill response's
// MemoryProposals count matches the batch.
func TestLearnerProposesJournaled(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\nDecided to always test before claiming done.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[
{"rule":"Always run go test ./... before claiming a task is done.","evidence":"main-epoch-1","contradicts":""},
{"rule":"Prefer compact output over long prose.","evidence":"main-epoch-1","contradicts":""}
],"user":[],"reaffirm":[]}`)
	rig := startRig(t, root)
	defer rig.stop(t)

	convID, d := runToDistill(t, rig, root)
	if d.MemoryProposals != 2 {
		t.Errorf("distill MemoryProposals = %d, want 2", d.MemoryProposals)
	}
	if _, err := os.Stat(d.WikiPath); err != nil {
		t.Fatalf("distill wrote no note: %v", err)
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	proposes := payloadsByAction(t, events, "memory_propose")
	if len(proposes) != 1 {
		t.Fatalf("memory_propose events = %d, want 1", len(proposes))
	}
	if proposes[0]["epoch"] != float64(1) {
		t.Errorf("propose epoch = %v, want 1", proposes[0]["epoch"])
	}
	rawProps, ok := proposes[0]["proposals"].([]interface{})
	if !ok || len(rawProps) != 2 {
		t.Fatalf("proposals = %v, want 2 entries", proposes[0]["proposals"])
	}
	wantRules := []string{
		"Always run go test ./... before claiming a task is done.",
		"Prefer compact output over long prose.",
	}
	for i, raw := range rawProps {
		p, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("proposal %d not an object: %v", i, raw)
		}
		if p["target"] != "memory.md" {
			t.Errorf("proposal %d target = %v, want memory.md", i, p["target"])
		}
		if p["rule"] != wantRules[i] {
			t.Errorf("proposal %d rule = %v, want %q", i, p["rule"], wantRules[i])
		}
		if p["evidence"] != "main-epoch-1" {
			t.Errorf("proposal %d evidence = %v, want main-epoch-1", i, p["evidence"])
		}
	}
	stats, ok := proposes[0]["stats"].(map[string]interface{})
	if !ok || stats["memory_kept"] != float64(2) || stats["memory_dropped"] != float64(0) {
		t.Errorf("stats = %v, want memory_kept 2 / memory_dropped 0", proposes[0]["stats"])
	}

	// The propose event is journaled before the distill marker, and a
	// successful learner means no learner-failure memory_update.
	proposeIdx, distillIdx := -1, -1
	for i, ev := range events {
		if ev.Type == store.EventMemoryUpdate {
			t.Errorf("unexpected memory_update event at seq %d: %s", ev.Seq, ev.Payload)
		}
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p map[string]interface{}
		_ = json.Unmarshal(ev.Payload, &p)
		switch p["action"] {
		case "memory_propose":
			proposeIdx = i
		case "distill":
			distillIdx = i
		}
	}
	if proposeIdx < 0 || distillIdx < 0 || proposeIdx > distillIdx {
		t.Errorf("propose idx %d, distill idx %d — want propose before distill", proposeIdx, distillIdx)
	}

	// The review surface returns the pending batch with both proposals.
	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if pend.Epoch != 1 || len(pend.Proposals) != 2 {
		t.Fatalf("memory_proposals = epoch %d, %d proposals; want epoch 1, 2", pend.Epoch, len(pend.Proposals))
	}
	for i, p := range pend.Proposals {
		if p.Target != "memory.md" || p.Rule != wantRules[i] {
			t.Errorf("pending proposal %d = %+v", i, p)
		}
	}
}

// lastDistillMarker returns the conversation's newest distill review_action.
func lastDistillMarker(t *testing.T, rig *testRig, convID int64) store.Event {
	t.Helper()
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventReviewAction {
			continue
		}
		var p map[string]interface{}
		if json.Unmarshal(events[i].Payload, &p) == nil && p["action"] == "distill" {
			return events[i]
		}
	}
	t.Fatal("no distill marker journaled")
	return store.Event{}
}

// TestFoldWindow pins the window arithmetic: after no marker, after one,
// and the empty-window shapes (no events at all / nothing new since the
// last marker both yield lastSeq < firstSeq).
func TestFoldWindow(t *testing.T) {
	ev := func(seq int, typ, action string) store.Event {
		p := "{}"
		if action != "" {
			p = fmt.Sprintf(`{"action":%q}`, action)
		}
		return store.Event{Seq: seq, Type: typ, Payload: json.RawMessage(p)}
	}
	marker := func(seq int) store.Event { return ev(seq, store.EventReviewAction, "distill") }
	msg := func(seq int) store.Event { return ev(seq, store.EventUserMessage, "") }

	cases := []struct {
		name                string
		events              []store.Event
		wantFirst, wantLast int
	}{
		{"empty log", nil, 1, 0},
		{"no marker", []store.Event{msg(1), msg(2), msg(3)}, 1, 3},
		{"after marker", []store.Event{msg(1), marker(2), msg(3), msg(4)}, 3, 4},
		{"only marker", []store.Event{marker(5)}, 6, 5},
		{"latest marker wins", []store.Event{msg(1), marker(2), msg(3), marker(4), msg(5)}, 5, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, last := FoldWindow(tc.events)
			if first != tc.wantFirst || last != tc.wantLast {
				t.Errorf("foldWindow = (%d, %d), want (%d, %d)", first, last, tc.wantFirst, tc.wantLast)
			}
		})
	}
}

// TestDistillFoldSchema covers the epoch-fold provenance fix: the distill
// marker journals the folded window [first_seq, last_seq] and the note's
// content hash explicitly, so consumers never reverse-derive the boundary.
// The second distill's window must start right after the first marker.
func TestDistillFoldSchema(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	note1 := "# Epoch 1\n\nFirst folded epoch.\n"
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", note1)
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[],"user":[],"reaffirm":[]}`)
	rig := startRig(t, root)
	defer rig.stop(t)

	convID, d1 := runToDistill(t, rig, root)
	if d1.WikiPath == "" {
		t.Fatalf("distill #1 failed: %+v", d1)
	}

	marker1 := lastDistillMarker(t, rig, convID)
	var p1 map[string]interface{}
	if err := json.Unmarshal(marker1.Payload, &p1); err != nil {
		t.Fatalf("marker #1 payload: %v", err)
	}
	if p1["first_seq"] != float64(1) {
		t.Errorf("marker #1 first_seq = %v, want 1", p1["first_seq"])
	}
	if p1["last_seq"] != float64(marker1.Seq-1) {
		t.Errorf("marker #1 last_seq = %v, want marker seq-1 = %d", p1["last_seq"], marker1.Seq-1)
	}
	last1, ok1 := p1["last_seq"].(float64)
	if !ok1 || last1 < 1 {
		t.Errorf("marker #1 window %v..%v — want the run's events folded", p1["first_seq"], p1["last_seq"])
	}
	if p1["note_sha"] != sha16([]byte(readFileStr(t, d1.WikiPath))) {
		t.Errorf("marker #1 note_sha = %v, want sha16 of the note on disk", p1["note_sha"])
	}

	// A second run + distill: window #2 starts right after marker #1 and
	// folds everything journaled since (the run's events here).
	note2 := "# Epoch 2\n\nSecond folded epoch.\n"
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", note2)
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Update hello.txt"})
	rig.pollUntilDone(t, convID)
	d2 := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if d2.WikiPath == "" {
		t.Fatalf("distill #2 failed: %+v", d2)
	}
	marker2 := lastDistillMarker(t, rig, convID)
	var p2 map[string]interface{}
	if err := json.Unmarshal(marker2.Payload, &p2); err != nil {
		t.Fatalf("marker #2 payload: %v", err)
	}
	if p2["first_seq"] != float64(marker1.Seq+1) {
		t.Errorf("marker #2 first_seq = %v, want marker#1 seq+1 = %d", p2["first_seq"], marker1.Seq+1)
	}
	if p2["last_seq"] != float64(marker2.Seq-1) {
		t.Errorf("marker #2 last_seq = %v, want marker#2 seq-1 = %d", p2["last_seq"], marker2.Seq-1)
	}
	if p2["note_sha"] != sha16([]byte(readFileStr(t, d2.WikiPath))) {
		t.Errorf("marker #2 note_sha = %v, want sha16 of the note on disk", p2["note_sha"])
	}
}

// TestWindowEvents pins the render-side window: the note covers only events
// after the latest distill marker; no marker renders the full log (the
// first-ever distill); a trailing marker yields an empty window, which
// handleDistill rejects before running the agent.
func TestWindowEvents(t *testing.T) {
	marker := func(seq int) store.Event {
		return store.Event{Seq: seq, Type: store.EventReviewAction, Payload: json.RawMessage(`{"action":"distill"}`)}
	}
	msg := func(seq int) store.Event {
		return store.Event{Seq: seq, Type: store.EventUserMessage, Payload: json.RawMessage(`{"text":"hi"}`)}
	}
	cases := []struct {
		name     string
		events   []store.Event
		wantSeqs []int
	}{
		{"empty log", nil, nil},
		{"no marker renders full log", []store.Event{msg(1), msg(2)}, []int{1, 2}},
		{"after marker renders tail", []store.Event{msg(1), marker(2), msg(3), msg(4)}, []int{3, 4}},
		{"latest marker wins", []store.Event{msg(1), marker(2), msg(3), marker(4), msg(5)}, []int{5}},
		{"trailing marker renders nothing", []store.Event{msg(1), marker(2)}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := windowEvents(tc.events)
			if len(got) != len(tc.wantSeqs) {
				t.Fatalf("windowEvents len = %d, want %d", len(got), len(tc.wantSeqs))
			}
			for i, want := range tc.wantSeqs {
				if got[i].Seq != want {
					t.Errorf("windowEvents[%d].Seq = %d, want %d", i, got[i].Seq, want)
				}
			}
		})
	}
}

// TestCapEvents pins the budget walk: newest events survive, the boundary
// event is dropped whole (never half-rendered), and a single oversized
// event is still kept — dropping it would distill nothing.
func TestCapEvents(t *testing.T) {
	ev := func(seq, size int) store.Event {
		// type "agent_text" (10 bytes) + size payload + 64 render pad = one event's cost
		return store.Event{Seq: seq, Type: "agent_text", Payload: json.RawMessage(strings.Repeat("x", size))}
	}
	events := []store.Event{ev(1, 10), ev(2, 10), ev(3, 10)} // 84 bytes each
	tail, omitted := capEvents(events, 170)                  // room for exactly 2
	if omitted != 1 || len(tail) != 2 || tail[0].Seq != 2 {
		t.Errorf("capEvents(170) = %d omitted, tail seqs %v — want 1 omitted, seqs [2 3]", omitted, seqsOf(tail))
	}
	tail, omitted = capEvents(events, 84) // room for exactly 1
	if omitted != 2 || len(tail) != 1 || tail[0].Seq != 3 {
		t.Errorf("capEvents(84) = %d omitted, tail seqs %v — want 2 omitted, seq [3]", omitted, seqsOf(tail))
	}
	tail, omitted = capEvents([]store.Event{ev(1, 5000)}, 100) // oversized: kept anyway
	if omitted != 0 || len(tail) != 1 {
		t.Errorf("oversized single event = %d omitted, %d kept — want 0 omitted, 1 kept", omitted, len(tail))
	}
	if _, omitted = capEvents(events, 1<<20); omitted != 0 {
		t.Errorf("under-budget capEvents omitted %d, want 0", omitted)
	}
}

func seqsOf(events []store.Event) []int {
	out := make([]int, len(events))
	for i, ev := range events {
		out[i] = ev.Seq
	}
	return out
}

// TestDistillPromptOmission pins the over-budget render at the real cap:
// the prompt declares the omitted seq range and renders only the tail.
func TestDistillPromptOmission(t *testing.T) {
	ev := func(seq int, fill byte, size int) store.Event {
		return store.Event{Seq: seq, Type: "agent_text", Payload: json.RawMessage("key-" + strings.Repeat(string(fill), size))}
	}
	events := []store.Event{ev(1, 'A', distillPromptBytesCap), ev(2, 'B', 100), ev(3, 'C', 100)}
	p := distillPrompt(events)
	if !strings.Contains(p, "1 older event(s), seq 1–1, omitted") {
		t.Errorf("prompt missing omission declaration:\n%.300s", p)
	}
	if strings.Contains(p, strings.Repeat("A", 100)) {
		t.Errorf("prompt still renders the omitted oldest event")
	}
	if !strings.Contains(p, "(seq 3)") {
		t.Errorf("prompt missing the newest event")
	}
	if small := distillPrompt([]store.Event{ev(1, 'A', 10)}); strings.Contains(small, "omitted") {
		t.Errorf("under-budget prompt carries the omission header")
	}
}

// TestDistillEmptyWindow pins the nothing-new guard: an immediate second
// distill (nothing journaled since marker #1) fails with a clear error
// instead of re-running the agent over an empty transcript and writing a
// note that summarizes nothing.
func TestDistillEmptyWindow(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\nFirst folded epoch.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[],"user":[],"reaffirm":[]}`)
	rig := startRig(t, root)
	defer rig.stop(t)

	convID, d1 := runToDistill(t, rig, root)
	if d1.WikiPath == "" {
		t.Fatalf("distill #1 failed: %+v", d1)
	}

	d2 := rig.callExpectErr(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if !strings.Contains(d2.Error, "nothing journaled since the last distill") {
		t.Errorf("empty-window distill error = %q", d2.Error)
	}
	if _, err := os.Stat(filepath.Join(root, "wiki", "main-epoch-2.md")); !os.IsNotExist(err) {
		t.Errorf("empty-window distill wrote an epoch-2 note (stat err %v)", err)
	}
	// The failed distill must not move the epoch counter: a later real
	// message + distill produces epoch 2, not 3.
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 2\n\nSecond folded epoch.\n")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Update hello.txt"})
	rig.pollUntilDone(t, convID)
	d3 := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if d3.WikiPath == "" {
		t.Fatalf("distill after the empty-window rejection failed: %+v", d3)
	}
	if _, err := os.Stat(filepath.Join(root, "wiki", "main-epoch-2.md")); err != nil {
		t.Errorf("epoch-2 note missing after real distill: %v", err)
	}
}

// TestLearnerVetoesWeakEvidence covers the daemon-side evidence gate: a
// memory proposal whose evidence is not the just-written note name and a
// user proposal backed by <2 distinct registered projects are dropped and
// counted; only the kept proposal reaches the journal.
func TestLearnerVetoesWeakEvidence(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	// The note contains the user-proposal rule text, so the veto is squared
	// on the <2-projects gate (registry holds the bound project only), not on
	// missing text evidence.
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\nAlways verify conclusions with a concrete tool result.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[
{"rule":"Always run go test ./... before claiming done.","evidence":"main-epoch-1","contradicts":""},
{"rule":"Never skip the type checker.","evidence":"main-epoch-WRONG","contradicts":""}
],"user":[
{"rule":"Always verify conclusions with a concrete tool result.","projects":["odo","ananke"]}
],"reaffirm":[]}`)
	rig := startRig(t, root)
	defer rig.stop(t)

	convID, d := runToDistill(t, rig, root)
	if d.MemoryProposals != 1 {
		t.Errorf("distill MemoryProposals = %d, want 1 (only the evidenced rule)", d.MemoryProposals)
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	proposes := payloadsByAction(t, events, "memory_propose")
	if len(proposes) != 1 {
		t.Fatalf("memory_propose events = %d, want 1", len(proposes))
	}
	var pp proposePayload
	if b, err := json.Marshal(proposes[0]); err != nil || json.Unmarshal(b, &pp) != nil {
		t.Fatalf("re-decode propose payload: %v", err)
	}
	if len(pp.Proposals) != 1 {
		t.Fatalf("journaled proposals = %v, want exactly the kept one", pp.Proposals)
	}
	kept := pp.Proposals[0]
	if kept.Target != "memory.md" || kept.Rule != "Always run go test ./... before claiming done." || kept.Evidence != "main-epoch-1" {
		t.Errorf("kept proposal = %+v", kept)
	}
	stats, ok := proposes[0]["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("stats missing: %v", proposes[0])
	}
	for k, want := range map[string]int{"memory_kept": 1, "memory_dropped": 1, "user_kept": 0, "user_dropped": 1} {
		if stats[k] != float64(want) {
			t.Errorf("stats[%s] = %v, want %d (all stats %v)", k, stats[k], want, stats)
		}
	}
}

// TestApplyMemoryWritesMemoryMD covers Demo A step 4: accepting one proposal
// writes its daemon-formatted line to .odo/memory.md, the rejected proposal
// is absent, and the apply journals memory_update{layer:memory,cause:apply}
// plus the review_action memory_apply marker.
func TestApplyMemoryWritesMemoryMD(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	seedProposeBatch(t, rig, convID, 1, []MemoryProposal{
		{Target: "memory.md", Rule: "Always run go test ./... before claiming a task is done.", Evidence: "main-epoch-1"},
		{Target: "memory.md", Rule: "Prefer compact output over long prose.", Evidence: "main-epoch-1"},
	}, nil)

	applied := rig.call(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted:       []MemoryAccept{{Target: "memory.md", Index: 0}},
	})
	if !applied.Applied {
		t.Fatal("apply_memory: applied must be true")
	}

	// Exact file: one daemon-formatted line, epoch from the propose event.
	want := "- Always run go test ./... before claiming a task is done. — cites: main-epoch-1; reaffirmed: 1\n"
	memPath := filepath.Join(root, ".odo", "memory.md")
	if got := readFileStr(t, memPath); got != want {
		t.Errorf("memory.md = %q, want %q", got, want)
	}
	if strings.Contains(readFileStr(t, memPath), "Prefer compact output") {
		t.Error("rejected proposal landed in memory.md")
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	applyUpdates := memoryUpdatesByCause(t, events, "apply")
	if len(applyUpdates) != 1 {
		t.Fatalf("memory_update(apply) events = %d, want 1", len(applyUpdates))
	}
	mu := applyUpdates[0]
	if mu["layer"] != "memory" {
		t.Errorf("memory_update layer = %v, want memory", mu["layer"])
	}
	if mu["before_sha"] != sha16(nil) || mu["after_sha"] != sha16([]byte(want)) {
		t.Errorf("memory_update shas = %v -> %v", mu["before_sha"], mu["after_sha"])
	}
	if d, _ := mu["detail"].(string); !strings.Contains(d, "accepted 1 rule(s)") {
		t.Errorf("memory_update detail = %v", mu["detail"])
	}

	applies := payloadsByAction(t, events, "memory_apply")
	if len(applies) != 1 {
		t.Fatalf("memory_apply events = %d, want 1", len(applies))
	}
	if applies[0]["epoch"] != float64(1) {
		t.Errorf("memory_apply epoch = %v, want 1", applies[0]["epoch"])
	}
	metrics, _ := applies[0]["metrics"].(map[string]interface{})
	if metrics["accepted"] != float64(1) || metrics["rejected"] != float64(1) {
		t.Errorf("memory_apply metrics = %v, want 1 accepted / 1 rejected", metrics)
	}

	// The batch is consumed: nothing pending for review anymore.
	if pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID}); pend.Epoch != 0 {
		t.Errorf("memory_proposals after apply = epoch %d, want 0 (consumed)", pend.Epoch)
	}
}

// TestMemoryInjectedIntoPrompt covers Demo A step 5: with .odo/memory.md
// present, the next prompt carries the "## Project memory" layer block, the
// journaled user_message recall list includes the .odo/memory.md marker, and
// the receipt maps it to the sha16 of the injected bytes.
func TestMemoryInjectedIntoPrompt(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	memContent := "- Always run go test ./... before claiming done. — cites: main-epoch-1; reaffirmed: 1\n"
	odoDir := filepath.Join(root, ".odo")
	if err := os.MkdirAll(odoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odoDir, "memory.md"), []byte(memContent), 0o644); err != nil {
		t.Fatal(err)
	}

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt (project memory)"})

	if recall := recallPathsFromEvent(t, sent.Event); len(recall) != 1 || recall[0] != ".odo/memory.md" {
		t.Fatalf("recall = %v, want [.odo/memory.md]", recall)
	}
	receipt := receiptFromEvent(t, sent.Event)
	if len(receipt) != 1 || receipt[".odo/memory.md"] != sha16([]byte(memContent)) {
		t.Errorf("receipt = %v, want {.odo/memory.md: %s}", receipt, sha16([]byte(memContent)))
	}

	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}
	if !strings.Contains(done.Diff.Content, "## Project memory (behavior rules)") {
		t.Error("prompt (diff content) missing the project memory header")
	}
	if !strings.Contains(done.Diff.Content, memContent) {
		t.Error("prompt (diff content) missing the memory.md body")
	}
}

// TestInjectionReceiptHashesFrozen pins the receipt values to frozen sha16
// vectors over known layer contents (user.md, memory.md, one wiki note's
// exact injected block), and verifies absent layers have no receipt entry.
// Vectors were computed with sha256[:8 hex16] over the pinned strings and
// cross-checked outside the codebase.
func TestInjectionReceiptHashesFrozen(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	userContent := "- Always state the invariant before the fix. — seen: odo, ananke\n"
	memContent := "- Always run go test ./... before claiming done. — cites: main-epoch-1; reaffirmed: 1\n"
	noteContent := "# Epoch 1\n\nDecision: hash the exact injected block, not the file.\n"
	writeUserMD(t, home, userContent)
	odoDir := filepath.Join(root, ".odo")
	if err := os.MkdirAll(odoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odoDir, "memory.md"), []byte(memContent), 0o644); err != nil {
		t.Fatal(err)
	}
	wikiDir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	notePath := filepath.Join(wikiDir, "main-epoch-1.md")
	if err := os.WriteFile(notePath, []byte(noteContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Frozen vectors: sha16 over the exact injected strings. The note hash
	// covers the built block "## main-epoch-1.md\n\n<content>\n\n---\n\n",
	// not the raw file.
	const (
		wantUserHash = "fec0f06d8f3463f1"
		wantMemHash  = "a34ef6f441cf2366"
		wantNoteHash = "b267447428c803da"
	)
	noteBlock := "## main-epoch-1.md\n\n" + noteContent + "\n\n---\n\n"
	if sha16([]byte(userContent)) != wantUserHash {
		t.Fatalf("sha16(user) = %s, frozen %s (helper drifted)", sha16([]byte(userContent)), wantUserHash)
	}
	if sha16([]byte(memContent)) != wantMemHash {
		t.Fatalf("sha16(memory) = %s, frozen %s (helper drifted)", sha16([]byte(memContent)), wantMemHash)
	}
	if sha16([]byte(noteBlock)) != wantNoteHash {
		t.Fatalf("sha16(noteBlock) = %s, frozen %s (helper drifted)", sha16([]byte(noteBlock)), wantNoteHash)
	}

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "receipt check"})

	wantRecall := []string{"~/.odo/user.md", ".odo/memory.md", notePath}
	if got := recallPathsFromEvent(t, sent.Event); fmt.Sprint(got) != fmt.Sprint(wantRecall) {
		t.Fatalf("recall = %v, want %v", got, wantRecall)
	}
	receipt := receiptFromEvent(t, sent.Event)
	wantReceipt := map[string]string{
		"~/.odo/user.md": wantUserHash,
		".odo/memory.md": wantMemHash,
		notePath:         wantNoteHash,
	}
	if len(receipt) != len(wantReceipt) {
		t.Fatalf("receipt = %v, want exactly %v", receipt, wantReceipt)
	}
	for k, want := range wantReceipt {
		if receipt[k] != want {
			t.Errorf("receipt[%q] = %q, want frozen %q", k, receipt[k], want)
		}
	}

	// Absent layer, absent entry: remove user.md, send again, and the
	// ~/.odo/user.md key disappears (the map is not shrunk to an empty
	// value or a stale hash).
	rig.pollUntilDone(t, convID) // the first run must finish before send 2
	if err := os.Remove(filepath.Join(home, ".odo", "user.md")); err != nil {
		t.Fatal(err)
	}
	sent2 := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "receipt check two"})
	receipt2 := receiptFromEvent(t, sent2.Event)
	if _, ok := receipt2["~/.odo/user.md"]; ok {
		t.Errorf("receipt after removing user.md still has the user.md key: %v", receipt2)
	}
	if len(receipt2) != 2 || receipt2[".odo/memory.md"] != wantMemHash || receipt2[notePath] != wantNoteHash {
		t.Errorf("receipt2 = %v, want memory.md + note keys only", receipt2)
	}
	rig.pollUntilDone(t, convID)

	// No layers at all: the receipt key is omitted entirely (M3 convention).
	rig2 := startRig(t, initRepo(t))
	defer rig2.stop(t)
	boot2 := rig2.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: rig2.root})
	sent3 := rig2.call(t, Request{Cmd: CmdSendMessage, ConversationID: boot2.Conversation.ID, Text: "plain"})
	var p map[string]interface{}
	if err := json.Unmarshal(sent3.Event.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if _, ok := p["receipt"]; ok {
		t.Errorf("receipt key present with no memory layers: %v", p["receipt"])
	}
	rig2.pollUntilDone(t, boot2.Conversation.ID)
}

// TestMemoryCapRotationArchive covers Demo C steps 1-2: a memory.md at the
// 4 KB cap that overflows on apply evicts the least-recently-reaffirmed rule
// (never the influx rule) to memory-archive.md under a rotated header;
// memory.md stays under cap.
func TestMemoryCapRotationArchive(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)

	// 32 rule lines of exactly 127 bytes each: 4096 bytes == cap on disk.
	// Padding lives inside the rule text so every line still parses as a
	// rule (a pad after the suffix would make the line opaque/unevictable).
	lineFor := func(marker string, reaffirmed int) string {
		suffix := fmt.Sprintf(" — cites: main-epoch-1; reaffirmed: %d", reaffirmed)
		pad := 127 - len("- ") - len(marker) - len(suffix)
		if pad < 0 {
			t.Fatalf("marker %q too long for fixed-width line", marker)
		}
		return "- " + marker + strings.Repeat("x", pad) + suffix
	}
	lines := []string{lineFor("OLDEST RULE", 1)} // lowest reaffirmed: rotation victim
	for i := 2; i <= 32; i++ {
		lines = append(lines, lineFor(fmt.Sprintf("rule %02d", i), 2))
	}
	odoDir := filepath.Join(root, ".odo")
	if err := os.MkdirAll(odoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := strings.Join(lines, "\n") + "\n"
	if len(old) != memoryCap {
		t.Fatalf("seed memory.md = %d bytes, want exactly %d", len(old), memoryCap)
	}
	if err := os.WriteFile(filepath.Join(odoDir, "memory.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	influx := "Always validate conclusions against real tool output."
	seedProposeBatch(t, rig, convID, 1, []MemoryProposal{
		{Target: "memory.md", Rule: influx, Evidence: "main-epoch-1"},
	}, nil)

	applied := rig.call(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted:       []MemoryAccept{{Target: "memory.md", Index: 0}},
	})
	if !applied.Applied {
		t.Fatal("apply_memory: applied must be true")
	}

	mem := readFileStr(t, filepath.Join(odoDir, "memory.md"))
	if len(mem) > memoryCap {
		t.Errorf("memory.md after apply = %d bytes, exceeds cap %d", len(mem), memoryCap)
	}
	if !strings.Contains(mem, "- "+influx+" — cites: main-epoch-1; reaffirmed: 1") {
		t.Error("influx rule missing from memory.md (it must never be evicted)")
	}
	if strings.Contains(mem, "OLDEST RULE") {
		t.Error("least-recently-reaffirmed rule survived the rotation")
	}
	// The overflow was one influx line (93 B): exactly one 127 B line is
	// evicted; rule 02 (reaffirmed 2) stays.
	if !strings.Contains(mem, "rule 02") {
		t.Error("rotation evicted more than the single LRU rule")
	}

	archive := readArchive(root)
	if !strings.Contains(archive, "— rotated from memory.md (overflow)") {
		t.Errorf("archive missing the rotated header:\n%s", archive)
	}
	if !strings.Contains(archive, lines[0]) {
		t.Error("archive missing the evicted rule's original line")
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	applyUpdates := memoryUpdatesByCause(t, events, "apply")
	if len(applyUpdates) != 1 {
		t.Fatalf("memory_update(apply) events = %d, want 1", len(applyUpdates))
	}
	if d, _ := applyUpdates[0]["detail"].(string); strings.Contains(d, "rotated") {
		t.Errorf("apply detail = %v, the rotation is its own cause:rotate event", d)
	}
	rotates := memoryUpdatesByCause(t, events, "rotate")
	if len(rotates) != 1 {
		t.Fatalf("memory_update(rotate) events = %d, want 1", len(rotates))
	}
	if rotates[0]["layer"] != "memory" {
		t.Errorf("rotate layer = %v, want memory", rotates[0]["layer"])
	}
	if d, _ := rotates[0]["detail"].(string); !strings.Contains(d, "rotated 1 to memory-archive.md (overflow)") {
		t.Errorf("rotate detail = %v, want the rotated-to-archive summary", d)
	}
}

// TestMemoryRetractionToArchive covers Demo C step 3: an accepted rule whose
// contradicts matches (normalized: case/whitespace-insensitive) a stored
// rule moves that rule to the archive under a retracted header; a contradicts
// that matches nothing is journaled as memory_update{cause:retract}, never
// silently ignored.
func TestMemoryRetractionToArchive(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)

	npmLine := "- Always use npm for package management. — cites: main-epoch-1; reaffirmed: 1"
	old := npmLine + "\n- Prefer small diffs. — cites: main-epoch-1; reaffirmed: 1\n"
	odoDir := filepath.Join(root, ".odo")
	if err := os.MkdirAll(odoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odoDir, "memory.md"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	seedProposeBatch(t, rig, convID, 1, []MemoryProposal{
		// Lowercase + double whitespace: the normalized compare must match.
		{Target: "memory.md", Rule: "Always use pnpm for package management.", Evidence: "main-epoch-1",
			Contradicts: "always  use NPM for package management."},
		{Target: "memory.md", Rule: "Always run the linter before a commit.", Evidence: "main-epoch-1",
			Contradicts: "Prefer hand-rolled shell scripts."},
	}, nil)

	applied := rig.call(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted: []MemoryAccept{
			{Target: "memory.md", Index: 0},
			{Target: "memory.md", Index: 1},
		},
	})
	if !applied.Applied {
		t.Fatal("apply_memory: applied must be true")
	}

	mem := readFileStr(t, filepath.Join(odoDir, "memory.md"))
	if !strings.Contains(mem, "- Always use pnpm for package management. — cites: main-epoch-1; reaffirmed: 1") {
		t.Error("memory.md missing the incoming pnpm rule")
	}
	// The contradicted rule must be GONE from memory.md. Assert the exact
	// old line: "npm" is a substring of "pnpm", so we match on the full
	// "- Always use npm for package management." line (present only if the
	// old npm rule survived) — never the new "- Always use pnpm..." line.
	if strings.Contains(mem, "- Always use npm for package management.") {
		t.Error("contradicted npm rule is still in memory.md")
	}
	if !strings.Contains(mem, "- Prefer small diffs. — cites: main-epoch-1; reaffirmed: 1") {
		t.Error("unrelated stored rule was touched")
	}
	if !strings.Contains(mem, "- Always run the linter before a commit. — cites: main-epoch-1; reaffirmed: 1") {
		t.Error("memory.md missing the unmatched-contradicts rule (it still lands)")
	}

	archive := readArchive(root)
	if !strings.Contains(archive, "— retracted: Always use pnpm for package management. (conflict)") {
		t.Errorf("archive missing the retracted header:\n%s", archive)
	}
	if !strings.Contains(archive, npmLine) {
		t.Error("archive missing the retracted rule's original line")
	}
	if strings.Contains(archive, "Prefer hand-rolled shell scripts") {
		t.Error("unmatched contradicts must NOT fabricate an archive entry")
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	applyUpdates := memoryUpdatesByCause(t, events, "apply")
	if len(applyUpdates) != 1 {
		t.Fatalf("memory_update(apply) events = %d, want 1 (the appended rules)", len(applyUpdates))
	}
	if d, _ := applyUpdates[0]["detail"].(string); strings.Contains(d, "retracted") {
		t.Errorf("apply detail = %v, the retraction is its own cause:retract event", d)
	}
	// Two retract records, both cause:"retract" distinguished by detail:
	// the matched conflict move, then the no-match surfacing.
	retracts := memoryUpdatesByCause(t, events, "retract")
	if len(retracts) != 2 {
		t.Fatalf("memory_update(retract) events = %d, want 2 (the match + the no-match)", len(retracts))
	}
	if retracts[0]["layer"] != "memory" || retracts[1]["layer"] != "memory" {
		t.Errorf("retract layers = %v/%v, want memory/memory", retracts[0]["layer"], retracts[1]["layer"])
	}
	if d, _ := retracts[0]["detail"].(string); !strings.Contains(d, "retracted 1 (conflict):") {
		t.Errorf("retract detail = %v, want the conflict-move summary", d)
	}
	if d, _ := retracts[1]["detail"].(string); !strings.Contains(d, `no match for contradicts: "Prefer hand-rolled shell scripts."`) {
		t.Errorf("retract detail = %v, want the no-match surfacing", d)
	}
}

// TestUserMDPromotionCrossProject covers Demo B: with two registered sibling
// projects whose memory.md holds the same imperative, a learner user proposal
// is kept with daemon-verified project names (never the LLM's self-tags) and
// apply writes the "seen:" line to ~/.odo/user.md. A second batch that would
// overflow the 4 KB user cap is refused wholesale and writes nothing.
func TestUserMDPromotionCrossProject(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Two siblings, each holding the recurring rule in their .odo/memory.md.
	rule := "Always verify conclusions with a concrete tool result."
	type sib struct {
		name  string
		added string
	}
	var rows []RegistryRow
	for _, s := range []sib{{"ananke", "2026-01-02T00:00:00Z"}, {"projb", "2026-01-01T00:00:00Z"}} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".odo"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".odo", "memory.md"),
			[]byte("- "+rule+" — cites: main-epoch-3; reaffirmed: 3\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, RegistryRow{Root: resolved, Name: s.name, Added: s.added})
	}
	regPath := filepath.Join(t.TempDir(), "projects.json")
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regPath, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODO_REGISTRY_PATH", regPath)

	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\n"+rule+"\n")
	// Self-tagged project names are bogus on purpose: the daemon must
	// replace them with the verified recurrence set.
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[],"user":[
{"rule":"`+rule+`","projects":["bogus-llm-tag"]}
],"reaffirm":[]}`)
	rig := startRig(t, root)
	defer rig.stop(t)

	convID, d := runToDistill(t, rig, root)
	if d.MemoryProposals != 1 {
		t.Fatalf("distill MemoryProposals = %d, want 1 (the user proposal)", d.MemoryProposals)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	ownName := filepath.Base(resolvedRoot)
	// sibling order: Added desc (ananke after projb registration date wins).
	wantProjects := []string{ownName, "ananke", "projb"}

	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if pend.Epoch != 1 || len(pend.Proposals) != 1 {
		t.Fatalf("memory_proposals = epoch %d, %d proposals; want epoch 1, 1", pend.Epoch, len(pend.Proposals))
	}
	up := pend.Proposals[0]
	if up.Target != "user.md" || up.Rule != rule {
		t.Errorf("user proposal = %+v", up)
	}
	if fmt.Sprint(up.Projects) != fmt.Sprint(wantProjects) {
		t.Errorf("daemon-verified projects = %v, want %v (LLM self-tags replaced)", up.Projects, wantProjects)
	}

	applied := rig.call(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted:       []MemoryAccept{{Target: "user.md", Index: 0}},
	})
	if !applied.Applied {
		t.Fatal("apply_memory: applied must be true")
	}
	userPath := filepath.Join(home, ".odo", "user.md")
	wantLine := "- " + rule + " — seen: " + strings.Join(wantProjects, ", ") + "\n"
	if got := readFileStr(t, userPath); got != wantLine {
		t.Errorf("user.md = %q, want %q", got, wantLine)
	}
	if fi, err := os.Stat(userPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("user.md mode = %v (err %v), want 0600", fi, err)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	userUpdates := 0
	for _, mu := range memoryUpdatesByCause(t, events, "apply") {
		if mu["layer"] == "user" {
			userUpdates++
		}
	}
	if userUpdates != 1 {
		t.Errorf("memory_update(apply, layer user) = %d, want 1", userUpdates)
	}

	// Second batch: a single rule too large for the remaining headroom is
	// refused wholesale — nothing written, nothing journaled, batch pending.
	oversized := strings.Repeat("r", 4100)
	seedProposeBatch(t, rig, convID, 2, []MemoryProposal{
		{Target: "user.md", Rule: oversized, Projects: []string{ownName, "ananke"}},
	}, nil)
	resp := rig.callExpectErr(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          2,
		Accepted:       []MemoryAccept{{Target: "user.md", Index: 0}},
	})
	if !strings.Contains(resp.Error, "would exceed") {
		t.Errorf("overflow apply error = %q, want mention of would exceed", resp.Error)
	}
	if got := readFileStr(t, userPath); got != wantLine {
		t.Errorf("user.md after refused apply = %q, want unchanged %q", got, wantLine)
	}
	if pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID}); pend.Epoch != 2 {
		t.Errorf("refused apply consumed the batch: memory_proposals epoch = %d, want 2 pending", pend.Epoch)
	}
}

// TestReadMemoryGuards covers read_memory: the daemon returns the three
// canonical files for the bound root (constructing the paths itself),
// rejects a foreign project_root, and reports missing files as "".
func TestReadMemoryGuards(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	rig := startRig(t, root)
	defer rig.stop(t)

	odoDir := filepath.Join(root, ".odo")
	if err := os.MkdirAll(odoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odoDir, "memory.md"), []byte("mem line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odoDir, "memory-archive.md"), []byte("archive line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeUserMD(t, home, "user line\n")

	got := rig.call(t, Request{Cmd: CmdReadMemory, ProjectRoot: root})
	if got.MemoryContent != "mem line\n" || got.ArchiveContent != "archive line\n" || got.UserContent != "user line\n" {
		t.Errorf("read_memory = (%q, %q, %q)", got.MemoryContent, got.ArchiveContent, got.UserContent)
	}
	// An empty project_root defaults to the bound root.
	if got2 := rig.call(t, Request{Cmd: CmdReadMemory}); got2.UserContent != "user line\n" {
		t.Errorf("read_memory (default root) user_content = %q", got2.UserContent)
	}

	// A foreign root is rejected by the same equality guard as resolveProject.
	resp := rig.callExpectErr(t, Request{Cmd: CmdReadMemory, ProjectRoot: t.TempDir()})
	if !strings.Contains(resp.Error, "bound to") {
		t.Errorf("foreign root error = %q, want the binding guard", resp.Error)
	}

	// Missing files come back as "".
	for _, p := range []string{
		filepath.Join(odoDir, "memory.md"),
		filepath.Join(odoDir, "memory-archive.md"),
		filepath.Join(home, ".odo", "user.md"),
	} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	empty := rig.call(t, Request{Cmd: CmdReadMemory, ProjectRoot: root})
	if empty.MemoryContent != "" || empty.ArchiveContent != "" || empty.UserContent != "" {
		t.Errorf("read_memory after removal = (%q, %q, %q), want all empty",
			empty.MemoryContent, empty.ArchiveContent, empty.UserContent)
	}
}

// TestApplyMemoryIdempotent covers the batch-consume contract: a second
// apply on the same epoch errors "already applied" and changes nothing; a
// refused apply (user.md overflow) leaves the batch pending so a retry after
// trimming user.md recomputes from the original proposals and succeeds.
func TestApplyMemoryIdempotent(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	seedProposeBatch(t, rig, convID, 1, []MemoryProposal{
		{Target: "memory.md", Rule: "Always run go test ./... before claiming done.", Evidence: "main-epoch-1"},
	}, nil)

	first := rig.call(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted:       []MemoryAccept{{Target: "memory.md", Index: 0}},
	})
	if !first.Applied {
		t.Fatal("first apply_memory: applied must be true")
	}
	memPath := filepath.Join(root, ".odo", "memory.md")
	afterFirst := readFileStr(t, memPath)

	// Second apply: consumed batch -> "already applied", applied:false, and
	// the file is byte-identical (no duplicate line).
	resp := rig.callExpectErr(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted:       []MemoryAccept{{Target: "memory.md", Index: 0}},
	})
	if !strings.Contains(resp.Error, "already applied") {
		t.Errorf("second apply error = %q, want already applied", resp.Error)
	}
	if resp.Applied {
		t.Error("second apply: applied must be false")
	}
	if got := readFileStr(t, memPath); got != afterFirst {
		t.Errorf("memory.md changed after duplicate apply: %q -> %q", afterFirst, got)
	}

	// Refused apply (user.md would overflow) stays pending; retry succeeds.
	writeUserMD(t, home, strings.Repeat("x", 4000)+"\n")
	bigRule := strings.Repeat("r", 200)
	seedProposeBatch(t, rig, convID, 2, []MemoryProposal{
		{Target: "user.md", Rule: bigRule, Projects: []string{"odo", "ananke"}},
	}, nil)
	resp = rig.callExpectErr(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          2,
		Accepted:       []MemoryAccept{{Target: "user.md", Index: 0}},
	})
	if !strings.Contains(resp.Error, "would exceed") {
		t.Errorf("overflow apply error = %q, want would exceed", resp.Error)
	}
	if got := readFileStr(t, filepath.Join(home, ".odo", "user.md")); got != strings.Repeat("x", 4000)+"\n" {
		t.Error("refused apply wrote to user.md")
	}
	pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID})
	if pend.Epoch != 2 || len(pend.Proposals) != 1 {
		t.Fatalf("batch after refused apply = epoch %d, %d proposals; want epoch 2, 1 pending", pend.Epoch, len(pend.Proposals))
	}

	// Trim the user file; the retry recomputes and succeeds.
	writeUserMD(t, home, "- Short durable line.\n")
	retry := rig.call(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          2,
		Accepted:       []MemoryAccept{{Target: "user.md", Index: 0}},
	})
	if !retry.Applied {
		t.Fatal("retry apply_memory: applied must be true")
	}
	userContent := readFileStr(t, filepath.Join(home, ".odo", "user.md"))
	if !strings.HasSuffix(userContent, "- "+bigRule+" — seen: odo, ananke\n") {
		t.Errorf("user.md after retry = %q", userContent)
	}
	if !strings.HasPrefix(userContent, "- Short durable line.\n") {
		t.Errorf("user.md lost its prior content on retry: %q", userContent)
	}
	if pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID}); pend.Epoch != 0 {
		t.Errorf("memory_proposals after retry = epoch %d, want 0 (consumed)", pend.Epoch)
	}
}

// TestUserMemoryIdempotency covers the mid-write retry window for user.md:
// writes go archive → user.md → memory.md, so when the memory.md write
// fails AFTER user.md was written the batch stays pending with the user
// rule already in the file. The retry replans against that user.md and must
// skip the already-stored rule body (exactly one "seen:" line, no
// duplicate), while a genuinely new rule in a later set still appends.
func TestUserMemoryIdempotency(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	memRule := "Always run go test ./... before claiming done."
	userRule := "Prefer boring solutions over clever ones."
	seedProposeBatch(t, rig, convID, 1, []MemoryProposal{
		{Target: "memory.md", Rule: memRule, Evidence: "main-epoch-1"},
		{Target: "user.md", Rule: userRule, Projects: []string{"odo", "ananke"}},
	}, nil)

	// Sabotage the memory.md write: a directory at its path makes the
	// atomic rename fail AFTER user.md is already written — the apply
	// errors, the batch stays pending, user.md holds the rule.
	memPath := filepath.Join(root, ".odo", "memory.md")
	if err := os.MkdirAll(memPath, 0o755); err != nil {
		t.Fatal(err)
	}
	resp := rig.callExpectErr(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted: []MemoryAccept{
			{Target: "memory.md", Index: 0},
			{Target: "user.md", Index: 1},
		},
	})
	if !strings.Contains(resp.Error, "write memory.md") {
		t.Fatalf("sabotaged apply error = %q, want the memory.md write failure", resp.Error)
	}
	userPath := filepath.Join(home, ".odo", "user.md")
	wantUserLine := "- " + userRule + " — seen: odo, ananke\n"
	if got := readFileStr(t, userPath); got != wantUserLine {
		t.Fatalf("user.md after partial apply = %q, want %q (written before the failure)", got, wantUserLine)
	}
	if pend := rig.call(t, Request{Cmd: CmdMemoryProposals, ConversationID: convID}); pend.Epoch != 1 {
		t.Fatalf("batch after partial apply = epoch %d, want 1 pending", pend.Epoch)
	}

	// Restore the write path and retry the SAME batch: planUserApply replans
	// against the already-applied user.md and skips the stored body.
	if err := os.Remove(memPath); err != nil {
		t.Fatal(err)
	}
	retry := rig.call(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted: []MemoryAccept{
			{Target: "memory.md", Index: 0},
			{Target: "user.md", Index: 1},
		},
	})
	if !retry.Applied {
		t.Fatal("retry apply_memory: applied must be true")
	}
	userContent := readFileStr(t, userPath)
	if userContent != wantUserLine {
		t.Errorf("user.md after retry = %q, want unchanged %q", userContent, wantUserLine)
	}
	if n := strings.Count(userContent, userRule); n != 1 {
		t.Errorf("user rule appears %d times in user.md, want exactly 1", n)
	}
	wantMem := "- " + memRule + " — cites: main-epoch-1; reaffirmed: 1\n"
	if got := readFileStr(t, memPath); got != wantMem {
		t.Errorf("memory.md after retry = %q, want %q", got, wantMem)
	}
	// The skipped user write journals no user-layer apply event (the failed
	// attempt journaled nothing, the retry changed nothing).
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	userUpdates := 0
	for _, mu := range memoryUpdatesByCause(t, events, "apply") {
		if mu["layer"] == "user" {
			userUpdates++
		}
	}
	if userUpdates != 0 {
		t.Errorf("memory_update(apply, layer user) = %d, want 0 (retry skipped the stored rule)", userUpdates)
	}

	// A later batch re-proposing the stored body (different seen list) still
	// skips it — skip is by rule body, not by seen set — while a genuinely
	// new rule in the same set appends.
	newRule := "Ship only after a smoke test."
	seedProposeBatch(t, rig, convID, 2, []MemoryProposal{
		{Target: "user.md", Rule: userRule, Projects: []string{"odo"}},
		{Target: "user.md", Rule: newRule, Projects: []string{"odo", "projb"}},
	}, nil)
	second := rig.call(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          2,
		Accepted: []MemoryAccept{
			{Target: "user.md", Index: 0},
			{Target: "user.md", Index: 1},
		},
	})
	if !second.Applied {
		t.Fatal("second apply_memory: applied must be true")
	}
	final := readFileStr(t, userPath)
	wantFinal := wantUserLine + "- " + newRule + " — seen: odo, projb\n"
	if final != wantFinal {
		t.Errorf("user.md final = %q, want %q (stored rule untouched, new rule appended)", final, wantFinal)
	}
	if n := strings.Count(final, userRule); n != 1 {
		t.Errorf("user rule appears %d times after second apply, want exactly 1", n)
	}
}

// TestPlanUserApplySkip pins the retry-skip contract of planUserApply: a
// rule whose normalized body is already stored is skipped — same body with
// a different seen list, letter case, or spacing included — an
// empty-normalized-body rule is never skipped (mirroring the memory.md
// guard), and a replay of the same set against the applied file converges.
func TestPlanUserApplySkip(t *testing.T) {
	old := "- Prefer boring solutions over clever ones. — seen: odo, ananke\n" +
		"- hand edited line without any seen list\n"
	got, err := planUserApply(old, []acceptedUserRule{
		// Duplicate: same body, different seen list + case/whitespace.
		{rule: "  prefer   BORING solutions over clever ones. ", projects: []string{"projb"}},
		{rule: "Ship only after a smoke test.", projects: []string{"odo"}},
		// Normalizes to "" — never skipped, appended verbatim.
		{rule: "   ", projects: []string{"odo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := old +
		"- Ship only after a smoke test. — seen: odo\n" +
		"-     — seen: odo\n" // "- " + 3-space rule + " — seen:"
	if got != want {
		t.Errorf("planUserApply = %q, want %q", got, want)
	}

	// Replay the same set against the applied file (pending-batch retry):
	// the stored body is skipped and the file is byte-identical.
	again, err := planUserApply(got, []acceptedUserRule{
		{rule: "Ship only after a smoke test.", projects: []string{"odo", "ananke"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Errorf("replay = %q, want unchanged %q (retry converges)", again, got)
	}
}
