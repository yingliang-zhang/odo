package ipc

// Declarative rules overlay — .odo/rules.json (P1 borrow from the
// 2026-08-13 tri-model harness audit, item 12 in
// docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md). A
// user-editable rules file layers ON TOP of the hardcoded gate policy
// (gatepolicy.go Tier-0/Tier-1) and can only TIGHTEN it, never peel a
// constraint off: deny and ask only ever ADD pipeline work, and an
// allow never wins on a gate-protected path. The syntax borrows grok's
// deny/ask/allow grammar but resolves per-path by ORDER — the LAST
// matching applicable rule wins (the gitignore precedent) — so a
// narrower rule can neutralize an earlier, wider one on unprotected
// paths:
//
//	[
//	  {"pattern": "**", "action": "deny", "actor": "auto_land",
//	   "reason": "repo frozen"},
//	  {"pattern": "docs/**", "action": "allow", "reason": "docs exempt"},
//	  {"pattern": "gui/**", "action": "ask", "reason": "panel gui"},
//	  {"pattern": "internal/ipc/**", "action": "allow", "reason": "leak"}
//	]
//
// With those: docs land (allow narrows the freeze on an unprotected
// path, ordering rule), gui still panels (ask — informational on
// today's M20 canon, which panels every armed diff; forward-compat
// against any future mechanical fast path), and the LAST allow is
// dropped (internal/ipc/ is Tier-1 gate source): every path keeps the
// wider deny and one rule_override_ignored row names the attempt.
//
// Actions:
//
//	deny   the auto-land pipeline refuses the diff pre-verify
//	       (auto_land_blocked{rule_deny:<reason>}; the human Accept
//	       click stays the unconditional escape, as with Tier-0).
//	ask    forces MoA panel review regardless of any risk-classifier
//	       fast path; journaled as rule_ask evidence rows.
//	allow  explicit passthrough; neutralizes an EARLIER matching rule
//	       on the same path. Never applicable on gate-protected paths
//	       (can-only-tighten): such a rule silently drops to the
//	       rule_override_ignored list.
//
// The rules file itself can never arrive by DIFF: .odo/ is a memory
// path (isMemoryPath), refused for every actor — the human writes and
// edits it by hand, out of band. The can-only-tighten invariant is
// enforced by CODE, not by file-listing the overlay: an "allow" on
// gate source (isGateSourcePath's compiled Tier-0/Tier-1 tables) is
// dropped before it can ever win a path.
// Absent file ⇒ zero rules, zero overhead, zero journal rows. A
// malformed file fails SAFE: zero rules active + one rules_parse_error
// row — never silently honor half a rule set, never block on a typo.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

// rulesFilePath names the overlay project-relatively (journal rows
// agree on the spelling by construction). .odo/ is a memory path
// (isMemoryPath): no pipeline
// diff can ever carry this file for ANY actor, the human click
// included — the human writes and edits it by hand.
const rulesFilePath = ".odo/rules.json"

// rulesActor is the pipeline's only declarative-rules actor today. A
// rule applies when its actor is "", "*", or exactly this — the schema
// keeps actor for forward compat (a future loop actor) without changing
// evaluation semantics.
const rulesActor = "auto_land"

// ruleEntry is one declarative rule (the .odo/rules.json v1 schema).
type ruleEntry struct {
	Pattern string `json:"pattern"` // doublestar glob on '/'-separated repo paths
	Action  string `json:"action"`  // "deny" | "ask" | "allow"
	Actor   string `json:"actor"`   // "" | "*" | "auto_land" — anything else never applies
	Reason  string `json:"reason"`
}

// rulesFileSchema is the on-disk shape: {"version": 1, "rules": [...]}.
// version is accepted but not gated — the schema is additive only
// (ADR-0002 discipline): unknown top-level fields are ignored.
type rulesFileSchema struct {
	Version int         `json:"version"`
	Rules   []ruleEntry `json:"rules"`
}

// ruleHit groups one rule's contribution to a diff's aggregate action:
// the index and pattern (journal evidence), the reason (blocked-row
// suffix for deny), and the matched paths.
type ruleHit struct {
	RuleIndex int
	Pattern   string
	Reason    string
	Paths     []string
}

// ruleOverrideIgnored records a can-only-tighten drop: an allow rule
// tried to win on gate-protected paths and was refused (the previous
// match stands). The pipeline journals one rule_override_ignored review
// row per ignored rule.
type ruleOverrideIgnored struct {
	RuleIndex int
	Pattern   string
	Paths     []string
}

// matchRule matches a doublestar glob against a '/'-separated repo path
// using the stdlib only (no third-party glob dependency): pattern and
// path split into segments, each matched with path.Match syntax, and a
// full "**" segment spans zero or more path segments. Matching
// case-folds both sides (the isGateTier0Path APFS precedent) and
// normalizes Windows separators defensively. Malformed patterns match
// nothing — loadRulesFile rejects them up front, this is the fail-safe.
func matchRule(pattern, p string) bool {
	pat := strings.Split(strings.ToLower(pattern), "/")
	segs := strings.Split(strings.ToLower(strings.ReplaceAll(p, `\`, "/")), "/")
	return matchRuleSegments(pat, segs)
}

// matchRuleSegments: greedy-with-backtrack "**" expansion. Worst case
// is exponential on adversarial patterns ("**/**/**"); rule sets are
// human-authored and tiny, and diff path lists are bounded — no cap
// warranted.
func matchRuleSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true // trailing "**" spans everything remaining
			}
			for i := 0; i <= len(segs); i++ {
				if matchRuleSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// evalRules folds the overlay over a diff's paths (the locked pipeline
// contract): "deny" with the first denying rule's reason if ANY path's
// last matching applicable rule is a deny, else "ask" with the first
// asking rule's reason, else "" (allow / no match is zero constraint).
// Thin wrapper over evalRulesDetailed for callers that don't journal.
func evalRules(rules []ruleEntry, diffPaths []string) (action, reason string) {
	action, hits, _ := evalRulesDetailed(rules, diffPaths)
	if len(hits) > 0 {
		reason = hits[0].Reason
	}
	return action, reason
}

// evalRulesDetailed is the full evaluation: the aggregate action, every
// contributing hit (journal evidence), and every can-only-tighten drop.
// Per path the LAST matching applicable rule wins (gitignore ordering);
// an allow on a gate-protected path (isGateSourcePath) is never
// applicable — it falls to the ignored list and the previous match
// stands. Deny hits outrank ask hits in the aggregate: a single denied
// path refuses the whole diff, panel-forced or not.
func evalRulesDetailed(rules []ruleEntry, diffPaths []string) (action string, hits []ruleHit, ignored []ruleOverrideIgnored) {
	deny := map[int]*ruleHit{}
	ask := map[int]*ruleHit{}
	ignoredPaths := map[int][]string{}
	for _, p := range diffPaths {
		eff := -1 // index of the last applicable match; -1 = no rule applies
		for i, r := range rules {
			if !ruleActorApplies(r.Actor) || !matchRule(r.Pattern, p) {
				continue
			}
			if r.Action == "allow" && isGateSourcePath(p) {
				ignoredPaths[i] = append(ignoredPaths[i], p)
				continue // can-only-tighten: an allow never wins on gate source
			}
			eff = i
		}
		if eff < 0 {
			continue
		}
		r := rules[eff]
		var bin map[int]*ruleHit
		switch r.Action {
		case "deny":
			bin = deny
		case "ask":
			bin = ask
		default:
			continue // allow — the path contributes no constraint
		}
		h, ok := bin[eff]
		if !ok {
			h = &ruleHit{RuleIndex: eff, Pattern: r.Pattern, Reason: r.Reason}
			bin[eff] = h
		}
		h.Paths = append(h.Paths, p)
	}
	// Deterministic output order: rule index, for hits and drops alike.
	collect := func(m map[int]*ruleHit) []ruleHit {
		out := make([]ruleHit, 0, len(m))
		for i := range rules {
			if h, ok := m[i]; ok {
				out = append(out, *h)
			}
		}
		return out
	}
	for i := range rules {
		if paths, ok := ignoredPaths[i]; ok {
			ignored = append(ignored, ruleOverrideIgnored{RuleIndex: i, Pattern: rules[i].Pattern, Paths: paths})
		}
	}
	switch {
	case len(deny) > 0:
		return "deny", collect(deny), ignored
	case len(ask) > 0:
		return "ask", collect(ask), ignored
	}
	return "", nil, ignored
}

// capRulePaths bounds a journaled path list at 20 entries (+N marker) —
// a 3000-file diff's deny/override evidence stays a bounded row.
func capRulePaths(paths []string) []string {
	const max = 20
	if len(paths) <= max {
		return paths
	}
	return append(append([]string(nil), paths[:max]...), fmt.Sprintf("… +%d more", len(paths)-max))
}

// ruleActorApplies: blank and "*" mean every pipeline actor; anything
// else must name the current actor exactly.
func ruleActorApplies(actor string) bool {
	return actor == "" || actor == "*" || actor == rulesActor
}

// loadRulesFile reads and strictly validates the overlay. Absent file ⇒
// (nil, nil) — the zero-overhead posture. ANY defect (unreadable,
// unparseable JSON, empty pattern, unknown action, bad glob) fails the
// WHOLE file with a non-nil error: the caller journals rules_parse_error
// and proceeds with zero active rules — the declared fail-safe (never
// silently honor half a rule set, and zero rules can only LACK
// constraints, never loosen the gate below its compiled floor).
func loadRulesFile(projectRoot string) ([]ruleEntry, error) {
	data, err := os.ReadFile(filepath.Join(projectRoot, rulesFilePath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", rulesFilePath, err)
	}
	var f rulesFileSchema
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", rulesFilePath, err)
	}
	for i, r := range f.Rules {
		if r.Pattern == "" {
			return nil, fmt.Errorf("%s rule %d: pattern is required", rulesFilePath, i)
		}
		switch r.Action {
		case "deny", "ask", "allow":
		default:
			return nil, fmt.Errorf("%s rule %d: invalid action %q (want deny|ask|allow)", rulesFilePath, i, r.Action)
		}
		if err := validateRulePattern(r.Pattern); err != nil {
			return nil, fmt.Errorf("%s rule %d: %v", rulesFilePath, i, err)
		}
	}
	return f.Rules, nil
}

// validateRulePattern compiles every glob segment up front — path.Match
// defers ErrBadPattern to match time, and a silent never-match rule is
// exactly the misconfiguration the fail-loud loader exists to catch.
// "**" validates as a segment of its own; mid-segment "**" is ordinary
// star-star syntax (path.Match accepts it).
func validateRulePattern(pattern string) error {
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, "_"); err != nil {
			return fmt.Errorf("invalid glob %q: %w", pattern, err)
		}
	}
	return nil
}

// journalRulesEvent appends one declarative-rules pipeline row
// (rules_parse_error / rule_override_ignored / rule_ask): additive
// review_action shapes riding the diff's conversation with the pipeline
// actor, like every other auto-land evidence row. Best-effort — these
// rows inform; the deny hard-block is journaled separately as
// auto_land_blocked (evidence-before-action there is handled by
// journalAutoLandBlocked's own discipline).
func (s *Server) journalRulesEvent(ctx context.Context, d store.Diff, payload map[string]interface{}) {
	payload["diff_id"] = d.ID
	payload["actor"] = autoActor
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(payload)); err != nil {
		log.Printf("auto-land: journal rules event (%v) for diff %d: %v", payload["action"], d.ID, err)
	}
}
