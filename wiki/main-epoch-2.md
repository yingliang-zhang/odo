> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# M19 Loop Tests: Fixing `nothing_to_audit` Failures

## Context
- 5 ipc loop tests failed with `nothing_to_audit`: `TestLoopFixpointClean`, `TestLoopP3NeverSpawnsFix`, `TestLoopBadLegBlocksClean`, `TestLoopHoldSeverityTightened`, `TestLoopSecondLoopRefused`.
- Root cause: fixtures commit after the frozen base but never pass `base=` to `/loop audit`, assuming the loop auto-derives the audit base from history.

## Key decisions
- Design-lock V6 (`docs/design/loop-design-lock.md`) confirmed authoritative: audit base comes **only** from SEED pending diffs or an explicit `base=` arg — no history auto-derivation.
- Per instruction: fix the 5 fixtures to pass `base=<frozen base sha>` explicitly; do **not** change the preflight.
- Preflight mechanics verified at `loop.go:164-174`: explicit `base=` skips the accumulated-subject check; empty base freezes `base = CurrentSHA` (= HEAD), so the subject is always empty → refusal.

## Code changes
- Baseline checkout lacked the M19 loop implementation; located surviving patch, applied it, build green, failure reproduced exactly as diagnosed.
- All 5 fixtures updated to pass explicit `base=` → 4/5 green immediately.
- `TestLoopBadLegBlocksClean`: leg-switching mux keyed on model labels the stub never receives; retargeted mux keys to the labels actually delivered → leg switching fires.
- `TestLoopSecondLoopRefused`: fixed clean-completes race — introduced a blocking finding so the loop stays non-terminal by construction, making the "already" refusal deterministic.
- `LoopNotifyOnComplete` default conflict: M19 diff defaults it `true` while the pre-existing `TestGetSettings` pin expected `false`; reconciled test vs. default against the lock after consulting `loop-design-lock.md`.
- Final verification run: `go test ./internal/ipc/ -count=1 -short -run Loop` plus `go build ./... && go vet ./...` — no further failures surfaced after the last edits.

## Integration status
- Auto-land blocked by auto_panel, reason `protected_path` — the diff was not landed automatically.

## Open loops
- Remaining M19 GUI scope awaits a follow-up diff: LoopChip (`deriveLoopStates`), V11 Tauri notification (`loop_notify_on_complete`), V13 slash autocomplete upgrade per the design lock.
- Landing unresolved: auto_panel blocked auto-land on a protected path; manual land/journal-done decision pending.