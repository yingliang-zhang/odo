# Store Schema & Journal Contracts

- Schema v3 adds diffs.goal: drainRun writes the round's review target at InsertDiff (revise-chain products get the chain head's goal byte-exact); diffGoal() reads the inline goal with fallback to originGoal for NULL legacy rows; migrations v1→v3 and v2→v3 are atomic ALTERs (main-epoch-18)
- Schema v4 dedupes fossil duplicate workstream names (new id keeps the name, old gets -dup-<id>) and adds a partial unique index on status='active'; the index is deliberately excluded from schemaV1 unconditional DDL because legacy DBs may carry duplicates needing dedupe first; CreateOrGet re-reads the winner row on constraint conflict (main-epoch-38)
- Store v4 dedupe is collision-free with a dedicated collision test (main-epoch-39)
- SearchEvents: LIKE patterns get ESCAPE '\' with likeEscape, and ordering switched to e.id DESC (created_at is only second-resolution and can tie) (main-epoch-23)
- Marker LIKE queries use dual-form colon-space OR (jsonPairMatch/jsonPairNegate) because JSON serialization variants differ; all 9 value-bound patterns converted (main-epoch-23)
- ListProjectEvents was deleted (no unbounded full-list API): ListProjectEventsPage(keyset: WHERE e.id > ? ORDER BY id ASC LIMIT 512-page) is the only listing, and limit ≤ 0 is a hard error (an earlier clamp-to-1<<30 silently resurrected the unbounded path and was rejected by the panel) (bug-fix-epoch-12)
- SQLite DSN hardening: _pragma=mmap_size(0) and synchronous(FULL) on both read-write and read-only open paths of the journal store (main-epoch-15)
- Journal-append discipline: additive rows only (ADR-0002); new payload variants extend existing events (e.g., journalAutoLandBlockedExtra adds repanel_count while ~14 other blocked reasons keep byte-identical payload shape); ledger corrections are correction rows with corrects_seq, never silent DB mutation (bug-fix-epoch-17)
