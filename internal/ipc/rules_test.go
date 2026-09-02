package ipc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// TestMatchRule pins the doublestar glob contract: stdlib-only matching
// over '/'-separated segments, "**" spanning zero or more segments,
// case-folded (the isGateTier0Path APFS precedent), defensive backslash
// normalization (git.PatchPaths' own ReplaceAll precedent).
func TestMatchRule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"internal/moa/*.go", "internal/moa/client.go", true},
		{"internal/moa/*.go", "internal/moa/sub/x.go", false},
		{"internal/moa/*.go", "cmd/moa/x.go", false},
		{"gui/src/**/*.tsx", "gui/src/components/w/Deep.tsx", true},
		{"gui/src/**/*.tsx", "gui/src/App.tsx", true}, // ** spans zero segments
		{"gui/src/**/*.tsx", "gui/src/App.ts", false},
		{"**", "a/b/c.txt", true},
		{"*", "x.go", true},
		{"*", "dir/x.go", false},
		{"internal/moa/*_test.go", "internal/moa/client_test.go", true},
		{"internal/moa/*_test.go", "internal/moa/client.go", false},
		// Case-folding: APFS resolves case variants identically.
		{"INTERNAL/STORE/*.GO", "Internal/Store/Store.go", true},
		// Defensive separator normalization (diff headers from tools).
		{"src/*.go", `src\win.go`, true},
		// Char classes ride path.Match per segment.
		{"[a-c]x.go", "bx.go", true},
		{"[a-c]x.go", "dx.go", false},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
	}
	for _, tc := range tests {
		if got := matchRule(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchRule(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// TestEvalRules pins the overlay semantics: deny aggregates across the
// whole diff and outranks ask, the last APPLICABLE match wins per path
// (gitignore ordering so a narrower rule can neutralize an earlier
// wider one on unprotected paths), the actor field filters, and an
// allow on gate-protected paths silently drops to the ignored list
// (can-only-tighten).
func TestEvalRules(t *testing.T) {
	t.Parallel()

	t.Run("last match wins: narrower allow neutralizes wider deny", func(t *testing.T) {
		rules := []ruleEntry{
			{Pattern: "**", Action: "deny", Actor: "auto_land", Reason: "freeze"},
			{Pattern: "docs/**", Action: "allow", Reason: "docs exempt"},
		}
		if action, reason := evalRules(rules, []string{"docs/guide.md"}); action != "" || reason != "" {
			t.Errorf("docs path = (%q, %q), want zero constraint", action, reason)
		}
		if action, reason := evalRules(rules, []string{"src/a.go"}); action != "deny" || reason != "freeze" {
			t.Errorf("src path = (%q, %q), want (deny, freeze)", action, reason)
		}
	})

	t.Run("later wider deny re-tightens", func(t *testing.T) {
		rules := []ruleEntry{
			{Pattern: "docs/**", Action: "allow"},
			{Pattern: "**", Action: "deny", Reason: "late freeze"},
		}
		if action, reason := evalRules(rules, []string{"docs/guide.md"}); action != "deny" || reason != "late freeze" {
			t.Errorf("= (%q, %q), want (deny, late freeze)", action, reason)
		}
	})

	t.Run("deny outranks ask across paths", func(t *testing.T) {
		rules := []ruleEntry{
			{Pattern: "gui/**", Action: "ask", Reason: "panel gui"},
			{Pattern: "src/**", Action: "deny", Reason: "src frozen"},
		}
		action, reason := evalRules(rules, []string{"gui/App.tsx", "src/a.go"})
		if action != "deny" || reason != "src frozen" {
			t.Errorf("= (%q, %q), want (deny, src frozen)", action, reason)
		}
	})

	t.Run("ask when only ask matches", func(t *testing.T) {
		rules := []ruleEntry{{Pattern: "gui/**", Action: "ask", Actor: "*", Reason: "panel gui"}}
		if action, reason := evalRules(rules, []string{"gui/App.tsx"}); action != "ask" || reason != "panel gui" {
			t.Errorf("= (%q, %q), want (ask, panel gui)", action, reason)
		}
	})

	t.Run("actor filter", func(t *testing.T) {
		// The foreign-actor rule never applies, so the wildcard ask wins.
		rules := []ruleEntry{
			{Pattern: "**", Action: "deny", Actor: "some_future_actor", Reason: "never applies"},
			{Pattern: "**", Action: "ask", Actor: "*", Reason: "wildcard applies"},
		}
		if action, reason := evalRules(rules, []string{"src/a.go"}); action != "ask" || reason != "wildcard applies" {
			t.Errorf("= (%q, %q), want (ask, wildcard applies)", action, reason)
		}
		// Once the actor names the pipeline (or is blank), the deny is
		// applicable — here as the ONLY rule, isolating the filter from
		// last-match-wins ordering.
		for _, actor := range []string{"auto_land", ""} {
			rules[0].Actor = actor
			if action, _ := evalRules(rules[:1], []string{"src/a.go"}); action != "deny" {
				t.Errorf("actor %q: = %q, want deny once the filter passes", actor, action)
			}
		}
	})

	t.Run("no rules and no matches are zero constraint", func(t *testing.T) {
		if action, _ := evalRules(nil, []string{"src/a.go"}); action != "" {
			t.Errorf("nil rules = %q, want \"\"", action)
		}
		rules := []ruleEntry{{Pattern: "zzz/**", Action: "deny", Reason: "never"}}
		if action, _ := evalRules(rules, []string{"src/a.go"}); action != "" {
			t.Errorf("miss = %q, want \"\"", action)
		}
	})
}

// TestEvalRulesCanOnlyTighten pins the hard boundary: an allow NEVER
// wins on gate-protected paths — not on Tier-1 prefixes, not on the
// Tier-0 core, not on ruling-① main.go — and each such attempt lands
// on the ignored list while any wider deny stays in force.
func TestEvalRulesCanOnlyTighten(t *testing.T) {
	t.Parallel()
	rules := []ruleEntry{
		{Pattern: "**", Action: "deny", Actor: "auto_land", Reason: "freeze"},
		{Pattern: "internal/**", Action: "allow", Actor: "auto_land", Reason: "claimed ops need"},
		{Pattern: "main.go", Action: "allow", Actor: "auto_land", Reason: "claimed entrypoint need"},
		{Pattern: ".odo/**", Action: "allow", Actor: "auto_land", Reason: "claimed state need"},
	}
	for _, tc := range []struct {
		path     string
		allowIdx int // the allow rule that tries (and fails) to win the path
	}{
		{"internal/store/store.go", 1},    // Tier-1 prefix
		{"internal/ipc/gatepolicy.go", 1}, // Tier-0 core
		{"internal/adapter/omp.go", 1},    // Tier-1 prefix
		{"main.go", 2},                    // ruling ① root entry
	} {
		action, hits, ignored := evalRulesDetailed(rules, []string{tc.path})
		if action != "deny" {
			t.Errorf("%s: action = %q, want deny (an allow must not loosen gate tiers)", tc.path, action)
		}
		if len(hits) != 1 || hits[0].RuleIndex != 0 || hits[0].Paths[0] != tc.path {
			t.Errorf("%s: hits = %+v, want the one wider deny", tc.path, hits)
		}
		if len(ignored) != 1 || ignored[0].RuleIndex != tc.allowIdx || ignored[0].Paths[0] != tc.path {
			t.Errorf("%s: ignored = %+v, want rule %d's drop recorded", tc.path, ignored, tc.allowIdx)
		}
	}
	// The same rule set narrows happily on unprotected paths — the drop
	// is about the TARGET, never the rule's shape.
	action, _, ignored := evalRulesDetailed(rules, []string{"gui/src/App.tsx"})
	if action != "deny" {
		t.Errorf("gui: action = %q, want deny (no allow matches this path at all)", action)
	}
	if len(ignored) != 0 {
		t.Errorf("gui: ignored = %+v, want none (no allow matched a protected path)", ignored)
	}
	// .odo/ is project STATE, not gate source — the isMemoryPath boundary
	// (not a gate-list entry) is what keeps a diff from ever carrying the
	// rules file, so at the rules layer the claimed-state allow narrows
	// the freeze like any unprotected path.
	if action, _ := evalRules(rules, []string{".odo/rules.json"}); action != "" {
		t.Errorf("state dir = %q, want zero constraint (.odo/ is not gate source)", action)
	}
	// An internal/ path OUTSIDE the Tier-1 prefixes is NOT protected —
	// protection is path-shaped per gatepolicy.go's compiled boundaries;
	// the same allow narrows the freeze there.
	if action, _ := evalRules(rules, []string{"internal/shouldstillexist/x.go"}); action != "" {
		t.Errorf("unprefixed internal path = %q, want zero constraint (prefix boundary honored)", action)
	}
	private := []ruleEntry{
		{Pattern: "**", Action: "deny", Reason: "freeze"},
		{Pattern: "docs/**", Action: "allow", Reason: "docs exempt"},
	}
	if action, _ := evalRules(private, []string{"docs/guide.md"}); action != "" {
		t.Errorf("docs = %q, want zero constraint (allow narrows on unprotected paths)", action)
	}
}

// TestLoadRulesFile pins the fail-loud loader: absent file is the
// zero-overhead posture; ANY defect — unparseable JSON, empty file,
// missing pattern, unknown action, bad glob — fails the WHOLE file to
// zero rules (the pipeline journals rules_parse_error and proceeds).
func TestLoadRulesFile(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, content string) string {
		t.Helper()
		dir := t.TempDir()
		if content != "" {
			if err := os.MkdirAll(filepath.Join(dir, ".odo"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, rulesFilePath), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	t.Run("absent file is zero rules, zero error", func(t *testing.T) {
		rules, err := loadRulesFile(write(t, ""))
		if err != nil || rules != nil {
			t.Errorf("= (%v, %v), want (nil, nil)", rules, err)
		}
	})

	t.Run("valid file parses every field", func(t *testing.T) {
		rules, err := loadRulesFile(write(t, `{"version": 1, "rules": [
			{"pattern": "internal/moa/*.go", "action": "deny", "actor": "auto_land", "reason": "MoA core is human-review-only"},
			{"pattern": "gui/src/**/*.tsx", "action": "ask", "actor": "*", "reason": "GUI panel"},
			{"pattern": "docs/**", "action": "allow"}
		]}`))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if len(rules) != 3 || rules[0].Reason != "MoA core is human-review-only" || rules[2].Actor != "" {
			t.Errorf("rules = %+v", rules)
		}
	})

	t.Run("malformed JSON fails the whole file", func(t *testing.T) {
		if _, err := loadRulesFile(write(t, `{"version": 1, "rules": [`)); err == nil {
			t.Error("want an error for truncated JSON")
		}
	})

	t.Run("empty file fails (JSON of nothing)", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".odo"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rulesFilePath), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadRulesFile(dir); err == nil {
			t.Error("want an error for an empty file")
		}
	})

	t.Run("schema defects fail loud, one per file", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			content string
			want    string
		}{
			{"empty pattern", `{"rules": [{"pattern": "", "action": "deny"}]}`, "pattern is required"},
			{"unknown action", `{"rules": [{"pattern": "**", "action": "block"}]}`, "invalid action"},
			{"bad glob", `{"rules": [{"pattern": "src/[", "action": "deny"}]}`, "invalid glob"},
		} {
			if _, err := loadRulesFile(write(t, tc.content)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: err = %v, want one containing %q", tc.name, err, tc.want)
			}
		}
	})

	t.Run("unknown keys and versions are tolerated (additive schema)", func(t *testing.T) {
		rules, err := loadRulesFile(write(t, `{"version": 42, "rules": [], "future_key": true}`))
		if err != nil || len(rules) != 0 {
			t.Errorf("= (%v, %v), want zero rules, nil error", rules, err)
		}
	})
}

// writeRulesFile drops a rules overlay into the fixture project root.
func writeRulesFile(t *testing.T, root, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rulesFilePath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rulesFilePath), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ruleRows collects the conversation's declarative-rules journal rows
// (rules_parse_error / rule_override_ignored / rule_ask), in order.
func ruleRows(t *testing.T, st *store.Store, convID int64) []map[string]interface{} {
	t.Helper()
	events, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]interface{}
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
		}
		if !jsonUnmarshalOK(ev.Payload, &p) || !strings.HasPrefix(p.Action, "rule") {
			continue
		}
		var full map[string]interface{}
		if err := json.Unmarshal(ev.Payload, &full); err != nil {
			t.Fatal(err)
		}
		out = append(out, full)
	}
	return out
}

// rulesFixture arms a two-model pipeline over autolandRepo with an
// optional rules overlay; the panel stub counts its calls so tests can
// prove blocks happen BEFORE any panel spend.
func rulesFixture(t *testing.T, rulesJSON string) (autonomyFixture, *Server, store.Diff, string, *int64) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	root, sha := autolandRepo(t)
	if rulesJSON != "" {
		writeRulesFile(t, root, rulesJSON)
	}
	s := &Server{store: f.st, projectRoot: root}
	d := f.addDiff(t, "p.diff", patchSrc("src/a.go", 1, 1, false))
	d.BaseSHA = &sha
	return f, s, d, root, calls
}

// TestAutoLandRulesDeny: a matching deny rule refuses the diff
// pre-verify with reason rule_deny:<reason> — zero panel spend, the
// diff stays pending (the human Accept click remains the escape).
func TestAutoLandRulesDeny(t *testing.T) {
	f, s, d, root, calls := rulesFixture(t,
		`{"version": 1, "rules": [{"pattern": "src/**", "action": "deny", "actor": "auto_land", "reason": "src is frozen"}]}`)
	s.autoLand(context.Background(), d, root, "goal", false, "")
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "rule_deny:src is frozen" {
		t.Fatalf("reasons = %v, want [rule_deny:src is frozen]", got)
	}
	if n := atomic.LoadInt64(calls); n != 0 {
		t.Errorf("panel calls = %d, want 0 (deny blocks pre-panel)", n)
	}
	det := blockedDetails(t, f.st, f.c.ID)
	if len(det) != 1 || !strings.Contains(det[0], `"src/**"`) || !strings.Contains(det[0], "src/a.go") {
		t.Errorf("detail = %v, want one row naming the rule pattern and the denied path", det)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending (human Accept stays the escape)", got.Status)
	}
}

// TestAutoLandRulesAsk: an ask rule journals its forced-review posture
// (rule_ask evidence row) and does NOT block — the pipeline proceeds
// to the next gate (verify_unconfigured on this fixture).
func TestAutoLandRulesAsk(t *testing.T) {
	f, s, d, root, calls := rulesFixture(t,
		`{"rules": [{"pattern": "**", "action": "ask", "reason": "panel everything"}]}`)
	s.autoLand(context.Background(), d, root, "goal", false, "")
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "verify_unconfigured" {
		t.Fatalf("reasons = %v, want [verify_unconfigured] (ask never blocks)", got)
	}
	if n := atomic.LoadInt64(calls); n != 0 {
		t.Errorf("panel calls = %d, want 0 (the fixture has no .odo-verify)", n)
	}
	rows := ruleRows(t, f.st, f.c.ID)
	if len(rows) != 1 || rows[0]["action"] != "rule_ask" || rows[0]["rule"] != "**" || rows[0]["reason"] != "panel everything" {
		t.Errorf("rule rows = %+v, want one rule_ask row with the rule and reason", rows)
	}
}

// TestAutoLandRulesAllowOnGatePathIgnored: an allow trying to narrow a
// wider deny on a Tier-1 path is dropped (rule_override_ignored row)
// and the deny stays in force — can-only-tighten at the pipeline level.
func TestAutoLandRulesAllowOnGatePathIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	root, sha := autolandRepo(t)
	writeRulesFile(t, root, `{"rules": [
		{"pattern": "**", "action": "deny", "reason": "freeze"},
		{"pattern": "internal/store/**", "action": "allow", "reason": "claimed store need"}
	]}`)
	s := &Server{store: f.st, projectRoot: root}
	d := f.addDiff(t, "p.diff", patchSrc("internal/store/mutate.go", 1, 1, false))
	d.BaseSHA = &sha
	s.autoLand(context.Background(), d, root, "goal", false, "")
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "rule_deny:freeze" {
		t.Fatalf("reasons = %v, want [rule_deny:freeze] (the allow must not win on gate source)", got)
	}
	if n := atomic.LoadInt64(calls); n != 0 {
		t.Errorf("panel calls = %d, want 0", n)
	}
	rows := ruleRows(t, f.st, f.c.ID)
	if len(rows) != 1 || rows[0]["action"] != "rule_override_ignored" ||
		rows[0]["rule_index"] != float64(1) || rows[0]["reason"] != "cannot loosen gate-tier protection" {
		t.Errorf("rule rows = %+v, want one rule_override_ignored row for rule 1", rows)
	}
}

// TestAutoLandRulesMalformedFailsSafe: a malformed rules file journals
// rules_parse_error and the pipeline proceeds with ZERO rules (the
// overlay never blocks on its own defects).
func TestAutoLandRulesMalformedFailsSafe(t *testing.T) {
	f, s, d, root, _ := rulesFixture(t, `{"version": 1, "rules": [`)
	s.autoLand(context.Background(), d, root, "goal", false, "")
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "verify_unconfigured" {
		t.Fatalf("reasons = %v, want [verify_unconfigured] (malformed file ⇒ zero rules, pipeline proceeds)", got)
	}
	rows := ruleRows(t, f.st, f.c.ID)
	if len(rows) != 1 || rows[0]["action"] != "rules_parse_error" {
		t.Errorf("rule rows = %+v, want one rules_parse_error row", rows)
	}
}

// TestAutoLandRulesAbsentIsSilent: no rules file ⇒ zero rows of every
// rules class, zero overhead — the pre-feature posture byte-for-byte.
func TestAutoLandRulesAbsentIsSilent(t *testing.T) {
	f, s, d, root, _ := rulesFixture(t, "")
	s.autoLand(context.Background(), d, root, "goal", false, "")
	if got := blockedReasons(t, f.st, f.c.ID); len(got) != 1 || got[0] != "verify_unconfigured" {
		t.Fatalf("reasons = %v, want [verify_unconfigured]", got)
	}
	if rows := ruleRows(t, f.st, f.c.ID); len(rows) != 0 {
		t.Errorf("rule rows = %+v, want none when the file is absent", rows)
	}
}
