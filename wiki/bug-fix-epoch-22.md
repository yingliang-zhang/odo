# Epoch 21: D6 Diversity Gate Re-application & U1 UI Layout Hardening

## D6 — Design-MoA diversity gate (Go, accepted)

Panel aggregate read NEEDS_FIXES, but root cause was environmental: the reviewed D6 diff was absent from the evaluation worktree (clean tree); it only existed in sibling worktree `6a917cb9`. Resolution: re-applied the reviewed diff verbatim (md5-identical) in the canonical worktree and verified. Panel then accepted.

**Changed files:** `internal/ipc/protocol.go` (DesignProposal += `endpoint`, `model_family`), `internal/ipc/design_moa.go` (per-leg receipts via `scrubBaseURL`+`modelspec.Family`; `designDiversity`/`designGateAdmits`: legs≥2 && fams≥2), `internal/ipc/loop_run.go` (auto-gate refusal ⇒ `auto_gate:"refused_diversity"` journal row, no spawn, pending falls through to human gate), `internal/ipc/loop_journal.go`, `internal/ipc/design_moa_test.go` (`TestRunDesignMoaDiversityGate`, `TestAutoGateFallsBackToHuman`).

**Reviewer findings — all three dispositioned as no-change:** empty `ModelFamily` unreachable + fail-closed; 500ms no-spawn window backed by fold-state assertion; unknown-model⇒basename is intended D7 semantics.

**Verification:** gofmt/build/vet clean; modelspec ok; focused D6 tests 5/5 PASS; full `internal/ipc` suite ok (574.7s); Tier-0 untouched.

## U1 — UI layout hardening lock (gui-only, U1.1–U1.5)

Binding spec: `docs/design/ui-layout-lock.md` §U1. U2 explicitly excluded (no ResizeObserver chat guard, no panel persistence, no prose cap, no bubble padding).

### Key decisions
- **Overflow model:** chips never unmount — six `data-overflow-group` wrappers toggle `.sb-hidden` (e2e `status-badge` hooks stay mounted). Hide order FIRST→LAST: ctx-meter → omp → panel → pipeline → bg-runs → count chips; group-atomic hiding, reverse-group recovery.
- **Recovery invariant (real bug found by tests):** a group may only recover when every higher-priority group is visible — prefix invariant of the lock order.
- **`+N` chip:** single chip + Radix popover reusing TopBar ⋯ overflow pattern; rows derived reactively from props (live values: ctx pct recomputed, OMP live node, count rows keep real jump buttons). DOM `firstElementChild` is not a React element — rows must be built from props, not DOM cloning.
- **Panel chip stays single `Panel ×N`** — per-model expansion was invented out-of-contract; wave-b e2e spec pins the single-chip contract (strict-mode violation forced revert).
- **`running` merged into pipeline chip** (`queued… · running 0:05`, one spinner) — merged chip is e2e-safe because fixture `runState.foreground` defaults false.
- **`.truncate` global cap deleted**; explicit `max-w-[220px]` only on slash-menu/mention-menu rows (`at-detail`, `slash-desc`).
- **Outline kill** scoped to `.chat-input textarea:focus`; ModelPill gets `focus-visible:ring-2` + Enter/Space keydown (preventDefault cleanly suppresses Radix's own handler).
- **Toast z 90→95**; `--text-nano: 10px` token added for grievance/queue-next-tag; type-token sweep across app.css and StatusBar.
- **`twMerge` trap:** bare `text-micro` conflicts with tier color classes; escaped STATUS_BADGE to `text-[length:var(--text-micro)]`.
- **Test infra:** `@types/node@^22` devDep added — vitest `css:false` stubs `.css?raw` to `""`, so CSS pin-board tests read files via `node:fs`. `SettingsPanel` switched to `window.setTimeout` to hold the DOM `number` type after @types/node landed.

### Changed files
`gui/src/components/StatusBar.tsx` (rewrite: overflow measure block, group wrappers, +N popover, token sweep), `gui/src/styles/app.css` (tokens, `.truncate` deletion, focus scope, toast z, `.sb-hidden` rule placed after flash rules, unlayered), `gui/src/components/ChatSurface.tsx` (menu row caps, focus-within border), `gui/src/components/ModelPill.tsx`, `gui/src/components/SettingsPanel.tsx`, `gui/package.json`+lock, `gui/src/components/status_bar.test.tsx` (new, 17 tests).

### Verification (all three gates, in order)
1. `npx tsc --noEmit` → clean.
2. `npx vitest run` → **192/192 passed** (14 files).
3. Playwright: `--grep -i "status|sidebar|panel"` matches only 5 titles; ran full suite instead → 124 passed / 2 failed (both the per-model `.panel-chip` strict-mode violation). After reverting to single chip, re-ran the 6 affected specs → **38 passed**. Go suite not run (gui-only constraint).

### Implementation bugs caught by tests
- `widths["plus"]` key mismatch — +N chip width never counted in overflow math.
- Recovery pass ran against pre-+N-commit DOM — guarded to run only once the +N chip exists (or nothing is hidden).
- Recovery priority violation (bg-runs visible while count hidden) — fixed with the higher-priority-visible constraint.
- Wrong test regex for `pipelineLabel("queued")` ("auto-land queued…").

## Open loops
- **U1 auto-land blocked** — `auto_panel` returned `auto_land_blocked` with reason `supply_chain_path`; the `@types/node@^22` package.json/lockfile addition is the likely trigger. Awaits supply-chain review or a decision on the dependency.
- **U2 remains unscheduled** — chat ResizeObserver guard, panel persistence/width, prose cap, bubble padding are a separate later diff, not yet tasked.