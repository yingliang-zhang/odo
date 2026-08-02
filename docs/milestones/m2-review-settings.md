# M2 — Review + Settings

## Pain

M1 works but:
- When the agent produces a diff, I have to review it alone. There's no way
  to fan out the review to multiple models for cross-checking (MoA pattern).
- I can't change model, timeout, or adapter settings from the UI — I have to
  edit `~/.odo/prefs.md` manually.
- If I want to run multiple agents in parallel on different parts of a task,
  I have to create separate workstreams manually. There's no fan-out command.

## Demo

### Demo A: MoA review fan-out

1. Agent produces a diff (as in M0/M1).
2. Instead of clicking "Accept" immediately, click "Review".
3. The diff is sent to N models in parallel (configured in settings).
4. Each model returns: verdict (ACCEPT/REJECT/NEEDS_FIXES) + comments.
5. The UI shows all verdicts side by side. If any model REJECTs, the
   "Accept" button is disabled and the reject reasons are highlighted.
6. If all models ACCEPT, the user can accept with confidence.

### Demo B: Settings panel

1. Click the gear icon in the sidebar.
2. A settings panel opens with:
   - Coding model: text input (reads/writes `~/.odo/prefs.md` `coding:` line)
   - Orchestrator model: text input (`orchestrator:` line)
   - OMP timeout: number input (seconds)
   - Default adapter: dropdown (OMP/Pi)
   - Review models: multi-select (which models to use for MoA review)
3. Changes are saved to `~/.odo/prefs.md` and take effect on the next run.
4. A "Restart daemon" button applies changes immediately.

### Demo C: Parallel agent fan-out

1. In a workstream, type a message and click "Fan-out" instead of "Send".
2. The message is sent to N agents in parallel (N configurable in settings).
3. Each agent works in its own worktree, producing its own diff.
4. The UI shows all agent runs in a list with their statuses.
5. When all agents finish, the user can compare diffs and accept the best one.

## Not built

- Sandbox containment — not planned
- Frozen contract pipelines — not planned
- Integrated terminal — not planned
- Mobile companion — not planned
- SSH remote execution — not planned

## Scope items

### 1. MoA review fan-out

#### 1a. Go daemon: `review_diff` command

New IPC command `CmdReviewDiff = "review_diff"`.
Request: `diff_id`.
Response: `reviews` (array of ReviewResult).

```go
type ReviewResult struct {
    Model   string `json:"model"`
    Verdict string `json:"verdict"` // "accept" | "reject" | "needs_fixes"
    Comments string `json:"comments"`
}
```

Implementation:
- Read the diff content from the diff file.
- Read review models from `~/.odo/prefs.md` (new `review:` line, comma-separated
  list of `model@provider` entries).
- For each review model, start an OMP run (via `NewOMPForKey`) with a review
  prompt: "Review this diff. Verdict: ACCEPT/REJECT/NEEDS_FIXES. Comments: ..."
- Run all reviews in parallel (goroutines).
- Wait for all to finish (with a timeout).
- Return the array of ReviewResult.

This reuses the existing adapter infrastructure. Each review is a one-shot
OMP run in a temp directory, similar to distill.

#### 1b. Frontend: Review button + results panel

- Add a "Review" button next to "Accept" and "Reject" in the diff card.
- On click, calls `review_diff` API. Shows loading state.
- On completion, shows a review results panel:
  - Each model's verdict as a badge (green=ACCEPT, red=REJECT, yellow=NEEDS_FIXES).
  - Comments below each verdict.
  - If any model REJECTs, highlight in red and disable "Accept".
- The review results are journaled as `review_action` events with
  `action: "moa_review"` and the review results in the payload.

### 2. Settings panel

#### 2a. Go daemon: `get_settings` + `update_settings` commands

`CmdGetSettings = "get_settings"`:
- Response: `settings` object with current values from `~/.odo/prefs.md`.
  Fields: `coding_model`, `coding_provider`, `orchestrator_model`,
  `orchestrator_provider`, `omp_timeout`, `default_adapter`, `review_models`.

`CmdUpdateSettings = "update_settings"`:
- Request: `settings` object with fields to update.
- Implementation: reads current prefs.md, updates the specified lines,
  writes back. Does NOT restart the daemon — changes take effect on next run
  (adapter re-reads prefs on each Start call, as designed in M0.1).

#### 2b. Frontend: Settings panel UI

- Gear icon in the sidebar header.
- Clicking opens a settings modal/panel:
  - Coding model: text input (model@provider format)
  - Orchestrator model: text input
  - OMP timeout: number input (seconds)
  - Default adapter: dropdown (OMP/Pi)
  - Review models: text input (comma-separated list)
- "Save" button calls `update_settings`.
- "Close" button closes the panel.
- Changes are applied immediately to prefs.md (next run uses new values).

### 3. Parallel agent fan-out

#### 3a. Go daemon: `fanout_send` command

`CmdFanoutSend = "fanout_send"`:
- Request: `conversation_id`, `text`, `n` (number of parallel agents).
- Implementation:
  1. Create N worktrees from the current HEAD.
  2. Start N adapter runs in parallel (each in its own worktree).
  3. Return an array of run IDs.
- Polling: `poll_events` continues to work per-conversation, but the
  response includes a `runs` array showing all active runs with their status.
- When a run finishes, its diff is extracted as usual.
- The user accepts/rejects each diff independently.

#### 3b. Frontend: Fan-out button + multi-run view

- "Fan-out" button next to "Send" (or a dropdown: Send / Fan-out).
- On click, shows a small input for N (default 2).
- Starts N parallel runs. The chat area shows all runs in a list:
  - Each run shows: status (running/done), agent_text summary, diff status.
  - Accept/Reject buttons per run.
- The user picks the best diff and accepts it. Other runs are auto-rejected
  (their worktrees are cleaned up).

## Architecture decisions for M2

| Decision | Value |
|---|---|
| MoA review | Parallel OMP runs via NewOMPForKey, each with review prompt |
| Settings storage | ~/.odo/prefs.md (unchanged from M0.1) |
| Settings UI | Modal panel, gear icon in sidebar |
| Fan-out | N worktrees from HEAD, N parallel adapter runs |
| Review models | Comma-separated `review:` line in prefs.md |
| Review journaling | `review_action` with `action: "moa_review"` |
| Fan-out cleanup | Accept one → auto-reject others → cleanup worktrees |

## Verification

```bash
go build ./... && go vet ./... && go test ./... -count=1
cd gui && npx tsc --noEmit && npm run build
cd src-tauri && cargo check
```

New tests:
- `TestReviewDiff`: review a diff with 2 stub models, verify parallel execution + verdicts.
- `TestGetSettings`: read current settings from prefs.md.
- `TestUpdateSettings`: update settings and verify prefs.md is rewritten.
- `TestFanoutSend`: send to 2 parallel agents, verify both produce diffs.
