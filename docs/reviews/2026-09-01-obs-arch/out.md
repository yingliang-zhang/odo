Working...
# Blind Review: Observability Completeness (Part A) + UX-2 Architecture (Part B)

## Part A — Observability completeness

### A1: What's observable but has NO GUI surface

**agent_error (32) — COVERED, reject as a gap.** Renders as error bubble in transcript (`MessageBubble.tsx:489-492`), closes runs with `status:"error"` in Runs tab (`runs.ts:117-118`), and counts into LearningPanel outcomes (`LearningPanel.tsx:51`). No action needed.

**memory_update (958) — PARTIALLY INVISIBLE.** Layers ride `memory/user/learner/curator/index/pins/note/ledger` with causes `apply|rotate|retract|failed|curate|pin` (`types.ts:67-70,166-172`). Covered: memory proposals (Memory tab badge), note retracts (wiki retracted badges, `types.ts:990-992`), learning fold (Learning tab). **Invisible**: `layer:"memory"` rotate/apply on the user's own memory file, `layer:"pins"`, `layer:"ledger"` changes, and `cause:"failed"` — none of these produce a badge or row outside a specific panel. P2: a "recent memory changes" section in Memory tab.

**review_action 14+ actions — MOSTLY COVERED.** Pipeline chip renders `auto_land_started/blocked/revise_round/accept` (`pipeline-chip.spec.ts:126-196`); ReviewInbox covers pending; `run_prompt/parked_goal_dropped/steer_dropped/todo_merge` render as transcript receipts (`mock-invoke.ts:329,346`, TasksPanel). **Invisible**: `distill`/`curate` markers surface only indirectly (wiki note appears), `refresh_attempted` (conflict/rebase outcome, `fixtures.ts:211`) and `moa_review` per-leg details have no timeline view — you see the verdict, not the path. P2: verdict timeline belongs in Review tab detail, low urgency.

**OMP chip — COVERED.** Shows provider count + grievance badge; popover has per-provider usage bars, limits, resets, errors (`StatusBar.tsx:702-772`). Note it degrades to a visible "unavailable" dimmed chip, never hidden (`StatusBar.tsx:851` "always renders — degrades to unavailable") — **this is the precedent D5's "hidden chip" degradation contradicts.**

**Sweeper — INVISIBLE (log-only), confirmed.** All decisions are `log.Printf` lines only (`internal/ipc/sweeper.go:92-145`): reclaimed/kept worktrees, pruned sessions/prompts, retired legacy branches. No journal event, no GUI surface. Mostly benign (janitor work), but "reclaimed N worktrees" after a crash is the user's only signal that state converged — a one-line status in Runs or daemon-health area would do. P2.

**Queue/steer — COVERED.** `Queue · N` chip derived from journal events, restart-safe (`QueueDock.tsx:6-9,88`).

### A2: Not captured anywhere (no event, no IPC) — ranked by user value

1. **Daemon restart mid-conversation** — a restart loses an in-flight run (wiki/UI-epoch-11.md:34 documents `daemon_restart` error lost an in-flight request) and the only traces are indirect. Should be a journal `daemon_lifecycle` event (start/restart/boot-sweep summary) → transcript receipt. **StatusBar/panel-worthy**: a "restarted, N runs interrupted" chip.
2. **Sweeper boot summary** (reclaimed/kept counts) — currently log-only (see A1). Rides the same `daemon_lifecycle` event cheaply.
3. **Disk/quota (CPFS) and OOM-watchdog state** — nothing captures either. But unless an agent action actually failed on it, polling this is scope creep → **CLI-only** for now.
4. **Multi-project cross-bar view** — StatusBar/panel are per-project; nothing shows "3 projects, 2 running". Real user value as workstreams multiply, but needs its own design session, not a bolt-on → **defer, panel-tab-shaped later**.
5. **Distill/curate backoff states** — retry/backoff of the memory distiller is invisible between runs; an `auto_distill` pending marker with next-attempt time would close it → panel-level, P2.
6. **Worktree states between sweeps** — derivable from journal + `WorktreeRefs` (`store/diffs.go:213-217`) but not rendered; Changes tab could show "held worktrees". P2.

## Part B — UX-2 architecture

### B1: kubectl dependency posture

Acceptable for a single-user Mac-laptop agent — `exec.Command` direct, no shell, is exactly the existing posture (`omp.go:322`, with `Setpgid` + `EnrichedEnv()`). BUT two real gaps in the lock as written:

- **PATH**: `EnrichedEnv()` (`omp.go:130-170`) appends homebrew/`~/.local/bin` etc., so Finder-launched daemons will find kubectl. Good — no work needed, just don't regress it.
- **KUBECONFIG**: rides `os.Environ()`; only missing if the user sets it in shell rc and launches from Finder. kubectl's default `~/.kube/config` still works, so real breakage is narrow — but it's silent under the current "degrade to hidden" design.

**Wrong degradation — this is the main fix.** Hiding the chip on failure is indistinguishable from "no cluster / no jobs / feature off", and it contradicts the codebase's own precedent: the OMP chip **always renders and degrades to a visible "unavailable" state with the error in the popover** (`StatusBar.tsx:851,684-686,723`). The k8s chip must do the same: once the namespace is configured, render a dimmed `Jobs · unavailable` with reason (`kubectl missing` / `cluster unreachable` / `auth expired`) in the popover. Visibility of *why* is the difference between a chip and a black hole.

**Alternative postures**: (ii) GUI-side kubectl splits truth across two processes and re-solves PATH in Tauri — reject. (iii) `~/.kube/cache` file-watch is undocumented cache-format coupling — reject. (iv) client-go in the daemon means a Go dep + in-cluster auth config the daemon must now own — overkill for one user, one cluster, read-only. **(i) is right.** Keep the lock.

### B2: Config coupling

**Per-project namespace is required and comes for free.** Settings are already per-project (`~/.odo/prefs.md` read via `project_root`-keyed `get_settings`, `lib.rs:582-585`, `App.tsx:562-566`), so `k8s_namespace`/`k8s_job_selector` in settings is naturally per-repo — different repos → different ns works with zero extra machinery. Keep the lock's shape; just document that it's per-project.

**status.json path (D5b): settings, per project, daemon-validated.** The daemon cannot guess `<work>/status.json` — work dirs live on pods, not on the Mac. Add `k8s_batch_status_glob` (e.g. `/data/batches/*/status.json`) to the same per-project settings; daemon resolves pod → exec `cat` only paths matching the glob (server-side check, no user-passed args, same whitelist posture as D5 Stage 0). **Contract ownership**: batch scripts own *writing*, daemon owns *reading*, and the schema is pinned in `docs/design/` with a `schema_version` field in status.json — not owned by either side implicitly.

### B3: Portability (no-k8s future)

Auto-detect ("chip appears when kubectl exists") is the wrong default: it makes a chip appear from an environment accident, and the pure-local user gets UI they never asked for. **Settings-gated, default off** — chip exists only when `k8s_namespace` is explicitly set for the project. UX cost is one settings field, once; the benefit is the chip's presence *means something* (configured → will show data or a reason, per B1). Together B1+B3 give a clean rule: **configured namespace → chip always renders (data or reason); unconfigured → no chip.** This portability question then costs nothing.

### B4: D5b batch bridge honesty

Four real problems, one minimal contract fixes all:

- **Pod name hardcoding** — footgun; pods are ephemeral. Resolve the pod per-read via the job/label selector (D5 Stage 0 already has the selector), `kubectl get pods -l <sel> -o jsonpath` → exec cat on the running pod. Never a stored pod name.
- **CPFS vs pod-local path** — the status file must live on the shared CPFS mount, not pod `/tmp` (the maxinsights `/tmp` lesson). Make the glob setting the *shared* path; reject pod-local tmp paths in validation.
- **Staleness** — status.json MUST carry `batch_id`, `started_at`, and a `updated_at` heartbeat the batch script touches every loop. Daemon shows "stale (> N× interval)" instead of a progress bar when `now - updated_at` exceeds threshold. A frozen file is **unknown**, never progress.
- **Crash mid-batch** — same mechanism covers it: heartbeat stops → stale badge; don't infer "done" from absence.

Also: stage list should be enumerated in the contract (transcode/push/verify…), not freeform strings, or the GUI renders garbage on the first script change.

### B5: GUI design

**One Jobs tab, two sections — yes.** The strip is already at 10-11 tabs with a known width squeeze (D2's W3 bug class: 616px strip vs 579px clientWidth); an 11th/12th tab for batches worsens it. `Jobs` tab = jobs table on top, batches section below (each batch: N/M progress bar, rate, ETA, stage, stale badge). **Progress bars do NOT belong in the chip** — the chip is glanceable count, not a meter (`Jobs · 3` / batch count); overflow rank must place it in the alarm tier (rank 3, beside `running`/`pipeline`), not rank 5, because a stuck batch is actionable. **What Tasks tab does NOT cover** and Jobs must: external cluster state (jobs are not todos — no journal SSOT, different lifecycle), batch progress/ETA (todo items have no rate), and cross-run aggregation. The two tabs must not merge — Tasks is agent-plan truth, Jobs is world truth; merging them re-creates the "external state in the journal" violation D5 explicitly forbids (`ux-batch-lock-2026-09-01.md:55-57`).

---

## VERDICT: **ACCEPT_WITH_FIXES**

The D5 posture (daemon exec kubectl, read-only, polled IPC, never journal, staged) is correct and consistent with the repo's own precedents. Fixes:

**P0**
1. Chip degradation: `kubectl-absent → hidden` is wrong. Once `k8s_namespace` is configured, the chip always renders; failure degrades to visible "unavailable + reason" (OMP-chip precedent, `StatusBar.tsx:851`). Unconfigured = no chip (B3 default-off).
2. D5b staleness contract: status.json requires `batch_id`/`started_at`/`updated_at` heartbeat; stale → "unknown" badge, never a frozen progress bar. Pod resolved by label selector per read, never hardcoded.

**P1**
3. Batch status path = per-project settings key `k8s_batch_status_glob`, CPFS-mounted paths only, server-side whitelist; schema + owner documented in `docs/design/`.
4. Put the `Jobs` chip at overflow rank 3 (actionable tier); document KUBECONFIG-reaches-daemon in the D5 risk note (lock already flags it — make it a concrete check: verify `EnrichedEnv` passes it through and note the Finder-launch caveat).

**P2**
5. Jobs tab = jobs table + batches sections (single tab); memory rotate/pins/ledger changes surfaced in Memory tab; sweeper summary + daemon restart as a lightweight lifecycle journal event.

**The single most important thing**: a configured surface must never fail silently — the chip that can disappear into "hidden" on error will be trusted when it shows nothing, and observability that lies by omission is worse than none.
