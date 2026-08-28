package main

import (
	"strings"
	"testing"
)

// D4: `odo rules retract` never touches more than one line and always
// records the exact removed line — the fail-closed match is the whole
// contract (zero or multiple matches refuse).
func TestRulesRetractPlan(t *testing.T) {
	content := "- alpha rule — cites: e1; reaffirmed: 1\n- beta rule — cites: e2; reaffirmed: 2\nhand note\n"

	got, line, err := rulesRetractPlan(content, "beta rule")
	if err != nil {
		t.Fatalf("unique match: %v", err)
	}
	if line != "- beta rule — cites: e2; reaffirmed: 2" {
		t.Errorf("matched line = %q, want the verbatim rule line", line)
	}
	if got != "- alpha rule — cites: e1; reaffirmed: 1\nhand note\n" {
		t.Errorf("remaining content = %q — every other line stays byte-exact", got)
	}

	// A substring matching several lines refuses with the count.
	if _, _, err := rulesRetractPlan(content, "rule"); err == nil || !strings.Contains(err.Error(), "2 memory.md lines") {
		t.Errorf("multi match = %v, want the counted refusal", err)
	}
	// Zero matches refuse too (no no-op journal rows).
	if _, _, err := rulesRetractPlan(content, "nonexistent"); err == nil {
		t.Error("zero matches must refuse")
	}
	// Empty text refuses.
	if _, _, err := rulesRetractPlan(content, "  "); err == nil {
		t.Error("empty match text must refuse")
	}
	// Removing the only line leaves a trailing-newline-free empty file.
	if got, _, err := rulesRetractPlan("- only — cites: e1; reaffirmed: 1\n", "only"); err != nil || got != "" {
		t.Errorf("last-line removal = %q, %v — want empty content", got, err)
	}
}
