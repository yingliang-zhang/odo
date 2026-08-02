# M1 — Multi-Workstream + Memory + E2E

## Pain

M0.1 works but:
- I can only work in one conversation; I can't switch between tasks without
  losing context (pain point #1 — the original pain).
- When a conversation gets long, the context fills up and the agent degrades.
  There's no way to distill the conversation into a wiki note and start fresh.
- I can't send steering messages while the agent is working — I have to wait
  for it to finish, then send a new message.
- The Pi adapter (alternative coding agent) isn't wired up — I'm locked to OMP.
- There's no automated GUI test, so every fix requires manual `tauri dev` testing.

## Demo

### Demo A: Multi-workstream + steering

1. Open Odo. Left rail shows the current project with one workstream ("main").
2. Click "+" to create a new workstream. Name it "refactor-auth".
3. The chat switches to the new workstream. Type: "Refactor the auth module
   to use JWT instead of session cookies."
4. Click Send. Agent starts working.
5. While agent is working, type a steering message: "Also add rate limiting
   to the login endpoint." Click Send. The message is queued; the agent
   sees it on the next turn.
6. Switch back to "main" workstream. The previous conversation is still there.
7. Switch to "refactor-auth". The agent's progress is visible.

### Demo B: Memory distiller

1. In a long conversation (50+ messages), click "Distill" in the sidebar.
2. The agent generates a markdown wiki note summarizing key decisions,
   code changes, and open questions.
3. The note is saved to `<project>/wiki/<workstream>-epoch-N.md`.
4. A new epoch starts: the conversation continues but the context is
   compacted (the wiki note replaces the old messages in the context window).
5. The agent's next response references the wiki note, not the old messages.

### Demo C: Pi adapter

1. In settings (sidebar), select "Pi" as the adapter.
2. Send a message. The Pi adapter processes it (5-verb interface: Start/
   Send/Events/Cancel/Close).
3. The conversation and diff flow is identical to OMP — the user sees no
   difference except the agent's output style.

### Demo D: E2E automated test

1. Run `go test ./internal/ipc/... -run TestM1E2E`.
2. The test creates a temp project, starts the daemon, creates multiple
   workstreams, sends messages, steers, distills, switches, and verifies
   the full loop without a human.
3. Run `cargo test` for Tauri-level integration tests (gated on a
   `ODO_E2E_DAEMON` env var like the M0 smoke test).

## Not built

- MoA review fan-out — M2
- Settings panel — M2
- Parallel agent fan-out — M2+
- Attestation — M1+ (only if user requests)
- Sandbox — not planned

## Scope items

### 1. Multi-workstream UI + API

The schema already supports multiple workstreams (ADR-0002). The UI needs:
- Sidebar: list workstreams for the current project. Each shows name + status
  (active/idle) + last activity timestamp.
- "New workstream" button: creates a workstream with a user-chosen name.
  Backend: `CreateOrGetWorkstream` already exists; add an IPC command
  `create_workstream` that takes a name.
- Workstream switch: clicking a workstream in the sidebar switches the
  active conversation. Backend: `bootstrap` already returns the latest
  conversation for the default workstream; extend to accept a workstream ID
  and return/create the appropriate conversation.
- IPC changes: `bootstrap` gains optional `workstream_id`; new command
  `create_workstream` with `project_root` + `name`.
- Frontend: sidebar renders workstream list; clicking switches context;
  the polling loop polls the active conversation only.

### 2. Steering (send while agent runs)

Currently `handleSendMessage` rejects new messages while an agent is running
(`server.go:179-182`). For M1:
- Add a `steering` field to the `send_message` request (or a new
  `steer_message` command). Steering messages are journaled as
  `user_message` events but do NOT start a new agent run.
- The adapter's `Send` method is called with the steering text. For OMP,
  this means the text is appended to the prompt for the next turn (or
  queued if the wrapper doesn't support mid-run injection).
- M1 limitation: OMP print mode may not support mid-run steering. The
  steering message is journaled and shown in chat immediately; the agent
  sees it when it processes the next turn (if the adapter supports `Send`).
  If the adapter returns "not supported" (like M0's OMP), the message is
  journaled but the user is told "steering not supported by current adapter."

### 3. Memory distiller (epoch → wiki)

- New IPC command `distill` that:
  1. Calls the orchestrator model (from prefs.md) with a prompt: "Summarize
     the key decisions, code changes, and open questions from this conversation."
  2. The conversation events are passed as context.
  3. The model's response is saved to `<project>/wiki/<workstream>-epoch-N.md`.
  4. A new epoch is started: `UpdateConversationEpoch` increments the epoch.
  5. Old events remain in the journal (append-only) but the UI shows only
     events from the current epoch.
- Frontend: "Distill" button in sidebar. After distillation, the chat
  shows a "Epoch N — previous epoch distilled to wiki/<note>.md" banner.
- The distiller uses the orchestrator model (GLM-5.2 from prefs.md), not
  the coding model, to avoid coupling summarization with code generation.

### 4. Pi adapter implementation

- Implement the `Adapter` interface for Pi (same 5 verbs: Start/Send/Events/
  Cancel/Close).
- Pi runs as a subprocess (similar to OMP) but with different command-line
  args and output format.
- The Pi adapter is registered alongside OMP; the user selects which adapter
  to use in the workstream or project settings.
- M1 scope: Pi adapter exists and can run a simple task. Full Pi feature
  parity (steering, streaming) may be deferred to M1.1.

### 5. E2E test infrastructure

#### 5a. Daemon-level E2E (Go)
- Extend `TestVisibleLoopAcceptRejectRestore` to cover M1 features:
  multi-workstream creation/switch, steering, distill, Pi adapter (with stub).
- All tests use stub wrappers (no real OMP/Pi calls).

#### 5b. Tauri-level integration test (Rust)
- Add a `#[test]` that starts a real daemon with a stub OMP wrapper,
  connects via the Rust socket client, and verifies the full bootstrap →
  send → poll → accept → restore cycle through the Tauri command layer.
- Gate on `ODO_E2E_DAEMON` env var (like the M0 smoke test).

#### 5c. Browser-level E2E (optional, time permitting)
- Use Tauri's WebDriver support or Playwright to test the actual UI:
  click buttons, type in input, verify diff rendering, drag-and-drop.
- Gate on a `ODO_E2E_UI` env var. May be deferred to M1.1 if time is tight.

## Architecture decisions for M1

| Decision | Value |
|---|---|
| Workstream creation | New IPC command `create_workstream`; UI sidebar list |
| Steering | Journaled as `user_message`; adapter `Send` called if supported |
| Memory distiller | Uses orchestrator model; saves to `<project>/wiki/`; epoch increment |
| Pi adapter | Same 5-verb interface; subprocess; registered alongside OMP |
| E2E tests | Go daemon-level (stub) + Rust Tauri-level (gated on env var) |
| Polling | Unchanged — 1.5s interval (M0 decision carries forward) |
| Epoch compaction | Old events stay in journal; UI filters by current epoch |
