package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// The skills-audit tests pin journal fixtures with EXPLICIT timestamps:
// the diff -> terminal mapping orders by created_at (second-precision),
// so fixtures stamp every row to make the production mapping rule
// deterministic. Steps run against the writable store; the store is
// CLOSED before the CLI runs, exercising the real read-only path.

// auditStep is one journaled row of a scripted conversation.
type auditStep struct {
	sec     int               // second offset inside the fixture minute
	kind    string            // "msg" | "done" | "err" | "paneldone" | "accept" | "reject" | "moarej" | "diff"
	text    string            // msg text
	receipt map[string]string // msg receipt (path -> block hash)
	patch   string            // diff kind only: patch content written to disk
}

func (s auditStep) at() time.Time {
	return time.Date(2026, 8, 1, 12, 0, s.sec, 0, time.UTC)
}

// seedAuditConv writes one workstream + conversation and replays steps.
// "diff" inserts a diff row; accept/reject/moarej reference the most
// recent diff. Returns the conversation ID.
func seedAuditConv(t *testing.T, st *store.Store, projectID int64, wsName string, steps []auditStep) int64 {
	t.Helper()
	ctx := context.Background()
	w, err := st.CreateOrGetWorkstream(ctx, projectID, wsName)
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var lastDiff store.Diff
	for _, step := range steps {
		at := step.at().Format("2006-01-02 15:04:05")
		if step.kind == "diff" {
			diffPath := filepath.Join(t.TempDir(), "run.diff")
			if step.patch != "" {
				if err := os.WriteFile(diffPath, []byte(step.patch), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			d, err := st.InsertDiff(ctx, c.ID, diffPath, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB().Exec(`UPDATE diffs SET created_at = ? WHERE id = ?`, at, d.ID); err != nil {
				t.Fatal(err)
			}
			lastDiff = d
			lastDiff.CreatedAt = at
			continue
		}
		var payload string
		switch step.kind {
		case "msg":
			p := map[string]interface{}{"text": step.text}
			if step.receipt != nil {
				p["receipt"] = step.receipt
			}
			b, _ := json.Marshal(p)
			payload = string(b)
		case "done":
			payload = `{}`
		case "err":
			payload = `{"error":"boom"}`
		case "paneldone":
			payload = `{"panel":true}`
		case "accept", "reject":
			payload = fmt.Sprintf(`{"action":%q,"diff_id":%d}`, step.kind, lastDiff.ID)
		case "autoaccept":
			payload = fmt.Sprintf(`{"action":"accept","diff_id":%d,"actor":%q}`, lastDiff.ID, ipc.AutoActor)
		case "moarej":
			payload = fmt.Sprintf(`{"action":"moa_review","diff_id":%d,"consensus_verdict":"reject","reviews":[]}`, lastDiff.ID)
		default:
			t.Fatalf("unknown step kind %q", step.kind)
		}
		typ := map[string]string{
			"msg": store.EventUserMessage, "done": store.EventAgentDone,
			"err": store.EventAgentError, "paneldone": store.EventAgentDone,
			"accept": store.EventReviewAction, "reject": store.EventReviewAction,
			"moarej": store.EventReviewAction, "autoaccept": store.EventReviewAction,
		}[step.kind]
		ev, err := st.AppendEvent(ctx, c.ID, typ, payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().Exec(`UPDATE events SET created_at = ? WHERE id = ?`, at, ev.ID); err != nil {
			t.Fatal(err)
		}
	}
	return c.ID
}

// runAuditJSON seeds the journal, closes it, chdirs into the project, and
// runs the CLI with --json, returning the parsed report.
func runAuditJSON(t *testing.T, convs map[string][]auditStep) skillsAuditReport {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic workstream order regardless of map iteration.
	names := make([]string, 0, len(convs))
	for name := range convs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		seedAuditConv(t, st, p.ID, name, convs[name])
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	stdout, stderr, code := captureCLI(t, func() int {
		return runSkillsCLI([]string{"audit", "--json"})
	})
	if code != 0 {
		t.Fatalf("skills audit: exit %d, stderr %q", code, stderr)
	}
	var report skillsAuditReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("audit --json: %v\n%s", err, stdout)
	}
	return report
}

// skillRowByPath finds a per-skill row by receipt path suffix.
func skillRowByPath(r skillsAuditReport, suffix string) *skillAuditRow {
	for i := range r.Skills {
		if strings.HasSuffix(r.Skills[i].Path, suffix) {
			return &r.Skills[i]
		}
	}
	return nil
}

// TestSkillsAuditAttributionWindow: a skill receipted on run 1's send is
// attributed to run 1's outcome only — run 2's outcome (same conversation,
// no skill) is baseline, not skill signal. The newest window also supplies
// the block-hash cohort.
func TestSkillsAuditAttributionWindow(t *testing.T) {
	x := map[string]string{".odo/skills/x.md": "hash-x-1", ".odo/memory.md": "memhash"}
	x2 := map[string]string{".odo/skills/x.md": "hash-x-2"}
	plain := map[string]string{".odo/memory.md": "memhash"}
	report := runAuditJSON(t, map[string][]auditStep{
		"main": {
			{sec: 1, kind: "msg", text: "do the x thing", receipt: x},
			{sec: 2, kind: "done"},
			{sec: 3, kind: "diff"},
			{sec: 4, kind: "accept"},
			// Same skill re-injected with NEW content: the cohort moves.
			{sec: 5, kind: "msg", text: "more x work", receipt: x2},
			{sec: 6, kind: "done"},
			{sec: 7, kind: "diff"},
			{sec: 8, kind: "reject"},
			// Skill-free run: baseline only.
			{sec: 9, kind: "msg", text: "plain task", receipt: plain},
			{sec: 10, kind: "done"},
			{sec: 11, kind: "diff"},
			{sec: 12, kind: "accept"},
		},
	})

	row := skillRowByPath(report, "skills/x.md")
	if row == nil {
		t.Fatalf("x.md row missing: %+v", report.Skills)
	}
	if row.Injections != 2 || row.Accepts != 1 || row.Rejects != 1 || row.WeakRejects != 0 {
		t.Errorf("x.md = %+v, want 2 injections / 1 accept / 1 reject", row)
	}
	if row.BlockHash != "hash-x-2" {
		t.Errorf("block hash cohort = %q, want the newest window's hash-x-2", row.BlockHash)
	}
	if skillRowByPath(report, "memory.md") != nil {
		t.Error("memory.md (not a skill path) leaked into skill rows")
	}
	if report.Baseline.Outcomes != 1 || report.Baseline.RejectRate != 0 || report.Baseline.AcceptRate != 1 {
		t.Errorf("baseline = %+v, want 1 skill-free accept", report.Baseline)
	}
	if report.Accepts != 2 || report.Rejects != 1 {
		t.Errorf("totals = %d accepts / %d rejects, want 2/1", report.Accepts, report.Rejects)
	}
}

// TestSkillsAuditNoDiffAndErrorBoundaries: a run that produced no diff
// closes the attribution window (its skills cannot bleed into the next
// outcome), and an errored run does the same while never generating an
// outcome label itself.
func TestSkillsAuditNoDiffAndErrorBoundaries(t *testing.T) {
	x := map[string]string{".odo/skills/x.md": "hash-x"}
	y := map[string]string{".odo/skills/y.md": "hash-y"}
	report := runAuditJSON(t, map[string][]auditStep{
		"main": {
			// Run 1: skill X in play, no diff produced — window closes.
			{sec: 1, kind: "msg", text: "try x", receipt: x},
			{sec: 2, kind: "done"},
			// Run 2: skill Y in play, run ERRORS with no diff — no outcome
			// label for Y, but the window still closes.
			{sec: 3, kind: "msg", text: "try y", receipt: y},
			{sec: 4, kind: "err"},
			// Run 3: no skills; its accept must be pure baseline.
			{sec: 5, kind: "msg", text: "plain"},
			{sec: 6, kind: "done"},
			{sec: 7, kind: "diff"},
			{sec: 8, kind: "accept"},
		},
	})

	if len(report.Skills) != 0 {
		t.Errorf("skill rows = %+v, want none (both skilled runs closed without outcomes)", report.Skills)
	}
	if report.Baseline.Outcomes != 1 || report.Baseline.AcceptRate != 1 {
		t.Errorf("baseline = %+v, want 1 skill-free accept", report.Baseline)
	}
	if report.Accepts != 1 || report.Rejects != 0 || report.WeakRejects != 0 {
		t.Errorf("totals = %d/%d/%d, want 1 accept only", report.Accepts, report.Rejects, report.WeakRejects)
	}
}

// TestSkillsAuditErroredWithDiffExcluded: drainRun journals a diff even
// for a failed run (partial changes are reviewable), but a review of an
// errored run's partial diff is neither an outcome nor a boundary — the
// errored terminal alone closes the window. Skill X in play on the
// errored run therefore earns zero labels from the accept, and the NEXT
// run's accept is pure baseline (X does not bleed past the boundary).
func TestSkillsAuditErroredWithDiffExcluded(t *testing.T) {
	x := map[string]string{".odo/skills/x.md": "hash-x"}
	report := runAuditJSON(t, map[string][]auditStep{
		"main": {
			// Run 1: skill X in play, run ERRORS but produced a diff;
			// the human accepts the partial changes.
			{sec: 1, kind: "msg", text: "try x", receipt: x},
			{sec: 2, kind: "err"},
			{sec: 3, kind: "diff"},
			{sec: 4, kind: "accept"},
			// Run 2: no skills; its accept must be pure baseline — X
			// stopped at the errored terminal's boundary.
			{sec: 5, kind: "msg", text: "plain"},
			{sec: 6, kind: "done"},
			{sec: 7, kind: "diff"},
			{sec: 8, kind: "accept"},
		},
	})

	if len(report.Skills) != 0 {
		t.Errorf("skill rows = %+v, want none (errored-run reviews carry no outcome)", report.Skills)
	}
	if report.Baseline.Outcomes != 1 || report.Baseline.AcceptRate != 1 {
		t.Errorf("baseline = %+v, want 1 skill-free accept", report.Baseline)
	}
	if report.Accepts != 1 || report.Rejects != 0 || report.WeakRejects != 0 {
		t.Errorf("totals = %d/%d/%d, want 1 accept only", report.Accepts, report.Rejects, report.WeakRejects)
	}
}

// TestSkillsAuditMoaWeakRejectAndOverride: a moa consensus reject with no
// human follow-up counts as a weak reject; a subsequent human action on
// the same diff overrides it (the human label alone counts).
func TestSkillsAuditMoaWeakRejectAndOverride(t *testing.T) {
	x := map[string]string{".odo/skills/x.md": "hash-x"}
	y := map[string]string{".odo/skills/y.md": "hash-y"}
	report := runAuditJSON(t, map[string][]auditStep{
		"main": {
			// Diff 1: moa reject, never human-reviewed -> weak reject.
			{sec: 1, kind: "msg", text: "x task", receipt: x},
			{sec: 2, kind: "done"},
			{sec: 3, kind: "diff"},
			{sec: 4, kind: "moarej"},
			// Diff 2: moa reject THEN human accept -> accept only.
			{sec: 5, kind: "msg", text: "y task", receipt: y},
			{sec: 6, kind: "done"},
			{sec: 7, kind: "diff"},
			{sec: 8, kind: "moarej"},
			{sec: 9, kind: "accept"},
		},
	})

	rx := skillRowByPath(report, "skills/x.md")
	ry := skillRowByPath(report, "skills/y.md")
	if rx == nil || ry == nil {
		t.Fatalf("rows missing: %+v", report.Skills)
	}
	if rx.Injections != 1 || rx.WeakRejects != 1 || rx.Accepts != 0 {
		t.Errorf("x.md = %+v, want 1 weak reject", rx)
	}
	if ry.Injections != 1 || ry.Accepts != 1 || ry.WeakRejects != 0 {
		t.Errorf("y.md = %+v, want 1 accept and no weak reject (human override)", ry)
	}
	if report.Accepts != 1 || report.WeakRejects != 1 || report.Rejects != 0 {
		t.Errorf("totals = %d/%d/%d, want 1 accept + 1 weak reject", report.Accepts, report.Rejects, report.WeakRejects)
	}
}

// TestSkillsAuditSlashAndPanelExclusion: slash messages journal no skill
// attribution (their receipts — real journals never carry skill paths —
// are ignored even when malformed), and panel one-shot agent_done markers
// are neither terminals nor boundaries.
func TestSkillsAuditSlashAndPanelExclusion(t *testing.T) {
	x := map[string]string{".odo/skills/x.md": "hash-x"}
	report := runAuditJSON(t, map[string][]auditStep{
		"main": {
			// Malformed/legacy journal: a slash send WITH a skill receipt.
			{sec: 1, kind: "msg", text: "/panel is x any good", receipt: x},
			{sec: 2, kind: "paneldone"},
			// A real send with no skills; the accept must stay baseline.
			{sec: 3, kind: "msg", text: "plain task"},
			{sec: 4, kind: "done"},
			{sec: 5, kind: "diff"},
			{sec: 6, kind: "accept"},
		},
	})

	if len(report.Skills) != 0 {
		t.Errorf("skill rows = %+v, want none (slash sends never attribute)", report.Skills)
	}
	if report.Baseline.Outcomes != 1 {
		t.Errorf("baseline = %+v, want exactly 1 outcome", report.Baseline)
	}
}

// TestSkillsAuditNoData exits cleanly with the no-data line on an empty
// journal.
func TestSkillsAuditNoData(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOrGetProject(ctx, root, "p"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	stdout, stderr, code := captureCLI(t, func() int {
		return runSkillsCLI([]string{"audit"})
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "no data") {
		t.Errorf("stdout %q, want the no-data line", stdout)
	}
}

// TestSkillsAuditUsage rejects unknown subcommands.
func TestSkillsAuditUsage(t *testing.T) {
	_, stderr, code := captureCLI(t, func() int {
		return runSkillsCLI([]string{"bogus"})
	})
	if code != 2 || !strings.Contains(stderr, "usage: odo skills audit") {
		t.Errorf("exit %d stderr %q, want usage on exit 2", code, stderr)
	}
}

// ---------------------------------------------------------------- threshold
// The flag rule is pure — threshold edges are exercised directly over
// aggregateSkills (all four legs: injections, rejects, distinct
// conversations, reject-rate vs 2x baseline). Rate comparisons are exact
// integer cross-multiplies, so the exactly-2x case must flag.

// mkOutcomes builds n outcomes of one kind sharing skills (nil = baseline).
func mkOutcomes(kind string, n int, startSeq int, convID int64, skills ...string) []skillOutcome {
	var out []skillOutcome
	for i := range n {
		var refs []skillRef
		for _, s := range skills {
			refs = append(refs, skillRef{path: s, blockHash: "h"})
		}
		out = append(out, skillOutcome{convID: convID, resolveSeq: startSeq + i, kind: kind, skills: refs})
	}
	return out
}

func TestSkillsAuditFlagThresholds(t *testing.T) {
	const X = ".odo/skills/x.md"
	baseOf := func(rejects, total int) []skillOutcome {
		return append(mkOutcomes("reject", rejects, 900, 99), mkOutcomes("accept", total-rejects, 950, 99)...)
	}

	tests := []struct {
		name     string
		outcomes []skillOutcome
		wantFlag bool
	}{
		{
			// Exactly at every leg: 10 injections, 3 rejects across 3
			// conversations, reject-rate 0.3 >= 2x0.1 (baseline 2/20).
			name: "exactly at all thresholds flags",
			outcomes: append(
				append(mkOutcomes("reject", 1, 1, 1, X), mkOutcomes("reject", 1, 2, 2, X)...),
				append(mkOutcomes("reject", 1, 3, 3, X), mkOutcomes("accept", 7, 4, 4, X)...)...,
			),
			wantFlag: true,
		},
		{
			name: "9 injections does not flag",
			outcomes: append(
				append(mkOutcomes("reject", 1, 1, 1, X), mkOutcomes("reject", 1, 2, 2, X)...),
				append(mkOutcomes("reject", 1, 3, 3, X), mkOutcomes("accept", 6, 4, 4, X)...)...,
			),
			wantFlag: false,
		},
		{
			name: "2 rejects does not flag",
			outcomes: append(
				append(mkOutcomes("reject", 1, 1, 1, X), mkOutcomes("reject", 1, 2, 2, X)...),
				mkOutcomes("accept", 8, 3, 3, X)...,
			),
			wantFlag: false,
		},
		{
			name: "rejects in only 2 conversations does not flag",
			outcomes: append(
				mkOutcomes("reject", 3, 1, 1, X),
				append(mkOutcomes("reject", 0, 4, 2, X), mkOutcomes("accept", 7, 4, 2, X)...)...,
			),
			wantFlag: false,
		},
		{
			// Float trap: reject-rate 0.3 vs baseline 0.15 (2x exactly).
			// 0.15*2 in float64 is 0.30000000000000004 > 0.3 — the integer
			// cross-multiply (2*3*20 >= 2*(2*3)*10 → 120 >= 120) must flag.
			name: "exactly 2x baseline flags (integer math)",
			outcomes: append(
				append(mkOutcomes("reject", 1, 1, 1, X), mkOutcomes("reject", 1, 2, 2, X)...),
				append(mkOutcomes("reject", 1, 3, 3, X), mkOutcomes("accept", 7, 4, 4, X)...)...,
			),
			wantFlag: true,
		},
		{
			name: "just below 2x baseline does not flag",
			outcomes: append(
				append(mkOutcomes("reject", 1, 1, 1, X), mkOutcomes("reject", 1, 2, 2, X)...),
				append(mkOutcomes("reject", 1, 3, 3, X), mkOutcomes("accept", 7, 4, 4, X)...)...,
			),
			wantFlag: false,
		},
	}
	// Baselines per case: cases 1-4 use 1/10 (need >= 0.2); case 5 uses
	// 3/20 = 0.15 (need >= 0.3); case 6 uses 2/10 = 0.2 (need >= 0.4).
	baselines := [][]skillOutcome{
		baseOf(1, 10), baseOf(1, 10), baseOf(1, 10), baseOf(1, 10), baseOf(3, 20), baseOf(2, 10),
	}
	for i, tc := range tests {
		all := append(append([]skillOutcome{}, tc.outcomes...), baselines[i]...)
		rows, base := aggregateSkills(all)
		if len(rows) != 1 {
			t.Fatalf("%s: rows = %+v, want the single skill", tc.name, rows)
		}
		if rows[0].Flagged != tc.wantFlag {
			t.Errorf("%s: flagged = %v, want %v (row %+v, baseline %+v)",
				tc.name, rows[0].Flagged, tc.wantFlag, rows[0], base)
		}
	}
}

// TestSkillsAuditAutoActorExcluded (M17 F5): auto-land resolutions
// (actor:auto_panel) are excluded from per-skill rows AND from the
// skill-free baseline — mirroring ComputeAutonomy's streak exclusion —
// while still reported as the separate AutoAccepts line. A flagged-skill
// denominator must never be inflated by the system's own approvals
// (live proof: journal seq 6668 accept{diff_id:17,actor:auto_panel}).
func TestSkillsAuditAutoActorExcluded(t *testing.T) {
	x := map[string]string{".odo/skills/x.md": "hash-x"}
	report := runAuditJSON(t, map[string][]auditStep{
		"main": {
			// Skill X in play, resolved by the AUTO pipeline: invisible
			// to both the skill row and the baseline.
			{sec: 1, kind: "msg", text: "x task", receipt: x},
			{sec: 2, kind: "done"},
			{sec: 3, kind: "diff"},
			{sec: 4, kind: "autoaccept"},
			// Skill X in play, HUMAN accept: the only counted outcome.
			{sec: 5, kind: "msg", text: "x task 2", receipt: x},
			{sec: 6, kind: "done"},
			{sec: 7, kind: "diff"},
			{sec: 8, kind: "accept"},
			// Skill-free, resolved by the AUTO pipeline: excluded from
			// the skill-free baseline.
			{sec: 9, kind: "msg", text: "plain"},
			{sec: 10, kind: "done"},
			{sec: 11, kind: "diff"},
			{sec: 12, kind: "autoaccept"},
		},
	})
	row := skillRowByPath(report, "skills/x.md")
	if row == nil || row.Injections != 1 || row.Accepts != 1 || row.WeakRejects != 0 || row.Rejects != 0 {
		t.Errorf("row(skills/x.md) = %+v, want 1 human accept only (auto excluded)", row)
	}
	if report.AutoAccepts != 2 || report.Accepts != 1 {
		t.Errorf("report totals accepts=%d auto_accepted=%d, want 1/2",
			report.Accepts, report.AutoAccepts)
	}
	if report.Baseline.Outcomes != 0 || report.Baseline.AcceptRate != 0 {
		t.Errorf("baseline = %+v, want empty (auto accept must not count as skill-free outcome)", report.Baseline)
	}
}

// TestSkillsAuditEmptyBaselineRateLeg: with no skill-free outcomes the
// rate leg is satisfied by the other three legs alone.
func TestSkillsAuditEmptyBaselineRateLeg(t *testing.T) {
	const X = ".odo/skills/x.md"
	out := append(mkOutcomes("reject", 3, 1, 1, X), mkOutcomes("accept", 7, 4, 2, X)...)
	// Rejects all in one conversation would fail the conversation leg —
	// spread them.
	out[1].convID, out[2].convID = 2, 3
	rows, _ := aggregateSkills(out)
	if len(rows) != 1 || !rows[0].Flagged {
		t.Errorf("rows = %+v, want the skill flagged with an empty baseline", rows)
	}
}
