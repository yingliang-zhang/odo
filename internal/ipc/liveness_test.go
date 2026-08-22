package ipc

import (
	"context"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// This file pins design contract C11 — "GUI-closed loops continue" — as a
// daemon property, not a GUI property. drainRun used to have exactly one
// driver: pollLocked (server.go), i.e. a live GUI poll loop. Close the GUI
// and every in-flight agent run stops mid-flight — for /loop fix/implement
// runs that means no terminal event, so loopPipelineAfterRun/fireLoopTick
// never fire and the loop wedges permanently (2026-08 panel P0). The
// liveness drain (runLivenessDrain/drainActiveRuns, default ON) is the
// daemon-side counterpart of the poll loop; startRig dark-launches it
// (livenessDisabled, the autoDisabled convention) and these tests opt back
// in. startRig dark-launches it for every OTHER test in the package, so a
// live tick is what this file — and only this file — runs with.

// TestLivenessDrainAdvancesRunsWithoutPoll proves the drain moves a run to
// its terminal state with ZERO GUI traffic: after send_message, nothing in
// this test ever calls poll_events, yet the run must journal its terminal
// agent_done, mark itself finished, and land its diff row (the drain's
// terminal tail executed, not just the event append).
func TestLivenessDrainAdvancesRunsWithoutPoll(t *testing.T) {
	root := initRepo(t)
	// HOME isolation for two effects: readUserMemory injects the real
	// ~/.odo/user.md into prompts otherwise, and a prefs.md with a review:
	// line would ARM auto-land (drain tail can land the diff out from
	// under the pending assertion). A bare temp HOME arms nothing — the
	// M20 arming gate exits silently, journal-quiet.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)

	// Opt back in, reversing startRig's dark-launch, at a tick fast enough
	// that a ~1s stub run drains across many ticks. Both stores happen
	// BEFORE any run exists, and every server-state read below goes
	// through s.mu — the drain goroutine reads these atomics continuously
	// (this file runs under -race).
	rig.server.livenessInterval.Store(int64(20 * time.Millisecond))
	rig.server.livenessDisabled.Store(false)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "advance with no gui"})

	// From here: no poll_events, no send, no cancel — the GUI is
	// effectively closed. ANY forward progress is the liveness drain.
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs, err := rig.store.ListEvents(context.Background(), convID, 0)
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		agentDone := false
		for _, ev := range evs {
			if ev.Type == store.EventAgentDone {
				agentDone = true
			}
		}
		rig.server.mu.Lock()
		var finished bool
		for _, meta := range rig.server.runs {
			if meta.conversationID == convID && meta.finished {
				finished = true
			}
		}
		rig.server.mu.Unlock()
		if agentDone && finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not advance without any poll within 5s (agent_done journaled=%v, meta finished=%v)", agentDone, finished)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The terminal tail ran, not merely the event journaled: the stub
	// writes hello.txt, so drainRun's diff path must have produced a
	// pending diff row (work is reviewable even with the GUI gone).
	diffs, err := rig.store.ListPendingDiffs(context.Background(), convID)
	if err != nil {
		t.Fatalf("ListPendingDiffs: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("pending diffs after liveness drain = %d, want 1 (drainRun's terminal diff path)", len(diffs))
	}
	// Ledger shape: agent_text lands BEFORE agent_done — the drain
	// consumed the adapter's event stream in order across its ticks.
	agent := evsOf(t, rig, convID, store.EventAgentText, store.EventAgentDone)
	if got, want := agent[len(agent)-1], store.EventAgentDone; got != want {
		t.Fatalf("last agent event = %s, want %s (full ordered drain)", got, want)
	}
	if agent[0] != store.EventAgentText {
		t.Fatalf("first agent event = %s, want %s", agent[0], store.EventAgentText)
	}
}

// TestLivenessDisabledSkipsDrains is the kill-switch half of the C11 pair:
// startRig's dark-launch (livenessDisabled=true, rest of this package's
// posture) must mean ticks fire but the drain body never runs — not even
// once — even when events have been sitting ready to drain. Keeps the
// pre-C11 package tests byte-stable by construction rather than by luck.
func TestLivenessDisabledSkipsDrains(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	// startRig already stored livenessDisabled=true — this test does NOT
	// opt back in. It only shrinks the tick so the window below exercises
	// ~100 skipped bodies instead of ~1: the switch, not the cadence,
	// must be what suppresses the drain.
	rig := startRig(t, root)
	defer rig.stop(t)
	rig.server.livenessInterval.Store(int64(20 * time.Millisecond))

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "must not drain"})

	// Generous window, no polls: the stub finishes in ~1s, so the second
	// half of the window has terminal events READY to drain (the exact
	// pre-C11 wedge shape) while ~100 ticks fire skip-only.
	time.Sleep(2 * time.Second)

	evs, err := rig.store.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == store.EventAgentDone || ev.Type == store.EventAgentError {
			t.Fatalf("terminal event %s journaled with the drain dark-launched — the kill switch leaked", ev.Type)
		}
	}
	rig.server.mu.Lock()
	defer rig.server.mu.Unlock()
	for _, meta := range rig.server.runs {
		if meta.conversationID == convID {
			if meta.finished {
				t.Fatal("run reached finished with the drain dark-launched")
			}
			if meta.consumed != 0 {
				t.Fatalf("run consumed %d events with the drain dark-launched, want 0", meta.consumed)
			}
			return
		}
	}
	t.Fatal("run vanished from the live map while disabled — nothing may retire it without a drain")
}

// evsOf re-reads the conversation journal and filters to the given event
// types, seq-ordered. Same-package cousin of allEventTypes, minus the
// poll (polling would itself drain — forbidden in this file's assertions).
func evsOf(t *testing.T, rig *testRig, convID int64, types ...string) []string {
	t.Helper()
	evs, err := rig.store.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	want := map[string]bool{}
	for _, ty := range types {
		want[ty] = true
	}
	var out []string
	for _, ev := range evs {
		if want[ev.Type] {
			out = append(out, ev.Type)
		}
	}
	if len(out) == 0 {
		t.Fatalf("journal has no events of types %v", types)
	}
	return out
}
