# UX-2: k8s_status IPC, Jobs status-bar chip, and UX-3a failure notification

Implemented per `docs/design/ux-batch-lock-amendment-a2.md` (§A2-1..A2-5 + §A2-6a; UX lock base `docs/design/ux-batch-lock-2026-09-01.md` §D5, quad-blind 4/4 accepted). Delivered in a dedicated worktree: 24 files staged (7 added / 17 modified, +1555/−17), worktree left dirty, zero commits. UX-2b (Stage-1 jobs tab) deliberately excluded to keep the diff small.

## Key decisions

**Daemon (Part 1, A2-1/2/3)**
- Feature gate: empty `k8s_namespace` → `{available:false, reason:"off"}` immediately, zero exec. Three new settings keys (`k8s_namespace`, `k8s_context`, `k8s_job_selector`) are read-only over IPC; `update_settings` never writes them (`loop_notify_on_complete` precedent — prefs.md is hand-edited).
- Response mirrors kubectl `get jobs,pods -o json` slices (swap-friendly): `{available, reason, jobs, pods, truncated, fetched_unix}`. Reason classes: `off | bad_namespace | ENOENT | timeout | auth | unreachable`; reason is never absent.
- Fail-loud validation order: namespace charset `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` → `bad_namespace` (no exec) → `exec.LookPath` for kubectl → `ENOENT`.
- Exec: `exec.CommandContext` 10s, argv-only `kubectl get jobs,pods -n <ns> -o json [--context v] [--selector v]`, `EnrichedEnv()`, 1KB stderr cap, never journaled. Kind split is raw passthrough; empty selector → 50-row hard cap + `truncated:true`; non-empty selector → direct query, no cap.
- Error classification: timeout (ctx deadline) / auth (Forbidden|Unauthorized) / unreachable (default).
- `enrichDaemonEnv` now extracts `KUBECONFIG` from `~/.zshrc` (`ExtractExportFromZshrc`, same pattern as the SUDO_CODING_KEY scrape).
- Fixed a nil-slice → `"null"` JSON serialization pitfall in the handler.

**Bridge + GUI (Part 2, A2-1/5)**
- New `k8s_status` Tauri command in `lib.rs` (mirror of `pending_counts`), `k8sStatus()` in `api.ts`, K8sJob/K8sPod/K8sStatus types in `types.ts` following kubectl-native schema.
- StatusBar `jobs` chip at OVERFLOW_RANK 3 (actionable tier; rank pinned by test). States: pref off → no chip (renders null, takes no collapsed width); unavailable → dimmed visible "Jobs · unavailable" + reason in popover title + last-good snapshot with age; ok → "Jobs · N [+ n batches]" count-only. No progress bar in the chip face. Error text capped at 240 chars.
- Poll: fetch on mount, then 5s interval; stops only when off (A2-1's no-polling guarantee). Broken keeps polling for recovery detection.
- Popover rows: name, phase, age, completions — not clickable (tab arrives in UX-2b).
- New `gui/src/jobs.ts`: pure derivation module (phase/age/completions/active count/chipLabel/reasonLabel).

**UX-3a (A2-6a)**
- `notify.ts` refactored to a shared `sendRunNotification` channel + `__odoRunNotify` e2e seam (fires before the hidden-gate; `__odoLoopNotify` precedent). `notifyRunFailed`: title "run failed in ws", body = error first line, 80-char truncation.
- `App.tsx recordEvents`: `agent_error` fires the failure notification unless `payload?.odo === true` (journalRunAdvisory) — first GUI consumer of that flag.
- Finished-flash ✗ tint: `SwitchCache.terminalError(root, wsId)` scans already-held poll events (skips `payload.odo` lines, truncated tail → false, doesn't touch LRU); `bgNotice.finished` restructured to `{id,name,errored}[]`; new `.bg-flash-error` mirrors `.bg-flash-done`. Zero new IPC.

**e2e**
- k8s_status scenarios injected via `?k8s=` query param, read once at fixtures module load (off / unavailable-ENOENT / ok-with-2-jobs). off→on transition intentionally not reproducible; statusbar.spec covers only the ok→broken degradation.
- Two initial Go test failures (mock's 60-item output not parsed) were reproduced locally and fixed before landing.

## Code changes
- New files include: `internal/ipc/k8s.go` (handler), `gui/src/jobs.ts` (+ its 12 unit tests), `notify.test.ts`, k8s_status e2e fixtures, run-notify/failure-notification e2e spec.
- Modified: `internal/ipc/protocol.go` (`CmdK8sStatus` + `K8sStatus` struct + response fields), `internal/ipc/settings.go` (3 keys), `internal/ipc/main.go` (KUBECONFIG), IPC dispatch registration, `gui/src-tauri/src/lib.rs`, `gui/src/api.ts`, `gui/src/types.ts`, `StatusBar.tsx` (JobsChip), `App.tsx`, `notify.ts`, e2e `statusbar.spec.ts` (3 chip states) and `background-runs.spec.ts` (error tint), statusbar vitest (rank pin assertion).

## Verification
| Gate | Result |
|---|---|
| go build / go vet / gofmt | clean |
| `go test ./internal/ipc/ -run 'K8s\|Settings'` | ok 7.7s — 9 new tests (off, bad_namespace, ENOENT, kind split, truncation, selector, context, timeout, reason classes) |
| `tsc --noEmit` | exit 0 |
| vitest (full) | 39 files, 458/458 passed (new: jobs 12, notify 6, terminalError 6, rank pin) |
| Playwright statusbar + run-notify | 6/6; 3 consecutive runs 18/18 |
| Playwright background-runs + boot + panel | 13/13 (incl. new ✗ tint) |
| Playwright overflow + chat + switch-cache | 9/9 (extension regression) |
| error-tint stability | 3 consecutive runs 3/3 |

## Known limitations (documented tradeoffs)
- `off` is sticky for the page lifetime; hand-edited prefs.md requires a reload — the price of no polling while off.
- Broken chip keeps 5s polling (recovery detection; not prohibited by A2-1).
- `terminalError` tints never-viewed workstreams and truncated tails as ✓ — unknown is not faked as error.
- e2e `?k8s=` read once at module load; only ok→broken degradation covered.

## Environment
- `gui/node_modules` APFS-cloned from the main checkout into the worktree (no install needed). Dev server stopped after use; port :1420 released.

## Open loops
- Staged diff (24 files, +1555/−17; worktree dirty, zero commits) awaits user review/landing.
- UX-2b Stage-1 jobs tab (clickable popover rows → jobs view) deferred per spec; not in this diff.