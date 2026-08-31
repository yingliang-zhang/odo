package main

// D9 learning control plane CLIs.
//
//	W3: `odo learning status` — the read-only front end of the SINGLE
//	    learning fold (internal/ipc/learning_status.go).
//	W6: the human learning actions + stall closeout — `list [--stalled]`
//	    (candidate/stall listing over the same fold), `drop`, `apply`,
//	    `promote --global`. The actions ride the SAME exported cores as the
//	    daemon's learning_action IPC (internal/ipc/learning_actions.go) —
//	    one actuation path each. Journal opens READ-WRITE through
//	    store.Open — WAL + busy_timeout coexists with a live daemon
//	    (unretract precedent); apply's file writes are marker-first
//	    (crash windows own the boot replayer), drop/promote never write
//	    files at all (promote NEVER writes user.md — D4 ruling ④).
//
//	odo learning status [--json]
//	odo learning list [--stalled] [--json]
//	odo learning drop <hash-or-unique-prefix>
//	odo learning apply <hash-or-unique-prefix>
//	odo learning promote --global <hash-or-unique-prefix>

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

const learningUsage = `usage: odo learning <subcommand>
  status [--json]          learning control-plane fold over the bound project's
                           journals: learning_episode rows (per-lane distill
                           outcome projections), memory_audit_flag rows, and
                           the candidate stage list (.odo/learning/
                           candidates.jsonl + learning_stage rows)
  list [--stalled] [--json]
                           the candidate stage list with stall markers;
                           --stalled shows only candidates carrying a
                           learning_stall advisory (aging without next-step
                           minimums — advisory only, never auto-promoted)
  drop <hash|prefix>       human candidate removal: journals learning_drop +
                           learning_stage (dropped_by_human) on main. Candidate
                           layer ONLY — never touches memory.md; landed lines
                           ride ` + "`odo rules retract <text>`" + ` (D4)
  apply <hash|prefix>      human-held apply: held_for_human candidate →
                           project_active, receipted memory_apply marker +
                           memory.md/archive writes (retractions included)
  promote --global <hash|prefix>
                           human global promotion: project_active candidate →
                           global_active with the measured evidence tuple.
                           Prints the rule line(s) — add them to ~/.odo/user.md
                           by hand; odo NEVER writes user.md (D4 ruling ④)
  <hash|prefix>            the 64-hex artifact hash or a unique prefix of it`

// renderLearningHuman prints the compact status table (stdout).
func renderLearningHuman(r ipc.LearningStatusReport) {
	fmt.Printf("odo learning status — %s\n", r.ProjectRoot)
	fmt.Printf("journal: %s\n", r.Journal)

	totals := r.EpisodeTotals
	fmt.Printf("\nepisodes: %d recorded", r.EpisodeCount)
	if r.EpisodeCount > 0 {
		latest := r.Episodes[0]
		fmt.Printf(" (latest: %s epoch %d, seq %d)", latest.Workstream, latest.Epoch, latest.Seq)
	}
	fmt.Println()
	if r.EpisodeCount > 0 {
		fmt.Printf("outcomes (all episodes): accepts %d (auto %d) · rejects %d (auto %d) · weak %d · verify_failed %d · revise rounds %d landed %d · suspended %d · agent_errors %d · tainted %d/%d · reverts %d\n",
			totals["accepted"], totals["auto_accepted"], totals["rejected"], totals["auto_rejected"], totals["weak_rejected"],
			totals["verify_failed"], totals["revise_rounds_spawned"], totals["revise_landed"], totals["ladder_suspended"],
			totals["agent_errors"], totals["false_stops"], totals["no_texts"], totals["human_reverts"])
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
			stalled := ""
			if c.Stalled {
				stalled = " STALLED (learning_stall advisory)"
			}
			fmt.Printf("  %-14s %-16s %s%s%s\n", c.Stage, c.Scope, trimHash(c.ArtifactHash), invalid, stalled)
		}
	}

	// D9-W6: stall advisories (W5 journal rows; advisory only).
	if len(r.Stalls) > 0 {
		fmt.Printf("\nstall advisories: %d (aging without next-step minimums — surfaced, NEVER auto-promoted):\n", len(r.Stalls))
		for _, s := range r.Stalls {
			fmt.Printf("  seq %-6d %-14s %s epoch %d: %s\n", s.Seq, s.Stage, trimHash(s.ArtifactHash), s.Epoch, s.Reason)
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

// openLearningProject resolves the project + journal for the write/scan
// CLIs (the unretract/rules-retract shape): cwd → journalRoot →
// store.Open read-write → project. Caller closes.
func openLearningProject() (root string, st *store.Store, p store.Project, err error) {
	cwd, cerr := os.Getwd()
	if cerr != nil {
		return "", nil, store.Project{}, fmt.Errorf("resolve cwd: %w", cerr)
	}
	root, cerr = journalRoot(cwd)
	if cerr != nil {
		return "", nil, store.Project{}, cerr
	}
	st, cerr = store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if cerr != nil {
		return "", nil, store.Project{}, cerr
	}
	p, cerr = st.GetProjectByRoot(context.Background(), root)
	if cerr != nil {
		st.Close()
		return "", nil, store.Project{}, fmt.Errorf("project %s not in journal: %w", root, cerr)
	}
	return root, st, p, nil
}

// printJSON marshals one payload (the status/list --json shape).
func printJSON(sub string, v interface{}) (int, bool) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning %s: marshal: %v\n", sub, err)
		return 1, false
	}
	fmt.Println(string(out))
	return 0, true
}

// runLearningStatus implements `odo learning status [--json]`.
func runLearningStatus(jsonOut bool) int {
	ctx := context.Background()
	_, st, p, err := openLearningProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning status: %v\n", err)
		return 1
	}
	defer st.Close()
	rep, err := ipc.ComputeLearningStatus(ctx, st, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning status: %v\n", err)
		return 1
	}
	if jsonOut {
		code, _ := printJSON("status", rep)
		return code
	}
	renderLearningHuman(rep)
	return 0
}

// runLearningList implements `odo learning list [--stalled] [--json]`:
// the candidate stage list over the SINGLE fold — advisory-only closeout
// (rows never move a stage).
func runLearningList(stalledOnly, jsonOut bool) int {
	ctx := context.Background()
	_, st, p, err := openLearningProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning list: %v\n", err)
		return 1
	}
	defer st.Close()
	rep, err := ipc.ComputeLearningStatus(ctx, st, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning list: %v\n", err)
		return 1
	}
	if jsonOut {
		if stalledOnly {
			var rows []ipc.LearningCandidateRow
			var stalls []ipc.LearningStallRow
			for _, c := range rep.Candidates {
				if c.Stalled {
					rows = append(rows, c)
				}
			}
			for _, s := range rep.Stalls {
				stalls = append(stalls, s)
			}
			code, _ := printJSON("list", map[string]interface{}{"candidates": rows, "stalls": stalls})
			return code
		}
		code, _ := printJSON("list", rep.Candidates)
		return code
	}

	if len(rep.Candidates) == 0 {
		fmt.Println("no learning candidates — .odo/learning/candidates.jsonl is empty (the lifecycle runs behind prefs learning_stages, default on)")
		return 0
	}
	shown := 0
	for _, c := range rep.Candidates {
		if stalledOnly && !c.Stalled {
			continue
		}
		shown++
		line := fmt.Sprintf("  %-14s %-16s %s seq %d", c.Stage, c.Scope, trimHash(c.ArtifactHash), c.CreatedSeq)
		if c.Invalid {
			line += " INVALID"
		}
		if c.Stalled {
			line += " STALLED"
			for _, s := range rep.Stalls {
				if s.ArtifactHash == c.ArtifactHash && s.Stage == c.Stage {
					line += fmt.Sprintf(" — %s (epoch %d)", s.Reason, s.Epoch)
					break
				}
			}
		}
		fmt.Println(line)
	}
	if stalledOnly {
		if shown == 0 {
			fmt.Println("no stalled candidates — every candidate is within its stage's next-step minimums (or terminal; advisories never auto-promote)")
		} else {
			fmt.Printf("%d stalled candidate(s) of %d\n", shown, len(rep.Candidates))
		}
	}
	return 0
}

// runLearningActionCLI backs `odo learning drop|apply|promote --global`:
// one store open per invocation, one exported core per action — the
// daemon's learning_action handler rides the same cores (single path).
func runLearningActionCLI(sub, id string) int {
	if id == "" {
		fmt.Fprintln(os.Stderr, learningUsage)
		return 2
	}
	ctx := context.Background()
	_, st, p, err := openLearningProject()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning %s: %v\n", sub, err)
		return 1
	}
	defer st.Close()

	var res ipc.LearningActionResult
	switch sub {
	case "drop":
		res, err = ipc.LearningDropCandidate(ctx, st, p, id)
	case "apply":
		res, err = ipc.LearningApplyCandidate(ctx, st, p, id)
	case "promote_global":
		res, err = ipc.LearningPromoteGlobal(ctx, st, p, id)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo learning %s: %v\n", sub, err)
		return 1
	}
	short := trimHash(res.ArtifactHash)

	switch res.Action {
	case "drop":
		fmt.Printf("dropped %s (was %s) — learning_drop seq %d, stage seq %d (epoch %d)\n",
			short, res.FromStage, res.MarkerSeq, res.StageSeq, res.Epoch)
		if res.FromStage == "project_active" {
			fmt.Println("journal semantics: candidate-layer only — its rules may already live in .odo/memory.md; remove them with `odo rules retract <text>` (D4)")
		}
	case "apply":
		if res.Present {
			fmt.Printf("confirmed %s → project_active (rules already present — converged, no write; stage seq %d)\n", short, res.StageSeq)
			break
		}
		fmt.Printf("applied %s → project_active: memory_apply marker seq %d, stage seq %d, memory.md %s→%s",
			short, res.MarkerSeq, res.StageSeq, res.BeforeSHA, res.AfterSHA)
		if len(res.Retracted) > 0 {
			fmt.Printf(", retracted %d (archive records appended)", len(res.Retracted))
		}
		fmt.Println()
		for _, u := range res.UnmatchedRetracts {
			fmt.Printf("note: retract target unmatched (already absent): %s\n", truncateRunes(u, 64))
		}
	case "promote_global":
		fmt.Printf("promoted %s → global_active: learning_promote{scope:%q} marker seq %d, stage seq %d (epoch %d)\n",
			short, "global", res.MarkerSeq, res.StageSeq, res.Epoch)
		fmt.Println("user.md is human-owned forever — odo wrote NOTHING there (D4 ruling ④, memory_autogate tiers never bypassed).")
		if len(res.RuleLines) > 0 {
			fmt.Println("add these rule line(s) to ~/.odo/user.md by hand:")
			for _, line := range res.RuleLines {
				fmt.Printf("  - %s\n", line)
			}
		}
	}
	return 0
}

// runLearningCLI dispatches `odo learning <sub>` — W3 carries status; W6
// adds list/drop/apply/promote --global.
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
	if len(positional) == 0 {
		fmt.Fprintln(os.Stderr, learningUsage)
		return 2
	}
	switch positional[0] {
	case "status":
		if len(positional) != 1 {
			fmt.Fprintln(os.Stderr, learningUsage)
			return 2
		}
		return runLearningStatus(jsonOut)
	case "list":
		stalled := false
		rest := positional[1:]
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "--stalled":
				stalled = true
			default:
				fmt.Fprintf(os.Stderr, "odo learning list: unknown flag %q\n", rest[i])
				return 2
			}
		}
		return runLearningList(stalled, jsonOut)
	case "drop":
		if len(positional) != 2 {
			fmt.Fprintln(os.Stderr, learningUsage)
			return 2
		}
		return runLearningActionCLI("drop", positional[1])
	case "apply":
		if len(positional) != 2 {
			fmt.Fprintln(os.Stderr, learningUsage)
			return 2
		}
		return runLearningActionCLI("apply", positional[1])
	case "promote":
		if len(positional) != 3 || positional[1] != "--global" {
			fmt.Fprintf(os.Stderr, "odo learning promote: only `promote --global <hash|prefix>` is human-anytime; project-level promotion is evidence-owned by the daemon, and a held candidate resolves via `odo learning apply`\n")
			return 2
		}
		return runLearningActionCLI("promote_global", positional[2])
	default:
		fmt.Fprintln(os.Stderr, learningUsage)
		return 2
	}
}
