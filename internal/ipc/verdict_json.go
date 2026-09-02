package ipc

// Structured verdict — the audit item-#5 fallback (P1 borrow from the
// 2026-08-13 harness audit; triaged 2026-08-14 in
// docs/design/fix-int-w7-output-schema-triage.md): OMP has no
// --output-schema flag, so the constraint is schema-IN-PROMPT + strict
// validation here. The review prompt's RESPONSE FORMAT section
// (buildReviewPrompt) asks every leg for exactly one JSON object:
//
//	{"verdict": "accept" | "reject" | "needs_fixes",
//	 "comments": "<review comments, max 500 chars>",
//	 "blockers": ["<blocker 1>", ...]}
//
// parseStructuredVerdict resolves a leg's answer into one of three
// postures:
//
//	structured  valid JSON object, all three fields present and
//	            well-typed (verdict one of the three tokens, comments a
//	            string, blockers an array of strings) — the verdict and
//	            its fields ride the ReviewResult (Blockers journaled
//	            additively on every reviews row).
//	malformed   valid JSON, but the schema is violated — the leg
//	            ACKNOWLEDGED the contract and broke it: that is not a
//	            verdict, it is an infra failure (same class as a
//	            timeout leg; the round fails closed, never
//	            mis-credited as dissent).
//	neither     not a JSON object at all (plain text, markdown-fenced
//	            JSON, prose) — the caller falls back to parseVerdict's
//	            legacy final-line token scan UNCHANGED. Backward compat:
//	            the structured path is a preference, never a
//	            requirement; legacy and structured legs aggregate under
//	            the same consensusVerdict semantics.
//
// Validation is hand-rolled (locked constraint: no JSON-schema
// dependency) against map[string]interface{}; unknown fields are
// ignored (additive tolerance).

import (
	"encoding/json"
	"strings"
)

// parseStructuredVerdict inspects one leg's text. structured=true means
// rr holds the parsed verdict; structured=false + malformed=true means
// JSON-that-violates-the-schema (infra); structured=false +
// malformed=false means legacy-text fallback.
func parseStructuredVerdict(model, text string) (rr ReviewResult, structured, malformed bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "{") || !strings.HasSuffix(t, "}") {
		return ReviewResult{}, false, false // fast gate: plainly not the contract
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(t), &raw); err != nil {
		// Fenced, trailing-prose, or otherwise broken text: treat as
		// legacy, exactly the pre-feature posture. (A leg cannot
		// "almost" acknowledge the contract — only a cleanly parsed
		// object binds it to the schema.)
		return ReviewResult{}, false, false
	}
	verdict, vOK := raw["verdict"].(string)
	comments, cOK := raw["comments"].(string)
	blockersRaw, bOK := raw["blockers"].([]interface{})
	if !vOK || !cOK || !bOK {
		return ReviewResult{}, false, true // missing or mistyped field
	}
	blockers := make([]string, 0, len(blockersRaw))
	for _, b := range blockersRaw {
		s, ok := b.(string)
		if !ok {
			return ReviewResult{}, false, true // blockers must be strings
		}
		blockers = append(blockers, s)
	}
	switch strings.ToLower(strings.TrimSpace(verdict)) {
	case "accept", "reject", "needs_fixes":
		return ReviewResult{
			Model:    model,
			Verdict:  strings.ToLower(strings.TrimSpace(verdict)),
			Comments: strings.TrimSpace(comments),
			Blockers: blockers,
		}, true, false
	}
	return ReviewResult{}, false, true // unknown verdict token
}
