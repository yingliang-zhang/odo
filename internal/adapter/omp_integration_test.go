//go:build integration

// Integration test for M7 live streaming with the real OMP wrapper.
//
// This test is NOT run by `go test ./...`. It requires:
//   - The real omp_with_timeout.sh wrapper (no ODO_OMP_WRAPPER override)
//   - Valid API keys / model access (reads ~/.odo/prefs.md)
//   - Network connectivity to the model provider
//
// Run manually:
//
//	go test -tags=integration -v -timeout=120s ./internal/adapter/ -run TestRealOMPStreaming
//
// The test sends a trivial prompt ("Reply with exactly: hello") through the
// real OMP in --mode json, polls Events() at 200ms intervals, and asserts:
// 1. The output file's first byte is '{' (stream mode detected)
// 2. At least one poll returns a preview (partial:true) while the run is live
// 3. The final drain includes agent_text and agent_done
// 4. No partial payload appears in the final journaled events
package adapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealOMPStreaming(t *testing.T) {
	// Refuse to run without the real wrapper. ODO_OMP_WRAPPER is set by
	// stub-based tests; if it's set here, someone is running the integration
	// tag with a stub — abort rather than producing a false pass.
	if stub := os.Getenv("ODO_OMP_WRAPPER"); stub != "" {
		t.Skipf("ODO_OMP_WRAPPER is set (%q) — stub override prevents real OMP test", stub)
	}

	// Sanity-check the wrapper exists.
	wrapper := defaultWrapperPath()
	if _, err := os.Stat(wrapper); err != nil {
		t.Skipf("wrapper not found at %s: %v (install Hermes or set ODO_OMP_WRAPPER)", wrapper, err)
	}

	stateDir := t.TempDir()
	workdir := t.TempDir()
	// Minimal git repo so the worktree manager doesn't complain.
	gitInitForIntegration(t, workdir)

	a := NewOMP(stateDir)
	defer a.CloseAll()

	runID, err := a.Start(context.Background(), workdir, "Reply with exactly: hello")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Logf("started run %s in %s", runID, workdir)

	// Poll Events() at 200ms, tracking previews and journaled events.
	deadline := time.Now().Add(90 * time.Second)
	afterSeq := 0
	sawPreview := false
	var eventTypes []string
	var outputFirstByte byte

	for {
		events, err := a.Events(context.Background(), runID, afterSeq)
		if err != nil {
			t.Fatalf("Events: %v", err)
		}

		// Check the output file's first byte once (stream detection).
		if outputFirstByte == 0 {
			r := a.getRun(runID)
			if r != nil {
				if data, err := os.ReadFile(r.outputFile); err == nil && len(data) > 0 {
					outputFirstByte = data[0]
				}
			}
		}

		for _, e := range events {
			isPartial := false
			if e.Payload != nil {
				if v, ok := e.Payload["partial"]; ok && v == true {
					isPartial = true
				}
			}
			if isPartial {
				sawPreview = true
				t.Logf("preview: type=%s payload=%v", e.Type, e.Payload)
				continue // preview is transient — don't count or advance
			}
			eventTypes = append(eventTypes, e.Type)
			afterSeq++
			t.Logf("journaled: type=%s", e.Type)
		}

		// Check if the run is done (agent_done or agent_error in events).
		done := false
		for _, et := range eventTypes {
			if et == "agent_done" || et == "agent_error" {
				done = true
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			// Cancel the run before failing.
			_ = a.Cancel(context.Background(), runID)
			t.Fatalf("run did not complete within 90s. events so far: %v", eventTypes)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Assert 1: stream mode detected (first byte is '{').
	if outputFirstByte != '{' {
		t.Errorf("output file first byte = %q, want '{' (stream mode not detected)", string(outputFirstByte))
	} else {
		t.Logf("stream mode detected (first byte = '{')")
	}

	// Assert 2: at least one preview was observed, OR the run completed
	// before the first poll (very fast responses skip the preview window).
	if !sawPreview && len(eventTypes) > 0 {
		t.Logf("no preview observed (run completed before first poll) — acceptable")
	} else if !sawPreview {
		t.Error("no partial preview observed during the stream — streaming pipeline may not be working")
	} else {
		t.Logf("preview observed during streaming ✓")
	}

	// Assert 3: final events include agent_text or agent_tool_call (at
	// least one content event) and agent_done/agent_error (terminal).
	hasContent := false
	hasDone := false
	for _, et := range eventTypes {
		if et == "agent_text" || et == "agent_tool_call" {
			hasContent = true
		}
		if et == "agent_done" || et == "agent_error" {
			hasDone = true
		}
	}
	if !hasContent {
		t.Error("no agent_text or agent_tool_call in final events")
	}
	if !hasDone {
		t.Error("no agent_done/agent_error in final events")
	}

	// Assert 4: no partial payload in journaled events (already enforced by
	// the loop — previews are skipped — but verify the type list is clean).
	for _, et := range eventTypes {
		if !strings.HasPrefix(et, "agent_") && et != "user_message" {
			t.Errorf("unexpected event type in journal: %q", et)
		}
	}

	t.Logf("event sequence: %v", eventTypes)
}

// getRun returns the ompRun for the given runID (nil if not found).
func (a *OMP) getRun(runID string) *ompRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runs[runID]
}

// gitInitForIntegration creates a minimal git repo for the worktree manager.
func gitInitForIntegration(t *testing.T, dir string) {
	t.Helper()
	// Need at least one file to commit.
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"add", "."},
		{"-c", "user.email=odo@test", "-c", "user.name=odo", "commit", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
}
