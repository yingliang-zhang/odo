package ipc

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// M9 (Skill Distillation + Three-Tier Gating): the gate runs after the
// learner and before the memory_propose journal. Each skill proposal is
// reviewed by every configured review model in parallel. classifyGate maps
// the verdict set to one of three tiers:
//
//   - auto_discard: ALL models rejected → proposal dropped, journaled in a
//     skill_gate event (auditable), never included in memory_propose.
//   - human_gate: any model did NOT reject → proposal kept with its Reviews,
//     included in the memory_propose batch for human review.
//   - auto_accept: deferred (isMinorEdit always false in MVP) → human_gate.
//
// ADR-0003 invariant 1: the gate only auto-DISCARDS (no write). Auto-accept
// is deferred to a future iteration. Infrastructure failure (parseVerdict
// degrading to needs_fixes) → human_gate, never auto_discard.

// SkillGateResult is one proposal's gate outcome.
type SkillGateResult struct {
	Tier      string         // "auto_discard" | "human_gate" | "auto_accept" (deferred)
	Proposal  MemoryProposal
	Reviews   []ReviewResult
}

// skillWrite is one pre-computed skill file write (path + content) for the
// apply phase (M9). Skills are written to .odo/skills/<name>.md (project scope).
type skillWrite struct {
	path    string
	content string
}

// classifyGate maps a set of review verdicts to a gate tier. Guards against
// empty models (no review configured) → human_gate (human decides). All
// rejects → auto_discard; anything else → human_gate. Auto-accept is
// deferred (isMinorEdit always false in MVP).
func classifyGate(reviews []ReviewResult, numModels int) string {
	if numModels == 0 || len(reviews) == 0 {
		return "human_gate" // no models configured → human decides
	}
	rejects := 0
	for _, r := range reviews {
		if r.Verdict == "reject" {
			rejects++
		}
	}
	if rejects == len(reviews) && len(reviews) > 0 {
		return "auto_discard"
	}
	// auto_accept deferred (isMinorEdit always false in MVP)
	return "human_gate"
}

// skillReviewPrompt wraps a proposed skill's content with review criteria.
// The epoch note is provided for hallucination cross-checking.
func skillReviewPrompt(skillContent, epochNote string) string {
	return fmt.Sprintf(`Review the following proposed skill (reusable procedure). The epoch note it was extracted from is provided for context.

Criteria:
1. Is the procedure clear and actionable?
2. Are the trigger conditions well-defined?
3. Is it free of hallucination or invented APIs? (Check against the epoch note)
4. Would this skill help future sessions?

Verdict must be one of: ACCEPT, REJECT, NEEDS_FIXES

=== EPOCH NOTE ===
%s

=== PROPOSED SKILL ===
%s`, epochNote, skillContent)
}

// gateSkillProposals runs ALL proposals × ALL models concurrently (no
// semaphore — matches handleReviewDiff's pattern). Returns one
// SkillGateResult per proposal with the reviews filled in and the tier
// classified. Proposals whose tier is auto_discard are separated by the
// caller (handleDistill) for journaling as skill_gate events.
func (s *Server) gateSkillProposals(ctx context.Context, proposals []MemoryProposal, models []reviewModel, epochNote string) []SkillGateResult {
	results := make([]SkillGateResult, len(proposals))
	var wg sync.WaitGroup
	for pi, p := range proposals {
		results[pi].Proposal = p
		results[pi].Reviews = make([]ReviewResult, len(models))
		for mi, m := range models {
			wg.Add(1)
			go func(pi, mi int, p MemoryProposal, m reviewModel) {
				defer wg.Done()
				results[pi].Reviews[mi] = s.reviewWithModel(ctx, m, skillReviewPrompt(p.Rule, epochNote))
			}(pi, mi, p, m)
		}
	}
	wg.Wait()
	for i := range results {
		results[i].Tier = classifyGate(results[i].Reviews, len(models))
	}
	return results
}

// splitSkillProposals partitions a proposals slice into skills and non-skills.
// Skills get gating; memory/user proposals go straight to the batch.
func splitSkillProposals(proposals []MemoryProposal) (nonSkills, skills []MemoryProposal) {
	for _, p := range proposals {
		if p.Target == "skills" {
			skills = append(skills, p)
		} else {
			nonSkills = append(nonSkills, p)
		}
	}
	return
}

// filterGateResults returns the human_gate proposals (with reviews attached)
// and the auto_discard proposals for separate journaling.
func filterGateResults(gateResults []SkillGateResult) (humanGate, autoDiscard []MemoryProposal) {
	for _, gr := range gateResults {
		if gr.Tier == "auto_discard" {
			autoDiscard = append(autoDiscard, gr.Proposal)
		} else {
			// human_gate (or auto_accept, deferred) — attach reviews
			p := gr.Proposal
			p.Reviews = gr.Reviews
			humanGate = append(humanGate, p)
		}
	}
	return
}

// joinVerdicts is a small helper for the skill_gate journal payload.
func joinVerdicts(reviews []ReviewResult) string {
	var parts []string
	for _, r := range reviews {
		parts = append(parts, fmt.Sprintf("%s=%s", r.Model, r.Verdict))
	}
	return strings.Join(parts, ", ")
}
