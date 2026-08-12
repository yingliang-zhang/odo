package ipc

// run_verdict tests (epoch-8, outstanding #1): the false-stop signature is
// exercised end-to-end through stub-agent runs — exit 0, empty output —
// plus the auto-land verdict gate at the unit seam, plus admission-ledger
// honesty (retry_fired records the TRUE admission outcome, never a lie).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// verdictRows returns every journaled memory_update{layer:"run_verdict"}
// payload for the conversation, in order.
func verdictRows(t *testing.T, st *store.Store, convID int64) []map[string]interface{} {
	t.Helper()
	events, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]interface{}
	for _, e := range events {
		if e.Type != store.EventMemoryUpdate {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("event %d: %v", e.ID, err)
		}
		if p["layer"] == "run_verdict" {
			rows = append(rows, p)
		}
	}
	return rows
}

// falseStopStub counts its invocations in counterFile and always exits 0
// with an EMPTY output file — the exact false-stop transport signature
// (OMP exits clean, zero output reaches the adapter).
func falseStopStub(counterFile string) string {
	return fmt.Sprintf(`#!/bin/sh
printf 'x\n' >> %q
output_file="$3"
sleep 1
: > "$output_file"
exit 0
`, counterFile)
}

func counterLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

// TestFalseStopRetryOnce pins the whole pipeline: run 1 false-stops → one
// verdict row + exactly one automatic retry (verbatim goal) → the retry
// false-stops too → its verdict row carries is_retry with retry_fired=false
// and NO third run ever starts (loop bound = 1).
func TestFalseStopRetryOnce(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home) // hermetic prefs: no auto-land noise
	counter := filepath.Join(t.TempDir(), "invocations")
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, falseStopStub(counter)))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "fs-trigger-token do the thing"})

	// Run 1: finishes as a clean-looking no-diff run (the bug shape). The
	// retry registers SYNCHRONOUSLY inside run 1's drain (round-2 panel
	// fix: admission under the drain's own s.mu), so AgentRunning never
	// goes false across the hand-off — this one pollUntilDone consumes the
	// retry's entire lifecycle: registration, run 2, its terminal drain
	// and its retire. Observing byConv mid-flight races a window that no
	// longer exists; the retry's durable observable is its stub firing.
	rig.pollUntilDone(t, convID)
	waitForCond(t, 10*time.Second, "retry stub invocation", func() bool {
		return counterLines(t, counter) >= 2
	})
	// If admission ever becomes asynchronous again, the retry's terminal
	// drain may still be pending — flush it. No-op under the synchronous
	// design (pollUntilDone already returned AgentRunning=false).
	rig.server.mu.Lock()
	retryRunning := rig.server.byConv[convID] != ""
	rig.server.mu.Unlock()
	if retryRunning {
		rig.pollUntilDone(t, convID)
	}

	// Loop bound: wait past a full stub period — a third invocation must
	// never appear.
	time.Sleep(2500 * time.Millisecond)
	if n := counterLines(t, counter); n != 2 {
		t.Fatalf("stub invocations = %d, want exactly 2 (one retry, no chain)", n)
	}
	prompts, err := filepath.Glob(filepath.Join(root, ".odo", "prompts", "*.txt"))
	if err != nil || len(prompts) != 2 {
		t.Fatalf("prompt files = %d (%v), want 2: original + retry", len(prompts), err)
	}
	for _, p := range prompts {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "fs-trigger-token do the thing") {
			t.Errorf("prompt %s missing the verbatim goal", filepath.Base(p))
		}
	}

	rows := verdictRows(t, rig.store, convID)
	if len(rows) != 2 {
		t.Fatalf("verdict rows = %d, want 2: %v", len(rows), rows)
	}
	first, second := rows[0], rows[1]
	if first["verdict"] != verdictFalseStop || first["is_retry"] != false || first["retry_fired"] != true {
		t.Errorf("first verdict row = %v, want false_stop fresh run with retry_fired", first)
	}
	if second["verdict"] != verdictFalseStop || second["is_retry"] != true || second["retry_fired"] != false {
		t.Errorf("second verdict row = %v, want false_stop is_retry with NO retry_fired", second)
	}
	for i, r := range rows {
		if r["texts"] != float64(0) || r["tool_calls"] != float64(0) {
			t.Errorf("row %d tallies = %v texts %v tool_calls, want zero", i, r["texts"], r["tool_calls"])
		}
	}

		// The journal stays truthful: two agent_done rows, zero agent_text —
	// nothing forged. The second stop surfaces ONE daemon-authored,
	// labeled advisory error (the human-wait fall-through, panel fix).
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	counts := map[string]int{}
	var advisory bool
	for _, ev := range events {
		counts[ev.Type]++
		if ev.Type != store.EventAgentError {
			continue
		}
		var p struct {
			Error string `json:"error"`
			Odo   bool   `json:"odo"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("event %d: %v", ev.ID, err)
		}
		if p.Odo && strings.Contains(p.Error, "resend manually") {
			advisory = true
		}
	}
	if counts[store.EventAgentDone] != 2 || counts[store.EventAgentText] != 0 || counts[store.EventAgentError] != 1 {
		t.Errorf("journal counts = %v, want 2 agent_done, 0 agent_text, 1 labeled advisory", counts)
	}
	if !advisory {
		t.Error("no labeled human-wait advisory journaled for the second consecutive false stop")
	}
}

// occupancyStub branches on the prompt: the slot holder sleeps long and
// writes real output (it must stay active across the assertions); every
// other run exits fast with the false-stop signature (exit 0, empty
// output, no diff). Every invocation is counted in counterFile.
func occupancyStub(counterFile string) string {
	return fmt.Sprintf(`#!/bin/sh
printf 'x\n' >> %q
prompt_file="$2"
output_file="$3"
if grep -q slot-holder-token "$prompt_file"; then
	sleep 5
	printf 'held the slot\n' > "$output_file"
	exit 0
fi
sleep 1
: > "$output_file"
exit 0
`, counterFile)
}

// TestRetryAdmissionReported pins the round-2 panel fix: when the single
// automatic retry canNOT be admitted, the verdict row must journal
// retry_fired=false (the old goroutine admission recorded a ledger lie —
// retry_fired=true journaled first, admission then silently vetoed) AND a
// labeled advisory must tell the human why nothing was produced. The
// forced refusal is the concurrency cap: resolveMaxConcurrent re-reads
// prefs.md per admission attempt, so tightening it after both runs
// registered deterministically wedges the retry — no sleeps, no races.
func TestRetryAdmissionReported(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home) // hermetic prefs: cap dance below stays local
	counter := filepath.Join(t.TempDir(), "invocations")
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, occupancyStub(counter)))
	rig := startRig(t, root)
	defer rig.stop(t)

	// conv1 holds the daemon-wide slot with a run that outlives the
	// assertions; conv2 gets the false-stop run.
	conv1 := bootstrapConv(t, rig, root)
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: conv1, Text: "slot-holder-token hold"})
	ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "side"})
	if ws.Workstream == nil {
		t.Fatal("create_workstream: missing workstream")
	}
	boot2 := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
	if boot2.Conversation == nil {
		t.Fatal("bootstrap side workstream: missing conversation")
	}
	conv2 := boot2.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: conv2, Text: "fs-trigger-token do the thing"})

	// conv2's retry admission happens inside this poll's drain. Shrink the
	// cap first, so the still-active slot holder already exhausts it.
	writePrefs(t, home, "max_concurrent_runs: 1\n")
	rig.pollUntilDone(t, conv2)

	// The ledger tells the truth: exactly one verdict row (the refused
	// retry never became a run), and it records retry_fired=false.
	rows := verdictRows(t, rig.store, conv2)
	if len(rows) != 1 {
		t.Fatalf("verdict rows = %d, want 1 (a refused retry journals no second run): %v", len(rows), rows)
	}
	if rows[0]["verdict"] != verdictFalseStop || rows[0]["is_retry"] != false || rows[0]["retry_fired"] != false {
		t.Errorf("verdict row = %v, want false_stop fresh run with retry_fired=false (admission refused)", rows[0])
	}

	// Exactly two stub invocations ever happened — the refused retry must
	// not have spawned an agent.
	if n := counterLines(t, counter); n != 2 {
		t.Errorf("stub invocations = %d, want 2 (slot holder + false-stopped run only)", n)
	}

	// The transcript says WHY nothing came back: one daemon-authored,
	// labeled advisory naming the drop reason.
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: conv2, AfterSeq: 0}).Events
	advisories := 0
	advisory := ""
	for _, ev := range events {
		if ev.Type != store.EventAgentError {
			continue
		}
		var p struct {
			Error string `json:"error"`
			Odo   bool   `json:"odo"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("event %d: %v", ev.ID, err)
		}
		if p.Odo {
			advisories++
			advisory = p.Error
		}
	}
	if advisories != 1 || !strings.Contains(advisory, "could not start") || !strings.Contains(advisory, "concurrency_cap") {
		t.Errorf("advisories = %d, last = %q, want one labeled admission refusal naming concurrency_cap", advisories, advisory)
	}
}

// phantomDiffStub leaves a real diff (hello.txt) but writes zero output —
// the belt-and-suspenders case: the diff branch must journal the verdict
// (no retry: a diff exists) and auto-land must hard-block it.
func phantomDiffStub(counterFile string) string {
	return fmt.Sprintf(`#!/bin/sh
printf 'x\n' >> %q
prompt_file="$2"
output_file="$3"
sleep 1
cp "$prompt_file" hello.txt
: > "$output_file"
exit 0
`, counterFile)
}

// TestPhantomDiffVerdictBlocksAutoLand: a zero-output run that still left a
// diff never auto-lands — the human reviews it. One verdict row, one
// blocked row, one invocation (no retry from the diff path).
func TestPhantomDiffVerdictBlocksAutoLand(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "auto_apply: main\n")
	counter := filepath.Join(t.TempDir(), "invocations")
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, phantomDiffStub(counter)))
	rig := startRig(t, root)
	defer rig.stop(t)
	rig.server.autoLandDone = make(chan struct{}, 1)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "phantom-diff-token"})
	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff — the phantom run must leave its side effect reviewable")
	}

	select {
	case <-rig.server.autoLandDone:
	case <-time.After(10 * time.Second):
		t.Fatal("auto-land pipeline never finished")
	}
	if got := blockedReasons(t, rig.store, convID); len(got) != 1 || got[0] != "run_false_stop" {
		t.Fatalf("blocked reasons = %v, want [run_false_stop]", got)
	}
	rows := verdictRows(t, rig.store, convID)
	if len(rows) != 1 || rows[0]["verdict"] != verdictFalseStop || rows[0]["retry_fired"] != false {
		t.Fatalf("verdict rows = %v, want one false_stop with retry_fired=false", rows)
	}
	time.Sleep(2500 * time.Millisecond)
	if n := counterLines(t, counter); n != 1 {
		t.Fatalf("stub invocations = %d, want 1 — the diff path never retries", n)
	}
}

// TestVerdictBlocksUnit pins the gate order at the unit seam: a verdicted
// producing run is blocked BEFORE any verify/panel spend, both classes.
func TestVerdictBlocksUnit(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := f.addDiff(t, "p.diff", patchSrc("src/a.go", 1, 1, false))
	s.autoLand(context.Background(), d, t.TempDir(), "goal", false, verdictNoText)
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "run_no_text" {
		t.Fatalf("no_text reasons = %v, want [run_no_text]", got)
	}

	f2 := newAutonomyFixture(t)
	s2 := &Server{store: f2.st, projectRoot: root}
	d2 := f2.addDiff(t, "p.diff", patchSrc("src/a.go", 1, 1, false))
	s2.autoLand(context.Background(), d2, t.TempDir(), "goal", false, verdictFalseStop)
	if got := blockedReasons(t, f2.st, f2.c.ID); len(got) != 1 || got[0] != "run_false_stop" {
		t.Fatalf("false_stop reasons = %v, want [run_false_stop]", got)
	}
}
