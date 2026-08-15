package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
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
// two topics, every bullet carrying a workstream-qualified
// (<ws>-epoch-N) citation (M12: bare (epoch-N) cites collide across
// workstreams; the daemon validates + repairs them before writing).
const curatorStubJSON = `{"topics":[
  {"title":"Authentication","slug":"authentication","bullets":["- JWT auth with refresh at /auth/refresh (main-epoch-1)","- Token TTL is 15 minutes (main-epoch-2)"]},
  {"title":"Build System","slug":"build-system","bullets":["- Boring build over clever (feature-epoch-1)"]}
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
	wantAuth := "# Authentication\n\n- JWT auth with refresh at /auth/refresh (main-epoch-1)\n- Token TTL is 15 minutes (main-epoch-2)\n"
	if got := readFileStr(t, authPath); got != wantAuth {
		t.Errorf("authentication.md = %q, want %q", got, wantAuth)
	}
	if got := readFileStr(t, authPath); !strings.Contains(got, "(main-epoch-1)") || !strings.Contains(got, "(main-epoch-2)") {
		t.Errorf("authentication.md is missing (<ws>-epoch-N) citations: %q", got)
	}
	wantBuild := "# Build System\n\n- Boring build over clever (feature-epoch-1)\n"
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
	if curates[0]["topics"] != float64(2) {
		t.Errorf("curate payload = %v, want topics 2", curates[0])
	}
	// M12: the marker carries input provenance — every note shown to the
	// curator with its content hash — plus trigger + gate.
	if curates[0]["trigger"] != "manual" || curates[0]["gate"] != "pass" {
		t.Errorf("curate marker = %v, want trigger manual, gate pass", curates[0])
	}
	notesRead, ok := curates[0]["notes_read"].([]interface{})
	if !ok || len(notesRead) != 3 {
		t.Fatalf("notes_read = %v, want 3 note entries", curates[0]["notes_read"])
	}
	wantShas := map[string]string{
		"main-epoch-1":    sha16([]byte("# Epoch 1 (main)\n\nAuthentication uses JWT with refresh tokens at /auth/refresh.\n")),
		"main-epoch-2":    sha16([]byte("# Epoch 2 (main)\n\nToken TTL set to 15 minutes.\n")),
		"feature-epoch-1": sha16([]byte("# Epoch 1 (feature)\n\nKeep the build boring.\n")),
	}
	for _, raw := range notesRead {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("notes_read entry = %v, want an object", raw)
		}
		name, _ := entry["name"].(string)
		if want, ok := wantShas[name]; !ok || entry["sha16"] != want {
			t.Errorf("notes_read entry %q sha16 = %v, want %q", name, entry["sha16"], want)
		}
		delete(wantShas, name)
	}
	if len(wantShas) != 0 {
		t.Errorf("notes_read missing entries: %v", wantShas)
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
	// The bare (epoch-1) citation is unambiguous here (only main-epoch-1
	// exists) — the daemon repairs it to the qualified form (M12).
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
	if !strings.Contains(got, "JWT auth with refresh at /auth/refresh (main-epoch-1)") {
		t.Errorf("new topic page = %q, want the repaired qualified citation", got)
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
	if len(receipt) != 2 || receipt["wiki/index.md"] != sha16([]byte(indexContent)) ||
		receipt["odo#memory-map"] != sha16([]byte(memoryMapBlock(root))) {
		t.Errorf("receipt = %v, want {wiki/index.md: %s, odo#memory-map: %s}",
			receipt, sha16([]byte(indexContent)), sha16([]byte(memoryMapBlock(root))))
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
		".odo/memory.md":  "46eb86bbcdf4eeda",
		".odo/pins.md":    "4ee15cc70447e2dd",
		"wiki/index.md":   "beb991d524a9dab3",
		notePath:          "f95c368d357f9829",
		"odo#memory-map":  sha16([]byte(memoryMapBlock(root))),
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
	writeNote(t, root, "main-epoch-2", "# Epoch 2\n\nToken TTL.\n")
	writeNote(t, root, "feature-epoch-1", "# Epoch 1 (feature)\n\nBoring build.\n")
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
// the frontend's detection regex (workstream-qualified since M12, the same
// pattern pinned in WikiBrowser.tsx, with a legacy bare fallback)
// classifies cited bullets as cited and uncited ones as uncited. Uncited
// bullets are still served (flagging, not removal).
func TestUncitedBulletDetection(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	content := "# Authentication\n\n" +
		"- JWT auth with refresh at /auth/refresh (main-epoch-1)\n" +
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
	// The exact regexes the frontend applies per bullet line (M12
	// qualified form + legacy bare fallback, same as WikiBrowser.tsx).
	citationRe := regexp.MustCompile(`\([A-Za-z0-9][A-Za-z0-9._-]*-epoch-\d+\)$`)
	legacyRe := regexp.MustCompile(`\(epoch-\d+\)$`)
	var bullets, cited, uncited int
	for _, line := range strings.Split(strings.TrimRight(got.WikiContent, "\n"), "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		bullets++
		if citationRe.MatchString(line) || legacyRe.MatchString(line) {
			cited++
		} else {
			uncited++
		}
	}
	if bullets != 2 || cited != 1 || uncited != 1 {
		t.Errorf("bullet classification = %d bullets (%d cited, %d uncited), want 2 (1 cited, 1 uncited)", bullets, cited, uncited)
	}
}

// TestCurateWriteFailureJournals: a write failure AFTER the parse (here a
// FILE sits at wiki/topics, so creating the topics dir fails) journals
// memory_update{layer:"curator", cause:"failed", detail:"write error: …"}
// — closing the asymmetry where only run/parse failures reached the
// journal — and still returns the error.
func TestCurateWriteFailureJournals(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", curatorStubJSON)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	// The stub's qualified citations must resolve (M12 liveness gate runs
	// before any write) so the failure lands on the write path itself.
	writeNote(t, root, "main-epoch-1", "# Epoch 1\n\nAuthentication uses JWT with refresh tokens.\n")
	writeNote(t, root, "main-epoch-2", "# Epoch 2\n\nToken TTL.\n")
	writeNote(t, root, "feature-epoch-1", "# Epoch 1 (feature)\n\nBoring build.\n")
	// A FILE at wiki/topics makes the topics-dir creation fail.
	if err := os.WriteFile(filepath.Join(root, "wiki", "topics"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := rig.callExpectErr(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})
	if !strings.Contains(resp.Error, "create topics dir") {
		t.Errorf("curate write failure: error = %q, want the topics-dir creation failure", resp.Error)
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	failed := memoryUpdatesByCause(t, events, "failed")
	if len(failed) != 1 {
		t.Fatalf("memory_update{cause:failed} count = %d, want 1", len(failed))
	}
	if failed[0]["layer"] != "curator" {
		t.Errorf("failed memory_update layer = %v, want curator", failed[0]["layer"])
	}
	detail, _ := failed[0]["detail"].(string)
	if !strings.Contains(detail, "write error:") {
		t.Errorf("failed memory_update detail = %q, want \"write error: …\"", detail)
	}
}

// TestCurateDuplicateSlugs: two topics sharing one slug write ONE page —
// the first occurrence wins, the duplicate is skipped (never a silent
// overwrite) — and the index lists the slug exactly once. The journaled
// review_action reports the WRITTEN topic count, not the raw list length.
func TestCurateDuplicateSlugs(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", `{"topics":[
	  {"title":"Authentication","slug":"authentication","bullets":["- first page wins (epoch-1)"]},
	  {"title":"Authentication Again","slug":"authentication","bullets":["- duplicate must be skipped (epoch-2)"]},
	  {"title":"Build System","slug":"build-system","bullets":["- boring build (epoch-1)"]}
	]}`)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	writeNote(t, root, "main-epoch-1", "# Epoch 1\n\nAuth and build notes.\n")
	writeNote(t, root, "main-epoch-2", "# Epoch 2\n\nMore auth notes.\n")

	rig.call(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})

	got := readFileStr(t, filepath.Join(root, "wiki", "topics", "authentication.md"))
	// The bare (epoch-N) citations were unambiguous and got repaired
	// (M12); the duplicate slug still loses to the first occurrence.
	if !strings.Contains(got, "first page wins (main-epoch-1)") {
		t.Errorf("authentication.md = %q, want the first occurrence's page", got)
	}
	if strings.Contains(got, "duplicate must be skipped") {
		t.Errorf("authentication.md = %q, the duplicate slug overwrote the first page", got)
	}
	indexContent := readFileStr(t, filepath.Join(root, "wiki", "index.md"))
	if n := strings.Count(indexContent, "→ topics/authentication.md"); n != 1 {
		t.Errorf("index lists the duplicate slug %d times, want exactly 1: %q", n, indexContent)
	}
	if !strings.Contains(indexContent, "→ topics/build-system.md") {
		t.Errorf("index is missing the surviving second topic: %q", indexContent)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	curates := payloadsByAction(t, events, "curate")
	if len(curates) != 1 || curates[0]["topics"] != float64(2) {
		t.Errorf("review_action{action:curate} = %v, want 1 event with topics 2 (written count, not raw 3)", curates)
	}
}

// TestCurateEmptyTopicsGuard: a {"topics":[]} curator answer refuses BEFORE
// the stale-clear — existing topic pages and the index survive — and the
// refusal is journaled like any other curator failure
// (memory_update{layer:"curator", cause:"failed", detail:"empty topics"}).
func TestCurateEmptyTopicsGuard(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", `{"topics":[]}`)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	writeNote(t, root, "main-epoch-1", "# Epoch 1\n\nAuthentication uses JWT with refresh tokens.\n")
	existingTopic := "# Authentication\n\n- pre-existing page (epoch-1)\n"
	topicPath := filepath.Join(root, "wiki", "topics", "authentication.md")
	if err := os.MkdirAll(filepath.Dir(topicPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(topicPath, []byte(existingTopic), 0o644); err != nil {
		t.Fatal(err)
	}
	existingIndex := "# Project Wiki Index\n\n## Topics\n- Authentication → topics/authentication.md\n"
	indexPath := filepath.Join(root, "wiki", "index.md")
	if err := os.WriteFile(indexPath, []byte(existingIndex), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := rig.callExpectErr(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})
	if !strings.Contains(resp.Error, "0 topics") {
		t.Errorf("empty-topics refusal: error = %q, want mention of 0 topics", resp.Error)
	}
	if got := readFileStr(t, topicPath); got != existingTopic {
		t.Errorf("existing topic page = %q after the refusal, want it preserved %q", got, existingTopic)
	}
	if got := readFileStr(t, indexPath); got != existingIndex {
		t.Errorf("existing index = %q after the refusal, want it preserved %q", got, existingIndex)
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	failed := memoryUpdatesByCause(t, events, "failed")
	if len(failed) != 1 {
		t.Fatalf("memory_update{cause:failed} count = %d, want 1", len(failed))
	}
	if failed[0]["layer"] != "curator" || failed[0]["detail"] != "empty topics" {
		t.Errorf("failed memory_update = %v, want layer curator + detail \"empty topics\"", failed[0])
	}
}

// TestCurateSkipsRetractedNotes (M17 F4, P0 review K3): a retracted note
// never reaches the curator prompt (it is out of recall; a curate built
// on it would revive retracted claims) and the marker's notes_read proves
// it was never shown; the all-retracted project errors out instead of
// curating nothing.
func TestCurateSkipsRetractedNotes(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	setOneShotEnv(t, "ODO_CURATOR_OUTPUT", curatorStubJSON)
	promptCopy := filepath.Join(t.TempDir(), "curator-prompt.txt")
	t.Setenv("ODO_CURATE_PROMPT_COPY", promptCopy)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	// Focused curator contract: one topic citing only main-epoch-2 — the
	// shared curatorStubJSON cites (feature-epoch-1), absent here, and the
	// citation gate would abort the pass this test needs to observe.
	contractPath := filepath.Join(t.TempDir(), "curator-out.json")
	if err := os.WriteFile(contractPath, []byte(`{"topics":[{"title":"Sessions","slug":"sessions","bullets":["- sessions replaced auth (main-epoch-2)"]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODO_CURATOR_OUTPUT", contractPath)

	writeNote(t, root, "main-epoch-1", "# Epoch 1\n\nRETRACTED_FACT_TOKEN auth.\n")
	writeNote(t, root, "main-epoch-2", "# Epoch 2\n\nKEPT_FACT_TOKEN sessions.\n")
	// Journal the epoch-1 retraction directly (the recall/curate
	// derivation reads these rows).
	if _, err := rig.store.AppendEvent(context.Background(), convID, "memory_update",
		mustJSON(map[string]interface{}{
			"layer": "note", "cause": "retract",
			"detail": "main-epoch-1 contradicted by main-epoch-2: auth replaced",
		})); err != nil {
		t.Fatal(err)
	}

	if resp := rig.call(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID}); resp.Error != "" {
		t.Fatalf("curate: %s", resp.Error)
	}
	raw, err := os.ReadFile(promptCopy)
	if err != nil {
		t.Fatalf("prompt copy: %v", err)
	}
	prompt := string(raw)
	if strings.Contains(prompt, "RETRACTED_FACT_TOKEN") {
		t.Error("curator prompt carried the retracted note's content")
	}
	if !strings.Contains(prompt, "KEPT_FACT_TOKEN") {
		t.Error("curator prompt missing the unretracted note's content")
	}
	var marker map[string]interface{}
	for _, m := range payloadsByAction(t, allEvents(t, rig, convID), "curate") {
		marker = m
	}
	if nr, ok := marker["notes_read"].([]interface{}); !ok || len(nr) != 1 {
		t.Errorf("marker notes_read = %v, want the single unretracted note", marker["notes_read"])
	}

	// All retracted: the curate refuses with an explicit error.
	root2 := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
	rig2 := startRig(t, root2)
	defer rig2.stop(t)
	conv2 := rig2.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root2}).Conversation.ID
	writeNote(t, root2, "main-epoch-1", "# Epoch 1\n\nOnly note.\n")
	if _, err := rig2.store.AppendEvent(context.Background(), conv2, "memory_update",
		mustJSON(map[string]interface{}{
			"layer": "note", "cause": "retract",
			"detail": "main-epoch-1 contradicted by main-epoch-2: gone",
		})); err != nil {
		t.Fatal(err)
	}
	resp := rig2.callExpectErr(t, Request{Cmd: CmdCurate, ProjectRoot: root2, ConversationID: conv2})
	if !strings.Contains(resp.Error, "every epoch note stands retracted") {
		t.Errorf("all-retracted curate error = %q, want the unretract hint", resp.Error)
	}
}

// TestCuratorViaMoa (R-W3) covers prefs `curator_via: moa`: the curator
// one-shot goes through one direct moa.Query — the exact wire request is
// capturable and its receipts (via/model/request_sha16/request_bytes +
// output budget) land additively on the pass marker — while a truncated
// answer fails the whole curate closed before any page rewrite, and
// explicit-"omp"/unknown values keep the OMP wrapper route byte-identical.
func TestCuratorViaMoa(t *testing.T) {
	t.Run("moa route journals wire receipts with the pass marker", func(t *testing.T) {
		root := initRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
		// The curator answer comes from the moa stub, NOT the wrapper:
		// ODO_CURATOR_OUTPUT is deliberately unset, so any reroute back to
		// the wrapper fails loudly.
		srv, calls := startPassMoaStub(t, curatorStubJSON, false)
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		writePrefs(t, home, "curator_via: moa\norchestrator: orch-m3k@test\n")
		rig := startRig(t, root)
		defer rig.stop(t)

		convID := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root}).Conversation.ID
		writeNote(t, root, "main-epoch-1", "# Epoch 1 (main)\n\nAuthentication uses JWT with refresh tokens at /auth/refresh.\n")
		writeNote(t, root, "main-epoch-2", "# Epoch 2 (main)\n\nToken TTL set to 15 minutes.\n")
		writeNote(t, root, "feature-epoch-1", "# Epoch 1 (feature)\n\nKeep the build boring.\n")

		resp := rig.call(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})
		if resp.WikiPath != "wiki/index.md" {
			t.Fatalf("curate wiki_path = %q, want wiki/index.md", resp.WikiPath)
		}

		// Pages + index rewritten identically to the wrapper route.
		wantAuth := "# Authentication\n\n- JWT auth with refresh at /auth/refresh (main-epoch-1)\n- Token TTL is 15 minutes (main-epoch-2)\n"
		if got := readFileStr(t, filepath.Join(root, "wiki", "topics", "authentication.md")); got != wantAuth {
			t.Errorf("authentication.md = %q, want %q", got, wantAuth)
		}

		// Route: exactly one moa.Query, on the prefs orchestrator model,
		// carrying the self-contained curator prompt + every note.
		got := calls()
		if len(got) != 1 {
			t.Fatalf("moa calls = %d, want 1", len(got))
		}
		if got[0].model != "orch-m3k" {
			t.Errorf("model = %q, want orch-m3k", got[0].model)
		}
		if !strings.Contains(got[0].prompt, "memory curator pass") ||
			!strings.Contains(got[0].prompt, "main-epoch-2") ||
			!strings.Contains(got[0].prompt, "feature-epoch-1") {
			t.Errorf("prompt missing curator instruction/notes: %.160q", got[0].prompt)
		}

		// Receipts on the pass marker, wire-exact: recomputed from the
		// body the stub received, independently of the stamping client.
		curates := payloadsByAction(t, allEvents(t, rig, convID), "curate")
		if len(curates) != 1 {
			t.Fatalf("curate markers = %d, want 1", len(curates))
		}
		m := curates[0]
		if m["via"] != "moa" || m["model"] != "orch-m3k" {
			t.Errorf("route = %v/%v, want moa/orch-m3k", m["via"], m["model"])
		}
		if m["request_sha16"] != sha16(got[0].body) {
			t.Errorf("request_sha16 = %v, want sha16 of the wire body", m["request_sha16"])
		}
		if m["request_bytes"] != float64(len(got[0].body)) {
			t.Errorf("request_bytes = %v, want %d", m["request_bytes"], len(got[0].body))
		}
		if m["budget"] != float64(16384) || m["output_tokens"] != float64(321) {
			t.Errorf("budget/output_tokens = %v/%v, want 16384/321", m["budget"], m["output_tokens"])
		}
		if _, present := m["escalations"]; present {
			t.Errorf("escalations present on a clean end_turn: %v", m["escalations"])
		}

		// No OMP process served the curator: nothing rendered the curator
		// instruction through the wrapper.
		matches, err := filepath.Glob(filepath.Join(root, ".odo", "prompts", "*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		for _, fp := range matches {
			b, _ := os.ReadFile(fp)
			if strings.Contains(string(b), "memory curator pass") {
				t.Errorf("curator prompt %s went through the OMP wrapper on the moa route", fp)
			}
		}
	})

	t.Run("truncated answer fails the curate closed", func(t *testing.T) {
		root := initRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
		srv, calls := startPassMoaStub(t, curatorStubJSON, true)
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		writePrefs(t, home, "curator_via: moa\norchestrator: orch-m3k@test\n")
		rig := startRig(t, root)
		defer rig.stop(t)

		convID := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root}).Conversation.ID
		writeNote(t, root, "main-epoch-1", "# Epoch 1\n\nFacts.\n")

		resp := rig.callExpectErr(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})
		if !strings.Contains(resp.Error, "curator truncated at the 32768-token hard cap after 1 escalation(s)") ||
			!strings.Contains(resp.Error, "nothing written") {
			t.Errorf("error = %q, want the truncation fail-closed message", resp.Error)
		}
		// One escalation: max_tokens at the 16384 default, re-issue at
		// doubled budget, still truncated → closed.
		if got := calls(); len(got) != 2 || got[0].maxTok != 16384 || got[1].maxTok != 32768 {
			t.Errorf("calls = %+v, want 16384 → 32768 escalate-then-close", got)
		}
		// Nothing written: no topic pages, no index, and no pass marker —
		// one journaled failed row is the durable trace.
		matches, _ := filepath.Glob(filepath.Join(root, "wiki", "topics", "*.md"))
		if len(matches) != 0 {
			t.Errorf("topic pages written after a truncated answer: %v", matches)
		}
		if _, err := os.Stat(filepath.Join(root, "wiki", "index.md")); !os.IsNotExist(err) {
			t.Errorf("index.md written after a truncated answer: %v", err)
		}
		if n := len(payloadsByAction(t, allEvents(t, rig, convID), "curate")); n != 0 {
			t.Errorf("curate pass markers = %d, want none (fail-closed)", n)
		}
		failed := false
		for _, ev := range allEvents(t, rig, convID) {
			if ev.Type != store.EventMemoryUpdate {
				continue
			}
			var p map[string]interface{}
			if json.Unmarshal(ev.Payload, &p) == nil && p["layer"] == "curator" && p["cause"] == "failed" {
				failed = true
				if !strings.Contains(fmt.Sprint(p["detail"]), "truncated") {
					t.Errorf("failed detail = %v, want the truncation fact", p["detail"])
				}
			}
		}
		if !failed {
			t.Error("memory_update{layer:curator,cause:failed} missing")
		}
	})

	t.Run("explicit omp and unknown values keep the OMP route", func(t *testing.T) {
		root := initRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, curatorFlowWrapper))
		setOneShotEnv(t, "ODO_CURATOR_OUTPUT", curatorStubJSON)
		// A moa stub that FAILS the test if ever called proves no reroute.
		var moaCalled atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			moaCalled.Store(true)
			w.WriteHeader(http.StatusTeapot)
		}))
		defer srv.Close()
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		rig := startRig(t, root)
		defer rig.stop(t)

		convID := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root}).Conversation.ID
		writeNote(t, root, "main-epoch-1", "# Epoch 1 (main)\n\nAuthentication uses JWT with refresh tokens at /auth/refresh.\n")
		writeNote(t, root, "main-epoch-2", "# Epoch 2 (main)\n\nToken TTL set to 15 minutes.\n")
		writeNote(t, root, "feature-epoch-1", "# Epoch 1 (feature)\n\nKeep the build boring.\n")

		curateOnce := func(label string) {
			resp := rig.call(t, Request{Cmd: CmdCurate, ProjectRoot: root, ConversationID: convID})
			if resp.WikiPath != "wiki/index.md" {
				t.Fatalf("%s curate failed: %+v", label, resp)
			}
			curates := payloadsByAction(t, allEvents(t, rig, convID), "curate")
			last := curates[len(curates)-1]
			for _, key := range []string{"via", "model", "request_sha16", "request_bytes", "output_tokens", "budget"} {
				if _, present := last[key]; present {
					t.Errorf("%s curate marker carries moa receipt key %q on the OMP route", label, key)
				}
			}
		}
		writePrefs(t, home, "curator_via: omp\n")
		curateOnce("explicit-omp")
		writePrefs(t, home, "curator_via: warp\n")
		curateOnce("unknown-value")
		if moaCalled.Load() {
			t.Error("moa gateway called on an OMP-route curate")
		}
		// The wrapper rendered both curator prompts.
		count := 0
		matches, err := filepath.Glob(filepath.Join(root, ".odo", "prompts", "*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		for _, fp := range matches {
			b, _ := os.ReadFile(fp)
			if strings.Contains(string(b), "memory curator pass") {
				count++
			}
		}
		if count != 2 {
			t.Errorf("curator prompt files = %d, want 2 (wrapper-served)", count)
		}
	})
}
