package ipc

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// TestForkConversationWireShape exercises fork_conversation end to end on
// the socket: journal prefix copied verbatim into a new lane's
// conversation, provenance on row + list join, receipt row, fresh
// worktree, source lane untouched.
func TestForkConversationWireShape(t *testing.T) {
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stopOnce(t)
	ctx := context.Background()

	c := bootstrapConv(t, rig, root)
	sendPayloads := []string{
		`{"text":"first goal"}`,
		`{"text":"agent reply"}`,
		`{"text":"second goal"}`,
		`{"text":"final text"}`,
	}
	types := []string{store.EventUserMessage, store.EventAgentText, store.EventUserMessage, store.EventAgentText}
	for i, pj := range sendPayloads {
		if _, err := rig.store.AppendEvent(ctx, c, types[i], pj); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	resp := rig.call(t, Request{Cmd: CmdForkConversation, ProjectRoot: root, ConversationID: c, FromSeq: 2})
	if !resp.OK {
		t.Fatalf("fork_conversation: %s", resp.Error)
	}
	if resp.Workstream == nil || resp.Conversation == nil || resp.Path == "" {
		t.Fatalf("response carries workstream+conversation+path: %+v", resp)
	}
	if resp.Workstream.Name != "main-fork-1" {
		t.Errorf("lane name = %q, want main-fork-1", resp.Workstream.Name)
	}
	newID := resp.Conversation.ID
	if resp.Conversation.ForkedFrom == nil || *resp.Conversation.ForkedFrom != c {
		t.Errorf("forked_from = %v, want %d", resp.Conversation.ForkedFrom, c)
	}
	if resp.Conversation.BaseCommitSHA == nil || *resp.Conversation.BaseCommitSHA == "" {
		t.Error("fork base sha empty — HEAD anchor missing")
	}
	if st, err := os.Stat(resp.Path); err != nil || !st.IsDir() {
		t.Errorf("worktree path %q not a directory: %v", resp.Path, err)
	}

	// Journal copy: exactly the seq<=2 prefix, byte-identical.
	evs, err := rig.store.ListEvents(ctx, newID, 0)
	if err != nil {
		t.Fatalf("list fork journal: %v", err)
	}
	if len(evs) != 3 { // 2 copied + 1 receipt
		t.Fatalf("fork journal = %d rows, want 2 copied + 1 receipt: %+v", len(evs), evs)
	}
	for i, ev := range evs[:2] {
		if ev.Seq != i+1 || ev.Type != types[i] || string(ev.Payload) != sendPayloads[i] {
			t.Errorf("copied row %d = seq %d type %s payload %s — want seq %d type %s payload %s",
				i, ev.Seq, ev.Type, ev.Payload, i+1, types[i], sendPayloads[i])
		}
	}
	// Receipt: review_action{conversation_forked}, actor human, provenance.
	var receipt struct {
		Action string `json:"action"`
		Actor  string `json:"actor"`
		Src    int64  `json:"src_conversation_id"`
		From   int    `json:"from_seq"`
		Copied int    `json:"copied"`
	}
	if err := json.Unmarshal(evs[2].Payload, &receipt); err != nil {
		t.Fatalf("receipt parse: %v", err)
	}
	if receipt.Action != "conversation_forked" || receipt.Actor != "human" || receipt.Src != c || receipt.From != 2 || receipt.Copied != 2 {
		t.Errorf("receipt = %+v", receipt)
	}

	// Source untouched (append-only: fork copies, never edits).
	srcEvs, err := rig.store.ListEvents(ctx, c, 0)
	if err != nil {
		t.Fatalf("list source: %v", err)
	}
	if len(srcEvs) != len(sendPayloads) {
		t.Errorf("source journal = %d rows after fork, want the untouched %d", len(srcEvs), len(sendPayloads))
	}

	// Sidebar surface: ListWorkstreams projects provenance on the fork
	// lane only.
	p, err := rig.store.GetProjectByRoot(ctx, root)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	ws, err := rig.store.ListWorkstreams(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListWorkstreams: %v", err)
	}
	var srcLane, forkLane *store.Workstream
	for i := range ws {
		if ws[i].ID == resp.Workstream.ID {
			forkLane = &ws[i]
		} else {
			srcLane = &ws[i]
		}
	}
	if forkLane == nil || forkLane.ForkedFrom == nil || *forkLane.ForkedFrom != c {
		t.Errorf("fork lane provenance = %+v, want src %d", forkLane, c)
	}
	if srcLane == nil || srcLane.ForkedFrom != nil {
		t.Errorf("source lane carries provenance: %+v", srcLane)
	}
}

// TestForkConversationRefusals: below-floor from_seq, past-end from_seq,
// and a fork of a missing conversation all refuse; the below-floor
// refusal names the journal floor, and a refused fork creates no lane.
func TestForkConversationRefusals(t *testing.T) {
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stopOnce(t)
	ctx := context.Background()
	c := bootstrapConv(t, rig, root)
	for range 2 {
		if _, err := rig.store.AppendEvent(ctx, c, store.EventUserMessage, `{"text":"m"}`); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	for _, tc := range []struct {
		name    string
		req     Request
		wantErr string
	}{
		{"below floor", Request{Cmd: CmdForkConversation, ProjectRoot: root, ConversationID: c, FromSeq: 0}, "below the journal floor"},
		{"past end", Request{Cmd: CmdForkConversation, ProjectRoot: root, ConversationID: c, FromSeq: 99}, ""},
		{"missing conversation", Request{Cmd: CmdForkConversation, ProjectRoot: root, ConversationID: 999, FromSeq: 1}, ""},
	} {
		resp := rig.callExpectErr(t, tc.req)
		if resp.OK {
			t.Errorf("%s: fork admitted", tc.name)
		}
		if resp.Error == "" {
			t.Errorf("%s: refusal carries no reason", tc.name)
		}
		if tc.wantErr != "" && !strings.Contains(resp.Error, tc.wantErr) {
			t.Errorf("%s: refusal %q does not name %q", tc.name, resp.Error, tc.wantErr)
		}
	}
	// No lanes were created by the refused attempts.
	p, _ := rig.store.GetProjectByRoot(ctx, root)
	ws, _ := rig.store.ListWorkstreams(ctx, p.ID)
	if len(ws) != 1 {
		t.Errorf("refusals created lanes: %+v", ws)
	}
}

// TestForkConversationSecondForkLadder: two forks of one source get
// distinct lanes (…-fork-1, …-fork-2) both pointing at the source.
func TestForkConversationSecondForkLadder(t *testing.T) {
	root := initRepo(t)
	rig := startRig(t, root)
	defer rig.stopOnce(t)
	ctx := context.Background()
	c := bootstrapConv(t, rig, root)
	if _, err := rig.store.AppendEvent(ctx, c, store.EventUserMessage, `{"text":"m"}`); err != nil {
		t.Fatalf("append: %v", err)
	}

	r1 := rig.call(t, Request{Cmd: CmdForkConversation, ProjectRoot: root, ConversationID: c, FromSeq: 1})
	r2 := rig.call(t, Request{Cmd: CmdForkConversation, ProjectRoot: root, ConversationID: c, FromSeq: 1})
	if !r1.OK || !r2.OK {
		t.Fatalf("forks: %s / %s", r1.Error, r2.Error)
	}
	if r1.Workstream.ID == r2.Workstream.ID {
		t.Fatal("second fork landed on the first fork's lane")
	}
	names := map[string]bool{r1.Workstream.Name: true, r2.Workstream.Name: true}
	if !names["main-fork-1"] || !names["main-fork-2"] {
		t.Errorf("lane names = %v, want main-fork-1 and main-fork-2", names)
	}
	for _, r := range []Response{r1, r2} {
		if r.Conversation.ForkedFrom == nil || *r.Conversation.ForkedFrom != c {
			t.Errorf("provenance = %v", r.Conversation.ForkedFrom)
		}
	}
	// Forks of a FORK chain correctly (provenance tracks the immediate
	// source, not transitive).
	r3 := rig.call(t, Request{Cmd: CmdForkConversation, ProjectRoot: root, ConversationID: r1.Conversation.ID, FromSeq: 1})
	if !r3.OK {
		t.Fatalf("fork-of-fork: %s", r3.Error)
	}
	if r3.Conversation.ForkedFrom == nil || *r3.Conversation.ForkedFrom != r1.Conversation.ID {
		t.Errorf("fork-of-fork provenance = %v, want %d", r3.Conversation.ForkedFrom, r1.Conversation.ID)
	}
}
