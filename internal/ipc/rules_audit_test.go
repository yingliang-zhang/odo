package ipc

// Self-improving Wave 1: the rules-audit engine battery — journal-level
// attribution (windows, cohort resolution, exclusion seams) first, then
// the pure flag-leg matrix (aggregateRules is I/O-free precisely so the
// boundaries pin without fixtures).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// ------------------------------------------------------------- journal fixtures

// raFixture is a seeded project (autonomyFixture precedent): a writable
// store whose journal lives under dir/.odo, where memory.md is written too.
type raFixture struct {
	st  *store.Store
	p   store.Project
	dir string
}

func newRAFixture(t *testing.T) *raFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateOrGetProject(ctx, dir, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &raFixture{st: st, p: p, dir: dir}
}

// raStep is one journaled row of a scripted conversation (auditStep
// precedent in cmd_skills_audit_test.go; timestamps made explicit because
// the diff -> terminal mapping orders by created_at).
type raStep struct {
	sec     int
	kind    string // "msg" | "snap" | "done" | "err" | "paneldone" | "accept" | "reject" | "autoaccept" | "moarej" | "diff"
	text    string // msg text / snap content
	receipt map[string]string
	sha     string // snap kind
}

// memoryRuleLine renders one learner-format rule line (learner.go:696).
func memoryRuleLine(text, cites string, epoch int) string {
	return fmt.Sprintf("- %s — cites: %s; reaffirmed: %d", text, cites, epoch)
}

// writeMemory replaces the fixture's .odo/memory.md ("" removes it).
func (f *raFixture) writeMemory(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(f.dir, ".odo", "memory.md")
	if content == "" {
		return // absent file: readProjectMemory yields ""
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedConv writes one workstream + conversation and replays steps.
// "diff" inserts a diff row; accept/reject/autoaccept/moarej reference the
// most recent diff. Returns the conversation ID.
func (f *raFixture) seedConv(t *testing.T, wsName string, steps []raStep) int64 {
	t.Helper()
	ctx := context.Background()
	w, err := f.st.CreateOrGetWorkstream(ctx, f.p.ID, wsName)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.st.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var lastDiff store.Diff
	for _, step := range steps {
		at := time.Date(2026, 8, 1, 12, 0, step.sec, 0, time.UTC).Format("2006-01-02 15:04:05")
		if step.kind == "diff" {
			d, err := f.st.InsertDiff(ctx, c.ID, filepath.Join(t.TempDir(), "run.diff"), "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.st.DB().Exec(`UPDATE diffs SET created_at = ? WHERE id = ?`, at, d.ID); err != nil {
				t.Fatal(err)
			}
			lastDiff = d
			continue
		}
		var payload string
		var typ string
		switch step.kind {
		case "msg":
			p := map[string]interface{}{"text": step.text}
			if step.receipt != nil {
				p["receipt"] = step.receipt
			}
			b, _ := json.Marshal(p)
			payload, typ = string(b), store.EventUserMessage
		case "snap":
			payload = fmt.Sprintf(`{"layer":"memory","cause":"snapshot","source":".odo/memory.md","content":%q,"sha":%q}`,
				step.text, step.sha)
			typ = store.EventMemoryUpdate
		case "done":
			payload, typ = `{}`, store.EventAgentDone
		case "err":
			payload, typ = `{"error":"boom"}`, store.EventAgentError
		case "paneldone":
			payload, typ = `{"panel":true}`, store.EventAgentDone
		case "accept", "reject":
			payload = fmt.Sprintf(`{"action":%q,"diff_id":%d}`, step.kind, lastDiff.ID)
			typ = store.EventReviewAction
		case "autoaccept":
			payload = fmt.Sprintf(`{"action":"accept","diff_id":%d,"actor":%q}`, lastDiff.ID, AutoActor)
			typ = store.EventReviewAction
		case "moarej":
			payload = fmt.Sprintf(`{"action":"moa_review","diff_id":%d,"consensus_verdict":"reject","reviews":[]}`, lastDiff.ID)
			typ = store.EventReviewAction
		default:
			t.Fatalf("unknown step kind %q", step.kind)
		}
		ev, err := f.st.AppendEvent(ctx, c.ID, typ, payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.DB().Exec(`UPDATE events SET created_at = ? WHERE id = ?`, at, ev.ID); err != nil {
			t.Fatal(err)
		}
	}
	return c.ID
}

func (f *raFixture) compute(t *testing.T) RulesAuditReport {
	t.Helper()
	r, err := ComputeRulesAudit(context.Background(), f.st, f.p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func ruleRow(t *testing.T, r RulesAuditReport, text string) *RulesAuditRow {
	t.Helper()
	for i := range r.Rules {
		if r.Rules[i].Rule == text {
			return &r.Rules[i]
		}
	}
	return nil
}

// ------------------------------------------------------------- attribution

// TestRulesAuditAttributionWindow: a rule landed mid-window is scored on
// post-land outcomes only; the rule present in EVERY snapshot is
// pre-window (no counterfactual) and earns no row.
func TestRulesAuditAttributionWindow(t *testing.T) {
	t.Parallel()
	f := newRAFixture(t)
	oldContent := memoryRuleLine("old rule", "n1", 1) + "\n"
	newContent := oldContent + memoryRuleLine("always test first", "n2", 2) + "\n"
	f.writeMemory(t, newContent)
	f.seedConv(t, "main", []raStep{
		{sec: 1, kind: "snap", text: oldContent, sha: "sha-old"},
		{sec: 2, kind: "msg", text: "task 1", receipt: map[string]string{rulesAuditMemoryReceipt: "sha-old"}},
		{sec: 3, kind: "done"},
		{sec: 4, kind: "diff"},
		{sec: 5, kind: "accept"},
		{sec: 6, kind: "snap", text: newContent, sha: "sha-new"},
		{sec: 7, kind: "msg", text: "task 2", receipt: map[string]string{rulesAuditMemoryReceipt: "sha-new"}},
		{sec: 8, kind: "done"},
		{sec: 9, kind: "diff"},
		{sec: 10, kind: "reject"},
	})
	r := f.compute(t)
	if r.Resolutions != 2 || r.Accepts != 1 || r.Rejects != 1 {
		t.Fatalf("totals = %+v, want 2 resolutions (1 accept, 1 reject)", r)
	}
	if r.SnapshotCohorts != 2 || r.NoSnapshots {
		t.Errorf("cohorts = %d noSnapshots=%v, want 2/false", r.SnapshotCohorts, r.NoSnapshots)
	}
	if r.CurrentRules != 2 || r.WindowRules != 1 || r.PreWindowRules != 1 {
		t.Errorf("rule counts = %d/%d/%d, want 2 current, 1 in-window, 1 pre-window",
			r.CurrentRules, r.WindowRules, r.PreWindowRules)
	}
	row := ruleRow(t, r, "always test first")
	if row == nil {
		t.Fatalf("missing row for always test first: %+v", r.Rules)
	}
	if row.Injections != 1 || row.Rejects != 1 || row.Accepts != 0 ||
		row.Conversations != 1 || row.RejectConversations != 1 ||
		row.RejectRate != 1 || row.AcceptRate != 0 || row.Flag != "" {
		t.Errorf("row = %+v, want 1 injection rejected, no flag", row)
	}
	if old := ruleRow(t, r, "old rule"); old != nil {
		t.Errorf("pre-window rule earned a row: %+v", old)
	}
	if r.Baseline.Outcomes != 2 || r.Baseline.AcceptRate != 0.5 || r.Baseline.RejectRate != 0.5 {
		t.Errorf("baseline = %+v, want n=2 50/50", r.Baseline)
	}
}

// TestRulesAuditBoundarySemantics ports the skills-audit boundary battery:
// no-diff terminals close windows, errored runs keep receipts from
// bleeding into the next outcome, and reviews of an errored run's partial
// diff label nothing.
func TestRulesAuditBoundarySemantics(t *testing.T) {
	t.Parallel()
	f := newRAFixture(t)
	content := memoryRuleLine("rule x", "n", 1) + "\n"
	f.writeMemory(t, content)
	x := map[string]string{rulesAuditMemoryReceipt: "sha-x"}
	f.seedConv(t, "main", []raStep{
		// The empty counterfactual cohort: without a journaled snapshot
		// LACKING the rule it is pre-window and earns no rows (the window
		// gate is pinned in TestRulesAuditEligibilityGates).
		{sec: 1, kind: "snap", text: "", sha: "sha-empty"},
		{sec: 2, kind: "snap", text: content, sha: "sha-x"},
		{sec: 3, kind: "msg", text: "x task", receipt: x},
		{sec: 4, kind: "done"},
		{sec: 5, kind: "diff"},
		{sec: 6, kind: "reject"},
		{sec: 7, kind: "msg", text: "plain no diff"},
		{sec: 7, kind: "done"}, // no-diff terminal: boundary
		{sec: 8, kind: "msg", text: "plain after"},
		{sec: 9, kind: "done"},
		{sec: 10, kind: "diff"},
		{sec: 11, kind: "accept"},
		{sec: 12, kind: "msg", text: "x task errored", receipt: x},
		{sec: 13, kind: "err"},
		{sec: 14, kind: "diff"},
		{sec: 15, kind: "accept"}, // review of an errored run: no label
		{sec: 16, kind: "msg", text: "plain 2"},
		{sec: 17, kind: "done"},
		{sec: 18, kind: "diff"},
		{sec: 19, kind: "reject"},
	})
	r := f.compute(t)
	if r.Resolutions != 3 || r.Rejects != 2 || r.Accepts != 1 {
		t.Fatalf("totals = %+v, want 3 resolutions (2 rejects, 1 accept)", r)
	}
	row := ruleRow(t, r, "rule x")
	if row == nil || row.Injections != 1 || row.Rejects != 1 {
		t.Fatalf("row = %+v, want rule x visible in exactly the first outcome", row)
	}
	if r.MemoryFreeOutcomes != 2 {
		t.Errorf("memory-free = %d, want 2 (errored run's receipt must not bleed)", r.MemoryFreeOutcomes)
	}
}

// TestRulesAuditWeakRejectAndOverride: an un-overridden moa reject is a
// weak outcome (0.5 weight in the reject-rate); a human action on the same
// diff replaces it.
func TestRulesAuditWeakRejectAndOverride(t *testing.T) {
	t.Parallel()
	f := newRAFixture(t)
	content := memoryRuleLine("rule x", "n", 1) + "\n"
	f.writeMemory(t, content)
	x := map[string]string{rulesAuditMemoryReceipt: "sha-x"}
	f.seedConv(t, "main", []raStep{
		{sec: 1, kind: "snap", text: "", sha: "sha-empty"},
		{sec: 1, kind: "snap", text: content, sha: "sha-x"},
		{sec: 2, kind: "msg", text: "x task 1", receipt: x},
		{sec: 3, kind: "done"},
		{sec: 4, kind: "diff"},
		{sec: 5, kind: "moarej"},
		{sec: 6, kind: "msg", text: "x task 2", receipt: x},
		{sec: 7, kind: "done"},
		{sec: 8, kind: "diff"},
		{sec: 9, kind: "moarej"},
		{sec: 10, kind: "accept"}, // human override: the moa row is neither outcome nor boundary
	})
	r := f.compute(t)
	row := ruleRow(t, r, "rule x")
	if row == nil || row.Injections != 2 || row.WeakRejects != 1 || row.Accepts != 1 || row.Rejects != 0 {
		t.Fatalf("row = %+v, want 1 weak reject + 1 accept", row)
	}
	if row.RejectRate != 0.25 {
		t.Errorf("reject-rate = %v, want 0.25 (weak weights 0.5)", row.RejectRate)
	}
	if r.Rejects != 0 || r.WeakRejects != 1 || r.Accepts != 1 {
		t.Errorf("totals rejs=%d weak=%d acc=%d, want 0/1/1", r.Rejects, r.WeakRejects, r.Accepts)
	}
}

// TestRulesAuditSlashAndPanelExclusion: slash user_messages (a /panel
// context block receipting memory.md) are not injections, and panel
// one-shot terminals never enter the run/diff pipeline — without the
// terminal exclusion the paneldone would close the window and hide the
// earlier real send's cohort.
func TestRulesAuditSlashAndPanelExclusion(t *testing.T) {
	t.Parallel()
	f := newRAFixture(t)
	content := memoryRuleLine("rule x", "n", 1) + "\n"
	f.writeMemory(t, content)
	x := map[string]string{rulesAuditMemoryReceipt: "sha-x"}
	f.seedConv(t, "main", []raStep{
		{sec: 1, kind: "snap", text: "", sha: "sha-empty"},
		{sec: 1, kind: "snap", text: content, sha: "sha-x"},
		{sec: 2, kind: "msg", text: "x task", receipt: x},
		{sec: 3, kind: "msg", text: "/panel review this", receipt: x},
		{sec: 4, kind: "paneldone"},
		{sec: 5, kind: "msg", text: "plain follow-up"},
		{sec: 6, kind: "done"},
		{sec: 7, kind: "diff"},
		{sec: 8, kind: "reject"},
	})
	r := f.compute(t)
	row := ruleRow(t, r, "rule x")
	if row == nil || row.Injections != 1 || row.Rejects != 1 {
		t.Fatalf("row = %+v, want rule x attributed (panel terminal must not close the window)", row)
	}
	if r.MemoryFreeOutcomes != 0 || r.Resolutions != 1 {
		t.Errorf("memory-free=%d resolutions=%d, want 0/1", r.MemoryFreeOutcomes, r.Resolutions)
	}
}

// TestRulesAuditAutoActorExcluded: auto_panel resolutions feed neither
// rule rows nor the baseline — the loop never grades itself (M17 F5).
func TestRulesAuditAutoActorExcluded(t *testing.T) {
	t.Parallel()
	f := newRAFixture(t)
	content := memoryRuleLine("rule x", "n", 1) + "\n"
	f.writeMemory(t, content)
	x := map[string]string{rulesAuditMemoryReceipt: "sha-x"}
	f.seedConv(t, "main", []raStep{
		{sec: 1, kind: "snap", text: "", sha: "sha-empty"},
		{sec: 1, kind: "snap", text: content, sha: "sha-x"},
		{sec: 2, kind: "msg", text: "x task 1", receipt: x},
		{sec: 3, kind: "done"},
		{sec: 4, kind: "diff"},
		{sec: 5, kind: "autoaccept"},
		{sec: 6, kind: "msg", text: "x task 2", receipt: x},
		{sec: 7, kind: "done"},
		{sec: 8, kind: "diff"},
		{sec: 9, kind: "accept"},
		{sec: 10, kind: "msg", text: "plain"},
		{sec: 11, kind: "done"},
		{sec: 12, kind: "diff"},
		{sec: 13, kind: "autoaccept"},
	})
	r := f.compute(t)
	row := ruleRow(t, r, "rule x")
	if row == nil || row.Injections != 1 || row.Accepts != 1 {
		t.Fatalf("row = %+v, want the human accept only", row)
	}
	if r.AutoAccepts != 2 || r.Accepts != 1 || r.Resolutions != 1 {
		t.Errorf("totals auto=%d acc=%d res=%d, want 2/1/1", r.AutoAccepts, r.Accepts, r.Resolutions)
	}
	if r.Baseline.Outcomes != 1 || r.Baseline.AcceptRate != 1 {
		t.Errorf("baseline = %+v, want n=1 accept-rate 1 (auto excluded)", r.Baseline)
	}
}

// TestRulesAuditNoSnapshots: receipts without any journaled snapshot are
// unknown cohorts — counted in totals, attributed to no rule, and the
// report says per-rule attribution is unavailable.
func TestRulesAuditNoSnapshots(t *testing.T) {
	t.Parallel()
	f := newRAFixture(t)
	f.writeMemory(t, memoryRuleLine("rule x", "n", 1)+"\n")
	f.seedConv(t, "main", []raStep{
		{sec: 1, kind: "msg", text: "old task", receipt: map[string]string{rulesAuditMemoryReceipt: "sha-legacy"}},
		{sec: 2, kind: "done"},
		{sec: 3, kind: "diff"},
		{sec: 4, kind: "reject"},
	})
	r := f.compute(t)
	if !r.NoSnapshots || r.SnapshotCohorts != 0 {
		t.Fatalf("noSnapshots=%v cohorts=%d, want true/0", r.NoSnapshots, r.SnapshotCohorts)
	}
	if r.UnknownCohortOutcomes != 1 || len(r.Rules) != 0 {
		t.Errorf("unknown=%d rules=%v, want 1 unknown cohort, no rows", r.UnknownCohortOutcomes, r.Rules)
	}
	if r.Resolutions != 1 || r.Rejects != 1 {
		t.Errorf("totals = %+v, want the reject in global counts", r)
	}
	if r.WindowRules != 0 || r.PreWindowRules != 0 {
		t.Errorf("window/pre = %d/%d, want 0/0 (no window provable)", r.WindowRules, r.PreWindowRules)
	}
}

// TestRulesAuditNoData: an unresolved journal computes to a zero report
// (never a crash).
func TestRulesAuditNoData(t *testing.T) {
	t.Parallel()
	f := newRAFixture(t)
	f.seedConv(t, "main", []raStep{{sec: 1, kind: "msg", text: "hello"}})
	r := f.compute(t)
	if r.Resolutions != 0 || len(r.Rules) != 0 || r.Flagged != 0 {
		t.Errorf("report = %+v, want zero resolutions, no rows, no flags", r)
	}
	if len(r.NovelFlags()) != 0 {
		t.Errorf("NovelFlags = %v, want none", r.NovelFlags())
	}
}

// ------------------------------------------------------------- flag legs (pure)

// mkRuleOutcomes builds n same-kind outcomes (aggregateSkills' mkOutcomes
// precedent).
func mkRuleOutcomes(kind string, n, startSeq, convID int, memHash string) []rulesOutcome {
	out := make([]rulesOutcome, 0, n)
	for i := range n {
		out = append(out, rulesOutcome{convID: int64(convID), resolveSeq: startSeq + i, kind: kind, memHash: memHash})
	}
	return out
}

// TestRulesAuditFlagThresholds pins the harmful-flag leg matrix on
// aggregateRules. NOTE the self-pool: the baseline is ALL non-auto
// resolutions (task spec), so a row's own outcomes dilute its margin —
// the fixtures below budget for it.
func TestRulesAuditFlagThresholds(t *testing.T) {
	t.Parallel()
	const X = "sha-x"
	// The empty cohort is the counterfactual that makes "rule x" in-window
	// (some journaled snapshot lacks it) — without it the rule is
	// pre-window and earns no rows at all.
	cohorts := map[string]map[string]bool{X: {"rule x": true}, "sha-empty": {}}
	current := []memoryRule{{text: "rule x", cites: "n"}}

	tests := []struct {
		name     string
		outcomes []rulesOutcome
		wantFlag bool
	}{
		{
			// 10 injections, 3 rejects in 3 conversations, reject-rate
			// 0.3 >= 2x0.15 global (3/20) — the float trap: 2*0.15 in
			// float64 is 0.30000000000000004 > 0.3, the integer
			// cross-multiply (6*20 >= 2*6*10 → 120 >= 120) must flag.
			name: "exactly at all thresholds flags (integer math)",
			outcomes: append(
				append(mkRuleOutcomes("reject", 1, 1, 1, X), mkRuleOutcomes("reject", 1, 2, 2, X)...),
				append(mkRuleOutcomes("reject", 1, 3, 3, X), mkRuleOutcomes("accept", 7, 4, 4, X)...)...,
			),
			wantFlag: true,
		},
		{
			name: "9 injections does not flag",
			outcomes: append(
				append(mkRuleOutcomes("reject", 1, 1, 1, X), mkRuleOutcomes("reject", 1, 2, 2, X)...),
				append(mkRuleOutcomes("reject", 1, 3, 3, X), mkRuleOutcomes("accept", 6, 4, 4, X)...)...,
			),
			wantFlag: false,
		},
		{
			name: "2 rejects does not flag",
			outcomes: append(
				append(mkRuleOutcomes("reject", 1, 1, 1, X), mkRuleOutcomes("reject", 1, 2, 2, X)...),
				mkRuleOutcomes("accept", 8, 3, 3, X)...,
			),
			wantFlag: false,
		},
		{
			name: "rejects in only 2 conversations does not flag",
			outcomes: append(
				mkRuleOutcomes("reject", 3, 1, 1, X),
				mkRuleOutcomes("accept", 7, 4, 2, X)...,
			),
			wantFlag: false,
		},
		{
			name: "just below 2x baseline does not flag",
			outcomes: append(
				append(mkRuleOutcomes("reject", 1, 1, 1, X), mkRuleOutcomes("reject", 1, 2, 2, X)...),
				append(mkRuleOutcomes("reject", 1, 3, 3, X), mkRuleOutcomes("accept", 7, 4, 4, X)...)...,
			),
			wantFlag: false,
		},
		{
			// Weak rejects add 0.5 to the rate but NEVER count as human
			// rejects: 2 rejects + 5 weak meets no count leg.
			name: "weak rejects do not satisfy the rejects leg",
			outcomes: append(
				append(mkRuleOutcomes("reject", 1, 1, 1, X), mkRuleOutcomes("reject", 1, 2, 2, X)...),
				append(mkRuleOutcomes("weak_reject", 5, 3, 3, X), mkRuleOutcomes("accept", 3, 8, 4, X)...)...,
			),
			wantFlag: false,
		},
	}
	// Baselines per case (memory-free outcomes): case 5 uses 1 reject in
	// 9 to push the global rate above the 2x bar; the rest are clean
	// accepts that hold the global rate at or below it.
	baselines := [][]rulesOutcome{
		mkRuleOutcomes("accept", 10, 900, 99, ""),
		mkRuleOutcomes("accept", 10, 900, 99, ""),
		mkRuleOutcomes("accept", 10, 900, 99, ""),
		mkRuleOutcomes("accept", 10, 900, 99, ""),
		append(mkRuleOutcomes("reject", 1, 900, 99, ""), mkRuleOutcomes("accept", 8, 901, 99, "")...),
		mkRuleOutcomes("accept", 10, 900, 99, ""),
	}
	for i, tc := range tests {
		all := append(append([]rulesOutcome{}, tc.outcomes...), baselines[i]...)
		rows, base, _, _, _, _ := aggregateRules(all, cohorts, current)
		if len(rows) != 1 {
			t.Fatalf("%s: rows = %+v, want the single rule", tc.name, rows)
		}
		got := rows[0].Flag == "harmful"
		if got != tc.wantFlag {
			t.Errorf("%s: harmful = %v, want %v (row %+v, baseline %+v)",
				tc.name, got, tc.wantFlag, rows[0], base)
		}
	}
}

// TestRulesAuditEffectiveFlag pins the effective flag: accept-rate >= 2x
// baseline accept-rate, and the >=1-accept guard (a zero-accept baseline
// makes the bare rate leg vacuous — without the guard every accept-less
// rule would flag "effective" against an accept-less baseline).
// Remember the self-pooled baseline (the row's outcomes are in it) when
// deriving these numbers. A harmful+effective overlap is infeasible to
// construct here: the outcome kinds partition the pool, so a baseline
// that halves BOTH rates needs rate mass with nowhere to go — there is
// no precedence case to pin.
func TestRulesAuditEffectiveFlag(t *testing.T) {
	t.Parallel()
	const X = "sha-x"
	cohorts := map[string]map[string]bool{X: {"rule x": true}, "sha-empty": {}}
	current := []memoryRule{{text: "rule x", cites: "n"}}

	tests := []struct {
		name     string
		outcomes []rulesOutcome
		base     []rulesOutcome
		wantFlag string
	}{
		{
			// Row 5/5 accepts (1.0); base is all rejects so the global
			// accept-rate is 5/11 (0.4545): 5*11 >= 2*5*5 (55 >= 50).
			name:     "2x accept baseline flags effective",
			outcomes: mkRuleOutcomes("accept", 5, 1, 1, X),
			base:     mkRuleOutcomes("reject", 6, 900, 99, ""),
			wantFlag: "effective",
		},
		{
			// Row 3/3 accepts (1.0); global accept-rate 6/9 (0.667):
			// 3*9 < 2*6*3 (27 < 36) — 1.5x, not 2x.
			name:     "below 2x does not flag",
			outcomes: mkRuleOutcomes("accept", 3, 1, 1, X),
			base:     append(mkRuleOutcomes("accept", 3, 900, 99, ""), mkRuleOutcomes("reject", 3, 903, 99, "")...),
			wantFlag: "",
		},
		{
			// Zero accepts anywhere: 0 >= 0 vacuous without the guard.
			name:     "zero accepts never flags effective",
			outcomes: mkRuleOutcomes("weak_reject", 10, 1, 1, X),
			base:     mkRuleOutcomes("weak_reject", 10, 900, 99, ""),
			wantFlag: "",
		},
	}
	// NOTE: the rule rows carry fewer than 10 injections on purpose —
	// effective has no injection leg (task spec), unlike harmful.
	for _, tc := range tests {
		outcomes := append(append([]rulesOutcome{}, tc.outcomes...), tc.base...)
		rows, _, _, _, _, _ := aggregateRules(outcomes, cohorts, current)
		if len(rows) != 1 || rows[0].Flag != tc.wantFlag {
			t.Errorf("%s: rows = %+v, want exactly the one rule flagged %q", tc.name, rows, tc.wantFlag)
		}
	}
}

// TestRulesAuditEligibilityGates pins the non-scoring gates: pre-window
// rules earn no rows, retracted rules earn no rows, memory-free and
// unknown-cohort outcomes feed only totals.
func TestRulesAuditEligibilityGates(t *testing.T) {
	t.Parallel()
	current := []memoryRule{{text: "rule x", cites: "n"}}

	// Pre-window: rule x in EVERY cohort.
	cohorts := map[string]map[string]bool{"sha-a": {"rule x": true}, "sha-b": {"rule x": true}}
	out := append(mkRuleOutcomes("reject", 3, 1, 1, "sha-a"), mkRuleOutcomes("reject", 2, 4, 2, "sha-b")...)
	rows, _, memFree, unknown, window, preWindow := aggregateRules(out, cohorts, current)
	if len(rows) != 0 || window != 0 || preWindow != 1 {
		t.Errorf("pre-window: rows=%v window=%d pre=%d, want none/0/1", rows, window, preWindow)
	}

	// Retracted rule: cohort carries it, current memory.md does not.
	// (cohorts2 carries the empty counterfactual so rule x is in-window.)
	cohorts2 := map[string]map[string]bool{"sha-a": {"rule x": true}, "sha-empty": {}}
	rows, _, _, _, _, _ = aggregateRules(mkRuleOutcomes("reject", 2, 1, 1, "sha-a"),
		cohorts2, []memoryRule{{text: "other rule", cites: "m"}})
	if len(rows) != 0 {
		t.Errorf("retracted: rows = %v, want none", rows)
	}

	// Memory-free + unknown cohorts.
	out = append(mkRuleOutcomes("accept", 2, 1, 1, ""), mkRuleOutcomes("reject", 1, 3, 1, "sha-ghost")...)
	_, base, memFree, unknown, _, _ := aggregateRules(out, cohorts2, current)
	if memFree != 2 || unknown != 1 {
		t.Errorf("memFree=%d unknown=%d, want 2/1", memFree, unknown)
	}
	if base.Outcomes != 3 || math.Abs(base.RejectRate-1.0/3.0) > 1e-9 {
		t.Errorf("baseline = %+v, want n=3 reject-rate 1/3", base)
	}
	// Row-shape pin: RejectRate weights weak rejects 0.5.
	rows, _, _, _, _, _ = aggregateRules(
		append(mkRuleOutcomes("reject", 1, 1, 1, "sha-a"), mkRuleOutcomes("weak_reject", 1, 2, 1, "sha-a")...),
		cohorts2, []memoryRule{{text: "rule x", cites: "n"}, {text: "rule y", cites: "m"}})
	for _, row := range rows {
		if row.Rule == "rule x" && row.RejectRate != 0.75 {
			t.Errorf("RejectRate = %v, want (2*1+1)/(2*2) = 0.75", row.RejectRate)
		}
	}
}

// TestRulesAuditPriorFlagsDedupe drives the full re-run loop at journal
// level: a flagged rule journals once; an unchanged re-measure adds
// nothing; a moved measurement re-flags.
func TestRulesAuditPriorFlagsDedupe(t *testing.T) {
	t.Parallel()
	f := newRAFixture(t)
	ruleText := "never skip regression tests"
	cohortContent := memoryRuleLine(ruleText, "n2", 2) + "\n"
	f.writeMemory(t, cohortContent)

	// Scenario: 10 injections of the rule (3 rejects across convs
	// a/b/c), plus 14 memory-free accepts diluting the global baseline to
	// 3/24 = 0.125 — the row's 0.3 reject-rate clears the 2x leg.
	runs := []struct {
		ws          string
		rejectRuns  map[int]bool // 1-based run indexes that reject
		ruleRuns    int
		freeAccepts int
	}{
		{"main", map[int]bool{1: true}, 4, 6},
		{"ws-b", map[int]bool{1: true}, 3, 4},
		{"ws-c", map[int]bool{1: true}, 3, 4},
	}
	x := map[string]string{rulesAuditMemoryReceipt: "sha-r"}
	for _, cfg := range runs {
		var steps []raStep
		steps = append(steps, raStep{sec: 0, kind: "snap", text: "", sha: "sha-empty"})
		steps = append(steps, raStep{sec: 0, kind: "snap", text: cohortContent, sha: "sha-r"})
		sec := 1
		for run := 1; run <= cfg.ruleRuns; run++ {
			steps = append(steps,
				raStep{sec: sec, kind: "msg", text: fmt.Sprintf("task %d", run), receipt: x},
				raStep{sec: sec + 1, kind: "done"},
				raStep{sec: sec + 2, kind: "diff"})
			sec += 3
			if cfg.rejectRuns[run] {
				steps = append(steps, raStep{sec: sec, kind: "reject"})
			} else {
				steps = append(steps, raStep{sec: sec, kind: "accept"})
			}
			sec++
		}
		for free := 0; free < cfg.freeAccepts; free++ {
			steps = append(steps,
				raStep{sec: sec, kind: "msg", text: "free"},
				raStep{sec: sec + 1, kind: "done"},
				raStep{sec: sec + 2, kind: "diff"},
				raStep{sec: sec + 3, kind: "accept"})
			sec += 4
		}
		f.seedConv(t, cfg.ws, steps)
	}

	r := f.compute(t)
	if r.Flagged != 1 {
		t.Fatalf("flagged = %d, want 1 (rows %+v, baseline %+v)", r.Flagged, r.Rules, r.Baseline)
	}
	novel := r.NovelFlags()
	if len(novel) != 1 || novel[0].Flag != "harmful" || novel[0].Rule != ruleText {
		t.Fatalf("novel = %+v, want the one harmful rule", novel)
	}
	if novel[0].Injections != 10 || novel[0].Rejects != 3 || novel[0].RejectConversations != 3 {
		t.Fatalf("novel row = %+v, want 10/3/3", novel[0])
	}

	// Journal the flag the way the CLI sink does, then re-measure:
	// identical evidence adds nothing.
	payload, _ := json.Marshal(RulesAuditFlagPayload(novel[0], r.Baseline))
	w, err := f.st.GetWorkstreamByName(context.Background(), f.p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.st.GetActiveConversation(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AppendEvent(context.Background(), c.ID, store.EventReviewAction, string(payload)); err != nil {
		t.Fatal(err)
	}
	r2 := f.compute(t)
	if r2.Flagged != 1 || len(r2.NovelFlags()) != 0 || len(r2.PriorFlags) != 1 {
		t.Errorf("re-run: flagged=%d novel=%v prior=%v, want 1/none/1 (idempotent)",
			r2.Flagged, r2.NovelFlags(), r2.PriorFlags)
	}

	// The flag row itself must not perturb attribution: same totals.
	if r2.Resolutions != r.Resolutions || r2.Accepts != r.Accepts || r2.Rejects != r.Rejects {
		t.Errorf("flag row perturbed the audit: %+v vs %+v", r2, r)
	}

	// A moved measurement (one more injected accept) re-flags: the
	// evidence tuple changed. seedConv can't extend an existing
	// conversation (one conversation per workstream), so append the extra
	// run onto main directly.
	more := []raStep{
		{sec: 200, kind: "msg", text: "task extra", receipt: x},
		{sec: 201, kind: "done"},
		{sec: 202, kind: "diff"},
		{sec: 203, kind: "accept"},
	}
	var lastDiff store.Diff
	for _, step := range more {
		at := time.Date(2026, 8, 1, 12, 0, step.sec, 0, time.UTC).Format("2006-01-02 15:04:05")
		var ev store.Event
		var aerr error
		switch step.kind {
		case "msg":
			p := map[string]interface{}{"text": step.text, "receipt": step.receipt}
			b, _ := json.Marshal(p)
			ev, aerr = f.st.AppendEvent(context.Background(), c.ID, store.EventUserMessage, string(b))
		case "done":
			ev, aerr = f.st.AppendEvent(context.Background(), c.ID, store.EventAgentDone, `{}`)
		case "diff":
			d, derr := f.st.InsertDiff(context.Background(), c.ID, filepath.Join(t.TempDir(), "run.diff"), "", "", "")
			if derr != nil {
				t.Fatal(derr)
			}
			if _, uerr := f.st.DB().Exec(`UPDATE diffs SET created_at = ? WHERE id = ?`, at, d.ID); uerr != nil {
				t.Fatal(uerr)
			}
			lastDiff = d
			continue
		case "accept":
			pl := fmt.Sprintf(`{"action":"accept","diff_id":%d}`, lastDiff.ID)
			ev, aerr = f.st.AppendEvent(context.Background(), c.ID, store.EventReviewAction, pl)
		}
		if aerr != nil {
			t.Fatal(aerr)
		}
		if _, uerr := f.st.DB().Exec(`UPDATE events SET created_at = ? WHERE id = ?`, at, ev.ID); uerr != nil {
			t.Fatal(uerr)
		}
	}
	r3 := f.compute(t)
	novel3 := r3.NovelFlags()
	if len(novel3) != 1 || novel3[0].Injections != 11 {
		t.Errorf("moved measurement: novel = %+v, want the rule re-flagged at 11 injections", novel3)
	}
}

// TestRulesAuditFlagPayloadShape pins the journal contract: the fields a
// Wave-3 learner read-back (DATA) and the ledger citation rely on.
func TestRulesAuditFlagPayloadShape(t *testing.T) {
	t.Parallel()
	row := RulesAuditRow{Rule: "r", Cites: "n", Flag: "harmful", Injections: 10, Rejects: 3, RejectConversations: 3}
	base := RulesAuditBaseline{Outcomes: 24, RejectRate: 0.125}
	b, _ := json.Marshal(RulesAuditFlagPayload(row, base))
	s := string(b)
	for _, want := range []string{
		`"action":"memory_audit_flag"`, `"actor":"rules_audit"`, `"verdict":"harmful"`,
		`"rule":"r"`, `"injections":10`, `"rejects":3`, `"reject_conversations":3`,
		`"baseline_outcomes":24`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("payload missing %s: %s", want, s)
		}
	}
}
