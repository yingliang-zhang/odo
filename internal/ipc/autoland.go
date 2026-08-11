package ipc

// M16 (O-1 v2): daemon-side auto-land — a pending diff lands without a
// human click only after surviving every layer below, in order:
//
//	pref gate       auto_apply == "main" (off = silent; the pref's
//	                fail-closed parse in settings.go unchanged)
//	run posture     the producing run must have finished cleanly
//	mechanical      protected paths (.odo/, wiki/), supply-chain files
//	gates            (manifests/lockfiles/.odo-verify), new top-level
//	                 directory (vs the diff's base tree), weakened tests
//	                 (net *_test.go assertion loss). Deterministic, zero
//	                 panel spend. Size gates are GONE (panel verdict
//	                 DROP_SIZE_KEEP_DIR: a 300-line cliff is fake
//	                 precision with 350K-token contexts; the token cost
//	                 breaker below is the ceiling).
//	verify gate     the repo-root `.odo-verify` command re-runs at the
//	                 run's worktree root. Absent/failed = blocked, always.
//	cost breaker    assembled prompt estimate (chars/4) > 87K tokens
//	                 (~25% of the smallest panel context) = blocked.
//	panel           the prefs.md `review:` models fan out on a GROUNDED
//	                 prompt: the journaled trigger text (never the agent's
//	                 self-report), the verify output, and an adversarial
//	                 instruction (three concrete failure scenarios first,
//	                 verdict last).
//	unanimity       consensusVerdict == "accept" requires EVERY reviewer
//	                 (the fail-open fix: a lone needs_fixes now blocks).
//	land            handleDiffAction's original path — protected-path
//	                 guard, unmerged-index refusal, 3-way apply, path-
//	                 scoped staged commit, worktree retire — plus
//	                 actor:"auto_panel" on the journaled review_action.
//
// Every decision journals a review_action (append-only audit):
//
//	moa_review{actor:"auto_panel"}        unanimous panel verdict
//	accept{actor:"auto_panel"}            auto-landed (streak-excluded:
//	                                      ComputeAutonomy counts these
//	                                      separately, never toward rungs)
//	auto_land_blocked{reason,...}         any gate/panel stop, with the
//	                                      panel verdicts attached when the
//	                                      panel ran (panel_disagreed)
//
// auto_apply values "branch"/"all" stay unconsumed (rung-0 contract:
// only "main" has pipeline semantics). M16 amends the M15 O-1
// no-auto-apply deferral for DIFF LANDING ONLY (m16-auto-land.md +
// README row/A1): skill proposals keep auto_accept deferred, and every
// land remains reversible (git).
import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
)

const (
	// autoActor marks every journaled review_action row the pipeline
	// produces. ComputeAutonomy excludes it from human streaks.
	autoActor = "auto_panel"

	// Cost breaker, replacing every line-count gate: the assembled review
	// prompt must stay under ~25% of the smallest panel context (350K).
	// ~4 chars per token on code+prose.
	autoLandMaxPromptTokens = 87_000

	// The verify gate's prompt budget: only the tail of the command's
	// combined output rides the prompt (diagnostics sit at the end).
	autoLandVerifyTailBytes = 8 * 1024

	// autoLandVerifyTimeout caps one verify run. The daemon's own suite
	// (~2 min) fits comfortably; a hanging build must not wedge the
	// serialization mutex.
	autoLandVerifyTimeout = 10 * time.Minute

	// verifyCmdFile names the per-project verification command file at the
	// repo root. Committed, so a run's worktree carries it too; a diff
	// touching it trips the supply-chain gate (self-modification).
	verifyCmdFile = ".odo-verify"
)

// autoLandSupplyChainFiles are basenames (case-insensitive) a diff may
// never auto-land: dependency manifests and lockfiles are single-line
// supply-chain RCE vectors that diff review structurally cannot audit
// (kimi's panel catch), plus the auto-land pipeline's own config file.
var autoLandSupplyChainFiles = map[string]bool{
	verifyCmdFile:       true,
	"go.mod":            true,
	"go.sum":            true,
	"package.json":      true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"cargo.toml":        true,
	"cargo.lock":        true,
	"pyproject.toml":    true,
	"poetry.lock":       true,
	"pipfile.lock":      true,
	"requirements.txt":  true,
	"gemfile":           true,
	"gemfile.lock":      true,
	"composer.json":     true,
	"composer.lock":     true,
}

// maybeAutoLand is drainRun's spawn point (goroutine, no locks held —
// the continuation trigger's shape). Everything the pipeline needs is
// copied off the run's runMeta by the caller. Pref-off returns SILENTLY:
// a disabled feature deserves no journal noise (unlike a blocked attempt,
// which is evidence).
func (s *Server) maybeAutoLand(d store.Diff, worktreePath, goal string, runErrored bool) {
	if adapter.ReadSettings().AutoApply != "main" {
		return
	}
	// One auto-land pipeline at a time per daemon: the final accept's
	// apply/add/commit must never interleave with another auto accept,
	// and a later diff re-evaluates against the newer HEAD.
	s.autoLandMu.Lock()
	defer s.autoLandMu.Unlock()
	if s.autoLandDone != nil { // test signal; nil in production
		defer func() {
			select {
			case s.autoLandDone <- struct{}{}:
			default:
			}
		}()
	}
	s.autoLand(context.Background(), d, worktreePath, goal, runErrored)
}

// autoLand runs the full pipeline for one pending diff. Blocks journal and
// return; only a unanimous panel after all gates calls handleDiffAction.
func (s *Server) autoLand(ctx context.Context, d store.Diff, worktreePath, goal string, runErrored bool) {
	if runErrored {
		s.journalAutoLandBlocked(ctx, d, "run_errored", "the producing run ended with agent_error", nil, "")
		return
	}
	data, err := os.ReadFile(d.PathOnDisk)
	if err != nil {
		s.journalAutoLandBlocked(ctx, d, "unparseable_diff", err.Error(), nil, "")
		return
	}
	diffText := string(data)

	if reason, detail := s.autoLandCheck(d); reason != "" {
		s.journalAutoLandBlocked(ctx, d, reason, detail, nil, "")
		return
	}

	verifyCmd, err := verifyCommand(worktreePath)
	if err != nil {
		s.journalAutoLandBlocked(ctx, d, "verify_unconfigured",
			"no usable "+verifyCmdFile+" at the repo root — the verify gate is mandatory for auto-land", nil, "")
		return
	}
	verifyTail, err := runVerify(ctx, worktreePath, verifyCmd)
	if err != nil {
		s.journalAutoLandBlocked(ctx, d, "verify_failed",
			capDetail(verifyCmd+" → "+err.Error()+"\n"+verifyTail), nil, "")
		return
	}

	prompt := autoLandPrompt(goal, diffText, verifyCmd, verifyTail)
	if est := len(prompt) / 4; est > autoLandMaxPromptTokens {
		s.journalAutoLandBlocked(ctx, d, "prompt_too_large",
			"prompt estimate "+strconv.Itoa(est)+" tokens > cap "+strconv.Itoa(autoLandMaxPromptTokens), nil, "")
		return
	}

	models := parseReviewModels(adapter.LoadPrefsRaw("review"))
	if len(models) == 0 {
		s.journalAutoLandBlocked(ctx, d, "no_review_models",
			"prefs.md has no review: line — auto-land requires the panel", nil, "")
		return
	}
	reviews := s.reviewFanout(ctx, models, prompt)
	cv := consensusVerdict(reviews)
	if cv != "accept" {
		s.journalAutoLandBlocked(ctx, d, "panel_disagreed", "", reviews, cv)
		return
	}

	// Evidence before action: the unanimous verdict must be on the journal
	// BEFORE the diff lands. A broken journal means no landing — an
	// unrecorded auto-accept is the one thing worse than none.
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":            "moa_review",
		"diff_id":           d.ID,
		"actor":             autoActor,
		"reviews":           reviews,
		"consensus_verdict": cv,
	})); err != nil {
		log.Printf("auto-land: journal panel verdict for diff %d: %v (NOT landing)", d.ID, err)
		return
	}
	if _, err := s.handleDiffAction(ctx, d.ID, "accept", autoActor); err != nil {
		// A human raced the panel (already accepted/rejected), or the
		// executor refused (protected path, conflicted index, apply
		// failure): the diff stays pending for the human either way.
		log.Printf("auto-land: accept diff %d: %v", d.ID, err)
	}
}

// autoLandCheck applies the deterministic pre-panel gates, cheapest first.
// A non-empty reason blocks. The gates are deliberately mechanical: each
// names exactly the artifact that made the call (audit-grade details, zero
// heuristics a prompt could talk its way around).
func (s *Server) autoLandCheck(d store.Diff) (reason, detail string) {
	paths, err := git.PatchPaths(d.PathOnDisk)
	if err != nil {
		return "unparseable_diff", err.Error()
	}
	// Double-layer with the executor (handleDiffAction re-checks): the
	// pre-panel check saves the panel spend and journals the clearer reason.
	for _, p := range paths {
		if isProtectedPath(p) {
			return "protected_path", p
		}
	}
	for _, p := range paths {
		base := strings.ToLower(p[strings.LastIndex(p, "/")+1:])
		if autoLandSupplyChainFiles[base] {
			return "supply_chain_path", p
		}
	}
	stat, err := git.PatchStats(d.PathOnDisk)
	if err != nil {
		return "unparseable_diff", err.Error()
	}
	base := ""
	if d.BaseSHA != nil {
		base = *d.BaseSHA
	}
	if base == "" {
		return "base_unresolvable", "diff has no base_sha — the new-top-dir gate cannot run"
	}
	tree, err := GitTopDirsResolver(s.projectRoot)(base)
	if err != nil {
		return "base_unresolvable", err.Error()
	}
	for _, f := range stat.Files {
		if f.DeletedFile {
			continue
		}
		p := strings.ReplaceAll(f.Path, "\\", "/")
		if slash := strings.Index(p, "/"); slash > 0 && !tree[p[:slash]] {
			return "new_top_dir", p[:slash] + "/ (new top-level directory)"
		}
	}
	added, removed, err := git.TestAssertionDelta(d.PathOnDisk)
	if err != nil {
		return "unparseable_diff", err.Error()
	}
	if removed > added {
		return "test_assertions_decreased",
			fmt.Sprintf("*_test.go assertions: +%d added / -%d removed (net loss)", added, removed)
	}
	return "", ""
}

// verifyCommand reads the worktree's .odo-verify: the first non-empty,
// non-# line is the shell command the verify gate runs at the worktree
// root. Absent or contentless means the gate cannot run (blocked,
// fail-closed).
func verifyCommand(worktreePath string) (string, error) {
	data, err := os.ReadFile(worktreePath + string(os.PathSeparator) + verifyCmdFile)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return line, nil
		}
	}
	return "", fmt.Errorf("%s has no command line", verifyCmdFile)
}

// runVerify executes cmd via sh -c at the worktree root under a hard
// timeout, returning the trailing autoLandVerifyTailBytes of combined
// output either way (the prompt tail on pass, the journal detail on fail).
// The command comes from the worktree's own committed .odo-verify — never
// from the diff under review (the supply-chain gate blocks that), so this
// runs no content the panel is judging.
func runVerify(ctx context.Context, worktreePath, cmd string) (string, error) {
	vctx, cancel := context.WithTimeout(ctx, autoLandVerifyTimeout)
	defer cancel()
	proc := exec.CommandContext(vctx, "sh", "-c", cmd)
	proc.Dir = worktreePath
	out, err := proc.CombinedOutput()
	if len(out) > autoLandVerifyTailBytes {
		out = out[len(out)-autoLandVerifyTailBytes:]
	}
	if vctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("verify timed out after %s", autoLandVerifyTimeout)
	}
	return string(out), err
}

// autoLandPrompt assembles the grounded review input (panel-top-2
// controls): the user's verbatim trigger text — never the agent's
// self-report — the verify receipt, and the adversarial instruction
// (glm's zero-cost control). The verdict must come LAST: parseVerdict
// takes the first verdict-token line, and an analysis-ending verdict keeps
// accidental token matches fail-closed.
func autoLandPrompt(goal, diffText, verifyCmd, verifyTail string) string {
	var b strings.Builder
	b.WriteString("An unattended gate will land the following diff WITHOUT human review if and only if every reviewer accepts. Judge it strictly.\n\n")
	if goal != "" {
		b.WriteString("The user's original instruction (the objective this diff claims to satisfy), verbatim:\n\"\"\"\n")
		b.WriteString(goal)
		b.WriteString("\n\"\"\"\n\n")
	}
	b.WriteString("Mechanical verification already ran at the author's worktree root (`")
	b.WriteString(verifyCmd)
	b.WriteString("` → exit 0), output tail:\n```\n")
	b.WriteString(verifyTail)
	b.WriteString("\n```\n\n")
	b.WriteString("Before any verdict, list three concrete ways this diff could plausibly be wrong — e.g. a mid-file semantic inversion, a test weakened so it no longer proves the behavior, or a caller the diff forgot to migrate. Then, on the final line, output exactly one verdict token: ACCEPT, REJECT, or NEEDS_FIXES.\n\n")
	b.WriteString(diffText)
	return b.String()
}

// journalAutoLandBlocked records one blocked auto-land attempt. reviews
// (attached when the panel ran) keep the dissent on the record.
func (s *Server) journalAutoLandBlocked(ctx context.Context, d store.Diff, reason, detail string, reviews []ReviewResult, consensus string) {
	payload := map[string]interface{}{
		"action":  "auto_land_blocked",
		"diff_id": d.ID,
		"actor":   autoActor,
		"reason":  reason,
	}
	if detail != "" {
		payload["detail"] = detail
	}
	if len(reviews) > 0 {
		payload["reviews"] = reviews
		payload["consensus_verdict"] = consensus
	}
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(payload)); err != nil {
		log.Printf("auto-land: journal blocked (%s) for diff %d: %v", reason, d.ID, err)
	}
}

// capDetail trims a journal detail to a reviewable size.
func capDetail(s string) string {
	const maxDetail = 4 * 1024
	if len(s) > maxDetail {
		return s[:maxDetail] + "\n…[truncated]"
	}
	return s
}

// reviewFanout sends prompt to every model in parallel, collecting
// position-stable verdicts (reviewWithModel degrades failures to
// needs_fixes — never an accidental accept). Shared by review_diff and
// the auto-land pipeline.
func (s *Server) reviewFanout(ctx context.Context, models []reviewModel, prompt string) []ReviewResult {
	reviews := make([]ReviewResult, len(models))
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reviews[i] = s.reviewWithModel(ctx, m, prompt)
		}()
	}
	wg.Wait()
	return reviews
}
