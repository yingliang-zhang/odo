package ipc

import (
	"fmt"
	"strings"
	"testing"
)

// Odo DX wave — write_memory (Memory tab direct edit) coverage: the two
// editable layers round-trip; every refused shape (foreign root, unknown
// layer, cap overflow, user.md/archive) writes nothing; a landed write
// journals nothing (single atomic rename — no recovery receipt needed).

func TestWriteMemoryEdgeCases(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Layer-name validation is cheapest-first: every name outside the two
	// GUI-editable files refuses and writes nothing.
	for _, name := range []string{"", "user.md", archiveFileName, "../memory.md", "memory.md/", ".odo/memory.md", "pins.md "} {
		resp := rig.callExpectErr(t, Request{Cmd: CmdWriteMemory, ProjectRoot: root, File: name, Content: "x"})
		if !strings.Contains(resp.Error, "refused") {
			t.Errorf("name %q: error = %q, want the argv refusal", name, resp.Error)
		}
	}

	// Cap refusal names the cap and the written size, naming the never-
	// truncate posture; it runs BEFORE any write.
	over := strings.Repeat("o", memoryCap+1)
	resp := rig.callExpectErr(t, Request{Cmd: CmdWriteMemory, ProjectRoot: root, File: memoryFileName, Content: over})
	if !strings.Contains(resp.Error, fmt.Sprintf("exceed %d bytes", memoryCap)) {
		t.Errorf("overflow refusal = %q, want the memoryCap message", resp.Error)
	}
	overPins := strings.Repeat("o", pinsCap+1)
	resp = rig.callExpectErr(t, Request{Cmd: CmdWriteMemory, ProjectRoot: root, File: "pins.md", Content: overPins})
	if !strings.Contains(resp.Error, fmt.Sprintf("exceed %d bytes", pinsCap)) {
		t.Errorf("pins overflow refusal = %q, want the pinsCap message", resp.Error)
	}

	// A foreign root is rejected by the same binding guard as resolveProject.
	resp = rig.callExpectErr(t, Request{Cmd: CmdWriteMemory, ProjectRoot: t.TempDir(), File: memoryFileName, Content: "x"})
	if !strings.Contains(resp.Error, "bound to") {
		t.Errorf("foreign root error = %q, want the binding guard", resp.Error)
	}

	// Happy path: memory.md lands byte-exact, readable through read_memory,
	// and the journal stays empty (no recovery receipt for a single rename).
	want := "# MEMORY\n\n- hand edited from the GUI\n"
	if w := rig.call(t, Request{Cmd: CmdWriteMemory, ProjectRoot: root, File: memoryFileName, Content: want}); !w.Applied {
		t.Fatal("write_memory memory.md: applied must be true")
	}
	if got := rig.call(t, Request{Cmd: CmdReadMemory, ProjectRoot: root}).MemoryContent; got != want {
		t.Errorf("read_memory after write = %q, want %q", got, want)
	}

	// pins.md is the second editable layer, independent file.
	pins := "- keep it lean\n"
	if w := rig.call(t, Request{Cmd: CmdWriteMemory, ProjectRoot: root, File: "pins.md", Content: pins}); !w.Applied {
		t.Fatal("write_memory pins.md: applied must be true")
	}
	if got := rig.call(t, Request{Cmd: CmdReadPins, ProjectRoot: root}).MemoryContent; got != pins {
		t.Errorf("read_pins after write = %q, want %q", got, pins)
	}

	// Empty body is legal (the user cleared the layer) — only overflow and
	// bad names refuse.
	if w := rig.call(t, Request{Cmd: CmdWriteMemory, ProjectRoot: root, File: memoryFileName, Content: ""}); !w.Applied {
		t.Fatal("write_memory empty body: applied must be true")
	}
	if got := rig.call(t, Request{Cmd: CmdReadMemory, ProjectRoot: root}).MemoryContent; got != "" {
		t.Errorf("read_memory after clear = %q, want empty", got)
	}

	// No journaled rows anywhere: the write is one atomic rename, so no
	// memory_update (daemon multi-step ops need those; this doesn't).
	if events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events; len(events) != 0 {
		t.Errorf("journaled events after direct writes = %v, want none", eventTypes(events))
	}
}
