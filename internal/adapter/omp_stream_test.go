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
	// message_end now journals both thinking and text blocks.
	if len(evs) != 2 {
		t.Fatalf("message_end fallback = %d events, want 2: %#v", len(evs), evs)
	}
	if evs[0].Type != "agent_thinking" || evs[0].Payload["text"] != "x" {
		t.Errorf("thinking event = %#v", evs[0])
	}
	if evs[1].Type != "agent_text" || evs[1].Payload["text"] != "instant answer" {
		t.Errorf("text event = %#v", evs[1])
	}

	// A streamed message after the fallback: exactly one new text event
	// (the thinking block is already journaled, text streamed via deltas).
	appendOutput(t, r,
		"{\"type\":\"message_start\",\"message\":{\"role\":\"assistant\",\"content\":[]}}\n"+
			"{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_start\",\"contentIndex\":0}}\n"+
			"{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"streamed\"}}\n"+
			"{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_end\",\"contentIndex\":0,\"content\":\"streamed in full\"}}\n"+
			"{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"streamed in full\"}]}}\n")
	evs = streamEvents(t, a, r, 2)
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

// TestSessionUsageFixture pins D3's measured-cost extractor against a
// live-shape transcript fixture: assistant-message usage blocks sum
// exactly (input/output/cacheRead/cacheWrite per record, cost.total at
// 6dp), the budgeted spend is input+output+cacheWrite (cacheRead is a
// recorded near-free hit, never budgeted), and a transcript carrying no
// usage blocks fails soft (ok=false — never fabricated numbers).
func TestSessionUsageFixture(t *testing.T) {
	msg := func(role, usage string) string {
		u := ""
		if usage != "" {
			u = ",\"usage\":" + usage
		}
		return "{\"type\":\"message\",\"message\":{\"role\":\"" + role +
			"\",\"content\":[{\"type\":\"text\",\"text\":\"x\"}]" + u + "}}\n"
	}

	dir := t.TempDir()
	// Two assistant turns in one file, a third in a second session file
	// (compaction overlay splits); a user row and a malformed line ride
	// along and must not pollute the sums.
	transcript := msg("user", "") +
		msg("assistant", `{"input":1000,"output":200,"cacheRead":700,"cacheWrite":100,"totalTokens":2000,"cost":{"input":0.001,"output":0.0005,"cacheRead":0,"cacheWrite":0.0002,"total":0.0017}}`) +
		"not json at all\n" +
		msg("assistant", `{"input":2000,"output":300,"cacheRead":0,"cacheWrite":0,"totalTokens":2300,"cost":{"input":0.002,"output":0.0008,"cacheRead":0,"cacheWrite":0,"total":0.0028}}`) +
		msg("assistant", "{}") // assistant row without usage: ignored
	transcript2 := msg("assistant", `{"input":4000,"output":600,"cacheRead":999,"cacheWrite":200,"totalTokens":5799,"cost":{"input":0.004,"output":0.001,"cacheRead":0.0005,"cacheWrite":0,"total":0.0055}}`)
	if err := os.WriteFile(filepath.Join(dir, "session-a.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session-b.jsonl"), []byte(transcript2), 0o600); err != nil {
		t.Fatal(err)
	}
	u, ok := SessionUsage(dir)
	if !ok {
		t.Fatal("usage-bearing transcript: ok=false")
	}
	want := Usage{
		InputTokens:      7000,
		OutputTokens:     1100,
		CacheReadTokens:  1699,
		CacheWriteTokens: 300,
		CostUSD:          0.01, // 0.0017 + 0.0028 + 0.0055, pinned at 6dp
		SpentTokens:      8400, // input+output+cacheWrite; cacheRead excluded
	}
	if u != want {
		t.Errorf("sums = %+v, want %+v", u, want)
	}

	t.Run("no usage records fails soft", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(msg("user", "")+msg("assistant", "")), 0o600); err != nil {
			t.Fatal(err)
		}
		if u, ok := SessionUsage(dir); ok || u != (Usage{}) {
			t.Errorf("usage-free transcript = (%+v, %v), want (zero, false)", u, ok)
		}
	})

	t.Run("missing transcript fails soft", func(t *testing.T) {
		if u, ok := SessionUsage(filepath.Join(t.TempDir(), "no-such-dir")); ok || u != (Usage{}) {
			t.Errorf("missing dir = (%+v, %v), want (zero, false)", u, ok)
		}
		if u, ok := SessionUsage(t.TempDir()); ok || u != (Usage{}) {
			t.Errorf("empty dir = (%+v, %v), want (zero, false)", u, ok)
		}
	})
}
