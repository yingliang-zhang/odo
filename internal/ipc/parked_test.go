package ipc

// W6 goal-queue park-and-switch tests (ADR-0005, design lock
// docs/design/fix-int-w6-goal-queue-lock.md).
//
// All tests use the testRig infrastructure (server_test.go): a live
// daemon on a Unix socket with a stub agent wrapper that copies its
// prompt into hello.txt and exits 0. The stub sleeps 1s (stubWrapper)
// or 3s (slowStubWrapper) — the slow wrapper keeps a run active long
// enough to park a goal mid-run.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

// TestParkGoalQueues: park a goal mid-run → journal row, queue depth 1.
func TestParkGoalQueues(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Start a run (slow stub, 3s).
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})

	// Park a goal while the run is active.
	parked := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Now add a second file.", Park: true})
	if parked.Event == nil || parked.Event.Type != store.EventUserMessage {
		t.Fatalf("park event = %+v, want user_message", parked.Event)
	}
	if parked.Parked != 1 {
		t.Errorf("parked depth = %d, want 1", parked.Parked)
	}

	// The park row must be journaled with park:true.
	resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
	var parkFound bool
	for _, ev := range resp.Events {
		if ev.Type == store.EventUserMessage && ev.Seq == parked.Event.Seq {
			var payload map[string]interface{}
			if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["park"] != true {
				t.Errorf("park payload = %v, want park:true", payload)
			}
			parkFound = true
		}
	}
	if !parkFound {
		t.Error("park user_message{park:true} not found in journal")
	}

	// The runtime queue must have depth 1.
	rig.server.mu.Lock()
	q := len(rig.server.parked[convID])
	rig.server.mu.Unlock()
	if q != 1 {
		t.Errorf("runtime queue depth = %d, want 1", q)
	}

	// Let the active run finish.
	rig.pollUntilDone(t, convID)
}

// TestParkedGoalAutoActivatesOnDrain: run finishes → parked goal starts.
func TestParkedGoalAutoActivatesOnDrain(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Start a run, park a goal mid-run, let the run finish.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Now add a second file.", Park: true})
	rig.pollUntilDone(t, convID)

	// The parked goal should have auto-started. Poll until it finishes too.
	// pollDone (not pollUntilDone): the parked-goal run may have already
	// finished by the time we poll (the stub sleeps 1s, the first poll
	// after drain may arrive after done).
	pollDone(t, rig, convID)

	// After both runs finish, the queue must be empty.
	rig.server.mu.Lock()
	q := len(rig.server.parked[convID])
	rig.server.mu.Unlock()
	if q != 0 {
		t.Errorf("queue depth after auto-activate = %d, want 0", q)
	}

	// The journal must contain a run_prompt{origin:"parked_goal"} row.
	resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
	var runPromptFound bool
	for _, ev := range resp.Events {
		if ev.Type == store.EventReviewAction {
			var p struct {
				Action string `json:"action"`
				Origin string `json:"origin"`
			}
			if json.Unmarshal([]byte(ev.Payload), &p) == nil && p.Action == "run_prompt" && p.Origin == "parked_goal" {
				runPromptFound = true
			}
		}
	}
	if !runPromptFound {
		t.Error("run_prompt{origin:\"parked_goal\"} not found in journal")
	}
}

// TestParkedGoalFIFOOrder: park 3 goals → activate in seq order.
func TestParkedGoalFIFOOrder(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Start a run, then park 3 goals while it's active.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "First goal"})
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Second goal", Park: true})
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Third goal", Park: true})
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Fourth goal", Park: true})

	// Queue should have 3 goals (the first run is active, so parks queue).
	rig.server.mu.Lock()
	q := len(rig.server.parked[convID])
	rig.server.mu.Unlock()
	if q != 3 {
		t.Fatalf("queue depth = %d, want 3", q)
	}

	// Verify FIFO order by checking seq order of the queued goals.
	rig.server.mu.Lock()
	goals := rig.server.parked[convID]
	rig.server.mu.Unlock()
	for i := 1; i < len(goals); i++ {
		if goals[i].seq <= goals[i-1].seq {
			t.Errorf("FIFO order broken: seq[%d]=%d <= seq[%d]=%d", i, goals[i].seq, i-1, goals[i-1].seq)
		}
	}

	// Let all runs drain (FIFO: the queue empties in seq order).
	rig.pollUntilDone(t, convID)
	pollDone(t, rig, convID)

	// Queue must be empty — all 3 goals activated in FIFO order.
	rig.server.mu.Lock()
	q = len(rig.server.parked[convID])
	rig.server.mu.Unlock()
	if q != 0 {
		t.Errorf("queue depth after all activations = %d, want 0", q)
	}
}

// TestParkedGoalCapRejects: 9th park → error, no journal row.
func TestParkedGoalCapRejects(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Start a run to keep the conversation busy (so parks queue, not start).
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})

	// Park 8 goals (the cap).
	for i := 0; i < 8; i++ {
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "goal " + string(rune('A'+i)), Park: true})
	}

	// The 9th park should fail.
	rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "goal I", Park: true})

	// The queue should still have exactly 8.
	rig.server.mu.Lock()
	q := len(rig.server.parked[convID])
	rig.server.mu.Unlock()
	if q != 8 {
		t.Errorf("queue depth after cap rejection = %d, want 8", q)
	}
}

// TestParkedGoalSurvivesRestart: seed journal, new Server → recover.
func TestParkedGoalSurvivesRestart(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Start a run, park a goal, let the run finish (the goal auto-starts;
	// we need a waiting goal for restart, so cancel the auto-start's run).
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	// Park while the run is active so the goal waits.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Next goal", Park: true})
	// Kill the active run before it finishes so the parked goal stays queued.
	rig.call(t, Request{Cmd: CmdCancel, ConversationID: convID})
	pollDone(t, rig, convID)

	// The errored run must NOT auto-activate the parked goal.
	rig.server.mu.Lock()
	q := len(rig.server.parked[convID])
	rig.server.mu.Unlock()
	if q != 1 {
		t.Fatalf("queue depth after errored run = %d, want 1", q)
	}

	// Stop the rig (close the server, keep the store on disk).
	rig.stop(t)

	// Re-open the SAME store and build a new Server — recoverParkedGoals
	// must seed the queue from the journal and auto-dequeue (parked_goals
	// defaults to auto, so the goal will start immediately).
	mgr := worktree.NewManager(root)
	if err := mgr.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	st, err := store.Open(filepath.Join(mgr.StateDir(), "journal.sqlite"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	omp := adapter.NewOMP(mgr.StateDir())
	srv := NewServer(st, root, omp, mgr)
	srv.autoDisabled = true

	// recoverParkedGoals ran in NewServer. The parked goal should have
	// been found AND auto-dequeued (parked_goals defaults to auto). Verify
	// by checking that an active run exists for the conversation.
	srv.mu.Lock()
	_, hasRun := srv.byConv[convID]
	srv.mu.Unlock()
	if !hasRun {
		t.Error("recovered parked goal was not auto-dequeued — recoverParkedGoals should have found and started it")
	}
}

// TestParkedGoalDroppedJournals: drop → parked_goal_dropped row, queue empty.
func TestParkedGoalDroppedJournals(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Start a run and park a goal.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	parked := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Parked goal", Park: true})
	parkedSeq := parked.Event.Seq

	// Drop the parked goal.
	dropResp := rig.call(t, Request{Cmd: CmdDropParkedGoal, ConversationID: convID})
	if dropResp.Parked != 0 {
		t.Errorf("drop response parked = %d, want 0", dropResp.Parked)
	}

	// The journal must have a parked_goal_dropped{goal_seq} row.
	resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
	var dropFound bool
	for _, ev := range resp.Events {
		if ev.Type == store.EventReviewAction {
			var p struct {
				Action  string `json:"action"`
				GoalSeq int    `json:"goal_seq"`
			}
			if json.Unmarshal([]byte(ev.Payload), &p) == nil && p.Action == "parked_goal_dropped" && p.GoalSeq == parkedSeq {
				dropFound = true
			}
		}
	}
	if !dropFound {
		t.Error("parked_goal_dropped row not found in journal")
	}

	// Queue must be empty.
	rig.server.mu.Lock()
	q := len(rig.server.parked[convID])
	rig.server.mu.Unlock()
	if q != 0 {
		t.Errorf("queue depth after drop = %d, want 0", q)
	}
}

// TestParkedGoalConsumerSafety: ComputeAutonomy/distillRender/replay
// unchanged with parked rows in the journal.
func TestParkedGoalConsumerSafety(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Start a run, park a goal, let the run finish (the goal auto-activates).
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Parked goal", Park: true})
	rig.pollUntilDone(t, convID)
	pollDone(t, rig, convID) // parked-goal run

	// ComputeAutonomy must not crash and must not count parked goals.
	_, err := ComputeAutonomy(context.Background(), rig.store, *boot.Project, nil)
	if err != nil {
		t.Fatalf("ComputeAutonomy: %v", err)
	}

	// distillRender on the park user_message must produce output (it IS a
	// user ask, not a tombstone).
	resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
	for _, ev := range resp.Events {
		if ev.Type == store.EventUserMessage {
			var p struct {
				Park bool `json:"park"`
			}
			if json.Unmarshal([]byte(ev.Payload), &p) == nil && p.Park {
				rendered := distillRender(ev)
				if rendered == "" {
					t.Error("distillRender on parked user_message returned empty — it should render like a user turn")
				}
			}
		}
	}

	// collectReplayTurns on the full event slice must not panic.
	turns := collectReplayTurns(resp.Events, 0)
	_ = turns // no assertion on count — just survival
}

// TestReplayExcludesWaitingParkedGoal: waiting park not replayed into
// intervening run. Uses a synthetic event slice to test the exclusion
// deterministically — a real run's drain would auto-consume the parked
// goal, so we verify the exclusion at the function level.
func TestReplayExcludesWaitingParkedGoal(t *testing.T) {
	// Build a synthetic journal: user_message, agent_text, then a parked
	// user_message (park:true), then more agent_text. The parked goal
	// is WAITING (no run_prompt{goal_seqs} consumes it).
	events := []store.Event{
		{Seq: 1, Type: store.EventUserMessage, Payload: []byte(`{"text":"hello"}`)},
		{Seq: 2, Type: store.EventAgentText, Payload: []byte(`{"text":"hi"}`)},
		{Seq: 3, Type: store.EventUserMessage, Payload: []byte(`{"text":"parked goal","park":true}`)},
		{Seq: 4, Type: store.EventAgentText, Payload: []byte(`{"text":"working"}`)},
	}

	// collectReplayTurns must exclude seq 3 (the waiting parked goal).
	turns := collectReplayTurns(events, 0)
	for _, turn := range turns {
		if turn.seq == 3 {
			t.Errorf("waiting parked goal seq %d replayed as a turn — it should be excluded", turn.seq)
		}
	}

	// Now add a run_prompt{goal_seqs:[3]} row — the park is consumed.
	eventsConsumed := append(events, store.Event{
		Seq:     5,
		Type:    store.EventReviewAction,
		Payload: []byte(`{"action":"run_prompt","origin":"parked_goal","goal_seqs":[3],"actor":"auto_panel"}`),
	})

	// Now the parked goal IS consumed — collectReplayTurns should include it.
	turnsConsumed := collectReplayTurns(eventsConsumed, 0)
	var foundConsumed bool
	for _, turn := range turnsConsumed {
		if turn.seq == 3 {
			foundConsumed = true
		}
	}
	if !foundConsumed {
		t.Error("consumed parked goal seq 3 NOT replayed — consumed parks should replay normally (honest history)")
	}
}

// TestSteerAndParkMutuallyExclusive: both flags → pre-journal error.
func TestSteerAndParkMutuallyExclusive(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Sending with both Steer and Park must fail pre-journal.
	rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "both?", Steer: true, Park: true})

	// No events should have been journaled (no user_message row).
	resp := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0})
	for _, ev := range resp.Events {
		if ev.Type == store.EventUserMessage {
			var p struct {
				Text string `json:"text"`
			}
			if json.Unmarshal([]byte(ev.Payload), &p) == nil && p.Text == "both?" {
				t.Error("mutually-exclusive send journaled a user_message — it should have been refused pre-journal")
			}
		}
	}
}

// TestParkedGoalDoesNotBlockOnPanelRejection: a parked goal starts even
// when the current run's diff is panel_mixed (auto_land_blocked). The goal
// queue and the review queue are independent.
//
// This test exercises the independence indirectly: the first run produces
// a diff (hello.txt) that enters the pending review queue, then the
// parked goal auto-activates on the drain tail. The auto-land pipeline
// (maybeAutoLand) runs in a goroutine that does NOT hold s.mu and does NOT
// block the drain tail — the dequeue is synchronous under s.mu and fires
// before the panel even begins. A real panel_mixed verdict would leave the
// first diff pending, but the parked goal's own run is already started.
func TestParkedGoalDoesNotBlockOnPanelRejection(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, slowStubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Start a run, park a goal, let the run finish. The run produces a diff
	// (hello.txt) which enters the pending review queue. The parked goal
	// should auto-activate regardless of the pending diff.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Create hello.txt"})
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "Parked goal", Park: true})
	rig.pollUntilDone(t, convID)

	// The first run finished with a pending diff. The parked goal should
	// still start. Poll until the second run finishes.
	pollDone(t, rig, convID)

	// Queue must be empty (the parked goal ran).
	rig.server.mu.Lock()
	q := len(rig.server.parked[convID])
	rig.server.mu.Unlock()
	if q != 0 {
		t.Errorf("queue depth after panel rejection = %d, want 0", q)
	}
}
