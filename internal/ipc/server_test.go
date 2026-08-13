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

// gitStatus returns untrimmed `git status --porcelain` output. (gitOut's
// TrimSpace eats the leading space of an unstaged-modification marker when
// it lands on the first line, making XY-column assertions lie.)
func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	return string(out)
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
	// M12: the auto subsystem defaults ON in production; pre-M12 tests
	// assert byte-stable journals, so rigs dark-launch it (auto_test.go
	// opts back in per test).
	srv.autoDisabled = true

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
	// M12: disarm pending auto-distill timers before closing the store —
	// a 120s timer firing into a closed journal outlives its test.
	r.server.mu.Lock()
	for id, entry := range r.server.autoPending {
		entry.timer.Stop()
		delete(r.server.autoPending, id)
	}
	r.server.mu.Unlock()
	// M17: drain detached auto-curates before closing the store — F3's
	// fail-open evaluation goroutine would otherwise journal into a
	// closed journal (or hold journal files open past TempDir cleanup).
	r.server.curateWG.Wait()
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
	// HOME isolation: readUserMemory injects real ~/.odo/user.md into every
	// prompt otherwise, and hello.txt (the stub echoes its prompt) stops
	// matching the bare message text in the accept/reject assertions below.
	t.Setenv("HOME", t.TempDir())

	// Install the stub agent wrapper.
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	// Use a temp HOME so the test doesn't read real ~/.odo/user.md
	// (which would inject memory layers into the stub agent's output).
	home := t.TempDir()
	t.Setenv("HOME", home)

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

// noopStubWrapper stands in for an agent that changes nothing: transcript
// out, clean exit, empty worktree. Pins the no-diff code path.
const noopStubWrapper = `#!/bin/sh
output_file="$3"
sleep 1
printf 'Nothing needed changing.\n' > "$output_file"
exit 0
`

// TestNoDiffRunRetiresWorktree pins the P1 leak fix: a run that leaves no
// diff has nothing to review, so it is retired immediately — worktree
// removed, in-memory run closed — instead of leaking until a review action
// that can never come.
func TestNoDiffRunRetiresWorktree(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, noopStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "read only"})
	// The live run's worktree is tracked in-memory (schema v2: no
	// workstream-level binding column; the diff row would carry it).
	runID := rig.server.byConv[convID]
	if runID == "" {
		t.Fatal("run did not register in byConv")
	}
	wtPath := rig.server.runs[runID].worktreePath
	if wtPath == "" {
		t.Fatal("run meta has no worktree path")
	}

	done := rig.pollUntilDone(t, convID)
	if got, want := fmt.Sprint(eventTypes(done.Events)), "[agent_text agent_done]"; got != want {
		t.Fatalf("journaled agent events = %s, want %s", got, want)
	}
	if done.Diff != nil {
		t.Errorf("no-diff run journaled a diff: %+v", done.Diff)
	}
	if len(done.Diffs) != 0 {
		t.Errorf("no-diff run left pending diffs: %+v", done.Diffs)
	}
	// The worktree is gone without a review action.
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree %s still on disk after no-diff run", wtPath)
	}
	if n := len(rig.server.runs); n != 0 {
		t.Errorf("server still tracks %d runs after no-diff completion", n)
	}
	if n := len(rig.server.byConv); n != 0 {
		t.Errorf("server still binds %d conversations after no-diff completion", n)
	}
}

// TestReviewDuringLiveRunKeepsLiveRun pins the live-run guard in retireRun:
// the diff under review is the product of an EARLIER finished run, but
// byConv now binds a new in-flight run on the same conversation. Reviewing
// the old diff must retire nothing — previously the unconditional retire
// killed the in-flight agent (adapter.Close) and deleted its worktree
// mid-write, surfacing as "accept interrupted my session".
func TestReviewDuringLiveRunKeepsLiveRun(t *testing.T) {
	root := initRepo(t)
	// HOME isolation: readUserMemory injects the real ~/.odo/user.md into
	// the prompt the stub copies into hello.txt.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Run 1: full visible loop up to a pending diff.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "run one"})
	done1 := rig.pollUntilDone(t, convID)
	if done1.Diff == nil {
		t.Fatal("run 1: no diff")
	}

	// Run 2 starts on the same conversation while run 1's diff is still
	// pending — the review-during-run window. The slow stub keeps run 2 in
	// flight while the review of run 1's diff lands.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "run two"})
	runID2pre := rig.server.byConv[convID]
	if runID2pre == "" || rig.server.runs[runID2pre] == nil {
		t.Fatal("run 2 did not register in byConv")
	}
	liveWT := rig.server.runs[runID2pre].worktreePath

	acc := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: done1.Diff.ID})
	if !acc.Applied {
		t.Fatalf("accept run-1 diff: %+v", acc)
	}
	// The accept itself still landed run 1's change.
	if got := readFileStr(t, filepath.Join(root, "hello.txt")); got != "run one" {
		t.Errorf("hello.txt = %q, want run one's accepted content", got)
	}

	// The live run survived the review: still tracked as unfinished, and
	// ITS worktree (schema v2: per-run dirs) untouched.
	runID2 := rig.server.byConv[convID]
	if runID2 == "" {
		t.Fatal("accept unbound the live run's conversation")
	}
	if meta2 := rig.server.runs[runID2]; meta2 == nil || meta2.finished {
		t.Errorf("live run after review = %+v, want tracked and unfinished", meta2)
	}
	if _, err := os.Stat(liveWT); err != nil {
		t.Errorf("live run worktree removed by review: %v", err)
	}

	// Run 2 completes undisturbed (pollUntilDone asserts agent_running=true
	// on its first poll — direct evidence the review didn't kill it) and
	// produces its own diff carrying its own prompt.
	done2 := rig.pollUntilDone(t, convID)
	if done2.Diff == nil {
		t.Fatal("run 2: no diff after surviving the review")
	}
	if !strings.Contains(done2.Diff.Content, "run two") {
		t.Errorf("run 2 diff missing its own prompt:\n%s", done2.Diff.Content)
	}
}

// TestAcceptDoesNotSweepMainCheckout pins P0 end to end through the socket:
// accept commits only the diff's own files. Dirt the user left in the main
// checkout — a modified tracked file and an untracked scratch file — is
// neither staged, committed, nor reverted by the accept.
func TestAcceptDoesNotSweepMainCheckout(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "make hello"})
	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}

	// User state in the main checkout: tracked edit + untracked scratch.
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scratch.txt"), []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	acc := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: done.Diff.ID})
	if !acc.Applied {
		t.Fatalf("accept: %+v", acc)
	}

	// The accept commit contains exactly the diff's file.
	if got := gitOut(t, root, "show", "--format=", "--name-only", "HEAD"); got != "hello.txt" {
		t.Errorf("accept commit files = %q, want exactly hello.txt", got)
	}
	// The user's files survived the accept untouched and uncommitted.
	status := gitStatus(t, root)
	for _, want := range []string{" M README.md", "?? scratch.txt"} {
		if !strings.Contains(status, want) {
			t.Errorf("status missing %q after accept:\n%s", want, status)
		}
	}
	if got := readFileStr(t, filepath.Join(root, "README.md")); got != "# user edit\n" {
		t.Errorf("README.md = %q, want the user's edit intact", got)
	}
}

// --------------------------------------------------- fix-INT base freshness (accept path)

// realPatch generates an applicable unified diff for edit applied in a
// scratch clone of root at its current HEAD — hand-shaped patch text
// (patchSrc) doesn't match real file contents, and the accept path's
// git apply --3way adjudicates content, not just paths.
func realPatch(t *testing.T, root string, edit func(dir string)) string {
	t.Helper()
	scratch := t.TempDir()
	gitIn(t, scratch, "clone", "-q", root, ".")
	edit(scratch)
	gitIn(t, scratch, "add", "-A")
	patch := gitOut(t, scratch, "diff", "--cached", "HEAD")
	if patch == "" {
		t.Fatal("edit produced an empty patch")
	}
	// gitOut trims whitespace — restore the newline terminating the last
	// hunk line, without which git apply reads a corrupt patch.
	return patch + "\n"
}

// baseBoundDiff stores a pending diff row whose base_sha is root's CURRENT
// HEAD — handleDiffAction reads base_sha from the STORE (unlike autoLand's
// by-value diff), so the in-memory BaseSHA trick can't play here.
func baseBoundDiff(t *testing.T, f autonomyFixture, root, name, patch string) store.Diff {
	t.Helper()
	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := f.st.InsertDiff(context.Background(), f.c.ID, path, gitOut(t, root, "rev-parse", "HEAD"), "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// driftMain advances root's HEAD with one new file, returning the new HEAD.
func driftMain(t *testing.T, root, rel string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, rel), []byte("package src // drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", rel)
	gitIn(t, root, "commit", "-m", "drift")
	return gitOut(t, root, "rev-parse", "HEAD")
}

// resolutionRow returns the single review_action row with the given action
// (accept/reject) journaled for diff d.
func resolutionRow(t *testing.T, f autonomyFixture, d store.Diff, action string) map[string]interface{} {
	t.Helper()
	events, err := f.st.ListEvents(context.Background(), f.c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []map[string]interface{}
	for _, e := range events {
		if e.Type != store.EventReviewAction {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("event %d: %v", e.ID, err)
		}
		if p["action"] == action && p["diff_id"] == float64(d.ID) {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		t.Fatalf("review_action{%s} rows for diff %d = %d, want 1 (journal: %d events)", action, d.ID, len(found), len(events))
	}
	return found[0]
}

func reviewActionRowsFor(t *testing.T, f autonomyFixture, d store.Diff) []map[string]interface{} {
	t.Helper()
	events, err := f.st.ListEvents(context.Background(), f.c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]interface{}
	for _, e := range events {
		if e.Type != store.EventReviewAction {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("event %d: %v", e.ID, err)
		}
		if p["diff_id"] == float64(d.ID) {
			rows = append(rows, p)
		}
	}
	return rows
}

// TestAcceptBlocksStaleBase (fix-INT D1/D3/D4): the FINAL freshness check
// is inside the accept path itself — a diff whose stored base_sha no longer
// equals main HEAD was judged (and auto-land verified/panel-reviewed)
// against a tree that no longer exists. The refusal names BOTH shas, the
// diff stays pending, NOTHING journals, and the main tree carries only the
// drift commit.
func TestAcceptBlocksStaleBase(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "landed.go"), []byte("package src // landed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	head := driftMain(t, root, "src/drift.go")

	_, err := s.handleDiffAction(context.Background(), d.ID, "accept", "")
	if err == nil {
		t.Fatal("accept on a stale base: want error")
	}
	for _, want := range []string{*d.BaseSHA, head} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending", got.Status)
	}
	if rows := reviewActionRowsFor(t, f, d); len(rows) != 0 {
		t.Errorf("freshness refusal journaled %v, want no rows (the refusal needs no audit row)", rows)
	}
	if _, serr := os.Stat(filepath.Join(root, "src", "landed.go")); !os.IsNotExist(serr) {
		t.Error("landed.go present in main — the refusal must precede the apply")
	}
}

// TestAcceptFreshBaseProceeds: the gate's fresh side — base == HEAD applies
// cleanly, and the journaled accept row carries base_sha/head_sha (D5),
// pinning the exact tree the decision was made against. On a fresh accept
// the freshness head IS the stored base, so both keys equal it.
func TestAcceptFreshBaseProceeds(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "landed.go"), []byte("package src // landed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))

	resp, err := s.handleDiffAction(context.Background(), d.ID, "accept", "")
	if err != nil {
		t.Fatalf("accept on a fresh base: %v", err)
	}
	if !resp.Applied {
		t.Error("resp.Applied = false, want true")
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffAccepted {
		t.Errorf("diff status = %q, want accepted", got.Status)
	}
	if data, rerr := os.ReadFile(filepath.Join(root, "src", "landed.go")); rerr != nil || !strings.Contains(string(data), "landed") {
		t.Errorf("landed.go = %q, %v — the patch must apply to main", data, rerr)
	}
	p := resolutionRow(t, f, d, "accept")
	if p["base_sha"] != *d.BaseSHA {
		t.Errorf("base_sha = %v, want %s", p["base_sha"], *d.BaseSHA)
	}
	if p["head_sha"] != *d.BaseSHA {
		t.Errorf("head_sha = %v, want the pre-accept HEAD %s (fresh: freshness head == base)", p["head_sha"], *d.BaseSHA)
	}
}

// TestAcceptNilBaseGrandfathered (D4): pre-v2 journal rows carry no
// base_sha — the freshness gate SKIPS them (the auto path already
// fail-closes a missing base as base_unresolvable, so the skip re-opens no
// hole), and the accept row still records base_sha:"" + the operative head.
func TestAcceptNilBaseGrandfathered(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	// addDiff's "" base stores a nil base_sha — the pre-v2 row shape.
	d := f.addDiff(t, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "landed.go"), []byte("package src // landed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	head := gitOut(t, root, "rev-parse", "HEAD")

	resp, err := s.handleDiffAction(context.Background(), d.ID, "accept", "")
	if err != nil {
		t.Fatalf("accept on a grandfathered (nil base) diff: %v", err)
	}
	if !resp.Applied {
		t.Error("resp.Applied = false, want true")
	}
	p := resolutionRow(t, f, d, "accept")
	if p["base_sha"] != "" {
		t.Errorf("base_sha = %v, want \"\" for a grandfathered row", p["base_sha"])
	}
	if p["head_sha"] != head {
		t.Errorf("head_sha = %v, want the operative HEAD %s", p["head_sha"], head)
	}
}

// TestRejectIgnoresStaleBase: freshness adjudicates ACCEPTS — reject
// writes nothing to the tree, so a stale base must never turn a rejection
// away. The reject payload still records base_sha/head_sha as evidence.
func TestRejectIgnoresStaleBase(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", patchSrc("src/a.go", 1, 1, false)) // reject never consults the patch
	head := driftMain(t, root, "src/drift.go")

	if _, err := s.handleDiffAction(context.Background(), d.ID, "reject", ""); err != nil {
		t.Fatalf("reject on a stale base: %v", err)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffRejected {
		t.Errorf("diff status = %q, want rejected", got.Status)
	}
	p := resolutionRow(t, f, d, "reject")
	if p["base_sha"] != *d.BaseSHA {
		t.Errorf("base_sha = %v, want %s", p["base_sha"], *d.BaseSHA)
	}
	if p["head_sha"] != head {
		t.Errorf("head_sha = %v, want the operative HEAD %s", p["head_sha"], head)
	}
}

// TestStackedPendingDiffsSharedBaseSecondBlocks: two pending diffs cut from
// the SAME base (queued parallel runs). Accepting #1 moves main HEAD, so
// #2's stored base is instantly stale — exactly the window the final check
// covers. #2 blocks naming the shas and stays pending; #1's row is intact.
func TestStackedPendingDiffsSharedBaseSecondBlocks(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d1 := baseBoundDiff(t, f, root, "one.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "one.go"), []byte("package src // one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	d2 := baseBoundDiff(t, f, root, "two.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "two.go"), []byte("package src // two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	base := *d1.BaseSHA
	if *d2.BaseSHA != base {
		t.Fatalf("setup bug: bases differ (%s vs %s), want one shared base", *d2.BaseSHA, base)
	}

	if _, err := s.handleDiffAction(context.Background(), d1.ID, "accept", ""); err != nil {
		t.Fatalf("accept #1 on the shared base: %v", err)
	}
	head := gitOut(t, root, "rev-parse", "HEAD")
	if head == base {
		t.Fatal("accept #1 must move HEAD (the path-scoped accept commit)")
	}

	_, err := s.handleDiffAction(context.Background(), d2.ID, "accept", "")
	if err == nil || !strings.Contains(err.Error(), base) {
		t.Fatalf("accept #2 err = %v, want the stale-base refusal naming %s", err, base)
	}
	got1, err := f.st.GetDiff(context.Background(), d1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got1.Status != store.DiffAccepted {
		t.Errorf("#1 status = %q, want accepted", got1.Status)
	}
	got2, err := f.st.GetDiff(context.Background(), d2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Status != store.DiffPending {
		t.Errorf("#2 status = %q, want pending", got2.Status)
	}
	if p := resolutionRow(t, f, d1, "accept"); p["head_sha"] != base {
		t.Errorf("#1 head_sha = %v, want the shared base %s", p["head_sha"], base)
	}
	if rows := reviewActionRowsFor(t, f, d2); len(rows) != 0 {
		t.Errorf("#2 journaled %v, want no rows", rows)
	}
}

// TestRunWorktreesDetachedAndFresh covers the schema-v2 detach-only design
// (B-class workstream↔git redesign): run worktrees are detached HEADs —
// they never name an odo/<name> branch (a symbolic HEAD was decoration; the
// odo/* refs were the "already used by worktree / cannot force update"
// failure vector) — and every run starts fresh from the current main HEAD,
// so it includes previously accepted changes. Two full loops must leave
// zero refs under refs/heads/odo/.
func TestRunWorktreesDetachedAndFresh(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// --- run 1: detached worktree, no odo/* refs ---
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "detach run one"})
	runID1 := rig.server.byConv[convID]
	if runID1 == "" {
		t.Fatal("run 1 did not register in byConv")
	}
	done1 := rig.pollUntilDone(t, convID)
	if done1.Diff == nil {
		t.Fatal("run 1: no diff")
	}
	// Capture pre-accept: a review action retires the run and its worktree.
	wt1 := rig.server.runs[runID1].worktreePath
	if wt1 == "" {
		t.Fatal("run 1 meta has no worktree path")
	}
	if got := gitOut(t, wt1, "branch", "--show-current"); got != "" {
		t.Errorf("run 1 worktree branch = %q, want \"\" (detached HEAD)", got)
	}

	acc1 := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: done1.Diff.ID})
	if !acc1.Applied {
		t.Fatal("accept_diff run 1: applied must be true")
	}
	if got := gitOut(t, root, "for-each-ref", "--format=%(refname:short)", "refs/heads/odo/"); got != "" {
		t.Errorf("after accept 1: odo/* refs = %q, want none", got)
	}

	// --- run 2: fresh from the new main HEAD (accept 1's hello.txt exists) ---
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "detach run two"})
	runID2 := rig.server.byConv[convID]
	if runID2 == "" {
		t.Fatal("run 2 did not register in byConv")
	}
	done2 := rig.pollUntilDone(t, convID)
	if done2.Diff == nil {
		t.Fatal("run 2: no diff")
	}
	wt2 := rig.server.runs[runID2].worktreePath
	if wt2 == "" {
		t.Fatal("run 2 meta has no worktree path")
	}
	if _, err := os.Stat(filepath.Join(wt2, "hello.txt")); err != nil {
		t.Errorf("run 2 worktree missing hello.txt from accept 1 (not fresh from main HEAD): %v", err)
	}
	if got := gitOut(t, wt2, "branch", "--show-current"); got != "" {
		t.Errorf("run 2 worktree branch = %q, want \"\" (detached HEAD)", got)
	}

	acc2 := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: done2.Diff.ID})
	if !acc2.Applied {
		t.Fatal("accept_diff run 2: applied must be true")
	}
	if got := gitOut(t, root, "for-each-ref", "--format=%(refname:short)", "refs/heads/odo/"); got != "" {
		t.Errorf("after accept 2: odo/* refs = %q, want none", got)
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

	// list: only "main". Schema v2: workstreams own no git refs (N:0) —
	// branch and worktree_path columns are gone; nothing to assert there.
	list := rig.call(t, Request{Cmd: CmdListWorkstreams, ProjectRoot: root})
	if len(list.Workstreams) != 1 {
		t.Fatalf("list_workstreams = %d entries, want 1", len(list.Workstreams))
	}
	if list.Workstreams[0].ID != boot.Workstream.ID || list.Workstreams[0].Name != "main" {
		t.Errorf("list[0] = %+v", list.Workstreams[0])
	}

	// create: the name is sanitized for display hygiene (sanitizeBranchName
	// still serves names; it no longer creates any ref).
	created := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "Refactor / auth module!"})
	if created.Workstream == nil {
		t.Fatal("create_workstream: missing workstream")
	}
	if created.Workstream.Name != "Refactor-auth-module" {
		t.Errorf("sanitized name = %q", created.Workstream.Name)
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
// without starting a run, queue the text for the continuation run (A2-lite),
// and are journaled silently when no agent is active.
func TestSteering(t *testing.T) {
	t.Run("active run queues the steer text", func(t *testing.T) {
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

		// A2-lite: the steer text is queued in the run's meta (not written
		// to a dead steering.txt file). The run completes normally, then
		// a continuation run starts with the queued text as the prompt.
		rig.pollUntilDone(t, convID)

		// The first run's events should be intact.
		types := rig.allEventTypes(t, convID)
		hasDone := false
		for _, ty := range types {
			if ty == "agent_done" {
				hasDone = true
			}
		}
		if !hasDone {
			t.Errorf("expected agent_done in events: %v", types)
		}

		// The steer message was journaled as a user_message with steer:true.
		// Poll events to get the full list including the steer payload.
		resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
		var steerFound bool
		for _, ev := range resp.Events {
			if ev.Type == store.EventUserMessage && ev.Seq == 2 {
				var payload map[string]interface{}
				if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
					t.Fatal(err)
				}
				if payload["steer"] != true {
					t.Errorf("steer event payload = %v, want steer:true", payload)
				}
				steerFound = true
			}
		}
		if !steerFound {
			t.Error("steer user_message event (seq 2) not found in poll")
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
	// HOME isolation: maybeAutoLand (drainRun) reads ~/.odo/prefs.md on the
	// real host; with auto_apply:main (M16-active dev machines) the run
	// below auto-reviews and inserts a review_action before the distill's.
	t.Setenv("HOME", t.TempDir())
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

	// The second distill folds only events journaled after marker #1 — an
	// empty window is now rejected — so give it a fresh run to fold.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Update hello.txt"})
	rig.pollUntilDone(t, convID)

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
	// M18 batch B pins: every leg journals the scrubbed endpoint it truly
	// hit (base_url — the httptest stub has no userinfo, so it rides
	// verbatim); a non-accept leg journals thinking_md (this stub emits no
	// thinking blocks, so the approximation = the leg's full response
	// text); ACCEPT legs stay unjournaled.
	if want := (ReviewResult{Model: "rm1@test", Verdict: "accept", Comments: "Ship it.", BaseURL: moaSrv.URL}); rev.Reviews[0] != want {
		t.Errorf("review[0] = %+v, want %+v", rev.Reviews[0], want)
	}
	if want := (ReviewResult{Model: "rm2@test", Verdict: "reject", Comments: "Needs tests.", ThinkingMD: "REJECT\n\nNeeds tests.", BaseURL: moaSrv.URL}); rev.Reviews[1] != want {
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

	// A4-lite: the review_action event carries a consensus_verdict.
	var fullPayload struct {
		ConsensusVerdict string `json:"consensus_verdict"`
	}
	if err := json.Unmarshal(last.Payload, &fullPayload); err != nil {
		t.Fatalf("consensus payload: %v", err)
	}
	// rm1=accept + rm2=reject → any reject → "reject"
	if fullPayload.ConsensusVerdict != "reject" {
		t.Errorf("consensus_verdict = %q, want %q", fullPayload.ConsensusVerdict, "reject")
	}
	// The response also carries the consensus field.
	if rev.Consensus != "reject" {
		t.Errorf("Response.Consensus = %q, want %q", rev.Consensus, "reject")
	}
}

// TestConsensusVerdict tests the deterministic unanimous tally: accept
// requires every reviewer; any reject dominates; a lone needs_fixes is
// dissent and must NOT read as accept (the former 2/3 tally failed open).
func TestConsensusVerdict(t *testing.T) {
	tests := []struct {
		name    string
		reviews []ReviewResult
		want    string
	}{
		{"empty", nil, "needs_fixes"},
		{"single accept", []ReviewResult{{Verdict: "accept"}}, "accept"},
		{"single reject", []ReviewResult{{Verdict: "reject"}}, "reject"},
		{"single needs_fixes", []ReviewResult{{Verdict: "needs_fixes"}}, "needs_fixes"},
		// Fail-open regression: under the 2/3 tally this read "accept" at N=3.
		{"2/3 accept + 1 needs_fixes is NOT accept", []ReviewResult{{Verdict: "accept"}, {Verdict: "accept"}, {Verdict: "needs_fixes"}}, "needs_fixes"},
		{"3/3 accept", []ReviewResult{{Verdict: "accept"}, {Verdict: "accept"}, {Verdict: "accept"}}, "accept"},
		{"1/3 reject dominates", []ReviewResult{{Verdict: "accept"}, {Verdict: "accept"}, {Verdict: "reject"}}, "reject"},
		{"2/3 reject", []ReviewResult{{Verdict: "reject"}, {Verdict: "reject"}, {Verdict: "accept"}}, "reject"},
		{"all needs_fixes", []ReviewResult{{Verdict: "needs_fixes"}, {Verdict: "needs_fixes"}, {Verdict: "needs_fixes"}}, "needs_fixes"},
		{"N=2 both accept", []ReviewResult{{Verdict: "accept"}, {Verdict: "accept"}}, "accept"},
		{"N=2 split", []ReviewResult{{Verdict: "accept"}, {Verdict: "needs_fixes"}}, "needs_fixes"},
		{"N=2 one reject", []ReviewResult{{Verdict: "accept"}, {Verdict: "reject"}}, "reject"},
		{"N=4 three accept one needs_fixes is NOT accept", []ReviewResult{{Verdict: "accept"}, {Verdict: "accept"}, {Verdict: "accept"}, {Verdict: "needs_fixes"}}, "needs_fixes"},
		{"N=4 two accept one reject", []ReviewResult{{Verdict: "accept"}, {Verdict: "accept"}, {Verdict: "reject"}, {Verdict: "needs_fixes"}}, "reject"},
		// A review call that errored degrades to needs_fixes (reviewWithModel):
		// it must block accept just like a deliberate dissent.
		{"degraded review blocks accept", []ReviewResult{{Verdict: "accept"}, {Verdict: "accept"}, {Verdict: "needs_fixes", Comments: "review failed: boom"}}, "needs_fixes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := consensusVerdict(tt.reviews); got != tt.want {
				t.Errorf("consensusVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestParseVerdict pins the verdict-line contract: the LAST verdict-token
// line wins (mid-analysis lookalikes and stray early tokens must not
// override the concluding verdict), comments are everything after it, and
// unparseable output degrades to needs_fixes. The early-ACCEPT case is the
// M16 panel regression: under first-match it read "accept".
func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		wantVerdict  string
		wantComments string
	}{
		{"verdict last with analysis before", "Analysis line one.\nAnalysis line two.\nACCEPT", "accept", "Analysis line one.\nAnalysis line two."},
		{"verdict first then comments", "NEEDS_FIXES\nbecause reasons", "needs_fixes", "because reasons"},
		{"early ACCEPT then concluding needs_fixes", "ACCEPT\n...wait, one problem:\nit drops a caller.\nNEEDS_FIXES\nfix the caller", "needs_fixes", "fix the caller"},
		{"early reject token then concluding accept", "REJECT tentative\nreconsidering...\nACCEPT\nship it", "accept", "ship it"},
		{"prefix form ACCEPT with trailing words", "looks fine\nACCEPT with minor nits", "accept", "looks fine"},
		// The prompt shape (think first, verdict on the FINAL line) — the
		// #16 auto_land_blocked row recorded three empty comments because
		// post-verdict text was empty for every vote. The fallback must
		// capture each vote's analysis as its justification.
		{"dissent keeps its reasons (M16 panel row)", "1. The retry path could double-apply the patch.\n2. Test does not cover the conflict branch.\n3. The lock order comment may be stale.\nREJECT", "reject", "1. The retry path could double-apply the patch.\n2. Test does not cover the conflict branch.\n3. The lock order comment may be stale."},
		{"prose mention does not match", "I cannot ACCEPT this patch", "needs_fixes", "I cannot ACCEPT this patch"},
		{"case-insensitive token", "accept", "accept", ""},
		{"no verdict line degrades", "just some analysis\nnothing conclusive", "needs_fixes", "just some analysis\nnothing conclusive"},
		{"empty", "", "needs_fixes", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVerdict("m", tt.text)
			if got.Verdict != tt.wantVerdict {
				t.Errorf("verdict = %q, want %q", got.Verdict, tt.wantVerdict)
			}
			if got.Comments != tt.wantComments {
				t.Errorf("comments = %q, want %q", got.Comments, tt.wantComments)
			}
		})
	}
}

// TestReviewVerdictTruncation pins the fail-closed truncation contract
// (M16 panel: a cut-off stream cannot prove the model's final position —
// even a cleanly parsing partial verdict counts as needs_fixes).
func TestReviewVerdictTruncation(t *testing.T) {
	rr := reviewVerdict("m", "So far so good.\nACCEPT", true)
	if rr.Verdict != "needs_fixes" {
		t.Errorf("truncated verdict = %q, want forced needs_fixes", rr.Verdict)
	}
	if rr.Comments == "" || !strings.Contains(rr.Comments, "truncated") {
		t.Errorf("truncated comments must carry the marker, got %q", rr.Comments)
	}
	if rr := reviewVerdict("m", "looks fine\nACCEPT", false); rr.Verdict != "accept" {
		t.Errorf("untruncated verdict = %q, want accept", rr.Verdict)
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
		CodingModel:            "t9s/kimi-k3",
		CodingProvider:         "sudo",
		OrchestratorModel:      "t9s/kimi-k3",
		OrchestratorProvider:   "sudo",
		OMPTimeout:             "1800",
		ReviewModels:           "",
		AutoDistill:            "on_idle",
		AutoDistillIdleSeconds: "120",
		MaxConcurrentRuns:      "4",
		AutoApply:              "off",
	}
	if *got.Settings != want {
		t.Errorf("defaults = %+v, want %+v", *got.Settings, want)
	}

	// A full prefs.md overrides every field. Explicit auto_distill: never
	// survives the M12 default flip.
	writePrefs(t, home, "# my prefs\ncoding: glm-5.2@sudo\norchestrator: orch-model@orch-prov\nreview: rm1@test,rm2@test\nomp_timeout: 900\nauto_distill: never\n")
	got = rig.call(t, Request{Cmd: CmdGetSettings, ProjectRoot: root})
	want = Settings{
		CodingModel:            "glm-5.2",
		CodingProvider:         "sudo",
		OrchestratorModel:      "orch-model",
		OrchestratorProvider:   "orch-prov",
		OMPTimeout:             "900",
		ReviewModels:           "rm1@test,rm2@test",
		AutoDistill:            "never",
		AutoDistillIdleSeconds: "120",
		MaxConcurrentRuns:      "4",
		AutoApply:              "off",
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
			CodingModel:  "t9s/kimi-k3", // provider keeps the file's "sudo"
			ReviewModels: "rm1@test,rm2@test",
			OMPTimeout:   "900",
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
	want := buildPrompt(text, nil, memoryLayers{})
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

// TestPanelTruncationFlagged: a model that stays at stop_reason=max_tokens
// past its hard cap ships the partial answer FLAGGED — payload marker in the
// rendered text plus the structured budget ledger — never an error row.
func TestPanelTruncationFlagged(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "partial panel answer"}},
			"stop_reason": "max_tokens",
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// pm1 is an unknown model: fallback spec 16384 → escalates once → 32768 cap.
	writePrefs(t, home, "review: pm1@test\n")
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel analyze this"})

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	var found bool
	for _, ev := range events {
		if ev.Type != store.EventAgentText {
			continue
		}
		var p struct {
			Text   string `json:"text"`
			Panel  bool   `json:"panel"`
			Models []struct {
				Text        string `json:"text"`
				Truncated   bool   `json:"truncated"`
				Budget      int    `json:"budget"`
				Escalations []struct {
					From int `json:"from"`
					To   int `json:"to"`
				} `json:"escalations"`
			} `json:"models"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || !p.Panel {
			continue
		}
		found = true
		if len(p.Models) != 1 {
			t.Fatalf("models = %d, want 1", len(p.Models))
		}
		m := p.Models[0]
		if m.Text != "partial panel answer" {
			t.Errorf("text = %q, want the partial answer shipped", m.Text)
		}
		if !m.Truncated || m.Budget != 32768 {
			t.Errorf("flag = (%v, %d), want (true, 32768)", m.Truncated, m.Budget)
		}
		if len(m.Escalations) != 1 || m.Escalations[0].From != 16384 || m.Escalations[0].To != 32768 {
			t.Errorf("escalations = %+v, want 16384→32768", m.Escalations)
		}
		if !strings.Contains(p.Text, "[output truncated at the 32768-token cap after 1 budget escalation(s)]") {
			t.Errorf("rendered marker missing from text: %q", p.Text)
		}
	}
	if !found {
		t.Fatal("no journaled panel agent_text found")
	}
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
