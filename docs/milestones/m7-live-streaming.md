# M7 — Live Streaming (Block-Level + Preview)

Shipped design: **K3+DSF approach** (2/3 of the tri-model spec, confirmed by
first-principles analysis). An earlier draft of this document proposed
tailing the session JSONL file; that approach was discarded. What follows is
what was implemented.

## Pain

The OMP adapter runs a subprocess, captures output to a file, and the daemon
polls the adapter's `Events()` on every `poll_events` IPC to drain events.
M0–M6 `Events()` returned nothing while the run was in flight — all events
appeared in a single burst at process exit, minutes after the prompt: "⟳
Working…" wall, then text + tool calls + done at once.

## Demo

1. Type "Create a file called hello.txt with the text 'Hello from Odo'" and
   Send.
2. Within ~1 s a dimmed, italic preview bubble shows the agent's first text
   block growing, ending in a pulsing caret.
3. When the block completes it converts to a normal journaled bubble.
4. While a tool runs, the preview shows "⟳ bash — Echoing hello-world"
   (spinning ⟳ + tool name + intent). The ToolTicker keeps spinning.
5. Block by block: text → tool call → tool result → next text block → ✓
   with duration → diff card with [Accept] [Reject]. No burst.

## Architecture

```
OMP subprocess (--mode json) → output.txt (JSONL stream)
                                    ↓
Adapter (omp.go): tail output.txt with byte-offset cursor per Events() call
  → parse JSONL (message_update.text_*, tool_execution_start/end)
  → Events() returns completed blocks + trailing transient preview
                        ↓
Daemon (server.go): drainRun() strips the partial preview (never journaled,
  never advances consumed), journals completed blocks, stashes the preview
  on runMeta
                        ↓
Frontend (App.tsx): poll_events every 350 ms running / 1500 ms idle
  → journaled blocks render as normal bubbles
  → preview renders as the dimmed preview bubble (replaced wholesale)
```

**Latency budget:** OMP writes a JSONL line at block completion (~0 ms) →
next frontend poll (≤350 ms) drains + journals + returns → render.
Worst case ~700 ms, typical ~400 ms.

## Design decisions (the lock)

1. **Source: `--mode json`, tail `output.txt`.** OMP's JSONL event stream
   goes to stdout; the wrapper pipes it into the output file (stderr merged
   via `2>&1` — non-JSON lines fail the parse and are skipped). The session
   JSONL file is kept only as the completion-time fallback. Verified live:
   `omp --mode json` emits `{"type":"session"…}` as the first bytes, then
   `message_update` sub-events (`text_start`/`text_delta`/`text_end`) for
   text and top-level `tool_execution_start`/`tool_execution_end` for tools.
2. **Granularity: one journaled event per completed block.** Deltas live
   only in a transient preview. There is no journaled delta type.
3. **Transport: unchanged `poll_events`, adaptive interval.** 350 ms while
   `agent_running`, 1500 ms idle. Long-poll rejected (`handleConn` is
   synchronous); WebSocket rejected (no new transport for a read-side UX).
4. **Rendering: one dimmed preview bubble.** Journal bubbles untouched.
   Text preview: opacity 0.6 + italic + pulsing caret. Tool preview:
   spinning ⟳ + tool name + intent. Replaced wholesale per poll; when the
   completed block arrives the preview disappears and the real bubble shows.
5. **Schema: no new event types, no store DDL.** Preview payloads add
   `partial:true` (+ `tool`/`call_id`/`intent` for tool previews) on the
   existing `agent_text`/`agent_tool_call` types. `poll_events` responses
   gain `preview?` and `streaming?`. Journaled blocks keep ADR-0002 keys
   exactly (`tool`/`args`, `tool`/`result`). Rust bridge is opaque `Value`:
   zero Rust changes.
6. **Auto-detection per run.** First byte of `output.txt`: `{` → stream
   mode; anything else → legacy byte-for-byte path. Empty/absent file stays
   undecided and retries next poll. Old text stubs can't regress. Streaming
   stubs sleep between JSONL appends.
7. **Distill stays one-shot.** `runOneShot` strips the trailing partial
   event (not counted, not concatenated); the final text it returns is
   unchanged.

## Event mapping (real stream → adapter events)

| JSONL line | Adapter result |
|---|---|
| `message_update` → `text_start` / `text_delta` | Preview: `agent_text{text, partial:true}`, accumulating |
| `message_update` → `text_end` | Journal `agent_text{text}` (deltas as fallback when `content` absent); clear preview |
| `message_end` (assistant) | Safety net: journal text blocks only when the message streamed nothing (non-streaming provider) |
| `tool_execution_start` | Preview `agent_tool_call{tool, call_id, intent, partial:true}`; stash args per `call_id` |
| `tool_execution_end` | Journal `agent_tool_call{tool, args}` + `agent_tool_result{tool, result}` (args merged from start; end carries none); clear preview |
| `tool_execution_update`, `thinking_*`, `turn_*`, `message_*` (user/toolResult), `session`, `agent_*` | Ignored |
| Non-JSON line (stderr noise, `[OMP_TIMEOUT]` diagnostic) | Skipped by the parse |

Thinking blocks stay hidden (M0.1 decision). `agent_done`/`agent_error`
remain the terminal events, appended after the final tail when the process
exits; cancel/error mid-stream keeps journaled blocks and drops the preview.

## Backend changes

### `internal/adapter/omp.go`

- `Start()`: appends `--mode json` after `--session-dir` (wrapper forwards
  it as an extra omp arg).
- `ompRun`: `streamMu`, `streamMode`/`streamLegacy`
  (detected), `streamOffset` (byte cursor), `streamEvents` (journaled
  blocks), `streamPreview`, `terminalAdded`, `textAcc` (text deltas per
  content index), `msgStreamed` (message_end dedup), `toolAcc`
  (`pendingTool{name,args,intent}` per call ID).
- `detectStream()`: first-byte detection. `tailStream(final)`: reads from
  `streamOffset`, parses complete lines, leaves a partial trailing line for
  the next call (parses it when `final`). `streamLine()`: the mapping table
  above. `appendTerminalLocked()`: `agent_done`/`agent_error` after the
  last block.
- `Events()`: streaming + running → journaled blocks since `afterSeq` plus
  the preview as the last element; streaming + done → final tail, terminal
  event, **no re-parse** of the session JSONL; if the stream journaled
  nothing at completion, degrade to `buildEvents` (session JSONL → text
  output). Legacy runs behave exactly as M0–M6 (nil while running,
  `buildEvents` at exit).
- `buildEvents` refactored to share `doneSummary` / `agentErrorEvent` with
  the streaming terminal path.

### `internal/ipc/server.go` + `protocol.go`

- `drainRun()`: strips a trailing `partial:true` event before journaling,
  stashes it on `runMeta.previewEvent`. It never advances `consumed` — the
  next `Events()` call re-sends the completed block it was previewing.
- `handlePollEvents()`: returns the primary run's `preview` plus
  `streaming:true` while a preview is present. Fan-out runs journal
  normally; their previews are not surfaced.
- `runOneShot()`: strips the trailing partial event (distill / MoA review
  unchanged).

### Consumers' contract

The preview is always the **last** element of `Events()` and carries
`partial:true`. Consumers that advance a cursor (daemon, `runOneShot`) MUST
strip it before counting. Documented on `Adapter.Events`.

## Frontend changes

- `gui/src/types.ts`: `PreviewEvent`; `PollEventsResponse.preview?` /
  `streaming?`; payload keys `partial?`, `intent?`, `call_id?`.
- `gui/src/App.tsx`: adaptive interval (`agentRunning ? 350 : 1500`;
  interval resets when the flag flips); `preview` state replaced wholesale
  per poll, cleared on bootstrap/workstream switch; passed to ChatSurface.
- `gui/src/components/ChatSurface.tsx`: `PreviewBubble` (text+caret /
  "⟳ tool — intent") rendered above the ToolTicker; auto-follow scroll
  triggers on preview changes too. Returns null for an empty text preview.
- `gui/src/styles/app.css`: `.bubble-preview` (opacity 0.6, italic),
  `.preview-caret` (reuses `ws-pulse`), `.preview-spinner` (reuses `spin`).

## Tests

`internal/adapter/omp_stream_test.go` (fake run = output file + done
channel; first-byte detection asserts on `streamMode`/`streamLegacy`):

- `TestStreamModeDetection` — `{` → stream; text → legacy; empty → undecided
- `TestStreamTextDelta` — preview accumulates deltas; `text_end` journals
  once; `message_end` does not duplicate; terminal summary from the block
- `TestStreamToolExecution` — start → partial preview (`tool`/`call_id`/
  `intent`, no `args`); end → journaled call+result with args merged
- `TestStreamLegacyFallback` — text output: nil mid-run, `agent_text` +
  `agent_done` at exit, byte-for-byte
- `TestStreamPartialLineSkipped` — unterminated line held at the cursor,
  parsed when completed
- `TestStreamMessageEndFallback` — no-delta provider: text journals at
  `message_end`; a later streamed message isn't duplicated
- `TestStreamTerminalError` — killed mid-run: blocks survive, preview
  dropped, `agent_error` carries stderr tail

`internal/ipc/streaming_test.go` (E2E through the socket):

- `TestStreamingVisibleLoopPreview` — streaming stub sleeps between JSONL
  appends (2 s delta→`text_end` window); asserts a partial preview passes
  `poll_events` with `streaming:true`, `partial` never journals, the
  journal ends `[user_message agent_text agent_done]`, and the preview is
  gone after completion.

Existing suites unchanged: text stubs auto-detect legacy (94 s full
`internal/ipc` run passes; `internal/adapter` passes).

## Migration plan

| Component | Change |
|---|---|
| `internal/adapter/omp.go` | `--mode json` + incremental tail + preview |
| `internal/ipc/server.go`, `protocol.go` | preview strip/stash/return; `streaming` flag; `runOneShot` guard |
| `gui/src/{App,types}.tsx/ts`, `ChatSurface.tsx`, `app.css` | adaptive poll + preview bubble |
| store / ADR-0002 / IPC commands / wrapper script / Rust | **unchanged** |

Rollout order used: adapter + tests → daemon + E2E test → frontend → gates.
Final gates: `go build/vet/test ./...` ✓, `tsc --noEmit` + `vite build` ✓,
`cargo check` ✓, real `omp_with_timeout.sh … --mode json` smoke run ✓
(exit 0, output begins `{"type":"session"…}`).

## Not built

- **Token-level journaled streaming / typing animation.** Deltas are
  preview-only, replaced wholesale per poll (≤350 ms cadence). No delta
  event type.
- **WebSocket transport / long-polling.** `poll_events` stays; 350 ms
  running latency is sub-second and the daemon stays single-connection
  synchronous.
- **Thinking-block rendering.** Stream `thinking_*` sub-events are parsed
  past, not shown. A future "thinking…" indicator could consume them.
- **Background daemon drain goroutine.** Journaling rate is tied to the
  frontend poll rate, which the 350 ms interval already covers.
- **Distill live progress.** `runOneShot` still blocks to the terminal
  event and returns the same concatenated text.
- **Preview for fan-out runs.** Each fan-out run journals fine; only the
  primary run's preview is surfaced (single `preview` field).
