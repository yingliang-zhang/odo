package ipc

// Daily-cap suspension drills (2026-08-26 storm fix): the first cap hit
// journals ONE cap_suspended_until row and arms ONE resume timer; the
// window's scheduler bookkeeping stops counting toward eligibility; the
// resume re-checks quota and restarts the ordinary cycle without
// catch-up; and the three panel fixes hold — FIX 1 (concurrent first
// hits converge on one row + one timer), FIX 2 (lookup failures re-arm,
// timers heal, no permanent silence), FIX 3 (the badge gates on the
// subsystem being enabled and never outlives the horizon).

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// projectIDForConv resolves the conversation's project (the suspension
// registry's key).
func projectIDForConv(t *testing.T, rig *testRig, convID int64) int64 {
	t.Helper()
	ctx := context.Background()
	c, err := rig.store.GetConversation(ctx, convID)
	if err != nil {
		t.Fatalf("get conversation %d: %v", convID, err)
	}
	w, err := rig.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		t.Fatalf("get workstream %d: %v", c.WorkstreamID, err)
	}
	return w.ProjectID
}

// capEntryFor snapshots the project's suspension entry (nil when none).
func capEntryFor(srv *Server, projectID int64) *autoCapEntry {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return srv.autoCap[projectID]
}

// backdateNewest rewrites the created_at of the conversation's newest n
// events (the autonomy_test.go seam): the quota horizon math needs
// counted markers whose release sits in a controlled future/past without
// a 24h sleep.
func backdateNewest(t *testing.T, rig *testRig, convID int64, at string, n int) {
	t.Helper()
	if _, err := rig.store.DB().Exec(
		`UPDATE events SET created_at = ? WHERE id IN (
		   SELECT id FROM events WHERE conversation_id = ? ORDER BY seq DESC LIMIT ?)`, at, convID, n); err != nil {
		t.Fatalf("backdate newest %d events: %v", n, err)
	}
}

// ago formats a past instant as the journal's UTC timestamp layout.
func ago(d time.Duration) string {
	return time.Now().UTC().Add(-d).Format(autoEventTimeLayout)
}

// journalSchedulerNoise appends n auto_distill scheduler rows cycling the
// three storm causes (scheduled / skipped / cap_suspended_until) — the
// window-eligibility fixtures for the exclusion drill.
func journalSchedulerNoise(t *testing.T, rig *testRig, convID int64, n int) {
	t.Helper()
	causes := []string{"scheduled", "skipped", autoCauseCapSuspended}
	for i := 0; i < n; i++ {
		if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer": "auto_distill", "cause": causes[i%len(causes)], "detail": fmt.Sprintf("noise %d", i),
		})); err != nil {
			t.Fatalf("journal scheduler noise %d: %v", i, err)
		}
	}
}

// installCapEntry installs a suspension entry directly (the
// armPendingNow-shaped seam for the resume path): timer unfired, firing
// false, ready for a synchronous runAutoCapResume drive.
func installCapEntry(srv *Server, projectID, convID int64, resumeAt time.Time) {
	srv.mu.Lock()
	srv.autoCap[projectID] = &autoCapEntry{
		convID:   convID,
		resumeAt: resumeAt,
		timer:    time.AfterFunc(time.Hour, func() {}),
	}
	srv.mu.Unlock()
}

// evaluate drives one synchronous activity evaluation (the drainRun
// finish callback's shape, without a run).
func evaluate(srv *Server, convID int64) {
	srv.mu.Lock()
	srv.maybeAutoAfterActivityLocked(context.Background(), convID)
	srv.mu.Unlock()
}

// TestAutoCapResumeAtMath pins the horizon formula: the cap releases when
// the (len-cap+1)-th oldest counted marker ages out of the 24h window.
func TestAutoCapResumeAtMath(t *testing.T) {
	t.Parallel()
	now := time.Now()
	ts := func(age time.Duration) string {
		return now.Add(-age).UTC().Format(autoEventTimeLayout)
	}
	got := autoCapResumeAt([]string{ts(12 * time.Hour), ts(11 * time.Hour)}, 2, now)
	if d := got.Sub(now); d < 11*time.Hour+30*time.Minute || d > 12*time.Hour+30*time.Minute {
		t.Errorf("horizon = %s from now, want ≈12h (oldest of 2 with cap 2)", d)
	}
	// Over-release (N > cap): the (N-cap+1)-th oldest is the release.
	got = autoCapResumeAt([]string{ts(12 * time.Hour), ts(6 * time.Hour), ts(1 * time.Hour)}, 2, now)
	if d := got.Sub(now); d < 17*time.Hour || d > 19*time.Hour {
		t.Errorf("over-release horizon = %s, want ≈18h (second oldest + 24h)", d)
	}
	// Below the cap: no pressure — release is now.
	if got := autoCapResumeAt([]string{ts(time.Hour)}, 12, now); !got.Equal(now) {
		t.Errorf("below-cap horizon = %s, want now", got)
	}
	// An already-passed horizon collapses to now.
	if got := autoCapResumeAt([]string{ts(26 * time.Hour)}, 1, now); !got.Equal(now) {
		t.Errorf("passed horizon = %s, want now (immediate release)", got)
	}
}

// TestAutoDailyCapSuspension (design 1+2+4): the first cap hit journals
// exactly ONE cap_suspended_until row (horizon = oldest counted + 24h)
// and arms ONE resume timer — a skipped row no longer appears; N
// subsequent activities journal NOTHING more (no scheduled, no skipped)
// and never arm a lane timer while the suspension holds; pending_counts
// discloses the resume time.
func TestAutoDailyCapSuspension(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	writePrefs(t, home, "auto_distill_daily_cap: 2\nauto_distill_max_per_hour: 99\n")

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	projectID := projectIDForConv(t, rig, convID)

	// Two counted markers (cap 2) aged 12h → horizon ≈ 12h future.
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "idle"})
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "urgent"})
	backdateNewest(t, rig, convID, ago(12*time.Hour), 2)
	journalWindow(t, rig, convID, 6, 3000)

	armPendingNow(rig.server, convID, distillTriggerIdle)
	rig.server.runAutoDistill(convID, distillTriggerIdle)

	rows := autoRows(t, rig, convID, autoCauseCapSuspended)
	if len(rows) != 1 {
		t.Fatalf("cap_suspended_until rows = %d, want exactly 1: %v", len(rows), rows)
	}
	detail := rows[0]["detail"].(string)
	resumeAt, err := time.Parse(time.RFC3339, detail)
	if err != nil {
		t.Fatalf("suspension detail %q is not RFC3339: %v", detail, err)
	}
	if d := time.Until(resumeAt); d < 11*time.Hour || d > 13*time.Hour {
		t.Errorf("suspension horizon = %s from now, want ≈12h (oldest counted + 24h)", d)
	}
	if skipped := autoRows(t, rig, convID, "skipped"); len(skipped) != 0 {
		t.Fatalf("skipped rows after cap hit = %v, want NONE (suspension replaced the skip)", skipped)
	}
	entry := capEntryFor(rig.server, projectID)
	if entry == nil {
		t.Fatal("no suspension entry after cap hit")
	}
	if entry.timer == nil || entry.firing {
		t.Errorf("entry = %+v, want one armed unfired timer", entry)
	}
	if !entry.resumeAt.Equal(resumeAt) {
		t.Errorf("entry resumeAt = %s, journaled %s — the row is the record, they must agree", entry.resumeAt, resumeAt)
	}

	// Design 1+2: five more activity completions journal NOTHING (no
	// scheduled, no skipped, no second suspension row) and arm nothing.
	for i := 0; i < 5; i++ {
		journalWindow(t, rig, convID, 1, 3000)
		evaluate(rig.server, convID)
	}
	if got := len(autoRows(t, rig, convID, "scheduled")); got != 0 {
		t.Errorf("scheduled rows while suspended = %d, want 0", got)
	}
	if got := len(autoRows(t, rig, convID, "skipped")); got != 0 {
		t.Errorf("skipped rows while suspended = %d, want 0", got)
	}
	if got := len(autoRows(t, rig, convID, autoCauseCapSuspended)); got != 1 {
		t.Errorf("cap_suspended_until rows while suspended = %d, want 1 (suspend once per window)", got)
	}
	if pendingEntry(rig.server, convID) != nil {
		t.Error("lane timer armed under suspension")
	}
	if entry := capEntryFor(rig.server, projectID); entry == nil || entry.timer == nil {
		t.Fatalf("suspension entry lost under activity: %+v", entry)
	}

	// Design 4 (daemon half): the badge discloses the resume time,
	// un-computed (the journaled row is the source).
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	info := pc.AutoDistillCapResume
	if info == nil {
		t.Fatal("pending_counts auto_distill_cap_resume = nil under suspension")
	}
	if info.Computed {
		t.Error("badge marked computed with the journaled row present")
	}
	if got, want := info.ResumeAtUnix, resumeAt.Unix(); got != want {
		t.Errorf("badge resume_at = %d, want %d (the row's horizon)", got, want)
	}
}

// TestAutoSchedulerRowsExcludedFromWindow (design 3): scheduled /
// skipped / cap_suspended_until rows count toward NEITHER eligibility
// axis (events nor bytes) and never render — the window measures
// agent/user activity only; outcome rows (fired) still render.
func TestAutoSchedulerRowsExcludedFromWindow(t *testing.T) {
	// Unit half: render/size for one excluded and one kept row.
	excluded := store.Event{Type: store.EventMemoryUpdate, Payload: json.RawMessage(
		`{"layer":"auto_distill","cause":"` + autoCauseCapSuspended + `","detail":"2030-01-01T00:00:00Z"}`)}
	if got := distillRenderSize(excluded); got != 0 {
		t.Errorf("scheduler row render size = %d, want 0", got)
	}
	if got := distillRender(excluded); got != "" {
		t.Errorf("scheduler row render = %q, want empty", got)
	}
	kept := store.Event{Type: store.EventMemoryUpdate, Payload: json.RawMessage(
		`{"layer":"auto_distill","cause":"fired","detail":"trigger=idle"}`)}
	if got := distillRenderSize(kept); got == 0 {
		t.Error("fired row render size = 0 — outcome rows are epoch signal, they keep rendering")
	}

	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// 3 real events under a pile of scheduler noise: pre-fix the rows
	// alone crossed min_events (and padded the byte axis); now the
	// evaluation must read window_events=3 / below_min_events.
	journalWindow(t, rig, convID, 3, 2000)
	journalSchedulerNoise(t, rig, convID, 21)
	evaluate(rig.server, convID)
	skips := autoRows(t, rig, convID, "skipped")
	if len(skips) == 0 {
		t.Fatal("no skip journaled for the tiny real window")
	}
	detail := skips[len(skips)-1]["detail"].(string)
	if !strings.Contains(detail, "window_events=3") || !strings.Contains(detail, "below_min_events") {
		t.Errorf("skip detail = %q, want window_events=3 + below_min_events (noise excluded from BOTH axes)", detail)
	}
	if pendingEntry(rig.server, convID) != nil {
		t.Error("armed on a scheduler-noise window")
	}

	// Real growth still counts: 3 more user events over the byte floor
	// arm the ordinary idle timer. (The noise fixture already journaled
	// "scheduled" rows — the NEW arm is the LAST one's detail.)
	journalWindow(t, rig, convID, 3, 20000)
	evaluate(rig.server, convID)
	if pendingEntry(rig.server, convID) == nil {
		t.Error("eligible window (6 real events, >16KB real bytes) did not arm")
	}
	scheds := autoRows(t, rig, convID, "scheduled")
	if last := scheds[len(scheds)-1]["detail"].(string); !strings.Contains(last, "trigger=idle") || !strings.Contains(last, "window_events=6") {
		t.Errorf("last scheduled detail = %q, want trigger=idle window_events=6 (real growth armed it)", last)
	}
}

// TestHealRowsExcludedFromWindow (2026-08-26 DSF follow-up, the
// eligibility half of the split): the boot replayer's recovery family —
// recover (the journaled name) / heal_replayed (the same family's
// design-note name) / heal_merged / heal_conflict / heal_resolved,
// journaled with layer = the healed memory layer — is
// boot/crash-recovery bookkeeping, excluded from BOTH eligibility axes
// exactly like scheduler noise (the render half — they keep folding into
// the prompt — is pinned in TestHealRowsStillRenderInDistillPrompt).
// KB-sized stranded_body payloads must not inflate the byte axis. The
// layer gate's negative space is pinned too: recovery-named causes on
// layers the replayer never heals still count as ordinary activity.
func TestHealRowsExcludedFromWindow(t *testing.T) {
	healRow := func(cause string, extra map[string]interface{}) store.Event {
		payload := map[string]interface{}{"layer": "memory", "cause": cause, "detail": "boot recovery bookkeeping"}
		for k, v := range extra {
			payload[k] = v
		}
		return store.Event{Type: store.EventMemoryUpdate, Payload: json.RawMessage(mustJSON(payload))}
	}
	for _, cause := range []string{"recover", "heal_replayed", "heal_merged", "heal_conflict", "heal_resolved"} {
		if st := measureWindow([]store.Event{healRow(cause, nil)}); st.events != 0 || st.eligibleBytes != 0 {
			t.Errorf("measureWindow(%s) = {events:%d bytes:%d}, want {0 0} — recovery rows are never window activity",
				cause, st.events, st.eligibleBytes)
		}
		if render := distillRender(healRow(cause, nil)); !strings.Contains(render, fmt.Sprintf(`"cause":%q`, cause)) {
			t.Errorf("distillRender(%s) = %q — heal outcomes keep rendering (they ARE the epoch's history)", cause, render)
		}
	}
	// A heal_conflict's stranded body is exactly what inflated
	// eligibleBytes pre-fix: 4KB of recovery payload, zero window bytes.
	conflict := healRow("heal_conflict", map[string]interface{}{
		"stranded_receipt_seq":  7,
		"stranded_conversation": 1,
		"stranded_body":         strings.Repeat("s", 4096),
	})
	if st := measureWindow([]store.Event{conflict}); st.events != 0 || st.eligibleBytes != 0 {
		t.Errorf("measureWindow(heal_conflict + 4KB stranded_body) = {events:%d bytes:%d}, want {0 0}", st.events, st.eligibleBytes)
	}

	// The layer gate keeps the exclusion structural: the SAME recovery
	// cause strings on a layer the boot replayer never heals (no writer
	// produces such rows today — this pins the negative space so a future
	// writer's genuine activity can't be silently undercounted) must
	// count on both axes.
	for _, layer := range []string{"note", "wiki", "auto_distill"} {
		row := store.Event{Type: store.EventMemoryUpdate, Payload: json.RawMessage(mustJSON(map[string]interface{}{
			"layer": layer, "cause": "heal_conflict", "detail": "hypothetical non-replay-layer writer",
		}))}
		if st := measureWindow([]store.Event{row}); st.events != 1 || st.eligibleBytes == 0 {
			t.Errorf("measureWindow(%s/heal_conflict) = {events:%d bytes:%d}, want events:1 bytes>0 — the heal exclusion stops at the replayer's layer family",
				layer, st.events, st.eligibleBytes)
		}
	}

	// Rig half (mirrors TestAutoSchedulerRowsExcludedFromWindow): a window
	// of nothing but heal rows is ineligible — the evaluation skips with
	// window_events=0 window_bytes=0 and never arms.
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	for i, cause := range []string{"recover", "heal_merged", "heal_conflict", "heal_resolved", "heal_conflict"} {
		payload := map[string]interface{}{
			"layer":  "memory",
			"cause":  cause,
			"detail": "boot recovery bookkeeping",
		}
		if cause == "heal_conflict" {
			payload["stranded_receipt_seq"] = i + 1
			payload["stranded_conversation"] = convID
			payload["stranded_body"] = strings.Repeat("s", 3000+i)
		}
		if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(payload)); err != nil {
			t.Fatalf("journal heal row: %v", err)
		}
	}
	evaluate(rig.server, convID)
	skips := autoRows(t, rig, convID, "skipped")
	if len(skips) == 0 {
		t.Fatal("no skip journaled for the heal-only window")
	}
	detail := skips[len(skips)-1]["detail"].(string)
	if !strings.Contains(detail, "window_events=0") || !strings.Contains(detail, "window_bytes=0") ||
		!strings.Contains(detail, "below_min_events") {
		t.Errorf("skip detail = %q, want window_events=0 window_bytes=0 below_min_events (heal rows excluded from BOTH axes)", detail)
	}
	if pendingEntry(rig.server, convID) != nil {
		t.Error("armed on a heal-only window")
	}

	// Real growth still counts under the heal noise: 6 real events over
	// the byte floor arm the ordinary idle timer, measured as exactly 6.
	journalWindow(t, rig, convID, 6, 20000)
	evaluate(rig.server, convID)
	if pendingEntry(rig.server, convID) == nil {
		t.Error("eligible window (6 real events) under heal noise did not arm")
	}
	scheds := autoRows(t, rig, convID, "scheduled")
	if last := scheds[len(scheds)-1]["detail"].(string); !strings.Contains(last, "trigger=idle") || !strings.Contains(last, "window_events=6") {
		t.Errorf("last scheduled detail = %q, want trigger=idle window_events=6 (real activity armed it, heal rows ignored)", last)
	}
}

// TestHealRowsStillRenderInDistillPrompt pins the render half of the
// split (the trap in the DSF note): the same heal_* rows that
// windowExcludedMemoryUpdate removes from the eligibility count KEEP
// RENDERING in the distill prompt — they are outcome rows in the
// fired / failed / cancelled_by_send class; they happened to real memory
// content, they ARE the epoch's history. Layer-agnostic (a pins
// heal_resolved renders too). Scheduler bookkeeping of the identical
// shape stays excluded (foldExcludedMemoryUpdate unchanged).
func TestHealRowsStillRenderInDistillPrompt(t *testing.T) {
	t.Parallel()
	rows := []struct {
		layer, cause string
	}{
		{"memory", "recover"},
		{"memory", "heal_merged"},
		{"memory", "heal_conflict"},
		{"pins", "heal_resolved"},
		{"archive", "heal_merged"},
	}
	var events []store.Event
	for i, row := range rows {
		events = append(events, store.Event{Seq: i + 1, Type: store.EventMemoryUpdate, Payload: json.RawMessage(mustJSON(map[string]interface{}{
			"layer": row.layer, "cause": row.cause, "detail": "boot recovery bookkeeping",
		}))})
	}
	events = append(events, store.Event{Seq: len(rows) + 1, Type: store.EventMemoryUpdate, Payload: json.RawMessage(
		`{"layer":"auto_distill","cause":"skipped","detail":"trigger=idle window_events=0 window_bytes=0 reason=below_min_events"}`)})

	prompt, _ := distillPrompt(events)
	for _, row := range rows {
		want := fmt.Sprintf(`"layer":%q,"cause":%q`, row.layer, row.cause)
		if !strings.Contains(prompt, want) {
			t.Errorf("distill prompt missing %s — heal outcomes are epoch signal, they keep rendering", want)
		}
	}
	if strings.Contains(prompt, `"cause":"skipped"`) {
		t.Error("scheduler row rendered in the distill prompt — the render filter must stay unchanged")
	}
}

// TestAutoCapResumeDistills (design 5): a resume past the horizon with
// quota available clears the suspension, journals exactly ONE normal
// scheduled row (the kick's ordinary evaluation), and the cycle resumes
// — a real distill lands. No catch-up, no backfill.
func TestAutoCapResumeDistills(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Note\n")
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	writePrefs(t, home, "auto_distill_daily_cap: 1\nauto_distill_max_per_hour: 99\n")
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	projectID := projectIDForConv(t, rig, convID)

	// One counted marker aged past the window → quota already free.
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "idle"})
	backdateNewest(t, rig, convID, ago(25*time.Hour), 1)
	journalWindow(t, rig, convID, 6, 3000)

	installCapEntry(rig.server, projectID, convID, time.Now().Add(-time.Minute))
	rig.server.runAutoCapResume(projectID)

	if entry := capEntryFor(rig.server, projectID); entry != nil {
		t.Fatalf("suspension entry survives a quota-free resume: %+v", entry)
	}
	if got := len(autoRows(t, rig, convID, "scheduled")); got != 1 {
		t.Fatalf("scheduled rows after resume = %d, want exactly 1 (no catch-up storm)", got)
	}
	if got := len(autoRows(t, rig, convID, "skipped")) + len(autoRows(t, rig, convID, autoCauseCapSuspended)); got != 0 {
		t.Errorf("skipped/suspension rows after resume = %d, want 0 (no backfill)", got)
	}
	entry := pendingEntry(rig.server, convID)
	if entry == nil || entry.trigger != distillTriggerIdle {
		t.Fatalf("pending after resume = %+v, want one idle timer (the ordinary cycle)", entry)
	}

	// Badge clean ahead of the resumed distill (the only in-window marker
	// is the aged-out fixture row — quota free).
	if pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); pc.AutoDistillCapResume != nil {
		t.Errorf("badge before the resumed fire = %+v, want nil (quota free)", pc.AutoDistillCapResume)
	}

	// The cycle really resumed: the armed timer's fire runs a distill —
	// exactly one NEW auto marker lands over the fixture baseline.
	ctx := context.Background()
	before, err := rig.store.CountAutoDistillsForConversation(ctx, convID, "")
	if err != nil {
		t.Fatalf("count markers: %v", err)
	}
	rig.server.runAutoDistill(convID, distillTriggerIdle)
	if got := len(autoRows(t, rig, convID, "fired")); got != 1 {
		t.Fatalf("fired rows = %d, want 1", got)
	}
	after, err := rig.store.CountAutoDistillsForConversation(ctx, convID, "")
	if err != nil || after != before+1 {
		t.Errorf("auto markers %d → %d (%v), want exactly one new marker (the resumed distill landed)", before, after, err)
	}
}

// TestAutoCapConcurrentFirstHit (FIX 1): two lanes of the same project
// race the cap — check→journal→arm serializes inside installAutoCapLocked,
// so exactly ONE cap_suspended_until row lands project-wide and exactly
// ONE resume entry/timer exists. run with -race for the full proof.
func TestAutoCapConcurrentFirstHit(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	writePrefs(t, home, "auto_distill_daily_cap: 1\nauto_distill_max_per_hour: 99\n")

	bootA := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convA := bootA.Conversation.ID
	ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "cap-race-b"})
	bootB := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
	convB := bootB.Conversation.ID
	projectID := projectIDForConv(t, rig, convA)

	// One counted marker on lane A (aged 12h → future horizon), both
	// lanes holding eligible windows and armed fires.
	journalMarker(t, rig, convA, map[string]interface{}{"action": "distill", "trigger": "idle"})
	backdateNewest(t, rig, convA, ago(12*time.Hour), 1)
	journalWindow(t, rig, convA, 6, 3000)
	journalWindow(t, rig, convB, 6, 3000)
	armPendingNow(rig.server, convA, distillTriggerUrgent)
	armPendingNow(rig.server, convB, distillTriggerUrgent)

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() { defer wg.Done(); <-start; rig.server.runAutoDistill(convA, distillTriggerUrgent) }()
	go func() { defer wg.Done(); <-start; rig.server.runAutoDistill(convB, distillTriggerUrgent) }()
	close(start)
	wg.Wait()

	total := len(autoRows(t, rig, convA, autoCauseCapSuspended)) + len(autoRows(t, rig, convB, autoCauseCapSuspended))
	if total != 1 {
		t.Fatalf("cap_suspended_until rows across both lanes = %d, want exactly 1 (FIX 1)", total)
	}
	rig.server.mu.Lock()
	entries := len(rig.server.autoCap)
	rig.server.mu.Unlock()
	if entries != 1 {
		t.Fatalf("suspension entries = %d, want 1", entries)
	}
	entry := capEntryFor(rig.server, projectID)
	if entry == nil || entry.timer == nil || entry.firing {
		t.Fatalf("entry after race = %+v, want one armed unfired timer", entry)
	}
	if pendingEntry(rig.server, convA) != nil || pendingEntry(rig.server, convB) != nil {
		t.Error("a lane survived the race with a pending fire entry")
	}
}

// TestAutoCapResumeDeadLane (FIX 2): the suspension's origin lane is gone
// (deleted mid-suspension) — the resume falls back to the project's
// active lanes and the cycle resumes; no permanent silence.
func TestAutoCapResumeDeadLane(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	projectID := projectIDForConv(t, rig, convID)
	journalWindow(t, rig, convID, 6, 3000)

	installCapEntry(rig.server, projectID, 999999, time.Now().Add(-time.Minute)) // origin lane gone
	rig.server.runAutoCapResume(projectID)

	if entry := capEntryFor(rig.server, projectID); entry != nil {
		t.Fatalf("entry survives the dead-lane resume: %+v", entry)
	}
	if got := len(autoRows(t, rig, convID, "scheduled")); got != 1 {
		t.Fatalf("scheduled rows = %d, want 1 — the live lane resumed the cycle", got)
	}
}

// TestAutoCapResumeQuotaFailureRearms (FIX 2): a transient store failure
// inside the resume's quota recheck keeps the suspension and re-arms a
// retry instead of dying (the activity gate depends on the entry).
func TestAutoCapResumeQuotaFailureRearms(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	rig.server.autoCapRetry = 10 * time.Second
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	projectID := projectIDForConv(t, rig, convID)
	journalWindow(t, rig, convID, 6, 3000)

	closed, err := store.Open(filepath.Join(t.TempDir(), "closed.sqlite"))
	if err != nil {
		t.Fatalf("open throwaway store: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close throwaway store: %v", err)
	}
	rig.server.store = closed
	defer func() { rig.server.store = rig.store }()

	installCapEntry(rig.server, projectID, convID, time.Now().Add(-time.Minute))
	rig.server.runAutoCapResume(projectID)

	entry := capEntryFor(rig.server, projectID)
	if entry == nil {
		t.Fatal("quota failure DROPPED the suspension — permanent silence (FIX 2)")
	}
	if entry.firing || entry.timer == nil {
		t.Errorf("entry after failure = %+v, want re-armed unfired retry timer", entry)
	}
	if d := time.Until(entry.resumeAt); d < 5*time.Second || d > 15*time.Second {
		t.Errorf("retry deadline = %s, want ≈10s (autoCapRetry seam)", d)
	}
	if got := len(autoRows(t, rig, convID, "scheduled")) + len(autoRows(t, rig, convID, "skipped")) + len(autoRows(t, rig, convID, autoCauseCapSuspended)); got != 0 {
		t.Errorf("journal rows after failed resume = %d, want 0 (failures re-arm quietly)", got)
	}
}

// TestAutoCapResumeEmptyProject: a resume for a conversation-less project
// ends the suspension silently — no lane can own a foldable window, so
// there is nothing to gate (and nothing to retry).
func TestAutoCapResumeEmptyProject(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)

	installCapEntry(rig.server, 999999, 999999, time.Now().Add(-time.Minute))
	rig.server.runAutoCapResume(999999)
	if entry := capEntryFor(rig.server, 999999); entry != nil {
		t.Errorf("empty-project entry survives the resume: %+v", entry)
	}
}

// TestAutoCapGateHealsLostTimer (FIX 2): the activity gate re-arms a lost
// resume timer while the suspension is live (and stays silent); once the
// horizon passes, the gate drops the entry and the same activity becomes
// the organic resume — the gate never swallows a project's activity
// forever.
func TestAutoCapGateHealsLostTimer(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	projectID := projectIDForConv(t, rig, convID)
	journalWindow(t, rig, convID, 6, 3000)

	// Lost-timer state: live horizon, no timer (e.g. a mid-crash entry).
	rig.server.mu.Lock()
	rig.server.autoCap[projectID] = &autoCapEntry{convID: convID, resumeAt: time.Now().Add(time.Hour)}
	rig.server.mu.Unlock()
	evaluate(rig.server, convID)
	entry := capEntryFor(rig.server, projectID)
	if entry == nil || entry.timer == nil {
		t.Fatalf("gate did not heal the lost timer: %+v", entry)
	}
	if got := len(autoRows(t, rig, convID, "scheduled")) + len(autoRows(t, rig, convID, "skipped")); got != 0 {
		t.Errorf("journal rows while gated = %d, want 0", got)
	}

	// Horizon passed without a fired resume: the same activity drops the
	// entry and resumes the ordinary cycle (one scheduled row).
	rig.server.mu.Lock()
	rig.server.autoCap[projectID].resumeAt = time.Now().Add(-time.Minute)
	rig.server.mu.Unlock()
	evaluate(rig.server, convID)
	if entry := capEntryFor(rig.server, projectID); entry != nil {
		t.Fatalf("expired entry survives the gate: %+v", entry)
	}
	if got := len(autoRows(t, rig, convID, "scheduled")); got != 1 {
		t.Errorf("scheduled rows after organic resume = %d, want 1", got)
	}
}

// TestAutoCapBadgeGating (FIX 3 + upgrade path): a capped pre-suspension
// journal (no cap_suspended_until row) gets the COMPUTED fallback (oldest
// counted + 24h); disabling auto-distill blanks the badge entirely; the
// journaled row outranks the computation un-computed; a passed hardened
// horizon ends the chip with NO computed resurrection.
func TestAutoCapBadgeGating(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	enableAuto(rig, 0, -1, 0)
	prefs := "auto_distill_daily_cap: 2\nauto_distill_max_per_hour: 99\n"
	writePrefs(t, home, prefs)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Upgrade path: cap hit on an old journal — no suspension row exists.
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "idle"})
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "urgent"})
	backdateNewest(t, rig, convID, ago(12*time.Hour), 2)

	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	info := pc.AutoDistillCapResume
	if info == nil || !info.Computed {
		t.Fatalf("badge for a pre-fix journal = %+v, want computed fallback", info)
	}
	if d := time.Until(time.Unix(info.ResumeAtUnix, 0)); d < 11*time.Hour || d > 13*time.Hour {
		t.Errorf("computed resume in %s, want ≈12h (oldest counted + 24h)", d)
	}

	// FIX 3: disabled auto-distill discloses nothing.
	writePrefs(t, home, "auto_distill: never\n"+prefs)
	if pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); pc.AutoDistillCapResume != nil {
		t.Errorf("badge with auto_distill=never = %+v, want nil (no chip while disabled)", pc.AutoDistillCapResume)
	}
	// Re-enable: the chip returns.
	writePrefs(t, home, prefs)
	if pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); pc.AutoDistillCapResume == nil {
		t.Error("badge missing after re-enable")
	}

	// The journaled row hardens the horizon: un-computed, the row's time.
	resume := time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer": "auto_distill", "cause": autoCauseCapSuspended, "detail": resume,
	})); err != nil {
		t.Fatalf("journal suspension row: %v", err)
	}
	pc = rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	info = pc.AutoDistillCapResume
	if info == nil || info.Computed {
		t.Fatalf("badge with a journaled row = %+v, want the row (un-computed)", info)
	}
	if got := time.Unix(info.ResumeAtUnix, 0).UTC().Format(time.RFC3339); got != resume {
		t.Errorf("badge resume = %s, want the row's %s", got, resume)
	}

	// A passed hardened horizon ENDS the chip — the computation never
	// resurrects what the record closed (even with markers still capped).
	passed := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer": "auto_distill", "cause": autoCauseCapSuspended, "detail": passed,
	})); err != nil {
		t.Fatalf("journal passed suspension row: %v", err)
	}
	if pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); pc.AutoDistillCapResume != nil {
		t.Errorf("badge after the hardened horizon passed = %+v, want nil", pc.AutoDistillCapResume)
	}
}

// TestAutoCapSuspensionSurvivesRestart: the journaled row restores at
// boot — one entry, one timer, NO duplicate row, and a T2 scan that
// journals nothing (the suspension silences the whole project until the
// resume fires).
func TestAutoCapSuspensionSurvivesRestart(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	rig := startRig(t, root)
	enableAuto(rig, 0, -1, 0)
	writePrefs(t, home, "auto_distill_daily_cap: 1\nauto_distill_max_per_hour: 99\n")
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	projectID := projectIDForConv(t, rig, convID)
	journalMarker(t, rig, convID, map[string]interface{}{"action": "distill", "trigger": "idle"})
	backdateNewest(t, rig, convID, ago(12*time.Hour), 1)
	journalWindow(t, rig, convID, 6, 3000)
	armPendingNow(rig.server, convID, distillTriggerIdle)
	rig.server.runAutoDistill(convID, distillTriggerIdle)
	rows := autoRows(t, rig, convID, autoCauseCapSuspended)
	if len(rows) != 1 {
		t.Fatalf("setup: cap rows = %d, want 1", len(rows))
	}
	resumeAt, err := time.Parse(time.RFC3339, rows[0]["detail"].(string))
	if err != nil {
		t.Fatalf("setup: detail parse: %v", err)
	}

	rig.stop(t)
	rig2 := restartRig(t, rig)
	defer rig2.stop(t)
	enableAuto(rig2, time.Millisecond, time.Hour, 0)
	if err := rig2.server.StartupAutoScan(context.Background()); err != nil {
		t.Fatalf("StartupAutoScan after restart: %v", err)
	}

	entry := capEntryFor(rig2.server, projectID)
	if entry == nil {
		t.Fatal("suspension not restored at boot (the journal outlives the daemon)")
	}
	if !entry.resumeAt.Equal(resumeAt) {
		t.Errorf("restored horizon = %s, want the row's %s", entry.resumeAt, resumeAt)
	}
	if entry.timer == nil || entry.firing {
		t.Errorf("restored entry = %+v, want one armed unfired timer", entry)
	}
	if got := len(autoRows(t, rig2, convID, autoCauseCapSuspended)); got != 1 {
		t.Errorf("cap rows after restore = %d, want 1 (the restored row IS the record)", got)
	}
	if got := len(autoRows(t, rig2, convID, "scheduled")) + len(autoRows(t, rig2, convID, "skipped")); got != 0 {
		t.Errorf("T2 journaled under suspension = %d rows, want 0", got)
	}
	if pendingEntry(rig2.server, convID) != nil {
		t.Error("T2 armed a lane timer under suspension")
	}

	// Activity under the restored suspension stays silent too, and the
	// badge reports the row's horizon un-computed.
	evaluate(rig2.server, convID)
	if got := len(autoRows(t, rig2, convID, "scheduled")) + len(autoRows(t, rig2, convID, "skipped")); got != 0 {
		t.Errorf("activity journaled under restored suspension = %d rows, want 0", got)
	}
	pc := rig2.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.AutoDistillCapResume == nil || pc.AutoDistillCapResume.Computed {
		t.Fatalf("badge after restart = %+v, want the restored row (un-computed)", pc.AutoDistillCapResume)
	}
}
