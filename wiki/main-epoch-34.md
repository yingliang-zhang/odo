> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# odo: fixes for 4 deep-review findings (symlink guard, tab staleness, /preview pinning, hidden-panel recompute)

Follow-up to the deep review of commit `ab20b62`, which left 4 residual findings after the deterministic-path round. All 4 were addressed in this session.

## Decisions

- **P0 — symlink guard root bypass (internal/ipc):** canonical `projectRoot` is now the final trust anchor. Guard verifies the resolved `dir` stays inside the project and rejects symlinks at the root nodes themselves (`.odo`, `wiki`, `wiki/topics`). Write side gets the same guard, not just reads.
- **P1 — keep-alive tab staleness (GUI):** panels stay mounted (state preserved) but refetch on inactive→active transition; uncommitted drafts are protected from overwrite. App passes an `active` prop into Wiki, Memory, Ledger, Skills panels.
- **P1 — /preview unpinned remote code:** Playwright invocation pinned to the lockfile version **1.62.1** instead of `playwright@^1`; install moved to an explicit setup phase. Verified the pinned spec resolves offline against the existing npx cache.
- **P2 — streaming recompute in hidden panels:** pipeline state memo stabilized by "last relevant review/memory event seq"; hidden Ledger no longer re-derives on every events-array replacement.
- **Explicit non-goals:** diff `PathOnDisk` (adapter/test-generated, outside the committable threat model, legitimately outside the project) and vision attachments (user-supplied explicit paths) were deliberately left unguarded.

## Code changes

### Symlink guard (Phase 1)
- `internal/ipc/guarded_read.go`: rewritten — three-arg guard with canonical projectRoot anchor; rejects symlinked root nodes.
- `internal/ipc/io.go`: write-side guard added.
- Migrated all committable call sites: `curator.go` (6 sites), `server.go` (AGENTS.md write), distill note writes, `apply_memory` skill writes, `preview.go` (attachmentDir guard + read-back containment check; signature gains `projectRoot`), plus display surfaces `handleLedger`/`handleReadMemory`.
- `writeTopicPages` stale-file cleanup (`os.Remove`) pre-guarded — it previously bypassed checks before the guard ran.
- Guard tests rewritten: three-arg signature + new root-node/anchor/write-side cases. Pass.

### /preview pinning (Phase 2)
- `internal/ipc/preview.go`: pinned `playwright@1.62.1` (from project lockfile), explicit setup phase, failure-class count 4→5, doc comments updated.
- New `PreviewSetup` test + CLI entry (`./cmd/...`); happy-path tests assert the pinned argv.
- Test isolation: subtests now isolate `HOME` so they don't hit the real machine's hermes npx.
- README preview prerequisite hint synced.

### GUI panels (Phase 3)
- `gui/src/App.tsx`: `active` passed to keep-alive panels; pipeline memo keyed on last relevant review/memory event seq; hidden Ledger derives only when active.
- `WikiBrowser.tsx`, `MemoryPanel.tsx`, `LedgerPanel.tsx`, `SkillsPanel.tsx`: activation-triggered refetch with draft protection. Fixed a TDZ bug (activation effect referenced `loadFiles` before its declaration) and a missed `active` destructure in SkillsPanel.
- `app_keepalive.test.tsx`: old "zero calls on tab switch-back" assertions updated to the new activation-refetch contract; new activation-refresh cases added; selectors relaxed to prefix match after badge counts changed tab accessible names ("Memory 1"). 7/7 pass.

## Verification

- Guard + preview Go tests, incl. `go test -race` targeted runs: pass.
- Full vitest + typecheck + production build: pass.
- Rust: 6/6 pass; go vet: pass (prior state, reconfirmed via targeted runs).
- Live `/preview` run (`ODO_PREVIEW_LIVE=1`) with chromium + guard + offline pinned path: verified.
- One e2e flake (ContextPanel strip-scroll timing) investigated: no causal link to these changes; reproduced only under full machine load (cargo+go concurrent); passed on two isolated reruns — pre-existing load-sensitivity flake.

## Open loops

- Full `go test ./internal/ipc` background suite was still running at session end — final green confirmation outstanding.
- Full Playwright e2e suite (119 tests) not re-run after the Phase 3 GUI changes; only targeted checks and the flake investigation were done.
- Pre-existing e2e strip-scroll timing flake in ContextPanel confirmed load-related but not fixed.
- Session's code changes not yet committed.