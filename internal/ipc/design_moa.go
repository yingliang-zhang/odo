package ipc

// R-W4 (Design-MoA, router-vs-omp-eval-2026-08-14 §3/§4): a goal fans out
// to blind-sealed proposal legs — one per prefs `review:` model, each an
// independent moa.QueryWithTools loop over read-only repo-root-scoped FS
// tools (16-round cap, no cross-leg communication) — then ONE
// orchestrator-model moa.Query synthesizes the proposals into a single
// DESIGN LOCK document. The row is journaled as review_action
// {action:"design_lock"} (additive payload keys, ADR-0002 immune): the
// missing "4th model call" of the consensusVerdict comment, productized.
//
// Truncation is strict (plan row #9): a leg whose final answer hits the
// model's hard output cap is a failed leg — its partial text never feeds
// the consolidator; the pipeline proceeds on the surviving legs and dies
// only when EVERY leg failed; a consolidator truncation fails the command
// closed with no design_lock row (the runMoaOneShot convention). Failures
// journal memory_update{layer:"design", cause:"failed"} (curate precedent)
// so a long pass never dies silently.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/moa"
	"github.com/yingliang-zhang/odo/internal/store"
)

const (
	// designContextFileCap bounds one inlined context file (the
	// read_file tool's own cap — same byte economy).
	designContextFileCap = fsReadBytesCap
	// designContextTotalCap bounds the inlined context block across all
	// files; the tool loop stays available for everything beyond it.
	designContextTotalCap = 256 * 1024
)

// handleDesignMoa runs the Design-MoA pipeline for req.Goal. Project-scoped
// (the resolveProject guard) with the conversation naming the journal home
// for the design_lock row (the handleCurate precedent). M19: the pipeline
// itself is extracted as runDesignMoa (the /loop tasks design gate calls
// the same pass); this handler keeps the dark-launch gate, the one-pass
// liveness flag, and the design_lock journaling.
func (s *Server) handleDesignMoa(ctx context.Context, req Request) (Response, error) {
	// Fail-closed dark launch (the *_via convention): absent or any value
	// short of explicit "moa" refuses before any work. resolveVia logs
	// unknown values and maps them to omp — either way this errors.
	if resolveVia("design", "design_via") != viaMoa {
		return Response{}, fmt.Errorf("design_moa requires design_via: moa in prefs")
	}
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		return Response{}, fmt.Errorf("design_moa: goal is required")
	}
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("design_moa: %w", err)
	}
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}

	// One design pass at a time (M11 P0 curating precedent): the pass runs
	// unlocked, other connections stay responsive.
	s.mu.Lock()
	if s.designing {
		s.mu.Unlock()
		return Response{}, fmt.Errorf("design_moa: already in progress")
	}
	s.designing = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.designing = false
		s.mu.Unlock()
	}()

	// fail mirrors curate's: no design_lock row, but the pass leaves a
	// durable failed marker instead of dying toast-and-log only.
	fail := func(err error, detail string) (Response, error) {
		_, _ = s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "design",
			"cause":  "failed",
			"detail": detail,
			"goal":   goal,
		}))
		return Response{}, err
	}

	out, err := runDesignMoa(ctx, "design_moa", s.projectRoot, goal, req.ContextFiles, "")
	if err != nil {
		return fail(err, err.Error())
	}

	// Journal the lock plus every leg's metadata (verdict semantics: text,
	// marks, receipts — the proposals ARE the design analog of review
	// verdicts). Full proposal texts ride the row: the design_lock is
	// falsifiable against exactly what the consolidator saw.
	payload := map[string]interface{}{
		"action":       "design_lock",
		"goal":         goal,
		"goal_sha16":   sha16([]byte(goal)),
		"design_lock":  out.lock,
		"design_sha16": sha16([]byte(out.lock)),
		"proposals":    out.proposals,
		"consolidator": out.consolidator,
	}
	if len(req.ContextFiles) > 0 {
		payload["context_files"] = req.ContextFiles
	}
	if out.droppedLegs > 0 {
		payload["dropped_legs"] = out.droppedLegs
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(payload)); err != nil {
		return Response{}, err
	}
	return Response{DesignLock: out.lock, DesignProposals: out.proposals}, nil
}

// designMoaOutcome is one runDesignMoa pass's products: the DESIGN LOCK
// text, every leg's receipt (failed legs included — their text is dropped,
// never consolidator input), the consolidator's wire receipt block, and
// the dropped-leg count.
type designMoaOutcome struct {
	lock         string
	proposals    []DesignProposal
	consolidator map[string]interface{}
	droppedLegs  int
}

// runDesignMoa runs the Design-MoA pipeline verbatim (M19 extraction from
// handleDesignMoa): blind-sealed proposal legs — one per prefs review:
// model, each an independent QueryWithTools loop over read-only
// repo-root-scoped tools — then ONE consolidator moa.Query synthesizing
// the surviving proposals. opName prefixes error text ("design_moa" from
// the IPC handler, "loop_design" from the /loop tasks gate);
// consolidatorModel "" resolves to the prefs orchestrator: line. Strict
// truncation: partial leg text never feeds the consolidator, and a
// consolidator truncation fails the whole pass (the fail-closed
// convention). Journaling stays with the caller.
func runDesignMoa(ctx context.Context, opName, root, goal string, contextFiles []string, consolidatorModel string) (designMoaOutcome, error) {
	models := parseReviewModels(adapter.LoadPrefsRaw("review"))
	if len(models) == 0 {
		return designMoaOutcome{}, errors.New("No review models configured for " + opName + ". Set the 'review:' line in prefs.md.")
	}
	// Context files are resolved against the bound repo root; an escape is
	// a caller error (refused), an unreadable in-root file degrades to an
	// inline note (the tools can still answer over the rest).
	ctxBlock, err := designContextBlock(root, contextFiles)
	if err != nil {
		return designMoaOutcome{}, fmt.Errorf("%s: %w", opName, err)
	}

	// Blind legs: independent QueryWithTools loops, repo-root scope. Same
	// prompt, same tools, no cross-leg visibility — the seal holds because
	// the executor exposes only reads and each leg builds its own message
	// chain.
	client := moa.NewClientFromEnv("", "")
	exec := newFSToolExecutorRooted(root)
	tools := moaFSTools()
	legSystem := "You are an expert design reviewer producing one independent, self-contained design proposal." +
		"\n\nYou have read-only tools over the project repository: read_file, grep, glob. " +
		exec.describeScope() +
		" Ground the proposal in the actual code and documents — do not ask the user to paste content. Every read is journaled." +
		"\n\nBlind-sealed discipline: other reviewers are producing their own proposals in parallel; they will not see yours and you must not defer to them. Write a proposal that stands alone."
	legPrompt := designLegPrompt(goal, ctxBlock)

	proposals := make([]DesignProposal, len(models))
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func() {
			defer wg.Done()
			proposals[i] = designLeg(ctx, client, m, legSystem, legPrompt, tools, exec)
		}()
	}
	wg.Wait()

	// Strict truncation + degrade: failed legs (error or truncation) are
	// excluded from the consolidator's plate. The pass dies only when
	// nothing survived.
	var good []DesignProposal
	for _, p := range proposals {
		if p.Error == "" {
			good = append(good, p)
		}
	}
	if len(good) == 0 {
		return designMoaOutcome{}, fmt.Errorf("%s: every proposal leg failed (%d legs)", opName, len(proposals))
	}

	// Consolidator: ONE moa.Query on the orchestrator model — a synthesis,
	// not a vote. Deadline policy is the R-W2/R-W3 one: one worst-case moa
	// attempt chain at the model's hard cap.
	if consolidatorModel == "" {
		consolidatorModel = adapter.ReadSettings().OrchestratorModel
	}
	cctx, cancel := context.WithTimeout(ctx, moa.TimeoutForModel(consolidatorModel))
	consolidated, cerr := client.Query(cctx, consolidatorModel, designConsolidatorSystem, designConsolidatorPrompt(goal, proposals))
	cancel()
	if cerr != nil {
		return designMoaOutcome{}, fmt.Errorf("%s: consolidator: %w", opName, cerr)
	}
	if consolidated.Truncated {
		return designMoaOutcome{}, fmt.Errorf("%s: consolidator %s truncated at the %d-token hard cap after %d escalation(s); no design lock", opName, consolidatorModel, consolidated.Budget, len(consolidated.Escalations))
	}

	return designMoaOutcome{
		lock:      consolidated.Text,
		proposals: proposals,
		consolidator: map[string]interface{}{
			"model":         consolidatorModel,
			"request_sha16": consolidated.RequestSHA16,
			"request_bytes": consolidated.RequestBytes,
			"budget":        consolidated.Budget,
			"output_tokens": consolidated.OutputTokens,
			"escalations":   consolidated.Escalations,
		},
		droppedLegs: len(proposals) - len(good),
	}, nil
}

// designLeg runs one blind proposal leg and folds the outcome into a
// DesignProposal — including the degraded marks (reviewWithModel's
// degrade-never-die precedence: a failed leg is metadata, not a pipeline
// abort). A truncated final answer is a failed leg: the partial is dropped
// (never consolidator input) and Truncated marks the cause.
func designLeg(ctx context.Context, client *moa.Client, m reviewModel, system, prompt string, tools []moa.Tool, exec *fsToolExecutor) DesignProposal {
	label := m.model + "@" + m.provider
	lctx, cancel := context.WithTimeout(ctx, moa.TimeoutForModel(m.model))
	defer cancel()
	res, calls, err := client.QueryWithTools(lctx, m.model, system, prompt, tools, exec.Execute, 0) // 0 → the client's 16-round cap
	if err != nil {
		return DesignProposal{Model: label, Error: err.Error(), ToolCalls: calls}
	}
	p := DesignProposal{
		Model:        label,
		ToolCalls:    calls,
		Budget:       res.Budget,
		OutputTokens: res.OutputTokens,
		Escalations:  res.Escalations,
		RequestSHA16: res.RequestSHA16,
		RequestBytes: res.RequestBytes,
	}
	if res.Truncated {
		p.Truncated = true
		p.Error = fmt.Sprintf("proposal truncated at the %d-token hard cap after %d escalation(s); excluded from consolidation", res.Budget, len(res.Escalations))
		return p
	}
	p.Text = res.Text
	return p
}

// designContextBlock renders req.ContextFiles as fenced inline content,
// each capped at designContextFileCap with a line-boundary cut. An
// in-root-but-unreadable file degrades to an inline note; a path escaping
// the repo root is an error (the fail-closed half of the fs executor's
// containment rule, applied daemon-side).
func designContextBlock(root string, files []string) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("# Context files\n")
	total := 0
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		abs := filepath.Clean(filepath.Join(root, f))
		if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return "", fmt.Errorf("context file %q escapes the project root", f)
		}
		content, err := os.ReadFile(abs)
		b.WriteString("\n## ")
		b.WriteString(f)
		b.WriteString("\n\n")
		if err != nil {
			b.WriteString("(unreadable: ")
			b.WriteString(err.Error())
			b.WriteString(")\n")
			continue
		}
		if total >= designContextTotalCap {
			b.WriteString("(omitted: context budget exhausted — read it via the tools)\n")
			continue
		}
		cut := designContextFileCap
		if rem := designContextTotalCap - total; rem < cut {
			cut = rem
		}
		text := capAtLineBoundary(string(content), cut)
		total += len(text)
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// designLegPrompt assembles the identical prompt every leg sees.
func designLegPrompt(goal, ctxBlock string) string {
	var b strings.Builder
	b.WriteString("# Goal\n\n")
	b.WriteString(goal)
	b.WriteString("\n\n")
	if ctxBlock != "" {
		b.WriteString(ctxBlock)
		b.WriteString("\n")
	}
	b.WriteString("Write one self-contained design proposal for the goal above: the problem framing, the decision and why, the risks, and the concrete plan. Ground claims in the repository via the tools where relevant.")
	return b.String()
}

// designConsolidatorSystem fixes the consolidator's role: a synthesizer,
// not a fourth opinion (the consensusVerdict "no 4th model call" rule —
// the call exists here to MERGE, not to out-vote the legs).
var designConsolidatorSystem = "You are a design consolidator. You receive blind-sealed design proposals from independent reviewers for one goal. " +
	"Synthesize them into a single DESIGN LOCK document: keep every convergent decision, arbitrate contradictions explicitly (name the losing option and why it loses), list residual risks, and end with the concrete implementation plan. " +
	"Do not add a new direction of your own — your authority is arbitration over the proposals, not invention."

// designConsolidatorPrompt plates the surviving proposals with stable leg
// labels (A/B/C in prefs order) and names the dropped legs, so the lock is
// arbitrating over a declared plate — a leg that silently vanished would
// be an invisible vote.
func designConsolidatorPrompt(goal string, proposals []DesignProposal) string {
	var b strings.Builder
	b.WriteString("# Goal\n\n")
	b.WriteString(goal)
	b.WriteString("\n\n# Blind design proposals\n")
	label := byte('A')
	for _, p := range proposals {
		if p.Error != "" {
			continue
		}
		fmt.Fprintf(&b, "\n## Leg %c (%s)\n\n%s\n", label, p.Model, p.Text)
		label++
	}
	dropped := 0
	for _, p := range proposals {
		if p.Error != "" {
			if dropped == 0 {
				b.WriteString("\n# Dropped legs (failed or truncated — excluded from consolidation)\n")
			}
			dropped++
			fmt.Fprintf(&b, "- %s: %s\n", p.Model, p.Error)
		}
	}
	b.WriteString("\nProduce the DESIGN LOCK now.\n")
	return b.String()
}
