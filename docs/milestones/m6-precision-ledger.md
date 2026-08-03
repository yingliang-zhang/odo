# M6 — Precision + Ledger (keyword recall + matched_terms + pull CLI + ledger.md + contradiction reports)

> **Rationale.** M5 closed the curation half: the curator rewrites topic pages
> from the full note set, `index.md` is always-injected, and citations are
> flagged but not verified. Two gaps remain. (1) Recall is still blind — it
> injects the newest ≤12 KB of epoch notes regardless of what the user
> actually asked, so a message about "authentication" gets the same notes as
> one about "testing." (2) The user cannot trust derived content: topic-page
> citations are unverified strings, and there is no auditable record of what
> the daemon measured. ADR-0003's "Precision + Ledger" phase closes both:
> keyword/topic-selected recall with a `matched_terms` payload that answers
> "why was this recalled?", a pull-based recall CLI (`odo wiki read <page>`)
> because coding agents already have shells — no MCP plumbing, a daemon-only
> `ledger.md` whose every metric line is substring-verified against a
> journaled payload (inv 4: no LLM in the metric data path), and distiller
> contradiction reports that retract the stale note with a record (inv 3).
> No new tables, no new event types — payload-key extensions only
> (ADR-0002 preserved).

## Pain

- I ask the agent about "authentication" and it re-asks a question epoch 1
  settled — because recall injects the newest ≤12 KB of notes regardless of
  topic. A 20-epoch project with notes about auth, build, and testing injects
  the 3 newest notes even when only one is relevant. The other 17 notes —
  including the load-bearing auth decision — are invisible unless they
  happen to be recent. I have no way to steer recall toward the topic I am
  actually working on, and no signal for *why* a given note was injected.
  (pain #1, ADR inv 6)
- I cannot trust the numbers the system reports. The ADR promises
  "verified-from-logs" metrics, but there is no ledger — no daemon-written
  record of run durations, token counts, or accept/reject rates — and even
  if there were, an LLM that selects metric lines could fabricate a number
  that never appeared in any log. Without mechanical substring verification
  (`quote ∈ referenced payload`) and without a browsable file, every metric
  is aspirational, not verified. (ADR inv 4)
- When a new epoch note contradicts an older one ("we switched from JWT to
  session cookies"), the old note is still injected — silently. The distiller
  has no contradiction detection, and the ADR's "retract with a record" (inv 3)
  has no implementation. The agent gets two incompatible facts and has no way
  to know which is current, and I have no record that the retraction happened.
  (ADR inv 3)

## Demo

### Demo A: Keyword recall — the right notes for the right question

1. A project has 15 epoch notes across 3 workstreams. Epoch 1 (workstream
   "main") records "Authentication uses JWT with refresh tokens." Epoch 14
   records "Switched build system to Boring." The user sends:
   "How does auth work in this project?"
2. The daemon tokenizes the message into terms `["auth", "authentication",
   "project", "work"]` (after stop-word removal). It matches these against
   every epoch note's content + filename. Epoch 1 matches `auth`/`authentication`
   (in its body); epoch 14 does not match any term. The ranking combines
   match-count with newest-first epoch order: matched notes rank above
   unmatched notes, and within each tier, newest-first.
3. The prompt is built with the keyword-selected notes (still capped at 12
   KB, still note-boundary cut). The `user_message` bubble's recall chip now
   reads `memory: index + 2 note(s) (keyword: auth)` — the `(keyword: …)`
   suffix appears when keyword selection changed the recall set. The tooltip
   lists each note with its matched terms: `main-epoch-1 [auth,
   authentication]`.
4. The agent's reply references the JWT decision from epoch 1 — even though
   epoch 1 is the oldest note and would have been dropped by pure
   newest-first recall. The `receipt` still covers every injected block;
   the `recall` payload now carries `{path, matched_terms}` per note (M6
   extension).

### Demo B: odo wiki read — pull-based recall for agents

1. An agent (running in a worktree with shell access) needs the full text of
   `wiki/main-epoch-1.md`, which aged out of the recall window. It runs:
   `odo wiki read main-epoch-1`
2. The CLI resolves the page name, guards the path (only `wiki/` and
   `wiki/topics/` are readable — same guard as `read_wiki` IPC), and prints
   the file's content to stdout. No daemon process needed — the CLI reads
   the file directly from the project root (it is a read-only operation on
   derived artifacts; the journal is the source of truth, not the daemon
   process).
3. The agent can also read topic pages: `odo wiki read topics/authentication`
   resolves to `wiki/topics/authentication.md`. A bare `odo wiki read index`
   reads `wiki/index.md`. An unknown page prints an error to stderr and exits
   1; a path-traversal attempt (`odo wiki read ../../etc/passwd`) is rejected
   with the same guard.
4. The agent's shell access is the transport — no MCP server, no socket
   protocol. The CLI is a subcommand of the existing `odo` binary (a new
   `wiki` subcommand mode; the default mode with no subcommand is still the
   daemon).

### Demo C: ledger.md — verified metrics, browsable and greppable

1. After distilling epoch 5 and accepting 2 of 3 proposed memory rules, the
   user opens `.odo/ledger.md`. It contains daemon-written rows like:
   ```
   ## epoch 5 — 2026-08-15T14:32:01Z
   - distill duration: 187s (review_action seq 42)
   - proposals: 3 (memory_propose seq 43)
   - accepted: 2, rejected: 1 (memory_apply seq 45)
   - recall notes: 4 (user_message seq 41)
   ```
2. Every number is verifiable: the daemon wrote each row by reading the
   cited journal event's payload and extracting the metric verbatim — e.g.,
   the `187s` duration is computed from the `review_action{action:"distill"}`
   event's `created_at` minus the preceding `user_message` event's
   `created_at` (both journaled timestamps), and the `4` recall count is the
   length of the `recall` array in that `user_message`'s payload. No LLM
   touched the metric data path (inv 4). A future LLM may *select* which rows
   to surface, but every quote is substring-verified against the referenced
   payload — a fabricated number is rejected with a `memory_update{layer:
   "ledger", cause: "verify_failed"}` event.
3. The ledger is never injected into agent prompts (ADR-0003: "never
   injected, pulled"). The user reads it directly; an agent that needs a
   metric pulls it via `odo wiki read ledger` (the ledger lives at
   `.odo/ledger.md` but the CLI maps the friendly name `ledger`).

### Demo D: Contradiction report — retract with a record

1. Epoch 6's distill note says "Auth switched from JWT to session cookies."
   The distiller's contradiction pass (runs inside distill, after the note is
   written) compares the new note against recalled older notes. It finds
   that epoch 1's note says "Authentication uses JWT" — a contradiction.
2. The daemon does NOT modify epoch 1's note file (epoch notes are
   append-only records — inv 2: rebuildable from the journal). Instead it
   journals a `memory_update{layer:"note", cause:"retract", detail:"epoch-1
   contradicted by epoch-6: JWT → session cookies", before_sha, after_sha}`
   event. The `before_sha`/`after_sha` are both the sha16 of epoch 1's
   unchanged content (the file is not modified — the retraction is a journal
   record, not a file mutation). The recall function skips retracted notes
   (it reads the journal's retraction events and excludes retracted
   `<ws>-epoch-<N>` pairs from the recall set).
3. The sidebar shows a chip: "⚠ epoch-1 retracted (contradicts epoch-6)."
   The wiki browser flags epoch 1's note row with a "⚠ retracted" badge.
   The user can still read epoch 1 (it is a record), but it is no longer
   injected. The retraction is reversible: a `memory_update{cause:
   "unretract"}` event (from a future command) would restore it; for M6 the
   retraction stands until the next distill's contradiction pass clears it.

## Not built

- **Embeddings / vector store.** Keyword matching (tokenized substring) is
  the M6 precision mechanism. Embeddings are rejected/deferred per ADR-0003
  ("violates inspectability, near-zero win at this scale; revisit only if
  composed index+pull demonstrably fails at scale"). The keyword matcher is
  inspectable (the matched terms are in the payload), deterministic, and
  adds zero dependencies.
- **Semantic similarity / fuzzy matching.** Keyword recall matches exact
  tokens (case-folded, stop-worded). "auth" does not match "authentication"
  via stemming — but the user's message typically contains both forms, and
  the daemon matches against note content + filename, so coverage is wide.
  Fuzzy matching is deferred (inspectability cost, scale not justified).
- **Auto-injection of ledger.md.** The ledger is pull-only (ADR-0003: "never
  injected, pulled"). An agent that wants a metric runs `odo wiki read
  ledger`. Injecting it would violate inv 4's spirit (metrics should not
  steer behavior unless explicitly pulled and verified).
- **MCP server / tool protocol.** The CLI is the transport. ADR-0003's
  divergence table resolved this: "K3's path: M5 index → M6 pull via plain
  CLI (coding agents already have shells)." An MCP server adds plumbing for
  no win — the agent already has a shell.
- **Ledger as SQL view.** GLM-5.2 proposed a SQL view over the journal; the
  ADR resolved for the file form ("browsable/greppable, but daemon-only
  writer + substring verification keeps GLM's un-editable-by-LLM
  property"). The file is `.odo/ledger.md`.
- **LLM-authored ledger rows without verification.** Rejected (inv 4). The
  daemon may accept an LLM's *selected* quote + source pointer, but it
  verifies `quote ∈ referenced payload` before writing the row. A failed
  verification is a journaled error, never a silent write.
- **Retraction file mutation.** Epoch notes are never edited (inv 2:
  rebuildable from the journal). Retraction is a journal event that recall
  filters on — the file on disk is the original record forever.
- **Cross-project contradiction.** M6 detects contradictions within a
  project's note set (new note vs. recalled older notes in the same project).
  Cross-project contradiction (a rule in project A contradicts a rule in
  project B's memory.md) is deferred — the learner's recurrence gate already
  surfaces cross-project overlap, and contradiction across projects is a
  weaker signal than within-project.

## Architecture decision statements

| Topic | Decision |
|---|---|
| Keyword recall: matching mechanism | **Tokenized substring** against note content + filename. The user's message text is tokenized: lowercased, split on non-alphanumeric runs, stop-worded (a built-in list of ~50 common English words), and each surviving token is checked as a case-insensitive substring against (a) the note's filename (`<ws>-epoch-<N>`) and (b) the note's full content. A note matches if ≥1 token appears in either. This is NOT regex, NOT stemming, NOT fuzzy — it is deterministic, inspectable, and dependency-free. `matched_terms` is the list of tokens that matched (de-duplicated, order-stable) |
| Keyword recall: what is matched | The note's **content + filename**. NOT the note title (epoch notes have no title field), NOT topic pages (topic pages are always-injected via index.md — keyword recall applies to the epoch-note recall layer only). The user's message text is the query. There is no separate "topic" field on notes — the note's content IS the topic signal |
| Keyword recall: interaction with newest-first | **Augment, not replace.** The existing newest-first epoch sort becomes the *tie-breaker* within tiers. The two tiers are: (1) notes with ≥1 keyword match, (2) notes with zero matches. Within each tier, newest-epoch-first. The 12 KB cap with note-boundary cut is unchanged — it now applies to the tiered list, so matched notes are injected first (up to the cap), and unmatched notes fill the remaining budget (if any). A message with no keyword matches (all stop-words, or no token appears in any note) falls back to pure newest-first — the current behavior, unchanged |
| Keyword recall: ranking formula | Two-tier: `matched (descending match-count, then newest-epoch) → unmatched (newest-epoch)`. Match-count = number of distinct query tokens found in the note. A note matching 3 terms ranks above a note matching 1 term, which ranks above any unmatched note. Same match-count → newest-epoch-first (existing sort). This is a simple, inspectable formula — no BM25, no TF-IDF, no embeddings |
| Keyword recall: how many notes injected | Still ≤12 KB total (recallMemoryCap), still note-boundary cut. The cap bounds the TOTAL recalled block — matched notes first, then unmatched notes to fill. Typically a keyword query injects 2-5 notes (matched) + a few unmatched (budget permitting). No separate per-tier cap — the single 12 KB cap is the only bound |
| matched_terms payload | **Per-note, in the recall payload.** The `recall` field on `user_message` changes from `[]string` (paths) to `[]object` with shape `{"path": "<path>", "matched_terms": ["<term>", ...]}`. For non-keyword layers (user.md, memory.md, pins.md, index.md) `matched_terms` is omitted (they are always-injected, not keyword-selected). The receipt map is unchanged (still keyed by path, still sha16 of the injected block). The `recall` slice on `memoryLayers` becomes `[]recallItem{path, matchedTerms}`; the journal payload serializes the new shape. The recall chip surfaces `(keyword: <terms>)` when any note has a non-empty `matched_terms` |
| odo wiki read CLI surface | **Subcommand of the existing `odo` binary.** `main.go` gains subcommand dispatch: `odo wiki read <page>` reads and prints to stdout; `odo` with no subcommand (or `odo -project … -socket …`) runs the daemon (current behavior). `wiki read` resolves `<page>` to a path under `<cwd>/wiki/`: bare names (`main-epoch-1`) → `wiki/main-epoch-1.md`; `topics/<slug>` → `wiki/topics/<slug>.md`; `index` → `wiki/index.md`; `ledger` → `.odo/ledger.md`. Path guard: the resolved path must be under `<cwd>/wiki/` or be `<cwd>/.odo/ledger.md` (same prefix check as `read_wiki`'s guard). Traversal (`../`) is rejected. No daemon access — reads files directly. Exits 0 on success, 1 on missing/guard failure |
| odo wiki read: daemon access | **None.** The CLI reads files directly from the project root (cwd). It does not connect to the daemon socket. Rationale: wiki notes, topic pages, index.md, and ledger.md are all plain files on disk; the daemon process is not needed for a read-only operation. The journal is the source of truth for *events*, but the wiki/ledger files are the derived artifacts the CLI serves. This keeps the CLI zero-dependency and usable in any worktree (the daemon is bound to one root; an agent's worktree is a different path) |
| odo wiki read: path guarding | **Prefix check on the resolved path.** `filepath.Rel(cwd, resolved)` must not start with `..` and must be under `wiki/` or be `.odo/ledger.md`. The `topics/` prefix is allowed (resolves to `wiki/topics/`). The `ledger` friendly name resolves to `.odo/ledger.md` (outside `wiki/` but inside `.odo/`). Same guard as `handleReadWiki`'s class-1 check, extended for the `.odo/ledger.md` exception. A missing file is an error (exit 1), not empty stdout — so a shell `test -n "$(odo wiki read …)"` check is reliable |
| ledger.md format | **Daemon-written Markdown, one section per epoch.** Path: `.odo/ledger.md` (gitignored, same dir as memory.md). Format: a `## epoch <N> — <RFC3339 timestamp>` header followed by `- <metric>: <value> (<event type> seq <S>)` bullets. Each bullet cites the journal event the metric was computed from (the `seq` is the event's seq in its conversation). The file is append-only across epochs (the daemon appends a new `## epoch N` section at each distill; it never rewrites old sections). No cap (the ledger grows with the project; the user can truncate old sections manually — it is a record, not an injected layer) |
| ledger.md: metrics | **Distill duration, proposal counts, accept/reject counts, recall note count.** All computed from journaled events: (1) distill duration = `review_action{action:"distill"}`.created_at − preceding `user_message`.created_at; (2) proposals = count of `memory_propose`'s `proposals` array; (3) accepted/rejected = `memory_apply`'s `metrics.accepted`/`metrics.rejected`; (4) recall notes = length of the `user_message`'s `recall` array (pre-M6: path count; M6: item count). These are all daemon-computed from journal payloads — no LLM in the data path (inv 4) |
| ledger.md: when rows are written | **At distill and at apply_memory.** The daemon appends a `## epoch N` section to `.odo/ledger.md` at the end of `handleDistill` (after the learner pass, before the epoch increment) and appends accept/reject metrics at the end of `handleApplyMemory`. A distill section is written even when the learner proposes nothing (the duration + recall count are still metrics). The append is best-effort: a write failure journals a `memory_update{layer:"ledger", cause:"write_failed"}` event but does not fail the distill (the ledger is a record, not a gate) |
| ledger.md: substring verification | **Daemon-side `quote ∈ referenced payload` check.** When an LLM (future: a `ledger_select` command) proposes a metric row, the daemon verifies the quote is a verbatim substring of the referenced event's payload (normalized: trim + collapse whitespace, case-sensitive on the payload's JSON string value). The verification function is `verifyLedgerQuote(quote string, event store.Event) bool`: it unmarshals the event's payload, stringifies it, and checks `strings.Contains(haystack, quote)`. A failed verification journals `memory_update{layer:"ledger", cause:"verify_failed", detail:"<quote>"}` and rejects the row. M6 ships the verification function + the daemon-computed rows (no LLM selection yet — the LLM selection path is a future extension that the verification function already supports) |
| Contradiction detection: when it runs | **Inside distill, after the note is written, before the learner.** The contradiction pass runs as a daemon-side text comparison (no LLM — inv 4 discipline extended to contradiction detection). It compares the just-written note's content against the recalled older notes (the same set `recallWikiNotes` would inject) using the same normalize-and-substring approach the learner's evidence veto uses. Running it inside distill means it adds latency to the already-blocking distill call — but it is a string comparison, not an LLM one-shot, so it adds milliseconds, not minutes |
| Contradiction detection: mechanism | **Daemon-side normalized substring heuristic.** The new note is split into sentences. Each sentence is normalized (`normalizeRule`: trim, lower, collapse whitespace) and compared against each older note's normalized content. A contradiction is flagged when a new-note sentence and an old-note sentence share ≥3 normalized words AND contain a negation/contrast signal (`not`, `no longer`, `switched`, `replaced`, `removed`, `instead of`, `changed from`) in the new-note sentence. This is a conservative heuristic — it errs toward NOT flagging (false negatives are safe; false positives would retract a valid note). The detected pairs are journaled as contradiction reports |
| Contradiction detection: surfacing | **Journal event + UI chip + recall exclusion.** A detected contradiction journals `memory_update{layer:"note", cause:"retract", detail:"<old>-epoch-<N> contradicted by <new>-epoch-<M>: <snippet>", before_sha, after_sha}` (before_sha = after_sha = the old note's sha16 — the file is not modified). The recall function reads retraction events and excludes retracted `<ws>-epoch-<N>` pairs. The sidebar shows a chip ("⚠ epoch-N retracted (contradicts epoch-M)"); the wiki browser flags the retracted note's row with a "⚠ retracted" badge. The contradiction report is NOT a memory_update to memory.md (it is a note-layer retraction, not a rule-layer retraction — the learner's `contradicts` field handles rule-layer retraction at apply time) |
| Contradiction: old note handling | **Retract with a journal record, no file mutation (inv 3).** The old note file is never edited — it is an append-only record (inv 2: rebuildable from the journal). "Retract with a record" means: (1) a `memory_update{cause:"retract"}` event is journaled naming the old note; (2) the recall function excludes the retracted note from the recall set; (3) the browser flags the note as retracted. The note is still readable (`odo wiki read`, `read_wiki` IPC) — it is a record. A retraction is reversible via a future `unretract` command (not in M6); for M6 the retraction persists until the next distill's contradiction pass re-evaluates |
| IPC commands | `CmdLedger = "ledger"` (read `.odo/ledger.md` content for the UI — same shape as `read_pins`); `CmdContradictions = "contradictions"` (return the conversation's retraction events for the browser's retraction badges). No new commands for keyword recall (it is internal to `recallWikiNotes`) or the CLI (it is a `main.go` subcommand, not an IPC command). `send_message`/`fanout_send` payloads are extended (matched_terms) |
| Frontend surfaces | Recall chip: `(keyword: …)` suffix + tooltip with per-note matched terms; ledger viewer (a new tab in the Memory Review panel or a new panel — reads via `ledger` IPC); retraction chip in sidebar ("⚠ epoch-N retracted"); retraction badge in wiki browser note list. The recall chip tooltip renders per-note matched terms |
| Tauri glue | Two new passthrough commands: `ledger`, `contradictions`. Both use `READ_TIMEOUT` (read-only). No timeout bumps (keyword recall + contradiction detection add milliseconds, not minutes; the CLI is not a Tauri command — it is a shell binary the agent runs) |
| Schema impact | **None — no new tables, no new event types.** The `recall` field on `user_message` changes shape from `[]string` to `[]object` (payload-key extension, ADR-0002 preserved — the `payload_json` column is a TEXT blob). Retraction events reuse `memory_update` with `layer:"note"`, `cause:"retract"`. Ledger write failures reuse `memory_update` with `layer:"ledger"`. The `review_action` event's existing `action:"distill"` carries the new contradiction count as a payload key. No migrations, no schema version bump |
| Review weight | Fresh-context dual-model review — touches `recallWikiNotes` (keyword tiering + retraction filter), `memoryLayers` (recall payload shape), `buildPrompt` (unchanged signature — the recall block is already a string), `handleDistill` (contradiction pass + ledger append), `handleApplyMemory` (ledger append), `main.go` (subcommand dispatch), new IPC (2 commands) + frontend (ledger viewer, retraction chip/badge): per dev principle #4 |

## Backend

### 1. Keyword recall (extend `internal/ipc/recall.go`)

The existing `recallWikiNotes` gains a `query string` parameter (the user's
message text) and returns matched terms per note. The 12 KB cap and
note-boundary cut are unchanged; only the *order* of notes changes (tiered).

```go
// recallItem is one recalled note with the query terms that matched it.
// matchedTerms is empty for notes included purely by newest-first fallback
// (the unmatched tier). The journal's user_message recall payload serializes
// this as {"path":"…","matched_terms":[…]}.
type recallItem struct {
    path         string
    matchedTerms []string
}
```

**`tokenizeQuery(text string) []string`** — lowercases, splits on
`[^a-z0-9]+`, filters through a stop-word set, de-duplicates preserving
order.

```go
// stopWords are filtered from the query — they appear in nearly every note
// and carry no topical signal. The list is small and fixed (no i18n, no
// config) to keep recall deterministic and dependency-free.
var stopWords = map[string]bool{
    "the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
    "were": true, "be": true, "been": true, "being": true, "have": true,
    "has": true, "had": true, "do": true, "does": true, "did": true,
    "will": true, "would": true, "should": true, "could": true, "can": true,
    "this": true, "that": true, "these": true, "those": true, "it": true,
    "its": true, "in": true, "on": true, "at": true, "to": true, "for": true,
    "of": true, "with": true, "and": true, "or": true, "but": true, "not": true,
    "how": true, "what": true, "why": true, "when": true, "who": true,
    "i": true, "you": true, "we": true, "they": true, "me": true, "my": true,
}
```

**`noteMatches(content, name string, terms []string) []string`** — returns
the subset of `terms` found as case-insensitive substrings in
`name + " " + content`. De-duplicated, order-stable.

**Extended `recallWikiNotes`**:

```go
func recallWikiNotes(projectRoot, workstreamName, query string, retracted map[string]bool) (memory string, items []recallItem, noteBytes [][]byte)
```

0. Filter: skip any note whose `<ws>-epoch-<N>` name is in `retracted`
   (populated by the caller from the journal's retraction events — §2).
1. Glob + parse epoch (unchanged).
2. Read each note's content (unchanged).
3. `terms := tokenizeQuery(query)`.
4. For each note, compute `matched := noteMatches(content, name, terms)`.
5. Partition into two tiers: `matched` (len(matched) > 0) and `unmatched`.
6. Sort `matched` by (match-count DESC, epoch DESC); sort `unmatched` by
   (epoch DESC).
7. Concatenate: matched tier first, then unmatched tier. Apply the 12 KB
   cap with note-boundary cut (unchanged). Build `items` and `noteBytes`
   in injection order.

When `query == ""` (e.g., fan-out where no single query applies, or a
future non-message trigger), `terms` is empty, every note is "unmatched,"
and the result is pure newest-first — the current behavior.

### 2. Retraction filter (extend `recallWikiNotes`)

Before partitioning, `recallWikiNotes` reads the conversation's retraction
events from the journal and excludes retracted `<ws>-epoch-<N>` pairs.
`recallWikiNotes` gains a `retracted map[string]bool` parameter (populated
by the caller from `handleSendMessage`/`handleFanoutSend`, which read the
journal). A note is skipped if `retracted[<ws>-epoch-<N>]` is true.

```go
// retractedNotes reads the conversation's memory_update{layer:"note",
// cause:"retract"} events and returns a set of "<ws>-epoch-<N>" note names
// that have been retracted (and not subsequently un-retracted). Used by the
// recall path to exclude retracted notes from the recall set.
func (s *Server) retractedNotes(ctx context.Context, conversationID int64) map[string]bool
```

Implementation: scan `ListEvents` newest-first for `memory_update` events
where `layer == "note"`; a `cause == "retract"` adds the note name (parsed
from `detail`); a `cause == "unretract"` removes it (future; not emitted in
M6 but the filter is forward-compatible). The `detail` field format is:
`"<ws>-epoch-<N> contradicted by <ws>-epoch-<M>: <snippet>"` — the first
token (before the first space) is the retracted note name.

### 3. memoryLayers + recall payload extension (`server.go`)

`memoryLayers` gains the query and retraction-set inputs and returns
`[]recallItem` instead of `[]string`:

```go
type memoryLayers struct {
    user    string
    project string
    pins    string
    index   string
    wiki    string
    recall  []recallItem // M6: was []string, now per-note with matched terms
    receipt map[string]string
}

func (s *Server) memoryLayers(ctx context.Context, wsName string, conversationID int64, query string) memoryLayers {
    // ... existing user/project/pins/index reads ...
    retracted := s.retractedNotes(ctx, conversationID)
    m, items, noteBytes := recallWikiNotes(s.projectRoot, wsName, query, retracted)
    ml.wiki = m
    // ... existing receipt population for user/project/pins/index ...
    for i, it := range items {
        ml.receipt[it.path] = sha16(noteBytes[i])
    }
    ml.recall = items
    return ml
}
```

The `user_message` payload's `recall` field serializes `[]recallItem` as
JSON objects. The fixed-marker layers (user.md, memory.md, pins.md,
index.md) are prepended as `recallItem{path, matchedTerms: nil}`:

```go
func (ml *memoryLayers) journalRecall() []interface{} {
    var out []interface{}
    add := func(path string) {
        out = append(out, map[string]interface{}{"path": path})
    }
    if ml.user != ""    { add("~/.odo/user.md") }
    if ml.project != "" { add(".odo/memory.md") }
    if ml.pins != ""    { add(".odo/pins.md") }
    if ml.index != ""   { add("wiki/index.md") }
    for _, it := range ml.recall {
        item := map[string]interface{}{"path": it.path}
        if len(it.matchedTerms) > 0 {
            item["matched_terms"] = it.matchedTerms
        }
        out = append(out, item)
    }
    return out
}
```

`handleSendMessage` and `handleFanoutSend` call `memoryLayers(ctx, w.Name,
c.ID, req.Text)` and journal `ml.journalRecall()` instead of `ml.recall`.
The `buildPrompt` signature is unchanged (the wiki block is already a
string).

### 4. Contradiction pass (new file `internal/ipc/contradiction.go`)

Runs inside `handleDistill`, after the note is written, before the learner.
Daemon-side string heuristic — no LLM.

```go
// contradictionTimeout is informational only — the pass is a string
// comparison, not an LLM call. It exists to bound a pathological note set.
const contradictionScanCap = 50 // max older notes scanned

// contradictionSignal is a negation/contrast token in a new-note sentence
// that, combined with ≥3 shared words with an older note, flags a
// contradiction.
var contradictionSignals = []string{
    "not", "no longer", "switched", "replaced", "removed",
    "instead of", "changed from", "migrated from", "deprecated",
}
```
Note: `contradictionSignals` are checked as raw substrings of the new-note
sentence (which is `normalizeRule`-normalized, not `tokenizeQuery`-tokenized).
The stop-words in `tokenizeQuery` (§1) apply only to keyword-recall query
tokenization, a separate code path — "not" being both a stop-word and a
contradiction signal is intentional and non-conflicting.

**`splitSentences(text string) []string`** — splits on `. `, `!\n`, `?\n`,
and bare newlines that start a bullet (`-\s`). Returns trimmed sentences.

**`detectContradictions(newNote string, oldNotes []epochNote) []contradiction`**:

```go
type contradiction struct {
    oldNote  string // "<ws>-epoch-<N>"
    newNote  string
    snippet  string // the contradicting sentence (truncated to 120 chars)
}
```

1. Tokenize `newNote` into sentences.
2. For each sentence, normalize it (`normalizeRule`).
3. For each older note (up to `contradictionScanCap`, newest-first):
   a. Normalize the older note's full content.
   b. For each new-note sentence: split both into word-sets; compute
      `shared = |intersection|`; if `shared >= 3` AND the new-note sentence
      contains a `contradictionSignal`, flag a contradiction.
4. Return the list (may be empty).

**`runContradictionPass(ctx, conversationID, noteName, noteContent, epoch)`**:
1. Read the recalled older notes for the workstream (reuse
   `recallWikiNotes` with `query=""` to get the newest-first set, excluding
   the just-written note).
2. `detectContradictions(noteContent, oldNotes)`.
3. For each contradiction: journal `memory_update{layer:"note",
   cause:"retract", detail:"<oldNote> contradicted by <newNote>: <snippet>",
   before_sha: sha16([]byte(oldNoteContent)), after_sha: sha16([]byte(oldNoteContent))}`.
   (The sha is the old note's content — it is not modified; the retraction
   is a journal record.)
4. Return the count (journaled in the distill `review_action`'s payload as
   `contradictions: <count>`).

**`itemsToEpochNotes(items []recallItem, projectRoot string) []epochNote`** —
re-reads each item's file from disk to recover the full content (recallWikiNotes
returns paths + matched terms, not content; the contradiction pass needs content).
Reuses the `epochNote` type from curator.go. The just-written note is excluded
by the caller (its name is known, so it is filtered from the items before this
call, or excluded here by name).

**`lastRecallCount(events []store.Event) int`** — scans the conversation's
events newest-first for the last `user_message` and returns the length of its
`recall` array (M6: the item count; pre-M6 rows: the path count). Used by the
ledger writer for the "recall notes" metric. Returns 0 when no user_message
exists (first distill).

### 5. Ledger writer (new file `internal/ipc/ledger.go`)

```go
const ledgerFileName = "ledger.md"

// ledgerMetric is one daemon-computed metric row, written to ledger.md as
// `- <label>: <value> (<event type> seq <S>)`.
type ledgerMetric struct {
    label string
    value string
    event string // the journal event type the metric was derived from
    seq   int    // the event's seq (0 when the metric spans two events)
}
```

**`appendLedger(projectRoot string, epoch int, metrics []ledgerMetric) error`**:
1. Build the section:
   ```
   ## epoch <N> — <RFC3339 UTC>
   - <label>: <value> (<event> seq <S>)
   …
   ```
2. Append to `.odo/ledger.md` (create if absent, mode 0644). If the file
   exists, append a `\n` separator + the section.
3. Return an error on I/O failure (the caller journals
   `memory_update{layer:"ledger", cause:"write_failed"}` but does not fail
   the distill).

**`distillLedgerMetrics(events []store.Event, epoch int, recallCount int) []ledgerMetric`**:
- `distill duration`: the distill `review_action` event's `created_at`
  minus the preceding `user_message` event's `created_at`. Both timestamps
  are journaled; the daemon parses them (`time.Parse(time.RFC3339, …)`).
- `recall notes`: `recallCount` (the length of the `recall` array on the
  last `user_message` before distill).
- `proposals`: the count of the `memory_propose` event's `proposals` array
  (0 if no propose was journaled — the learner failed or proposed nothing).

**`applyLedgerMetrics(batch pendingBatch, accepted, rejected int) []ledgerMetric`**:
- `accepted`: `accepted` count, from the `memory_apply` `review_action`.
- `rejected`: `rejected` count.

**`verifyLedgerQuote(quote string, event store.Event) bool`** — the inv-4
substring verification. Unmarshals the event's payload to a string
haystack (JSON marshal → string), normalizes (trim, collapse whitespace),
and checks `strings.Contains(haystack, quote)`. Returns false if the
payload is not a string or the quote is absent. This function is the
mechanical gate: any future LLM-selected ledger row must pass it before
being written.

### 6. handleDistill extension (`server.go`)

After the note is written, before the learner:

```go
// M6: contradiction pass (daemon-side, no LLM). Read the recalled older
// notes (newest-first, excluding the just-written note) with no keyword
// query — we want the full recall set, not a keyword-filtered one.
_, oldItems, _ := recallWikiNotes(s.projectRoot, w.Name, "", nil)
oldNotes := itemsToEpochNotes(oldItems, s.projectRoot) // read content from disk
count := s.runContradictionPass(ctx, c.ID, noteName, note, c.Epoch, oldNotes)

// M6: ledger append (best-effort).
recallCount := lastRecallCount(events)
if err := appendLedger(s.projectRoot, c.Epoch, distillLedgerMetrics(events, c.Epoch, recallCount)); err != nil {
    _, _ = s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
        "layer":  "ledger",
        "cause":  "write_failed",
        "detail": err.Error(),
    }))
}
```

The `review_action{action:"distill"}` event gains a `contradictions: <count>`
payload key. The learner pass runs after (unchanged).

### 7. handleApplyMemory extension (`server.go`)

After the `memory_apply` `review_action` is journaled:

```go
// M6: ledger append for accept/reject metrics (best-effort).
if err := appendLedger(s.projectRoot, batch.epoch, applyLedgerMetrics(batch, len(req.Accepted), len(rejected))); err != nil {
    _, _ = s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
        "layer":  "ledger",
        "cause":  "write_failed",
        "detail": err.Error(),
    }))
}
```

### 8. IPC commands (`protocol.go`)

```go
CmdLedger        = "ledger"         // {} → {memory_content: ledger.md content}
CmdContradictions = "contradictions" // {conversation_id} → {events: [retraction memory_update events]}
```

New `Request` fields: none (uses existing `ConversationID` and
`ProjectRoot`).

New `Response` fields: none (reuses `MemoryContent` for ledger, `Events`
for contradictions).

**`handleLedger(ctx, req) (Response, error)`**: resolve project, return
`.odo/ledger.md` content as `MemoryContent` (same shape as `read_pins`).
Missing file → `MemoryContent: ""`.

**`handleContradictions(ctx, req) (Response, error)`**: validate
conversation, scan events for `memory_update{layer:"note",
cause:"retract"}`, return them as `Events`.

### 8b. Explicit `.odo` diff guard (`server.go` — `handleDiffAction`)

ADR-0003 invariant 1 says "the daemon rejects accepted diffs that touch
them" (memory.md, memory-archive.md, pins.md). M4/M5 enforced this only
via gitignore (soft enforcement). M6 adds an explicit daemon-side guard.

**`handleDiffAction`** gains a path-prefix check before `git.ApplyDiff`:

```go
// rejectDiffsUnder checks whether the diff touches any protected path.
// Protected: .odo/ (memory.md, memory-archive.md, pins.md, ledger.md,
// journal.db, worktrees) and wiki/ (epoch notes, topics, index.md —
// derived artifacts owned by the daemon, not the agent).
func (s *Server) rejectProtectedPaths(diff *store.Diff) error {
    protectedPrefixes := []string{".odo/", "wiki/"}
    // The diff's files are in diff.Files (a []string of changed paths).
    for _, f := range diff.Files {
        for _, p := range protectedPrefixes {
            if strings.HasPrefix(f, p) {
                return fmt.Errorf("diff touches protected path %q (invariant 1: agents never write memory)", f)
            }
        }
    }
    return nil
}
```

Called at the top of `handleDiffAction` (both accept and reject paths
check — reject also logs, but only accept can write). On violation:
journal `agent_error{detail: "diff rejected: protected path touched"}`
and return the error to the caller (the diff stays `pending`).

This guard is belt-and-suspenders with the existing gitignore — if the
gitignore is ever removed or misconfigured, the daemon still rejects the
diff. It also protects `wiki/` (derived artifacts) from agent edits.

### 9. main.go subcommand dispatch

```go
func main() {
    // ... flag parsing for -project, -socket ...
    args := flag.Args()
    if len(args) > 0 && args[0] == "wiki" {
        os.Exit(runWikiCLI(args[1:]))
    }
    // ... existing daemon startup ...
}

// runWikiCLI dispatches `odo wiki <sub>`. M6: `read <page>` only.
func runWikiCLI(args []string) int {
    if len(args) < 2 || args[0] != "read" {
        fmt.Fprintln(os.Stderr, "usage: odo wiki read <page>")
        return 2
    }
    return wikiRead(args[1])
}
```

**`wikiRead(page string) int`** (new file `cmd_wiki.go` in package main):
1. Resolve `cwd` (`os.Getwd`).
2. Map `page` to a path:
   - `page == "index"` → `wiki/index.md`
   - `page == "ledger"` → `.odo/ledger.md`
   - `strings.HasPrefix(page, "topics/")` → `wiki/<page>.md`
   - otherwise → `wiki/<page>.md`
3. Guard: `filepath.Rel(cwd, resolved)` must not start with `..` and must
   be under `wiki/` or be `.odo/ledger.md`.
4. Read the file. On error: print to stderr, return 1.
5. Write content to stdout. Return 0.

## Frontend

### 10. Recall chip: keyword suffix + per-note matched terms

`MessageBubble.tsx` — the `recall` payload changes from `string[]` to an
array of objects. Update the type:

```typescript
interface RecallItem {
  path: string;
  matched_terms?: string[];
}
// In EventPayload:
recall?: RecallItem[];
```

`recallChipLabel`:
- Count notes = items where `path` is not a fixed marker.
- If any note has non-empty `matched_terms`, append
  `(keyword: <comma-joined unique terms>)` to the label.
- Example: `memory: index + 2 note(s) (keyword: auth, authentication)`.

`shortRecallPath`: unchanged (operates on the `path` field, present in
both old and new shapes).

Tooltip: for each item, render `shortRecallPath(item.path)` + (if
`matched_terms` is non-empty) ` [term1, term2]`. The tooltip joiner is
`\n`.

### 11. Ledger viewer

`MemoryReviewPanel.tsx` (or a new `LedgerPanel.tsx`) — add a "Ledger" tab
alongside "Proposals" and "Files". The tab calls `ledger()` API and renders
the raw `.odo/ledger.md` content in a read-only `<pre>` (same reader
component as the pins/memory.md reader). The ledger is read-only in the UI
(the daemon is the only writer). A "Open in editor" hint is not needed —
the user can `cat .odo/ledger.md` or `odo wiki read ledger`.

### 12. Retraction chip + badge

`App.tsx` — `recordEvents`: a `memory_update{layer:"note",
cause:"retract"}` sets a `lastRetraction` state
`{oldNote: string, newNote: string, snippet: string}` (parsed from the
`detail` field). The sidebar shows an ephemeral chip:
"⚠ `<oldNote>` retracted (contradicts `<newNote>`)." Dismissable (same
pattern as `lastMemoryUpdate`).

`WikiBrowser.tsx` — the note list calls `contradictions()` (or reads the
retraction events from the conversation's event history) and flags any
note whose name matches a retracted `<ws>-epoch-<N>` with a "⚠ retracted"
badge next to its row. The note is still clickable (it is a record) but
the badge signals it is no longer injected.

### 13. App.tsx state + handlers

- New state: `lastRetraction: {oldNote, newNote, snippet} | null`.
- New handlers: `handleLedgerTab` (opens the review panel's ledger tab).
- `recordEvents`: `memory_update` with `layer:"note"` sets
  `lastRetraction`; `layer:"ledger"` could set a toast (optional — ledger
  write failures are rare and logged).

### 14. types.ts + api.ts

```typescript
// types.ts
interface RecallItem {
  path: string;
  matched_terms?: string[];
}
// EventPayload.recall: RecallItem[] (was string[])
// New: RetractionPayload fields in EventPayload: before_sha, after_sha (already present)
// New response types:
interface LedgerResponse { ok: boolean; error?: string; memory_content?: string; }
interface ContradictionsResponse { ok: boolean; error?: string; events?: OdoEvent[]; }
```

```typescript
// api.ts
export async function ledger(): Promise<LedgerResponse> {
  return unwrap(await invoke<LedgerResponse>("ledger", { projectRoot: null }));
}
export async function contradictions(conversationId: number): Promise<ContradictionsResponse> {
  return unwrap(await invoke<ContradictionsResponse>("contradictions", { conversationId }));
}
```

## Tauri glue

`gui/src-tauri/src/lib.rs`:
- New passthrough commands: `ledger`, `contradictions`. Both use
  `READ_TIMEOUT` (read-only, no daemon-side blocking).
- `ledger`: `json!({"cmd": "ledger", "project_root": root})`.
- `contradictions`: `json!({"cmd": "contradictions", "conversation_id": conversation_id})`.
- Register both in `invoke_handler`.
- No timeout bumps. The CLI (`odo wiki read`) is not a Tauri command — it
  is a shell binary the agent runs directly.

## Verification

```bash
go build ./... && go vet ./... && go test ./... -count=1
cd gui && npx tsc --noEmit && npm run build
cd src-tauri && cargo check
```

## New Go tests (internal/ipc)

1. `TestKeywordRecallRanksMatches` — seed 5 epoch notes: epoch 1 mentions
   "authentication JWT", epochs 2-5 mention "build system". Query
   "authentication" → `recallWikiNotes` returns epoch 1 FIRST in the
   `items` list (matched tier) with `matchedTerms: ["authentication"]`,
   followed by epochs 5,4,3,2 (unmatched tier, newest-first) → assert the
   injected block's first `##` header is epoch 1.
2. `TestKeywordRecallFallsBackWhenNoMatch` — query "zzz" (matches no note)
   → `recallWikiNotes` returns pure newest-first (unchanged from pre-M6)
   with all `matchedTerms` empty.
3. `TestKeywordRecallStopWords` — query "how does the auth work" →
   `tokenizeQuery` returns `["auth", "work"]` (stop-words removed) →
   notes matching "auth" rank first.
4. `TestRecallPayloadMatchedTerms` — send_message with text "auth" on a
   project with a matching note → the journaled `user_message`'s `recall`
   payload is an array of objects; the matching note's object has
   `matched_terms: ["auth"]`; the fixed markers (user.md, index.md) have
   no `matched_terms` key (omitted when empty).
5. `TestRetractionExcludesNoteFromRecall` — seed epoch 1 + epoch 2. Journal
   a `memory_update{layer:"note", cause:"retract", detail:"main-epoch-1
   contradicted by main-epoch-2: …"}`. `recallWikiNotes` with the
   retracted set → epoch 1 is excluded; only epoch 2 is injected.
6. `TestContradictionPassFlagsConflict` — new note: "Auth switched from
   JWT to session cookies." Old note: "Authentication uses JWT." →
   `detectContradictions` returns one contradiction (oldNote=epoch-1,
   snippet="Auth switched…"); the distill journals a
   `memory_update{layer:"note", cause:"retract"}` event.
7. `TestContradictionPassNoFalsePositive` — new note: "Added a JWT refresh
   endpoint." Old note: "Authentication uses JWT." → no contradiction
   (shared words but no negation/contrast signal in the new sentence) →
   `detectContradictions` returns empty.
8. `TestLedgerAppendedAtDistill` — distill epoch 1 → `.odo/ledger.md`
   contains a `## epoch 1` section with `distill duration: …s
   (review_action seq …)` and `recall notes: …` bullets → the `seq`
   values reference real journaled events.
9. `TestLedgerAppendedAtApply` — distill + apply_memory (accept 2, reject
   1) → `.odo/ledger.md` has an epoch section with `accepted: 2, rejected:
   1 (memory_apply seq …)`.
10. `TestLedgerWriteFailureJournalsNotFails` — make `.odo/` read-only →
    `appendLedger` fails → `memory_update{layer:"ledger",
    cause:"write_failed"}` journaled → distill still succeeds (returns
    `OK:true`).
11. `TestVerifyLedgerQuote` — (a) a quote that IS a verbatim substring of
    a journaled event's payload → `verifyLedgerQuote` returns true; (b) a
    fabricated number not in the payload → returns false. Pins inv 4.
12. `TestCLIRunsFromWorktree` — (in `main_test.go`) `odo wiki read
    main-epoch-1` with cwd set to a temp project root containing
    `wiki/main-epoch-1.md` → stdout is the file content, exit 0; `odo wiki
    read ../../etc/passwd` → exit 1, stderr has "only files under wiki/".
    Tests the subcommand dispatch + path guard without a running daemon.
13. `TestDiffGuardRejectsProtectedPaths` — seed a diff whose file list
    includes `.odo/memory.md` or `wiki/topics/auth.md` → `accept_diff`
    returns an error naming the protected path, the diff stays `pending`,
    no `git apply` is called. A diff with only normal files (e.g.,
    `hello.txt`) is accepted normally.

GUI E2E (cua-host AX tree, M3/M4/M5 pattern): Demo A/B/C/D as described;
assert recall chip `(keyword: …)` suffix + tooltip, ledger tab content,
retraction chip + wiki-browser badge.

## Review

Changes on the agent-prompt path (`recallWikiNotes` keyword tiering +
retraction filter, `memoryLayers` recall payload shape, `handleDistill`
contradiction pass + ledger append, `handleApplyMemory` ledger append),
new `main.go` subcommand dispatch + `cmd_wiki.go`, new IPC (2 commands) +
frontend (ledger viewer, retraction chip/badge, recall chip keyword
suffix): fresh-context dual-model review (GLM-5.2 + K3 audit) before close
(per dev principle #4).

## Risks and rejected alternatives

### Risks

1. **Keyword recall false negatives.** Tokenized substring matching misses
   synonyms ("auth" does not match "login" unless "login" is in the
   query). A note about "session management" won't match a query about
   "authentication" even though they are related. **Mitigation:** the
   unmatched tier fills the 12 KB cap with newest-first notes, so a
   miss degrades to the current behavior (no regression). The user can
   also use `odo wiki read <page>` to pull any note directly. Embeddings
   (deferred) would close the synonym gap but violate inspectability at
   this scale.

2. **Contradiction false positives.** The negation/contrast heuristic
   could flag a non-contradiction (e.g., "we did NOT remove JWT" shares
   words with "uses JWT" and contains "not"). **Mitigation:** the
   heuristic requires `shared >= 3` words AND a signal token; "not" alone
   in an affirmative sentence ("did not remove") is a weak signal. The
   recall exclusion is reversible (a future `unretract` command). The
   retraction chip lets the user see and correct false positives. The
   heuristic errs conservative (fewer flags, not more).

3. **Ledger growth.** `.odo/ledger.md` is append-only with no cap. A
   100-epoch project accumulates 100 sections. **Mitigation:** the file is
   never injected (pull-only); the user can truncate old sections
   manually (it is a record, not a source of truth — the journal is).
   A future `odo ledger prune` CLI could trim old sections (deferred).

4. **Recall payload shape change (breaking).** The `recall` field on
   `user_message` changes from `[]string` to `[]object`. Existing journal
   rows have the old shape; the frontend must handle both. **Mitigation:**
   the frontend's `recall` parser checks `typeof item === "string"` (old)
   vs `typeof item === "object"` (new) and normalizes both to
   `{path: item}` / `item`. The daemon only writes the new shape going
   forward; old events replay with the old shape (the journal is
   append-only — no backfill). This is a payload-key extension, not a
   schema change (ADR-0002 preserved).

### Rejected alternatives

1. **Embeddings / vector store for keyword recall.** Rejected (ADR-0003
   rejected/deferred): at this scale (≤50 notes in the recall window),
   tokenized substring matching is inspectable (matched terms are in the
   payload), deterministic, and adds zero dependencies. Embeddings
   violate inspectability (the similarity score is opaque) and add a
   runtime dependency (an embedding model). Revisit only if the composed
   index+pull path demonstrably fails at scale (the ADR's documented
   revisit trigger).

2. **LLM one-shot for contradiction detection.** Rejected (inv 4
   discipline): an LLM contradiction pass would be in the metric/data
   path — its output (a contradiction verdict) would steer recall
   exclusion. The daemon-side heuristic is mechanical, deterministic, and
   auditable (the shared-word count and signal token are in the journal
   detail). An LLM pass would also add a one-shot's latency (minutes) to
   every distill; the daemon pass adds milliseconds.

3. **Store ledger as a SQL view over the journal.** GLM-5.2 proposed this;
   the ADR resolved for the file form ("browsable/greppable, but
   daemon-only writer + substring verification keeps GLM's un-editable-by-
   LLM property"). A SQL view is not greppable, not human-readable without
   a query tool, and would require a new table or view (schema impact).
   The file is `.odo/ledger.md`, append-only, daemon-written.

4. **MCP server for pull-based recall.** Rejected (ADR-0003 divergence
   table: "K3's path: M5 index → M6 pull via plain CLI"). An MCP server
   adds a transport layer (JSON-RPC over stdio/HTTP), a tool schema, and
   daemon plumbing — for no win over a shell binary the agent already
   can run. Coding agents have shells; `odo wiki read <page>` is one
   `exec` away. The CLI also works in a worktree (different path from the
   daemon's bound root) without daemon access.

5. **Separate `odo-cli` binary.** Rejected: the existing `odo` binary is
   already the project's compiled artifact; a subcommand mode reuses the
   build, the path resolution, and the user's muscle memory (`odo` is on
   their PATH). A second binary doubles the build surface and the install
   instructions. The subcommand dispatch is ~20 lines in `main.go`.

6. **Verify ledger quotes at write time via an LLM.** Rejected (inv 4):
   the verification is mechanical — `strings.Contains(haystack, quote)`.
   An LLM verifier would be in the data path and could be confabulated.
   The daemon's `verifyLedgerQuote` is the gate; an LLM may *select*
   candidate quotes (future), but the daemon verifies each one.

7. **Retract by editing the epoch note file.** Rejected (inv 2: epoch
   notes are rebuildable from the journal; inv 3: no silent deletion).
   Editing the file would make the on-disk note diverge from the
   journal's record of what was distilled. The retraction is a journal
   event that the recall function filters on — the file is the permanent
   record, the journal controls injection. This mirrors how `memory.md`
   retraction works (the rule is moved to the archive, not deleted from
   the journal).
