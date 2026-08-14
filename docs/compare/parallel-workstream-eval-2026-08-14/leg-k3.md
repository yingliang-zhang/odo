Working...
Gathering complete (~12 min). Writing the report.

## A. Verdict

**Needs-work — one hard daemon blocker plus one hard GUI blocker.**

The parallel-run substrate works today: 4 workstreams × 4 concurrent run slots × per-run isolated worktrees can execute simultaneously right now. But **the results cannot be accumulated**: `checkBaseFresh` (internal/ipc/server.go:1722) refuses *any* accept — human or auto-land — once main HEAD has drifted from the diff's recorded base, with error text "re-run the task on current HEAD or reject the diff" (server.go:1731), and **no rebase mechanic exists anywhere in the codebase** (grep for `rebase` in `internal/ipc` returns only the comment at autoland.go:21). With 4 parallel workstreams, the first landed diff invalidates the other three. Secondary blocker: W6's park-and-switch queue is daemon-only — the GUI `SendOptions` has no `park` field (gui/src/api.ts:96-107) and no park control exists in `gui/src` (grep: only unrelated "Sparkles" icon matches), matching the plan's row 6: "GUI dock = future GUI wave" (docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md:65).

## B. Daemon-side readiness assessment

All seven stated claims verified against code:

| Claim | Verdict | Evidence |
|---|---|---|
| 1. Multi-workstream IPC | ✓ | `handleBootstrap` (server.go:389) creates "main"/targets named ws + `CreateConversation` (server.go:414); `handleCreateWorkstream` (server.go:494); `handleListWorkstreams` (server.go:512) |
| 2. Concurrency cap 4 | ✓ | `maxConcurrentDefault = 4` (server.go:1450); `activeRunCount()` is daemon-wide over `s.runs` (server.go:1439); enforced at send (server.go:663), steer continuation (server.go:1359), and parked-goal admission (`startParkedGoalRunLocked`, parked.go:195) |
| 3. W6 park-and-switch | ✓ | parked.go end-to-end: durable journal-derived FIFO cap 8 (parked.go:40, `deriveParkedGoals` parked.go:56), auto-dequeue at runDone/park-on-free/startup, recovery via `recoverParkedGoals`, errored runs hold queue (`dequeueParkedGoalOnRunDoneLocked`, parked.go:150) |
| 4. Sidebar + StatusBar | ✓ | `dotState` reducer splits foreground/background/pending/idle (Sidebar.tsx:14-19:236-239); chip = "N background runs" → jump first (StatusBar.tsx:65-75) |
| 5. Cross-ws attention | ✓ | `handlePendingCounts` returns `PendingCounts`, `RunningWorkstreams`, `ParkedGoals`, auto-distill + distilling (server.go:3042-3100) |
| 6. Auto-land pipeline | ✓ | Full gate ladder (autoland.go:3-97); one pipeline at a time via `autoLandMu` (autoland.go:197-198, rationale server.go:114-123) |
| 7. Parked in pending_counts | ✓ | server.go:3087-3104 + `Response.ParkedGoals` |

**Works today:** create 4 ws (GUI form: App.tsx:840), send one task each — all start immediately if conversation free (one run per conversation guard, server.go:656-660), each in its own detached worktree at HEAD (`worktree.Manager.Create`, internal/worktree/worktree.go:77), diffs land independently with protected-path guards (server.go:1768). Steer continuations work cross-ws (`SendOptions.steer`, api.ts:98).

**Missing/breaks:**
1. **Base-staleness serialization (hard).** `checkBaseFresh` rejects accept on HEAD drift, invoked in the shared accept branch (server.go:1787) — applies to *both* human and auto-land. N parallel diffs on one base → N−1 unlandable; remediation is a full agent re-run. 3-way apply exists (autoland.go:68) but is never reached when base moved.
2. **autoLandMu + staleness compound waste.** Pipeline holds verify+panel for minutes (server.go:117); sequential pipelines mean pipeline #2's final freshness check fails after #1 lands → `base_stale_at_land` with the whole completed panel burned (autoland.go:24-28).
3. **No GUI for parked goals** (api.ts:96-107; zero park hits in gui/src). Dequeue-admission feedback on cap exhaustion is log-only (`log.Printf`, parked.go:196-197) — invisible from the GUI.
4. **Cap exhaustion sharing.** One cap serves: user sends, steer continuations, parked auto-dequeues. Whether the M18 revive ladder's spawned repair rounds (`settleRevise`, settle.go:365, "Called from autoLand with autoLandMu held") also check the cap — **not verified**; if not, repair rounds can crowd out parked starts.
5. **Curate is daemon-wide**, coalesced by a single `curating bool` (server.go:102); distills are per-conversation (`distilling map`, server.go:101). Distill/curate drop `s.mu` around agent runs (comment server.go:88-89). Non-blocking by design; second concurrent curate is skipped, not queued — acceptable, not a gap.
6. **Memory/wiki write-race under parallel distills is unverified.** Two conversations' distills can run concurrently; both write `wiki/` and `memory.md`. Whether a wiki-write mutex exists — **not verified** (would need distill write-path read). Flag for the wave.

## C. GUI gaps blocking parallel development (ranked)

1. **No park affordance (QueueDock) — hard gap.** Can the user park a goal on ws C while A/B run? Only via raw IPC outside the GUI. `sendMessage` exposes `steer` only (api.ts:96-107). Daemon is ready (parked.go); plan row 6 leaves "GUI dock = future GUI wave". Fills: goal-queue visibility, park, drop, resume.
2. **Background-run chip is single-target.** `onJumpWorkstream(backgroundRuns[0].id)` (StatusBar.tsx:71) — with 3 background runs, click always jumps to the first; no per-run chooser, no ETA, no finish notification. Title tooltip lists names only (StatusBar.tsx:68).
3. **Review tab is foreground-scoped.** Changes panel shows the *active* conversation's diffs from `pollEvents` (App.tsx:474-475); even the StatusBar pending badge counts only foreground diffs (`pendingDiffs={diffs.length}`, App.tsx:1409). Global review debt is visible only via sidebar pills (`pendingCounts` pill, Sidebar.tsx:283). Reviewing B while A runs **works** — switch bootstraps B's conversation + diff (`handleSwitchWorkstream` App.tsx:706-712; bootstrap returns `Diff`, server.go:427) — but there is no cross-workstream review queue.
4. **Switch doesn't preserve context.** Every bootstrap resets panel state (`setDiffs([])`, `bootstrappedRef.current = false`, App.tsx:385-389) — scroll position, open panel tab, draft text are lost per jump. Context preserved contextually via full event history replay, fine for transcripts.
5. **GUI Wave A not started** (plan row 10, status ⏳): task registry, "still running" StatusBar, attention-ordered sidebar. The sidebar status dots already ship (Sidebar.tsx:236), so much of Wave A is registry/ordering polish, not the unblock.
6. **No completion distinction**: a background run finishing transitions dot to amber `pending` (Sidebar.tsx:15-19) silently; no toast/sound.

## D. Prerequisites ranked

| # | Feature | Gap filled | Cost | Prerequisite for | Parallelizable |
|---|---|---|---|---|---|
| P0a | **Stale-diff rebase/refresh mechanic** — auto attempt `git apply --3way` on fresh HEAD + re-verify, else agent "refresh-on-new-base" follow-up run; both paths journal rebase evidence | Staleness serialization (B-1/2); without it parallel landing is impossible | M | All parallel-landing workflow | Parallel with P0b (boring, daemon-only) |
| P0b | **GUI QueueDock** (wave row 6's deferred half): park/drop/resume controls + per-conv queue view fed by existing `ParkedGoals` | Park goal on C from GUI; cap-exhaustion visibility | M GUI | Full park-and-switch UX; nothing daemon-side | Yes — daemon contract frozen in parked.go |
| P1a | **Background-run chooser** — chip becomes multi-target dropdown; add finish flash | Monitoring 4 runs (C-2) | S | — | Yes, independent GUI |
| P1b | **autoLand pipeline reorder**: re-check base freshness *after* panel before land spend accounting; optionally 2 parallel pipelines with acceptMu already serializing the land critical section | Wasted panel spend (B-2) | S–M | Only with P0a (else moot) | After P0a |
| P2 | GUI Wave A remainder (task registry, attention ordering) (plan row 10) | At-a-glance monitoring polish | M | Long-unattended autonomy | Yes; needs daemon registry substrate |

Sequencing: **P0a and P0b first, in parallel** — together they convert the current "4-run fire-and-forget that mostly can't land" into the actual workflow. P1a is a trivial add-on to P0b.

## E. Risk assessment

1. **Base-staleness cascade (highest, certain).** Every land invalidates all pending diffs daemon-wide (HEAD equality, not file overlap — server.go:1728-1734). *Mitigation: P0a; until then, review+accept diffs in strict RRR order and send dependent re-runs immediately.*
2. **Wasted auto-land spend.** N finishing runs → one pipeline queue; later pipelines likely die at final freshness with full panel cost (autoland.go:197 + 24-28). *Mitigation: P1b.*
3. **Cap exhaustion, silent.** All 4 slots running + parked goal dequeued → log-only refusal loop until a slot frees (parked.go:196); GUI shows nothing (no dock). *Mitigation: P0b surfaces queue depth via `ParkedGoals` (server.go:3100).*
4. **Git conflicts between workstreams: containable.** Runs touch only their own detached worktrees (worktree.go:77-84, `Manager.mu` serializes worktree bookkeeping, worktree.go:24-30); conflicts surface as land-time failures, never mid-run corruption. Accept critical section is daemon-wide (`acceptMu`, server.go:106-112). *Mitigation: inherent + disjoint-file task split (§F).*
5. **Memory cross-contamination: medium.** Parallel agents *read* shared memory/pins/wiki concurrently (safe); *writes* come from distill/curate/skill proposals — protected-path gate keeps diffs out of `.odo/`/`wiki/` (server.go:1768-1779). Concurrent-distill wiki write-race unverified (B-6). Cross-workstream recall injects matched-only content (`memoryLayers.cross`, server.go:728) — goal text shouldn't leak unrelated context, but recall quality dips with 4 active themes. *Mitigation: sequential distill cycles are only ~minutes; verify wiki write serialization before 4-way.*
6. **Review fatigue.** Parallel finishing yields N panels + revise ladders queued (settle.go:365-is-per-pipeline; `autoLandMu` serializes). Human accepts are serialized anyway (acceptMu). GUI foreground-scoped badges hide global debt (C-3). *Mitigation: sidebar pills + eventual review-queue wave; keep auto_apply="main" ON for mechanical daemon items, human review for GUI/visual (already forced: `human_gate_visual`, autoland.go:70-77).*
7. **Refresh drift on switch.** Auto-open panel on 0→1 diff transition (App.tsx:476-478) fires in the active ws only — benign.

Net: the failure mode is **never** "agents trample each other" — the isolation margins (per-run worktree, per-diff accept mutex, per-diff base pinning) are strong. Failures manifest as *serialization pressure downstream*: unlandable diffs, queue waste, invisible debt.

## F. Recommended workstream split

**3 workstreams** (holds one slot free for the user's foreground interactions under cap 4), mapped strictly onto the plan's dependency DAG:

| WS | Items | Type | Internal chain | Why |
|---|---|---|---|---|
| **WS-A "moa-lane"** | #2 → #7 → #8 → #9 | daemon | #2→#7→#8→#9 (hard deps) | The only >1-deep chain; fully serial inside. Files: `internal/moa/client.go` (#2), IPC distill/learner callsites + `internal/modelspec` (#7/8), new consolidator (#9). |
| **WS-B "ledger-guardian"** | #5 → #3 → #4-daemon | daemon | all small; order: #5 first (5-min residual check could null it) | Receipt/taxonomy audit area. Files: `internal/ipc/review.go`-adjacent payloads, `internal/ipc/risk.go`, `cmd_autonomy_audit.go`. Split #4 deliberately: daemon schema here, GUI rendering to WS-C. |
| **WS-C "gui-wave"** | #10 → #11 → #4-GUI | GUI | #10→#11; #4-GUI slots after WS-B ships schema | All GUI-confined. Files: `gui/src/**` (Sidebar, StatusBar, LedgerPanel, new registry). |

**File-overlap matrix (hot files, conflict risk):**

| | internal/moa | internal/ipc (server/review/risk) | gui/src | cmd / docs |
|---|---|---|---|---|
| WS-A | ●● | ● (#7/8 callsites) | — | ○ model spec |
| WS-B | — | ●● (#3/4/5) | — | ● autonomy cmd |
| WS-C | — | — (reads `ParkedGoals` contract only) | ●● | ○ |

- **WS-A × WS-B collide on `internal/ipc/`** for #7/8 vs #3/4/5 — both touch server-side Go in `internal/ipc`. Overlap is file-level-disjoint in practice (#7/8 aim at distill/learner paths; #3/4/5 at review/audit paths), but `server.go` edits from both are the real conflict surface. Mitigation: #5 is "check only" and lands in minutes — run it first in WS-B before WS-A's #7 arrives; or fold #5 into WS-A's #2 wave.
- **WS-C is cleanly isolated** until #4-GUI, which needs WS-B's schema (plan row 10's "rows ride #4's schema"). WS-C can start everything in row 10 that doesn't need the schema today.
- With today's constraints (no GUI park, staleness serialization): **send only the head item in each WS now**; as each finishes, review/accept in RRR order (WS-B items are smallest → land first to minimize stale cascades), then send the next. Post-P0a/P0b this becomes: park all 9 goals up front (cap 8/conv is ample — WS-A conv holds ≤4, WS-B ≤3, WS-C ≤3) and let auto-dequeue chain them.

**Review complexity:** WS-B diffs are small/semantic (best first), WS-A medium, WS-C mostly visual — which is already force-gated to human review (`human_gate_visual`, autoland.go:70), matching where the user's eyeballs are required anyway.
