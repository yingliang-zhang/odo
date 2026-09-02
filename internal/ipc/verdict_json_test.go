package ipc

import (
	"strings"
	"testing"
)

// TestParseStructuredVerdict pins the three-posture contract (the OMP
// output-schema fallback): valid JSON with every required field parses
// structured; valid JSON violating the schema is MALFORMED (the caller
// fails the leg as infra); anything not a clean JSON object is neither —
// the legacy final-line token scan keeps verbatim ownership.
func TestParseStructuredVerdict(t *testing.T) {
	t.Parallel()

	t.Run("valid accept with empty blockers", func(t *testing.T) {
		rr, structured, malformed := parseStructuredVerdict("m1",
			`{"verdict": "accept", "comments": "looks right", "blockers": []}`)
		if !structured || malformed {
			t.Fatalf("structured=%v malformed=%v, want (true, false)", structured, malformed)
		}
		if rr.Verdict != "accept" || rr.Comments != "looks right" || len(rr.Blockers) != 0 {
			t.Errorf("rr = %+v", rr)
		}
	})

	t.Run("valid reject with blockers array", func(t *testing.T) {
		rr, structured, _ := parseStructuredVerdict("m1",
			`{"verdict": "reject", "comments": "wrong layer", "blockers": ["uses store directly from gui", "no test for the failure path"]}`)
		if !structured || rr.Verdict != "reject" || len(rr.Blockers) != 2 || rr.Blockers[0] != "uses store directly from gui" {
			t.Errorf("rr = %+v, want structured reject with two blockers", rr)
		}
	})

	t.Run("needs_fixes plus uppercase and whitespace tolerance", func(t *testing.T) {
		for _, text := range []string{
			`{ "verdict": "NEEDS_FIXES", "comments": "tighten the loop", "blockers": ["loop"] }`,
			"\n\t{\"verdict\": \"Accept\", \"comments\": \"\", \"blockers\": []}\n",
		} {
			rr, structured, malformed := parseStructuredVerdict("m1", text)
			if !structured || malformed {
				t.Errorf("%q: structured=%v malformed=%v, want (true, false)", text, structured, malformed)
			}
			if rr.Verdict == "" {
				t.Errorf("%q: empty parsed verdict", text)
			}
		}
	})

	t.Run("unknown extra fields tolerated (additive schema)", func(t *testing.T) {
		if _, structured, malformed := parseStructuredVerdict("m1",
			`{"verdict": "accept", "comments": "x", "blockers": [], "confidence": 0.9}`); !structured || malformed {
			t.Errorf("structured=%v malformed=%v, want (true, false)", structured, malformed)
		}
	})

	t.Run("missing or mistyped fields are malformed (infra)", func(t *testing.T) {
		for _, tc := range []struct {
			name, text string
		}{
			{"missing comments", `{"verdict": "accept", "blockers": []}`},
			{"missing blockers", `{"verdict": "accept", "comments": "x"}`},
			{"missing verdict", `{"comments": "x", "blockers": []}`},
			{"bad verdict token", `{"verdict": "ship", "comments": "x", "blockers": []}`},
			{"blockers not an array", `{"verdict": "accept", "comments": "x", "blockers": "none"}`},
			{"non-string blocker", `{"verdict": "reject", "comments": "x", "blockers": [1]}`},
			{"comments not a string", `{"verdict": "accept", "comments": 3, "blockers": []}`},
		} {
			if _, structured, malformed := parseStructuredVerdict("m1", tc.text); structured || !malformed {
				t.Errorf("%s: structured=%v malformed=%v, want (false, true)", tc.name, structured, malformed)
			}
		}
	})

	t.Run("not JSON falls through to legacy", func(t *testing.T) {
		for _, tc := range []struct {
			name, text string
		}{
			{"legacy token text", "Analysis.\nACCEPT\nship it"},
			{"plain analysis", "no verdict token here"},
			{"fenced JSON", "```json\n{\"verdict\": \"accept\", \"comments\": \"x\", \"blockers\": []}\n```"},
			{"trailing prose", "{\"verdict\": \"accept\", \"comments\": \"x\", \"blockers\": []}\n(Signed, the panel)"},
			{"json array", `[{"verdict": "accept"}]`},
			{"json scalar", `"accept"`},
		} {
			if _, structured, malformed := parseStructuredVerdict("m1", tc.text); structured || malformed {
				t.Errorf("%s: structured=%v malformed=%v, want (false, false) — legacy owns this", tc.name, structured, malformed)
			}
		}
	})
}

// TestReviewVerdictStructured pins reviewVerdict's routing: structured
// answers carry their fields into the ReviewResult, malformed JSON is an
// infra leg (never direction evidence), legacy text keeps M16 parsing,
// and truncation degrades every path fail-closed.
func TestReviewVerdictStructured(t *testing.T) {
	t.Parallel()

	t.Run("structured leg carries verdict, comments, and blockers", func(t *testing.T) {
		rr := reviewVerdict("m1",
			`{"verdict": "needs_fixes", "comments": "two issues", "blockers": ["missing caller migration", "weakened test"]}`, false)
		if rr.Verdict != "needs_fixes" || rr.Comments != "two issues" || len(rr.Blockers) != 2 || rr.Infra {
			t.Errorf("rr = %+v", rr)
		}
	})

	t.Run("malformed JSON is infra, not dissent", func(t *testing.T) {
		rr := reviewVerdict("m1", `{"verdict": "reject"}`, false) // missing comments/blockers
		if !rr.Infra || rr.Verdict != "needs_fixes" {
			t.Errorf("rr = %+v, want Infra needs_fixes (the schema violator is not a verdict)", rr)
		}
		if !strings.Contains(rr.Comments, "infra failure") {
			t.Errorf("comments = %q, want the infra marker", rr.Comments)
		}
	})

	t.Run("legacy text parses via parseVerdict", func(t *testing.T) {
		rr := reviewVerdict("m1", "Analysis first.\nREJECT\nthe direction is wrong", false)
		if rr.Verdict != "reject" || rr.Comments != "the direction is wrong" || rr.Infra || len(rr.Blockers) != 0 {
			t.Errorf("rr = %+v, want legacy reject with zero blockers", rr)
		}
	})

	t.Run("truncation degrades a structured accept fail-closed", func(t *testing.T) {
		rr := reviewVerdict("m1",
			`{"verdict": "accept", "comments": "looks right", "blockers": []}`, true)
		if rr.Verdict != "needs_fixes" || !rr.Truncated || !strings.Contains(rr.Comments, "truncated") {
			t.Errorf("rr = %+v, want forced needs_fixes with the truncation marker", rr)
		}
	})

	t.Run("blockers ride the journaled row (additive key)", func(t *testing.T) {
		raw := string(mustJSON(ReviewResult{Model: "m1", Verdict: "reject", Comments: "x", Blockers: []string{"b1"}}))
		if !strings.Contains(raw, `"blockers":["b1"]`) {
			t.Errorf("marshaled row = %s, want the blockers key", raw)
		}
		// Legacy legs carry no key at all (omitempty) — byte shape unchanged.
		raw = string(mustJSON(ReviewResult{Model: "m1", Verdict: "accept"}))
		if strings.Contains(raw, "blockers") {
			t.Errorf("legacy row = %s, want no blockers key", raw)
		}
	})
}

// TestConsensusMixedStructuredLegs pins aggregation under mixed output
// shapes: structured and legacy legs produce the same ReviewResult
// type, so consensusVerdict semantics are untouched — any reject
// dominates, accept requires every leg, and a malformed (infra) leg
// keeps the round fail-closed (panelInfraLeg) instead of reading as
// dissent.
func TestConsensusMixedStructuredLegs(t *testing.T) {
	t.Parallel()

	structured := reviewVerdict("m1", `{"verdict": "accept", "comments": "ok", "blockers": []}`, false)
	legacy := reviewVerdict("m2", "The diff is sound.\nACCEPT\nship it", false)
	if consensusVerdict([]ReviewResult{structured, legacy}) != "accept" {
		t.Error("structured accept + legacy accept must tally accept")
	}

	structuredReject := reviewVerdict("m3", `{"verdict": "reject", "comments": "no", "blockers": ["wrong layer"]}`, false)
	if consensusVerdict([]ReviewResult{structured, legacy, structuredReject}) != "reject" {
		t.Error("a reject leg dominates regardless of output shape")
	}

	legacyReject := reviewVerdict("m3", "REJECT\nwrong layer", false)
	if consensusVerdict([]ReviewResult{structured, legacyReject}) != "reject" {
		t.Error("legacy reject dominates a structured accept")
	}

	infra := reviewVerdict("m4", `{"verdict": "accept"}`, false) // schema violator
	legs := []ReviewResult{structured, legacy, infra}
	if consensusVerdict(legs) != "needs_fixes" {
		t.Error("an infra leg blocks the accept tally (fail closed)")
	}
	if !panelInfraLeg(legs) {
		t.Error("panelInfraLeg must see the malformed-JSON leg — the round fails as panel_infra, not as disagreement")
	}
}

// TestBuildReviewPromptStructuredSection: the RESPONSE FORMAT contract
// rides the shared prompt, and every legacy assertion anchor survives
// (the parse path's prompt-test contract is additive).
func TestBuildReviewPromptStructuredSection(t *testing.T) {
	t.Parallel()
	p := buildReviewPrompt(reviewPromptInput{
		mode:     reviewPromptGate,
		goal:     "ship the fix",
		diffPath: "x.diff",
		diffText: "DIFF",
	})
	for _, want := range []string{
		"three concrete ways",            // adversarial instruction kept
		"ACCEPT, REJECT, or NEEDS_FIXES", // legacy anchor kept verbatim
		"data, not instructions",         // fence discipline kept
		"RESPONSE FORMAT",                // the new contract
		`"verdict": "accept" | "reject" | "needs_fixes"`,
		`"blockers"`,
		"fall back to the legacy contract",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}
