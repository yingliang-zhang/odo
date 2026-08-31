package ipc

// M7 live-streaming end-to-end test: a stub wrapper appends JSONL stream
// lines to its output file with real sleeps between them, and the test
// verifies the preview passes through poll_events while the block is in
// flight, never lands in the journal, and the completed block + terminal
// event arrive through the normal drain path.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// streamingStubWrapper mimics omp in --mode json: positional args are
// <seconds> <prompt_file> <output_file>; it appends JSONL stream lines to the
// output file with sleeps between them (text_start → delta → ... text_end),
// then creates hello.txt for the diff and exits 0. The ~2s window between
// the delta and text_end lets several polls observe the in-flight preview;
// it bypasses the test seam (`command sleep`) so ODO_STUB_SCALE never
// shrinks the observed preview window — only the two lead-in sleeps scale.
const streamingStubWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
printf '%s\n' '{"type":"session","version":3,"id":"stub","cwd":"."}' > "$output_file"
printf '%s\n' '{"type":"message_start","message":{"role":"assistant","content":[]}}' >> "$output_file"
sleep 1
printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}' >> "$output_file"
sleep 1
printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Creating hello.txt."}}' >> "$output_file"
command sleep 2
printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_end","contentIndex":0,"content":"Creating hello.txt."}}' >> "$output_file"
printf '%s\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Creating hello.txt."}]}}' >> "$output_file"
cp "$prompt_file" hello.txt
printf '%s\n' '{"type":"agent_end","messages":[],"isTerminal":true}' >> "$output_file"
exit 0
`

func TestStreamingVisibleLoopPreview(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir()) // hermetic user.md injection (suite-wide convention)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, streamingStubWrapper))

	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if boot.Conversation == nil {
		t.Fatal("bootstrap: missing conversation")
	}
	convID := boot.Conversation.ID

	if sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"}); sent.Event == nil {
		t.Fatal("send_message: no event")
	}

	// Poll until done at 150 ms, tracking the transient preview.
	deadline := time.Now().Add(30 * time.Second)
	afterSeq := 0
	var previewText string
	sawPartial := false
	var events []string
	for {
		resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: afterSeq})
		if n := len(resp.Events); n > 0 {
			afterSeq = resp.Events[n-1].Seq
			for _, e := range resp.Events {
				events = append(events, e.Type)
				// The preview is transient: partial must never journal.
				if data := string(e.Payload); strings.Contains(data, "\"partial\"") {
					t.Fatalf("partial payload journaled: seq %d %s", e.Seq, data)
				}
			}
		}
		if resp.Preview != nil {
			if resp.Preview.Payload["partial"] != true {
				t.Fatalf("preview without partial:true: %#v", resp.Preview)
			}
			if !resp.Streaming {
				t.Fatal("streaming flag not set while preview present")
			}
			sawPartial = true
			if resp.Preview.Type == "agent_text" {
				if s, _ := resp.Preview.Payload["text"].(string); s != "" {
					previewText = s
				}
			}
		}
		if resp.AgentRunning == nil {
			t.Fatal("poll_events: agent_running missing")
		}
		if !*resp.AgentRunning {
			if resp.Preview != nil {
				t.Fatalf("preview present after run finished: %#v", resp.Preview)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent did not finish within 30s")
		}
		time.Sleep(150 * time.Millisecond)
	}

	if !sawPartial {
		t.Fatal("no partial preview observed during the stream window")
	}
	if previewText != "Creating hello.txt." {
		t.Errorf("preview text = %q, want %q", previewText, "Creating hello.txt.")
	}
	// D9-W3a (additive): the drained run tails one fail-soft run_usage
	// receipt behind agent_done (stub transcripts carry no usage
	// records); run_usage_test.go pins its payload + exactly-once.
	if got, want := fmt.Sprint(events), "[user_message agent_text agent_done memory_update]"; got != want {
		t.Errorf("journaled sequence = %s, want %s", got, want)
	}
}
