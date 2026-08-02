# M3 — Memory Recall + Wiki Browser (+ user.md + Visibility Pack)

> **Scope amendment (2026-08-02).** Dual-model (GLM-5.2 + K3) memory-architecture
> review added two items to the frozen recall/browser scope: global user memory
> (`~/.odo/user.md`, same injection code path as wiki recall) and a small
> visibility pack (run status bar, desktop notification, workstream badges)
> plus the M2 F5 follow-up (`default_adapter` consumed). Architecture
> rationale lives in ADR-0003.

> **Rationale.** M1's distiller closes only the *write* half of the memory
> loop: it summarizes a conversation into `wiki/<ws>-epoch-N.md` and bumps the
> epoch, but `buildPrompt` (the single chokepoint that assembles every agent
> prompt) never reads a wiki note back. The next agent run — whether a single
> `send_message` or an M2 `fanout_send` — starts in its worktree with zero
> project memory, re-asking questions the conversation already settled. This
> is the most direct miss against pain point #1 (the first-listed: "Memory
> fills up — per-project structured wiki") and #2 ("Context loss on session
> switch"), both within one hop. The alternatives are weaker: progress
> visualization (#3) is already half-addressed by M0.1's ToolTicker and
> polishes a loop that already closes; a standalone "structured wiki" without
> recall is more write-only plumbing; session compaction is just distill
> renamed; and diff-compare UX for fan-out is polish that traces to no pain
> point in three hops. M3 closes the loop by making `buildPrompt`
> memory-aware (recall) and making the result *visible* inside Odo (wiki
> browser) — the two share one code path (reading `wiki/` files) and
> principle #3 ("invisible work is presumptive scope creep") requires the
> browser to ship in the same milestone as recall, not later.

## Pain

M2 works but:
- I distill a long conversation into a wiki note, but the very next agent run
  doesn't see it. The agent re-asks questions we already answered — the
  distill is a write-only artifact, not memory. (pain #1, #2)
- The agent knows nothing about *me*: durable cross-project principles
  (first-principles reasoning, concise output, tool preferences) never reach
  the prompt, so every run re-learns my working style from scratch.
  `~/.odo/prefs.md` configures models but is daemon-side config, never
  injected. There is no user-level memory. (pain #1, #2)
- When I switch workstreams or restart, the epoch banner says "previous
  epochs distilled to the wiki," but neither I nor the agent can read what
  was distilled from inside Odo. The memory exists on disk and is invisible.
  (pain #1, #2)
- After several distills I have a pile of `wiki/<ws>-epoch-N.md` files I
  can only read by leaving Odo and opening a text editor. I can't verify what
  the agent will "remember" before I send the next message. (pain #1)
- While a run executes I can't see at a glance how long it has been going or
  what tool it's on, and when it finishes in another window I get no signal —
  I sit and poll. (pain #3)
- With several workstreams, the sidebar doesn't tell me which one has a live
  run or an unreviewed diff waiting. (pain #3)

## Demo

### Demo A: Recall closes the distill loop

1. In a workstream with 20+ messages about an auth refactor, click "Distill".
   The wiki note is written and the epoch banner appears (as in M1).
2. Type a new message: "Continue the auth refactor — add the token refresh
   endpoint." Click Send.
3. The `user_message` bubble in chat shows "memory: user.md + 1 note
   recalled" — one chip for the global user memory, one per wiki note.
4. The agent's response references the distilled decisions — e.g., "Per the
   JWT decision from epoch 1, I'll add the refresh endpoint at
   `/auth/refresh`…" — instead of starting from scratch.
5. The diff builds on the established pattern; the agent did not re-ask what
   `distill` already captured.

### Demo B: Wiki browser — see and verify the memory

1. In the sidebar, the Memory section now shows "N wiki notes" below the
   Distill button, with a "Browse" link.
2. Click "Browse". A panel opens. The first row is always a pinned
   "user.md (global)" entry; below it the wiki notes for the workstream:
   `main-epoch-1`, `main-epoch-2`, … with modification times, newest first.
3. Click a note. Its full markdown content renders in a reader view inside
   Odo — no external editor needed. Clicking the user.md row shows the global
   user memory (or a hint to create `~/.odo/user.md` if absent).
4. The user can verify exactly what the agent will recall before sending the
   next message.

### Demo C: See the run, get told when it's done

1. Send a message that takes a couple of minutes. A status line appears in
   the chat panel: `running — 1m 12s — tool: bash (call 7)`, updating live.
2. Switch to another app. When the run finishes, a macOS notification
   "Odo: run finished in main" appears.
3. In the sidebar, the running workstream shows a green dot; a workstream
   with unreviewed diffs shows a red pill with the pending count. No polling,
   no clicking into each workstream to check.

## Not built

- Wiki note editing — deferred past the M5 curator (ownership rule per
  ADR-0003: curator owns topic pages, humans own memory.md and pins)
- Project memory.md (`.odo/memory.md`) + automatic user.md learning
  (preference distiller) — M4 (ADR-0003)
- Structured wiki index / topic pages / curator pass — M5 (ADR-0003)
- Pull-based recall (`odo wiki read`) + ledger.md with verified metrics —
  M6 (ADR-0003)
- Cross-workstream memory — recall is per-workstream (matches M1's distill
  scope); project-wide recall is M4+
- Attestation / sandbox / integrated terminal (PTY) — not planned
- Diff-compare UX for fan-out — polish, traces to no pain point in 3 hops
- Richer streaming progress — M3 ships only the status bar; deeper
  visualization is polish

## Scope items

### 1. Memory recall (daemon-side prompt injection)

#### 1a. Go daemon: `recallWikiNotes` + `buildPrompt` memory parameter

New helper in `internal/ipc/server.go`:

```go
// recallMemoryCap bounds the total recalled memory injected into a prompt.
// Wiki notes are distill summaries (small by design); the cap keeps a
// long-running project from overwhelming the agent's context window. Notes
// are included most-recent-first; the cut happens on a note boundary so no
// note is half-included.
const recallMemoryCap = 12 * 1024 // 12 KB ≈ 3k tokens

// recallWikiNotes reads all wiki/<workstreamName>-epoch-*.md files for the
// workstream, ordered newest-epoch first, concatenates them under headers,
// and truncates to recallMemoryCap on a note boundary. Returns the memory
// block ("" when no notes exist) and the paths of the notes actually
// included (for journaling).
func recallWikiNotes(projectRoot, workstreamName string) (memory string, paths []string)
```

Implementation:
- Glob `filepath.Join(projectRoot, "wiki", workstreamName+"-epoch-*.md")`.
- Sort by epoch descending (parse the `-epoch-N` suffix).
- Build a `strings.Builder`: for each note, write `## <note basename>\n\n`
  + file content + `\n\n---\n\n`. Track cumulative size; stop when the next
  note would exceed `recallMemoryCap`.
- Return the built string and the included paths. No notes → `("", nil)`.

Global user memory helper (new file `internal/ipc/recall.go` or alongside):

```go
// userMemoryCap bounds the global user memory injected into every prompt.
// Durable principles are few by nature; the cap keeps steering small by
// design (ADR-0003).
const userMemoryCap = 4 * 1024 // 4 KB ≈ 1k tokens

// readUserMemory reads ~/.odo/user.md (global, user-maintained durable
// principles and preferences). Returns "" when the file is absent or empty.
// Content is capped at userMemoryCap with a line-boundary cut. M3 only
// reads this file; M4 adds the learner that writes it.
func readUserMemory() string
```

`buildPrompt` gains user memory, injected FIRST (stable prefix is
prompt-cache friendly; ADR-0003 injection order
user.md → wiki → attachments → message):

```go
// buildPrompt renders the agent prompt. userMem (global, durable user
// principles) is injected first, then project memory (distilled wiki
// notes), then attachment hints, then the user's text.
func buildPrompt(text string, attachments []string, userMem, memory string) string {
    var b strings.Builder
    if userMem != "" {
        b.WriteString("## User memory (durable cross-project principles)\n\n")
        b.WriteString(userMem)
        b.WriteString("\n\n---\n\n")
    }
    if memory != "" {
        b.WriteString("## Project memory (from prior distilled epochs)\n\n")
        b.WriteString(memory)
        b.WriteString("\n\n---\n\n")
    }
    if len(attachments) > 0 {
        fmt.Fprintf(&b, "Attached files: %s. Read them before proceeding.\n\n",
            strings.Join(attachments, ", "))
    }
    b.WriteString(text)
    return b.String()
}
```

Call-site changes in `handleSendMessage` and `handleFanoutSend`:
- After resolving the conversation `c`, look up the workstream via
  `s.store.GetWorkstream(ctx, c.WorkstreamID)` to get `w.Name`.
- Call `memory, recallPaths := recallWikiNotes(s.projectRoot, w.Name)`.
- Call `userMem := readUserMemory()`; when non-empty, prepend the marker
  path: `recallPaths = append([]string{"~/.odo/user.md"}, recallPaths...)`.
- Pass to `buildPrompt`:
  `prompt := buildPrompt(req.Text, req.Attachments, userMem, memory)`.
- Extend the journaled `user_message` payload with the recall paths:

```go
msgPayload := map[string]interface{}{"text": req.Text}
if len(req.Attachments) > 0 {
    msgPayload["attachments"] = req.Attachments
}
if len(recallPaths) > 0 {
    msgPayload["recall"] = recallPaths
}
```

This is a payload-key extension on the existing `user_message` event type
(ADR-0002) — the same pattern M1 used to extend `review_action` with
`action: "distill"`. No new event type, no new table, no schema migration.
Backward-compatible: when no wiki notes exist, `recall` is absent and the
prompt is identical to today.

#### 1b. Frontend: recall chip on `user_message` bubble

- `types.ts`: extend `EventPayload` with `recall?: string[]`.
- `MessageBubble.tsx`: in the `user_message` case, if `p.recall` is non-empty,
  render a chip below the text. The path `~/.odo/user.md` renders as the
  literal label `user.md`; wiki paths are shortened to `wiki/<basename>`.
  Chip label: `memory: user.md + {n-1} note(s) recalled` when user.md is
  present, else `memory: {n} note(s) recalled`. The chip uses a distinct
  style (`.recall-chip`, muted color) so the user can see at a glance that
  memory was injected.
- No new API call — the recall paths arrive in the existing `poll_events` /
  `bootstrap` event stream, already parsed by the frontend.

### 2. Wiki browser (IPC + UI)

#### 2a. Go daemon: `list_wiki` + `read_wiki` commands

New IPC commands in `internal/ipc/protocol.go`:

```go
CmdListWiki = "list_wiki"
CmdReadWiki = "read_wiki"
```

New `Request` field:

```go
Path string `json:"path,omitempty"` // read_wiki: wiki note path
```

(`conversation_id` already exists on `Request` and is used by `list_wiki` to
resolve the workstream name, matching how `distill` works.)

New response types:

```go
// WikiNoteInfo describes one distilled wiki note for the browser list.
type WikiNoteInfo struct {
    Path      string `json:"path"`
    Name      string `json:"name"`       // e.g. "main-epoch-1"
    Epoch     int    `json:"epoch"`     // parsed from the filename
    ModifiedAt string `json:"modified_at"`
}
```

New `Response` fields:

```go
WikiNotes  []WikiNoteInfo `json:"wiki_notes,omitempty"`
WikiContent string        `json:"wiki_content,omitempty"`
```

`handleListWiki`:
- Resolve the conversation → workstream (reuse `checkConversation`).
- Glob `wiki/<ws.Name>-epoch-*.md`, parse the epoch from each filename.
- Stat each file for `ModifiedAt`. Return the list sorted by epoch descending.

`handleReadWiki`:
- Allow exactly two path classes: (a) inside `<projectRoot>/wiki/`
  (path traversal guard: `filepath.Rel` + prefix check), or (b) exactly
  `~/.odo/user.md` — the one global file, so the browser can show the pinned
  user memory row.
- Read the file content. Return as `WikiContent`. Missing user.md returns
  ok with empty `wiki_content` (frontend renders a create-hint).

Both handlers are read-only file operations — no journal writes, no schema
impact. They reuse the workstream-resolution logic already shared by
`handleDistill`.

#### 2b. Frontend: `WikiBrowser` component + sidebar integration

New API functions in `api.ts`:

```ts
export function listWiki(conversationId: number): Promise<ListWikiResponse> {
  return invoke<ListWikiResponse>("list_wiki", { conversationId });
}
export function readWiki(path: string): Promise<ReadWikiResponse> {
  return invoke<ReadWikiResponse>("read_wiki", { path });
}
```

New types in `types.ts`:

```ts
export interface WikiNoteInfo {
  path: string;
  name: string;
  epoch: number;
  modified_at: string;
}
export interface ListWikiResponse {
  ok: boolean;
  error?: string;
  wiki_notes?: WikiNoteInfo[];
}
export interface ReadWikiResponse {
  ok: boolean;
  error?: string;
  wiki_content?: string;
}
```

`Sidebar.tsx` — extend the existing Memory section:
- Below the Distill button, show a note count ("N wiki notes") and a "Browse"
  button. The count is fetched on bootstrap/workstream-switch via `listWiki`
  (cheap, read-only; can be cached in App state alongside `workstreams`).
- Clicking "Browse" opens the `WikiBrowser` modal.

New `WikiBrowser.tsx` component:
- Modal panel (same overlay style as `SettingsPanel`).
- Left: a pinned synthetic first row `user.md (global)` (path
  `~/.odo/user.md`, always present even when zero wiki notes), then the list
  of `WikiNoteInfo` entries (name + epoch + relative time), newest first.
  Clicking selects one.
- Right: reader view rendering the selected note's markdown content (fetched
  via `readWiki`). For the thin loop, render as preformatted text with
  markdown-style headers/links styled by CSS (no heavyweight markdown library;
  the same "dependency-free" principle as M0.1's syntax highlighting).
  Selecting the user.md row with empty content renders a hint:
  "No ~/.odo/user.md yet — create it to give agents your durable principles."
- Close button returns to the sidebar.

Tauri Rust pass-through (`lib.rs`):
- Two new commands, `list_wiki` and `read_wiki`, forwarding to the daemon with
  `READ_TIMEOUT`. Register both in `invoke_handler`.

### 3. Visibility pack (status bar, notification, badges) + default_adapter fix

#### 3a. Run status bar (frontend, derived from existing events)

- While the active conversation has a running agent, a status line in the
  chat panel header shows:
  `running — <elapsed> — tool: <last tool> (call <n>)`.
- Derived entirely from the event stream the frontend already holds:
  elapsed = now − the run's first `agent_running`/user_message event; last
  tool + count from `tool_call` events for the active run. 1 s local timer
  re-render; no new IPC, no daemon change.

#### 3b. Desktop notification on `agent_done` (Tauri plugin)

- Use the official `@tauri-apps/plugin-notification` (JS side) +
  `tauri-plugin-notification` (Rust side) — the only new dependency this
  milestone.
- On the `agent_done` event, when the window is unfocused/hidden
  (`document.hidden`): send a notification titled
  `Odo: run finished in <workstream>` with body = first 80 chars of the
  agent's last text. Request notification permission lazily on first
  qualifying event.
- The bridge from the JS notification API is covered by the existing
  `agent_done` detection in the poll loop — one small helper file
  (`notify.ts`) keeps it isolated.

#### 3c. Workstream badges (sidebar, derived state)

- Sidebar workstream rows gain two indicators:
  - green pulsing dot while that workstream's conversation has a live run
    (from the same run-state derivation as 3a, tracked per conversation);
  - red pill with the pending-diff count when > 0, derived from `diff` events
    whose status is pending (the same events the DiffViewer already
    consumes).
- All derived from already-streamed state; no new IPC. If implementation
  finds the per-workstream diff events insufficient on bootstrap, one small
  read-only IPC (`pending_counts`) is permitted as a fallback — decide at
  implementation, keep it boring either way.

#### 3d. `default_adapter` actually consumed (M2 F5 follow-up)

- Bug: the Settings panel persists `default_adapter` to `~/.odo/prefs.md`,
  but the daemon never reads it — settings writes are inert. Fix in
  `internal/adapter`: when a workstream's adapter field is empty, resolve
  the prefs `default_adapter` key; when that key is absent, fall back to
  `"omp"` (today's behavior). One helper (`resolveAdapter`) plus a unit
  test. After this, the Settings panel field does what it says.

## Architecture decisions for M3

| Decision | Value |
|---|---|
| Recall injection point | `buildPrompt` — single call site serving both `send_message` and `fanout_send` |
| Memory source | `wiki/<ws>-epoch-*.md` (M1 distill output, read from project root — not the worktree) |
| Global user memory | `~/.odo/user.md`, ≤4 KB, read at every prompt build (M3 reads; M4 adds the writer) |
| Injection order | user.md → wiki recall → attachments → message (stable prefix, prompt-cache friendly; ADR-0003) |
| Memory cap | 12 KB total for wiki notes, most-recent-epoch first, cut on note boundary; 4 KB for user.md, line boundary |
| Recall journaling | `recall: [paths]` added to `user_message` payload (`~/.odo/user.md` listed first when present) — no new event type, ADR-0002 preserved |
| Wiki browser transport | Two new read-only IPC commands (`list_wiki`, `read_wiki`); no journal writes |
| Path safety | `read_wiki` allows only `<project>/wiki/**` and the single global `~/.odo/user.md` |
| user.md in browser | Pinned synthetic first row in the WikiBrowser list; empty content renders a create-hint |
| Notifications | `@tauri-apps/plugin-notification` (first new dependency since M0; official Tauri plugin) |
| Status bar / badges | Derived from the existing event stream; no new IPC (one optional read-only `pending_counts` fallback) |
| `default_adapter` fix | Empty workstream adapter resolves prefs `default_adapter` → `"omp"` (M2 F5 follow-up) |
| Schema impact | None — payload-key extension only, same pattern as M1's `review_action` distill payload |
| Review weight | Fresh-context review — touches the agent-context path (`buildPrompt`) and extends a journal payload, per dev principle #4 |
| Polling | Unchanged — 1.5 s interval (M0 decision carries forward) |

## Verification

```bash
go build ./... && go vet ./... && go test ./... -count=1
cd gui && npx tsc --noEmit && npm run build
cd src-tauri && cargo check
```

New tests in `internal/ipc/server_test.go`:

- `TestRecallInjectsWikiNote`: distill a conversation (writes a wiki note with
  known content from the stub summary) → send a new message → poll until done
  → assert the diff content contains a snippet from the wiki note (proving
  `buildPrompt` injected it) → assert the `user_message` event payload has
  `recall: [<wiki path>]`.
- `TestUserMDInjected`: with `HOME` pointed at `t.TempDir()`, write
  `~/.odo/user.md` with a sentinel line → send a message → assert the prompt
  contains the sentinel and the `recall` payload lists `~/.odo/user.md`
  first. Without user.md, prompt and payload are unaffected (backward
  compatible).
- `TestUserMDCap`: user.md > 4 KB → injected block ≤ 4 KB, cut on a line
  boundary.
- `TestRecallEmptyWhenNoWiki`: send a message on a fresh workstream with no
  prior distill → assert the `user_message` payload has no `recall` field and
  the prompt is unchanged (backward compatible).
- `TestRecallCapsSize`: write 5 wiki notes totaling > 12 KB → send a message
  → assert the recalled memory block in the prompt is ≤ 12 KB and only the
  most recent notes are included (verify via the `recall` paths list length).
- `TestFanoutRecall`: `fanout_send` after a distill → assert every run's
  prompt contains the wiki note (proves the shared `buildPrompt` call site
  serves fan-out too).
- `TestListWiki`: after distill, `list_wiki` returns the note info with
  correct epoch, name, and path. Empty workstream returns an empty list.
- `TestReadWiki`: `read_wiki` returns the note content. A path outside
  `wiki/` (other than the exact `~/.odo/user.md`) is rejected with an error.
- `TestDefaultAdapterFallback` (adapter package): empty workstream adapter +
  prefs `default_adapter: pi` → resolves `"pi"`; without the prefs key →
  resolves `"omp"`.

Status bar / notification / badges are verified by the GUI test (Demo C
steps): status line visible during a run, badges in the sidebar, and a
notification path exercised best-effort (permission-dependent on first run).
