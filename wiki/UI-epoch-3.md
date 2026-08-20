> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo GUI: Install Verification & Status-Bar Icon Fix (2026-08-19)

## Context
Three related incidents around the Odo Tauri app deployment and a UI regression in the status bar, all resolved against main checkout at `~/Projects/odo`.

## Decisions & findings

### 1. "Install failed" report — actually succeeded
User reported the app install failed while in use. Evidence showed otherwise:
- `/Applications/Odo.app` binary SHA-256 matched the bundle artifact exactly; `diff -rq` of the full Contents tree showed no differences.
- Running process (pid 36089) started at 17:14, *after* the 17:13:57 `ditto` copy — so it was already running the new code (`ffd2b0d`, chat-column + IME composer).
- Only anomaly: 4 leftover `rw.*.Odo_0.1.0_aarch64.dmg` temp files in `target/release/bundle/macos/`, indicating retried dmg packaging. The pipeline only consumes `.app`, so no real loss. Leftover dmgs were cleaned.

### 2. Status-bar "running" icon rendered above text instead of left
- **Root cause**: Tailwind preflight sets all `svg` to `display:block`. The `status-run` chip's spinner svg is a direct child of a span (non-flex parent), so the block-level svg took its own line, stacking above the "running" text. Same root cause hit a second victim: the `Check` icon in the path-copy button's 2s "Copied!" feedback. Chips built on `STATUS_BADGE` were unaffected (it carries `inline-flex`).

**Fix** — `gui/src/components/StatusBar.tsx`, commit `85eaef8`:
| Location | Change |
|---|---|
| `status-run` chip | Added `inline-flex items-center gap-[3px]` (matching `STATUS_BADGE` convention); duration span's `ml-[2px]` superseded by `gap` |
| `status-fact-btn` | Same flex fix; text moved into a `min-w-0 truncate` child span to preserve `text-ellipsis` on long paths (bare text in a flex parent can't ellipsize) |

**Verification**: dev mocks forced `agent_running` → pre-fix chip was 28px tall with block svg on top; post-HMR 17px single-line, icon left and vertically centered, confirmed by screenshot. fact-btn verified live with clipboard permission. `tsc` clean, vitest 57/57.

**Deployment**: FF merge `ffd2b0d..85eaef8` into main → `tauri:build` → `ditto` over `/Applications/Odo.app` → restart (new pid 40541, binary SHA matched bundle). Two independent daemon processes (27946/31371) untouched; running agent conversations unaffected.

### 3. Post-restart confirmation
After an unexplained app restart, user asked whether the fix was in main. Confirmed:
- Main checkout HEAD = `85eaef8`; `git branch --contains` → `main`.
- Working tree dirty only in `wiki/` notes (memory distillation writes); `gui/` source clean — no uncommitted work to lose.
- Installed binary SHA-256 (`61b3221b…`) identical to bundle; running GUI pid 40541 maps to `/Applications/Odo.app/Contents/MacOS/odo-gui` — i.e., the fixed build.
- The restart relaunched from the same installed binary; no code was lost.

## Recurring pattern
Verification method established for deploy questions: compare installed binary SHA-256 against `target/release/bundle` artifact + check running pid's `txt` mapping + confirm main HEAD. This gives a definitive answer to "is the installed app the latest code?" without rebuilding.

## Open loops
None.