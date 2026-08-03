package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// M5 Curation tests. Two seams:
//   - the curator one-shot runs through the OMP wrapper stub: the wrapper
//     detects the curator prompt ("memory curator pass"), saves a copy of
//     the prompt when ODO_CURATE_PROMPT_COPY is set, and returns the JSON
//     contract pinned in ODO_CURATOR_OUTPUT (same env seams as
//     learnerFlowWrapper);
//   - prompt injection is observed through the user_message recall/receipt
//     payload and the run's diff (the hello.txt branch copies the prompt).
//
// Every test pins HOME to a temp dir (user.md/injection hermeticity); rigs
// leave ODO_REGISTRY_PATH to startRig's temp default.

// curatorFlowWrapper serves every prompt the daemon builds: the M5 curator
// one-shot (JSON contract from ODO_CURATOR_OUTPUT), the distill one-shot
// (note body from ODO_DISTILL_OUTPUT), and the plain agent run (hello.txt,
// which makes the run's diff carry the prompt verbatim).
const curatorFlowWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
if grep -q "memory curator pass" "$prompt_file"; then
  if [ -n "$ODO_CURATE_PROMPT_COPY" ]; then
    cp "$prompt_file" "$ODO_CURATE_PROMPT_COPY"
  fi
  cat "$ODO_CURATOR_OUTPUT" > "$output_file"
  exit 0
fi
if grep -q "Summarize the key decisions" "$prompt_file"; then
  cat "$ODO_DISTILL_OUTPUT" > "$output_file"
  exit 0
fi
sleep 1
cp "$prompt_file" hello.txt
printf 'Created hello.txt as requested.\n' > "$output_file"
exit 0
`

// curatorStubJSON is the shape-conformant curator answer used by most tests:
// two topics, every bullet carrying an (epoch-N) citation.
const curatorStubJSON = `{"topics":[
  {"title":"Authentication","slug":"authentication","bullets":["- JWT auth with refresh at /auth/refresh (epoch-1)","- Token TTL is 15 minutes (epoch-2)"]},
  {"title":"Build System","slug":"build-system","bullets":["- Boring build over clever (epoch-1)"]}
]}`

// writeNote seeds one epoch note on disk (what distill would have written).
func writeNote(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, "wiki", name+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCurateRewritesTopicPages covers Demo A: 3 epoch notes across 2
// workstreams, a shape-conformant curator one-shot → topic pages written
// with their (epoch-N) citations, index.md regenerated ≤2 KB with the topic
// list, and review_action{action:"curate"} + memory_update{layer:"index",
// cause:"curate"} journaled.
func TestCurateRewritesTopicPages(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", curatorStubJSON)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	writeNote(t, root, "main-epoch-1", "# Epoch 1 (main)\n\nAuthentication uses JWT with refresh tokens at /auth/refresh.\n")
	writeNote(t, root, "main-epoch-2", "# Epoch 2 (main)\n\nToken TTL set to 15 minutes.\n")
	writeNote(t, root, "feature-epoch-1", "# Epoch 1 (feature)\n\nKeep the build boring.\n")

	resp := rig.call(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})
	if resp.WikiPath != "wiki/index.md" {
		t.Errorf("curate wiki_path = %q, want wiki/index.md", resp.WikiPath)
	}

	authPath := filepath.Join(root, "wiki", "topics", "authentication.md")
	wantAuth := "# Authentication\n\n- JWT auth with refresh at /auth/refresh (epoch-1)\n- Token TTL is 15 minutes (epoch-2)\n"
	if got := readFileStr(t, authPath); got != wantAuth {
		t.Errorf("authentication.md = %q, want %q", got, wantAuth)
	}
	if got := readFileStr(t, authPath); !strings.Contains(got, "(epoch-1)") || !strings.Contains(got, "(epoch-2)") {
		t.Errorf("authentication.md is missing (epoch-N) citations: %q", got)
	}
	wantBuild := "# Build System\n\n- Boring build over clever (epoch-1)\n"
	if got := readFileStr(t, filepath.Join(root, "wiki", "topics", "build-system.md")); got != wantBuild {
		t.Errorf("build-system.md = %q, want %q", got, wantBuild)
	}

	indexContent := readFileStr(t, filepath.Join(root, "wiki", "index.md"))
	wantIndex := "# Project Wiki Index\n\n## Topics\n" +
		"- Authentication → topics/authentication.md\n" +
		"- Build System → topics/build-system.md\n"
	if indexContent != wantIndex {
		t.Errorf("index.md = %q, want %q", indexContent, wantIndex)
	}
	if len(indexContent) > indexCap {
		t.Errorf("index.md = %d bytes, exceeds indexCap %d", len(indexContent), indexCap)
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	curates := payloadsByAction(t, events, "curate")
	if len(curates) != 1 {
		t.Fatalf("review_action{action:curate} count = %d, want 1", len(curates))
	}
	if curates[0]["topics"] != float64(2) || curates[0]["notes_read"] != float64(3) {
		t.Errorf("curate payload = %v, want topics 2, notes_read 3", curates[0])
	}
	updates := memoryUpdatesByCause(t, events, "curate")
	if len(updates) != 1 {
		t.Fatalf("memory_update{cause:curate} count = %d, want 1", len(updates))
	}
	u := updates[0]
	if u["layer"] != "index" {
		t.Errorf("memory_update layer = %v, want index", u["layer"])
	}
	if u["before_sha"] != sha16([]byte("")) || u["after_sha"] != sha16([]byte(indexContent)) {
		t.Errorf("memory_update shas = %v → %v, want %s → %s",
			u["before_sha"], u["after_sha"], sha16([]byte("")), sha16([]byte(indexContent)))
	}
	if u["detail"] != "rewrote 2 topics + index" {
		t.Errorf("memory_update detail = %v, want \"rewrote 2 topics + index\"", u["detail"])
	}
}

// TestCurateGeneration2Rule: a pre-existing wiki/topics/old.md is removed on
// the next pass, and the rewritten pages carry only what the source notes
// said — the curator never reads the previous topic page (generation-2
// rule), so a fabricated bullet cannot survive.
func TestCurateGeneration2Rule(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", `{"topics":[{"title":"Authentication","slug":"authentication","bullets":["- JWT auth with refresh at /auth/refresh (epoch-1)"]}]}`)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	writeNote(t, root, "main-epoch-1", "# Epoch 1\n\nAuthentication uses JWT with refresh tokens at /auth/refresh.\n")
	oldPath := filepath.Join(root, "wiki", "topics", "old.md")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("# Old\n\n- FABRICATED: the moon is cheese (epoch-42)\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rig.call(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old.md still present after curate — stale topic pages must be cleared")
	}
	got := readFileStr(t, filepath.Join(root, "wiki", "topics", "authentication.md"))
	if strings.Contains(got, "FABRICATED") {
		t.Errorf("new topic page contains the fabricated bullet: %q", got)
	}
	if !strings.Contains(got, "JWT auth with refresh at /auth/refresh (epoch-1)") {
		t.Errorf("new topic page = %q, want the content sourced from the notes only", got)
	}
}

// TestCurateNoteCap: with 60 epoch notes the curator prompt carries exactly
// curatorNoteCap (50), newest-first — verified against the prompt file the
// wrapper stub received, not the model's answer.
func TestCurateNoteCap(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", `{"topics":[{"title":"T","slug":"t","bullets":["- x (epoch-60)"]}]}`)
	promptCopy := filepath.Join(t.TempDir(), "curator-prompt.txt")
	t.Setenv("ODO_CURATE_PROMPT_COPY", promptCopy)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	for n := 1; n <= 60; n++ {
		writeNote(t, root, fmt.Sprintf("main-epoch-%d", n), fmt.Sprintf("# Epoch %d\n\nnote %d body\n", n, n))
	}

	notes, err := allEpochNotes(root)
	if err != nil {
		t.Fatalf("allEpochNotes: %v", err)
	}
	if len(notes) != curatorNoteCap {
		t.Fatalf("allEpochNotes = %d notes, want curatorNoteCap %d", len(notes), curatorNoteCap)
	}
	if notes[0].epoch != 60 || notes[len(notes)-1].epoch != 11 {
		t.Errorf("note order = %d … %d, want newest-first 60 … 11", notes[0].epoch, notes[len(notes)-1].epoch)
	}

	rig.call(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: boot.Conversation.ID})

	prompt := readFileStr(t, promptCopy)
	if got := strings.Count(prompt, "(workstream: main, epoch:"); got != curatorNoteCap {
		t.Errorf("curator prompt carries %d note headers, want %d", got, curatorNoteCap)
	}
	if !strings.Contains(prompt, "--- main-epoch-60 (workstream: main, epoch: 60) ---") {
		t.Error("curator prompt is missing the newest note (epoch 60)")
	}
	if !strings.Contains(prompt, "--- main-epoch-11 (workstream: main, epoch: 11) ---") {
		t.Error("curator prompt is missing the 50th note (epoch 11)")
	}
	if strings.Contains(prompt, "--- main-epoch-10 (workstream: main, epoch: 10) ---") {
		t.Error("curator prompt contains epoch 10 — the cap must exclude it")
	}
	if strings.Contains(prompt, "--- main-epoch-1 (workstream: main, epoch: 1) ---") {
		t.Error("curator prompt contains epoch 1 — the cap must exclude it")
	}
	if i60, i11 := strings.Index(prompt, "main-epoch-60 ("), strings.Index(prompt, "main-epoch-11 ("); i60 < 0 || i11 <= i60 {
		t.Errorf("prompt order: epoch 60 at %d, epoch 11 at %d — want newest-first", i60, i11)
	}
}

// TestCurateEmptyProjectErrors: a project with zero epoch notes refuses —
// nothing to synthesize, nothing written, nothing journaled.
func TestCurateEmptyProjectErrors(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", curatorStubJSON)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	resp := rig.callExpectErr(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: boot.Conversation.ID})
	if !strings.Contains(resp.Error, "no epoch notes to curate") {
		t.Errorf("curate empty project: error = %q, want mention of no epoch notes", resp.Error)
	}
	if events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: boot.Conversation.ID, AfterSeq: 0}).Events; len(events) != 0 {
		t.Errorf("journaled events on a refused curate = %v, want none", eventTypes(events))
	}
}

// TestCurateFailureJournalsAndErrors: a non-JSON curator answer journals
// memory_update{layer:"curator", cause:"failed"} AND errors the command —
// curate is a standalone command, unlike the learner's silent degradation.
func TestCurateFailureJournalsAndErrors(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", "this is not json at all")
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	writeNote(t, root, "main-epoch-1", "# Epoch 1\n\nAuthentication uses JWT with refresh tokens.\n")

	resp := rig.callExpectErr(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})
	if !strings.Contains(resp.Error, "no JSON object in curator output") {
		t.Errorf("curate failure: error = %q, want the parse failure", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(root, "wiki", "index.md")); !os.IsNotExist(err) {
		t.Error("index.md written despite the curator parse failure")
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	failed := memoryUpdatesByCause(t, events, "failed")
	if len(failed) != 1 {
		t.Fatalf("memory_update{cause:failed} count = %d, want 1", len(failed))
	}
	if failed[0]["layer"] != "curator" {
		t.Errorf("failed memory_update layer = %v, want curator", failed[0]["layer"])
	}
	if detail, _ := failed[0]["detail"].(string); !strings.Contains(detail, "no JSON object") {
		t.Errorf("failed memory_update detail = %q, want the parse error", detail)
	}
}

// TestIndexInjectedIntoPrompt: wiki/index.md rides as the "## Wiki index"
// layer — prompt header + sentinel, the recall marker, and a receipt entry
// covering exactly the injected bytes.
func TestIndexInjectedIntoPrompt(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	indexContent := "# Project Wiki Index\n\n## Topics\n- AUTH INDEX SENTINEL → topics/auth-sentinel.md\n"
	indexPath := filepath.Join(root, "wiki", "index.md")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte(indexContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Continue the auth refactor"})
	if recall := recallPathsFromEvent(t, sent.Event); len(recall) != 1 || recall[0] != "wiki/index.md" {
		t.Fatalf("recall = %v, want [wiki/index.md]", recall)
	}
	receipt := receiptFromEvent(t, sent.Event)
	if len(receipt) != 1 || receipt["wiki/index.md"] != sha16([]byte(indexContent)) {
		t.Errorf("receipt = %v, want {wiki/index.md: %s}", receipt, sha16([]byte(indexContent)))
	}

	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}
	if !strings.Contains(done.Diff.Content, "## Wiki index") {
		t.Error("diff content (agent prompt) is missing the \"## Wiki index\" header")
	}
	if !strings.Contains(done.Diff.Content, "AUTH INDEX SENTINEL") {
		t.Error("diff content (agent prompt) is missing the index sentinel")
	}
}

// TestPinsInjectedIntoPrompt: .odo/pins.md rides as the
// "## Pins (user-authored, verbatim)" layer — header + sentinel, recall
// marker, receipt entry.
func TestPinsInjectedIntoPrompt(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	pinsContent := "- PINS SENTINEL: never deploy on Fridays\n"
	if err := os.MkdirAll(filepath.Join(root, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".odo", "pins.md"), []byte(pinsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Continue the auth refactor"})
	if recall := recallPathsFromEvent(t, sent.Event); len(recall) != 1 || recall[0] != ".odo/pins.md" {
		t.Fatalf("recall = %v, want [.odo/pins.md]", recall)
	}
	receipt := receiptFromEvent(t, sent.Event)
	if len(receipt) != 1 || receipt[".odo/pins.md"] != sha16([]byte(pinsContent)) {
		t.Errorf("receipt = %v, want {.odo/pins.md: %s}", receipt, sha16([]byte(pinsContent)))
	}

	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}
	if !strings.Contains(done.Diff.Content, "## Pins (user-authored, verbatim)") {
		t.Error("diff content (agent prompt) is missing the \"## Pins (user-authored, verbatim)\" header")
	}
	if !strings.Contains(done.Diff.Content, "PINS SENTINEL") {
		t.Error("diff content (agent prompt) is missing the pins sentinel")
	}
}

// TestPinCommand covers Demo C: pin → `- <text>` appended to .odo/pins.md
// verbatim, memory_update{layer:"pins", cause:"pin"} journaled with the
// text in the detail; a second pin appends on its own line; an overflow is
// refused with an error naming the pin and nothing written.
func TestPinCommand(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	pinsFile := filepath.Join(root, ".odo", "pins.md")

	// Nothing pinned yet: read_pins comes back empty.
	if got := rig.call(t, Request{Cmd: CmdReadPins, ProjectRoot: root}).MemoryContent; got != "" {
		t.Fatalf("read_pins before any pin = %q, want empty", got)
	}

	resp := rig.call(t, Request{Cmd: CmdPin, ProjectRoot: root, ConversationID: convID, Text: "Never deploy on Fridays."})
	if !resp.Applied {
		t.Fatal("pin: applied must be true")
	}
	if got := readFileStr(t, pinsFile); got != "- Never deploy on Fridays.\n" {
		t.Errorf("pins.md = %q, want %q", got, "- Never deploy on Fridays.\n")
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	pins := memoryUpdatesByCause(t, events, "pin")
	if len(pins) != 1 || pins[0]["layer"] != "pins" || pins[0]["detail"] != "Never deploy on Fridays." {
		t.Fatalf("memory_update{layer:pins, cause:pin} = %v, want 1 event naming the pin text", pins)
	}

	// A second pin lands on its own line; read_pins returns the full file.
	rig.call(t, Request{Cmd: CmdPin, ProjectRoot: root, ConversationID: convID, Text: "No force pushes to main."})
	want := "- Never deploy on Fridays.\n- No force pushes to main.\n"
	if got := readFileStr(t, pinsFile); got != want {
		t.Errorf("pins.md after second pin = %q, want %q", got, want)
	}
	if got := rig.call(t, Request{Cmd: CmdReadPins, ProjectRoot: root}).MemoryContent; got != want {
		t.Errorf("read_pins = %q, want %q", got, want)
	}

	// Overflow: fill the file to just under the cap, then refuse the pin
	// that would push it over — the error names the pin, nothing is written.
	lines := make([]string, 38)
	for i := range lines {
		lines[i] = "- " + strings.Repeat("x", 50)
	}
	full := strings.Join(lines, "\n") + "\n" // 38 × 53 = 2014 bytes
	if err := os.WriteFile(pinsFile, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	fat := strings.Repeat("o", 40)
	resp = rig.callExpectErr(t, Request{Cmd: CmdPin, ProjectRoot: root, ConversationID: convID, Text: fat})
	if !strings.Contains(resp.Error, "would exceed") || !strings.Contains(resp.Error, fat) {
		t.Errorf("overflow refusal = %q, want the cap message naming the pin text", resp.Error)
	}
	if got := readFileStr(t, pinsFile); got != full {
		t.Errorf("pins.md changed despite the refusal: %d bytes, want %d", len(got), len(full))
	}
	events = rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	if pins := memoryUpdatesByCause(t, events, "pin"); len(pins) != 2 {
		t.Errorf("pin events after the refusal = %d, want 2 (refusals never journal)", len(pins))
	}
}

// TestInjectionReceiptWithIndexAndPins pins (literally — frozen sha16
// vectors) the injection receipt for a known layer set, its absence for
// empty layers, and the recall order user.md → memory.md → pins.md →
// index.md → note paths.
func TestInjectionReceiptWithIndexAndPins(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	writeUserMD(t, home, "- Prefer compact output.\n")
	memContent := "- Always run go test before claiming done. — cites: main-epoch-1; reaffirmed: 1\n"
	if err := os.MkdirAll(filepath.Join(root, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".odo", "memory.md"), []byte(memContent), 0o644); err != nil {
		t.Fatal(err)
	}
	pinsContent := "- Never deploy on Fridays.\n"
	if err := os.WriteFile(filepath.Join(root, ".odo", "pins.md"), []byte(pinsContent), 0o644); err != nil {
		t.Fatal(err)
	}
	indexContent := "# Project Wiki Index\n\n## Topics\n- Authentication → topics/authentication.md\n"
	if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wiki", "index.md"), []byte(indexContent), 0o644); err != nil {
		t.Fatal(err)
	}
	noteContent := "# Epoch 1\n\n- JWT auth with refresh at /auth/refresh\n"
	notePath := writeNote(t, root, "main-epoch-1", noteContent)

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Build the next piece"})
	wantRecall := []string{"~/.odo/user.md", ".odo/memory.md", ".odo/pins.md", "wiki/index.md", notePath}
	recall := recallPathsFromEvent(t, sent.Event)
	if len(recall) != len(wantRecall) {
		t.Fatalf("recall = %v, want %v", recall, wantRecall)
	}
	for i := range wantRecall {
		if recall[i] != wantRecall[i] {
			t.Fatalf("recall = %v, want %v (order: user.md → memory.md → pins.md → index.md → notes)", recall, wantRecall)
		}
	}

	// Frozen vectors (sha256[:16] of the exact injected bytes, computed once
	// and frozen here): a drift in rendering, order, or hashing flips a hash.
	receipt := receiptFromEvent(t, sent.Event)
	wantReceipt := map[string]string{
		"~/.odo/user.md": "99ab47d9f8d99c16",
		".odo/memory.md": "46eb86bbcdf4eeda",
		".odo/pins.md":   "4ee15cc70447e2dd",
		"wiki/index.md":  "beb991d524a9dab3",
		notePath:         "f95c368d357f9829",
	}
	if len(receipt) != len(wantReceipt) {
		t.Fatalf("receipt = %v, want %v", receipt, wantReceipt)
	}
	for k, want := range wantReceipt {
		if receipt[k] != want {
			t.Errorf("receipt[%s] = %s, want %s", k, receipt[k], want)
		}
	}

	// The prompt carries the five layers in the same order.
	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}
	prompt := done.Diff.Content
	wantOrder := []string{
		"## User memory (durable cross-project principles)",
		"## Project memory (behavior rules)",
		"## Pins (user-authored, verbatim)",
		"## Wiki index",
		"## Prior notes (recalled)",
	}
	prev := -1
	for _, header := range wantOrder {
		i := strings.Index(prompt, header)
		if i < 0 {
			t.Errorf("prompt is missing header %q", header)
			continue
		}
		if i <= prev {
			t.Errorf("prompt header %q at %d is out of order (previous at %d)", header, i, prev)
		}
		prev = i
	}
}

// TestListTopics: after a curate, list_topics returns one entry per topic
// page — Name carrying the parsed title, Path the page, Epoch 0 (topics are
// not per-epoch notes). An empty project returns an empty list.
func TestListTopics(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", curatorStubJSON)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	if list := rig.call(t, Request{Cmd: CmdListTopics, ProjectRoot: root}); len(list.WikiNotes) != 0 {
		t.Fatalf("list_topics before any curate = %d topics, want 0", len(list.WikiNotes))
	}

	writeNote(t, root, "main-epoch-1", "# Epoch 1\n\nAuthentication uses JWT with refresh tokens.\n")
	rig.call(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})

	list := rig.call(t, Request{Cmd: CmdListTopics, ProjectRoot: root})
	if len(list.WikiNotes) != 2 {
		t.Fatalf("list_topics = %d topics, want 2", len(list.WikiNotes))
	}
	bySlug := map[string]WikiNoteInfo{}
	for _, n := range list.WikiNotes {
		bySlug[strings.TrimSuffix(filepath.Base(n.Path), ".md")] = n
	}
	auth, ok := bySlug["authentication"]
	if !ok {
		t.Fatalf("list_topics = %+v, missing the authentication topic", list.WikiNotes)
	}
	if auth.Name != "Authentication" {
		t.Errorf("topic name = %q, want the parsed title \"Authentication\"", auth.Name)
	}
	if auth.Epoch != 0 {
		t.Errorf("topic epoch = %d, want 0 (topics are not per-epoch notes)", auth.Epoch)
	}
	if wantPath := filepath.Join(root, "wiki", "topics", "authentication.md"); auth.Path != wantPath {
		t.Errorf("topic path = %q, want %q", auth.Path, wantPath)
	}
	if _, err := time.Parse(time.RFC3339, auth.ModifiedAt); err != nil {
		t.Errorf("topic modified_at %q is not RFC3339: %v", auth.ModifiedAt, err)
	}
	if _, ok := bySlug["build-system"]; !ok {
		t.Errorf("list_topics = %+v, missing the build-system topic", list.WikiNotes)
	}
}

// TestReadWikiTopicsPath pins the read_wiki contract the Topics tab relies
// on: the existing wiki/ path guard already serves wiki/topics/*.md.
func TestReadWikiTopicsPath(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	content := "# Authentication\n\n- JWT auth with refresh at /auth/refresh (epoch-1)\n"
	topicPath := filepath.Join(root, "wiki", "topics", "authentication.md")
	if err := os.MkdirAll(filepath.Dir(topicPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(topicPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := rig.call(t, Request{Cmd: CmdReadWiki, Path: topicPath})
	if got.WikiContent != content {
		t.Errorf("read_wiki topic page = %q, want %q", got.WikiContent, content)
	}
}

// TestUncitedBulletDetection pins the contract the browser's uncited-badge
// consumes at the Go level: the daemon serves the topic page verbatim and
// the frontend's detection regex (`\(epoch-\d+\)$`, the same pattern pinned
// in WikiBrowser.tsx) classifies cited bullets as cited and uncited ones as
// uncited. Uncited bullets are still served (flagging, not removal).
func TestUncitedBulletDetection(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	content := "# Authentication\n\n" +
		"- JWT auth with refresh at /auth/refresh (epoch-1)\n" +
		"- summary synthesis that names no source note\n"
	topicPath := filepath.Join(root, "wiki", "topics", "authentication.md")
	if err := os.MkdirAll(filepath.Dir(topicPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(topicPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := rig.call(t, Request{Cmd: CmdReadWiki, Path: topicPath})
	if got.WikiContent != content {
		t.Fatalf("read_wiki topic page = %q, want %q", got.WikiContent, content)
	}
	// The exact regex the frontend applies per bullet line.
	citationRe := regexp.MustCompile(`\(epoch-\d+\)$`)
	var bullets, cited, uncited int
	for _, line := range strings.Split(strings.TrimRight(got.WikiContent, "\n"), "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		bullets++
		if citationRe.MatchString(line) {
			cited++
		} else {
			uncited++
		}
	}
	if bullets != 2 || cited != 1 || uncited != 1 {
		t.Errorf("bullet classification = %d bullets (%d cited, %d uncited), want 2 (1 cited, 1 uncited)", bullets, cited, uncited)
	}
}
