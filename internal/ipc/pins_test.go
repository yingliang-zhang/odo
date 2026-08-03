package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// M5-hardening pin validation tests (TestPinCommand in curator_test.go
// covers the happy path, the overflow refusal, and injection).

// TestPinRejectsMultiLine: a pin carrying a newline is refused — pins.md is
// one `- <text>` line per pin, so a newline would corrupt the format and
// the downstream line-boundary read. Nothing is written, nothing journaled.
func TestPinRejectsMultiLine(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	pinsFile := filepath.Join(root, ".odo", "pins.md")

	resp := rig.callExpectErr(t, Request{Cmd: CmdPin, ProjectRoot: root, ConversationID: convID, Text: "line1\nline2"})
	if !strings.Contains(resp.Error, "single-line") {
		t.Errorf("multi-line pin refusal = %q, want the single-line message", resp.Error)
	}
	if _, err := os.Stat(pinsFile); !os.IsNotExist(err) {
		t.Error("pins.md written despite the multi-line refusal")
	}
	if events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events; len(events) != 0 {
		t.Errorf("journaled events on a refused pin = %v, want none", eventTypes(events))
	}
}

// TestPinTrimsWhitespace: leading/trailing whitespace is trimmed before the
// pin lands — `  spaced pin  ` stores as `- spaced pin` — and a
// whitespace-only pin is refused as empty (refusals write nothing).
func TestPinTrimsWhitespace(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	pinsFile := filepath.Join(root, ".odo", "pins.md")

	resp := rig.call(t, Request{Cmd: CmdPin, ProjectRoot: root, ConversationID: convID, Text: "  spaced pin  "})
	if !resp.Applied {
		t.Fatal("pin with surrounding whitespace: applied must be true")
	}
	if got := readFileStr(t, pinsFile); got != "- spaced pin\n" {
		t.Errorf("pins.md = %q, want %q", got, "- spaced pin\n")
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	pins := memoryUpdatesByCause(t, events, "pin")
	if len(pins) != 1 || pins[0]["detail"] != "spaced pin" {
		t.Errorf("memory_update{cause:pin} = %v, want 1 event with the trimmed text as detail", pins)
	}

	// Whitespace-only: refused as empty, and the first pin survives.
	respErr := rig.callExpectErr(t, Request{Cmd: CmdPin, ProjectRoot: root, ConversationID: convID, Text: "   "})
	if !strings.Contains(respErr.Error, "text is empty") {
		t.Errorf("whitespace-only pin refusal = %q, want the empty-text message", respErr.Error)
	}
	if got := readFileStr(t, pinsFile); got != "- spaced pin\n" {
		t.Errorf("pins.md changed despite the empty refusal: %q, want %q", got, "- spaced pin\n")
	}
}
