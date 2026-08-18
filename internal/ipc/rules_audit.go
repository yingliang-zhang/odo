package ipc

// Self-improving MVP Wave 1 (design docs/design/self-improving-first-
// principles-2026-08-15.md §E): the measure step — `odo rules audit`
// joins memory.md injection receipts to human outcomes. Rules the learner
// lands via memory_propose → apply_memory are injected forever with no
// effectiveness tracking (the design's axis-(a) gap); this audit closes
// the loop as TELEMETRY ONLY: it flags rules for a human, it never
// retracts, demotes, or acts (flag-only, no auto-action, ever; measure is
// LLM-free per ADR-0003 inv 4, deterministic from journal events).
//
// Attribution model: cmd_skills_audit.go's, ported into this package so
// the engine ships where the journal seams live (ComputeAutonomy
// precedent) — send → terminal → diff → outcome, same window/boundary/
// errored-run rules, same AutoActor exclusion (only the human verdict is
// ground truth: auto_panel rows feed neither rule rows nor the baseline,
// they report as the separate auto lines). Where the skills audit scores
// per-skill receipt paths, this audit scores the single .odo/memory.md
// receipt. The receipt hashes the WHOLE injected block, so one block hash
// is the cohort: journalRuleSnapshots (W2 item 3) journals the exact
// injected bytes per change as memory_update{layer:"memory",
// cause:"snapshot", sha, content}, and a receipt hash resolves to the
// snapshot's rule set — "which rules were in play" replays exactly,
// without replaying memory_apply events (block-level attribution: the
// OUTCOME attributes to the whole block, never to one line inside it).
//
// Window (counterfactual) rule: a CURRENT memory.md rule becomes
// auditable only when the journal proves it was added within the audited
// span — at least one journaled snapshot whose rule set lacks it. A rule
// present in every snapshot existed the entire window: no before/after
// contrast exists, so it is excluded from rows (counted as pre-window).
// Rule identity is the rule TEXT (the `- <text> — cites: ...` group):
// reaffirmation bumps the epoch on the raw line without changing the
// text, and retraction/re-landing of the same text resumes the same
// identity. Opaque lines (no cites tag) are never candidates — they are
// human hand-edits, never rotation/retraction targets (learner spec §3).
// Receipt hashes with NO matching snapshot anywhere (pre-W2 journals)
// count as unknown-cohort outcomes: total/baseline signal, zero rule
// attribution — never a fabricated cohort.
//
// Baseline: the task spec's global pool — resolutions / total across ALL
// conversations (accepts+rejects+weak rejects minus the auto actor). It
// is NOT the skills audit's skill-free pool: every rule's own outcomes
// join the baseline, so the 2x rate legs are conservative by construction
// (a harmful rule dilutes its own margin). baseline N >= every row's
// injections, so the empty-baseline branch the skills audit needs cannot
// occur here.
//
// Rate math follows the skills audit exactly (second conventions are
// prohibited): weak rejects weight 0.5 via doubling, and both rate legs
// compare cross-multiplied integers — reject leg
// (2R+W)*baseN >= factor*(2BR+BW)*inj, accept leg A*baseN >=
// factor*BA*inj — so exactly-2x boundaries are not lost to float error.
// "effective" additionally requires >=1 human accept: with a zero-accept
// baseline the rate leg is 0>=0 vacuous and would flag every accept-less
// rule.
//
// Flags: harmful when ALL of injections >= 10, human rejects >= 3,
// rejects span >= 3 conversations, reject-rate >= 2x baseline; effective
// when accept-rate >= 2x baseline (harmful wins on overlap — risk first).
// Flag-only: rows land on stdout, one review_action{action:
// "memory_audit_flag"} per flagged rule on main's conversation, and one
// ledger.md section citing those seqs. Prior flag rows make re-runs
// idempotent: an unchanged evidence tuple (rule, verdict, injections,
// rejects, reject conversations) adds no new signal and is skipped — the
// re-measure step runs per epoch and the ledger must not accumulate
// duplicate sections for a journal state that has not moved.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

// rulesAuditMemoryReceipt is the receipt key of the always-injected
// project-memory layer (memoryLayers / slashContextBlock).
const rulesAuditMemoryReceipt = ".odo/memory.md"

// rulesAuditFlagAction is the journaled review_action for one flag row.
const rulesAuditFlagAction = "memory_audit_flag"

// rulesAuditActor is the journaled provenance of flag rows ("" = human
// click, AutoActor = the auto-land pipeline — both reserved).
const rulesAuditActor = "rules_audit"

// Flag thresholds (deterministic; see the header for the math). Named per
// the skills audit (skillFlagMin* precedent).
const (
	rulesFlagMinInjections          = 10 // resolved outcomes with the rule in play
	rulesFlagMinRejects             = 3  // human reject events
	rulesFlagMinRejectConversations = 3  // distinct conversations carrying those rejects
	rulesFlagRateFactor             = 2  // rate >= factor x baseline (both legs)
)

// RulesAuditThresholds is the machine-readable threshold set (JSON map).
func RulesAuditThresholds() map[string]int {
	return map[string]int{
		"min_injections":           rulesFlagMinInjections,
		"min_rejects":              rulesFlagMinRejects,
		"min_reject_conversations": rulesFlagMinRejectConversations,
		"rate_factor":              rulesFlagRateFactor,
	}
}

// RulesAuditRow is one per-rule line of the report.
type RulesAuditRow struct {
	Rule                string  `json:"rule"`
	Cites               string  `json:"cites,omitempty"`
	Injections          int     `json:"injections"` // resolved outcomes with the rule in play
	Accepts             int     `json:"accepts"`
	Rejects             int     `json:"rejects"`
	WeakRejects         int     `json:"weak_rejects"`
	Conversations       int     `json:"conversations"`
	RejectConversations int     `json:"reject_conversations"`
	AcceptRate          float64 `json:"accept_rate"`
	RejectRate          float64 `json:"reject_rate"`    // weak rejects weight 0.5
	Flag                string  `json:"flag,omitempty"` // "harmful" | "effective"
}

// RulesAuditBaseline is the global outcome pool (ALL non-auto resolutions
// across all conversations), per the task spec — not a rule-free pool.
type RulesAuditBaseline struct {
	Outcomes   int     `json:"outcomes"`
	AcceptRate float64 `json:"accept_rate"`
	RejectRate float64 `json:"reject_rate"`
}

// RulesAuditFlag is the evidence tuple of one journaled memory_audit_flag
// row; re-flag equality keys on it (identical numbers add nothing).
type RulesAuditFlag struct {
	Rule                string `json:"rule"`
	Verdict             string `json:"verdict"`
	Injections          int    `json:"injections"`
	Rejects             int    `json:"rejects"`
	RejectConversations int    `json:"reject_conversations"`
}

// RulesAuditReport is ComputeRulesAudit's result (and the CLI's --json
// shape).
type RulesAuditReport struct {
	ProjectRoot           string             `json:"project_root"`
	Journal               string             `json:"journal"`
	WorkstreamsScanned    int                `json:"workstreams_scanned"`
	ConversationsScanned  int                `json:"conversations_scanned"`
	Resolutions           int                `json:"resolutions"` // accepts+rejects+weak (non-auto)
	Accepts               int                `json:"accepts"`
	Rejects               int                `json:"rejects"`
	WeakRejects           int                `json:"weak_rejects"`
	MemoryFreeOutcomes    int                `json:"memory_free_outcomes"`
	AutoAccepts           int                `json:"auto_accepts"` // excluded from rows and baseline (M17 F5)
	AutoRejects           int                `json:"auto_rejects"`
	UnknownCohortOutcomes int                `json:"unknown_cohort_outcomes"`
	SnapshotCohorts       int                `json:"snapshot_cohorts"`
	NoSnapshots           bool               `json:"no_snapshots"`
	CurrentRules          int                `json:"current_rules"`
	WindowRules           int                `json:"window_rules"`
	PreWindowRules        int                `json:"pre_window_rules"`
	Rules                 []RulesAuditRow    `json:"rules"`
	Baseline              RulesAuditBaseline `json:"baseline"`
	Flagged               int                `json:"flagged"`
	FlagThresholds        map[string]int     `json:"flag_thresholds"`
	PriorFlags            []RulesAuditFlag   `json:"prior_flags,omitempty"`
}

// rulesSendInfo is one non-slash user_message: its seq and the memory.md
// block hash its receipt carried ("" = no memory block injected).
type rulesSendInfo struct {
	seq     int
	memHash string
}

// rulesTerminalInfo is one run terminal (agent_done / agent_error), minus
// panel/vision one-shots.
type rulesTerminalInfo struct {
	seq       int
	createdAt string
	errored   bool
}

// rulesReviewScan is one parsed review_action relevant to outcomes.
type rulesReviewScan struct {
	seq       int
	action    string // "accept" | "reject" | "moa_review"
	actor     string
	diffID    int64
	consensus string
}

// rulesConvScan is the parsed event stream of one conversation.
type rulesConvScan struct {
	sends     []rulesSendInfo
	terminals []rulesTerminalInfo
	reviews   []rulesReviewScan
}

// rulesOutcome is one resolved outcome plus its memory cohort (the newest
// .odo/memory.md receipt hash within its window).
type rulesOutcome struct {
	convID     int64
	resolveSeq int
	kind       string // "accept" | "reject" | "weak_reject" | "auto_*"
	memHash    string // "" = memory-free outcome
}

// rulesAuditSlashCommands mirrors the slash routes in handleSendMessage
// (server.go) and cmd_recall_audit.go's auditSlashCommands — keep all
// three in sync. Slash user_messages carry a .odo/memory.md receipt from
// the slash context block; counting them would score panel-only contexts
// as rule injections.
var rulesAuditSlashCommands = []string{"/panel", "/vision", "/preview"}

// rulesIsSlash reports whether a journaled user_message text is a slash
// payload, mirroring the daemon's routing rule: the trimmed text is
// "<cmd>" or "<cmd> <args>" for a routed command.
func rulesIsSlash(text string) bool {
	t := strings.TrimSpace(text)
	for _, cmd := range rulesAuditSlashCommands {
		if t == cmd || strings.HasPrefix(t, cmd+" ") {
			return true
		}
	}
	return false
}

// rulesScanConversation parses a conversation's events into sends,
// terminals, and review actions (scanConversation port — same payload
// contracts).
func rulesScanConversation(events []store.Event) rulesConvScan {
	var cs rulesConvScan
	for _, ev := range events {
		switch ev.Type {
		case store.EventUserMessage:
			var p struct {
				Text    string            `json:"text"`
				Receipt map[string]string `json:"receipt"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || rulesIsSlash(p.Text) {
				continue
			}
			cs.sends = append(cs.sends, rulesSendInfo{seq: ev.Seq, memHash: p.Receipt[rulesAuditMemoryReceipt]})
		case store.EventAgentDone, store.EventAgentError:
			var p struct {
				Panel  bool `json:"panel"`
				Vision bool `json:"vision"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && (p.Panel || p.Vision) {
				continue
			}
			cs.terminals = append(cs.terminals, rulesTerminalInfo{
				seq: ev.Seq, createdAt: ev.CreatedAt, errored: ev.Type == store.EventAgentError,
			})
		case store.EventReviewAction:
			var p struct {
				Action    string `json:"action"`
				Actor     string `json:"actor"`
				DiffID    int64  `json:"diff_id"`
				Consensus string `json:"consensus_verdict"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			switch p.Action {
			case "accept", "reject":
				cs.reviews = append(cs.reviews, rulesReviewScan{seq: ev.Seq, action: p.Action, actor: p.Actor, diffID: p.DiffID})
			case "moa_review":
				cs.reviews = append(cs.reviews, rulesReviewScan{seq: ev.Seq, action: p.Action, actor: p.Actor, diffID: p.DiffID, consensus: p.Consensus})
			}
		}
	}
	return cs
}

// rulesMapDiffTerminal assigns each diff the terminal of the run that
// produced it (mapDiffTerminal port): the NEWEST unclaimed terminal with
// created_at <= the diff's created_at, same-second ties FIFO by seq.
// Returns diffID -> terminal seq.
func rulesMapDiffTerminal(terminals []rulesTerminalInfo, diffs []store.Diff) map[int64]int {
	claimed := make([]bool, len(terminals))
	out := make(map[int64]int, len(diffs))
	for _, d := range diffs {
		best := -1
		for i, t := range terminals {
			if claimed[i] || t.createdAt > d.CreatedAt {
				continue
			}
			if best < 0 || t.createdAt > terminals[best].createdAt ||
				(t.createdAt == terminals[best].createdAt && t.seq < terminals[best].seq) {
				best = i
			}
		}
		if best >= 0 {
			claimed[best] = true
			out[d.ID] = terminals[best].seq
		}
	}
	return out
}

// rulesWindowMemHash returns the newest .odo/memory.md receipt hash among
// sends in (latest boundary < end, end] — the memory cohort of the
// resolving run (windowSkills port, single-key degenerate case). "" when
// no window send injected a memory block.
func rulesWindowMemHash(sends []rulesSendInfo, boundary []int, end int) string {
	start := 0
	for _, b := range boundary {
		if b < end && b > start {
			start = b
		}
	}
	hash := ""
	for _, s := range sends {
		if s.seq <= start || s.seq > end {
			continue
		}
		if s.memHash != "" {
			hash = s.memHash // sends ascend: newest cohort wins
		}
	}
	return hash
}

// rulesConvOutcomes joins one conversation's parsed events and diff rows
// into resolved outcomes with their memory cohorts (convOutcomes port).
// Diffs whose producing terminal cannot be identified are skipped.
func rulesConvOutcomes(cs rulesConvScan, diffs []store.Diff, convID int64) []rulesOutcome {
	termSeq := rulesMapDiffTerminal(cs.terminals, diffs)

	// The run's send for a diff: the newest send before its terminal.
	sendOfDiff := map[int64]int{}
	for diffID, tSeq := range termSeq {
		send := 0
		for _, s := range cs.sends {
			if s.seq < tSeq && s.seq > send {
				send = s.seq
			}
		}
		sendOfDiff[diffID] = send // 0 = un-attributable
	}

	// Latest human action per diff. auto_panel rows are NEVER the human
	// action (M17 F5): they neither override moa weak outcomes nor
	// masquerade as human accept/reject.
	humanSeq := map[int64]int{}
	for _, r := range cs.reviews {
		if (r.action == "accept" || r.action == "reject") && r.actor != AutoActor {
			if r.seq > humanSeq[r.diffID] {
				humanSeq[r.diffID] = r.seq
			}
		}
	}

	// Weak moa outcomes: consensus "reject" with no subsequent human
	// action on that diff.
	weak := map[int]bool{}
	for _, r := range cs.reviews {
		if r.action == "moa_review" && r.consensus == "reject" && humanSeq[r.diffID] <= r.seq {
			weak[r.seq] = true
		}
	}

	// Errored diffs: their reviews are infrastructure noise — neither
	// outcomes nor boundaries; the errored terminal closes the window.
	erroredTerm := make(map[int]bool, len(cs.terminals))
	for _, t := range cs.terminals {
		erroredTerm[t.seq] = t.errored
	}
	erroredDiff := map[int64]bool{}
	for diffID, tSeq := range termSeq {
		if erroredTerm[tSeq] {
			erroredDiff[diffID] = true
		}
	}

	// Boundaries up front: every human/weak resolution seq, every
	// diff-less terminal, and every errored terminal.
	claimed := map[int]bool{}
	for _, tSeq := range termSeq {
		claimed[tSeq] = true
	}
	var boundary []int
	for _, r := range cs.reviews {
		if r.action == "accept" || r.action == "reject" || weak[r.seq] {
			if erroredDiff[r.diffID] {
				continue
			}
			boundary = append(boundary, r.seq)
		}
	}
	for _, t := range cs.terminals {
		if !claimed[t.seq] || t.errored {
			boundary = append(boundary, t.seq)
		}
	}
	sort.Ints(boundary)

	var outcomes []rulesOutcome
	for _, r := range cs.reviews {
		kind := ""
		switch {
		case r.action == "accept" || r.action == "reject":
			kind = r.action
			if r.actor == AutoActor {
				// auto-land resolutions get their OWN labels — excluded
				// from rule rows AND the baseline (M17 F5).
				kind = "auto_" + r.action
			}
		case weak[r.seq]:
			kind = "weak_reject"
		default:
			continue
		}
		end := sendOfDiff[r.diffID]
		if _, ok := termSeq[r.diffID]; !ok || end == 0 {
			continue // diff not in this conversation or un-attributable
		}
		if erroredDiff[r.diffID] {
			continue // errored run: terminal is already a boundary
		}
		outcomes = append(outcomes, rulesOutcome{
			convID: convID, resolveSeq: r.seq, kind: kind,
			memHash: rulesWindowMemHash(cs.sends, boundary, end),
		})
	}
	return outcomes
}

// rulesOfContent returns the rule-text set of one memory.md snapshot
// (non-opaque lines only).
func rulesOfContent(content string) map[string]bool {
	set := map[string]bool{}
	for _, r := range parseMemoryLines(content) {
		if r.opaque || r.text == "" {
			continue
		}
		set[r.text] = true
	}
	return set
}

// aggregateRules folds outcomes into per-rule rows + the global baseline,
// applying the flag rule. Pure (no I/O) so the threshold edge cases are
// unit-testable without journal fixtures (aggregateSkills precedent).
// Returns the rows (sorted: flagged first — harmful before effective —
// then rejects, injections, rule text), the baseline, the memory-free and
// unknown-cohort outcome counts, and the in-window/pre-window current
// rule counts.
func aggregateRules(outcomes []rulesOutcome, cohorts map[string]map[string]bool, current []memoryRule) (
	rows []RulesAuditRow, base RulesAuditBaseline, memFree, unknown, windowRules, preWindowRules int) {

	rows = []RulesAuditRow{} // never nil: --json always renders an array

	// Current rule eligibility: text -> cites for non-opaque rules. The
	// window gate needs one journaled snapshot LACKING the rule; zero
	// snapshots means no window can be proven either way (pre-window and
	// in-window both stay 0 and NoSnapshots drives the report note).
	cites := map[string]string{}
	eligible := map[string]bool{}
	preWindow := 0
	for _, r := range current {
		if r.opaque || r.text == "" {
			continue
		}
		if _, dup := cites[r.text]; !dup {
			cites[r.text] = r.cites
		}
	}
	for text := range cites {
		inWindow := false
		for _, set := range cohorts {
			if !set[text] {
				inWindow = true
				break
			}
		}
		switch {
		case inWindow:
			eligible[text] = true
		case len(cohorts) > 0:
			preWindow++
		}
	}

	type acc struct {
		inj, acc, rej, weak int
		convs, rejConvs     map[int64]bool
	}
	byRule := map[string]*acc{}
	var bInj, bAcc, bRej, bWeak int
	for _, o := range outcomes {
		if strings.HasPrefix(o.kind, "auto_") {
			continue // auto-land resolutions feed only the auto lines
		}
		bInj++
		switch o.kind {
		case "accept":
			bAcc++
		case "reject":
			bRej++
		case "weak_reject":
			bWeak++
		}
		if o.memHash == "" {
			memFree++
			continue
		}
		set, ok := cohorts[o.memHash]
		if !ok {
			unknown++ // pre-W2 receipt: truth, but not rule signal
			continue
		}
		for text := range set {
			if !eligible[text] {
				continue
			}
			a := byRule[text]
			if a == nil {
				a = &acc{convs: map[int64]bool{}, rejConvs: map[int64]bool{}}
				byRule[text] = a
			}
			a.inj++
			a.convs[o.convID] = true
			switch o.kind {
			case "accept":
				a.acc++
			case "reject":
				a.rej++
				a.rejConvs[o.convID] = true
			case "weak_reject":
				a.weak++
			}
		}
	}

	base = RulesAuditBaseline{Outcomes: bInj}
	if bInj > 0 {
		base.AcceptRate = float64(bAcc) / float64(bInj)
		base.RejectRate = float64(2*bRej+bWeak) / float64(2*bInj)
	}
	for text, a := range byRule {
		row := RulesAuditRow{
			Rule: text, Cites: cites[text],
			Injections: a.inj, Accepts: a.acc, Rejects: a.rej, WeakRejects: a.weak,
			Conversations:       len(a.convs),
			RejectConversations: len(a.rejConvs),
			AcceptRate:          float64(a.acc) / float64(a.inj),
			RejectRate:          float64(2*a.rej+a.weak) / float64(2*a.inj),
		}
		// Flag legs, integer cross-multiplied (float trap: 2x0.15 in
		// float64 is 0.30000000000000004 — the exact boundary must flag).
		// bInj >= a.inj always (the row's outcomes are in the baseline),
		// so no empty-baseline branch is needed.
		harmful := a.inj >= rulesFlagMinInjections &&
			a.rej >= rulesFlagMinRejects &&
			len(a.rejConvs) >= rulesFlagMinRejectConversations &&
			(2*a.rej+a.weak)*bInj >= rulesFlagRateFactor*(2*bRej+bWeak)*a.inj
		effective := a.acc >= 1 &&
			a.acc*bInj >= rulesFlagRateFactor*bAcc*a.inj
		switch {
		case harmful:
			row.Flag = "harmful"
		case effective:
			row.Flag = "effective"
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		fi, fj := rows[i].Flag, rows[j].Flag
		if (fi != "") != (fj != "") {
			return fi != ""
		}
		if fi != fj {
			return fi == "harmful" // risk first
		}
		if rows[i].Rejects != rows[j].Rejects {
			return rows[i].Rejects > rows[j].Rejects
		}
		if rows[i].Injections != rows[j].Injections {
			return rows[i].Injections > rows[j].Injections
		}
		return rows[i].Rule < rows[j].Rule
	})
	return rows, base, memFree, unknown, len(eligible), preWindow
}

// rulesPriorFlags extracts the evidence tuples of memory_audit_flag rows
// already journaled on a conversation (re-run idempotence key).
func rulesPriorFlags(events []store.Event) []RulesAuditFlag {
	var out []RulesAuditFlag
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action              string `json:"action"`
			Verdict             string `json:"verdict"`
			Rule                string `json:"rule"`
			Injections          int    `json:"injections"`
			Rejects             int    `json:"rejects"`
			RejectConversations int    `json:"reject_conversations"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.Action != rulesAuditFlagAction {
			continue
		}
		out = append(out, RulesAuditFlag{
			Rule: p.Rule, Verdict: p.Verdict,
			Injections: p.Injections, Rejects: p.Rejects, RejectConversations: p.RejectConversations,
		})
	}
	return out
}

// NovelFlags returns the flagged rows whose evidence tuple has not been
// journaled before (identical numbers carry no new signal; re-runs stay
// idempotent).
func (r RulesAuditReport) NovelFlags() []RulesAuditRow {
	seen := map[RulesAuditFlag]bool{}
	for _, f := range r.PriorFlags {
		seen[f] = true
	}
	var out []RulesAuditRow
	for _, row := range r.Rules {
		if row.Flag == "" {
			continue
		}
		sig := RulesAuditFlag{
			Rule: row.Rule, Verdict: row.Flag,
			Injections: row.Injections, Rejects: row.Rejects, RejectConversations: row.RejectConversations,
		}
		if seen[sig] {
			continue
		}
		out = append(out, row)
	}
	return out
}

// RulesAuditMainWorkstream is where flag rows journal: the audit is
// project-scoped but the journal is per-conversation; main is the stable
// default every project seeds ("main" also keys `odo journal` defaults).
const RulesAuditMainWorkstream = "main"

// RulesAuditFlagPayload builds the review_action payload for one flag row
// (the journal contract GUI/learners may later read as DATA).
func RulesAuditFlagPayload(row RulesAuditRow, base RulesAuditBaseline) map[string]interface{} {
	return map[string]interface{}{
		"action":               rulesAuditFlagAction,
		"actor":                rulesAuditActor,
		"verdict":              row.Flag,
		"rule":                 row.Rule,
		"cites":                row.Cites,
		"injections":           row.Injections,
		"accepts":              row.Accepts,
		"rejects":              row.Rejects,
		"weak_rejects":         row.WeakRejects,
		"conversations":        row.Conversations,
		"reject_conversations": row.RejectConversations,
		"accept_rate":          row.AcceptRate,
		"reject_rate":          row.RejectRate,
		"baseline_accept_rate": base.AcceptRate,
		"baseline_reject_rate": base.RejectRate,
		"baseline_outcomes":    base.Outcomes,
	}
}

// RulesAuditLedgerEntry pairs one journaled flag row with its event seq —
// the ledger bullet cites the journaled review_action (ADR-0003 inv 4:
// every metric row names its journal source).
type RulesAuditLedgerEntry struct {
	Row RulesAuditRow
	Seq int
}

// AppendRulesAuditLedger writes the audit's ledger.md section: one bullet
// per journaled flag. Daemon-only file, pull-only consumption, LLM-free
// numbers (inv 4) — this CLI performs the daemon role for the measure
// step (the journal is opened read-write by the caller, unretract
// precedent).
func AppendRulesAuditLedger(projectRoot string, base RulesAuditBaseline, entries []RulesAuditLedgerEntry) error {
	metrics := make([]ledgerMetric, 0, len(entries))
	for _, e := range entries {
		r := e.Row
		m := ledgerMetric{
			event: "review_action/" + rulesAuditFlagAction,
			seq:   e.Seq,
		}
		if r.Flag == "harmful" {
			m.label = fmt.Sprintf("memory rule %q flagged harmful", r.Rule)
			m.value = fmt.Sprintf("injections %d · rejects %d across %d conversation(s) · reject-rate %.1f%% vs baseline %.1f%%",
				r.Injections, r.Rejects, r.RejectConversations, r.RejectRate*100, base.RejectRate*100)
		} else {
			m.label = fmt.Sprintf("memory rule %q flagged effective", r.Rule)
			m.value = fmt.Sprintf("injections %d · accepts %d · accept-rate %.1f%% vs baseline %.1f%%",
				r.Injections, r.Accepts, r.AcceptRate*100, base.AcceptRate*100)
		}
		metrics = append(metrics, m)
	}
	return appendLedger(projectRoot, "rules audit", metrics)
}

// ComputeRulesAudit scans every active workstream's active conversation
// of the bound project (conversations are never deleted — epochs are
// counters on the single conversation row, so this covers all history),
// replays the memory.md snapshot cohorts, joins receipts to outcomes, and
// returns the flag report. Pure read; callers may use a read-only or a
// read-write store open (the CLI needs the write open for its flag sinks,
// like `odo unretract`).
func ComputeRulesAudit(ctx context.Context, st *store.Store, project store.Project) (RulesAuditReport, error) {
	report := RulesAuditReport{
		ProjectRoot:    project.RootPath,
		Journal:        filepath.Join(project.RootPath, ".odo", "journal.sqlite"),
		FlagThresholds: RulesAuditThresholds(),
	}
	streams, err := st.ListWorkstreams(ctx, project.ID)
	if err != nil {
		return report, err
	}
	report.WorkstreamsScanned = len(streams)

	type convData struct {
		id     int64
		wsName string
		events []store.Event
		diffs  []store.Diff
	}
	var convs []convData
	for _, w := range streams {
		c, cerr := st.GetActiveConversation(ctx, w.ID)
		if cerr != nil {
			continue // workstreams without a conversation contribute nothing
		}
		report.ConversationsScanned++
		events, lerr := st.ListEvents(ctx, c.ID, 0)
		if lerr != nil {
			continue // a half-readable conversation must not sink the whole audit
		}
		diffs, derr := st.ListDiffs(ctx, c.ID)
		if derr != nil {
			continue
		}
		convs = append(convs, convData{c.ID, w.Name, events, diffs})
	}

	// Pass 1 (global): every journaled memory.md cohort — snapshot sha ->
	// injected rule set. Global because a receipt's snapshot row may live
	// on another conversation (snapshots journal per conversation on its
	// first send after a change).
	cohorts := map[string]map[string]bool{}
	for _, cd := range convs {
		for _, ev := range cd.events {
			if ev.Type != store.EventMemoryUpdate {
				continue
			}
			var p struct {
				Layer   string `json:"layer"`
				Cause   string `json:"cause"`
				Content string `json:"content"`
				Sha     string `json:"sha"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || p.Layer != "memory" || p.Cause != "snapshot" || p.Sha == "" {
				continue
			}
			if _, seen := cohorts[p.Sha]; !seen {
				cohorts[p.Sha] = rulesOfContent(p.Content)
			}
		}
	}
	report.SnapshotCohorts = len(cohorts)
	report.NoSnapshots = report.SnapshotCohorts == 0

	// Pass 2: outcomes per conversation.
	var outcomes []rulesOutcome
	for _, cd := range convs {
		outcomes = append(outcomes, rulesConvOutcomes(rulesScanConversation(cd.events), cd.diffs, cd.id)...)
	}
	for _, o := range outcomes {
		switch o.kind {
		case "accept":
			report.Accepts++
		case "reject":
			report.Rejects++
		case "weak_reject":
			report.WeakRejects++
		case "auto_accept":
			report.AutoAccepts++
		case "auto_reject":
			report.AutoRejects++
		}
	}
	report.Resolutions = report.Accepts + report.Rejects + report.WeakRejects

	// Current memory.md rules (capped read — the same view injection uses:
	// a rule past the cap is not injected, so it earns no rows).
	var current []memoryRule
	for _, r := range parseMemoryLines(readProjectMemory(project.RootPath)) {
		if !r.opaque && r.text != "" {
			current = append(current, r)
		}
	}
	report.CurrentRules = len(current)

	rows, base, memFree, unknown, window, preWindow := aggregateRules(outcomes, cohorts, current)
	report.Rules = rows
	report.Baseline = base
	report.MemoryFreeOutcomes = memFree
	report.UnknownCohortOutcomes = unknown
	report.WindowRules = window
	report.PreWindowRules = preWindow
	for _, row := range rows {
		if row.Flag != "" {
			report.Flagged++
		}
	}

	// Prior flag rows (main conversation only — the sink target).
	for _, cd := range convs {
		if cd.wsName == RulesAuditMainWorkstream {
			report.PriorFlags = rulesPriorFlags(cd.events)
			break
		}
	}
	return report, nil
}
