// Package adapter — OMP adapter: runs the OMP coding agent in print mode via
// the Hermes timeout wrapper, as a detached subprocess in a run worktree.
//
// M0.1 shape: Start spawns the wrapper (non-blocking), resolving the
// model/provider from ~/.odo/prefs.md; Events polls the process and, once it
// exits, turns the transcript output file into agent_text + agent_tool_call /
// agent_tool_result events parsed from the print-mode output, then agent_done
// (or agent_error). Unparseable output degrades to M0 behavior: agent_text +
// agent_done only.
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/yingliang-zhang/odo/internal/worktree"
)

const (
	defaultTimeoutSeconds = "600"
	maxStderrTail         = 4096

	// Fallback model config when ~/.odo/prefs.md is missing or unparseable.
	defaultModel    = "t9s/kimi-k3"
	defaultProvider = "sudo" // passed to the wrapper as "custom:sudo"
)

// OMP is the M0 Adapter backed by the Hermes OMP wrapper script.
type OMP struct {
	wrapperPath      string
	stateDir         string // <project>/.odo; prompt/session/output files live here
	timeout          string
	prefsKey         string // prefs.md key to read model from ("coding" or "orchestrator")
	modelOverride    string // non-empty: bypass prefs.md for the model
	providerOverride string // non-empty: bypass prefs.md for the provider

	mu           sync.Mutex // guards runs + configLogged; run results sync via done channel
	runs         map[string]*ompRun
	configLogged bool // stderr log of resolved model config happens once
}

type ompRun struct {
	id         string
	sessionDir string // directory containing the OMP JSONL transcript
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
		prefsKey:    "coding",
		runs:        make(map[string]*ompRun),
	}
}

// NewOMPForKey creates an OMP adapter that reads its model config from the
// given prefs.md key (e.g. "orchestrator" for the distill use case).
func NewOMPForKey(stateDir, key string) *OMP {
	o := NewOMP(stateDir)
	o.prefsKey = key
	return o
}

// NewOMPExplicit creates an OMP adapter with an explicit model/provider pair,
// bypassing prefs.md. The MoA review fan-out uses it so each reviewer runs
// its own configured model.
func NewOMPExplicit(stateDir, model, provider string) *OMP {
	o := NewOMP(stateDir)
	o.modelOverride = model
	o.providerOverride = provider
	return o
}

// resolveModelConfig resolves the wrapper's --hermes-model / --hermes-provider
// args. Explicit overrides (NewOMPExplicit) win; otherwise ~/.odo/prefs.md is
// re-read on every call so prefs edits apply to the next run without a daemon
// restart. Falls back to the M0 defaults. The resolved pair is logged to
// stderr once (first use) for debugging.
func (a *OMP) resolveModelConfig() (model, providerArg string) {
	model, provider := a.modelOverride, a.providerOverride
	pModel, pProvider := LoadPrefsByKey(a.prefsKey)
	if model == "" {
		model = pModel
	}
	if provider == "" {
		provider = pProvider
	}
	if model == "" {
		model = defaultModel
	}
	if provider == "" {
		provider = defaultProvider
	}
	providerArg = "custom:" + provider

	a.mu.Lock()
	if !a.configLogged {
		a.configLogged = true
		fmt.Fprintf(os.Stderr, "odo: omp model config resolved: provider=%s model=%s\n", providerArg, model)
	}
	a.mu.Unlock()
	return model, providerArg
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

	model, providerArg := a.resolveModelConfig()
	args := []string{
		resolveTimeout(a.timeout),
		promptFile,
		outputFile,
		"--workflow", "coupled-v1",
		"--role", "implement",
		"--run-id", runID,
		"--hermes-provider", providerArg,
		"--hermes-model", model,
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
		sessionDir: sessionDir,
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

// Send implements Adapter. Since M1 it appends the steering message to
// steering.txt in the run's session directory — a best-effort hand-off the
// wrapper may read between turns. A wrapper that never reads it just ignores
// the file; the daemon already journaled the message.
func (a *OMP) Send(ctx context.Context, runID string, message string) error {
	a.mu.Lock()
	r, ok := a.runs[runID]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("omp: unknown run %q", runID)
	}
	select {
	case <-r.done:
		return fmt.Errorf("omp: run %q already finished", runID)
	default:
	}
	f, err := os.OpenFile(filepath.Join(r.sessionDir, "steering.txt"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("omp: steering file: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(message + "\n"); err != nil {
		return fmt.Errorf("omp: write steering: %w", err)
	}
	return nil
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

// Regexes for OMP print-mode tool output:
//
//	⏺ read(file_path="hello.txt")
//	  ⇐ 1  // hello.txt
//
// toolCallRe matches a tool invocation line; toolResultRe matches the
// single-line result that follows it. Both patterns tolerate surrounding
// whitespace; the args capture is greedy so a `)` inside args is kept.
var (
	toolCallRe   = regexp.MustCompile(`(?m)^⏺[ \t]+(\w+)\((.*)\)[ \t]*$`)
	toolResultRe = regexp.MustCompile(`(?m)^[ \t]*⇐[ \t]*(.*)$`)
)

// parseToolCalls extracts agent_tool_call / agent_tool_result events from raw
// OMP print-mode output. A `⏺ TOOL(args)` line emits an agent_tool_call event
// with payload {"tool": TOOL, "args": args}; each `⇐ RESULT` line between a
// tool call and the next one emits an agent_tool_result with payload
// {"tool": TOOL, "result": RESULT} attributed to that tool. Non-empty text
// before the first tool call is emitted as a leading agent_text event.
// Returns nil when the output contains no tool calls, so the caller can fall
// back to whole-text agent_text behavior.
func parseToolCalls(text string) []AgentEvent {
	calls := toolCallRe.FindAllStringSubmatchIndex(text, -1)
	if len(calls) == 0 {
		return nil
	}

	var events []AgentEvent
	if prefix := strings.TrimSpace(text[:calls[0][0]]); prefix != "" {
		events = append(events, AgentEvent{
			Type:    "agent_text",
			Payload: map[string]interface{}{"text": prefix},
		})
	}
	for i, m := range calls {
		name := text[m[2]:m[3]]
		args := text[m[4]:m[5]]
		events = append(events, AgentEvent{
			Type:    "agent_tool_call",
			Payload: map[string]interface{}{"tool": name, "args": args},
		})
		// Results belong to this tool when they appear after its call line
		// and before the next call line.
		region := text[m[1]:]
		if i+1 < len(calls) {
			region = text[m[1]:calls[i+1][0]]
		}
		for _, rm := range toolResultRe.FindAllStringSubmatchIndex(region, -1) {
			events = append(events, AgentEvent{
				Type:    "agent_tool_result",
				Payload: map[string]interface{}{"tool": name, "result": region[rm[2]:rm[3]]},
			})
		}
	}
	return events
}

// buildEvents derives the run's terminal events from process state.
// It reads the OMP session JSONL transcript (which contains structured
// content blocks: text, toolCall, toolResult, thinking) instead of the
// print-mode stdout, because print mode only emits text blocks and
// omits ⏺/⇐ tool markers (those are TUI decorations).
// Falls back to the print-mode output file when no JSONL is available.
func (a *OMP) buildEvents(r *ompRun) []AgentEvent {
	events := parseSessionJSONL(r)

	// Fallback: if JSONL parsing produced nothing, use the print-mode
	// output file (which may contain the final text response, or just
	// "Working..." when the model didn't emit a text block).
	if len(events) == 0 {
		text := ""
		if data, err := os.ReadFile(r.outputFile); err == nil {
			text = strings.TrimSpace(string(data))
		}
		// Strip the "Working..." status prefix that OMP emits to stderr
		// (which the wrapper merges into stdout via 2>&1).
		text = stripWorkingPrefix(text)
		if text != "" {
			events = append(events, AgentEvent{
				Type:    "agent_text",
				Payload: map[string]interface{}{"text": text},
			})
		}
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

	// Build summary from the first agent_text event, or "agent completed".
	summary := "agent completed"
	for _, ev := range events {
		if ev.Type == "agent_text" {
			if t, ok := ev.Payload["text"].(string); ok && t != "" {
				summary = t
				if i := strings.IndexByte(summary, '\n'); i >= 0 {
					summary = summary[:i]
				}
				if len(summary) > 200 {
					summary = summary[:200]
				}
			}
			break
		}
	}
	events = append(events, AgentEvent{
		Type:    "agent_done",
		Payload: map[string]interface{}{"summary": summary},
	})
	return events
}

// stripWorkingPrefix removes the "Working..." status line that OMP emits
// at the start of print-mode output (merged from stderr via 2>&1).
func stripWorkingPrefix(text string) string {
	// "Working..." is the OMP initial status; strip it if it's the only
	// content or if it's a prefix followed by the real output.
	if text == "Working..." || text == "Working…" {
		return ""
	}
	// Strip leading "Working...\n" prefix
	for _, prefix := range []string{"Working...\n", "Working…\n"} {
		if strings.HasPrefix(text, prefix) {
			return strings.TrimSpace(text[len(prefix):])
		}
	}
	return text
}

// parseSessionJSONL reads the OMP session JSONL transcript and extracts
// agent_text, agent_tool_call, and agent_tool_result events from the
// structured message content blocks.
func parseSessionJSONL(r *ompRun) []AgentEvent {
	// Find the JSONL file in the session directory.
	matches, err := filepath.Glob(filepath.Join(r.sessionDir, "*.jsonl"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	// Use the first (and typically only) JSONL file.
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return nil
	}

	var events []AgentEvent
	// Track the last tool name for tool_result attribution.
	lastTool := ""

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// JSONL entries are objects with "message" containing "role" and "content".
		// We use encoding/json for robust parsing.
		var entry struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		// Parse the message object.
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}

		// Process assistant messages (content is an array of blocks).
		if msg.Role == "assistant" {
			var blocks []struct {
				Type       string          `json:"type"`
				Text       string          `json:"text"`
				Thinking   string          `json:"thinking"`
				Name       string          `json:"name"`
				Arguments  json.RawMessage `json:"arguments"`
				ToolCallID string          `json:"id"`
			}
			if err := json.Unmarshal(msg.Content, &blocks); err != nil {
				continue
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						events = append(events, AgentEvent{
							Type:    "agent_text",
							Payload: map[string]interface{}{"text": b.Text},
						})
					}
				case "toolCall":
					args := strings.TrimSpace(string(b.Arguments))
					events = append(events, AgentEvent{
						Type:    "agent_tool_call",
						Payload: map[string]interface{}{"tool": b.Name, "args": args},
					})
					lastTool = b.Name
				}
			}
		}

		// Process toolResult messages.
		if msg.Role == "toolResult" {
			var result struct {
				ToolName string          `json:"toolName"`
				Content  json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(entry.Message, &result); err != nil {
				continue
			}
			// Extract text from content array.
			resultText := ""
			var contentBlocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(result.Content, &contentBlocks); err == nil {
				for _, cb := range contentBlocks {
					if cb.Type == "text" && cb.Text != "" {
						resultText = cb.Text
						break
					}
				}
			}
			tool := result.ToolName
			if tool == "" {
				tool = lastTool
			}
			if tool != "" {
				events = append(events, AgentEvent{
					Type:    "agent_tool_result",
					Payload: map[string]interface{}{"tool": tool, "result": resultText},
				})
			}
		}
	}

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
