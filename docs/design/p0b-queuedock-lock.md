# DESIGN LOCK: P0b — GUI QueueDock: Park/Drop/Resume + Parked-Goal Visibility

> Tri-model MoA consolidation (K3/GLM/DSF, --thinking max, 540s, blind-sealed). 3/3 converged.

## Core design (3/3 convergence)

**Two surfaces, one data source.** The user parks a goal via a toggle in the ChatSurface composer; the QueueDock derives parked goals from journal events the GUI already polls; the Sidebar shows per-workstream depth badges from `pending_counts.parked_goals`.

**No daemon changes.** All W6 daemon contracts are frozen. The GUI only consumes existing IPC: `send_message{park:true}`, `resume_parked_goal`, `drop_parked_goal`, `pending_counts.parked_goals`.

## UI surfaces

### 1. Park toggle (ChatSurface composer)

A small toggle button inside `.chat-input`, immediately left of the Send button. `aria-pressed` + `.park-armed` accent class when on.

- Armed → submit sends `send_message{park:true}`, never `steer`
- `steer = agentRunning && !parkArmed` — structural mutex (daemon refuses steer+park, server.go:623)
- Submit button label changes: `Send`/`Steer` → `Park`
- Disabled when draft starts with `/` (slash commands route before park branch)
- No keyboard shortcut (fat-finger risk)

### 2. QueueDock (per-active-conversation)

A collapsible chip + popover between PlanChip and the composer (PlanChip pattern: `plan-chip-wrap`/`plan-popover`/`plan-list`/`plan-row` CSS classes).

- Shows only for the active conversation, only when ≥1 waiting parked goal
- Each row: position #1…N, goal text (2-line clamp, tail-pinned), `▶ Resume` and `✕ Drop` buttons
- Head row tagged `next`
- **Resume** → `resume_parked_goal{conversation_id, goal_seq=seq}` — disabled when `agentRunning || distillLocked`
- **Drop** → `drop_parked_goal{conversation_id, goal_seq=seq}` — always enabled; two-step inline confirm (no `window.confirm`)
- `busySeq` state prevents double-click (PlanChip `busyId` precedent)
- Daemon errors (`no parked goal with seq N`) treated as benign reconcile, not error banner
- One-line hold note: *"queued goals start when the current run finishes"*

### 3. Sidebar badge (per-workstream)

A `ws-parked-pill` next to the existing `ws-pending-pill` (Sidebar.tsx:277). Shows `parkedGoals[w.id]` from `pending_counts.parked_goals`. Hidden when 0. Active-project rows only (mirrors pending pill scoping). Distinct color (amber/queue token, NOT `--err`).

### 4. MessageBubble reconciliation

Two branches before the generic fallthrough (MessageBubble.tsx:216):
- `run_prompt{origin:"parked_goal"}` with actor → `null` (auto-dequeue, run streams visibly); without actor → badge `resumed parked goal`
- `parked_goal_dropped` → badge `dropped parked goal`

### StatusBar: NOT extended (K3+GLM)

Sidebar pill covers cross-workstream glanceability. A third surface duplicates state. Rejected.

## Data source: journal-derived (3/3 convergence)

**No new daemon command to list parked goals.** The QueueDock derives the active conversation's queue client-side from journal events — a TypeScript port of the daemon's own `deriveParkedGoals` (parked.go:59-89):

- parked = `user_message{park:true, non-empty text}` events
- consumed = `run_prompt{goal_seqs}` + `parked_goal_dropped{goal_seq}`
- waiting = parked − consumed, in seq order

This mirrors `todo.ts:deriveTodoState` → `PlanChip` (ChatSurface.tsx:19). The park response echoes the journaled event into `recordEvents` (App.tsx:589), so a fresh park renders instantly. Cross-workstream action requires switching (bootstrap replays the journal).

**Badge (daemon `parked_goals`) is authoritative depth; dock is derived and may transiently lag by one poll tick.**

## Files to touch

| File | Change |
|---|---|
| `gui/src/types.ts` | `SendMessageRequest.park`, `SendMessageResponse.parked`, `PendingCountsResponse.parked_goals`, `EventPayload` park/goal_seqs/goal_seq/actor; new `ParkedGoal`, `ParkedGoalResponse` |
| `gui/src/parked.ts` | **NEW** — `deriveParkedGoals(events): ParkedGoal[]` (port of parked.go:59-89) |
| `gui/src/api.ts` | `SendOptions.park`; `sendMessage` park branch; new `resumeParkedGoal`, `dropParkedGoal` |
| `gui/src-tauri/src/lib.rs` | `send_message` park param; 2 new commands (`resume_parked_goal`, `drop_parked_goal`); `generate_handler` registration |
| `gui/src/App.tsx` | `parkedGoals` state, `refreshPendingCounts` parse, `handleSend` park param, `handleResumeParked`/`handleDropParked` callbacks, prop wiring |
| `gui/src/components/ChatSurface.tsx` | park toggle button, `parkArmed` state, submit steer/park computation, QueueDock mount |
| `gui/src/components/QueueDock.tsx` | **NEW** — chip + popover + row actions (Resume/Drop) |
| `gui/src/components/Sidebar.tsx` | `parkedCounts` prop, `ws-parked-pill` |
| `gui/src/components/MessageBubble.tsx` | 2 review_action branches (run_prompt/parked_goal_dropped) |
| `gui/src/styles/app.css` | `.park-toggle`, `.ws-parked-pill`, `.queue-dock*` classes (token-based) |
| `gui/src/dev/fixtures.ts` | `parkedGoals` fixture, seeded parked event |
| `gui/src/dev/mock-invoke.ts` | park send branch, resume/drop cases, `pending_counts.parked_goals` |
| `gui/e2e/parked-goals.spec.ts` | **NEW** — 7 E2E scenarios |

## Hard rules

1. **No daemon changes** — all edits GUI/bridge only; `internal/` untouched.
2. **Park toggle forces `steer=false`** — structural mutex (server.go:623).
3. **QueueDock derives ONLY from `deriveParkedGoals(events)`** — never from a local queue cache.
4. **Sends specific `goal_seq`** (never 0) — the dock always has a seq per row.
5. **`ok:false` from resume/drop is benign reconcile** — never an error banner (auto-dequeue race).
6. **Badge (daemon `parked_goals`) is authoritative depth** — dock may transiently lag.
7. **No `window.confirm`** — use inline two-step confirm (existing pattern).
8. **No Tailwind** — CSS tokens only, match existing patterns.
9. **No git add/commit.** Touch only the files listed above.

## Test names

E2E (Playwright, `gui/e2e/parked-goals.spec.ts`):
1. `park toggle parks a goal and clears the composer`
2. `queue dock lists goals FIFO with next tag`
3. `park while running overrides steer`
4. `drop removes row and journals`
5. `resume clears head and shows receipt`
6. `sidebar parked pill reflects depth`
7. `full queue error keeps draft`

Manual (real daemon):
- `parked_goals: auto` → park on busy conversation → dock shows goal → run finishes → auto-dequeue
- `parked_goals: manual` → park → goal holds → Resume/Drop
- Over-cap (8 goals) → 9th park surfaces daemon error
- Errored run → advisory holds queue → Resume works
- Daemon restart mid-queue → reload GUI → dock repopulates from journal replay

## Verification

```bash
cd gui && npx tsc --noEmit
cd gui/src-tauri && cargo check
cd gui && npx playwright test e2e/parked-goals.spec.ts
go build ./... && go test ./internal/ipc/ -count=1  # daemon untouched — guard only
```
