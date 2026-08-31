package ipc

// D9-W4 (lock stage machine; K3 spec §2.3 "lint" + "security" gates, §4.3
// R2 freeze): the two LLM-free pre-replay gates. Both are pure functions
// over the candidate row + base content + journal-derived freeze set —
// zero model calls, determinism pinned (replay-e depends on it).
//
//   - lint: projected block honors memoryCap, every non-opaque line parses
//     as memoryLineRe, no duplicate normalized add (vs base or in-batch),
//     every retract target exists in base, every evidence cite resolves to
//     a real wiki note (contained check — never an escaping path), no add
//     text is in the R2 candidate freeze set (rolled-back or harmful-
//     dropped texts within 3 main-lane epochs).
//   - security: no rule line matches secret-shaped patterns (risk.go
//     family — assignments, ~/.ssh/id_*-style paths, userinfo URLs,
//     ../ escapes). A memory rule is prompt content: planted secret-shape
//     or path-escape material is an exfil/poisoning channel, rejected
//     per line with the pattern name journaled.
//
// Reports are ordered and stable: violations are collected in a fixed
// check order and sorted within a check by rule text, so two executions
// of the same inputs marshal byte-identical (double-execution pin).

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

// The three W4 gates, journaled as review_action{action:"learning_gate",
// gate:<name>} — the stage row cites all three verdicts.
const (
	learningGateLint     = "lint"
	learningGateSecurity = "security"
	learningGateReplay   = "replay"
)

// learningViolation is one per-line (or per-rule) gate reject. Reason is
// the stable machine-readable cause; Pattern names the security regex
// family when relevant.
type learningViolation struct {
	Rule    string `json:"rule"`
	Reason  string `json:"reason"`
	Pattern string `json:"pattern,omitempty"`
}

// learningGateReport is one gate's journaled outcome. Detail carries the
// gate-specific metrics map (replay uses it for the a–h counters; lint/
// security stay empty — everything they know is in Violations).
type learningGateReport struct {
	Gate       string                 `json:"gate"`
	Verdict    string                 `json:"verdict"` // "pass" | "fail" | "unverifiable" (replay only; FAIL)
	Violations []learningViolation    `json:"violations,omitempty"`
	Detail     map[string]interface{} `json:"detail,omitempty"`
}

func (r learningGateReport) passed() bool { return r.Verdict == "pass" }

// learningFreezeMainEpochs is the R2 candidate freeze window: a rolled-back
// (or harmful-dropped) rule TEXT cannot re-enter via a candidate for 3
// MAIN-lane epochs (project-scoped cadence — candidates are project
// artifacts; the lane-level oscillationWindowEpochs guard in
// memory_flags.go is the sibling with the same constant, deliberately:
// one convention, scope-keyed windows).
const learningFreezeMainEpochs = 3

// learningCandidateFreezeSet folds MAIN-lane learning_rollback rows (W5
// emits them; W4 lands the fold + check) plus learning_frozen rows into
// the R2 frozen-text set: normalized rule text -> freeze reason
// (learning_frozen journals DECORATED texts — the stage interrupt's
// text + " (" + reason + ")" form, learningFrozenHits — so the fold
// keys the bare rule, learningFrozenBareText). currentMainEpoch
// is the lane's just-completed epoch; a rollback at epoch E freezes texts
// while 0 <= currentMainEpoch-E <= learningFreezeMainEpochs (boundary fixture:
// rollback at N ⇒ re-propose at N+1..N+3 rejected, N+4 free; the same-epoch
// E==current read stays frozen — conservative).
func learningCandidateFreezeSet(events []store.Event, currentMainEpoch int) map[string]string {
	frozen := map[string]string{}
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string   `json:"action"`
			Epoch  int      `json:"epoch"`
			Reason string   `json:"reason"`
			Rules  []string `json:"retracted"`
			Texts  []string `json:"texts"`
		}
		if jsonUnmarshalOK(ev.Payload, &p) != true {
			continue
		}
		var texts []string
		var why string
		switch p.Action {
		case "learning_rollback":
			texts, why = p.Rules, "rolled back"
		case "learning_frozen":
			// Production shape (#118 panel): undecorate before the
			// freeze key — the journaled text carries its reason.
			for i, t := range p.Texts {
				p.Texts[i] = learningFrozenBareText(t)
			}
			texts, why = p.Texts, "frozen"
		default:
			continue
		}
		if p.Reason != "" {
			why = p.Reason
		}
		age := currentMainEpoch - p.Epoch
		if age < 0 || age > learningFreezeMainEpochs {
			continue // outside the freeze window (N+4 free per R2)
		}
		for _, t := range texts {
			if nt := normalizeRule(t); nt != "" {
				frozen[nt] = fmt.Sprintf("oscillation_guard: %s at main epoch %d (within %d)", why, p.Epoch, learningFreezeMainEpochs)
			}
		}
	}
	return frozen
}

// learningFrozenReasonMarker delimits the reason decoration the R2
// stage-interrupt appends to each journaled learning_frozen text
// (learningFrozenHits: text + " (" + reason + ")"; the reason itself
// carries parentheses — "(within N)" — so the cut keys on this fixed
// prefix, never on a paren pair).
const learningFrozenReasonMarker = " (oscillation_guard: "

// learningFrozenBareText strips the stage-interrupt's reason decoration
// from a journaled learning_frozen text, returning the bare rule text
// (the freeze-set key). Undecorated texts pass through unchanged.
func learningFrozenBareText(t string) string {
	if i := strings.LastIndex(t, learningFrozenReasonMarker); i > 0 && strings.HasSuffix(t, ")") {
		return t[:i]
	}
	return t
}

// learningEvidenceNoteRe matches a cites token the learner emits: a wiki
// note name WITHOUT extension or path separators (`main-epoch-16`,
// `moa-chain-epoch-1`). Anything else ("/", "..", ".md") never reaches the
// filesystem — containment by construction, the readWithinDir precedent.
var learningEvidenceNoteRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// learningWikiNoteExists reports whether wiki/<token>.md exists under the
// project's wiki dir. The token is pre-filtered by learningEvidenceNoteRe,
// so filepath.Join cannot escape <projectRoot>/wiki.
func learningWikiNoteExists(projectRoot, token string) bool {
	if !learningEvidenceNoteRe.MatchString(token) {
		return false
	}
	st, err := os.Stat(filepath.Join(projectRoot, "wiki", token+".md"))
	return err == nil && !st.IsDir()
}

// lintLearningCandidate runs the §2.3 lint gate. base is the FULL uncapped
// memory.md the candidate projected from; frozen is the R2 freeze set
// (learningCandidateFreezeSet, main-lane events — nil disables the check
// for unit fixtures).
func lintLearningCandidate(projectRoot, base string, cand LearningCandidate, frozen map[string]string) learningGateReport {
	rep := learningGateReport{Gate: learningGateLint, Verdict: "pass"}
	var vs []learningViolation

	// (1) Cap: the projected injected block must fit memoryCap — the same
	// bound the file write path and the injection read enforce.
	if len(cand.Content) > memoryCap {
		vs = append(vs, learningViolation{
			Reason: fmt.Sprintf("content exceeds memoryCap (%d > %d)", len(cand.Content), memoryCap),
		})
	}

	// (2) Format: every non-empty line of the projection must parse as a
	// daemon rule line OR be an opaque base line preserved verbatim
	// (planMemoryApply never rewrites opaque lines; anything else is a
	// malformed render — unreachable through planMemoryApply, asserted
	// fail-closed).
	baseOpaque := map[string]bool{}
	for _, r := range parseMemoryLines(base) {
		if r.opaque {
			baseOpaque[r.raw] = true
		}
	}
	baseRules := map[string]bool{}
	for _, r := range parseMemoryLines(base) {
		if !r.opaque && r.text != "" {
			baseRules[normalizeRule(r.text)] = true
		}
	}
	for _, line := range strings.Split(cand.Content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if memoryLineRe.MatchString(line) {
			continue
		}
		if baseOpaque[line] {
			continue
		}
		vs = append(vs, learningViolation{Rule: line, Reason: "line fails memoryLineRe and is not a preserved opaque base line"})
	}

	// (3) Duplicates: an add must be novel against base AND in-batch —
	// normalized compare, the planMemoryApply/contradiction convention.
	seen := map[string]bool{}
	for _, a := range cand.Delta.Add {
		na := normalizeRule(a.Rule)
		if na == "" {
			vs = append(vs, learningViolation{Rule: a.Rule, Reason: "empty rule text"})
			continue
		}
		if baseRules[na] {
			vs = append(vs, learningViolation{Rule: a.Rule, Reason: "duplicate of existing memory.md rule"})
		}
		if seen[na] {
			vs = append(vs, learningViolation{Rule: a.Rule, Reason: "duplicate within delta.add"})
		}
		seen[na] = true
	}

	// (4) Retract targets: a retract entry names a verbatim rule text that
	// must exist in base (normalized, non-opaque). Phantom retractions are
	// rejected (K3: the D4 line — retraction is evidence-bound, never
	// invented).
	for _, t := range cand.Delta.Retract {
		if nt := normalizeRule(t); nt == "" || !baseRules[nt] {
			vs = append(vs, learningViolation{Rule: t, Reason: "retract target absent from base memory.md"})
		}
	}

	// (5) Evidence: every add must cite a note that exists under wiki/.
	// Multi-cite entries are comma-separated (the cites group of
	// memoryLineRe); each token is checked individually.
	for _, a := range cand.Delta.Add {
		if strings.TrimSpace(a.Evidence) == "" {
			vs = append(vs, learningViolation{Rule: a.Rule, Reason: "missing evidence cite"})
			continue
		}
		for _, tok := range strings.Split(a.Evidence, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			if !learningWikiNoteExists(projectRoot, tok) {
				vs = append(vs, learningViolation{Rule: a.Rule, Reason: fmt.Sprintf("evidence note %q missing under wiki/", tok)})
			}
		}
	}

	// (6) R2 freeze: an add whose normalized text is frozen (rolled back /
	// harmful-dropped within learningFreezeMainEpochs main-lane epochs) is
	// rejected — the candidate-side enforcement of the oscillation freeze
	// (lock R2; learner-vet and stage-interrupt are the sibling points).
	for _, a := range cand.Delta.Add {
		if reason, froz := frozen[normalizeRule(a.Rule)]; froz {
			vs = append(vs, learningViolation{Rule: a.Rule, Reason: reason})
		}
	}

	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Rule != vs[j].Rule {
			return vs[i].Rule < vs[j].Rule
		}
		return vs[i].Reason < vs[j].Reason
	})
	if len(vs) > 0 {
		rep.Verdict = "fail"
		rep.Violations = vs
	}
	return rep
}

// --- security gate ---------------------------------------------------------

// learningSecretAssignRe matches a secret-shaped ASSIGNMENT
// (`MY_API_KEY=...`, `DB_PASSWORD: ...`) — value-bearing, never a bare
// env-name mention in prose (riskEnvSecretRe's family, tightened for rule
// text per K3 §2.3 "assignments").
var learningSecretAssignRe = regexp.MustCompile(`[A-Z][A-Z0-9_]*(_KEY|_TOKEN|_SECRET|_PASSWORD)\s*[:=]\s*\S`)

// learningSecretPathTokens are planted secret-store path shapes
// (riskSecretPathTokens family).
var learningSecretPathTokens = []string{".ssh/id_", ".aws/credentials", ".gnupg", "keychain"}

// learningUserinfoURLRe matches a userinfo-bearing URL
// (scheme://user[:pass]@host) — credential smuggling in prompt text.
var learningUserinfoURLRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s/@]+@`)

// securityLearningCandidate runs the §2.3 security gate over the
// candidate's delta texts: secret assignments, secret-store paths,
// userinfo URLs, and ../ path escapes. Per-line rejects name the pattern
// family (journaled evidence).
func securityLearningCandidate(cand LearningCandidate) learningGateReport {
	rep := learningGateReport{Gate: learningGateSecurity, Verdict: "pass"}
	var vs []learningViolation
	check := func(text string) {
		if learningSecretAssignRe.MatchString(text) {
			vs = append(vs, learningViolation{Rule: text, Reason: "secret-shaped assignment", Pattern: "secret_assignment"})
		}
		for _, tok := range learningSecretPathTokens {
			if strings.Contains(text, tok) {
				vs = append(vs, learningViolation{Rule: text, Reason: "secret-store path token", Pattern: "secret_path"})
				break
			}
		}
		if learningUserinfoURLRe.MatchString(text) {
			vs = append(vs, learningViolation{Rule: text, Reason: "userinfo-bearing URL", Pattern: "userinfo_url"})
		}
		if strings.Contains(text, "../") || strings.Contains(text, `..\`) {
			vs = append(vs, learningViolation{Rule: text, Reason: "path escape token", Pattern: "dotdot_escape"})
		}
	}
	for _, a := range cand.Delta.Add {
		check(a.Rule)
	}
	for _, t := range cand.Delta.Retract {
		check(t)
	}
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Rule != vs[j].Rule {
			return vs[i].Rule < vs[j].Rule
		}
		return vs[i].Pattern < vs[j].Pattern
	})
	if len(vs) > 0 {
		rep.Verdict = "fail"
		rep.Violations = vs
	}
	return rep
}
