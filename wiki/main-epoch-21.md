> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo Panel Review: Diff #36 Rejection, Stale Daemon Binary, and Three-Round Rule Implementation

## Context

Diff #36 was auto-rejected by the auto-land panel (`panel_mixed` split verdict). The user asked whether the panel was operating correctly, then ordered a binary rebuild, then defined the target panel semantics: **3 total review rounds — rounds 1–2 require unanimous accept (with audit-review-loop fixes in between); round 3 passes on majority.**

## Key findings (diagnosis)

1. **Panel mechanics were fully healthy** for #36: verify passed (seq 8510→8516), all 3 legs voted (kimi-k3 reject / glm-5.2 accept / deepseek-v4-flash accept), zero infra errors, reject persisted to journal (seq 8517/8519), patch retained on disk (`.odo/diffs/6a89a6d3-a8d167d76d7c.diff`, 57KB, 8 files), daemon PID 6068 stable for 19h36m.
2. **The running daemon was a stale binary.** Epoch-20's claim "new daemon binary built (=954ff22)" was false:
   - `go version -m` on the live daemon → `vcs.revision=5ec522bd+dirty` (diff #27-era code, predating #32/#34).
   - Live DB remained `schema_version=2` with no `diffs.goal` column; the v3 migration code was correct but never ran — proving v3 code was absent from the process.
   - The "new" binary built 2026-08-20 23:51 was never replaced; it was built from #27+dirty all along.
3. **Consequences of stale binary:** #36 was auto-rejected under the OLD one-vote-veto rule (`panel_mixed`); under the majority rule #36 itself carried, 2:1 would have landed. The goal-anchor bug fix from 954ff22 (the fix whose absence caused #34's wrongful rejection) was also not live.
4. **kimi leg's objections were substantive governance items, not malfunction:**
   - Majority vote abandons the fail-closed one-vote-veto property; `TestSettleMixedRejectAutoRejects` had been deleted in favor of a ladder variant, leaving the old property unguarded.
   - A truncated leg still counted in denominator N (2 accept + 1 truncated → 4>3 lands); old threshold required `truncated==0`; only `continue` abstention was added without shrinking N.

## Actions taken

### 1. Binary rebuild + daemon restart (completed pre-kill)
- Built from main checkout at `954ff22`; verified `vcs.revision=954ff2231a7b…` (`+dirty` from wiki docs only, zero `.go` modifications).
- Followed wiki deployment rule: **replace binary first, then kill daemon**. Atomic cp-to-temp+mv of `<project>/odo` and `~/.odo/bin/odo`; all three SHAs match `eccc4944…` (old: `134261d1…`).
- Launched detached restart script `/tmp/odo-restart-final.sh` (log `/tmp/odo-restart-final.log`) with 5s delay: SIGTERM 6068 → 60s grace → SIGKILL; restart from `<project>/odo`; verify new PID, `odo.sock`, **`schema_version=3`, `diffs.goal` column** (behavioral proof new code is live).

### 2. Three-round panel rule (implemented, verified)
- **Discovery:** main baseline already had a majority-accept valve at the cap (settle.go, 2026-08-22 security cut): ≥2/3 accept + zero reject + zero infra + zero truncated, with gate-source diffs permanently excluded; it just fired at the 4th evaluation.
- **Core change:** `settleMaxReviseRounds 3→2` (one-line constant) plus synchronized comment/head-note updates. Resulting semantics:

| Evaluation | Rule |
|---|---|
| Round 1 (original diff) | unanimous accept → land; any reject → auto-reject (fail-closed, unchanged) |
| Round 2 (revise 1) | same (fail-closed, unchanged) |
| Round 3 (revise 2, terminal) | majority valve → `majority_accept` evidence persisted, then land; else suspend → human |

- **kimi objections adjudicated:** ① majority exists only in the terminal round; first two rounds keep veto. ② truncated leg invalidates the valve (conservative fail-closed default retained — no instruction to relax).
- **Changed files (8, +173/−102):** `settle.go` (constant + 4 comments incl. 2026-08-23 doctrine note), `autoland.go`/`loop.go` head notes, README ×2, `docs/design/auto-land-zero-land-zero-manual-lock.md` (dated amendment), GUI dev fixtures chain (removed impossible round-3 row), e2e fixture text, `settle_test.go` (rewrote 3 cap tests, unskipped the RoundCap test, **new `TestSettleMajorityValveLandsAtCap`** — the valve's land path previously had zero test coverage; test asserts `majority_accept` moa row precedes land, round-2 product lands, prior diffs superseded, ladder not suspended).
- **Verification (all green):** 4 changed tests PASS; full `go test ./...` 7 packages ok (ipc 483s); `go vet`/gofmt clean; GUI tsc 0 errors + vitest 110/110; playwright pipeline-chip 11/11.

## Notable semantics

- The valve's only reachable composition is `{2 accept, 1 needs_fixes}` — a reject leg auto-rejects before the ladder, and an infra leg blocks before evaluation. "Majority passes" therefore applies strictly as specified.

## Open loops

- Restart verification pending: confirm `/tmp/odo-restart-final.log` shows new PID, `schema_version=3`, and `diffs.goal` column (script was in-flight when the session tree was killed).
- This conversation's cap 3→2 diff was `auto_land_blocked` with `reason=verify_failed` at session drain (seq 8850) — diagnose the verify failure and resubmit; even after panel passes, landing requires manual Accept (`settle.go`/`autoland.go` are protectedGateFiles).
- Truncated-leg denominator policy unresolved: valve currently fails closed on any truncated leg; user decision needed if this should ever be relaxed.
- #36 is superseded by the cap 3→2 change — patch remains on disk as history; confirm no resubmit is wanted.