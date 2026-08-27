package ipc

// M19 (/loop): the journal contract + the pure fold everything derives
// from (docs/design/loop-design-lock.md C1: zero in-memory authority —
// this file's deriveLoopStates IS the loop's state machine truth, and the
// GUI mirrors it in gui/src/loop.ts).
//
// One event type, store.EventLoopEvent, discriminated by payload kind:
//
//	loop_started{mode, base, max_rounds, budget_tokens, hold_severity,
//	            auditors[], tasks?[], task_source?, file?, seed_diffs?[]}
//	loop_design_lock{task, goal, goal_sha16, design_sha16, design_lock |
//	            design_path, proposals[], consolidator{}, base_url_scrubbed}
//	loop_task_spawn{task, task_sha16, design_sha16, amended?,
//	            design_lock | design_path, goal_seqs?, prompt_tokens_est,
//	            origin:"loop_ctl"?}
//	loop_task_done{task, status:
//	            landed|settle_blocked|vetoed|design_infra|skipped, diff_id?, detail?}
//	loop_audit_round{round, subject_sha16, subject_bytes, findings_count,
//	            blocking_count, findings | findings_path, legs[],
//	            prev_findings_sha16?, verify_failed?}
//	loop_verdict{round, verdict: clean|fix|audit_infra|stall|round_cap,
//	            blocking_fps[], new_fps[], carried_fps[], reason}
//	loop_fix_spawn{round, findings_count, findings_sha16,
//	            prompt_tokens_est}
//	loop_diff_bound{diff_id, round? (Mode A) | task? (Mode B)}
//	            the loop⇄diff binding, journaled at DRAIN when the
//	            run's diff is inserted (the diff does not exist at
//	            spawn — P1 #13; the spawn row's old diff_id contract
//	            was dead). Task attribution (loopAdjudicateTask's
//	            accept/blocked gating) and the boot recovery's
//	            exclusion (loopOwnedSeedDiffIDs) key on it.
//	loop_suspended{cause, detail?}   cause: audit_infra | stall |
//	            fix_no_diff | run_tainted | risk:<class> |
//	            subject_too_large | seed_blocked | restart_mid_run |
//	            human_interleave | round_cap | design_infra |
//	            settle_blocked | spawn_failed
//	loop_completed{rounds, fixes_landed}
//	loop_stopped{detail?}
//	loop_budget_exceeded{spent_tokens, budget_tokens, projected}
//	loop_recovered{action: reran_audit | reran_design, detail?}
//	loop_resumed{cause, budget?, origin:"loop_ctl"?}
//	loop_notified{terminal_kind}
//
// Common keys on every daemon-written row: kind, loop_id (seq of the
// loop_started row), mode (audit|tasks|tasks_final — later rows of a
// tasks loop flip to tasks_final once the task list drains), actor
// "auto_loop", spent_tokens (cumulative: Σ leg output_tokens + spawn
// prompt estimates, re-derivable by the fold). Rows written from the
// GUI's loop_ctl carry origin:"loop_ctl" and no actor (a human click).
//
// Mode A fix lands journal through handleDiffAction as review_action
// accept{actor:"auto_loop"}; fix-pipeline evidence (verify failures,
// land failures) rides review_action auto_land_blocked{actor:"auto_loop",
// reason:"loop_*"} — the blocked-row-as-evidence channel, nothing about
// loop STATE (V1's review_action purity is about lifecycle rows).
//
// Mode B task adjudication attributes a post-spawn accept/blocked
// review_action to the task ONLY through the loop_diff_bound chain (a
// revise ladder's product rows chain to the bound diff via
// origin_diff_id) — or, on pre-binding journals (no loop_diff_bound
// row anywhere for the loop), by the pipeline-actor fallback
// (auto_loop/auto_panel). A human accept, or an unrelated inbox diff's
// pipeline rows, never resolve the task (P1 #13).

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
)

// loopActor marks every daemon-automation loop row (ComputeAutonomy
// excludes it from human streaks exactly like autoActor).
const loopActor = "auto_loop"

// loop kinds (the discriminated EventLoopEvent payload's kind key).
const (
	loopKindStarted        = "loop_started"
	loopKindDesignLock     = "loop_design_lock"
	loopKindTaskSpawn      = "loop_task_spawn"
	loopKindTaskDone       = "loop_task_done"
	loopKindAuditRound     = "loop_audit_round"
	loopKindVerdict        = "loop_verdict"
	loopKindFixSpawn       = "loop_fix_spawn"
	loopKindDiffBound      = "loop_diff_bound"
	loopKindSuspended      = "loop_suspended"
	loopKindCompleted      = "loop_completed"
	loopKindStopped        = "loop_stopped"
	loopKindBudgetExceeded = "loop_budget_exceeded"
	loopKindRecovered      = "loop_recovered"
	loopKindResumed        = "loop_resumed"
	loopKindNotified       = "loop_notified"
)

// Loop modes.
const (
	loopModeAudit      = "audit"
	loopModeTasks      = "tasks"
	loopModeTasksFinal = "tasks_final" // rows after the task list drains
)

// loop verdicts (loop_verdict.verdict; closed set per the design lock).
const (
	loopVerdictClean      = "clean"
	loopVerdictFix        = "fix"
	loopVerdictAuditInfra = "audit_infra"
	loopVerdictStall      = "stall"
	loopVerdictRoundCap   = "round_cap"
)

// loopTaskDone statuses (loop_task_done.status).
const (
	loopTaskLanded        = "landed"
	loopTaskSettleBlocked = "settle_blocked"
	loopTaskVetoed        = "vetoed"
	loopTaskDesignInfra   = "design_infra"
	loopTaskSkipped       = "skipped" // human rejected the orphaned diff after restart_mid_run
)

// loopBodyCapBytes: payload bodies over 32KB spill to .odo/loop/<id>/
// artifact files; the journal row carries sha16+path only (design lock).
const loopBodyCapBytes = 32 * 1024

// loopFixMarker decodes a user_message's loop provenance marker (the
// auto_revise precedent): presence marks the row as a loop-synthesized
// fix/implement prompt, not a human ask. Round is set for Mode A fix
// runs, Task for Mode B implement runs.
type loopFixMarker struct {
	LoopID int `json:"loop_id"`
	Round  int `json:"round,omitempty"`
	Task   int `json:"task,omitempty"`
}

// parseLoopFixMarker extracts the loop marker from a user_message payload;
// ok=false means the row is not a loop-spawned prompt.
func parseLoopFixMarker(payload []byte) (loopFixMarker, bool) {
	var p struct {
		Marker loopFixMarker `json:"loop_fix"`
	}
	if !jsonUnmarshalOK(payload, &p) || p.Marker.LoopID <= 0 {
		return loopFixMarker{}, false
	}
	return p.Marker, true
}

// --- prefs (fail-to-default, read per tick — a prefs edit takes effect on
// the loop's next decision, never retroactively) -----------------------------

// loopMaxRounds resolves loop_max_rounds (default 10, floor 1).
func loopMaxRounds() int {
	if v := adapter.LoadPrefsRaw("loop_max_rounds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return 10
}

// loopBudgetTokens resolves loop_budget_tokens (default 2M, floor 100K).
func loopBudgetTokens() int {
	if v := adapter.LoadPrefsRaw("loop_budget_tokens"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 100_000 {
			return n
		}
	}
	return 2_000_000
}

// loopHoldSeverity resolves loop_hold_severity: P1 | P2 (default P2 —
// P0/P1/P2 block, P3/nits never hold the loop). Unknown values log and
// fall back to P2 (fail-to-default, never fail-open).
func loopHoldSeverity() string {
	switch v := strings.ToUpper(strings.TrimSpace(adapter.LoadPrefsRaw("loop_hold_severity"))); v {
	case "":
		return "P2"
	case "P1", "P2":
		return v
	default:
		log.Printf("loop: unknown loop_hold_severity %q — using P2", v)
		return "P2"
	}
}

// loopAuditorModels resolves loop_auditor_models (default: the prefs
// review: line), parsed with the review-line shape.
func loopAuditorModels() []reviewModel {
	models := parseReviewModels(adapter.LoadPrefsRaw("loop_auditor_models"))
	if len(models) == 0 {
		models = parseReviewModels(adapter.LoadPrefsRaw("review"))
	}
	return models
}

// loopDesignGateAuto resolves loop_design_gate (human | auto; default
// human — a malformed value keeps the human gate, fail-closed).
func loopDesignGateAuto() bool {
	return strings.EqualFold(strings.TrimSpace(adapter.LoadPrefsRaw("loop_design_gate")), "auto")
}

// loopConsolidatorModel resolves loop_consolidator (default: the prefs
// orchestrator: line via runDesignMoa's "" resolution). Only the model
// part rides the moa call; the provider label is receipt-only.
func loopConsolidatorModel() string {
	model, _ := adapter.ParseModelProvider(adapter.LoadPrefsRaw("loop_consolidator"))
	return model
}

// loopRunAdapterLocked resolves the adapter (+ its registry key) that
// loop-spawned fix/implement runs start on (V12): the loop_implementer
// pref pins an explicit model@provider for loop runs only; absent or
// malformed, the default adapter (the coding: line) — normal send_message
// resolution is never involved either way.
//
// The override is REGISTERED under the stable key "loop" at first use
// (once, mutex-guarded, LRU-free): the runMeta's adapter key must resolve
// to the SAME instance at drain/cancel time — an orphan instance meant
// Events/Cancel answered "unknown run" forever while adapterFor("")
// silently resolved the default (P1). Registering once also pins the
// model at first spawn: a mid-loop prefs edit applies on the next daemon
// start, never retroactively to runs already keyed on the old instance.
// Caller holds s.mu (startLoopRunLocked's critical section).
func (s *Server) loopRunAdapterLocked() (adapter.Adapter, string) {
	raw := strings.TrimSpace(adapter.LoadPrefsRaw("loop_implementer"))
	if raw == "" {
		return s.adapterFor(""), ""
	}
	model, provider := adapter.ParseModelProvider(raw)
	if model == "" {
		log.Printf("loop: malformed loop_implementer %q — using the coding: line", raw)
		return s.adapterFor(""), ""
	}
	if ad, ok := s.adapterNamed("loop"); ok {
		return ad, "loop"
	}
	ad := adapter.NewOMPModelOverride(s.mgr.StateDir(), model, provider)
	s.adaptersMu.Lock()
	s.adapters["loop"] = ad
	s.adaptersMu.Unlock()
	return ad, "loop"
}

// --- spending ledger ---------------------------------------------------------

// loopRowSpend is one row's contribution to the loop's cumulative
// spend: per-leg wire receipts carry output_tokens; run spawns stamp a
// prompt estimate (chars/4). The GUI reads the latest row's spent_tokens;
// the fold re-derives the cumulative from any journal prefix.
func loopRowSpend(payload map[string]interface{}) int {
	// Normalize through JSON first: journal-time payloads carry CONCRETE
	// slices ([]auditLegResult, []DesignProposal) which a
	// .([]interface{}) assertion never satisfies (P1 — verified
	// mechanically: leg output_tokens silently accumulated to zero, so
	// the budget breaker never tripped on leg spend). The round-trip
	// makes extraction see exactly the wire shape the row journals.
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0
	}
	var p map[string]interface{}
	if err := json.Unmarshal(raw, &p); err != nil {
		return 0
	}
	total := 0
	if v, ok := p["output_tokens"].(float64); ok {
		total += int(v)
	}
	// Audit/design rows carry per-leg receipts.
	for _, key := range []string{"legs", "proposals"} {
		arr, ok := p[key].([]interface{})
		if !ok {
			continue
		}
		for _, item := range arr {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if v, ok := m["output_tokens"].(float64); ok {
				total += int(v)
			}
		}
	}
	// The design row's consolidator block carries its own output tokens.
	if c, ok := p["consolidator"].(map[string]interface{}); ok {
		if v, ok := c["output_tokens"].(float64); ok {
			total += int(v)
		}
	}
	if v, ok := p["prompt_tokens_est"].(float64); ok {
		total += int(v)
	}
	return total
}

// --- artifact spill (design lock: bodies >32KB live in .odo/loop/<id>/,
// the journal carries sha16+path only) -----------------------------------------

// loopSpillBody writes body to .odo/loop/<loopID>/<name> when it exceeds
// loopBodyCapBytes, returning the project-relative path and sha16. Bodies
// at/under the cap return ("", "", false) and ride the journal inline.
func (s *Server) loopSpillBody(loopID int64, name, body string) (relPath, sha string, err error) {
	dir := filepath.Join(s.projectRoot, ".odo", "loop", strconv.FormatInt(loopID, 10))
	relPath = filepath.Join(".odo", "loop", strconv.FormatInt(loopID, 10), name)
	abs := filepath.Join(s.projectRoot, relPath)
	// Contained spill (2026-08-25 review P0): the loop artifact tree lives
	// under the committable .odo/ — a planted symlink must not pull the
	// mkdir or the raw os.WriteFile (follows links) outside the project.
	if err := guardProjectWritePath(s.projectRoot, abs); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
		return "", "", err
	}
	return relPath, sha16([]byte(body)), nil
}

// spillField moves value into an artifact file when over cap: on spill the
// payload gains <field>_path + <field>_sha16 and <field> is replaced by a
// tombstone marker line (the row stays small; the bytes stay falsifiable).
func (s *Server) spillField(payload map[string]interface{}, loopID int64, field, name string) {
	raw, ok := payload[field].(string)
	if !ok || len(raw) <= loopBodyCapBytes {
		return
	}
	relPath, sha, err := s.loopSpillBody(loopID, name, raw)
	if err != nil {
		log.Printf("loop: spill %s: %v (body stays inline)", name, err)
		return
	}
	delete(payload, field)
	payload[field+"_path"] = relPath
	payload[field+"_sha16"] = sha
}

// loopArtifactBody reads back a spilled artifact with the two checks the
// write side attested but the readers skipped (2026-08-25 audit P2):
// containment through the read guard (the .odo/loop tree is daemon-owned;
// a planted symlink or a tampered journaled path must not read outside
// it) and the journaled <field>_sha16 — a replaced/corrupted artifact
// must never steer the next round's fix or implementation prompt as if
// it were the attested bytes. Any violation degrades to nil, the reader's
// "no body" path (fail closed: findings go empty, the design lock goes
// absent) — never to unverified content.
func (s *Server) loopArtifactBody(payload []byte, pathField string) []byte {
	rel := jsonStr(payload, pathField)
	if rel == "" {
		return nil
	}
	data, err := readWithinDir(s.projectRoot, filepath.Join(s.projectRoot, ".odo", "loop"), filepath.Join(s.projectRoot, rel))
	if err != nil {
		log.Printf("loop: spill %s unreadable: %v", rel, err)
		return nil
	}
	if sha := jsonStr(payload, strings.TrimSuffix(pathField, "_path")+"_sha16"); sha != "" && sha16(data) != sha {
		log.Printf("loop: spill %s sha16 mismatch — artifact refused (swapped after journaling)", rel)
		return nil
	}
	return data
}

// --- journaling ---------------------------------------------------------------

// journalLoop appends one EventLoopEvent row for loopID, stamping the
// common keys. Every row carries mode + spent_tokens so the GUI's fold is
// read-free; the daemon's tick re-derives regardless.
func (s *Server) journalLoop(ctx context.Context, conversationID, loopID int64, mode, kind string, payload map[string]interface{}, spent int) (store.Event, error) {
	payload["kind"] = kind
	payload["loop_id"] = loopID
	if mode != "" {
		payload["mode"] = mode
	}
	if _, ok := payload["actor"]; !ok {
		payload["actor"] = loopActor
	}
	payload["spent_tokens"] = spent
	return s.store.AppendEvent(ctx, conversationID, store.EventLoopEvent, mustJSON(payload))
}

// journalLoopBestEffort mirrors journalLoop but only logs failures (the
// journalLadder precedent — a broken journal must not wedge a pipeline;
// the fold simply sees less).
func (s *Server) journalLoopBestEffort(ctx context.Context, conversationID, loopID int64, mode, kind string, payload map[string]interface{}, spent int) {
	if _, err := s.journalLoop(ctx, conversationID, loopID, mode, kind, payload, spent); err != nil {
		log.Printf("loop: journal %s (loop %d, conv %d): %v", kind, loopID, conversationID, err)
	}
}

// --- the fold (C1: pure, journal-derived, restart-proof) ----------------------

// loopRound is one audit round's fold-relevant record.
type loopRound struct {
	seq          int
	round        int
	subjectSHA16 string
	blockingFPS  []string // sorted fps from the round's loop_verdict row
	verdict      string   // the round's loop_verdict.verdict
	completeLegs int      // legs parsed complete (audit_infra streak base)
}

// loopTask is one Mode B task's fold state.
type loopTask struct {
	n             int
	text          string
	spawned       bool
	done          bool
	doneStatus    string
	designLockSeq int // seq of the design lock awaiting the human gate (0 none)
	designSHA16   string
}

// loopState is one loop's derived state — the ONLY authority over what
// the loop does next (in-memory maps are liveness fast paths only).
type loopState struct {
	id           int64  // seq of loop_started
	status       string // active | suspended | completed | stopped
	cause        string // suspended cause (latest)
	mode         string // audit | tasks
	base         string
	maxRounds    int
	budgetTokens int
	holdSeverity string
	auditors     []string
	seedDiffs    []int64 // pending diff ids journaled at loop_started
	taskSource   string
	file         string

	tasks       []loopTask
	finalAudit  bool // all tasks done — remaining rows ride tasks_final
	rounds      []loopRound
	infraStreak int // consecutive trailing audit_infra verdicts

	awaitingFixSpawn bool // latest verdict is fix with no fix spawn after
	fixOpen          bool // a fix spawn has no accept/blocked/suspend after
	fixSpawnSeq      int
	// fixOutcome resolves the latest fix verdict's landing path:
	// "landed" (accept{auto_loop}), "unlanded" (verify/land evidence row
	// or a post-suspension re-audit decision), "" while open. The tick
	// launches the next audit round on either resolved value.
	fixOutcome string

	// loop_diff_bound bindings (P1 #13): the drain-journaled loop⇄diff
	// map. boundDiffs pairs with the start row's seed_diffs as the boot
	// recovery's exclusion (loopOwnedSeedDiffIDs); boundTasks gates
	// loopAdjudicateTask's accept/blocked attribution. Nil until the
	// loop's first binding row.
	boundDiffs map[int64]bool
	boundTasks map[int64]int // diff id → Mode B task n

	spentTokens   int
	fixesLanded   int // accept rows attributed to a Mode A fix (D1: pipeline actor + loop_diff_bound{round})
	resumedCause  string
	notifiedKinds map[string]bool

	lastKind string
	lastSeq  int
}

// active reports whether the loop still drives ticks (not terminal, not
// suspended). A suspended loop is still "the one loop per conversation" —
// a second /loop refuses while any non-terminal loop exists.
func (st *loopState) active() bool { return st.status == "active" }

// terminal reports whether the loop is done for good.
func (st *loopState) terminal() bool { return st.status == "completed" || st.status == "stopped" }

// deriveLoopStates folds one conversation's seq-ascending journal into
// every loop's state, keyed by loop id (= loop_started seq), in start
// order. THE mirror contract: gui/src/loop.ts implements the same fold
// over the same payload keys; vitest and Go tests pin matching behavior.
func deriveLoopStates(events []store.Event) []*loopState {
	var order []*loopState
	byID := map[int64]*loopState{}
	for _, ev := range events {
		switch ev.Type {
		case store.EventLoopEvent:
			var p struct {
				Kind   string `json:"kind"`
				LoopID int64  `json:"loop_id"`
			}
			if !jsonUnmarshalOK(ev.Payload, &p) || p.Kind == "" {
				continue
			}
			// The loop's id is the loop_started row's SEQ (payload loop_id
			// is 0 there — the journal is append-only, no post-hoc stamp);
			// every later row carries the real id in its payload.
			id := p.LoopID
			if p.Kind == loopKindStarted {
				id = int64(ev.Seq)
			}
			st := byID[id]
			if st == nil && p.Kind == loopKindStarted {
				st = foldLoopStarted(ev, id)
				byID[id] = st
				order = append(order, st)
			}
			if st == nil {
				continue // row of an unknown loop (corrupt journal) — skip
			}
			if p.Kind != loopKindStarted {
				foldLoopRow(st, ev, p.Kind)
			}
		case store.EventReviewAction:
			// Loop-owned pipeline facts: fix lands and blocked evidence
			// rows close the loop's open Mode A fix phase.
			var p struct {
				Action   string `json:"action"`
				Actor    string `json:"actor"`
				DiffID   int64  `json:"diff_id"`
				Reason   string `json:"reason"`
				GoalSeqs []int  `json:"goal_seqs"`
			}
			if !jsonUnmarshalOK(ev.Payload, &p) {
				continue
			}
			// Attribute to the loop with an open phase: the newest
			// non-terminal one (one active loop per conversation).
			st := newestFoldLoop(order)
			if st == nil {
				continue
			}
			// D1 attribution (2026-08-27 lock; post-reroute the fix lands
			// through the full auto-land path as auto_panel, no longer as
			// auto_loop): an accept/blocked row closes the fix phase IFF
			// (a) its actor is a pipeline actor (auto_loop or auto_panel)
			// AND (b) a loop_diff_bound{round} row names the diff. No
			// binding ⇒ no attribution, fail-closed: a human accept of an
			// unrelated inbox diff, or a pipeline row on a diff the loop
			// never owned, must never resolve the fix phase. Mode B task
			// bindings (boundTasks) close in loopAdjudicateTask instead,
			// never here.
			boundRound := st.boundDiffs[p.DiffID] && st.boundTasks[p.DiffID] == 0
			if !boundRound {
				continue
			}
			switch {
			case p.Action == "accept" && (p.Actor == loopActor || p.Actor == autoActor):
				st.fixesLanded++
				if st.fixOpen {
					st.fixOpen = false
					st.fixOutcome = "landed"
				}
				// A land between round N-1's verdict and round N is the
				// stall comparator's intervening fix — tracked by seq in
				// the tick (the fold keeps only counters).
			case p.Action == "auto_land_blocked" && (p.Actor == loopActor || p.Actor == autoActor):
				// Any attributed blocked row is the round fact the next
				// audit prompt reads — verify/panel/land failures, drift
				// refusals, panel minority suspends alike (D1+D7: the
				// loop's audit engine owns convergence).
				st.fixOpen = false
				st.fixOutcome = "unlanded"
			}
		}
	}
	return order
}

// newestFoldLoop returns the newest loop not in a terminal state (the one
// review/test rows attribute to), else nil.
func newestFoldLoop(order []*loopState) *loopState {
	for i := len(order) - 1; i >= 0; i-- {
		if !order[i].terminal() {
			return order[i]
		}
	}
	return nil
}

// foldLoopStarted builds the initial state from a loop_started payload.
func foldLoopStarted(ev store.Event, id int64) *loopState {
	st := &loopState{
		id:            id,
		status:        "active",
		spentTokens:   jsonInt(ev.Payload, "spent_tokens"),
		notifiedKinds: map[string]bool{},
		lastKind:      loopKindStarted,
		lastSeq:       ev.Seq,
	}
	var p struct {
		Mode         string   `json:"mode"`
		Base         string   `json:"base"`
		MaxRounds    int      `json:"max_rounds"`
		BudgetTokens int      `json:"budget_tokens"`
		HoldSeverity string   `json:"hold_severity"`
		Auditors     []string `json:"auditors"`
		Tasks        []string `json:"tasks"`
		TaskSource   string   `json:"task_source"`
		File         string   `json:"file"`
		SeedDiffs    []int64  `json:"seed_diffs"`
	}
	if jsonUnmarshalOK(ev.Payload, &p) {
		st.mode = p.Mode
		st.base = p.Base
		st.maxRounds = p.MaxRounds
		st.budgetTokens = p.BudgetTokens
		st.holdSeverity = p.HoldSeverity
		st.auditors = p.Auditors
		st.taskSource = p.TaskSource
		st.file = p.File
		st.seedDiffs = p.SeedDiffs
		for i, t := range p.Tasks {
			st.tasks = append(st.tasks, loopTask{n: i + 1, text: t})
		}
	}
	return st
}

// foldLoopRow folds one subsequent loop_event row into st.
func foldLoopRow(st *loopState, ev store.Event, kind string) {
	spent := jsonInt(ev.Payload, "spent_tokens")
	if spent > st.spentTokens {
		st.spentTokens = spent
	}
	st.lastKind, st.lastSeq = kind, ev.Seq
	switch kind {
	case loopKindSuspended:
		st.status = "suspended"
		st.cause = jsonStr(ev.Payload, "cause")
		st.fixOpen = false
		st.awaitingFixSpawn = false
	case loopKindBudgetExceeded:
		st.status = "suspended"
		st.cause = "budget_exceeded"
		st.fixOpen = false
		st.awaitingFixSpawn = false
	case loopKindCompleted:
		st.status = "completed"
	case loopKindStopped:
		st.status = "stopped"
	case loopKindResumed:
		st.status = "active"
		st.resumedCause = jsonStr(ev.Payload, "cause")
		st.cause = ""
		if b := jsonInt(ev.Payload, "budget"); b > 0 {
			st.budgetTokens = b
		}
		st.fixOpen = false
		st.awaitingFixSpawn = false
		latestFix := len(st.rounds) > 0 && st.rounds[len(st.rounds)-1].verdict == loopVerdictFix
		switch st.resumedCause {
		case "fix_no_diff", "run_tainted", "restart_mid_run":
			// The lock's "one automatic re-spawn on resume": the
			// interrupted fix respawns from the SAME findings.
			if latestFix && st.fixOutcome == "" {
				st.awaitingFixSpawn = true
			}
		default:
			// Every other suspend resolves to a RE-AUDIT on resume
			// (risk:* manually landed, human interleave, seed blocked…):
			// reality changed, the fixpoint re-derives it.
			if latestFix && st.fixOutcome == "" {
				st.fixOutcome = "unlanded"
			}
		}
	case loopKindRecovered:
		st.status = "active"
		st.cause = ""
	case loopKindAuditRound:
		r := loopRound{seq: ev.Seq, round: jsonInt(ev.Payload, "round"),
			subjectSHA16: jsonStr(ev.Payload, "subject_sha16")}
		// Leg receipts: count complete legs (the audit_infra streak base —
		// a round with zero complete legs cannot close clean, C4).
		var pr struct {
			Legs []struct {
				Verdict string `json:"verdict"`
			} `json:"legs"`
		}
		if jsonUnmarshalOK(ev.Payload, &pr) {
			for _, l := range pr.Legs {
				if l.Verdict == "complete" {
					r.completeLegs++
				}
			}
		}
		st.rounds = append(st.rounds, r)
		st.fixOpen = false // the round row may carry prior-facts — fix phase closed either way
	case loopKindVerdict:
		v := jsonStr(ev.Payload, "verdict")
		if len(st.rounds) > 0 {
			last := &st.rounds[len(st.rounds)-1]
			last.verdict = v
			last.blockingFPS = jsonStrList(ev.Payload, "blocking_fps")
		}
		switch v {
		case loopVerdictFix:
			st.awaitingFixSpawn = true
			st.fixOutcome = ""
			st.infraStreak = 0
		case loopVerdictAuditInfra:
			st.infraStreak++
		default:
			st.infraStreak = 0
		}
	case loopKindFixSpawn:
		st.awaitingFixSpawn = false
		st.fixOpen = true
		st.fixOutcome = ""
		st.fixSpawnSeq = ev.Seq
	case loopKindDiffBound:
		// P1 #13: the loop⇄diff binding row (drain-journaled). A fix
		// respawn after restart binds a second diff for the same round —
		// every bound id accumulates and stays in the exclusion set.
		if diffID := int64(jsonInt(ev.Payload, "diff_id")); diffID > 0 {
			if st.boundDiffs == nil {
				st.boundDiffs = map[int64]bool{}
			}
			st.boundDiffs[diffID] = true
			if t := jsonInt(ev.Payload, "task"); t >= 1 {
				if st.boundTasks == nil {
					st.boundTasks = map[int64]int{}
				}
				st.boundTasks[diffID] = t
			}
		}
	case loopKindDesignLock:
		t := jsonInt(ev.Payload, "task")
		if t >= 1 && t <= len(st.tasks) {
			st.tasks[t-1].designLockSeq = ev.Seq
			st.tasks[t-1].designSHA16 = jsonStr(ev.Payload, "design_sha16")
		}
	case loopKindTaskSpawn:
		t := jsonInt(ev.Payload, "task")
		if t >= 1 && t <= len(st.tasks) {
			st.tasks[t-1].spawned = true
			st.tasks[t-1].designLockSeq = 0
		}
	case loopKindTaskDone:
		t := jsonInt(ev.Payload, "task")
		if t >= 1 && t <= len(st.tasks) {
			st.tasks[t-1].done = true
			st.tasks[t-1].doneStatus = jsonStr(ev.Payload, "status")
			st.tasks[t-1].designLockSeq = 0
		}
		if st.mode == loopModeTasks && st.allTasksDone() {
			st.finalAudit = true
		}
	case loopKindNotified:
		k := jsonStr(ev.Payload, "terminal_kind")
		if k != "" {
			st.notifiedKinds[k] = true
		}
	}
}

// allTasksDone reports whether every Mode B task reached a terminal
// per-task status (landed, vetoed, settle_blocked, design_infra all count
// — the loop SUSPENDS on the blocked ones separately; "done" here marks
// the task list drained).
func (st *loopState) allTasksDone() bool {
	if len(st.tasks) == 0 {
		return false
	}
	for _, t := range st.tasks {
		if !t.done {
			return false
		}
	}
	return true
}

// latestVerdict returns the newest audit round's verdict ("" before any).
func (st *loopState) latestVerdict() string {
	if len(st.rounds) == 0 {
		return ""
	}
	return st.rounds[len(st.rounds)-1].verdict
}

// jsonStr/jsonInt/jsonStrList are the fold's payload readers (absent
// keys read as zero values; the fold never errors on half-written rows).
func jsonStr(payload []byte, key string) string {
	var m map[string]interface{}
	if !jsonUnmarshalOK(payload, &m) {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func jsonInt(payload []byte, key string) int {
	var m map[string]interface{}
	if !jsonUnmarshalOK(payload, &m) {
		return 0
	}
	f, _ := m[key].(float64)
	return int(f)
}

func jsonStrList(payload []byte, key string) []string {
	var m map[string]interface{}
	if !jsonUnmarshalOK(payload, &m) {
		return nil
	}
	arr, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// jsonIntList decodes a payload integer array.
func jsonIntList(payload []byte, key string) []int {
	var m map[string]interface{}
	if !jsonUnmarshalOK(payload, &m) {
		return nil
	}
	arr, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]int, 0, len(arr))
	for _, v := range arr {
		if f, ok := v.(float64); ok {
			out = append(out, int(f))
		}
	}
	return out
}

// fmtLoopMode is the row-mode helper: Mode B rows flip to tasks_final
// once the task list drains (the GUI chip buckets on it).
func fmtLoopMode(st *loopState) string {
	if st.mode == loopModeTasks && st.finalAudit {
		return loopModeTasksFinal
	}
	return st.mode
}

// loopOwnedSeedDiffIDs is recoverPendingDiffs' loop-exclusion set (P1
// #13): the pending diff ids a NON-terminal loop already owns, from two
// sources — loop_diff_bound rows (a drained loop run's inserted diff;
// the loop's own pipeline owns its outcome) and the loop_started row's
// seed_diffs (pending inbox diffs the loop claimed up front; they
// pre-date any pipeline run, so no binding row can ever name them).
// Without the exclusion the boot recovery re-fires auto-land on a diff
// the loop's restart tick simultaneously re-drives (the double-panel
// twin). Terminal loops (completed/stopped) contribute NOTHING: their
// orphans return to the normal inbox flow. Store read failures
// propagate (never a silently partial set — the exclusion is
// complete-or-absent; whether to proceed open or closed on error is
// the caller's policy, documented there).
func (s *Server) loopOwnedSeedDiffIDs(ctx context.Context) (map[int64]bool, error) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListAllPendingDiffs(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	pendingByConv := map[int64]map[int64]bool{}
	var convOrder []int64
	for _, r := range rows {
		if _, ok := pendingByConv[r.ConversationID]; !ok {
			pendingByConv[r.ConversationID] = map[int64]bool{}
			convOrder = append(convOrder, r.ConversationID)
		}
		pendingByConv[r.ConversationID][r.ID] = true
	}
	out := map[int64]bool{}
	for _, convID := range convOrder {
		events, err := s.store.ListEvents(ctx, convID, 0)
		if err != nil {
			return nil, err
		}
		pending := pendingByConv[convID]
		for _, st := range deriveLoopStates(events) {
			if st.terminal() {
				continue
			}
			for id := range st.boundDiffs {
				if pending[id] {
					out[id] = true
				}
			}
			for _, id := range st.seedDiffs {
				if pending[id] {
					out[id] = true
				}
			}
		}
	}
	return out, nil
}
