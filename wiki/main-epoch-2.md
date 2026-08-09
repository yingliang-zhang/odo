# Conversation Summary — Odo Dev Session (2026-08-09)

Context: user checked dev status → asked about branches/merge → directed Op1+Op3 of `rename-install-cleanup-brief.md` → asked to roll back the rename.

## Key Decisions

| # | Decision | Rationale |
|---|---|---|
| 1 | **No merge needed** — `odo/main` ⊂ `main` (merge-base = `ac8bed8` = odo/main tip; main ahead 5→7 commits) | odo/main never had commits main lacked |
| 2 | **"Two mains" explained** — `odo/main` is a local work branch with a literal `/` in the name (M11c design), not a remote | Workstreams store bare `main`; git consumers prefix `odo/` (`server.go:453`); run worktrees check out `odo/main` via `CreateWorktreeOnBranch`; accept lands on `main` then `AdvanceBranch` fast-forwards `odo/main` |
| 3 | **Execute Op1 (GitHub rename) + Op3 (cleanup)**, defer Op2 (install to /Applications) | User directive "先完成1，3" against the brief's numbered ops |
| 4 | **Keep tauri identifier `com.yingliangzhang.odo`** | Bundle ID = macOS app identity, not repo name; changing resets permissions + Tauri stores for zero value |
| 5 | **Do NOT rename local dir `~/Projects/odo`** | 7 live worktrees + running daemon reference the path; cosmetic change, no pain point |
| 6 | **Leave historical docs/logs untouched** | Append-only history; only functional references updated (README had no repo URLs) |
| 7 | **Defer `journal.sqlite` reset** | Live daemon (PID 30215) holds SQLite WAL; session writes it in real time — deletion would corrupt running state |
| 8 | **Full rollback of the rename** | User had previously abandoned `odo-agent`; revert, don't rewrite history (`80bd148` kept as append-only log) |

Dev status established up front: **M0–M11 all CLOSED** (tri-model 3/3 ACCEPT each), plus GUI Belts A–D, PR1–3 polish, A1 clipboard fix, A2-lite/A4-lite.

## Code Changes

**Rename (executed, then reverted):**
- `gh repo rename odo-agent --repo yingliang-zhang/odo --yes`; `origin` → `odo-agent.git`
- go.mod module path + 15 Go files rewritten — 16 files, 26 lines, import paths only → commit `cb7bde4`, pushed
- Gates: `go build` ✅ `go vet` ✅ `go test ./...` ✅ (ipc 122.8s)
- Ops logged in `memory/log.md` → commit `80bd148`, pushed

**Cleanup (kept):**
- Deleted 6 stale session+prompt pairs (preserved live session `6a7852d9-bd583ceb9585`), `gui/dist`, `gui/test-results`; truncated `daemon.log`
- `wiki/`, `.odo/ledger.md` already absent

**Incident (self-caused, repaired same session):**
- Bulk `sed` traversed `.odo/worktrees/*`, rewriting all 7 worktree checkouts; reverse-substitution also corrupted line 47 of the brief `.md` in each. All restored; every worktree verified `git status` clean.

**Rollback (final state):**
- `gh repo rename odo -R yingliang-zhang/odo-agent`; `origin` → `odo.git` (old URL auto-redirects)
- `git revert --no-edit cb7bde4` → `753d553`; rollback log entry → `a39825a`; pushed `80bd148..a39825a`
- Gates green again (ipc 122.7s)
- `wiki/main-epoch-1.md` annotated `SUPERSEDED` + decisions marked **ROLLED BACK** to prevent stale recall
- `odo-agent` survives only in 2 historical docs (design brief + log.md) — intentional

**Learned (distill/curate actions):** 4 memory rules + 3 skills proposed (`rename-github-repo-and-go-module`, `reset-odo-journal-safely`, `scoped-bulk-text-replacement`) — MoA reviews mostly ACCEPT (one deepseek-v4-flash truncation failure); indexed to 6 topics.

## Open Questions / Pending

1. **`journal.sqlite` reset — manual, needs Odo fully quit:**
   `cd ~/Projects/odo && rm .odo/journal.sqlite*` (bootstrap recreates empty journal)
2. **Op2 — release build + install to `/Applications`** — deferred by user
3. **`odo/main` lags `main` by 7 commits** (SUDO_CODING_KEY/IME/PATH fixes) — pre-existing state, untouched; `AdvanceBranch` fast-forwards it on next accept
4. **M7 GUI webview E2E (cua-driver)** — still outstanding per log
5. **`steering.txt` write path is dead code** — Adapter interface not cleaned (noted in A2 brief RC8; queue-continuation superseded it)