> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# GUI surfaces for gate-drift latch + design-gate verb (audit actions #3+#4)

## Background
Quad-model architecture audit (2026-08-31, 8 legs) found two control-plane surfaces with zero GUI visibility (the "observability gets GUI, actuation gets CLI" drift, K3/GLM legs), both confirmed by grep:
- **D1 gate-drift latch** (`gatepolicy.go`, `autoland.go:300`): freezes all pipeline landing when Tier-0 gate files drift; each refusal journals `auto_land_blocked {reason:"gate_policy_drift"}`.
- **Design gate** (`loop.go:19`): "human design gate unless `loop_design_gate: auto`" — reachable only via loop state, no GUI entry.

Scope strictly `gui/src`; daemon/internal untouched; no new dependencies. All work in the agent's own worktree, left dirty.

## Key decisions
1. **Fold, not push** — established doctrine: GUI derives from journal events, no daemon push. Gate-drift detection = latest `auto_land_blocked` row with reason `gate_policy_drift` within the current daemon boot/session window, folded in `pipeline.ts` mirroring the existing review_action phase fold.
2. **Banner anchor = StatusBar** (already hosts PipelineChip): inline, non-foldable red/accent row instead of a new panel/overlay — inline rows avoid the App Esc-gate chain registration requirement (pitfalls #12/#19). Copy via `strings.ts`: "⚠ Gate policy drift — landing frozen; run `odo gate re-pin` + commit + restart daemon". Banner width subtracted from the StatusBar overflow computation.
3. **Design-gate surface = existing LoopChip + `loop_ctl` verbs**: daemon `loop_ctl` already accepts `approve_design`/`amend_design`/`veto_design` and resolves the pending task itself; wired action buttons into LoopChip for the pending-design-gate loop state through `gui/src/api.ts`. Chosen over a separate pending-gates list + `/panel consult` routing as the minimal honest surface. Visibility predicate: a suspended loop with no gate stays invisible.
4. **`EventPayload` is a closed interface** — tsc rejected fixture extras (`tier0`, `design_lock`) that no fold reads; dropped the extras rather than widening the type.
5. **Worktree convention** (epoch-40/41): agent worktrees lack `gui/node_modules`; symlink the main checkout's install before running gates.

## Code changes (all `gui/src`)
- `pipeline.ts`: gate-drift fold (boot-window-scoped `auto_land_blocked`/`gate_policy_drift` detection).
- StatusBar + App: banner prop threaded from App; inline banner row plus overflow-width subtraction.
- `strings.ts`: banner + design-gate copy.
- `api.ts`: design-gate verbs added to the loop-ctl API union.
- LoopChip: design-gate action buttons + corrected spinner/visibility predicate.
- `mock-invoke.ts`: `loop_ctl` design-verb parity cases in the mock adapter.
- Tests: fold cases in the existing pipeline test suite (banner visible with blocked row / absent without) — 21/21 file-green; `statusbar.test.tsx` banner render/separator/overflow cases using a rerender pattern (not the debounced ResizeObserver wait) — 23/23; new LoopChip component test folding fixtures through `deriveLoopStates` with a hoisted mock asserting dispatch — 6/6.

## Verification
- Scoped gates (from `gui`, `PATH=~/.hermes/node/bin:$PATH`): `npx tsc --noEmit` pass; full `npx vitest run` **429/429 green**.
- Playwright full e2e (~117 specs): first run **timed out at 20 min** (baseline ~5 min); no leftover playwright processes; a diagnostic re-run with output captured to a log was launched async — **outcome not observed before the session ended**.
- Pitfall hit twice: the persistent shell's cwd silently reset to the repo root, so `vitest run` loaded no config (node env, no jsdom, no setup file) and failed spuriously — always pass explicit `cwd` when running vitest from the persistent shell.

## Open loops
- Playwright e2e gate unconfirmed: check the log from the async re-run; if it still stalls, diagnose the 20-min timeout vs the ~5-min/117-spec baseline before declaring the diff green.
- Final per-item report + command tails owed to the user (task required "report per-item + tails"), including documentation of the LoopChip/`loop_ctl` choice for the design-gate surface.
- Worktree handoff: task required staging in the agent's own worktree and leaving it dirty; no git staging was observed in-session — confirm staged state before handoff.