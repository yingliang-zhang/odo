Working...
I have sufficient evidence. Writing the report now.

## A. Verdict

**needs-work — daemon substrate is ready, but the GUI is single-pane.**

The daemon can run 4 agents in parallel across 4 workstreams *today* (`handleSendMessage` enforces the cap at `server.go:663`, each run gets an isolated worktree via `worktree.Manager.Create` at `worktree.go:76`). What blocks the practical workflow is the GUI: it is **single-conversation-scoped** — `workstream`, `conversation`, `events`, `diff`, `diffs` are all singular React states (`App.tsx:112-122`), the poll loop only polls the active conversation (`App.tsx:457`), and `pendingDiffInfos` returns diffs for one conversation only (`server.go:3864` → `ListPendingDiffs(ctx, conversationID)`). The user cannot watch workstream A's run stream while reviewing workstream B's diff without a full context-switching `bootstrap` that replaces everything wholesale (`App.tsx:374-401`). The daemon is a parallel engine behind a serial cockpit.

---

## B. Daemon-side readiness assessment

### What works today

| Capability | Evidence | Status |
|---|---|---|
| Create N workstreams | `handleCreateWorkstream` `server.go:494`; `handleBootstrap` creates/returns workstream + conversation `server.go:389-435` | ✅ |
| 4 concurrent agent runs | `maxConcurrentDefault = 4` `server.go:1450`; cap enforced in `handleSendMessage` `server.go:663` and `startParkedGoalRunLocked` `parked.go:210`; `activeRunCount()` counts non-finished runs daemon-wide `server.go:1439` | ✅ |
| Worktree isolation per run | `worktree.Manager.Create` `worktree.go:76` — each run gets a detached worktree at HEAD; `Manager.mu` serializes git worktree add/remove only, not diff extraction | ✅ |
| W6 park-and-switch | `handleParkGoal` `parked.go:116` queues a goal per-conversation FIFO (cap 8); `dequeueParkedGoalOnRunDoneLocked` `parked.go:166` auto-starts on runDone; park works on any workstream's conversation | ✅ |
| Per-workstream status reporting | `handlePendingCounts` `server.go:3042` returns `PendingCounts` (per-WS pending diffs), `RunningWorkstreams`, `ParkedGoals` (per-WS queue depth `server.go:3069-3086`) | ✅ |
| Accept/reject any diff by ID | `handleDiffAction` `server.go:1735` looks up diff by `diffID` — not scoped to the active conversation; works cross-workstream | ✅ |
| Auto-land per diff | `drainRun` spawns `go s.maybeAutoLand(...)` as a goroutine `server.go:1675`; multiple can be spawned | ✅ (serialized — see gaps) |

### What's missing / bottlenecked

| Gap | Evidence | Impact |
|---|---|---|
| **`acceptMu` serializes ALL landings daemon-wide** | `server.go:112` (one mutex, comment: "two concurrent accepts share one main checkout"); `handleDiffAction` takes it at `server.go:1748` | Two humans accepting diffs in WS-A and WS-B cannot proceed simultaneously. By design — there is one main checkout. Not a correctness bug, but a throughput ceiling. |
| **`autoLandMu` serializes ONE auto-land pipeline** | `server.go:122`; `maybeAutoLand` locks it at `autoland.go:197`; comment: "autoLandMu serializes ONE pipeline at a time" | If 4 diffs finish simultaneously, pipelines 2-4 block on the mutex — each holds a goroutine waiting. The verify gate (10 min timeout, `autoland.go:153`) means worst-case serialization is 40 min. Not a deadlock, but a severe throughput bottleneck for parallel work. |
| **`curating` is a single daemon-wide bool** | `server.go:102`; guard at `curator.go:493` ("already in progress"); auto-curate guard at `auto.go:782` | Only one curate pass at a time, daemon-wide. Curate is epoch-boundary (rare), so low practical impact. |
| **Memory/wiki are per-PROJECT, not per-workstream** | `memoryLayers` reads `.odo/memory.md`, `.odo/pins.md`, `wiki/index.md` from `s.projectRoot` `server.go:761-771`; no per-workstream scoping | All workstreams share the same `memory.md`, `pins.md`, and `wiki/` dir. Parallel agents read the same memory layers; a distill/curate in one workstream rewrites shared wiki notes visible to all. Cross-contamination risk (see §E). |
| **`distilling` is per-conversation but distill output is project-scoped** | `s.distilling` keyed by `conversationID` `server.go:101` — multiple distills can run concurrently. But distill writes to `wiki/` which is project-level | Concurrent distills in different workstreams both write epoch notes to the same `wiki/` dir — potential filename/epoch collision if epochs are shared. |
| **No IPC for "list all pending diffs across workstreams"** | `pendingDiffInfos` `server.go:3864` calls `ListPendingDiffs(ctx, conversationID)` — one conversation only. There is no `ListAllPendingDiffs` for the project. | The GUI cannot fetch diff *content* for workstream B while viewing A. It can see the *count* (via `pending_counts`) but not the diff itself without switching conversation. |

### Q1 answer: Can 4 agents run in parallel right now?

**Yes, mechanically.** Create 4 workstreams (`create_workstream`), bootstrap each (`bootstrap` with `workstream_id`), send a message to each (`send_message`). The cap check at `server.go:663` admits all 4 (activeRunCount < 4). Each gets an isolated worktree. Each drains independently via its own `drainRun`. Each produces a diff that can be auto-landed (serialized) or human-reviewed.

**What breaks under load:** simultaneous auto-land pipelines serialize on `autoLandMu`. Simultaneous human accepts serialize on `acceptMu`. Curate is daemon-wide single. These are throughput ceilings, not correctness failures — but with 4 workstreams all finishing at once, the user faces a 4-deep queue for landing.

---

## C. GUI gaps blocking parallel development (ranked)

### C1. **No split-view / multi-pane — single conversation only** (Critical)
The entire GUI state is singular: one `workstream`, one `conversation`, one `events[]`, one `diff`, one `diffs[]` (`App.tsx:112-122`). The poll loop polls only `conversationRef.current` (`App.tsx:457`). `pendingDiffInfos` returns one conversation's diffs (`server.go:3864`). **The user cannot see workstream A's streaming run and workstream B's pending diff on the same screen.** Switching workstreams calls `bootstrap` which replaces all state wholesale (`App.tsx:706-733` → `applyBootstrap` `App.tsx:374-401`). This is the single largest gap.

### C2. **No cross-workstream diff review queue** (Critical)
The changes panel renders `diffs` from the active conversation only (`App.tsx:1342-1356`). There is no IPC call to fetch pending diffs across all workstreams (no `ListAllPendingDiffs`; `handlePendingCounts` returns counts only, `server.go:3088-3095`). To review B's diff while A runs, the user must switch to B (losing A's live stream), review, accept/reject, then switch back (A's stream resumes from the new bootstrap — but the preview bubble is reset, `App.tsx:384`). **Review and monitoring are mutually exclusive.**

### C3. **Background-run chip is a count + jump-to-first only** (High)
`StatusBar.tsx:67-77`: shows "N background runs" as a single button, clicking jumps to `backgroundRuns[0].id`. No per-workstream detail, no streaming preview of background runs, no way to see which background run is doing what without switching. With 4 workstreams, the user gets "3 background runs" and a blind jump.

### C4. **No per-workstream streaming preview** (High)
The streaming preview (`resp.preview`, `App.tsx:470`) is only for the active conversation's poll. Background workstreams' live output is invisible — the user sees a purple "background" dot (`Sidebar.tsx:21`) but no content. There is no IPC to fetch a preview for a non-active conversation without a full `pollEvents` (which requires the conversation ID and returns that conversation's events only).

### C5. **No "park goal" UI affordance** (Medium)
W6 park-and-switch exists daemon-side (`handleParkGoal`, `parked.go:116`) and `pending_counts` reports `ParkedGoals` per workstream (`server.go:3069-3086`), but I found no GUI element that sends `park:true` in a `send_message` request or displays the parked-goal queue depth per workstream. The `Request.Park` field exists (`protocol.go:94`) but the GUI composer would need a "park" toggle. The sidebar shows pending-diff pills (`Sidebar.tsx:277`) but not parked-goal counts. [INFERENCE: the parked-goal badge is not wired in the GUI based on the Sidebar/StatusBar code I read; I did not exhaustively search all GUI files.]

### C6. **Switching loses scroll position and panel state** (Low)
`applyBootstrap` resets `setWikiFocus(null)`, `setDiffs([])`, `prevDiffsCountRef.current = 0` (`App.tsx:386-390`). Switching back to a workstream re-bootstraps from scratch — no preserved scroll, no preserved panel tab per workstream. Minor friction multiplied by 4 workstreams.

---

## D. Prerequisites ranked

| Rank | Feature | Gap filled | Cost | Prereq for | Parallelizable with |
|---|---|---|---|---|---|
| **D1** | **Cross-workstream diff queue IPC + panel** | C2 — add `ListAllPendingDiffs` (project-scoped, not conversation-scoped) to `server.go`; GUI fetches and renders all pending diffs regardless of active workstream, with accept/reject by diffID (already works via `handleDiffAction`) | **M** | D2, D3 | D4 (independent: daemon IPC vs GUI) |
| **D2** | **Split-view or tabbed multi-workstream pane** | C1 — let the user view 2+ workstreams simultaneously: either a split chat+diff layout, or a tabbed conversation view preserving each workstream's scroll/events/panel state | **L** | Nothing (enabler) | D1, D4 |
| **D3** | **Per-workstream streaming preview in sidebar/StatusBar** | C4 — poll `preview` for running workstreams via a lightweight IPC (or include background previews in `pending_counts`), render a mini-stream in the sidebar row | **M** | D1 (needs the IPC pattern) | D4 |
| **D4** | **`autoLandMu` parallelism** (daemon) | Bottleneck: serialize only the final accept (`acceptMu` already does), not the verify+panel phase. Let multiple pipelines run verify/panel concurrently, queue only at `handleDiffAction` | **M** | Nothing | D1, D2, D3 |
| **D5** | **Park-goal UI affordance** | C5 — composer "park" toggle; sidebar parked-goal badge per workstream | **S** | Nothing | All others |
| **D6** | **Per-workstream memory/wiki isolation** (daemon) | Cross-contamination: scope `wiki/` and memory to per-workstream dirs, or accept shared-scope with a documented invariant | **L** | Nothing | All others |

**Minimum viable parallel workflow:** D1 + D2. With those two, the user can see all pending diffs in one panel and view 2 workstreams side by side. D3 and D4 are throughput improvements. D5 is convenience. D6 is a risk mitigation (see §E).

---

## E. Risk assessment: parallel self-development failure modes

| Risk | Mechanism | Severity | Mitigation |
|---|---|---|---|
| **Git conflicts on `server.go`** | `server.go` is 4068 lines; daemon tasks #3, #5, #8, #9 all modify it. Each worktree branches from HEAD at creation (`worktree.go:83` → `git.CreateWorktree` at HEAD). If WS-A lands a change to `server.go` and WS-B's worktree was cut before that, WS-B's diff applies onto stale HEAD → `checkBaseFresh` refuses (`server.go:1785`, `errBaseStale`). The diff goes back to pending — the user must rebase manually. | **High** | Assign only one workstream to touch `server.go` at a time. Split daemon work by file: moa/settle.go in one WS, server.go receipts/asserts in another, curator.go/auto.go in a third. Accept that base-stale will fire and budget for rebases. |
| **Memory cross-contamination** | `memoryLayers` reads `.odo/memory.md` from `s.projectRoot` (`server.go:762-766`). All workstreams inject the same project memory. A distill in WS-A may propose a rule learned from WS-A's work that is irrelevant or misleading for WS-B. `apply_memory` writes to the shared file. | **High** | Disable auto-distill for parallel development sessions (`parked_goals: manual` is separate; need an auto-distill disable). Or accept shared memory and review all proposals in one queue. D6 (per-WS memory) is the real fix but costly. |
| **Wiki/epoch-note collision** | Distill writes epoch notes to `wiki/` (project-level). `handleDeleteWorkstream` refuses deletion with pending diffs but does not isolate wiki. Two concurrent distills could write notes for overlapping epochs. | **Medium** | Distill is per-conversation (`s.distilling` keyed by convID, `server.go:101`) so two distills *can* run concurrently. Wiki filenames are epoch-scoped (`WikiNoteInfo.Epoch`, `protocol.go:187`); collision is unlikely unless two workstreams distill the same epoch simultaneously. Mitigation: serialize distills via `parked_goals: manual` or accept the risk. |
| **Review fatigue** | 4 workstreams × 1 diff each = 4 diffs to review. With auto-land `autoLandMu` serializing, the user also sees blocked pipelines piling up. | **Medium** | Enable `auto_apply: "main"` so clean diffs auto-land (the pipeline handles verify + panel). The user only reviews diffs that the panel blocks. This reduces review load but the panel spend serializes (D4). |
| **Auto-land serialization** | `autoLandMu` (`server.go:122`) — one pipeline at a time. 4 ready diffs = 4 × (verify 10min + panel ~30s) = up to 44 min of serialized landing. | **High** (throughput) | D4: parallelize verify+panel, serialize only the final `acceptMu` section. This is the single highest-impact daemon change for parallel throughput. |
| **Concurrency cap exhaustion** | If all 4 slots are occupied by long-running agents and the user parks a 5th goal, `startParkedGoalRunLocked` logs "concurrency cap reached" and the goal stays queued (`parked.go:211`). The goal auto-starts when a slot frees. | **Low** | This is by design. The cap is configurable via `max_concurrent_runs` in prefs.md (`resolveMaxConcurrent`, `server.go:1454`). Raise it to 6-8 for parallel development. The real ceiling is API rate limits and machine resources, not the daemon. |
| **`acceptMu` land race** | Two humans (or human + auto-land) accept simultaneously → serialized by `acceptMu` (`server.go:112`). The 2nd waits. Not a correctness issue (the final base-freshness check `server.go:1785` catches drift). | **Low** | Acceptable. The base-freshness check is the safety net — a 2nd accept that finds HEAD moved journals `base_stale_at_land` and leaves the diff pending. |

---

## F. Recommended workstream split

### 3-workstream grouping

**WS-Alpha — MoA pipeline chain (daemon, `internal/moa/` + `internal/ipc/settle.go`)**
- #2 R-W1 moa resilience (retry + typed errors)
- #7 R-W2 distill → moa migration (depends on #2)
- #9 R-W4 Design-MoA consolidator (depends on #2, #7, #8)

Files: `internal/moa/client.go`, `internal/ipc/settle.go`, `internal/ipc/autoland.go` (review fanout). Internal dependency chain — must be sequential within the workstream. #9 also needs #8 from WS-Beta, so #9 starts only after WS-Beta delivers #8.

**WS-Beta — Daemon receipts + asserts + curator migration (daemon, `server.go` + `curator.go` + `auto.go`)**
- #3 R-W1.5 receipts fill (independent — start immediately)
- #5 A-P0 #2 visible⟺logged assert residual (independent — start immediately)
- #8 R-W3 learner/curator → moa (blocked until WS-Alpha delivers #7)

Files: `internal/ipc/server.go` (#3, #5), `internal/ipc/curator.go` + `internal/ipc/auto.go` (#8). #3 and #5 can run in parallel within the workstream (both touch `server.go` but different functions: `assembleRunPrompt` vs assertion paths — moderate merge risk). #8 waits for WS-Alpha's #7.

**WS-Gamma — GUI waves (GUI, `gui/src/`)**
- #4 A-P0 #1 Guardian taxonomy GUI rendering (daemon schema done — start immediately)
- #10 GUI Wave A: task registry + StatusBar + Sidebar (depends on #4 schema)
- #11 GUI Wave B: context meter, plan comments (depends on #10)

Files: `gui/src/components/*`, `gui/src/App.tsx`. Fully isolated from daemon workstreams — zero file overlap. Internal chain #4 → #10 → #11.

### File overlap matrix

|  | WS-Alpha (moa/settle) | WS-Beta (server/curator) | WS-Gamma (GUI) |
|---|---|---|---|
| **WS-Alpha** | — | `settle.go` vs `server.go`: low overlap (different files, both in `internal/ipc/`); `autoland.go` shared by #2 (resilience) and review fanout — **medium** if #9 touches review prompt builder | none |
| **WS-Beta** | medium (above) | — | none |
| **WS-Gamma** | none | none | — |

### Cross-workstream dependencies

```
WS-Alpha:  #2 ──→ #7 ──→ #9 ←─ (needs #8 from WS-Beta)
WS-Beta:   #3 ─┐
           #5 ─┴──→ #8 ←─ (needs #7 from WS-Alpha)
WS-Gamma:  #4 ──→ #10 ──→ #11
```

Critical path: #2 → #7 → #8 → #9 (crosses WS-Alpha and WS-Beta). WS-Gamma is fully parallel — it can run from t=0 with no daemon coordination.

### Why not 4 workstreams?

Splitting #3 and #5 into a 4th workstream gains little: both are S-cost and both touch `server.go`, so they'd conflict with WS-Beta's #8 anyway. The serialization benefit of 4 vs 3 workstreams is marginal (the critical path is the moa chain, not the independent S-tasks). 3 workstreams with 2 daemon + 1 GUI maximizes parallelism while keeping `server.go` edits in one workstream.

### Practical note

WS-Gamma can start **immediately** and run fully parallel — it is the proof-of-concept for the workflow. WS-Alpha and WS-Beta share the `internal/ipc/` package; the user should land WS-Alpha's #2 and #7 before WS-Beta's #8 to avoid base-stale churn. If the user wants to validate parallel development with minimal risk, start WS-Gamma alone alongside one daemon workstream — the GUI touches no daemon files, so there is zero merge conflict.
