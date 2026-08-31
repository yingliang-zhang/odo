package ipc

// M12 (D-todo): the durable plan layer's test battery. Pure parse/merge/
// derivation units first, then journal-level merge receipts, then the
// integration surfaces: drainRun ingest, prompt injection + receipt,
// fold-boundary sweep vs FoldWindow arithmetic, staleness, distill-prompt
// seeding, the user todo_update IPC, and the adversarial-volume bound.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// ---------------------------------------------------------------- scanner

func TestFindTodoBlocks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"zero blocks", "plain agent prose with no fences", nil},
		{"one block", "prose\n```odo-todo\n[{\"op\":\"add\"}]\n```\nmore prose", []string{"[{\"op\":\"add\"}]\n"}},
		{"two blocks", "```odo-todo\n[{\"op\":\"add\"}]\n```\nmiddle\n```odo-todo\n[{\"op\":\"done\"}]\n```\ntail",
			[]string{"[{\"op\":\"add\"}]\n", "[{\"op\":\"done\"}]\n"}},
		{"unterminated", "```odo-todo\n[{\"op\":\"add\"}]", nil},
		{"other fence untouched", "```json\n[{\"op\":\"add\"}]\n```", nil},
		{"tag suffix not ours", "```odo-todoextra\n[{\"op\":\"add\"}]\n```", nil},
		{"trailing ws on tag line", "```odo-todo  \n[{\"op\":\"add\"}]\n```", []string{"[{\"op\":\"add\"}]\n"}},
		{"closing fence tolerant", "```odo-todo\n[]\n```  ", []string{"[]\n"}},
		{"fences inside text kept", "```odo-todo\nnote ``` inside\n```", []string{"note ``` inside\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findTodoBlocks(tc.text)
			if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", tc.want) {
				t.Errorf("findTodoBlocks = %q, want %q", got, tc.want)
			}
		})
	}
}

// ------------------------------------------------------------------ parser

func TestParseTodoBlock(t *testing.T) {
	t.Parallel()
	valid := `[{"op":"add","text":"x"},{"op":"done","id":"t1"},{"op":"strike","id":"t2"},{"op":"reword","id":"t3","text":"y"}]`
	if ops, err := parseTodoBlock(valid, false); err != nil || len(ops) != 4 {
		t.Fatalf("valid block: ops=%v err=%v", ops, err)
	}
	reopen := `[{"op":"reopen","id":"t1"}]`
	if _, err := parseTodoBlock(reopen, false); err == nil || !strings.Contains(err.Error(), "unknown op") {
		t.Errorf("reopen in an agent block: err=%v, want unknown-op parse_error", err)
	}
	if ops, err := parseTodoBlock(reopen, true); err != nil || len(ops) != 1 || ops[0].Op != todoOpReopen {
		t.Errorf("reopen for the user set: ops=%v err=%v", ops, err)
	}
	bad := []struct {
		name string
		raw  string
	}{
		{"malformed json", `[{"op":"add",`},
		{"non-array object", `{"op":"add","text":"x"}`},
		{"non-array literal", `42`},
		{"null", `null`},
		{"unknown op", `[{"op":"explode"}]`},
		{"add missing text", `[{"op":"add"}]`},
		{"done missing id", `[{"op":"done"}]`},
		{"reword missing text", `[{"op":"reword","id":"t1"}]`},
		{"unknown field", `[{"op":"add","text":"x","evil":true}]`},
		{"wrong field type", `[{"op":"add","text":5}]`},
		{"non-object element", `["add"]`},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseTodoBlock(tc.raw, false); err == nil || !strings.HasPrefix(err.Error(), "parse_error") {
				t.Errorf("%s: err=%v, want parse_error", tc.name, err)
			}
		})
	}
}

// FuzzTodoBlockParse: the parser must never panic and must either decode a
// structurally-valid op array or return a parse_error — never both, never
// hang (the batch's parser-fuzz requirement; runs over seeds + corpus in
// normal test runs).
func FuzzTodoBlockParse(f *testing.F) {
	for _, s := range []string{
		`[]`, `{}`, `null`, `[`, `[{"op":"add","text":"a"}]`, `["x"]`,
		`[{"op":1}]`, `[{"op":"done","id":"t1"}]`, `[[{"op":"add"}]]`,
		`[{"op":"reword","id":"t1","text":"x","extra":""}]`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ops, err := parseTodoBlock(s, false)
		if err != nil && !strings.HasPrefix(err.Error(), "parse_error") {
			t.Fatalf("non-parse error %v for %q", err, s)
		}
		if err == nil {
			for _, op := range ops {
				if op.Op == "" {
					t.Fatalf("empty op name accepted from %q", s)
				}
			}
		}
	})
}

// ------------------------------------------------------- merge op semantics

func TestTodoOpSemantics(t *testing.T) {
	t.Parallel()
	var st todoState
	var rejects []todoReject
	apply := func(ops []todoOp, seq int) int {
		return st.applyTodoOps(ops, seq, &rejects)
	}

	if n := apply([]todoOp{{Op: todoOpAdd, Text: "first item"}}, 10); n != 1 || st.items[0].ID != "t1" || st.items[0].OriginSeq != 10 {
		t.Fatalf("add: items=%+v applied=%d", st.items, n)
	}
	if n := apply([]todoOp{{Op: todoOpReword, ID: "t1", Text: "renamed"}}, 11); n != 1 || st.items[0].Text != "renamed" || st.items[0].UpdatedSeq != 11 || st.items[0].OriginSeq != 10 {
		t.Fatalf("reword: item=%+v (origin_seq must not move)", st.items[0])
	}
	if n := apply([]todoOp{{Op: todoOpAdd, Text: "second"}}, 12); n != 1 || st.items[1].ID != "t2" {
		t.Fatalf("second add: items=%+v", st.items)
	}
	if n := apply([]todoOp{{Op: todoOpDone, ID: "t2"}}, 13); n != 1 || st.items[1].Status != todoStatusDone || st.items[1].UpdatedSeq != 13 {
		t.Fatalf("done: item=%+v", st.items[1])
	}
	// done on a non-open id rejects, strike on the still-open one works.
	if n := apply([]todoOp{{Op: todoOpDone, ID: "t2"}}, 14); n != 0 {
		t.Fatalf("double done applied %d", n)
	}
	if n := apply([]todoOp{{Op: todoOpStrike, ID: "t1"}}, 15); n != 1 || st.items[0].Status != todoStatusStruck {
		t.Fatalf("strike: item=%+v", st.items[0])
	}
	// Unknown ids and a reopen round-trip.
	if n := apply([]todoOp{{Op: todoOpDone, ID: "t99"}, {Op: todoOpReopen, ID: "t9x"}}, 16); n != 0 {
		t.Fatalf("unknown ids applied %d", n)
	}
	if n := apply([]todoOp{{Op: todoOpReopen, ID: "t2"}}, 17); n != 1 || st.items[1].Status != todoStatusOpen {
		t.Fatalf("reopen: item=%+v", st.items[1])
	}
	if n := apply([]todoOp{{Op: todoOpReopen, ID: "t2"}}, 18); n != 0 {
		t.Fatalf("reopen on open applied %d", n)
	}
	var reasons []string
	for _, r := range rejects {
		reasons = append(reasons, r.Reason)
	}
	want := []string{"not_open", "unknown_id", "unknown_id", "already_open"}
	if fmt.Sprint(reasons) != fmt.Sprint(want) {
		t.Errorf("reject reasons = %v, want %v", reasons, want)
	}
}

func TestTodoTextGuards(t *testing.T) {
	t.Parallel()
	var st todoState
	var rejects []todoReject
	ops := []todoOp{
		{Op: todoOpAdd, Text: "  \n\t  "},                         // sanitizes to empty
		{Op: todoOpAdd, Text: strings.Repeat("x", todoTextCap+1)}, // too long
		{Op: todoOpAdd, Text: "with `backticks` and\nnewline"},    // fence chars stripped
		{Op: todoOpAdd, Text: strings.Repeat("x", todoTextCap)},   // at the cap: allowed
	}
	applied := st.applyTodoOps(ops, 5, &rejects)
	if applied != 2 {
		t.Fatalf("applied = %d, want 2 (fence-stripped + at-cap)", applied)
	}
	if st.items[0].Text != "with backticks and newline" {
		t.Errorf("sanitized text = %q, want backticks/newline gone", st.items[0].Text)
	}
	if len(st.items[1].Text) != todoTextCap {
		t.Errorf("at-cap text len = %d, want %d", len(st.items[1].Text), todoTextCap)
	}
	if len(rejects) != 2 || rejects[0].Reason != "empty_text" || rejects[1].Reason != "text_too_long" {
		t.Errorf("rejects = %+v", rejects)
	}
}

func TestTodoDuplicateReaffirm(t *testing.T) {
	t.Parallel()
	var st todoState
	var rejects []todoReject
	st.applyTodoOps([]todoOp{{Op: todoOpAdd, Text: "Write the migration plan"}}, 20, &rejects)
	if n := st.applyTodoOps([]todoOp{{Op: todoOpAdd, Text: "  write the  MIGRATION plan  "}}, 25, &rejects); n != 1 {
		t.Fatalf("reaffirm applied = %d, want 1", n)
	}
	if len(st.items) != 1 || st.items[0].ID != "t1" {
		t.Fatalf("duplicate created a new item: %+v", st.items)
	}
	if st.items[0].UpdatedSeq != 25 || st.items[0].OriginSeq != 20 {
		t.Errorf("reaffirm bumps updated_seq only: %+v", st.items[0])
	}
	if len(rejects) != 0 {
		t.Errorf("reaffirm is not a reject: %+v", rejects)
	}
	// But a CLOSED item is not reaffirmed — the add creates a fresh item.
	st.applyTodoOps([]todoOp{{Op: todoOpDone, ID: "t1"}}, 26, &rejects)
	if n := st.applyTodoOps([]todoOp{{Op: todoOpAdd, Text: "write the migration plan"}}, 27, &rejects); n != 1 || len(st.items) != 2 || st.items[1].ID != "t2" {
		t.Errorf("add over a done duplicate: items=%+v applied=%d", st.items, n)
	}
}

func TestTodoIDMonotonicNeverReused(t *testing.T) {
	t.Parallel()
	var st todoState
	var rejects []todoReject
	st.applyTodoOps([]todoOp{{Op: todoOpAdd, Text: "a"}, {Op: todoOpAdd, Text: "b"}}, 10, &rejects)
	st.applyTodoOps([]todoOp{{Op: todoOpStrike, ID: "t1"}}, 11, &rejects)
	st.applyTodoOps([]todoOp{{Op: todoOpAdd, Text: "c"}}, 12, &rejects)
	if st.items[2].ID != "t3" {
		t.Errorf("add after strike = id %s, want t3 (struck ids never reused)", st.items[2].ID)
	}
	// scanTodoState re-derives the counter from journaled snapshots: even
	// with the struck item out of the latest snapshot, t1 stays burned.
	snap := []todoEntry{{ID: "t2", Text: "b", Status: todoStatusOpen, OriginSeq: 10, UpdatedSeq: 10}}
	evs := []store.Event{
		{Seq: 1, Type: store.EventReviewAction, Payload: json.RawMessage(`{"action":"todo_merge","snapshot":[{"id":"t1","text":"a","status":"struck","origin_seq":10,"updated_seq":11}]}`)},
	}
	b2, _ := json.Marshal(map[string]interface{}{"action": "todo_merge", "snapshot": snap})
	evs = append(evs, store.Event{Seq: 2, Type: store.EventReviewAction, Payload: b2})
	st2 := scanTodoState(evs)
	var rj []todoReject
	st2.applyTodoOps([]todoOp{{Op: todoOpAdd, Text: "d"}}, 13, &rj)
	if st2.items[1].ID != "t3" {
		t.Errorf("re-derided counter reuses burned ids: new id = %s, want t3", st2.items[1].ID)
	}
}

func TestTodoOpenCap(t *testing.T) {
	t.Parallel()
	var st todoState
	var rejects []todoReject
	ops := make([]todoOp, 0, todoOpenCap+5)
	for i := 0; i < todoOpenCap+5; i++ {
		ops = append(ops, todoOp{Op: todoOpAdd, Text: fmt.Sprintf("item %d", i)})
	}
	applied := st.applyTodoOps(ops, 10, &rejects)
	if applied != todoOpenCap {
		t.Errorf("applied = %d, want the open cap %d", applied, todoOpenCap)
	}
	if len(rejects) != 5 {
		t.Fatalf("rejects = %d, want 5", len(rejects))
	}
	for _, r := range rejects {
		if r.Reason != "cap" {
			t.Errorf("reject reason = %q, want cap", r.Reason)
		}
	}
}

// --------------------------------------------- derivation (sweep / stale)

// todoEvent builds one event for the derivation fixtures.
func todoEvent(seq int, typ, payload string) store.Event {
	return store.Event{Seq: seq, Type: typ, Payload: json.RawMessage(payload)}
}

func TestTodoSweepAtFoldBoundary(t *testing.T) {
	t.Parallel()
	// Marker at seq 5: items updated at/before it sweep, items after stay.
	snapshot := `{"action":"todo_merge","snapshot":[
		{"id":"t1","text":"still open","status":"open","origin_seq":2,"updated_seq":2},
		{"id":"t2","text":"done in epoch","status":"done","origin_seq":2,"updated_seq":6},
		{"id":"t3","text":"done at boundary","status":"done","origin_seq":2,"updated_seq":5},
		{"id":"t4","text":"struck","status":"struck","origin_seq":2,"updated_seq":6}
	]}`
	events := []store.Event{
		todoEvent(1, store.EventUserMessage, `{"text":"start"}`),
		todoEvent(4, store.EventReviewAction, snapshot),
		todoEvent(5, store.EventReviewAction, `{"action":"distill","epoch":2}`),
		todoEvent(6, store.EventReviewAction, snapshot),
	}
	views := TodoStateFromEvents(events)
	got := map[string]TodoViewItem{}
	for _, v := range views {
		got[v.ID] = v
	}
	if got["t2"].Swept {
		t.Error("t2 (updated past the fold) must stay visible")
	}
	if !got["t3"].Swept {
		t.Error("t3 (updated_seq == boundary) must sweep — boundary is inclusive")
	}
	if got["t4"].Swept {
		t.Error("t4 (updated past the fold) must stay visible")
	}
	// Consistency with FoldWindow/windowEvents: the sweep uses the same
	// marker scout, so a done item swept here is exactly one the fold's
	// window retired (seq ≤ foldBoundary).
	if b := foldBoundary(events); b != 5 {
		t.Fatalf("foldBoundary = %d, want 5", b)
	}
	visible := visibleTodoItems(views)
	ids := make([]string, 0, len(visible))
	for _, v := range visible {
		ids = append(ids, v.ID)
	}
	if fmt.Sprint(ids) != "[t1 t2 t4]" {
		t.Errorf("visible ids = %v, want [t1 t2 t4] (open first, then done/struck)", ids)
	}
}

func TestTodoStaleAfterThreeFolds(t *testing.T) {
	t.Parallel()
	snapshot := `{"action":"todo_merge","snapshot":[
		{"id":"t1","text":"aging item","status":"open","origin_seq":2,"updated_seq":2},
		{"id":"t2","text":"fresh item","status":"open","origin_seq":6,"updated_seq":6}
	]}`
	mk := func(markerSeqs ...int) []store.Event {
		evs := []store.Event{todoEvent(4, store.EventReviewAction, snapshot)}
		for i, s := range markerSeqs {
			evs = append(evs, todoEvent(s, store.EventReviewAction, fmt.Sprintf(`{"action":"distill","epoch":%d}`, i+2)))
		}
		return evs
	}
	byID := func(events []store.Event) map[string]TodoViewItem {
		out := map[string]TodoViewItem{}
		for _, v := range TodoStateFromEvents(events) {
			out[v.ID] = v
		}
		return out
	}
	g := byID(mk(3, 4))
	if g["t1"].Stale {
		t.Error("two folds past updated_seq: not stale yet")
	}
	g = byID(mk(3, 4, 5))
	if !g["t1"].Stale {
		t.Error("three folds past updated_seq: want stale")
	}
	if g["t2"].Stale {
		t.Error("fresh item (only two trailing folds) must not read stale")
	}
	// A reaffirm (updated_seq bump) de-stales the same item.
	snapshot2 := `{"action":"todo_merge","snapshot":[
		{"id":"t1","text":"aging item","status":"open","origin_seq":2,"updated_seq":6},
		{"id":"t2","text":"fresh item","status":"open","origin_seq":6,"updated_seq":6}
	]}`
	evs := append(mk(3, 4, 5), todoEvent(6, store.EventReviewAction, snapshot2))
	if byID(evs)["t1"].Stale {
		t.Error("reaffirmed item must de-stale until three NEW folds pass")
	}
}

// ------------------------------------------------------------ render block

func TestRenderTodoBlockOmitsAndCaps(t *testing.T) {
	t.Parallel()
	if got := renderTodoBlock(nil); got != "" {
		t.Errorf("empty state block = %q, want absent", got)
	}
	sweptOnly := []TodoViewItem{{todoEntry: todoEntry{ID: "t1", Text: "old", Status: todoStatusDone, OriginSeq: 1, UpdatedSeq: 1}, Swept: true}}
	if got := renderTodoBlock(sweptOnly); got != "" {
		t.Errorf("swept-only block = %q, want absent", got)
	}
	var views []TodoViewItem
	for i := 1; i <= 20; i++ {
		views = append(views, TodoViewItem{todoEntry: todoEntry{
			ID: fmt.Sprintf("t%d", i), Text: strings.Repeat("x", 100), Status: todoStatusOpen, OriginSeq: 1, UpdatedSeq: 1,
		}})
	}
	views = append(views, TodoViewItem{todoEntry: todoEntry{ID: "t21", Text: "finished", Status: todoStatusDone, OriginSeq: 1, UpdatedSeq: 2}})
	block := renderTodoBlock(views)
	if len(block) > todoBlockCap {
		t.Errorf("block = %d bytes, want ≤ %d INCLUDING the omission marker", len(block), todoBlockCap)
	}
	if !strings.Contains(block, "## Current plan (todo — journal-backed, durable across folds)") {
		t.Error("header missing")
	}
	if !strings.Contains(block, "more item(s) omitted by the 1.5KB cap") || !strings.Contains(block, "`odo todo`") {
		t.Errorf("omission marker missing or wrong: tail = %q", block[len(block)-120:])
	}
	var omitted int
	if _, err := fmt.Sscanf(block[strings.LastIndex(block, "_…"):], "_… %d more item(s)", &omitted); err != nil || omitted < 1 {
		t.Errorf("marker count unparseable: %v (omitted=%d)", err, omitted)
	}
	// Whole-item cut: no partial line — every emitted item line ends the
	// block body section cleanly.
	for _, v := range views[:3] {
		if !strings.Contains(block, "- [ ] "+v.ID+":") {
			t.Errorf("early open item %s cut while later content stayed", v.ID)
		}
	}
	if !strings.Contains(block, "- [x] ") && strings.Contains(block, "t21") {
		t.Error("done item rendered without its [x] mark")
	}
}

func TestRenderTodoLineMarks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		v    TodoViewItem
		want string
	}{
		{TodoViewItem{todoEntry: todoEntry{ID: "t3", Text: "open", Status: todoStatusOpen}}, "- [ ] t3: open"},
		{TodoViewItem{todoEntry: todoEntry{ID: "t1", Text: "old", Status: todoStatusOpen}, Stale: true}, "- [~] t1: old (stale)"},
		{TodoViewItem{todoEntry: todoEntry{ID: "t7", Text: "fin", Status: todoStatusDone}}, "- [x] t7: fin (done this epoch)"},
		{TodoViewItem{todoEntry: todoEntry{ID: "t4", Text: "cut", Status: todoStatusStruck}}, "- [-] t4: cut (struck this epoch)"},
	}
	for _, tc := range cases {
		if got := renderTodoLine(tc.v); got != tc.want {
			t.Errorf("renderTodoLine = %q, want %q", got, tc.want)
		}
	}
}

// Prompt-injection order is a pure buildPrompt property: todo between the
// resume card and the replay (send path only — slash never sees either).
func TestTodoInjectionPosition(t *testing.T) {
	t.Parallel()
	ml := memoryLayers{resume: "RESUME-CARD", todo: "PLAN-BLOCK", replay: "REPLAY-BLOCK"}
	p := buildPrompt("msg", nil, ml)
	iR := strings.Index(p, "RESUME-CARD")
	iT := strings.Index(p, "PLAN-BLOCK")
	iReplay := strings.Index(p, "REPLAY-BLOCK")
	if !(iR >= 0 && iT > iR && iReplay > iT) {
		t.Errorf("order = resume@%d todo@%d replay@%d, want resume < todo < replay", iR, iT, iReplay)
	}
	// Empty todo: no gap appears between resume and replay beyond the rule.
	ml.todo = ""
	p2 := buildPrompt("msg", nil, ml)
	if strings.Contains(p2, "PLAN-BLOCK") {
		t.Error("empty todo must render nothing")
	}
}

// ------------------------------------------------ snapshot cap + journal

func TestSnapshotForJournalCap(t *testing.T) {
	t.Parallel()
	var items []todoEntry
	for i := 1; i <= 12; i++ {
		items = append(items, todoEntry{
			ID: fmt.Sprintf("t%d", i), Text: strings.Repeat("x", 120), Status: todoStatusOpen, OriginSeq: 1, UpdatedSeq: 1,
		})
	}
	for i := 13; i <= 24; i++ {
		items = append(items, todoEntry{
			ID: fmt.Sprintf("t%d", i), Text: strings.Repeat("y", 120), Status: todoStatusDone, OriginSeq: 1, UpdatedSeq: 5,
		})
	}
	snap := snapshotForJournal(items, 0)
	b, _ := json.Marshal(snap)
	if len(b) > todoSnapshotCap {
		t.Errorf("snapshot = %d bytes, want ≤ %d", len(b), todoSnapshotCap)
	}
	opens, dones := 0, 0
	for _, it := range snap {
		if it.Status == todoStatusOpen {
			opens++
		} else {
			dones++
		}
	}
	if opens != 12 {
		t.Errorf("open items = %d, want all 12 (open is never size-dropped)", opens)
	}
	if dones >= 12 || dones == 0 {
		t.Errorf("done kept = %d, want a partial newest-first set", dones)
	}
	// Newest-first survival: the kept done ids are a contiguous tail of the
	// done series (no hole where an older entry displaced a newer one).
	var doneIDs []string
	for _, it := range snap {
		if it.Status != todoStatusOpen {
			doneIDs = append(doneIDs, it.ID)
		}
	}
	if dones > 0 {
		wantTail := make([]string, dones)
		for i := 0; i < dones; i++ {
			wantTail[i] = fmt.Sprintf("t%d", 24-(dones-1-i))
		}
		if fmt.Sprint(doneIDs) != fmt.Sprint(wantTail) {
			t.Errorf("kept done ids = %v, want newest-first tail %v", doneIDs, wantTail)
		}
	}
	// Swept entries never enter the snapshot in the first place.
	items = []todoEntry{
		{ID: "t1", Text: "open", Status: todoStatusOpen, OriginSeq: 1, UpdatedSeq: 1},
		{ID: "t2", Text: "old done", Status: todoStatusDone, OriginSeq: 1, UpdatedSeq: 2},
	}
	snap = snapshotForJournal(items, 5) // boundary past t2's updated_seq
	if len(snap) != 1 || snap[0].ID != "t1" {
		t.Errorf("swept item in snapshot: %+v", snap)
	}
}

// bareServer returns a Server over a fresh in-tmp journal with one
// project/workstream/conversation — for journal-level merge tests that
// don't need the socket rig.
func bareServer(t *testing.T) (*Server, int64) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("ODO_REGISTRY_PATH", filepath.Join(t.TempDir(), "projects.json"))
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(root, "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	p, err := st.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(st, root, nil, nil), c.ID
}

// todoMerges scans a conversation journal for todo_merge payloads in seq order.
func todoMerges(t *testing.T, st *store.Store, convID int64) []map[string]interface{} {
	t.Helper()
	events, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]interface{}
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p map[string]interface{}
		if json.Unmarshal(ev.Payload, &p) == nil && p["action"] == "todo_merge" {
			p["_seq"] = ev.Seq
			out = append(out, p)
		}
	}
	return out
}

func TestTodoMergeJournaledSnapshotAndSHA(t *testing.T) {
	s, convID := bareServer(t)
	ctx := context.Background()
	ops := []todoOp{{Op: todoOpAdd, Text: "journal me"}, {Op: todoOpDone, ID: "t9"}}
	ev, err := s.mergeTodoOps(ctx, convID, "agent", ops, nil, 7)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Action   string       `json:"action"`
		Origin   string       `json:"origin"`
		Applied  int          `json:"ops_applied"`
		Rejected []todoReject `json:"ops_rejected"`
		Snapshot []todoEntry  `json:"snapshot"`
		SHA      string       `json:"snapshot_sha"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Action != "todo_merge" || p.Origin != "agent" {
		t.Errorf("payload identity = %s/%s", p.Action, p.Origin)
	}
	if p.Applied != 1 || len(p.Rejected) != 1 || p.Rejected[0].Reason != "unknown_id" {
		t.Errorf("counts = applied %d rejects %+v", p.Applied, p.Rejected)
	}
	if len(p.Snapshot) != 1 || p.Snapshot[0].Text != "journal me" || p.Snapshot[0].UpdatedSeq != 7 {
		t.Errorf("snapshot = %+v", p.Snapshot)
	}
	snapJSON, _ := json.Marshal(p.Snapshot)
	if p.SHA != sha16(snapJSON) {
		t.Errorf("snapshot_sha = %s, want sha16(snapshot json) = %s", p.SHA, sha16(snapJSON))
	}
}

func TestTodoAdversarialThousandOpsBounded(t *testing.T) {
	s, convID := bareServer(t)
	raw := "["
	for i := 0; i < 1000; i++ {
		if i > 0 {
			raw += ","
		}
		raw += fmt.Sprintf(`{"op":"done","id":"t%d"}`, i+1)
	}
	raw += "]"
	ops, err := parseTodoBlock(raw, false)
	if err != nil || len(ops) != 1000 {
		t.Fatalf("parse 1000 ops: n=%d err=%v", len(ops), err)
	}
	ev, err := s.mergeTodoOps(context.Background(), convID, "agent", ops, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Applied          int          `json:"ops_applied"`
		Rejected         []todoReject `json:"ops_rejected"`
		RejectsTruncated int          `json:"rejects_truncated"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Applied != 0 {
		t.Errorf("applied = %d, want 0", p.Applied)
	}
	if len(p.Rejected) != todoRejectsMax {
		t.Errorf("journaled rejects = %d, want the %d cap (journal bounded)", len(p.Rejected), todoRejectsMax)
	}
	if p.RejectsTruncated != 1000-todoRejectsMax {
		t.Errorf("rejects_truncated = %d, want %d", p.RejectsTruncated, 1000-todoRejectsMax)
	}
	if got := len(ev.Payload); got > 32*1024 {
		t.Errorf("merge event = %d bytes, want bounded well under the journal row comfort zone", got)
	}
}

func TestTodoAdversarialThousandAddsCap(t *testing.T) {
	t.Parallel()
	ops := make([]todoOp, 0, 1000)
	for i := 0; i < 1000; i++ {
		ops = append(ops, todoOp{Op: todoOpAdd, Text: fmt.Sprintf("adversarial %d", i)})
	}
	var rejects []todoReject
	st := scanTodoState(nil)
	applied := st.applyTodoOps(ops, 3, &rejects)
	if applied != todoOpenCap {
		t.Errorf("applied = %d, want the open cap %d", applied, todoOpenCap)
	}
	capRejects, opCapRejects := 0, 0
	for _, r := range rejects {
		switch r.Reason {
		case "cap":
			capRejects++
		case "op_cap":
			opCapRejects++
		}
	}
	// 100 consumed (30 applied + 70 cap), 900 refused at the ops bound.
	if capRejects != todoOpsMax-todoOpenCap || opCapRejects != 1000-todoOpsMax {
		t.Errorf("rejects cap=%d op_cap=%d, want %d/%d", capRejects, opCapRejects, todoOpsMax-todoOpenCap, 1000-todoOpsMax)
	}
}

// --------------------------------------------------------- drain integration

// todoFlowWrapper: agent runs emit an odo-todo block (from $ODO_RUN_TODO,
// empty → plain text), distill one-shots copy their prompt aside (when
// ODO_PROMPT_COPY is set) and note from ODO_DISTILL_OUTPUT.
const todoFlowWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
if grep -q "Summarize the key decisions" "$prompt_file"; then
  [ -n "$ODO_PROMPT_COPY" ] && cp "$prompt_file" "$ODO_PROMPT_COPY"
  cat "$ODO_DISTILL_OUTPUT" > "$output_file"
  exit 0
fi
sleep 1
cp "$prompt_file" hello.txt
{
  printf 'Run complete.\n\n'
  cat "$ODO_RUN_TODO"
  printf '\n'
} > "$output_file"
exit 0
`

func TestTodoDrainRunMergeIntegration(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	runTodo := filepath.Join(t.TempDir(), "run-todo.txt")
	good := "```odo-todo\n[{\"op\":\"add\",\"text\":\"verify the accept loop\"},{\"op\":\"add\",\"text\":\"write gate tests\"}]\n```\n"
	if err := os.WriteFile(runTodo, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODO_RUN_TODO", runTodo)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, todoFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.pollUntilDone(t, convID)

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	textSeq, mergeSeq := -1, -1
	var agentTextPayload string
	for _, ev := range events {
		if ev.Type == store.EventAgentText {
			textSeq, agentTextPayload = ev.Seq, string(ev.Payload)
		}
		if ev.Type == store.EventReviewAction && strings.Contains(string(ev.Payload), `"todo_merge"`) {
			if mergeSeq < 0 {
				mergeSeq = ev.Seq
				if mergeSeq != textSeq+1 {
					t.Errorf("todo_merge seq %d, want immediately after agent_text seq %d", mergeSeq, textSeq)
				}
			}
		}
	}
	if textSeq < 0 || mergeSeq <= textSeq {
		t.Fatalf("agent_text seq %d, todo_merge seq %d", textSeq, mergeSeq)
	}
	// The agent_text itself is never modified — block travels verbatim.
	if !strings.Contains(agentTextPayload, "```odo-todo") {
		t.Error("agent_text was modified — the todo block must travel verbatim")
	}
	merges := todoMerges(t, rig.store, convID)
	if len(merges) != 1 {
		t.Fatalf("todo_merge events = %d, want exactly 1 per block-carrying agent_text", len(merges))
	}
	if merges[0]["origin"] != "agent" || merges[0]["ops_applied"].(float64) != 2 {
		t.Errorf("merge origin/applied = %v/%v", merges[0]["origin"], merges[0]["ops_applied"])
	}
	snap := merges[0]["snapshot"].([]interface{})
	if len(snap) != 2 || snap[0].(map[string]interface{})["id"] != "t1" || snap[1].(map[string]interface{})["id"] != "t2" {
		t.Errorf("snapshot = %v", snap)
	}

	// All-rejected blocks still journal the merge (intake is never silent)
	// — and parse_errors too, without touching the agent_text.
	bad := "```odo-todo\nnot json\n```\n\n```odo-todo\n[{\"op\":\"done\",\"id\":\"t999\"}]\n```\n"
	if err := os.WriteFile(runTodo, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Update hello.txt"})
	rig.pollUntilDone(t, convID)
	merges = todoMerges(t, rig.store, convID)
	if len(merges) != 2 {
		t.Fatalf("merges after the bad run = %d, want 2", len(merges))
	}
	m2 := merges[1]
	if m2["ops_applied"].(float64) != 0 {
		t.Errorf("all-rejected merge applied = %v, want 0", m2["ops_applied"])
	}
	rejects := m2["ops_rejected"].([]interface{})
	if len(rejects) != 2 {
		t.Fatalf("rejects = %v, want parse_error + unknown_id", rejects)
	}
	r0 := rejects[0].(map[string]interface{})
	r1 := rejects[1].(map[string]interface{})
	if !strings.HasPrefix(r0["reason"].(string), "parse_error") {
		t.Errorf("first reject = %v, want parse_error (bad block zero-applied)", r0)
	}
	if r1["reason"] != "unknown_id" {
		t.Errorf("second reject = %v, want unknown_id (good block, bad ref)", r1)
	}
	// The snapshot from the earlier merge survives a zero-apply merge.
	if got := len(m2["snapshot"].([]interface{})); got != 2 {
		t.Errorf("zero-apply snapshot items = %d, want the prior 2 intact", got)
	}
}

// -------------------------------------------------------- injection receipt

func TestTodoInjectionReceiptAndEmptyAbsence(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Empty state: no block, no receipt entry.
	sent0 := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	var p0 struct {
		Text    string            `json:"text"`
		Receipt map[string]string `json:"receipt"`
	}
	if err := json.Unmarshal(sent0.Event.Payload, &p0); err != nil {
		t.Fatal(err)
	}
	if _, ok := p0.Receipt["journal#todo"]; ok {
		t.Error("empty todo state journaled a journal#todo receipt")
	}
	rig.pollUntilDone(t, convID)

	// Add two items + strike a third candidate in one IPC round-trip.
	rig.call(t, Request{Cmd: CmdTodoUpdate, ConversationID: convID, Action: "add", Text: "ship the batch"})
	rig.call(t, Request{Cmd: CmdTodoUpdate, ConversationID: convID, Action: "add", Text: "polish the chip"})
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "continue"})
	var p struct {
		Receipt map[string]string `json:"receipt"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatal(err)
	}
	sha, ok := p.Receipt["journal#todo"]
	if !ok {
		t.Fatalf("receipt = %v, want journal#todo", p.Receipt)
	}
	// Ground truth: the prompt files the adapter handed the agent — the
	// one for THIS send names the user's "continue" text at its tail.
	prompts, err := filepath.Glob(filepath.Join(root, ".odo", "prompts", "*.txt"))
	if err != nil || len(prompts) == 0 {
		t.Fatalf("prompt files = %v, %v", prompts, err)
	}
	var prompt []byte
	for _, f := range prompts {
		b, _ := os.ReadFile(f)
		if strings.HasSuffix(strings.TrimSpace(string(b)), "continue") {
			prompt = b
		}
	}
	if prompt == nil {
		t.Fatal("no prompt file for the receipt send")
	}
	if !strings.Contains(string(prompt), "## Current plan (todo — journal-backed, durable across folds)") {
		t.Error("injected prompt lacks the plan block header")
	}
	for _, want := range []string{"- [ ] t1: ship the batch", "- [ ] t2: polish the chip"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("injected prompt lacks %q", want)
		}
	}
	// The receipt sha covers exactly the injected block.
	block := renderTodoBlock(TodoStateFromEvents(mustListEvents(t, rig.store, convID)))
	if sha16([]byte(block)) != sha {
		t.Errorf("receipt sha %s != sha16(rendered block) %s", sha, sha16([]byte(block)))
	}
	if len(block) == 0 || len(block) > todoBlockCap {
		t.Errorf("block size %d outside (0, %d]", len(block), todoBlockCap)
	}
}

func mustListEvents(t *testing.T, st *store.Store, convID int64) []store.Event {
	t.Helper()
	events, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// -------------------------------------------------- boot re-materialization

func TestTodoBootRematerialization(t *testing.T) {
	s, convID := bareServer(t)
	ctx := context.Background()
	// Seed state via one merge (simulating the pre-restart journal).
	if _, err := s.mergeTodoOps(ctx, convID, "agent", []todoOp{{Op: todoOpAdd, Text: "survives the reboot"}}, nil, 5); err != nil {
		t.Fatal(err)
	}
	// A FRESH Server (the boot re-materialization path): state comes from
	// one journal scan, not from any prior process memory.
	st2 := s.store
	fresh := NewServer(st2, s.projectRoot, nil, nil)
	ml, err := fresh.runMemoryLayers(ctx, "main", convID, "continue", learningCohortLive)
	if err != nil {
		t.Fatalf("fresh runMemoryLayers: %v", err)
	}
	if !strings.Contains(ml.todo, "t1: survives the reboot") {
		t.Errorf("fresh server todo block = %q", ml.todo)
	}
	if _, ok := ml.receipt["journal#todo"]; !ok {
		t.Error("fresh server receipt lacks journal#todo")
	}
}

// ------------------------------------------------------------ distill seed

func TestTodoDistillPromptSeeded(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	runTodo := filepath.Join(t.TempDir(), "run-todo.txt")
	if err := os.WriteFile(runTodo, []byte("```odo-todo\n[{\"op\":\"add\",\"text\":\"fold-proof loop\"},{\"op\":\"add\",\"text\":\"soon done\"}]\n```\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODO_RUN_TODO", runTodo)
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\nDecided things.\n\n## Open loops\n\n- fold-proof loop\n")
	t.Setenv("ODO_LEARNER_OUTPUT", `{"memory":[],"user":[],"reaffirm":[]}`)
	promptCopy := filepath.Join(t.TempDir(), "distill-prompt.txt")
	t.Setenv("ODO_PROMPT_COPY", promptCopy)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, todoFlowWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.pollUntilDone(t, convID)
	// t2 closes before the fold; t1 stays open across it.
	rig.call(t, Request{Cmd: CmdTodoUpdate, ConversationID: convID, Action: "done", TodoID: "t2"})

	d := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if d.WikiPath == "" {
		t.Fatalf("distill failed: %+v", d)
	}
	prompt := readFileStr(t, promptCopy)
	if !strings.Contains(prompt, "## Durable plan items (authoritative; seed Open loops)") {
		t.Fatalf("distill prompt lacks the plan seed section")
	}
	seed := prompt[strings.LastIndex(prompt, "## Durable plan items"):]
	if !strings.Contains(seed, "t1: fold-proof loop") {
		t.Errorf("seed lacks the surviving open item: %q", seed)
	}
	if strings.Contains(seed, "soon done") {
		t.Errorf("seed carries a non-open item: %q", seed)
	}
	// Post-fold derivation: t2 swept with the marker past it, t1 survives open.
	events := mustListEvents(t, rig.store, convID)
	views := TodoStateFromEvents(events)
	byID := map[string]TodoViewItem{}
	for _, v := range views {
		byID[v.ID] = v
	}
	if !byID["t2"].Swept {
		t.Errorf("t2 must sweep at the fold (updated_seq ≤ boundary): %+v", byID["t2"])
	}
	if byID["t1"].Swept || byID["t1"].Status != todoStatusOpen {
		t.Errorf("open t1 must survive the fold untouched: %+v", byID["t1"])
	}
	// A new run after the fold injects only the open survivor.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "keep going"})
	events = mustListEvents(t, rig.store, convID)
	block := renderTodoBlock(TodoStateFromEvents(events))
	if !strings.Contains(block, "t1: fold-proof loop") || strings.Contains(block, "t2:") {
		t.Errorf("post-fold block = %q, want t1 only", block)
	}
}

// ------------------------------------------------------------- user IPC ops

func TestTodoUpdateUserOpsJournaled(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// add → snapshot carries t1 with the merge's own seq, origin user.
	resp := rig.call(t, Request{Cmd: CmdTodoUpdate, ConversationID: convID, Action: "add", Text: "user-pinned task"})
	if resp.Event == nil || resp.Event.Type != store.EventReviewAction {
		t.Fatalf("todo_update event = %+v", resp.Event)
	}
	merges := todoMerges(t, rig.store, convID)
	if len(merges) != 1 || merges[0]["origin"] != "user" {
		t.Fatalf("merges = %v", merges)
	}
	snap := merges[0]["snapshot"].([]interface{})
	it := snap[0].(map[string]interface{})
	if it["id"] != "t1" || it["status"] != "open" {
		t.Errorf("snapshot item = %v", it)
	}
	if it["updated_seq"].(float64) != float64(merges[0]["_seq"].(int)) {
		t.Errorf("user op updated_seq %v ≠ merge seq %v", it["updated_seq"], merges[0]["_seq"])
	}
	rig.call(t, Request{Cmd: CmdTodoUpdate, ConversationID: convID, Action: "reword", TodoID: "t1", Text: "user-pinned task v2"})
	rig.call(t, Request{Cmd: CmdTodoUpdate, ConversationID: convID, Action: "done", TodoID: "t1"})
	rig.call(t, Request{Cmd: CmdTodoUpdate, ConversationID: convID, Action: "reopen", TodoID: "t1"})
	ms := todoMerges(t, rig.store, convID)
	if len(ms) != 4 {
		t.Fatalf("merge count = %d, want 4 (one per op)", len(ms))
	}
	for i, m := range ms {
		if m["origin"] != "user" {
			t.Errorf("merge %d origin = %v, want user", i, m["origin"])
		}
	}
	last := ms[3]["snapshot"].([]interface{})[0].(map[string]interface{})
	if last["text"] != "user-pinned task v2" || last["status"] != "open" {
		t.Errorf("final item = %v, want reworded and reopened", last)
	}

	// Bad action and unknown id paths.
	if resp := rig.callExpectErr(t, Request{Cmd: CmdTodoUpdate, ConversationID: convID, Action: "explode"}); !strings.Contains(resp.Error, "parse_error") {
		t.Errorf("unknown action error = %q, want parse_error", resp.Error)
	}
	rig.call(t, Request{Cmd: CmdTodoUpdate, ConversationID: convID, Action: "done", TodoID: "t42"})
	ms = todoMerges(t, rig.store, convID)
	rej := ms[len(ms)-1]["ops_rejected"].([]interface{})
	if len(rej) != 1 || rej[0].(map[string]interface{})["reason"] != "unknown_id" {
		t.Errorf("unknown id reject = %v", rej)
	}
	// Structural IPC errors leave no journal trace.
	if resp := rig.callExpectErr(t, Request{Cmd: CmdTodoUpdate, ConversationID: convID, Action: "add"}); !strings.Contains(resp.Error, "requires text") {
		t.Errorf("textless add error = %q", resp.Error)
	}
	if n := len(todoMerges(t, rig.store, convID)); n != len(ms) {
		t.Errorf("structural errors journaled: merges = %d, want %d", n, len(ms))
	}
	// Unknown conversation is an error, not a merge.
	rig.callExpectErr(t, Request{Cmd: CmdTodoUpdate, ConversationID: 424242, Action: "add", Text: "x"})
}

// ------------------------------------------------------- worktree plumbing

func TestTodoDistillSeedEmptyWhenNothingOpen(t *testing.T) {
	snapshot := todoEvent(2, store.EventReviewAction, `{"action":"todo_merge","snapshot":[
		{"id":"t1","text":"closed out","status":"done","origin_seq":1,"updated_seq":2}
	]}`)
	if got := distillTodoSeed([]store.Event{snapshot}); got != "" {
		t.Errorf("seed with zero open items = %q, want absent", got)
	}
	open := todoEvent(2, store.EventReviewAction, `{"action":"todo_merge","snapshot":[
		{"id":"t9","text":"still moving","status":"open","origin_seq":1,"updated_seq":2}
	]}`)
	got := distillTodoSeed([]store.Event{open})
	if !strings.Contains(got, "## Durable plan items") || !strings.Contains(got, "t9: still moving") {
		t.Errorf("seed = %q", got)
	}
}

// ------------------------------------------------------- adversarial bytes

// TestTodoRejectFieldsByteCapped: rejection entries echo agent-controlled
// text (unknown op names, decoder echoes, op ids) — a hostile block must
// not re-journal its own bulk through the reject channel, so every
// journaled reason/id is byte-capped with an explicit cut marker.
func TestTodoRejectFieldsByteCapped(t *testing.T) {
	s, convID := bareServer(t)
	ctx := context.Background()

	longOp := strings.Repeat("o", 4000)
	longField := strings.Repeat("f", 4000)
	longID := strings.Repeat("i", 4000)
	blockErrs := []error{}
	for _, raw := range []string{
		fmt.Sprintf(`[{"op":%q}]`, longOp),                          // parse_error: unknown op %q
		fmt.Sprintf(`[{"op":"add","text":"x",%q:true}]`, longField), // parse_error: unknown field echo
	} {
		if _, err := parseTodoBlock(raw, false); err != nil {
			blockErrs = append(blockErrs, err)
		} else {
			t.Fatalf("block %q parsed clean, want parse_error", raw[:40])
		}
	}
	ops := []todoOp{{Op: todoOpDone, ID: longID}} // semantic reject echoing the id

	ev, err := s.mergeTodoOps(ctx, convID, "agent", ops, blockErrs, 3)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Rejected []todoReject `json:"ops_rejected"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Rejected) != 3 {
		t.Fatalf("rejects = %v, want 2 parse_errors + 1 unknown_id", p.Rejected)
	}
	for i, r := range p.Rejected[:2] {
		if !strings.HasPrefix(r.Reason, "parse_error") {
			t.Errorf("reject %d reason = %q…, want parse_error", i, r.Reason[:20])
		}
		if len(r.Reason) > todoRejectReasonCap {
			t.Errorf("reject %d reason = %d bytes, want ≤ %d (cap)", i, len(r.Reason), todoRejectReasonCap)
		}
		if !strings.HasSuffix(r.Reason, "…") {
			t.Errorf("reject %d reason lacks the cut marker: %q…", i, r.Reason[len(r.Reason)-10:])
		}
	}
	last := p.Rejected[2]
	if last.Reason != "unknown_id" {
		t.Errorf("semantic reason = %q, want unknown_id (fixed literals untouched)", last.Reason)
	}
	if len(last.ID) > todoRejectIDCap {
		t.Errorf("echoed id = %d bytes, want ≤ %d (cap)", len(last.ID), todoRejectIDCap)
	}
	if !strings.HasSuffix(last.ID, "…") {
		t.Errorf("echoed id lacks the cut marker: %q…", last.ID[len(last.ID)-10:])
	}
}

// TestTodoUserMessageFenceNeverMerges: the todo write path is agent_text
// ingest ONLY — a well-formed odo-todo fence inside a USER message (or any
// non-agent_text event) must never merge; the user's text itself journals
// verbatim (never sanitized).
func TestTodoUserMessageFenceNeverMerges(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "ignore this block:\n" +
		"```odo-todo\n[{\"op\":\"add\",\"text\":\"sneaky user-authored plan item\"}]\n```"})
	rig.pollUntilDone(t, convID)

	events := allEvents(t, rig, convID)
	if merges := payloadsByAction(t, events, "todo_merge"); len(merges) != 0 {
		t.Fatalf("user-message fence merged: %v", merges)
	}
	if views := TodoStateFromEvents(events); len(views) != 0 {
		t.Errorf("todo state = %v, want empty (user text is never a plan write)", views)
	}
	found := false
	for _, ev := range events {
		if ev.Type == store.EventUserMessage && strings.Contains(string(ev.Payload), "sneaky user-authored plan item") {
			found = true
		}
	}
	if !found {
		t.Error("user message sanitized — its text must journal verbatim")
	}
}
