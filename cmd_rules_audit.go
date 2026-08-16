package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// Self-improving MVP Wave 1 (design docs/design/self-improving-first-
// principles-2026-08-15.md §E Wave 1): `odo rules audit` — the measure
// step. The computation is ipc.ComputeRulesAudit against the journal
// (send → terminal → diff → outcome; auto_panel excluded — only human
// verdicts grade rules), the SAME engine shape as ipc.ComputeAutonomy.
// Block-level attribution: the .odo/memory.md receipt hash resolves to a
// journaled memory_update{layer:"memory", cause:"snapshot"} cohort, and a
// CURRENT memory.md rule is audited only when some journaled snapshot
// lacks it (added within the window — otherwise no counterfactual).
//
// Unlike `odo skills audit` this CLI has SINKS: per flagged rule it
// journals review_action{action:"memory_audit_flag"} on main's
// conversation and appends one ledger.md section citing those seqs
// (daemon-only, pull-based, LLM-free — ADR-0003 inv 4). The report
// itself is flag-only: no retraction, no memory writes, no LLM anywhere
// in the path. Re-runs are idempotent on identical evidence tuples
// (rule, verdict, injections, rejects, reject conversations) — prior
// journaled flags suppress exact duplicates.
//
// The journal opens READ-WRITE through store.Open — WAL + busy_timeout
// coexists with a live daemon (unretract precedent); the scan fails on a
// missing/unreadable journal, but sink failures only warn on stderr —
// the stdout report is the deliverable and a wedged ledger must not
// withhold it (appendLedger fail-open precedent).
//
//	odo rules audit [--json]

const rulesAuditUsage = `usage: odo rules audit [--json]
  audit         memory-rule outcome report over resolved diffs across ALL
                conversations of the bound project (read-only scan;
                flagged rules journal review_action{action:
                "memory_audit_flag"} on main + a ledger.md section)
  --json        machine-readable report (one JSON object)`

// sinkFlags journals one memory_audit_flag per novel flagged rule on
// main's active conversation, then appends the ledger section citing the
// journaled seqs. Returns the human summary line; failures warn on
// stderr and degrade (report still ships).
func sinkFlags(ctx context.Context, st *store.Store, project store.Project, report ipc.RulesAuditReport, root string) string {
	novel := report.NovelFlags()
	dupes := report.Flagged - len(novel)
	switch {
	case len(novel) == 0 && dupes > 0:
		return fmt.Sprintf("flags: %d — all identical to already-journaled evidence; journal + ledger untouched (idempotent re-run)", dupes)
	case len(novel) == 0:
		return "flags: 0 — journal + ledger untouched"
	}

	w, err := st.GetWorkstreamByName(ctx, project.ID, ipc.RulesAuditMainWorkstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo rules audit: flag sink skipped: main workstream: %v\n", err)
		return fmt.Sprintf("flags: %d (%d dupes skipped) — JOURNAL/LEDGER WRITE FAILED (see stderr)", len(novel), dupes)
	}
	c, err := st.GetActiveConversation(ctx, w.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo rules audit: flag sink skipped: main conversation: %v\n", err)
		return fmt.Sprintf("flags: %d (%d dupes skipped) — JOURNAL/LEDGER WRITE FAILED (see stderr)", len(novel), dupes)
	}

	var entries []ipc.RulesAuditLedgerEntry
	failed := 0
	for _, row := range novel {
		payload, _ := json.Marshal(ipc.RulesAuditFlagPayload(row, report.Baseline))
		ev, aerr := st.AppendEvent(ctx, c.ID, store.EventReviewAction, string(payload))
		if aerr != nil {
			failed++
			fmt.Fprintf(os.Stderr, "odo rules audit: journal flag %q: %v\n", row.Rule, aerr)
			continue
		}
		entries = append(entries, ipc.RulesAuditLedgerEntry{Row: row, Seq: ev.Seq})
	}
	note := fmt.Sprintf("flags: %d journaled on main seq %s (%d dupes skipped",
		len(entries), seqsCompact(entries), dupes)
	if failed > 0 {
		note += fmt.Sprintf(", %d journal failures — see stderr", failed)
	}
	if len(entries) > 0 {
		if lerr := ipc.AppendRulesAuditLedger(root, report.Baseline, entries); lerr != nil {
			fmt.Fprintf(os.Stderr, "odo rules audit: ledger section: %v\n", lerr)
			note += " · ledger FAILED (see stderr)"
		} else {
			note += " · ledger.md section appended"
		}
	}
	return note + ")"
}

// seqsCompact renders the journaled seqs as "3,4,7" (flag counts are
// per-epoch small; a range fold would hide gaps from partial failures).
func seqsCompact(entries []ipc.RulesAuditLedgerEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, fmt.Sprintf("%d", e.Seq))
	}
	return strings.Join(parts, ",")
}

// renderRulesAuditHuman prints the compact report (stdout).
func renderRulesAuditHuman(r ipc.RulesAuditReport, sinkNote string) {
	fmt.Printf("odo rules audit — %s\n", r.ProjectRoot)
	fmt.Printf("journal: %s · %d workstream(s) · %d conversation(s) scanned\n",
		r.Journal, r.WorkstreamsScanned, r.ConversationsScanned)
	if r.Resolutions == 0 {
		fmt.Println("no data: 0 resolved outcomes found — nothing to audit yet")
		fmt.Println(sinkNote)
		return
	}
	fmt.Printf("resolved outcomes: %d (accepts %d · rejects %d · weak rejects %d · memory-free %d)\n",
		r.Resolutions, r.Accepts, r.Rejects, r.WeakRejects, r.MemoryFreeOutcomes)
	if auto := r.AutoAccepts + r.AutoRejects; auto > 0 {
		fmt.Printf("auto outcomes: %d (auto-land actor %q: accepts %d · rejects %d — excluded from labels and baseline)\n",
			auto, ipc.AutoActor, r.AutoAccepts, r.AutoRejects)
	}
	if r.UnknownCohortOutcomes > 0 {
		fmt.Printf("unknown cohorts: %d outcome(s) (memory receipt with no matching snapshot — pre-W2 journal; counted in totals, never attributed)\n",
			r.UnknownCohortOutcomes)
	}
	fmt.Printf("baseline (all non-auto resolutions): accept-rate %.1f%% · reject-rate %.1f%% (n=%d)\n",
		r.Baseline.AcceptRate*100, r.Baseline.RejectRate*100, r.Baseline.Outcomes)
	fmt.Printf("memory cohorts: %d snapshot(s) · current rules: %d (in-window %d · pre-window %d)\n",
		r.SnapshotCohorts, r.CurrentRules, r.WindowRules, r.PreWindowRules)
	if r.NoSnapshots {
		fmt.Println("no memory snapshots journaled — per-rule attribution unavailable (pre-W2 journal; cohort content unknowable)")
	}
	if r.PreWindowRules > 0 {
		fmt.Printf("pre-window rules: %d (present in every journaled snapshot — no counterfactual; excluded from flagging)\n",
			r.PreWindowRules)
	}
	shown := 0
	for _, row := range r.Rules {
		if row.Injections > 0 {
			shown++
		}
	}
	if shown == 0 {
		fmt.Println("no in-window rule injections in any resolved outcome — nothing to score")
	} else {
		fmt.Printf("\nrules (harmful: injections>=%d rejects>=%d conversations>=%d reject-rate>=%dx baseline · effective: accept-rate>=%dx baseline):\n",
			r.FlagThresholds["min_injections"], r.FlagThresholds["min_rejects"],
			r.FlagThresholds["min_reject_conversations"], r.FlagThresholds["rate_factor"],
			r.FlagThresholds["rate_factor"])
		fmt.Printf("  %-44s %-16s %4s %4s %4s %4s %4s %4s %7s %7s %s\n",
			"RULE", "CITES", "INJ", "ACC", "REJ", "WEAK", "CONV", "RCON", "ACC%", "REJ%", "FLAG")
		for _, row := range r.Rules {
			if row.Injections == 0 {
				continue
			}
			fmt.Printf("  %-44s %-16s %4d %4d %4d %4d %4d %4d %6.1f%% %6.1f%% %s\n",
				truncateRunes(row.Rule, 44), truncateRunes(row.Cites, 16),
				row.Injections, row.Accepts, row.Rejects, row.WeakRejects,
				row.Conversations, row.RejectConversations,
				row.AcceptRate*100, row.RejectRate*100, row.Flag)
		}
		if zero := len(r.Rules) - shown; zero > 0 {
			fmt.Printf("  (%d in-window rule(s) with no resolved outcomes yet omitted)\n", zero)
		}
	}
	fmt.Println(sinkNote)
}

// runRulesCLI dispatches `odo rules <sub>`. Only `audit` exists (Wave 1).
func runRulesCLI(args []string) int {
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
		fmt.Fprintln(os.Stderr, rulesAuditUsage)
		return 2
	}

	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo rules audit: resolve cwd: %v\n", err)
		return 1
	}
	root, err := journalRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo rules audit: %v\n", err)
		return 1
	}
	// Read-write open (flags journal out of this process): WAL +
	// busy_timeout coexists with a live daemon (unretract precedent).
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo rules audit: %v\n", err)
		return 1
	}
	defer st.Close()
	p, err := st.GetProjectByRoot(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo rules audit: project %s not in journal: %v\n", root, err)
		return 1
	}

	report, err := ipc.ComputeRulesAudit(ctx, st, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo rules audit: %v\n", err)
		return 1
	}

	sinkNote := sinkFlags(ctx, st, p, report, root)

	if jsonOut {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "odo rules audit: marshal: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
		fmt.Fprintln(os.Stderr, sinkNote) // stdout stays pure JSON
		return 0
	}
	renderRulesAuditHuman(report, sinkNote)
	return 0
}
