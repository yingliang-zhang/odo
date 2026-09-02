package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo/internal/ipc"
)

// P1 borrow #7 (`odo spawn`, 2026-09-03): the AGENT-FACING bridge to the
// daemon's spawn_subagent IPC. An OMP agent running in a worktree shells
// out `odo spawn --goal "<task>" [--context-file <path>]`; the daemon
// starts an isolated child in its own "sub-" worktree, journals the whole
// run back into the parent conversation, and (never auto-lands) the
// extracted diff as a proposal. Unlike `odo todo`/`odo journal`, spawn
// NEEDS the live daemon (adapter start is in-process), so the transport
// is the project socket — resolved from the caller's git context:
//
//	odo spawn --goal "<task>" [--context-file <path>] [--conversation N] [--worktree <path>]
//
//   - Socket: ODO_SOCKET → the repo's git-common-dir → walk-up for
//     .odo/odo.sock. cwd inside a run worktree hits rule 2 (git common
//     dir names the MAIN checkout, whose .odo/odo.sock is the owning
//     daemon).
//   - Conversation: --conversation N, else ODO_CONVERSATION_ID, else the
//     daemon derives it from --worktree (default: cwd) against its live
//     run table.
//   - Recursion: the daemon marks every subagent worktree's GIT-DIR
//     with `odo_subagent`; spawning from beneath one passes the marker
//     through and the handler refuses (one level of isolation only).

const spawnUsage = `usage: odo spawn --goal "<task>" [--context-file <path>] [--conversation N] [--worktree <path>]
  --goal            the subagent's task (required)
  --context-file    file whose body prepends as the prompt's Context section
  --conversation    parent conversation id (default: derived from --worktree)
  --worktree        caller worktree for conversation derivation (default: cwd)
  Result prints one JSON line: the admitted subagent id, run id, worktree.`

// runSpawnCLI dispatches `odo spawn`.
func runSpawnCLI(args []string) int {
	goal, contextFile := "", ""
	conversation := int64(0)
	worktreePath := ""
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case args[i] == "--goal":
			goal = next()
		case strings.HasPrefix(args[i], "--goal="):
			goal = strings.TrimPrefix(args[i], "--goal=")
		case args[i] == "--context-file":
			contextFile = next()
		case strings.HasPrefix(args[i], "--context-file="):
			contextFile = strings.TrimPrefix(args[i], "--context-file=")
		case args[i] == "--conversation":
			fmt.Sscan(next(), &conversation)
		case strings.HasPrefix(args[i], "--conversation="):
			fmt.Sscan(strings.TrimPrefix(args[i], "--conversation="), &conversation)
		case args[i] == "--worktree":
			worktreePath = next()
		case strings.HasPrefix(args[i], "--worktree="):
			worktreePath = strings.TrimPrefix(args[i], "--worktree=")
		default:
			fmt.Fprintf(os.Stderr, "odo spawn: unknown argument %q\n%s\n", args[i], spawnUsage)
			return 2
		}
	}
	if strings.TrimSpace(goal) == "" {
		fmt.Fprintln(os.Stderr, spawnUsage)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo spawn: getwd: %v\n", err)
		return 1
	}
	if worktreePath == "" {
		worktreePath = cwd
	}

	ctx := context.Background()
	req := ipc.Request{Cmd: ipc.CmdSpawnSubagent, Goal: goal, Path: worktreePath}
	if conversation != 0 {
		req.ConversationID = conversation
	} else if v := os.Getenv("ODO_CONVERSATION_ID"); v != "" {
		fmt.Sscan(v, &req.ConversationID)
	}
	if contextFile != "" {
		body, err := os.ReadFile(contextFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "odo spawn: read context file: %v\n", err)
			return 1
		}
		req.Context = string(body)
	}
	// Recursion marker: the daemon tagged this worktree's git dir when it
	// spawned us (fs-truth, not env). Pass it through; the handler owns
	// the refusal.
	if id, err := readSpawnMarker(ctx, worktreePath); err == nil && id != "" {
		req.SubagentID = id
	}

	sock, err := resolveDaemonSocket(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo spawn: %v\n", err)
		return 1
	}
	resp, err := spawnRoundTrip(sock, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo spawn: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "odo spawn: daemon refused: %s\n", resp.Error)
		return 1
	}
	if resp.Subagent == nil {
		fmt.Fprintln(os.Stderr, "odo spawn: daemon answered ok without a subagent row")
		return 1
	}
	out, _ := json.Marshal(resp.Subagent)
	fmt.Println(string(out))
	fmt.Fprintf(os.Stderr, "odo spawn: %s admitted (worktree %s)\n", resp.Subagent.SubagentID, resp.Subagent.WorktreePath)
	return 0
}

// readSpawnMarker reads <gitdir>/odo_subagent for the caller worktree —
// "" when unmarked (a normal run worktree or checkout).
func readSpawnMarker(ctx context.Context, worktreePath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "rev-parse", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-dir: %w", err)
	}
	gitDir := strings.TrimSpace(string(out))
	body, err := os.ReadFile(filepath.Join(gitDir, "odo_subagent"))
	if err != nil {
		return "", nil // unmarked worktree: not a subagent tree
	}
	return strings.TrimSpace(string(body)), nil
}

// resolveDaemonSocket finds the owning daemon's socket: ODO_SOCKET → the
// git common dir's parent (the MAIN checkout) → walk-up for .odo/odo.sock.
func resolveDaemonSocket(cwd string) (string, error) {
	if sock := os.Getenv("ODO_SOCKET"); sock != "" {
		return sock, nil
	}
	cmd := exec.CommandContext(context.Background(), "git", "-C", cwd, "rev-parse", "--git-common-dir")
	if out, err := cmd.Output(); err == nil {
		commonDir := strings.TrimSpace(string(out))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(cwd, commonDir)
		}
		mainRoot := filepath.Dir(strings.TrimSuffix(commonDir, string(filepath.Separator)))
		cand := filepath.Join(mainRoot, ".odo", "odo.sock")
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	// Walk up for a .odo/odo.sock (monorepo layouts without a resolvable
	// common dir hit this fallback).
	for dir := cwd; ; dir = filepath.Dir(dir) {
		cand := filepath.Join(dir, ".odo", "odo.sock")
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
	}
	return "", fmt.Errorf("daemon socket not found (ODO_SOCKET unset, no .odo/odo.sock above %s) — is odo running for this project?", cwd)
}

// spawnRoundTrip is the one-request socket client (the GUI bridge's Go
// mirror): dial, one JSON line request, one JSON line response.
func spawnRoundTrip(socket string, req ipc.Request) (ipc.Response, error) {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return ipc.Response{}, fmt.Errorf("dial %s: %w", socket, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return ipc.Response{}, fmt.Errorf("send: %w", err)
	}
	var resp ipc.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return ipc.Response{}, fmt.Errorf("recv: %w", err)
	}
	return resp, nil
}
