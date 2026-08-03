# M5 — Curation (topic pages + index.md + citations + pin affordance)

> **Rationale.** M4 closed the *learning* half of the memory loop:
> `buildPrompt` injects `user.md` (global) + `memory.md` (project rules) +
> recalled wiki notes, and the learner promotes epoch-observed rules into
> the always-injected layers. But the wiki notes themselves are per-
> workstream chronicles — a flat pile of `main-epoch-1.md`,
> `main-epoch-2.md`, … with no cross-workstream synthesis. A 20-epoch
> project has 20 notes; recall injects the newest ≤12 KB and drops the
> rest, so decisions from epoch 1 are invisible by epoch 10 even when they
> are still load-bearing. ADR-0003's "Curation" phase is: a curator pass
> that rewrites **topic pages** from the FULL set of epoch notes (never
> incrementally from the previous topic page — generation-2 rule, prevents
> confabulation drift), an always-injected `index.md` (≤2 KB table of
> contents), mandatory `(epoch-N)` citations per bullet (uncited bullets
> flagged in the browser), and a pin affordance ("remember: X" verbatim
> hoover). Wiki editing UI stays deferred — the curator owns topic pages;
> humans own `memory.md` and pins.

## Pain

- After 20+ distills across multiple workstreams I have a pile of epoch
  notes. Recall only injects the newest ≤12 KB, so load-bearing decisions
  from early epochs are silently dropped. There is no synthesized, cross-
  workstream view — the agent re-asks questions that epoch 1 settled
  because the note that answered them has aged out of the recall window.
  (pain #1)
- I can't tell *what the agent will remember* at a glance. The wiki
  browser shows individual epoch notes (chronological chronicles), not a
  topic-organized synthesis. To verify the project's accumulated
  knowledge I have to read every note. There is no index, no topic
  grouping, no way to see "what does this project know about
  authentication?" without scanning 20 files. (pain #1, #3)
- When the curator (or any future LLM) writes a topic page, I have no way
  to verify its claims trace to real epoch notes. A topic page bullet
  could be confabulated — the model's invention with no source — and I
  would inject it into every future prompt unchecked. There is no citation
  requirement, no uncited-bullet flagging, and no pin affordance for the
  verbatim user statements I want injected exactly as I wrote them.
  (ADR inv 2, 3)

## Demo

### Demo A: Curate rewrites topic pages from the full note set

1. A project has 3 workstreams, 15 total epoch notes (5 per workstream).
   The oldest note (epoch 1, workstream "main") records the decision
   "Authentication uses JWT with refresh tokens at `/auth/refresh`."
2. Click "Curate" in the sidebar Memory section. A busy state shows
   "Curating…". The curator pass (one orchestrator-model one-shot) reads
   the FULL set of epoch notes — all 15, across all workstreams — and
   rewrites `wiki/topics/*.md` from scratch (generation-2 rule: never
   from the previous topic page).
3. The result: `wiki/topics/authentication.md` with a bullet
   `- JWT auth with refresh at /auth/refresh (epoch-1)` — the `(epoch-N)`
   citation is mandatory. `wiki/index.md` (≤2 KB) is regenerated with a
   topic list: `## Topics\n- authentication → topics/authentication.md`.
4. A `memory_update` event fires; the sidebar shows a green "curated"
   chip. The wiki browser now has a "Topics" tab listing the topic pages.
5. Send a new message. The `user_message` bubble shows
   `memory: user.md + memory.md + index + 2 note(s)` — `index.md` is now
   always-injected. The agent's reply references the JWT decision from
   epoch 1 even though the recall window (newest ≤12 KB) would have
   dropped epoch-1's note.

### Demo B: Uncited bullets are flagged in the browser

1. After a curate pass, open the wiki browser and switch to the "Topics"
   tab. Click `topics/authentication.md`.
2. The reader view renders the topic page. Bullets with a valid
   `(epoch-N)` citation render normally. A bullet without a citation
   (e.g., the curator emitted a summary line with no source) is
   highlighted with a warning style and a "⚠ uncited" badge.
3. The user can see at a glance which bullets trace to a real epoch note
   and which are unverified. An uncited bullet is still injected (it may
   be useful), but it is flagged so the user can verify or remove it.
4. Clicking the `(epoch-N)` citation link navigates to the source epoch
   note in the browser (cross-reference: topic page → source note).

### Demo C: Pin affordance — "remember: X" verbatim hoover

1. In the sidebar Memory section, a "Pin" input (single-line text field
   with a "Pin" button) lets the user type a verbatim statement:
   "remember: Never deploy on Fridays."
2. Click Pin. The statement is stored verbatim in `.odo/pins.md` as
   `- Never deploy on Fridays.` — no LLM processing, no curation, no
   rewording. A `memory_update {layer:"pins", cause:"pin"}` event fires;
   the sidebar chip shows "pinned: Never deploy on Fridays."
3. Send a new message. The `user_message` bubble shows
   `memory: user.md + memory.md + pins + index + 2 note(s)`. The pin is
   injected as a separate `## Pins (user-authored, verbatim)` block in
   the prompt. The agent's reply respects the pin.
4. Pins are human-owned: the curator never touches them. The user can
   edit `.odo/pins.md` directly (it's human-owned, like `memory.md`).
   Removing a pin is a text edit in the file or in the Memory Review
   panel's reader tab.

## Not built

- **Wiki editing UI.** The curator owns topic pages; humans own
  `memory.md` and pins. An in-app editor for topic pages is deferred
  past M5 (ADR-0003: "Wiki editing UI stays deferred"). The user can
  always edit files on disk.
- **Auto-curate at distill.** The curator is a separate `curate` command
  triggered on demand, not auto-run at every distill. Rationale: the
  curator reads the FULL note set (expensive) and rewrites ALL topic
  pages; running it at every epoch boundary would dominate distill
  latency. A "curate after distill" checkbox is permitted but defaults
  off.
- **M6 scope:** pull-based recall CLI (`odo wiki read`), `ledger.md`
  with verified metrics — deferred.
- **Topic page deletion / archival.** Topic pages are regenerated from
  scratch on every curate pass; stale topics (whose source notes were
  all archived) simply disappear on the next curate. No explicit
  archival path.
- **Per-bullet accept/reject gates.** Topic pages are curator-owned
  derived artifacts (inv 2: rebuildable from the journal). Per-bullet
  review would be theater — the user can curate again or edit the file.
- **Attestation / sandboxing.**

## Architecture decision statements

| Topic | Decision |
|---|---|
| Curator cadence | On-demand `curate` IPC command, NOT integrated into distill (distill already runs distill + learner = 15 min; adding curator would extend blocking). A "Curate" button in the sidebar. Optional `curate: true` flag on `distill` for auto-curate-after-distill (defaults false) |
| Curator model | Orchestrator model (`distillAdapter`, same as distill + learner), `curatorTimeout = 10*time.Minute` — the curator reads all notes and rewrites all topic pages, so it needs the full distill-level budget |
| Generation-2 rule | The curator reads the FULL set of epoch notes (all workstreams, all epochs) and rewrites ALL topic pages from scratch. It NEVER reads the previous topic page. This prevents confabulation drift (each curate pass is generation-1 from source notes, not generation-N from prior summaries) |
| Note cap | `curatorNoteCap = 50` — the curator reads up to 50 most-recent epoch notes (newest-first across all workstreams). This bounds input cost as the project grows. When the project exceeds 50 notes, the oldest notes are excluded from curation (they remain in the journal; a future M6 pull-based CLI can retrieve them on demand). The cap is on INPUT notes, not on output topic pages |
| Topic page format | `wiki/topics/<slug>.md`, each capped at `topicFileCap = 8 KB` (whole-bullet cut). Each page: `# <Title>\n` + bullets. Each bullet: `- <statement> (epoch-N)` — the `(epoch-N)` citation is mandatory. Uncited bullets (no `(epoch-N)` suffix) are preserved but flagged in the browser. The daemon does NOT verify citations at write time (the curator is trusted to cite; the browser flags, the user decides) |
| index.md format | `wiki/index.md`, ≤2 KB, always-injected. Structure: `# Project Wiki Index\n\n## Topics\n- <topic title> → topics/<slug>.md\n…`. One line per topic. The daemon enforces the 2 KB cap at write time (line-boundary cut from the end — never drops a topic entirely, truncates the list). `index.md` is regenerated on every curate pass |
| Pin affordance | User action (not agent). A "Pin" input in the sidebar: user types text, clicks Pin. The text is stored verbatim in `.odo/pins.md` as `- <text>`. No LLM processing. A pin differs from a `memory.md` rule: a rule is a daemon-formatted behavior contract (`- <rule> — cites: <note>; reaffirmed: <epoch>`); a pin is a raw verbatim user statement with no metadata. Both are always-injected; pins are human-owned (the curator never touches them) |
| Pins file | `.odo/pins.md` (gitignored, same location as `memory.md`). ≤2 KB, line-boundary cut at read. Human-owned: the daemon writes it only via the `pin` IPC command (user-initiated); the curator never touches it. The daemon rejects accepted diffs that touch `.odo/pins.md` (same guard as `memory.md`) |
| Injection order | `user.md → memory.md → pins.md → index.md → wiki recall → attachments → message` (ADR inv 6 extended). Stable prefix: human-authored layers first (user.md, memory.md, pins), then derived index, then recalled notes, then churn. `index.md` is the bridge: a derived summary that names every topic, injected before the detailed notes |
| index.md injection | Always injected (like memory.md/user.md), ≤2 KB. Sits AFTER pins, BEFORE wiki recall. The receipt covers it (sha16 of the injected string). The recall payload lists `wiki/index.md` as a fixed marker (like `.odo/memory.md`) |
| Receipt extension | `memoryLayers` gains an `index` field. The receipt map adds `wiki/index.md → sha16([]byte(indexBlock))` when non-empty. The recall payload adds `wiki/index.md` after `.odo/pins.md` and before the note paths |
| IPC commands | `CmdCurate = "curate"` (on-demand, reads all notes, rewrites topics + index, journals `memory_update`); `CmdPin = "pin"` (stores a verbatim pin); `CmdReadPins = "read_pins"` (returns `.odo/pins.md` content for the review panel reader tab). `read_wiki` is extended to allow `wiki/topics/**` (currently only `wiki/<ws>-epoch-*.md` + `~/.odo/user.md`) |
| Frontend components | "Curate" button in sidebar Memory section; "Pin" input + button in sidebar; wiki browser "Topics" tab; uncited-bullet flagging in the topic page reader; `index.md` chip on the recall chip label |
| Tauri glue | Three new passthrough commands: `curate`, `pin`, `read_pins`. `DISTILL_READ_TIMEOUT` bumped from 960 s to **1200 s** (10 min distill + 5 min learner + 10 min curator + margin) — only when `curate: true` is set on distill; standalone `curate` uses a new `CURATE_READ_TIMEOUT = 660 s` (10 min curator + margin) |
| Topic page ownership | Curator owns `wiki/topics/*.md` + `wiki/index.md` (derived, rebuildable — inv 2). Humans own `.odo/memory.md` + `.odo/pins.md` + `~/.odo/user.md` (source-of-truth, never overwritten by the curator) |
| Schema impact | None — no new tables, no new event types. `memory_update` (M4) carries curator events with `layer: "curator"` / `layer: "index"` / `layer: "pins"`. The `review_action` event carries `action: "curate"` to mark a curate pass (like `action: "distill"`). Payload-key extension only (ADR-0002 preserved) |
| Review weight | Fresh-context dual-model review — touches `buildPrompt` (new `index` + `pins` params), `memoryLayers` (new fields + receipt), new IPC + frontend: per dev principle #4 |

## Backend

### 1. Curator pass (new file `internal/ipc/curator.go`)

The curator runs as a separate IPC command (`curate`), not integrated
into `handleDistill`. It reads the FULL set of epoch notes across all
workstreams, sends them to the orchestrator model in one one-shot, and
rewrites all topic pages + `index.md` from scratch.

```go
const (
    // curatorTimeout bounds the one-shot orchestrator curator run.
    // The curator reads all epoch notes and rewrites all topic pages, so
    // it needs the full distill-level budget (generation-2 rule: the
    // curator never reads previous topic pages, only source notes).
    curatorTimeout = 10 * time.Minute

    // curatorNoteCap bounds the number of epoch notes the curator reads.
    // Notes are newest-first across all workstreams. This bounds input
    // cost as the project grows; oldest notes beyond the cap remain in
    // the journal (M6 pull-based recall can retrieve them on demand).
    curatorNoteCap = 50

    // indexCap bounds wiki/index.md at 2 KB (ADR-0003: always-injected,
    // ≤2 KB). The cap is enforced at write time with a line-boundary cut
    // from the end (never drops a topic entirely, truncates the list).
    indexCap = 2 * 1024

    // pinsCap bounds .odo/pins.md at 2 KB at read time (line-boundary
    // cut). Pins are human-owned; the daemon writes them only via the
    // pin IPC command and never truncates at write time (refuse-on-
    // overflow, like user.md).
    pinsCap = 2 * 1024

    // topicFileCap bounds each wiki/topics/<slug>.md at 8 KB. Topic
    // pages are derived artifacts; a whole-bullet cut at the cap keeps
    // pages scannable without half-bullets.
    topicFileCap = 8 * 1024
)
```

**`allEpochNotes(projectRoot string) ([]epochNote, error)`** — reads
ALL `wiki/<ws>-epoch-*.md` files across ALL workstreams (not just the
active one), sorted newest-epoch-first across all workstreams. Capped at
`curatorNoteCap` notes. Returns `[]epochNote{name, workstream, epoch,
content}`.

```go
type epochNote struct {
    name       string // e.g. "main-epoch-3"
    workstream string
    epoch      int
    content    string
}
```

Implementation:
- Glob `filepath.Join(projectRoot, "wiki", "*-epoch-*.md")` (all
  workstreams, not just the active one — curation is project-wide).
- Parse the epoch from each filename via `wikiNoteEpoch`.
- Stat each file for modification time (tie-breaker for same-epoch
  notes across workstreams: newest mtime first).
- Sort by epoch descending; within the same epoch, by mtime descending.
- Read each file's content. Cap the total at `curatorNoteCap` notes.
- Return the slice.

**`curatorPrompt(notes []epochNote) string`** — renders the curator
one-shot prompt. Inputs in stable order: all epoch notes, newest-first,
each under a `=== <note-name> (workstream: <ws>, epoch: <N>) ===`
header. The instruction:

```go
func curatorPrompt(notes []epochNote) string {
    var b strings.Builder
    b.WriteString(`You are running odo's memory curator pass. Synthesize the epoch notes below into topic pages — one per topic area (e.g., "authentication", "build-system", "testing").

Output JSON ONLY (no prose, no markdown fence), exactly this shape:
{"topics":[{"title":"<Topic Title>","slug":"<topic-slug>","bullets":["- <statement> (epoch-N)","- <statement> (epoch-N)"]}]}

Rules:
- Each topic groups related decisions across workstreams and epochs.
- Every bullet MUST end with a "(epoch-N)" citation naming the source epoch note.
- A bullet without a citation is allowed but will be flagged in the UI as "uncited."
- The slug is lowercase, hyphenated, no spaces (e.g., "authentication").
- Do NOT copy previous topic pages — write from the source notes only (generation-1).
- 3-10 topics is typical; fewer for small projects.

=== EPOCH NOTES (newest-first) ===
`)
    for _, n := range notes {
        fmt.Fprintf(&b, "--- %s (workstream: %s, epoch: %d) ---\n%s\n\n",
            n.name, n.workstream, n.epoch, n.content)
    }
    return b.String()
}
```

**`curatorResult`** — the JSON contract:

```go
type curatorResult struct {
    Topics []struct {
        Title   string   `json:"title"`
        Slug    string   `json:"slug"`
        Bullets []string `json:"bullets"`
    } `json:"topics"`
}
```

**`parseCuratorOutput(raw string) (*curatorResult, error)`** — same
fence-tolerant JSON extraction as `parseLearnerOutput`.

**`writeTopicPages(projectRoot string, res *curatorResult) (topics []string, err error)`**:
1. Create `wiki/topics/` if absent.
2. Clear the directory: remove all `*.md` files in `wiki/topics/` (the
   curator rewrites from scratch — generation-2 rule). A failure to
   remove a single file is logged, not fatal (best-effort).
3. For each topic: write `wiki/topics/<slug>.md` with:
   ```
   # <Title>

   <bullet1>
   <bullet2>
   ```
   (one bullet per line, LF-joined, single trailing newline).
4. Return the list of written topic paths.

**`writeIndex(projectRoot string, res *curatorResult) (string, error)`**:
1. Build `index.md` content:
   ```
   # Project Wiki Index

   ## Topics
   - <title1> → topics/<slug1>.md
   - <title2> → topics/<slug2>.md
   ```
2. Enforce `indexCap` (2 KB) with a line-boundary cut from the end
   (truncates the topic list, never drops a topic file — the file still
   exists even if it falls off the index).
3. Write atomically to `wiki/index.md` (mode 0644).
4. Return the written content.

**`handleCurate(ctx context.Context, req Request) (Response, error)`**:
1. Resolve the project (reuse `resolveProject`).
2. Read `allEpochNotes(s.projectRoot)`. If zero notes, return an error
   ("no epoch notes to curate — distill first").
3. Run the curator one-shot via `runOneShot(ctx, ad, curatorPrompt(notes),
   curatorTimeout)` where `ad` is `s.distillAdapter` (falls back to
   `s.adapters[""]` like distill + learner).
4. Parse the output. On failure: journal
   `memory_update {layer:"curator", cause:"failed", detail:"…"}` and
   return the error (curate is a standalone command, not embedded in
   distill, so a failure returns an error to the caller — unlike the
   learner which degrades silently inside distill).
5. Write topic pages (each capped at `topicFileCap = 8 KB`, whole-bullet
   cut) + index.md.
6. Journal `review_action {action:"curate", topics: <count>,
   notes_read: <count>}`.
7. Journal `memory_update {layer:"index", cause:"curate", before_sha,
   after_sha, detail:"rewrote <N> topics + index"}`.
8. Return `Response{WikiPath: "wiki/index.md", MemoryProposals: 0}` (the
   `MemoryProposals` field is reused as a topic count for the sidebar
   badge — 0 means no learner proposals, as expected for curate).

### 2. Pin affordance (new file `internal/ipc/pins.go`)

```go
// readPins reads <projectRoot>/.odo/pins.md capped at pinsCap with a
// line-boundary cut (mirrors readUserMemory). "" when absent/empty.
// Pins are human-owned: the daemon writes them only via the pin IPC
// command and never truncates at write time.
func readPins(projectRoot string) string {
    b, err := os.ReadFile(filepath.Join(projectRoot, ".odo", "pins.md"))
    if err != nil {
        return ""
    }
    return capAtLineBoundary(string(b), pinsCap)
}
```

**`handlePin(ctx context.Context, req Request) (Response, error)`**:
1. `req.Text` is the verbatim pin statement. Empty text is an error.
2. Read `.odo/pins.md` in FULL (uncapped — the refuse-on-overflow check
   needs the truth, like user.md).
3. Append `- <text>\n` (if the file is non-empty, prepend `\n`).
4. Check: would the result exceed `pinsCap` (2 KB)? If so, **refuse**
   with an error naming the pin text (never truncate a user file —
   mirror `planUserApply`'s refuse-on-overflow).
5. Write atomically (0644).
6. Journal `memory_update {layer:"pins", cause:"pin", detail:"<text>"}`
   (the pin text is in the detail for the sidebar chip).
7. Return `Response{Applied: true}`.

**`handleReadPins(ctx context.Context, req Request) (Response, error)`**:
1. Resolve the project (same guard as `handleReadMemory`).
2. Return `.odo/pins.md` content as `MemoryContent` (reusing the
   existing field — the frontend reader tab uses the same component as
   `read_memory`).

### 3. buildPrompt extension + injection receipt

`buildPrompt` signature gains two new params:

```go
func buildPrompt(text string, attachments []string, userMem, projectMem, pins, index, memory string) string
```

Injection order (ADR inv 6 extended):
```
## User memory (durable cross-project principles)     ← userMem (existing)
## Project memory (behavior rules)                   ← projectMem (existing, M4)
## Pins (user-authored, verbatim)                     ← pins (new, M5)
## Wiki index (always-injected)                      ← index (new, M5)
## Prior notes (recalled)                            ← memory (existing, renamed M4)
Attached files: …                                     ← attachments (existing)
<text>                                                ← message (existing)
```

`memoryLayers` gains `pins` and `index` fields:

```go
type memoryLayers struct {
    user    string
    project string
    pins    string // .odo/pins.md (M5)
    index   string // wiki/index.md (M5)
    wiki    string
    recall  []string
    receipt map[string]string
}
```

`memoryLayers` method extension:
```go
ml.pins = readPins(s.projectRoot)
ml.index = readIndex(s.projectRoot)
// ... existing user/project/wiki reads ...
if ml.pins != "" {
    ml.receipt[".odo/pins.md"] = sha16([]byte(ml.pins))
    ml.recall = append(ml.recall, ".odo/pins.md")
}
if ml.index != "" {
    ml.receipt["wiki/index.md"] = sha16([]byte(ml.index))
    ml.recall = append(ml.recall, "wiki/index.md")
}
```

Recall payload order: `~/.odo/user.md → .odo/memory.md → .odo/pins.md → wiki/index.md → note paths`

The `recall` slice and `receipt` map follow the same M3/M4 convention:
keys are omitted entirely when empty (no empty arrays/objects).

### 4. readIndex helper

```go
// readIndex reads <projectRoot>/wiki/index.md capped at indexCap with a
// line-boundary cut. "" when absent/empty. M5: always-injected (ADR-0003).
func readIndex(projectRoot string) string {
    b, err := os.ReadFile(filepath.Join(projectRoot, "wiki", "index.md"))
    if err != nil {
        return ""
    }
    return capAtLineBoundary(string(b), indexCap)
}
```

### 5. read_wiki path extension

`handleReadWiki` currently allows two path classes: (a) inside
`<projectRoot>/wiki/` (but only `wiki/<ws>-epoch-*.md` via the glob in
`handleListWiki` — actually the read guard allows ANY file under
`wiki/`), and (b) exactly `~/.odo/user.md`. The read guard already
allows `wiki/topics/*.md` because the prefix check is
`strings.HasPrefix(rel, "wiki"+string(filepath.Separator))`. So **no
code change is needed** for reading topic pages — the existing guard
already covers `wiki/topics/**`. The `list_wiki` command, however, only
lists `wiki/<ws>-epoch-*.md` for the active workstream. A new
`list_topics` command or an extension to `list_wiki` is needed for the
browser's Topics tab.

**`handleListTopics(ctx context.Context, req Request) (Response, error)`**:
1. Resolve the project.
2. Glob `wiki/topics/*.md`. Parse the title (first `# ` line) from each.
3. Return `WikiNotes` (reusing the type — `Name` = slug, `Path` = full
   path, `Epoch` = 0 for topics, `ModifiedAt` = stat mtime).

### 6. IPC commands (protocol.go)

```go
CmdCurate       = "curate"        // on-demand: reads all notes, rewrites topics + index
CmdPin          = "pin"           // {text} → stores verbatim pin in .odo/pins.md
CmdReadPins     = "read_pins"    // {} → {memory_content: pins.md content}
CmdListTopics   = "list_topics"  // {} → {wiki_notes: [{name, path, modified_at}]}
```

New `Request` fields: none (uses existing `Text` for pin, `ProjectRoot`
for the others).

New `Response` fields: none (reuses `WikiNotes`, `WikiContent`,
`MemoryContent`, `Applied`, `WikiPath`, `MemoryProposals`).

### 7. memory_update event extensions

`memory_update` (M4) gains new `layer` and `cause` values:
- `layer: "curator"` / `cause: "failed"` — curator one-shot failure.
- `layer: "index"` / `cause: "curate"` — index.md rewritten.
- `layer: "pins"` / `cause: "pin"` — a pin was added.

The `review_action` event gains `action: "curate"` (like `action:
"distill"`).

No new event types, no new tables (ADR-0002 preserved).

## Frontend

### 8. Sidebar: Curate button + Pin input

`Sidebar.tsx` — extend the Memory section:
- Below the "Browse" button, add a "Curate" button (same style as
  "Distill"). Busy state: "Curating…". Calls `curate()` API. On
  success, refresh the wiki note count + topic count and show a toast
  "Curated N topics".
- Below the "Curate" button, add a "Pin" input: a single-line text
  field with a "Pin" button. On submit, calls `pin(text)` API. On
  success, shows a toast "Pinned: <text>". The input clears after
  submit.
- New props threaded from `App.tsx`: `onCurate`, `onPin`, `topicCount`
  (for a "N topics" line under the Curate button, like the wiki note
  count).

### 9. Wiki browser: Topics tab

`WikiBrowser.tsx` — add a tab switcher (Notes / Topics):
- "Notes" tab (existing): per-workstream epoch notes, newest-first.
- "Topics" tab (new): lists `wiki/topics/*.md` via `list_topics`. Each
  row shows the topic title (parsed from the `# ` line) + modified time.
  Clicking a topic opens its content in the reader view.
- The pinned `user.md (global)` row stays in the Notes tab.
- Uncited bullets: in the reader view, a bullet line without an
  `(epoch-N)` suffix is rendered with a warning style + "⚠ uncited"
  badge. The detection is client-side: regex `\(epoch-\d+\)$` on each
  bullet line; if no match, flag it.
- Citation links: an `(epoch-N)` suffix in a topic page bullet is
  rendered as a clickable link that switches to the Notes tab and
  selects the source epoch note (if it exists in the current
  workstream's note list).

### 10. Recall chip + receipt extension

`MessageBubble.tsx` — extend `recallChipLabel`:
- New fixed markers: `.odo/pins.md` → `pins`, `wiki/index.md` → `index`.
- Label shapes:
  - `memory: user.md + memory.md + pins + index + 2 note(s)` (all layers)
  - `memory: memory.md + index + 1 note(s)` (no user.md, no pins)
  - `memory: index + 3 note(s)` (only index + notes)
- `shortRecallPath`: `.odo/pins.md` → `pins`, `wiki/index.md` → `index`.

### 11. App.tsx state + handlers

- New state: `topicCount: number | null` (like `wikiNoteCount`).
- New handlers: `handleCurate` (calls `curate()`, refreshes counts), 
  `handlePin` (calls `pin(text)`, shows chip).
- `refreshWikiCount` is extended to also call `listTopics` and set
  `topicCount`.
- `recordEvents`: `memory_update` with `layer: "pins"` or `layer:
  "index"` sets the chip state (same as M4's `lastMemoryUpdate`).

### 12. Tauri glue

`gui/src-tauri/src/lib.rs`:
- New `CURATE_READ_TIMEOUT: Duration = Duration::from_secs(660)` (10 min
  curator + margin).
- New passthrough commands: `curate`, `pin`, `read_pins`,
  `list_topics`. Same shape as existing `distill` / `read_memory`.
- `curate` uses `CURATE_READ_TIMEOUT`; `pin` / `read_pins` /
  `list_topics` use `READ_TIMEOUT`.
- If auto-curate-after-distill is supported (`curate: true` on distill),
  `DISTILL_READ_TIMEOUT` is bumped to **1200 s** (10 + 5 + 10 + margin).
  Register all four in `invoke_handler`.

## Verification

```bash
go build ./... && go vet ./... && go test ./... -count=1
cd gui && npx tsc --noEmit && npm run build
cd src-tauri && cargo check
```

## New Go tests (internal/ipc)

1. `TestCurateRewritesTopicPages` — seed 3 epoch notes across 2
   workstreams with a stub curator output (JSON with 2 topics, each with
   cited bullets) → `curate` → assert `wiki/topics/*.md` written with
   correct content + `(epoch-N)` citations → assert `wiki/index.md`
   ≤2 KB with topic list → assert `review_action {action:"curate"}` +
   `memory_update {layer:"index"}` journaled.
2. `TestCurateGeneration2Rule` — pre-existing `wiki/topics/old.md` with
   a fabricated bullet → `curate` → assert `old.md` is REMOVED (the
   curator clears + rewrites) and the new topics do NOT contain the
   fabricated bullet (proves the curator wrote from notes, not from the
   old page).
3. `TestCurateNoteCap` — seed 60 epoch notes → `curate` → assert the
   curator prompt file contains exactly 50 notes (newest-first) —
   verify via the prompt file content, not the model output.
4. `TestCurateEmptyProjectErrors` — `curate` on a project with zero
   epoch notes → error "no epoch notes to curate".
5. `TestCurateFailureJournalsAndErrors` — stub curator returns non-JSON
   → `memory_update {layer:"curator", cause:"failed"}` journaled +
   command returns an error (unlike learner, curate is standalone, so
   failure surfaces to the caller).
6. `TestIndexInjectedIntoPrompt` — write `wiki/index.md` with a sentinel
   → send_message → assert prompt contains `## Wiki index` header +
   sentinel → assert recall payload includes `wiki/index.md` → assert
   receipt maps `wiki/index.md` to `sha16([]byte(indexContent))`.
7. `TestPinsInjectedIntoPrompt` — write `.odo/pins.md` with a sentinel
   → send_message → assert prompt contains `## Pins` header + sentinel
   → assert recall includes `.odo/pins.md` → assert receipt maps it.
8. `TestPinCommand` — `pin` with text "Never deploy on Fridays." →
   `.odo/pins.md` contains `- Never deploy on Fridays.\n` →
   `memory_update {layer:"pins", cause:"pin"}` journaled → second pin
   appends on a new line → overflow (>2 KB) is refused (error naming
   the pin, nothing written).
9. `TestInjectionReceiptWithIndexAndPins` — frozen sha16 vectors for
   known index.md + pins.md block; absent layers have no receipt entry;
   the recall payload order is `user.md → memory.md → pins.md →
   index.md → note paths`.
10. `TestListTopics` — after curate, `list_topics` returns topic info
    with correct title/slug/path. Empty project returns an empty list.
11. `TestReadWikiTopicsPath` — `read_wiki` with a `wiki/topics/*.md`
    path returns the content (the existing guard already allows it, but
    this test pins the contract).
12. `TestUncitedBulletDetection` — (frontend-level, if feasible via the
    GUI test harness) a topic page with a cited + an uncited bullet →
    the uncited bullet is flagged in the reader view.

GUI E2E (cua-host AX tree, M3/M4 pattern): Demo A/B/C as described;
assert "Curated" toast, Topics tab, uncited badge, pin chip, recall chip
label with `index` and `pins`.

## Review

Changes on the agent-prompt path (`buildPrompt` signature + 2 new
params), `memoryLayers` (2 new fields + receipt), new IPC (4 commands)
+ frontend (Curate/Pin UI, Topics tab, uncited flagging): fresh-context
dual-model review (GLM-5.2 + K3 audit) before close (per dev principle
#4).

## Risks and rejected alternatives

### Risks

1. **Curator latency.** The curator reads up to 50 epoch notes and runs a
   10-minute one-shot. On a large project this is a long blocking call
   (the daemon serves one connection at a time). **Mitigation:** the
   curator is on-demand (not auto-run at every distill); the user clicks
   "Curate" when they want a fresh synthesis. The UI shows a busy state
   and the poll loop is paused (like distill).

2. **Confabulation despite the generation-2 rule.** The curator could
   still fabricate a bullet that has an `(epoch-N)` citation but whose
   content is not actually in that epoch note. The daemon does NOT verify
   citations at write time (the citation is a string, not a verified
   pointer). **Mitigation:** the browser flags uncited bullets (no
   `(epoch-N)` suffix); the user can click a citation to navigate to
   the source note and verify. Full citation verification (substring
   check, like M6's ledger rule) is deferred to M6 where the
   `odo wiki read` CLI enables pull-based verification.

3. **Topic page churn.** Every curate pass rewrites ALL topic pages from
   scratch. Topic slugs may change between passes (the model may name a
   topic "auth" one time and "authentication" the next), breaking
   citation links. **Mitigation:** the curator prompt instructs the
   model to use stable slugs (lowercase, hyphenated). The `index.md` is
   regenerated each pass, so the topic list is always current. Broken
   citation links (an `(epoch-N)` pointing to a note that was aged out
   of the recall window) degrade gracefully — the browser shows the
   topic page but the link target is absent.

4. **Pin overflow.** A user who pins many statements could fill `.odo/
   pins.md` past 2 KB. The refuse-on-overflow guard prevents silent
   truncation but leaves the batch (the latest pin) un-stored. 
   **Mitigation:** the error names the offending pin text; the user can
   remove older pins from the file and retry. Pins are few by nature
   (they are durable verbatim statements, not ephemeral notes).

### Rejected alternatives

1. **Integrate the curator into distill (auto-curate at every epoch
   boundary).** Rejected: the curator reads up to 50 notes and runs a
   10-minute one-shot. Adding it to every distill would make distill a
   25-minute blocking call (10 distill + 5 learner + 10 curator). The
   user distills frequently (every 50+ messages) but only needs curation
   occasionally (when the topic landscape has drifted). An on-demand
   `curate` command is the right cadence. An optional `curate: true`
   flag on distill is permitted for users who want auto-curation, but
   defaults off.

2. **Verify citations at write time (daemon-side substring check).**
   Rejected for M5: the citation is a string `(epoch-N)`, not a
   structured pointer. Verifying that the bullet's content appears in
   the named epoch note (normalized substring check, like the learner's
   evidence veto) would catch fabrications, but it would also reject
   legitimate syntheses (the curator may combine information from two
   notes into one bullet, citing only one). Citation verification is
   deferred to M6, where the `odo wiki read` CLI enables pull-based
   verification and the user can audit specific claims.

3. **Store pins in `~/.odo/user.md` (merge pins with user memory).**
   Rejected: pins are verbatim user statements with no metadata; user.md
   rules are daemon-formatted (`- <rule> — seen: <p1>, <p2>`). Mixing
   them would require the parser to distinguish two line formats in one
   file, and a pin's verbatim text would be indistinguishable from a
   hand-edited user.md line. Separate files (`pins.md` vs `user.md`)
   keep the ownership boundary clean: pins are project-scoped, user.md
   is global.

4. **Embeddings / vector store for topic selection.** Rejected
   (ADR-0003 rejected/deferred): at this scale (≤50 notes), a single
   one-shot that reads all notes is cheaper and more inspectable than an
   embedding pipeline. The curator reads all notes and decides topics
   in one pass — no retrieval step needed. Revisit only if the note cap
   proves insufficient at scale.

5. **Per-bullet accept/reject gate for topic pages.** Rejected (ADR-0003
   rejects per-entry gates as "theater"): topic pages are derived
   artifacts (inv 2: rebuildable from the journal). A batch-accepted
   queue of topic bullets is worse than no gate — the user would
   rubber-stamp them. The user can re-curate (rewrite from scratch) or
   edit the file directly. The browser's uncited-bullet flagging gives
   the user a review surface without a gate.

6. **Incremental topic page updates (edit the previous topic page
   instead of rewriting).** Rejected (ADR-0003 generation-2 rule):
   incremental updates from the previous topic page would compound
   confabulation drift. Each curate pass is generation-1 from source
   notes. The cost is a full rewrite each pass; the benefit is that
   topic pages never accumulate model-generated errors across passes.
