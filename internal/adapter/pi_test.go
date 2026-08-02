package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// installStubPi writes a shell script standing in for the `pi` CLI and points
// ODO_PI_COMMAND at it. The script records its argv (one arg per line) to
// argsFile, prints body to stdout, waits, then exits with code.
func installStubPi(t *testing.T, script string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "stub_pi.sh")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODO_PI_COMMAND", stub)
	t.Setenv("ODO_ARGS_FILE", argsFile)
	return argsFile
}

const stubPiOK = `#!/bin/sh
printf '%s\n' "$@" > "$ODO_ARGS_FILE"
sleep 1
printf 'Pi summary of the task.\n'
exit 0
`

const stubPiFail = `#!/bin/sh
printf '%s\n' "$@" > "$ODO_ARGS_FILE"
sleep 1
printf 'boom: model refused\n' >&2
exit 1
`

// piEventsUntilDone polls Events until the run's terminal event or the
// deadline, returning everything seen.
func piEventsUntilDone(t *testing.T, a *Pi, runID string) []AgentEvent {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var all []AgentEvent
	for {
		evs, err := a.Events(t.Context(), runID, len(all))
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		all = append(all, evs...)
		if n := len(evs); n > 0 {
			if typ := evs[n-1].Type; typ == "agent_done" || typ == "agent_error" {
				return all
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("pi run did not finish within 20s")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestPiAdapter(t *testing.T) {
	ctx := t.Context()
	workdir := t.TempDir()

	argsFile := installStubPi(t, stubPiOK)
	a := NewPi(t.TempDir())
	defer a.CloseAll()

	runID, err := a.Start(ctx, workdir, "Summarize the repo")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Start must not block: the stub sleeps 1s and Events reports nothing yet.
	evs, err := a.Events(ctx, runID, 0)
	if err != nil {
		t.Fatalf("Events while running: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("Events while running = %d events, want 0", len(evs))
	}

	events := piEventsUntilDone(t, a, runID)
	// Seen events must match a fresh full replay: Events is idempotent.
	replay, err := a.Events(ctx, runID, 0)
	if err != nil {
		t.Fatalf("Events replay: %v", err)
	}
	if fmt.Sprint(events) != fmt.Sprint(replay) {
		t.Errorf("replay mismatch:\n got %v\nwant %v", replay, events)
	}

	if got := eventTypesOf(events); fmt.Sprint(got) != "[agent_text agent_done]" {
		t.Fatalf("terminal events = %v, want [agent_text agent_done]", got)
	}
	if events[0].Payload["text"] != "Pi summary of the task." {
		t.Errorf("agent_text = %q", events[0].Payload["text"])
	}
	if events[1].Payload["summary"] != "Pi summary of the task." {
		t.Errorf("agent_done summary = %q", events[1].Payload["summary"])
	}

	// The CLI contract: --print --prompt <text> --cwd <workdir>.
	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("stub args file: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(argsBytes)), "\n")
	want := []string{"--print", "--prompt", "Summarize the repo", "--cwd", workdir}
	if fmt.Sprint(args) != fmt.Sprint(want) {
		t.Errorf("pi argv = %v, want %v", args, want)
	}
	// Runs in the given workdir.
	if _, err := os.Stat(workdir); err != nil {
		t.Errorf("workdir: %v", err)
	}

	// No mid-run steering in M1.
	if err := a.Send(ctx, runID, "also do X"); err == nil {
		t.Error("Send: want error (steering unsupported), got nil")
	}

	// Close removes the run; further Events calls fail.
	if err := a.Close(ctx, runID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := a.Events(ctx, runID, 0); err == nil {
		t.Error("Events after Close: want unknown-run error, got nil")
	}
}

func TestPiAdapterFailure(t *testing.T) {
	ctx := t.Context()
	installStubPi(t, stubPiFail)
	a := NewPi(t.TempDir())
	defer a.CloseAll()

	runID, err := a.Start(ctx, t.TempDir(), "do something")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := piEventsUntilDone(t, a, runID)
	if got := eventTypesOf(events); fmt.Sprint(got) != "[agent_error]" {
		t.Fatalf("terminal events = %v, want [agent_error]", got)
	}
	msg, _ := events[0].Payload["error"].(string)
	if !strings.Contains(msg, "boom: model refused") {
		t.Errorf("agent_error payload missing stderr tail: %q", msg)
	}
}

func eventTypesOf(events []AgentEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}
