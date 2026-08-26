package ipc

// M12 (D-auto): daemon-side automatic distill + conditional auto-curate.
//
// The GUI timer is gone; the daemon owns every trigger and journals every
// evaluation outcome — nothing skips silently. Triggers: T1 run-finished +
// idle (arm after drainRun finishes), T2 startup compensation (boot scan of
// active conversations' un-folded windows), T3 urgent size (rendered window
// ≥ auto_distill_urgent_kb fires without idle), T4 manual (existing IPC,
// exempt from caps/backoff, keeps let-finish + send refusal).
//
// Journal contract (memory_update{layer:"auto_distill"}):
//   scheduled          — a timer was armed (detail: trigger, eta, stats)
//   fired              — the timer's evaluation passed, distill starting
//   skipped            — evaluation or disarm (detail reason):
//                        disabled | below_min_events | below_min_bytes |
//                        hourly_cap | backoff | backoff_suspended |
//                        run_active | distill_active | slash_active |
//                        disarmed_by_send | disarmed_by_user |
//                        superseded_by_manual | superseded_by_urgent |
//                        superseded_by_activity
//
// daily_cap is NOT a skipped reason anymore (2026-08-26 storm fix): the
// first cap hit per suspension window journals ONE
//   cap_suspended_until    — detail: RFC3339 earliest quota release
// and the project then suspends: activity journals NOTHING (no repeated
// scheduled/skipped — those bookkeeping rows were growing the very
// window they measured: 3786→3819 events, 497KB→565KB of pure scheduler
// noise in production) and exactly one resume timer points at the
// horizon. installAutoCapLocked is the check→journal→arm critical
// section (FIX 1: concurrent lanes race it under s.mu — one row, one
// timer). The resume re-checks the quota, then either re-suspends once
// (no catch-up backfill) or clears and restarts the ordinary cycle
// (FIX 2: lookup failures re-arm a retry instead of dying — the silence
// can never become permanent). Scheduler bookkeeping (scheduled /
// skipped / cap_suspended_until) is excluded from the fold render AND
// from window eligibility (foldExcludedMemoryUpdate /
// windowExcludedMemoryUpdate — the render/eligibility split), so the
// storm has no bytes to feed on either. The journal row is the
// durable record: boot restores live suspensions (StartupAutoScan), and
// pending_counts discloses the resume time to the Memory tab
// (autoCapResumeForBadges — rowless pre-fix journals get the computed
// fallback oldest-counted+24h).
//
// M17 F1 retired window_exceeds_prompt_budget: the render filter
// (distillRender) keeps real windows under the cap by construction, and an
// over-cap window now folds its renderable tail with the SAME omission
// declaration the manual path always used — no more hard-skip that never
// re-armed (the 0-fired/25-skips production stall).
//   cancelled_by_send  — a send/steer/slash cancelled an in-flight AUTO
//                        distill at the pre-note checkpoint (cancel-before-
//                        note); post-checkpoint the fold is committed and
//                        inputs proceed without cancelling, so no row
//   failed             — the distill attempt errored (feeds failure backoff)
//
// Failure backoff is derived from the journal, so it survives daemon
// restarts: consecutive failed rows since the newest user message or
// successful distill marker → 5m → 30m → 2h → suspended until the next
// user event (a journaled user_message resets the streak).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
)

// Distill trigger classes journaled on every marker (T4 manual included, so
// spend-per-trigger-class is one SQL LIKE away).
const (
	distillTriggerManual  = "manual"
	distillTriggerIdle    = "idle"
	distillTriggerStartup = "startup"
	distillTriggerUrgent  = "urgent"
)

// errAutoDistillCancelled is distillCore's pre-note abort for a
// cancelled_by_send auto distill: no note, no marker, no extra journal —
// the cancelling send already journaled the cancellation.
var errAutoDistillCancelled = errors.New("auto distill cancelled before note")

// errAutoDistillSuperseded is distillCore's committed-phase abort: the
// journal grew past the rendered window with rows the fold did not author
// and no post-commit input passed the gate. distillCore has already
// journaled skipped{superseded_by_activity} and deleted the orphan note;
// runAutoDistill re-arms for a fresh fold instead of journaling failed.
var errAutoDistillSuperseded = errors.New("auto distill superseded by journal activity")

// autoBackoffSteps maps consecutive auto-distill failure counts (1-indexed)
// to the wait before the next attempt may fire; counts beyond the list are
// suspended until the next user event.
var autoBackoffSteps = []time.Duration{5 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// autoStartupJitterMax caps T2's randomized fire delay: booting the app
// after offline work shouldn't slam every stale conversation at once.
const autoStartupJitterMax = 60 * time.Second

// autoPendingEntry is one armed (not yet fired) auto-distill timer.
type autoPendingEntry struct {
	trigger string
	fireAt  time.Time
	timer   *time.Timer
}

// autoCauseCapSuspended is the suspension journal cause (detail: RFC3339
// earliest quota release). One row per suspension window; the row is the
// durable record boot restores and the badge leverage reads.
const autoCauseCapSuspended = "cap_suspended_until"

// autoCapRetryDelay is the FIX-2 re-arm wait when a resume's lookups
// fail transiently (store hiccup): the suspension outlives the failure
// by construction, retrying instead of dying into permanent silence.
const autoCapRetryDelay = time.Minute

// autoCapEntry is one project's daily-cap suspension (s.autoCap, keyed
// by project like the cap itself). Exactly one entry per project: the
// storm fix's "suspend once, single timer" invariant —
// installAutoCapLocked owns the check→journal→arm critical section.
// timer is nil only between the fire claim (runAutoCapResume takes it)
// and the resume's outcome; firing marks that in-flight window so a
// racing inspection never mistakes the empty slot for a lost timer.
type autoCapEntry struct {
	convID   int64       // kick target: the lane that hit the cap (fallback: the project's active lanes)
	resumeAt time.Time   // earliest quota release — the single timer's deadline
	timer    *time.Timer // the ONE pending resume; nil inside runAutoCapResume
	firing   bool        // resume in flight — the timer slot is intentionally empty
}

// autoInFlight is the cancel-before-note handle for a fired auto distill:
// a send/steer/slash flips cancelled + calls cancel; distillCore checks
// cancelled after the one-shot returns, before any artifact is written.
// committed flips at that checkpoint — past it the fold must complete, so
// the gate stops cancelling (a cancelled_by_send row would lie about a
// fold that lands). inputPassed records a post-commit gate pass: the
// input's journaled rows are then ATTRIBUTED mid-fold growth for the
// marker-time supersession probe (they sit above the pinned window).
type autoInFlight struct {
	trigger     string
	cancel      context.CancelFunc
	cancelled   bool
	committed   bool
	inputPassed bool
}

// autoPrefs is the resolved auto-distill/auto-curate preference set
// (fail-to-default via LoadPrefsRaw, the resolveMaxConcurrent pattern).
type autoPrefs struct {
	enabled        bool          // auto_distill != "never" (M12 flip: default on_idle)
	idle           time.Duration // T1 quiet period, clamped ≥ 15s
	minEvents      int
	minBytes       int
	urgentBytes    int
	maxPerHour     int
	dailyCap       int
	curateMinNotes int
	curateMaxAge   time.Duration
}

const (
	autoIdleDefault       = 120 * time.Second
	autoIdleFloor         = 15 * time.Second // daemon floor: prefs can't idle-fire faster
	autoMinEventsDefault  = 6
	autoMinKBDefault      = 16
	autoUrgentKBDefault   = 128
	autoMaxPerHourDefault = 2
	autoDailyCapDefault   = 12
	autoCurateMinNotes    = 4
	autoCurateMaxAgeDays  = 7
	autoEventTimeLayout   = "2006-01-02 15:04:05" // SQLite datetime('now'), UTC
)

// intPref reads a positive-integer prefs value, failing to def on absence
// or garbage (never to zero: every knob below is a rate/coverage budget).
func intPref(key string, def int) int {
	if v := adapter.LoadPrefsRaw(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// resolveAutoPrefs reads the seven auto_* prefs plus auto_distill's on/off.
// Called per evaluation (not cached) so a prefs edit takes effect on the
// next activity — same discipline as resolveMaxConcurrent/resolveReplayCaps.
// There is no separate auto_curate on/off: `auto_distill: never` disables
// the conditional auto-curate too (fail-closed — one switch stops every
// daemon-driven memory write; the manual curate is unaffected).
func (s *Server) resolveAutoPrefs() autoPrefs {
	p := autoPrefs{
		enabled:        !s.autoDisabled && adapter.LoadPrefsRaw("auto_distill") != "never", // M12 flip: "on_idle" is the default
		minEvents:      intPref("auto_distill_min_events", autoMinEventsDefault),
		minBytes:       intPref("auto_distill_min_kb", autoMinKBDefault) * 1024,
		urgentBytes:    intPref("auto_distill_urgent_kb", autoUrgentKBDefault) * 1024,
		maxPerHour:     intPref("auto_distill_max_per_hour", autoMaxPerHourDefault),
		dailyCap:       intPref("auto_distill_daily_cap", autoDailyCapDefault),
		curateMinNotes: intPref("auto_curate_min_notes", autoCurateMinNotes),
		curateMaxAge:   time.Duration(intPref("auto_curate_max_age_days", autoCurateMaxAgeDays)) * 24 * time.Hour,
	}
	idle := autoIdleDefault
	if v := adapter.LoadPrefsRaw("auto_distill_idle_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			idle = time.Duration(n) * time.Second
		}
	}
	if idle < autoIdleFloor {
		idle = autoIdleFloor // daemon floor: clamp up, never below 15s
	}
	p.idle = idle
	return p
}

// resolvedIdle applies the test-seam override to the prefs idle.
func (s *Server) resolvedIdle(p autoPrefs) time.Duration {
	if s.autoIdle > 0 {
		return s.autoIdle
	}
	return p.idle
}

// windowStats measures one un-folded epoch window. eligibleBytes is the
// POST-FILTER rendered size (M17 F1): /panel and /vision advisory
// agent_text excluded, thinking/tool-result payloads tombstoned,
// review_action/memory_update one-lined — the bytes the distiller is
// actually sent, so a 30KB panel reply or a 200KB thinking dump never
// triggers (or blocks) a fold by itself.
type windowStats struct {
	events        int
	eligibleBytes int
}

// measureWindow sizes the window with distillRenderSize — the same
// accounting capEvents and the distiller's render use, so eligibility,
// urgency, and coverage honesty all speak the exact byte unit of what's
// sent (M17 F1: previously it measured the RAW payload the prompt never
// rendered, so windows crossed the 256 KiB cap inside one run).
func measureWindow(window []store.Event) windowStats {
	var st windowStats
	for _, ev := range window {
		// Scheduler bookkeeping AND boot-recovery heal rows count toward
		// NEITHER axis (windowExcludedMemoryUpdate — the eligibility-side
		// predicate, wider than the render's foldExcludedMemoryUpdate):
		// not bytes and not events — the window measures agent/user
		// activity only, so a suspended day's worth of trigger noise or a
		// post-crash boot's heal storm (KB-sized stranded_body payloads
		// included) can't keep an otherwise-quiet window "eligible" (or
		// feed the daily-cap feedback loop the noise came from). Heal rows
		// still RENDER in the prompt — the split lives in the two
		// predicates' comments in server.go.
		if ev.Type == store.EventMemoryUpdate {
			var p struct {
				Layer string `json:"layer"`
				Cause string `json:"cause"`
			}
			if jsonUnmarshalOK(ev.Payload, &p) && windowExcludedMemoryUpdate(p.Layer, p.Cause) {
				continue
			}
		}
		st.events++
		st.eligibleBytes += distillRenderSize(ev)
	}
	return st
}

// journalAuto writes one memory_update{layer:"auto_distill"} row. Every
// scheduler decision rides this; a journal failure is logged (the caller
// proceeds — a broken journal must not wedge user sends).
func (s *Server) journalAuto(ctx context.Context, convID int64, cause, detail string) {
	if _, err := s.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":  "auto_distill",
		"cause":  cause,
		"detail": detail,
	})); err != nil {
		log.Printf("auto-distill: journal %s for conversation %d: %v", cause, convID, err)
	}
}

// gateAutoDistillForSendLocked enforces user-input precedence over
// auto-distill: any send/steer/slash disarms a scheduled auto-distill
// (journaled) and cancels an in-flight AUTO distill at the pre-note
// checkpoint (journaled cancelled_by_send, then the input proceeds — NO
// refusal). Past the checkpoint the fold is COMMITTED: cancelling would
// journal a cancelled_by_send lie (the cancel is a no-op on the fold's
// WithoutCancel context and the marker lands anyway), so the gate only
// records the pass — the input's rows land above the pinned marker
// window, attributed for the supersession probe. A MANUAL distill keeps
// let-finish + the historical refusal text. Caller holds s.mu.
func (s *Server) gateAutoDistillForSendLocked(ctx context.Context, convID int64) error {
	kind, ok := s.distillKind[convID]
	if !ok {
		s.disarmAutoLocked(ctx, convID, "disarmed_by_send")
		return nil
	}
	if kind == distillTriggerManual {
		return fmt.Errorf("send_message: distill in progress for conversation %d", convID)
	}
	if ifl := s.autoInFlight[convID]; ifl != nil && !ifl.cancelled {
		if ifl.committed {
			ifl.inputPassed = true
			return nil // fold completes; no cancel, no cancelled_by_send row
		}
		ifl.cancelled = true
		ifl.cancel()
		s.journalAuto(ctx, convID, "cancelled_by_send",
			fmt.Sprintf("trigger=%s reason=user_send", kind))
	}
	return nil
}

// disarmAutoLocked stops and forgets a pending auto-distill timer,
// journaling the disarm (cause skipped — the trigger never fired). A no-op
// (and no journal) when nothing is armed. Caller holds s.mu.
func (s *Server) disarmAutoLocked(ctx context.Context, convID int64, reason string) {
	entry := s.autoPending[convID]
	if entry == nil {
		return
	}
	entry.timer.Stop()
	delete(s.autoPending, convID)
	s.journalAuto(ctx, convID, "skipped",
		fmt.Sprintf("trigger=%s reason=%s", entry.trigger, reason))
}

// releaseSlashSlot drops one /panel or /vision query slot; the last
// release re-evaluates auto-distill for the conversation (slash answers
// grew the window — the same activity-completion rule as run-finished).
func (s *Server) releaseSlashSlot(ctx context.Context, convID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slashing[convID] > 1 {
		s.slashing[convID]--
		return
	}
	delete(s.slashing, convID)
	s.maybeAutoAfterActivityLocked(ctx, convID)
}

// maybeAutoAfterActivityLocked is the T1/T3 evaluation at every activity
// completion (drainRun finish, slash-query finish): urgency upgrades the
// trigger and the delay, eligibility + coverage-honesty skips journal,
// and an eligible window arms one idle timer. An already-armed timer
// short-circuits re-arming but NOT re-measurement: a window that crossed
// the urgent threshold since the arm supersedes its idle timer (T3's
// "fire without idle" has no expiry — an over-cap window folds with a
// declared omission now instead of blocking, M17 F1). Concurrency
// coexistence (live run, in-flight distill,
// live slash) short-circuits silently — those states re-visit this
// function on their own completion. Caller holds s.mu.
func (s *Server) maybeAutoAfterActivityLocked(ctx context.Context, convID int64) {
	prefs := s.resolveAutoPrefs()
	if !prefs.enabled {
		return
	}
	// Daily-cap suspension gate: while suspended, activity journals NOTHING
	// and arms NOTHING — the storm's per-activity scheduled+skipped pairs
	// were what fed the un-folded window. The gate also heals a lost timer
	// and drops an expired horizon so the evaluation falls through as the
	// organic resume (FIX 2).
	if s.gateAutoCapLocked(ctx, convID) {
		return
	}
	if _, ok := s.distillKind[convID]; ok {
		return // a fold is imminent; its marker is the record
	}
	if runID, ok := s.byConv[convID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return // drainRun fires this again at finish
		}
	}
	if s.slashing[convID] > 0 {
		return // releaseSlashSlot fires this again at completion
	}
	events, err := s.store.ListEvents(ctx, convID, 0)
	if err != nil {
		return
	}
	window := windowEvents(events)
	stats := measureWindow(window)

	if entry := s.autoPending[convID]; entry != nil {
		if entry.trigger != distillTriggerIdle || stats.eligibleBytes < prefs.urgentBytes {
			return // already armed, still sub-urgent — never double-schedule
		}
		// K3 F1: the window crossed urgent while an idle timer was armed.
		// Supersede it (journaled) and fall through to the fresh evaluation
		// below, which re-arms as trigger=urgent with delay 0.
		entry.timer.Stop()
		delete(s.autoPending, convID)
		s.journalAuto(ctx, convID, "skipped", fmt.Sprintf(
			"trigger=%s window_events=%d window_bytes=%d reason=superseded_by_urgent",
			entry.trigger, stats.events, stats.eligibleBytes))
	}

	if stats.events < prefs.minEvents {
		s.journalAuto(ctx, convID, "skipped", fmt.Sprintf(
			"trigger=%s window_events=%d window_bytes=%d reason=below_min_events",
			distillTriggerIdle, stats.events, stats.eligibleBytes))
		return
	}
	if stats.eligibleBytes < prefs.minBytes {
		s.journalAuto(ctx, convID, "skipped", fmt.Sprintf(
			"trigger=%s window_events=%d window_bytes=%d reason=below_min_bytes",
			distillTriggerIdle, stats.events, stats.eligibleBytes))
		return
	}
	trigger := distillTriggerIdle
	delay := s.resolvedIdle(prefs)
	if stats.eligibleBytes >= prefs.urgentBytes {
		trigger = distillTriggerUrgent // T3: fire without idle
		delay = 0
	}
	s.armAutoLocked(ctx, convID, trigger, delay, stats)
}

// armAutoLocked installs the pending timer and journals the scheduled row.
// trigger stays fixed at arm time; the fire re-evaluates eligibility but
// never reclassifies urgency (mid-idle urgency crossings are handled by
// supersession: disarm + journaled skip + fresh arm, not a retag). Caller
// holds s.mu.
func (s *Server) armAutoLocked(ctx context.Context, convID int64, trigger string, delay time.Duration, stats windowStats) {
	if s.autoStopped {
		// Shutdown began (Wait/rig teardown): a new timer would outlive
		// the drain. In-flight distills' backoff/supersession re-arms
		// land here too — they die quietly; the run itself completes.
		return
	}
	if s.autoPending[convID] != nil {
		return // belt: maybeAutoAfterActivityLocked already checked
	}
	// Mid-delete/deleted lane bar (2026-08-25 review follow-up): a timer
	// armed now would claim liveness past the delete commit; skip quietly
	// (activity on a dying lane is the caller's concern to surface).
	if err := s.guardLiveConversationLocked(ctx, convID); err != nil {
		log.Printf("auto-distill: skip arm for conversation %d — %v", convID, err)
		return
	}
	fireAt := time.Now().Add(delay)
	entry := &autoPendingEntry{
		trigger: trigger,
		fireAt:  fireAt,
	}
	// Claim-by-identity (K3): the callback proceeds only while the pending
	// slot still holds THIS entry. Supersession stops the timer and re-arms
	// a fresh entry (urgent upgrade, disarm + re-arm), and time.Timer.Stop
	// cannot retract a callback that has already started — without the
	// identity check that stale callback would spawn runAutoDistill with
	// its old trigger label, and the run would claim (and so mislabel and
	// orphan) the fresh entry. The callback blocks on s.mu, which the
	// armer is holding, so the entry is always installed before any
	// callback can observe the slot.
	entry.timer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		if s.autoPending[convID] != entry {
			s.mu.Unlock()
			return // superseded between arm and fire: the fresh entry owns the slot
		}
		// P1: register the distill goroutine BEFORE releasing s.mu —
		// the identity check can pass only while holding mu, so this Add
		// is serialized against stopAutoDistill's clear: any fire that
		// passed the check is counted before distillWG.Wait can return.
		s.distillWG.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.distillWG.Done()
			s.runAutoDistill(convID, trigger)
		}()
	})
	s.autoPending[convID] = entry
	s.journalAuto(ctx, convID, "scheduled", fmt.Sprintf(
		"trigger=%s eta=%s window_events=%d window_bytes=%d",
		trigger, fireAt.UTC().Format(time.RFC3339), stats.events, stats.eligibleBytes))
}

// runAutoDistill is the fire path (timer callback goroutine → here): claim
// the pending slot, re-evaluate everything (eligibility, coverage honesty,
// failure backoff, frequency caps), journal the outcome, and either drive
// distillCore or journal the skip. Backoff skips re-arm at the remainder
// of the backoff window so the attempt converges without user activity.
func (s *Server) runAutoDistill(convID int64, trigger string) {
	ctx := context.Background()
	s.mu.Lock()
	entry := s.autoPending[convID]
	if entry == nil {
		s.mu.Unlock()
		return // disarmed between arm and fire
	}
	delete(s.autoPending, convID)
	s.mu.Unlock()

	c, err := s.store.GetConversation(ctx, convID)
	if err != nil {
		log.Printf("auto-distill: conversation %d gone at fire: %v", convID, err)
		return
	}
	prefs := s.resolveAutoPrefs()
	if !prefs.enabled {
		s.journalAuto(ctx, convID, "skipped", fmt.Sprintf("trigger=%s reason=disabled", trigger))
		return
	}
	events, err := s.store.ListEvents(ctx, convID, 0)
	if err != nil {
		log.Printf("auto-distill: list events for conversation %d: %v", convID, err)
		return
	}
	window := windowEvents(events)
	stats := measureWindow(window)
	skip := func(reason string) {
		s.journalAuto(ctx, convID, "skipped", fmt.Sprintf(
			"trigger=%s window_events=%d window_bytes=%d reason=%s",
			trigger, stats.events, stats.eligibleBytes, reason))
	}

	if stats.events < prefs.minEvents {
		skip("below_min_events")
		return
	}
	if stats.eligibleBytes < prefs.minBytes {
		skip("below_min_bytes")
		return
	}

	// Failure backoff: consecutive failed rows since the newest
	// user-visible reset (user message or successful distill marker).
	failures, lastFailAt := consecutiveAutoFailures(events)
	if failures > len(autoBackoffSteps) {
		skip("backoff_suspended")
		return
	}
	if failures > 0 {
		wait := autoBackoffSteps[failures-1]
		if retryIn := wait - time.Since(lastFailAt); retryIn > 0 {
			skip(fmt.Sprintf("backoff retry_after=%s", retryIn.Round(time.Second)))
			s.mu.Lock()
			// Reclaim the attempt at the backoff deadline (armed, journaled,
			// disarmable like any other pending row).
			s.armAutoLocked(ctx, convID, trigger, retryIn, stats)
			s.mu.Unlock()
			return
		}
	}

	// Frequency caps (manual markers never carry an auto trigger, so they
	// are excluded by the count queries by construction).
	hourAgo := time.Now().UTC().Add(-time.Hour).Format(autoEventTimeLayout)
	if n, err := s.store.CountAutoDistillsForConversation(ctx, convID, hourAgo); err == nil && n >= prefs.maxPerHour {
		skip("hourly_cap")
		return
	}
	dayAgo := time.Now().UTC().Add(-24 * time.Hour).Format(autoEventTimeLayout)
	if w, werr := s.store.GetWorkstream(ctx, c.WorkstreamID); werr == nil {
		if n, err := s.store.CountAutoDistillsForProject(ctx, w.ProjectID, dayAgo); err == nil && n >= prefs.dailyCap {
			times, terr := s.store.AutoDistillTimesForProject(ctx, w.ProjectID, dayAgo)
			if terr != nil || len(times) == 0 {
				// Horizon unreadable: fall back to the pre-fix journaled
				// skip — a suspension without a trustworthy horizon would
				// fabricate a resume time (worse than one noisy row).
				skip("daily_cap")
				return
			}
			// Storm fix: suspend, never skip-loop. The first hit per
			// suspension window journals the single cap_suspended_until row
			// and arms the single resume timer; concurrent capped fires
			// converge inside the critical section (FIX 1).
			horizon := autoCapResumeAt(times, prefs.dailyCap, time.Now())
			s.mu.Lock()
			s.installAutoCapLocked(ctx, convID, w.ProjectID, horizon, true)
			s.mu.Unlock()
			return
		}
	}

	// Concurrency registration — the TOCTOU bridge between the unlocked
	// evaluation above and the in-flight state sends race against.
	s.mu.Lock()
	if runID, ok := s.byConv[convID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			s.mu.Unlock()
			skip("run_active")
			return
		}
	}
	if _, ok := s.distillKind[convID]; ok {
		s.mu.Unlock()
		skip("distill_active")
		return
	}
	if s.slashing[convID] > 0 {
		s.mu.Unlock()
		skip("slash_active")
		return
	}
	// Mid-delete/deleted lane bar (2026-08-25 review follow-up): w loads
	// under this hold; between them the flag and the SQL commit leave no
	// start window.
	if w, werr := s.store.GetWorkstream(ctx, c.WorkstreamID); werr != nil {
		s.mu.Unlock()
		skip("workstream_lookup")
		return
	} else if err := s.guardLiveWorkstreamLocked(w); err != nil {
		s.mu.Unlock()
		skip("workstream_deleted")
		return
	}
	s.distilling[convID] = struct{}{}
	s.distillKind[convID] = trigger
	distillCtx, cancel := context.WithCancel(ctx)
	s.autoInFlight[convID] = &autoInFlight{trigger: trigger, cancel: cancel}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.distilling, convID)
		delete(s.distillKind, convID)
		delete(s.autoInFlight, convID)
		cancel()
		s.mu.Unlock()
	}()

	s.journalAuto(ctx, convID, "fired", fmt.Sprintf(
		"trigger=%s window_events=%d window_bytes=%d", trigger, stats.events, stats.eligibleBytes))
	if _, err := s.distillCore(distillCtx, c, trigger); err != nil {
		// A cancellation was journaled by the cancelling send already —
		// the aborted core must not double-write an outcome row.
		s.mu.Lock()
		ifl := s.autoInFlight[convID]
		wasCancelled := (ifl != nil && ifl.cancelled) || errors.Is(err, errAutoDistillCancelled)
		s.mu.Unlock()
		if wasCancelled {
			return
		}
		if errors.Is(err, errAutoDistillSuperseded) {
			// P1-2: the supersession journaled itself; nothing failed, so
			// no failed row (backoff must not count it). Re-arm as a T1
			// idle timer: the fresh fold renders the grown window, and the
			// idle delay converges when the unattributed writer is still
			// active (an immediate retry could supersede-loop).
			s.mu.Lock()
			s.armAutoLocked(ctx, convID, distillTriggerIdle, s.resolvedIdle(prefs), stats)
			s.mu.Unlock()
			return
		}
		log.Printf("auto-distill: conversation %d: %v", convID, err)
		s.journalAuto(ctx, convID, "failed", fmt.Sprintf("trigger=%s error=%s", trigger, err))
	}
}

// installAutoCapLocked is the daily-cap suspension's check→journal→arm
// critical section: exactly the FIRST caller for a suspension window
// journals the single cap_suspended_until row and installs the entry;
// every later caller — a concurrent capped fire (FIX 1), the boot-time
// row restore, the activity gate's heal — only converges the horizon
// EARLIER (a fresher quota ledger wins; a later estimate never pushes the
// resume past the journaled record) and heals the single timer.
// journalRow=false is the boot restore: the row it re-arms from already
// IS the window's record. Caller holds s.mu.
func (s *Server) installAutoCapLocked(ctx context.Context, convID, projectID int64, resumeAt time.Time, journalRow bool) {
	if s.autoStopped {
		return // shutdown began: a new resume timer would outlive the drain
	}
	now := time.Now()
	if entry := s.autoCap[projectID]; entry != nil {
		if entry.firing {
			return // the in-flight resume owns the window: its outcome decides
		}
		if now.Before(entry.resumeAt) {
			if resumeAt.Before(entry.resumeAt) && !resumeAt.Before(now) {
				entry.resumeAt = resumeAt
			}
			s.armAutoCapLocked(projectID, entry)
			return
		}
		// Horizon passed under us: the old suspension is over; the fresh
		// horizon below starts a new window (one new row).
		dropAutoCapEntry(entry)
		delete(s.autoCap, projectID)
	}
	entry := &autoCapEntry{convID: convID, resumeAt: resumeAt}
	s.autoCap[projectID] = entry
	if journalRow {
		s.journalAuto(ctx, convID, autoCauseCapSuspended, resumeAt.UTC().Format(time.RFC3339))
	}
	s.armAutoCapLocked(projectID, entry)
}

// armAutoCapLocked installs the entry's ONE resume timer (idempotent: a
// live timer or an in-flight resume short-circuits). Same claim pattern
// as armAutoLocked: the callback proceeds only while the registry still
// holds THIS entry with an unclaimed, unfired timer, and the resume
// goroutine joins distillWG before s.mu is released so Wait's drain
// counts it. Caller holds s.mu.
func (s *Server) armAutoCapLocked(projectID int64, entry *autoCapEntry) {
	if s.autoCap[projectID] != entry || entry.timer != nil || entry.firing || s.autoStopped {
		return
	}
	delay := time.Until(entry.resumeAt)
	if delay < 0 {
		delay = 0
	}
	entry.timer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		if s.autoCap[projectID] != entry || entry.timer == nil || entry.firing {
			s.mu.Unlock()
			return // superseded or claimed between arm and fire
		}
		s.distillWG.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.distillWG.Done()
			s.runAutoCapResume(projectID)
		}()
	})
}

// dropAutoCapEntry stops the entry's timer WITHOUT touching the registry —
// the caller owns deletion (the map slot is the timer callback's identity).
func dropAutoCapEntry(entry *autoCapEntry) {
	if entry.timer != nil {
		entry.timer.Stop()
		entry.timer = nil
	}
}

// gateAutoCapLocked reports whether the conversation's project sits under
// a daily-cap suspension. While it does, activity journals NOTHING and
// arms NOTHING (the storm's per-activity scheduled+skipped pairs were
// what fed the un-folded window). The gate heals the entry in passing: a
// lost timer re-arms (FIX 2 — the gate never swallows the project's
// activity forever), an expired horizon drops so the evaluation falls
// through as the organic resume. Caller holds s.mu. Conv→project
// resolution is SQL under the lock (the guardLiveConversationLocked
// precedent, human-paced) and runs only while a suspension exists for
// SOME project, so the steady-state cost is nil.
func (s *Server) gateAutoCapLocked(ctx context.Context, convID int64) bool {
	if len(s.autoCap) == 0 {
		return false
	}
	c, err := s.store.GetConversation(ctx, convID)
	if err != nil {
		return false // unknown lane: the ordinary evaluation's own guards decide
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return false
	}
	entry := s.autoCap[w.ProjectID]
	if entry == nil {
		return false
	}
	if time.Now().Before(entry.resumeAt) {
		if entry.timer == nil && !entry.firing {
			log.Printf("auto-distill: re-arming lost cap-resume timer for project %d (deadline %s)",
				w.ProjectID, entry.resumeAt.UTC().Format(time.RFC3339))
		}
		s.armAutoCapLocked(w.ProjectID, entry)
		return true
	}
	if entry.firing {
		return true // the resume decides in a moment — stay silent
	}
	// Horizon passed without a fired resume (timer lost to a state the
	// gate's heal couldn't see): drop and fall through — the evaluation
	// below either re-caps (fresh suspension) or folds.
	dropAutoCapEntry(entry)
	delete(s.autoCap, w.ProjectID)
	return false
}

// autoCapResumeAt computes the earliest moment the project's 24h quota
// drops below cap: at the horizon the (len(times)-cap+1)-th oldest
// counted marker has aged out. A horizon already in the past collapses to
// now (the release is immediate — and a quota ledger with no pressure at
// all, len < cap, also reports now).
func autoCapResumeAt(times []string, cap int, now time.Time) time.Time {
	if cap < 1 {
		cap = 1
	}
	i := len(times) - cap
	if i < 0 {
		return now
	}
	resume := parseEventTime(times[i]).Add(24 * time.Hour)
	if resume.Before(now) {
		return now
	}
	return resume
}

// resolvedAutoCapRetry applies the test-seam override to the FIX-2 retry.
func (s *Server) resolvedAutoCapRetry() time.Duration {
	if s.autoCapRetry > 0 {
		return s.autoCapRetry
	}
	return autoCapRetryDelay
}

// autoCapQuota re-reads the project's 24h quota ledger: the earliest
// release time and whether the daily cap still holds.
func (s *Server) autoCapQuota(ctx context.Context, projectID int64) (resumeAt time.Time, capped bool, err error) {
	prefs := s.resolveAutoPrefs()
	dayAgo := time.Now().UTC().Add(-24 * time.Hour).Format(autoEventTimeLayout)
	times, err := s.store.AutoDistillTimesForProject(ctx, projectID, dayAgo)
	if err != nil {
		return time.Time{}, false, err
	}
	return autoCapResumeAt(times, prefs.dailyCap, time.Now()), len(times) >= prefs.dailyCap, nil
}

// autoCapKickConvs picks the resume's kick targets: the suspension's
// origin lane when still active, else every active conversation of the
// project (a lane deleted mid-suspension must not take the resume down
// with it — FIX 2). Each target gets ONE ordinary evaluation — a sub-min
// window journals its usual skip, an eligible one arms its usual timer,
// exactly like activity would have. A conversation-less project kicks
// nothing: no lane can own a foldable window, so the suspension simply
// ends.
func (s *Server) autoCapKickConvs(ctx context.Context, projectID, convID int64) ([]int64, error) {
	convs, err := s.store.ListActiveConversations(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("auto-cap resume: list conversations for project %d: %w", projectID, err)
	}
	for _, c := range convs {
		if c.ID == convID {
			return []int64{convID}, nil
		}
	}
	ids := make([]int64, 0, len(convs))
	for _, c := range convs {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

// runAutoCapResume is the suspension timer's fire path: re-check the
// quota, then either re-suspend (journal-free when the horizon held; ONE
// extension row when markers landed mid-suspension and moved it — never
// a skipped/scheduled backfill) or clear the suspension and restart the
// ordinary cycle with one evaluation per live lane. Entry bookkeeping
// runs under s.mu; lookups and the kick run outside it (the
// runAutoDistill shape). FIX 2 (timer resilience): a transient store or
// lookup failure re-arms the entry resolvedAutoCapRetry() out instead of
// dying — the suspension (and the activity gate's silence) never becomes
// permanent through one bad lookup.
func (s *Server) runAutoCapResume(projectID int64) {
	ctx := context.Background()
	s.mu.Lock()
	entry := s.autoCap[projectID]
	if entry == nil || entry.timer == nil || entry.firing {
		if entry != nil && !entry.firing {
			// Timer-less, non-firing entry is a state only a bug leaves
			// (the gate heals live ones): drop it so the next cap
			// evaluation re-installs cleanly.
			delete(s.autoCap, projectID)
		}
		s.mu.Unlock()
		return
	}
	entry.timer = nil
	entry.firing = true
	convID := entry.convID
	s.mu.Unlock()

	resumeAt, capped, qerr := s.autoCapQuota(ctx, projectID)
	if qerr != nil {
		log.Printf("auto-distill: cap resume quota recheck for project %d: %v — retrying", projectID, qerr)
		s.mu.Lock()
		if s.autoCap[projectID] == entry {
			entry.firing = false
			entry.resumeAt = time.Now().Add(s.resolvedAutoCapRetry())
			s.armAutoCapLocked(projectID, entry)
		}
		s.mu.Unlock()
		return
	}
	if capped {
		s.mu.Lock()
		if s.autoCap[projectID] == entry {
			delete(s.autoCap, projectID)
			entry.firing = false
			now := time.Now()
			switch {
			case !resumeAt.After(now):
				// Unparseable ledger timestamps: the horizon is UNKNOWN,
				// not "now" — an immediate re-fire would hot-loop. Retry
				// on the FIX-2 cadence, journal nothing.
				entry.resumeAt = now.Add(s.resolvedAutoCapRetry())
				s.autoCap[projectID] = entry
				s.armAutoCapLocked(projectID, entry)
			case resumeAt.Equal(entry.resumeAt):
				// The journaled horizon held: re-arm, journal NOTHING.
				s.autoCap[projectID] = entry
				s.armAutoCapLocked(projectID, entry)
			default:
				// Markers landed mid-suspension (an in-flight fold's
				// marker): ONE extension row with the truthful new horizon.
				s.installAutoCapLocked(ctx, convID, projectID, resumeAt, true)
			}
		}
		s.mu.Unlock()
		return
	}

	// Quota available: restart the ordinary cycle — each live lane is
	// re-evaluated ONCE; no catch-up of the silenced interval.
	targets, terr := s.autoCapKickConvs(ctx, projectID, convID)
	s.mu.Lock()
	if s.autoCap[projectID] == entry {
		if terr != nil {
			log.Printf("auto-distill: cap resume for project %d: %v — retrying", projectID, terr)
			entry.firing = false
			entry.resumeAt = time.Now().Add(s.resolvedAutoCapRetry())
			s.armAutoCapLocked(projectID, entry)
			s.mu.Unlock()
			return
		}
		delete(s.autoCap, projectID)
	}
	for _, id := range targets {
		s.maybeAutoAfterActivityLocked(ctx, id)
	}
	s.mu.Unlock()
}

// autoCapResumeForBadges derives the Memory tab's daily-cap chip for
// pending_counts. Read-only; never journals. Precedence: the in-memory
// suspension entry (authoritative while the daemon lives), then the
// journal's newest cap_suspended_until row (the durable record — survives
// restarts, and its passing horizon ENDS the chip: a hardened horizon is
// never extended by computation), then the upgrade fallback for journals
// that predate the row (oldest counted distill + 24h, marked computed).
// FIX 3: a disabled auto subsystem discloses NOTHING — no chip, and a
// disable never extends a stale impression past its timestamp.
func (s *Server) autoCapResumeForBadges(ctx context.Context, projectID int64) *AutoCapResumeInfo {
	prefs := s.resolveAutoPrefs()
	if !prefs.enabled {
		return nil
	}
	s.mu.Lock()
	if entry := s.autoCap[projectID]; entry != nil && time.Now().Before(entry.resumeAt) {
		info := &AutoCapResumeInfo{ResumeAtUnix: entry.resumeAt.Unix()}
		s.mu.Unlock()
		return info
	}
	s.mu.Unlock()
	payload, err := s.store.LatestAutoCapSuspension(ctx, projectID)
	if err == nil && payload != nil {
		var row struct {
			Detail string `json:"detail"`
		}
		jsonUnmarshalOK([]byte(*payload), &row)
		resumeAt, perr := time.Parse(time.RFC3339, row.Detail)
		if perr != nil || !time.Now().Before(resumeAt) {
			return nil
		}
		return &AutoCapResumeInfo{ResumeAtUnix: resumeAt.Unix()}
	}
	// Rowless upgrade path (or unreadable leverage — degrade quietly).
	dayAgo := time.Now().UTC().Add(-24 * time.Hour).Format(autoEventTimeLayout)
	times, terr := s.store.AutoDistillTimesForProject(ctx, projectID, dayAgo)
	if terr != nil || len(times) < prefs.dailyCap {
		return nil
	}
	resumeAt := autoCapResumeAt(times, prefs.dailyCap, time.Now())
	if !time.Now().Before(resumeAt) {
		return nil
	}
	return &AutoCapResumeInfo{ResumeAtUnix: resumeAt.Unix(), Computed: true}
}

// consecutiveAutoFailures walks the journal newest-first, counting
// auto_distill{failed} rows until a reset event: a user message ("next
// user event" clears suspension) or a successful distill marker (any
// trigger — a working fold proves the pipeline). The newest failure's
// journal timestamp drives the backoff window.
func consecutiveAutoFailures(events []store.Event) (failures int, lastFailedAt time.Time) {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		switch ev.Type {
		case store.EventUserMessage:
			return failures, lastFailedAt
		case store.EventReviewAction:
			var p struct {
				Action string `json:"action"`
			}
			if jsonUnmarshalOK(ev.Payload, &p) && p.Action == "distill" {
				return failures, lastFailedAt
			}
		case store.EventMemoryUpdate:
			var p struct {
				Layer string `json:"layer"`
				Cause string `json:"cause"`
			}
			if jsonUnmarshalOK(ev.Payload, &p) && p.Layer == "auto_distill" && p.Cause == "failed" {
				failures++
				if failures == 1 {
					lastFailedAt = parseEventTime(ev.CreatedAt)
				}
			}
		}
	}
	return failures, lastFailedAt
}

// jsonUnmarshalOK decodes a journaled payload into v, reporting whether
// the decode succeeded — malformed payloads read as absent, never fatal.
func jsonUnmarshalOK(data []byte, v interface{}) bool {
	return json.Unmarshal(data, v) == nil
}

// parseEventTime interprets the SQLite datetime('now') UTC timestamp the
// journal stamps on every row; unparseable values degrade to the zero time
// (a zero lastFailedAt makes every backoff window read as expired — the
// fail-open direction, which is a retry, never a loss).
func parseEventTime(createdAt string) time.Time {
	if t, err := time.Parse(autoEventTimeLayout, createdAt); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// StartupAutoScan is T2 (startup compensation), run once at daemon boot:
// every active conversation's un-folded window is recomputed from the
// journal — eligible AND stale (last event older than the idle period)
// conversations arm one startup trigger with 0–60s jitter, killing the
// "run ended after app close" missed-fold hole. It also runs the legacy
// auto_curate_after_distill migration and the startup half of the
// conditional auto-curate. Best-effort: per-conversation journal writes
// continue on individual errors.
func (s *Server) StartupAutoScan(ctx context.Context) error {
	p, err := s.resolveProject(ctx, s.projectRoot)
	if err != nil {
		return fmt.Errorf("auto-distill startup scan: %w", err)
	}
	convs, err := s.store.ListActiveConversations(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("auto-distill startup scan: %w", err)
	}
	s.migrateAutoCuratePref(ctx, convs)

	prefs := s.resolveAutoPrefs()
	if prefs.enabled {
		// Suspension restore (storm fix): a cap_suspended_until row whose
		// horizon outlived the daemon re-arms its entry and single resume
		// timer — a restart drops the in-memory registry but never the
		// journal. journalRow=false: the row being restored IS this
		// window's record. A passed horizon is skipped: the ordinary T2
		// scan below re-arms the stale windows, and their fires re-cap or
		// fold organically.
		if payload, err := s.store.LatestAutoCapSuspension(ctx, p.ID); err == nil && payload != nil && len(convs) > 0 {
			var row struct {
				Detail string `json:"detail"`
			}
			if jsonUnmarshalOK([]byte(*payload), &row) {
				if resumeAt, perr := time.Parse(time.RFC3339, row.Detail); perr == nil && time.Now().Before(resumeAt) {
					s.mu.Lock()
					s.installAutoCapLocked(ctx, convs[0].ID, p.ID, resumeAt, false)
					s.mu.Unlock()
				}
			}
		}
		// The cap (and its suspension) is project-wide, so a live
		// suspension silences T2 for the WHOLE project: journal NOTHING,
		// arm NOTHING — the resume timer owns the restart.
		s.mu.Lock()
		capEntry := s.autoCap[p.ID]
		suspended := capEntry != nil && time.Now().Before(capEntry.resumeAt)
		s.mu.Unlock()
		if suspended {
			// Journal NOTHING, arm NOTHING — the single resume timer owns
			// the restart, curate included (the resume's kick and later
			// distill successes drive it).
			return nil
		}
		idle := s.resolvedIdle(prefs)
		for _, c := range convs {
			events, err := s.store.ListEvents(ctx, c.ID, 0)
			if err != nil {
				log.Printf("auto-distill startup scan: list events for conversation %d: %v", c.ID, err)
				continue
			}
			if len(events) == 0 {
				continue // defensive: the freshness check below indexes the tail
			}
			window := windowEvents(events)
			stats := measureWindow(window)
			reason := ""
			switch {
			case stats.events < prefs.minEvents:
				reason = "below_min_events"
			case stats.eligibleBytes < prefs.minBytes:
				reason = "below_min_bytes"
			default:
				if last := parseEventTime(events[len(events)-1].CreatedAt); time.Since(last) < idle {
					reason = "window_fresh" // recent activity — the idle path owns it
				}
			}
			if reason != "" {
				s.journalAuto(ctx, c.ID, "skipped", fmt.Sprintf(
					"trigger=%s window_events=%d window_bytes=%d reason=%s",
					distillTriggerStartup, stats.events, stats.eligibleBytes, reason))
				continue
			}
			jitter := time.Duration(0)
			if s.autoJitter > 0 {
				jitter = time.Duration(rand.Int64N(int64(s.autoJitter)))
			}
			s.mu.Lock()
			s.armAutoLocked(ctx, c.ID, distillTriggerStartup, jitter, stats)
			s.mu.Unlock()
		}
	}

	// Startup half of the conditional auto-curate (the distill half lives
	// in distillCore's success path).
	if len(convs) > 0 {
		s.maybeAutoCurate(p.ID, convs[0].ID)
	}
	return nil
}

// migrateAutoCuratePref removes the retired M10 auto_curate_after_distill
// pref and journals the migration once (idempotence = the key being gone).
// The migration row lands in the first active conversation — the journal
// is per-conversation and the curate is project-wide; one row is truthful
// and findable, N identical rows would be noise dressed as rigor.
func (s *Server) migrateAutoCuratePref(ctx context.Context, convs []store.Conversation) {
	if adapter.LoadPrefsRaw("auto_curate_after_distill") == "" {
		return
	}
	removed, err := adapter.RemovePrefsKey("auto_curate_after_distill")
	if err != nil || !removed || len(convs) == 0 {
		if err != nil {
			log.Printf("auto-distill startup scan: remove legacy pref: %v", err)
		}
		return
	}
	if _, err := s.store.AppendEvent(ctx, convs[0].ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":  "curator",
		"cause":  "migration",
		"detail": "removed auto_curate_after_distill pref — auto-curate is now daemon-conditional (≥ auto_curate_min_notes new notes, or the latest curate older than auto_curate_max_age_days); the GUI chain is gone (M12)",
	})); err != nil {
		log.Printf("auto-distill startup scan: journal pref migration: %v", err)
	}
}

// maybeAutoCurate evaluates the conditional auto-curate after a successful
// distill and at startup: fire when ≥ auto_curate_min_notes distill
// markers landed since the latest passing curate, OR the latest curate is
// older than auto_curate_max_age_days. NEVER chained (no auto after every
// distill regardless of state) — the thresholds are the trigger. Runs
// detached: distill/startup callers are never blocked on a curate.
//
// M17 F4:
//   - Never-curated age leg: with lastAt == nil the age source is the
//     OLDEST UNRETRACTED epoch note's mtime — epoch notes are the curation
//     input (source of truth; topic pages are derived artifacts), so
//     their age measures curation staleness, and a never-curated project
//     (the M12 unreachable-by-construction case) can fire.
//   - Failure backoff: the newest curator failure (memory_update
//     {layer:"curator", cause:"failed"|"gate_failed"}, any trigger —
//     failure modes are input-state facts) suppresses auto retries for
//     autoCurateFailureBackoff; the suppression is DERIVED (journal rows,
//     like the auto-distill ladder) and a newer passing curate resets it.
//     The blocked evaluation journals skipped{reason:"backoff"} with the
//     journaled next-eligible-at — evaluations that would not fire anyway
//     stay silent.
func (s *Server) maybeAutoCurate(projectID, convID int64) {
	s.curateWG.Add(1)
	go func() {
		defer s.curateWG.Done()
		ctx := context.Background()
		prefs := s.resolveAutoPrefs()
		if !prefs.enabled {
			return
		}
		// allEpochNotes is the curator's own input shape — a curate needs
		// at least one source note before any trigger can matter.
		if notes, err := allEpochNotes(s.projectRoot); err != nil || len(notes) == 0 {
			return
		}
		since, lastAt, err := s.store.AutoCurateState(ctx, projectID)
		if err != nil {
			log.Printf("auto-curate: state: %v", err)
			return
		}
		maxAge := prefs.curateMaxAge
		if s.autoCurateAge != 0 {
			maxAge = s.autoCurateAge // test seam (no journal backdating)
		}
		trigger := ""
		switch {
		case since >= prefs.curateMinNotes:
			trigger = "auto_notes"
		case lastAt != nil:
			if time.Since(parseEventTime(*lastAt)) >= maxAge {
				trigger = "auto_age"
			}
		default:
			// Never curated: age the oldest unretracted note (retracted
			// ones are dead knowledge — they must not drag the clock).
			retracted := s.retractedNotes(ctx, convID)
			if oldest, ok := oldestUnretractedNoteMtime(s.projectRoot, retracted); ok &&
				time.Since(oldest) >= maxAge {
				trigger = "auto_age"
			}
		}
		if trigger == "" {
			return
		}
		// Failure backoff: a curate failure newer than the newest pass
		// gates auto retries until failedAt + autoCurateFailureBackoff.
		if failAt, err := s.store.LatestCurateFailureAt(ctx, projectID); err != nil {
			log.Printf("auto-curate: failure state: %v", err)
		} else if failAt != nil && (lastAt == nil || *failAt > *lastAt) {
			if next := parseEventTime(*failAt).Add(autoCurateFailureBackoff); time.Now().Before(next) {
				s.journalCurateSkip(ctx, convID, trigger, since, next)
				return
			}
		}
		s.runAutoCurate(ctx, projectID, convID, trigger, since)
	}()
}

// autoCurateFailureBackoff is the flat wait after a failed curate before
// auto-curate may retry — the M17 minimal mirroring of the auto-distill
// ladder (one step, derived from the journal, success resets).
const autoCurateFailureBackoff = 24 * time.Hour

// journalCurateSkip journals one memory_update{layer:"curator",
// cause:"skipped"} for a backoff-gated evaluation: the trigger it blocked
// and the next-eligible-at timestamp, so "why no curate?" has a durable
// journal answer.
func (s *Server) journalCurateSkip(ctx context.Context, convID int64, trigger string, notesSince int, nextEligibleAt time.Time) {
	if _, err := s.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":  "curator",
		"cause":  "skipped",
		"detail": fmt.Sprintf("trigger=%s notes_since=%d reason=backoff next_eligible_at=%s", trigger, notesSince, nextEligibleAt.UTC().Format(time.RFC3339)),
	})); err != nil {
		log.Printf("auto-curate: journal skip: %v", err)
	}
}

// runAutoCurate takes the project-wide curate slot and drives the shared
// curate pipeline with its trigger provenance. curateCore journals
// failures itself (including citation-gate failures); this wrapper only
// logs. Notes-since-last is re-measured inside curateCore for the marker.
func (s *Server) runAutoCurate(ctx context.Context, projectID, convID int64, trigger string, notesSince int) {
	s.mu.Lock()
	if s.curating {
		s.mu.Unlock()
		return // another curate (any trigger) is in flight
	}
	s.curating = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.curating = false
		s.mu.Unlock()
	}()
	if err := s.curateCore(ctx, projectID, convID, trigger, notesSince); err != nil {
		log.Printf("auto-curate (%s): %v", trigger, err)
	}
}

// handleAutoDistillCtl implements the composer chip's Cancel: disarm a
// scheduled (not yet fired) auto-distill. The disarm is journaled like any
// send-caused disarm. In-flight auto distills are NOT touched — a send
// cancels those (cancel-before-note), the chip only governs countdowns.
func (s *Server) handleAutoDistillCtl(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	if req.Action != "disarm" {
		return Response{}, fmt.Errorf("auto_distill_ctl: unsupported action %q (want \"disarm\")", req.Action)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	armed := s.autoPending[c.ID] != nil
	s.disarmAutoLocked(ctx, c.ID, "disarmed_by_user")
	return Response{Disarmed: armed}, nil
}
