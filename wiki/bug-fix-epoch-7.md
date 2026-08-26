# Journal Replayer — Regenerate Round 3 (FIX A–F)

## Context & status

- Feature: recover stranded memory/pins intents at boot — project-wide total-order journal replayer (newest receipt per layer), entry-merge, explicit `heal_conflict` protocol, resolve/dismiss IPC, GUI stranded-ops banner.
- Replay semantics settled and undisputed across the panel; round 3 scope was the conflict **lifecycle** only.
- History: diff #76 (replayer incl. FIX 1–6) was blocked by an unrelated e2e flake — one-shot width assertion racing rAF-batched resize commits; root-caused and fixed on main as #77 (`eedd080`). Zero-change re-snapshot #78 applied `.odo/diffs/6a8e5bc5-1db0b0835cd0.diff` identically (15 files: 6 `gui/*`, 9 `internal/*` = 8 ipc + 1 store; 2582+/237−) and was blocked again by the auto-panel (`needs_fixes`, `repair_prompt_too_large`).

## Decisions

- **Doctrine boundary (FIX B):** store migrations only UPDATE/ALTER — every lane row survives archiving physically. Therefore literal `LEFT JOIN` in `ListProjectEvents`/`ListHealLedgerRows` encodes survival semantics as a query invariant; receipts on archived/soft-deleted workstreams still fold into replay and the stranded count. Boundary stated in the doctrine comment (verbatim for the completion note): hard cascade-delete / whole-journal `rotate` = **unrecoverable by construction** — explicitly not overclaimed.
- **Supersession retires conflicts (FIX D):** when an evaluation (boot replay or runtime) finds a layer's newest receipt LANDED, any older OPEN `heal_conflict` for that layer is unresolvable → journal `heal_resolved{layer, receipt_seq, actor:"superseded", dismissed:true}`; stranded count drops accordingly. Retirement rows journal *after* the heal row they follow from (order checked and fixed).
- **Resolve freshness guard (FIX E):** conflict journalling records the live disk sha (`disk_sha16_at_conflict`); `handleResolveHealConflict` re-reads the file and refuses to overwrite when the sha moved (newer receipt or hand edit), returning a descriptive error **and** journaling the FIX D superseded dismissal — closes the stale-body clobber of human hand edits.
- **Project-wide count/rows consistency (FIX F):** `strandedTotal` was project-wide but the GUI folded only current-conversation rows ("N stranded" badge with zero actionable rows). `pending_counts` payload now carries open heal-ledger rows project-wide as `(conversation_id, layer, receipt_seq)`; GUI Memory tab folds that list; resolve/dismiss routes by the row's owning conversation (valid: `checkConversation` is project-scoped).
- Exclusions honored: no landWG/auto-land run-lifecycle changes; no docs/panel-evidence/attestation/census files; completion note in final agent text only.

## Code changes

- **FIX A:** `gofmt -w` over all changed Go files (fixed double-tab/misaligned lines in `audit_fixes_test.go`); gate `gofmt -l internal/` empty.
- **FIX B:** store — both queries INNER→LEFT JOIN + doctrine boundary comment. An edit splice initially duplicated a header and dropped the `ListProjectEvents` lead paragraph; repaired and re-verified by read.
- **FIX C:** live apply side — recovery block now carries verbatim per-rule added lines (previously only `recovery.memory.body` post-state + before/after SHAs); `memoryMergePlan` reuses the receipt's recorded line when present, falling back to `reaffirmed: 1` only when absent. No test pinned the exact recovery JSON shape, so no ripple.
- **FIX D:** `memory_replay.go` — retirement calls added at both eval branches + a retire helper; eval doc updated.
- **FIX E:** conflict journalling stores `disk_sha16_at_conflict`; resolve handler implements the freshness guard (several `edit` match-typo retries on this file).
- **FIX F:** `handlePendingCounts` (in `server.go`, not where first assumed) extended; `MemoryPanel` routes by owning conversation; mock-invoke surfaces project-wide rows; `StrandedOp`/api shape untouched.
- **Drills added/updated:** archived-workstream receipt still folds at boot (B); `TestMemoryReplayForeignEntryMerge` updated to pin preserved metadata (C); supersession retirement → count returns to 0 (D); conflict → hand edit → Resolve refused, auto-dismissed (E); project-wide `pending_counts` rows/count consistency daemon test (F). Two-lane collision drill tail re-pinned: a sibling resolve moves the projection, so the second resolve is now correctly **refused as stale** (E doctrine). e2e: two lanes, two conflicts → badge 2 + two rows; resolving one → 1.

## Verification (as of transcript cutoff)

- `go build ./...`, `go vet ./internal/...` OK; `gofmt -l internal/` empty; `tsc --noEmit` clean.
- Replay test subset: **20/20 pass**, 4 new drills green.
- FIX F e2e spec passed after root-causing a false failure (below); full 25-spec playwright gate launched in background.

### Operational gotcha

Playwright reported count=2/rows=1 stably: a **stale Vite dev server from another worktree** (`6a8e4fc6`, up since 10:55) held `:1420`, and `reuseExistingServer: true` silently reused pre-FIX-F code — it would also have sabotaged the pipeline's e2e verify. Killed it; rerun green. Also: bare `playwright test` from the repo root hits vitest — run the e2e dir explicitly from `gui/`.

## Open loops

- Full `go test ./internal/...` background run showed failures in `ipc`; filtered ipc/store reruns were launched but results were still pending at transcript cutoff.
- Full playwright gate (25 specs) was running in the background at cutoff; result unknown.
- Final completion note still owed; must record the FIX B doctrine boundary verbatim (hard cascade-delete / whole-journal `rotate` = unrecoverable by construction).
- Auto-land panel adjudication of round 3 pending (prior rounds blocked by verify flake, then `repair_prompt_too_large`).