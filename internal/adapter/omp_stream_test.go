package adapter

// M7 live-streaming tests: --mode json output files carry OMP's JSONL event
// stream; Events tails them incrementally, journals completed blocks, and
// returns the in-flight block as a trailing partial preview. Text-producing
// stubs auto-detect to the legacy path and can't regress.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newStreamRun registers a fake run whose "process" is just an output file
// the test appends to; closing done simulates process exit.
func newStreamRun(t *testing.T, a *OMP) *ompRun {
	t.Helper()
	dir := t.TempDir()
	r := &ompRun{
		id:         "test-run",
		sessionDir: dir, // no *.jsonl: keeps the legacy fallback honest
		workdir:    dir,
		outputFile: filepath.Join(dir, "output.txt"),
		stderr:     &tailBuffer{},
		done:       make(chan struct{}),
	}
	a.mu.Lock()
	a.runs[r.id] = r
	a.mu.Unlock()
	return r
}

func appendOutput(t *testing.T, r *ompRun, s string) {
	t.Helper()
	f, err := os.OpenFile(r.outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func streamEvents(t *testing.T, a *OMP, r *ompRun, afterSeq int) []AgentEvent {
	t.Helper()
	evs, err := a.Events(context.Background(), r.id, afterSeq)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestStreamModeDetection(t *testing.T) {
	t.Run("json stream detected", func(t *testing.T) {
		a := NewOMP(t.TempDir())
		r := newStreamRun(t, a)
		appendOutput(t, r, "{\"type\":\"session\",\"version\":3,\"id\":\"s1\",\"cwd\":\".\"}\n")

		if evs := streamEvents(t, a, r, 0); evs != nil {
			t.Fatalf("session line produced events: %#v", evs)
		}
		if !r.streamMode || r.streamLegacy {
			t.Errorf("streamMode=%v streamLegacy=%v, want true/false", r.streamMode, r.streamLegacy)
		}
	})

	t.Run("text output locks legacy", func(t *testing.T) {
		a := NewOMP(t.TempDir())
		r := newStreamRun(t, a)
		appendOutput(t, r, "Working...\n")

		if evs := streamEvents(t, a, r, 0); evs != nil {
			t.Fatalf("legacy run emitted mid-run events: %#v", evs)
		}
		if r.streamMode || !r.streamLegacy {
			t.Errorf("streamMode=%v streamLegacy=%v, want false/true", r.streamMode, r.streamLegacy)
		}
	})

	t.Run("empty file stays undecided", func(t *testing.T) {
		a := NewOMP(t.TempDir())
		r := newStreamRun(t, a)
		appendOutput(t, r, "")

		streamEvents(t, a, r, 0)
		if r.streamMode || r.streamLegacy {
			t.Errorf("streamMode=%v streamLegacy=%v, want false/false", r.streamMode, r.streamLegacy)
		}
	})
}

func TestStreamTextDelta(t *testing.T) {
	a := NewOMP(t.TempDir())
	r := newStreamRun(t, a)
	appendOutput(t, r,
		"{\"type\":\"session\",\"version\":3,\"id\":\"s1\",\"cwd\":\".\"}\n"+
			"{\"type\":\"message_start\",\"message\":{\"role\":\"assistant\",\"content\":[]}}\n"+
			"{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_start\",\"contentIndex\":0}}\n"+
			"{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"Hello\"}}\n")

	// First tail: the in-flight text block is the trailing preview.
	evs := streamEvents(t, a, r, 0)
	if len(evs) != 1 || evs[0].Type != "agent_text" || evs[0].Payload["partial"] != true {
		t.Fatalf("preview = %#v", evs)
	}
	if evs[0].Payload["text"] != "Hello" {
		t.Errorf("preview text = %q", evs[0].Payload["text"])
	}
	if len(r.streamEvents) != 0 {
		t.Fatalf("preview must not journal yet: %#v", r.streamEvents)
	}

	// Deltas accumulate into the same preview.
	appendOutput(t, r, "{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\" world\"}}\n")
	evs = streamEvents(t, a, r, 0)
	if len(evs) != 1 || evs[0].Payload["text"] != "Hello world" {
		t.Fatalf("accumulated preview = %#v", evs)
	}

	// text_end journals the block (delta fallback: no content field) and
	// clears the preview.
	appendOutput(t, r, "{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_end\",\"contentIndex\":0}}\n")
	evs = streamEvents(t, a, r, 0)
	if len(evs) != 1 || evs[0].Type != "agent_text" || evs[0].Payload["text"] != "Hello world" {
		t.Fatalf("journaled text = %#v", evs)
	}
	if _, bad := evs[0].Payload["partial"]; bad {
		t.Errorf("journaled block must not be partial: %#v", evs[0].Payload)
	}

	// The real stream follows with the full message_end: it must NOT
	// double-journal the same text (the block streamed a text_end).
	appendOutput(t, r, "{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Hello world\"}]}}\n")
	if evs := streamEvents(t, a, r, 1); evs != nil {
		t.Fatalf("message_end duplicated a streamed block: %#v", evs)
	}

	// Process exit: terminal event lands after the streamed block, with the
	// summary taken from it, and the list is then exhausted.
	close(r.done)
	evs = streamEvents(t, a, r, 1)
	if len(evs) != 1 || evs[0].Type != "agent_done" || evs[0].Payload["summary"] != "Hello world" {
		t.Fatalf("terminal = %#v", evs)
	}
	if evs := streamEvents(t, a, r, 2); evs != nil {
		t.Fatalf("events past terminal: %#v", evs)
	}
}

func TestStreamToolExecution(t *testing.T) {
	a := NewOMP(t.TempDir())
	r := newStreamRun(t, a)
	appendOutput(t, r,
		"{\"type\":\"session\",\"version\":3,\"id\":\"s1\",\"cwd\":\".\"}\n"+
			"{\"type\":\"tool_execution_start\",\"toolCallId\":\"call_1\",\"toolName\":\"bash\",\"args\":{\"command\":\"ls\"},\"intent\":\"List files\"}\n")

	// While the tool runs the preview is the in-flight call — tool name,
	// call id, and intent, never journaled.
	evs := streamEvents(t, a, r, 0)
	if len(evs) != 1 || evs[0].Type != "agent_tool_call" || evs[0].Payload["partial"] != true {
		t.Fatalf("tool preview = %#v", evs)
	}
	p := evs[0].Payload
	if p["tool"] != "bash" || p["call_id"] != "call_1" || p["intent"] != "List files" {
		t.Errorf("tool preview payload = %#v", p)
	}
	if _, bad := p["args"]; bad {
		t.Errorf("preview should not promise args it has not finished: %#v", p)
	}

	// tool_execution_end journals call + result (ADR-0002 keys) and clears
	// the preview.
	appendOutput(t, r, "{\"type\":\"tool_execution_end\",\"toolCallId\":\"call_1\",\"toolName\":\"bash\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"total 24\"}]},\"isError\":false}\n")
	evs = streamEvents(t, a, r, 0)
	if len(evs) != 2 {
		t.Fatalf("tool completion = %#v", evs)
	}
	if evs[0].Type != "agent_tool_call" || evs[0].Payload["tool"] != "bash" || evs[0].Payload["args"] != `{"command":"ls"}` {
		t.Errorf("call = %#v", evs[0])
	}
	if evs[1].Type != "agent_tool_result" || evs[1].Payload["tool"] != "bash" || evs[1].Payload["result"] != "total 24" {
		t.Errorf("result = %#v", evs[1])
	}
	if r.streamPreview != nil {
		t.Errorf("preview not cleared: %#v", r.streamPreview)
	}
}

func TestStreamLegacyFallback(t *testing.T) {
	a := NewOMP(t.TempDir())
	r := newStreamRun(t, a)
	appendOutput(t, r, "All good. Byte-for-byte legacy text.\n")

	if evs := streamEvents(t, a, r, 0); evs != nil {
		t.Fatalf("legacy run emitted mid-run events: %#v", evs)
	}

	close(r.done)
	evs := streamEvents(t, a, r, 0)
	if len(evs) != 2 {
		t.Fatalf("legacy completion = %#v", evs)
	}
	if evs[0].Type != "agent_text" || evs[0].Payload["text"] != "All good. Byte-for-byte legacy text." {
		t.Errorf("text = %#v", evs[0])
	}
	if evs[1].Type != "agent_done" {
		t.Errorf("terminal = %#v", evs[1])
	}
}

func TestStreamPartialLineSkipped(t *testing.T) {
	a := NewOMP(t.TempDir())
	r := newStreamRun(t, a)
	appendOutput(t, r, "{\"type\":\"session\",\"version\":3,\"id\":\"s1\"}\n")
	sessionOffset := r.streamOffset

	// A partial line (no trailing newline) is not parsed; the cursor stops
	// at the last complete line.
	appendOutput(t, r, "{\"type\":\"tool_execution_start\",\"toolCa")
	if evs := streamEvents(t, a, r, 0); evs != nil {
		t.Fatalf("partial line produced events: %#v", evs)
	}
	if r.streamOffset != sessionOffset+int64(len("{\"type\":\"session\",\"version\":3,\"id\":\"s1\"}\n")) {
		t.Errorf("offset consumed the partial line: %d", r.streamOffset)
	}

	// Completing the line makes it parse on the next tail.
	appendOutput(t, r, "llId\":\"c1\",\"toolName\":\"bash\",\"args\":{},\"intent\":\"i\"}\n")
	evs := streamEvents(t, a, r, 0)
	if len(evs) != 1 || evs[0].Type != "agent_tool_call" || evs[0].Payload["partial"] != true {
		t.Fatalf("completed line = %#v", evs)
	}
}

// TestStreamMessageEndFallback covers providers that deliver a whole
// assistant message without message_update deltas: the text journals at
// message_end. A following streamed message must not re-trigger the net.
func TestStreamMessageEndFallback(t *testing.T) {
	a := NewOMP(t.TempDir())
	r := newStreamRun(t, a)
	appendOutput(t, r,
		"{\"type\":\"message_start\",\"message\":{\"role\":\"assistant\",\"content\":[]}}\n"+
			"{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"thinking\",\"thinking\":\"x\"},{\"type\":\"text\",\"text\":\"instant answer\"}]}}\n")

	evs := streamEvents(t, a, r, 0)
	if len(evs) != 1 || evs[0].Type != "agent_text" || evs[0].Payload["text"] != "instant answer" {
		t.Fatalf("message_end fallback = %#v", evs)
	}

	// A streamed message after the fallback: exactly one new text event.
	appendOutput(t, r,
		"{\"type\":\"message_start\",\"message\":{\"role\":\"assistant\",\"content\":[]}}\n"+
			"{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_start\",\"contentIndex\":0}}\n"+
			"{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"streamed\"}}\n"+
			"{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_end\",\"contentIndex\":0,\"content\":\"streamed in full\"}}\n"+
			"{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"streamed in full\"}]}}\n")
	evs = streamEvents(t, a, r, 1)
	if len(evs) != 1 || evs[0].Payload["text"] != "streamed in full" {
		t.Fatalf("streamed message after fallback = %#v", evs)
	}
}

func TestStreamTerminalError(t *testing.T) {
	a := NewOMP(t.TempDir())
	r := newStreamRun(t, a)
	appendOutput(t, r,
		"{\"type\":\"session\",\"version\":3,\"id\":\"s1\"}\n"+
			"{\"type\":\"message_start\",\"message\":{\"role\":\"assistant\",\"content\":[]}}\n"+
			"{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_start\",\"contentIndex\":0}}\n"+
			"{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_end\",\"contentIndex\":0,\"content\":\"partial work\"}}\n")

	// Killed mid-run: journaled blocks survive, the preview is dropped, and
	// the terminal event is agent_error (stderr tail included).
	r.err = errors.New("signal: killed")
	_, _ = r.stderr.Write([]byte("boom"))
	close(r.done)
	evs := streamEvents(t, a, r, 0)
	if len(evs) != 2 || evs[0].Type != "agent_text" || evs[0].Payload["text"] != "partial work" {
		t.Fatalf("streamed blocks lost: %#v", evs)
	}
	if evs[1].Type != "agent_error" {
		t.Fatalf("terminal = %#v", evs[1])
	}
	if msg, _ := evs[1].Payload["error"].(string); !strings.Contains(msg, "signal: killed") || !strings.Contains(msg, "boom") {
		t.Errorf("error message = %q", msg)
	}
}
