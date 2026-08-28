package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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
	// stopflight guards the teardown: crash drills stop mid-test
	// (daemon restart) and their fatal-abort defer stops again — one
	// sync.Once replaces the hand-rolled stopped flags.
	stopflight sync.Once
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

// resetSharedMoa re-arms the Server's shared MoA client (P1 #10) for the
// next use. Production builds it ONCE per daemon lifetime (env and keys
// are fixed there); a test that hot-swaps MOA_BASE_URL mid-Server must
// reset explicitly, or every later leg keeps hitting the FIRST mock
// gateway (the sync.Once already consumed its URL).
func resetSharedMoa(t *testing.T, s *Server) {
	t.Helper()
	s.moaOnce = sync.Once{}
	s.moaShared = nil
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
	// C11: same posture for the liveness drain — default ON in production,
	// dark-launched in rigs so drains stay explicit (poll-driven) and
	// journals stay deterministic. liveness_test.go is the opt-in
	// coverage (the auto_test.go convention).
	srv.livenessDisabled.Store(true)

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

// restartRig stops the live rig and reopens the SAME journal with a fresh
// Server (all NewServer boot recoveries run — the parked / memory-replay
// crash drills stage a "daemon restarted" by swapping the server under the
// store). The returned rig answers on a new socket; the caller defers its
// stop (the old rig's own deferred stop already ran through stopOnce).
func restartRig(t *testing.T, r *testRig) *testRig {
	t.Helper()
	r.stopOnce(t)
	mgr := worktree.NewManager(r.root)
	if err := mgr.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	st, err := store.Open(filepath.Join(mgr.StateDir(), "journal.sqlite"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	omp := adapter.NewOMP(mgr.StateDir())
	srv := NewServer(st, r.root, omp, mgr)
	// Same dark-launch as startRig (auto + liveness stay deterministic).
	srv.autoDisabled = true
	srv.livenessDisabled.Store(true)
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
	return &testRig{root: r.root, sock: sock, store: st, server: srv, adapter: omp, listen: l}
}

// stopOnce is the single-flight teardown for drills that stop the
// daemon mid-test (restart window) AND defer a teardown for fatal
// aborts: both call sites race the same rig, so the Once makes the
// second call a no-op instead of double-closing the store.
func (r *testRig) stopOnce(t *testing.T) {
	t.Helper()
	r.stopflight.Do(func() { r.stop(t) })
}

func (r *testRig) stop(t *testing.T) {
	t.Helper()
	// C11: stop the liveness drain FIRST — liveness_test.go opt-ins leave
	// a live tick, and a tick journals under s.mu; it must not outlive the
	// store close below (idempotent with the Wait path).
	r.server.stopLiveness()
	// M12+P1: close the auto-distill subsystem (stop pending timers, bar
	// re-arms) and JOIN already-fired distills before closing the store —
	// the M12 disarm alone could not reach a timer that had already
	// fired, and its distillCore (journal/wiki/git writes) outlived
	// TempDir cleanup. This bypasses Wait() because rigs never stop
	// Serve — the two teardowns share stopAutoDistill as the one
	// subsystem-close path.
	r.server.stopAutoDistill()
	r.server.distillWG.Wait()
	// P1: join the boot-time stranded-diff recovery — it reads the store.
	r.server.recoverWG.Wait()
	// Mirror Wait's ordering: every drain-capable context is joined
	// above, so seal admissions AND drop still-registered runs' lifetime
	// pins before the land join — a late pipeline admission is refused,
	// an in-flight RUN never blocks teardown either.
	r.server.sealLandAndReleasePins()
	// P1 (#63 verify-flake class): join every spawned auto-land pipeline
	// — the recover fan-out's accept tails write journal and worktree git
	// state past the status flip a test polls for; they must complete
	// before the store close and TempDir cleanup below. In-flight
	// pipelines are joined, never cancelled.
	r.server.landWG.Wait()
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
	// Single-flight teardown: the mid-test restart below stops this rig
	// explicitly; a fatal abort before it must not leak the live server.
	defer rig.stopOnce(t)

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
	rig.stopOnce(t)
	rig = startRig(t, root)
	defer rig.stopOnce(t)

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
	// The run's transcript dir retired with it; the prompt capture stays
	// behind as the "what the agent was shown" audit record until the
	// startup sweeper ages it out.
	if _, err := os.Stat(filepath.Join(root, ".odo", "sessions", runID)); !os.IsNotExist(err) {
		t.Errorf("session dir for %s still on disk after no-diff run", runID)
	}
	if _, err := os.Stat(filepath.Join(root, ".odo", "prompts", runID+".txt")); err != nil {
		t.Errorf("prompt capture for %s removed at retire — it must survive until boot sweep: %v", runID, err)
	}
	if n := len(rig.server.runs); n != 0 {
		t.Errorf("server still tracks %d runs after no-diff completion", n)
	}
	if n := len(rig.server.byConv); n != 0 {
		t.Errorf("server still binds %d conversations after no-diff completion", n)
	}
}

// wikiStubWrapper mimics an agent that does real work (hello.txt) AND
// writes into daemon-owned memory — the diff carries a memory-path hunk.
const wikiStubWrapper = `#!/bin/sh
output_file="$3"
sleep 1
printf 'real work\n' > hello.txt
mkdir -p wiki
printf '# agent note\n' > wiki/agent-note.md
printf 'Did the work.\n' > "$output_file"
exit 0
`

const odoMemStubWrapper = `#!/bin/sh
output_file="$3"
sleep 1
printf 'real work\n' > hello.txt
mkdir -p .odo
printf 'agent memory\n' > .odo/memory.md
printf 'Did the work.\n' > "$output_file"
exit 0
`

// TestMemoryDiffRefusedAtRegistration pins the 2026-08-24 fail-fast: a run
// whose diff touches daemon-owned memory (.odo/, wiki/) is refused at
// REGISTRATION — no diff row is ever inserted (pre-panel's protected_path
// block and the executor's every-actor refusal made any such row a
// permanent pending wedge), the run retires like a no-diff outcome, the
// transcript gets an advisory naming the path and the correct route
// (distill/wiki-commit), and the extracted .diff stays in .odo/diffs/ as
// the salvage record. The accept-time rejectMemoryPaths stays the
// backstop (m6 guard tests cover it).
func TestMemoryDiffRefusedAtRegistration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		script   string
		wantPath string
	}{
		{"wiki hunk", wikiStubWrapper, "wiki/agent-note.md"},
		{"odo hunk", odoMemStubWrapper, ".odo/memory.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := initRepo(t)
			t.Setenv("ODO_OMP_WRAPPER", writeStub(t, tc.script))
			rig := startRig(t, root)
			defer rig.stop(t)

			boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
			convID := boot.Conversation.ID
			rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "do work + remember"})
			runID := rig.server.byConv[convID]
			if runID == "" {
				t.Fatal("run did not register in byConv")
			}
			wtPath := rig.server.runs[runID].worktreePath

			done := rig.pollUntilDone(t, convID)
			if done.Diff != nil || len(done.Diffs) != 0 {
				t.Errorf("refused run surfaced a diff: %+v / %+v", done.Diff, done.Diffs)
			}
			// The review channel never holds unlandable bytes.
			if diffs, err := rig.store.ListDiffs(context.Background(), convID); err != nil || len(diffs) != 0 {
				t.Errorf("ListDiffs = %d rows (err %v), want none", len(diffs), err)
			}
			// Transcript advisory: names the protected path + the distill route.
			events, err := rig.store.ListEvents(context.Background(), convID, 0)
			if err != nil {
				t.Fatal(err)
			}
			advisory := ""
			for _, ev := range events {
				if ev.Type == store.EventAgentError && strings.Contains(string(ev.Payload), `"odo":true`) {
					advisory = string(ev.Payload)
				}
			}
			for _, sub := range []string{"NOT registered", tc.wantPath, "distill/wiki-commit"} {
				if !strings.Contains(advisory, sub) {
					t.Errorf("advisory %q missing %q", advisory, sub)
				}
			}
			// Salvage: exactly one patch stays archived in .odo/diffs/ (the
			// daemon names it by runDirID, not the byConv run id), carrying
			// both the real work and the refused memory hunk.
			matches, err := filepath.Glob(filepath.Join(root, ".odo", "diffs", "*.diff"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("salvage patches = %v (err %v), want exactly 1", matches, err)
			}
			data, err := os.ReadFile(matches[0])
			if err != nil {
				t.Fatalf("read salvage patch: %v", err)
			}
			for _, sub := range []string{"hello.txt", tc.wantPath} {
				if !strings.Contains(string(data), sub) {
					t.Errorf("salvage patch missing hunk for %q", sub)
				}
			}
			// Retired immediately like a no-diff run.
			if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
				t.Errorf("worktree %s still on disk after refusal", wtPath)
			}
			if n := len(rig.server.runs); n != 0 {
				t.Errorf("server still tracks %d runs after refusal", n)
			}
		})
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

// TestReviewOfOlderDiffRetiresItsOwnRun pins retireRun's target selection
// (tri-review P1, 2026-08-24): with TWO finished runs pending review on one
// conversation, reviewing the OLDER diff must close that diff's own run and
// remove ITS worktree — never the newer run byConv happens to bind. The old
// byConv-first selection closed the newer run, deleted ITS worktree (mid
// auto-land verify, when that run's own diff was still in the pipeline),
// and unbound the conversation while the reviewed diff's worktree was
// orphaned.
func TestReviewOfOlderDiffRetiresItsOwnRun(t *testing.T) {
	root := initRepo(t)
	// HOME isolation: readUserMemory injects the real ~/.odo/user.md into
	// the prompt the stub copies into hello.txt.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Run 1 completes → diff A pending; run 1 finished but still in the maps.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "run one"})
	done1 := rig.pollUntilDone(t, convID)
	if done1.Diff == nil {
		t.Fatal("run 1: no diff")
	}
	runID1 := rig.server.byConv[convID]
	wt1 := rig.server.runs[runID1].worktreePath

	// Run 2 completes → diff B pending; byConv now binds run 2.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "run two"})
	done2 := rig.pollUntilDone(t, convID)
	if done2.Diff == nil {
		t.Fatal("run 2: no diff")
	}
	runID2 := rig.server.byConv[convID]
	if runID2 == "" || runID2 == runID1 {
		t.Fatalf("run 2 binding = %q, want a fresh run id distinct from %q", runID2, runID1)
	}
	wt2 := rig.server.runs[runID2].worktreePath

	// Review the OLDER diff: it lands, its own run and worktree retire.
	acc := rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: done1.Diff.ID})
	if !acc.Applied {
		t.Fatalf("accept run-1 diff: %+v", acc)
	}
	if got := readFileStr(t, filepath.Join(root, "hello.txt")); got != "run one" {
		t.Errorf("hello.txt = %q, want run one's accepted content", got)
	}
	if meta1 := rig.server.runs[runID1]; meta1 != nil {
		t.Errorf("reviewed run still tracked: %+v", meta1)
	}
	if _, err := os.Stat(wt1); err == nil {
		t.Error("reviewed diff's own worktree survived its review")
	}

	// The newer run survives untouched: still bound, still tracked, ITS
	// worktree still on disk. Under byConv-first selection all three died.
	if got := rig.server.byConv[convID]; got != runID2 {
		t.Errorf("byConv after review = %q, want newer run %q still bound", got, runID2)
	}
	if meta2 := rig.server.runs[runID2]; meta2 == nil {
		t.Error("newer run closed by the older diff's review")
	}
	if _, err := os.Stat(wt2); err != nil {
		t.Errorf("newer run's worktree removed by the older diff's review: %v", err)
	}
}

// TestRejectArchivesWorktreeRescue pins the #47 incident (epoch-35; #49
// fix): reviewing a diff retires its worktree, destroying bytes newer
// than the archived patch — a reject left the fix's only copy in a stale
// backup. The resolution must archive the divergent delta to
// .odo/diffs/<run>-rescue.diff first and receipt it on the reject row,
// while the judged patch file stays untouched (patch_sha16 lineage).
func TestRejectArchivesWorktreeRescue(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir()) // memory-layer reads must not see the real HOME
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	ctx := context.Background()
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "run one"})
	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}
	d, err := rig.store.GetDiff(ctx, done.Diff.ID)
	if err != nil {
		t.Fatal(err)
	}
	runID := rig.server.byConv[convID]
	wt := rig.server.runs[runID].worktreePath
	// Post-drain bytes living ONLY in the worktree — the incident shape:
	// edits made after the drain snapshot archived the patch.
	if err := os.WriteFile(filepath.Join(wt, "fix.txt"), []byte("post-drain line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rej := rig.call(t, Request{Cmd: CmdRejectDiff, DiffID: done.Diff.ID})
	if rej.Applied {
		t.Error("reject_diff: applied must be false")
	}

	// The rescue archived the post-drain bytes; the worktree still retired.
	rescuePath := filepath.Join(root, ".odo", "diffs",
		strings.TrimSuffix(filepath.Base(d.PathOnDisk), ".diff")+"-rescue.diff")
	data, err := os.ReadFile(rescuePath)
	if err != nil {
		t.Fatalf("rescue archive: %v", err)
	}
	if !strings.Contains(string(data), "fix.txt") || !strings.Contains(string(data), "post-drain line") {
		t.Errorf("rescue archive must carry the post-drain bytes:\n%s", data)
	}
	// The judged patch is NEVER mutated — the rescue is a sibling.
	if archived, err := os.ReadFile(d.PathOnDisk); err != nil || strings.Contains(string(archived), "post-drain line") {
		t.Errorf("judged patch mutated (err=%v) — patch_sha16 lineage broken", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree must still retire on reject, stat err = %v", err)
	}
	// The reject row carries the receipt.
	events, err := rig.store.ListEvents(ctx, convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	rejectRow := ""
	for _, e := range events {
		if e.Type == store.EventReviewAction && strings.Contains(string(e.Payload), `"action":"reject"`) {
			rejectRow = string(e.Payload)
		}
	}
	if rejectRow == "" {
		t.Fatal("no reject row journaled")
	}
	if !strings.Contains(rejectRow, `"rescue":"archived"`) || !strings.Contains(rejectRow, "-rescue.diff") {
		t.Errorf("reject row lacks the rescue receipt: %s", rejectRow)
	}
}

// TestRejectMatchesArchivedSkipsRescue pins the common case: an untouched
// worktree re-snapshots byte-identical to the judged patch, so reject
// writes NO duplicate file and receipts matches_archived.
func TestRejectMatchesArchivedSkipsRescue(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	ctx := context.Background()
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "run one"})
	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}
	d, err := rig.store.GetDiff(ctx, done.Diff.ID)
	if err != nil {
		t.Fatal(err)
	}

	rej := rig.call(t, Request{Cmd: CmdRejectDiff, DiffID: done.Diff.ID})
	if rej.Applied {
		t.Error("reject_diff: applied must be false")
	}
	rescuePath := filepath.Join(root, ".odo", "diffs",
		strings.TrimSuffix(filepath.Base(d.PathOnDisk), ".diff")+"-rescue.diff")
	if _, err := os.Stat(rescuePath); !os.IsNotExist(err) {
		t.Errorf("identical delta must not duplicate (stat err = %v)", err)
	}
	events, err := rig.store.ListEvents(ctx, convID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == store.EventReviewAction && strings.Contains(string(e.Payload), `"action":"reject"`) {
			found = strings.Contains(string(e.Payload), `"rescue":"matches_archived"`)
		}
	}
	if !found {
		t.Error("reject row must receipt matches_archived")
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
	d, err := f.st.InsertDiff(context.Background(), f.c.ID, path, gitOut(t, root, "rev-parse", "HEAD"), "", "")
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

// TestAcceptRefusesDirtyPatchPaths pins the tri-review P0 pre-apply refusal
// (fresh base): the main checkout carries uncommitted user work on the
// patch's OWN paths. Before the guard, a failed --3way triggered the I7
// rollback — reset+checkout restoring HEAD bytes over edits the tool never
// touched (the failed apply itself wrote NOTHING) — and a clean merge
// swept the edits into the accept commit. The accept now refuses, names
// the dirty paths, writes nothing, journals nothing, and stays pending;
// committing the user's work unblocks the retry. Dirt on OTHER paths never
// blocks (TestAcceptDoesNotSweepMainCheckout).
func TestAcceptRefusesDirtyPatchPaths(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // agent edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))

	// stagedWording: the staged shape diverts to the pre-adjudication
	// staged-edit refusal (P1, 2026-08-24 — IndexEditsBeyondHEAD names any
	// index entry diverging from HEAD before the fresh path's porcelain
	// dirty check runs); the unstaged shape keeps the dirty refusal's
	// "uncommitted changes" wording. Both stay byte-intact, pending, and
	// side-effect free — the shared assertions below cover that.
	refuse := func(stage bool) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // user work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		wording := "uncommitted changes"
		if stage {
			gitIn(t, root, "add", "src/a.go")
			wording = "staged changes"
		}
		_, err := s.handleDiffAction(context.Background(), d.ID, "accept", "", "")
		if err == nil {
			t.Fatalf("stage=%v: accept over dirty patch paths: want refusal", stage)
		}
		for _, want := range []string{wording, "src/a.go"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("stage=%v: err = %q, want it to name %q", stage, err.Error(), want)
			}
		}
		if errors.Is(err, errBaseStale) {
			t.Errorf("stage=%v: err wraps errBaseStale — user dirt must never fire the auto-revise round", stage)
		}
		// Nothing was attempted: user bytes intact, status pending, no rows.
		if got := readFileStr(t, filepath.Join(root, "src", "a.go")); got != "package src // user work\n" {
			t.Errorf("stage=%v: src/a.go = %q, want the user's work intact", stage, got)
		}
		if got, gerr := f.st.GetDiff(context.Background(), d.ID); gerr != nil {
			t.Fatal(gerr)
		} else if got.Status != store.DiffPending {
			t.Errorf("stage=%v: diff status = %q, want pending", stage, got.Status)
		}
		if rows := reviewActionRowsFor(t, f, d); len(rows) != 0 {
			t.Errorf("stage=%v: journal rows = %v, want none (a refusal is not an outcome)", stage, rows)
		}
		gitIn(t, root, "reset", "-q", "HEAD", "--", "src/a.go")
		gitIn(t, root, "checkout", "--", "src/a.go")
	}
	refuse(false) // unstaged edit on the patch path
	refuse(true)  // staged edit on the patch path

	// Retryable by construction: with the path clean the same accept lands.
	if _, err := s.handleDiffAction(context.Background(), d.ID, "accept", "", ""); err != nil {
		t.Fatalf("accept on clean patch paths: %v", err)
	}
	if got := readFileStr(t, filepath.Join(root, "src", "a.go")); got != "package src // agent edit\n" {
		t.Errorf("src/a.go = %q, want the accepted agent edit", got)
	}
	if got, gerr := f.st.GetDiff(context.Background(), d.ID); gerr != nil {
		t.Fatal(gerr)
	} else if got.Status != store.DiffAccepted {
		t.Errorf("diff status = %q, want accepted", got.Status)
	}
}

// TestAcceptRefreshRefusesDirtyPatchPaths pins the same refusal on the
// STALE-base refresh path: HEAD moved on a disjoint path, and the patch's
// own path carries uncommitted user work. The refresh is refused BEFORE
// any apply — journaled as refresh_attempted{dirty_refusal} (the trail
// must say why a stale base did not refresh), the error does NOT wrap
// errBaseStale (an auto-revise would regenerate a patch that hits the
// same refusal), and the user's bytes survive byte-identical.
func TestAcceptRefreshRefusesDirtyPatchPaths(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // agent edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	driftMain(t, root, "drift.go") // HEAD moves off the base on a disjoint path
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // user work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := s.handleDiffAction(context.Background(), d.ID, "accept", "", "")
	if err == nil {
		t.Fatal("refresh over dirty patch paths: want refusal")
	}
	for _, want := range []string{"uncommitted changes", "src/a.go"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err.Error(), want)
		}
	}
	if errors.Is(err, errBaseStale) {
		t.Error("err wraps errBaseStale — auto-land would auto-revise into the same refusal")
	}
	if got := readFileStr(t, filepath.Join(root, "src", "a.go")); got != "package src // user work\n" {
		t.Errorf("src/a.go = %q, want the user's work intact", got)
	}
	if got, gerr := f.st.GetDiff(context.Background(), d.ID); gerr != nil {
		t.Fatal(gerr)
	} else if got.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending", got.Status)
	}
	rows := reviewActionRowsFor(t, f, d)
	if len(rows) != 1 || rows[0]["action"] != "refresh_attempted" || rows[0]["outcome"] != "dirty_refusal" {
		t.Fatalf("journal rows = %v, want exactly refresh_attempted{dirty_refusal}", rows)
	}
}

// TestAcceptRefusesStagedPatchPaths pins the tri-review P1 pre-adjudication
// refusal (2026-08-24): the patch's own path carries a STAGED index edit
// diverging from HEAD while the worktree happens to hold the post-image —
// the shape every worktree-level guard misses (the extra-edits check
// compares bytes; the porcelain dirty check never runs on the
// already-landed branch it would land through). The accept's stage+commit
// pair would then rewrite the index entry wholesale, losing the staged
// blob. The accept now refuses BEFORE base adjudication, on every accept
// branch: the error names the path and the unstage remedy, the diff stays
// pending, nothing is journaled, and the staged entry plus worktree bytes
// survive byte-exact. Unstaging unblocks the retry — even with an
// unrelated path left staged, which the gate ignores by construction.
func TestAcceptRefusesStagedPatchPaths(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // agent edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	// The finding's exact shape: the patch path holds the post-image in
	// the worktree while the REAL index entry carries a different staged
	// blob (the user staged a sketch, then reverted the file on disk
	// without unstaging).
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // user staged sketch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "src/a.go")
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // agent edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexBefore := gitOut(t, root, "ls-files", "-s", "--", "src/a.go")

	_, err := s.handleDiffAction(context.Background(), d.ID, "accept", "", "")
	if err == nil {
		t.Fatal("accept over staged patch paths: want refusal, got nil")
	}
	for _, want := range []string{"staged changes", "src/a.go", "unstage"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err.Error(), want)
		}
	}
	if errors.Is(err, errBaseStale) {
		t.Error("err wraps errBaseStale — staged user content must never fire an auto-revise round")
	}
	if got, gerr := f.st.GetDiff(context.Background(), d.ID); gerr != nil {
		t.Fatal(gerr)
	} else if got.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending", got.Status)
	}
	if rows := reviewActionRowsFor(t, f, d); len(rows) != 0 {
		t.Errorf("journal rows = %v, want none (a refusal is not an outcome)", rows)
	}
	// Side-effect free: the staged index entry survives verbatim (the
	// ls-files fingerprint is unchanged), and the worktree keeps the
	// post-image bytes the user left there.
	if got := gitOut(t, root, "ls-files", "-s", "--", "src/a.go"); got != indexBefore {
		t.Errorf("index entry = %q, want untouched %q (the staged sketch survived)", got, indexBefore)
	}
	if got := readFileStr(t, filepath.Join(root, "src", "a.go")); got != "package src // agent edit\n" {
		t.Errorf("src/a.go = %q, want the worktree bytes exactly as seeded", got)
	}

	// Retryable by construction: unstaging the sketch unblocks the same
	// accept, and a staged edit on an UNRELATED path (README.md) neither
	// trips the gate nor gets swept — the refusal axes are the patch's
	// own paths, exactly like DirtyPaths'.
	gitIn(t, root, "reset", "-q", "HEAD", "--", "src/a.go")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "README.md")
	if _, err := s.handleDiffAction(context.Background(), d.ID, "accept", "", ""); err != nil {
		t.Fatalf("accept with a clean patch-path index (staged unrelated path): %v", err)
	}
	if got, gerr := f.st.GetDiff(context.Background(), d.ID); gerr != nil {
		t.Fatal(gerr)
	} else if got.Status != store.DiffAccepted {
		t.Errorf("diff status = %q, want accepted", got.Status)
	}
	// The retry rode the already-landed branch the finding targets: the
	// post-image was already sitting in the worktree uncommitted.
	row := resolutionRow(t, f, d, "accept")
	if row["already_landed"] != true {
		t.Errorf("accept row already_landed = %v, want true", row["already_landed"])
	}
	if got := gitOut(t, root, "show", "--format=", "--name-only", "HEAD"); got != "src/a.go" {
		t.Errorf("accept commit files = %q, want exactly src/a.go", got)
	}
	if status := gitStatus(t, root); !strings.Contains(status, "M  README.md") {
		t.Errorf("status = %q, want README.md still staged and untouched by the accept", status)
	}
}

// TestAcceptStaleBaseRefreshConflict (P0a; supersedes fix-INT's
// TestAcceptBlocksStaleBase): a stale base no longer hard-refuses — the
// accept path attempts a --3way REBASE under acceptMu. When main and the
// diff edited the SAME file, the merge conflicts: the attempt journals
// refresh_attempted{outcome:"conflict"} naming BOTH shas, rolls main back
// to the pre-attempt tree, and the diff stays pending (NOT conflict —
// DiffConflict is reserved for fresh-base apply failures). The returned
// error wraps errBaseStale and names both shas plus the refresh outcome.
func TestAcceptStaleBaseRefreshConflict(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package src // agent edit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	oldBase := *d.BaseSHA
	// Drift main by editing the SAME line the patch rewrites.
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package src // user drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", "src/a.go")
	gitIn(t, root, "commit", "-m", "user drift")
	head := gitOut(t, root, "rev-parse", "HEAD")

	_, err := s.handleDiffAction(context.Background(), d.ID, "accept", "", "")
	if err == nil {
		t.Fatal("accept on a stale, conflicting base: want error")
	}
	if !errors.Is(err, errBaseStale) {
		t.Errorf("err = %v, want errors.Is(err, errBaseStale)", err)
	}
	for _, want := range []string{oldBase, head, "conflict"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending (refresh failures never mark conflict)", got.Status)
	}
	// The refresh attempt is the ONLY journaled row for the diff, and it
	// records the conflict with both shas and git's diagnostics.
	rows := reviewActionRowsFor(t, f, d)
	if len(rows) != 1 || rows[0]["action"] != "refresh_attempted" {
		t.Fatalf("journal rows = %v, want exactly one refresh_attempted row", rows)
	}
	r := rows[0]
	if r["outcome"] != "conflict" || r["phase"] != "accept_apply" {
		t.Errorf("refresh row = %v, want outcome=conflict phase=accept_apply", r)
	}
	if r["base_sha"] != oldBase || r["target_sha"] != head {
		t.Errorf("refresh shas = %v→%v, want %s→%s", r["base_sha"], r["target_sha"], oldBase, head)
	}
	if detail, _ := r["detail"].(string); detail == "" {
		t.Error("refresh row has no detail — the conflict's git diagnostics must ride the journal")
	}
	// Main rolled back: the drifted content is intact, the index carries no
	// unmerged entries, and the checkout is clean.
	if got := readFileStr(t, filepath.Join(root, "src", "a.go")); got != "package src // user drift\n" {
		t.Errorf("src/a.go = %q, want the drifted content (rollback must restore main)", got)
	}
	if unmerged := gitOut(t, root, "ls-files", "-u"); unmerged != "" {
		t.Errorf("unmerged index entries after rollback:\n%s", unmerged)
	}
	if status := gitOut(t, root, "status", "--porcelain"); status != "" {
		t.Errorf("main status = %q after rollback, want clean", status)
	}
}

// TestAcceptStaleBaseRefreshClean (P0a): main drifted on a path the diff
// doesn't touch — the accept path's --3way rebase merges cleanly, the
// accept succeeds in one action, and the journal tells the whole story:
// refresh_attempted{clean} FIRST, then the accept row carrying
// refreshed_from_sha (the diff's ORIGINAL base) with base_sha/head_sha on
// the refreshed base. The store row's base moves to the landed-upon HEAD,
// and both the drift commit's and the diff's content are in main.
func TestAcceptStaleBaseRefreshClean(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "landed.go"), []byte("package src // landed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	oldBase := *d.BaseSHA
	head := driftMain(t, root, "src/drift.go") // disjoint new-file drift

	resp, err := s.handleDiffAction(context.Background(), d.ID, "accept", "", "")
	if err != nil {
		t.Fatalf("accept on a stale but disjoint base: %v", err)
	}
	if !resp.Applied {
		t.Error("resp.Applied = false, want true (the refresh applied the diff)")
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffAccepted {
		t.Errorf("diff status = %q, want accepted", got.Status)
	}
	if got.BaseSHA == nil || *got.BaseSHA != head {
		t.Errorf("store base_sha = %v, want the refreshed HEAD %s", got.BaseSHA, head)
	}
	if data, rerr := os.ReadFile(filepath.Join(root, "src", "landed.go")); rerr != nil || !strings.Contains(string(data), "landed") {
		t.Errorf("landed.go = %q, %v — the refreshed accept must apply to main", data, rerr)
	}
	if _, serr := os.Stat(filepath.Join(root, "src", "drift.go")); serr != nil {
		t.Errorf("drift.go missing: %v — the refresh must not roll back the drift", serr)
	}
	// Journal-first (hard rule 6): the refresh row precedes the accept.
	rows := reviewActionRowsFor(t, f, d)
	if len(rows) != 2 || rows[0]["action"] != "refresh_attempted" || rows[1]["action"] != "accept" {
		t.Fatalf("journal rows = %v, want [refresh_attempted, accept] in that order", rows)
	}
	r := rows[0]
	if r["outcome"] != "clean" || r["phase"] != "accept_apply" {
		t.Errorf("refresh row = %v, want outcome=clean phase=accept_apply", r)
	}
	if r["base_sha"] != oldBase || r["target_sha"] != head {
		t.Errorf("refresh shas = %v→%v, want %s→%s", r["base_sha"], r["target_sha"], oldBase, head)
	}
	if _, hasDetail := r["detail"]; hasDetail {
		t.Errorf("clean refresh row carries detail = %v — detail is conflict/error only", r["detail"])
	}
	a := rows[1]
	if a["refreshed_from_sha"] != oldBase {
		t.Errorf("accept refreshed_from_sha = %v, want the original base %s", a["refreshed_from_sha"], oldBase)
	}
	if a["base_sha"] != head || a["head_sha"] != head {
		t.Errorf("accept base/head = %v/%v, want the refreshed HEAD %s for both", a["base_sha"], a["head_sha"], head)
	}
}

// TestAcceptFreshBaseNoRefresh (P0a): a fresh base (stored base_sha ==
// main HEAD) takes the normal accept path — no refresh attempt, no
// refresh_attempted row, and no refreshed_from_sha key on the accept row.
func TestAcceptFreshBaseNoRefresh(t *testing.T) {
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "src", "landed.go"), []byte("package src // landed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))

	if _, err := s.handleDiffAction(context.Background(), d.ID, "accept", "", ""); err != nil {
		t.Fatalf("accept on a fresh base: %v", err)
	}
	rows := reviewActionRowsFor(t, f, d)
	if len(rows) != 1 || rows[0]["action"] != "accept" {
		t.Fatalf("journal rows = %v, want exactly one accept row (a fresh base never refreshes)", rows)
	}
	if _, has := rows[0]["refreshed_from_sha"]; has {
		t.Errorf("fresh accept carries refreshed_from_sha = %v — no refresh happened", rows[0]["refreshed_from_sha"])
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

	resp, err := s.handleDiffAction(context.Background(), d.ID, "accept", "", "")
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

	resp, err := s.handleDiffAction(context.Background(), d.ID, "accept", "", "")
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

	if _, err := s.handleDiffAction(context.Background(), d.ID, "reject", "", ""); err != nil {
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

// TestStackedPendingDiffsSharedBaseSecondRefreshes (P0a; supersedes
// fix-INT's *SecondBlocks): two pending diffs cut from the SAME base
// (queued parallel runs). Accepting #1 moves main HEAD, so #2's stored
// base is instantly stale — exactly the window the final gate covers —
// but the diffs are disjoint, so #2's accept REBASES it onto the new HEAD
// and lands instead of refusing (the old posture made N parallel diffs on
// one base → N−1 unlandable; this is the contract P0a exists to change).
func TestStackedPendingDiffsSharedBaseSecondRefreshes(t *testing.T) {
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

	if _, err := s.handleDiffAction(context.Background(), d1.ID, "accept", "", ""); err != nil {
		t.Fatalf("accept #1 on the shared base: %v", err)
	}
	head := gitOut(t, root, "rev-parse", "HEAD")
	if head == base {
		t.Fatal("accept #1 must move HEAD (the path-scoped accept commit)")
	}

	resp, err := s.handleDiffAction(context.Background(), d2.ID, "accept", "", "")
	if err != nil {
		t.Fatalf("accept #2 on the now-stale shared base: %v — disjoint diffs must refresh, not refuse", err)
	}
	if !resp.Applied {
		t.Error("accept #2 resp.Applied = false, want true")
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
	if got2.Status != store.DiffAccepted {
		t.Errorf("#2 status = %q, want accepted (the refresh landed it)", got2.Status)
	}
	if got2.BaseSHA == nil || *got2.BaseSHA != head {
		t.Errorf("#2 base_sha = %v, want refreshed to #1's accept commit %s", got2.BaseSHA, head)
	}
	// Both diffs' content is in main, and #2's row set records the refresh.
	if p := resolutionRow(t, f, d1, "accept"); p["head_sha"] != base {
		t.Errorf("#1 head_sha = %v, want the shared base %s", p["head_sha"], base)
	}
	rows := reviewActionRowsFor(t, f, d2)
	if len(rows) != 2 || rows[0]["action"] != "refresh_attempted" || rows[1]["action"] != "accept" {
		t.Fatalf("#2 journal rows = %v, want [refresh_attempted, accept]", rows)
	}
	if rows[0]["outcome"] != "clean" || rows[0]["base_sha"] != base || rows[0]["target_sha"] != head {
		t.Errorf("#2 refresh row = %v, want {clean, %s→%s}", rows[0], base, head)
	}
	if rows[1]["refreshed_from_sha"] != base {
		t.Errorf("#2 accept refreshed_from_sha = %v, want %s", rows[1]["refreshed_from_sha"], base)
	}
	for _, name := range []string{"one.go", "two.go"} {
		if _, serr := os.Stat(filepath.Join(root, "src", name)); serr != nil {
			t.Errorf("src/%s missing after both refreshed accepts: %v", name, serr)
		}
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

// TestBootstrapAfterSeqHint covers the repeat-switch tail replay: a
// bootstrap carrying the GUI's switch-cache hint (conversation_id +
// after_seq) replays only events beyond the sequence high-water; a stale
// hint naming another conversation falls back to the full replay so a
// client can never silently lose the middle of a journal.
func TestBootstrapAfterSeqHint(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	ctx := context.Background()

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	for i := range 5 {
		if _, err := rig.store.AppendEvent(ctx, convID, store.EventUserMessage,
			mustJSON(map[string]interface{}{"text": fmt.Sprintf("m%d", i)})); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	full := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if len(full.Events) != 5 {
		t.Fatalf("baseline replay: %d events, want 5", len(full.Events))
	}

	// The warm-cache contract: replay only the tail beyond after_seq.
	cut := full.Events[2].Seq
	tail := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, ConversationID: convID, AfterSeq: cut})
	if len(tail.Events) != 2 {
		t.Fatalf("hinted bootstrap: %d events, want the 2-row tail", len(tail.Events))
	}
	for _, ev := range tail.Events {
		if ev.Seq <= cut {
			t.Errorf("hinted bootstrap replayed seq %d ≤ after_seq %d", ev.Seq, cut)
		}
	}
	if tail.Conversation == nil || tail.Conversation.ID != convID {
		t.Errorf("hinted bootstrap: conversation = %+v, want id %d", tail.Conversation, convID)
	}

	// A hint naming another conversation (stale cache after an epoch fold
	// replaced the active one) must NOT tail-trim: the merged journal
	// would silently elide every row ≤ after_seq.
	stale := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, ConversationID: convID + 999, AfterSeq: cut})
	if len(stale.Events) != 5 {
		t.Errorf("stale-conversation hint: %d events, want full replay of 5", len(stale.Events))
	}

	// after_seq 0 is the cold-cache contract: also a full replay.
	cold := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, ConversationID: convID, AfterSeq: 0})
	if len(cold.Events) != 5 {
		t.Errorf("after_seq 0: %d events, want full replay of 5", len(cold.Events))
	}
}

// TestSteering covers steer=true messages: journaled as user_message rows
// only while a run is ACTIVE, queued there for the continuation run
// (A2-lite). A conversation with no live run refuses fail-closed —
// journal-only steers orphaned in the journal with nothing to ever close
// the ledger on them (Hermes steer queue: every journaled steer ends
// consumed or explicitly dropped).
func TestSteering(t *testing.T) {
	t.Run("active run queues the steer text", func(t *testing.T) {
		root := initRepo(t)
		// W2: journalRuleSnapshots fires on the first send whenever a rule
		// file exists — without HOME isolation the real ~/.odo/user.md
		// inserts a snapshot row and shifts every later seq. Isolate HOME
		// (the file's norm) so the seq arithmetic below is hermetic.
		t.Setenv("HOME", t.TempDir())
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

		// The queue entry carries its journal identity: seq 2 (the steer
		// row just journaled) plus the verbatim text.
		rig.server.mu.Lock()
		meta := rig.server.runs[rig.server.byConv[convID]]
		rig.server.mu.Unlock()
		if meta == nil || len(meta.queuedSteers) != 1 ||
			meta.queuedSteers[0].seq != 2 || meta.queuedSteers[0].text != "Also add a second line." {
			t.Fatalf("queuedSteers = %+v, want one entry {seq 2, the steer text}", meta)
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

	t.Run("no active run refuses cleanly", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		// Fail-closed: with no run there is no queue to receive the steer
		// — refuse pre-journal rather than orphan the row.
		resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "queued", Steer: true})
		if !strings.Contains(resp.Error, "steer: no active run for conversation") {
			t.Errorf("steer error = %q, want the fail-closed refusal", resp.Error)
		}
		if got := rig.allEventTypes(t, convID); len(got) != 0 {
			t.Errorf("events = %v, want NOTHING journaled for a refused orphan steer", got)
		}
	})

	t.Run("finished run refuses cleanly", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
		rig.pollUntilDone(t, convID)

		// The meta survives completion; a steer landing on it would append
		// to a dead run's queue and be lost — refuse like the no-run case.
		resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "too late", Steer: true})
		if !strings.Contains(resp.Error, "steer: no active run for conversation") {
			t.Errorf("steer error = %q, want the fail-closed refusal", resp.Error)
		}
		// The journal stayed at the first run's shape: no steer row.
		users := 0
		for _, ev := range mustListEvents(t, rig.store, convID) {
			if ev.Type == store.EventUserMessage {
				users++
			}
		}
		if users != 1 {
			t.Errorf("user_message count = %d, want 1 (the refused steer journaled nothing)", users)
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
		// HOME isolation: a non-empty real ~/.odo/user.md now earns a
		// snapshot memory_update row on the send below, breaking the
		// byte-exact journal assertion (TestDistill precedent).
		t.Setenv("HOME", t.TempDir())
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
		"[user_message agent_text agent_done memory_update review_action memory_update]"; got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	// The fold marker and the wiki commit row ride above learner/failed.
	var muPayload map[string]interface{}
	if err := json.Unmarshal(events[len(events)-3].Payload, &muPayload); err != nil {
		t.Fatalf("memory_update payload: %v", err)
	}
	if muPayload["layer"] != "learner" || muPayload["cause"] != "failed" {
		t.Errorf("memory_update payload = %v, want learner/failed", muPayload)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(events[len(events)-2].Payload, &payload); err != nil {
		t.Fatalf("review_action payload: %v", err)
	}
	if payload["action"] != "distill" || payload["epoch"] != float64(2) || payload["wiki_path"] != wantPath {
		t.Errorf("review_action payload = %v", payload)
	}
	// The pipeline commits its own wiki output and journals the commit as
	// additive telemetry above the marker (layer wiki / cause commit).
	var wikiMu map[string]interface{}
	if err := json.Unmarshal(events[len(events)-1].Payload, &wikiMu); err != nil {
		t.Fatalf("wiki commit payload: %v", err)
	}
	if wikiMu["layer"] != "wiki" || wikiMu["cause"] != "commit" {
		t.Errorf("trailing row = %v, want the wiki commit telemetry", wikiMu)
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

// distillMoaCall is one moa request the stub captured: the model, and the
// user content of the last message (the distill prompt, re-issued verbatim
// on a budget escalation).
type distillMoaCall struct {
	model  string
	maxTok int
	prompt string
}

// startDistillMoaStub installs a moa-API stub that records every request
// and answers: the first call with stop_reason=max_tokens (driving one
// budget-escalation re-issue), later calls per truncateNote — end_turn
// with noteText, or max_tokens again (the still-truncated failure). Wire
// shape matches messageResponse (content blocks + stop_reason + usage).
func startDistillMoaStub(t *testing.T, noteText string, truncateNote bool) (*httptest.Server, func() []distillMoaCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []distillMoaCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			MaxTok   int    `json:"max_tokens"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var prompt string
		if n := len(req.Messages); n > 0 {
			prompt = req.Messages[n-1].Content
		}
		mu.Lock()
		calls = append(calls, distillMoaCall{model: req.Model, maxTok: req.MaxTok, prompt: prompt})
		first := len(calls) == 1
		mu.Unlock()
		stop := "end_turn"
		outTok := 555
		if first || truncateNote {
			stop = "max_tokens"
			outTok = 777
		}
		text := noteText
		if first {
			// The truncated attempt's text is discarded by the escalation.
			text = ""
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": stop,
			"usage":       map[string]int{"output_tokens": outTok},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []distillMoaCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]distillMoaCall(nil), calls...)
	}
}

// passMoaCall records one moa request as the stub received it: the decoded
// fields plus the RAW body, so a test can recompute the journaled
// request_sha16/request_bytes receipt independently of the client (the
// wire-exact discipline of R-W1.5's TestRequestReceiptWireExact).
type passMoaCall struct {
	model  string
	maxTok int
	prompt string
	body   []byte
}

// startPassMoaStub installs a moa-API stub for the R-W3 learner/curate
// routes: every request is answered end_turn with answer unless truncate
// is set (max_tokens forever plus an unterminated-JSON body — the
// budget-escalation-then-still-truncated shape that must fail the pass
// closed).
func startPassMoaStub(t *testing.T, answer string, truncate bool) (*httptest.Server, func() []passMoaCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []passMoaCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model    string `json:"model"`
			MaxTok   int    `json:"max_tokens"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.Unmarshal(body, &req)
		var prompt string
		if n := len(req.Messages); n > 0 {
			prompt = req.Messages[n-1].Content
		}
		mu.Lock()
		calls = append(calls, passMoaCall{model: req.Model, maxTok: req.MaxTok, prompt: prompt, body: body})
		mu.Unlock()
		stop, text, outTok := "end_turn", answer, 321
		if truncate {
			stop, text, outTok = "max_tokens", `{"truncated":[`, 777
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": stop,
			"usage":       map[string]int{"output_tokens": outTok},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []passMoaCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]passMoaCall(nil), calls...)
	}
}

// TestDistillViaMoa (R-W2) covers the prefs `distill_via: moa` route: the
// fold prompt goes through one moa.Query — the exact wire request is
// capturable and its receipts (via/model/prompt_sha16/output budget,
// escalations) land additively on the distill marker — while absent,
// explicit-"omp", and unknown values keep the OMP route byte-identical. A
// truncated answer fails the fold closed: no note, no marker, one
// journaled failed row.
func TestDistillViaMoa(t *testing.T) {
	t.Run("moa route journals the exact-request receipts", func(t *testing.T) {
		root := initRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		noteText := "# Epoch 1\n\nCreated hello.txt as folded.\n\n## Open loops\nNone.\n"
		srv, calls := startDistillMoaStub(t, noteText, false)
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		writePrefs(t, home, "distill_via: moa\norchestrator: orch-m3k@test\n")
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
		rig.pollUntilDone(t, convID)

		d := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
		wantPath := filepath.Join(root, "wiki", "main-epoch-1.md")
		if d.WikiPath != wantPath || d.Epoch != 2 {
			t.Fatalf("distill = (path %q, epoch %d)", d.WikiPath, d.Epoch)
		}
		note, err := os.ReadFile(wantPath)
		if err != nil || string(note) != noteText {
			t.Errorf("note = %q, %v — want the stub's answer verbatim", note, err)
		}

		// Route: exactly 2 moa calls (one max_tokens → one escalation
		// re-issue at double the unknown-model fallback budget), serving
		// the prefs orchestrator model with the self-contained prompt.
		got := calls()
		if len(got) != 2 {
			t.Fatalf("moa calls = %d, want 2 (truncated + escalated)", len(got))
		}
		for i, c := range got {
			if c.model != "orch-m3k" {
				t.Errorf("call %d model = %q, want orch-m3k", i, c.model)
			}
			if !strings.Contains(c.prompt, "Summarize the key decisions") ||
				!strings.Contains(c.prompt, "Create hello.txt") {
				t.Errorf("call %d prompt missing distill instruction/events: %.120q", i, c.prompt)
			}
		}
		if got[0].maxTok != 16384 || got[1].maxTok != 32768 {
			t.Errorf("max_tokens escalation = %d → %d, want 16384 → 32768", got[0].maxTok, got[1].maxTok)
		}
		// The re-issued prompt is byte-identical: the receipt's prompt_sha16
		// attests one exact request body, not two divergent attempts.
		if got[0].prompt != got[1].prompt {
			t.Error("escalated request's prompt differs from the first")
		}

		// Receipts on the fold marker (additive over the OMP-route shape).
		// The wiki commit memory_update rides above the marker as the tail
		// row; the marker itself carries the receipts.
		events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
		if tail := events[len(events)-1]; tail.Type != store.EventMemoryUpdate {
			t.Fatalf("last event = %s, want the wiki commit memory_update", tail.Type)
		}
		last := events[len(events)-2]
		if last.Type != store.EventReviewAction {
			t.Fatalf("marker event = %s, want review_action", last.Type)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(last.Payload, &payload); err != nil {
			t.Fatalf("marker payload: %v", err)
		}
		if payload["via"] != "moa" || payload["model"] != "orch-m3k" {
			t.Errorf("receipt route = %v/%v, want moa/orch-m3k", payload["via"], payload["model"])
		}
		if payload["prompt_sha16"] != sha16([]byte(got[1].prompt)) {
			t.Errorf("prompt_sha16 = %v, want sha16 of the wire prompt", payload["prompt_sha16"])
		}
		if payload["budget"] != float64(32768) || payload["output_tokens"] != float64(555) {
			t.Errorf("budget/output_tokens = %v/%v, want 32768/555", payload["budget"], payload["output_tokens"])
		}
		esc, ok := payload["escalations"].([]interface{})
		if !ok || len(esc) != 1 {
			t.Fatalf("escalations = %v, want one ledger entry", payload["escalations"])
		}
		e := esc[0].(map[string]interface{})
		if e["from"] != float64(16384) || e["to"] != float64(32768) || e["output_tokens"] != float64(777) {
			t.Errorf("escalation = %v, want 16384→32768 at 777 output tokens", e)
		}

		// No OMP process served the distill: the prompts dir carries the
		// chat run and the learner one-shot, and nothing rendered the
		// distill instruction through the wrapper.
		matches, err := filepath.Glob(filepath.Join(root, ".odo", "prompts", "*.txt"))
		if err != nil || len(matches) != 2 {
			t.Fatalf("prompt files = %v, err %v — want user + learner only", matches, err)
		}
		for _, m := range matches {
			b, _ := os.ReadFile(m)
			if strings.Contains(string(b), "Summarize the key decisions") {
				t.Errorf("distill prompt %s went through the OMP wrapper on the moa route", m)
			}
		}
	})

	t.Run("truncated answer fails the fold closed", func(t *testing.T) {
		root := initRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		srv, calls := startDistillMoaStub(t, "partial note that must never commit", true)
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		writePrefs(t, home, "distill_via: moa\norchestrator: orch-m3k@test\n")
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
		rig.pollUntilDone(t, convID)

		resp := rig.callExpectErr(t, Request{Cmd: CmdDistill, ConversationID: convID})
		if !strings.Contains(resp.Error, "truncated at the 32768-token hard cap") ||
			!strings.Contains(resp.Error, "fold not committed") {
			t.Errorf("error = %q, want the truncation fail-closed message", resp.Error)
		}
		if got := len(calls()); got != 2 {
			t.Errorf("moa calls = %d, want 2 (escalate once, still truncated)", got)
		}
		// No note, no fold marker, epoch unmoved; the manual failure is
		// journaled (M12: an error toast alone leaves no durable trace).
		if _, err := os.Stat(filepath.Join(root, "wiki", "main-epoch-1.md")); !os.IsNotExist(err) {
			t.Errorf("truncated distill wrote a note: %v", err)
		}
		events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
		var failed, folded bool
		for _, ev := range events {
			if isDistillMarkerEvent(ev) {
				folded = true
			}
			var p map[string]interface{}
			if ev.Type == store.EventMemoryUpdate && json.Unmarshal(ev.Payload, &p) == nil &&
				p["layer"] == "distill" && p["cause"] == "failed" {
				failed = true
			}
		}
		if folded {
			t.Error("fold marker journaled after a truncated answer")
		}
		if !failed {
			t.Error("memory_update{layer:distill,cause:failed} missing")
		}
		conv, err := rig.store.GetConversation(context.Background(), convID)
		if err != nil || conv.Epoch != 1 {
			t.Errorf("epoch = %d, want 1 (no fold committed)", conv.Epoch)
		}
	})

	t.Run("absent, explicit omp, and unknown values keep the OMP route", func(t *testing.T) {
		root := initRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
		// A moa stub that FAILS the test if ever called proves no reroute.
		var moaCalled atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			moaCalled.Store(true)
			w.WriteHeader(http.StatusTeapot)
		}))
		defer srv.Close()
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		markerCount := 0
		distillOnce := func(label string) {
			rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Work " + label})
			rig.pollUntilDone(t, convID)
			d := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
			if d.WikiPath == "" {
				t.Fatalf("%s distill failed", label)
			}
			markerCount++
			events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
			found := 0
			for _, ev := range events {
				if !isDistillMarkerEvent(ev) {
					continue
				}
				found++
				var p map[string]interface{}
				if err := json.Unmarshal(ev.Payload, &p); err != nil {
					t.Fatalf("marker payload: %v", err)
				}
				for _, key := range []string{"via", "model", "prompt_sha16", "output_tokens", "budget"} {
					if _, present := p[key]; present {
						t.Errorf("%s marker carries moa receipt key %q on the OMP route", label, key)
					}
				}
			}
			if found != markerCount {
				t.Errorf("%s distill markers = %d, want %d", label, found, markerCount)
			}
		}
		// No line at all: the dark-launch default is the OMP one-shot —
		// its prompt goes through the wrapper like before R-W2.
		distillOnce("absent")
		writePrefs(t, home, "distill_via: omp\n")
		distillOnce("explicit-omp")
		writePrefs(t, home, "distill_via: warp\n")
		distillOnce("unknown-value")
		if moaCalled.Load() {
			t.Error("moa gateway called on an OMP-route distill")
		}
		matches, err := filepath.Glob(filepath.Join(root, ".odo", "prompts", "*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		distillPrompts := 0
		for _, m := range matches {
			b, _ := os.ReadFile(m)
			if strings.Contains(string(b), "Summarize the key decisions") {
				distillPrompts++
			}
		}
		if distillPrompts != 3 {
			t.Errorf("distill prompts via wrapper = %d, want 3", distillPrompts)
		}
	})
}

// TestDistillMarkerJournalsOmittedSeqs (M18 W2 item 2): when the 256 KiB
// prompt cap cut the window's head, the distill marker carries
// omitted_count / omitted_first_seq / omitted_last_seq — the journal-fact
// twin of the prompt's omission declaration line (TestDistillPromptOmission
// pins the struct/line agreement). first_seq/last_seq keep their full
// (epoch) window meaning; omitted_* name the held-back prefix. Under the
// cap the three keys are ABSENT (additive, optional-when-absent).
func TestDistillMarkerJournalsOmittedSeqs(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Two agent_text rows that each alone outgrow the prompt budget:
	// capEvents keeps the newest (a single newest event is never dropped),
	// so seq 1 becomes the held-back prefix [1, 1].
	big := `{"text":"` + strings.Repeat("x", distillPromptBytesCap) + `"}`
	for range 2 {
		if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventAgentText, big); err != nil {
			t.Fatal(err)
		}
	}

	d1 := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if d1.WikiPath == "" {
		t.Fatalf("over-cap distill failed: %+v", d1)
	}
	markers := payloadsByAction(t, allEvents(t, rig, convID), "distill")
	if len(markers) != 1 {
		t.Fatalf("distill markers = %d, want 1", len(markers))
	}
	m := markers[0]
	if m["omitted_count"] != float64(1) || m["omitted_first_seq"] != float64(1) || m["omitted_last_seq"] != float64(1) {
		t.Errorf("omitted keys = %v/%v/%v, want 1/1/1 (the held-back prefix)",
			m["omitted_count"], m["omitted_first_seq"], m["omitted_last_seq"])
	}
	if m["first_seq"] != float64(1) || m["last_seq"] != float64(2) {
		t.Errorf("window seqs = %v..%v, want 1..2 (full-window meaning unchanged)",
			m["first_seq"], m["last_seq"])
	}

	// Control: a follow-up window well under the cap journals NO omitted_*
	// keys — the fact exists only when the cap dropped events.
	if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventAgentText, `{"text":"tiny tail"}`); err != nil {
		t.Fatal(err)
	}
	d2 := rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})
	if d2.WikiPath == "" {
		t.Fatalf("control distill failed: %+v", d2)
	}
	markers = payloadsByAction(t, allEvents(t, rig, convID), "distill")
	if len(markers) != 2 {
		t.Fatalf("distill markers = %d, want 2", len(markers))
	}
	for _, k := range []string{"omitted_count", "omitted_first_seq", "omitted_last_seq"} {
		if _, ok := markers[1][k]; ok {
			t.Errorf("under-cap marker carries %s — want absent (the omission fact journals only on a cap drop)", k)
		}
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
	// Mock the MoA API: return verdicts based on the model name. Capture
	// each leg's exact request body for the R-W1.5 wire receipt pins (the
	// fanout is parallel → mutex-guarded).
	var bodyMu sync.Mutex
	bodies := map[string][]byte{}
	moaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.Unmarshal(raw, &req)
		bodyMu.Lock()
		bodies[req.Model] = raw
		bodyMu.Unlock()
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
	// R-W1.5: the response reviews carry the wire-exact request receipt —
	// sha16 + byte count of the EXACT body the stub observed for that leg.
	bodyMu.Lock()
	b1, ok1 := bodies["rm1"]
	b2, ok2 := bodies["rm2"]
	bodyMu.Unlock()
	if !ok1 || !ok2 {
		t.Fatalf("stub captured bodies for %v, want both rm1 and rm2", [2]bool{ok1, ok2})
	}
	// D2: the first leg runs grounded (no grounded_reviewer: pref ⇒ the
	// line's FIRST entry) — assert its receipts, then mask them for the
	// pre-D2 shape pin (the stub answers in one round, so no tool calls).
	g0 := rev.Reviews[0]
	if !g0.Grounded || g0.ResolvedBy != "first" || g0.ScopeSHA16 == "" || g0.ScopeFiles == 0 ||
		g0.ToolBudgetExhausted || g0.ReadBytes != 0 || len(g0.ToolCalls) != 0 {
		t.Errorf("grounded receipts = %+v, want grounded/resolved_by/scope set, zero tool spend", g0)
	}
	g0.Grounded, g0.ResolvedBy, g0.ScopeSHA16, g0.ScopeFiles, g0.ScopeTruncated = false, "", "", 0, false
	if want := (ReviewResult{Model: "rm1@test", Verdict: "accept", Comments: "Ship it.", BaseURL: moaSrv.URL,
		RequestSHA16: sha16(b1), RequestBytes: len(b1)}); !reflect.DeepEqual(g0, want) {
		t.Errorf("review[0] = %+v, want %+v", g0, want)
	}
	if want := (ReviewResult{Model: "rm2@test", Verdict: "reject", Comments: "Needs tests.", ThinkingMD: "REJECT\n\nNeeds tests.", BaseURL: moaSrv.URL,
		RequestSHA16: sha16(b2), RequestBytes: len(b2)}); !reflect.DeepEqual(rev.Reviews[1], want) {
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
			Model        string `json:"model"`
			Verdict      string `json:"verdict"`
			RequestSHA16 string `json:"request_sha16"`
			RequestBytes int    `json:"request_bytes"`
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
	// R-W1.5: the JOURNALED row carries the same wire-exact receipts —
	// the moa_review evidence attests the exact bytes each leg received.
	for _, rv := range payload.Reviews {
		model := strings.SplitN(rv.Model, "@", 2)[0]
		want, ok := bodies[model]
		if !ok {
			t.Errorf("journaled review %q: no captured body for model %q", rv.Model, model)
			continue
		}
		if rv.RequestSHA16 != sha16(want) || rv.RequestBytes != len(want) {
			t.Errorf("journaled review %q receipt = %q/%d, want sha16+len of the wire body (=%q/%d)",
				rv.Model, rv.RequestSHA16, rv.RequestBytes, sha16(want), len(want))
		}
	}

	// A4-lite: the review_action event carries a consensus_verdict.
	var fullPayload struct {
		ConsensusVerdict string `json:"consensus_verdict"`
		PatchSha16       string `json:"patch_sha16"`
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

	// M18 W2 item 4: patch_sha16 attests the EXACT diff bytes the panel
	// judged — sha16 of the diff file on disk the handler fenced verbatim.
	diffBytes, err := os.ReadFile(done.Diff.Path)
	if err != nil {
		t.Fatalf("read reviewed diff: %v", err)
	}
	if fullPayload.PatchSha16 != sha16(diffBytes) {
		t.Errorf("patch_sha16 = %q, want sha16 of the judged diff bytes %q", fullPayload.PatchSha16, sha16(diffBytes))
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
		AutoApply:              "main",
		// M19 (V11): loop_notify_on_complete defaults ON; the lock pins it.
		LoopNotifyOnComplete: true,
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
		AutoApply:              "main",
		// No loop_notify_on_complete line in the prefs above: default ON.
		LoopNotifyOnComplete: true,
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

	// Symlink containment (tri-review P0, 2026-08-24): lexical Clean+Rel
	// passes for a checked-in wiki/ symlink, so reads resolve and refuse
	// external targets — committed wiki/ content is implantable.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("TOP SECRET KEY"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlinked NOTE...
	rigWiki := filepath.Join(root, "wiki")
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(rigWiki, "leak.md")); err != nil {
		t.Fatal(err)
	}
	resp := rig.callExpectErr(t, Request{Cmd: CmdReadWiki, Path: filepath.Join(rigWiki, "leak.md")})
	if !strings.Contains(resp.Error, "symlink escapes") {
		t.Errorf("read_wiki symlink note: error = %q, want the escape refused and named", resp.Error)
	}
	if strings.Contains(resp.WikiContent, "TOP SECRET") {
		t.Error("read_wiki symlink note leaked external content")
	}
	// ... and a symlinked parent DIR covering notes beneath it.
	if err := os.MkdirAll(filepath.Join(rigWiki, "misc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(rigWiki, "misc"), filepath.Join(rigWiki, "misc-real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rigWiki, "misc")); err != nil {
		t.Fatal(err)
	}
	resp = rig.callExpectErr(t, Request{Cmd: CmdReadWiki, Path: filepath.Join(rigWiki, "misc", "secret.md")})
	if !strings.Contains(resp.Error, "symlink escapes") {
		t.Errorf("read_wiki symlink dir: error = %q, want the escape refused and named", resp.Error)
	}
	if strings.Contains(resp.WikiContent, "TOP SECRET") {
		t.Error("read_wiki symlink dir leaked external content")
	}
	// An INTRA-wiki symlink stays readable: the resolved form never leaves
	// wiki/, so only the smuggling direction locks.
	if err := os.Symlink(filepath.Join(rigWiki, "main-epoch-1.md"), filepath.Join(rigWiki, "alias.md")); err != nil {
		t.Fatal(err)
	}
	if got = rig.call(t, Request{Cmd: CmdReadWiki, Path: filepath.Join(rigWiki, "alias.md")}); got.WikiContent != content {
		t.Errorf("read_wiki intra-wiki alias = %q, want %q (containment locks only the smuggling direction)", got.WikiContent, content)
	}
}

// TestReadSkill covers the read_skill containment classes (tri-review P0,
// 2026-08-24): a real project skill rounds back, a project-missing name
// falls through to the user's global tree, and a checked-in SYMLINK in the
// project skills dir is refused with the escape named — the repo-committable
// candidate must not read outside .odo/skills, and a refusal is never
// silently shadowed by a same-named global skill.
func TestReadSkill(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	rig := startRig(t, root)
	defer rig.stop(t)

	projectSkills := filepath.Join(root, ".odo", "skills")
	if err := os.MkdirAll(projectSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	globalSkills := filepath.Join(home, ".odo", "skills")
	if err := os.MkdirAll(globalSkills, 0o755); err != nil {
		t.Fatal(err)
	}

	// Happy path: a real project skill file.
	body := "# review checklist\n\nverify first\n"
	if err := os.WriteFile(filepath.Join(projectSkills, "review.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := rig.call(t, Request{Cmd: CmdReadSkill, Path: "review.md"})
	if got.SkillContent != body {
		t.Errorf("read_skill project = %q, want %q", got.SkillContent, body)
	}

	// Project-missing names fall through to the global tree.
	globalBody := "# global habit\n"
	if err := os.WriteFile(filepath.Join(globalSkills, "habit.md"), []byte(globalBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if got = rig.call(t, Request{Cmd: CmdReadSkill, Path: "habit.md"}); got.SkillContent != globalBody {
		t.Errorf("read_skill global fallback = %q, want %q", got.SkillContent, globalBody)
	}

	// A project candidate symlinked OUTSIDE the project is refused, escape
	// named, nothing leaked — even with a same-named global skill present.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("TOP SECRET KEY"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(projectSkills, "leak.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalSkills, "leak.md"), []byte("# legit global leak.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := rig.callExpectErr(t, Request{Cmd: CmdReadSkill, Path: "leak.md"})
	if !strings.Contains(resp.Error, "symlink escapes") {
		t.Errorf("read_skill symlink: error = %q, want the escape refused and named", resp.Error)
	}
	if strings.Contains(resp.SkillContent, "TOP SECRET") {
		t.Error("read_skill leaked the external secret")
	}
	if strings.Contains(resp.SkillContent, "legit global") {
		t.Error("read_skill refusal was shadowed by the same-named global skill")
	}
}

// TestReadFile covers the inline file preview IPC (tri-model right sidebar
// gap): project-relative and absolute paths inside the root round-trip with
// exact content, ~/.odo is reachable, binary files and escape attempts
// (absolute outside, ../ traversal, symlink escape) are rejected, and a
// large file truncates at readFileMaxBytes.
func TestReadFile(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	rig := startRig(t, root)
	defer rig.stop(t)

	rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})

	// Project file, read via a relative path.
	target := filepath.Join(root, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "package main\n\n// 你好 — unicode stays intact\n"
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// The daemon canonicalizes (EvalSymlinks) — macOS t.TempDir() under
	// /var symlinks through /private/var, so compare resolved forms.
	targetResolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	got := rig.call(t, Request{Cmd: CmdReadFile, Path: "src/main.go"})
	if got.FileContent != content {
		t.Errorf("relative read = %q, want %q", got.FileContent, content)
	}
	if got.FileResolved != targetResolved {
		t.Errorf("resolved = %q, want %q", got.FileResolved, targetResolved)
	}
	if got.FileTruncated {
		t.Error("small file unexpectedly truncated")
	}

	// Same file via its absolute path.
	got = rig.call(t, Request{Cmd: CmdReadFile, Path: target})
	if got.FileContent != content {
		t.Errorf("absolute read = %q, want %q", got.FileContent, content)
	}

	// ~/.odo is the second allowed tree.
	secret := filepath.Join(home, ".odo", "notes.md")
	if err := os.MkdirAll(filepath.Dir(secret), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("secret notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = rig.call(t, Request{Cmd: CmdReadFile, Path: "~/.odo/notes.md"})
	if got.FileContent != "secret notes\n" {
		t.Errorf("~/.odo read = %q, want 'secret notes\\n'", got.FileContent)
	}

	// Binary file: a NUL in the first 8 KiB is rejected as non-previewable.
	bin := filepath.Join(root, "blob.bin")
	if err := os.WriteFile(bin, []byte{'P', 'K', 0, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	resp := rig.callExpectErr(t, Request{Cmd: CmdReadFile, Path: "blob.bin"})
	if !strings.Contains(resp.Error, "binary") {
		t.Errorf("binary error = %q, want 'binary'", resp.Error)
	}

	// Escapes are rejected: absolute outside, ../ traversal, symlink out.
	outside := filepath.Join(root, "sub", "..", "..", "etc-passwd")
	for _, p := range []string{
		"/etc/hosts",
		filepath.Join(root, "..", "..", "escape.txt"),
		outside,
	} {
		resp := rig.callExpectErr(t, Request{Cmd: CmdReadFile, Path: p})
		if !strings.Contains(resp.Error, "outside project root") && !strings.Contains(resp.Error, "no such file") {
			t.Errorf("escape %s: error = %q, want containment/not-found", p, resp.Error)
		}
	}

	// Symlink inside the project pointing outside is rejected.
	link := filepath.Join(root, "evil-link")
	if err := os.Symlink("/etc", link); err == nil {
		resp := rig.callExpectErr(t, Request{Cmd: CmdReadFile, Path: "evil-link/hosts"})
		if !strings.Contains(resp.Error, "outside project root") {
			t.Errorf("symlink escape: error = %q, want containment", resp.Error)
		}
	}

	// A >512KB file truncates at readFileMaxBytes with the flag set.
	big := filepath.Join(root, "big.txt")
	bigContent := strings.Repeat("x", readFileMaxBytes+1024)
	if err := os.WriteFile(big, []byte(bigContent), 0o644); err != nil {
		t.Fatal(err)
	}
	got = rig.call(t, Request{Cmd: CmdReadFile, Path: "big.txt"})
	if !got.FileTruncated {
		t.Error("big file: truncated flag missing")
	}
	if len(got.FileContent) != readFileMaxBytes {
		t.Errorf("big file content = %d bytes, want %d", len(got.FileContent), readFileMaxBytes)
	}
}

// TestReadFileSparsePreview pins the streaming large-file branch
// (tri-review P2): a file orders of magnitude past the cap — a sparse
// 4 GiB log stand-in — previews with truncation set and its head intact.
// The bytes past the cap must never be read (the pre-fix implementation
// os.ReadFile'd the whole file before slicing; a multi-GB read would not
// survive the test's memory/time budget).
func TestReadFileSparsePreview(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})

	// Text head (the binary guard reads only the first 8 KiB), then a hole
	// out to 4 GiB — holes return NULs lazily, so reads stay cheap.
	head := strings.Repeat("a", 16*1024) + "\n"
	huge := filepath.Join(root, "huge.log")
	fh, err := os.OpenFile(huge, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(head); err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(4 << 30); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}

	got := rig.call(t, Request{Cmd: CmdReadFile, Path: "huge.log"})
	if !got.FileTruncated {
		t.Error("4 GiB sparse file: truncated flag missing")
	}
	if len(got.FileContent) != readFileMaxBytes {
		t.Errorf("sparse preview = %d bytes, want %d", len(got.FileContent), readFileMaxBytes)
	}
	if !strings.HasPrefix(got.FileContent, head) {
		t.Error("sparse preview head mangled — prefix must match the written bytes")
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

// TestListPendingReviews covers the P1a review inbox IPC: pending diffs
// from every active workstream surface in one list with content and
// workstream labels, and accept-by-diffID works cross-workstream without
// switching the active workstream first.
func TestListPendingReviews(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	wsMain := boot.Workstream.ID
	convMain := boot.Conversation.ID

	created := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "feature-x"})
	wsX := created.Workstream.ID
	bootX := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: wsX})
	convX := bootX.Conversation.ID

	// One pending diff per workstream; the stub copies the prompt into
	// hello.txt, so the prompt text lands inside each run's diff content.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convMain, Text: "main work"})
	doneMain := rig.pollUntilDone(t, convMain)
	if doneMain.Diff == nil {
		t.Fatal("main run: no diff")
	}
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convX, Text: "feature work"})
	doneX := rig.pollUntilDone(t, convX)
	if doneX.Diff == nil {
		t.Fatal("feature run: no diff")
	}

	resp := rig.call(t, Request{Cmd: CmdListAllPendingDiffs, ProjectRoot: root})
	if len(resp.AllPendingDiffs) != 2 {
		t.Fatalf("inbox rows = %d, want 2: %+v", len(resp.AllPendingDiffs), resp.AllPendingDiffs)
	}
	// Ordered by workstream id: main's diff first, then feature-x's.
	rows := resp.AllPendingDiffs
	if rows[0].ID != doneMain.Diff.ID || rows[1].ID != doneX.Diff.ID {
		t.Errorf("row order = [%d %d], want [%d %d]",
			rows[0].ID, rows[1].ID, doneMain.Diff.ID, doneX.Diff.ID)
	}
	if rows[0].WorkstreamID != wsMain || rows[0].WorkstreamName != "main" || rows[0].ConversationID != convMain {
		t.Errorf("row 0 labels = (%d,%q,%d), want (%d,%q,%d)",
			rows[0].WorkstreamID, rows[0].WorkstreamName, rows[0].ConversationID, wsMain, "main", convMain)
	}
	if rows[1].WorkstreamID != wsX || rows[1].WorkstreamName != "feature-x" || rows[1].ConversationID != convX {
		t.Errorf("row 1 labels = (%d,%q,%d), want (%d,%q,%d)",
			rows[1].WorkstreamID, rows[1].WorkstreamName, rows[1].ConversationID, wsX, "feature-x", convX)
	}
	if !strings.Contains(rows[0].Content, "main work") {
		t.Errorf("row 0 content missing prompt text; got %q", rows[0].Content)
	}
	if !strings.Contains(rows[1].Content, "feature work") {
		t.Errorf("row 1 content missing prompt text; got %q", rows[1].Content)
	}

	// Accept the feature-x diff by ID without switching to that workstream.
	rig.call(t, Request{Cmd: CmdAcceptDiff, DiffID: doneX.Diff.ID})
	resp = rig.call(t, Request{Cmd: CmdListAllPendingDiffs, ProjectRoot: root})
	if len(resp.AllPendingDiffs) != 1 || resp.AllPendingDiffs[0].ID != doneMain.Diff.ID {
		t.Fatalf("post-accept inbox = %+v, want only main's diff", resp.AllPendingDiffs)
	}
}

// TestListPendingReviewsIncludesOrphanConversationDiff verifies the inbox
// JOIN scope: a diff that stays pending on a pre-distill (no longer active)
// conversation still surfaces — the sidebar count uses the same scope, so
// the row must be actionable from the inbox.
func TestListPendingReviewsIncludesOrphanConversationDiff(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	wsID := boot.Workstream.ID

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "orphan work"})
	done := rig.pollUntilDone(t, convID)
	if done.Diff == nil {
		t.Fatal("no diff")
	}

	// Distill retires the conversation (new epoch, new conversation row);
	// the diff stays pending on the old one.
	rig.call(t, Request{Cmd: CmdDistill, ConversationID: convID})

	resp := rig.call(t, Request{Cmd: CmdListAllPendingDiffs, ProjectRoot: root})
	if len(resp.AllPendingDiffs) != 1 {
		t.Fatalf("inbox rows = %d, want 1 (orphan diff surfaced): %+v",
			len(resp.AllPendingDiffs), resp.AllPendingDiffs)
	}
	row := resp.AllPendingDiffs[0]
	if row.ID != done.Diff.ID || row.ConversationID != convID || row.WorkstreamID != wsID {
		t.Errorf("row = %+v, want diff %d on conversation/workstream %d/%d",
			row, done.Diff.ID, convID, wsID)
	}
	if !strings.Contains(row.Content, "orphan work") {
		t.Errorf("row content missing prompt text; got %q", row.Content)
	}

	// The row is still actionable from the inbox.
	rig.call(t, Request{Cmd: CmdRejectDiff, DiffID: done.Diff.ID})
	resp = rig.call(t, Request{Cmd: CmdListAllPendingDiffs, ProjectRoot: root})
	if len(resp.AllPendingDiffs) != 0 {
		t.Errorf("post-reject inbox = %+v, want empty", resp.AllPendingDiffs)
	}
}

// TestListPendingReviewsEmptyProject verifies a fresh project returns an
// empty inbox, not an error.
func TestListPendingReviewsEmptyProject(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	resp := rig.call(t, Request{Cmd: CmdListAllPendingDiffs, ProjectRoot: root})
	if len(resp.AllPendingDiffs) != 0 {
		t.Errorf("fresh inbox = %+v, want empty", resp.AllPendingDiffs)
	}
	// A foreign project root is refused.
	rig.callExpectErr(t, Request{Cmd: CmdListAllPendingDiffs, ProjectRoot: filepath.Join(root, "elsewhere")})
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

	// Capture the exact bodies the gateway observed (one model leg →
	// sequential requests: initial budget + one escalation re-issue).
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
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
				RequestSHA16 string `json:"request_sha16"`
				RequestBytes int    `json:"request_bytes"`
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
		// R-W1.5: the journaled panel entry attests the FINAL request on
		// the wire — the escalated re-issue's body (16384 → 32768).
		if len(bodies) != 2 {
			t.Fatalf("requests = %d, want 2 (one escalation re-issue)", len(bodies))
		}
		if m.RequestSHA16 != sha16(bodies[1]) || m.RequestBytes != len(bodies[1]) {
			t.Errorf("panel receipt = %q/%d, want sha16+len of the final wire body (=%q/%d)",
				m.RequestSHA16, m.RequestBytes, sha16(bodies[1]), len(bodies[1]))
		}
		if m.RequestSHA16 == sha16(bodies[0]) {
			t.Error("panel receipt must track the escalated body, not the truncated first attempt")
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

// ruleSnapshotRow is one journaled memory_update{cause:"snapshot"|
// "snapshot_failed"} payload with its event seq.
type ruleSnapshotRow struct {
	seq     int
	payload map[string]interface{}
}

// ruleSnapshotRows decodes the conversation's memory_update rows of one
// cause, in seq order.
func ruleSnapshotRows(t *testing.T, events []store.Event, cause string) []ruleSnapshotRow {
	t.Helper()
	var out []ruleSnapshotRow
	for _, ev := range events {
		if ev.Type != store.EventMemoryUpdate {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("memory_update payload: %v", err)
		}
		if p["cause"] == cause {
			out = append(out, ruleSnapshotRow{seq: ev.Seq, payload: p})
		}
	}
	return out
}

// TestRuleSnapshotOnChange covers the W2 rule-file materialization on the
// send path: the first send with non-empty memory.md/pins.md/user.md
// journals one memory_update{cause:"snapshot"} row per layer pinning the
// exact injected bytes (sha pairs with the user_message receipt entry for
// the same source, seq ordered before it); an unchanged send journals no
// new rows; a hand-edit earns a fresh row with a new sha; an over-cap file
// truncates at the reader's cap with capped:true (absent otherwise).
func TestRuleSnapshotOnChange(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	writeRuleFiles := func(mem, pins, user string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, ".odo", "memory.md"), []byte(mem), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".odo", "pins.md"), []byte(pins), 0o644); err != nil {
			t.Fatal(err)
		}
		writeUserMD(t, home, user)
	}
	memA, pinsA, userA := "- snap-rule alpha\n", "- snap-pin alpha\n", "snap-user alpha\n"
	writeRuleFiles(memA, pinsA, userA)

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "first snapshot send"})
	rig.pollUntilDone(t, convID)
	if sent.Event == nil {
		t.Fatal("first send: missing user_message event")
	}

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	snaps := ruleSnapshotRows(t, events, "snapshot")
	if len(snaps) != 3 {
		t.Fatalf("snapshot rows after first send = %d, want 3 (memory/pins/user): %v", len(snaps), snaps)
	}
	wantFirst := map[string]struct{ source, content string }{
		"memory": {".odo/memory.md", memA},
		"pins":   {".odo/pins.md", pinsA},
		"user":   {"~/.odo/user.md", userA},
	}
	receipt := receiptFromEvent(t, sent.Event)
	firstSha := map[string]string{}
	for _, row := range snaps {
		layer, _ := row.payload["layer"].(string)
		want, ok := wantFirst[layer]
		if !ok {
			t.Fatalf("snapshot row layer = %v, want one of memory/pins/user", row.payload["layer"])
		}
		if row.payload["source"] != want.source {
			t.Errorf("layer %s source = %v, want %s", layer, row.payload["source"], want.source)
		}
		if row.payload["content"] != want.content {
			t.Errorf("layer %s content = %q, want the exact file bytes %q", layer, row.payload["content"], want.content)
		}
		if want := sha16([]byte(want.content)); row.payload["sha"] != want {
			t.Errorf("layer %s sha = %v, want %s", layer, row.payload["sha"], want)
		}
		if _, hasCapped := row.payload["capped"]; hasCapped {
			t.Errorf("layer %s: capped key present on an untruncated read", layer)
		}
		if row.seq >= sent.Event.Seq {
			t.Errorf("layer %s snapshot seq %d ≥ user_message seq %d — the row must precede the message it serves", layer, row.seq, sent.Event.Seq)
		}
		if receipt[want.source] != row.payload["sha"] {
			t.Errorf("receipt[%q] = %q, want the snapshot sha %v", want.source, receipt[want.source], row.payload["sha"])
		}
		firstSha[layer], _ = row.payload["sha"].(string)
	}

	// Unchanged files: no new rows.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "unchanged second send"})
	rig.pollUntilDone(t, convID)
	events = rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	if snaps = ruleSnapshotRows(t, events, "snapshot"); len(snaps) != 3 {
		t.Fatalf("snapshot rows after unchanged send = %d, want still 3: %v", len(snaps), snaps)
	}

	// Hand-edits: one fresh row per layer with new shas.
	memB, pinsB, userB := "- snap-rule beta\n", "- snap-pin beta\n", "snap-user beta\n"
	writeRuleFiles(memB, pinsB, userB)
	sent3 := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "post-edit third send"})
	rig.pollUntilDone(t, convID)
	events = rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	snaps = ruleSnapshotRows(t, events, "snapshot")
	if len(snaps) != 6 {
		t.Fatalf("snapshot rows after hand-edit = %d, want 6 (3 first-sight + 3 changed): %v", len(snaps), snaps)
	}
	wantEdit := map[string]struct{ source, content string }{
		"memory": {".odo/memory.md", memB},
		"pins":   {".odo/pins.md", pinsB},
		"user":   {"~/.odo/user.md", userB},
	}
	receipt3 := receiptFromEvent(t, sent3.Event)
	seenEdit := map[string]bool{}
	for _, row := range snaps[3:] {
		layer, _ := row.payload["layer"].(string)
		want := wantEdit[layer]
		if row.payload["content"] != want.content || row.payload["sha"] != sha16([]byte(want.content)) {
			t.Errorf("edited layer %s row = %q sha %v, want %q sha %s", layer, row.payload["content"], row.payload["sha"], want.content, sha16([]byte(want.content)))
		}
		if row.payload["sha"] == firstSha[layer] {
			t.Errorf("edited layer %s sha unchanged %v — a hand-edit must earn a new sha", layer, row.payload["sha"])
		}
		if receipt3[want.source] != row.payload["sha"] {
			t.Errorf("post-edit receipt[%q] = %q, want the new snapshot sha %v", want.source, receipt3[want.source], row.payload["sha"])
		}
		seenEdit[layer] = true
	}
	for layer := range wantEdit {
		if !seenEdit[layer] {
			t.Errorf("no fresh snapshot row for edited layer %s", layer)
		}
	}

	// Over-cap memory.md: the row carries the truncated injected bytes with
	// capped:true, and the sha still pairs with the receipt.
	if err := os.WriteFile(filepath.Join(root, ".odo", "memory.md"), []byte(strings.Repeat("- filler rule line about go vet and tests\n", 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	sent4 := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "capped fourth send"})
	rig.pollUntilDone(t, convID)
	events = rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	snaps = ruleSnapshotRows(t, events, "snapshot")
	if len(snaps) != 7 {
		t.Fatalf("snapshot rows after over-cap edit = %d, want 7 (only memory changed): %v", len(snaps), snaps)
	}
	cappedRow := snaps[6]
	wantCapped := readProjectMemory(root)
	if cappedRow.payload["layer"] != "memory" {
		t.Errorf("over-cap row layer = %v, want memory", cappedRow.payload["layer"])
	}
	if cappedRow.payload["content"] != wantCapped {
		t.Errorf("over-cap content = %d bytes, want the injected cut (%d bytes)", len(fmt.Sprint(cappedRow.payload["content"])), len(wantCapped))
	}
	if cappedRow.payload["sha"] != sha16([]byte(wantCapped)) {
		t.Errorf("over-cap sha = %v, want %s", cappedRow.payload["sha"], sha16([]byte(wantCapped)))
	}
	if cappedRow.payload["capped"] != true {
		t.Errorf("over-cap row capped = %v, want true", cappedRow.payload["capped"])
	}
	if receipt4 := receiptFromEvent(t, sent4.Event); receipt4[".odo/memory.md"] != cappedRow.payload["sha"] {
		t.Errorf("capped receipt entry = %q, want the snapshot sha %v", receipt4[".odo/memory.md"], cappedRow.payload["sha"])
	}
}

// TestRuleSnapshotReconstruction replays memory.md A→B→C across three
// sends: content-at-seq-N (the newest snapshot row with seq ≤ N) equals the
// bytes live when the user_message at seq N was journaled — the middle send
// reconstructs the middle value.
func TestRuleSnapshotReconstruction(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	memPath := filepath.Join(root, ".odo", "memory.md")

	send := func(text string) *store.Event {
		t.Helper()
		resp := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: text})
		rig.pollUntilDone(t, convID)
		if resp.Event == nil {
			t.Fatalf("%s: missing user_message event", text)
		}
		return resp.Event
	}
	if err := os.WriteFile(memPath, []byte("- version A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgA := send("turn A")
	if err := os.WriteFile(memPath, []byte("- version B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgB := send("turn B")
	if err := os.WriteFile(memPath, []byte("- version C\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgC := send("turn C")

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	if snaps := ruleSnapshotRows(t, events, "snapshot"); len(snaps) != 3 {
		t.Fatalf("snapshot rows = %d, want exactly 3 (one per change): %v", len(snaps), snaps)
	}
	// contentAt reconstructs the layer content the user_message at seq N
	// was served: the newest memory-layer snapshot with seq ≤ N.
	contentAt := func(seq int) string {
		t.Helper()
		for i := len(events) - 1; i >= 0; i-- {
			ev := events[i]
			if ev.Seq > seq || ev.Type != store.EventMemoryUpdate {
				continue
			}
			var p struct {
				Layer   string `json:"layer"`
				Cause   string `json:"cause"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				continue
			}
			if p.Layer == "memory" && p.Cause == "snapshot" {
				return p.Content
			}
		}
		return ""
	}
	for _, tc := range []struct {
		name string
		msg  *store.Event
		want string
	}{
		{"first", msgA, "- version A\n"},
		{"middle", msgB, "- version B\n"},
		{"last", msgC, "- version C\n"},
	} {
		if got := contentAt(tc.msg.Seq); got != tc.want {
			t.Errorf("%s send content-at-seq-%d = %q, want %q", tc.name, tc.msg.Seq, got, tc.want)
		}
		if got := receiptFromEvent(t, tc.msg)[".odo/memory.md"]; got != sha16([]byte(tc.want)) {
			t.Errorf("%s send receipt entry = %q, want sha16 of %q", tc.name, got, tc.want)
		}
	}
}

// TestRuleSnapshotFailOpen: a snapshot append failure (a test trigger
// rejects it) journals the snapshot_failed hole marker best-effort and the
// send still completes (appendLedger precedent — a broken snapshot journal
// must not wedge user sends).
func TestRuleSnapshotFailOpen(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	if err := os.WriteFile(filepath.Join(root, ".odo", "memory.md"), []byte("- rejected rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reject snapshot appends only: `%"cause":"snapshot"%` requires the
	// closing quote, so `"cause":"snapshot_failed"` (quote vs underscore)
	// misses and the fallback row still lands.
	if _, err := rig.store.DB().Exec(`CREATE TRIGGER reject_snapshot BEFORE INSERT ON events
		WHEN NEW.type = 'memory_update' AND NEW.payload_json LIKE '%"cause":"snapshot"%'
		BEGIN SELECT RAISE(ABORT, 'snapshot append rejected by test'); END;`); err != nil {
		t.Fatalf("install trigger: %v", err)
	}

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "fail-open send"})
	rig.pollUntilDone(t, convID)

	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	if snaps := ruleSnapshotRows(t, events, "snapshot"); len(snaps) != 0 {
		t.Fatalf("snapshot rows = %d, want 0 (the trigger rejected them): %v", len(snaps), snaps)
	}
	failed := ruleSnapshotRows(t, events, "snapshot_failed")
	if len(failed) != 1 {
		t.Fatalf("snapshot_failed rows = %d, want exactly 1 (the fail-open hole marker): %v", len(failed), failed)
	}
	if failed[0].payload["layer"] != "memory" {
		t.Errorf("snapshot_failed layer = %v, want memory", failed[0].payload["layer"])
	}
	if detail, _ := failed[0].payload["detail"].(string); !strings.Contains(detail, "snapshot append rejected by test") {
		t.Errorf("snapshot_failed detail = %q, want the trigger error", detail)
	}
	// The send itself succeeded (rig.call fails the test otherwise) and the
	// loop closed: its user_message and the run's terminal event are in.
	sent := 0
	for _, ty := range rig.allEventTypes(t, convID) {
		switch ty {
		case store.EventUserMessage:
			sent++
		}
	}
	if sent != 1 {
		t.Errorf("user_message count = %d, want 1 (the fail-open send journaled normally)", sent)
	}
}

// ---------------------------------------------------------------------
// M18 W2 item 4 — model-visible ⇔ logged pre-send closure
// ---------------------------------------------------------------------

// promptFileForText returns the .odo/prompts capture whose tail is the
// (verbatim, un-layered) user text — ground truth for what the agent saw.
func promptFileForText(t *testing.T, root, tail string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, ".odo", "prompts", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.HasSuffix(strings.TrimSpace(string(b)), strings.TrimSpace(tail)) {
			return f
		}
	}
	t.Fatalf("no prompt file ending with %q (%d prompt files found)", tail, len(files))
	return ""
}

// TestAssertPromptReceiptsDetectsGap pins the fail-closed gate: an entry
// dropped between injection start and the send, a hash drifting from the
// injected bytes, and journaled totals drifting from the adapter-bound
// prompt are each refused, named.
func TestAssertPromptReceiptsDetectsGap(t *testing.T) {
	ml := memoryLayers{
		user:          "principles\n",
		project:       "rules\n",
		pins:          "pin\n",
		index:         "idx\n",
		memoryMap:     "map\n",
		todo:          "plan\n",
		resume:        "card\n",
		wiki:          "## Prior notes (recalled)\n\n### main-epoch-3\n",
		skills:        "skill dump\n",
		cross:         "cross dump\n",
		recall:        []recallItem{{path: "/root/wiki/main-epoch-3.md"}},
		skillReceipts: []skillReceiptItem{{path: "/root/.odo/skills/zeta.md", blockHash: "aa"}},
		crossItems:    []crossSource{{path: "/root/wiki/topics/zeta.md"}},
		replay:        "## Journal replay (current epoch)\n",
		replayFirst:   2, replayLast: 3, replayAfter: 1,
		receipt: map[string]string{
			"~/.odo/user.md":                        sha16([]byte("principles\n")),
			".odo/memory.md":                        sha16([]byte("rules\n")),
			".odo/pins.md":                          sha16([]byte("pin\n")),
			"wiki/index.md":                         sha16([]byte("idx\n")),
			"odo#memory-map":                        sha16([]byte("map\n")),
			"journal#todo":                          sha16([]byte("plan\n")),
			"/root/wiki/main-epoch-3.md#open-loops": sha16([]byte("card\n")),
			"/root/wiki/main-epoch-3.md":            "00", // presence-only (sealed at recall)
			"/root/.odo/skills/zeta.md":             "aa", // presence-only (sealed at load)
			"/root/wiki/topics/zeta.md":             "bb", // presence-only (sealed at source)
		},
	}
	prompt := buildPrompt("the goal", nil, ml)
	payload := promptReceiptPayload(ml, prompt)
	if err := assertPromptReceipts(ml, prompt, payload); err != nil {
		t.Fatalf("clean assembly refused: %v", err)
	}
	clone := func() (memoryLayers, map[string]interface{}) {
		m := ml
		m.receipt = map[string]string{}
		for k, v := range ml.receipt {
			m.receipt[k] = v
		}
		return m, promptReceiptPayload(m, prompt)
	}
	t.Run("missing entry", func(t *testing.T) {
		m, pl := clone()
		delete(m.receipt, "~/.odo/user.md")
		if err := assertPromptReceipts(m, prompt, pl); err == nil || !strings.Contains(err.Error(), "missing entry") {
			t.Errorf("err = %v, want a missing-entry breach", err)
		}
	})
	t.Run("hash mismatch", func(t *testing.T) {
		m, pl := clone()
		m.receipt[".odo/memory.md"] = "deadbeefdeadbeef"
		if err := assertPromptReceipts(m, prompt, pl); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
			t.Errorf("err = %v, want a hash-mismatch breach", err)
		}
	})
	t.Run("block hash mismatch", func(t *testing.T) {
		m, pl := clone()
		m.receipt["journal#todo"] = "deadbeefdeadbeef"
		if err := assertPromptReceipts(m, prompt, pl); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
			t.Errorf("err = %v, want a block-hash breach", err)
		}
	})
	t.Run("missing open-loops entry", func(t *testing.T) {
		m, pl := clone()
		delete(m.receipt, "/root/wiki/main-epoch-3.md#open-loops")
		if err := assertPromptReceipts(m, prompt, pl); err == nil || !strings.Contains(err.Error(), "missing entry") {
			t.Errorf("err = %v, want a missing-entry breach for the resume card", err)
		}
	})
	t.Run("presence-only entry missing", func(t *testing.T) {
		m, pl := clone()
		delete(m.receipt, "/root/wiki/topics/zeta.md")
		if err := assertPromptReceipts(m, prompt, pl); err == nil || !strings.Contains(err.Error(), "missing entry") {
			t.Errorf("err = %v, want a missing-entry breach at the presence bound", err)
		}
	})
	t.Run("total mismatch", func(t *testing.T) {
		m, pl := clone()
		pl["total_prompt_bytes"] = len(prompt) + 1
		if err := assertPromptReceipts(m, prompt, pl); err == nil || !strings.Contains(err.Error(), "total_prompt_bytes") {
			t.Errorf("err = %v, want a totals-drift breach", err)
		}
	})
	t.Run("prompt sha mismatch", func(t *testing.T) {
		m, pl := clone()
		pl["prompt_sha16"] = "0bad0bad0bad0bad"
		if err := assertPromptReceipts(m, prompt, pl); err == nil || !strings.Contains(err.Error(), "prompt_sha16") {
			t.Errorf("err = %v, want a prompt-sha breach", err)
		}
	})
}

// receiptClassTable classifies every memoryLayers field for M18 W2 item 4:
// receipted (content-hash / block-hash / presence-only, with the sealing
// boundary) or exempt with a named reason. The reflection test below pins
// exhaustiveness in both directions — a new layer field without a row here
// fails.
var receiptClassTable = map[string]struct {
	class  string // content | block | presence | exempt
	detail string
}{
	"user":           {"content", "~/.odo/user.md"},
	"project":        {"content", ".odo/memory.md"},
	"pins":           {"content", ".odo/pins.md"},
	"index":          {"content", "wiki/index.md"},
	"memoryMap":      {"block", "odo#memory-map"},
	"todo":           {"block", "journal#todo"},
	"resume":         {"block", "<note>#open-loops (key carries the note path)"},
	"wiki":           {"presence", "per-note path — block hash sealed by recallWikiNotesCapped"},
	"recall":         {"presence", "pairs with wiki"},
	"skills":         {"presence", "per-skill path — block hash sealed by loadSkillsForPrompt"},
	"skillReceipts":  {"presence", "pairs with skills"},
	"cross":          {"presence", "per-source chunk — sha sealed by crossWsBlock"},
	"crossItems":     {"presence", "pairs with cross"},
	"replay":         {"exempt", "structural sub-receipt (first/last/after/bytes/dropped_seqs)"},
	"replayFirst":    {"exempt", "part of the replay sub-receipt"},
	"replayLast":     {"exempt", "part of the replay sub-receipt"},
	"replayAfter":    {"exempt", "part of the replay sub-receipt"},
	"replayDropped":  {"exempt", "part of the replay sub-receipt (nil without drops)"},
	"receipt":        {"exempt", "the receipt map itself"},
	"recallHeldBack": {"exempt", "journaled as the recall_held_back count fact"},
}

func TestMemoryLayersReceiptCoverageReflect(t *testing.T) {
	typ := reflect.TypeOf(memoryLayers{})
	for i := range typ.NumField() {
		if name := typ.Field(i).Name; receiptClassTable[name].class == "" {
			t.Errorf("memoryLayers field %q unclassified — receipt it or name the exemption", name)
		}
	}
	for name := range receiptClassTable {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("class table row %q names no memoryLayers field — stale row", name)
		}
	}

	// Full assemblies: a cold one (fold boundary, empty replay → resume
	// card) and a warm one (turns after the boundary → replay window) —
	// every classified key lands, and the gate passes both.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_REGISTRY_PATH", filepath.Join(t.TempDir(), "projects.json"))
	root := t.TempDir()
	writeUserMD(t, home, "# durable principles\n")
	writeFile := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(".odo/memory.md", "# zeta rules\n")
	writeFile(".odo/pins.md", "zeta pin\n")
	writeFile(".odo/skills/zeta-flow.md", "---\nname: zeta-flow\ndescription: zeta steps\nkeywords: [zeta]\n---\n\nDo the zeta.\n")
	writeFile("wiki/index.md", "# zeta index\n")
	writeEpochNote(t, root, "main-epoch-3", "# Epoch 3\n\nzeta decision log.\n\n## Open loops\n\n- finish the zeta migration\n")
	writeTopicPage(t, root, "zeta-topic", "# Zeta\n\nzeta surfaced here. (ui-epoch-2)\n")
	writeEpochNote(t, root, "ui-epoch-2", "# UI 2\n\nzeta sibling content\n")

	st, err := store.Open(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	p, err := st.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, root, nil, nil)
	append := func(evType string, payload map[string]interface{}) {
		t.Helper()
		if _, err := st.AppendEvent(ctx, c.ID, evType, mustJSON(payload)); err != nil {
			t.Fatal(err)
		}
	}
	append(store.EventUserMessage, map[string]interface{}{"text": "zeta ask"})
	append(store.EventAgentText, map[string]interface{}{"text": "zeta answer"})
	append(store.EventReviewAction, map[string]interface{}{"action": "distill", "last_seq": 2})
	if _, err := s.mergeTodoOps(ctx, c.ID, "agent", []todoOp{{Op: todoOpAdd, Text: "zeta cleanup"}}, nil, 4); err != nil {
		t.Fatal(err)
	}

	check := func(name string, ml memoryLayers) {
		t.Helper()
		for field, body := range map[string]string{
			"user": ml.user, "project": ml.project, "pins": ml.pins, "index": ml.index,
			"memoryMap": ml.memoryMap, "todo": ml.todo, "skills": ml.skills,
			"wiki": ml.wiki, "cross": ml.cross,
		} {
			if body == "" {
				t.Errorf("%s: layer %s assembled empty — fixture must cover every field", name, field)
			}
		}
		for k, want := range map[string]string{
			"~/.odo/user.md": ml.user, ".odo/memory.md": ml.project, ".odo/pins.md": ml.pins,
			"wiki/index.md": ml.index, "odo#memory-map": ml.memoryMap, "journal#todo": ml.todo,
		} {
			if got, ok := ml.receipt[k]; !ok || got != sha16([]byte(want)) {
				t.Errorf("%s: receipt[%q] = %q ok=%v, want sha16 of the injected body", name, k, got, ok)
			}
		}
		for _, it := range ml.recall {
			if _, ok := ml.receipt[it.path]; !ok {
				t.Errorf("%s: recalled note %q lacks a receipt entry", name, it.path)
			}
		}
		for _, sr := range ml.skillReceipts {
			if _, ok := ml.receipt[sr.path]; !ok {
				t.Errorf("%s: skill block %q lacks a receipt entry", name, sr.path)
			}
		}
		for _, src := range ml.crossItems {
			if _, ok := ml.receipt[src.path]; !ok {
				t.Errorf("%s: cross chunk %q lacks a receipt entry", name, src.path)
			}
		}
		pr := buildPrompt("zeta next steps", nil, ml)
		if err := assertPromptReceipts(ml, pr, promptReceiptPayload(ml, pr)); err != nil {
			t.Errorf("%s: full assembly refused: %v", name, err)
		}
	}

	cold, err := s.runMemoryLayers(ctx, "main", c.ID, "zeta next steps")
	if err != nil {
		t.Fatalf("cold runMemoryLayers: %v", err)
	}
	if cold.resume == "" || cold.replay != "" {
		t.Fatalf("cold assembly: resume present=%v replay empty=%v, want card + no replay", cold.resume != "", cold.replay == "")
	}
	loops := 0
	for k, v := range cold.receipt {
		if strings.HasSuffix(k, "#open-loops") {
			loops++
			if v != sha16([]byte(cold.resume)) {
				t.Errorf("open-loops receipt = %s, want sha16 of the card", v)
			}
		}
	}
	if loops != 1 {
		t.Errorf("open-loops receipt entries = %d, want exactly 1", loops)
	}
	check("cold", cold)

	append(store.EventUserMessage, map[string]interface{}{"text": "zeta follow-up"})
	append(store.EventAgentText, map[string]interface{}{"text": "zeta done"})
	warm, err := s.runMemoryLayers(ctx, "main", c.ID, "zeta next steps")
	if err != nil {
		t.Fatalf("warm runMemoryLayers: %v", err)
	}
	if warm.replay == "" || warm.resume != "" {
		t.Fatalf("warm assembly: replay present=%v resume empty=%v, want replay + no card", warm.replay != "", warm.resume == "")
	}
	check("warm", warm)
	// The replay rides a structural sub-receipt instead of a content hash.
	payload := promptReceiptPayload(warm, buildPrompt("zeta next steps", nil, warm))
	if _, ok := payload["replay"]; !ok {
		t.Error("warm payload lacks the replay sub-receipt")
	}
}

// TestSendJournalsPromptReceiptClosure: the send's user_message closure
// (total_prompt_bytes, prompt_sha16) byte-matches the exact prompt the
// adapter captured, and the injected read-back map is receipted under
// odo#memory-map (receipted key + recomputable hash).
func TestSendJournalsPromptReceiptClosure(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	writeEpochNote(t, root, "main-epoch-1", "Authentication uses JWT with refresh tokens.\n")
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	const text = "Explain JWT auth"
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: text})
	rig.pollUntilDone(t, convID)

	var p struct {
		Total   int               `json:"total_prompt_bytes"`
		SHA     string            `json:"prompt_sha16"`
		Receipt map[string]string `json:"receipt"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(promptFileForText(t, root, text))
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != len(b) {
		t.Errorf("total_prompt_bytes = %d, want %d (captured prompt bytes)", p.Total, len(b))
	}
	if p.SHA != sha16(b) {
		t.Errorf("prompt_sha16 = %s, want %s (captured prompt bytes)", p.SHA, sha16(b))
	}
	sha, ok := p.Receipt["odo#memory-map"]
	if !ok {
		t.Fatal("odo#memory-map absent — the wiki dir exists so the read-back map was injected")
	}
	mapBlock := memoryMapBlock(root)
	if want := sha16([]byte(mapBlock)); sha != want {
		t.Errorf("odo#memory-map = %s, want %s (sha16 of the injected block)", sha, want)
	}
	if !strings.Contains(string(b), mapBlock) {
		t.Error("captured prompt lacks the read-back map body the receipt attests")
	}
}

// countingAdapter records adapter starts (the fail-closed drill's "stub
// adapter zero starts" probe); the rest of the contract is inert.
type countingAdapter struct{ starts int }

func (c *countingAdapter) Start(_ context.Context, _ string, _ string) (string, error) {
	c.starts++
	return "counting-run", nil
}
func (c *countingAdapter) Send(_ context.Context, _, _ string) error { return nil }
func (c *countingAdapter) Events(_ context.Context, _ string, _ int) ([]adapter.AgentEvent, error) {
	return nil, nil
}
func (c *countingAdapter) Cancel(_ context.Context, _ string) error { return nil }
func (c *countingAdapter) Close(_ context.Context, _ string) error  { return nil }

// TestRunMemoryLayersJournalReadFailure: a journal READ failure refuses
// prompt assembly with a precise cause. The pre-fix shape swallowed the
// error and assembled a blind prompt (no replay, no recall, no rule
// snapshots) with zero trace — the one silent hole on the fail-closed
// chain.
func TestRunMemoryLayersJournalReadFailure(t *testing.T) {
	s, convID := bareServer(t)
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runMemoryLayers(context.Background(), "main", convID, "hi"); err == nil ||
		!strings.Contains(err.Error(), "list journal events") {
		t.Errorf("runMemoryLayers error = %v, want a list journal events refusal", err)
	}
	prompt, payload, err := s.assembleRunPrompt(context.Background(), "main", convID, "hi")
	if err == nil || !strings.Contains(err.Error(), "journal read failed") {
		t.Errorf("assembleRunPrompt error = %v, want the blind-prompt refusal", err)
	}
	if prompt != "" || payload != nil {
		t.Errorf("refused assembly returned prompt=%q payload=%v, want both empty", prompt, payload)
	}
}

// TestSendFailsClosedOnReceiptBreach: with a receipt diverging from the
// injected layers (test seam simulating the production gap this gate
// guards), the send journals the attempt user_message, refuses via the
// existent agent_error, and the adapter records ZERO starts.
func TestSendFailsClosedOnReceiptBreach(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	// Seed the index so the receipt legitimately carries it; the seam then
	// drops exactly that entry between assembly and the gate.
	if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wiki", "index.md"), []byte("# index\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rig := startRig(t, root)
	defer rig.stop(t)

	fake := &countingAdapter{}
	rig.server.RegisterAdapter("countstub", fake)
	rig.server.receiptBreachForTest = func(ml *memoryLayers) { delete(ml.receipt, "wiki/index.md") }

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "hi", Adapter: "countstub"})
	if !strings.Contains(resp.Error, "prompt receipt assertion failed") {
		t.Errorf("error = %q, want the assertion refusal", resp.Error)
	}
	if fake.starts != 0 {
		t.Errorf("adapter starts = %d, want 0 (refusal must precede adapter start)", fake.starts)
	}
	// Journal-first: attempt user_message, then the agent_error naming the
	// breach — both on record.
	if got, want := fmt.Sprint(rig.allEventTypes(t, convID)), "[user_message agent_error]"; got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
	var errText string
	for _, ev := range mustListEvents(t, rig.store, convID) {
		if ev.Type == store.EventAgentError {
			var p struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(ev.Payload, &p)
			errText = p.Error
		}
	}
	if !strings.Contains(errText, "missing entry") {
		t.Errorf("agent_error = %q, want it to name the missing receipt entry", errText)
	}
}

// waitRunPromptRow waits for a follow-up run's review_action
// {action:"run_prompt", origin:<origin>} receipt and returns the decoded
// payload — journaled BEFORE the follow-up's adapter starts (journal-
// first), so polling is the race-free observation.
func waitRunPromptRow(t *testing.T, rig *testRig, convID int64, origin string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		for _, ev := range mustListEvents(t, rig.store, convID) {
			if ev.Type != store.EventReviewAction {
				continue
			}
			var p map[string]interface{}
			if json.Unmarshal(ev.Payload, &p) == nil && p["action"] == "run_prompt" && p["origin"] == origin {
				return p
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the %s run_prompt row never journaled", origin)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestContinuationJournalsRunPrompt: a steer-queued continuation anchors
// its unified receipt closure on review_action{action:"run_prompt",
// origin:"continuation"} (actor:auto_panel so the fold whitelist excludes
// it) — byte-matching the continuation's captured prompt — and journals NO
// user_message duplicate (the steers are already journaled). The row ALSO
// carries steer_seqs, the exact consumption linkage: the queued seqs are
// claimed by this run, never silently folded into a raw prompt.
func TestContinuationJournalsRunPrompt(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	writeEpochNote(t, root, "main-epoch-1", "zeta context\n")
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "zeta work"})
	const steerText = "continue the zeta work"
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: steerText, Steer: true})
	rig.pollUntilDone(t, convID)

	// The continuation admission journals the row BEFORE its adapter
	// starts (journal-first) — wait for it, then drain the run it spawned.
	row := waitRunPromptRow(t, rig, convID, "continuation")
	pollDone(t, rig, convID)

	if got := row["origin"]; got != "continuation" {
		t.Errorf("origin = %v, want continuation (steer chain, not retry)", got)
	}
	if got := row["actor"]; got != autoActor {
		t.Errorf("actor = %v, want %q — the Item-1 fold whitelist keys on it", got, autoActor)
	}
	// Consumption linkage: the steer was seq 2 (send=1, steer=2). A
	// steerless continuation/retry row omits the key entirely — payload
	// byte-stability downstream depends on that.
	seqs, _ := row["steer_seqs"].([]interface{})
	if len(seqs) != 1 || seqs[0] != float64(2) {
		t.Errorf("run_prompt steer_seqs = %v, want [2] (the consumed steer row)", row["steer_seqs"])
	}
	b, err := os.ReadFile(promptFileForText(t, root, steerText))
	if err != nil {
		t.Fatal(err)
	}
	if got := row["prompt_sha16"]; got != sha16(b) {
		t.Errorf("run_prompt prompt_sha16 = %v, want %s (captured continuation prompt)", got, sha16(b))
	}
	if got, ok := row["total_prompt_bytes"].(float64); !ok || int(got) != len(b) {
		t.Errorf("run_prompt total_prompt_bytes = %v, want %d", row["total_prompt_bytes"], len(b))
	}
	receipt, _ := row["receipt"].(map[string]interface{})
	if _, ok := receipt["odo#memory-map"]; !ok {
		t.Errorf("run_prompt receipt lacks odo#memory-map: %v", row["receipt"])
	}
	users := 0
	for _, ev := range mustListEvents(t, rig.store, convID) {
		if ev.Type == store.EventUserMessage {
			users++
		}
	}
	if users != 2 {
		t.Errorf("user_message count = %d, want 2 (send + steer; continuation wrote no duplicate)", users)
	}
}

// TestDropQueuedSteer pins the manual drop op: a queued steer the human
// drops leaves the active run's queue with a review_action
// {action:"steer_dropped", steer_seq} receipt (no actor, no cause — the
// parked_goal_dropped shape), the continuation then carries only the
// surviving steer, and an absent seq (never queued, or already consumed)
// refuses journal-neutral — the benign race the GUI treats as a
// reconcile.
func TestDropQueuedSteer(t *testing.T) {
	t.Run("drop removes the steer and journals the single-seq row", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
		// The slow stub runs 3s: both steers (seq 2, seq 3) land mid-run.
		steerA := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "first note", Steer: true})
		steerB := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "second note", Steer: true})
		seqA := steerA.Event.Seq
		seqB := steerB.Event.Seq

		// The human drops A while the run is still live.
		drop := rig.call(t, Request{Cmd: CmdDropQueuedSteer, ConversationID: convID, SteerSeq: int64(seqA)})
		if !drop.OK {
			t.Fatalf("drop_queued_steer: %s", drop.Error)
		}

		// The runtime queue kept only B.
		rig.server.mu.Lock()
		meta := rig.server.runs[rig.server.byConv[convID]]
		rig.server.mu.Unlock()
		if meta == nil || len(meta.queuedSteers) != 1 ||
			meta.queuedSteers[0].seq != int64(seqB) || meta.queuedSteers[0].text != "second note" {
			t.Fatalf("queuedSteers after drop = %+v, want [{seq %d, second note}]", meta, seqB)
		}

		// The drop row: single-seq form, no actor (a human decision), no
		// cause (that field belongs to the pipeline's batch closes).
		drops := payloadsByAction(t, allEvents(t, rig, convID), "steer_dropped")
		if len(drops) != 1 {
			t.Fatalf("steer_dropped rows = %d, want exactly 1", len(drops))
		}
		if got := drops[0]["steer_seq"]; got != float64(seqA) {
			t.Errorf("steer_dropped steer_seq = %v, want %d", got, seqA)
		}
		if _, ok := drops[0]["cause"]; ok {
			t.Errorf("manual drop row carried cause: %v (batch closes own that field)", drops[0])
		}
		if _, ok := drops[0]["actor"]; ok {
			t.Errorf("manual drop row carried actor: %v (human decisions carry none)", drops[0])
		}

		// The continuation consumes only B: its run_prompt links B's seq,
		// and its prompt contains B's text but NOT A's (proof the drop
		// really removed A, not just journaled over it).
		rig.pollUntilDone(t, convID)
		row := waitRunPromptRow(t, rig, convID, "continuation")
		seqs, _ := row["steer_seqs"].([]interface{})
		if len(seqs) != 1 || seqs[0] != float64(seqB) {
			t.Errorf("continuation steer_seqs = %v, want [%d] (A was dropped)", row["steer_seqs"], seqB)
		}
		pollDone(t, rig, convID)
		b, err := os.ReadFile(promptFileForText(t, root, "second note"))
		if err != nil {
			t.Fatal(err)
		}
		// A's journaled steer row replays into the prompt as HISTORY (one
		// occurrence); the queued-prompt suffix — where a failed drop
		// would repeat it — must not carry it at all.
		if n := strings.Count(string(b), "first note"); n != 1 {
			t.Errorf("continuation prompt carried %d x dropped steer text, want exactly 1 (journal replay only)", n)
		}
		if n := strings.Count(string(b), "second note"); n != 2 {
			t.Errorf("continuation prompt carried %d x surviving steer text, want 2 (replay + queued suffix)", n)
		}
		if got := len(payloadsByAction(t, allEvents(t, rig, convID), "steer_dropped")); got != 1 {
			t.Errorf("steer_dropped rows after continuation = %d, want 1 (consumption is not a drop)", got)
		}
	})

	t.Run("absent seq refuses journal-neutral", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
		rig := startRig(t, root)
		defer rig.stop(t)

		boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
		convID := boot.Conversation.ID
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
		steered := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "mid-run note", Steer: true})
		seq := steered.Event.Seq

		// Never-queued seq: exact contract refusal, nothing journaled.
		resp := rig.callExpectErr(t, Request{Cmd: CmdDropQueuedSteer, ConversationID: convID, SteerSeq: 99})
		if resp.Error != "no queued steer with seq 99" {
			t.Errorf("drop error = %q, want the exact contract refusal", resp.Error)
		}

		// The queued steer survives the bogus drop and is consumed by the
		// continuation — after which dropping IT refuses the same way:
		// consumed already closes the ledger, so the refusal must not
		// journal a drop row ever.
		rig.pollUntilDone(t, convID)
		row := waitRunPromptRow(t, rig, convID, "continuation")
		seqs, _ := row["steer_seqs"].([]interface{})
		if len(seqs) != 1 || seqs[0] != float64(seq) {
			t.Fatalf("continuation steer_seqs = %v, want [%d] (bogus drop lost nothing)", row["steer_seqs"], seq)
		}
		resp = rig.callExpectErr(t, Request{Cmd: CmdDropQueuedSteer, ConversationID: convID, SteerSeq: int64(seq)})
		if resp.Error != fmt.Sprintf("no queued steer with seq %d", seq) {
			t.Errorf("consumed-seq drop error = %q, want the exact contract refusal", resp.Error)
		}
		if got := len(payloadsByAction(t, allEvents(t, rig, convID), "steer_dropped")); got != 0 {
			t.Errorf("steer_dropped rows = %d, want 0 (both refusals were journal-neutral)", got)
		}
	})
}

// TestSteerDroppedOnCancel: a user kill abandons the run's steers WITH it
// — drainRun continues nothing off an errored run, so the drained queue
// closes as one batch steer_dropped{steer_seqs, cause:"cancelled"} (the
// cancel mark split from a genuine agent error), journaled exactly once.
func TestSteerDroppedOnCancel(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	steerA := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "first note", Steer: true})
	steerB := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "second note", Steer: true})
	seqA, seqB := steerA.Event.Seq, steerB.Event.Seq

	rig.call(t, Request{Cmd: CmdCancel, ConversationID: convID})

	// Drain the killed run to terminal (cancel-op shape: the adapter's own
	// agent_error follows the already-journaled "cancelled by user").
	pollDone(t, rig, convID)

	drops := payloadsByAction(t, allEvents(t, rig, convID), "steer_dropped")
	if len(drops) != 1 {
		t.Fatalf("steer_dropped rows = %d, want exactly 1 for the abandoned batch", len(drops))
	}
	if got := drops[0]["cause"]; got != "cancelled" {
		t.Errorf("steer_dropped cause = %v, want cancelled (user kill, not an agent error)", got)
	}
	if got := drops[0]["actor"]; got != autoActor {
		t.Errorf("steer_dropped actor = %v, want %q (pipeline closes, not the human)", got, autoActor)
	}
	seqs, _ := drops[0]["steer_seqs"].([]interface{})
	if len(seqs) != 2 || seqs[0] != float64(seqA) || seqs[1] != float64(seqB) {
		t.Errorf("steer_dropped steer_seqs = %v, want [%d %d]", drops[0]["steer_seqs"], seqA, seqB)
	}
	if got := len(payloadsByAction(t, allEvents(t, rig, convID), "run_prompt")); got != 0 {
		t.Errorf("run_prompt rows = %d, want 0 (a cancelled run fires no continuation)", got)
	}
}

// failStubWrapper sleeps long enough for steers to land mid-run, then
// exits 1 — the adapter drains it as a genuine agent_error, never the
// user-kill mark: exactly one side of the cause split below.
const failStubWrapper = `#!/bin/sh
sleep 2
echo "agent exploded" >&2
exit 1
`

// TestSteerDroppedOnAgentError: the genuine-error side of the cancel
// split — an agent-errored run abandons its queue as
// steer_dropped{steer_seqs, cause:"errored"}, and the error journal
// shape otherwise matches the pre-queue behavior.
func TestSteerDroppedOnAgentError(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, failStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "try and fail"})
	steerA := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "first note", Steer: true})
	steerB := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "second note", Steer: true})
	seqA, seqB := steerA.Event.Seq, steerB.Event.Seq

	pollDone(t, rig, convID)

	drops := payloadsByAction(t, allEvents(t, rig, convID), "steer_dropped")
	if len(drops) != 1 {
		t.Fatalf("steer_dropped rows = %d, want exactly 1 for the abandoned batch", len(drops))
	}
	if got := drops[0]["cause"]; got != "errored" {
		t.Errorf("steer_dropped cause = %v, want errored (agent error, not a user kill)", got)
	}
	seqs, _ := drops[0]["steer_seqs"].([]interface{})
	if len(seqs) != 2 || seqs[0] != float64(seqA) || seqs[1] != float64(seqB) {
		t.Errorf("steer_dropped steer_seqs = %v, want [%d %d]", drops[0]["steer_seqs"], seqA, seqB)
	}
	if got := len(payloadsByAction(t, allEvents(t, rig, convID), "run_prompt")); got != 0 {
		t.Errorf("run_prompt rows = %d, want 0 (an errored run fires no continuation)", got)
	}
}

// TestSteerLedgerClosedOnDaemonRestart pins panel diff #9 finding 1: a
// restart strands every queued steer (runMeta.queuedSteers is memory-only
// by design), and without recovery the GUI's journal-derived queue would
// repopulate them as immortal, un-droppable rows. recoverOpenSteers folds
// the journal at NewServer and closes the ledger exactly once per
// conversation — one batched steer_dropped{cause:"daemon_restart"} —
// and a second boot folds the closure rows and no-ops (idempotent).
func TestSteerLedgerClosedOnDaemonRestart(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	// Single-flight teardown: the crash stop below owns the rig only on
	// the happy path; a fatal abort before it still gets torn down.
	defer rig.stopOnce(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	// The slow stub runs 3s: both steers land mid-run and sit in the
	// memory-only queue (the crash shape — no drain ever closes them).
	steerA := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "first note", Steer: true})
	steerB := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "second note", Steer: true})
	seqA, seqB := steerA.Event.Seq, steerB.Event.Seq
	// Crash: stop with the run live. The journaled steers stay open.
	rig.stopOnce(t)

	mgr := worktree.NewManager(root)
	if err := mgr.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	st, err := store.Open(filepath.Join(mgr.StateDir(), "journal.sqlite"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	steerDrops := func() []map[string]interface{} {
		events, err := st.ListEvents(context.Background(), convID, 0)
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]interface{}
		for _, e := range events {
			if e.Type != store.EventReviewAction {
				continue
			}
			var p map[string]interface{}
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatalf("event %d: %v", e.ID, err)
			}
			if p["action"] == "steer_dropped" {
				out = append(out, p)
			}
		}
		return out
	}

	// First boot after the crash: one batched closure for both open seqs.
	_ = NewServer(st, root, adapter.NewOMP(mgr.StateDir()), mgr)
	drops := steerDrops()
	if len(drops) != 1 {
		t.Fatalf("steer_dropped rows = %d, want exactly 1 (batched daemon_restart close)", len(drops))
	}
	if got := drops[0]["cause"]; got != "daemon_restart" {
		t.Errorf("steer_dropped cause = %v, want daemon_restart", got)
	}
	if got := drops[0]["actor"]; got != autoActor {
		t.Errorf("steer_dropped actor = %v, want %q (pipeline closes, not the human)", got, autoActor)
	}
	seqs, _ := drops[0]["steer_seqs"].([]interface{})
	if len(seqs) != 2 || seqs[0] != float64(seqA) || seqs[1] != float64(seqB) {
		t.Errorf("steer_dropped steer_seqs = %v, want [%d %d]", drops[0]["steer_seqs"], seqA, seqB)
	}

	// Second boot: the closure row folds in — idempotent, no double-close.
	_ = NewServer(st, root, adapter.NewOMP(mgr.StateDir()), mgr)
	if got := len(steerDrops()); got != 1 {
		t.Errorf("steer_dropped rows after second boot = %d, want still 1 (recovery is idempotent)", got)
	}
}

// TestDeriveOrphanedRequest pins the fold's expectation matrix: every
// user_message opens an expectation closed by agent_done/agent_error,
// EXCEPT the no-terminal class — steers, parked goals, /loop control rows
// (field-keyed, never text-keyed) — and unparseable payloads skip
// (recoverOpenSteers precedent).
func TestDeriveOrphanedRequest(t *testing.T) {
	msg := func(seq int, payload string) store.Event {
		return store.Event{Seq: seq, Type: store.EventUserMessage, Payload: json.RawMessage(payload)}
	}
	term := func(seq int, typ string) store.Event {
		return store.Event{Seq: seq, Type: typ, Payload: json.RawMessage(`{}`)}
	}
	cases := []struct {
		name    string
		events  []store.Event
		pending bool
	}{
		{"empty", nil, false},
		{"answered ask", []store.Event{msg(1, `{"text":"hi"}`), term(2, store.EventAgentDone)}, false},
		{"failed ask answered", []store.Event{msg(1, `{"text":"hi"}`), term(2, store.EventAgentError)}, false},
		{"stranded ask", []store.Event{msg(1, `{"text":"do the thing"}`)}, true},
		{"stranded slash ask", []store.Event{msg(1, `{"text":"/panel q","context_scope":"full"}`)}, true},
		{"stranded run then err", []store.Event{msg(1, `{"text":"a"}`), msg(2, `{"text":"b"}`), term(3, store.EventAgentError)}, false},
		{"steer skipped", []store.Event{msg(1, `{"text":"nudge","steer":true}`)}, false},
		{"parked skipped", []store.Event{msg(1, `{"text":"goal","park":true}`)}, false},
		{"loop control skipped", []store.Event{msg(1, `{"text":"/loop status","context_scope":"/loop"}`)}, false},
		{"interleave: steer after stranded ask keeps it open", []store.Event{msg(1, `{"text":"a"}`), msg(2, `{"text":"nudge","steer":true}`)}, true},
		{"unparseable skipped", []store.Event{{Seq: 1, Type: store.EventUserMessage, Payload: json.RawMessage(`{broken`)}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveOrphanedRequest(tc.events); got != tc.pending {
				t.Errorf("deriveOrphanedRequest = %v, want %v", got, tc.pending)
			}
		})
	}
}

// TestOrphanedRequestClosedOnDaemonRestart pins the 2026-08-19 failure: a
// daemon kill (a stray SIGQUIT took a live /panel down) strands an
// in-flight ask — user_message journaled, terminal agent_done/agent_error
// never landed — and the GUI showed the question with zero signal.
// recoverOrphanedRequests folds the journal at NewServer and closes each
// stranded ask with one agent_error{cause:daemon_restart}. The answered
// ask is untouched, a live steer is closed by the sibling steer sweep
// (not by an error row), a parked goal is resumed rather than error-
// closed, and a second boot folds the closure row (idempotent).
func TestOrphanedRequestClosedOnDaemonRestart(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	writePrefs(t, home, "") // hermetic prefs: no auto-land noise
	rig := startRig(t, root)
	// Single-flight teardown: the crash stop below owns the rig only on
	// the happy path; a fatal abort before it still gets torn down.
	defer rig.stopOnce(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Stage the crash shape directly in the journal (RPC sends would run
	// to completion against the stub and close their own expectation —
	// the crash kills the legs BETWEEN journal and terminal).
	stages := []struct {
		typ     string
		payload string
	}{
		{store.EventUserMessage, `{"text":"answered ask"}`},
		{store.EventAgentDone, `{}`},
		{store.EventUserMessage, `{"text":"mid-run nudge","steer":true}`},
		{store.EventUserMessage, `{"text":"later goal","park":true}`},
		{store.EventUserMessage, `{"text":"/panel stranded question","context_scope":"full"}`},
	}
	for _, s := range stages {
		if _, err := rig.store.AppendEvent(context.Background(), convID, s.typ, s.payload); err != nil {
			t.Fatal(err)
		}
	}
	rig.stopOnce(t)

	mgr := worktree.NewManager(root)
	if err := mgr.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	st, err := store.Open(filepath.Join(mgr.StateDir(), "journal.sqlite"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	errorRows := func() []map[string]interface{} {
		events, err := st.ListEvents(context.Background(), convID, 0)
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]interface{}
		for _, e := range events {
			if e.Type != store.EventAgentError {
				continue
			}
			var p map[string]interface{}
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatalf("event %d: %v", e.ID, err)
			}
			out = append(out, p)
		}
		return out
	}
	newBootServer := func() *Server {
		srv := NewServer(st, root, adapter.NewOMP(mgr.StateDir()), mgr)
		srv.autoDisabled = true          // same dark-launch as startRig: no timer noise
		srv.livenessDisabled.Store(true) // C11 ditto: no background drains
		return srv
	}

	// First boot: exactly one closure — the stranded /panel ask. The
	// answered ask provokes none; the parked goal dequeues into a stub run
	// (no user_message, fix-int-w6 contract), and the steer is the sibling
	// sweep's ledger, not this one's.
	_ = newBootServer()
	rows := errorRows()
	if len(rows) != 1 {
		t.Fatalf("agent_error rows after first boot = %d, want exactly 1 (stranded ask closure)\nrows: %v", len(rows), rows)
	}
	if got := rows[0]["cause"]; got != "daemon_restart" {
		t.Errorf("agent_error cause = %v, want daemon_restart", got)
	}

	// Second boot: the closure row clears the fold — idempotent, no
	// double-close.
	_ = newBootServer()
	if got := len(errorRows()); got != 1 {
		t.Errorf("agent_error rows after second boot = %d, want still 1 (recovery is idempotent)", got)
	}
}

// TestPanelProgressHeartbeat pins the poll-side /panel tally: while a
// consult holds the send RPC (legs still answering), poll_events reports
// {done,total}; once the last leg answers the tally is gone (never
// journaled, never stale past the consult).
func TestPanelProgressHeartbeat(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	writePrefs(t, home, "review: pm1@test, pm2@test\n")
	// Gate the MoA stub per arrival order: the first leg answers
	// immediately, the second parks on release — a deterministic
	// {done:1,total:2} mid-flight observation window.
	release := make(chan struct{})
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) >= 2 {
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "panel-answer"}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// The /panel RPC holds for the whole consult (daemon contract), so it
	// runs on its own connection in a goroutine — rig.call would block the
	// test goroutine. Errors land on the result channel.
	type sendResult struct {
		ok  bool
		err string
	}
	sendDone := make(chan sendResult, 1)
	go func() {
		conn, err := net.Dial("unix", rig.sock)
		if err != nil {
			sendDone <- sendResult{err: err.Error()}
			return
		}
		defer conn.Close()
		if err := json.NewEncoder(conn).Encode(Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel heartbeat check"}); err != nil {
			sendDone <- sendResult{err: err.Error()}
			return
		}
		var resp Response
		if err := json.NewDecoder(conn).Decode(&resp); err != nil {
			sendDone <- sendResult{err: err.Error()}
			return
		}
		sendDone <- sendResult{ok: resp.OK, err: resp.Error}
	}()

	// Poll until the first leg's answer lands in the tally: 1 of 2.
	var prog *PanelProgress
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID})
		if resp.PanelProgress != nil {
			prog = resp.PanelProgress
			if prog.Total == 2 && prog.Done == 1 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if prog == nil || prog.Total != 2 || prog.Done != 1 {
		t.Fatalf("mid-flight panel_progress = %+v, want {done:1 total:2}", prog)
	}
	// Per-leg detail: both models registered at fan-out (prefs order),
	// exactly one leg flipped done — and it must agree with the tally.
	if len(prog.Legs) != 2 {
		t.Fatalf("mid-flight legs = %+v, want 2 rows", prog.Legs)
	}
	if prog.Legs[0].Model != "pm1@test" || prog.Legs[1].Model != "pm2@test" {
		t.Errorf("leg models = %q, %q, want pm1@test, pm2@test (prefs order)", prog.Legs[0].Model, prog.Legs[1].Model)
	}
	doneLegs := 0
	for _, leg := range prog.Legs {
		if leg.Done {
			doneLegs++
		}
		if leg.Error {
			t.Errorf("leg %s unexpectedly flagged error mid-flight", leg.Model)
		}
	}
	if doneLegs != prog.Done {
		t.Errorf("done legs = %d, want agreement with tally Done = %d", doneLegs, prog.Done)
	}

	close(release)
	res := <-sendDone
	if !res.ok {
		t.Fatalf("/panel RPC: %s", res.err)
	}
	// Consult over: the tally is unregistered before the RPC returns, so
	// the very next poll must report nothing — no stale progress rows.
	if resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID}); resp.PanelProgress != nil {
		t.Fatalf("panel_progress after completion = %+v, want absent", resp.PanelProgress)
	}
}

// TestPendingCountsSlashConsult pins slash-consult visibility in the
// sidebar badge feed: a /panel holds no run-table entry, yet its
// workstream must read as running for the whole consult (sidebar dot,
// "still running" line, StatusBar chip all derive from running_workstreams)
// and drop out the moment the consult ends.
func TestPendingCountsSlashConsult(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	writePrefs(t, home, "review: pm1@test\n")
	// Single leg, parked until release: the whole consult sits inside one
	// deterministic window.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": "panel-answer"}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	wsID := boot.Workstream.ID
	if pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); len(pc.RunningWorkstreams) != 0 {
		t.Fatalf("fresh running_workstreams = %v, want empty", pc.RunningWorkstreams)
	}

	type sendResult struct {
		ok  bool
		err string
	}
	sendDone := make(chan sendResult, 1)
	go func() {
		conn, err := net.Dial("unix", rig.sock)
		if err != nil {
			sendDone <- sendResult{err: err.Error()}
			return
		}
		defer conn.Close()
		if err := json.NewEncoder(conn).Encode(Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel badge check"}); err != nil {
			sendDone <- sendResult{err: err.Error()}
			return
		}
		var resp Response
		if err := json.NewDecoder(conn).Decode(&resp); err != nil {
			sendDone <- sendResult{err: err.Error()}
			return
		}
		sendDone <- sendResult{ok: resp.OK, err: resp.Error}
	}()

	// Wait for the consult to register, then the badge must list the
	// workstream even though no agent run exists.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rig.server.mu.Lock()
		live := rig.server.slashing[convID] > 0
		rig.server.mu.Unlock()
		if live {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mid := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if got, want := fmt.Sprint(mid.RunningWorkstreams), fmt.Sprint([]int64{wsID}); got != want {
		t.Fatalf("mid-consult running_workstreams = %s, want %s", got, want)
	}

	close(release)
	res := <-sendDone
	if !res.ok {
		t.Fatalf("/panel RPC: %s", res.err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rig.server.mu.Lock()
		done := rig.server.slashing[convID] == 0
		rig.server.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if len(pc.RunningWorkstreams) != 0 {
		t.Fatalf("post-consult running_workstreams = %v, want empty", pc.RunningWorkstreams)
	}
}

// failStartAdapter errors every Start — the post-receipt admission
// failure seam (agent_start) without scripting the whole stub agent.
type failStartAdapter struct{ err error }

func (a failStartAdapter) Start(context.Context, string, string) (string, error) { return "", a.err }
func (failStartAdapter) Send(context.Context, string, string) error              { return nil }
func (failStartAdapter) Events(context.Context, string, int) ([]adapter.AgentEvent, error) {
	return nil, nil
}
func (failStartAdapter) Cancel(context.Context, string) error { return nil }
func (failStartAdapter) Close(context.Context, string) error  { return nil }

// TestSteerRetryAgentStartFailureNoDoubleClose pins panel diff #9 finding
// "double-close": the retry's run_prompt receipt lands BEFORE the adapter
// starts (journal-first, evidence-before-action), so a post-receipt
// failure must NOT also journal steer_dropped — one steer ends exactly
// once (consumed OR dropped, never both).
func TestSteerRetryAgentStartFailureNoDoubleClose(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir()) // hermetic prefs: no auto-land noise
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, silentSlowStub))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "fs-trigger-token do the thing"})
	// The silent stub runs 2s: the steer lands mid-run and rides the
	// automatic retry verdict when run 1 drains as false_stop.
	steer := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "also this", Steer: true})
	seqS := steer.Event.Seq

	// The retry's adapter start fails: swap the default adapter BEFORE
	// the drain that fires the verdict's synchronous retry admission.
	rig.server.adaptersMu.Lock()
	rig.server.adapters[""] = failStartAdapter{err: fmt.Errorf("injected start failure")}
	rig.server.adaptersMu.Unlock()

	rig.pollUntilDone(t, convID)

	// The retry receipt stands (journal-first consumed the steer)…
	row := waitRunPromptRow(t, rig, convID, "retry")
	seqs, _ := row["steer_seqs"].([]interface{})
	if len(seqs) != 1 || seqs[0] != float64(seqS) {
		t.Errorf("retry run_prompt steer_seqs = %v, want [%d]", row["steer_seqs"], seqS)
	}
	// …and NO steer_dropped row contradicts it (the double-close the
	// panel caught: consumed AND abandoned for one seq).
	if got := len(payloadsByAction(t, allEvents(t, rig, convID), "steer_dropped")); got != 0 {
		t.Errorf("steer_dropped rows = %d, want 0 — a post-receipt failure closes nothing (the receipt already consumed)", got)
	}
}

// silentSlowStub is the false-stop signature (exit 0, ZERO output) on a
// steer-landing delay: run 1 counts as false_stop and earns the single
// automatic retry.
const silentSlowStub = `#!/bin/sh
output_file="$3"
sleep 2
: > "$output_file"
exit 0
`

// TestFalseStopRetryConsumesSteers: steers queued against a false-stop
// run ride the automatic retry (verbatim goal + steer texts, the pre-
// queue assembly), so they are CONSUMED — the retry's run_prompt carries
// their seqs and NO steer_dropped row ever lands (the goal itself has no
// seq: the retry can drop only steers).
func TestFalseStopRetryConsumesSteers(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir()) // hermetic prefs: no auto-land noise
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, silentSlowStub))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "fs-steer-token do the thing"})
	steered := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "and this too", Steer: true})
	seq := steered.Event.Seq

	// The retry registers SYNCHRONOUSLY inside run 1's drain (round-2
	// panel fix), so AgentRunning never goes false across the hand-off —
	// this one pollUntilDone consumes the retry's full lifecycle.
	rig.pollUntilDone(t, convID)
	// Guard the TestFalseStopRetryOnce flush: only needed if admission
	// ever goes asynchronous again.
	rig.server.mu.Lock()
	retryRunning := rig.server.byConv[convID] != ""
	rig.server.mu.Unlock()
	if retryRunning {
		rig.pollUntilDone(t, convID)
	}

	// Exactly one follow-up run — the retry — and it claims the steer.
	runs := payloadsByAction(t, allEvents(t, rig, convID), "run_prompt")
	if len(runs) != 1 {
		t.Fatalf("run_prompt rows = %d, want exactly 1 (the retry)", len(runs))
	}
	if got := runs[0]["origin"]; got != "retry" {
		t.Errorf("run_prompt origin = %v, want retry (false-stop retry, not a continuation)", got)
	}
	seqs, _ := runs[0]["steer_seqs"].([]interface{})
	if len(seqs) != 1 || seqs[0] != float64(seq) {
		t.Errorf("retry steer_seqs = %v, want [%d] (the steer rode the retry)", runs[0]["steer_seqs"], seq)
	}
	if got := len(payloadsByAction(t, allEvents(t, rig, convID), "steer_dropped")); got != 0 {
		t.Errorf("steer_dropped rows = %d, want 0 (admission consumed the steer)", got)
	}

	// The retry prompt is the verbatim goal joined with the steer text —
	// assembly behavior unchanged by the identity threading.
	b, err := os.ReadFile(promptFileForText(t, root, "and this too"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "fs-steer-token do the thing") {
		t.Error("retry prompt missing the verbatim goal ahead of the steer texts")
	}
}
