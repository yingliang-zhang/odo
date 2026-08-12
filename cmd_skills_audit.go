package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// M15 B-strategy-1 (S-1): `odo skills audit` joins skill injections to
// outcomes. Skill injections are already receipted on user_message.receipt
// (per-skill path -> sha16 of the injected block, internal/ipc/skills.go),
// but nothing ever measured whether runs with a skill in play end up
// accepted. This audit closes the loop as TELEMETRY ONLY: it flags
// underperforming skills for a human; it never downweights, retracts, or
// acts (flag-only, no auto-action, ever).
//
// Attribution model (per conversation, joined by temporal order):
//
//   - A run is one send -> one terminal event (agent_done / agent_error).
//     The diffs table pins which terminals produced a reviewable diff:
//     diff rows are inserted immediately after their terminal event, so
//     each diff maps to the newest unclaimed terminal with
//     created_at <= diff.created_at (same-second ties resolve FIFO by seq —
//     run order; second-precision timestamps cannot order events inside one
//     wall-clock second).
//   - Resolution events are: human accept/reject review_actions, no-diff
//     terminals (a run that produced no diff, errored or not — the window
//     closed; there is just no outcome), and un-overridden moa reject
//     reviews. Reviews of an errored run's diff are NOT resolutions:
//     drainRun journals a diff even for failed runs (partial changes are
//     reviewable), but infrastructure noise is not skill signal, so the
//     errored terminal alone closes that window, exactly like a no-diff
//     terminal. A human review action for the same diff always overrides a
//     prior moa review: overridden moa reviews are neither outcomes nor
//     boundaries.
//   - A skill is "in play" for an outcome when any non-slash user_message
//     in the same conversation between the previous resolution and the
//     resolved run's send carries the skill's path in its receipt
//     (receipt paths under .odo/skills/ — global and project scope share
//     the marker). Slash-command messages journal no skill receipts and
//     are excluded regardless.
//   - Outcome labels: human accept, human reject, moa weak reject
//     (consensus_verdict "reject" without a subsequent human review
//     action for that diff). agent_error runs never generate outcome
//     labels — infrastructure noise is not skill signal; their terminals
//     close the window whether or not a diff was journaled, so a failed
//     run's receipts cannot bleed into the next outcome.
//   - M17 F5 actor filter: review_action rows journaled with
//     actor:"auto_panel" (ipc.AutoActor — the auto-land pipeline) are NOT
//     human outcomes. They still close windows, and they get their own
//     auto_accept/auto_reject labels reported as the separate `auto`
//     count line, but every per-skill row AND the skill-free baseline
//     excludes them (live proof pre-fix: seq 6668
//     accept{actor:"auto_panel",diff_id:17} inflated accepts/baseline).
//     Auto-land's moa_review rows keep the M15 weak-signal semantics
//     (a tri-model consensus IS the weak signal), keyed off human
//     override only.
//
// Reported per skill (grouped by receipt path, carrying the block hash of
// the newest attributing window — the "last cohort"): injections (=
// resolved outcomes with the skill in play), accepts, rejects, weak
// rejects, accept/reject rates, the skill-free baseline rates, and a
// deterministic flag; auto-land resolutions report as the separate
// `auto` count line (F5). A skill is flagged only when ALL of: injections >=
// 10, human rejects >= 3, rejects span >= 3 distinct conversations, and
// reject-rate >= 2x the baseline reject-rate. Weak rejects weight 0.5 in
// the rates, and the rate comparison runs cross-multiplied in integers —
// (2R+W)*baseN >= factor*(2BR+BW)*inj — so exactly-2x boundaries are not
// lost to float error; an empty baseline means the rate leg is satisfied
// by any reject signal (already implied by the rejects >= 3 leg).
//
// Like `odo recall audit`: the journal is opened READ-ONLY (query_only),
// no daemon, no LLM. Output is a human report on stdout; --json emits one
// machine-readable object.
//
//	odo skills audit [--json]

const skillsAuditUsage = `usage: odo skills audit [--json]
  audit         skill outcome report over resolved runs across ALL
                conversations of the bound project (read-only, no daemon)
  --json        machine-readable report (one JSON object)`

// Flag thresholds (deterministic; see the header comment for the math).
const (
	skillFlagMinInjections          = 10 // resolved outcomes with the skill in play
	skillFlagMinRejects             = 3  // human reject events
	skillFlagMinRejectConversations = 3  // distinct conversations carrying those rejects
	skillFlagRejectRateFactor       = 2  // reject-rate >= factor x baseline reject-rate
)

// isSkillReceiptPath reports whether a journaled receipt key names a skill
// file. Skill receipts are keyed by display path: ".odo/skills/<name>.md"
// (project scope) or "<home>/.odo/skills/<name>.md" (global scope) — both
// contain the marker; no other layer's path does.
func isSkillReceiptPath(path string) bool {
	return strings.Contains(filepath.ToSlash(path), ".odo/skills/")
}

// skillRef is one skill sighting on a send: receipt path + block hash.
type skillRef struct {
	path      string
	blockHash string
}

// sendInfo is one non-slash user_message: its seq and the skills its
// receipt carried (sorted by path for determinism).
type sendInfo struct {
	seq    int
	skills []skillRef
}

// terminalInfo is one run terminal (agent_done / agent_error), excluding
// panel/vision one-shots (those journal agent_done with a mode marker and
// never enter the run/diff pipeline).
type terminalInfo struct {
	seq       int
	createdAt string
	errored   bool
}

// reviewScan is one parsed review_action relevant to outcomes: human
// accept/reject, or a moa_review with its consensus. actor carries the
// journaled provenance ("" = human click, ipc.AutoActor = the auto-land
// pipeline — M17 F5).
type reviewScan struct {
	seq       int
	action    string // "accept" | "reject" | "moa_review"
	actor     string
	diffID    int64
	consensus string // moa_review only
}

// convScan is the parsed event stream of one conversation.
type convScan struct {
	sends     []sendInfo
	terminals []terminalInfo
	reviews   []reviewScan
}

// skillOutcome is one resolved outcome plus the skills in play for it.
type skillOutcome struct {
	convID     int64
	resolveSeq int    // journal seq of the resolution event (ordering key)
	kind       string // "accept" | "reject" | "weak_reject"
	skills     []skillRef
}

// scanConversation parses a conversation's events into sends, terminals,
// and review actions.
func scanConversation(events []store.Event) convScan {
	var cs convScan
	for _, ev := range events {
		switch ev.Type {
		case store.EventUserMessage:
			var p struct {
				Text    string            `json:"text"`
				Receipt map[string]string `json:"receipt"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || isSlashMessage(p.Text) {
				continue
			}
			s := sendInfo{seq: ev.Seq}
			for path, hash := range p.Receipt {
				if isSkillReceiptPath(path) {
					s.skills = append(s.skills, skillRef{path: path, blockHash: hash})
				}
			}
			sort.Slice(s.skills, func(i, j int) bool { return s.skills[i].path < s.skills[j].path })
			cs.sends = append(cs.sends, s)
		case store.EventAgentDone, store.EventAgentError:
			var p struct {
				Panel  bool `json:"panel"`
				Vision bool `json:"vision"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && (p.Panel || p.Vision) {
				continue
			}
			cs.terminals = append(cs.terminals, terminalInfo{
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
				cs.reviews = append(cs.reviews, reviewScan{seq: ev.Seq, action: p.Action, actor: p.Actor, diffID: p.DiffID})
			case "moa_review":
				cs.reviews = append(cs.reviews, reviewScan{seq: ev.Seq, action: p.Action, actor: p.Actor, diffID: p.DiffID, consensus: p.Consensus})
			}
		}
	}
	return cs
}

// mapDiffTerminal assigns each diff the terminal event of the run that
// produced it: the NEWEST unclaimed terminal with created_at <= the
// diff's created_at (diff rows insert immediately after their terminal).
// Same-second ties resolve FIFO by seq — second-precision DATETIME cannot
// order events within one wall-clock second. Errored terminals
// participate: partial changes are reviewable, so a failed run can still
// carry a diff. Returns diffID -> terminal seq.
func mapDiffTerminal(terminals []terminalInfo, diffs []store.Diff) map[int64]int {
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

// convOutcomes joins one conversation's parsed events and diff rows into
// resolved outcomes with their in-play skills. Diffs whose producing
// terminal cannot be identified (legacy/truncated journals) are skipped —
// their outcomes never fabricate attribution.
func convOutcomes(cs convScan, diffs []store.Diff, convID int64) []skillOutcome {
	termSeq := mapDiffTerminal(cs.terminals, diffs)

	// The run's send for a diff: the newest send before its terminal.
	sendOfDiff := map[int64]int{}
	for diffID, tSeq := range termSeq {
		send := 0
		for _, s := range cs.sends {
			if s.seq < tSeq && s.seq > send {
				send = s.seq
			}
		}
		sendOfDiff[diffID] = send // 0 = no send found: un-attributable
	}

	// Latest human action per diff (a diff resolves at most once, but keep
	// the max-seq rule so pre-M15 double-review journals still parse).
	// M17 F5: actor:"auto_panel" rows are NEVER the human action — the
	// auto-land pipeline must not override moa weak outcomes nor masquerade
	// as human accept/reject (live proof: seq 6668 inflated the accepts).
	humanSeq := map[int64]int{}
	for _, r := range cs.reviews {
		if (r.action == "accept" || r.action == "reject") && r.actor != ipc.AutoActor {
			if r.seq > humanSeq[r.diffID] {
				humanSeq[r.diffID] = r.seq
			}
		}
	}

	// Weak moa outcomes: consensus "reject" with no subsequent human
	// action on that diff (a later human accept/reject overrides — the
	// moa review is then neither outcome nor boundary).
	weak := map[int]bool{} // review seq -> is weak outcome
	for _, r := range cs.reviews {
		if r.action == "moa_review" && r.consensus == "reject" && humanSeq[r.diffID] <= r.seq {
			weak[r.seq] = true
		}
	}

	// Errored diffs: drainRun journals a diff even for failed runs
	// (partial changes are reviewable), but a review of an errored run's
	// partial diff is infrastructure noise, not skill signal — such
	// reviews are neither outcomes nor boundaries; the errored terminal
	// itself closes the window, exactly like a no-diff terminal.
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

	// Boundaries are known UP FRONT (window computation needs the full
	// set, including resolutions that land later in the journal): every
	// human/weak resolution seq, plus every terminal that produced no
	// diff (the run closed with nothing to review — errored or not),
	// plus every errored terminal (its diff review is no resolution).
	claimed := map[int]bool{}
	for _, tSeq := range termSeq {
		claimed[tSeq] = true
	}
	var boundary []int
	for _, r := range cs.reviews {
		if r.action == "accept" || r.action == "reject" || weak[r.seq] {
			if erroredDiff[r.diffID] {
				continue // the errored terminal, not this review, is the boundary
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

	var outcomes []skillOutcome
	for _, r := range cs.reviews {
		kind := ""
		switch {
		case r.action == "accept" || r.action == "reject":
			kind = r.action
			if r.actor == ipc.AutoActor {
				// M17 F5: auto-land resolutions get their OWN labels —
				// excluded from per-skill rows AND the skill-free baseline,
				// counted as the separate `auto` line. They still carry the
				// in-play skills so the count is attributable.
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
			continue // errored run: no outcome label; terminal is already a boundary
		}
		outcomes = append(outcomes, skillOutcome{
			convID: convID, resolveSeq: r.seq, kind: kind,
			skills: windowSkills(cs.sends, boundary, end),
		})
	}
	return outcomes
}

// windowSkills returns the deduplicated skill set receipted on sends in
// (latest boundary < end, end]: the "in play" skills of the resolving
// run. boundary is the sorted resolution seqs so far INCLUDING the
// current outcome's own seq, which is >= end and therefore never the
// window's lower bound.
func windowSkills(sends []sendInfo, boundary []int, end int) []skillRef {
	start := 0
	for _, b := range boundary {
		if b < end && b > start {
			start = b
		}
	}
	lastHash := map[string]string{}
	for _, s := range sends {
		if s.seq <= start || s.seq > end {
			continue
		}
		for _, ref := range s.skills {
			lastHash[ref.path] = ref.blockHash // later sends overwrite: newest cohort wins
		}
	}
	out := make([]skillRef, 0, len(lastHash))
	for path, hash := range lastHash {
		out = append(out, skillRef{path: path, blockHash: hash})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// skillAuditRow is one per-skill line of the report.
type skillAuditRow struct {
	Path                string  `json:"path"`
	BlockHash           string  `json:"block_hash"` // last cohort: hash of the newest attributing window
	Injections          int     `json:"injections"` // resolved outcomes with the skill in play
	Accepts             int     `json:"accepts"`
	Rejects             int     `json:"rejects"`
	WeakRejects         int     `json:"weak_rejects"`
	RejectConversations int     `json:"reject_conversations"`
	AcceptRate          float64 `json:"accept_rate"`
	RejectRate          float64 `json:"reject_rate"` // weak rejects weight 0.5
	Flagged             bool    `json:"flagged"`
}

// skillAuditBaseline are the skill-free outcome pool rates.
type skillAuditBaseline struct {
	Outcomes   int     `json:"outcomes"`
	AcceptRate float64 `json:"accept_rate"`
	RejectRate float64 `json:"reject_rate"`
}

// skillsAuditReport is the --json shape.
type skillsAuditReport struct {
	ProjectRoot          string             `json:"project_root"`
	Journal              string             `json:"journal"`
	WorkstreamsScanned   int                `json:"workstreams_scanned"`
	ConversationsScanned int                `json:"conversations_scanned"`
	Accepts              int                `json:"accepts"`
	Rejects              int                `json:"rejects"`
	WeakRejects          int                `json:"weak_rejects"`
	SkillFreeOutcomes    int                `json:"skill_free_outcomes"`
	AutoAccepts          int                `json:"auto_accepts"` // M17 F5: auto-land resolutions, excluded from labels/baseline
	AutoRejects          int                `json:"auto_rejects"`
	Skills               []skillAuditRow    `json:"skills"`
	Baseline             skillAuditBaseline `json:"baseline"`
	FlagThresholds       map[string]int     `json:"flag_thresholds"`
}

// aggregateSkills folds outcomes into per-skill rows + the skill-free
// baseline, applying the flag rule. Pure (no I/O) so the threshold edge
// cases are unit-testable without journal fixtures.
func aggregateSkills(outcomes []skillOutcome) (rows []skillAuditRow, base skillAuditBaseline) {
	rows = []skillAuditRow{} // never nil: --json always renders an array
	type acc struct {
		inj, acc, rej, weak int
		rejectConvs         map[int64]bool
		lastHash            string
		lastSeq             int
	}
	bySkill := map[string]*acc{}
	var bInj, bAcc, bRej, bWeak int
	for _, o := range outcomes {
		if strings.HasPrefix(o.kind, "auto_") {
			continue // M17 F5: auto-land resolutions feed only the `auto` line
		}
		if len(o.skills) == 0 {
			bInj++
			switch o.kind {
			case "accept":
				bAcc++
			case "reject":
				bRej++
			case "weak_reject":
				bWeak++
			}
			continue
		}
		for _, ref := range o.skills {
			a := bySkill[ref.path]
			if a == nil {
				a = &acc{rejectConvs: map[int64]bool{}}
				bySkill[ref.path] = a
			}
			a.inj++
			if o.resolveSeq >= a.lastSeq {
				a.lastSeq = o.resolveSeq
				a.lastHash = ref.blockHash
			}
			switch o.kind {
			case "accept":
				a.acc++
			case "reject":
				a.rej++
				a.rejectConvs[o.convID] = true
			case "weak_reject":
				a.weak++
			}
		}
	}
	base = skillAuditBaseline{Outcomes: bInj}
	if bInj > 0 {
		base.AcceptRate = float64(bAcc) / float64(bInj)
		base.RejectRate = float64(2*bRej+bWeak) / float64(2*bInj)
	}
	for path, a := range bySkill {
		row := skillAuditRow{
			Path: path, BlockHash: a.lastHash,
			Injections: a.inj, Accepts: a.acc, Rejects: a.rej, WeakRejects: a.weak,
			RejectConversations: len(a.rejectConvs),
			AcceptRate:          float64(a.acc) / float64(a.inj),
			RejectRate:          float64(2*a.rej+a.weak) / float64(2*a.inj),
		}
		// Flag legs. The rate leg compares cross-multiplied integers
		// (weak rejects weight 0.5 via doubling): reject-rate >= factor x
		// baseline-reject-rate. Empty baseline => baseline reject-rate is
		// 0 and the leg is satisfied by the rejects >= 3 leg already.
		flagged := a.inj >= skillFlagMinInjections &&
			a.rej >= skillFlagMinRejects &&
			len(a.rejectConvs) >= skillFlagMinRejectConversations
		if flagged && bInj > 0 {
			flagged = (2*a.rej+a.weak)*bInj >= skillFlagMinRejectRateFactorX(2*bRej+bWeak)*a.inj
		}
		row.Flagged = flagged
		rows = append(rows, row)
	}
	// Deterministic display order: flagged first, then rejects, then
	// injections, then path.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Flagged != rows[j].Flagged {
			return rows[i].Flagged
		}
		if rows[i].Rejects != rows[j].Rejects {
			return rows[i].Rejects > rows[j].Rejects
		}
		if rows[i].Injections != rows[j].Injections {
			return rows[i].Injections > rows[j].Injections
		}
		return rows[i].Path < rows[j].Path
	})
	return rows, base
}

// skillFlagMinRejectRateFactorX multiplies by the flag factor — spelled
// out so the integer rate comparison reads at the call site.
func skillFlagMinRejectRateFactorX(n int) int { return skillFlagRejectRateFactor * n }

// collectSkillsAudit scans every active workstream's active conversation
// of the bound project (conversations are never deleted — epochs are
// counters on the single conversation row, so this covers all history).
func collectSkillsAudit(ctx context.Context, jp journalProj) (skillsAuditReport, error) {
	report := skillsAuditReport{
		ProjectRoot: jp.project.RootPath,
		Journal:     filepath.Join(jp.project.RootPath, ".odo", "journal.sqlite"),
		FlagThresholds: map[string]int{
			"min_injections":           skillFlagMinInjections,
			"min_rejects":              skillFlagMinRejects,
			"min_reject_conversations": skillFlagMinRejectConversations,
			"reject_rate_factor":       skillFlagRejectRateFactor,
		},
	}
	streams, err := jp.store.ListWorkstreams(ctx, jp.project.ID)
	if err != nil {
		return report, err
	}
	report.WorkstreamsScanned = len(streams)

	var outcomes []skillOutcome
	for _, w := range streams {
		c, cerr := jp.store.GetActiveConversation(ctx, w.ID)
		if cerr != nil {
			continue // workstreams without a conversation contribute nothing
		}
		report.ConversationsScanned++
		events, lerr := jp.store.ListEvents(ctx, c.ID, 0)
		if lerr != nil {
			continue // a half-readable conversation must not sink the whole audit
		}
		diffs, derr := jp.store.ListDiffs(ctx, c.ID)
		if derr != nil {
			continue
		}
		outcomes = append(outcomes, convOutcomes(scanConversation(events), diffs, c.ID)...)
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
	rows, base := aggregateSkills(outcomes)
	report.Skills = rows
	report.Baseline = base
	report.SkillFreeOutcomes = base.Outcomes
	return report, nil
}

// renderSkillsAuditHuman prints the compact report (stdout).
func renderSkillsAuditHuman(r skillsAuditReport) {
	fmt.Printf("odo skills audit — %s\n", r.ProjectRoot)
	fmt.Printf("journal: %s · %d workstream(s) · %d conversation(s) scanned\n",
		r.Journal, r.WorkstreamsScanned, r.ConversationsScanned)
	total := r.Accepts + r.Rejects + r.WeakRejects
	if total == 0 {
		fmt.Println("no data: 0 resolved outcomes found — nothing to audit yet")
		return
	}
	fmt.Printf("resolved outcomes: %d (accepts %d · rejects %d · weak rejects %d · skill-free %d)\n",
		total, r.Accepts, r.Rejects, r.WeakRejects, r.SkillFreeOutcomes)
	if auto := r.AutoAccepts + r.AutoRejects; auto > 0 {
		fmt.Printf("auto outcomes: %d (auto-land actor %q: accepts %d · rejects %d — excluded from labels and baseline)\n",
			auto, ipc.AutoActor, r.AutoAccepts, r.AutoRejects)
	}
	fmt.Printf("baseline (skill-free outcomes): accept-rate %.1f%% · reject-rate %.1f%% (n=%d)\n",
		r.Baseline.AcceptRate*100, r.Baseline.RejectRate*100, r.Baseline.Outcomes)
	if len(r.Skills) == 0 {
		fmt.Println("no skill injections in any resolved outcome — nothing to score")
		return
	}
	fmt.Printf("\nskills (flag: injections>=%d rejects>=%d conversations>=%d reject-rate>=%dx baseline):\n",
		skillFlagMinInjections, skillFlagMinRejects, skillFlagMinRejectConversations, skillFlagRejectRateFactor)
	fmt.Printf("  %-44s %-17s %4s %4s %4s %4s %7s %7s %5s\n",
		"SKILL", "COHORT", "INJ", "ACC", "REJ", "WEAK", "ACC%", "REJ%", "FLAG")
	for _, s := range r.Skills {
		flag := ""
		if s.Flagged {
			flag = "FLAG"
		}
		fmt.Printf("  %-44s %-17s %4d %4d %4d %4d %6.1f%% %6.1f%% %5s\n",
			truncateRunes(s.Path, 44), truncateRunes(s.BlockHash, 17), s.Injections,
			s.Accepts, s.Rejects, s.WeakRejects, s.AcceptRate*100, s.RejectRate*100, flag)
	}
}

// runSkillsCLI dispatches `odo skills <sub>`. Only `audit` exists (M15 S-1).
func runSkillsCLI(args []string) int {
	jsonOut := false
	var positional []string
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 1 || positional[0] != "audit" {
		fmt.Fprintln(os.Stderr, skillsAuditUsage)
		return 2
	}

	ctx := context.Background()
	jp, closeStore, err := journalStore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo skills: %v\n", err)
		return 1
	}
	defer closeStore()

	report, err := collectSkillsAudit(ctx, jp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo skills audit: %v\n", err)
		return 1
	}
	if jsonOut {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "odo skills audit: marshal: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}
	renderSkillsAuditHuman(report)
	return 0
}
