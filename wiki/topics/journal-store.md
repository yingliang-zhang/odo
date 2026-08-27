# Journal & SQLite Store

- Schema v3: `diffs.goal` column stores the review objective at `InsertDiff` (revise products inherit the chain head's goal); atomic v1→v3 and v2→v3 ALTERs, fresh DBs record v3 directly (main-epoch-18) (main-epoch-20)
- Schema v4: dedupes fossil duplicate workstream names (new id keeps name, old gets `-dup-<id>`) plus a partial unique index on `status='active'`; `CreateOrGet` re-reads the winner row on constraint conflict; the index is deliberately excluded from unconditional v1 DDL because legacy DBs may carry duplicates needing dedupe first (main-epoch-38)
- SQLite DSN hardening against recurring modernc.org/sqlite WAL-recovery SIGBUS daemon crashes: `_pragma=mmap_size(0)` + `synchronous(FULL)` on BOTH read-write and read-only open paths (RO shares the live WAL, same crash class) (main-epoch-15) (UI-epoch-10) (UI-epoch-11)
- SIGBUS daemon deaths recur (2× same signature) and are the only unexpected daemon-death class; mmap-disable is a journaled therapy, not a proven root cause — production recurrence under observation (UI-epoch-10) (main-epoch-15)
- v4 event dedupe made collision-free with a dedicated collision test (main-epoch-39)
- No unbounded journal listing API: `ListProjectEvents` was deleted outright; `ListProjectEventsPage` keyset pagination is the only listing, and `limit <= 0` is a hard error (the panel rejected the design's silent `1<<30` clamp) (bug-fix-epoch-11) (bug-fix-epoch-12)
- Read-path narrowing: pinned logs read via `FoldWindow`/`LatestFoldBoundary` single-row probe; no-pin markers fall back to full history (the sweep's purpose is rescuing pre-pin legacy batches) (main-epoch-23)
- LIKE safety: `ESCAPE '\\'` with `likeEscape`; ordering `ORDER BY e.id DESC` because created_at second resolution can tie; marker queries use `jsonPairMatch/jsonPairNegate` dual-form colon-space OR (main-epoch-23)
- Ledger divergence convention: reconcile by appending `ledger_correction` journal rows naming `corrects_seq` — never silent DB mutation; store terminal statuses (e.g. wrongly-rejected #34/#41) are left as-is as history (main-epoch-18) (main-epoch-20) (main-epoch-27)
- `.odo/diffs/` is an append-only, sweeper-exempt archive — accepted patches stay on disk permanently; no queue-clearing after accepts (main-epoch-22) (main-epoch-41)
- Journal read failures propagate fail-closed: `runMemoryLayers` returns an error and `assembleRunPrompt` refuses to assemble a blind prompt even though the failure-append may itself fail on a dead journal (main-epoch-15)
