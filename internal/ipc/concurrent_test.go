package ipc

// M11 P0 concurrency tests: goroutine-per-connection Serve with the shared
// run table guarded by s.mu. Each test would hang, race, or double-journal
// against the serial M0 Serve; run with `go test -race`.

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// asyncResult carries one round-trip's outcome back from a helper goroutine:
// transport errors in err, application errors in resp.Error (resp.OK false).
type asyncResult struct {
	resp Response
	err  error
}

// asyncCall runs one round-trip on a fresh connection in its own goroutine
// and delivers the result on the returned channel (buffered, so the
// goroutine never leaks even if the test stops reading).
func asyncCall(rig *testRig, req Request) <-chan asyncResult {
	ch := make(chan asyncResult, 1)
	go func() {
		resp, err := rig.roundTrip(req)
		ch <- asyncResult{resp: resp, err: err}
	}()
	return ch
}

// asyncPollTillDone polls until the conversation's agent stops running and
// delivers the final poll response. The first poll must report running —
// callers launch right after send, while the stub agent still sleeps.
func asyncPollTillDone(rig *testRig, convID int64) <-chan asyncResult {
	ch := make(chan asyncResult, 1)
	go func() {
		deadline := time.Now().Add(15 * time.Second)
		first := true
		for {
			resp, err := rig.roundTrip(Request{Cmd: CmdPollEvents, ConversationID: convID})
			if err != nil {
				ch <- asyncResult{err: err}
				return
			}
			if !resp.OK {
				ch <- asyncResult{resp: resp}
				return
			}
			if resp.AgentRunning == nil {
				ch <- asyncResult{err: fmt.Errorf("poll_events: agent_running missing")}
				return
			}
			if first && !*resp.AgentRunning {
				ch <- asyncResult{err: fmt.Errorf("poll_events: first poll should report agent_running=true")}
				return
			}
			first = false
			if !*resp.AgentRunning {
				ch <- asyncResult{resp: resp}
				return
			}
			if time.Now().After(deadline) {
				ch <- asyncResult{err: fmt.Errorf("agent did not finish within 15s")}
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	return ch
}

// roundTrip is rig.call without t.Fatal (which is illegal off the test
// goroutine); transport and application errors come back to the caller.
func (r *testRig) roundTrip(req Request) (Response, error) {
	conn, err := net.Dial("unix", r.sock)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// bootstrapConvWithRun additionally drives one agent run to completion, so
// the journal holds events a distill can fold — distilling an untouched
// conversation is rejected as an empty window.
func bootstrapConvWithRun(t *testing.T, rig *testRig, root string) int64 {
	t.Helper()
	convID := bootstrapConv(t, rig, root)
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.pollUntilDone(t, convID)
	return convID
}

// bootstrapConv returns the bound conversation ID for a fresh rig.
func bootstrapConv(t *testing.T, rig *testRig, root string) int64 {
	t.Helper()
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if boot.Conversation == nil {
		t.Fatal("bootstrap: missing conversation")
	}
	return boot.Conversation.ID
}

// requireOK fails unless res came back with a transport-clean OK response.
func requireOK(t *testing.T, what string, res asyncResult) {
	t.Helper()
	if res.err != nil {
		t.Fatalf("%s: transport: %v", what, res.err)
	}
	if !res.resp.OK {
		t.Fatalf("%s: %s", what, res.resp.Error)
	}
}

// TestConcurrentSendDifferentConversations: two goroutines send to different
// conversations simultaneously; both succeed and both agents reach done.
func TestConcurrentSendDifferentConversations(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	conv1 := bootstrapConv(t, rig, root)
	ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "side"})
	if ws.Workstream == nil {
		t.Fatal("create_workstream: missing workstream")
	}
	boot2 := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
	if boot2.Conversation == nil {
		t.Fatal("bootstrap side workstream: missing conversation")
	}
	conv2 := boot2.Conversation.ID
	if conv1 == conv2 {
		t.Fatalf("expected distinct conversations, both are %d", conv1)
	}

	c1 := asyncCall(rig, Request{Cmd: CmdSendMessage, ConversationID: conv1, Text: "Create a.txt"})
	c2 := asyncCall(rig, Request{Cmd: CmdSendMessage, ConversationID: conv2, Text: "Create b.txt"})
	r1, r2 := <-c1, <-c2
	requireOK(t, "send conversation 1", r1)
	requireOK(t, "send conversation 2", r2)
	if r1.resp.Event == nil || r2.resp.Event == nil {
		t.Fatal("send: user message event missing from response")
	}

	// Both conversations have their own live run; each completes. Poll them
	// concurrently — sequentially, waiting out conv1's ~3s stub leaves conv2's
	// agent already done before its first poll could see it running.
	d1 := asyncPollTillDone(rig, conv1)
	d2 := asyncPollTillDone(rig, conv2)
	requireOK(t, "poll conversation 1", <-d1)
	requireOK(t, "poll conversation 2", <-d2)
}

// TestConcurrentSendSameConversation: two simultaneous sends to ONE
// conversation — the mutex must serialize them so exactly one registers the
// run and the other is refused "already running".
func TestConcurrentSendSameConversation(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	c1 := asyncCall(rig, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create a.txt"})
	c2 := asyncCall(rig, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create a.txt"})

	okCount, refused := 0, 0
	for i, res := range []asyncResult{<-c1, <-c2} {
		if res.err != nil {
			t.Fatalf("send %d: transport: %v", i+1, res.err)
		}
		switch {
		case res.resp.OK:
			okCount++
		case strings.Contains(res.resp.Error, "already running"):
			refused++
		default:
			t.Fatalf("send %d: unexpected error %q", i+1, res.resp.Error)
		}
	}
	if okCount != 1 || refused != 1 {
		t.Fatalf("two sends to one conversation: ok=%d refused=%d, want exactly 1/1", okCount, refused)
	}

	rig.pollUntilDone(t, convID)
}

// TestConcurrentDrainNoDoubleJournal: 8 pollers hammer poll_events for the
// same in-flight run. Drains must serialize: each adapter event journals
// exactly once and the run's diff is inserted exactly once.
func TestConcurrentDrainNoDoubleJournal(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})

	const pollers = 8
	errs := make(chan error, pollers)
	var wg sync.WaitGroup
	for range pollers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.Now().Add(15 * time.Second)
			for {
				resp, err := rig.roundTrip(Request{Cmd: CmdPollEvents, ConversationID: convID})
				if err != nil {
					errs <- err
					return
				}
				if !resp.OK {
					errs <- fmt.Errorf("poll_events: %s", resp.Error)
					return
				}
				if resp.AgentRunning != nil && !*resp.AgentRunning {
					return
				}
				if time.Now().After(deadline) {
					errs <- fmt.Errorf("agent did not finish within 15s")
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	// Full journal scan: a double drain would show duplicate agent_text /
	// agent_done rows.
	counts := map[string]int{}
	for _, ty := range rig.allEventTypes(t, convID) {
		counts[ty]++
	}
	if counts[store.EventUserMessage] != 1 || counts[store.EventAgentText] != 1 || counts[store.EventAgentDone] != 1 {
		t.Fatalf("journal event counts = %v, want exactly one user_message, agent_text, agent_done", counts)
	}

	// A double drain would also insert the run's diff twice.
	final := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID})
	if len(final.Diffs) != 1 {
		t.Fatalf("pending diffs = %d, want exactly 1", len(final.Diffs))
	}
}

// distillSleepNote: the distill command runs two one-shots through the stub
// (summary, then learner) — with slowStubWrapper's 3s sleep a distill stays
// in flight for ~6s, giving a wide window for concurrent-request assertions.

// TestPollDuringDistill: poll_events on a second connection is answered
// promptly while a distill is mid-run (the serial M0 Serve would hold it
// behind the distill's connection for the whole run).
func TestPollDuringDistill(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConvWithRun(t, rig, root)

	distill := asyncCall(rig, Request{Cmd: CmdDistill, ConversationID: convID})
	time.Sleep(time.Second) // distill handler is now inside its ~6s agent run

	for i := range 3 {
		start := time.Now()
		res := <-asyncCall(rig, Request{Cmd: CmdPollEvents, ConversationID: convID})
		elapsed := time.Since(start)
		requireOK(t, fmt.Sprintf("poll %d during distill", i+1), res)
		if elapsed > 2*time.Second {
			t.Fatalf("poll during distill took %v; want < 2s — distill must not block other connections", elapsed)
		}
	}

	fin := <-distill
	requireOK(t, "distill", fin)
	if fin.resp.WikiPath == "" {
		t.Fatal("distill: no wiki_path in response")
	}
}

// TestDoubleDistillGuard: two concurrent distills for the same conversation —
// the first reserves the slot, the second is refused "already in progress".
func TestDoubleDistillGuard(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConvWithRun(t, rig, root)

	first := asyncCall(rig, Request{Cmd: CmdDistill, ConversationID: convID})
	time.Sleep(time.Second) // first distill is past its guard, slot reserved

	second := <-asyncCall(rig, Request{Cmd: CmdDistill, ConversationID: convID})
	if second.err != nil {
		t.Fatalf("second distill: transport: %v", second.err)
	}
	if second.resp.OK {
		t.Fatal("second concurrent distill succeeded; want the in-progress guard to refuse it")
	}
	if !strings.Contains(second.resp.Error, "already in progress") {
		t.Fatalf("second distill error = %q, want 'already in progress'", second.resp.Error)
	}

	fin := <-first
	requireOK(t, "first distill", fin)
}

// TestCancelDuringDistill: cancel responds promptly while a distill is
// running. The distill's one-shot is not a conversation run, so cancel is
// refused "no active run" — the point is the response arrives without
// waiting for the distill to finish.
func TestCancelDuringDistill(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConvWithRun(t, rig, root)

	distill := asyncCall(rig, Request{Cmd: CmdDistill, ConversationID: convID})
	time.Sleep(time.Second) // distill is mid agent run

	start := time.Now()
	res := <-asyncCall(rig, Request{Cmd: CmdCancel, ConversationID: convID})
	elapsed := time.Since(start)
	if res.err != nil {
		t.Fatalf("cancel: transport: %v", res.err)
	}
	if res.resp.OK {
		t.Fatal("cancel with no active run unexpectedly succeeded")
	}
	if !strings.Contains(res.resp.Error, "no active run") {
		t.Fatalf("cancel error = %q, want 'no active run'", res.resp.Error)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("cancel during distill took %v; want < 2s — cancel must not wait on the distill", elapsed)
	}

	fin := <-distill
	requireOK(t, "distill", fin)
}

// TestCapRejectsSecondSend: with max_concurrent_runs=1, a second send while
// the first agent is still running must be rejected with the cap error.
// Uses rig.roundTrip (not rig.call) because rig.call fatals on !OK.
func TestCapRejectsSecondSend(t *testing.T) {
	root := initRepo(t)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	// Set cap to 1 via prefs.
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "max_concurrent_runs: 1\n")
	rig := startRig(t, root)
	defer rig.stop(t)

	// Bootstrap main workstream.
	boot1, err := rig.roundTrip(Request{Cmd: CmdBootstrap, ProjectRoot: root})
	if err != nil || !boot1.OK {
		t.Fatalf("bootstrap: err=%v resp=%+v", err, boot1)
	}
	if boot1.Conversation == nil {
		t.Fatal("bootstrap: missing conversation")
	}
	conv1 := boot1.Conversation.ID

	// Create + bootstrap side workstream.
	wsResp, err := rig.roundTrip(Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "side"})
	if err != nil || !wsResp.OK {
		t.Fatalf("create_workstream: err=%v resp=%+v", err, wsResp)
	}
	if wsResp.Workstream == nil {
		t.Fatal("create_workstream: missing workstream")
	}
	boot2, err := rig.roundTrip(Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: wsResp.Workstream.ID})
	if err != nil || !boot2.OK {
		t.Fatalf("bootstrap side: err=%v resp=%+v", err, boot2)
	}
	if boot2.Conversation == nil {
		t.Fatal("bootstrap side: missing conversation")
	}
	conv2 := boot2.Conversation.ID

	// Start first agent (succeeds — 0 active runs, cap=1).
	r1, err := rig.roundTrip(Request{Cmd: CmdSendMessage, ConversationID: conv1, Text: "Create a.txt"})
	if err != nil {
		t.Fatalf("first send: transport: %v", err)
	}
	if !r1.OK {
		t.Fatalf("first send should succeed at cap=1 with 0 active runs, got: %s", r1.Error)
	}

	// Second send to a different conversation must be rejected at cap.
	r2, err := rig.roundTrip(Request{Cmd: CmdSendMessage, ConversationID: conv2, Text: "Create b.txt"})
	if err != nil {
		t.Fatalf("second send: transport: %v", err)
	}
	if r2.OK {
		t.Fatal("second send should have been rejected at cap=1, got OK")
	}
	if !strings.Contains(r2.Error, "cap") {
		t.Fatalf("expected cap error, got: %s", r2.Error)
	}

}
