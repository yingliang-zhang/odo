# Diff #104 Verify Fix Pass: 4 Failing E2E Specs Root-Caused and Fixed

## Context

The P2 implementation (preview panel, run-history panel, failure overlay) in worktree `/Users/yingliangzhang/Projects/odo/.odo/worktrees/6a924c6f-93f4858ca262` (detached at `169ca02`, parent of "todo: accept diff #103") failed verify on exactly 4 new e2e specs; all 146 pre-existing specs stayed green. Fix scope was restricted to `gui/src/**` and `gui/e2e/**`, work left uncommitted. Failing log: `.odo/verify/diff-104-1787975060261809000.log`. Fixes land as unstaged worktree changes on top of the staged diff: 4 files, +45/−11.

## Root causes and fixes

1. **Open-live click timeout** (`gui/e2e/preview-panel.spec.ts:54`) — two stacked causes:
   - The spec injected a bare orphan `agent_tool_result` (a 0-call burst). `ChatSurface`/`runRenderItems` folds such bursts into the previous run's collapsed "N tool calls" `<details>`, so the Open-live affordance never became actionable. Fix: prepend the matching `agent_tool_call` event (spec line 60) — a 1-call burst renders inline, matching the daemon wire shape.
   - Playwright pins a click to the locator's first DOM resolution; before the poll ingests the pair, `.last()` still resolves to the fixture's folded tool-group card — an invisible dead end. Fix: wait for this card's `summary` to be visible before clicking (line 71).
2. **read_file highlight never renders** (`preview-panel.spec.ts:77`) — two stacked causes:
   - **Real component bug** in `gui/src/components/PreviewPanel.tsx`: `PreviewFilePane` wrote a "seen identity" into `prevRef` during React StrictMode's discarded first effect pass (whose fetch is cancelled via `alive=false` before resolving). The committed second pass believed content had already been fetched and skipped it, leaving the pane on "Loading…" forever. Fix (lines 81–113): track `loadedKeyRef` — the `fetchKey` (`${path}\n${projectRoot}`) whose content has *landed* — instead of identity previously seen; on fetch rejection reset it (line 107) so re-entry retries. This is the same double-invoke discipline as `MemoryPanel`'s `mountedRef`; existing vitest semantics (mount once, +1 fetch per activation edge) confirmed compatible.
   - Fixture content was `"export const seed = 42;\n"` — `export` is a TS keyword and claimed the `.first()` slot of `.tok-keyword`, breaking the assertion's target. Changed to `"const seed = 42;\n"` (spec line 92).
3. **frame-src lock affordance check** (`preview-panel.spec.ts:98`) — same orphan-result collapse as (1). Fix: prepend `agent_tool_call` for `fetch_page` (line 112) plus the summary visibility wait (line 117). The lock assertion itself (`[data-slot="preview-live"]` count 0) was untouched.
4. **Row-click transcript jump** (`gui/e2e/runs.spec.ts:77`) — the assertion measured `getBoundingClientRect()` of the `[data-seq]` element, which is the `.bubble-mount` wrapper with `display:contents` (generates no box, rect is always all-zero, can never intersect). Fix: measure the `.bubble` inside the anchor (`[data-seq="${s}"] .bubble`, line 112). The scrolled-into-viewport + intersection contract is unchanged.

Collateral fix found during the full-suite gate run: `gui/e2e/failure-overlay.spec.ts` — the dismiss test's two back-to-back poll-failure arms (worst-case ~25s each under the mock's poll backoff) plus the 8s dismissed window exceed the default 30s cap under load; bumped with `test.setTimeout(90_000)` (line 73) and corrected the header comment. Timeout policy is unchanged: timeouts account for backoff, never sleeps.

## Key decisions

- **Assertions were not weakened.** Spec changes either restore the daemon wire shape (call+result pair), or fix measurement of an element that is structurally un-measurable (`display:contents`); the latter is justified inline in the spec against the unchanged intersection contract. The adoption-lock assertions all stand verbatim.
- **Component-level fix over test plumbing.** The StrictMode-starved fetch was a genuine user-visible bug (permanent "Loading…" in dev StrictMode), fixed in the component rather than worked around in the spec.
- **Diagnosis method:** reproduce locally first (4 failed / 3 passed), then arm the mock-invoke fixtures in a live browser tab and probe the real DOM to confirm each mechanism (tool-group collapse, StrictMode starvation, Open-live sandbox iframe attributes) before re-running.

## Verification

- Target re-run: 7/7 pass across `preview-panel.spec.ts` and `runs.spec.ts` (~15s, no timeout spinning).
- Three gates run per protocol: `npx tsc --noEmit`, `npx vitest run`, `npx playwright test --reporter=line`; the failure-overlay timeout flake surfaced during the playwright gate and was fixed (above) before the final re-run; session closed with no remaining failures. Final full-suite tail counts are not recoverable from this transcript excerpt beyond the stated 7/7.
- Work remains uncommitted in the worktree per instruction.

## Open loops

- Diff #104 needs a verify re-run and re-submission to the pipeline (P2 auto-land); the verify log on disk is still the pre-fix failing one.
- Carried from epoch 27: P3 of the adoption lock not started (explicitly deferred); stale `stash@{0}` P1 recovery stash still pending cleanup.