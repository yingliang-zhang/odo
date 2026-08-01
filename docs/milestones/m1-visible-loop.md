# M1 — Visible Loop

## Pain

Can't send a message to an AI coding agent, see it work, review the diff, and have it applied to my repo — then close the app and reopen it with the conversation still there. (Pain points #2, #3)

## Demo

1. Open Odo. Left rail shows the current project. Chat surface is empty.
2. Type: "Create a file called hello.txt with the text 'Hello from Odo'"
3. Click Send. The message appears in the chat.
4. An agent activity indicator shows "Running..." with a live tool ticker:
   - "Using tool: write → hello.txt"
5. When the agent finishes, a diff card appears inline:
   ```
   + Hello from Odo
   ```
   with [Accept] [Reject] buttons.
6. Click Accept. The file `hello.txt` now exists in the project directory.
7. Quit Odo completely (Cmd+Q).
8. Reopen Odo. The conversation is restored — the user message, the agent
   activity, and the accepted diff are all visible.
9. Run `cat hello.txt` in a terminal — it contains "Hello from Odo".

## Not built

- File attachments (drag-and-drop, clipboard paste) — M0.1
- Steering (submit messages while agent works) — M1+
- Pi adapter implementation — M1 (interface defined, OMP only for M0)
- Multi-workstream concurrent UI — M1 (schema supports it, UI is single-workstream)
- Memory distiller (epoch → wiki) — M1
- Workstream epoch rotation / compaction — M1
- MoA review fan-out — M2
- Cryptographic attestation — M1+ (ADR-0001 defers)
- Sandbox containment — not planned
- Frozen contract pipelines — not planned
- Settings panel — M2
- Integrated terminal — not planned
- Syntax highlighting in diff viewer — M0.1
- Parallel agent fan-out — M2+
- Mobile companion — not planned
- SSH remote execution — not planned

## Architecture decisions for M0

| Decision | Value |
|---|---|
| Agent execution | OMP subprocess, print mode, polled events |
| Event transport | Polling (1.5s interval), declared as polling |
| Storage location | `<project>/.odo/worktrees/<run-id>` + `<project>/.odo/diffs/` (not /tmp) |
| `git apply` | In Go daemon (Invariant 1) |
| Worktree lifetime | Persists until accept/reject/close (no defer RemoveAll) |
| Restore | Full-quit journal restore on next launch |
| Journal schema | Fresh (ADR-0002), 5 tables |
| Frontend | React + Vite in Tauri 2 WebView |
| Model config | `~/.odo/prefs.md` — orchestrator + coding + review_panel |
| In-app orchestrator | None — user IS the orchestrator in M0; chat talks directly to coding adapter |
