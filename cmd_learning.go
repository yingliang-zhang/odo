package main

// D9-W3 learning control plane: `odo learning status` — the CLI front end
// of the SINGLE learning fold (internal/ipc/learning_status.go). The GUI
// consumes the same fold through the learning_status IPC and never
// re-folds; here the journal opens read-write-capable (store.Open — WAL +
// busy_timeout coexists with a live daemon, cmd_rules_audit precedent),
// but this command writes NOTHING (read-only scan, unlike rules audit's
// flag sinks).
//
//	odo learning status [--json]

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

const learningUsage = `usage: odo learning status [--json]
  status [--json]  learning control-plane fold over the bound project's
                   journals: learning_episode rows (per-lane distill outcome
                   projections), memory_audit_flag rows (rules-audit harmful/
                   effective flags with their thresholds), and the candidate
                   stage list (.odo/learning/candidates.jsonl — empty until
                   the W4 lifecycle wave)`

// renderLearningHuman prints the compact status table (stdout).
func renderLearningHuman(r ipc.LearningStatusReport) {
	fmt.Printf("odo learning status — %s\n", r.ProjectRoot)
	fmt.Printf("journal: %s\n", r.Journal)

	t := r.EpisodeTotals
	fmt.Printf("\nepisodes: %d recorded", r.EpisodeCount)
	if r.EpisodeCount > 0 {
		latest := r.Episodes[0]
		fmt.Printf(" (latest: %s epoch %d, seq %d)", latest.Workstream, latest.Epoch, latest.Seq)
	}
	fmt.Println()
	if r.EpisodeCount > 0 {
		fmt.Printf("outcomes (all episodes): accepts %d (auto %d) · rejects %d (auto %d) · weak %d · verify_failed %d · revise rounds %d landed %d · suspended %d · agent_errors %d · tainted %d/%d · reverts %d\n",
			t["accepted"], t["auto_accepted"], t["rejected"], t["auto_rejected"], t["weak_rejected"],
			t["verify_failed"], t["revise_rounds_spawned"], t["revise_landed"], t["ladder_suspended"],
			t["agent_errors"], t["false_stops"], t["no_texts"], t["human_reverts"])
	}

	fmt.Printf("\nflags (harmful: injections>=%d rejects>=%d conversations>=%d reject-rate>=%dx baseline · effective: accept-rate>=%dx baseline): %d\n",
		r.FlagThresholds["min_injections"], r.FlagThresholds["min_rejects"],
		r.FlagThresholds["min_reject_conversations"], r.FlagThresholds["rate_factor"],
		r.FlagThresholds["rate_factor"], len(r.Flags))
	for _, f := range r.Flags {
		label := f.Verdict
		fmt.Printf("  seq %-6d %-10s inj %-3d rej %-3d convs %-3d %s\n",
			f.Seq, label, f.Injections, f.Rejects, f.RejectConversations, truncateRunes(f.Rule, 64))
	}

	fmt.Printf("\ncandidates: %d", len(r.Candidates))
	if len(r.Candidates) == 0 {
		fmt.Print(" — the lifecycle wave (W4) has not run; .odo/learning/candidates.jsonl is the W3 writer deliverable\n")
	} else {
		fmt.Println()
		for _, c := range r.Candidates {
			invalid := ""
			if c.Invalid {
				invalid = " INVALID (hash resolves to no candidates.jsonl row)"
			}
			fmt.Printf("  %-14s %-16s %s%s\n", c.Stage, c.Scope, trimHash(c.ArtifactHash), invalid)
		}
	}
}

// trimHash renders an artifact hash's first 12 chars (display only).
func trimHash(h string) string {
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}

// runLearningCLI dispatches `odo learning <sub>` — W3 carries only status.
func runLearningCLI(args []string) int {
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
	if len(positional) != 1 || positional[0] != "status" {
		fmt.Fprintln(os.Stderr, learningUsage)
		return 2
	}

	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning status: resolve cwd: %v\n", err)
		return 1
	}
	root, err := journalRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning status: %v\n", err)
		return 1
	}
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning status: %v\n", err)
		return 1
	}
	defer st.Close()
	p, err := st.GetProjectByRoot(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning status: project %s not in journal: %v\n", root, err)
		return 1
	}

	rep, err := ipc.ComputeLearningStatus(ctx, st, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning status: %v\n", err)
		return 1
	}
	if jsonOut {
		out, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "odo learning status: marshal: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}
	renderLearningHuman(rep)
	return 0
}
