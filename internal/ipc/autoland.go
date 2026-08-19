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
//	base freshness  P0a refresh: entry drift triggers a --3way rebase
//	                 probe in a throwaway worktree (ProbeApplyClean —
//	                 the main checkout is never touched, acceptMu never
//	                 taken). Clean = the diff's base pointer moves to
//	                 current HEAD (UpdateDiffBaseSHA +
//	                 refresh_attempted{clean, pre_spend_probe}) and the
//	                 pipeline proceeds to verify+panel attesting the
//	                 tree the land now targets; conflict/error =
//	                 refresh_attempted{...} then blocked base_stale,
//	                 diff pending. The AUTHORITATIVE check is still
//	                 final, inside handleDiffAction's accept branch
//	                 under acceptMu (checkAndRefreshBase — zero
//	                 check-to-apply window): drift past it attempts the
//	                 same rebase against the real checkout, and a failed
//	                 one journals base_stale_at_land via the caller's
//	                 errors.Is(errBaseStale) branch with the completed
//	                 panel riding the blocked row.
//	verify gate     the repo-root `.odo-verify` command re-runs at the
//	                 run's worktree root with an allowlisted child
//	                 environment (verifyEnviron) — the gate executes the
//	                 agent's unreviewed code, and the daemon's keys must
//	                 never be visible to it (panel P0). Absent/failed =
//	                 blocked, always.
//	verify          M18 batch B: an exit-0 verify whose output tail
//	evidence         carries ZERO test evidence (no PASS token, no go
//	                 "ok" line, no non-zero N-passed count — the
//	                 conservative whitelist in review.go) never counts
//	                 as verified: {verify_no_evidence}, diff pending.
//	                 A build-only .odo-verify can never satisfy this —
//	                 a wrong-path verify used to give false release
//	                 confidence (M16 semantics).
//	cost breaker    assembled prompt estimate (chars/4) > 87K tokens
//	                 (~25% of the smallest panel context) = blocked.
//	panel           the prefs.md `review:` models fan out on the SHARED
//	                 grounded prompt (M18 batch B: buildReviewPrompt,
//	                 review.go — the same builder manual review_diff
//	                 uses): the journaled/original goal verbatim (never
//	                 the agent's self-report), a mechanical facts block
//	                 (files, +- counts, protected-path hits, verify
//	                 outcome, run_verdict tallies where available), the
//	                 verify output tail, and the adversarial instruction
//	                 (three concrete failure scenarios first, verdict
//	                 last). Truncated reviews count as needs_fixes;
//	                 parseVerdict honors only the FINAL verdict line
//	                 (panel P1: truncated/early-ACCEPT bypass), and the
//	                 diff body is fenced as data. Non-accept legs
//	                 journal thinking_md (≤4KB) and every leg journals
//	                 base_url (scrubbed) — the verdict contract itself
//	                 is byte-identical.
//	unanimity       consensusVerdict == "accept" requires EVERY reviewer
//	                 (the fail-open fix: a lone needs_fixes now blocks).
//	visual class    REMOVED — GUI diffs land through the same unanimous-panel pipeline as daemon diffs; the panel verdict is sufficient.
//	settlement      M18 (settle.go): the four panel outcomes split —
//	                 accept lands; unanimous reject blocks
//	                 {panel_unanimous_reject} + transcript advisory (the
//	                 diff stays pending — never auto-rejected); any
//	                 reject at all blocks {panel_mixed}; an errored leg
//	                 blocks {panel_infra} (infra is not a verdict); and
//	                 zero rejects + ≥1 needs_fixes enters the auto-revise
//	                 ladder (≤2 fresh repair rounds, no-progress stop,
//	                 journal-derived suspension).
//	land            handleDiffAction's original path — protected-path
//	                 guard, unmerged-index refusal, the FINAL base-
//	                 freshness adjudication (checkAndRefreshBase: a
//	                 clean refresh re-applies onto current HEAD, a
//	                 failed one wraps errBaseStale → base_stale_at_land
//	                 below), 3-way apply, path-scoped staged commit,
//	                 worktree retire — plus actor:"auto_panel" on the
//	                 journaled review_action.
//
// Every decision journals a review_action (append-only audit):
//
//	auto_land_started{stage:verify|panel} liveness breadcrumb journaled
//	                                      immediately before each silent
//	                                      stage (the verify gate and the
//	                                      panel fan-out) — the GUI pipeline
//	                                      chip derives "running" from these
//	                                      instead of holding "queued" through
//	                                      the multi-minute silent window
//	                                      (pipeline-indicator-lock Phase 2)
//	moa_review{actor:"auto_panel"}        unanimous panel verdict
//	accept{actor:"auto_panel"}            auto-landed (streak-excluded:
//	                                      ComputeAutonomy counts these
//	                                      separately, never toward rungs)
//	refresh_attempted{clean|conflict|error} a stale-base rebase attempt
//	                                      (P0a): phase pre_spend_probe =
//	                                      the entry probe (actor auto_panel),
//	                                      accept_apply = the final under-
//	                                      mutex rebase; always preceding the
//	                                      accept/blocked row it feeds
//	auto_revise_round{actor:"auto_panel"} a spawned repair round (M18)
//	auto_land_blocked{reason,...}         any gate/panel stop, with the
//	                                      panel verdicts attached when the
//	                                      panel ran, and the diff's
//	                                      patch sha16 on every row
//	memory_update{layer:"auto_land"}      ladder_suspended/resumed (M18)
//
// fix-INT W5: every review_action row above EXCEPT auto_land_started
// (blocked, auto moa_review, accept, revise round) additionally carries
// the Guardian risk receipt — a started row is a liveness breadcrumb,
// not an outcome: nothing resolved, so nothing is rated (ComputeAutonomy's
// risk switch skips it, and it must never inflate Unrated). The receipt
// — risk_class ([]string, severity-ranked, ["none"] when rated clean),
// risk_classifier ("mechanical"), and risk_evidence (one trigger
// artifact per class, omitted when clean) — classified from the patch
// bytes by risk.go; all three keys omitted when the patch is
// unreadable (patch_sha16 precedent).
//
// auto_apply values "branch"/"all" stay unconsumed (rung-0 contract:
// only "main" has pipeline semantics). M16 amends the M15 O-1
// no-auto-apply deferral for DIFF LANDING ONLY (m16-auto-land.md +
// README row/A1): skill proposals keep auto_accept deferred, and every
// land remains reversible (git).
import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

	// AutoActor is autoActor exported for cross-package consumers that
	// must split human from pipeline outcomes (cmd_skills_audit.go's M17
	// F5 actor filter) without reaching into ipc internals.
	AutoActor = autoActor

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
func (s *Server) maybeAutoLand(d store.Diff, worktreePath, goal string, runErrored bool, runVerdict string) {
	if adapter.ReadSettings().AutoApply != "main" {
		return
	}
	// Pipelines run concurrently; the final handleDiffAction accept is
	// serialized by acceptMu (the sole main-checkout mutex) — a racing
	// pipeline re-adjudicates freshness there and refreshes or blocks
	// (base_stale_at_land), so concurrent verdicts never double-apply.
	if s.autoLandDone != nil { // test signal; nil in production
		defer func() {
			select {
			case s.autoLandDone <- struct{}{}:
			default:
			}
		}()
	}
	s.autoLand(context.Background(), d, worktreePath, goal, runErrored, runVerdict)
}

// autoLand runs the full pipeline for one pending diff. Blocks journal and
// return; only a unanimous panel after all gates calls handleDiffAction.
func (s *Server) autoLand(ctx context.Context, d store.Diff, worktreePath, goal string, runErrored bool, runVerdict string) {
	if runErrored {
		s.journalAutoLandBlocked(ctx, d, "run_errored", "the producing run ended with agent_error", nil, "")
		return
	}
	// run_verdict gate (epoch-8, outstanding #1): a tainted run is blocked
	// before ANY other spend — "有 diff 零 text" means the tool side effects
	// are real but the answer/summary never made it back, so there is no
	// self-report-free confidence a panel verdict could stand on. The diff
	// stays pending for the human (conservative, same posture as
	// base_stale). false_stop here is the belt-and-suspenders case: it
	// implies zero tool calls, but a phantom diff still never auto-lands.
	if runVerdict != "" {
		s.journalAutoLandBlocked(ctx, d, "run_"+runVerdict, "the producing run's verdict is "+runVerdict+" (no reliable output)", nil, "")
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

	// The verify below attests the run's worktree (diff base + diff), but
	// the land applies onto the main checkout's CURRENT HEAD. If HEAD has
	// drifted since the base was cut, verify would attest a tree nobody
	// lands — so entry drift is adjudicated before any verify/panel spend.
	// P0a: drift no longer blocks outright. Probe the rebase in a throwaway
	// worktree (ProbeApplyClean: the main checkout is never touched and
	// acceptMu is never taken — human accepts stay unblocked mid-probe).
	// A clean probe means current_HEAD+diff merges by itself: move the
	// diff's base pointer and proceed to verify+panel attesting the tree
	// the land now targets. Anything else keeps the conservative posture:
	// refresh_attempted journaled first, then blocked base_stale with the
	// diff pending. ENTRY filter only: drift arriving mid-pipeline is
	// caught by the FINAL check inside handleDiffAction
	// (checkAndRefreshBase, sentinel errBaseStale) and journals
	// base_stale_at_land with the completed panel attached.
	base := ""
	if d.BaseSHA != nil {
		base = *d.BaseSHA
	}
	head, err := git.CurrentSHA(s.projectRoot)
	if err != nil {
		s.journalAutoLandBlocked(ctx, d, "base_stale", "cannot read main checkout HEAD: "+err.Error(), nil, "")
		return
	}
	if head != base {
		clean, probeDetail, perr := git.ProbeApplyClean(s.projectRoot, d.PathOnDisk)
		if perr != nil || !clean {
			outcome := "error"
			refreshErr := perr
			if perr == nil {
				outcome = "conflict"
				refreshErr = errors.New(probeDetail)
			}
			s.journalRefreshAttempt(ctx, d, "pre_spend_probe", outcome, base, head, refreshErr)
			reasonDetail := fmt.Sprintf("main HEAD %s drifted from diff base %s — refresh probe: %s", head, base, outcome)
			if probeDetail != "" {
				reasonDetail += ": " + capDetail(probeDetail)
			} else if perr != nil {
				reasonDetail += ": " + perr.Error()
			}
			s.journalAutoLandBlocked(ctx, d, "base_stale", reasonDetail, nil, "")
			return
		}
		// Clean rebase available: move the diff's base pointer to the tree
		// verify/panel are about to attest, journal the refresh, and fall
		// through. The FINAL gate's checkAndRefreshBase re-reads the store
		// and re-adjudicates (HEAD moving again after this point refreshes
		// once more there or refuses — at most one attempt per gate, never
		// a loop).
		if uerr := s.store.UpdateDiffBaseSHA(ctx, d.ID, head); uerr != nil {
			// Fail closed: the probe touched nothing, but a store that
			// can't move the base pointer can't be trusted to carry the
			// rest of the pipeline's journal either.
			s.journalAutoLandBlocked(ctx, d, "base_stale",
				"clean refresh probe (base "+base+" → "+head+") but recording the new base failed: "+uerr.Error(), nil, "")
			return
		}
		if d.BaseSHA != nil {
			*d.BaseSHA = head
		}
		s.journalRefreshAttempt(ctx, d, "pre_spend_probe", "clean", base, head, nil)
	}

	// Liveness breadcrumb (indicator lock Phase 2): every FREE gate has
	// passed; the next state change is minutes of silent verify or a
	// blocked row. Journal the stage entry so the pipeline chip can say
	// "running" instead of "queued" through the silent window.
	s.journalAutoLandStarted(ctx, d, "verify", data)

	// Path-scoped verify: pass diff paths so GUI-only diffs can use a
	// lighter verify command (tsc + playwright instead of go test).
	// M19: the gate itself is extracted as runVerifyGate (the /loop
	// Mode A fix pipeline calls it verbatim).
	verifyPaths, _ := git.PatchPaths(d.PathOnDisk)
	gate := runVerifyGate(ctx, s.projectRoot, worktreePath, verifyPaths)
	if !gate.ok {
		s.journalAutoLandBlocked(ctx, d, gate.reason, gate.detail, nil, "")
		return
	}
	verifyCmd, verifyTail := gate.cmd, gate.tail

	prompt := buildReviewPrompt(reviewPromptInput{
		mode:       reviewPromptGate,
		goal:       goal,
		diffPath:   d.PathOnDisk,
		diffText:   diffText,
		verifyCmd:  verifyCmd,
		verifyTail: verifyTail,
		verifyNote: "exit 0 (pass evidence present in the output tail)",
	})
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
	// Second breadcrumb: the verify is attested, the fan-out below is the
	// last silent stage (minutes under model latency).
	s.journalAutoLandStarted(ctx, d, "panel", data)
	reviews := s.reviewFanout(ctx, models, prompt)
	cv := consensusVerdict(reviews)
	// M18 settlement: an infra leg is not a verdict — the round never
	// validly completed (fail closed, and it never ticks the ladder).
	if panelInfraLeg(reviews) {
		s.journalAutoLandBlocked(ctx, d, "panel_infra",
			"a review leg failed on transport/auth/timeout — infra failures are not verdicts", reviews, cv)
		return
	}
	switch settlementClass(cv, reviews) {
	case "reject_unanimous":
		// Every reviewer rejected the DIRECTION: the diff stays pending
		// (a diff is the user's work product — never auto-rejected), and
		// the fall-through to the human is transcript-visible, not
		// ledger-only.
		s.journalAutoLandBlocked(ctx, d, "panel_unanimous_reject",
			"every reviewer rejected the direction; the diff stays pending for the human", reviews, cv)
		s.journalRunAdvisory(ctx, d.ConversationID, fmt.Sprintf(
			"the auto-land panel unanimously rejected diff #%d — it stays pending for your review (the reasons are in the journal).", d.ID))
		return
	case "reject_mixed":
		s.journalAutoLandBlocked(ctx, d, "panel_mixed",
			"at least one reviewer rejected the direction; the diff stays pending for the human", reviews, cv)
		return
	case "needs_fixes":
		// Zero rejects + ≥1 needs_fixes: nobody said the direction is
		// wrong, it's just not done — the auto-revise ladder decides
		// (spawn a repair round, block to the human, or demote). ladderMu
		// serializes the whole read-decide-spawn: two racing pipelines from
		// the same conversation must not fork the rounds chain.
		s.ladderMu.Lock()
		s.settleRevise(ctx, d, diffText, reviews)
		s.ladderMu.Unlock()
		return
	}
	// settlementClass "accept" falls through to the M16 landing path.

	// Evidence before action: the unanimous verdict must be on the journal
	// BEFORE the diff lands. A broken journal means no landing — an
	// unrecorded auto-accept is the one thing worse than none. patch_sha16
	// (M18 W2 item 4) attests the exact bytes the panel judged (data was
	// read above; an unreadable diff blocked as unparseable_diff already).
	moaPayload := map[string]interface{}{
		"action":            "moa_review",
		"diff_id":           d.ID,
		"actor":             autoActor,
		"reviews":           reviews,
		"consensus_verdict": cv,
		"patch_sha16":       sha16(data),
		// Tri-model right sidebar gap (read-only run/verify log): the
		// verify that attested this landing was previously ephemeral —
		// it rode only the review prompt. Journal it (capped like blocked
		// details) so the landed row carries its own run output.
		"verify_cmd":  verifyCmd,
		"verify_tail": capDetail(verifyTail),
	}
	// W5: the risk receipt for exactly the bytes the panel judged.
	mountRiskReceipt(moaPayload, riskReceiptKeys(diffText))
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(moaPayload)); err != nil {
		log.Printf("auto-land: journal panel verdict for diff %d: %v (NOT landing)", d.ID, err)
		return
	}
	if _, err := s.handleDiffAction(ctx, d.ID, "accept", autoActor, ""); err != nil {
		// Drift mid-pipeline (HEAD moved after the entry probe): the
		// FINAL gate's automatic refresh failed (conflict/error — its
		// refresh_attempted row already precedes this one). The completed
		// panel rides the blocked row as advisory evidence
		// (the blocked-row-as-evidence precedent) — the human re-runs or
		// rejects with the verdict on record.
		if errors.Is(err, errBaseStale) {
			s.journalAutoLandBlocked(ctx, d, "base_stale_at_land",
				err.Error()+" — the verify and panel attested the pre-drift tree; the diff stays pending for the human", reviews, cv)
		}
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

// verifyGateOutcome is runVerifyGate's result: on ok, the command that
// ran and its capped output tail; on failure, the autoLand blocked reason
// and detail (verbatim, so blocked-row text stays byte-identical to the
// pre-extraction shape).
type verifyGateOutcome struct {
	ok     bool
	cmd    string
	tail   string
	reason string
	detail string
}

// provisionVerifyDeps gives a GUI diff's verify command its node toolchain.
// Per-run worktrees carry tracked files only, so gui/node_modules is absent
// unless the producing run happened to create it — a GUI-scoped .odo-verify
// (`cd gui && npx tsc …`) then dies in seconds with npx's "not the tsc
// command" tail and the diff blocks verify_failed on an environment lie
// (diff #8, 2026-08-19). Runs already fix this by hand (symlink from the
// project checkout's install); the gate codifies the same step. Absent
// project install is NOT provisioned (npm ci is slow, networked, and its
// own supply chain) — the verify then fails with the real tail instead.
// Best-effort: a failed symlink surfaces as the honest verify error.
func provisionVerifyDeps(projectRoot, worktreePath string, diffPaths []string) {
	gui := false
	for _, p := range diffPaths {
		if strings.HasPrefix(p, "gui/") {
			gui = true
			break
		}
	}
	if !gui {
		return
	}
	dst := filepath.Join(worktreePath, "gui", "node_modules")
	if _, err := os.Lstat(dst); err == nil {
		return // present (real dir or an earlier link) — leave it alone
	}
	src := filepath.Join(projectRoot, "gui", "node_modules")
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return
	}
	_ = os.Symlink(src, dst)
}

// runVerifyGate runs the verify gate verbatim (M19 extraction from
// autoLand — the /loop Mode A fix pipeline calls the same gate):
// command resolution → execution under the allowlisted environment →
// the M18 pass-evidence rule. Unconfigured, failed, and evidence-less
// outcomes are failures with exactly the reasons/details autoLand
// journaled inline before the extraction.
func runVerifyGate(ctx context.Context, projectRoot, worktreePath string, diffPaths []string) verifyGateOutcome {
	provisionVerifyDeps(projectRoot, worktreePath, diffPaths)
	verifyCmds, err := verifyCommands(worktreePath, diffPaths)
	if err != nil {
		return verifyGateOutcome{reason: "verify_unconfigured",
			detail: "no usable " + verifyCmdFile + " at the repo root — the verify gate is mandatory for auto-land"}
	}
	// Scope-union execution: a mixed-scope diff (go+gui, diff #9) resolves
	// to several commands — every one must pass, first failure blocks.
	verifyCmd := strings.Join(verifyCmds, " && ")
	var out strings.Builder
	for _, cmd := range verifyCmds {
		tail, err := runVerify(ctx, worktreePath, cmd)
		if tail != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(tail)
		}
		if err != nil {
			return verifyGateOutcome{reason: "verify_failed",
				detail: capDetail(cmd + " → " + err.Error() + "\n" + out.String())}
		}
	}
	verifyTail := out.String()
	if len(verifyTail) > autoLandVerifyTailBytes {
		verifyTail = verifyTail[len(verifyTail)-autoLandVerifyTailBytes:]
	}
	// Verify-evidence gate (M18 batch B): exit 0 that proves nothing. A
	// verify whose output tail carries ZERO test evidence (no PASS token,
	// no go "ok" line, no non-zero N-passed count — the conservative
	// whitelist in review.go) never counts as "verified": a wrong-path
	// verify used to give false release confidence. A build-only
	// .odo-verify can never satisfy this, by design (m16 gate 7).
	if !verifyHasPassEvidence(verifyTail) {
		return verifyGateOutcome{reason: "verify_no_evidence",
			detail: capDetail("verify exit 0 (`" + verifyCmd + "`) but the output tail carries zero test evidence (no PASS token, no ok line, no N-passed count) — a verify that ran no tests proves nothing\n\n" + verifyTail)}
	}
	return verifyGateOutcome{ok: true, cmd: verifyCmd, tail: verifyTail}
}

// verifyCommands reads the worktree's .odo-verify and resolves the command
// list the gate runs sequentially at the worktree root. Absent or
// contentless means the gate cannot run (blocked, fail-closed).
//
// Path-scoped verify (Fix 3, zero-manual-accept), scope-union selection
// (panel diff #9 finding 3): a "glob: command" line runs whenever the
// diff TOUCHES that scope (≥1 path matches); the bare fallback line
// ADDITIONALLY runs when any diff path sits outside every glob's scope.
// The previous all-paths-match rule silently disqualified the gui line on
// mixed-scope diffs: diff #9's ~700 frontend lines landed on go vet alone.
// Example:
//
//	gui/**: cd gui && npx tsc --noEmit && npx playwright test --reporter=line
//	go build ./... && go vet ./... && go test ./...
//
// A pure-gui diff runs the gui line only; a pure-go diff the fallback
// only; a mixed diff both, file order then fallback. Supply-chain gate
// blocks .odo-verify self-modification.
func verifyCommands(worktreePath string, diffPaths []string) ([]string, error) {
	data, err := os.ReadFile(worktreePath + string(os.PathSeparator) + verifyCmdFile)
	if err != nil {
		return nil, err
	}
	type scopedLine struct{ glob, cmd string }
	var scoped []scopedLine
	var fallback string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Check for glob-scoped line: "glob: command" where glob
		// must contain * or / (to distinguish from a bare command
		// that happens to contain ": ").
		if idx := strings.Index(line, ": "); idx > 0 {
			glob := line[:idx]
			cmd := line[idx+2:]
			if strings.ContainsAny(glob, "*/") && glob != "" && cmd != "" {
				scoped = append(scoped, scopedLine{glob, cmd})
			}
			continue
		}
		// First bare line is the fallback
		if fallback == "" {
			fallback = line
		}
	}
	uncovered := len(diffPaths) == 0
	for _, p := range diffPaths {
		inside := false
		for _, sc := range scoped {
			if pathMatch(p, sc.glob) {
				inside = true
				break
			}
		}
		if !inside {
			uncovered = true
			break
		}
	}
	var cmds []string
	for _, sc := range scoped {
		for _, p := range diffPaths {
			if pathMatch(p, sc.glob) {
				cmds = append(cmds, sc.cmd)
				break
			}
		}
	}
	if uncovered && fallback != "" {
		cmds = append(cmds, fallback)
	}
	if len(cmds) == 0 {
		return nil, fmt.Errorf("%s has no command line", verifyCmdFile)
	}
	return cmds, nil
}

// pathMatch checks a single path against a glob, supporting ** for
// recursive directory matching.
func pathMatch(p, glob string) bool {
	// Normalize: strip leading a/ or b/ from git diff paths
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	// Convert ** to a simple prefix match (gui/** → gui/)
	if strings.HasSuffix(glob, "/**") {
		prefix := strings.TrimSuffix(glob, "/**")
		return strings.HasPrefix(p, prefix+"/") || p == prefix
	}
	matched, _ := filepath.Match(glob, p)
	return matched
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
	// The verify command executes the agent's UNREVIEWED code (go test runs
	// its init()/TestMain). It must never see the daemon's secrets — the
	// panel API keys are process env (kimi's panel P0: exfiltration fires
	// pre-review, even when the diff is later blocked).
	proc.Env = verifyEnviron(os.Environ())
	out, err := proc.CombinedOutput()
	if len(out) > autoLandVerifyTailBytes {
		out = out[len(out)-autoLandVerifyTailBytes:]
	}
	if vctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("verify timed out after %s", autoLandVerifyTimeout)
	}
	return string(out), err
}

// verifyEnviron allowlists the child environment for the verify command:
// shell/toolchain basics plus GO*/GIT_*/CGO_* passthrough — and nothing
// else. Everything credential-shaped (SUDO_*, *_KEY, *_TOKEN, AWS_*,
// SSH_AUTH_SOCK) stays with the daemon. An allowlist (not a denylist)
// because the leak costs the API keys, the miss costs a journaled
// verify_failed. Known residual: a GOPROXY URL with embedded basic-auth
// rides in via the GO prefix — private-proxy users accept that exposure
// to their own proxy; gateway keys are never GO-shaped.
func verifyEnviron(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case name == "PATH", name == "HOME", name == "TMPDIR", name == "TMP", name == "TEMP",
			name == "USER", name == "LOGNAME", name == "SHELL", name == "TERM",
			name == "LANG", strings.HasPrefix(name, "LC_"),
			strings.HasPrefix(name, "GO"), strings.HasPrefix(name, "GIT_"), strings.HasPrefix(name, "CGO_"):
			out = append(out, kv)
		}
	}
	return out
}

// journalAutoLandStarted records the pipeline's entry into one of its two
// silent stages (verify gate, panel fan-out) — a liveness breadcrumb for
// the GUI pipeline chip (pipeline-indicator-lock Phase 2), NOT a decision.
// No risk receipt: nothing resolved, so nothing is rated. patch_sha16
// names the exact bytes about to be attested, same as the outcome rows.
// Best-effort like journalRefreshAttempt: a lost breadcrumb degrades the
// chip to "queued" for that stage — the evidence rows that follow are
// journaled unconditionally regardless.
func (s *Server) journalAutoLandStarted(ctx context.Context, d store.Diff, stage string, data []byte) {
	payload := map[string]interface{}{
		"action":      "auto_land_started",
		"diff_id":     d.ID,
		"actor":       autoActor,
		"stage":       stage,
		"patch_sha16": sha16(data),
	}
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(payload)); err != nil {
		log.Printf("auto-land: journal stage start (%s) for diff %d: %v", stage, d.ID, err)
	}
}

// journalAutoLandBlocked records one blocked auto-land attempt. Reviews
// (attached when the panel ran) keep the dissent on the record; the
// diff's patch sha16 rides every row (M18: the ladder's no-progress
// comparator and the audit's diff identity), best-effort — a row about an
// unreadable patch simply omits it. fix-INT W5: the Guardian risk receipt
// rides every blocked row too, classified from the same bytes
// (risk_class/risk_evidence/risk_classifier; unreadable = all omitted).
func (s *Server) journalAutoLandBlocked(ctx context.Context, d store.Diff, reason, detail string, reviews []ReviewResult, consensus string) {
	payload := map[string]interface{}{
		"action":  "auto_land_blocked",
		"diff_id": d.ID,
		"actor":   autoActor,
		"reason":  reason,
	}
	if data, err := os.ReadFile(d.PathOnDisk); err == nil {
		payload["patch_sha16"] = sha16(data)
		// W5: risk receipt from the same bytes — every blocked reason
		// carries the diff's hazard classification (all ~14 reasons).
		mountRiskReceipt(payload, riskReceiptKeys(string(data)))
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
