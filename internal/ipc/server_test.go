package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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

// gitOut runs git like gitIn but returns trimmed stdout.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", dir, "-c", "user.email=odo@test", "-c", "user.name=odo"}, args...)
	out, err := exec.Command("git", argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// startRig builds a project repo and a live daemon bound to it.
func startRig(t *testing.T, root string) *testRig {
	t.Helper()
	// NewServer registers the bound project in the global registry; without
	// an override that write lands in the real user's ~/.odo. Tests pre-set
	// ODO_REGISTRY_PATH to seed siblings (bound registration appends to it).
	if os.Getenv("ODO_REGISTRY_PATH") == "" {
		t.Setenv("ODO_REGISTRY_PATH", filepath.Join(t.TempDir(), "projects.json"))
	}
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

// TestWorkstreamBranchAccumulatesAccepts covers M11c: runs on a workstream
// check out the odo/<name> branch (not a detached HEAD), and each accept
// advances that branch to the new main HEAD so the next run's worktree
// includes the previously accepted changes.
func TestWorkstreamBranchAccumulatesAccepts(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// --- run 1: the worktree is on the workstream branch, not detached ---
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "branch run one"})
	done1 := rig.pollUntilDone(t, convID)
	if done1.Diff == nil {
		t.Fatal("run 1: no diff")
	}
	bound1 := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if bound1.Workstream == nil || bound1.Workstream.WorktreePath == nil {
		t.Fatal("bootstrap: workstream has no bound worktree during run 1")
	}
	if got := gitOut(t, *bound1.Workstream.WorktreePath, "symbolic-ref", "HEAD"); got != "refs/heads/odo/main" {
		t.Errorf("run 1 worktree HEAD = %q, want refs/heads/odo/main", got)
	}

	acc1 := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: done1.Diff.ID})
	if !acc1.Applied {
		t.Fatal("accept_diff run 1: applied must be true")
	}
	// Accept advanced odo/main to the new main HEAD.
	if branch, head := gitOut(t, root, "rev-parse", "odo/main"), gitOut(t, root, "rev-parse", "HEAD"); branch != head {
		t.Errorf("after accept 1: odo/main = %s, HEAD = %s, want equal", branch, head)
	}

	// --- run 2: the branch checkout includes run 1's accepted change ---
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "branch run two"})
	done2 := rig.pollUntilDone(t, convID)
	if done2.Diff == nil {
		t.Fatal("run 2: no diff")
	}
	bound2 := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if bound2.Workstream == nil || bound2.Workstream.WorktreePath == nil {
		t.Fatal("bootstrap: workstream has no bound worktree during run 2")
	}
	// hello.txt was committed to main (and odo/main) by run 1's accept, so
	// it exists in run 2's branch checkout.
	if _, err := os.Stat(filepath.Join(*bound2.Workstream.WorktreePath, "hello.txt")); err != nil {
		t.Errorf("run 2 worktree missing hello.txt from accept 1: %v", err)
	}

	acc2 := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: done2.Diff.ID})
	if !acc2.Applied {
		t.Fatal("accept_diff run 2: applied must be true")
	}
	if branch, head := gitOut(t, root, "rev-parse", "odo/main"), gitOut(t, root, "rev-parse", "HEAD"); branch != head {
		t.Errorf("after accept 2: odo/main = %s, HEAD = %s, want equal", branch, head)
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
// surface a friendly agent_error on adapters without steering, and are
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

// TestCancelRun covers the cancel command: with a run in flight it kills
// the agent process, journals agent_error{cancelled by user} immediately,
// and the normal drain path finishes the run on the next poll; with no
// active run it refuses cleanly.
func TestCancelRun(t *testing.T) {
	t.Run("active run is killed and journaled", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})

		// The slow stub sleeps 3s, so the run is still active when cancel
		// lands; the command itself answers ok with no event payload.
		cancelled := rig.call(t, Request{Cmd: CmdCancel, ConversationID: convID})
		if cancelled.Event != nil {
			t.Fatalf("cancel event = %+v, want none (error is journaled, not returned)", cancelled.Event)
		}

		// Drain until the killed process reports terminal. The cancelled-by-
		// user error is already in the journal; the adapter's own terminal
		// agent_error (process killed) follows on a later poll.
		var sawCancel bool
		afterSeq := 0
		deadline := time.Now().Add(20 * time.Second)
		for {
			resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: afterSeq})
			for _, ev := range resp.Events {
				afterSeq = ev.Seq
				if ev.Type == store.EventAgentError {
					var payload map[string]interface{}
					if err := json.Unmarshal(ev.Payload, &payload); err != nil {
						t.Fatalf("agent_error payload: %v", err)
					}
					if payload["error"] == "cancelled by user" {
						sawCancel = true
					}
				}
			}
			if resp.AgentRunning == nil {
				t.Fatal("poll_events: agent_running missing")
			}
			if !*resp.AgentRunning {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("cancelled run did not finish within 20s")
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !sawCancel {
			t.Error("agent_error{cancelled by user} was not journaled")
		}
		if got, want := fmt.Sprint(rig.allEventTypes(t, convID)),
			"[user_message agent_error agent_error]"; got != want {
			t.Errorf("events = %s, want %s", got, want)
		}

		// A cancel settles the run: a fresh send is accepted immediately and
		// that run completes normally (proving byConv released the slot).
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt again"})
		rig.pollUntilDone(t, convID)
	})

	t.Run("no active run refuses cleanly", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		resp := rig.callExpectErr(t, Request{Cmd: CmdCancel, ConversationID: convID})
		if !strings.Contains(resp.Error, "no active run") {
			t.Errorf("cancel error = %q, want no active run", resp.Error)
		}
	})

	t.Run("finished run refuses cleanly", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
		rig.pollUntilDone(t, convID)

		resp := rig.callExpectErr(t, Request{Cmd: CmdCancel, ConversationID: convID})
		if !strings.Contains(resp.Error, "no active run") {
			t.Errorf("cancel after finish error = %q, want no active run", resp.Error)
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
	// M4: every distill is followed by the learner pass. The stub returns
	// plain text (not the learner's JSON contract), so the learner degrades
	// to a journaled memory_update{layer:learner,cause:failed} — never a
	// distill failure (spec §2).
	if got, want := fmt.Sprint(rig.allEventTypes(t, convID)),
		"[user_message agent_text agent_done memory_update review_action]"; got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	var muPayload map[string]interface{}
	if err := json.Unmarshal(events[len(events)-2].Payload, &muPayload); err != nil {
		t.Fatalf("memory_update payload: %v", err)
	}
	if muPayload["layer"] != "learner" || muPayload["cause"] != "failed" {
		t.Errorf("memory_update payload = %v, want learner/failed", muPayload)
	}
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
	if err != nil || len(matches) != 3 { // user prompt + distill prompt + learner prompt (M4)
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
	// Mock the MoA API: return verdicts based on the model name.
	moaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var text string
		switch req.Model {
		case "rm1":
			text = "ACCEPT\n\nShip it."
		case "rm2":
			text = "REJECT\n\nNeeds tests."
		default:
			text = "NEEDS_FIXES\n\nUnknown model."
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": "end_turn",
		})
	}))
	defer moaSrv.Close()
	t.Setenv("MOA_BASE_URL", moaSrv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")
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
		CodingModel:             "t9s/kimi-k3",
		CodingProvider:          "sudo",
		OrchestratorModel:       "t9s/kimi-k3",
		OrchestratorProvider:    "sudo",
		OMPTimeout:              "600",
		ReviewModels:            "",
		AutoDistill:             "never",
		AutoDistillIdleSeconds:  "30",
		AutoCurateAfterDistill:  "false",
		MaxConcurrentRuns:       "4",
	}
	if *got.Settings != want {
		t.Errorf("defaults = %+v, want %+v", *got.Settings, want)
	}

	// A full prefs.md overrides every field.
	writePrefs(t, home, "# my prefs\ncoding: glm-5.2@sudo\norchestrator: orch-model@orch-prov\nreview: rm1@test,rm2@test\nomp_timeout: 900\n")
	got = rig.call(t, Request{Cmd: CmdGetSettings, ProjectRoot: root})
	want = Settings{
		CodingModel:             "glm-5.2",
		CodingProvider:          "sudo",
		OrchestratorModel:       "orch-model",
		OrchestratorProvider:    "orch-prov",
		OMPTimeout:              "900",
		ReviewModels:            "rm1@test,rm2@test",
		AutoDistill:             "never",
		AutoDistillIdleSeconds:  "30",
		AutoCurateAfterDistill:  "false",
		MaxConcurrentRuns:       "4",
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
		},
	})
	s := *upd.Settings
	if s.CodingModel != "t9s/kimi-k3" || s.CodingProvider != "sudo" {
		t.Errorf("coding after update = %s@%s", s.CodingModel, s.CodingProvider)
	}
	if s.ReviewModels != "rm1@test,rm2@test" || s.OMPTimeout != "900" {
		t.Errorf("settings after update = %+v", s)
	}

	// Exact rewrite: comment kept, coding updated in place, new keys appended.
	content, err := os.ReadFile(filepath.Join(home, ".odo", "prefs.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# my prefs\ncoding: t9s/kimi-k3@sudo\nreview: rm1@test,rm2@test\nomp_timeout: 900\n"
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
	want = "# my prefs\ncoding: t9s/kimi-k3@sudo\nreview: rm1@test,rm2@test\nomp_timeout: 1200\n"
	if string(content) != want {
		t.Errorf("prefs.md after second update = %q, want %q", content, want)
	}
}


// --- M3: memory recall (~/.odo/user.md + wiki notes) + wiki browser IPC ---
//
// The stub wrapper copies the prompt file into hello.txt in the worktree,
// so the diff content after a run IS the prompt buildPrompt produced. Every
// test pins HOME to a temp dir for hermeticity (the dev machine may have a
// real ~/.odo/user.md one day).

// writeUserMD writes ~/.odo/user.md under the test home directory.
func writeUserMD(t *testing.T, home, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".odo", "user.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// recallPathsFromEvent extracts the journaled recall paths from a
// user_message event payload (nil when the key is absent).
func recallPathsFromEvent(t *testing.T, ev *store.Event) []string {
	t.Helper()
	if ev == nil {
		t.Fatal("missing user_message event")
	}
	var p map[string]interface{}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	raw, ok := p["recall"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		switch item := v.(type) {
		case string: // pre-M6 shape: bare paths
			out = append(out, item)
		case map[string]interface{}: // M6: {"path": …, "matched_terms"?}
			s, ok := item["path"].(string)
			if !ok {
				t.Fatalf("recall entry missing path: %v", v)
			}
			out = append(out, s)
		default:
			t.Fatalf("recall entry not a string/object: %v", v)
		}
	}
	return out
}

// TestRecallInjectsWikiNote verifies the M3 distill-loop closure: a note
// under wiki/ is injected into the next run's prompt and journaled in the
// user_message payload's recall list.
func TestRecallInjectsWikiNote(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	sentinel := "PRIOR DECISION SENTINEL: refresh tokens live at /auth/refresh"
	notePath := filepath.Join(root, "wiki", "main-epoch-1.md")
	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte("# Epoch 1\n\n"+sentinel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Continue the auth refactor"})
	if recall := recallPathsFromEvent(t, sent.Event); len(recall) != 1 || recall[0] != notePath {
		t.Fatalf("recall = %v, want [%s]", recall, notePath)
	}

	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}
	if !strings.Contains(done.Diff.Content, sentinel) {
		t.Error("diff content (agent prompt) is missing the wiki note sentinel")
	}
}

// TestUserMDInjected verifies the global user memory: ~/.odo/user.md is
// injected into the prompt and listed first in the recall payload (before
// wiki paths). With an empty HOME the payload has no recall key at all
// (backward compatible).
func TestUserMDInjected(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	sentinel := "USER PRINCIPLE SENTINEL: first-principles reasoning, concise output"
	writeUserMD(t, home, sentinel+"\n")
	notePath := filepath.Join(root, "wiki", "main-epoch-1.md")
	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte("# Epoch 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Build the next piece"})
	if recall := recallPathsFromEvent(t, sent.Event); len(recall) != 2 || recall[0] != "~/.odo/user.md" || recall[1] != notePath {
		t.Fatalf("recall = %v, want [~/.odo/user.md %s]", recall, notePath)
	}
	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}
	if !strings.Contains(done.Diff.Content, sentinel) {
		t.Error("diff content (agent prompt) is missing the user.md sentinel")
	}

	// Second part: a fresh rig with an empty HOME recalls nothing.
	t.Setenv("HOME", t.TempDir())
	rig2 := startRig(t, initRepo(t))
	defer rig2.stop(t)
	boot2 := rig2.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: rig2.root})
	sent2 := rig2.call(t, Request{Cmd: CmdSendMessage, ConversationID: boot2.Conversation.ID, Text: "plain"})
	var p map[string]interface{}
	if err := json.Unmarshal(sent2.Event.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if _, ok := p["recall"]; ok {
		t.Errorf("recall key present without any memory: %v", p["recall"])
	}
	rig2.pollUntilDone(t, boot2.Conversation.ID)
}

// TestUserMDCap verifies readUserMemory caps ~/.odo/user.md at
// userMemoryCap with a line-boundary cut (no half line).
func TestUserMDCap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// ~8 KB of user memory: 300 lines of 28 bytes each (27 chars + newline).
	line := strings.Repeat("p", 27)
	lines := make([]string, 300)
	for i := range lines {
		lines[i] = line
	}
	content := strings.Join(lines, "\n") + "\n"
	writeUserMD(t, home, content)

	got := readUserMemory()
	if len(got) == 0 || len(got) > userMemoryCap {
		t.Fatalf("len(readUserMemory()) = %d, want (0, %d]", len(got), userMemoryCap)
	}
	if !strings.HasPrefix(content, got) {
		t.Fatal("capped memory is not a prefix of the file's lines")
	}
	if rest := content[len(got):]; !strings.HasPrefix(rest, "\n") {
		t.Errorf("cut lands mid-line: byte after the cut starts %q", rest[:1])
	}
}

// TestRecallEmptyWhenNoWiki is the backward-compatibility case: no wiki
// notes and no user.md → no recall key in the payload and the prompt is
// exactly what the current buildPrompt produces from the text alone.
func TestRecallEmptyWhenNoWiki(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	text := "Create hello.txt (no memory)"
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: text})
	var p map[string]interface{}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if _, ok := p["recall"]; ok {
		t.Errorf("recall key present without any memory: %v", p["recall"])
	}

	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}
	want := buildPrompt(text, nil, "", "", "", "", "", "")
	if want != text {
		t.Fatalf("buildPrompt(%q, nil, empty layers) = %q, want the text unchanged", text, want)
	}
	if !strings.Contains(done.Diff.Content, "+"+want) {
		t.Errorf("diff content does not contain the plain prompt")
	}
	if strings.Contains(done.Diff.Content, "memory (") {
		t.Error("diff content contains injected memory headers on a no-memory prompt")
	}
}

// TestRecallCapsSize verifies the wiki recall budget: five ~4 KB notes
// exceed recallMemoryCap, so only the newest epochs are recalled and the
// cut lands on a note boundary.
func TestRecallCapsSize(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	// Five notes, ~4 KB each: epoch N is a single line of letter 'a'+N-1,
	// uniquely identifiable inside the prompt/diff.
	wikiDir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 5; n++ {
		body := strings.Repeat(string(rune('a'+n-1)), 4000)
		p := filepath.Join(wikiDir, fmt.Sprintf("main-epoch-%d.md", n))
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Direct contract: the injected memory block never exceeds the cap.
	memory, items, noteBlocks := recallWikiNotes(root, "main", "", nil)
	if len(noteBlocks) != len(items) {
		t.Errorf("noteBlocks = %d, want %d (one injected block per item)", len(noteBlocks), len(items))
	}
	if len(memory) > recallMemoryCap {
		t.Errorf("len(memory) = %d, exceeds recallMemoryCap %d", len(memory), recallMemoryCap)
	}
	if len(items) == 0 || len(items) > 3 {
		t.Errorf("recalled %d notes, want 1..3", len(items))
	}

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "big recall"})
	recall := recallPathsFromEvent(t, sent.Event)
	if len(recall) > 3 {
		t.Errorf("journaled recall = %d paths, want ≤ 3", len(recall))
	}
	joined := strings.Join(recall, " ")
	for _, want := range []string{"epoch-5", "epoch-4", "epoch-3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("recall %v is missing %s (newest epochs win)", recall, want)
		}
	}
	for _, unwanted := range []string{"epoch-2", "epoch-1"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("recall %v unexpectedly includes %s", recall, unwanted)
		}
	}

	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}
	// The injected memory block in the prompt fits the cap plus the fixed
	// header overhead ("## <basename>" + separator per note).
	for n, ch := range map[int]string{5: "e", 4: "d", 3: "c"} {
		if !strings.Contains(done.Diff.Content, strings.Repeat(ch, 100)) {
			t.Errorf("diff content is missing the epoch %d note body", n)
		}
	}
	for n, ch := range map[int]string{2: "b", 1: "a"} {
		if strings.Contains(done.Diff.Content, strings.Repeat(ch, 100)) {
			t.Errorf("diff content contains the epoch %d note body; cap did not cut the oldest notes", n)
		}
	}
}


// TestListWiki verifies the list_wiki IPC: notes come back newest-epoch
// first with parsed name/epoch and a non-empty modified_at; a fresh
// workstream returns an empty list.
func TestListWiki(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	wikiDir := filepath.Join(root, "wiki")
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2} {
		p := filepath.Join(wikiDir, fmt.Sprintf("main-epoch-%d.md", n))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("# epoch %d\n", n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	list := rig.call(t, Request{Cmd: CmdListWiki, ConversationID: convID})
	if len(list.WikiNotes) != 2 {
		t.Fatalf("list_wiki = %d notes, want 2", len(list.WikiNotes))
	}
	if first := list.WikiNotes[0]; first.Name != "main-epoch-2" || first.Epoch != 2 {
		t.Errorf("first note = %+v, want main-epoch-2 (newest first)", first)
	}
	if second := list.WikiNotes[1]; second.Name != "main-epoch-1" || second.Epoch != 1 {
		t.Errorf("second note = %+v, want main-epoch-1", second)
	}
	for _, note := range list.WikiNotes {
		if wantPath := filepath.Join(wikiDir, note.Name+".md"); note.Path != wantPath {
			t.Errorf("note path = %q, want %q", note.Path, wantPath)
		}
		if note.ModifiedAt == "" {
			t.Errorf("note %s: modified_at is empty", note.Name)
		} else if _, err := time.Parse(time.RFC3339, note.ModifiedAt); err != nil {
			t.Errorf("note %s: modified_at %q is not RFC3339: %v", note.Name, note.ModifiedAt, err)
		}
	}

	// A fresh second workstream returns an empty list.
	created := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "feature-wiki"})
	boot2 := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: created.Workstream.ID})
	empty := rig.call(t, Request{Cmd: CmdListWiki, ConversationID: boot2.Conversation.ID})
	if len(empty.WikiNotes) != 0 {
		t.Errorf("list_wiki on a fresh workstream = %v, want empty", empty.WikiNotes)
	}
}

// TestReadWiki verifies the read_wiki path classes: wiki notes round-trip
// with exact content, ~/.odo/user.md is readable via its literal tilde
// path, a missing user.md is OK with empty content, a missing wiki note is
// an error, and anything outside those two classes is rejected.
func TestReadWiki(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	content := "# Epoch 1\n\nexact content with unicode — 你好\n"
	notePath := filepath.Join(root, "wiki", "main-epoch-1.md")
	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// The listed path round-trips to the exact content.
	list := rig.call(t, Request{Cmd: CmdListWiki, ConversationID: convID})
	if len(list.WikiNotes) != 1 {
		t.Fatalf("list_wiki = %d notes, want 1", len(list.WikiNotes))
	}
	got := rig.call(t, Request{Cmd: CmdReadWiki, Path: list.WikiNotes[0].Path})
	if got.WikiContent != content {
		t.Errorf("read_wiki content = %q, want %q", got.WikiContent, content)
	}

	// The global user.md is readable via the literal tilde path.
	userContent := "durable principle: concise output\n"
	writeUserMD(t, home, userContent)
	if got = rig.call(t, Request{Cmd: CmdReadWiki, Path: "~/.odo/user.md"}); got.WikiContent != userContent {
		t.Errorf("read_wiki user.md = %q, want %q", got.WikiContent, userContent)
	}

	// A missing user.md is OK with empty content (frontend create-hint).
	if err := os.Remove(filepath.Join(home, ".odo", "user.md")); err != nil {
		t.Fatal(err)
	}
	if got = rig.call(t, Request{Cmd: CmdReadWiki, Path: "~/.odo/user.md"}); got.WikiContent != "" {
		t.Errorf("read_wiki missing user.md = %q, want empty", got.WikiContent)
	}

	// A missing wiki note is an error.
	rig.callExpectErr(t, Request{Cmd: CmdReadWiki, Path: filepath.Join(root, "wiki", "main-epoch-99.md")})

	// Paths outside wiki/ and ~/.odo/user.md are rejected — including ../
	// traversal that stays inside the project root.
	for _, p := range []string{
		filepath.Join(root, "README.md"),
		"/etc/hosts",
		filepath.Join(root, "wiki", "..", "README.md"),
		filepath.Join(root, "wiki", "..", "..", "secret"),
	} {
		resp := rig.callExpectErr(t, Request{Cmd: CmdReadWiki, Path: p})
		if !strings.Contains(resp.Error, "only files under wiki/ or ~/.odo/user.md are readable") {
			t.Errorf("read_wiki %s: error = %q", p, resp.Error)
		}
	}
}

// TestPendingCounts covers the M3 §3c sidebar-badge fallback: pending diff
// counts keyed by workstream, the in-memory list of workstreams with a live
// run, and project-root validation. The slow wrapper keeps the run alive
// long enough to observe running_workstreams mid-run.
func TestPendingCounts(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	wsID := boot.Workstream.ID

	// Fresh project: no pending diffs, nothing running.
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if len(pc.PendingCounts) != 0 {
		t.Errorf("fresh pending_counts = %v, want empty", pc.PendingCounts)
	}
	if len(pc.RunningWorkstreams) != 0 {
		t.Errorf("fresh running_workstreams = %v, want empty", pc.RunningWorkstreams)
	}

	// Send a run and check mid-flight: workstream listed as running.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	mid := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if got, want := fmt.Sprint(mid.RunningWorkstreams), fmt.Sprint([]int64{wsID}); got != want {
		t.Errorf("mid-run running_workstreams = %s, want %s", got, want)
	}

	// After the run: one pending diff on the workstream, nothing running.
	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("poll_events: no diff after agent_done")
	}
	pc = rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.PendingCounts[wsID] != 1 {
		t.Errorf("pending_counts[%d] = %d, want 1 (all = %v)", wsID, pc.PendingCounts[wsID], pc.PendingCounts)
	}
	if len(pc.RunningWorkstreams) != 0 {
		t.Errorf("post-run running_workstreams = %v, want empty", pc.RunningWorkstreams)
	}

	// Accepting the diff drops the count to zero.
	rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: done.Diff.ID})
	pc = rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if len(pc.PendingCounts) != 0 {
		t.Errorf("post-accept pending_counts = %v, want empty", pc.PendingCounts)
	}

	// A foreign project root is refused.
	rig.callExpectErr(t, Request{Cmd: CmdPendingCounts, ProjectRoot: filepath.Join(root, "elsewhere")})
}

// moaMockServer returns a httptest server that mocks the MoA API and a
// cleanup function. Each call returns the model name in the text field so
// tests can verify routing. Callers must set MOA_BASE_URL and SUDO_CODING_KEY.
func moaMockServer(t *testing.T, text string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPanelRouting verifies /panel routing: prefix match routes to
// handlePanelQuery, /panelx falls through, no models configured is an
// error, and /panel while an agent is running is rejected.
func TestPanelRouting(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	// Mock the MoA API.
	moaSrv := moaMockServer(t, "panel response from model")
	t.Setenv("MOA_BASE_URL", moaSrv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Without a review: line the command refuses.
	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel analyze this"})
	if !strings.Contains(resp.Error, "No review models configured") {
		t.Errorf("/panel no models: error = %q", resp.Error)
	}

	// With models configured: /panel routes to handlePanelQuery.
	writePrefs(t, home, "review: pm1@test, pm2@test\n")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel analyze this"})

	// Verify the panel response is journaled as agent_text with panel=true.
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	var foundPanel bool
	for _, ev := range events {
		if ev.Type == store.EventAgentText {
			var p struct {
				Panel  bool `json:"panel"`
				Models []struct {
					Model string `json:"model"`
					Text  string `json:"text"`
				} `json:"models"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && p.Panel {
				foundPanel = true
				if len(p.Models) != 2 {
					t.Errorf("panel models = %d, want 2", len(p.Models))
				}
			}
		}
	}
	if !foundPanel {
		t.Error("/panel: no agent_text with panel=true found in events")
	}

	// /panelx does NOT route — falls through to normal send (agent run).
	// This creates a real agent run, so we poll it to completion.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panelx create hello.txt"})
	rig.pollUntilDone(t, convID)
}

// TestPanelWhileRunning verifies /panel is rejected while an agent run is active.
func TestPanelWhileRunning(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))

	moaSrv := moaMockServer(t, "panel response")
	t.Setenv("MOA_BASE_URL", moaSrv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	writePrefs(t, home, "review: pm1@test\n")

	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Start a slow agent run (3s delay in the wrapper).
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})

	// /panel while the agent is running should be rejected.
	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel analyze"})
	if !strings.Contains(resp.Error, "agent already running") {
		t.Errorf("/panel while running: error = %q, want 'agent already running'", resp.Error)
	}

	// Wait for the slow run to finish.
	rig.pollUntilDone(t, convID)
}

// TestVisionRouting verifies /vision routing: prefix match routes to
// handleVisionQuery, /visionx falls through, and no prompt is an error.
func TestVisionRouting(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	// Mock the MoA API.
	moaSrv := moaMockServer(t, "vision analysis from K3")
	t.Setenv("MOA_BASE_URL", moaSrv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// /vision with no prompt text is an error.
	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision "})
	if !strings.Contains(resp.Error, "prompt text is required") {
		t.Errorf("/vision empty: error = %q", resp.Error)
	}

	// /vision with a prompt routes to handleVisionQuery and journals a vision event.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision describe this screenshot"})

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	var foundVision bool
	for _, ev := range events {
		if ev.Type == store.EventAgentText {
			var p struct {
				Vision bool `json:"vision"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && p.Vision {
				foundVision = true
			}
		}
	}
	if !foundVision {
		t.Error("/vision: no agent_text with vision=true found in events")
	}

	// /visionx does NOT route — falls through to normal send.
	// This starts a real agent run, so poll it to completion.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/visionx create hello.txt"})
	rig.pollUntilDone(t, convID)
}
