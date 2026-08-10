package ipc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// M12 (D-auto) tests: daemon-side auto-distill scheduler (T1-T3 triggers),
// eligibility/frequency/backoff gates, cancel-before-note, the slash gate,
// coverage honesty, the conditional auto-curate, and the pending_counts
// disclosure surface. Rigs opt in via enableAuto (startRig dark-launches
// the subsystem to keep pre-M12 journals byte-stable).

// enableAuto arms the subsystem on a rig and speeds the clock seams.
// idle==0 keeps the prefs-resolved idle (defaults to 120s).
func enableAuto(rig *testRig, idle, jitter, curateAge time.Duration) {
	rig.server.autoDisabled = false
	rig.server.autoIdle = idle
	if jitter >= 0 {
		rig.server.autoJitter = jitter
	}
	rig.server.autoCurateAge = curateAge
}

// journalWindow appends n user_message events of ~bytes payload each —
// the eligibility/urgency fixtures without paying for agent runs.
func journalWindow(t *testing.T, rig *testRig, convID int64, n, bytes int) {
	t.Helper()
	ctx := context.Background()
	text := strings.Repeat("x", bytes)
	for i := 0; i < n; i++ {
		if _, err := rig.store.AppendEvent(ctx, convID, "user_message", mustJSON(map[string]interface{}{"text": text})); err != nil {
			t.Fatalf("journal window event %d: %v", i, err)
		}
	}
}

// journalPanelAnswer appends one /panel-style agent_text (payload-flagged
// advisory — excluded from eligibility bytes).
func journalPanelAnswer(t *testing.T, rig *testRig, convID int64, bytes int) {
	t.Helper()
	if _, err := rig.store.AppendEvent(context.Background(), convID, "agent_text", mustJSON(map[string]interface{}{
		"text":  strings.Repeat("y", bytes),
		"panel": true,
	})); err != nil {
		t.Fatalf("journal panel answer: %v", err)
	}
}

// journalMarker appends one review_action payload verbatim (marker
// fixtures: distill triggers, curate passes).
func journalMarker(t *testing.T, rig *testRig, convID int64, payload map[string]interface{}) {
	t.Helper()
	if _, err := rig.store.AppendEvent(context.Background(), convID, "review_action", mustJSON(payload)); err != nil {
		t.Fatalf("journal marker: %v", err)
	}
}

// journalAutoFailure appends one auto_distill{failed} memory_update.
func journalAutoFailure(t *testing.T, rig *testRig, convID int64, detail string) {
	t.Helper()
	if _, err := rig.store.AppendEvent(context.Background(), convID, "memory_update", mustJSON(map[string]interface{}{
		"layer": "auto_distill", "cause": "failed", "detail": detail,
	})); err != nil {
		t.Fatalf("journal auto failure: %v", err)
	}
}

// autoRows returns the conversation's journaled memory_update
// {layer:auto_distill} payloads, optionally narrowed to one cause.
func autoRows(t *testing.T, rig *testRig, convID int64, cause string) []map[string]interface{} {
	t.Helper()
	events, err := rig.store.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var out []map[string]interface{}
	for _, row := range memoryUpdatesByCause(t, events, cause) {
		if row["layer"] == "auto_distill" {
			out = append(out, row)
		}
	}
	return out
}

// waitForCond polls cond until timeout, failing the test with desc.
func waitForCond(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out (%s) waiting for %s", timeout, desc)
}

// pendingEntry snapshots the (possibly nil) pending auto-distill for convID.
func pendingEntry(srv *Server, convID int64) *autoPendingEntry {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return srv.autoPending[convID]
}

// armPendingNow installs a pending entry and returns immediately, so the
// test can drive the fire path synchronously via runAutoDistill.
func armPendingNow(srv *Server, convID int64, trigger string) {
	srv.mu.Lock()
	srv.autoPending[convID] = &autoPendingEntry{
		trigger: trigger,
		fireAt:  time.Now(),
		timer:   time.AfterFunc(time.Hour, func() {}),
	}
	srv.mu.Unlock()
}

// autoSlowLearnerWrapper sleeps inside the LEARNER one-shot instead: the
// fold parks in its COMMITTED phase (post-checkpoint, pre-marker) for ~3s,
// so P1-2 tests can drive sends / synthetic journal growth at a
// deterministic point of the fold. The distill one-shot and agent runs
// stay fast.
const autoSlowLearnerWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
if grep -q "memory learner pass" "$prompt_file"; then
  sleep 3
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

// waitCommitted blocks until the conversation's in-flight auto fold has
// passed the pre-note checkpoint (committed=true — the start of the slow
// learner window in the P1-2 tests).
func waitCommitted(t *testing.T, srv *Server, convID int64) {
	t.Helper()
	waitForCond(t, 10*time.Second, "fold committed (post-checkpoint)", func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		ifl := srv.autoInFlight[convID]
		return ifl != nil && ifl.committed
	})
}

// autoSlowDistillWrapper sleeps inside the distill one-shot, widening the
// cancel-before-note / slash-gate race windows; agent runs stay 1s.
const autoSlowDistillWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
if grep -q "Summarize the key decisions" "$prompt_file"; then
  sleep 3
  cat "$ODO_DISTILL_OUTPUT" > "$output_file"
  exit 0
fi
sleep 1
cp "$prompt_file" hello.txt
printf 'Created hello.txt as requested.\n' > "$output_file"
exit 0
`

// TestAutoIdleSchedulesAfterRunFinish (T1): a run finishing on an eligible
// window arms exactly one idle timer (driven through the real drainRun
// path), repeated evaluation never double-schedules, and a send disarms
// the pending schedule (journaled).
func TestAutoIdleSchedulesAfterRunFinish(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0) // prefs idle (120s default)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000) // ≥6 events, ≥16KB eligible

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.pollUntilDone(t, convID)

	if rows := autoRows(t, rig, convID, "scheduled"); len(rows) != 1 {
		t.Fatalf("scheduled rows = %d, want exactly 1: %v", len(rows), rows)
	} else if !strings.Contains(rows[0]["detail"].(string), "trigger=idle") {
		t.Errorf("scheduled detail = %v, want trigger=idle", rows[0]["detail"])
	}
	entry := pendingEntry(rig.server, convID)
	if entry == nil {
		t.Fatal("no pending auto-distill after run finish")
	}
	if entry.trigger != distillTriggerIdle {
		t.Errorf("pending trigger = %q, want idle", entry.trigger)
	}
	if eta := time.Until(entry.fireAt); eta < 100*time.Second || eta > 125*time.Second {
		t.Errorf("fire eta = %s, want ≈120s (prefs default)", eta.Round(time.Second))
	}

	// No double-schedule: a second finish-level evaluation is a no-op.
	rig.server.mu.Lock()
	rig.server.maybeAutoAfterActivityLocked(context.Background(), convID)
	rig.server.mu.Unlock()
	if rows := autoRows(t, rig, convID, "scheduled"); len(rows) != 1 {
		t.Fatalf("scheduled rows after re-evaluation = %d, want 1", len(rows))
	}

	// pending_counts exposes the countdown.
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if len(pc.AutoDistill) != 1 || pc.AutoDistill[0].ConversationID != convID ||
		pc.AutoDistill[0].Trigger != distillTriggerIdle || pc.AutoDistill[0].EtaUnix < time.Now().Unix() {
		t.Errorf("pending_counts auto_distill = %+v, want one idle entry for conv %d with a future eta", pc.AutoDistill, convID)
	}

	// A send disarms the pending schedule (journaled, then proceeds).
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt again"})
	if got := pendingEntry(rig.server, convID); got != nil {
		t.Errorf("pending entry survived the send: %+v", got)
	}
	rows := autoRows(t, rig, convID, "skipped")
	found := false
	for _, row := range rows {
		if strings.Contains(row["detail"].(string), "disarmed_by_send") {
			found = true
		}
	}
	if !found {
		t.Errorf("no skipped{disarmed_by_send} row after send: %v", rows)
	}
	pc = rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	for _, a := range pc.AutoDistill {
		if a.ConversationID == convID {
			t.Errorf("pending_counts still reports a countdown for conv %d after disarm", convID)
		}
	}
	rig.pollUntilDone(t, convID)
}

// TestAutoStartupCompensation (T2): booting with an eligible, stale
// (post-close) window arms exactly one startup trigger — and a second scan
// never double-arms.
func TestAutoStartupCompensation(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	// 1ms idle: every journaled window is stale. 1h jitter cap: the armed
	// timer stays observable instead of firing before the assertions.
	enableAuto(rig, time.Millisecond, time.Hour, 0)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	if err := rig.server.StartupAutoScan(context.Background()); err != nil {
		t.Fatalf("StartupAutoScan: %v", err)
	}
	rows := autoRows(t, rig, convID, "scheduled")
	if len(rows) != 1 || !strings.Contains(rows[0]["detail"].(string), "trigger=startup") {
		t.Fatalf("startup scheduled rows = %v, want exactly one trigger=startup", rows)
	}
	if pendingEntry(rig.server, convID) == nil {
		t.Fatal("no pending entry after startup scan")
	}

	// Idempotent: a second scan must not double-arm or double-journal.
	if err := rig.server.StartupAutoScan(context.Background()); err != nil {
		t.Fatalf("StartupAutoScan #2: %v", err)
	}
	if rows := autoRows(t, rig, convID, "scheduled"); len(rows) != 1 {
		t.Fatalf("scheduled rows after second scan = %d, want 1", len(rows))
	}
}

// TestAutoUrgentFiresImmediately (T3): a window over the urgent byte
// threshold fires without idle — trigger=urgent on every journal row and
// the distill marker.
func TestAutoUrgentFiresImmediately(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Distilled\n\nUrgent fold.\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 25000) // ≈150KB rendered ≥ 128KB urgent

	rig.server.mu.Lock()
	rig.server.maybeAutoAfterActivityLocked(context.Background(), convID)
	rig.server.mu.Unlock()

	entry := pendingEntry(rig.server, convID)
	if entry == nil {
		t.Fatal("urgent window did not arm")
	}
	if entry.trigger != distillTriggerUrgent {
		t.Errorf("pending trigger = %q, want urgent", entry.trigger)
	}
	if eta := time.Until(entry.fireAt); eta > 2*time.Second {
		t.Errorf("urgent eta = %s, want immediate (no idle)", eta)
	}

	// The 0-delay timer fires on its own goroutine.
	waitForCond(t, 15*time.Second, "urgent distill marker", func() bool {
		for _, m := range payloadsByAction(t, allEvents(t, rig, convID), "distill") {
			if m["trigger"] == distillTriggerUrgent {
				return true
			}
		}
		return false
	})
	if rows := autoRows(t, rig, convID, "fired"); len(rows) != 1 {
		t.Fatalf("fired rows = %v, want exactly 1", rows)
	}
	if pendingEntry(rig.server, convID) != nil {
		t.Error("pending entry leaked past the fire")
	}
	// The marker carries trigger + measured window stats (M12 provenance).
	var marker map[string]interface{}
	for _, m := range payloadsByAction(t, allEvents(t, rig, convID), "distill") {
		if m["trigger"] == distillTriggerUrgent {
			marker = m
		}
	}
	// window stats cover the whole folded journal — the 6 user events plus
	// the scheduler's own small rows — never fewer than the user window.
	if ev, ok := marker["window_events"].(float64); !ok || ev < 6 {
		t.Errorf("marker window_events = %v, want ≥6", marker["window_events"])
	}
	if b, ok := marker["window_bytes"].(float64); !ok || b < 128*1024 {
		t.Errorf("marker window_bytes = %v, want ≥128KB", marker["window_bytes"])
	}
	if _, err := os.Stat(filepath.Join(root, "wiki", "main-epoch-1.md")); err != nil {
		t.Errorf("urgent distill wrote no note: %v", err)
	}
}

// allEvents lists the full journal for a conversation.
func allEvents(t *testing.T, rig *testRig, convID int64) []store.Event {
	t.Helper()
	events, err := rig.store.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	return events
}

// TestAutoEligibility covers the two threshold gates and the panel/vision
// byte exclusion: a 30KB /panel answer must not trigger a fold by itself.
func TestAutoEligibility(t *testing.T) {
	// Unit: measureWindow excludes payload-flagged advisory answers.
	mk := func(payload string) store.Event {
		return store.Event{Type: "agent_text", Payload: []byte(payload)}
	}
	window := []store.Event{
		mk(`{"text":"big","panel":true}`),
		mk(`{"text":"bigger","vision":true}`),
		mk(`{"text":"counts"}`),
	}
	stats := measureWindow(window)
	if stats.events != 3 {
		t.Errorf("measureWindow events = %d, want 3", stats.events)
	}
	wantBytes := len("agent_text") + len(`{"text":"counts"}`) + 64
	if stats.eligibleBytes != wantBytes {
		t.Errorf("measureWindow eligibleBytes = %d, want %d (panel+vision excluded)", stats.eligibleBytes, wantBytes)
	}

	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	evaluate := func() {
		rig.server.mu.Lock()
		rig.server.maybeAutoAfterActivityLocked(context.Background(), convID)
		rig.server.mu.Unlock()
	}

	// Below min events: 1 huge event is below both gates; events wins.
	journalWindow(t, rig, convID, 1, 20000)
	evaluate()
	rows := autoRows(t, rig, convID, "skipped")
	if len(rows) != 1 || !strings.Contains(rows[0]["detail"].(string), "below_min_events") {
		t.Fatalf("skips = %v, want 1 below_min_events", rows)
	}
	if pendingEntry(rig.server, convID) != nil {
		t.Fatal("armed despite below_min_events")
	}

	// Panel bytes excluded: fold phase 1 away (marker resets the window),
	// then 5 small user messages + one 20KB flagged panel answer = 6 events
	// but ~5.5KB eligible bytes — below the 16KB floor.
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "manual"})
	journalWindow(t, rig, convID, 5, 1000)
	journalPanelAnswer(t, rig, convID, 20000)
	evaluate()
	rows = autoRows(t, rig, convID, "skipped")
	last := rows[len(rows)-1]["detail"].(string)
	if !strings.Contains(last, "below_min_bytes") {
		t.Errorf("last skip = %q, want below_min_bytes (panel bytes must not count)", last)
	}
	if pendingEntry(rig.server, convID) != nil {
		t.Fatal("armed on panel-inflated window")
	}

	// Unflagged advisory bytes DO count: one plain 20KB agent_text makes
	// the window eligible (6+ events, ~26KB plain bytes).
	if _, err := rig.store.AppendEvent(context.Background(), convID, "agent_text",
		mustJSON(map[string]interface{}{"text": strings.Repeat("z", 20000)})); err != nil {
		t.Fatal(err)
	}
	evaluate()
	if pendingEntry(rig.server, convID) == nil {
		t.Error("eligible window (plain agent_text bytes) did not arm")
	}
	rows = autoRows(t, rig, convID, "scheduled")
	if len(rows) != 1 {
		t.Errorf("scheduled rows = %d, want 1 after eligibility", len(rows))
	}
}

// TestAutoFrequencyCaps: the ≤2/hour/conversation and ≤12/day/project caps
// skip + journal; manual markers never count toward either cap. Markers
// precede the window because a distill marker folds the journal — the
// eligible re-window must come after them.
func TestAutoFrequencyCaps(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Note\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Two auto markers this hour (hourly cap 2) + manual markers around
	// them (exempt by construction), then the fresh eligible window.
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "idle"})
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "manual"})
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "urgent"})
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "manual"})
	journalWindow(t, rig, convID, 6, 3000)

	armPendingNow(rig.server, convID, distillTriggerIdle)
	rig.server.runAutoDistill(convID, distillTriggerIdle)
	rows := autoRows(t, rig, convID, "skipped")
	if n := len(rows); n == 0 || !strings.Contains(rows[n-1]["detail"].(string), "hourly_cap") {
		t.Fatalf("skips = %v, want hourly_cap", rows)
	}
	if pendingEntry(rig.server, convID) != nil {
		t.Error("cap skip left a pending entry")
	}

	// Daily cap: prefs override (12 → 1); the two auto markers above hit it.
	writePrefs(t, home, "auto_distill_daily_cap: 1\nauto_distill_max_per_hour: 99\n")
	armPendingNow(rig.server, convID, distillTriggerIdle)
	rig.server.runAutoDistill(convID, distillTriggerIdle)
	rows = autoRows(t, rig, convID, "skipped")
	if n := len(rows); !strings.Contains(rows[n-1]["detail"].(string), "daily_cap") {
		t.Fatalf("skips = %v, want daily_cap", rows)
	}

	// Manual exemption, clean journal: three manual markers must not trip
	// the hourly cap (2/hour) — the fire proceeds to a real distill.
	root2 := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig2 := startRig(t, root2)
	defer rig2.stop(t)
	enableAuto(rig2, 0, -1, 0)
	boot2 := rig2.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root2})
	conv2 := boot2.Conversation.ID
	for range 3 {
		journalMarker(t, rig2, conv2, map[string]interface{}{"action": "distill", "trigger": "manual"})
	}
	journalWindow(t, rig2, conv2, 6, 3000)
	armPendingNow(rig2.server, conv2, distillTriggerIdle)
	rig2.server.runAutoDistill(conv2, distillTriggerIdle)
	if rows := autoRows(t, rig2, conv2, "fired"); len(rows) != 1 {
		t.Fatalf("fired rows = %v, want 1 (manual markers must not trip the cap)", rows)
	}
}

// retryAfterWithin reports whether detail contains a backoff retry_after=dur
// whose parsed duration falls within (step-10s, step]. The journaled value is
// the remaining time at journal-write moment, which decays below the nominal
// step by the time the assertion runs — exact-string matching flakes.
func retryAfterWithin(detail string, step time.Duration) bool {
	const prefix = "backoff retry_after="
	i := strings.Index(detail, prefix)
	if i < 0 {
		return false
	}
	rest := detail[i+len(prefix):]
	if j := strings.IndexAny(rest, " \t"); j >= 0 {
		rest = rest[:j]
	}
	d, err := time.ParseDuration(rest)
	if err != nil {
		return false
	}
	return d > step-10*time.Second && d <= step
}

// TestAutoBackoffFromJournal: consecutive failed rows impose 5m → 30m → 2h
// → suspended backoff (journaled skips with retry re-arms), and the next
// user event clears the streak entirely.
func TestAutoBackoffFromJournal(t *testing.T) {
	// Unit: the step ladder itself is the M12 contract.
	wantSteps := []time.Duration{5 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	if len(autoBackoffSteps) != len(wantSteps) {
		t.Fatalf("autoBackoffSteps = %v, want %v", autoBackoffSteps, wantSteps)
	}
	for i := range wantSteps {
		if autoBackoffSteps[i] != wantSteps[i] {
			t.Errorf("autoBackoffSteps[%d] = %s, want %s", i, autoBackoffSteps[i], wantSteps[i])
		}
	}

	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Note\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	// One failure → 5m backoff, re-armed ≈5m out, journaled.
	journalAutoFailure(t, rig, convID, "boom 1")
	armPendingNow(rig.server, convID, distillTriggerIdle)
	rig.server.runAutoDistill(convID, distillTriggerIdle)
	rows := autoRows(t, rig, convID, "skipped")
	if n := len(rows); n == 0 || !retryAfterWithin(rows[n-1]["detail"].(string), 5*time.Minute) {
		t.Fatalf("skips = %v, want backoff retry_after≈5m", rows)
	}
	if entry := pendingEntry(rig.server, convID); entry == nil ||
		time.Until(entry.fireAt) < 4*time.Minute || time.Until(entry.fireAt) > 6*time.Minute {
		t.Errorf("backoff re-arm = %+v, want ≈5m", entry)
	}
	// The re-armed fire is a no-op second attempt in this test; disarm it.
	rig.server.mu.Lock()
	rig.server.disarmAutoLocked(context.Background(), convID, "test cleanup")
	rig.server.mu.Unlock()

	// Three consecutive failures → 2h backoff.
	journalAutoFailure(t, rig, convID, "boom 2")
	journalAutoFailure(t, rig, convID, "boom 3")
	armPendingNow(rig.server, convID, distillTriggerIdle)
	rig.server.runAutoDistill(convID, distillTriggerIdle)
	rows = autoRows(t, rig, convID, "skipped")
	if n := len(rows); !retryAfterWithin(rows[n-1]["detail"].(string), 2*time.Hour) {
		t.Fatalf("skips = %v, want backoff retry_after≈2h", rows)
	}
	rig.server.mu.Lock()
	rig.server.disarmAutoLocked(context.Background(), convID, "test cleanup")
	rig.server.mu.Unlock()

	// Four failures → suspended until the next user event; no re-arm.
	journalAutoFailure(t, rig, convID, "boom 4")
	armPendingNow(rig.server, convID, distillTriggerIdle)
	rig.server.runAutoDistill(convID, distillTriggerIdle)
	rows = autoRows(t, rig, convID, "skipped")
	if n := len(rows); !strings.Contains(rows[n-1]["detail"].(string), "backoff_suspended") {
		t.Fatalf("skips = %v, want backoff_suspended", rows)
	}
	if pendingEntry(rig.server, convID) != nil {
		t.Error("suspended backoff re-armed")
	}

	// The next user event clears the streak: the very next evaluation fires.
	if _, err := rig.store.AppendEvent(context.Background(), convID, "user_message",
		mustJSON(map[string]interface{}{"text": "resume"})); err != nil {
		t.Fatal(err)
	}
	armPendingNow(rig.server, convID, distillTriggerIdle)
	rig.server.runAutoDistill(convID, distillTriggerIdle)
	if rows := autoRows(t, rig, convID, "fired"); len(rows) != 1 {
		t.Fatalf("fired rows = %v, want 1 after user event cleared suspension", rows)
	}
}

// TestAutoDistillFailureJournals: a failed auto distill lands a journaled
// failed row (the backoff scan's input) — never just a daemon.log line.
func TestAutoDistillFailureJournals(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	// No ODO_DISTILL_OUTPUT: the stub writes an empty note → the one-shot
	// fails with "run produced no output".
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	armPendingNow(rig.server, convID, distillTriggerIdle)
	rig.server.runAutoDistill(convID, distillTriggerIdle)
	rows := autoRows(t, rig, convID, "failed")
	if len(rows) != 1 || !strings.Contains(rows[0]["detail"].(string), "trigger=idle") {
		t.Fatalf("failed rows = %v, want 1 with trigger=idle", rows)
	}
	if markers := payloadsByAction(t, allEvents(t, rig, convID), "distill"); len(markers) != 0 {
		t.Errorf("failed distill left markers: %v", markers)
	}
}

// TestAutoCancelBeforeNote: a send arriving mid-auto-distill cancels it
// before the note write (journaled cancelled_by_send), then proceeds
// normally — no refusal, no note, no marker, epoch unmoved.
func TestAutoCancelBeforeNote(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, autoSlowDistillWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Should never land\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	armPendingNow(rig.server, convID, distillTriggerIdle)
	done := make(chan struct{})
	go func() {
		rig.server.runAutoDistill(convID, distillTriggerIdle)
		close(done)
	}()
	// The stub sleeps 3s inside the distill one-shot; the send lands
	// mid-flight.
	waitForCond(t, 5*time.Second, "distill in flight", func() bool {
		rig.server.mu.Lock()
		defer rig.server.mu.Unlock()
		return rig.server.autoInFlight[convID] != nil
	})
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"}) // must not refuse
	<-done

	rows := autoRows(t, rig, convID, "cancelled_by_send")
	if len(rows) != 1 {
		t.Fatalf("cancelled_by_send rows = %v, want exactly 1", rows)
	}
	if markers := payloadsByAction(t, allEvents(t, rig, convID), "distill"); len(markers) != 0 {
		t.Errorf("cancelled auto distill left a marker: %v", markers)
	}
	if _, err := os.Stat(filepath.Join(root, "wiki", "main-epoch-1.md")); !os.IsNotExist(err) {
		t.Error("cancelled auto distill wrote a note (cancel-before-note violated)")
	}
	if c, err := rig.store.GetConversation(context.Background(), convID); err != nil || c.Epoch != 1 {
		t.Errorf("epoch = %d, want 1 (cancelled fold must not move the epoch)", c.Epoch)
	}
	// The send proceeded normally.
	rig.pollUntilDone(t, convID)
}

// TestManualDistillGatesUnchanged: manual distill keeps let-finish +
// refusal — sends, steers, /panel and /vision are all refused with the
// historical error text — and the marker gains trigger:"manual" with the
// measured window stats.
func TestManualDistillGatesUnchanged(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, autoSlowDistillWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Manual fold\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	distill := asyncCall(rig, Request{Cmd: CmdDistill, ConversationID: convID})
	waitForCond(t, 5*time.Second, "manual distill in flight", func() bool {
		rig.server.mu.Lock()
		defer rig.server.mu.Unlock()
		return rig.server.distillKind[convID] == distillTriggerManual
	})

	for _, tc := range []struct {
		name  string
		text  string
		steer bool
	}{
		{"send", "Create hello.txt", false},
		{"steer", "follow up", true},
		{"/panel", "/panel analyze this", false},
		{"/vision", "/vision what is here", false},
	} {
		resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: tc.text, Steer: tc.steer})
		if !strings.Contains(resp.Error, "distill in progress") {
			t.Errorf("%s during manual distill: error = %q, want the historical refusal", tc.name, resp.Error)
		}
	}

	res := <-distill
	requireOK(t, "manual distill", res)
	var marker map[string]interface{}
	for _, m := range payloadsByAction(t, allEvents(t, rig, convID), "distill") {
		marker = m
	}
	if marker["trigger"] != distillTriggerManual {
		t.Errorf("manual marker = %v, want trigger manual", marker)
	}
	if marker["window_events"] != float64(6) {
		t.Errorf("manual marker window_events = %v, want 6", marker["window_events"])
	}
	if b, ok := marker["window_bytes"].(float64); !ok || b < 16*1024 {
		t.Errorf("manual marker window_bytes = %v, want ≥16KB", marker["window_bytes"])
	}
}

// TestSlashGateDistillRefusesDuringSlash: the reciprocal half of the slash
// gate — a live /panel or /vision slot refuses a distill (previously the
// slash answer could be folded into last_seq unseen).
func TestSlashGateDistillRefusesDuringSlash(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Note\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	rig.server.mu.Lock()
	rig.server.slashing[convID] = 1
	rig.server.mu.Unlock()

	resp := rig.callExpectErr(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if !strings.Contains(resp.Error, "slash query in progress") {
		t.Errorf("distill during slash: error = %q, want slash query refusal", resp.Error)
	}

	// The last slash release re-evaluates auto-distill (slash answers grew
	// the window): the timer arms.
	rig.server.releaseSlashSlot(context.Background(), convID)
	if pendingEntry(rig.server, convID) == nil {
		t.Error("slash completion did not arm an idle auto-distill on the eligible window")
	}
}

// TestAutoCoverageHonesty: an auto fold whose prompt would drop oldest
// events never fires — skipped + journaled + surfaced via pending_counts
// until an honest (manual) fold clears the block.
func TestAutoCoverageHonesty(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Honest fold\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// 7×40KB ≈ 280KB > the 256KB prompt budget: capEvents would omit —
	// urgency must not fire, the skip must journal, the badge must show.
	journalWindow(t, rig, convID, 7, 40000)
	rig.server.mu.Lock()
	rig.server.maybeAutoAfterActivityLocked(context.Background(), convID)
	rig.server.mu.Unlock()

	if pendingEntry(rig.server, convID) != nil {
		t.Error("armed a fold whose prompt would omit events")
	}
	rows := autoRows(t, rig, convID, "skipped")
	if len(rows) != 1 || !strings.Contains(rows[0]["detail"].(string), "window_exceeds_prompt_budget") {
		t.Fatalf("skips = %v, want window_exceeds_prompt_budget", rows)
	}
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	foundBlock := false
	for _, a := range pc.AutoDistill {
		if a.ConversationID == convID && a.BlockedReason == "window_exceeds_prompt_budget" {
			foundBlock = true
		}
	}
	if !foundBlock {
		t.Errorf("pending_counts auto_distill = %+v, want the coverage block surfaced", pc.AutoDistill)
	}

	// The manual distill is the honest way out (its prompt declares the
	// omission); a successful fold clears the badge.
	rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	pc = rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	for _, a := range pc.AutoDistill {
		if a.ConversationID == convID && a.BlockedReason != "" {
			t.Errorf("block survived a successful manual fold: %+v", a)
		}
	}
}

// TestAutoDistillCtlDisarms: the composer chip's Cancel — disarm is
// journaled, idempotent in its response, and rejects unknown actions.
func TestAutoDistillCtlDisarms(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	rig.server.mu.Lock()
	rig.server.maybeAutoAfterActivityLocked(context.Background(), convID)
	rig.server.mu.Unlock()
	if pendingEntry(rig.server, convID) == nil {
		t.Fatal("fixture failed to arm")
	}

	resp := rig.call(t, Request{Cmd: CmdAutoDistillCtl, ConversationID: convID, Action: "disarm"})
	if !resp.Disarmed {
		t.Error("first disarm: disarmed=false, want true")
	}
	if pendingEntry(rig.server, convID) != nil {
		t.Error("pending entry survived the ctl disarm")
	}
	rows := autoRows(t, rig, convID, "skipped")
	if n := len(rows); n == 0 || !strings.Contains(rows[n-1]["detail"].(string), "disarmed_by_user") {
		t.Errorf("skips = %v, want disarmed_by_user", rows)
	}

	resp = rig.call(t, Request{Cmd: CmdAutoDistillCtl, ConversationID: convID, Action: "disarm"})
	if resp.Disarmed {
		t.Error("second disarm on nothing-armed: disarmed=true, want false")
	}
	if respErr := rig.callExpectErr(t, Request{Cmd: CmdAutoDistillCtl, ConversationID: convID, Action: "kill"}); !strings.Contains(respErr.Error, "unsupported action") {
		t.Errorf("unknown action error = %q, want unsupported action", respErr.Error)
	}
}

// TestPendingCountsReportsInFlightDistills: the response carries
// distilling + distilling_convs while a distill runs (any kind).
func TestPendingCountsReportsInFlightDistills(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, autoSlowDistillWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Note\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	fresh := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if fresh.Distilling || len(fresh.DistillingConvs) != 0 {
		t.Fatalf("fresh distilling state = %v %v, want quiet", fresh.Distilling, fresh.DistillingConvs)
	}

	distill := asyncCall(rig, Request{Cmd: CmdDistill, ConversationID: convID})
	waitForCond(t, 5*time.Second, "distill in flight", func() bool {
		rig.server.mu.Lock()
		defer rig.server.mu.Unlock()
		_, ok := rig.server.distilling[convID]
		return ok
	})
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if !pc.Distilling {
		t.Error("pending_counts distilling=false mid-distill, want true")
	}
	if len(pc.DistillingConvs) != 1 || pc.DistillingConvs[0] != convID {
		t.Errorf("distilling_convs = %v, want [%d]", pc.DistillingConvs, convID)
	}
	requireOK(t, "manual distill", <-distill)
}

// TestAutoCurateNotesTrigger (conditional, never chained): 4 new distill
// markers since the latest curate fire one auto curate with the trigger +
// input provenance journaled; fewer markers do not.
func TestAutoCurateNotesTrigger(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", curatorStubJSON)
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	projectID := projectIDOf(t, rig, convID)

	// The stub cites exactly these notes — the liveness gate resolves.
	writeNote(t, root, "main-epoch-1", "# Epoch 1\n\nJWT auth.\n")
	writeNote(t, root, "main-epoch-2", "# Epoch 2\n\nToken TTL.\n")
	writeNote(t, root, "feature-epoch-1", "# Epoch 1 (feature)\n\nBoring build.\n")

	markCurates := func(trigger string) map[string]interface{} {
		var found map[string]interface{}
		for _, m := range payloadsByAction(t, allEvents(t, rig, convID), "curate") {
			if m["trigger"] == trigger {
				found = m
			}
		}
		return found
	}

	// 3 markers < min(4): nothing fires.
	for range 3 {
		journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "idle"})
	}
	rig.server.maybeAutoCurate(projectID, convID)
	time.Sleep(300 * time.Millisecond)
	if m := markCurates("auto_notes"); m != nil {
		t.Errorf("curate at 3 notes = %v, want no fire", m)
	}

	// The 4th marker crosses the threshold.
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "idle"})
	rig.server.maybeAutoCurate(projectID, convID)
	waitForCond(t, 10*time.Second, "auto_notes curate marker", func() bool {
		return markCurates("auto_notes") != nil
	})
	m := markCurates("auto_notes")
	if m["gate"] != "pass" {
		t.Errorf("auto_notes marker = %v, want gate pass", m)
	}
	if m["notes_since_last"] != float64(4) {
		t.Errorf("notes_since_last = %v, want 4", m["notes_since_last"])
	}
	if notesRead, ok := m["notes_read"].([]interface{}); !ok || len(notesRead) != 3 {
		t.Errorf("notes_read = %v, want 3 note entries", m["notes_read"])
	}
	if _, err := os.Stat(filepath.Join(root, "wiki", "index.md")); err != nil {
		t.Errorf("auto curate wrote no index: %v", err)
	}
}

// TestAutoCurateAgeTrigger: an old curate (age ≥ threshold) fires even
// under the notes threshold; a fresh curate does not.
func TestAutoCurateAgeTrigger(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", curatorStubJSON)
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, -time.Nanosecond) // every existing curate reads as stale
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	projectID := projectIDOf(t, rig, convID)

	writeNote(t, root, "main-epoch-1", "# Epoch 1\n\nJWT auth.\n")
	writeNote(t, root, "main-epoch-2", "# Epoch 2\n\nToken TTL.\n")
	writeNote(t, root, "feature-epoch-1", "# Epoch 1 (feature)\n\nBoring build.\n")

	// A passing curate marker sits older than the (seam-shrunk) threshold;
	// one distill since — below the notes trigger.
	journalMarker(t, rig, convID, map[string]interface{}{"action": "curate", "gate": "pass"})
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "idle"})
	rig.server.maybeAutoCurate(projectID, convID)
	waitForCond(t, 10*time.Second, "auto_age curate marker", func() bool {
		for _, m := range payloadsByAction(t, allEvents(t, rig, convID), "curate") {
			if m["trigger"] == "auto_age" {
				return true
			}
		}
		return false
	})

	// A never-curated project with <4 notes must NOT treat age as a
	// trigger (no marker exists to be old).
	root2 := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	rig2 := startRig(t, root2)
	defer rig2.stop(t)
	enableAuto(rig2, 0, -1, -time.Nanosecond)
	boot2 := rig2.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root2})
	conv2 := boot2.Conversation.ID
	writeNote(t, root2, "main-epoch-1", "# Epoch 1\n\nOnly note.\n")
	rig2.server.maybeAutoCurate(projectIDOf(t, rig2, conv2), conv2)
	time.Sleep(300 * time.Millisecond)
	for _, m := range payloadsByAction(t, allEvents(t, rig2, conv2), "curate") {
		t.Errorf("never-curated project fired a(curate) at 1 note: %v", m)
	}
}

// TestAutoCurateDeadCitationGate: a citation naming no on-disk note (or an
// ambiguous bare one) skips the WHOLE curate before any write — the old
// generation survives, gate_failed is journaled with the dead tokens.
func TestAutoCurateDeadCitationGate(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", `{"topics":[
	  {"title":"Authentication","slug":"authentication","bullets":["- claim (epoch-1)"]}
	]}`)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// main-epoch-1 AND feature-epoch-1 both exist: the bare (epoch-1)
	// citation collides across workstreams — it must NEVER resolve
	// silently to the wrong one.
	writeNote(t, root, "main-epoch-1", "# Epoch 1 (main)\n\nJWT auth.\n")
	writeNote(t, root, "feature-epoch-1", "# Epoch 1 (feature)\n\nAlso epoch 1.\n")
	oldPage := "# Old Topic\n\n- keep me\n"
	oldPath := filepath.Join(root, "wiki", "topics", "old.md")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte(oldPage), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := rig.callExpectErr(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})
	if !strings.Contains(resp.Error, "dead citation") {
		t.Errorf("curate error = %q, want dead citation", resp.Error)
	}
	if got := readFileStr(t, oldPath); got != oldPage {
		t.Errorf("old topic page = %q, want it intact (gate skips before the stale-clear)", got)
	}
	if _, err := os.Stat(filepath.Join(root, "wiki", "topics", "authentication.md")); !os.IsNotExist(err) {
		t.Error("dead-cited topic page landed despite the gate")
	}

	var marker map[string]interface{}
	for _, m := range payloadsByAction(t, allEvents(t, rig, convID), "curate") {
		marker = m
	}
	if marker["gate"] != "failed" {
		t.Errorf("curate marker = %v, want gate failed", marker)
	}
	if dead, ok := marker["dead_citations"].([]interface{}); !ok || len(dead) != 1 {
		t.Errorf("dead_citations = %v, want 1 token", marker["dead_citations"])
	}
	gateRows := memoryUpdatesByCause(t, allEvents(t, rig, convID), "gate_failed")
	if len(gateRows) != 1 || gateRows[0]["layer"] != "curator" {
		t.Errorf("gate_failed memory_update = %v, want 1 curator row", gateRows)
	}
}

// TestCheckTopicCitations (unit): the citation-liveness pre-check —
// qualified exists → kept; bare unambiguous → repaired; bare ambiguous →
// dead; qualified missing → dead; non-epoch parens are not citations.
func TestCheckTopicCitations(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "main-epoch-1", "x\n")
	writeNote(t, root, "main-epoch-2", "x\n")
	writeNote(t, root, "feature-epoch-1", "x\n")

	topics := []topic{
		{Title: "T", Slug: "t", Bullets: []string{
			"- qualified and live (main-epoch-2)",
			"- bare and unambiguous (epoch-2)",
			"- bare and ambiguous (epoch-1)",
			"- qualified but missing (ui-epoch-9)",
			"- prose parens (see README) stay",
			"- no parens at all",
		}},
	}
	repaired, dead := checkTopicCitations(root, topics)
	if len(repaired) != 1 || len(repaired[0].Bullets) != 6 {
		t.Fatalf("repaired = %+v, want the topic with 6 bullets", repaired)
	}
	if !strings.HasSuffix(repaired[0].Bullets[1], "(main-epoch-2)") {
		t.Errorf("bare unambiguous bullet repaired to %q, want (main-epoch-2)", repaired[0].Bullets[1])
	}
	if !strings.HasSuffix(repaired[0].Bullets[0], "(main-epoch-2)") {
		t.Errorf("qualified live bullet changed: %q", repaired[0].Bullets[0])
	}
	if len(dead) != 2 {
		t.Fatalf("dead = %v, want the ambiguous + missing citations", dead)
	}
	gotTokens := dead[0].token + "|" + dead[1].token
	if gotTokens != "(epoch-1)|(ui-epoch-9)" {
		t.Errorf("dead tokens = %q, want (epoch-1)|(ui-epoch-9)", gotTokens)
	}
	if !strings.Contains(repaired[0].Bullets[4], "(see README)") {
		t.Errorf("prose parens mangled: %q", repaired[0].Bullets[4])
	}
}

// TestLegacyAutoCuratePrefMigration: the boot scan strips the retired M10
// pref and journals the migration exactly once.
func TestLegacyAutoCuratePrefMigration(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	writePrefs(t, home, "# mine\nauto_distill: never\nauto_curate_after_distill: true\nomp_timeout: 900\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	if err := rig.server.StartupAutoScan(context.Background()); err != nil {
		t.Fatalf("StartupAutoScan: %v", err)
	}
	if got := currentPrefs(t, home); strings.Contains(got, "auto_curate_after_distill") {
		t.Errorf("prefs still carry the retired key: %q", got)
	}
	rows := memoryUpdatesByCause(t, allEvents(t, rig, convID), "migration")
	if len(rows) != 1 || rows[0]["layer"] != "curator" {
		t.Fatalf("migration rows = %v, want 1 curator row", rows)
	}
	// Idempotent by construction (the key being gone is the stamp).
	if err := rig.server.StartupAutoScan(context.Background()); err != nil {
		t.Fatalf("StartupAutoScan #2: %v", err)
	}
	if rows := memoryUpdatesByCause(t, allEvents(t, rig, convID), "migration"); len(rows) != 1 {
		t.Errorf("migration rows after second scan = %d, want 1", len(rows))
	}
	// Other keys survived the strip.
	for _, k := range []string{"auto_distill: never", "omp_timeout: 900"} {
		if !strings.Contains(currentPrefs(t, home), k) {
			t.Errorf("prefs lost %q in the migration: %q", k, currentPrefs(t, home))
		}
	}
}

// currentPrefs reads ~/.odo/prefs.md text.
func currentPrefs(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".odo", "prefs.md"))
	if err != nil {
		t.Fatalf("read prefs: %v", err)
	}
	return string(b)
}

// projectIDOf resolves a conversation's project ID through the store.
func projectIDOf(t *testing.T, rig *testRig, convID int64) int64 {
	t.Helper()
	ctx := context.Background()
	c, err := rig.store.GetConversation(ctx, convID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	w, err := rig.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		t.Fatalf("get workstream: %v", err)
	}
	return w.ProjectID
}

// TestAutoUrgentUpgradeSupersedesIdle (P1-1, K3 F1): a window armed under
// the urgent threshold that later crosses it supersedes its idle timer —
// journaled skipped{superseded_by_urgent} + a fresh scheduled{urgent} with
// eta ≈ 0 (T3's "fire without idle" does not expire at arm time).
func TestAutoUrgentUpgradeSupersedesIdle(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Note\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0) // prefs idle (120s): the first arm cannot fire mid-test

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	evaluate := func() {
		rig.server.mu.Lock()
		rig.server.maybeAutoAfterActivityLocked(context.Background(), convID)
		rig.server.mu.Unlock()
	}

	journalWindow(t, rig, convID, 6, 16384) // ≈99KB rendered — eligible, sub-urgent
	evaluate()
	entry := pendingEntry(rig.server, convID)
	if entry == nil || entry.trigger != distillTriggerIdle {
		t.Fatalf("first arm = %+v, want an idle entry", entry)
	}
	if eta := time.Until(entry.fireAt); eta < 100*time.Second || eta > 125*time.Second {
		t.Fatalf("idle eta = %s, want ≈120s (prefs default)", eta.Round(time.Second))
	}

	journalWindow(t, rig, convID, 3, 14000) // +≈41KB → ≈140KB ≥ 128KB urgent
	evaluate()

	// The upgraded entry is read FIRST — the 0-delay timer claims it on its
	// own goroutine almost immediately (same pattern as T3's test).
	entry = pendingEntry(rig.server, convID)
	if entry == nil || entry.trigger != distillTriggerUrgent {
		t.Fatalf("upgraded entry = %+v, want an urgent entry", entry)
	}
	if eta := time.Until(entry.fireAt); eta > 2*time.Second {
		t.Errorf("urgent eta = %s, want immediate (no idle)", eta)
	}

	// Synchronously journaled at the upgrade: one idle arm + one urgent arm.
	rows := autoRows(t, rig, convID, "scheduled")
	if len(rows) != 2 {
		t.Fatalf("scheduled rows = %d, want 2 (idle arm + urgent upgrade): %v", len(rows), rows)
	}
	if d := rows[0]["detail"].(string); !strings.Contains(d, "trigger=idle") {
		t.Errorf("first scheduled detail = %q, want trigger=idle", d)
	}
	if d := rows[1]["detail"].(string); !strings.Contains(d, "trigger=urgent") {
		t.Errorf("second scheduled detail = %q, want trigger=urgent", d)
	}
	skips := autoRows(t, rig, convID, "skipped")
	if len(skips) != 1 || !strings.Contains(skips[0]["detail"].(string), "superseded_by_urgent") {
		t.Fatalf("skipped rows = %v, want exactly one superseded_by_urgent", skips)
	}

	// The stopped idle timer must not fire: exactly one fire, urgent.
	waitForCond(t, 15*time.Second, "urgent distill marker", func() bool {
		for _, m := range payloadsByAction(t, allEvents(t, rig, convID), "distill") {
			if m["trigger"] == distillTriggerUrgent {
				return true
			}
		}
		return false
	})
	if rows := autoRows(t, rig, convID, "fired"); len(rows) != 1 {
		t.Fatalf("fired rows = %v, want exactly 1 (the superseded idle timer must be stopped)", rows)
	}
}

// TestAutoCommittedPhaseSendFoldsPinnedWindow (P1-2, K3 F2 + DSF P1): a
// send arriving AFTER the pre-note checkpoint does not cancel and journals
// NO cancelled_by_send row — the fold completes, its marker claims exactly
// the rendered window [first_seq, last_seq] pinned at prompt build, and the
// send's user message sits ABOVE that boundary: visible in the next epoch's
// window and replay instead of folded-away-unseen.
func TestAutoCommittedPhaseSendFoldsPinnedWindow(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, autoSlowLearnerWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Pinned fold\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	armPendingNow(rig.server, convID, distillTriggerIdle)
	done := make(chan struct{})
	go func() {
		rig.server.runAutoDistill(convID, distillTriggerIdle)
		close(done)
	}()
	// The fold commits at the checkpoint; the learner one-shot then sleeps
	// 3s, holding the fold open in its committed phase for the send.
	waitCommitted(t, rig.server, convID)
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"}) // must not refuse

	var marker map[string]interface{}
	waitForCond(t, 15*time.Second, "committed-phase distill marker", func() bool {
		for _, m := range payloadsByAction(t, allEvents(t, rig, convID), "distill") {
			marker = m
		}
		return marker != nil
	})
	<-done

	if rows := autoRows(t, rig, convID, "cancelled_by_send"); len(rows) != 0 {
		t.Fatalf("cancelled_by_send rows = %v, want zero (post-checkpoint gate must not cancel)", rows)
	}
	events := allEvents(t, rig, convID)
	var sendSeq int
	for _, ev := range events {
		if ev.Type == store.EventUserMessage && strings.Contains(string(ev.Payload), "Create hello.txt") {
			sendSeq = ev.Seq
		}
	}
	if sendSeq == 0 {
		t.Fatal("the send's user_message never journaled")
	}
	// Pinned to the render snapshot: the pre-fold journal ended with the
	// scheduler's `fired` row, immediately before the send's message.
	if marker["first_seq"] != float64(1) {
		t.Errorf("marker first_seq = %v, want 1 (no prior fold)", marker["first_seq"])
	}
	if marker["last_seq"] != float64(sendSeq-1) {
		t.Errorf("marker last_seq = %v, want %d (render-time pin — the message must NOT be folded)",
			marker["last_seq"], sendSeq-1)
	}
	// The message is above the fold boundary and stays visible: the replay
	// derivation and the next epoch's render window both include it.
	if b := foldBoundary(events); b != sendSeq-1 {
		t.Errorf("fold boundary = %d, want %d (payload last_seq, not the marker's own seq)", b, sendSeq-1)
	}
	visible := false
	for _, ev := range windowEvents(events) {
		if ev.Seq == sendSeq {
			visible = true
		}
		if isDistillMarkerEvent(ev) {
			t.Errorf("marker row seq %d leaked into the next epoch's render window", ev.Seq)
		}
	}
	if !visible {
		t.Error("the send's message is invisible to the next epoch (folded but never rendered)")
	}
	if c, err := rig.store.GetConversation(context.Background(), convID); err != nil || c.Epoch != 2 {
		t.Errorf("epoch = %d, want 2 (the committed fold must complete)", c.Epoch)
	}
	// The send proceeded normally — its run drained to agent_done (plain
	// poll: the run typically ENDED during the slow learner, so the
	// agent_running precondition of pollUntilDone does not hold here).
	polled := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
	sawDone := false
	for _, ev := range polled.Events {
		if ev.Type == store.EventAgentDone {
			sawDone = true
		}
	}
	if !sawDone {
		t.Error("the send's run never completed (no agent_done)")
	}
}

// TestAutoCommittedPhaseSupersededByActivity (P1-2): journal growth past
// the rendered window the fold did NOT author — and with no post-commit
// input through the gate — abandons the fold instead of landing a marker:
// skipped{superseded_by_activity} journaled once, no marker, no epoch
// move, the orphan note deleted, and an idle timer re-armed for a fresh
// fold over the grown window.
func TestAutoCommittedPhaseSupersededByActivity(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, autoSlowLearnerWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Should never be linked\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	armPendingNow(rig.server, convID, distillTriggerIdle)
	done := make(chan struct{})
	go func() {
		rig.server.runAutoDistill(convID, distillTriggerIdle)
		close(done)
	}()
	waitCommitted(t, rig.server, convID)
	// Unattributed growth: a row appended without passing the send gate
	// (the gate flips inputPassed; this write simulates every other
	// journaled writer — a todo merge, a diff accept, …).
	if _, err := rig.store.AppendEvent(context.Background(), convID, "user_message",
		mustJSON(map[string]interface{}{"text": "concurrent write"})); err != nil {
		t.Fatal(err)
	}

	<-done
	skips := autoRows(t, rig, convID, "skipped")
	count := 0
	for _, row := range skips {
		if strings.Contains(row["detail"].(string), "superseded_by_activity") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("superseded_by_activity rows = %d, want exactly 1 (skips: %v)", count, skips)
	}
	if markers := payloadsByAction(t, allEvents(t, rig, convID), "distill"); len(markers) != 0 {
		t.Errorf("superseded fold left a marker: %v", markers)
	}
	if _, err := os.Stat(filepath.Join(root, "wiki", "main-epoch-1.md")); !os.IsNotExist(err) {
		t.Error("superseded fold kept the orphan note (must be deleted — the journal never links it)")
	}
	if c, err := rig.store.GetConversation(context.Background(), convID); err != nil || c.Epoch != 1 {
		t.Errorf("epoch = %d, want 1 (superseded fold must not move the epoch)", c.Epoch)
	}
	if rows := autoRows(t, rig, convID, "failed"); len(rows) != 0 {
		t.Errorf("supersession journaled as failure (poisons backoff): %v", rows)
	}
	entry := pendingEntry(rig.server, convID)
	if entry == nil || entry.trigger != distillTriggerIdle {
		t.Fatalf("re-arm = %+v, want an idle pending entry (fresh fold over the grown window)", entry)
	}
	if eta := time.Until(entry.fireAt); eta < 100*time.Second || eta > 125*time.Second {
		t.Errorf("re-arm eta = %s, want ≈idle 120s (never an immediate supersede-loop)", eta.Round(time.Second))
	}
}

// TestAutoCommittedPhaseBookkeepingKeepsFold (GLM+DSF): metadata
// bookkeeping landing in the committed phase — a /pin row and an
// auto-curate pass (review_action{action:"curate"} +
// memory_update{layer:"curator"|"index"}) — is attributed growth, so the
// fold COMPLETES: no supersede skip, marker pinned at the render-time
// last_seq, the bookkeeping rows above the boundary (visible, unclaimed),
// and the epoch moves.
func TestAutoCommittedPhaseBookkeepingKeepsFold(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, autoSlowLearnerWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Fold with mid-fold bookkeeping\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	journalWindow(t, rig, convID, 6, 3000)

	armPendingNow(rig.server, convID, distillTriggerIdle)
	done := make(chan struct{})
	go func() {
		rig.server.runAutoDistill(convID, distillTriggerIdle)
		close(done)
	}()
	waitCommitted(t, rig.server, convID)
	// The render-time pin: the last journaled row when the fold committed
	// (the scheduler's `fired` row) — every bookkeeping row below must land
	// ABOVE the marker's last_seq.
	committed := allEvents(t, rig, convID)
	renderPin := committed[len(committed)-1].Seq

	// A /pin row (memory_update{layer:"pins"}) …
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate,
		mustJSON(map[string]interface{}{"layer": "pins", "cause": "pin", "detail": "Never deploy on Fridays."})); err != nil {
		t.Fatal(err)
	}
	// … and a full auto-curate pass (marker + curator/index bookkeeping).
	journalMarker(t, rig, convID, map[string]interface{}{"action": "curate", "trigger": "auto_notes", "topics": 0})
	for _, row := range []map[string]interface{}{
		{"layer": "curator", "cause": "gate_failed", "detail": "dead citation(s): wiki/ghost.md"},
		{"layer": "index", "cause": "curate", "before_sha": "a", "after_sha": "b"},
	} {
		if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(row)); err != nil {
			t.Fatal(err)
		}
	}

	var marker map[string]interface{}
	waitForCond(t, 15*time.Second, "distill marker with mid-fold bookkeeping", func() bool {
		for _, m := range payloadsByAction(t, allEvents(t, rig, convID), "distill") {
			marker = m
		}
		return marker != nil
	})
	<-done

	for _, row := range autoRows(t, rig, convID, "skipped") {
		if strings.Contains(row["detail"].(string), "superseded_by_activity") {
			t.Fatalf("bookkeeping in the committed phase superseded the fold: %v", row)
		}
	}
	if marker["last_seq"] != float64(renderPin) {
		t.Errorf("marker last_seq = %v, want %d (render-time pin — bookkeeping rows stay above the boundary)",
			marker["last_seq"], renderPin)
	}
	if b := foldBoundary(allEvents(t, rig, convID)); b != renderPin {
		t.Errorf("fold boundary = %d, want %d", b, renderPin)
	}
	if c, err := rig.store.GetConversation(context.Background(), convID); err != nil || c.Epoch != 2 {
		t.Errorf("epoch = %d, want 2 (the committed fold must complete)", c.Epoch)
	}
}

// TestAutoStaleTimerCallbackCannotClaimRearm (K3): a timer that fires
// after its entry was superseded (stopped + re-armed fresh, the urgent
// upgrade shape) must exit silently — claiming the fresh entry would run
// it early with the stale trigger label and orphan the fresh timer.
func TestAutoStaleTimerCallbackCannotClaimRearm(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	stats := windowStats{events: 6, eligibleBytes: 20000}
	rig.server.mu.Lock()
	rig.server.armAutoLocked(context.Background(), convID, distillTriggerIdle, time.Millisecond, stats)
	stale := rig.server.autoPending[convID]
	// Supersede: stop + replace BEFORE the stale callback can acquire s.mu
	// (the test holds it through the re-arm, so the callback — fired or
	// about to fire — parks in Lock until the swap is complete).
	stale.timer.Stop()
	delete(rig.server.autoPending, convID)
	rig.server.armAutoLocked(context.Background(), convID, distillTriggerUrgent, time.Hour, stats)
	fresh := rig.server.autoPending[convID]
	rig.server.mu.Unlock()
	defer fresh.timer.Stop()
	if stale == nil || fresh == nil || stale == fresh {
		t.Fatalf("arm->supersede->re-arm left entries stale=%p fresh=%p, want two distinct", stale, fresh)
	}

	// The 1ms stale timer fires well inside this window; claim-by-identity
	// must leave the fresh urgent entry installed and unclaimed.
	time.Sleep(300 * time.Millisecond)
	if got := pendingEntry(rig.server, convID); got != fresh {
		t.Fatalf("pending entry = %+v, want the re-armed urgent entry %p (stale timer claimed it)", got, fresh)
	}
}
