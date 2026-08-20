> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo GUI: Panel-Lock, Steer Queue, and IME/verify-gate Fixes

Work session on the Odo repo (GUI + Go daemon) covering three items: greying out human review buttons during auto-land panel review, a Hermes-style steer-prompt queue UI, and two bug reports (stuck-blue input box, diff 8 "stuck" panel). All changes sit in worktrees pending the normal diff-accept flow.

## Item 1 — Lock human review buttons while panel reviews a diff

**Decision.** Lock on pipeline phase via a single shared truth table — `in_flight` (verify/panel/refresh) and `landing` lock; `queued`, `revise`, `blocked`, `suspended`, `landed` stay usable because those are states where human action is the escape hatch (e.g. `blocked` hands review back to the human, `suspended` needs human accept to resume). Scope is active-conversation-only (design lock): inbox rows for other conversations never get locked.

**Changes** (+199/−37):
- `gui/src/pipeline.ts`: new exported predicate `pipelineHumanLocked`; `pipelineLabel` moved here from `StatusBar` so the status bar and button chips share one label source.
- `DiffViewer.tsx`: Review/Accept/Reject buttons plus commit-editor confirm wired to the lock; inline chip (spinner + phase label) shown next to the buttons.
- `ReviewInbox.tsx`: row-level Accept/Reject locked; lock state passed through to expanded DiffViewer.
- `App.tsx`: plumbs `pipelineStates` through. Locked style = 50% opacity + pointer-events off.
- Tests: `pipeline.test.ts` (truth table), new e2e case in `pipeline-chip.spec.ts` (queued baseline usable → locked on panel → unlocked on blocked, both surfaces).

**Verification.** vitest 59/59, `tsc --noEmit` clean, full Playwright 102/102, live mock-server screenshots confirmed greyed buttons + chip text + intact layout.

This became **diff #8**.

## Item 2 — Hermes-style steer prompt queue

**Key finding.** The daemon already *was* a queue: `handleSteering` appends to `meta.queuedSteers` and drains at run end into a continuation run. Only the visibility layer was missing. Change = display layer + three fail-closed daemon fixes; zero behavior change to scheduling.

**Display layer:**
- Top floating card: `Processing` + spinner + current run's prompt (2-line clamp, full text in title); label becomes `Processing N queued steers` when a continuation consumes several; card disappears when the run ends.
- Queue list above the composer: `Queued steers · N` header, rows with `#1 next` marker, truncated text, Drop with two-stage confirm (QueueDock pattern reused).
- New `gui/src/steer_queue.ts`: journal-only derivation (`user_message{steer:true}` minus `run_prompt.steer_seqs` and `steer_dropped` closures) — survives daemon restarts/session switches via journal replay, no new polling.
- Transcript cleanups: `steer` badge on steer bubbles; quiet chips (`Steer follow-up` / `Retry` / `dropped queued steer`) replace raw `run_prompt diff #?` noise.

**Daemon fail-closed fixes** (`internal/ipc/server.go`, `protocol.go`):
1. Orphan steers rejected: no active run or finished run → `steer: no active run for conversation %d`, nothing journaled.
2. `queuedSteers` carry journal seqs; `run_prompt` receipts gain `steer_seqs` (steer-less lines byte-identical).
3. New `drop_queued_steer` IPC + batched `steer_dropped{cause}` journaled on every abandon path (cancelled/errored/run_terminal/admission). Ledger closes: every journaled steer ends consumed or dropped.

**Hermes alignment vs. deliberate deviations.** Aligned: queue above textbox, pinned floating active-prompt card, turn-boundary drain, inline delete, persistent backing. Deviated: queue items not editable (Drop + retype instead — no un-steer edit channel in daemon, not worth a new IPC); journal-backed instead of localStorage (cross-restart/multi-window consistency).

**Verification.** `go test ./internal/ipc/` green (8 new pins), vitest 73/73, e2e 21/21 incl. new `steer-queue.spec.ts`, full-lifecycle visual run on mock dev server. (+738/−74)

This became **diff #9**.

## Item 3 — Blue input box + diff 8 "stuck panel"

### Input box permanently blue — root cause and fix
**Mechanism.** On WKWebView, the Enter that confirms an IME candidate sometimes arrives without `isComposing`/`keyCode 229` → the guard lets it through → submit fires mid-composition → the input session aborts, `compositionend` never arrives → `composingRef` stuck `true` → the value-sync effect's guard refuses all future writes → macOS IME marked text (blue, select-all look) stays forever. Matches all symptoms (Chinese input, persistence, selected appearance).

**Fix** (`ChatSurface.tsx`, `submitDraft`): after a successful send, synchronously clear `ta.value = ""` and `composingRef = false` — sending semantically ends the composition without waiting for an event that may never come. All send paths (Enter / button / ⌘Enter) converge there.

**Verification honesty.** The blue *visual* is WKWebView/macOS-specific and not reproducible in Chromium/WebKit; verified at DOM-contract level — new regression test (stuck-composition submit must leave box empty) **fails pre-fix, passes post-fix**; playwright 102/102, vitest 57/57, tsc clean.

### Diff 8 was never stuck
Journal evidence: diff 8 created 18:41:08; `auto_land_blocked` at 18:41:14 — the verify command failed in **6 seconds** with `npx tsc` → "not the tsc command": the run's worktree has no `gui/node_modules` (worktrees contain only tracked files). Re-verified in the same worktree with the symlink added: tsc clean + **102/102 e2e green**. The block was an environmental false positive; content is green. Base matches main HEAD (`25353ad`), so a manual Accept lands it cleanly.

### Structural fix shipped with this item
- `runVerifyGate` now auto-provisions `gui/node_modules` by symlinking from the main checkout (codifying the agents' manual workaround), covering both call sites (autoLand and loop-fix pipeline), with parent-dir creation; three new Go test subcases pin it. `go build`/`vet` clean, `go test ./internal/ipc/` 382s green.
- Note: this worktree also touches protected path `internal/ipc/autoland.go` → auto-land will `protected_path`-block by design; manual accept required. (+122/−3)

### Incidental findings
- The running daemon is a **stale 10:26 binary** — no `auto_land_started` rows in the journal, so no stage-progression visibility. Recompile + restart needed (could not be done mid-session without killing the run).
- Diff 9 (steer queue) terminally blocked 19:15: go verify passed, panel 2/3 `needs_fixes`, auto-repair couldn't start (85 KB > 64 KB prompt limit). Panel found real issues: queued steers become undeletable ghosts after daemon restart, Drop two-stage confirm reset by poll heartbeat, mixed go+gui diff only ran go-side verify. **Fix round recommended before landing; do not force-accept.**
- Pre-existing gofmt debt in `loop_audit.go`/`loop_journal.go`/`server.go` (unformatted at HEAD) — not introduced here, left untouched.

## Open loops

- Diff 8 (panel-lock UI): auto-land false-positive blocked; content re-verified 102/102 green — awaiting manual Accept in the app.
- Diff 9 (steer queue UI): panel found real defects (ghost steers after restart, Drop-confirm poll reset, mixed-diff verify scope); needs a fix round before any accept — do not force-accept.
- Item 3 fix worktree (+122/−3, touches protected `internal/ipc/autoland.go`): auto-land will block on `protected_path`; needs manual accept.
- Daemon restart pending: current daemon is the stale 10:26 binary without `auto_land_started` instrumentation; recompile + restart after the session.
- Blue-input fix verified only at DOM level; if blue persists in the installed app, next step is real WKWebView devtools inspection.
- Pre-existing gofmt debt (`loop_audit.go`, `loop_journal.go`, `server.go`) still unaddressed.