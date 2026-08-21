package ipc

import (
	"context"
	"fmt"
	"sync"
)

// Panel-gated memory apply: the learner's proposals (all targets) are
// reviewed by every configured review model in parallel, and the panel —
// not a human — is the default accept/reject decision. The gate runs after
// the learner and before the memory_propose journal; the auto-apply lands
// after the fold commits (distillCore tail), consuming the batch the
// marker just made pending.
//
//   - Every proposal × every model fans out through reviewProposals; the
//     reviews ride the journaled batch on MemoryProposal.Reviews so the
//     MemoryPanel outcome view (and the journal) shows exactly what was
//     judged.
//   - panelAccepts maps the verdict set to the binary decision: ACCEPT
//     requires every configured model's clean accept; any reject,
//     needs_fixes, or infra leg fails closed (a contested or unattested
//     rule must not steer future runs).
//   - Skills keep the stricter M9 pre-batch split (skill_gate rows): an
//     all-reject skill is auto-discarded before the batch — skills inject
//     into every prompt, so a unanimous panel rejection never even
//     reaches the apply surface. Mixed skill verdicts stay in the batch
//     and fail closed at the panel decision like any other target.
//   - No review models configured (prefs `review:` empty): the gate is
//     inert — proposals journal unreviewed, stay pending, and the human
//     apply path (apply_memory) remains as the fallback.
//
// Infra posture (ADR-0003): an infra leg fails the proposal closed, but
// the failed rule text survives in the journaled memory_propose batch;
// nothing is silently lost and memory.md stays human-editable for
// salvage. Auto-apply errors (e.g. user.md overflow refusal) journal
// memory_update{cause:"auto_apply_failed"} and leave the batch pending —
// the sweep skips epochs with that marker so a refused batch is not
// re-gated (and re-charged) at every distill.

// SkillGateResult is one skill proposal's gate outcome.
type SkillGateResult struct {
	Tier     string // "auto_discard" | "human_gate" | "auto_accept" (deferred)
	Proposal MemoryProposal
	Reviews  []ReviewResult
}

// skillWrite is one pre-computed skill file write (path + content) for the
// apply phase (M9). Skills are written to .odo/skills/<name>.md (project scope).
type skillWrite struct {
	path    string
	content string
}

// classifyGate maps a set of review verdicts to a gate tier for the skill
// pre-batch split. Guards against empty models (no review configured) →
// human_gate (the batch keeps it for the pending/fallback path). All
// rejects → auto_discard; anything else → human_gate (kept in the batch;
// the panel decision at apply time is panelAccepts' job, not this tier's).
func classifyGate(reviews []ReviewResult, numModels int) string {
	if numModels == 0 || len(reviews) == 0 {
		return "human_gate" // no models configured → stays in batch
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
	return "human_gate"
}

// panelAccepts is the panel's binary decision on one gated proposal:
// every configured model must have returned a clean accept — any reject,
// needs_fixes, infra, or missing leg fails the proposal closed. numModels
// is the configured panel size (a leg that errored still ships an Infra
// ReviewResult, so len(reviews) == numModels in a complete fan-out).
func panelAccepts(reviews []ReviewResult, numModels int) bool {
	if numModels == 0 || len(reviews) != numModels {
		return false
	}
	for _, r := range reviews {
		if r.Infra || r.Verdict != "accept" {
			return false
		}
	}
	return true
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

// ruleReviewPrompt wraps a proposed behavior rule (memory.md or user.md
// target) with review criteria, mirroring skillReviewPrompt's
// evidence-anchored shape. The epoch note is provided for hallucination
// cross-checking; the rule's own evidence tag names the note section it
// was distilled from.
func ruleReviewPrompt(p MemoryProposal, epochNote string) string {
	return fmt.Sprintf(`Review the following proposed %s behavior rule. It will steer every future session in this %s. The epoch note it was distilled from is provided for context.

Criteria:
1. Is the rule clearly supported by the epoch note (no hallucination, no over-generalization)?
2. Is it actionable and unambiguous (a future agent can follow it without interpretation)?
3. Is it durable (a stable convention, not a transient task detail or a restatement of an existing instruction)?
4. Would following it have prevented a real mistake or improved the epoch's work?

Verdict must be one of: ACCEPT, REJECT, NEEDS_FIXES

=== EPOCH NOTE ===
%s

=== PROPOSED RULE ===
%s`, p.Target, scopeOf(p.Target), epochNote, p.Rule)
}

// scopeOf names a rule target's blast radius for the review prompt.
func scopeOf(target string) string {
	if target == "user.md" {
		return "global config (all projects)"
	}
	return "project"
}

// proposalReviewPrompt dispatches on target: skills use the procedure
// prompt, everything else the behavior-rule prompt. One dispatch shared by
// the distill gate and the legacy-batch sweep.
func proposalReviewPrompt(p MemoryProposal, epochNote string) string {
	if p.Target == "skills" {
		return skillReviewPrompt(p.Rule, epochNote)
	}
	return ruleReviewPrompt(p, epochNote)
}

// reviewProposals runs ALL proposals × ALL models concurrently (no
// semaphore — matches handleReviewDiff's pattern), returning one review
// slice per proposal (aligned by index) with every configured model's
// verdict — infra legs ship Infra ReviewResults, never gaps.
func (s *Server) reviewProposals(ctx context.Context, proposals []MemoryProposal, models []reviewModel, mkPrompt func(MemoryProposal) string) [][]ReviewResult {
	out := make([][]ReviewResult, len(proposals))
	var wg sync.WaitGroup
	for pi, p := range proposals {
		out[pi] = make([]ReviewResult, len(models))
		for mi, m := range models {
			wg.Add(1)
			go func(pi, mi int, p MemoryProposal, m reviewModel) {
				defer wg.Done()
				out[pi][mi] = s.reviewWithModel(ctx, m, mkPrompt(p))
			}(pi, mi, p, m)
		}
	}
	wg.Wait()
	return out
}

// gateSkillProposals reviews skill proposals through the panel and
// classifies each into the pre-batch tier. Proposals whose tier is
// auto_discard are separated by the caller (distillCore) for journaling as
// skill_gate events; the rest carry their reviews into the batch.
func (s *Server) gateSkillProposals(ctx context.Context, proposals []MemoryProposal, models []reviewModel, epochNote string) []SkillGateResult {
	allReviews := s.reviewProposals(ctx, proposals, models, func(p MemoryProposal) string {
		return skillReviewPrompt(p.Rule, epochNote)
	})
	results := make([]SkillGateResult, len(proposals))
	for i, p := range proposals {
		results[i] = SkillGateResult{
			Proposal: p,
			Reviews:  allReviews[i],
			Tier:     classifyGate(allReviews[i], len(models)),
		}
	}
	return results
}

// splitSkillProposals partitions a proposals slice into skills and non-skills.
// Skills keep the pre-batch auto-discard split; memory/user proposals are
// gated straight into the batch (their reviews ride along).
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
