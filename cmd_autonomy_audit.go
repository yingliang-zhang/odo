package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/yingliang-zhang/odo/internal/ipc"
)

// M15 B-strategy-1 (O-1 rung-0): `odo autonomy audit` prints the autonomy
// streak snapshot: per diff-class human-accept streaks, rung thresholds,
// and the auto_apply pref. It is pure observability — rung 0 instruments
// the loop so a later milestone has the evidence; nothing auto-applies.
//
// The computation is ipc.ComputeAutonomy — the SAME journal reads the
// autonomy_status IPC serves the GUI — run here against a READ-ONLY
// (query_only) journal open, no daemon, no LLM.
//
//	odo autonomy audit [--json]

const autonomyAuditUsage = `usage: odo autonomy audit [--json]
  audit         autonomy streak report over resolved diffs across ALL
                conversations of the bound project (read-only, no daemon)
  --json        machine-readable report (one JSON object)`

// renderAutonomyHuman prints the compact report (stdout).
func renderAutonomyHuman(r ipc.AutonomyReport) {
	fmt.Printf("odo autonomy audit — %s\n", r.ProjectRoot)
	fmt.Printf("journal: %s · %d workstream(s) · %d conversation(s) scanned · %d resolutions (+%d auto-landed)\n",
		r.Journal, r.WorkstreamsScanned, r.ConversationsScanned, r.Resolutions, r.AutoAccepted)
	fmt.Printf("auto-apply: %s · current rung: %d (rung-1 at %d clean accepts, rung-2 at %d)\n",
		r.AutoApply, r.CurrentRung, r.RungThresholds["rung_1"], r.RungThresholds["rung_2"])
	if r.Resolutions == 0 {
		fmt.Println("no data: 0 resolved diffs found — nothing to audit yet")
		return
	}
	fmt.Println("\nper-class streaks:")
	for _, c := range r.Classes {
		next := "—"
		if c.NextThreshold > 0 {
			next = fmt.Sprintf("%d/%d", minInt(c.Streak, c.NextThreshold), c.NextThreshold)
		}
		elig := ""
		if c.Eligible != "" {
			elig = " · " + c.Eligible + " eligible"
		}
		fmt.Printf("  %-13s %4d acc %4d rej · streak %-3d next %-5s%s\n", c.Class, c.Accepted, c.Rejected, c.Streak, next, elig)
		fmt.Printf("    %s\n", c.Description)
	}
	fmt.Printf("\nrevert detection: %s\n", r.RevertCheck)
	if r.UnreadableDiffs > 0 {
		fmt.Printf("unreadable patch files: %d (classified as unclassified; no revert evidence)\n", r.UnreadableDiffs)
	}
}

// minInt clamps the streak display to the next threshold.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// runAutonomyCLI dispatches `odo autonomy <sub>`. Only `audit` exists
// (M15 O-1); later rungs land beside it.
func runAutonomyCLI(args []string) int {
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
		fmt.Fprintln(os.Stderr, autonomyAuditUsage)
		return 2
	}

	ctx := context.Background()
	jp, closeStore, err := journalStore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo autonomy: %v\n", err)
		return 1
	}
	defer closeStore()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo autonomy audit: resolve cwd: %v\n", err)
		return 1
	}
	root, err := journalRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo autonomy audit: %v\n", err)
		return 1
	}
	report, err := ipc.ComputeAutonomy(ctx, jp.store, jp.project, ipc.GitTopDirsResolver(root))
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo autonomy audit: %v\n", err)
		return 1
	}
	if jsonOut {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "odo autonomy audit: marshal: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}
	renderAutonomyHuman(report)
	return 0
}
