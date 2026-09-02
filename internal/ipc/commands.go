package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
)

// Odo DX wave — Run/Test hub: .odo/commands.json registers named shell
// commands (test suites, builds, lint) the Runs tab executes on click.
// The commands are the project's own user-authored config — the same
// trust class as .odo-verify's verify line, executed via sh -c so pipes
// and redirects compose (runVerify's posture). Unlike k8s_status this IS
// journaled: a command outcome is a run artifact (the conversation's
// history), not a cluster-state poll — the row still debits nothing.
//
// Schema (version 1):
//   {"version": 1, "commands": [
//     {"name": "tests", "cmd": "go test ./...", "cwd": ".", "timeout": 120}
//   ]}

// commandsFileName is the hub's config file. The path is daemon-
// constructed (<root>/.odo/<name>) — never request-derived.
const commandsFileName = "commands.json"

// commandTailCap bounds each captured output stream (stdout/stderr) in the
// journaled payload and the IPC response. The bound holds AT CAPTURE (the
// tailWriter slides the front off as bytes arrive) — a flooding command
// can never grow a daemon buffer past the cap.
const commandTailCap = 2048

// commandKillDrain bounds the post-kill wait: after the group kill lands,
// a grandchild that somehow missed it (or a lagging pipe teardown) can no
// longer wedge the handler past this delay — Wait returns, the pipe copy
// stays bounded, and the journaled timeout row is never hostage to a
// leaked child holding a write fd.
const commandKillDrain = time.Second

// Per-command timeout policy (seconds in the schema): 0/absent takes the
// default; anything longer is clamped to the ceiling so a hand-edited
// 999999-second row can't pin an IPC connection effectively forever.
const (
	commandDefaultTimeout = 120 * time.Second
	commandMaxTimeout     = 600 * time.Second
)

// commandSpec is one registered command (validated).
type commandSpec struct {
	Name    string `json:"name"`
	Cmd     string `json:"cmd"`
	Cwd     string `json:"cwd,omitempty"`
	Timeout int    `json:"timeout,omitempty"` // seconds
}

// commandsConfig is .odo/commands.json's top-level shape.
type commandsConfig struct {
	Version  int           `json:"version"`
	Commands []commandSpec `json:"commands"`
}

// tailWriter is a bounded tail buffer: Write appends, and bytes beyond the
// cap slide off the FRONT, so String holds the last `cap` bytes written.
// Bounded at capture — never an unbounded CombinedOutput sliced later.
type tailWriter struct {
	buf []byte
	cap int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - w.cap; over > 0 {
		copy(w.buf, w.buf[over:])
		w.buf = w.buf[:len(w.buf)-over]
	}
	return len(p), nil
}

func (w *tailWriter) String() string { return string(w.buf) }

// loadCommands reads <root>/.odo/commands.json and validates schema
// version 1 fail-loud. A missing file, malformed JSON, a bad version, and
// per-entry defects each refuse as a named, distinct error — the Runs tab
// surfaces the string to the user who owns the file.
func loadCommands(root string) ([]commandSpec, error) {
	raw, err := os.ReadFile(filepath.Join(root, ".odo", commandsFileName))
	if err != nil {
		return nil, fmt.Errorf("read .odo/%s: %w", commandsFileName, err)
	}
	var cfg commandsConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", commandsFileName, err)
	}
	if cfg.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported version %d (want version 1)", commandsFileName, cfg.Version)
	}
	seen := make(map[string]bool, len(cfg.Commands))
	for i := range cfg.Commands {
		c := &cfg.Commands[i]
		if strings.TrimSpace(c.Name) == "" {
			return nil, fmt.Errorf("%s: command %d has an empty name", commandsFileName, i)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("%s: duplicate command name %q", commandsFileName, c.Name)
		}
		seen[c.Name] = true
		if strings.TrimSpace(c.Cmd) == "" {
			return nil, fmt.Errorf("%s: command %q has an empty cmd", commandsFileName, c.Name)
		}
	}
	return cfg.Commands, nil
}

// resolveCommandCwd confines a command's working directory to the project
// tree: "." (the root) is the default and common case; absolute paths and
// ".." escapes are refused (the write-side sibling of read_file's
// containment — an escaped cwd would run user config against foreign
// trees under the daemon's credentials).
func resolveCommandCwd(root, cwd string) (string, error) {
	if cwd == "" || cwd == "." {
		return root, nil
	}
	if filepath.IsAbs(cwd) {
		return "", fmt.Errorf("cwd %q must be project-relative", cwd)
	}
	joined := filepath.Join(root, cwd)
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("cwd %q escapes the project root", cwd)
	}
	return joined, nil
}

// handleRunCommand executes the named .odo/commands.json command and
// journals the outcome. Request: ConversationID (the journal's target
// lane) + Name. Validation/lookup failures are plain IPC errors and
// journal nothing (no run happened); an EXECUTED command journals its
// result regardless of the verdict (exit 0 or not) — that row is the
// artifact the Runs tab folds.
func (s *Server) handleRunCommand(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("run_command: %w", err)
	}
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	specs, err := loadCommands(s.projectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("run_command: %w", err)
	}
	var spec *commandSpec
	for i := range specs {
		if specs[i].Name == req.Name {
			spec = &specs[i]
			break
		}
	}
	if spec == nil {
		return Response{}, fmt.Errorf("run_command: no command named %q in .odo/%s", req.Name, commandsFileName)
	}
	dir, err := resolveCommandCwd(s.projectRoot, spec.Cwd)
	if err != nil {
		return Response{}, fmt.Errorf("run_command: %q: %w", spec.Name, err)
	}
	timeout := time.Duration(spec.Timeout) * time.Second
	if timeout <= 0 {
		timeout = commandDefaultTimeout
	}
	if timeout > commandMaxTimeout {
		timeout = commandMaxTimeout
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	proc := exec.CommandContext(cctx, "sh", "-c", spec.Cmd)
	proc.Dir = dir
	// Process-group kill (quad-audit P2, preview.go's per-shot lifecycle
	// guarantee): sh -c is only the WRAPPER — a command that backgrounds a
	// dev server or watcher otherwise loses its grandchildren at the
	// deadline (the child's Kill reaches the shell only), and the survivors
	// hold the stdout/stderr pipe write-ends open, wedging the io copy past
	// the 600s clamp. Setpgid + a Cancel that kills the NEGATED pid retires
	// the whole group; WaitDelay bounds the final teardown.
	proc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	proc.Cancel = func() error {
		return syscall.Kill(-proc.Process.Pid, syscall.SIGKILL)
	}
	proc.WaitDelay = commandKillDrain
	// EnrichedEnv: the same PATH/gateway reach the agent's shell tools see
	// — a Finder-launched daemon otherwise runs commands against a
	// environment no login shell ever produced (k8sExec's A2-2 lesson).
	proc.Env = adapter.EnrichedEnv()
	stdout := &tailWriter{cap: commandTailCap}
	stderr := &tailWriter{cap: commandTailCap}
	proc.Stdout = stdout
	proc.Stderr = stderr
	start := time.Now()
	runErr := proc.Run()
	duration := time.Since(start).Milliseconds()

	result := CommandResult{
		Name:       spec.Name,
		StdoutTail: stdout.String(),
		StderrTail: stderr.String(),
		DurationMs: duration,
	}
	timedOut := cctx.Err() == context.DeadlineExceeded
	switch {
	case timedOut:
		result.ExitCode = -1
		result.TimedOut = true
	case runErr != nil:
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			// The process never ran (e.g. no /bin/sh) — a transport
			// failure, not a command verdict: no artifact to journal.
			return Response{}, fmt.Errorf("run_command: %q: %w", spec.Name, runErr)
		}
		result.ExitCode = exitErr.ExitCode()
	}

	// Journal the artifact (the run-history complement of the never-
	// journaled k8s pollers). Best-effort: a wedged journal must not eat
	// the answer the GUI is waiting for (journalAuto's posture).
	if _, jerr := s.store.AppendEvent(ctx, c.ID, store.EventCommandResult, mustJSON(map[string]interface{}{
		"name":        result.Name,
		"exit_code":   result.ExitCode,
		"stdout_tail": result.StdoutTail,
		"stderr_tail": result.StderrTail,
		"duration_ms": result.DurationMs,
		"timed_out":   result.TimedOut,
	})); jerr != nil {
		log.Printf("run_command: journal %q result (conversation %d): %v", spec.Name, c.ID, jerr)
	}
	return Response{CommandResult: &result}, nil
}
