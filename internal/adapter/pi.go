// Package adapter — Pi adapter: runs the Pi coding agent as a detached
// subprocess, mirroring the OMP adapter's shape. M1 scope: one-shot runs
// (`pi --print --prompt <text> --cwd <workdir>`) with stdout captured to an
// output file; mid-run steering is not supported.
package adapter

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/yingliang-zhang/odo/internal/worktree"
)

// Pi is an Adapter backed by the `pi` CLI in print mode.
type Pi struct {
	command  string // pi executable or stub script
	stateDir string // <project>/.odo; output files live here

	mu   sync.Mutex // guards runs; run results sync via done channel
	runs map[string]*piRun
}

type piRun struct {
	id         string
	workdir    string
	outputFile string
	cmd        *exec.Cmd
	stderr     *tailBuffer

	done chan struct{} // closed when the process has exited
	err  error         // set before done is closed; safe to read after <-done
}

// NewPi returns a Pi adapter. Output files go under stateDir. The command
// defaults to `pi` on PATH and can be overridden with ODO_PI_COMMAND (used by
// tests/smoke scripts).
func NewPi(stateDir string) *Pi {
	command := os.Getenv("ODO_PI_COMMAND")
	if command == "" {
		command = "pi"
	}
	return &Pi{
		command:  command,
		stateDir: stateDir,
		runs:     make(map[string]*piRun),
	}
}

// Start implements Adapter. It spawns `pi --print --prompt <text> --cwd
// <workdir>` with stdout redirected to an output file under the state dir.
func (a *Pi) Start(ctx context.Context, workdir string, prompt string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	runID := worktree.NewRunID()
	sessionDir := filepath.Join(a.stateDir, "sessions", "pi-"+runID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return "", fmt.Errorf("pi: session dir: %w", err)
	}
	outputFile := filepath.Join(sessionDir, "output.txt")
	out, err := os.Create(outputFile)
	if err != nil {
		return "", fmt.Errorf("pi: create output file: %w", err)
	}

	cmd := exec.Command(a.command, "--print", "--prompt", prompt, "--cwd", workdir)
	cmd.Dir = workdir
	cmd.Stdout = out
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	// Own process group so Cancel can kill pi AND any of its children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		out.Close()
		return "", fmt.Errorf("pi: spawn: %w", err)
	}
	r := &piRun{
		id:         runID,
		workdir:    workdir,
		outputFile: outputFile,
		cmd:        cmd,
		stderr:     stderr,
		done:       make(chan struct{}),
	}
	a.mu.Lock()
	a.runs[runID] = r
	a.mu.Unlock()

	go func() {
		r.err = cmd.Wait()
		out.Close() // process exited; our copy of the stdout fd is released
		close(r.done)
	}()
	return runID, nil
}

// Send implements Adapter. Pi has no mid-run steering in M1.
func (a *Pi) Send(ctx context.Context, runID string, message string) error {
	return fmt.Errorf("pi: Send: steering not supported")
}

// Events implements Adapter. While the run is in flight it returns nothing;
// once the process exits it turns the captured stdout into agent_text +
// agent_done (or agent_error) and returns the tail after afterSeq.
func (a *Pi) Events(ctx context.Context, runID string, afterSeq int) ([]AgentEvent, error) {
	a.mu.Lock()
	r, ok := a.runs[runID]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("pi: unknown run %q", runID)
	}

	select {
	case <-r.done:
	default:
		return nil, nil // still running
	}

	events := buildPiEvents(r)
	if afterSeq >= len(events) {
		return nil, nil
	}
	return events[afterSeq:], nil
}

// buildPiEvents derives the run's terminal events from process state and the
// captured stdout.
func buildPiEvents(r *piRun) []AgentEvent {
	var events []AgentEvent
	text := ""
	if data, err := os.ReadFile(r.outputFile); err == nil {
		text = strings.TrimSpace(string(data))
	}
	if text != "" {
		events = append(events, AgentEvent{
			Type:    "agent_text",
			Payload: map[string]interface{}{"text": text},
		})
	}

	if r.err != nil {
		msg := fmt.Sprintf("agent failed: %v", r.err)
		if tail := r.stderr.String(); tail != "" {
			msg = fmt.Sprintf("%s: %s", msg, tail)
		}
		events = append(events, AgentEvent{
			Type:    "agent_error",
			Payload: map[string]interface{}{"error": msg},
		})
		return events
	}

	summary := text
	if summary == "" {
		summary = "agent completed"
	}
	if i := strings.IndexByte(summary, '\n'); i >= 0 {
		summary = summary[:i]
	}
	if len(summary) > 200 {
		summary = summary[:200]
	}
	events = append(events, AgentEvent{
		Type:    "agent_done",
		Payload: map[string]interface{}{"summary": summary},
	})
	return events
}

// Cancel implements Adapter: SIGKILL the run's process group.
func (a *Pi) Cancel(ctx context.Context, runID string) error {
	a.mu.Lock()
	r, ok := a.runs[runID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("pi: unknown run %q", runID)
	}
	select {
	case <-r.done:
		return nil // already finished
	default:
	}
	_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
	return nil
}

// Close implements Adapter. It kills the run if still running and drops all
// state. The worktree is intentionally left on disk for accept/reject.
func (a *Pi) Close(ctx context.Context, runID string) error {
	a.mu.Lock()
	r, ok := a.runs[runID]
	if ok {
		delete(a.runs, runID)
	}
	a.mu.Unlock()
	if !ok {
		return nil // unknown or already closed: nothing to do
	}
	select {
	case <-r.done:
	default:
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		<-r.done // reap
	}
	return nil
}

// CloseAll kills every run still in flight. Called on daemon shutdown so no
// orphaned agent processes keep writing into worktrees.
func (a *Pi) CloseAll() {
	a.mu.Lock()
	runs := make([]*piRun, 0, len(a.runs))
	for id, r := range a.runs {
		runs = append(runs, r)
		delete(a.runs, id)
	}
	a.mu.Unlock()
	for _, r := range runs {
		select {
		case <-r.done:
		default:
			_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
			<-r.done
		}
	}
}
