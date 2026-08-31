# Concurrency, Locking & Storage

- Auto-distill spawns and timer callbacks are wrapped into the Server waitgroup, recoverPendingDiffs fan-out tracked the same way, and rig.stop drains in-flight work (~3.7–3.9s blocking) — untracked goroutines wrote wiki/journal after TempDir cleanup and store close (main-epoch-42)
- delete_workstream vs bootstrap atomicity: deletingWs is hoisted unconditionally before the conversation lookup and bootstrap's create section runs under the same lock with a guard — a conversationless lane previously allowed bootstrap under a deleted workstream (main-epoch-42)
- handleDeleteWorkstream checks daemon in-memory active state (run/distill/slash/panel/loop/autoPending), then journal-derived loop activity, before SQL delete — blocking hidden diffs on soft-deleted workstreams (main-epoch-38)
- Loop admission is atomic: state fold plus loop_started journal append happen in one critical section; the early fold outside the lock is retained only as a fast-reject hint (main-epoch-15)
- Daemon-side runLivenessDrain (2s default interval, atomic toggle) closes the C11 falsehood that GUI-closed loops keep progressing — drain no longer depends on GUI polling (main-epoch-14)
- SQLite DSN hardening: mmap disabled (_pragma=mmap_size(0)) and synchronous(FULL) on both read-write and read-only opens — the RO path shares the live WAL and carries the same SIGBUS class (main-epoch-15)
- Fold commits are atomic: store.CommitFold bumps the epoch and writes the marker in one transaction (main-epoch-23)
- SearchEvents escapes LIKE and orders by e.id DESC (created_at second-resolution ties); marker queries match both colon-space and spaced JSON pair forms (main-epoch-23)
- Read-path narrowing uses a LatestFoldBoundary probe plus windowed event reads; no-pin legacy markers fall back to full-history (firstSeq=0) — narrowing by marker.seq+1 broke the legacy sweep rescue (main-epoch-23)
- migrateV4 dedupes fossil duplicate workstream names (loser rows renamed -dup-<id>) with a partial unique index on active status; the index is excluded from fresh-DB DDL because legacy DBs need dedupe first, and CreateOrGet re-reads the winner row on constraint conflict (main-epoch-38)
- Concurrent panels use per-consult batch-group progress slices — defer removes only its own batch and poll snapshots merge, eliminating Done > Total and mixed legs (main-epoch-38)
- retireRun retires the run matching the diff's own worktreePath; the byConv binding is fallback only — sequential runs in one conversation previously closed the wrong worktree (main-epoch-28)
