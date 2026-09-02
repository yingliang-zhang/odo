package ipc

// M16 (O-1 v2) → M20: daemon-side auto-land — a pending diff lands after
// surviving every layer below, in order. M20 made the pipeline the
// DEFAULT landing canon (panel-owns-resolution): no human review step
// anywhere in the ordinary path.
//
//	arming          M20: every completed run's diff enters the pipeline;
//	                the survivors below resolve WITHOUT a human. The
//	                auto_apply pref survives only as an explicit kill
//	                switch ("off" = silent), and a prefs.md with no review:
//	                line means UNARMED = silent skip inside autoLand (zero
//	                journal noise, both postures).
//	run posture     the producing run must have finished cleanly
//	mechanical      2026-08-20 user doctrine ("review everything
//	gates            automatically"): hard blocks remain ONLY where a
//	                 panel verdict is impossible or structurally invalid —
//	                 memory paths (.odo/, wiki/: the executor refuses those
//	                 for EVERY actor, human click included, so a panel would
//	                 attest bytes that can never land) and supply-chain
//	                 files (manifests/lockfiles: single-line RCE vectors
//	                 diff review structurally cannot audit — the panel's own
//	                 catch; .odo-verify: a self-modified oracle attesting
//	                 itself). Everything else the old gates stopped for —
//	                 protected gate source files, new top-level directories,
//	                 net *_test.go assertion loss — degrades to a mechanical
//	                 risk annotation the panel must weigh, and a gate-source
//	                 diff lands only behind a journaled unanimous verdict on
//	                 the EXACT patch bytes (executor evidence gate,
//	                 handleDiffAction). Deterministic, zero panel spend for
//	                 the hard blocks. Size gates are GONE (panel verdict
//	                 DROP_SIZE_KEEP_DIR: a 300-line cliff is fake
//	                 precision with 350K-token contexts; the token cost
//	                 breaker below is the ceiling).
//	base freshness  M20 reconcile chain, in order — the zombie killer:
//	                 (1) ALREADY-LANDED roundtrip (ProbeAlreadyLanded:
//	                 a reverse --check applies ⇔ the post-image is fully
//	                 in main — manual merge/cherry-pick/identical edit;
//	                 the pipeline's apply-path blindness to side-channel
//	                 landings was the diff-20 class). Read-only, zero
//	                 spend: refresh_attempted{already_landed} journals
//	                 and the FINAL gate's own roundtrip ledgers the
//	                 no-op land under acceptMu (accept row carries
//	                 already_landed:true). (2) P0a REFRESH: entry drift
//	                 triggers a --3way rebase probe in a throwaway
//	                 worktree (ProbeApplyClean — the main checkout is
//	                 never touched, acceptMu never taken); clean = base
//	                 pointer moves to HEAD and the pipeline proceeds.
//	                 (3) REBASE ROUND: conflict/error at the probe (or
//	                 at the FINAL gate mid-pipeline) regenerates the
//	                 task on current HEAD via the settle ladder
//	                 (trigger:"base_stale" on the round row; supersede
//	                 retires the stale diff when the round lands). The
//	                 AUTHORITATIVE adjudication is still final, inside
//	                 handleDiffAction's accept branch under acceptMu
//	                 (checkAndRefreshBase — zero check-to-apply window,
//	                 same roundtrip→refresh chain against the real
//	                 checkout). Partial landings reverse-check dirty
//	                 every time — conservative, never a false accept.
//	verify gate     the repo-root `.odo-verify` command re-runs at the
//	                 run's worktree root with an allowlisted child
//	                 environment (verifyEnviron) under a scratch HOME
//	                 (P1 #11: env-shaped keys stripped, file-shaped
//	                 credentials hidden; go/playwright caches mounted by
//	                 name) — the gate executes the agent's unreviewed
//	                 code, and the daemon's secrets must never be visible
//	                 to it (panel P0). Absent/failed = blocked, always.
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
//	settlement      M18 (settle.go) → M20 verdict ownership → D7
//	                 verdict policy: accept lands; a CORROBORATED
//	                 reject — ≥2 reject legs from ≥2 distinct model
//	                 families — AUTO-REJECTS {panel_unanimous_reject |
//	                 panel_mixed} (evidence row carries the full
//	                 dissent, transcript advisory fires, chain
//	                 ancestors supersede; M18 parked them for a human
//	                 the pipeline no longer assumes); a MINORITY
//	                 reject — exactly 1 reject leg, or ≥2 rejects of
//	                 one family — SUSPENDS {panel_minority_reject,
//	                 repanel_count} for human triage (no reject row,
//	                 the diff stays pending, one bounded re-panel per
//	                 boot). Verify failures never join the reject set.
//	                 An
//	                 errored leg stays blocked-pending {panel_infra}
//	                 (infra is not a verdict — recover-pending-diffs
//	                 re-fires on restart); zero rejects + ≥1 needs_fixes
//	                 enters the auto-revise ladder (≤2 fresh repair
//	                 rounds — the third evaluation is terminal: the
//	                 majority-accept valve (≥2/3, zero rejects/infra/
//	                 truncated) fires or the ladder suspends;
//	                 no-progress stop; journal-derived suspension
//	                 resumed by ANY landing).
//	land            handleDiffAction's original path — memory-path
//	                 refusal (every actor), the gate-source evidence
//	                 gate (a non-human actor lands gate files only
//	                 behind a moa_review verdict row whose patch_sha16
//	                 matches the bytes being landed), unmerged-index
//	                 refusal, the FINAL base-
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
// Restart recovery taxonomy (recoverPendingDiffs): auto_land_blocked
// (≠panel_infra), pipeline moa_review, and auto_revise_round are TERMINAL
// for their diff — a restart never re-fires those. auto_land_started and
// refresh_attempted are breadcrumbs and panel_infra is not a verdict —
// those shapes re-fire; a diff with only them (or nothing) is stranded.
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
// auto_apply values "main"/"branch"/"all" all read as ON under M20 (the
// pref was kept back-compatible: only "off" disables). Skill proposals
// keep auto_accept deferred — the O-1 no-auto-apply posture lifts for
// DIFF LANDING ONLY — and every land remains reversible (git).
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	// (~9 min: 532s measured 2026-08-27, 535s with diff #87's added
	// tests) fits comfortably; a hanging build must not wedge the
	// serialization mutex. Raised 10m→15m on 2026-08-28: the suite grew
	// past 10m under daemon-side load and the default 600s go-test
	// timeout panicked verify (diff #87). Raised 15m→30m on 2026-09-01:
	// diff #142's GUI segment ALONE (tsc + vitest 513 + playwright 168
	// single-worker 14.8m) measured 952s — the deadline killed the go
	// segment before it ran (verify_ms 952493, all executed segments
	// green). The cap bounds ONE runVerify command; scope-union diffs
	// pay it per command at worst.
	autoLandVerifyTimeout = 30 * time.Minute
	// verifyLogKeepBytes caps one persisted .odo/verify log (tail-biased:
	// diagnostics sit at the end); verifyLogKeepCount bounds the directory.
	verifyLogKeepBytes = 1 << 20
	verifyLogKeepCount = 32

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
// copied off the run's runMeta by the caller. M20: the pipeline is the
// DEFAULT landing canon (no arming click, no human review step) — the
// auto_apply pref survives only as an explicit kill switch: "off"
// returns SILENTLY (a disabled feature deserves no journal noise, unlike
// a blocked attempt, which is evidence).
func (s *Server) maybeAutoLand(d store.Diff, worktreePath, goal string, runErrored bool, runVerdict string) {
	if adapter.ReadSettings().AutoApply == "off" {
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
	// D1 drift latch — the FIRST blocked-row gate, ahead of the arming
	// silent-return: a latched daemon lands NOTHING through any pipeline
	// (direct autoLand, loop pipelines, recoverPendingDiffs, and the
	// settle ladder downstream), and the refusal must be journaled per
	// attempt so the wedge is visible even on unarmed projects. Only the
	// AutoApply-off kill switch above (in maybeAutoLand) outranks it —
	// a disabled feature stays silent by design.
	if s.gateDrift {
		s.journalAutoLandBlocked(ctx, d, "gate_policy_drift",
			"gate policy drift latched at boot (internal/ipc/gatepolicy.go vs internal/ipc/gate_manifest.json) — no pipeline lands until a human runs 'odo gate re-pin', commits both files, and restarts the daemon", nil, "")
		return
	}
	// M20 arming gate — FIRST, before any journal write, git probe, or
	// verify spend: a prefs.md without a review: line means no panel can
	// exist, so the pipeline is UNARMED and exits SILENT (same posture as
	// the off kill switch — zero journal noise for a feature that cannot
	// run). This subsumes the M16 no_review_models blocked row: under
	// default-on, "no panel configured" is the ordinary unconfigured
	// state, not a per-diff failure worth an evidence row; it also keeps
	// verify_unconfigured rows off projects that never armed the panel.
	models := parseReviewModels(adapter.LoadPrefsRaw("review"))
	if len(models) == 0 {
		return
	}
	// Producing-run evidence outranks the panel-size advisory (2026-08-22
	// panel review, deepseek/kimi): run_errored and tainted-verdict rows
	// are per-diff, actionable diagnostics — if the once-per-lifetime
	// single_judge advisory fired first, it would mask them on the only
	// diff class where the run's own failure evidence exists, and consume
	// its one shot on a diff the config wasn't responsible for.
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
	// P1 #8 (2026-08-22 panel review): N=1 "unanimity" degrades the panel
	// to a single judge — one model's ACCEPT would land work the doctrine
	// (3-model blind panel, log.md:50 deferred item) assumes ≥2 dissent
	// channels for. The pipeline stays UNARMED at one model; the first
	// attempt per daemon lifetime journals a single_judge_panel advisory
	// (the degraded state must not be invisible — unlike the ordinary
	// unconfigured silent return above), later diffs pend silent for the
	// human. Advisory surfaces (/panel, review_diff) are N-unrestricted.
	if len(models) == 1 {
		if s.singleJudgeAdvised.CompareAndSwap(false, true) {
			s.journalAutoLandBlocked(ctx, d, "single_judge_panel",
				"prefs 'review:' configures a single model — auto-land requires a ≥2-model panel (N=1 unanimity is a single judge with no dissent channel); the diff stays pending for human review",
				nil, "")
		}
		return
	}
	data, err := os.ReadFile(d.PathOnDisk)
	if err != nil {
		s.journalAutoLandBlocked(ctx, d, "unparseable_diff", err.Error(), nil, "")
		return
	}
	diffText := string(data)

	reason, detail, riskNotes := s.autoLandCheck(d)
	if reason != "" {
		s.journalAutoLandBlocked(ctx, d, reason, detail, nil, "")
		return
	}
	// Declarative rules overlay (.odo/rules.json — P1 borrow, item 12):
	// loaded once at pipeline start, AFTER the gate-policy risk gate and
	// BEFORE any rebase/verify spend. The file can only TIGHTEN: deny
	// blocks the diff for the human (reason rule_deny:<rule.reason>);
	// ask forces the panel (the armed M20 canon panels every diff
	// unconditionally today — the journal row records the forced
	// posture; forward-compat against any future mechanical fast path);
	// allow is passthrough and never wins on gate-protected paths
	// (rule_override_ignored rows name each attempt). Absent file ⇒
	// zero rules, zero rows, zero overhead; malformed file fails SAFE
	// (zero active rules + a rules_parse_error row — the overlay never
	// blocks on its own defects).
	if rules, rulesErr := loadRulesFile(s.projectRoot); rulesErr != nil {
		s.journalRulesEvent(ctx, d, map[string]interface{}{
			"action": "rules_parse_error",
			"detail": rulesErr.Error() + " — running with zero declarative rules",
		})
	} else if len(rules) > 0 {
		rulePaths, rpErr := git.PatchPaths(d.PathOnDisk)
		if rpErr != nil {
			// autoLandCheck already parsed the patch — this is the
			// fail-closed backstop, never the ordinary path.
			s.journalAutoLandBlocked(ctx, d, "unparseable_diff", rpErr.Error(), nil, "")
			return
		}
		ruleAction, ruleHits, ruleIgnored := evalRulesDetailed(rules, rulePaths)
		for _, ig := range ruleIgnored {
			s.journalRulesEvent(ctx, d, map[string]interface{}{
				"action":     "rule_override_ignored",
				"rule_index": ig.RuleIndex,
				"rule":       ig.Pattern,
				"paths":      capRulePaths(ig.Paths),
				"reason":     "cannot loosen gate-tier protection",
			})
		}
		switch ruleAction {
		case "deny":
			h := ruleHits[0]
			s.journalAutoLandBlocked(ctx, d, "rule_deny:"+h.Reason,
				fmt.Sprintf("declarative rule #%d (%q) denies %d path(s) [%s]; the diff stays pending — the human Accept click remains the escape",
					h.RuleIndex, h.Pattern, len(h.Paths), strings.Join(capRulePaths(h.Paths), ", ")), nil, "")
			return
		case "ask":
			// The panel below runs unconditionally (M20); journal the
			// forced-review posture as evidence.
			for _, h := range ruleHits {
				s.journalRulesEvent(ctx, d, map[string]interface{}{
					"action":     "rule_ask",
					"rule_index": h.RuleIndex,
					"rule":       h.Pattern,
					"reason":     h.Reason,
					"paths":      capRulePaths(h.Paths),
				})
			}
		}
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
		// M20 reconcile BEFORE the merge probe (read-only): content that
		// already reached main through a side path (manual merge,
		// cherry-pick) is a bookkeeping resolution — skip the rebase
		// probe, skip verify+panel spend (nothing is being judged), and
		// let the FINAL gate's own already-landed path ledger the no-op
		// land under acceptMu.
		if landed, _, lerr := git.ProbeAlreadyLanded(s.projectRoot, d.PathOnDisk); lerr == nil && landed {
			s.journalRefreshAttempt(ctx, d, "pre_spend_probe", "already_landed", base, head, nil)
			if _, err := s.handleDiffAction(ctx, d.ID, "accept", autoActor, ""); err != nil {
				log.Printf("auto-land: already-landed accept for diff %d: %v", d.ID, err)
			}
			return
		}
		clean, probeDetail, perr := git.ProbeApplyClean(s.projectRoot, d.PathOnDisk)
		if perr != nil || !clean {
			outcome := "error"
			refreshErr := perr
			if perr == nil {
				outcome = "conflict"
				refreshErr = errors.New(probeDetail)
			}
			s.journalRefreshAttempt(ctx, d, "pre_spend_probe", outcome, base, head, refreshErr)
			feedback := fmt.Sprintf("main HEAD %s drifted from diff base %s — refresh probe: %s", head, base, outcome)
			if probeDetail != "" {
				feedback += "\n" + probeDetail
			} else if perr != nil {
				feedback += "\n" + perr.Error()
			}
			// M20: the drifted diff regenerates on current HEAD (rebase
			// revise round, trigger base_stale) — the M16 "re-run the
			// task" advice mechanized; nothing parks for a human. When
			// the round's diff lands, supersedeChain retires this one.
			s.ladderMu.Lock()
			s.settleBaseStale(ctx, d, diffText, feedback)
			s.ladderMu.Unlock()
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
	verifyPaths, pathsErr := git.PatchPaths(d.PathOnDisk)
	// D9-W3a (additive): the gate's wall time rides the journal rows it
	// feeds — blocked rows on failure, the moa_review evidence row on
	// success. Verify duration was previously unjournaled (a real gap
	// the D9 legs surfaced); the key is additive (ADR-0002).
	verifyStart := time.Now()
	gate := runVerifyGate(ctx, s.projectRoot, worktreePath, verifyPaths, "diff-"+strconv.FormatInt(d.ID, 10))
	verifyMs := time.Since(verifyStart).Milliseconds()
	if !gate.ok {
		s.journalAutoLandBlockedExtra(ctx, d, gate.reason, gate.detail, nil, "",
			map[string]interface{}{"verify_ms": verifyMs})
		if gate.reason == "verify_unconfigured" {
			// Discoverability (verify_advisory.go): an unconfigured or
			// scope-missing project blocks EVERY such diff here; surface
			// the one-time manual fix in the transcript the user reads.
			s.adviseVerifyUnconfigured(ctx, d.ConversationID, worktreePath, verifyPaths)
		}
		return
	}
	verifyCmd, verifyTail := gate.cmd, gate.tail

	promptIn := reviewPromptInput{
		mode:       reviewPromptGate,
		goal:       goal,
		diffPath:   d.PathOnDisk,
		diffText:   diffText,
		verifyCmd:  verifyCmd,
		verifyTail: verifyTail,
		verifyNote: "exit 0 (pass evidence present in the output tail)",
		riskNotes:  riskNotes,
	}
	prompt := buildReviewPrompt(promptIn)
	if est := len(prompt) / 4; est > autoLandMaxPromptTokens {
		s.journalAutoLandBlocked(ctx, d, "prompt_too_large",
			"prompt estimate "+strconv.Itoa(est)+" tokens > cap "+strconv.Itoa(autoLandMaxPromptTokens), nil, "")
		return
	}

	// D2: exactly one leg of the fan-out runs grounded (scoped read-only
	// repo tools over the diff's import neighborhood). The grounded
	// prompt variant is built only when the plan is usable; every other
	// leg keeps the byte-identical ungrounded prompt.
	ground := s.planGrounded(models, s.projectRoot, verifyPaths, pathsErr)
	groundedPrompt := ""
	if ground.ok {
		promptIn.grounded = true
		groundedPrompt = buildReviewPrompt(promptIn)
	}

	// Second breadcrumb: the verify is attested, the fan-out below is the
	// last silent stage (minutes under model latency).
	s.journalAutoLandStarted(ctx, d, "panel", data)
	reviews := s.reviewFanout(ctx, models, prompt, &ground, groundedPrompt)
	cv := consensusVerdict(reviews)
	// M18 settlement: an infra leg is not a verdict — the round never
	// validly completed (fail closed, and it never ticks the ladder).
	// Infra stays blocked-pending under M20: a transport/auth/timeout
	// failure resolves on the next pipeline trigger (recover-pending-diffs
	// re-fires pending diffs at daemon start), never by discarding the diff.
	if panelInfraLeg(reviews) {
		// D2: a degraded GROUNDED leg on a round that required grounding
		// (gate-source diffs by default) journals its posture — the lock's
		// fail-visible clause for a grounding that never ran.
		var extra map[string]interface{}
		if groundedLegDegraded(reviews) {
			extra = map[string]interface{}{"grounding": "degraded"}
		}
		s.journalAutoLandBlockedExtra(ctx, d, "panel_infra",
			"a review leg failed on transport/auth/timeout — infra failures are not verdicts", reviews, cv, extra)
		return
	}
	switch settlementClass(cv, reviews) {
	case "reject_independent":
		// D7 reject_independent (≥2 reject legs from ≥2 distinct model
		// families) — M20 auto-reject mechanics, unchanged: a
		// corroborated direction-level reject no longer parks the diff
		// for a human — M18's "never auto-reject" posture assumed one.
		// Evidence before action: the blocked row carries the full
		// dissent (blocked-row-as-evidence precedent), the transcript
		// advisory makes the resolution visible where the user reads,
		// and the reject row itself names the pipeline as actor
		// (streak-excluded in ComputeAutonomy, risk-attested like every
		// resolution). The blocked reason keeps the unanimity split for
		// journal-name stability; the classification itself stays in
		// settlementClass. The patch file stays on disk; the chain's
		// older pending siblings retire as superseded (the chain is
		// dead); the revised instruction is the re-entry.
		reason := "panel_unanimous_reject"
		detail := "every reviewer rejected the direction; auto-rejected (the dissent is on this row; the patch stays on disk)"
		advisory := "the auto-land panel unanimously rejected diff #%d — auto-rejected (the reasons are in the journal). Revise the instruction and resend if the direction should change."
		split := false
		for _, r := range reviews {
			if r.Verdict != "reject" {
				split = true
				break
			}
		}
		if split {
			reason = "panel_mixed"
			detail = "reviewers from ≥2 model families rejected the direction; auto-rejected (the dissent is on this row; the patch stays on disk)"
			advisory = "the auto-land panel rejected diff #%d (corroborated across model families — the reasons are in the journal); it was auto-rejected. Revise the instruction and resend if the direction should change."
		}
		s.journalAutoLandBlocked(ctx, d, reason, detail, reviews, cv)
		s.journalRunAdvisory(ctx, d.ConversationID, fmt.Sprintf(advisory, d.ID))
		if _, err := s.handleDiffAction(ctx, d.ID, "reject", autoActor, ""); err != nil {
			// A racer already resolved it (human override, a chain
			// supersede) — the evidence rows above stand either way.
			log.Printf("auto-land: auto-reject diff %d: %v", d.ID, err)
		} else {
			s.supersedeChain(ctx, d)
		}
		return
	case "reject_minority":
		// D7 reject_minority — exactly 1 reject leg, or ≥2 rejects all
		// of one model family. A single voice (or one family's
		// correlated voices) must not end the chain on a coin-flip:
		// SUSPEND for human triage instead of auto-rejecting. The
		// blocked row (full dissent attached, repanel_count journaled
		// from the prior-row ledger) is the whole action — NO reject
		// row, NO chain supersede; the diff stays PENDING and the inbox
		// accept/reject click is the resolution. The row is non-
		// terminal for the restart recovery (one fresh panel per boot
		// until repanel_count reaches panelMinorityRepanelMax).
		rejects := 0
		for _, r := range reviews {
			if r.Verdict == "reject" {
				rejects++
			}
		}
		detail := "one reviewer rejected the direction; a single dissenting leg has no auto-reject capacity (D7) — suspended for human triage (the dissent is on this row; the patch stays on disk; a daemon restart re-panels once)"
		advisory := "the auto-land panel had a single REJECT on diff #%d — NOT auto-rejected (D7 verdict policy: one dissenting voice does not end the chain). Triage it in the inbox: accept or reject (the reasons are in the journal); a restart re-panels it once."
		if rejects > 1 {
			detail = fmt.Sprintf("%d reviewers rejected, all from one model family; a correlated dissent has no auto-reject capacity (D7) — suspended for human triage (the dissent is on this row; the patch stays on disk; a daemon restart re-panels once)", rejects)
			advisory = fmt.Sprintf("the auto-land panel rejected diff #%%d (%d reject legs, all one model family) — NOT auto-rejected (D7 verdict policy: a same-family dissent is correlated). Triage it in the inbox: accept or reject (the reasons are in the journal); a restart re-panels it once.", rejects)
		}
		s.journalAutoLandBlockedExtra(ctx, d, "panel_minority_reject", detail, reviews, "reject_minority",
			map[string]interface{}{"repanel_count": s.minorityRepanelCount(ctx, d)})
		s.journalRunAdvisory(ctx, d.ConversationID, fmt.Sprintf(advisory, d.ID))
		return
	case "needs_fixes":
		// Zero rejects + ≥1 needs_fixes: nobody said the direction is
		// wrong, it's just not done — the auto-revise ladder decides
		// (spawn a repair round, or demote). ladderMu serializes the
		// whole read-decide-spawn: two racing pipelines from the same
		// conversation must not fork the rounds chain.
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
		// D9-W3a (additive): the attesting verify's wall time.
		"verify_ms": verifyMs,
		// Tri-model right sidebar gap (read-only run/verify log): the
		// verify that attested this landing was previously ephemeral —
		// it rode only the review prompt. Journal it (capped like blocked
		// details) so the landed row carries its own run output.
		"verify_cmd":  verifyCmd,
		"verify_tail": capDetail(verifyTail),
	}
	if gate.logPath != "" {
		moaPayload["verify_log"] = gate.logPath
	}
	// W5: the risk receipt for exactly the bytes the panel judged.
	mountRiskReceipt(moaPayload, riskReceiptKeys(diffText))
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(moaPayload)); err != nil {
		log.Printf("auto-land: journal panel verdict for diff %d: %v (NOT landing)", d.ID, err)
		return
	}
	if _, err := s.handleDiffAction(ctx, d.ID, "accept", autoActor, ""); err != nil {
		// Drift mid-pipeline (HEAD moved after the entry probe): the
		// FINAL gate exhausted its resolutions (already-landed roundtrip
		// and --3way refresh conflicted — its refresh_attempted row
		// already precedes this one). The completed panel rides the
		// blocked row as evidence (blocked-row-as-evidence precedent);
		// M20 then regenerates the task on current HEAD via a base_stale
		// revise round — the fresh diff re-enters the full pipeline, so
		// the pre-drift verdict never attests the post-drift tree.
		if errors.Is(err, errBaseStale) {
			s.journalAutoLandBlocked(ctx, d, "base_stale_at_land",
				err.Error()+" — the verify and panel attested the pre-drift tree; regenerating on current HEAD", reviews, cv)
			s.ladderMu.Lock()
			s.settleBaseStale(ctx, d, diffText, err.Error())
			s.ladderMu.Unlock()
		}
		// A human raced the panel (already accepted/rejected), or the
		// executor refused (protected path, conflicted index, apply
		// failure): the diff stays pending for triage either way.
		log.Printf("auto-land: accept diff %d: %v", d.ID, err)
	}
}

// recoverPendingDiffs re-fires the auto-land pipeline for pending diffs a
// daemon restart STRANDED: maybeAutoLand normally fires from drainRun on
// run completion, so a restart after the run drained but before the
// pipeline journaled an outcome leaves the diff pending with zero
// pipeline records; a restart mid-pipeline leaves only breadcrumbs
// (auto_land_started, refresh_attempted) — the same stranded shape.
//
// Dedup (option A, restart double-panel fix): the pre-filter recovery
// re-fired EVERY pending diff unconditionally, so each restart re-spent
// verify+panel on diffs whose outcomes were already journaled — e.g. a
// panel_mixed row awaiting the human's reject click got a second full
// panel. Diffs with a terminal pipeline row (pipelineTerminalDiffIDs)
// are skipped; only panel_infra stays retryable (infra is not a verdict
// — this re-fire IS its designed retry channel, the panelInfraLeg block)
// and a D7 panel_minority_reject re-panels once per boot until its
// repanel_count reaches panelMinorityRepanelMax.
// Diffs owned by a non-terminal loop (seed + loop_diff_bound rows) are
// likewise skipped (P1 #13): recoverLoops' tick is their retry channel,
// and both firing together was the boot-time double-panel spend.
//
// Each surviving diff spawns a goroutine whose maybeAutoLand re-checks
// the pref and re-adjudicates base freshness (P0a refresh) — the same
// path as if the run had just finished. The worktree may have been
// reclaimed by the sweeper; an empty path blocks verify_unconfigured
// (the correct outcome — the user can re-run or reject). A journal read
// failure aborts the whole recovery (fail closed): with a broken store
// the pipeline can only spend, never land — its evidence rows would fail.
func (s *Server) recoverPendingDiffs(ctx context.Context) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		log.Printf("recover-pending-diffs: get project: %v", err)
		return
	}
	rows, err := s.store.ListAllPendingDiffs(ctx, p.ID)
	if err != nil {
		log.Printf("recover-pending-diffs: list pending: %v", err)
		return
	}
	stranded, err := strandedPendingDiffs(ctx, s.store, rows)
	if err != nil {
		log.Printf("recover-pending-diffs: %v (NOT re-firing: a broken journal means spend without land)", err)
		return
	}
	if skipped := len(rows) - len(stranded); skipped > 0 {
		log.Printf("recover-pending-diffs: %d/%d pending diffs already adjudicated — skipping their re-fire", skipped, len(rows))
	}
	// P1 #13: a NON-TERMINAL loop owns its seed and loop-produced diffs —
	// recoverLoops' tick re-drives them, and re-firing auto-land here would
	// double the verify+panel spend on the same rows (the restart double-
	// panel fix's sibling case, boot-time this time). An enumeration error
	// fails OPEN (the spend duplicates; nothing lands twice — acceptMu
	// serializes the land gate either way).
	if owned, oerr := s.loopOwnedSeedDiffIDs(ctx); oerr != nil {
		log.Printf("recover-pending-diffs: loop seed enumeration: %v (failing open — loop-owned diffs may re-fire alongside their loops)", oerr)
	} else if len(owned) > 0 {
		var keep []store.PendingDiffRow
		skipped := 0
		for _, r := range stranded {
			if owned[r.ID] {
				skipped++
				continue
			}
			keep = append(keep, r)
		}
		if skipped > 0 {
			log.Printf("recover-pending-diffs: %d pending diff(s) owned by active loops — recoverLoops drives them, skipping their re-fire", skipped)
		}
		stranded = keep
	}
	for _, r := range stranded {
		wtPath := ""
		if r.WorktreePath != nil {
			wtPath = *r.WorktreePath
		}
		baseSHA := ""
		if r.BaseSHA != nil {
			baseSHA = *r.BaseSHA
		}
		log.Printf("recover-pending-diffs: re-triggering auto-land for diff #%d (conv %d, base %s)", r.ID, r.ConversationID, baseSHA)
		// Fix B4: the panel judges against the user's original instruction.
		// Schema v3: the diff row carries it verbatim — the conversation's
		// newest message is the wrong anchor for a diff produced runs ago
		// (the #34 false objective-mismatch rejection).
		// landWG: the spawned pipeline outruns this enumeration (recoverWG
		// only joins the enumeration) — register it so Wait joins the
		// accept tail too (the #63 verify-flake class).
		s.landWG.Add(1)
		go func(d store.Diff, wtPath, goal string) {
			defer s.landWG.Done()
			s.maybeAutoLand(d, wtPath, goal, false, "")
		}(r.Diff, wtPath, s.diffGoal(ctx, r.Diff))
	}
}

// strandedPendingDiffs filters pending diff rows down to those with NO
// terminal pipeline row in their conversation's journal — the recovery's
// re-fire set. One journal scan per conversation; input order preserved.
func strandedPendingDiffs(ctx context.Context, st *store.Store, rows []store.PendingDiffRow) ([]store.PendingDiffRow, error) {
	byConv := make(map[int64][]store.PendingDiffRow)
	var convOrder []int64
	for _, r := range rows {
		if _, seen := byConv[r.ConversationID]; !seen {
			convOrder = append(convOrder, r.ConversationID)
		}
		byConv[r.ConversationID] = append(byConv[r.ConversationID], r)
	}
	var out []store.PendingDiffRow
	for _, convID := range convOrder {
		events, err := st.ListEvents(ctx, convID, 0)
		if err != nil {
			return nil, fmt.Errorf("list events conv %d: %w", convID, err)
		}
		terminal := pipelineTerminalDiffIDs(events)
		for _, r := range byConv[convID] {
			if !terminal[r.ID] {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// pipelineTerminalDiffIDs returns the diff IDs the auto-land pipeline has
// already ADJUDICATED in one conversation's journal — the restart
// recovery's dedup set. A diff is terminal when a review_action row names
// it (diff_id) with:
//
//	auto_land_blocked{reason != panel_infra}  every settled/blocked outcome
//	                                          (run/verify gates, base
//	                                          staleness, panel_mixed /
//	                                          panel_unanimous_reject, the
//	                                          ladder's revise_* stops) —
//	                                          EXCEPT a D7
//	                                          panel_minority_reject row
//	                                          with repanel_count <
//	                                          panelMinorityRepanelMax:
//	                                          the recovery re-panels it
//	                                          once per boot until the
//	                                          count reaches the bound
//	                                          (then the row turns
//	                                          terminal and the diff parks
//	                                          human-only)
//	moa_review{actor:auto_panel}              the pre-land evidence row — a
//	                                          land-failure race leaves the
//	                                          diff pending but judged
//	auto_revise_round                         the ladder owns the diff now;
//	                                          the spawned round's own
//	                                          pipeline supersedes it when
//	                                          that round lands
//
// NOT terminal, by design: auto_land_started / refresh_attempted are
// breadcrumbs — a restart mid-pipeline leaves exactly those, and the diff
// IS stranded. panel_infra is not a verdict: its designed resolution IS
// the restart re-fire (autoLand, the panelInfraLeg block). A D7
// panel_minority_reject suspend is likewise retryable — bounded: it
// re-panels once per boot until repanel_count reaches
// panelMinorityRepanelMax, then parks human-only. Human-triggered
// moa_review rows carry no actor and never dedup — the pipeline genuinely
// has not run for that diff. Human accept/reject rows can't coexist with
// a pending diff at all, and pipeline accept/reject rows only appear with
// a status update, so neither is needed here.
func pipelineTerminalDiffIDs(events []store.Event) map[int64]bool {
	terminal := make(map[int64]bool)
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action       string `json:"action"`
			Actor        string `json:"actor"`
			Reason       string `json:"reason"`
			DiffID       int64  `json:"diff_id"`
			RepanelCount int    `json:"repanel_count"`
		}
		if !jsonUnmarshalOK(ev.Payload, &p) || p.DiffID == 0 {
			continue
		}
		switch {
		case p.Action == "auto_land_blocked" && p.Reason != "panel_infra" &&
			(p.Reason != "panel_minority_reject" || p.RepanelCount >= panelMinorityRepanelMax):
			// D7: a minority suspend stays retryable (one re-panel per
			// boot) until its ledger count reaches the bound; every other
			// non-infra blocked reason is terminal on landing.
		case p.Action == "moa_review" && p.Actor == autoActor:
		case p.Action == "auto_revise_round":
		default:
			continue
		}
		terminal[p.DiffID] = true
	}
	return terminal
}

// autoLandCheck applies the deterministic pre-panel evaluation, cheapest
// first, in two classes (2026-08-20 user doctrine: "review everything
// automatically" — content judgments belong to the panel, not to
// mechanical stops):
//
//   - HARD BLOCKS (non-empty reason): reserved for verdicts that are
//     impossible or structurally invalid. Memory paths (.odo/, wiki/)
//     can never land — the executor refuses them for EVERY actor, the
//     human click included, so a panel would attest bytes landing is
//     impossible for. The D1 Tier-0 gate core (gatepolicy.go,
//     gate_manifest.json) is human-only — no panel verdict can land it,
//     so the check blocks pre-panel and saves the spend. Supply-chain
//     files (manifests/lockfiles) are single-line RCE vectors diff review
//     structurally cannot audit; .odo-verify self-modification would
//     additionally be the verify oracle attesting itself.
//   - RISK ANNOTATIONS (returned notes, never a block): everything a
//     reviewer CAN audit — protected gate source files, new top-level
//     directories, net *_test.go assertion loss. Each note is a
//     mechanical, audit-grade fact (zero heuristics a prompt could talk
//     its way around) injected into the panel's facts block; the panel
//     owns the verdict, and a gate-source diff then lands only behind
//     the executor's evidence gate (handleDiffAction: a journaled
//     unanimous verdict whose patch_sha16 matches the landed bytes).
func (s *Server) autoLandCheck(d store.Diff) (reason, detail string, annotations []string) {
	paths, err := git.PatchPaths(d.PathOnDisk)
	if err != nil {
		return "unparseable_diff", err.Error(), nil
	}
	// Double-layer with the executor (handleDiffAction re-checks): the
	// pre-panel check saves the panel spend and journals the clearer
	// reason. Three classes: memory paths (hard block — unmendable), the
	// D1 Tier-0 gate core (hard block — human-only, no panel verdict can
	// ever land it), Tier-1 gate sources (annotation — attestable).
	for _, p := range paths {
		if isMemoryPath(p) {
			return "protected_path", p, nil
		}
	}
	for _, p := range paths {
		if isGateTier0Path(p) {
			return "gate_core_path", "gate core " + p + " is Tier-0 (human-only): no pipeline landing path exists, unanimous panel attestation included — a human Accept is required", nil
		}
	}
	for _, p := range paths {
		if isGateSourcePath(p) {
			annotations = append(annotations, "gate source touched: "+p+" — this diff modifies the reviewing pipeline itself and lands on a unanimous panel verdict with no human click; score ANY weakening of gates, unanimity, or the verify oracle as REJECT")
		}
	}
	for _, p := range paths {
		base := strings.ToLower(p[strings.LastIndex(p, "/")+1:])
		if autoLandSupplyChainFiles[base] {
			return "supply_chain_path", p, nil
		}
	}
	stat, err := git.PatchStats(d.PathOnDisk)
	if err != nil {
		return "unparseable_diff", err.Error(), annotations
	}
	base := ""
	if d.BaseSHA != nil {
		base = *d.BaseSHA
	}
	if base == "" {
		return "base_unresolvable", "diff has no base_sha — the new-top-dir gate cannot run", annotations
	}
	tree, err := GitTopDirsResolver(s.projectRoot)(base)
	if err != nil {
		return "base_unresolvable", err.Error(), annotations
	}
	newDirs := map[string]bool{}
	for _, f := range stat.Files {
		if f.DeletedFile {
			continue
		}
		p := strings.ReplaceAll(f.Path, "\\", "/")
		if slash := strings.Index(p, "/"); slash > 0 && !tree[p[:slash]] && !newDirs[p[:slash]] {
			newDirs[p[:slash]] = true
			annotations = append(annotations, "new top-level directory: "+p[:slash]+"/ — nothing in the diff's base tree places it; weigh whether the placement is intentional")
		}
	}
	added, removed, err := git.TestAssertionDelta(d.PathOnDisk)
	if err != nil {
		return "unparseable_diff", err.Error(), annotations
	}
	if removed > added {
		annotations = append(annotations,
			fmt.Sprintf("test assertions decreased: +%d added / -%d removed (net loss) — if removed assertions covered surviving behavior the verify oracle itself just got weaker; weigh it", added, removed))
	}
	return "", "", annotations
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
	// logPath is the project-relative .odo/verify log holding the gate's
	// FULL output ("" when nothing ran or the best-effort write failed).
	// The journaled tail stays the quick-read record; this ends the
	// capDetail-eats-the----FAIL-line blind reproductions (#47/#48).
	logPath string
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
func runVerifyGate(ctx context.Context, projectRoot, worktreePath string, diffPaths []string, logLabel string) verifyGateOutcome {
	provisionVerifyDeps(projectRoot, worktreePath, diffPaths)
	verifyCmds, err := verifyCommands(worktreePath, diffPaths)
	if err != nil {
		return verifyGateOutcome{reason: "verify_unconfigured",
			detail: "no usable " + verifyCmdFile + " at the repo root — the verify gate is mandatory for auto-land"}
	}
	// Full-output persistence (#49): every terminal outcome below writes
	// the UNCAPPED combined output to <project>/.odo/verify/; the journal
	// keeps the capped tail plus a pointer to that file. capDetail's 4KB
	// tail swallowed the actual --- FAIL line twice (#47, #48), forcing a
	// blind same-bytes reproduction — the on-disk log is the diagnosis.
	// Best-effort: a failed write just reverts to the capped-tail-only
	// record. The pointer is appended AFTER capDetail so the truncation
	// can never eat it.
	var out strings.Builder // capped tails for prompt/journal (unchanged contract)
	var full bytes.Buffer   // uncapped record for the .odo/verify log
	writeLog := func(o verifyGateOutcome) verifyGateOutcome {
		if full.Len() > 0 {
			o.logPath = writeVerifyLog(projectRoot, logLabel, full.Bytes())
		}
		if !o.ok && o.logPath != "" {
			o.detail += "\n[full verify output: " + o.logPath + "]"
		}
		return o
	}
	// Scope-union execution: a mixed-scope diff (go+gui, diff #9) resolves
	// to several commands — every one must pass, first failure blocks.
	verifyCmd := strings.Join(verifyCmds, " && ")
	for _, cmd := range verifyCmds {
		raw, err := runVerify(ctx, worktreePath, cmd)
		fmt.Fprintf(&full, "$ %s\n%s\n", cmd, raw)
		tail := keepTail(string(raw), autoLandVerifyTailBytes)
		if tail != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(tail)
		}
		if err != nil {
			return writeLog(verifyGateOutcome{reason: "verify_failed",
				detail: capDetail(cmd + " → " + err.Error() + "\n" + out.String())})
		}
	}
	verifyTail := keepTail(out.String(), autoLandVerifyTailBytes)
	// Verify-evidence gate (M18 batch B): exit 0 that proves nothing. A
	// verify whose output tail carries ZERO test evidence (no PASS token,
	// no go "ok" line, no non-zero N-passed count — the conservative
	// whitelist in review.go) never counts as "verified": a wrong-path
	// verify used to give false release confidence. A build-only
	// .odo-verify can never satisfy this, by design (m16 gate 7).
	if !verifyHasPassEvidence(verifyTail) {
		return writeLog(verifyGateOutcome{reason: "verify_no_evidence",
			detail: capDetail("verify exit 0 (`" + verifyCmd + "`) but the output tail carries zero test evidence (no PASS token, no ok line, no N-passed count) — a verify that ran no tests proves nothing\n\n" + verifyTail)})
	}
	return writeLog(verifyGateOutcome{ok: true, cmd: verifyCmd, tail: verifyTail})
}

// verifyCommands reads the worktree's .odo-verify and resolves the command
// list the gate runs sequentially at the worktree root. Absent or
// contentless means the gate cannot run (blocked, fail-closed).
//
// Path-scoped verify (Fix 3, zero-manual-accept), scope-union selection
// scopedLine is one "glob: command" verify line.
type scopedLine struct{ glob, cmd string }

// parseVerifyFile splits .odo-verify content into its scoped (glob:
// command) lines and the bare fallback command, skipping blanks and
// comments. A line containing ": " whose left side carries * or / is
// scoped; the first other non-comment line is the fallback. THE parser
// for the file format — verifyCommands (gate, reads the run worktree's
// copy) and verifyCommitConfig (advisory, reads HEAD's copy) must never
// drift apart on what a line means.
func parseVerifyFile(content string) (scoped []scopedLine, fallback string) {
	for _, line := range strings.Split(content, "\n") {
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
	return scoped, fallback
}

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
	scoped, fallback := parseVerifyFile(string(data))
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
// timeout, returning the FULL combined
// output either way — the gate tail-caps for prompt/journal and persists
// the uncapped record to .odo/verify (runVerifyGate).
// The command comes from the worktree's own committed .odo-verify — never
// from the diff under review (the supply-chain gate blocks that), so this
// runs no content the panel is judging.
func runVerify(ctx context.Context, worktreePath, cmd string) ([]byte, error) {
	vctx, cancel := context.WithTimeout(ctx, autoLandVerifyTimeout)
	defer cancel()
	proc := exec.CommandContext(vctx, "sh", "-c", cmd)
	proc.Dir = worktreePath
	// The verify command executes the agent's UNREVIEWED code (go test runs
	// its init()/TestMain). It must never see the daemon's secrets — the
	// panel API keys are process env (kimi's panel P0: exfiltration fires
	// pre-review, even when the diff is later blocked).
	env := verifyEnviron(os.Environ())
	// P1 #11: the env allowlist blocks key-SHAPED leaks, but FILE-shaped
	// credentials (~/.ssh, ~/.aws, ~/.config, XDG-compliant apps whose
	// config root derives from HOME) stayed readable — a second exfil
	// channel of the same m16 P0 class. Verify now runs against an empty
	// scratch HOME; the two credential-free caches the toolchain needs
	// (go's build/module cache, playwright's browser install) are mounted
	// back EXPLICITLY below so a per-verify sandbox doesn't turn every
	// run into a cold rebuild/re-download. An unbuildable sandbox means
	// the gate cannot hold its posture: fail closed.
	scratch, err := os.MkdirTemp("", "odo-verify-home-")
	if err != nil {
		return nil, fmt.Errorf("verify sandbox: %w (refusing to run unreviewed code against a credential-filled HOME)", err)
	}
	defer os.RemoveAll(scratch)
	if home := os.Getenv("HOME"); home != "" {
		env = append(env, goToolchainCacheEnv()...)
		if dir := playwrightBrowsersDir(home); dir != "" {
			env = append(env, "PLAYWRIGHT_BROWSERS_PATH="+dir)
		}
	}
	env = setEnv(env, "HOME="+scratch)
	proc.Env = env
	out, err := proc.CombinedOutput()
	if vctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("verify timed out after %s", autoLandVerifyTimeout)
	}
	return out, err
}

// verifyEnviron allowlists the child environment for the verify command:
// shell/toolchain basics plus GO*/GIT_*/CGO_* passthrough — and nothing
// else. Everything credential-shaped (SUDO_*, *_KEY, *_TOKEN, AWS_*,
// SSH_AUTH_SOCK) stays with the daemon. An allowlist (not a denylist)
// because the leak costs the API keys, the miss costs a journaled
// verify_failed. Known residual: a GOPROXY URL with embedded basic-auth
// rides in via the GO prefix — private-proxy users accept that exposure
// to their own proxy; gateway keys are never GO-shaped.
//
// The HOME the allowlist passes is REPLACED by the caller (P1 #11 scratch
// HOME) — it stays in the list so the no-hardcoded-OS shell basics stay in
// one place, and so a future reader sees env policy here and sandbox policy
// at runVerify, never two partial env builders.

// setEnv replaces NAME's entry in env (or appends it) so the slice never
// carries duplicates (exec dedupes last-wins, but a single source beats a
// reader auditing two HOME lines).
func setEnv(env []string, kv string) []string {
	name, _, _ := strings.Cut(kv, "=")
	for i, e := range env {
		if strings.HasPrefix(e, name+"=") {
			env[i] = kv
			return env
		}
	}
	return append(env, kv)
}

// goToolchainCacheEnv mounts the go toolchain's caches into the verify
// sandbox (P1 #11): the values are resolved against the REAL environment
// (so a user's go/env-configured GOPATH/GOCACHE keeps working), then re-
// exported explicitly since the child's HOME no longer resolves them.
// GOCACHE/GOMODCACHE/GOPATH hold no credentials — GONOSUMDB-style
// credential-adjacent knobs stay OUT (they ride the GO passthrough only
// when explicitly set in the daemon's env, the pre-#11 posture). A missing
// go binary (non-go project) yields nothing; the verify command itself
// decides whether that is fatal.
func goToolchainCacheEnv() []string {
	cmd := exec.Command("go", "env", "GOCACHE", "GOMODCACHE", "GOPATH")
	// Pin the telemetry dir off HOME: go's async telemetry counter init
	// writes into $HOME/.../go/telemetry AFTER the query exits — under a
	// test/sandbox scratch HOME that write races TempDir cleanup
	// (observed as cleanup-failure flakes in the auto-land test group).
	// An explicitly configured GOTELEMETRYDIR rides through untouched.
	if os.Getenv("GOTELEMETRYDIR") == "" {
		cmd.Env = append(os.Environ(), "GOTELEMETRYDIR="+filepath.Join(os.TempDir(), "odo-go-telemetry"))
	}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	names := []string{"GOCACHE", "GOMODCACHE", "GOPATH"}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var env []string
	for i, name := range names {
		if i < len(lines) {
			if v := strings.TrimSpace(lines[i]); v != "" {
				env = append(env, name+"="+v)
			}
		}
	}
	return env
}

// playwrightBrowsersDir locates the installed playwright browser cache the
// gui-scoped verify needs under a scratch HOME (npx playwright test would
// otherwise attempt a cold re-download and fail offline). It holds no
// credentials; PLAYWRIGHT_BROWSERS_PATH is deliberately NOT in
// verifyEnviron's allowlist — env extension is opt-in per directory, and
// this is the one audited exception. "" when uninstalled (e2e-free
// projects lose nothing).
func playwrightBrowsersDir(home string) string {
	if v := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); v != "" {
		return v
	}
	var dir string
	if runtime.GOOS == "darwin" {
		dir = filepath.Join(home, "Library", "Caches", "ms-playwright")
	} else {
		dir = filepath.Join(home, ".cache", "ms-playwright")
	}
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return dir
	}
	return ""
}
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

// journalAutoLandBlockedExtra records one blocked auto-land attempt.
// Reviews (attached when the panel ran) keep the dissent on the record;
// the diff's patch sha16 rides every row (M18: the ladder's no-progress
// comparator and the audit's diff identity), best-effort — a row about an
// unreadable patch simply omits it. fix-INT W5: the Guardian risk receipt
// rides every blocked row too, classified from the same bytes
// (risk_class/risk_evidence/risk_classifier; unreadable = all omitted).
// journalAutoLandBlockedExtra is journalAutoLandBlocked with optional
// additive payload keys (ADR-0002 discipline) — D7's repanel_count rides
// the minority row only; every other blocked reason keeps its exact
// byte shape (nil extra).
func (s *Server) journalAutoLandBlockedExtra(ctx context.Context, d store.Diff, reason, detail string, reviews []ReviewResult, consensus string, extra map[string]interface{}) {
	payload := map[string]interface{}{
		"action":  "auto_land_blocked",
		"diff_id": d.ID,
		"actor":   autoActor,
		"reason":  reason,
	}
	for k, v := range extra {
		payload[k] = v
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

// journalAutoLandBlocked is the plain call shape every blocked reason
// uses except D7's panel_minority_reject (which adds repanel_count).
func (s *Server) journalAutoLandBlocked(ctx context.Context, d store.Diff, reason, detail string, reviews []ReviewResult, consensus string) {
	s.journalAutoLandBlockedExtra(ctx, d, reason, detail, reviews, consensus, nil)
}

// truncMarker prefixes every trimmed record (journal details, verify
// logs) so a reader knows bytes were dropped at the head.
const truncMarker = "…[earlier truncated]\n"

// keepTail returns the trailing max bytes of s, rune-safe at the leading
// cut. THE tail cutter: prompt tails, capped journal details, and
// .odo/verify logs all cut identically.
func keepTail(s string, max int) string {
	if len(s) > max {
		cut := len(s) - max
		for cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut++
		}
		return s[cut:]
	}
	return s
}

// capDetail trims a journal detail to a reviewable size, keeping the
// TAIL: go-test failure diagnostics (--- FAIL lines, build errors) and
// error summaries live at the end of the output — head-trimming used to
// journal the first 4KB of PASS spam while swallowing the actual failure
// (#40 investigate). The cut is rune-safe at the leading boundary.
func capDetail(s string) string {
	const maxDetail = 4 * 1024
	if len(s) > maxDetail {
		return truncMarker + keepTail(s, maxDetail)
	}
	return s
}

// writeVerifyLog persists the gate's FULL combined output to
// <project>/.odo/verify/<label>-<unixms>.log (per-log tail cap guards
// against pathological spam) and returns the project-relative path the
// journal references. Best-effort: "" on failure — the capped journal
// tail remains as the pre-#49 record.
func writeVerifyLog(projectRoot, label string, full []byte) string {
	dir := filepath.Join(projectRoot, ".odo", "verify")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("auto-land: verify log dir: %v", err)
		return ""
	}
	name := fmt.Sprintf("%s-%d.log", label, time.Now().UnixNano())
	content := string(full)
	if len(content) > verifyLogKeepBytes {
		content = truncMarker + keepTail(content, verifyLogKeepBytes)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		log.Printf("auto-land: verify log %s: %v", name, err)
		return ""
	}
	pruneVerifyLogs(dir)
	return filepath.Join(".odo", "verify", name)
}

// pruneVerifyLogs bounds .odo/verify to the newest verifyLogKeepCount
// logs — bounded audit retention; the oldest age out first. Best-effort.
func pruneVerifyLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= verifyLogKeepCount {
		return
	}
	type aged struct {
		name string
		mod  time.Time
	}
	files := make([]aged, 0, len(entries))
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr == nil && !e.IsDir() {
			files = append(files, aged{e.Name(), info.ModTime()})
		}
	}
	if len(files) <= verifyLogKeepCount {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files[:len(files)-verifyLogKeepCount] {
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil {
			log.Printf("auto-land: verify log prune %s: %v", f.name, err)
		}
	}
}

// reviewFanout sends prompt to every model in parallel, collecting
// position-stable verdicts (reviewWithModel degrades failures to
// needs_fixes — never an accidental accept). Shared by review_diff and
// the auto-land pipeline. ground designates the one grounded leg (D2 —
// planGrounded's output; nil arms nothing): its prompt is groundedPrompt
// (buildReviewPrompt's grounded variant) and its fail-posture rides the
// plan — a required-but-init-failed grounding ships Infra so the round
// fails closed, while a non-required init failure falls back to the
// ordinary leg so the panel keeps the capacity it had pre-D2.
func (s *Server) reviewFanout(ctx context.Context, models []reviewModel, prompt string, ground *groundedPlan, groundedPrompt string) []ReviewResult {
	reviews := make([]ReviewResult, len(models))
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch {
			case ground != nil && i == ground.idx && ground.ok:
				reviews[i] = s.reviewWithModelGrounded(ctx, m, groundedPrompt, *ground)
			case ground != nil && i == ground.idx && ground.required:
				reviews[i] = ground.infraReview(m, s.sharedMoa())
			default:
				reviews[i] = s.reviewWithModel(ctx, m, prompt)
			}
		}()
	}
	wg.Wait()
	return reviews
}
