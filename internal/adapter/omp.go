// Package adapter — OMP adapter: runs the OMP coding agent in print mode via
// the Hermes timeout wrapper, as a detached subprocess in a run worktree.
//
// M0.1 shape: Start spawns the wrapper (non-blocking), resolving the
// model/provider from ~/.odo/prefs.md; Events polls the process and, once it
// exits, turns the transcript output file into agent_text + agent_tool_call /
// agent_tool_result events parsed from the print-mode output, then agent_done
// (or agent_error). Unparseable output degrades to M0 behavior: agent_text +
// agent_done only.
//
// M7 (live streaming): Start passes --mode json, so the output file carries
// OMP's JSONL event stream instead of print-mode text. While the run is in
// flight, Events tails that stream with a byte-offset cursor, returns
// completed blocks (text_end, tool_execution_end) as journal events, and
// returns the in-flight text/tool progress as a trailing transient preview
// event (partial:true — never journaled). Runs whose output does not start
// with '{' (text stubs) auto-detect to the legacy behavior unchanged.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/yingliang-zhang/odo/internal/modelspec"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

const (
	// Whole-run wall-clock deadline for one OMP agent run (prefs.md
	// omp_timeout still overrides). 600s was sized before thinking models:
	// task-tier normal runs kimi/glm at thinking=max where a single gateway
	// round measured up to 125s, so a 16-round tool run needs ~33min worst
	// case — 600s structurally mid-kills deep-thinking runs. 1800s covers
	// the worst case; omp's own idle watchdog still kills true hangs.
	defaultTimeoutSeconds = "1800"
	maxStderrTail         = 4096

	// Fallback model config when ~/.odo/prefs.md is missing or unparseable.
	defaultModel    = "t9s/kimi-k3"
	defaultProvider = "sudo" // passed to the wrapper as "custom:sudo"
)

// OMP is the M0 Adapter backed by the Hermes OMP wrapper script.
type OMP struct {
	wrapperPath  string
	stateDir     string // <project>/.odo; prompt/session/output files live here
	timeout      string
	prefsKey     string // prefs.md key to read model from ("coding" or "orchestrator")
	prewalkModel string // prefs.md `prewalk_model` — empty = off
	// M19 (loop_implementer): when fixedModel is set, it beats the prefs
	// key — a per-instance model override for loop-spawned runs that never
	// touches what normal sends resolve (V12).
	fixedModel    string
	fixedProvider string

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

	// M7 live-streaming state (--mode json). Guarded by streamMu; Events is
	// the only writer.
	streamMu      sync.Mutex
	streamMode    bool                     // output file begins with '{' → JSONL stream
	streamLegacy  bool                     // output file decisively not JSON → M0–M6 path
	streamOffset  int64                    // bytes of the output file consumed so far
	streamEvents  []AgentEvent             // completed blocks, in stream order
	streamPreview *AgentEvent              // in-flight block preview (partial:true)
	terminalAdded bool                     // agent_done/agent_error appended
	textAcc       map[int]*strings.Builder // text_delta accumulation per content index
	msgStreamed   bool                     // current assistant message produced text_end(s)
	toolAcc       map[string]pendingTool   // in-flight tool calls by call ID (args live on tool_execution_start)
}

// pendingTool carries a tool call's identity from tool_execution_start to
// tool_execution_end (which repeats name + result but NOT args).
type pendingTool struct {
	name   string
	args   string
	intent string
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

// EnrichedEnv returns os.Environ() with a PATH that includes common
// tool locations missing when the daemon is launched from a .app bundle
// (macOS GUI apps get a minimal PATH like /usr/bin:/bin:/usr/sbin:/sbin).
// Adds homebrew, ~/.local/bin, ~/.cargo/bin, ~/.omp/bin, ~/.hermes/node/bin,
// ~/go/bin, and conda (if present) so the wrapper and OMP's child processes
// can find omp, go, node, git, python3, etc.
// Also injects SUDO_CODING_KEY from ~/.zshrc — OMP's models.yml references
// it by env-var name, and the .app launch environment doesn't source shell
// profiles so it's missing.
func EnrichedEnv() []string {
	env := os.Environ()
	home, _ := os.UserHomeDir()
	extraPaths := []string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".cargo", "bin"),
		filepath.Join(home, ".omp", "bin"),
		filepath.Join(home, ".hermes", "node", "bin"),
		filepath.Join(home, "go", "bin"),
	}
	// Add conda/miniconda paths if the directories exist.
	for _, p := range []string{
		"/opt/homebrew/Caskroom/miniconda/base/bin",
		filepath.Join(home, "miniconda3", "bin"),
		filepath.Join(home, "opt", "anaconda3", "bin"),
	} {
		if _, err := os.Stat(p); err == nil {
			extraPaths = append(extraPaths, p)
		}
	}
	found := false
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = e + string(filepath.ListSeparator) + strings.Join(extraPaths, string(filepath.ListSeparator))
			found = true
			break
		}
	}
	if !found {
		env = append(env, "PATH="+strings.Join(extraPaths, string(filepath.ListSeparator)))
	}

	// Inject SUDO_CODING_KEY from ~/.zshrc if not already in the
	// environment. OMP's models.yml uses the env-var name as the apiKey
	// value, and .app-launched daemons don't source shell profiles.
	hasKey := false
	for _, e := range env {
		if strings.HasPrefix(e, "SUDO_CODING_KEY=") {
			hasKey = true
			break
		}
	}
	if !hasKey {
		if key := ExtractExportFromZshrc(home, "SUDO_CODING_KEY"); key != "" {
			env = append(env, "SUDO_CODING_KEY="+key)
		}
	}

	return env
}

// ExtractExportFromZshrc reads ~/.zshrc and extracts the value of an
// `export VAR="value"` line. Returns "" if not found or unreadable.
// This is a lightweight parser — it doesn't source the file, just
// matches the export line. Exported so main.go can use it for daemon env.
func ExtractExportFromZshrc(home, varName string) string {
	data, err := os.ReadFile(filepath.Join(home, ".zshrc"))
	if err != nil {
		return ""
	}
	prefix := "export " + varName + "="
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			val := strings.TrimPrefix(line, prefix)
			// Strip surrounding quotes if present.
			val = strings.Trim(val, `"`)
			val = strings.Trim(val, `'`)
			return val
		}
	}
	return ""
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
		wrapperPath:  wrapper,
		stateDir:     stateDir,
		timeout:      defaultTimeoutSeconds,
		prefsKey:     "coding",
		prewalkModel: LoadPrefsRaw("prewalk_model"),
		runs:         make(map[string]*ompRun),
	}
}

// NewOMPForKey creates an OMP adapter that reads its model config from the
// given prefs.md key (e.g. "orchestrator" for the distill use case).
func NewOMPForKey(stateDir, key string) *OMP {
	o := NewOMP(stateDir)
	o.prefsKey = key
	return o
}

// NewOMPModelOverride creates an OMP adapter pinned to an explicit
// model@provider pair (the prefs.md `loop_implementer:` value for
// loop-spawned runs, M19 V12). The pinned pair wins over every prefs key;
// provider is wrapped in the same custom: scheme resolveModelConfig uses.
func NewOMPModelOverride(stateDir, model, provider string) *OMP {
	o := NewOMP(stateDir)
	o.fixedModel, o.fixedProvider = model, provider
	return o
}

// resolveModelConfig resolves the wrapper's --hermes-model / --hermes-provider
// args. The prefs.md key (e.g. "coding" or "orchestrator") determines which
// model line is read. Falls back to the M0 defaults.
func (a *OMP) resolveModelConfig() (model, providerArg string) {
	model, provider := LoadPrefsByKey(a.prefsKey)
	if a.fixedModel != "" {
		model, provider = a.fixedModel, a.fixedProvider
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
		"--hermes-provider", providerArg,
		"--hermes-model", model,
		"--task-tier", "normal",
		"--session-dir", sessionDir,
		// M7: --mode json makes OMP emit its live JSONL event stream
		// (message_update deltas, tool_execution_* lifecycle events) to
		// stdout, which the wrapper pipes into the output file. Runs whose
		// output does not start with '{' auto-detect back to legacy text
		// parsing, so text-producing stubs are unaffected.
		"--mode", "json",
	}
	args = compactionOverlayArgs(model, sessionDir, args)
	// P0: prewalk opt-in — pass --prewalk flags when a smol model is
	// configured in prefs (`prewalk_model` key). Empty = off.
	args = append(args, prewalkArgs(a.prewalkModel)...)
	cmd := exec.Command(a.wrapperPath, args...)
	cmd.Dir = workdir
	cmd.Env = EnrichedEnv()
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

// compactionOverlayArgs appends a per-model compaction overlay to the wrapper
// args. The policy (compact ratio × context window) is written as a
// fixed-token --config overlay; the Hermes wrapper forwards unknown args
// straight to omp (same mechanism as the profile's SAFE_COMPACTION_OVERLAY).
// Fixed tokens, never thresholdPercent: percent mode recomputes off the
// models.yml contextWindow (auto fallback compacts at 85% → 765K on a 1M
// model, far past the profile's ~315K working set). Unknown models get no
// overlay at all — they keep the global omp config, since a fabricated
// threshold off an unverified window is worse than none. An overlay write
// failure likewise falls back to the global config (fail-open: a chat run
// must never die over a policy hint).
func compactionOverlayArgs(model, sessionDir string, args []string) []string {
	threshold, ok := modelspec.CompactThresholdTokens(model)
	if !ok {
		return args
	}
	overlay := filepath.Join(sessionDir, "odo-compaction.yml")
	// Enable astGrep (P0: free AST search for the agent; OMP default is off).
	content := fmt.Sprintf("compaction:\n  thresholdTokens: %d\n  thresholdPercent: -1\nastGrep:\n  enabled: true\n", threshold)
	if err := os.WriteFile(overlay, []byte(content), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "odo: compaction overlay for %s: %v (falling back to global omp config)\n", model, err)
		return args
	}
	return append(args, "--config="+overlay)
}

// prewalkArgs appends --prewalk flags when a prewalk model is configured.
// The prewalk model is read from prefs (`prewalk_model` key); empty = off.
func prewalkArgs(prewalkModel string) []string {
	if prewalkModel == "" {
		return nil
	}
	return []string{"--prewalk", "--prewalk-into=" + prewalkModel}
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

// Events implements Adapter. Legacy runs: while the run is in flight it
// returns nothing; once the process exits it deterministically rebuilds the
// run's event list (agent_text from the output file, then agent_done or
// agent_error) and returns the tail after afterSeq.
//
// M7 streaming runs (--mode json detected from the output file's first
// byte): Events tails the JSONL stream on every call. Completed blocks
// (text_end payloads, tool_execution_* pairs) are returned as journal
// events; the in-flight block is appended as a trailing transient preview
// event (payload partial:true) that consumers MUST NOT journal — the daemon
// strips it and runOneShot skips it. afterSeq counts journaled events only;
// the preview never advances it.
func (a *OMP) Events(ctx context.Context, runID string, afterSeq int) ([]AgentEvent, error) {
	a.mu.Lock()
	r, ok := a.runs[runID]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("omp: unknown run %q", runID)
	}

	r.streamMu.Lock()
	if !r.streamMode && !r.streamLegacy {
		r.detectStream()
	}

	select {
	case <-r.done:
		if r.streamMode {
			r.tailStream(true) // final drain: catch the last lines
			if len(r.streamEvents) > 0 {
				if !r.terminalAdded {
					r.appendTerminalLocked()
				}
				if afterSeq >= len(r.streamEvents) {
					r.streamMu.Unlock()
					return nil, nil
				}
				out := make([]AgentEvent, len(r.streamEvents)-afterSeq)
				copy(out, r.streamEvents[afterSeq:])
				r.streamMu.Unlock()
				return out, nil
			}
			// The stream parsed to nothing (degenerate input / stderr-only
			// noise): rebuild from the session JSONL or text output exactly
			// like a legacy run.
			r.streamLegacy = true
		}
		r.streamMu.Unlock()

		events := a.buildEvents(r)
		if afterSeq >= len(events) {
			return nil, nil
		}
		return events[afterSeq:], nil
	default:
		if !r.streamMode {
			r.streamMu.Unlock()
			return nil, nil // legacy: nothing until the process exits
		}
		r.tailStream(false)
		out := make([]AgentEvent, 0, len(r.streamEvents)+1)
		if afterSeq < len(r.streamEvents) {
			out = append(out, r.streamEvents[afterSeq:]...)
		}
		if r.streamPreview != nil {
			out = append(out, *r.streamPreview)
		}
		r.streamMu.Unlock()
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	}
}

// detectStream inspects the output file's first byte: '{' selects the M7
// --mode json stream; anything else locks the legacy text path for this
// run. An unreadable or empty file leaves the choice open and is retried on
// the next Events call.
func (r *ompRun) detectStream() {
	f, err := os.Open(r.outputFile)
	if err != nil {
		return
	}
	defer f.Close()
	var b [1]byte
	n, err := f.Read(b[:])
	if err != nil || n == 0 {
		return
	}
	if b[0] == '{' {
		r.streamMode = true
	} else {
		r.streamLegacy = true
	}
}

// tailStream consumes new complete JSONL lines from the output file since
// streamOffset. A trailing partial line (no \n yet) is left for the next
// call; when final is true the process has exited, so an unterminated tail
// is parsed as a complete line (junk fails the JSON parse and is skipped).
func (r *ompRun) tailStream(final bool) {
	f, err := os.Open(r.outputFile)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(r.streamOffset, io.SeekStart); err != nil {
		return // truncated/rotated: retry next call
	}
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return
	}

	end := len(data)
	var trailing []byte
	lastNL := bytes.LastIndexByte(data, '\n')
	switch {
	case lastNL == len(data)-1:
		// Ends with a newline: every line is complete.
	case lastNL >= 0:
		end = lastNL + 1
		trailing = data[end:]
	default:
		// No newline at all: partial line only.
		end = 0
		trailing = data
	}
	for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
		r.streamLine(line)
	}
	if final && len(trailing) > 0 {
		r.streamLine(trailing)
		end = len(data)
	}
	r.streamOffset += int64(end)
}

// streamLine parses one line of OMP's --mode json stream and mutates the
// run's journaled-blocks list and transient preview. Only three things
// journal: finished text blocks (text_end), finished tool executions
// (tool_execution_end, emitting call + result), and — as a safety net for
// providers that skip message_update deltas — whole assistant messages at
// message_end when the message streamed nothing. Non-JSON lines (stderr
// noise the wrapper merged via 2>&1, timeout diagnostics appended after the
// stream) fail the parse and are skipped.
func (r *ompRun) streamLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Update *struct {
			Type         string `json:"type"`
			ContentIndex int    `json:"contentIndex"`
			Delta        string `json:"delta"`
			Content      string `json:"content"`
		} `json:"assistantMessageEvent"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Args       json.RawMessage `json:"args"`
		Intent     string          `json:"intent"`
		Result     *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}

	switch ev.Type {
	case "message_update":
		u := ev.Update
		if u == nil {
			return
		}
		switch u.Type {
		case "text_start":
			if r.textAcc == nil {
				r.textAcc = make(map[int]*strings.Builder)
			}
			r.textAcc[u.ContentIndex] = &strings.Builder{}
		case "text_delta":
			b := r.textAcc[u.ContentIndex]
			if b == nil {
				return // delta without text_start: not ours to show
			}
			b.WriteString(u.Delta)
			r.streamPreview = &AgentEvent{
				Type:    "agent_text",
				Payload: map[string]interface{}{"text": b.String(), "partial": true},
			}
		case "text_end":
			text := u.Content
			if text == "" {
				if b := r.textAcc[u.ContentIndex]; b != nil {
					text = b.String()
				}
			}
			delete(r.textAcc, u.ContentIndex)
			if text != "" {
				r.streamEvents = append(r.streamEvents, AgentEvent{
					Type:    "agent_text",
					Payload: map[string]interface{}{"text": text},
				})
				r.msgStreamed = true
			}
			r.streamPreview = nil
		}
	case "message_start":
		// A new assistant message: the safety-net journal rule applies to
		// the new message only.
		if ev.Message != nil && ev.Message.Role == "assistant" {
			r.msgStreamed = false
		}
	case "message_end":
		// Safety net: a complete assistant text block that never streamed
		// deltas (non-streaming provider, instant reply) journals here.
		// Thinking blocks are always journaled here regardless of
		// msgStreamed — they never appear in streaming delta events.
		if ev.Message == nil || ev.Message.Role != "assistant" || len(ev.Message.Content) == 0 {
			return
		}
		var blocks []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		}
		if err := json.Unmarshal(ev.Message.Content, &blocks); err != nil {
			return
		}
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" && !r.msgStreamed {
				r.streamEvents = append(r.streamEvents, AgentEvent{
					Type:    "agent_text",
					Payload: map[string]interface{}{"text": b.Text},
				})
			}
			if b.Type == "thinking" && b.Thinking != "" {
				r.streamEvents = append(r.streamEvents, AgentEvent{
					Type:    "agent_thinking",
					Payload: map[string]interface{}{"text": b.Thinking},
				})
			}
		}
	case "tool_execution_start":
		if r.toolAcc == nil {
			r.toolAcc = make(map[string]pendingTool)
		}
		r.toolAcc[ev.ToolCallID] = pendingTool{
			name:   ev.ToolName,
			args:   strings.TrimSpace(string(ev.Args)),
			intent: ev.Intent,
		}
		r.streamPreview = &AgentEvent{
			Type: "agent_tool_call",
			Payload: map[string]interface{}{
				"tool":    ev.ToolName,
				"call_id": ev.ToolCallID,
				"intent":  ev.Intent,
				"partial": true,
			},
		}
	case "tool_execution_end":
		// tool_execution_end carries name + result but NOT args; the start
		// event supplied them. Merge, preferring what the start stashed.
		pending := r.toolAcc[ev.ToolCallID]
		delete(r.toolAcc, ev.ToolCallID)
		name := pending.name
		if name == "" {
			name = ev.ToolName
		}
		args := pending.args
		if args == "" && len(ev.Args) > 0 {
			args = strings.TrimSpace(string(ev.Args))
		}
		result := ""
		if ev.Result != nil {
			for _, c := range ev.Result.Content {
				if c.Type == "text" && c.Text != "" {
					result = c.Text
					break
				}
			}
		}
		// Payload keys match ADR-0002 exactly (tool/args, tool/result) —
		// journaled blocks are indistinguishable from M0–M6 events.
		r.streamEvents = append(r.streamEvents,
			AgentEvent{
				Type:    "agent_tool_call",
				Payload: map[string]interface{}{"tool": name, "args": args},
			},
			AgentEvent{
				Type:    "agent_tool_result",
				Payload: map[string]interface{}{"tool": name, "result": result},
			},
		)
		r.streamPreview = nil
	}
}

// appendTerminalLocked appends the run's terminal event to the streamed
// blocks once (streamMu held). The summary/error shapes mirror buildEvents.
func (r *ompRun) appendTerminalLocked() {
	r.terminalAdded = true
	r.streamPreview = nil
	if r.err != nil {
		r.streamEvents = append(r.streamEvents, agentErrorEvent(r))
		return
	}
	r.streamEvents = append(r.streamEvents, AgentEvent{
		Type:    "agent_done",
		Payload: map[string]interface{}{"summary": doneSummary(r.streamEvents)},
	})
}

// doneSummary builds the agent_done summary from the first agent_text in
// events, or "agent completed" when there is none.
func doneSummary(events []AgentEvent) string {
	for _, ev := range events {
		if ev.Type != "agent_text" {
			continue
		}
		if t, ok := ev.Payload["text"].(string); ok && t != "" {
			summary := t
			if i := strings.IndexByte(summary, '\n'); i >= 0 {
				summary = summary[:i]
			}
			if len(summary) > 200 {
				summary = summary[:200]
			}
			return summary
		}
	}
	return "agent completed"
}

// agentErrorEvent builds the terminal agent_error event from the process
// error plus the captured stderr tail.
func agentErrorEvent(r *ompRun) AgentEvent {
	msg := fmt.Sprintf("agent failed: %v", r.err)
	if tail := r.stderr.String(); tail != "" {
		msg = fmt.Sprintf("%s: %s", msg, tail)
	}
	return AgentEvent{
		Type:    "agent_error",
		Payload: map[string]interface{}{"error": msg},
	}
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
		events = append(events, agentErrorEvent(r))
		return events
	}

	events = append(events, AgentEvent{
		Type:    "agent_done",
		Payload: map[string]interface{}{"summary": doneSummary(events)},
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
				case "thinking":
					if b.Thinking != "" {
						events = append(events, AgentEvent{
							Type:    "agent_thinking",
							Payload: map[string]interface{}{"text": b.Thinking},
						})
					}
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

// Usage is one OMP run's measured token ledger (D3, the 2026-08-27
// control-plane hardening lock): the sum over every assistant-message
// `usage` block in the run's session JSONL — the REAL executor cost the
// loop budget constrains, replacing the spawn-row chars/4 estimate.
// SpentTokens (= input+output+cache_write) is the budgeted quantity;
// CacheReadTokens rides the receipt but is never budgeted (a cache hit
// is near-free work already billed at input time). CostUSD is Σ
// cost.total rounded to 6dp. Keys match the journaled receipt payload
// one-to-one so the ipc layer merges it directly.
type Usage struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	SpentTokens      int     `json:"spent_tokens"`
}

// SessionUsage measures a finished run's real token cost by summing the
// per-assistant-message usage blocks across the session dir's *.jsonl
// transcripts. LLM-free and deterministic. The shape is
// live-transcript-verified (omp --mode json sessions):
//
//	message.usage = {input, output, cacheRead, cacheWrite, totalTokens,
//	                 cost{input, output, cacheRead, cacheWrite, total}}
//
// ok=false means no measurement exists — missing dir, no *.jsonl, or no
// usage records (a non-JSON-mode run, a crashed stub). Fail-soft: the
// caller journals usage_available:false and NEVER fabricates numbers.
func SessionUsage(sessionDir string) (Usage, bool) {
	matches, err := filepath.Glob(filepath.Join(sessionDir, "*.jsonl"))
	if err != nil || len(matches) == 0 {
		return Usage{}, false
	}
	var u Usage
	found := false
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !strings.Contains(line, "\"usage\"") {
				continue
			}
			var entry struct {
				Message struct {
					Role  string `json:"role"`
					Usage *struct {
						Input       int `json:"input"`
						Output      int `json:"output"`
						CacheRead   int `json:"cacheRead"`
						CacheWrite  int `json:"cacheWrite"`
						TotalTokens int `json:"totalTokens"`
						Cost        struct {
							Total float64 `json:"total"`
						} `json:"cost"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				continue
			}
			mu := entry.Message.Usage
			if entry.Message.Role != "assistant" || mu == nil {
				continue
			}
			found = true
			u.InputTokens += mu.Input
			u.OutputTokens += mu.Output
			u.CacheReadTokens += mu.CacheRead
			u.CacheWriteTokens += mu.CacheWrite
			u.CostUSD += mu.Cost.Total
		}
	}
	if !found {
		return Usage{}, false
	}
	// 6dp: below one-millionth of a dollar the float noise is meaningless.
	u.CostUSD = math.Round(u.CostUSD*1e6) / 1e6
	u.SpentTokens = u.InputTokens + u.OutputTokens + u.CacheWriteTokens
	return u, true
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
