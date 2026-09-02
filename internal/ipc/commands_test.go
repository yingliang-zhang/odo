package ipc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Odo DX wave — run_command (Run/Test hub) coverage: config validation
// refusals (missing/malformed/version/dup/empty), the happy-path exec +
// journaled command_result artifact, non-zero exits, the timeout row, and
// cwd containment.

// writeCommandsJSON installs a .odo/commands.json fixture in the rig root.
func writeCommandsJSON(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".odo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, commandsFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// commandResultPayloads decodes the conversation's command_result rows.
func commandResultPayloads(t *testing.T, rig *testRig, convID int64) []map[string]interface{} {
	t.Helper()
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	var out []map[string]interface{}
	for _, ev := range events {
		if ev.Type != "command_result" {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("command_result payload: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// commandsRig boots the shared fixture: repo + daemon + conversation.
func commandsRig(t *testing.T) (*testRig, string, int64) {
	t.Helper()
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	t.Cleanup(func() { rig.stop(t) })
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	return rig, root, boot.Conversation.ID
}

func TestRunCommandValidation(t *testing.T) {
	rig, root, convID := commandsRig(t)

	// Missing file refuses by name and journals nothing.
	resp := rig.callExpectErr(t, Request{Cmd: CmdRunCommand, ProjectRoot: root, ConversationID: convID, Name: "tests"})
	if !strings.Contains(resp.Error, commandsFileName) {
		t.Errorf("missing file error = %q, want the filename", resp.Error)
	}
	if got := commandResultPayloads(t, rig, convID); len(got) != 0 {
		t.Errorf("journaled rows after missing-file refusal = %v, want none", got)
	}

	cases := []struct {
		name string
		body string
		want string
	}{
		{"malformed json", `{`, "not valid JSON"},
		{"bad version", `{"version": 2, "commands": []}`, "unsupported version"},
		{"empty version", `{}`, "unsupported version"},
		{"empty name", `{"version": 1, "commands": [{"cmd": "true"}]}`, "empty name"},
		{"duplicate name", `{"version": 1, "commands": [{"name": "a", "cmd": "true"}, {"name": "a", "cmd": "false"}]}`, "duplicate command name"},
		{"empty cmd", `{"version": 1, "commands": [{"name": "a"}]}`, "empty cmd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeCommandsJSON(t, root, tc.body)
			resp := rig.callExpectErr(t, Request{Cmd: CmdRunCommand, ProjectRoot: root, ConversationID: convID, Name: "a"})
			if !strings.Contains(resp.Error, tc.want) {
				t.Errorf("error = %q, want %q", resp.Error, tc.want)
			}
		})
	}

	// Unknown name against a VALID config refuses without an exec.
	writeCommandsJSON(t, root, `{"version": 1, "commands": [{"name": "tests", "cmd": "true"}]}`)
	resp = rig.callExpectErr(t, Request{Cmd: CmdRunCommand, ProjectRoot: root, ConversationID: convID, Name: "nope"})
	if !strings.Contains(resp.Error, "no command named") {
		t.Errorf("unknown-name error = %q, want the lookup refusal", resp.Error)
	}
	if got := commandResultPayloads(t, rig, convID); len(got) != 0 {
		t.Errorf("journaled rows after validation refusals = %v, want none", got)
	}
}

func TestRunCommandHappyAndRedPaths(t *testing.T) {
	rig, root, convID := commandsRig(t)
	writeCommandsJSON(t, root, `{"version": 1, "commands": [
		{"name": "echo", "cmd": "echo hello from hub"},
		{"name": "red", "cmd": "echo oops >&2 && exit 3"},
		{"name": "flood", "cmd": "python3 -c \"print('x'*10000)\" || printf 'x%.0s' {1..10000}"}
	]}`)

	// Green: exit 0 with the stdout tail returned AND journaled.
	green := rig.call(t, Request{Cmd: CmdRunCommand, ProjectRoot: root, ConversationID: convID, Name: "echo"})
	res := green.CommandResult
	if res == nil || res.ExitCode != 0 || !strings.Contains(res.StdoutTail, "hello from hub") || res.TimedOut {
		t.Fatalf("green result = %+v, want exit 0 with the echo tail", res)
	}

	// Red: non-zero exit journals the failure with the stderr tail — the
	// artifact exists BECAUSE failures are run history.
	red := rig.call(t, Request{Cmd: CmdRunCommand, ProjectRoot: root, ConversationID: convID, Name: "red"})
	if red.CommandResult == nil || red.CommandResult.ExitCode != 3 || !strings.Contains(red.CommandResult.StderrTail, "oops") {
		t.Fatalf("red result = %+v, want exit 3 with the stderr tail", red.CommandResult)
	}

	// Tails are bounded at capture: 10K written, ≤cap journaled, and the
	// kept window is the TAIL (the flood is all one byte — length proves it).
	floodRes := rig.call(t, Request{Cmd: CmdRunCommand, ProjectRoot: root, ConversationID: convID, Name: "flood"}).CommandResult
	if floodRes == nil || floodRes.ExitCode != 0 {
		t.Fatalf("flood result = %+v, want exit 0", floodRes)
	}
	if got := len(floodRes.StdoutTail); got > commandTailCap+1 {
		t.Errorf("stdout tail = %d bytes, want ≤%d (+trailing newline)", got, commandTailCap)
	}

	rows := commandResultPayloads(t, rig, convID)
	if len(rows) != 3 {
		t.Fatalf("journaled command_result rows = %d, want 3 (one per executed command)", len(rows))
	}
	first := rows[0]
	if first["name"] != "echo" || first["exit_code"] != float64(0) {
		t.Errorf("first row = %v, want echo exit 0", first)
	}
	if !strings.Contains(first["stdout_tail"].(string), "hello from hub") {
		t.Errorf("first row stdout_tail = %v, want the echo text", first["stdout_tail"])
	}
	if _, ok := first["duration_ms"].(float64); !ok {
		t.Errorf("first row duration_ms missing: %v", first)
	}
	second := rows[1]
	if second["name"] != "red" || second["exit_code"] != float64(3) {
		t.Errorf("second row = %v, want red exit 3", second)
	}
}

func TestRunCommandTimeout(t *testing.T) {
	rig, root, convID := commandsRig(t)
	writeCommandsJSON(t, root, `{"version": 1, "commands": [
		{"name": "slow", "cmd": "sleep 30", "timeout": 1}
	]}`)
	res := rig.call(t, Request{Cmd: CmdRunCommand, ProjectRoot: root, ConversationID: convID, Name: "slow"}).CommandResult
	if res == nil || !res.TimedOut || res.ExitCode != -1 {
		t.Fatalf("timeout result = %+v, want exit -1 with timed_out", res)
	}
	rows := commandResultPayloads(t, rig, convID)
	if len(rows) != 1 || rows[0]["timed_out"] != true {
		t.Fatalf("journaled timeout row = %v, want one timed_out true", rows)
	}
}

func TestRunCommandCwd(t *testing.T) {
	rig, root, convID := commandsRig(t)
	sub := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandsJSON(t, root, `{"version": 1, "commands": [
		{"name": "sub", "cmd": "pwd", "cwd": "sub/dir"},
		{"name": "escape", "cmd": "true", "cwd": ".."},
		{"name": "absolute", "cmd": "true", "cwd": "/tmp"}
	]}`)

	posixRoot := filepath.ToSlash(root)
	res := rig.call(t, Request{Cmd: CmdRunCommand, ProjectRoot: root, ConversationID: convID, Name: "sub"}).CommandResult
	if res == nil || res.ExitCode != 0 || !strings.Contains(res.StdoutTail, posixRoot+"/sub/dir") {
		t.Errorf("sub cwd result = %+v, want pwd under %s/sub/dir", res, posixRoot)
	}

	for _, name := range []string{"escape", "absolute"} {
		resp := rig.callExpectErr(t, Request{Cmd: CmdRunCommand, ProjectRoot: root, ConversationID: convID, Name: name})
		if !strings.Contains(resp.Error, "cwd") {
			t.Errorf("%s cwd error = %q, want the containment refusal", name, resp.Error)
		}
	}
	// Containment refusals happen pre-exec: only the successful run journals.
	if rows := commandResultPayloads(t, rig, convID); len(rows) != 1 || rows[0]["name"] != "sub" {
		t.Errorf("journaled rows = %v, want just the sub row", rows)
	}
}
