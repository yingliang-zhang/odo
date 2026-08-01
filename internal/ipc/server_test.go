package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

// This file tests the M0 "visible loop" end to end through the real socket:
// bootstrap -> send_message -> poll (running) -> poll (done + diff) ->
// accept/reject -> daemon restart -> session restore. The only stub is the
// agent itself: ODO_OMP_WRAPPER points at a shell script standing in for the
// Hermes wrapper, which the adapter treats as a black box in production too.

// stubWrapper mimics omp_with_timeout.sh: positional args are
// <seconds> <prompt_file> <output_file>; it "does the work" by copying the
// prompt into hello.txt in its cwd (the worktree) and writing a transcript
// line to the output file, then exits 0.
const stubWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
sleep 1
cp "$prompt_file" hello.txt
printf 'Created hello.txt as requested.\n' > "$output_file"
exit 0
`

type testRig struct {
	root    string // temp project repo
	sock    string
	store   *store.Store
	server  *Server
	adapter *adapter.OMP
	listen  net.Listener
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	argv := append([]string{"-C", dir, "-c", "user.email=odo@test", "-c", "user.name=odo"}, args...)
	if out, err := exec.Command("git", argv...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// startRig builds a project repo and a live daemon bound to it.
func startRig(t *testing.T, root string) *testRig {
	t.Helper()
	mgr := worktree.NewManager(root)
	if err := mgr.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	st, err := store.Open(filepath.Join(mgr.StateDir(), "journal.sqlite"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	omp := adapter.NewOMP(mgr.StateDir())
	srv := NewServer(st, root, omp, mgr)

	// Socket in its own short dir: macOS caps sun_path at ~104 chars and
	// t.TempDir() paths under /var/folders are already ~60.
	sockDir, err := os.MkdirTemp("", "odo-sock")
	if err != nil {
		t.Fatalf("sockdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "odo.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(l)
	return &testRig{root: root, sock: sock, store: st, server: srv, adapter: omp, listen: l}
}

func (r *testRig) stop(t *testing.T) {
	t.Helper()
	r.adapter.CloseAll()
	r.listen.Close()
	if err := r.store.Close(); err != nil {
		t.Fatalf("store close: %v", err)
	}
}

// call sends one request on a fresh connection and decodes the response.
func (r *testRig) call(t *testing.T, req Request) Response {
	t.Helper()
	conn, err := net.Dial("unix", r.sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Fatalf("%s: %s", req.Cmd, resp.Error)
	}
	return resp
}

// pollUntilDone polls until the agent finishes (deadline 20s) and returns the
// final response. It also verifies the first poll reports agent_running.
func (r *testRig) pollUntilDone(t *testing.T, convID int64) Response {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	afterSeq := 0
	first := true
	for {
		resp := r.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: afterSeq})
		if n := len(resp.Events); n > 0 {
			afterSeq = resp.Events[n-1].Seq
		}
		if resp.AgentRunning == nil {
			t.Fatal("poll_events: agent_running missing")
		}
		if first && !*resp.AgentRunning {
			t.Fatal("poll_events: first poll should report agent_running=true")
		}
		first = false
		if !*resp.AgentRunning {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatal("agent did not finish within 20s")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func eventTypes(events []store.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

func TestVisibleLoopAcceptRejectRestore(t *testing.T) {
	// Project repo with one initial commit (worktree add needs a HEAD).
	root := t.TempDir()
	gitIn(t, root, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# smoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "init")

	// Install the stub agent wrapper.
	wrapperPath := filepath.Join(t.TempDir(), "stub_wrapper.sh")
	if err := os.WriteFile(wrapperPath, []byte(stubWrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODO_OMP_WRAPPER", wrapperPath)

	rig := startRig(t, root)

	// --- bootstrap: fresh project gets project/workstream/conversation ---
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if boot.Project == nil || boot.Workstream == nil || boot.Conversation == nil {
		t.Fatal("bootstrap: missing project/workstream/conversation")
	}
	convID := boot.Conversation.ID
	if boot.Workstream.Name != "main" {
		t.Errorf("workstream = %q, want main", boot.Workstream.Name)
	}
	if len(boot.Events) != 0 {
		t.Errorf("bootstrap: fresh conversation has %d events", len(boot.Events))
	}

	// --- run 1: send -> poll -> accept ---
	msg1 := "Create hello.txt (run one)"
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: msg1})
	if sent.Event == nil || sent.Event.Type != store.EventUserMessage || sent.Event.Seq != 1 {
		t.Fatalf("send_message: bad event %+v", sent.Event)
	}

	done := rig.pollUntilDone(t, convID)
	if got, want := fmt.Sprint(eventTypes(done.Events)), "[agent_text agent_done]"; got != want {
		t.Fatalf("journaled agent events = %s, want %s", got, want)
	}
	if done.Diff == nil {
		t.Fatal("poll_events: no diff after agent_done")
	}
	if done.Diff.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending", done.Diff.Status)
	}
	if !strings.Contains(done.Diff.Content, "hello.txt") || !strings.Contains(done.Diff.Content, "+Create hello.txt (run one)") {
		t.Errorf("diff content missing new file:\n%s", done.Diff.Content)
	}
	// User repo untouched before accept.
	if _, err := os.Stat(filepath.Join(root, "hello.txt")); !os.IsNotExist(err) {
		t.Error("hello.txt exists in project before accept")
	}

	acc := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: done.Diff.ID})
	if !acc.Applied || acc.DiffID != done.Diff.ID {
		t.Errorf("accept_diff = %+v", acc)
	}
	content, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatalf("hello.txt not applied: %v", err)
	}
	if string(content) != msg1 {
		t.Errorf("hello.txt = %q, want %q", content, msg1)
	}
	// Accept retires the worktree.
	if entries, _ := os.ReadDir(filepath.Join(root, ".odo", "worktrees")); len(entries) != 0 {
		t.Errorf("worktrees dir not empty after accept: %d entries", len(entries))
	}

	// --- restart: journal survives; second server restores the session ---
	rig.stop(t)
	rig = startRig(t, root)
	defer rig.adapter.CloseAll()

	boot2 := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if boot2.Conversation == nil || boot2.Conversation.ID != convID {
		t.Fatalf("bootstrap after restart: conversation = %+v, want id %d", boot2.Conversation, convID)
	}
	if got, want := fmt.Sprint(eventTypes(boot2.Events)),
		"[user_message agent_text agent_done review_action]"; got != want {
		t.Errorf("restored events = %s, want %s", got, want)
	}
	if boot2.Diff == nil || boot2.Diff.Status != store.DiffAccepted {
		t.Errorf("restored diff = %+v, want accepted", boot2.Diff)
	}

	// --- run 2 on the restored conversation: send -> poll -> reject ---
	msg2 := "Create hello.txt (run two)"
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: msg2})
	done2 := rig.pollUntilDone(t, convID)
	if done2.Diff == nil {
		t.Fatal("run 2: no diff")
	}
	rej := rig.call(t, Request{Cmd: CmdRejectDiff, DiffID: done2.Diff.ID})
	if rej.Applied {
		t.Error("reject_diff: applied must be false")
	}
	content, err = os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != msg1 {
		t.Errorf("hello.txt after reject = %q, want unchanged %q", content, msg1)
	}
	st, err := rig.store.GetDiff(context.Background(), done2.Diff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != store.DiffRejected {
		t.Errorf("diff 2 status = %q, want rejected", st.Status)
	}
	// Double review is refused.
	resp := rig.callExpectErr(t, Request{Cmd: CmdAcceptDiff, DiffID: done2.Diff.ID})
	if !strings.Contains(resp.Error, "already rejected") {
		t.Errorf("double review error = %q", resp.Error)
	}
}

// callExpectErr sends a request expecting ok=false.
func (r *testRig) callExpectErr(t *testing.T, req Request) Response {
	t.Helper()
	conn, err := net.Dial("unix", r.sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK {
		t.Fatalf("%s: expected error, got ok", req.Cmd)
	}
	return resp
}
