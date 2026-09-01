> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo — UX-3b/c flake re-apply and D5b multi-namespace K8s Jobs tab + batch bridge (2026-09-01)

Two sequential one-shot tasks in the Odo auto-land pipeline: re-apply a flake-rejected patch (diff #139 content), then implement the locked D5b UX batch as a single pipeline diff.

## Task 1 — Re-apply UX-3b/c (advisory amber styling + backoff surface)

**Situation:** The designated worktree `.odo/worktrees/6a96a8a0-2898f52b180e` no longer existed (swept). The active worktree `6a96c931-5ffd32ac1b94` was a clean tree at HEAD `6dd539c`; diff #139's base `644ccb6` is an ancestor, so replay was safe.

**Decisions and outcome:**
- Replayed verbatim from the journal archive `.odo/diffs/6a96a8a0-2898f52b180e.diff` (diff #139, status=rejected for an unrelated load flake only) using `git apply --3way` — zero edits, per the flake-blocked-autoland-reapply skill. No re-derivation of the patch.
- Staged exactly 12 files, +496/−16, byte-identical to the #139 review (the task text said "8 files dirty"; the archived diff's true scope is 12): `advisory-events.spec.ts`, `App.tsx`, `ChatSurface`, `MemoryPanel`, `MessageBubble`, `memory_backoff.test.tsx`, `messagebubble.test.tsx`, `memory.test.ts`, `memory.ts`, `runs.test.ts`, `runs.ts`, `types.ts`.
- Fast gates only (bytes unchanged, per skill convention): `go build ./...` exit 0; `git diff --cached --check` clean; `git status --short` showed only expected files; after APFS-clone of `node_modules` (`cp -Rc`), `tsc --noEmit` clean.
- Did NOT rerun vitest/e2e: the patch had already passed the full suite 484/484 twice plus e2e 3/3 in epoch-53; the original blocker (`app_journal_search` 5s timeout under tauri-build+playwright concurrency, pitfall #42 flake class) is closed by `.odo-verify` now running vitest with `--maxWorkers=2`.
- Tree left staged-dirty, zero commits; new diff row created by the daemon's `drainRun` (`git add -A` → `ExtractDiff` → `InsertDiff`). Auto panel accepted the run (seq 22194).

## Task 2 — D5b: multi-ns K8s status, batch progress bridge, gated Jobs tab

Locked scope from `docs/design/ux-batch-lock-amendment-a4-multi-ns.md`, `a2.md`, and `ux-batch-lock-2026-09-01.md` §D5+A3. Worktree at HEAD `bbb8002` (main, clean).

### Key decisions (locked constraints honored)
- Degradation contract verbatim everywhere: *data may be absent, the reason may never be absent*. off-by-config ⇒ no chip/tab/polling; on-but-broken ⇒ visible with per-namespace reason rows. No third chip state — partial availability stays a healthy chip with degraded ns rows.
- Strip stays 9 static contributions; the conditional 10th "jobs" entry gates on `k8sConfigured` (`k8s_namespace` setting non-empty) exactly like the chip; a stored "jobs" tab fails the `PANEL_TAB_IDS` localStorage allowlist when k8s is off and lands on "tasks" (accepted behavior).
- At most ONE `k8s_status` poller app-wide: state lifted to App via a shared hook; JobsChip becomes a pure consumer; poll gate = `docVisible && (chip visible || jobs tab mounted-active)`; `k8s_batch_status` rides the same hook/gate. Folded chip + closed tab ⇒ zero forks.
- Progress bars render in the Jobs tab only; chip face shows counts only.
- kubectl discipline: `get` only; `exec cat` is the only pod-file read verb; `--all-namespaces` forbidden (A4 D2, rejected 4/4); shared 10s `context.WithTimeout`; EnrichedEnv + LimitReader stderr cap reused verbatim; no migration needed ("lab" ≡ `["lab"]`, `""` stays off).
- `schema_mismatch` batch rows use the **filename** as the row label (a mismatched schema's field names are untrustworthy) and sort to the bottom (no timestamp).

### Code changes
**Daemon (Go):**
- `internal/ipc/k8s.go` — rewritten to multi-ns parallel fan: comma-split/trim/drop-empty-only parsing; fail-loud `bad_namespace` (offending element(s) in Detail) and N>5 cap before any exec; whole-response `exec.LookPath` check before the fan; WaitGroup fan of per-ns goroutines; `Namespaces []K8sNsStatus` in configured order (`{name, ok, reason, detail, job_count}`); flat-merged `Jobs`/`Pods`; `Truncated` = OR across ns (50-row cap stays per-ns); `Available` true when ≥1 ns answers; `s.k8sTimeoutForTest` preserved.
- `internal/ipc/k8s_batch.go` — new `handleK8sBatchStatus`: local-mount `*.json` (depth 1) read priority; per-read pod fallback via `k8s_job_selector` label query (0 → `pod_not_found`, >1 → `ambiguous_pod`, empty selector + no dir → `no_pod_selector`); `schema_version == 1` gate with visible `schema_mismatch` rows (files never dropped); daemon computes `stale = (now - updated_unix) > 90s` and ships both; batches sorted `updated_unix` desc; 25-row cap.
- `internal/adapter/settings.go` — `K8sBatchDir` field + `ReadSettings` (`LoadPrefsRaw("k8s_batch_dir")`) + `UpdateSettings` non-empty-write branch.
- `internal/ipc/protocol.go` — `CmdK8sBatchStatus` const, Response fields, `Namespaces` on `K8sStatus`; Settings aliases the adapter shape.
- `internal/ipc/server.go` — dispatch case; `gui/src-tauri/src/lib.rs` — tauri command + `invoke_handler` registration mirroring k8s_status.

**GUI:**
- `gui/src/types.ts` — `namespaces?: K8sNsStatusRow[]` on `K8sStatus` + batch row types.
- `gui/src/jobs.ts` — pure per-ns active-count derivations from flat jobs; batch/badge derivations.
- New App-lifted shared hook for k8s + batch snapshots (single poll fan per tick).
- `gui/src/api.ts` — `k8sBatchStatus(projectRoot?)`.
- `gui/src/components/StatusBar.tsx` — JobsChip as consumer: in-flight ref guard on fetchStatus (pre-existing defect, required by A4 D6), pollNow on popover open, per-ns header rows ("lab · 12 jobs" / dimmed reasonLabel + capped detail tail), batch one-liners under a divider (`transcode 72% · ETA 8m · stale 2m`), `onOpenJobsTab` → App `openTab("jobs")`. Existing off/absent/broken/healthy chrome unchanged; off-latch stays sticky per page lifetime.
- `gui/src/components/JobsPanel.tsx` — new: flat jobs table (namespace | name | phase | age | completions), default sort configured-ns order then age newest-first, client-side ns quick-switcher within the configured set only, batches section with true N/M progress bars + rate + ETA (hidden when `rate_per_min <= 0`) + staleness grey.
- `gui/src/contrib.ts` — exported `K8S_CONTRIBUTION` (id "jobs", **Container** icon); `PanelTab` union widened to include "jobs"; `PANEL_TAB_IDS` stays static 9.
- `gui/src/App.tsx` — `k8sConfigured` derived from the same settings read; conditional contributions strip; tab badge derived from lifted state (PanelBadgeInput extended with activeJobs/activeBatches; static-9 badge assertions unchanged).
- `gui/src/components/SettingsPanel.tsx` — Kubernetes block in General: `k8s_namespace` as add/remove chips input (round-trips to comma string on save) with per-chip RFC-1123 + N≤5 visible errors; plain text inputs for `k8s_context` / `k8s_job_selector` / `k8s_batch_dir`.
- `gui/src/dev/fixtures.ts` / `gui/src/dev/mock-invoke.ts` — two-ns fixture (default + lab, `metadata.namespace` on jobs) + `k8s_batch_status` mock (running ~72%, stale, done).

**Docs:** `docs/design/d5b-batch-status.md` (initially created without the `.md` extension, fixed) — pins status.json schema, atomic tmp+rename write, heartbeat cadence, ownership split (scripts write / daemon reads / schema_version gates upgrades), stage-vocabulary guidance, 90s staleness rule, RBAC honesty paragraph.

### Tests added/extended
- Go `internal/ipc/k8s_test.go`: multi-ns split, per-element `bad_namespace` naming the offender, N>5 fail-loud, shared-deadline timeout on both ns legs, partial failure ⇒ `Available:true` + degraded row, flat-merge order, per-ns cap + OR'd `Truncated`. New `internal/ipc/k8s_batch_test.go`: local-dir read, schema_mismatch row (bottom-sorted, filename label), stale math via `updated_unix`, off case, pod-fallback classes via mock-kubectl argv log (asserts `-- cat <path>` and the only-cat verb).
- Vitest: jobs.ts multi-ns/batch derivations; shared hook; StatusBar in-flight guard (two rapid polls ⇒ one invoke) + pollNow on open + per-ns rows + batch one-liners; SettingsPanel chips round-trip; contrib ≤9 static + jobs absent when gated off.
- E2e: context-panel-tabs keeps the k8s-off nine-tab/no-arrow scenario unchanged and adds a `?k8s=ok` ten-tab scenario (assert tab count + jobs clickability, not no-arrow); statusbar extended for per-ns rows + batch lines + click-through to Jobs tab.

### Process notes
- One atomic multi-op edit failed mid-application; recovered per the atomic-multi-op-edit-recovery skill (re-read exact regions, resent). Follow-up fix removed leftover garbage in `k8sBatchFindPods`. One `jobs.test.ts` edit was rejected by the edit engine and resent with a corrected anchor payload.
- **New pitfall found (root cause):** vitest runs without `globals: true`, so testing-library does NOT auto-cleanup between tests in a file — stacked panel instances caused "multiple elements matched". Fix: explicit `cleanup()` in the three affected test files.
- `:1420` confirmed free before starting the worktree dev server (pitfall #56 — `reuseExistingServer` reusing a stale tree); server started via hub and stopped after e2e.
- `node_modules` provisioned in the worktree via APFS clone (`cp -Rc`).

### Verification evidence (as captured)
- Existing Go k8s tests green (single-ns behavior byte-identical after the rewrite).
- `tsc --noEmit` green after iterative fixes.
- Targeted vitest regressions 49/49 green; new tests 15/15 green; full-suite run surfaced one wrong test expectation (`lastGoodAge` phrase already ends in "ago" — module convention; implementation was correct), fixed and re-run.
- Two affected e2e specs executed against the worktree dev server; their captured output is elided from the transcript.
- All changes staged (`git add -A`), tree left for the pipeline; `.odo-verify` untouched (supply-chain gate).

## Open loops
- Confirm the D5b pipeline verdict: full gate run (go build/vet/test, tsc, vitest `--maxWorkers=2`, playwright) plus the final "files touched + test counts" report — the e2e results and final report text are not visible in the conversation record; verify the diff landed green.
- Confirm the UX-3b/c re-applied diff (from #139 content) actually landed — auto_panel accepted at seq 22194, but the land/merge itself was not observed.
- Live-cluster smoke of `k8s_batch_status` (local-dir read and exec-cat pod fallback) and the multi-ns fan — currently covered only by mock-kubectl harness tests and dev fixtures.
- Decide whether the vitest-globals / testing-library no-auto-cleanup pitfall should be persisted as a reusable skill note (auto_distill fired; curator skipped).