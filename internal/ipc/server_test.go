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

// initRepo builds a git project repo with one initial commit (worktree add
// needs a HEAD).
func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitIn(t, root, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# smoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "init")
	return root
}

// writeStub installs an agent wrapper script and returns its path.
func writeStub(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub_wrapper.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// slowStubWrapper is stubWrapper with a 3s delay: long enough for a steering
// request to land while the agent is still running.
const slowStubWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
sleep 3
cp "$prompt_file" hello.txt
printf 'Created hello.txt as requested.\n' > "$output_file"
exit 0
`

// stubPiSlow mimics the pi CLI: it sleeps, prints to stdout, exits 0.
const stubPi = `#!/bin/sh
sleep 3
printf 'Pi summary of the task.\n'
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
	root := initRepo(t)

	// Install the stub agent wrapper.
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

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

// allEventTypes re-polls from seq 0 and returns every journaled event type.
func (r *testRig) allEventTypes(t *testing.T, convID int64) []string {
	t.Helper()
	resp := r.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
	return eventTypes(resp.Events)
}

// TestCreateWorkstream covers create_workstream + list_workstreams: name
// sanitization, idempotent create-or-get, ordering, and error cases.
func TestCreateWorkstream(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})

	// list: only "main", with its name as git branch (M1 populates the column).
	list := rig.call(t, Request{Cmd: CmdListWorkstreams, ProjectRoot: root})
	if len(list.Workstreams) != 1 {
		t.Fatalf("list_workstreams = %d entries, want 1", len(list.Workstreams))
	}
	if list.Workstreams[0].ID != boot.Workstream.ID || list.Workstreams[0].Name != "main" {
		t.Errorf("list[0] = %+v", list.Workstreams[0])
	}
	if b := list.Workstreams[0].Branch; b == nil || *b != "main" {
		t.Errorf("main branch = %v, want \"main\"", b)
	}

	// create: the name is sanitized to a git-safe branch; branch == name.
	created := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "Refactor / auth module!"})
	if created.Workstream == nil {
		t.Fatal("create_workstream: missing workstream")
	}
	if created.Workstream.Name != "Refactor-auth-module" {
		t.Errorf("sanitized name = %q", created.Workstream.Name)
	}
	if b := created.Workstream.Branch; b == nil || *b != created.Workstream.Name {
		t.Errorf("branch = %v, want name %q", b, created.Workstream.Name)
	}

	// create-or-get: the same input returns the same row.
	again := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "Refactor / auth module!"})
	if again.Workstream.ID != created.Workstream.ID {
		t.Errorf("duplicate create: new id %d, want %d", again.Workstream.ID, created.Workstream.ID)
	}

	// list: oldest first.
	list = rig.call(t, Request{Cmd: CmdListWorkstreams, ProjectRoot: root})
	if len(list.Workstreams) != 2 {
		t.Fatalf("list_workstreams = %d entries, want 2", len(list.Workstreams))
	}
	if list.Workstreams[0].Name != "main" || list.Workstreams[1].ID != created.Workstream.ID {
		t.Errorf("order = [%s, %d]", list.Workstreams[0].Name, list.Workstreams[1].ID)
	}

	// errors: empty name, name that sanitizes to nothing, wrong project root.
	for _, name := range []string{"", "   ", "!!!"} {
		resp := rig.callExpectErr(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: name})
		if !strings.Contains(resp.Error, "name") {
			t.Errorf("name %q: error = %q, want mention of name", name, resp.Error)
		}
	}
	other := rig.callExpectErr(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: t.TempDir(), Name: "x"})
	if !strings.Contains(other.Error, "bound to") {
		t.Errorf("wrong root: error = %q", other.Error)
	}
}

// TestBootstrapByWorkstream covers the workstream_id extension: each
// workstream gets its own conversation, and bootstraps restore by workstream.
func TestBootstrapByWorkstream(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	mainConv := boot.Conversation.ID
	mainWS := boot.Workstream.ID

	created := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "feature-x"})
	wsX := created.Workstream.ID

	// Bootstrap the new workstream: fresh conversation, no carried events.
	bootX := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: wsX})
	if bootX.Workstream == nil || bootX.Workstream.ID != wsX {
		t.Fatalf("bootstrap by workstream: workstream = %+v", bootX.Workstream)
	}
	if bootX.Conversation == nil || bootX.Conversation.ID == mainConv {
		t.Fatalf("bootstrap by workstream: conversation = %+v, want a new one", bootX.Conversation)
	}
	if bootX.Conversation.WorkstreamID != wsX {
		t.Errorf("conversation workstream_id = %d, want %d", bootX.Conversation.WorkstreamID, wsX)
	}
	if len(bootX.Events) != 0 {
		t.Errorf("fresh workstream conversation has %d events", len(bootX.Events))
	}
	if bootX.AgentRunning == nil {
		t.Error("bootstrap: agent_running missing")
	}

	// Repeat bootstraps restore the right conversation per workstream.
	bootX2 := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: wsX})
	if bootX2.Conversation.ID != bootX.Conversation.ID {
		t.Errorf("re-bootstrap workstream: conversation %d, want %d", bootX2.Conversation.ID, bootX.Conversation.ID)
	}
	bootMain := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: mainWS})
	if bootMain.Conversation.ID != mainConv {
		t.Errorf("bootstrap main by id: conversation %d, want %d", bootMain.Conversation.ID, mainConv)
	}

	// Unknown workstream id is an error; default bootstrap still works.
	resp := rig.callExpectErr(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: 424242})
	if !strings.Contains(resp.Error, "bootstrap") {
		t.Errorf("bogus workstream_id: error = %q", resp.Error)
	}
	bootDefault := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if bootDefault.Conversation.ID != mainConv {
		t.Errorf("default bootstrap: conversation %d, want main %d", bootDefault.Conversation.ID, mainConv)
	}
}

// TestSteering covers steer=true messages: they journal a user_message
// without starting a run, reach a running OMP agent via the steering file,
// surface a friendly agent_error on adapters without steering (Pi), and are
// journaled silently when no agent is active.
func TestSteering(t *testing.T) {
	t.Run("supported writes the steering file", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})

		// The slow stub runs 3s: steering lands while the run is active.
		steered := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Also add a second line.", Steer: true})
		if steered.Event == nil || steered.Event.Type != store.EventUserMessage || steered.Event.Seq != 2 {
			t.Fatalf("steer event = %+v, want user_message seq 2", steered.Event)
		}

		// The OMP adapter handed the text to the run via steering.txt.
		matches, err := filepath.Glob(filepath.Join(root, ".odo", "sessions", "*", "steering.txt"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("steering.txt not found (matches=%v, err=%v)", matches, err)
		}
		content, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "Also add a second line.\n" {
			t.Errorf("steering.txt = %q", content)
		}

		// No error event was journaled; the run completes normally.
		rig.pollUntilDone(t, convID)
		if got, want := fmt.Sprint(rig.allEventTypes(t, convID)),
			"[user_message user_message agent_text agent_done]"; got != want {
			t.Errorf("events = %s, want %s", got, want)
		}
	})

	t.Run("unsupported adapter surfaces agent_error", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		t.Setenv("ODO_PI_COMMAND", writeStub(t, stubPi))
		rig := startRig(t, root)
		defer rig.stop(t)
		pi := adapter.NewPi(worktree.NewManager(root).StateDir())
		defer pi.CloseAll()
		rig.server.RegisterAdapter("pi", pi)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Do work", Adapter: "pi"})

		// Pi has no steering (M1): the message is journaled AND the user is
		// told via agent_error instead of a failed response.
		steered := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "steer", Steer: true})
		if steered.Event == nil || steered.Event.Type != store.EventUserMessage {
			t.Fatalf("steer event = %+v", steered.Event)
		}
		rig.pollUntilDone(t, convID)
		if got, want := fmt.Sprint(rig.allEventTypes(t, convID)),
			"[user_message user_message agent_error agent_text agent_done]"; got != want {
			t.Fatalf("events = %s, want %s", got, want)
		}
		events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
		var payload map[string]interface{}
		if err := json.Unmarshal(events[2].Payload, &payload); err != nil {
			t.Fatalf("agent_error payload: %v", err)
		}
		if payload["error"] != "Steering not supported by current adapter." {
			t.Errorf("agent_error = %v", payload["error"])
		}
	})

	t.Run("no active run journals only", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		steered := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "queued", Steer: true})
		if steered.Event == nil || steered.Event.Type != store.EventUserMessage {
			t.Fatalf("steer event = %+v", steered.Event)
		}
		if got, want := fmt.Sprint(rig.allEventTypes(t, convID)), "[user_message]"; got != want {
			t.Errorf("events = %s, want %s", got, want)
		}
	})
}

// TestDistill covers the distill command: it refuses while a run is active,
// writes the wiki note for the distilled epoch, increments the epoch, and
// journals a review_action (ADR-0002 event types stay fixed).
func TestDistill(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Distill during an active run is refused.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	resp := rig.callExpectErr(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if !strings.Contains(resp.Error, "still running") {
		t.Errorf("distill during run: error = %q", resp.Error)
	}
	rig.pollUntilDone(t, convID)

	// Distill epoch 1: blocking call returns the note path + new epoch.
	d1 := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	wantPath := filepath.Join(root, "wiki", "main-epoch-1.md")
	if d1.WikiPath != wantPath {
		t.Errorf("wiki_path = %q, want %q", d1.WikiPath, wantPath)
	}
	if d1.Epoch != 2 {
		t.Errorf("epoch = %d, want 2", d1.Epoch)
	}
	note, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("wiki note: %v", err)
	}
	if !strings.Contains(string(note), "Created hello.txt") {
		t.Errorf("wiki note missing agent summary:\n%s", note)
	}

	// Conversation epoch persisted; review_action journaled with the payload.
	conv, err := rig.store.GetConversation(context.Background(), convID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.Epoch != 2 {
		t.Errorf("stored epoch = %d, want 2", conv.Epoch)
	}
	if got, want := fmt.Sprint(rig.allEventTypes(t, convID)),
		"[user_message agent_text agent_done review_action]"; got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	var payload map[string]interface{}
	last := events[len(events)-1]
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatalf("review_action payload: %v", err)
	}
	if payload["action"] != "distill" || payload["epoch"] != float64(2) || payload["wiki_path"] != wantPath {
		t.Errorf("review_action payload = %v", payload)
	}

	// The distill prompt carried the conversation events to the agent.
	matches, err := filepath.Glob(filepath.Join(root, ".odo", "prompts", "*.txt"))
	if err != nil || len(matches) != 2 { // user prompt + distill prompt
		t.Fatalf("prompt files = %v, err = %v", matches, err)
	}
	found := false
	for _, m := range matches {
		b, _ := os.ReadFile(m)
		if strings.Contains(string(b), "Summarize the key decisions") && strings.Contains(string(b), "Create hello.txt") {
			found = true
		}
	}
	if !found {
		t.Error("distill prompt missing summary instruction and conversation text")
	}

	// A second distill bumps the epoch again; the first note survives.
	d2 := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if d2.Epoch != 3 || d2.WikiPath != filepath.Join(root, "wiki", "main-epoch-2.md") {
		t.Errorf("second distill = (epoch %d, %q)", d2.Epoch, d2.WikiPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("epoch-1 note gone after second distill: %v", err)
	}

	// Unknown conversation errors out.
	if resp := rig.callExpectErr(t, Request{Cmd: CmdDistill, ConversationID: 424242}); resp.Error == "" {
		t.Error("distill bogus conversation: want error")
	}
}

// TestPiRunIPC runs a full send->poll cycle through the daemon with the Pi
// adapter selected by the send_message "adapter" field, verifying the
// adapter routing (start, drain, events) end to end.
func TestPiRunIPC(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	t.Setenv("ODO_PI_COMMAND", writeStub(t, stubPi))
	rig := startRig(t, root)
	defer rig.stop(t)
	pi := adapter.NewPi(worktree.NewManager(root).StateDir())
	defer pi.CloseAll()
	rig.server.RegisterAdapter("pi", pi)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Unknown adapter names are rejected.
	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "x", Adapter: "bogus"})
	if !strings.Contains(resp.Error, "unknown adapter") {
		t.Errorf("bogus adapter: error = %q", resp.Error)
	}

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Summarize the repo", Adapter: "pi"})
	if sent.Event == nil || sent.Event.Type != store.EventUserMessage {
		t.Fatalf("send event = %+v", sent.Event)
	}
	done := rig.pollUntilDone(t, convID)
	if got, want := fmt.Sprint(eventTypes(done.Events)), "[agent_text agent_done]"; got != want {
		t.Fatalf("pi events = %s, want %s", got, want)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(done.Events[0].Payload, &payload); err != nil {
		t.Fatalf("agent_text payload: %v", err)
	}
	if payload["text"] != "Pi summary of the task." {
		t.Errorf("pi agent_text = %v", payload["text"])
	}
	// The stub changed no files, so no diff was produced — but the run
	// drained through the Pi adapter without an error event.
	if done.Diff != nil {
		t.Errorf("unexpected diff from no-op pi run: %+v", done.Diff)
	}
}

// TestAttachmentsJournal verifies that attachments sent with send_message
// are journaled in the user_message event payload.
func TestAttachmentsJournal(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Send with attachments.
	files := []string{"/path/to/main.py", "/path/to/utils.go"}
	sent := rig.call(t, Request{
		Cmd:            CmdSendMessage,
		ConversationID: convID,
		Text:           "Create hello.txt (attachment test)",
		Attachments:    files,
	})
	if sent.Event == nil || sent.Event.Type != store.EventUserMessage {
		t.Fatalf("send_message: bad event %+v", sent.Event)
	}

	// Verify the journaled event payload contains the attachments.
	var payload struct {
		Attachments []string `json:"attachments"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal user_message payload: %v", err)
	}
	if len(payload.Attachments) != len(files) {
		t.Fatalf("attachments: got %d, want %d", len(payload.Attachments), len(files))
	}
	for i, want := range files {
		if payload.Attachments[i] != want {
			t.Errorf("attachment[%d]: got %q, want %q", i, payload.Attachments[i], want)
		}
	}

	// Poll all events and verify the user_message carries attachments.
	all := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
	var foundAttach bool
	for _, ev := range all.Events {
		if ev.Type == store.EventUserMessage {
			var p struct {
				Attachments []string `json:"attachments"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			if len(p.Attachments) == len(files) {
				foundAttach = true
			}
		}
	}
	if !foundAttach {
		t.Error("no user_message event with attachments found in poll")
	}
}

// writePrefs writes ~/.odo/prefs.md in the test home directory.
func writePrefs(t *testing.T, home, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".odo", "prefs.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reviewStubWrapper serves both the coding run (default model) and the two
// MoA review runs (models rm1/rm2, selected by --hermes-model). The review
// branch proves parallel execution: each reviewer drops a marker and waits
// for the other's; a sequential executor would time out and exit 1, which
// degrades that review to an error the verdict assertions then catch.
const reviewStubWrapper = `#!/bin/sh
prompt_file="$2"
output_file="$3"
model=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--hermes-model" ]; then shift; model="$1"; fi
  shift
done
case "$model" in
  rm1|rm2)
    : > "$ODO_REVIEW_MARKER/$model.started"
    i=0
    while [ $i -lt 100 ]; do
      if [ -f "$ODO_REVIEW_MARKER/rm1.started" ] && [ -f "$ODO_REVIEW_MARKER/rm2.started" ]; then break; fi
      i=$((i+1))
      sleep 0.1
    done
    if [ ! -f "$ODO_REVIEW_MARKER/rm1.started" ] || [ ! -f "$ODO_REVIEW_MARKER/rm2.started" ]; then
      echo "reviews ran sequentially" >&2
      exit 1
    fi
    if [ "$model" = "rm1" ]; then
      printf 'ACCEPT\nShip it.\n' > "$output_file"
    else
      printf 'REJECT\nNeeds tests.\n' > "$output_file"
    fi
    exit 0
    ;;
esac
sleep 1
cp "$prompt_file" hello.txt
printf 'Created hello.txt as requested.\n' > "$output_file"
exit 0
`

// TestReviewDiff covers the MoA review fan-out: the diff goes to every model
// on the prefs.md review: line in parallel, verdicts and comments come back
// in config order, and the result is journaled as a review_action event
// (action "moa_review").
func TestReviewDiff(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, reviewStubWrapper))
	markerDir := t.TempDir()
	t.Setenv("ODO_REVIEW_MARKER", markerDir)
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff to review")
	}
	diffID := done.Diff.ID

	// Without a review: line the command refuses.
	resp := rig.callExpectErr(t, Request{Cmd: CmdReviewDiff, DiffID: diffID})
	if !strings.Contains(resp.Error, "No review models configured.") {
		t.Errorf("review without models: error = %q", resp.Error)
	}
	// A line whose entries are all malformed refuses too.
	writePrefs(t, home, "review: not-a-model\n")
	resp = rig.callExpectErr(t, Request{Cmd: CmdReviewDiff, DiffID: diffID})
	if !strings.Contains(resp.Error, "No review models configured.") {
		t.Errorf("malformed review line: error = %q", resp.Error)
	}
	// Missing diff_id is an error.
	resp = rig.callExpectErr(t, Request{Cmd: CmdReviewDiff})
	if !strings.Contains(resp.Error, "diff_id is required") {
		t.Errorf("missing diff_id: error = %q", resp.Error)
	}

	// Two reviewers: prefs are re-read for every command.
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	rev := rig.call(t, Request{Cmd: CmdReviewDiff, DiffID: diffID})
	if len(rev.Reviews) != 2 {
		t.Fatalf("reviews = %d, want 2", len(rev.Reviews))
	}
	if want := (ReviewResult{Model: "rm1@test", Verdict: "accept", Comments: "Ship it."}); rev.Reviews[0] != want {
		t.Errorf("review[0] = %+v, want %+v", rev.Reviews[0], want)
	}
	if want := (ReviewResult{Model: "rm2@test", Verdict: "reject", Comments: "Needs tests."}); rev.Reviews[1] != want {
		t.Errorf("review[1] = %+v, want %+v", rev.Reviews[1], want)
	}
	// The marker files prove both reviewers started; the stub exits 1 when
	// they did not overlap, which would have surfaced as a failed review
	// (verdict needs_fixes, "review failed:" comments) above.
	for _, m := range []string{"rm1", "rm2"} {
		if _, err := os.Stat(filepath.Join(markerDir, m+".started")); err != nil {
			t.Errorf("marker for %s missing: %v", m, err)
		}
	}

	// The review is journaled as a review_action with action moa_review.
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	last := events[len(events)-1]
	if last.Type != store.EventReviewAction {
		t.Fatalf("last event = %s, want review_action", last.Type)
	}
	var payload struct {
		Action  string `json:"action"`
		DiffID  int64  `json:"diff_id"`
		Reviews []struct {
			Model   string `json:"model"`
			Verdict string `json:"verdict"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatalf("review_action payload: %v", err)
	}
	if payload.Action != "moa_review" || payload.DiffID != diffID {
		t.Errorf("review_action payload = action %q diff %d", payload.Action, payload.DiffID)
	}
	if len(payload.Reviews) != 2 {
		t.Errorf("journaled reviews = %d, want 2", len(payload.Reviews))
	}
}

// TestGetSettings covers get_settings: absent prefs yield the compiled-in
// defaults; a full prefs.md overrides every field.
func TestGetSettings(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	// No prefs.md yet: everything falls back to the adapter defaults.
	got := rig.call(t, Request{Cmd: CmdGetSettings, ProjectRoot: root})
	if got.Settings == nil {
		t.Fatal("get_settings: settings missing")
	}
	want := Settings{
		CodingModel:          "t9s/kimi-k3",
		CodingProvider:       "sudo",
		OrchestratorModel:    "t9s/kimi-k3",
		OrchestratorProvider: "sudo",
		OMPTimeout:           "600",
		DefaultAdapter:       "omp",
		ReviewModels:         "",
	}
	if *got.Settings != want {
		t.Errorf("defaults = %+v, want %+v", *got.Settings, want)
	}

	// A full prefs.md overrides every field.
	writePrefs(t, home, "# my prefs\ncoding: glm-5.2@sudo\norchestrator: orch-model@orch-prov\nreview: rm1@test,rm2@test\nomp_timeout: 900\ndefault_adapter: pi\n")
	got = rig.call(t, Request{Cmd: CmdGetSettings, ProjectRoot: root})
	want = Settings{
		CodingModel:          "glm-5.2",
		CodingProvider:       "sudo",
		OrchestratorModel:    "orch-model",
		OrchestratorProvider: "orch-prov",
		OMPTimeout:           "900",
		DefaultAdapter:       "pi",
		ReviewModels:         "rm1@test,rm2@test",
	}
	if *got.Settings != want {
		t.Errorf("from prefs = %+v, want %+v", *got.Settings, want)
	}
}

// TestUpdateSettings covers update_settings: non-empty fields are updated in
// place or appended, a half-given model pair keeps the file's other half,
// and unmanaged lines pass through untouched.
func TestUpdateSettings(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	writePrefs(t, home, "# my prefs\ncoding: glm-5.2@sudo\n")
	rig := startRig(t, root)
	defer rig.stop(t)

	// A request without a settings object is an error.
	resp := rig.callExpectErr(t, Request{Cmd: CmdUpdateSettings, ProjectRoot: root})
	if !strings.Contains(resp.Error, "settings object is required") {
		t.Errorf("nil settings: error = %q", resp.Error)
	}

	upd := rig.call(t, Request{
		Cmd:         CmdUpdateSettings,
		ProjectRoot: root,
		Settings: &Settings{
			CodingModel:    "t9s/kimi-k3", // provider keeps the file's "sudo"
			ReviewModels:   "rm1@test,rm2@test",
			OMPTimeout:     "900",
			DefaultAdapter: "pi",
		},
	})
	s := *upd.Settings
	if s.CodingModel != "t9s/kimi-k3" || s.CodingProvider != "sudo" {
		t.Errorf("coding after update = %s@%s", s.CodingModel, s.CodingProvider)
	}
	if s.ReviewModels != "rm1@test,rm2@test" || s.OMPTimeout != "900" || s.DefaultAdapter != "pi" {
		t.Errorf("settings after update = %+v", s)
	}

	// Exact rewrite: comment kept, coding updated in place, new keys appended.
	content, err := os.ReadFile(filepath.Join(home, ".odo", "prefs.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# my prefs\ncoding: t9s/kimi-k3@sudo\nreview: rm1@test,rm2@test\nomp_timeout: 900\ndefault_adapter: pi\n"
	if string(content) != want {
		t.Errorf("prefs.md = %q, want %q", content, want)
	}

	// A later update touching one key leaves everything else byte-identical.
	rig.call(t, Request{
		Cmd:         CmdUpdateSettings,
		ProjectRoot: root,
		Settings:    &Settings{OMPTimeout: "1200"},
	})
	content, err = os.ReadFile(filepath.Join(home, ".odo", "prefs.md"))
	if err != nil {
		t.Fatal(err)
	}
	want = "# my prefs\ncoding: t9s/kimi-k3@sudo\nreview: rm1@test,rm2@test\nomp_timeout: 1200\ndefault_adapter: pi\n"
	if string(content) != want {
		t.Errorf("prefs.md after second update = %q, want %q", content, want)
	}
}

// TestFanoutSend covers parallel agent fan-out: one message starts N runs,
// each producing its own diff in its own worktree; the runs are tracked in
// poll_events while active, and reviewing one diff retires only its own
// run, leaving sibling diffs reviewable.
func TestFanoutSend(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	sent := rig.call(t, Request{Cmd: CmdFanoutSend, ConversationID: convID, Text: "Create hello.txt", N: 2})
	if sent.Event == nil || sent.Event.Type != store.EventUserMessage {
		t.Fatalf("fanout_send: bad event %+v", sent.Event)
	}
	if len(sent.Runs) != 2 {
		t.Fatalf("fanout_send: runs = %d, want 2", len(sent.Runs))
	}
	for _, r := range sent.Runs {
		if r.Status != "running" {
			t.Errorf("run %s status = %q, want running", r.RunID, r.Status)
		}
	}
	if sent.Runs[0].RunID == sent.Runs[1].RunID {
		t.Error("fanout runs share a run id")
	}

	// A plain send is refused while the fan-out runs.
	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "more work"})
	if !strings.Contains(resp.Error, "already running") {
		t.Errorf("send during fanout: error = %q", resp.Error)
	}

	// Both runs finish and report done; each journaled its own agent events.
	done := rig.pollUntilDone(t, convID)
	if len(done.Runs) != 2 {
		t.Fatalf("final poll: runs = %d, want 2", len(done.Runs))
	}
	for _, r := range done.Runs {
		if r.Status != "done" {
			t.Errorf("run %s final status = %q, want done", r.RunID, r.Status)
		}
	}
	var texts, dones int
	for _, e := range rig.allEventTypes(t, convID) {
		switch e {
		case store.EventAgentText:
			texts++
		case store.EventAgentDone:
			dones++
		}
	}
	if texts != 2 || dones != 2 {
		t.Errorf("agent events: %d texts, %d dones; want 2 each", texts, dones)
	}

	// Each run produced its own pending diff (distinct files, both real).
	rows, err := rig.store.DB().QueryContext(context.Background(),
		`SELECT id, path_on_disk, status FROM diffs WHERE conversation_id = ? ORDER BY id`, convID)
	if err != nil {
		t.Fatalf("list diffs: %v", err)
	}
	defer rows.Close()
	var diffIDs []int64
	var diffPaths []string
	for rows.Next() {
		var id int64
		var path, status string
		if err := rows.Scan(&id, &path, &status); err != nil {
			t.Fatal(err)
		}
		if status != store.DiffPending {
			t.Errorf("diff %d status = %q, want pending", id, status)
		}
		diffIDs = append(diffIDs, id)
		diffPaths = append(diffPaths, path)
	}
	if len(diffIDs) != 2 {
		t.Fatalf("diffs = %d, want 2", len(diffIDs))
	}
	if diffPaths[0] == diffPaths[1] {
		t.Error("both diffs share a path")
	}
	for _, p := range diffPaths {
		b, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(b), "hello.txt") {
			t.Errorf("diff %s missing hello.txt (err=%v)", p, err)
		}
	}

	// Accepting one diff retires only its own run and worktree.
	acc := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: diffIDs[0]})
	if !acc.Applied {
		t.Error("accept_diff: applied must be true")
	}
	poll := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
	if len(poll.Runs) != 1 {
		t.Fatalf("runs after first review = %d, want 1", len(poll.Runs))
	}
	entries, err := os.ReadDir(filepath.Join(root, ".odo", "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("worktrees after first review = %d, want 1", len(entries))
	}
	d, err := rig.store.GetDiff(context.Background(), diffIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != store.DiffPending {
		t.Errorf("sibling diff status = %q, want pending", d.Status)
	}

	// Rejecting the sibling diff retires it too; the fan-out is drained.
	rig.call(t, Request{Cmd: CmdRejectDiff, DiffID: diffIDs[1]})
	poll = rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
	if len(poll.Runs) != 0 {
		t.Errorf("runs after both reviews = %d, want 0", len(poll.Runs))
	}
	entries, err = os.ReadDir(filepath.Join(root, ".odo", "worktrees"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("worktrees after both reviews = %d, want 0", len(entries))
	}
}
