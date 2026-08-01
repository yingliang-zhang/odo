// Package adapter — OMP adapter: runs the OMP coding agent in print mode via
// the Hermes timeout wrapper, as a detached subprocess in a run worktree.
//
// M0 shape: Start spawns the wrapper (non-blocking); Events polls the process
// and, once it exits, turns the transcript output file into agent_text +
// agent_done (or agent_error). Individual tool-call events are not parsed in
// M0 — the UI shows "Running..." then the result.
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

const (
	defaultTimeoutSeconds = "600"
	maxStderrTail         = 4096
)

// OMP is the M0 Adapter backed by the Hermes OMP wrapper script.
type OMP struct {
	wrapperPath string
	stateDir    string // <project>/.odo; prompt/session/output files live here
	timeout     string

	mu   sync.Mutex // guards runs only; run results sync via done channel
	runs map[string]*ompRun
}

type ompRun struct {
	id         string
	workdir    string
	outputFile string
	cmd        *exec.Cmd
	stderr     *tailBuffer

	done chan struct{} // closed when the process has exited
	err  error         // set before done is closed; safe to read after <-done
}

// tailBuffer caps captured stderr at maxStderrTail bytes (last-write-wins is
// fine: git/omp errors come at the end).
type tailBuffer struct {
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > maxStderrTail {
		t.buf = t.buf[len(t.buf)-maxStderrTail:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.buf)) }

// defaultWrapperPath resolves the Hermes wrapper under the user's home.
func defaultWrapperPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".hermes", "profiles", "orchestrator", "scripts", "omp_with_timeout.sh")
}

// NewOMP returns an OMP adapter. State (prompts, session dirs, output files)
// goes under stateDir. The wrapper path defaults to the Hermes profile script
// and can be overridden with ODO_OMP_WRAPPER (used by tests/smoke scripts).
func NewOMP(stateDir string) *OMP {
	wrapper := os.Getenv("ODO_OMP_WRAPPER")
	if wrapper == "" {
		wrapper = defaultWrapperPath()
	}
	return &OMP{
		wrapperPath: wrapper,
		stateDir:    stateDir,
		timeout:     defaultTimeoutSeconds,
		runs:        make(map[string]*ompRun),
	}
}

// Start implements Adapter. It writes the prompt to the state dir and spawns
// the wrapper in workdir. The cwd semantic ("OMP operates in the worktree")
// comes from cmd.Dir; no literal --cwd flag is passed because the wrapper
// forwards unknown args to omp.
func (a *OMP) Start(ctx context.Context, workdir string, prompt string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	runID := worktree.NewRunID()
	promptDir := filepath.Join(a.stateDir, "prompts")
	sessionDir := filepath.Join(a.stateDir, "sessions", runID)
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		return "", fmt.Errorf("omp: prompts dir: %w", err)
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return "", fmt.Errorf("omp: session dir: %w", err)
	}
	promptFile := filepath.Join(promptDir, runID+".txt")
	if err := os.WriteFile(promptFile, []byte(prompt), 0o600); err != nil {
		return "", fmt.Errorf("omp: write prompt: %w", err)
	}
	outputFile := filepath.Join(sessionDir, "output.txt")

	args := []string{
		a.timeout,
		promptFile,
		outputFile,
		"--workflow", "coupled-v1",
		"--role", "implement",
		"--run-id", runID,
		"--hermes-provider", "custom:sudo",
		"--hermes-model", "t9s/kimi-k3",
		"--task-tier", "normal",
		"--session-dir", sessionDir,
	}
	cmd := exec.Command(a.wrapperPath, args...)
	cmd.Dir = workdir
	cmd.Stdout = nil // transcript goes to outputFile via the wrapper
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	// Own process group so Cancel can kill the wrapper AND the omp child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("omp: spawn wrapper: %w", err)
	}
	r := &ompRun{
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
		close(r.done)
	}()
	return runID, nil
}

// Send implements Adapter. Steering is not part of M0 (milestone: M1+).
func (a *OMP) Send(ctx context.Context, runID string, message string) error {
	return fmt.Errorf("omp: Send: steering not supported in M0")
}

// Events implements Adapter. While the run is in flight it returns nothing;
// once the process exits it deterministically rebuilds the run's event list
// (agent_text from the output file, then agent_done or agent_error) and
// returns the tail after afterSeq.
func (a *OMP) Events(ctx context.Context, runID string, afterSeq int) ([]AgentEvent, error) {
	a.mu.Lock()
	r, ok := a.runs[runID]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("omp: unknown run %q", runID)
	}

	select {
	case <-r.done:
	default:
		return nil, nil // still running
	}

	events := a.buildEvents(r)
	if afterSeq >= len(events) {
		return nil, nil
	}
	return events[afterSeq:], nil
}

// buildEvents derives the run's terminal events from process state.
// Call only after <-r.done.
func (a *OMP) buildEvents(r *ompRun) []AgentEvent {
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
	if i := strings.IndexByte(summary, '\n'); i >= 0 {
		summary = summary[:i]
	}
	if len(summary) > 200 {
		summary = summary[:200]
	}
	if summary == "" {
		summary = "agent completed"
	}
	events = append(events, AgentEvent{
		Type:    "agent_done",
		Payload: map[string]interface{}{"summary": summary},
	})
	return events
}

// Cancel implements Adapter: SIGKILL the run's process group (wrapper + omp).
func (a *OMP) Cancel(ctx context.Context, runID string) error {
	a.mu.Lock()
	r, ok := a.runs[runID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("omp: unknown run %q", runID)
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
func (a *OMP) Close(ctx context.Context, runID string) error {
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
func (a *OMP) CloseAll() {
	a.mu.Lock()
	runs := make([]*ompRun, 0, len(a.runs))
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
