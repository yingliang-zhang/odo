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
//                        hourly_cap | daily_cap | backoff | backoff_suspended |
//                        window_exceeds_prompt_budget | run_active |
//                        distill_active | slash_active | disarmed_by_send |
//                        disarmed_by_user | superseded_by_manual |
//                        superseded_by_urgent
//   cancelled_by_send  — a send/steer/slash cancelled an in-flight AUTO
//                        distill before the note write (cancel-before-note)
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

// autoInFlight is the cancel-before-note handle for a fired auto distill:
// a send/steer/slash flips cancelled + calls cancel; distillCore checks
// cancelled after the one-shot returns, before any artifact is written.
type autoInFlight struct {
	trigger   string
	cancel    context.CancelFunc
	cancelled bool
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

// windowStats measures one un-folded epoch window. eligibleBytes excludes
// /panel and /vision agent_text events (payload-flagged advisory answers):
// a 30KB panel reply must not trigger a fold by itself (M12 eligibility).
type windowStats struct {
	events        int
	eligibleBytes int
}

// measureWindow sizes the window with capEvents' render formula
// (len(type)+len(payload)+64) so eligibility, urgency, and coverage
// honesty all speak the same byte unit.
func measureWindow(window []store.Event) windowStats {
	var st windowStats
	for _, ev := range window {
		st.events++
		if ev.Type == store.EventAgentText {
			var p struct {
				Panel  bool `json:"panel"`
				Vision bool `json:"vision"`
			}
			if jsonUnmarshalOK(ev.Payload, &p) && (p.Panel || p.Vision) {
				continue
			}
		}
		st.eligibleBytes += len(ev.Type) + len(ev.Payload) + 64
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
// (journaled) and cancels an in-flight AUTO distill before the note is
// written (journaled cancelled_by_send, then the input proceeds — NO
// refusal). A MANUAL distill keeps let-finish + the historical refusal
// text. Caller holds s.mu.
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
// and an eligible window arms one idle timer. Concurrency coexistence
// (live run, in-flight distill, live slash) and double-arming short-circuit
// silently — those states re-visit this function on their own completion.
// Caller holds s.mu.
func (s *Server) maybeAutoAfterActivityLocked(ctx context.Context, convID int64) {
	prefs := s.resolveAutoPrefs()
	if !prefs.enabled {
		return
	}
	if s.autoPending[convID] != nil {
		return // already armed — never double-schedule
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

	// Coverage honesty: an auto fold whose prompt would silently drop
	// oldest events never fires — skip + journal + surface the block via
	// pending_counts (a manual distill remains the honest way out; T3
	// urgency makes reaching this a corner case by construction).
	if _, omitted := capEvents(window, distillPromptBytesCap); omitted > 0 {
		s.autoBlocked[convID] = "window_exceeds_prompt_budget"
		s.journalAuto(ctx, convID, "skipped", fmt.Sprintf(
			"trigger=%s window_events=%d window_bytes=%d reason=window_exceeds_prompt_budget",
			distillTriggerIdle, stats.events, stats.eligibleBytes))
		return
	}
	delete(s.autoBlocked, convID)

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
// never reclassifies urgency. Caller holds s.mu.
func (s *Server) armAutoLocked(ctx context.Context, convID int64, trigger string, delay time.Duration, stats windowStats) {
	if s.autoPending[convID] != nil {
		return // belt: maybeAutoAfterActivityLocked already checked
	}
	fireAt := time.Now().Add(delay)
	s.autoPending[convID] = &autoPendingEntry{
		trigger: trigger,
		fireAt:  fireAt,
		timer: time.AfterFunc(delay, func() {
			go s.runAutoDistill(convID, trigger)
		}),
	}
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
	if _, omitted := capEvents(window, distillPromptBytesCap); omitted > 0 {
		s.mu.Lock()
		s.autoBlocked[convID] = "window_exceeds_prompt_budget"
		s.mu.Unlock()
		skip("window_exceeds_prompt_budget")
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
			skip("daily_cap")
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
	delete(s.autoBlocked, convID)
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
		log.Printf("auto-distill: conversation %d: %v", convID, err)
		s.journalAuto(ctx, convID, "failed", fmt.Sprintf("trigger=%s error=%s", trigger, err))
	}
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
		idle := s.resolvedIdle(prefs)
		for _, c := range convs {
			events, err := s.store.ListEvents(ctx, c.ID, 0)
			if err != nil {
				log.Printf("auto-distill startup scan: list events for conversation %d: %v", c.ID, err)
				continue
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
				if _, omitted := capEvents(window, distillPromptBytesCap); omitted > 0 {
					reason = "window_exceeds_prompt_budget"
				} else if last := parseEventTime(events[len(events)-1].CreatedAt); time.Since(last) < idle {
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
func (s *Server) maybeAutoCurate(projectID, convID int64) {
	go func() {
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
		if since >= prefs.curateMinNotes {
			trigger = "auto_notes"
		} else if lastAt != nil && time.Since(parseEventTime(*lastAt)) >= maxAge {
			// The age trigger needs a curate to be old — a never-curated
			// project would otherwise fire after its very first note.
			trigger = "auto_age"
		}
		if trigger == "" {
			return
		}
		s.runAutoCurate(ctx, convID, trigger, since)
	}()
}

// runAutoCurate takes the project-wide curate slot and drives the shared
// curate pipeline with its trigger provenance. curateCore journals
// failures itself (including citation-gate failures); this wrapper only
// logs. Notes-since-last is re-measured inside curateCore for the marker.
func (s *Server) runAutoCurate(ctx context.Context, convID int64, trigger string, notesSince int) {
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
	if err := s.curateCore(ctx, convID, trigger, notesSince); err != nil {
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
