# ODO Review Rounds: E2E spec revision + 8-finding security/perf fix batch

## Round 1 — Panel review of `gui/e2e/advisory-slash.spec.ts`

Three findings adjudicated; one partially adopted, two rejected after source audit.

| Finding | Verdict | Basis |
|---|---|---|
| Comment date is a hallucination | Partially adopted | Today actually is 2026-08-24 (glm reviewer clocked 2025-07-18); kept the claim but rewrote attribution to verifiable evidence: journal seq 11099 (`auto_land_blocked reason=verify_failed`), 117-spec at 5.1min, `expect(spinner).toHaveCount(0)` in the same file, standalone 6/6 + repeat-each 5/5 |
| POLL applied inconsistently | Rejected after audit | `panelThinking` is a local counter (App.tsx:968/1002), fixture never injects `panelProgressState`, error-banner/textarea restore are RPC-local — none ride poll ticks; tagging them would mask mechanism. Instead the comment now states the scope rule (poll-loop → 12s; RPC-local → 5s default, with App.tsx references) |
| Timeout relaxation unbounded | Rejected | 12s is the pre-existing REFRESH convention across five sibling specs (parked-goals, steer-queue, background-runs, review-inbox, sidebar); adding a second convention was refused |

**Change:** comment block rewrite + 15 poll-dependent assertions tagged `POLL = { timeout: 12_000 }` (+36/−15). Audit confirmed the 15 cover the entire poll-dependent surface (journaled bubbles, heartbeat N/M, `.panel-leg`, queue rows, test 1 spinner settle); test 5 (untouched-composer restore) is fully RPC-local and correctly untouched.

**Verification:** `npx tsc --noEmit` exit 0; `playwright test e2e/advisory-slash.spec.ts --repeat-each=3` → 18/18 passed (52.2s).

## Round 2 — 8 findings (security + performance), all confirmed real

Verified against source before dispatch; P0 had more same-class sites than the report listed. Five parallel subagents, conflict-serialized: Agent A owned `server.go` + `internal/git`.

### Key decisions

- **P0 containment scope:** only project-side paths are constrained; global `~/.odo` files (user.md, pins) are outside the threat model — restricting them would break legitimate dotfiles symlinks. `wiki/` is a git-committable surface, so it's in scope.
- **Escape semantics:** containment violations degrade to `vanished`/`absent`, not new error faces.
- **#2 approach:** pre-commit per-file byte-level comparison — build post-image via a temp index, compare against worktree `hash-object` (whole-file `CommitPaths` would otherwise absorb stray edits).
- **#3 v1 boundary:** capture runs through `npx playwright screenshot` CLI, so browser-layer interception is impossible; fixed via Go-side per-hop redirect validation + final-URL capture. JS/meta-refresh redirects remain unblocked — documented in header comment as v1 boundary.
- **#4:** semantic equality check on diffs (not reference) + `memo` + froze 8 unstable props.
- **#5:** keep-alive via conditional-mount removal; MemoryPanel deep-link handled with a nonce scheme to preserve `initialTab` semantics.
- **#7:** write failure on IPC treated as terminal (no retry) to avoid duplicate daemon execution.

### Landed changes

| Finding | Fix | Verification |
|---|---|---|
| 1 (P0) symlink prompt injection | Containment helper + 11 files (curator ×3, recall_cross ×2, memory_autogate, learner ×4, readArchive, recall/index/memory/pins/wiki/skill readers) | 12/12 targeted Go tests |
| 2 alreadyLanded stray-edit commit | Temp-index post-image vs worktree hash-object byte compare | (Agent A, server.go compiles) |
| 3 /preview redirect bypass | Per-hop redirect validation, final URL capture, `final_url` audit field; behavior change: unreachable hosts now fail-fast at probe | 14/14 preview tests |
| 4 350ms full-App re-render | Diffs semantic equality + memo ChatSurface + prop freezing → quiet tick = zero re-render | 126/126 vitest |
| 5 tab-switch unmount | Six-panel keep-alive + memory deep-link nonce | same vitest run |
| 6 read_file Stat→ReadFile race | Part of Agent A slice | — |
| 7 IPC write-fail retry | `lib.rs` write failure → terminal error | cargo check |
| 8 tab-bar overflow (94px) | TabStripOverflow agent landed UI fix | see open loop on e2e failures |

### GUI recommendation ordering (user-supplied plan, status)

1. Stable polling state, ChatSurface memo, windowing — **partially done** (equality + memo; true windowed list not yet).
2. Keep-alive tabs, consolidate 6 tabs → 3–4 + More — **keep-alive done; consolidation not done**.
3. Composer toolbar merge, status-bar noise reduction — not started.
4. Visual rework (bubble area, reading flow, 12–13px secondary text) — not started.
5. WKWebView perf (backdrop-filter, transform/opacity-only animation) — not started.

## Open loops

- Root-cause the 2 full-suite e2e failures reported by the TabStripOverflow agent (`review-inbox:69`, `switch-cache:64`). Its "unrelated to changes" conclusion only reverted a ContextPanel.tsx re-test, so a regression from the App.tsx keep-alive restructure cannot be excluded; baseline experiment + full verification was in progress at transcript end.
- `/preview` residual risk: JS/meta-refresh redirects bypass the Go-side per-hop validation (accepted v1 boundary, documented in preview.go header).
- `advisory-slash.spec.ts` fragility: test 2's `Panel consulting` assertion assumes the fixture never injects `panelProgressState`; if progress injection is added the visibility becomes poll-gated and needs POLL (documented in comment).
- Dirty same-path edit rollback: already-landed gap closed this round; the concurrent-save window gap from the earlier recheck was not addressed in this batch.
- GUI plan items 3–5 (status-bar/composer consolidation, visual rework, WKWebView perf work) remain unstarted, awaiting user prioritization.