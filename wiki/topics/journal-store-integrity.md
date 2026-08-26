# Journal & Store Integrity

- SQLite DSN hardened on both RW and RO open paths: `_pragma=mmap_size(0)` + `synchronous(FULL)` — the RO path shares the live WAL so it carries the same SIGBUS class (main-epoch-15)
- Schema v3 stores `diffs.goal` at InsertDiff (revise-chain products inherit the chain head's goal byte-exact) — kills the objective-anchor bug where reviews bound to the session's last user message instead of the diff's provenance (main-epoch-18)
- Schema v4: dedupes fossil duplicate workstream names (`-dup-<id}` suffix) + partial unique index on `status='active'`, deliberately excluded from unconditional DDL because legacy DBs need dedupe first; `CreateOrGet` re-reads the winner row on constraint conflict (main-epoch-38)
- v4 dedupe in the store layer is collision-free with a dedicated collision test (main-epoch-39)
- `store.CommitFold` makes epoch bump + marker one transaction; distillCore uses a builder closure; pinned by `TestCommitFoldAtomic` (main-epoch-23)
- Search correctness: LIKE queries use `ESCAPE '\'` + `likeEscape` and `ORDER BY e.id DESC` (created_at second-resolution can tie); marker value patterns converted to dual-form colon-space jsonPair matches (main-epoch-23)
- Read-path windowing: `LatestFoldBoundary` single-row probe feeds `listFoldWindowEvents`; auto/curate/autogate switched to windowed reads; no-pin markers fall back to `firstSeq=0` full history so the sweep still rescues legacy batches (main-epoch-23)
- Loop fold decodes each row payload once (was 2–4 per-key decodes) (main-epoch-23)
- Journal failure propagation is fail-closed: `runMemoryLayers` returns errors and `assembleRunPrompt` refuses to assemble a blind prompt; `journalRunAdvisory` returns error and advisory debounce keys are occupied only on success under `verifyAdviseMu` (main-epoch-15)
- SIGBUS incident root cause remains journal-adjacent and unresolved at the library level: modernc WAL-recovery mmap crash; the DSN remedy is journaled therapy, production recurrence under observation (main-epoch-15)
