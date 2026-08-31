package ipc

// D9-W3a run_usage pins: exactly one measured-cost receipt per drained
// REGULAR run (loop runs carry D3's loop_run_usage instead), fail-soft
// everywhere — a missing transcript or a non-OMP adapter degrades to
// usage_available:false + reason, never fabricated numbers, and a rig
// re-draining a finished run must not double-journal.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

// runUsageReceipts counts the conversation's run_usage rows
// (memory_update{layer:"run_usage"}) and returns the last payload.
func runUsageReceipts(t *testing.T, st *store.Store, convID int64) (int, map[string]interface{}) {
	t.Helper()
	events, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	var last map[string]interface{}
	for _, ev := range events {
		var p map[string]interface{}
		if ev.Type == store.EventMemoryUpdate && json.Unmarshal(ev.Payload, &p) == nil && p["layer"] == "run_usage" {
			n++
			last = p
		}
	}
	return n, last
}

// TestJournalRunUsageFailSoft: no session transcript ⇒ the row degrades
// honestly (usage_available:false + reason) instead of vanishing or
// fabricating zeros.
func TestJournalRunUsageFailSoft(t *testing.T) {
	t.Parallel()
	f := newAutonomyFixture(t)
	mgr := worktree.NewManager(f.dir)
	if err := mgr.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: f.st, projectRoot: f.dir, mgr: mgr}
	meta := &runMeta{runID: "run-missing", conversationID: f.c.ID}

	s.journalRunUsage(context.Background(), meta)

	n, row := runUsageReceipts(t, f.st, f.c.ID)
	if n != 1 {
		t.Fatalf("run_usage rows = %d, want 1", n)
	}
	if row["usage_available"] != false {
		t.Fatalf("missing transcript must journal unavailable: %v", row)
	}
	if row["reason"] == nil || row["reason"] == "" {
		t.Fatalf("unavailable row must name its reason: %v", row)
	}
	if row["input_tokens"] != nil {
		t.Fatalf("fabricated numbers on an unavailable row: %v", row)
	}
	if row["run_id"] != "run-missing" {
		t.Fatalf("run_id: %v", row)
	}
}

// TestJournalRunUsageMeasured: a session transcript sums into the row
// (adapter.SessionUsage's own wire-shape pins live in the adapter; this
// pins the RECEIPT wiring).
func TestJournalRunUsageMeasured(t *testing.T) {
	t.Parallel()
	f := newAutonomyFixture(t)
	mgr := worktree.NewManager(f.dir)
	if err := mgr.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: f.st, projectRoot: f.dir, mgr: mgr}
	meta := &runMeta{runID: "run-measured", conversationID: f.c.ID}

	dir := filepath.Join(mgr.StateDir(), "sessions", meta.runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := `{"message":{"role":"assistant","usage":{"input":100,"output":50,"cacheRead":10,"cacheWrite":5,"totalTokens":165,"cost":{"total":0.01}}}}
{"message":{"role":"assistant","usage":{"input":300,"output":60,"cacheRead":0,"cacheWrite":8,"totalTokens":368,"cost":{"total":0.02}}}}
`
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	s.journalRunUsage(context.Background(), meta)

	n, row := runUsageReceipts(t, f.st, f.c.ID)
	if n != 1 {
		t.Fatalf("run_usage rows = %d, want 1", n)
	}
	if row["usage_available"] != true {
		t.Fatalf("measured transcript must journal available: %v", row)
	}
	if row["input_tokens"] != float64(400) || row["output_tokens"] != float64(110) ||
		row["cache_read_tokens"] != float64(10) || row["cache_write_tokens"] != float64(13) {
		t.Fatalf("usage sum = %v, want 400/110/10/13", row)
	}
	if row["cost_usd"] != float64(0.03) {
		t.Fatalf("cost_usd = %v, want 0.03", row["cost_usd"])
	}
	if row["reason"] != nil {
		t.Fatalf("measured row must not carry a failure reason: %v", row)
	}
}

// TestDrainRunUsageExactlyOnce: a full drain of a regular (non-loop) run
// journals EXACTLY one receipt, and re-draining the finished run (rig
// polls re-enter drainRun) journals no second one.
func TestDrainRunUsageExactlyOnce(t *testing.T) {
	t.Setenv("ODO_REGISTRY_PATH", filepath.Join(t.TempDir(), "projects.json"))
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "auto_apply: main\n") // panel-less: no auto-land arms
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, `#!/bin/sh
cp "$2" hello.txt
output_file="$3"
printf 'file created\n' > "$output_file"
exit 0
`))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "create a file"})
	rig.pollUntilDone(t, convID)
	// A diff-producing run does not retire at drain: its finished meta
	// stays registered (the diff waits on review).
	rig.server.mu.Lock()
	var finished *runMeta
	for _, meta := range rig.server.runs {
		if meta.conversationID == convID && meta.finished {
			finished = meta
			break
		}
	}
	rig.server.mu.Unlock()
	if finished == nil {
		t.Fatal("finished run meta not retained")
	}
	// Re-drain the finished run a few times: the exactly-once flag must pin.
	for i := 0; i < 3; i++ {
		if err := rig.server.drainRun(context.Background(), finished); err != nil {
			t.Fatal(err)
		}
	}

	if n, row := runUsageReceipts(t, rig.store, convID); n != 1 {
		t.Fatalf("run_usage rows = %d, want exactly 1 across re-drains (last: %v)", n, row)
	}
}

// TestRunUsageReceiptGate pins the drainRun defer's predicate: regular
// finished runs owe one receipt; unfinished runs, loop runs, and an
// already-receipted run owe none.
func TestRunUsageReceiptGate(t *testing.T) {
	t.Parallel()
	var m runMeta
	if m.runUsageReceiptDue() {
		t.Fatal("unfinished run owes no receipt")
	}
	m.finished = true
	if !m.runUsageReceiptDue() {
		t.Fatal("finished regular run owes a receipt")
	}
	m.runUsageJournaled = true
	if m.runUsageReceiptDue() {
		t.Fatal("already-receipted run owes no second row")
	}
	m.runUsageJournaled = false
	m.loopID = 7
	if m.runUsageReceiptDue() {
		t.Fatal("loop runs carry loop_run_usage instead — no run_usage twin")
	}
}
