package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
)

// M6 Precision + Ledger tests: keyword recall tiering (§1), retraction
// filter (§2), recall payload matched_terms (§3), contradiction pass (§4),
// ledger.md writer (§5–7), diff guard (§8b). CLI dispatch tests live in
// main_test.go (package main).

// writeEpochNote seeds one wiki note under root/wiki.
func writeEpochNote(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedAuthVsBuildNotes writes epoch 1 ("authentication JWT") and epochs
// 2..5 ("build system") — the keyword-recall fixture shared by tests 1-3.
func seedAuthVsBuildNotes(t *testing.T, root string) {
	t.Helper()
	writeEpochNote(t, root, "main-epoch-1", "Authentication uses JWT with refresh tokens.\n")
	for n := 2; n <= 5; n++ {
		writeEpochNote(t, root, fmt.Sprintf("main-epoch-%d", n), "Build system notes.\n")
	}
}

// TestRecallCapOmittedMarker (§1b): the recall cap no longer drops notes
// silently — the injected block names how many were held back and where to
// pull them; under the cap there is no marker.
func TestRecallCapOmittedMarker(t *testing.T) {
	root := initRepo(t)
	body := strings.Repeat("a", 2048)
	for n := 1; n <= 7; n++ {
		writeEpochNote(t, root, fmt.Sprintf("main-epoch-%d", n), body)
	}
	memory, items, _ := recallWikiNotes(root, "main", "zzz-no-match", nil)
	if len(items) >= 7 {
		t.Fatalf("all 7 notes fit under the cap — fixture assumption broken")
	}
	want := fmt.Sprintf("_%d more note(s) held back by the %dKB recall cap", 7-len(items), recallMemoryCap/1024)
	if !strings.Contains(memory, want) {
		t.Errorf("memory block missing omitted-count marker %q", want)
	}

	root2 := initRepo(t)
	writeEpochNote(t, root2, "main-epoch-1", "short\n")
	m2, _, _ := recallWikiNotes(root2, "main", "", nil)
	if strings.Contains(m2, "held back") {
		t.Error("omitted marker present under the cap")
	}
}

// eventSeqByAction returns the seq of the last review_action with the given
// action (0 when absent).
func eventSeqByAction(t *testing.T, events []store.Event, action string) int {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventReviewAction {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(events[i].Payload, &p); err != nil {
			t.Fatalf("review_action payload: %v", err)
		}
		if p["action"] == action {
			return events[i].Seq
		}
	}
	return 0
}

// TestKeywordRecallRanksMatches (spec test 1): the matched tier ranks above
// the unmatched tier — epoch 1 mentions "authentication", epochs 2-5 do
// not, so a query for "authentication" injects epoch 1 FIRST even though it
// is the oldest note, followed by the unmatched tier newest-first.
func TestKeywordRecallRanksMatches(t *testing.T) {
	root := t.TempDir()
	seedAuthVsBuildNotes(t, root)

	memory, items, _ := recallWikiNotes(root, "main", "authentication", nil)
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5 (cap not reached): %+v", len(items), items)
	}
	if !strings.HasSuffix(items[0].path, "main-epoch-1.md") {
		t.Errorf("items[0] = %s, want main-epoch-1.md (matched tier first)", items[0].path)
	}
	if fmt.Sprint(items[0].matchedTerms) != "[authentication]" {
		t.Errorf("items[0].matchedTerms = %v, want [authentication]", items[0].matchedTerms)
	}
	for i, want := range []int{5, 4, 3, 2} {
		if !strings.HasSuffix(items[i+1].path, fmt.Sprintf("main-epoch-%d.md", want)) {
			t.Errorf("items[%d] = %s, want main-epoch-%d.md (unmatched tier newest-first)", i+1, items[i+1].path, want)
		}
	}
	// The injected block's first note header is the matched epoch 1.
	if !strings.HasPrefix(memory, "## main-epoch-1.md") {
		t.Errorf("memory block starts with %q, want ## main-epoch-1.md", memory[:min(40, len(memory))])
	}
}

// TestKeywordRecallFallsBackWhenNoMatch (spec test 2): a query matching no
// note degrades to pure newest-first with all matchedTerms empty — the
// pre-M6 behavior, unchanged.
func TestKeywordRecallFallsBackWhenNoMatch(t *testing.T) {
	root := t.TempDir()
	seedAuthVsBuildNotes(t, root)

	_, items, _ := recallWikiNotes(root, "main", "zzz", nil)
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5", len(items))
	}
	for i, want := range []int{5, 4, 3, 2, 1} {
		if !strings.HasSuffix(items[i].path, fmt.Sprintf("main-epoch-%d.md", want)) {
			t.Errorf("items[%d] = %s, want main-epoch-%d.md", i, items[i].path, want)
		}
		if len(items[i].matchedTerms) != 0 {
			t.Errorf("items[%d].matchedTerms = %v, want empty", i, items[i].matchedTerms)
		}
	}
}

// TestKeywordRecallStopWords (spec test 3): stop-words are filtered from
// the query, and the surviving tokens drive matching.
func TestKeywordRecallStopWords(t *testing.T) {
	if got := fmt.Sprint(tokenizeQuery("how does the auth work")); got != "[auth work]" {
		t.Fatalf("tokenizeQuery = %s, want [auth work]", got)
	}
	root := t.TempDir()
	seedAuthVsBuildNotes(t, root)
	_, items, _ := recallWikiNotes(root, "main", "how does the auth work", nil)
	if len(items) == 0 || !strings.HasSuffix(items[0].path, "main-epoch-1.md") {
		t.Fatalf("items[0] = %+v, want main-epoch-1.md first (matched 'auth')", items)
	}
	if fmt.Sprint(items[0].matchedTerms) != "[auth]" {
		t.Errorf("items[0].matchedTerms = %v, want [auth]", items[0].matchedTerms)
	}
}

// TestRecallPayloadMatchedTerms (spec test 4): the journaled user_message
// recall payload is an array of objects; the keyword-matched note carries
// matched_terms; fixed markers omit the key.
func TestRecallPayloadMatchedTerms(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	writeUserMD(t, home, "# durable principles\n")
	writeEpochNote(t, root, "main-epoch-1", "Authentication uses JWT with refresh tokens.\n")
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "auth"})

	var p map[string]interface{}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	raw, ok := p["recall"].([]interface{})
	if !ok || len(raw) == 0 {
		t.Fatalf("recall payload = %v, want a non-empty array", p["recall"])
	}
	var noteTerms []interface{}
	var sawUserMarker, sawNote bool
	for _, v := range raw {
		item, ok := v.(map[string]interface{})
		if !ok {
			t.Fatalf("recall entry not an object: %v (M6 shape is []object)", v)
		}
		path, _ := item["path"].(string)
		switch {
		case path == "~/.odo/user.md":
			sawUserMarker = true
			if _, has := item["matched_terms"]; has {
				t.Errorf("fixed marker user.md carries matched_terms: %v (omitted when empty)", item)
			}
		case strings.HasSuffix(path, "main-epoch-1.md"):
			sawNote = true
			noteTerms, _ = item["matched_terms"].([]interface{})
		}
	}
	if !sawUserMarker {
		t.Error("recall payload missing the ~/.odo/user.md fixed marker")
	}
	if !sawNote {
		t.Fatal("recall payload missing the seeded note")
	}
	if fmt.Sprint(noteTerms) != "[auth]" {
		t.Errorf("note matched_terms = %v, want [auth]", noteTerms)
	}
	rig.pollUntilDone(t, convID)
}

// TestRetractionExcludesNoteFromRecall (spec test 5): a journaled
// memory_update{layer:"note", cause:"retract"} removes the named note from
// the recall set — via retractedNotes directly and via the send path.
func TestRetractionExcludesNoteFromRecall(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	writeEpochNote(t, root, "main-epoch-1", "Authentication uses JWT.\n")
	writeEpochNote(t, root, "main-epoch-2", "Auth switched from JWT to session cookies.\n")
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()
	if _, err := rig.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":      "note",
		"cause":      "retract",
		"detail":     "main-epoch-1 contradicted by main-epoch-2: JWT → session cookies",
		"before_sha": "deadbeefdeadbeef",
		"after_sha":  "deadbeefdeadbeef",
	})); err != nil {
		t.Fatalf("journal retraction: %v", err)
	}

	retracted := rig.server.retractedNotes(ctx, convID)
	if !retracted["main-epoch-1"] || len(retracted) != 1 {
		t.Fatalf("retractedNotes = %v, want {main-epoch-1}", retracted)
	}
	memory, items, _ := recallWikiNotes(root, "main", "", retracted)
	if len(items) != 1 || !strings.HasSuffix(items[0].path, "main-epoch-2.md") {
		t.Fatalf("items = %+v, want only main-epoch-2.md", items)
	}
	if strings.Contains(memory, "epoch-1") {
		t.Errorf("retracted note content still injected: %q", memory)
	}

	// The full send path filters too (memoryLayers reads the journal).
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "auth"})
	recall := recallPathsFromEvent(t, sent.Event)
	if len(recall) != 1 || !strings.HasSuffix(recall[0], "main-epoch-2.md") {
		t.Errorf("send recall = %v, want only main-epoch-2.md", recall)
	}
	rig.pollUntilDone(t, convID)
}

// m6DistillFlow reruns the learnerFlowWrapper protocol: distill #1 writes
// the "old" note, then the oneshot file is rewritten and distill #2 writes
// the "new" note. Returns the events after the second distill.
func m6DistillFlow(t *testing.T, note1, note2 string) (*testRig, int64, []store.Event) {
	t.Helper()
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	oneshot := filepath.Join(t.TempDir(), "distill-output.txt")
	if err := os.WriteFile(oneshot, []byte(note1), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODO_DISTILL_OUTPUT", oneshot)
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[],"user":[],"reaffirm":[]}`)
	rig := startRig(t, root)
	t.Cleanup(func() { rig.stop(t) })

	convID, resp1 := runToDistill(t, rig, root)
	if resp1.WikiPath == "" {
		t.Fatalf("distill #1 failed: %+v", resp1)
	}
	// The stub `cat`s the output file per invocation — rewrite it in place
	// so distill #2 emits the new note.
	if err := os.WriteFile(oneshot, []byte(note2), 0o644); err != nil {
		t.Fatal(err)
	}
	// Distill #2 folds only events journaled after marker #1 — an empty
	// window is now rejected — so give it a fresh run to fold.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Update hello.txt"})
	rig.pollUntilDone(t, convID)
	resp2 := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if resp2.WikiPath == "" {
		t.Fatalf("distill #2 failed: %+v", resp2)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	return rig, convID, events
}

// TestContradictionPassFlagsConflict (spec test 6): the new note carries
// the signal token "switched" and shares the salient token "jwt" with the
// old note → one contradiction; distill journals the retraction event and
// the old note file is NOT mutated.
func TestContradictionPassFlagsConflict(t *testing.T) {
	// Unit level: the heuristic itself.
	old := []epochNote{{name: "main-epoch-1", content: "Authentication uses JWT.\n"}}
	found := detectContradictions("Auth switched from JWT to session cookies.", old)
	if len(found) != 1 {
		t.Fatalf("detectContradictions = %+v, want exactly one", found)
	}
	if found[0].oldNote != "main-epoch-1" {
		t.Errorf("oldNote = %q, want main-epoch-1", found[0].oldNote)
	}
	if found[0].snippet != "Auth switched from JWT to session cookies." {
		t.Errorf("snippet = %q, want the new sentence", found[0].snippet)
	}

	// End to end: distill #2 journals memory_update{layer:"note",
	// cause:"retract"} naming main-epoch-1.
	rig, _, events := m6DistillFlow(t,
		"# Epoch 1\n\nAuthentication uses JWT.\n",
		"# Epoch 2\n\nAuth switched from JWT to session cookies.\n")
	retracts := memoryUpdatesByCause(t, events, "retract")
	if len(retracts) != 1 {
		t.Fatalf("retract events = %d, want 1", len(retracts))
	}
	if retracts[0]["layer"] != "note" {
		t.Errorf("retract layer = %v, want note", retracts[0]["layer"])
	}
	detail, _ := retracts[0]["detail"].(string)
	if !strings.HasPrefix(detail, "main-epoch-1 contradicted by main-epoch-2: ") {
		t.Errorf("retract detail = %q, want main-epoch-1 contradicted by main-epoch-2", detail)
	}
	if retracts[0]["before_sha"] != retracts[0]["after_sha"] {
		t.Errorf("before_sha %v != after_sha %v (the file is never mutated)", retracts[0]["before_sha"], retracts[0]["after_sha"])
	}
	// The distill review_action carries the M6 contradiction count.
	distills := payloadsByAction(t, events, "distill")
	if len(distills) != 2 {
		t.Fatalf("distill events = %d, want 2", len(distills))
	}
	if distills[1]["contradictions"] != float64(1) {
		t.Errorf("distill #2 contradictions = %v, want 1", distills[1]["contradictions"])
	}
	// Inv 2: epoch 1's file still holds the original record — the
	// retraction is a journal event, never a file mutation.
	got := readFileStr(t, filepath.Join(rig.root, "wiki", "main-epoch-1.md"))
	if !strings.Contains(got, "Authentication uses JWT.") || strings.Contains(got, "switched") {
		t.Errorf("epoch-1 file no longer the original record: %q", got)
	}
}

// TestContradictionPassNoFalsePositive (spec test 7): shared salient token
// without a contradiction signal never flags — an affirmative addition is
// not a retraction.
func TestContradictionPassNoFalsePositive(t *testing.T) {
	old := []epochNote{{name: "main-epoch-1", content: "Authentication uses JWT.\n"}}
	if found := detectContradictions("Added a JWT refresh endpoint.", old); len(found) != 0 {
		t.Fatalf("detectContradictions = %+v, want empty (no signal token)", found)
	}

	_, _, events := m6DistillFlow(t,
		"# Epoch 1\n\nAuthentication uses JWT.\n",
		"# Epoch 2\n\nAdded a JWT refresh endpoint.\n")
	if n := len(memoryUpdatesByCause(t, events, "retract")); n != 0 {
		t.Errorf("retract events = %d, want 0 (no false positive)", n)
	}
}

// TestLedgerAppendedAtDistill (spec test 8): the distill ledger section
// carries duration + recall + proposals rows, each citing the real seq of
// the journaled event the metric came from; the distill review_action's
// payload carries duration_ms and contradictions keys.
func TestLedgerAppendedAtDistill(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\nDecided things.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[],"user":[],"reaffirm":[]}`)
	// A seeded note makes the send's recall non-empty so the recall metric
	// cites a real user_message seq.
	writeEpochNote(t, root, "main-epoch-1", "Seeded build notes.\n")
	rig := startRig(t, root)
	defer rig.stop(t)

	convID, resp := runToDistill(t, rig, root)
	if resp.WikiPath == "" {
		t.Fatalf("distill failed: %+v", resp)
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	distillSeq := eventSeqByAction(t, events, "distill")
	if distillSeq == 0 {
		t.Fatal("no distill review_action journaled")
	}
	recallEv := lastRecallEvent(events)
	if recallEv == nil {
		t.Fatal("no recall-carrying user_message")
	}

	// The distill review_action's payload carries the M6 keys.
	distills := payloadsByAction(t, events, "distill")
	if _, ok := distills[0]["duration_ms"]; !ok {
		t.Error("distill payload missing duration_ms")
	}
	if _, ok := distills[0]["contradictions"]; !ok {
		t.Error("distill payload missing contradictions")
	}

	ledger := readFileStr(t, filepath.Join(root, ".odo", "ledger.md"))
	if !strings.Contains(ledger, "## epoch 1 — ") {
		t.Errorf("ledger missing '## epoch 1 — ' section header:\n%s", ledger)
	}
	if !strings.Contains(ledger, "- distill duration: ") ||
		!strings.Contains(ledger, fmt.Sprintf("(review_action/distill seq %d)", distillSeq)) {
		t.Errorf("ledger missing duration row citing seq %d:\n%s", distillSeq, ledger)
	}
	if !strings.Contains(ledger, fmt.Sprintf("- recall notes: 1 (user_message seq %d)", recallEv.Seq)) {
		t.Errorf("ledger missing recall row citing seq %d:\n%s", recallEv.Seq, ledger)
	}
	// The stubbed learner proposed nothing: the absence IS the record.
	if !strings.Contains(ledger, "- proposals: 0 (no memory_propose event)") {
		t.Errorf("ledger missing the zero-proposals row:\n%s", ledger)
	}
}

// TestLedgerAppendedAtApply (spec test 9): apply_memory appends a separate
// "## epoch N (apply)" section with the accepted/rejected counts citing the
// memory_apply marker's seq.
func TestLedgerAppendedAtApply(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	seedProposeBatch(t, rig, convID, 1, []MemoryProposal{
		{Target: "memory.md", Rule: "Rule one for the ledger test.", Evidence: "main-epoch-1"},
		{Target: "memory.md", Rule: "Rule two for the ledger test.", Evidence: "main-epoch-1"},
		{Target: "memory.md", Rule: "Rule three for the ledger test.", Evidence: "main-epoch-1"},
	}, nil)

	applied := rig.call(t, Request{
		Cmd:            CmdApplyMemory,
		ConversationID: convID,
		Epoch:          1,
		Accepted:       []MemoryAccept{{Target: "memory.md", Index: 0}, {Target: "memory.md", Index: 1}},
	})
	if !applied.Applied {
		t.Fatal("apply_memory: applied must be true")
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	applySeq := eventSeqByAction(t, events, "memory_apply")
	if applySeq == 0 {
		t.Fatal("no memory_apply review_action journaled")
	}
	ledger := readFileStr(t, filepath.Join(root, ".odo", "ledger.md"))
	if !strings.Contains(ledger, "## epoch 1 (apply) — ") {
		t.Errorf("ledger missing '## epoch 1 (apply) — ' section:\n%s", ledger)
	}
	want := fmt.Sprintf("- accepted: 2, rejected: 1 (review_action/memory_apply seq %d)", applySeq)
	if !strings.Contains(ledger, want) {
		t.Errorf("ledger missing %q:\n%s", want, ledger)
	}
}

// TestLedgerWriteFailureJournalsNotFails (spec test 10): a ledger write
// failure journals memory_update{layer:"ledger", cause:"write_failed"} but
// never fails the distill. (.odo/ledger.md is seeded as a DIRECTORY: the
// O_APPEND open fails with EISDIR while the SQLite journal stays writable
// — a read-only .odo/ would break the journal long before the ledger.)
func TestLedgerWriteFailureJournalsNotFails(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\nDecided things.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[],"user":[],"reaffirm":[]}`)
	rig := startRig(t, root)
	defer rig.stop(t)

	if err := os.MkdirAll(filepath.Join(root, ".odo", "ledger.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	convID, resp := runToDistill(t, rig, root)
	if resp.WikiPath == "" {
		t.Fatal("distill must succeed despite the ledger write failure")
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	failed := memoryUpdatesByCause(t, events, "write_failed")
	if len(failed) != 1 {
		t.Fatalf("write_failed events = %d, want 1", len(failed))
	}
	if failed[0]["layer"] != "ledger" {
		t.Errorf("write_failed layer = %v, want ledger", failed[0]["layer"])
	}
}

// TestVerifyLedgerQuote (spec test 11): a verbatim quote of a journaled
// payload passes; a fabricated number fails. The mechanical gate that keeps
// any future LLM-selected ledger row honest (inv 4).
func TestVerifyLedgerQuote(t *testing.T) {
	ev := store.Event{Payload: json.RawMessage(`{"action":"distill","duration_ms":187000,"epoch":6}`)}
	if !verifyLedgerQuote(`"duration_ms":187000`, ev) {
		t.Error("verbatim quote must verify")
	}
	if verifyLedgerQuote(`"duration_ms":999999`, ev) {
		t.Error("fabricated number must NOT verify")
	}
	if verifyLedgerQuote("", ev) {
		t.Error("empty quote must NOT verify")
	}
	// Whitespace in the quote is normalized away (trim + collapse).
	padded := store.Event{Payload: json.RawMessage(`{"a":  "b c"}`)}
	if !verifyLedgerQuote("\"a\":  \"b c\"", padded) {
		t.Error("collapse-normalized quote must verify")
	}
}

// TestDiffGuardRejectsProtectedPaths (spec test 13): accept_diff refuses a
// patch touching .odo/ or wiki/, journals agent_error, and leaves the diff
// pending with no git apply; a benign patch still applies. A mode-only
// patch (no +++ line) is caught via the diff --git b-side.
func TestDiffGuardRejectsProtectedPaths(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	seed := func(name, patch string) store.Diff {
		t.Helper()
		p := filepath.Join(root, ".odo", "diffs", name)
		if err := os.WriteFile(p, []byte(patch), 0o644); err != nil {
			t.Fatal(err)
		}
		d, err := rig.store.InsertDiff(ctx, convID, p, "base")
		if err != nil {
			t.Fatalf("InsertDiff: %v", err)
		}
		return d
	}

	protectedPatches := []struct {
		name  string
		patch string
		want  string // protected path named in the error
	}{
		{"odo.diff", "diff --git a/.odo/memory.md b/.odo/memory.md\nindex 1111111..2222222 100644\n--- a/.odo/memory.md\n+++ b/.odo/memory.md\n@@ -1 +1 @@\n-old\n+new\n", ".odo/memory.md"},
		{"wiki.diff", "diff --git a/wiki/main-epoch-1.md b/wiki/main-epoch-1.md\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/wiki/main-epoch-1.md\n@@ -0,0 +1 @@\n+hello\n", "wiki/main-epoch-1.md"},
		{"mode.diff", "diff --git a/.odo/pins.md b/.odo/pins.md\nold mode 100644\nnew mode 100755\n", ".odo/pins.md"},
	}
	for _, tc := range protectedPatches {
		d := seed(tc.name, tc.patch)
		before := eventsTypes(t, rig, convID)
		resp := rig.callExpectErr(t, Request{Cmd: CmdAcceptDiff, DiffID: d.ID})
		if !strings.Contains(resp.Error, tc.want) {
			t.Errorf("%s: error = %q, want it to name %s", tc.name, resp.Error, tc.want)
		}
		got, err := rig.store.GetDiff(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.DiffPending {
			t.Errorf("%s: diff status = %s, want pending (still reviewable)", tc.name, got.Status)
		}
		after := eventsTypes(t, rig, convID)
		if len(after) <= len(before) || after[len(after)-1] != "agent_error" {
			t.Errorf("%s: events %v → %v, want an agent_error appended", tc.name, before, after)
		}
	}

	// No git apply happened for the protected patches: neither target file
	// exists outside the diff fixtures.
	if _, err := os.Stat(filepath.Join(root, "wiki", "main-epoch-1.md")); !os.IsNotExist(err) {
		t.Error("wiki/main-epoch-1.md exists — git apply ran despite the guard")
	}

	// A benign patch applies normally.
	d := seed("benign.diff", "diff --git a/hello.txt b/hello.txt\nnew file mode 100644\nindex 0000000..ce01362\n--- /dev/null\n+++ b/hello.txt\n@@ -0,0 +1 @@\n+hello\n")
	resp := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: d.ID})
	if !resp.Applied {
		t.Fatalf("benign accept: applied = false (resp %+v)", resp)
	}
	if got := readFileStr(t, filepath.Join(root, "hello.txt")); got != "hello\n" {
		t.Errorf("hello.txt = %q, want hello", got)
	}

	// git.PatchPaths contract: the mode-only change parses to its path from
	// the "diff --git" header alone (no ---/+++ lines present).
	paths, err := git.PatchPaths(filepath.Join(root, ".odo", "diffs", "mode.diff"))
	if err != nil || len(paths) != 1 || paths[0] != ".odo/pins.md" {
		t.Errorf("PatchPaths(mode.diff) = %v, %v; want [.odo/pins.md]", paths, err)
	}
}

// TestLedgerZeroProposalsNoCrossEpoch (K3 review fix): a zero-proposal
// distill following a proposing distill must show "proposals: 0", NOT
// inherit the previous epoch's memory_propose count.
func TestLedgerZeroProposalsNoCrossEpoch(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	// First distill: learner proposes 1 rule
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 1\n\nDecided: use JWT.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[{"rule":"Use JWT for auth","evidence":"JWT mentioned"}],"user":[],"reaffirm":[]}`)
	writeEpochNote(t, root, "main-epoch-1", "Seeded notes.\n")
	rig := startRig(t, root)
	defer rig.stop(t)

	// First distill — should produce a memory_propose with proposals
	convID, resp := runToDistill(t, rig, root)
	_ = convID
	if resp.WikiPath == "" {
		t.Fatalf("first distill failed: %+v", resp)
	}

	// Second distill: learner proposes nothing
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT", "# Epoch 2\n\nMore notes.\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[],"user":[],"reaffirm":[]}`)
	_, resp2 := runToDistill(t, rig, root)
	if resp2.WikiPath == "" {
		t.Fatalf("second distill failed: %+v", resp2)
	}

	// Read ledger
	ledger := readFileStr(t, filepath.Join(root, ".odo", "ledger.md"))

	// Epoch 1 section should have "proposals: 1"
	if !strings.Contains(ledger, "## epoch 1 — ") {
		t.Errorf("ledger missing epoch 1 section:\n%s", ledger)
	}

	// Epoch 2 section should have "proposals: 0 (no memory_propose event)"
	// NOT "proposals: 1" — that would be cross-epoch misattribution
	if !strings.Contains(ledger, "## epoch 2 — ") {
		t.Errorf("ledger missing epoch 2 section:\n%s", ledger)
	}
	// Find the epoch 2 section and check its proposals line
	epoch2Start := strings.Index(ledger, "## epoch 2 — ")
	if epoch2Start < 0 {
		t.Fatal("no epoch 2 section")
	}
	epoch2Section := ledger[epoch2Start:]
	if !strings.Contains(epoch2Section, "proposals: 0") {
		t.Errorf("epoch 2 should show proposals: 0, but got:\n%s", epoch2Section)
	}
	if strings.Contains(epoch2Section, "proposals: 1") {
		t.Errorf("epoch 2 inherited epoch 1's proposals (cross-epoch misattribution):\n%s", epoch2Section)
	}
}

// TestDiffGuardCQuotedPath (K3 hardening): git C-quotes the +++ header
// when a path carries non-ASCII bytes (+++ "b/.odo/<escapes>"). The parser
// must unquote the octal escapes back to real filesystem names — else a
// protected target parses as unprotected-or-absent and the prefix check
// never fires, and git pathspecs built from the raw escapes match nothing.
// (Only the non-ASCII tail is octal-escaped; the .odo/ prefix stays literal.)
func TestDiffGuardCQuotedPath(t *testing.T) {
	patch := filepath.Join(t.TempDir(), "quoted.diff")
	content := "diff --git \"a/.odo/m\\303\\251mory.md\" \"b/.odo/m\\303\\251mory.md\"\n" +
		"index 1111111..2222222 100644\n" +
		"--- \"a/.odo/m\\303\\251mory.md\"\n" +
		"+++ \"b/.odo/m\\303\\251mory.md\"\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n"
	if err := os.WriteFile(patch, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	aPaths, bPaths, err := git.DiffPaths(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(bPaths) != 1 || bPaths[0] != ".odo/mémory.md" {
		t.Fatalf("DiffPaths b-side = %v, want [.odo/mémory.md]", bPaths)
	}
	if len(aPaths) != 1 || aPaths[0] != ".odo/mémory.md" {
		t.Fatalf("DiffPaths a-side = %v, want [.odo/mémory.md]", aPaths)
	}
	if err := rejectProtectedPaths(bPaths); err == nil || !strings.Contains(err.Error(), ".odo/") {
		t.Errorf("rejectProtectedPaths = %v, want a protected-path error naming .odo/", err)
	}

	// A quoted benign path is still extracted (not silently dropped by the
	// quote) and passes the guard.
	benign := filepath.Join(t.TempDir(), "quoted-benign.diff")
	benignPatch := strings.Replace(content, ".odo/m\\303\\251mory.md", "src/f\\303\\251.go", -1)
	if err := os.WriteFile(benign, []byte(benignPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	if paths, err := git.PatchPaths(benign); err != nil {
		t.Fatal(err)
	} else if len(paths) != 1 || paths[0] != "src/fé.go" {
		t.Fatalf("PatchPaths(benign) = %v, want [src/fé.go]", paths)
	} else if err := rejectProtectedPaths(paths); err != nil {
		t.Errorf("rejectProtectedPaths(benign) = %v, want nil", err)
	}
}

// TestContradictionPassDoesNotReJournalRetracted (K3 hardening): a note
// already in the journal's retraction set is never re-journaled — neither
// by a later pass re-flagging the same fact, nor by two sentences of one
// new note flagging the same old note.
func TestContradictionPassDoesNotReJournalRetracted(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	t.Cleanup(func() { rig.stop(t) })
	ctx := context.Background()

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	writeEpochNote(t, root, "main-epoch-1", "Authentication uses JWT.\n")
	writeEpochNote(t, root, "main-epoch-2", "Build system notes for the project.\n")

	// In-pass dedup: two sentences flag the same old note → one journal entry.
	const flagTwice = "Auth switched from JWT to session cookies.\nAuth switched from JWT to session cookies.\n"
	if n := rig.server.runContradictionPass(ctx, convID, "main-epoch-3", flagTwice, 3); n != 1 {
		t.Fatalf("first pass = %d, want 1 (duplicate sentences journal once)", n)
	}
	// Cross-pass dedup: epoch 1 is now retracted; a re-flag must not duplicate.
	const flag = "Auth switched from JWT to session cookies.\n"
	if n := rig.server.runContradictionPass(ctx, convID, "main-epoch-4", flag, 4); n != 0 {
		t.Fatalf("second pass = %d, want 0 (epoch 1 already retracted)", n)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	retracts := memoryUpdatesByCause(t, events, "retract")
	if len(retracts) != 1 {
		t.Fatalf("retract events = %d, want exactly 1 — no duplicate retraction", len(retracts))
	}
	if detail, _ := retracts[0]["detail"].(string); !strings.HasPrefix(detail, "main-epoch-1 contradicted by main-epoch-3") {
		t.Errorf("retract detail = %q, want main-epoch-1 contradicted by main-epoch-3", detail)
	}
}

// TestDistillLedgerListEventsFailureJournalsGap (K3 hardening, inv 3 edge):
// when the ledger's metrics scan fails, the skipped section is journaled
// as memory_update{layer:"ledger", cause:"write_failed"} with the
// list_events detail — never silently dropped. The scan fails here on an
// already-cancelled ctx; the gap record still lands because the journal
// write uses a cancel-free ctx.
func TestDistillLedgerListEventsFailureJournalsGap(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	t.Cleanup(func() { rig.stop(t) })
	ctx := context.Background()

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	distillEv, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction,
		mustJSON(map[string]interface{}{"action": "distill", "epoch": 2}))
	if err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	rig.server.journalDistillLedger(cancelled, convID, 1, distillEv)

	events, err := rig.store.ListEvents(ctx, convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	failed := memoryUpdatesByCause(t, events, "write_failed")
	found := false
	for _, p := range failed {
		detail, _ := p["detail"].(string)
		if p["layer"] == "ledger" && strings.HasPrefix(detail, "list_events: ") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing memory_update{layer:ledger, cause:write_failed, detail:'list_events: …'}: got %+v", failed)
	}
	if _, err := os.Stat(filepath.Join(root, ".odo", "ledger.md")); !os.IsNotExist(err) {
		t.Error("ledger.md must not gain a section when the metrics scan failed")
	}
}

// eventsTypes returns the conversation's journaled event types in order.
func eventsTypes(t *testing.T, rig *testRig, convID int64) []string {
	t.Helper()
	evs, err := rig.store.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}
