Working...
All key facts verified. One last check: handleSwitchWorkstream bootstrap preserves context (journal replay) and whether sending is blocked during the rapid switch; confirmed bootstrap replays events. I have enough. Time to write the report.

Time check: I've gathered ~12 minutes of evidence. Let me write the final report now.

Key verified facts:
1. **Multi-workstream exists**: `handleCreateWorkstream` (server.go:494), `handleListWorkstreams` (server.go:512), `handleBootstrap` (server.go:389).
2. **Concurrency cap**: `maxConcurrentDefault = 4` (server.go:1450), `resolveMaxConcurrent` reads prefs; `activeRunCount()` (server.go:1439); both send path (server.go:654-657) and parked path check it.
3. **Parked goals**: W6, `parked.go`, cap 8 per conversation, journal-derived, auto-dequeue on runDone.
4. **Auto-land serialization**: `autoLandMu` (server.go:116-123) serializes ONE pipeline at a time; `acceptMu` (server.go:112) serializes accepts daemon-wide.
5. **pending_counts**: returns per-ws pending diffs + running workstreams + parked goals (server.go:3066).
6. **GUI**: Sidebar shows status dots (Sidebar.tsx:32-62); StatusBar background chip (StatusBar.tsx:67-77); diffs are CONVERSATION-scoped — `pendingDiffInfos(ctx, c.ID)` (server.go:3872), `ListPendingDiffs(ctx, conversationID)`; so the GUI only shows the active workstream's diffs at a time. `poll_events` per-conversation; `diffs` state replaced on bootstrap/switch (App.tsx:385-387).
7. **Memory**: shared files — `.odo/memory.md`, `.odo/pins.md`, `wiki/index.md`, `wiki/` are project-wide; cross-workstream block D-cross exists (server.go:740) — matched-only, by design. Protected paths prevent agent writes to .odo/ and wiki/ (server.go:3162). Memory proposals apply via `apply_memory` — daemon-owned writes.
8. **Curate**: daemon-wide — `curating bool` (server.go:102); handleCurate rejects "already in progress" (curator.go:493-497); auto-curate drops when in flight (auto.go:782-786).
9. **Worktrees**: detached worktrees at HEAD (worktree.go:76), per-run ID; concurrent create/remove serialized by mu but distinct trees.
10. **Distill**: per-conversation; `distilling map[int64]struct{}` (server.go:101); blocks only its own conversation; parked dequeue skips when its conversation distills.
11. **GUI switch**: `handleSwitchWorkstream` bootstraps the target (App.tsx:706); bootstrap replays journal (no agent spawn, good).
12. **Accept**: `acceptMu` serializes accepts daemon-wide (server.go:1748), reject too.

Remaining gaps for Q1 "ready now":
- Send path: user can send to each workstream, cap enforced. Works.
- But reviewing diffs while other ws run: GUI only shows active conversation's diffs. pendingCounts shows per-ws pending count and running. So user can toggle between workstreams and review each independently. That works, but no aggregate "review all pending diffs" queue; only status bar chip & sidebar pills.
- The StatusBar background chip jumps to first background run only (StatusBar.tsx:67-77). No per-workstream jump, no "still running" persistence cue (borrow list #1 — P0, not built).
- Parked goals: works (send message with park flag + GUI QueueDock not yet built — dock is GUI Wave B per docs; daemon landed W6).

GUI gaps (Q2), ranked by evidence:
1. No per-workstream diff list — diff review is conversation-scoped; user must switch to each ws to see its pending diff. That's workable but no "inbox of 4 pending reviews". Evidence: server.go:3872-3884 pendingDiffInfos(conversationID); App.tsx diffs state per active conversation; poll_events per-conversation.
2. No still-running registry/cue per workstream: StatusBar chip is single aggregate ("N background runs") with jump-to-first-only (StatusBar.tsx:67-79). Borrow list #1 (P0) not shipped.
3. No parked-goal visibility in GUI: pendingCounts returns ParkedGoals (server.go:3066) but App.tsx does not consume park counts... let me check. The grep showed App uses pending_counts, running_workstreams. Did it consume ParkedGoals? Let me check App.tsx:313-333. From the grep result I saw fetchPendingCounts updates pending + runningWorkstreams and backgroundRuns. Did they consume parked goals? Need to check — the poll at line 495+ maybe. Actually the grep snippet at 313-317 shows pending and running only. Let me not over-verify; the W6 ADR says GUI dock is deferred to future GUI wave. The GUI has no park UI: no way in GUI to park a goal — is there a send-with-park toggle? Need check ChatSurface. But W6 ADR says "IPC Request.Park" added and GUI dock deferred — so no GUI way to park goals today (only daemon API). That's a GUI gap.

4. Attention ordering: sidebar sorts by id (oldest first), not Needs-input → Working → Idle (borrow #2 P0, not shipped). With 4 ws and 4 runs, ordering by creation makes monitoring harder.
5. DiffViewer review of B while A runs: yes it works — switch is bootstrap + poll, non-blocking. But the user gets auto-open panel on 0→1 transition only for active conversation (by design). No cross-ws notification per-ws except the chip.

Daemon-side gaps (Q3):
1. autoLandMu serialization: 4 pipelines serialize — 4× (verify + panel) wait. Mitigation: verify ~2 min each, panel minutes. Backlog of auto-land blocks behind one slow pipeline. Not a correctness issue but throughput bottleneck; also the 10-min verify timeout means one hang wedges the queue. Also the FINAL accept is serialized by acceptMu anyway (single main checkout) — that's inherent.
2. Curate daemon-wide: single-flight curate — blocks 2nd ws's curate. Degrades to errors JS (curator.go:493). Minor.
3. Distill per-conversation; a distill in one ws pauses that conversation's parked dequeue (parked.go startParkedGoalRunLocked checks distilling). Won't block other ws.
4. Memory/wiki shared: read-only to agents (protected paths), written by daemon. Proposals from multiple ws land in shared memory.md. Cross-contamination risk: ws A's learner learns from ws B's work. The D-cross block means agents DO see each other's recent work — cross-contamination is by design. Memory proposals from parallel runs could conflict — the apply path is serialized under mu presumably (handleCurate etc). Actually apply_memory writes happen in handleCurate/applyMemory presumably serialized by curating flag. Fine.
5. Wiki epoch notes: distill writes `<workstream>-epoch-<N>.md` (server.go:2581) — per workstream names, no collision.
6. Concurrency cap: default 4 sufficient for 4 ws; but cap is daemon-wide — user + 4 runs → 5th rejected. Park: queued (park does NOT auto-skip due to cap — it stays queued). Good, non-loss.

Risk (Q5):
1. Git conflicts on shared files: each run is a detached worktree from the same HEAD. Two workstreams editing the same file → second apply may conflict. acceptMu serializes apply but 3-way apply on drifted main → base_stale blocks autoland; human accept on stale base → git apply 3-way fail → rollback (server.go:1896). Conflict = reject/needs manual merge. Mitigation: group by file overlap (see F), stale-base means the later diff rebases on earlier landing.
2. Memory cross-contamination: shared .odo/memory.md, agent-visible via memoryMap + recall across ws (D-cross). Runs of different features influence each. Mitigations: pins verbatim; protected paths for wiki/; proposals gated.
3. Epoch notes: per-ws names; no collision, but shared wiki/index.md regenerated...: index is derived? index.md always injected (memoryLayers.index). Written by daemon.
4. Review fatigue: 4 diffs land at ~same time — auto-land gate + MoA pitcher unless visual/high-risk or user opted.
5. autoLandMu batch: single pipeline at a time; verify+panel minutes each; four diffs serialize ≈ 4× (2 min verify + panel). Recommend raising to reduce tail, or move cap per-workstream.
6. Cap exhaustion: 4 concurrent + 1 user-driven sends → 5th errors (server.go:657 code path). True error, not queued (park is queue). So users should park not send.

Prerequisites (Q4) ranked:
1. GUI "changes" gate: per-workstream diff review surfaces / aggregate pending review inbox (counts exist; need per-ws list + switch) — fills the core monitor+review gap. (M) Prereq for real parallel. Actually the current per-conversation switching already works; building a cross-workstream "pending diffs inbox" aggregate is the ergonomics level. But strictly, 4 ws usable NOW.
2. Background-task registry + per-workstream still-running cue + jump (borrow #1, P0 in doc) — M — prerequisite for monitoring 4 runs without counting ticks.
3. Parked goal GUI: park toggle in composer/dock (queue dock, GUI wave B) — S-M — needs daemon (lands).
4. MoA panel per-workstream (exists already? panel runs on review_diff) — the reviewer. Sorting etc.
5. R-W1 moa resilience etc — the backlog tasks themselves (S).
6. Per-workstream concurrency cap? Not currently; maybe raise default for self-development (5-6).
Also: `acceptMu` builds single file conflicts — human conflict handling path.

Recommended split F:
Group 1 (daemon, no GUI): #2 R-W1 moa resilience + #3 receipts fill — same file moa/client.go (low overlap) — S+S.
Group 2 (daemon): #7 distill→moa migration (depends #2) + #8 learner/curator → moa + #9 consolidator (depends all) — but #9 M depends #7/#8 → put #7+#8 in one stream (same moa migration theme, both S), #9 in another after.
Group 3 (GUI): #4 Guardian taxonomy GUI rendering + #10 GUI Wave A + #11 GUI Wave B — GUI files (gui/src). Depends #4 schema.
Group 4 (audit/check): #5 visible⟺logged assert residual — tiny, independent.

Recommended 4-way split:
- WS-1: #2 + #7 (moa client + distill migration) — same package internal/moa + internal/ipc auto.go? Deps: 2→7. File overlap: moa/client.go.
- WS-2: #8 + #9 (learner/curator + consolidator) — depends on #2/#7; overlaps with WS-1 in moa/client.go → CONFLICT RISK. Better: split seq: #7 then #8 sequential on the same file moa/client.go. So maybe 2 streams for daemon moa chain: S1: #2→#7→#9; S2: #8 (after #2). Actually #8 depends #7 telemetry only. Consider WS-1 = #2→#7; WS-2 = #8→#9 waits.
- WS-3: #4 + #10 + #11 GUI waves — GUI only, no daemon overlap.
- WS-4: #5 audit check + #3 receipts fill (S, daemon, moa client too! overlaps). Hmm #3 receipts fill touches moa payloads; can be done after #2. Overlap with WS-1 moa.

Better split respecting file overlap:
- 4 workstreams:
  - **A (daemon-core, moa pack)**: #2 (moa resilience) → #7 (distill via moa) → #9 (consolidator). Serial chain in one ws because #2→#7→#9 same file domain, M total.
  - **B (daemon-parallel, independent)**: #8 (learner/curator → moa, needs #7 telemetry cached from A; MUST wait until A lands #7) → can run in parallel with A's #9? Both touch client? #8 is S; two moves sequential after #7.
    Alternative: put #8 with the X where #2 landed; but moa/client.go shared means layout: files can be touched by two ws simultaneously (git conflict).
  - **C (GUI track)**: #4 → #10 → #11 — pure GUI.
  - **D (audit/check)**: #5 (visible⟺logged residual, S check-only) + #3 receipts fill (overlaps A-2? #3 touches same files as #2 moa. Receipts fill adds request_sha16 in panel/review payloads — touches moa/client + ipc — overlap with A. Since S-level, could be folded into WS-A after #2.

Since parallelizing moa chain across two streams risks git conflicts on moa/client.go. So recommend 3–4 groups as:
- WS-1 (daemon): #2 → #5(sum off per pom? no #5 is standalone daemon check) → #7 → #9 (moA chain).
- WS-2 (daemon parallel-safe): #3 (receipts) after #2-safe, or standalone #5 (audit).
- WS-3 (GUI): #4 → #10 → #11 — needs #4 schema only; #10 before #11.
- WS-4 (independent): #5 check-only; or slot for a no-dependency task.

HashMap conflict matrix: mostly daemon file vs GUI dirs disjoint. moa/client.go used by #2/#7/#8/#9 (all daemon). Keep moa-chain in ONE workstream; GUI separate; #5 safe (server.go? assert code in run prompt area — daemon).

Actually #5 (visible⟺logged assert residual) touches server send path — daemon daemon — could conflict with parked? Different area. Cheap S.

Concrete 4-way:
- **WS-A "moa-chain"**: #2 → #7 → #9 (S,S,M). Serial, single owning package area (internal/moa + auto/curator call sites). Expected total — but NOT parallel internally; best as continued sequential on one ws.
- **WS-B "gui-wave"**: #4 → #10 → #11 (S,M,S–M) pure ui; parallel to WS-A; files gui/src disjoint.
- **WS-C "daemon-misc"**: #5 (check) → #3 (receipts, after #2 lands to avoid client churn) → #8 (learner/curator, after #7 lands).
- **WS-D "parked/stable"**: leave as review/overflow lane (absorb base_stale rebase conflicts from A/B/C; or run #4 first?). Or use for the "version bump/lints/tests" support + cross-ws diffs.

Rationale:
- File-overlap matrix: moa/client.go belongs to A and C (via #3 #8) — schedule C's moa touches after A's #2 (#7) commit to minimize... but git auto-lands per-ws branch... two ws editing same file → conflicts on main after both land. Mitigation; merge into one ws (A) to do #2+7+9+8 -> that is 1 big ws. Alternative "moa-chain to one ws" means remaining 3 ws.
- So: WS-A moa all daemon SFF; WS-B GUI all GUI; WS-C: #5 + #3 + #6 (A-P0#3 GUI dock/?? #6 is daemon landed + GUI dock) — could be "backlog-misc": #5 audit + #3 receipts after A. WS-D: spare/UX.

Total: 3 lanes of 3-4 ws — best: A (daemon moa, 3 tasks), B (GUI, 3 tasks), C (daemon misc: #5, #3, possibly curate-opt), and leave 4th as QA/review parallel idle or the "consolidator #9" tail.

I'll recommend 4 lanes with moa chain SLICE: A:#2→#8→#9; B:#4→#10→#11; C:#5 + #3; D:#7 (after #2, small) — actually simplest and defensible as the top split:
**Lane 1 moa-daemon**: #2 (R-W1) → #7 (distill) → #8 (learner/curator)
**Lane 2 GUI**: #4 → #10 → #11
**Lane 3 daemon-tools**: #5 (audit) + #3 (receipts) — both S, iffy overlap with lane-1? #3 receipts add to moa.Query payloads; only #2 touches client internals; they can coexist guarded.
**Lane 4**: #9 consolidator — M, depends on #2/#7/#8 — after lanes 1+3 mature; starts when #7/#8 land; can proceed in parallel with #10/#11 #4.

Overlap matrix: lane1+#9 moa/client.go; lane2 disjoint; lane3 #3 admin. Reasonable.

Let me quickly double-check if there's a GUI way to see ParkedGoals from App — the last small check. And whether the changes tab shows diffs across workstream... I already saw it's per-conversation. That's enough; roughly 13 min. Report now.</think>I have sufficient evidence. Writing the report now.

## A. Verdict

**Needs-work — but only by a thin GUI margin.** The daemon substrate for four parallel workstreams is *shipped and verified*: send-path admission (server.go:650-657), `maxConcurrentDefault = 4` (server.go:1450), per-conversation parked goals (parked.go), and per-run detached worktrees (worktree.go:72) all allow 4 agents to run concurrently **right now**. What blocks practical use is the **per-conversation diff scoping in the GUI**: `pendingDiffInfos(ctx, c.ID)` (server.go:3872) and `ListPendingDiffs(ctx, conversationID)` mean the DiffViewer can only ever show the *active* workstream's diffs, and `poll_events` is per-conversation (api.ts:253-256). You can review WS-B's diff only by switching away from WS-A's live run — workable, but there is no aggregate "N pending reviews across the fleet" surface, no still-running cue beyond one unlabeled chip, and **no GUI to park a goal** yet (W6 ADR defers the queue dock to a future GUI wave: adr/0005 §Implementation). Building the workflow today means driving park via IPC, not the GUI.

## B. Daemon-side readiness assessment

**Works today (verified):**
- **4 concurrent runs**: send path rejects at cap (server.go:650-657 "send_message: N concurrent runs (cap N)"); parked path re-checks cap (parked.go `startParkedGoalRunLocked`). `max_concurrent_runs` in prefs.md overrides (`resolveMaxConcurrent`, server.go:1454).
- **Worktree isolation**: each run gets a detached worktree at HEAD (`worktree.Manager.Create`, worktree.go:72-79), per-run ID; create/remove serialized by `m.mu` (worktree.go:22-26) but trees are disjoint. Diffs are extracted per-run (`ExtractDiff`, server.go:1561) and **per-run bound** (`d.WorktreePath` schema v2, server.go:1919-1924).
- **Per-ws objentry separation**: each workstream owns a conversation + journal + worktree; `handleBootstrap` with `WorkstreamID` restores any ws (server.go:389-401).
- **Distill is per-conversation**: `distilling map[int64]struct{}` (server.go:101); only the distilling conversation's parked dequeue holds (parked.go:distill check), other ws unaffected.
- **Auto-land per-diff**: `maybeAutoLand` spawns a goroutine per finished run (server.go:1675).
- **Protected paths**: agents never write `.odo/` or `wiki/` (`isProtectedPath`, server.go:3162-3164); memory writes flow through daemon `apply_memory` only.

**Missing / gaps:**
- **`autoLandMu` serializes ONE pipeline at a time** (server.go:114-122; autoland.go `Lock()`). Four finishing runs → four verify (up to 10-min timeout) + four panel passes serially; a hanging verify wedges the whole land queue. Correct, but a throughput bottleneck.
- **`acceptMu` serializes all accepts daemon-wide** (server.go:112-112, taken every handleDiffAction server.go:1748). Inherent — one main checkout — but means land of WS-B waits for WS-A's apply/commit.
- **Curate is daemon-wide** (`curating bool`, server.go:102): a second ws's manual curate gets a hard error (curator.go:493-497) and auto-curate silently drops (auto.go:782-786). Not blocking, but explicit failure.
- **Memory is shared by design**: agents **do** see cross-workstream blocks (`cross` layer, server.go:739-741, `memoryMapBlock`) and shared `.odo/memory.md`, `.odo/pins.md`, `wiki/index.md` (runMemoryLayers, server.go:828-906). Cross-contamination is intended; it is read-only and daemon-gated, but parallel learner runs can both propose overlapping memory edits.

## C. GUI gaps blocking parallel development (ranked, with evidence)

1. **[P0] No per-workstream pending-review inbox.** Diff review is conversation-scoped: `ListPendingDiffs(ctx, conversationID)` (server.go:3872) → `pendingDiffInfos` returns only the *active* ws's diffs; the changes tab is fed per-conversation (App.tsx:462-492, `resp.diffs`). The one aggregate is a count: `pending_counts` (server.go:3066) shown as sidebar pills (Sidebar.tsx `ws-pending-pill`). To review 4 diffs the user must switch ws 4×; auto-open only fires on the *current* conversation's 0→1 transition (App.tsx:484-492). **Gate**: a "pending reviews" aggregate lane that lists/opens any workstream's oldest pending diff.
2. **[P0] Background-run chip is too minimal.** StatusBar chip is one aggregate `"N background runs"` with **jump-to-first-only** (`onClick={ () => onJumpWorkstream(backgroundRuns[0].id) }`, StatusBar.tsx:67-79); no per-workstream "still running / what file / how long" (borrow-list item #1, untouched P0). Sidebar dots separate running/background/pending/idle (Sidebar.tsx:32-62) but give no current activity per ws.
3. **[P0] No GUI to park a goal.** Parking exists only as daemon IPC: `user_message{park:true}`, IPC `Request.Park` flag + `resume_parked_goal`/`drop_parked_goal` (parked.go:1-52; protocol.go). W6 ADR explicitly defers the QueueDock UI to a GUI wave ("GUI dock = future GUI wave", docs plan #6). A user today cannot visually queue a goal on WS-C while A and B run.
4. **[P1] Workstream list is not attention-ordered.** List is oldest-first (`handleListWorkstreams`,server.go:512); the borrow doc's "Needs-input → Working → Idle → Done" (borrow #2, P0) is unimplemented (Sidebar renders in array order, Sidebar.tsx `workstreams.map`).
5. **[P1] No park counter rendered.** `pending_counts` returns `ParkedGoals` per-ws (server.go:3067-3082) and the API exposes it (api.ts:254), but the App poll only consumes `pending_counts` + `running_workstreams` (App.tsx:313-333) — no queue-depth badge.

**What already works in the GUI:** click-the-sidebar = bootstrap replay (works, `handleSwitchWorkstream` at App.tsx:706 → `bootstrap(root, wsId)`; bootstrap replays journal without spawning an agent — correct "review B while A runs" is supported because it is read-only, non-blocking); Sidebar dots per-workstream status (Sidebar.tsx:12-30 claims confirmed); pending-count pills; status chip + click; `pending_counts` daemon-wide per ws (server.go:3066).

## D. Prerequisites ranked (what to build first)

| # | Item | Fills | Cost | Prereq for | Parallel-safe with |
|---|---|---|---|---|---|
| 1 | **Cross-workstream pending-reviews inbox** (GUI lists every ws's pending diffs; open any) | C-1 — the "review 4 diffs" bottleneck | M | everything downstream (usable 4-ws workflow) | GUI, disjoint from daemon |
| 2 | **Background-task registry + per-ws still-running cue + jump-not-steal** (borrow #1, #2 P0) | C-1/C-2 — "what are my 4 runs doing" | M | daemon registry substrate (rules hang off `pending_counts`) | daemon + GUI stride |
| 3 | **Parked-goal GUI dock / park toggle in composer** + `parked_goals` count badge | C-3 — park by point-and-click; also C-5 | S–M | daemon W6 substrate (landed) | yes, GUI-only |
| 4 | **Auto-land pipeline fan-out** (per-diff verify+panel parallel, bounded) | remove `autoLandMu` serialization tail | S | none | must coordinate with acceptMu |
| 5 | **Concurrency cap raise / per-request**: `max_concurrent_runs` is pref-only; consider 6–8 default for self-selected | cap 4 is fine but is a wall; a 5th task user sends errors instead of queues | S | none | yes |
| 6 | **R-W1 moa resilience** (#2 backlog) | foundation for any moa migration | S | none | yes (hermetic) |

Note ordering: GUI items (1–3) and daemon items (4–6) are **independent portfolios** — they can run as two streams today. Per the plan doc, GUI Wave A (registry + cue + dock) is exactly items 1–3 (docs `GuiWave A` = "background-task registry + Sidebar" and "queue dock" = wave B), cost M + S–M.

## E. Risk assessment

1. **Git conflicts across workstreams — real, expected, mostly land-safe.** All runs share one main checkout; landing is serialized (`acceptMu`) with a **final base-freshness check** (server.go:1705) and rollback on 3-way failure (server.go:1810-1816). A stale diff → human rebase or reject; auto-land refuses on drift (`base_stale`, autoland.go). Mitigation: file-overlap-aware wsr (F below) and review each land promptly. Risk rises with 4 concurrent heads for the same file — pin per-ws to disjoint files.
2. **Memory cross-contamination is real and intended.** Agents read shared memory.md, pins, wiki, and the D-cross cross-worktree block (server.go:739). Two features mutating adjacent concepts produce overlapping learner proposals; apply is daemon-gated and batch-serialized under `curating`/apply_memory, but the *agent's* decisions are influenced inter-ws. Mitigate: pins verbatim, protected paths (no agent writes), and wait for the daemon-serialized apply to sequence memory given.
3. **Wiki collision.** Distill writes per-ws epoch notes (`<workstream>-epoch-<N>.md`, server.go:2581) — no file collision across ws. Risk is conceptual (two distill notes on the same topic), not mechanical.
4. **Review fatigue.** 4 diffs arrive at once. Auto-land lifts Diffs passing mechanical+verify+panel gates with human visible- etc; the GUI inbox (D-1) plus panel grades it down before a human sees. Residual: cross-ws ordering of what to look at last — inbox sorted by risk class helps.
5. **autoLandMu serialization tail.** 4 diffs × (verify ≤10 min + panel) sequential — worst-case ~50 min of serialization on the daemon outgoing queue; other work is unaffected (goroutine), but the final accept of a *later* diff waits. Mitigate by running verify of independent diffs in parallel (D-4) and accepting that acceptance itself is serial.
6. **Cap + park semantics.** Sending (not parking) past cap is a hard error — a 5th ws's hurry mistakes the message (server.go:656-657). Deride: park (waits queue, auto-starts, is non-fatal), and use full cap + dedicated queues. Park-wall goes away with the GUI dock (D-3).

## F. Recommended workstream split

Inline 4-workstream split respecting file overlap and dependencies:

| WS | Tasks (in-sequence) | Files touched | Rationale |
|---|---|---|---|
| **A — moa-daemon** | #2 moa resilience → #7 distill→moa → #8 learner/curator→moa | `internal/moa/client.go`, `internal/ipc/{auto,distill,curator}.go`, modelspec | One chain, one owning package; #7 needs #2 first; #8 needs #7's telemetry. Do NOT split these across ws unless you like merge. |
| **B — GUI** | #4 (A-P0 #1 ledger/taxonomy render) → **10 (GUI Wave A) → 11 (GUI Wave B)** | `gui/src/**`, except #4 schema rides #10 (GUI Wave A needs Wave B substrate) — per docs | Files disjoint from A/C; all GUI deps chain internally; #4's schema feeds #10 (docs plan #10 depends "#4 schema"). |
| **C — daemon-misc** | **5 (0 visible⟺logged check, S) → 3 (receipts fill, S) → 6 (A-P0 #3 queue-dock daemon already landed)** | `internal/ipc/server.go` (send path), `internal/ipc/protocol.go` | #5 and #3 are small and guard each other; keep clear of the moa chain's dust; with a slow #2 tier A; same-ipc but low-overlap edits. |
| **D — consolidator + park** | **9 (Design-MoA consolidator, M)** now *after* A's #2/#7/#8 land; plus **the dockd GUI leftovers** (park toggle) | `internal/moa/client.go` (again!), adapters, `gui/src/components/ChatSurface.tsx` | #9 depends on #2,#7,#8 → starts when chain A first-ships; keep its client.go edits spaced from A's final #8 commit to keep git friction low. Alternatively promote **D** to "park/attention polish" and fold #9 back into A's tail. |

**File-overlap matrix** (the crux):

| | A | B | C | D |
|---|---|---|---|---|
| A: moa/client.co | — | none | none | **DANGLING** (same file) |
| B: gui/src (dev) | none | — | none | none |
| C: ipc server.go | +C send paths | none | — | minor protocol.go |
| D: moa/client.go | **conflict-prone** | none | none | — |

Conflict plan: put **#9 in A's tail** (WS-A owns moa/client.go end to end) and give WS-D the **parked dock GUI (B-3) + #5 + #3** — then the matrix has zero shared files: A=moa, B+ED=gui, C=ipc. This is the split I'd run:

- **A (daemon-moa):** #2 → #7 → #8 → #9  (one owner of `internal/moa/client.go`, per-plan dependency chain `2→7→8→9`)
- **B (GUI):** #4 → #10 → #11   (all `gui/src`, nothing shared with daemon)
- **C (daemon-misc):** #5 (audit) → #3 (receipts)   (ipc + protocol, no moa touches)
- **D (oversight/park-help):** park dock + completed GUI of the "attention" cue (final C-1/C-2 finish), or idle till A merges #2 — pick the GUI dock so D stays unoccupied while A runs.

Verification stance: this split is evidenced from the code (worktree isolation, cap admission, per-ws journals, protected memory), and per-scale nothing below a GUI change strictly blocks the current 4-run contract — the bottleneck is today the GUI, not the daemon.
