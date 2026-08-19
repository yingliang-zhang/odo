package ipc

// M19 (/loop): the daemon-driven audit fixpoint + task pipeline
// (docs/design/loop-design-lock.md). Architecture: pure journal fold
// (loop_journal.go — zero in-memory authority, ladderState doctrine) with
// ticks at three seams only (C1): the /loop slash route, drainRun's
// terminal tail (loop-provenance runs skip maybeAutoLand and drive the
// loop's own pipeline instead), and driver-goroutine completion under
// loopWG (curateWG precedent — the MoA fan-outs stay off the IPC thread).
//
// Mode A (audit): each round audits git diff base..HEAD (base frozen at
// loop start, V6), unions findings mechanically, and spawns a BYOF fix
// run for blocking findings (≤ hold severity). A landed fix re-audits
// until clean. Fix pipeline per round: risk gate (protected paths /
// supply-chain class → suspend, V5) → runVerifyGate verbatim → journaled
// autoActor land (handleDiffAction). Verify failure never lands and rides
// as advisory evidence into the next round (never a suspend).
//
// Mode B (tasks): per task, design = runDesignMoa (human design gate
// unless loop_design_gate: auto), implement run, review = s.autoLand
// VERBATIM (verify → panel → revise ladder ≤3 → majority valve →
// suspension — inherit, never fork, C8). The loop journals task-boundary
// rows and folds settle's terminal rows to learn outcomes. The drained
// task list flips to a final Mode A audit over the accumulated diff.
//
// Failure posture (headline): never auto-resume (suspends wait for
// /loop resume); infra is never a verdict (C4); one loop per
// conversation (C10); human sends suspend (V8) but are never refused.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
)

// --- /loop slash route ---------------------------------------------------------

// handleLoop is the /loop slash route (the /panel prefix-block precedent,
// routed in handleSendMessage outside s.mu). rest is everything after
// "/loop". Subcommands: audit | tasks | status | stop | resume.
func (s *Server) handleLoop(ctx context.Context, c *store.Conversation, rest string) (Response, error) {
	sub, args := rest, ""
	if i := strings.IndexAny(rest, " \n"); i >= 0 {
		sub, args = rest[:i], strings.TrimSpace(rest[i+1:])
	}
	switch sub {
	case "audit":
		return s.loopStartAudit(ctx, c, args)
	case "tasks":
		return s.loopStartTasks(ctx, c, args)
	case "status":
		return s.loopStatus(ctx, c)
	case "stop":
		return s.loopStopCmd(ctx, c, "stopped via /loop stop", "")
	case "resume":
		var budget int64
		for _, tok := range strings.Fields(args) {
			if v, ok := strings.CutPrefix(tok, "budget="); ok {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					budget = n
				}
			}
		}
		return s.loopResumeCmd(ctx, c, budget, "")
	default:
		return Response{}, fmt.Errorf("/loop: expected audit | tasks | status | stop | resume")
	}
}

// loopLeadingFlags splits /loop arguments into the LEADING run of
// key=value flag tokens (base=, rounds=, budget=) and the remaining
// body. The leading-run rule (P2) is the whole contract: flags parse
// only before the first non-flag token, so a k=v-looking token inside
// task text is inert — "/loop tasks rounds=2 1. set log warn=true" sets
// rounds but never a log flag, and the task text keeps its own token —
// and leading flags never pollute task 1's text either. For `audit`
// (flags-only grammar) the body is ignored, preserving old behavior.
func loopLeadingFlags(args string) (flags map[string]string, body string) {
	flags = map[string]string{}
	rest := strings.TrimSpace(args)
	for {
		rest = strings.TrimLeft(rest, " ")
		if rest == "" {
			return flags, ""
		}
		i := strings.IndexAny(rest, " \n\t")
		tok := rest
		if i >= 0 {
			tok = rest[:i]
		}
		k, v, ok := strings.Cut(tok, "=")
		if !ok || k == "" || v == "" {
			return flags, rest
		}
		flags[k] = v
		if i < 0 {
			return flags, ""
		}
		rest = rest[i+1:]
	}
}

// loopActiveStates folds the conversation and returns the newest
// non-terminal loop (nil when none — C10's admission check).
func (s *Server) loopActiveState(ctx context.Context, conversationID int64) (*loopState, []store.Event, error) {
	events, err := s.store.ListEvents(ctx, conversationID, 0)
	if err != nil {
		return nil, nil, err
	}
	return newestFoldLoop(deriveLoopStates(events)), events, nil
}

// journalLoopSlash journals the /loop user_message for transcript truth
// (the /panel shape: slash queries carry context_scope so originGoal and
// the human-interleave hook skip them).
func (s *Server) journalLoopSlash(ctx context.Context, c *store.Conversation, text string) error {
	_, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(map[string]interface{}{
		"text":          text,
		"context_scope": "/loop",
	}))
	return err
}

// loopStartAudit implements /loop audit [base=<sha>] [rounds=N] [budget=T].
func (s *Server) loopStartAudit(ctx context.Context, c *store.Conversation, args string) (Response, error) {
	// Preflight parity with Mode B (P2): Mode A's fix rounds land through
	// the same journaled autoActor pipeline — refuse outright unless
	// auto_apply: main.
	if adapter.ReadSettings().AutoApply != "main" {
		return Response{}, fmt.Errorf("loop: /loop audit requires auto_apply: main (fix rounds land through the auto-land pipeline)")
	}
	// Flags lead; Mode A has no body — a non-flag token past the leading
	// run is ignored (same as before, when unknown keys landed in opts).
	flags, _ := loopLeadingFlags(args)
	// Slash integrity gates (the /panel precedent): an in-flight AUTO
	// distill is cancelled pre-note; a manual one refuses. The one-loop
	// admission check (C10) rides the SAME critical section (P2 — an
	// unlocked fold raced a sibling /loop admission into a double start).
	s.mu.Lock()
	if err := s.gateAutoDistillForSendLocked(ctx, c.ID); err != nil {
		s.mu.Unlock()
		return Response{}, err
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("loop: agent already running for conversation %d — /loop audit starts on landed work", c.ID)
		}
	}
	st, _, err := s.loopActiveState(ctx, c.ID)
	s.mu.Unlock()
	if err != nil {
		return Response{}, err
	}
	if st != nil {
		return Response{}, fmt.Errorf("loop: a %s loop is already %s for this conversation%s — /loop resume or /loop stop first",
			st.mode, st.status, loopCauseSuffix(st))
	}

	base := flags["base"]
	if base == "" {
		if base, err = git.CurrentSHA(s.projectRoot); err != nil {
			return Response{}, fmt.Errorf("loop: read main HEAD: %w", err)
		}
	}
	rounds, budget := loopMaxRounds(), loopBudgetTokens()
	if v := flags["rounds"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			rounds = n
		}
	}
	if v := flags["budget"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 100_000 {
			budget = n
		}
	}
	auditors := loopAuditorModels()
	if len(auditors) == 0 {
		return Response{}, fmt.Errorf("loop: no auditor models — set the 'review:' line (or loop_auditor_models:) in prefs.md")
	}

	// SEED listing (V6): the conversation's pending diffs ride the started
	// row; the driver lands each via s.autoLand verbatim first.
	pending, err := s.store.ListPendingDiffs(ctx, c.ID)
	if err != nil {
		return Response{}, fmt.Errorf("loop: list pending diffs: %w", err)
	}
	// nothing_to_audit (V6): no diffs to seed, an empty accumulated
	// subject, and no explicit base to audit from — refuse pre-journal.
	if len(pending) == 0 && flags["base"] == "" {
		subject, derr := git.DiffRange(s.projectRoot, base, "HEAD")
		if derr != nil {
			return Response{}, fmt.Errorf("loop: resolve subject: %w", derr)
		}
		if strings.TrimSpace(subject) == "" {
			return Response{}, fmt.Errorf("loop: nothing_to_audit — no pending diffs and no accumulated diff (pass base=<sha> to audit a range)")
		}
	}

	seedIDs := make([]int64, 0, len(pending))
	for _, d := range pending {
		seedIDs = append(seedIDs, d.ID)
	}
	if err := s.journalLoopSlash(ctx, c, "/loop audit "+args); err != nil {
		return Response{}, err
	}
	auditorLabels := make([]string, 0, len(auditors))
	for _, m := range auditors {
		auditorLabels = append(auditorLabels, m.model+"@"+m.provider)
	}
	// The loop's id IS this row's seq (the lock's convention); the fold
	// derives it from seq (loop_started rows carry loop_id 0 — mutating
	// the payload post-append would break journal purity).
	ev, err := s.journalLoop(ctx, c.ID, 0, loopModeAudit, loopKindStarted, map[string]interface{}{
		"base":          base,
		"max_rounds":    rounds,
		"budget_tokens": budget,
		"hold_severity": loopHoldSeverity(),
		"auditors":      auditorLabels,
		"seed_diffs":    seedIDs,
	}, 0)
	if err != nil {
		return Response{}, err
	}
	s.fireLoopTick(c.ID)
	return Response{Event: &ev}, nil
}

// loopStartTasks implements /loop tasks <inline | file:<md> | queue>.
func (s *Server) loopStartTasks(ctx context.Context, c *store.Conversation, args string) (Response, error) {
	// Preflight (test-plan locked): Mode B's review stage is the auto-land
	// pipeline — tasks refuse outright unless auto_apply: main.
	if adapter.ReadSettings().AutoApply != "main" {
		return Response{}, fmt.Errorf("loop: /loop tasks requires auto_apply: main (the implement stage's review IS the auto-land pipeline)")
	}
	st, events, err := s.loopActiveState(ctx, c.ID)
	if err != nil {
		return Response{}, err
	}
	if st != nil {
		return Response{}, fmt.Errorf("loop: a %s loop is already %s for this conversation%s — /loop resume or /loop stop first",
			st.mode, st.status, loopCauseSuffix(st))
	}
	// Flags lead (P2): only the leading run of k=v tokens parses as
	// options; the REST is the task source. A k=v-looking token inside
	// task text is inert, and leading flags never pollute task 1's text.
	flags, taskBody := loopLeadingFlags(args)

	source, file, body := "inline", "", taskBody
	if strings.HasPrefix(body, "file:") {
		source, file = "file", strings.TrimSpace(strings.TrimPrefix(body, "file:"))
		data, err := s.readLoopTaskFile(file)
		if err != nil {
			return Response{}, fmt.Errorf("loop: %w", err)
		}
		body = data
	}
	var tasks []string
	var goalSeqs []int
	if body == "queue" {
		source = "queue"
		for _, g := range deriveParkedGoals(events) {
			tasks = append(tasks, g.text)
			goalSeqs = append(goalSeqs, g.seq)
		}
		if len(tasks) == 0 {
			return Response{}, fmt.Errorf("loop: queue is empty — park goals first")
		}
	} else {
		tasks = parseNumberedTasks(body)
		if len(tasks) == 0 {
			return Response{}, fmt.Errorf("loop: task list required — number each task on its own line (1. … 2. …), or pass file:<md> | queue (flags lead: /loop tasks rounds=2 …)")
		}
	}

	s.mu.Lock()
	if err := s.gateAutoDistillForSendLocked(ctx, c.ID); err != nil {
		s.mu.Unlock()
		return Response{}, err
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("loop: agent already running for conversation %d", c.ID)
		}
	}
	s.mu.Unlock()

	rounds, budget := loopMaxRounds(), loopBudgetTokens()
	if v := flags["rounds"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			rounds = n
		}
	}
	if v := flags["budget"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 100_000 {
			budget = n
		}
	}
	base, err := git.CurrentSHA(s.projectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("loop: read main HEAD: %w", err)
	}
	if err := s.journalLoopSlash(ctx, c, "/loop tasks "+args); err != nil {
		return Response{}, err
	}
	auditors := loopAuditorModels()
	auditorLabels := make([]string, 0, len(auditors))
	for _, m := range auditors {
		auditorLabels = append(auditorLabels, m.model+"@"+m.provider)
	}
	payload := map[string]interface{}{
		"base":          base,
		"max_rounds":    rounds,
		"budget_tokens": budget,
		"hold_severity": loopHoldSeverity(),
		"auditors":      auditorLabels,
		"tasks":         tasks,
		"task_source":   source,
	}
	if file != "" {
		payload["file"] = file
	}
	if len(goalSeqs) > 0 {
		payload["task_goal_seqs"] = goalSeqs
	}
	ev, err := s.journalLoop(ctx, c.ID, 0, loopModeTasks, loopKindStarted, payload, 0)
	if err != nil {
		return Response{}, err
	}
	s.fireLoopTick(c.ID)
	return Response{Event: &ev}, nil
}

// readLoopTaskFile reads a project-relative task file with the
// handleReadFile containment rule (V9: `..` escapes refused).
func (s *Server) readLoopTaskFile(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("file: empty path")
	}
	clean := filepath.Clean(rel)
	root := strings.TrimSuffix(s.projectRoot, "/")
	abs := root + "/" + clean
	if clean == "" || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || filepath.IsAbs(rel) || !strings.HasPrefix(abs, root+"/") {
		return "", fmt.Errorf("file %q escapes the project root", rel)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("file %q: %w", rel, err)
	}
	if len(data) > settleDiffCapBytes {
		return "", fmt.Errorf("file %q over the %dB cap", rel, settleDiffCapBytes)
	}
	return string(data), nil
}

// parseNumberedTasks parses a numbered task list: one item per line
// (`1. …`, also `1) …`), with a strictly-increasing single-line fallback
// for `1. … 2. …` pastes.
func parseNumberedTasks(text string) []string {
	var out []string
	lines := strings.Split(text, "\n")
	for _, ln := range lines {
		if t, ok := leadingNumber(ln, 0); ok && t != "" {
			out = append(out, t)
		}
	}
	if len(out) <= 1 && !strings.Contains(text, "\n") {
		// Single-line form: split at N. markers with strictly increasing N.
		out = nil
		want, rest := 1, " "+text+" "
		for want <= 64 {
			marker := fmt.Sprintf(" %d. ", want)
			i := strings.Index(rest, marker)
			if i < 0 {
				break
			}
			next := strings.Index(rest[i+len(marker):], fmt.Sprintf(" %d. ", want+1))
			var item string
			if next < 0 {
				item = rest[i+len(marker):]
			} else {
				item = rest[i+len(marker) : i+len(marker)+next]
			}
			if t := strings.TrimSpace(item); t != "" {
				out = append(out, t)
			} else {
				return nil // empty item in the middle: ambiguous — refuse
			}
			rest = rest[i:]
			want++
		}
	}
	return out
}

// leadingNumber parses a `N. text` / `N) text` line start, returning the
// text when the line is numbered and ncheck is satisfied (0 = any).
func leadingNumber(ln string, ncheck int) (string, bool) {
	ln = strings.TrimSpace(ln)
	i := 0
	for i < len(ln) && ln[i] >= '0' && ln[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(ln) || (ln[i] != '.' && ln[i] != ')') {
		return "", false
	}
	n, err := strconv.Atoi(ln[:i])
	if err != nil || (ncheck > 0 && n != ncheck) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimLeft(ln[i+1:], " ")), true
}

// loopCauseSuffix renders " (suspended: cause)" for refusal messages.
func loopCauseSuffix(st *loopState) string {
	if st.status == "suspended" && st.cause != "" {
		return " (suspended: " + st.cause + ")"
	}
	if st.status == "suspended" {
		return " (suspended)"
	}
	return ""
}

// suspendLoopOnHumanSendLocked implements V8: a human send/steer/park
// while a loop is active suspends it (human_interleave) — deterministic,
// the conversation NEVER refuses the send. Called right after the human
// user_message journals, so the suspension always postdates the send.
// Caller holds s.mu. A pending design gate survives the suspend (the
// lock row is journal-derived; /loop resume returns to the gate).
func (s *Server) suspendLoopOnHumanSendLocked(ctx context.Context, conversationID int64) {
	st, _, err := s.loopActiveState(ctx, conversationID)
	if err != nil || st == nil || !st.active() {
		return
	}
	s.journalLoopBestEffort(ctx, conversationID, st.id, fmtLoopMode(st), loopKindSuspended, map[string]interface{}{
		"cause":  "human_interleave",
		"detail": "a human message arrived while the loop was active — /loop resume restarts the loop",
	}, st.spentTokens)
}
