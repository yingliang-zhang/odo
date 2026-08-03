# M4 — Learning (project memory.md + user.md learning + injection receipt + memory chip)

> **Rationale.** M3 closed the *read* half of the memory loop: `buildPrompt`
> injects a wiki recall block plus the global `user.md`, and the user can
> verify what the agent sees via the wiki browser. But nothing *learns*: the
> distilled epoch notes hold behavior-shaping rules ("always verify with real
> tool output", "prefer compact output") that stay trapped in per-workstream
> notes, reused on every run but never promoted to the always-injected layers
> where they would actually steer. ADR-0003's "Learning" phase is: project
> `memory.md` (behavior rules, always injected), user.md learning (evidence-
> constrained: every rule cited; user.md rules must recur across ≥2 projects),
> an injection receipt (content hashes so M5 can audit what the model saw),
> a memory-changed surface in the UI, and archive rotation with
> retraction-with-record.

## Pain

- I distill a conversation into a wiki note; it contains an imperative rule
  ("always run `go vet` before finishing") that would change what the agent
  does on the *next* run. But recall only injects notes' *content*, not a
  distilled rule set, and nothing ever promotes an observation into a durable
  behavior contract. (pain #1)
- `memory.md` is an always-injected layer for behavior rules, but nothing
  writes it. The closest today is `user.md`, which the user must hand-
  maintain. The write path M1's distill opened for notes never extends to
  rules. (pain #1, #2)
- When the agent's prompt is built, nobody (including an M5 auditor) can tell
  *which* injected layers went in in what order — the `recall` paths list
  records *paths*, not *content identity*. A stale/truncated `user.md` is
  injected silently. There is no receipt. (ADR inv 5)
- When my project `memory.md` fills up or a new epoch contradicts an old
  rule, the old rule should be demoted to an append-only archive with a
  reason — not silently dropped, not kept bloating the cap. Nothing does
  this today. (ADR inv 3)
- After `memory.md` or `user.md` changes I have no in-app signal that the
  agent's next prompt will differ. (pain #3 — invisible work)

## Demo

### Demo A: project memory is learned and injected

1. In a workstream, `send_message` a repeatable instruction ("Always run
   `go test ./...` before claiming a task is done."). Let the run finish;
   click **Distill**.
2. The daemon's learner pass (one orchestrator-model one-shot, after the
   wiki note write) turns the new epoch note into candidate rules. The
   sidebar Memory section shows a badge **"2 memory rules proposed"** with a
   **Review** button.
3. Open the review panel: it lists the two proposed rules, each with its
   source note (`main-epoch-1`), and the daemon's suggested wording. Accept
   both; reject none.
4. `.odo/memory.md` is written (`- Always run go test ./... before claiming
   a task done. — cites: main-epoch-1; reaffirmed: 1`). A `memory_update`
   event fires; the sidebar Memory section shows a green "memory updated"
   chip and a detail line linking to the new `memory.md` content.
5. Send another message: the `user_message` bubble shows
   `memory: user.md + memory.md + 1 note(s) recalled`, and the journal
   payload carries `recall: ["~/.odo/user.md", ".odo/memory.md", "<path>/main-epoch-1.md"]`
   plus a `receipt: {path: sha256[:16]}` map. The agent's reply reflects the
   rule (it no longer claims done without running the test).

### Demo B: user.md learns a cross-project rule

1. A second real project (say `ananke`) has a `~/.odo/user.md`-eligible
   rule theme ("Always verify a conclusion with a concrete tool result.")
2. Distill this project's current workstream. The learner scans its own new
   note AND — via the `~/.odo/projects.json` registry — the other
   registered project's `memory.md`, and finds the same rule theme in the
   new note + ananke's `memory.md` = ≥2 distinct registered projects.
3. The review panel shows a second section "user.md (global) — saw in 2
   projects": the recurring principle with the two cited projects.
4. Accept → `~/.odo/user.md` gains
   `- Always verify conclusions with concrete tool results. — seen: odo, ananke`.
   The chip + review panel reflect it; the `seen:` field carries the
   recurrence evidence (an M5 audit can confirm ≥2 from it).
5. A single-project-only rule is offered only as a `memory.md` row — never
   directly to `user.md`.

### Demo C: archive rotation + retraction-with-record

1. Fill `.odo/memory.md` near its 4 KB cap (e.g. 16 rules). Distill a new,
   clearly useful rule and accept it in review.
2. The daemon detects overflow; the least-recently-reaffirmed rules (lowest
   `reaffirmed`) are rotated to `.odo/memory-archive.md` (appended under an
   `## <RFC3339> — rotated from memory.md (overflow)` header), and
   `memory.md` stays ≤ 4 KB. The `memory_update` event carries the change;
   the review panel shows "rotated to archive".
3. A new epoch note contradicts an existing rule: on accept of the new one,
   the conflicting old rule moves to the archive under a
   `## <RFC3339> — retracted: <incoming rule prefix> (conflict)` header, is
   removed from memory.md, is journaled, and is surfaced in the panel.
4. Archive is append-only: subsequent writes append, never rewrite.

## Not built

- **Auto-write without human review.** The ADR auto gate (accept-rate
  > 90% for 3 epochs) is only about batch-theater detection: M4 records the
  accept/reject counts per epoch in the journal. No rule write happens
  without a human accepting it in the batch review panel.
- **Silent auto-detection.** The learner proposes; the human decides. A
  proposal never lands in a file unless reviewed AND accepted.
- **Continuous/parallel learning.** Learning runs only at the distill epoch
  boundary (ADR inv 7: distill is the only write cadence), as a sync
  one-shot at the end of `handleDistill`.
- **M5 scope:** curator pass, `topics/`, `index.md`, pin affordance —
  deferred (ADR-0003 L5).
- **M6 scope:** pull-based recall CLI, ledger.md with verified metrics —
  deferred.
- **Attestation / sandboxing / per-entry accept/reject theater.**
- **A second global file registry beyond `~/.odo/projects.json`.**

## Architecture decision statements

| Topic | Decision |
|---|---|
| Learning cadence | At distill (epoch boundary), synchronous learner one-pass using the orchestrator model (`distillAdapter`, same family distill uses); proposals journaled |
| Proposal storage | Journal-only: one `review_action {action:"memory_propose", epoch, proposals:[…]}` per distill epoch (single event carries both `memory` and `user` proposals); no new DB table (ADR-0002 preserved) |
| Apply/completion | `apply_memory` IPC references a *proposal batch* (epoch/seq); writes files + journals `review_action {action:"memory_apply", …}` and a `memory_update` event |
| Batch identity | One pending batch per conversation. A new distill *supersedes* the previous pending batch even when the newer distill emits no batch (learner failure or zero proposals): the pending batch is the one whose `epoch` equals the latest distill's `newEpoch − 1`. Apply marks the current batch consumed (idempotent — second apply returns "already applied") |
| memory.md format | `- <rule> — cites: <epoch-note-name>; reaffirmed: <epoch>` per line (parseable, human-editable). Rules without a `cites:` tag are "opaque text" — kept but never rotation/retract-eligible |
| user.md format | `- <rule> — seen: <p1>, <p2>` (user-owned shape; citations = project recurrence list) |
| Caps | `memory.md` ≤ 4 KB, `user.md` ≤ 4 KB, line-boundary cut; recall block ≤ 12 KB. Caps enforced at *read* and at *apply* |
| Injection order | `user.md → memory.md → wiki recall → attachments → message` (ADR inv 6), via a `projectMem` parameter in `buildPrompt` |
| Injection receipt | `receipt: {path: sha256[:16]}` hashed over the *exact injected strings* (non-empty layers only), journaled on every prompt-building `user_message` |
| Cross-project registry | `~/.odo/projects.json` (daemon-owned, 0600), appended idempotently at `NewServer`; registry `root` stored resolved via `EvalSymlinks` at write time; learner reads exactly `Join(row.Root, ".odo", "memory.md")` for sibling rows |
| Evidence validation (daemon) | At propose-parse: drop memory proposals whose `evidence` ≠ the just-written epoch note; drop user proposals unless the rule is found (normalized) in ≥2 distinct registered projects' staged inputs ({own memory.md, new note} ∪ sibling memory.mds). Dropped counts carried in the propose event |
| Reaffirm mechanics | Learner output may also list `reaffirm:[existing rule texts]`; on apply these lines' `reaffirmed` bumps to the proposed epoch. This keeps "least-recently-reaffirmed" rotation meaningful (not pure FIFO) |
| Overflows | memory.md: LRU whole-rule rotation to archive (influx excluded); user.md: **apply refuses** a set that would overflow 4 KB, erroring with the offending rule (cap-on-read stays) |
| Contradiction | learner emits `contradicts:<existing rule text>`; apply matches with normalized comparison (trim/lower/collapse ws); match → retraction-with-record to archive + `memory_update`; no-match → journaled and surfaced, never silent |
| New event type (value only) | `memory_update` added to `store` const block + frontend `EventType` union. No new DB table |
| GUI timeouts | `gui/src-tauri/src/lib.rs` `DISTILL_READ_TIMEOUT` bumped from 660 s to cover distill+learner (10 + 5 m) + margin → **960 s** |
| Review UI | New Memory Review modal from the sidebar Memory section (§7) + ephemeral chip |
| Review weight | `buildPrompt` signature + journal payloads + new IPC: fresh-context dual-model review before close |

## Backend

### 1. Global cross-project registry

`~/.odo/projects.json` — a daemon-owned global file (mode 0600, sibling of
`prefs.md`/`user.md`):

```json
[{"root": "/Users/yingliangzhang/Projects/odo", "name": "odo", "added": "2026-08-03T…"}]
```

- `ensureProjectRegistered(root)` (new helper in `internal/ipc/registry.go`):
  `name = filepath.Base(root)`; `root = filepath.EvalSymlinks(root)` before
  storing (symlink-safe); parse-if-exists, append if `root` absent, write
  atomically (temp + rename, 0600). Called once at `NewServer` (the binding
  point — no ambiguity). Registration failure is best-effort: log-and-
  continue; the learner degrades to no siblings.
- Reader helper `registeredProjects()` returns the resolved rows. **The
  bound project is excluded by comparing EvalSymlinks-resolved forms on
  BOTH sides** — i.e. the daemon also stores its own bound root in
  EvalSymlinks-resolved form at `NewServer` (macOS rewrites
  `/var→/private/var`, exactly what `t.TempDir()` produces, so comparing
  `filepath.Abs` root against a resolved row would falsely include the
  bound project as its own sibling and let one project satisfy the ≥2
  recurrence gate twice).
- **The learner reads exactly one path per sibling row**
  `filepath.Join(row.Root, ".odo", "memory.md")` after a
  `filepath.Clean` equality check on that joined path == the literal
  string; nothing else. No symlink resolution at read time (roots were
  EvalSymlinks'd at write).

### 2. Learner pass (runs inside `handleDistill`, after the wiki note write)

After the wiki note is persisted, the daemon runs **one more orchestrator
one-shot** (reuse `runOneShot` + `distillAdapter`, new constant
`learnerTimeout = 5*time.Minute`).

`learnerPrompt` (new helper, `internal/ipc/learner.go`), inputs in stable
order:

1. the workstream's new `-epoch-N` note just written (its content),
2. the current `.odo/memory.md` of this project (≤ 4 KB),
3. for each sibling from the registry (≤ 3, sorted by `added` desc): that
   project's `.odo/memory.md` content (≤ 4 KB each),
4. the current `~/.odo/user.md` (≤ 4 KB).

Output contract — a JSON object the daemon `json.Unmarshal`s:

```json
{
  "memory": [
    {"rule": "...imperative...", "evidence": "main-epoch-1", "contradicts": ""}
  ],
  "user": [
    {"rule": "...global imperatives...", "projects": ["odo", "ananke"]}
  ],
  "reaffirm": ["existing rule text to bump to current epoch", "…"]
}
```

- `memory` entries: behavioral rules from the new note, absent from current
  memory.md; `evidence` (the just-written note name) mandatory,
  `contradicts` optional.
- `user` entries: allowed only when the rule is found (normalized) in ≥2
  distinct registered projects' staged inputs — where "staged inputs" for
  the *bound* project = {current memory.md, the just-written new note}, and
  for each sibling = that project's `.odo/memory.md`. If fewer than 2
  registered projects total exist (bound + siblings), `user` must be `[]`
  (the panel then hides the user.md section).
- `reaffirm`: optional list of *existing* rule texts whose
  `reaffirmed` epochs get bumped at apply (keeps LRU meaningful).
- **Daemon-side validation at parse** (removes dependence on LLM
  self-tagging — ADR inv 4's discipline applied to rules too):
  - drop-and-count any `memory` proposal whose `evidence` is not exactly the
    just-written epoch note name;
  - drop-and-count any `user` proposal that is not evidenced in ≥2 distinct
    registered projects (own memory.md/note + sibling memory.mds as above),
    or whose rule text is not found (normalized) in ≥2 of those staged
    inputs;
  - dedupe (verbatim match) against current memory.md.
- **Journal** one
  `review_action {action:"memory_propose", epoch: <N>, proposals:[…after
  veto…], reaffirm:[…existing rule texts…], stats:{memory_kept,
  memory_dropped, user_kept, user_dropped}}`.
  `reaffirm` is journaled here so the apply pass can recover it after a
  daemon restart (journal-only persistence — §5 apply reads the batch from
  the journal). Each proposal object carries its own fields
  (`{target, rule, evidence?, contradicts?, projects?}`, `projects`
  replaced by daemon-verified values before journaling; `contradicts` is
  journaled as-is and matched at apply-time). Files are NOT written on
  propose.
- **Learner failure** (empty output / non-JSON / timeout): journal
  `memory_update {layer:"learner", cause:"failed", detail:"…"}` and
  continue — distill must not fail because learning failed. No
  `memory_propose` event is emitted in this case (well-defined "no pending
  proposals" path).
- `handleDistill`'s `Response` gains `MemoryProposals int`
  (`memory_proposals,omitempty`) = count of pending `memory` + `user`
  proposals in the new batch (0 if learner failed) so the frontend can
  badge.
- The Distill UI busy state already blocks while `handleDistill` runs; the
  learner extends that same block. No new concurrency.

### 3. memory.md + user.md file I/O, caps, rotation, retraction

New helper file `internal/ipc/learner.go` (plus `internal/ipc/io.go` for the
shared atomic writer):

- constants: `memoryCap = 4*1024`, `recallMemory / recallCap` unchanged for
  wiki, `archiveName = "memory-archive.md"`, `memoryFileName = "memory.md"`.
- `readProjectMemory(projectRoot) string` — read `.odo/memory.md`, cap at
  `memoryCap` with a line-boundary cut (mirror `readUserMemory`).
- `parseMemoryLines(content) []memoryRule{text, cites string, reaffirmed
  int, opaque bool}` — regex
  `^- (.+?) — cites: ([^;]+)\s*(?:; reaffirmed: (\d+))?$`;
  non-matching lines are preserved as opaque text (human-hand-edited rules
  without a `cites:` tag) — kept but never a rotation/retraction candidate.
- `writeFileAtomic(path, content)` in `internal/ipc/io.go` (temp +
  rename in the same dir, mode 0644 for project memory files, 0600 for
  `~/.odo/user.md` — mirroring `UpdateSettings`'s pattern but shared).
- `applyMemoryRules(projectRoot string, old string, accepted []acceptedRule, epoch int)`
  — the single write path for memory.md; `acceptedRule` carries
  `{rule, evidence, contradicts}` from the proposal structs (NOT bare rule
  strings — the steps below depend on `evidence` and `contradicts`):
  1. append each accepted rule line
     `- <rule> — cites: <evidence>; reaffirmed: <epoch>` (`evidence` = the
     proposal's `evidence`; `epoch` = the propose event's epoch = the
     distilled note's epoch, `c.Epoch` before `IncrementEpoch` — NOT the
     post-increment `newEpoch` returned by the existing distill event),
  2. bump any `reaffirm` targets (match on normalized text),
  3. compute `projected = len(newContentBytes plus '\n')`; while projected >
     `memoryCap` and candidates exist, evict the whole rule with the lowest
     `reaffirmed` (never the incoming rule — it is newest by definition),
     append each to `memory-archive.md` under a fresh header
     `\n## <RFC3339> — rotated from memory.md (overflow)\n`;
  4. if a new rule had `contradicts` that normalizes to an stored rule,
     remove that stored rule and append it to the archive under
     `\n## <RFC3339> — retracted: <incoming rule prefix> (conflict)\n`;
     if `contradicts` matches nothing, journal
     `memory_update {layer:"memory", cause:"retract", detail:"no match for
     contradicts: …"}`
     rather than silently keeping the contradiction;
  5. write the resulting `memory.md` (atomically, 0644) and journal
     `memory_update` (§6).

  `old` is the file read in FULL (uncapped). The capped `readProjectMemory`
  is injection-only and must never be the apply-read basis — otherwise apply
  would silently truncate a legitimately-over-cap file (human-edited opaque
  lines are kept word-for-word; ADR inv 3 "no silent truncation"). The same
  full-read rule applies to user.md (the refuse-on-overflow check and
  write-back read the file in full, uncapped).
- **user.md applies**: `applyMemory` reads `~/.odo/user.md` (in full, see
  above), appends accepted user proposals as
  `- <rule> — seen: <p1>, <p2>` where `seen:` derives **from the daemon's
  own verified matches** (sibling registry `name`s whose `.odo/memory.md`
  contained the rule, plus the bound project's own registry name when its
  memory.md/note matched) — never from the LLM's self-tagged `projects`
  array, which is display-only. Refuses the whole user-target apply with an
  error naming the offending rule when the result would exceed 4 KB ("no
  silent write": never truncate a user file; cap-on-read remains). user.md
  writes use mode 0600.

### 4. buildPrompt memory parameter + injection receipt

- `buildPrompt` signature (existing shape + one new param):
  ```go
  func buildPrompt(text string, attachments []string, userMem, projectMem, memory string) string
  ```
  The new `projectMem` opaque block is injected **after `userMem`, before the
  attachments/message**; stable headers:
  `## User memory (durable cross-project principles)` →
  `## Project memory (behavior rules)` (new) →
  `## Prior notes (recalled)` (the existing wiki block — header renamed from
  the current `## Project memory (from prior distilled epochs)` to avoid
  two "Project memory" sections) →
  `Attached files: …` → message.
  (`recall` is a return value, NOT a buildPrompt parameter.)
- `handleSendMessage` / `handleFanoutSend` (both — the shared call site):
  ```go
  projectMem := readProjectMemory(s.projectRoot)
  memory, notePaths, noteBytes := recallWikiNotes(s.projectRoot, w.Name) // now returns injected bytes too
  receipt := map[string]string{}
  if userMem != "" { receipt["~/.odo/user.md"] = sha16([]byte(userMem)) }   // fixed key
  if projectMem != "" { receipt[".odo/memory.md"] = sha16([]byte(projectMem)) } // fixed key
  for i, p := range notePaths { receipt[p] = sha16(noteBytes[i]) }            // p is the abs note path
  // recall payload order: user.md (present) → memory.md (present) → notePaths (abs)
  recall := []string{}
  if userMem != "" { recall = append(recall, "~/.odo/user.md") }
  if projectMem != "" { recall = append(recall, ".odo/memory.md") }
  recall = append(recall, notePaths...)
  payload := {"text":…, "attachments":…, "recall": recall, "receipt": receipt}
  prompt := buildPrompt(req.Text, req.Attachments, userMem, projectMem, memory)
  ```
  Each receipt sha = 16 hex chars of SHA-256 **of the string actually
  injected**. For each note, the hashed bytes are **the exact block string
  `recallWikiNotes` built** — `## <basename>\n\n<content>\n\n---\n\n` — i.e.
  `noteBytes[i]` is that full block as appended to the memory section, not
  the raw file content. `recallWikiNotes` must return the per-note byte
  slices it built (signature:
  `(memory string, paths []string, noteBytes [][]byte)`) so the hash matches
  injection exactly. Receipt keys are the exact strings in `recall`
  (`~/.odo/user.md`, `.odo/memory.md`, and the note paths) — no re-joining.
  The `recall` payload slice and the note-receipt loop use **different**
  slices: `recall` prepends the fixed markers, while the
  `notePaths`/`noteBytes` loop covers notes only. Entries are added only for
  non-empty injected layers, and per M3's established convention (server.go
  340-343) `recall`/`receipt` are **omitted entirely when empty** — no empty
  arrays/objects in the payload.
- The `user_message` payload-extension precedent (M1/M3) continues: `recall`
  and `receipt` ride the existing payload; no new table or event type.
- **Steering carve-out**: `handleSteering` journals `user_message` without
  any prompt build — it carries no `recall`/`receipt`. State this so
  "receipt on every user_message" is understood as "every prompt-building
  user_message".

### 5. IPC: memory read/proposals/apply + memory_update event

New commands (protocol.go):

```go
CmdReadMemory      = "read_memory"        // {project_root} → {memory_content, archive_content, user_content}
CmdMemoryProposals = "memory_proposals"   // {conversation_id} → {epoch, seq, proposals:[…]}
CmdApplyMemory     = "apply_memory"       // {conversation_id, epoch, accepted:[{target,index}], ...} → {…}
```

(One batch per epoch; the `epoch` in the request pins the batch.)

- `handleMemoryProposals`: pending batch = the `memory_propose` event whose
  `epoch` equals (latest distill event's `newEpoch − 1`); review it via
  `ListEvents` newest-first (skip consumed batches — an `action:"memory_apply"`
  for that `epoch` makes it "no pending"). If the latest distill emitted no
  propose (learner failure / zero proposals after veto), the previous batch
  is superseded and nothing is pending. Return
  `{epoch, seq, proposals: event.payload.proposals, reaffirm:
  event.payload.reaffirm}` where each proposal carries its own
  `target:"memory.md"|"user.md"`.
- `handleApplyMemory` — **all-or-nothing**: nothing is written or journaled
  unless every target in the batch succeeds.
  1. validate `epoch` matches the latest pending propose (else
     "already applied" / "no pending batch");
  2. split `accepted` by proposal `target` (`memory.md` / `user.md`);
  3. **pre-compute** for `memory.md`: rule lines + rotation/retraction/cap
     result, WITHOUT touching disk;
  4. **pre-compute** for `user.md`: lines from accepted `user` proposals,
     and check the 4 KB cap — if overflow, **refuse the whole apply** with
     an error naming the offending rule and write nothing / journal nothing;
  5. only then: write `memory.md` (atomic, 0644), write `user.md` if any
     (`atomic`, 0600), journal `memory_update` (§6) for each changed layer;
  6. journal `review_action {action:"memory_apply", epoch, accepted, rejected, metrics:{accepted, rejected}}`
     (daemon-computed, no LLM) — this is the batch-consumed marker;
  7. "already applied" is returned ONLY for a second apply after a
     *successful* consume. A refused apply (user.md overflow) leaves the
     batch pending — a retry recomputes from the original proposes and may
     succeed (e.g. the user trimmed user.md in between).
  Because the batch is only marked consumed when *all* targets succeed, a
  refused apply leaves the batch pending and a retry recomputes from the
  original proposes — no duplicate lines, no partial state.
- `handleReadMemory`: no user-supplied path. `{project_root}` is equality-
  checked against the bound root (reuse the `resolveProject` guard,
  server.go 181-190 — reject any other root), then the handler returns the
  contents of the three canonical files it constructs itself:
  `filepath.Join(s.projectRoot, ".odo", "memory.md")`,
  `filepath.Join(s.projectRoot, ".odo", "memory-archive.md")`,
  `filepath.Join(home, ".odo", "user.md")`, as `memory_content`,
  `archive_content`, `user_content`. Missing files return `""`. The archive
  is returned in full (append-only, uncapped at this scale — it is never
  injected) and this read path is brand-new: M3's `read_wiki` guard (wiki/
  only) is incompatible with `.odo/` and is not reused.

### 6. memory_update event

- `store.go` const block: add `EventMemoryUpdate = "memory_update"`.
- Payload shape (journal-recorded on every memory layer change):
  `{layer: "learner"|"memory"|"user", cause: "apply"|"rotate"|"retract"|"failed", before_sha, after_sha, detail}`.
  (enumerated values so the UI switch is exhaustive; `failed` is the
  learner-failure cause, `detail` carries the error text; `verify_failed`
  was dropped — evidence-vetoed proposals are counted in the propose event's
  `stats`, not as a separate memory_update.)
- The `memory_update` events are journaled like any other event type and
  arrive through the normal poll path; no new journal table.

## Frontend

### 7. Memory ReviewPanel + sidebar integration

- `api.ts`: `readMemory()`, `memoryProposals(conversationId)`,
  `applyMemory(req)`; `types.ts` adds `MemoryProposal {target, rule,
  evidence?, contradicts?, projects?}` (evidence optional — user-target
  proposals have no note evidence; the daemon sets it when the base
  project's matched input was the new note), `PendingMemoryBatch {epoch,
  seq, proposals, reaffirm?}` (reaffirm is daemon-internal, echoed for
  transparency), responses, and `EventType` = `… | "memory_update"`;
  `EventPayload` gains `receipt?: Record<string,string>`.
- `components/MemoryReviewPanel.tsx` (new, modal overlay, same visual
  language as `WikiBrowser`):
  - header: "Memory Review — <conversation>", close X,
  - `memory.md` section: each proposal row shows rule text + evidence note
    + [Accept][Reject] (default Accept);
  - when `user` proposals exist (>0), a "user.md (global)" section with the
    project list; hidden when the batch's `user` is empty (§2);
  - "Apply" button → `applyMemory`; on success refresh + show
    "applied — N rules" + the `memory_update` change summary;
  - reader tab "current memory.md / archive (append-only)" reusing the
    WikiBrowser markdown CSS.
- `Sidebar.tsx` Memory section: after the wiki-count line, when
  `pendingMemoryProposals > 0` show `N memory proposed — Review`; when
  `lastMemoryUpdate` set, show the green "memory updated" chip (click →
  MemoryReviewPanel). New props threaded from `App.tsx`.
- Review panel project lists (user.md section) show **daemon-verified matched
  project names** (from the apply-path computed `seen:` set) — never the
  LLM's display-only `projects` tags.

### 8. Chip + Distill integration

- `App.tsx` **must NOT handle `memory_update` in `applyBootstrap`** (bootstrap
  replaces events wholesale; a `memory_update` replayed from a prior session
  must not re-chip). Handle it only in `recordEvents` (the poll path).
  Add two state vars: `pendingMemoryProposals: number`,
  `lastMemoryUpdate: {layer, detail} | null` (alongside `wikiNoteCount`).
- Chip auto-dismisses after 30 s or on click.
- `handleDistill`: after `distill(cid)` returns, read
  `memoryProposals(cid)` (new IPC) and set `pendingMemoryProposals`; the
  sidebar badge appears. (The distill response's `memory_proposals` int is a
  non-committed hint; the array always comes from `memory_proposals`.)
- `MessageBubble.tsx`: memory.md gets its own label on the recall chip.
  The label is presence-conditioned (only layers actually in `recall` are
  rendered): render `user.md` iff `recall` includes the `~/.odo/user.md`
  marker, `memory.md` iff it includes `.odo/memory.md`, and the note count
  is `recall.length − (number of fixed markers present)`. Example shapes:
  `memory: user.md + memory.md + 1 note(s)` (both fixed, one note),
  `memory: memory.md + 2 note(s)` (no user.md), `memory: 2 note(s)` (neither
  fixed layer exists).

### 9. Tauri glue

- `gui/src-tauri/src/lib.rs`: bump `DISTILL_READ_TIMEOUT` from 660 s to
  **960 s** (10 min distill + 5 min learner + margin), and update its
  comment accordingly. Add passthrough commands
  `read_memory`, `memory_proposals`, `apply_memory` (same shape as
  `list_wiki`/`read_wiki`).

## Verification commands

```bash
go build ./... && go vet ./... && go test ./... -count=1
cd gui && npx tsc --noEmit && npm run build
cd src-tauri && cargo check
```

## New Go tests (internal/ipc + internal/adapter as needed)

1. `TestLearnerProposesJournaled` — distill writes note + `memory_propose`
   event holds the `memory`/`proposal` arrays with per-target entries; count
   returned in `MemoryProposals`.
2. `TestLearnerVetoesWeakEvidence` — learner output with wrong/missing
   `evidence` and <2 project `user` → dropped, counted in `stats`.
3. `TestApplyMemoryWritesMemoryMD` — apply accepts → `.odo/memory.md`
   contains `- <rule> — cites: main-epoch-1; reaffirmed: 1`; rejected absent.
4. `TestMemoryInjectedIntoPrompt` — memory.md present → prompt contains
   `## Project memory` and `.odo/memory.md` in `recall`; receipt maps.
5. `TestInjectionReceiptHashesFrozen` — frozen sha16 vectors for known
   user.md + memory.md block; absent layers have no receipt entry.
6. `TestMemoryCapRotationArchive` — fill ≥4K, accept overflow rule →
   file ≤4K, archive has under `## … rotated` header, incoming rule retained.
7. `TestMemoryRetractionToArchive` — contradicts matched → moved to
   archive with `retracted:`; no-match → journaled + surfaced.
8. `TestUserMDPromotionCrossProject` — registry 2, learner user proposal
   verified, apply writes `seen:` projects line; refused (error) when would
   overflow 4 KB.
9. `TestReadMemoryGuards` — read_memory returns memory/archive/user
    contents for the bound root; a different `project_root` is rejected
    (resolveProject equality guard); missing files return `""`.
10. `TestApplyMemoryIdempotent` — second apply on same epoch returns
    "already applied", no file change.

GUI E2E (cua-host AX tree, M3 pattern): Demo A/B/C as described; assert
chip appears after `memory_update`, journal `receipt` has the expected
paths/hashes.

## Review

Changes on the agent-prompt path (`buildPrompt` signature), journal
payloads (`receipt`), new IPC + event: fresh-context dual-model review
(GLM-5.2 + K3 audit) before close (per dev principle #4).