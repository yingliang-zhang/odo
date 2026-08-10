package ipc

// M12 (D-todo): the durable plan layer. Agents emit a fenced ```odo-todo
// JSON op array inside their normal agent_text; the daemon parses the block
// mechanically (fixed schema, no content evaluation) at drainRun ingest —
// after the agent_text event's AppendEvent succeeds — and merges the ops
// into the journaled todo state. The daemon remains the sole writer of
// every layer (ADR-0003 inv 1, amended 2026-08-10); todo content is a
// RECORD (plan state), never a RULE.
//
// Storage is journal-only: one review_action{action:"todo_merge"} event per
// merge carrying the FULL snapshot of live items —
//
//   {"action":"todo_merge","origin":"agent|user",
//    "ops_applied":int,"ops_rejected":[{"op","id","reason"}],
//    "snapshot":[{"id":"t1","text":"…","status":"open|done|struck",
//                 "origin_seq":int,"updated_seq":int}],
//    "snapshot_sha":"…"}
//
// — so every audit point is exact and boot re-materializes with one journal
// scan (todo.todoStateFromEvents). NO table, NO derived file (D-todo.2).
//
// Snapshot contract (the cap behavior, recorded here because the spec names
// ~4KB and leaves the overflow rule to implementation):
//   - The snapshot lists LIVE items only: every open item, plus done/struck
//     items not yet swept (still visible this epoch). Swept items are
//     omitted — they remain recoverable in earlier todo_merge snapshots
//     forever (inv 3: retraction-with-record, never deletion).
//   - When the rendered snapshot would exceed todoSnapshotCap (4KB), the
//     OLDEST NON-OPEN entries are omitted first until it fits; dropped
//     entries stay journaled in older snapshots.
//   - Open items are never size-dropped — they are the layer's working
//     truth. The pathological all-open overflow (30 items × 240B text) is
//     bounded by todoOpenCap/todoTextCap (~10KB) and journaled complete.
//
// Sweep + staleness are DERIVED at read time, never stored (one source of
// truth for both): a done/struck item leaves the default view once a fold
// boundary passes its updated_seq; an open item untouched through ≥3 folds
// renders with the `~` stale marker but is never auto-struck.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

const (
	// todoBlockCap bounds the injected "## Current plan" block (prompt
	// registry row "todo" — 1.5KB, not prefs-clamped, mirroring the
	// replay/resume layering around it).
	todoBlockCap = 1536
	// todoOpenCap bounds open items per conversation; excess adds are
	// rejected ("cap").
	todoOpenCap = 30
	// todoTextCap bounds one item's single-line text (bytes).
	todoTextCap = 240
	// todoSnapshotCap bounds the rendered journaled snapshot (~4KB; the
	// overflow rule lives in the file header).
	todoSnapshotCap = 4 * 1024
	// todoOpsMax bounds ops consumed from one agent_text's blocks; ops
	// beyond it are rejected ("op_cap") so a 1000-op adversarial block is
	// bounded (parsing itself is linear and cheap).
	todoOpsMax = 100
	// todoRejectsMax bounds journaled per-op reject entries; beyond it a
	// rejects_truncated count fields the rest (an adversarial block must
	// not bloat the journal).
	todoRejectsMax = 50
	// todoRejectReasonCap bounds one journaled reject reason (bytes);
	// todoRejectIDCap bounds the echoed id. parse_error strings embed
	// agent-controlled text (unknown op names, decoder echoes) and op ids
	// echo verbatim — a hostile block must not re-journal its own bulk
	// through the reject channel.
	todoRejectReasonCap = 200
	todoRejectIDCap     = 64
	// todoStaleFolds is the untouched-fold count that marks an open item
	// stale (synthetic `~` marker; never auto-struck).
	todoStaleFolds = 3
)

// Todo item statuses (journaled in snapshot.status).
const (
	todoStatusOpen   = "open"
	todoStatusDone   = "done"
	todoStatusStruck = "struck"
)

// Op names. add/done/strike/reword are the agent's set (the fenced block);
// reopen is user-only (the GUI checkbox unchecking a done item) — an agent
// block containing it zero-applies as an unknown op.
const (
	todoOpAdd    = "add"
	todoOpDone   = "done"
	todoOpStrike = "strike"
	todoOpReword = "reword"
	todoOpReopen = "reopen"
)

// todoEntry is one journaled snapshot element. Field order is part of the
// snapshot_sha contract (Go marshals struct fields in declaration order).
type todoEntry struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	Status     string `json:"status"` // open | done | struck
	OriginSeq  int    `json:"origin_seq"`
	UpdatedSeq int    `json:"updated_seq"`
}

// TodoViewItem is one todo entry plus its derived sweep/stale flags — the
// read-time view every consumer (injection, GUI derivation, `odo todo`,
// distill seeding) shares. Exported for the CLI (cmd_todo.go reuses the
// daemon's derivation verbatim, like FoldWindow).
type TodoViewItem struct {
	todoEntry
	Stale bool `json:"stale"` // open and untouched through ≥3 folds
	Swept bool `json:"swept"` // done/struck with a fold boundary past updated_seq
}

// todoReject is one journaled rejected op with its reason.
type todoReject struct {
	Op     string `json:"op,omitempty"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason"`
}

// todoOp is one structurally-validated op from a block (or one user op
// from todo_update).
type todoOp struct {
	Op   string
	ID   string
	Text string
}

// todoState is the folded in-memory state: live items in id order plus the
// next id to assign (monotonic, never reused — the max is taken over ALL
// todo_merge snapshots in the journal, so ids struck and swept away ages
// ago stay burned). The zero value starts ids at t1.
type todoState struct {
	items  []todoEntry
	nextID int
}

// claimID allocates the next daemon id; nextID floors at 1 so the
// zero-value state (unit tests, no prior merge) starts at t1 like the
// journal scan does.
func (st *todoState) claimID() string {
	if st.nextID < 1 {
		st.nextID = 1
	}
	id := fmt.Sprintf("t%d", st.nextID)
	st.nextID++
	return id
}

// findTodoBlocks extracts the contents of every ```odo-todo fenced block in
// agent text in source order. The scan is manual (strings.Index walks, no
// regex): linear in the text, no backtracking, no compile cost. The block
// ends at the next ``` fence line; an unterminated block yields no content
// (the text stays an ordinary event, untouched).
func findTodoBlocks(text string) []string {
	var blocks []string
	rest := text
	for {
		i := strings.Index(rest, "```odo-todo")
		if i < 0 {
			return blocks
		}
		rest = rest[i+len("```odo-todo"):]
		// The tag must end at a newline (or EOF): "```odo-todolist" is not
		// ours. json code fences with a language tag carry it on the same
		// line, so tolerate trailing whitespace before the newline.
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			return blocks
		}
		if tagTail := strings.TrimSpace(rest[:nl]); tagTail != "" {
			continue
		}
		rest = rest[nl+1:]
		// Closing fence: a line that is exactly ``` (whitespace-tolerant).
		end := -1
		at := 0
		for {
			lineEnd := strings.IndexByte(rest[at:], '\n')
			var line string
			if lineEnd < 0 {
				line = rest[at:]
			} else {
				line = rest[at : at+lineEnd]
			}
			if strings.TrimSpace(line) == "```" {
				end = at
				break
			}
			if lineEnd < 0 {
				break // unterminated: no block
			}
			at += lineEnd + 1
		}
		if end < 0 {
			return blocks
		}
		blocks = append(blocks, rest[:end])
		// Resume after the closing fence line.
		closeNl := strings.IndexByte(rest[end:], '\n')
		if closeNl < 0 {
			return blocks
		}
		rest = rest[end+closeNl+1:]
	}
}

// parseTodoBlock decodes one block's JSON op array. Structural soundness is
// block-wide: malformed JSON, a non-array payload, unknown op names,
// missing required fields, or wrong field types zero-apply the whole block
// with parse_error (a block is one opaque unit — the daemon never salvages
// half a malformed block). allowReopen distinguishes the user's IPC set
// (add/done/strike/reopen/reword) from the agent block's set (reopen is an
// unknown op there). Semantic failures (unknown ids, caps, empty text) are
// NOT detected here — they reject per op during the merge.
func parseTodoBlock(raw string, allowReopen bool) ([]todoOp, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &raws); err != nil {
		return nil, fmt.Errorf("parse_error: invalid JSON: %v", err)
	}
	if raws == nil {
		return nil, fmt.Errorf("parse_error: block is not a JSON array")
	}
	ops := make([]todoOp, 0, len(raws))
	for i, rm := range raws {
		var shape struct {
			Op   string `json:"op"`
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		dec := json.NewDecoder(strings.NewReader(string(rm)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&shape); err != nil {
			return nil, fmt.Errorf("parse_error: op %d: bad object: %v", i, err)
		}
		switch shape.Op {
		case todoOpAdd:
			if shape.Text == "" {
				return nil, fmt.Errorf("parse_error: op %d: add requires text", i)
			}
		case todoOpDone, todoOpStrike:
			if shape.ID == "" {
				return nil, fmt.Errorf("parse_error: op %d: %s requires id", i, shape.Op)
			}
		case todoOpReword:
			if shape.ID == "" || shape.Text == "" {
				return nil, fmt.Errorf("parse_error: op %d: reword requires id and text", i)
			}
		case todoOpReopen:
			if !allowReopen {
				return nil, fmt.Errorf("parse_error: op %d: unknown op %q", i, shape.Op)
			}
			if shape.ID == "" {
				return nil, fmt.Errorf("parse_error: op %d: reopen requires id", i)
			}
		default:
			return nil, fmt.Errorf("parse_error: op %d: unknown op %q", i, shape.Op)
		}
		ops = append(ops, todoOp{Op: shape.Op, ID: shape.ID, Text: shape.Text})
	}
	return ops, nil
}

// sanitizeTodoText forces an item text into the layer's single-line shape:
// strip markup-fence chars (an item must never break the block's list
// rendering or open a new fence), drop line breaks, trim. The byte cap is
// enforced by the caller (a rejected op must name the real reason).
func sanitizeTodoText(s string) string {
	s = strings.ReplaceAll(s, "`", "")
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// scanTodoState folds every todo_merge snapshot in the journal into the
// current live state: the LATEST snapshot is the item truth (swept items
// have already dropped out of newer snapshots); the id counter derives
// from the max id seen in ANY snapshot so struck ids are never reused.
func scanTodoState(events []store.Event) todoState {
	st := todoState{nextID: 1}
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action   string      `json:"action"`
			Snapshot []todoEntry `json:"snapshot"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.Action != "todo_merge" {
			continue
		}
		if p.Snapshot != nil {
			st.items = p.Snapshot
		}
		for _, it := range p.Snapshot {
			if n := todoIDNum(it.ID); n >= st.nextID {
				st.nextID = n + 1
			}
		}
	}
	sort.SliceStable(st.items, func(i, j int) bool {
		return todoIDNum(st.items[i].ID) < todoIDNum(st.items[j].ID)
	})
	return st
}

// todoIDNum parses "t<N>" → N (0 when malformed — such an id never
// matches a real item).
func todoIDNum(id string) int {
	if !strings.HasPrefix(id, "t") {
		return 0
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// findItem returns the item index by id, or -1.
func (st *todoState) findItem(id string) int {
	for i := range st.items {
		if st.items[i].ID == id {
			return i
		}
	}
	return -1
}

func (st *todoState) openCount() int {
	n := 0
	for _, it := range st.items {
		if it.Status == todoStatusOpen {
			n++
		}
	}
	return n
}

// applyTodoOps applies validated ops in order, journal-rejecting semantic
// failures with reasons. seq is the journal seq the merge attributes
// updates to (the carrying agent_text for agent merges; the merge event's
// own seq for user merges). Consumption is bounded at todoOpsMax; the
// overflow rejects as "op_cap".
func (st *todoState) applyTodoOps(ops []todoOp, seq int, rejects *[]todoReject) (applied int) {
	for i, op := range ops {
		if i >= todoOpsMax {
			*rejects = append(*rejects, todoReject{Op: op.Op, ID: op.ID, Reason: "op_cap"})
			continue
		}
		switch op.Op {
		case todoOpAdd:
			text := sanitizeTodoText(op.Text)
			switch {
			case text == "":
				*rejects = append(*rejects, todoReject{Op: op.Op, Reason: "empty_text"})
			case len(text) > todoTextCap:
				*rejects = append(*rejects, todoReject{Op: op.Op, Reason: "text_too_long"})
			default:
				// Duplicate-normalized add → reaffirm the matching OPEN item
				// (bump updated_seq, no new id) — the cheap recency signal.
				norm := normalizeRule(text)
				dup := -1
				for k := range st.items {
					if st.items[k].Status == todoStatusOpen && normalizeRule(st.items[k].Text) == norm {
						dup = k
						break
					}
				}
				if dup >= 0 {
					st.items[dup].UpdatedSeq = seq
					applied++
					continue
				}
				if st.openCount() >= todoOpenCap {
					*rejects = append(*rejects, todoReject{Op: op.Op, Reason: "cap"})
					continue
				}
				st.items = append(st.items, todoEntry{
					ID:         st.claimID(),
					Text:       text,
					Status:     todoStatusOpen,
					OriginSeq:  seq,
					UpdatedSeq: seq,
				})
				applied++
			}
		case todoOpDone, todoOpStrike:
			i := st.findItem(op.ID)
			if i < 0 {
				*rejects = append(*rejects, todoReject{Op: op.Op, ID: op.ID, Reason: "unknown_id"})
				continue
			}
			if st.items[i].Status != todoStatusOpen {
				*rejects = append(*rejects, todoReject{Op: op.Op, ID: op.ID, Reason: "not_open"})
				continue
			}
			if op.Op == todoOpDone {
				st.items[i].Status = todoStatusDone
			} else {
				st.items[i].Status = todoStatusStruck
			}
			st.items[i].UpdatedSeq = seq
			applied++
		case todoOpReopen:
			i := st.findItem(op.ID)
			if i < 0 {
				*rejects = append(*rejects, todoReject{Op: op.Op, ID: op.ID, Reason: "unknown_id"})
				continue
			}
			if st.items[i].Status == todoStatusOpen {
				*rejects = append(*rejects, todoReject{Op: op.Op, ID: op.ID, Reason: "already_open"})
				continue
			}
			if st.openCount() >= todoOpenCap {
				*rejects = append(*rejects, todoReject{Op: op.Op, ID: op.ID, Reason: "cap"})
				continue
			}
			st.items[i].Status = todoStatusOpen
			st.items[i].UpdatedSeq = seq
			applied++
		case todoOpReword:
			text := sanitizeTodoText(op.Text)
			i := st.findItem(op.ID)
			if i < 0 {
				*rejects = append(*rejects, todoReject{Op: op.Op, ID: op.ID, Reason: "unknown_id"})
				continue
			}
			if st.items[i].Status != todoStatusOpen {
				*rejects = append(*rejects, todoReject{Op: op.Op, ID: op.ID, Reason: "not_open"})
				continue
			}
			switch {
			case text == "":
				*rejects = append(*rejects, todoReject{Op: op.Op, Reason: "empty_text"})
			case len(text) > todoTextCap:
				*rejects = append(*rejects, todoReject{Op: op.Op, Reason: "text_too_long"})
			default:
				st.items[i].Text = text
				st.items[i].UpdatedSeq = seq
				applied++
			}
		}
	}
	return applied
}

// distillMarkerSeqs returns the seqs of all fold markers (review_action
// distill rows) in ascending order.
func distillMarkerSeqs(events []store.Event) []int {
	var seqs []int
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Action == "distill" {
			seqs = append(seqs, ev.Seq)
		}
	}
	return seqs
}

// TodoStateFromEvents folds the journal into the derived todo view
// consumers share: live items from the latest todo_merge snapshot, swept
// flagged by the fold boundary (done/struck items with updated_seq at or
// before it — they leave injection and the default render; older journal
// snapshots keep them), stale flagged on open items untouched through
// ≥ todoStaleFolds folds (the `~` marker; never auto-struck). Exported:
// `odo todo` renders exactly this (journal-only, read-only).
func TodoStateFromEvents(events []store.Event) []TodoViewItem {
	st := scanTodoState(events)
	boundary := foldBoundary(events)
	markers := distillMarkerSeqs(events)
	views := make([]TodoViewItem, 0, len(st.items))
	for _, it := range st.items {
		v := TodoViewItem{todoEntry: it}
		if it.Status != todoStatusOpen && boundary > 0 && it.UpdatedSeq <= boundary {
			v.Swept = true
		}
		if it.Status == todoStatusOpen && !v.Swept {
			folds := 0
			for _, ms := range markers {
				if ms > it.UpdatedSeq {
					folds++
				}
			}
			if folds >= todoStaleFolds {
				v.Stale = true
			}
		}
		views = append(views, v)
	}
	return views
}

// visibleTodoItems filters the derived view to the default render set
// (injection, GUI, CLI): open items first (id order), then done/struck
// items still inside the current epoch. Swept items are the journal's
// record, not the working view.
func visibleTodoItems(views []TodoViewItem) []TodoViewItem {
	open := make([]TodoViewItem, 0, len(views))
	done := make([]TodoViewItem, 0, len(views))
	for _, v := range views {
		if v.Swept {
			continue
		}
		if v.Status == todoStatusOpen {
			open = append(open, v)
		} else {
			done = append(done, v)
		}
	}
	return append(open, done...)
}

// renderTodoBlock renders the injected "## Current plan" section: every
// visible item, open first, marked [ ] open / [~] stale / [x] done /
// [-] struck (struck and done both stay labeled — a struck item is a
// retraction with record, not a completion). The block is byte-capped at
// todoBlockCap with whole-item cut and a count-named omission marker
// (the replay omission-marker convention) pointing at `odo todo`. "" when
// there is nothing honest to show (no open items and no done/struck items
// inside the epoch) — empty renders neither block nor receipt entry.
func renderTodoBlock(views []TodoViewItem) string {
	visible := visibleTodoItems(views)
	if len(visible) == 0 {
		return ""
	}
	header := "## Current plan (todo — journal-backed, durable across folds)\n\n"
	lines := make([]string, len(visible))
	for i, v := range visible {
		lines[i] = renderTodoLine(v) + "\n"
	}
	marker := func(omitted int) string {
		return fmt.Sprintf("\n_… %d more item(s) omitted by the 1.5KB cap — pull the full plan with `odo todo`._", omitted)
	}
	// Whole-item cut: emit the maximal prefix whose total INCLUDING the
	// omission marker stays inside todoBlockCap (the budget registry bills
	// the layer at exactly 1.5KB — the marker may not ride past it).
	emitted := len(lines)
	for emitted > 0 {
		size := len(header)
		for _, l := range lines[:emitted] {
			size += len(l)
		}
		if size <= todoBlockCap && (emitted == len(lines) || size+len(marker(len(lines)-emitted)) <= todoBlockCap) {
			break
		}
		emitted--
	}
	var b strings.Builder
	b.WriteString(header)
	for _, l := range lines[:emitted] {
		b.WriteString(l)
	}
	if omitted := len(lines) - emitted; omitted > 0 {
		b.WriteString(marker(omitted))
	}
	return b.String()
}

// renderTodoLine renders one item for the injected block.
func renderTodoLine(v TodoViewItem) string {
	switch v.Status {
	case todoStatusOpen:
		if v.Stale {
			return fmt.Sprintf("- [~] %s: %s (stale)", v.ID, v.Text)
		}
		return fmt.Sprintf("- [ ] %s: %s", v.ID, v.Text)
	case todoStatusDone:
		return fmt.Sprintf("- [x] %s: %s (done this epoch)", v.ID, v.Text)
	default: // struck
		return fmt.Sprintf("- [-] %s: %s (struck this epoch)", v.ID, v.Text)
	}
}

// distillTodoSeed renders the surviving OPEN items into the distill prompt
// as labeled authoritative state (D-todo.4): the note's Open loops section
// is seeded from truth — the distiller can't drop a loop it was explicitly
// handed. "" when nothing is open.
func distillTodoSeed(events []store.Event) string {
	views := TodoStateFromEvents(events)
	var open []TodoViewItem
	for _, v := range views {
		if v.Status == todoStatusOpen {
			open = append(open, v)
		}
	}
	if len(open) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Durable plan items (authoritative; seed Open loops)\n\n")
	b.WriteString("These open plan items are journaled todo state (durable across folds). Treat them as authoritative for the note's Open loops section.\n\n")
	for _, v := range open {
		if v.Stale {
			fmt.Fprintf(&b, "- %s: %s (stale)\n", v.ID, v.Text)
		} else {
			fmt.Fprintf(&b, "- %s: %s\n", v.ID, v.Text)
		}
	}
	return b.String()
}

// snapshotForJournal renders the post-merge live state into the journaled
// snapshot, applying the file-header cap rule: all open items; unswept
// done/struck entries newest-first for what fits inside todoSnapshotCap
// (oldest non-open omitted first). Reflection of sweep OUT of the record:
// entries whose updated_seq is already behind the fold boundary at merge
// time are omitted here — the journal's earlier snapshots keep them.
func snapshotForJournal(items []todoEntry, boundary int) []todoEntry {
	open := make([]todoEntry, 0, len(items))
	closed := make([]todoEntry, 0, len(items))
	for _, it := range items {
		if it.Status == todoStatusOpen {
			open = append(open, it)
		} else if boundary == 0 || it.UpdatedSeq > boundary {
			closed = append(closed, it)
		}
	}
	// closed is id-ordered (creation order); newest-first = reverse id order.
	out := make([]todoEntry, 0, len(open)+len(closed))
	out = append(out, open...)
	fit := func(entries []todoEntry) int {
		b, _ := json.Marshal(entries)
		return len(b)
	}
	for i := len(closed) - 1; i >= 0; i-- {
		out = append(out, closed[i])
		if fit(out) > todoSnapshotCap {
			// Drop the entry that overflowed (the OLDEST non-open candidate
			// is always the one just appended in this newest-first walk when
			// texts are size-bounded; re-check keeps arbitrary mixes exact).
			out = out[:len(out)-1]
			break
		}
	}
	// Restore stable render order: open first, then closed in id order.
	closedOut := out[len(open):]
	sort.SliceStable(closedOut, func(i, j int) bool {
		return todoIDNum(closedOut[i].ID) < todoIDNum(closedOut[j].ID)
	})
	return out
}

// capTodoRejectField truncates a journaled reject field to at most cap
// bytes total, marking the cut with an ellipsis (rune-boundary safe).
func capTodoRejectField(s string, cap int) string {
	if len(s) <= cap {
		return s
	}
	body := cap - len("…")
	for body > 0 && (s[body]&0xC0) == 0x80 {
		body-- // don't split a UTF-8 continuation byte
	}
	return s[:body] + "…"
}

// mergeTodoOps runs one merge: load the journaled state, apply ops,
// journal the full new snapshot. Returns the journaled event.
func (s *Server) mergeTodoOps(ctx context.Context, conversationID int64, origin string, ops []todoOp, blockErrs []error, seq int) (store.Event, error) {
	events, err := s.store.ListEvents(ctx, conversationID, 0)
	if err != nil {
		return store.Event{}, err
	}
	return s.journalTodoMerge(ctx, conversationID, origin, ops, blockErrs, seq, events)
}

// journalTodoMerge is the merge tail with the event list supplied by the
// caller (handleTodoUpdate lists under s.mu so the precomputed seq cannot
// go stale before the append). seq is the seq updates attribute to (agent
// merge: the carrying agent_text; user merge: the merge event's own
// precomputed seq); origin goes verbatim into the journaled payload.
//
// A merge journals even when every op rejected (ops_applied: 0 with named
// reasons) — the WHOLE-BLOCK parse_error case included: intake is never
// silent. Journaled reject entries are capped at todoRejectsMax plus a
// rejects_truncated count, so an adversarial block is bounded (the batch's
// "1000-op block" rule).
func (s *Server) journalTodoMerge(ctx context.Context, conversationID int64, origin string, ops []todoOp, blockErrs []error, seq int, events []store.Event) (store.Event, error) {
	st := scanTodoState(events)
	var rejects []todoReject
	for _, berr := range blockErrs {
		// Structural failures zero-apply their block; each bad block lands
		// one journaled reject naming the parse reason.
		rejects = append(rejects, todoReject{Reason: berr.Error()})
	}
	applied := st.applyTodoOps(ops, seq, &rejects)

	// Byte-cap every journaled reject field BEFORE the count cap: reasons
	// embed decoder echoes / unknown op names and ids echo verbatim (all
	// agent-controlled) — the reject channel must stay bulk-proof.
	for i := range rejects {
		rejects[i].Reason = capTodoRejectField(rejects[i].Reason, todoRejectReasonCap)
		rejects[i].ID = capTodoRejectField(rejects[i].ID, todoRejectIDCap)
	}

	truncated := 0
	if len(rejects) > todoRejectsMax {
		truncated = len(rejects) - todoRejectsMax
		rejects = rejects[:todoRejectsMax]
	}
	boundary := foldBoundary(events)
	snapshot := snapshotForJournal(st.items, boundary)
	snapJSON, _ := json.Marshal(snapshot)
	payload := map[string]interface{}{
		"action":       "todo_merge",
		"origin":       origin,
		"ops_applied":  applied,
		"ops_rejected": rejects,
		"snapshot":     snapshot,
		"snapshot_sha": sha16(snapJSON),
	}
	if truncated > 0 {
		payload["rejects_truncated"] = truncated
	}
	return s.store.AppendEvent(ctx, conversationID, store.EventReviewAction, mustJSON(payload))
}

// mergeAgentTodo is the drainRun ingest hook: scan the just-journaled
// agent_text for odo-todo blocks, parse each mechanically, and journal one
// todo_merge per agent_text that carried at least one block. The agent_text
// event is never modified. Failures are logged, never returned — a broken
// todo merge must never wedge the run drain.
func (s *Server) mergeAgentTodo(ctx context.Context, conversationID int64, ev store.Event) {
	var p struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(ev.Payload, &p) != nil || p.Text == "" {
		return
	}
	blocks := findTodoBlocks(p.Text)
	if len(blocks) == 0 {
		return
	}
	var ops []todoOp
	var blockErrs []error
	for _, raw := range blocks {
		bops, err := parseTodoBlock(raw, false)
		if err != nil {
			blockErrs = append(blockErrs, err)
			continue
		}
		ops = append(ops, bops...)
	}
	if _, err := s.mergeTodoOps(ctx, conversationID, "agent", ops, blockErrs, ev.Seq); err != nil {
		log.Printf("ipc: todo merge for conversation %d (agent_text seq %d): %v", conversationID, ev.Seq, err)
	}
}

// handleTodoUpdate implements the GUI's "Plan" popover: one user op
// (add/done/strike/reopen/reword) journaled exactly like an agent merge
// with origin:"user". The daemon assigns ids; the GUI references journaled
// ids verbatim. Caller-visible errors are contract violations (bad action,
// missing fields); semantic rejects journal on the merge (unknown id, cap,
// …) and the op lands as a journaled reject, not an IPC error.
func (s *Server) handleTodoUpdate(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	// Structural validation mirrors the agent block's shape so a malformed
	// GUI op zero-applies instead of journaling nonsense. Structural errors
	// are IPC errors (the GUI is daemon-trusted input, unlike agent text).
	rawOp := map[string]string{"op": req.Action}
	if req.TodoID != "" {
		rawOp["id"] = req.TodoID
	}
	if req.Text != "" {
		rawOp["text"] = req.Text
	}
	rawJSON, _ := json.Marshal([]map[string]string{rawOp})
	ops, err := parseTodoBlock(string(rawJSON), true)
	if err != nil {
		return Response{}, fmt.Errorf("todo_update: %v", err)
	}

	// Held for the whole merge: the update's attributed seq is the merge
	// event's own seq, computed as next-after-latest — under s.mu this is
	// stable relative to every other mu-holding writer (send/steer/drain).
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return Response{}, err
	}
	next := 1
	if len(events) > 0 {
		next = events[len(events)-1].Seq + 1
	}
	ev, err := s.journalTodoMerge(ctx, c.ID, "user", ops, nil, next, events)
	if err != nil {
		return Response{}, err
	}
	return Response{Event: &ev}, nil
}
