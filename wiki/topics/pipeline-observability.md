# Pipeline Observability, Journal & Recovery

- `auto_land_started{stage:verify|panel}` journal rows expose the in-flight stage with no new event types; rows are excluded from distill prompts and render only as chip labels ("verify running..." / "panel reviewing..."), with unknown stages degrading to generic in-flight (bug-fix-epoch-1)
- Legal silent window: verify 10-min timeout + per-leg `TimeoutForModel` = 1446s gives a true-stuck bound of ~35 min/phase; the older "~61min" figure was pre-P1#9 escalation-stack residue and is superseded (main-epoch-23)
- Restart recovery dedup (Plan A): terminal rows (blocked with reason != panel_infra, auto_panel moa_review, auto_revise_round) skip re-fire; `auto_land_started`/`refresh_attempted` breadcrumbs, panel_infra, and human reviews still re-fire as genuinely stranded; journal read failure fails closed and abandons the whole pass (UI-epoch-9)
- Observability gap (open): `capDetail` keeps the first 4KB of an 8KB tail while `go test` `--- FAIL` lines live at the tail end, so `verify_failed` blocked rows never name the failing test — only reproduction reveals it; follow-up diff proposed (main-epoch-25)
- Prompt token estimate fixed: ASCII/4 + 1 token per non-ASCII rune (chars/4 underestimated CJK ~3x, the 87K breaker cause); loop spawn `prompt_tokens_est` still uses chars/4 as a known open loop (main-epoch-23)
- Journal read-path narrowing: `LatestFoldBoundary` single-row probe + windowed ListEvents, with no-pin marker falling back to full history so pre-pin legacy batches stay rescuable (main-epoch-23)
- Store-layer contracts pinned: `CommitFold` epoch bump + marker in one transaction; `SearchEvents` ESCAPE '\\' + `ORDER BY e.id DESC` (created_at second-resolution ties); marker LIKE queries use `jsonPairMatch/jsonPairNegate` dual forms (main-epoch-23)
- Contradiction pass skips notes carrying the `supersededBanner` prefix so curator-stamped notes stop re-flagging (main-epoch-23)
- Escalation ledger: `Escalation.InputTokens` records the re-paid prompt cost of abandoned requests on each max-tokens bump (main-epoch-23)
