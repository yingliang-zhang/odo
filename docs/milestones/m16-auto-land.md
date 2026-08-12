# M16 — Auto-Land (pref-gated unanimous-panel diff landing)

Ships the first behavior that consumes `auto_apply`. Scope: `main` only — a pending diff
lands without a human click after surviving every gate below, in order. Anything else
stays exactly the M15 rung-0 manual-review path.

## Pain

The M15 C0 line/`>300 lines` class behaved as a friction generator, not a risk
classifier: 3 of the last 8 real journal diffs (37.5%) exceeded 300 lines, and with
350K-token panel contexts a 300-line cliff is fake precision. User call: drop the C0
hard gate entirely; panel review decides. The 2026-08-11 design panel (K3 / GLM / DSF)
returned **3/3 DROP_SIZE_KEEP_DIR**: all size gates out, new-top-level-directory gate
stays, and three findings sharper than the brief:

1. **Fail-open (verified in code):** `consensusVerdict` used ceil-majority, so
   **2 ACCEPT + 1 NEEDS_FIXES auto-landed** — an explicit dissent didn't block. Fixed
   to unanimity for the auto-land condition (the display layer's 2/3 semantics for
   human visibility are unchanged).
2. **Test-weakening detection:** a net loss of `*_test.go` assertions
   (`git.TestAssertionDelta`) is the cheapest tripwire for an agent quietly gutting
   its own tests. Mechanically blocks before any panel spend.
3. **Supply-chain paths** (`go.mod replace`, `go.sum`, lockfiles, manifests, and
   `.odo-verify` itself) never auto-land — single-line RCE surface a diff review
   structurally can't price.

## The gate stack (`internal/ipc/autoland.go`, in order)

1. **Pref** — `adapter.ReadSettings().AutoApply != "main"` returns *silently*
   (feature-off deserves no journal noise).
2. **Run posture** — the producing run must have finished cleanly (`agent_error`
   blocks).
3. **Mechanical gates** (deterministic, zero panel spend): protected paths
   (`.odo/`, `wiki/`), supply-chain files, new top-level directory (vs the diff's
   base tree), net `_test.go` assertion loss. Size gates are GONE.
4. **Verify gate** — the repo-root `.odo-verify` command re-runs at the run's
   worktree root. Missing/unreadable/failing = blocked, always (fail-closed).
5. **Cost breaker** — assembled prompt estimate (chars/4) > 87K tokens
   (~25% of the smallest panel context) = blocked. This is the ceiling that replaced
   every line-count gate.
6. **Panel** — prefs.md `review:` models fan out on a GROUNDED prompt: the journaled
   trigger text (never the agent's self-report), the verify output tail, and an
   adversarial instruction (three concrete failure scenarios first, verdict last).
7. **Unanimity** — `consensusVerdict == "accept"` requires EVERY reviewer to accept.
8. **Land** — handleDiffAction's original path (protected-path guard, unmerged-index
   refusal, 3-way apply, path-scoped staged commit, worktree retire) plus
   `actor:"auto_panel"` on the journaled review_action.

## Journal contract (append-only audit)

- `moa_review{actor:"auto_panel"}` — unanimous panel verdict
- `accept{actor:"auto_panel"}` — auto-landed; `ComputeAutonomy` counts these
  separately, never toward rungs (an auto-land must not inflate the streaks that
  would earn future autonomy)
- `auto_land_blocked{reason, detail, [reviews]}` — any gate/panel stop, with the
  panel verdicts attached when the panel ran. M18 retired the lump
  `panel_disagreed` reason into the settlement classes — see
  docs/milestones/m18-settlement-ladder.md.

`auto_apply` values `branch`/`all` stay unconsumed (accepting them still fails closed
in settings; nothing reads them). Skill proposals keep auto_accept deferred — the M15
O-1 no-auto-apply posture lifts for DIFF LANDING ONLY. Every land remains
reversible via git.

## Activation (three prerequisites, all manual)

1. Rebuild + restart the daemon (the live binary predates this code until then).
2. `prefs.md`: `auto_apply: main` (default `off` = fully manual, unchanged).
3. Repo-root `.odo-verify` — a text config, not a script: the first non-comment
   non-empty line is the shell command (`#` lines are comments; no chmod involved).
   This repo's runs `go build ./... && go vet ./... && go test ./...`.

## Tests

- `TestConsensusVerdict` — prior fail-open cases flipped + explicit
  2-ACCEPT-1-NEEDS_FIXES regression.
- `TestTestAssertionDelta` — added/removed/pinned assertion counting under the same
  patch-parser rules as `PatchStats`.
- `TestAutoLandCheck` — each mechanical gate named as the blocking reason.
- `TestVerifyCommand` (missing file is fail-closed) / `TestRunVerify`.
- `TestAutoLandPrompt` — grounded prompt carries goal, verify command+tail,
  adversarial instruction.
- `TestAutoLandBlockedPaths` — every blocked exit journals the right reason;
  panel-needing cases isolate HOME (no `review:` line = blocked, never landed).
- `TestMaybeAutoLandPrefOffSilent` — pref-off produces zero journal rows.
- `TestPanelLive` — env-gated (`ODO_PANEL_LIVE=1`) terminal harness mirroring
  handlePanelQuery's fanout; never runs in the default suite.

## Panel review (2026-08-11, HEAD `50e3e32`): unanimous NEEDS_FIXES

Grounded tri-model adversarial review of the shipped gate stack (`TestPanelLive`
harness, inlined gate code + a 7-read batched wiring audit; all three models
completed, none truncated). Verdict 3/3 NEEDS_FIXES — architecture endorsed
("the spine is genuinely right"), concrete fixable holes. Landed in this fix round:

1. **Truncated/early-ACCEPT unanimity bypass** (all three models): `parseVerdict`
   now honors only the FINAL verdict-token line (`server.go`), and a truncated
   review is forced `needs_fixes` regardless of what the partial text parses to
   (`reviewVerdict`). Shared by `review_diff` and auto-land — the manual panel
   inherits the hardening. The auto-land prompt fences the diff body as data.
2. **Verify executes unreviewed agent code with the daemon's env** (kimi P0):
   `runVerify` now runs with `verifyEnviron`, an allowlisted child env
   (PATH/HOME/TMPDIR + GO*/GIT_*/CGO_* passthrough; SUDO_*/AWS_*/keys stay with
   the daemon).
3. **Verify attested a tree nobody lands** (kimi P0, deepseek P1): new
   `base_stale` gate — main checkout HEAD must still equal the diff's base_sha,
   checked cheaply before any verify/panel spend. Stale = blocked; humans rebase.
4. **Comment-out evasion of the weakened-tests gate** (kimi P1):
   `TestAssertionDelta` skips comment lines on both sides — `-assert.X` /
   `+// assert.X` now nets a removal, blocking.
5. Doc/test reconciliation: `.odo-verify` described as the text config it is;
   new pins `TestParseVerdict`, `TestReviewVerdictTruncation`,
   `TestVerifyEnviron`, the `base_stale` blocked-path, and three comment-evasion
   cases in `TestTestAssertionDelta`.

Deferred, with the panel's own framing (design-level, not this round):

- Process-group sweep on verify timeout (orphaned grandchildren of `go test`
  survive the direct kill; journaled/fail-closed, needs a unix build-tag split).
- Minimum panel-size policy: N=1 makes "unanimous" trivial (a prefs validation
  or doc-level decision — the owner picks the `review:` line).
- Merge-preview verify (kimi's option b: throwaway HEAD+diff worktree) as a
  throughput upgrade over `base_stale`'s block-and-wait.
- Repeated-block visibility: `base_stale`/`verify_failed` storms are journaled
  but have no GUI surface.
- Stubbed-reviewer integration test of the full land path and a
  `ComputeAutonomy` tally pin over `auto_panel` rows (unit pins shipped here).

## Non-goals

Rung promotions beyond rung-0, branch-rung landing (`odo/auto-<ws>` batch flow —
still data-gated on `odo autonomy audit` streaks, README A1), per-file verdict
splitting (loses cross-file coherence; panel-ranked last), any consumption of
`branch`/`all`, and skill-proposal auto-accept. The C0–C3 autonomy classification
is untouched — it feeds streak reports only, a separate semantic from the landing gate.
