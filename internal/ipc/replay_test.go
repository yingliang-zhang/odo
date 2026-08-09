package ipc

// M6.1 (recall query token union), R4 (cold-start resume card), and the
// journal-pull wiring (G4) tests. R1 replay block construction is exercised
// end-to-end through the send-path integration tests.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

func chatEvent(seq int, typ, text string) store.Event {
	return store.Event{
		Seq:     seq,
		Type:    typ,
		Payload: json.RawMessage(mustJSON(map[string]interface{}{"text": text})),
	}
}

func distillMarkerEvent(seq, lastSeq int) store.Event {
	return store.Event{
		Seq:     seq,
		Type:    store.EventReviewAction,
		Payload: json.RawMessage(mustJSON(map[string]interface{}{"action": "distill", "first_seq": 1, "last_seq": lastSeq})),
	}
}

func hasToken(tokens []string, want string) bool {
	for _, tok := range tokens {
		if tok == want {
			return true
		}
	}
	return false
}

// TestRecallQueryUnion: the recall query is the user's message UNION the
// last recallCtxTurns current-epoch turns — folded turns never seed, and
// turns older than the window are dropped. A CJK-only current message
// (which tokenizes to almost nothing under the ASCII split) still inherits
// the thread's English terms.
func TestRecallQueryUnion(t *testing.T) {
	events := []store.Event{
		chatEvent(1, store.EventUserMessage, "folded turn mentions jwtsecret"),
		chatEvent(2, store.EventAgentText, "folded agent answer"),
		distillMarkerEvent(3, 2),
		chatEvent(4, store.EventUserMessage, "older visible turn dropme"),
		chatEvent(5, store.EventAgentText, "edit internal/ipc/recall.go tokenizeQuery next"),
		chatEvent(6, store.EventUserMessage, "接着改这一步"),
		chatEvent(7, store.EventAgentText, "done keeptoken"),
	}
	q := recallQuery("现在继续", events)
	for _, want := range []string{"edit internal/ipc/recall.go", "接着改这一步", "done keeptoken"} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing last-3 turn text %q", want)
		}
	}
	for _, gone := range []string{"jwtsecret", "folded agent answer", "dropme"} {
		if strings.Contains(q, gone) {
			t.Errorf("query leaked excluded turn text %q", gone)
		}
	}
	terms := tokenizeQuery(q)
	if !hasToken(terms, "tokenizequery") || !hasToken(terms, "recall") {
		t.Errorf("CJK message did not inherit turn terms: %v", terms)
	}
}

// TestRecallQueryNoEvents: with no journal state the query is the message
// alone — first-send behavior (and its frozen receipts) is unchanged.
func TestRecallQueryNoEvents(t *testing.T) {
	if q := recallQuery("hello", nil); q != "hello" {
		t.Errorf("recallQuery(nil) = %q, want the text unchanged", q)
	}
	if q := recallQuery("hello", []store.Event{}); q != "hello" {
		t.Errorf("recallQuery(empty) = %q, want the text unchanged", q)
	}
}

func TestOpenLoopsSection(t *testing.T) {
	cases := []struct{ name, note, want string }{
		{"absent", "# Epoch 1\n\nbody\n", ""},
		{"none", "## Open loops\n\nNone.\n", ""},
		{"dash none", "## Open loops\n\n- None.\n", ""},
		{"none no period", "## Open loops\n\nNone\n", ""},
		{"blank", "## Open loops\n\n\n", ""},
		{"none prefix kept", "## Open loops\n\nNone whatsoever\n", "None whatsoever"},
		{"bullets", "# E\n\n## Open loops\n\n- item A\n- item B\n", "- item A\n- item B"},
		{"stops at next h2", "## Open loops\n\n- A\n\n## Ledger\n\nx\n", "- A"},
		{"keeps h3", "## Open loops\n\n### Later\n\n- A\n", "### Later\n\n- A"},
		{"heading case-insensitive", "## OPEN LOOPS\n\n- A\n", "- A"},
	}
	for _, tc := range cases {
		if got := openLoopsSection(tc.note); got != tc.want {
			t.Errorf("%s: openLoopsSection = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestBuildResumeCard: the card carries the newest note's loops, the source
// note, and the fold stamp; it stops at the next H2 and advertises the
// journal pull path (the "may be lossy" caveat).
func TestBuildResumeCard(t *testing.T) {
	root := initRepo(t)
	writeEpochNote(t, root, "main-epoch-3",
		"# Epoch 3\n\nx\n\n## Open loops\n\n- ship A\n- decide B\n\n## Done\n\ny\n")
	events := []store.Event{
		chatEvent(1, store.EventUserMessage, "hi"),
		distillMarkerEvent(2, 9),
	}
	card, notePath := buildResumeCard(root, "main", events)
	for _, want := range []string{"## Resume context (cold start", "main-epoch-3.md", "seq 9", "- ship A", "- decide B", "odo journal"} {
		if !strings.Contains(card, want) {
			t.Errorf("card missing %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, "## Done") {
		t.Error("card leaks past the Open loops section into the next H2")
	}
	if !strings.HasSuffix(notePath, "main-epoch-3.md") {
		t.Errorf("notePath = %q, want the source note", notePath)
	}
}

// TestBuildResumeCardGates: nothing honest to hand off means no card.
func TestBuildResumeCardGates(t *testing.T) {
	marker := []store.Event{distillMarkerEvent(2, 9)}

	// A conversation that never distilled: the seq stamp would be
	// meaningless (seq is per-conversation).
	root := initRepo(t)
	writeEpochNote(t, root, "main-epoch-1", "# E\n\n## Open loops\n\n- A\n")
	if card, _ := buildResumeCard(root, "main", []store.Event{chatEvent(1, store.EventUserMessage, "hi")}); card != "" {
		t.Error("card fired without a fold in this conversation")
	}

	// Fold happened but there is no note to hand off from.
	root = initRepo(t)
	if card, _ := buildResumeCard(root, "main", marker); card != "" {
		t.Error("card fired with no wiki notes")
	}

	// The explicit None form gets no card.
	root = initRepo(t)
	writeEpochNote(t, root, "main-epoch-1", "# E\n\n## Open loops\n\nNone.\n")
	if card, _ := buildResumeCard(root, "main", marker); card != "" {
		t.Error("card fired on a None open-loops section")
	}

	// Newest note wins; older notes' loops are never resurrected (they may
	// already be resolved).
	root = initRepo(t)
	writeEpochNote(t, root, "main-epoch-1", "# E1\n\n## Open loops\n\n- stale loop\n")
	writeEpochNote(t, root, "main-epoch-2", "# E2\n\nno loops section\n")
	if card, _ := buildResumeCard(root, "main", marker); card != "" {
		t.Error("card resurrected loops from an older note past the newest one")
	}
}

// TestResumeCardEndToEnd: the first message after a fold sees the card and
// an empty replay window; the message after that has replay turns again and
// the card self-limits.
func TestResumeCardEndToEnd(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, learnerFlowWrapper))
	setOneShotEnv(t, "ODO_DISTILL_OUTPUT",
		"# Epoch 1\n\nDecided things.\n\n## Open loops\n\n- decide the panel cap\n- push 1de583c\n\n## Done\n\nx\n")
	setOneShotEnv(t, "ODO_LEARNER_OUTPUT", `{"memory":[],"user":[],"reaffirm":[]}`)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.pollUntilDone(t, convID)
	if d := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID}); d.WikiPath == "" {
		t.Fatalf("distill failed: %+v", d)
	}

	// Cold start of epoch 2: replay is empty, the card hands off.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "继续"})
	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff — the stub wrote nothing")
	}
	prompt := done.Diff.Content
	for _, want := range []string{"## Resume context (cold start", "- decide the panel cap", "- push 1de583c", "odo journal"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("cold-start prompt missing %q", want)
		}
	}
	// The note is also recalled verbatim (newest-first fallback), so scope
	// the leak check to the card segment itself.
	cardStart := strings.Index(prompt, "## Resume context")
	if cardStart < 0 {
		t.Fatal("prompt has no resume card segment")
	}
	card := prompt[cardStart:]
	if end := strings.Index(card, "\n---\n"); end >= 0 {
		card = card[:end]
	}
	if strings.Contains(card, "## Done") {
		t.Error("card leaks past the Open loops section into the next H2")
	}

	// The follow-up has replay turns again: the card self-limits.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "继续第二步"})
	done2 := rig.pollUntilDone(t, convID)
	if done2.Diff == nil {
		t.Fatal("no diff on the follow-up run")
	}
	if !strings.Contains(done2.Diff.Content, "## Recent conversation") {
		t.Error("epoch-2 replay window missing on the follow-up prompt")
	}
	if strings.Contains(done2.Diff.Content, "## Resume context (cold start") {
		t.Error("card still injected once the replay window is non-empty")
	}
}

// TestPromptAdvertisesJournalPull (G4): every standing prompt surface names
// the journal pull path, and the distill prompt mandates the section the
// resume card renders.
func TestPromptAdvertisesJournalPull(t *testing.T) {
	root := initRepo(t)
	writeEpochNote(t, root, "main-epoch-1", "x\n")
	block := memoryMapBlock(root)
	for _, want := range []string{"odo journal folded", "odo journal range A B", "odo journal tail N", "NOT injected"} {
		if !strings.Contains(block, want) {
			t.Errorf("memoryMapBlock missing %q", want)
		}
	}
	if got := memoryMapBlock(t.TempDir()); got != "" {
		t.Errorf("memoryMapBlock on a fresh project = %q, want empty", got)
	}

	p := distillPrompt(nil)
	for _, want := range []string{"## Open loops", "None."} {
		if !strings.Contains(p, want) {
			t.Errorf("distillPrompt missing open-loops mandate %q", want)
		}
	}

	s := &Server{projectRoot: root}
	// Production invariant: bootstrap opens the store (creating .odo/)
	// before generateAgentsMD writes.
	if err := os.MkdirAll(filepath.Join(root, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.generateAgentsMD()
	agents, err := os.ReadFile(filepath.Join(root, ".odo", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agents), "odo journal folded|range|tail") {
		t.Error("AGENTS.md project rules do not name the journal pull path")
	}
}
