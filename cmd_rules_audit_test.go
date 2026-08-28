package main

// Self-improving Wave 1: the `odo rules audit` CLI end-to-end — the
// seeded journal below reproduces one harmful rule across three
// workstreams; the assertions pin stdout, the review_action sink row on
// main, the ledger.md section, and re-run idempotence. The pure flag-leg
// matrix lives in internal/ipc/rules_audit_test.go; here the store opens,
// chdir, and file sinks are the contract under test.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// rulesAuditConvs scripts the harmful scenario: 10 injections of one
// rule (3 human rejects, one per conversation) plus 14 memory-free
// accepts, so the global reject-rate is 3/24 = 0.125 and the rule's 0.3
// clears the 2x leg. The per-conversation snapshots seed the empty
// counterfactual cohort (window gate) and the rule cohort.
func rulesAuditConvs() (map[string][]auditStep, string, string) {
	ruleText := "never skip regression tests"
	cohort := fmt.Sprintf("- %s — cites: n2; reaffirmed: 2\n", ruleText)
	x := map[string]string{".odo/memory.md": "sha-r"}
	build := func(rejectRun int, ruleRuns, freeAccepts int) []auditStep {
		steps := []auditStep{
			{sec: 0, kind: "snap", text: "", sha: "sha-empty"},
			{sec: 0, kind: "snap", text: cohort, sha: "sha-r"},
		}
		sec := 1
		for run := 1; run <= ruleRuns; run++ {
			steps = append(steps,
				auditStep{sec: sec, kind: "msg", text: fmt.Sprintf("task %d", run), receipt: x},
				auditStep{sec: sec + 1, kind: "done"},
				auditStep{sec: sec + 2, kind: "diff"})
			sec += 3
			if run == rejectRun {
				steps = append(steps, auditStep{sec: sec, kind: "reject"})
			} else {
				steps = append(steps, auditStep{sec: sec, kind: "accept"})
			}
			sec++
		}
		for free := 0; free < freeAccepts; free++ {
			steps = append(steps,
				auditStep{sec: sec, kind: "msg", text: "free"},
				auditStep{sec: sec + 1, kind: "done"},
				auditStep{sec: sec + 2, kind: "diff"},
				auditStep{sec: sec + 3, kind: "accept"})
			sec += 4
		}
		return steps
	}
	convs := map[string][]auditStep{
		"main": build(1, 4, 6),
		"ws-b": build(1, 3, 4),
		"ws-c": build(1, 3, 4),
	}
	return convs, ruleText, cohort
}

// runRulesAudit seeds the journal + memory.md, closes the store (the CLI
// reopens it read-write itself), chdirs in, and runs the CLI.
func runRulesAudit(t *testing.T, convs map[string][]auditStep, memory string, args ...string) (root, stdout, stderr string, code int) {
	t.Helper()
	root = t.TempDir()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		t.Fatal(err)
	}
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
	if memory != "" {
		if err := os.MkdirAll(filepath.Join(root, ".odo"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".odo", "memory.md"), []byte(memory), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	stdout, stderr, code = captureCLI(t, func() int {
		return runRulesCLI(args)
	})
	return root, stdout, stderr, code
}

// countFlagRows reopens the journal and counts memory_audit_flag rows on
// main's conversation.
func countFlagRows(t *testing.T, root string) int {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.GetProjectByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWorkstreamByName(ctx, p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetActiveConversation(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := st.ListEvents(ctx, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var pl struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(ev.Payload, &pl) == nil && pl.Action == "memory_audit_flag" {
			n++
		}
	}
	return n
}

// TestRulesAuditFlagSinksEndToEnd: the flagged rule journals exactly one
// memory_audit_flag row on main and one ledger section; an unchanged
// re-run is a no-op for both sinks (re-measure per epoch must not
// duplicate).
func TestRulesAuditFlagSinksEndToEnd(t *testing.T) {
	convs, ruleText, cohort := rulesAuditConvs()
	root, stdout, stderr, code := runRulesAudit(t, convs, cohort, "audit")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"odo rules audit — ", "resolved outcomes: 24", "harmful",
		"flags: 1 journaled on main seq ", "ledger.md section appended",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, ruleText) {
		t.Errorf("stdout missing the rule text %q:\n%s", ruleText, stdout)
	}
	if got := countFlagRows(t, root); got != 1 {
		t.Errorf("memory_audit_flag rows = %d, want 1", got)
	}
	ledger, err := os.ReadFile(filepath.Join(root, ".odo", "ledger.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## rules audit — ", "flagged harmful", ruleText,
		"reject-rate 30.0% vs baseline 12.5%", "review_action/memory_audit_flag seq ",
	} {
		if !strings.Contains(string(ledger), want) {
			t.Errorf("ledger missing %q:\n%s", want, ledger)
		}
	}

	// Re-run against the SAME journal (the sink pass reopens it):
	// identical evidence is already journaled — sinks untouched.
	t.Chdir(root)
	stdout2, stderr2, code2 := captureCLI(t, func() int {
		return runRulesCLI([]string{"audit"})
	})
	if code2 != 0 {
		t.Fatalf("re-run exit %d, stderr %q", code2, stderr2)
	}
	if !strings.Contains(stdout2, "all identical to already-journaled evidence") {
		t.Errorf("re-run stdout missing idempotent note:\n%s", stdout2)
	}
	if got := countFlagRows(t, root); got != 1 {
		t.Errorf("after re-run: memory_audit_flag rows = %d, want 1 (no duplicate)", got)
	}
	ledger2, err := os.ReadFile(filepath.Join(root, ".odo", "ledger.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(ledger2), "## rules audit — "); got != 1 {
		t.Errorf("after re-run: ledger sections = %d, want 1", got)
	}
}

// TestRulesAuditJSON: --json emits one machine-readable report on stdout;
// the sink note moves to stderr so stdout parses cleanly.
func TestRulesAuditJSON(t *testing.T) {
	convs, ruleText, cohort := rulesAuditConvs()
	_, stdout, stderr, code := runRulesAudit(t, convs, cohort, "audit", "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	var report ipc.RulesAuditReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("--json did not parse: %v\n%s", err, stdout)
	}
	if report.Resolutions != 24 || report.Accepts != 21 || report.Rejects != 3 {
		t.Errorf("totals = %+v, want 24/21/3", report)
	}
	if report.Flagged != 1 || len(report.Rules) != 1 || report.Rules[0].Rule != ruleText ||
		report.Rules[0].Flag != "harmful" || report.Rules[0].RejectConversations != 3 {
		t.Errorf("rows = %+v, want the single harmful rule", report.Rules)
	}
	if report.Baseline.Outcomes != 24 || report.Baseline.RejectRate != 0.125 {
		t.Errorf("baseline = %+v, want n=24 reject-rate 0.125", report.Baseline)
	}
	if report.WindowRules != 1 || report.PreWindowRules != 0 || report.SnapshotCohorts != 2 {
		t.Errorf("cohorts/window/pre = %d/%d/%d, want 2/1/0",
			report.SnapshotCohorts, report.WindowRules, report.PreWindowRules)
	}
	if !strings.Contains(stderr, "flags: 1 journaled") {
		t.Errorf("stderr missing sink note: %q", stderr)
	}
}

// TestRulesAuditNoData: an unresolved journal reports no data, and zero
// flags means zero sink writes (no ledger file appears).
func TestRulesAuditNoData(t *testing.T) {
	convs := map[string][]auditStep{
		"main": {{sec: 1, kind: "msg", text: "hello"}},
	}
	root, stdout, stderr, code := runRulesAudit(t, convs, "", "audit")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "no data: 0 resolved outcomes") {
		t.Errorf("stdout = %q, want the no-data note", stdout)
	}
	if !strings.Contains(stdout, "flags: 0") {
		t.Errorf("stdout = %q, want the untouched-sinks note", stdout)
	}
	if _, err := os.Stat(filepath.Join(root, ".odo", "ledger.md")); !os.IsNotExist(err) {
		t.Error("ledger.md must not appear when nothing was flagged")
	}
}

// TestRulesAuditUsage rejects unknown subcommands; the D4 dispatch keeps
// retract text-only (multi-word match strings arrive as extra args).
func TestRulesAuditUsage(t *testing.T) {
	_, stderr, code := captureCLI(t, func() int {
		return runRulesCLI([]string{"bogus"})
	})
	if code != 2 || !strings.Contains(stderr, "usage: odo rules <audit|retract>") {
		t.Errorf("exit %d, stderr %q, want exit 2 + usage", code, stderr)
	}
	// `retract` without match text: same usage, exit 2.
	_, stderr2, code2 := captureCLI(t, func() int {
		return runRulesCLI([]string{"retract"})
	})
	if code2 != 2 || !strings.Contains(stderr2, "usage: odo rules <audit|retract>") {
		t.Errorf("retract-no-args exit %d, stderr %q, want exit 2 + usage", code2, stderr2)
	}
}
