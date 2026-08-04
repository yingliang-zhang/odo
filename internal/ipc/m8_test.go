package ipc

// M8 multi-run tests: fan-out event attribution, cancel-all-lanes,
// steering broadcast, and RunInfo enrichment.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fanoutStubWrapper mimics omp in print mode (legacy): it sleeps briefly,
// writes a text line to the output file, creates hello.txt, and exits 0.
// The fan-out test starts N of these; each gets its own output file via
// the wrapper's positional args.
const fanoutStubWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
sleep 1
printf 'Agent reply.\n' > "$output_file"
cp "$prompt_file" hello.txt
exit 0
`

// TestFanoutEventAttribution verifies that fan-out events are stamped
// with run_id (runDirID) and run_index (0-based batch ordinal), and that
// single-run events stay unattributed (no run_id key).
func TestFanoutEventAttribution(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, fanoutStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if boot.Conversation == nil {
		t.Fatal("bootstrap: missing conversation")
	}
	convID := boot.Conversation.ID

	// Send a fan-out of 2.
	sent := rig.call(t, Request{Cmd: CmdFanoutSend, ConversationID: convID, Text: "Create hello.txt", N: 2})
	if len(sent.Runs) != 2 {
		t.Fatalf("fanout_send: got %d runs, want 2", len(sent.Runs))
	}
	for i, r := range sent.Runs {
		if r.Index != i {
			t.Errorf("run %d index = %d, want %d", i, r.Index, i)
		}
		if r.RunID == "" {
			t.Errorf("run %d run_id is empty", i)
		}
	}

	// Poll until done.
	deadline := time.Now().Add(30 * time.Second)
	var events []string
	var attributedCount int
	for {
		resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID})
		for _, e := range resp.Events {
			events = append(events, e.Type)
			// Check attribution on agent_* events (not user_message).
			if e.Type != "user_message" {
				payload := map[string]interface{}{}
				_ = json.Unmarshal(e.Payload, &payload)
				if _, hasRid := payload["run_id"]; hasRid {
					attributedCount++
				}
				if _, hasIdx := payload["run_index"]; hasIdx {
					attributedCount++
				}
			}
		}
		if resp.AgentRunning != nil && !*resp.AgentRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fan-out did not complete within 30s")
		}
		time.Sleep(150 * time.Millisecond)
	}

	// Fan-out events should have run_id + run_index (at least 2 attributed
	// agent events: 2 runs × at least 1 event each).
	if attributedCount < 2 {
		t.Errorf("expected at least 2 attributed event keys, got %d", attributedCount)
	}
	t.Logf("event sequence: %v", events)
}

// TestFanoutCancelAllLanes verifies that cancel during a fan-out kills
// all live lanes and journals attributed agent_error for each.
func TestFanoutCancelAllLanes(t *testing.T) {
	root := initRepo(t)
	// Use a slow stub so cancel actually lands during the run.
	slowFanoutStub := `#!/bin/sh
prompt_file="$2"
output_file="$3"
sleep 10
printf 'Done.\n' > "$output_file"
exit 0
`
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowFanoutStub))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if boot.Conversation == nil {
		t.Fatal("bootstrap: missing conversation")
	}
	convID := boot.Conversation.ID

	// Start a fan-out of 3.
	sent := rig.call(t, Request{Cmd: CmdFanoutSend, ConversationID: convID, Text: "Slow task", N: 3})
	if len(sent.Runs) != 3 {
		t.Fatalf("fanout_send: got %d runs, want 3", len(sent.Runs))
	}

	// Wait a moment for the runs to start.
	time.Sleep(500 * time.Millisecond)

	// Cancel all.
	cancelResp := rig.call(t, Request{Cmd: CmdCancel, ConversationID: convID})
	if cancelResp.Error != "" {
		t.Fatalf("cancel: %s", cancelResp.Error)
	}

	// Poll and verify attributed agent_error events.
	deadline := time.Now().Add(10 * time.Second)
	var errorEvents int
	for {
		resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID})
		for _, e := range resp.Events {
			if e.Type == "agent_error" {
				payload := map[string]interface{}{}
				_ = json.Unmarshal(e.Payload, &payload)
				if payload["error"] == "cancelled by user" {
					if _, hasRid := payload["run_id"]; hasRid {
						errorEvents++
					}
				}
			}
		}
		if resp.AgentRunning != nil && !*resp.AgentRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel did not stop all lanes within 10s")
		}
		time.Sleep(200 * time.Millisecond)
	}

	if errorEvents < 3 {
		t.Errorf("expected at least 3 attributed cancelled agent_errors, got %d", errorEvents)
	}
}

// TestSingleRunNoAttribution verifies that single-run events do NOT
// carry run_id or run_index (byte-identical to pre-M8 behavior).
func TestSingleRunNoAttribution(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, fanoutStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if boot.Conversation == nil {
		t.Fatal("bootstrap: missing conversation")
	}
	convID := boot.Conversation.ID

	// Single-run send.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})

	// Poll until done.
	deadline := time.Now().Add(30 * time.Second)
	var hasAttribution bool
	for {
		resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID})
		for _, e := range resp.Events {
			if e.Type != "user_message" {
				payload := map[string]interface{}{}
				_ = json.Unmarshal(e.Payload, &payload)
				if _, hasRid := payload["run_id"]; hasRid {
					hasAttribution = true
				}
			}
		}
		if resp.AgentRunning != nil && !*resp.AgentRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("single run did not complete within 30s")
		}
		time.Sleep(150 * time.Millisecond)
	}

	if hasAttribution {
		t.Error("single-run events carry run_id — should be unattributed")
	}
}

// TestFanoutRunInfos verifies that RunInfo from poll_events includes
// the index field and uses runDirID (not the adapter ID).
func TestFanoutRunInfos(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, fanoutStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if boot.Conversation == nil {
		t.Fatal("bootstrap: missing conversation")
	}
	convID := boot.Conversation.ID

	// Start fan-out of 2.
	sent := rig.call(t, Request{Cmd: CmdFanoutSend, ConversationID: convID, Text: "Create hello.txt", N: 2})
	if len(sent.Runs) != 2 {
		t.Fatalf("fanout_send: got %d runs, want 2", len(sent.Runs))
	}
	runIDs := make(map[string]bool, 2)
	for _, r := range sent.Runs {
		runIDs[r.RunID] = true
		if r.Index < 0 || r.Index > 1 {
			t.Errorf("run index = %d, want 0 or 1", r.Index)
		}
	}

	// First poll should return runs with matching run_ids.
	resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID})
	if len(resp.Runs) != 2 {
		t.Fatalf("poll_events: got %d runs, want 2", len(resp.Runs))
	}
	for _, r := range resp.Runs {
		if !runIDs[r.RunID] {
			t.Errorf("poll run_id %q not in fanout_send response", r.RunID)
		}
		if r.Index < 0 || r.Index > 1 {
			t.Errorf("poll run index = %d, want 0 or 1", r.Index)
		}
	}

	// Wait for completion.
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID})
		if resp.AgentRunning != nil && !*resp.AgentRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fan-out did not complete within 30s")
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// TestFanoutSteeringBroadcast verifies that steering during a fan-out
// does not error (it broadcasts to all live lanes). The stub doesn't
// support steering, so the daemon should not return an error — the
// message is journaled and the Send call is best-effort.
func TestFanoutSteeringBroadcast(t *testing.T) {
	root := initRepo(t)
	slowStub := `#!/bin/sh
prompt_file="$2"
output_file="$3"
sleep 5
printf 'Done.\n' > "$output_file"
exit 0
`
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStub))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if boot.Conversation == nil {
		t.Fatal("bootstrap: missing conversation")
	}
	convID := boot.Conversation.ID

	// Start a fan-out of 2.
	rig.call(t, Request{Cmd: CmdFanoutSend, ConversationID: convID, Text: "Slow task", N: 2})
	time.Sleep(500 * time.Millisecond)

	// Steer — should not error even though fan-out is active.
	steerResp := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Actually use world.txt", Steer: true})
	if steerResp.Error != "" {
		t.Fatalf("steering during fan-out should not error: %s", steerResp.Error)
	}
	if steerResp.Event == nil {
		t.Fatal("steering: no event journaled")
	}

	// Cancel to clean up.
	rig.call(t, Request{Cmd: CmdCancel, ConversationID: convID})

	// Verify the steering message was journaled.
	deadline := time.Now().Add(10 * time.Second)
	var sawSteerMsg bool
	for {
		resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID})
		for _, e := range resp.Events {
			if e.Type == "user_message" {
				payload := map[string]interface{}{}
				_ = json.Unmarshal(e.Payload, &payload)
				if txt, _ := payload["text"].(string); txt == "Actually use world.txt" {
					sawSteerMsg = true
				}
			}
		}
		if resp.AgentRunning != nil && !*resp.AgentRunning {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !sawSteerMsg {
		t.Error("steering message not found in journal")
	}
}

// Ensure context is imported (used by tests that need it for cancel).
var _ = context.Background
var _ = fmt.Sprintf
var _ = strings.Contains
