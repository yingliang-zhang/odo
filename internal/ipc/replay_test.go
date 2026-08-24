package ipc

// M6.1 (recall query token union), R4 (cold-start resume card), and the
// journal-pull wiring (G4) tests. R1 replay block construction is exercised
// end-to-end through the send-path integration tests.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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
	for _, want := range []string{"odo journal folded", "odo journal range A B", "odo journal tail N", "odo journal search <terms>", "NOT injected"} {
		if !strings.Contains(block, want) {
			t.Errorf("memoryMapBlock missing %q", want)
		}
	}
	if got := memoryMapBlock(t.TempDir()); got != "" {
		t.Errorf("memoryMapBlock on a fresh project = %q, want empty", got)
	}

	p, _ := distillPrompt(nil)
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
	if !strings.Contains(string(agents), "odo journal folded|range|tail") ||
		!strings.Contains(string(agents), "odo journal search <terms>") {
		t.Error("AGENTS.md project rules do not name the journal pull paths")
	}
}

// agentsMDRig builds the bootstrap-owned layout generateAgentsMD expects:
// a git root with .odo/ materialized (production opens the store first).
func agentsMDRig(t *testing.T) (root, odoDir string) {
	t.Helper()
	root = initRepo(t)
	odoDir = filepath.Join(root, ".odo")
	if err := os.MkdirAll(odoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, odoDir
}

// TestAgentsMDSkipsEscapingRuleSymlink (2026-08-24 tri-review P0): .odo/ is committable, so an implanted memory.md symlink pointing at an
// external secret must degrade to "section absent" — the prompt bridge
// carries NEITHER the secret bytes nor a ## Memory heading — exactly the
// containment the sibling rule readers already had.
func TestAgentsMDSkipsEscapingRuleSymlink(t *testing.T) {
	root, odoDir := agentsMDRig(t)
	external := filepath.Join(t.TempDir(), "secret.md")
	const secret = "EXTERNAL-SECRET-BYTES"
	if err := os.WriteFile(external, []byte(secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(odoDir, "memory.md")); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	(&Server{projectRoot: root}).generateAgentsMD()
	agents, err := os.ReadFile(filepath.Join(odoDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if strings.Contains(string(agents), secret) {
		t.Errorf("AGENTS.md copied external secret bytes through the planted symlink:\n%s", agents)
	}
	if strings.Contains(string(agents), "## Memory") {
		t.Errorf("AGENTS.md rendered a ## Memory section for an escaping symlink:\n%s", agents)
	}
}

// TestAgentsMDReadsSymlinkWithinOdo: the in-dir fast path stays intact —
// a pins.md symlink resolving deeper INSIDE the project-odo root reads
// like a plain file (only escapes degrade).
func TestAgentsMDReadsSymlinkWithinOdo(t *testing.T) {
	root, odoDir := agentsMDRig(t)
	target := filepath.Join(odoDir, "pins-real.md")
	const pins = "pin it down\n"
	if err := os.WriteFile(target, []byte(pins), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(odoDir, "pins.md")); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	(&Server{projectRoot: root}).generateAgentsMD()
	agents, err := os.ReadFile(filepath.Join(odoDir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(agents), "## Pins") || !strings.Contains(string(agents), pins) {
		t.Errorf("AGENTS.md lost the in-dir symlink target's pins:\n%s", agents)
	}
}

// TestAgentsMDRefusesSymlinkWrite: the write-side twin — the daemon owns
// AGENTS.md, so a planted symlink at the path must NOT be followed onto
// the external file it names. generation logs and skips; the external
// file stays byte-unchanged and no error escapes.
func TestAgentsMDRefusesSymlinkWrite(t *testing.T) {
	root, odoDir := agentsMDRig(t)
	external := filepath.Join(t.TempDir(), "sentinel.md")
	const sentinel = "SENTINEL-DO-NOT-OVERWRITE\n"
	if err := os.WriteFile(external, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(odoDir, "AGENTS.md")); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	var buf bytes.Buffer
	prev, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(prevFlags) })

	(&Server{projectRoot: root}).generateAgentsMD() // must not panic or write

	if got, err := os.ReadFile(external); err != nil || string(got) != sentinel {
		t.Errorf("external AGENTS.md target = %q, %v, want the sentinel bytes untouched", got, err)
	}
	if !strings.Contains(buf.String(), "refusing to write through symlink") {
		t.Errorf("log = %q, want the skipped write logged", buf.String())
	}
}

// --- Batch 1, item B: configurable replay caps + actionable omission marker.

// TestReplayOmissionMarkerActionable: the omission marker names the exact
// dropped seq window and its journal pull command, and the dropped window
// comes back for the replay receipt.
func TestReplayOmissionMarkerActionable(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no prefs.md: resolveReplayCaps → defaults
	big := strings.Repeat("x", replayTurnCapDefault-100)
	turns := []replayTurn{
		{seq: 2, role: "user", text: big},
		{seq: 3, role: "agent", text: "ok"},
		{seq: 4, role: "user", text: big},
		{seq: 5, role: "agent", text: "ok"},
		{seq: 6, role: "user", text: big},
		{seq: 7, role: "agent", text: "ok"},
	}
	block, firstSeq, lastSeq, droppedSeqs := renderReplay(turns, resolveReplayCaps())

	if !strings.HasPrefix(block, "## Recent conversation (journal replay: current epoch, seq 3–7)") {
		t.Errorf("header = %q, want covered seq 3–7", block[:min(80, len(block))])
	}
	marker := "1 older turn(s) (seq 2–2) omitted by the 8KB cap; pull with `odo journal range 2 2` or browse the tail via `odo journal tail 200`"
	if !strings.Contains(block, marker) {
		t.Errorf("block missing actionable marker %q", marker)
	}
	if strings.Contains(block, "they remain in the journal") {
		t.Error("old passive omission text must be gone — the marker now names the pull path")
	}
	if fmt.Sprint(droppedSeqs) != "[2 2]" {
		t.Errorf("droppedSeqs = %v, want [2 2]", droppedSeqs)
	}
	if firstSeq != 3 || lastSeq != 7 {
		t.Errorf("firstSeq,lastSeq = %d,%d, want 3,7", firstSeq, lastSeq)
	}
}

// TestResolveReplayCaps: defaults hold without prefs; valid values apply;
// garbage fails closed to the defaults; out-of-range values clamp.
func TestResolveReplayCaps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := resolveReplayCaps(); got.total != 8*1024 || got.turn != 4*1024 {
		t.Errorf("no prefs = %+v, want 8KB/4KB defaults", got)
	}
	writePrefs(t, home, "replay_total_kb: 32\nreplay_turn_kb: 2\n")
	if got := resolveReplayCaps(); got.total != 32*1024 || got.turn != 2*1024 {
		t.Errorf("prefs 32/2 = %+v, want 32KB/2KB", got)
	}
	writePrefs(t, home, "replay_total_kb: garbage\nreplay_turn_kb: x\n")
	if got := resolveReplayCaps(); got.total != 8*1024 || got.turn != 4*1024 {
		t.Errorf("garbage prefs = %+v, want 8KB/4KB defaults (fail closed)", got)
	}
	writePrefs(t, home, "replay_total_kb: 1000\nreplay_turn_kb: 0\n")
	if got := resolveReplayCaps(); got.total != 64*1024 || got.turn != 1*1024 {
		t.Errorf("out-of-range = %+v, want clamped 64KB/1KB", got)
	}
}

// TestRenderReplayHonorsPrefsTotalCap: a raised replay_total_kb actually
// widens the rendered window — 20KB of turns fit at 32KB but drop at the
// 8KB default.
func TestRenderReplayHonorsPrefsTotalCap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "replay_total_kb: 32\n")
	big := strings.Repeat("x", 4000)
	turns := make([]replayTurn, 0, 5)
	for i := 1; i <= 5; i++ {
		turns = append(turns, replayTurn{seq: i, role: "user", text: big})
	}
	if _, _, _, dropped := renderReplay(turns, resolveReplayCaps()); dropped != nil {
		t.Errorf("32KB cap dropped %v, want all 5 turns kept", dropped)
	}
	if _, _, _, dropped := renderReplay(turns, replayCaps{total: replayTotalCapDefault, turn: replayTurnCapDefault}); dropped == nil {
		t.Error("8KB default must drop older turns for the same fixture")
	}
}

// TestReplayDroppedSeqsReceiptAndPromptBytes: end-to-end over the send
// path — the journaled user_message carries the replay dropped_seqs window
// (absent without drops) and total_prompt_bytes, and the prompt's omission
// marker matches the journaled window.
func TestReplayDroppedSeqsReceiptAndPromptBytes(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	type replayReceipt struct {
		FirstSeq    int   `json:"first_seq"`
		LastSeq     int   `json:"last_seq"`
		DroppedSeqs []int `json:"dropped_seqs"`
	}
	type payload struct {
		Replay           *replayReceipt `json:"replay"`
		TotalPromptBytes int            `json:"total_prompt_bytes"`
	}
	readPayload := func(t *testing.T, resp Response) payload {
		t.Helper()
		if resp.Event == nil {
			t.Fatal("send response missing the journaled event")
		}
		var p payload
		if err := json.Unmarshal(resp.Event.Payload, &p); err != nil {
			t.Fatalf("user_message payload: %v", err)
		}
		return p
	}

	big := strings.Repeat("turn ", 1000) // 5KB → per-turn truncation in the replay
	sent1 := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: big})
	rig.pollUntilDone(t, convID)
	// The prompt file the adapter handed the agent is the ground truth the
	// receipt measures.
	prompts, err := filepath.Glob(filepath.Join(root, ".odo", "prompts", "*.txt"))
	if err != nil || len(prompts) != 1 {
		t.Fatalf("prompt files = %v, %v, want exactly 1", prompts, err)
	}
	promptBytes, err := os.ReadFile(prompts[0])
	if err != nil {
		t.Fatal(err)
	}
	if p := readPayload(t, sent1); p.Replay != nil {
		t.Errorf("first send replay = %+v, want nil (nothing to replay)", p.Replay)
	} else if p.TotalPromptBytes != len(promptBytes) {
		t.Errorf("total_prompt_bytes = %d, want %d (assembled prompt)", p.TotalPromptBytes, len(promptBytes))
	}

	sent2 := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: big})
	rig.pollUntilDone(t, convID)
	if p := readPayload(t, sent2); p.Replay == nil {
		t.Fatal("second send replay receipt missing")
	} else if len(p.Replay.DroppedSeqs) != 0 {
		t.Errorf("second send dropped_seqs = %v, want absent (window still fits)", p.Replay.DroppedSeqs)
	}

	sent3 := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "third"})
	done3 := rig.pollUntilDone(t, convID)
	p3 := readPayload(t, sent3)
	if p3.Replay == nil || len(p3.Replay.DroppedSeqs) != 2 {
		t.Fatalf("third send dropped_seqs = %+v, want [first last]", p3.Replay)
	}
	if p3.Replay.DroppedSeqs[0] > p3.Replay.DroppedSeqs[1] {
		t.Errorf("dropped window inverted: %v", p3.Replay.DroppedSeqs)
	}
	if p3.TotalPromptBytes <= 0 {
		t.Errorf("total_prompt_bytes = %d, want > 0", p3.TotalPromptBytes)
	}
	if done3.Diff == nil {
		t.Fatal("no diff — the stub wrote nothing")
	}
	marker := fmt.Sprintf("omitted by the 8KB cap; pull with `odo journal range %d %d` or browse the tail via `odo journal tail 200`",
		p3.Replay.DroppedSeqs[0], p3.Replay.DroppedSeqs[1])
	if !strings.Contains(done3.Diff.Content, marker) {
		t.Errorf("prompt missing actionable omission marker %q", marker)
	}
}

// TestFormatReplayTurnRuneSafe pins the rune-safe per-turn cut: the byte
// cap must not split a multi-byte rune. CJK text (3 bytes/rune) makes a
// mid-rune cut the common case, not the edge, and this renderer is shared
// by the slash conversation tail (renderConvBlock), so one fix covers
// both. The truncated line stays valid UTF-8 and the truncation marker
// still reports the cut.
func TestFormatReplayTurnRuneSafe(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no prefs.md: resolveReplayCaps → defaults
	// replayTurnCapDefault (4096) is not a multiple of 3, so a byte cut of
	// 回-heavy text lands mid-rune — exactly the case that used to corrupt.
	cjk := strings.Repeat("回复", replayTurnCapDefault/3)
	line := formatReplayTurn(replayTurn{seq: 1, role: "user", text: cjk}, resolveReplayCaps().turn)
	if !utf8.ValidString(line) {
		t.Errorf("truncated turn is invalid UTF-8: %q…", line[:min(60, len(line))])
	}
	if !strings.Contains(line, "[truncated at 4KB]") {
		t.Errorf("truncation marker missing from %q…", line[:min(60, len(line))])
	}

	// Block level: a CJK-heavy epoch rendered through renderReplay (the
	// shared renderer) also stays valid UTF-8 and keeps the marker.
	block, _, _, _ := renderReplay([]replayTurn{
		{seq: 1, role: "user", text: cjk},
		{seq: 2, role: "agent", text: cjk},
	}, resolveReplayCaps())
	if !utf8.ValidString(block) {
		t.Error("rendered replay block is invalid UTF-8")
	}
	if !strings.Contains(block, "[truncated at 4KB]") {
		t.Error("block-level truncation marker missing")
	}
}

// TestRecallQuerySeedRuneSafe pins the same rune-safe cut for recall seeds:
// the seed feeds tokenizeQuery (CJK bigrams over range-by-rune), where an
// invalid tail would silently cost the last bigram(s) — on the CJK-primary
// query path that is real term loss, not a cosmetic glitch. The query
// stays valid UTF-8 and the per-turn cap still binds.
func TestRecallQuerySeedRuneSafe(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no prefs.md: resolveReplayCaps → defaults
	// 回复 (6 bytes / 2 runes) repeated past the cap: a raw byte cut can
	// land mid-rune — same shape as TestFormatReplayTurnRuneSafe.
	monster := strings.Repeat("回复", replayTurnCapDefault)
	events := []store.Event{chatEvent(1, store.EventAgentText, monster)}
	q := recallQuery("继续", events)
	if !utf8.ValidString(q) {
		t.Errorf("recall query is invalid UTF-8 (mid-rune seed cut): %q…", q[:min(60, len(q))])
	}
	if len(q) > len("继续")+2+replayTurnCapDefault {
		t.Errorf("recall query = %d bytes, want ≤ %d (message + separator + seed cap)", len(q), len("继续")+2+replayTurnCapDefault)
	}
}
