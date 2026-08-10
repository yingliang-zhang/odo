package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// M12 (D-todo): `odo todo` renders the journaled plan read-only — straight
// from the journal, no derived file, no daemon (the batch's "journal only,
// no file dependency" requirement: this test never writes one).

// seedTodoJournal builds the plan fixture: an epoch-1 merge (t1 open,
// t2 open), a fold, then an epoch-2 merge where t2 closed and the fold
// swept nothing open. The store is CLOSED before the CLI runs so the
// read-only query path is exercised (seedJournal's discipline).
func seedTodoJournal(t *testing.T, root string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	append := func(typ, payload string) {
		t.Helper()
		if _, err := st.AppendEvent(ctx, c.ID, typ, payload); err != nil {
			t.Fatal(err)
		}
	}
	append(store.EventUserMessage, `{"text":"seed"}`)
	append(store.EventReviewAction, `{"action":"todo_merge","origin":"agent","ops_applied":2,"ops_rejected":[],"snapshot":[
		{"id":"t1","text":"open survivor","status":"open","origin_seq":2,"updated_seq":2},
		{"id":"t2","text":"closes before fold","status":"open","origin_seq":2,"updated_seq":2}
	],"snapshot_sha":"x"}`)
	append(store.EventReviewAction, `{"action":"distill","epoch":2,"first_seq":1,"last_seq":3,"note_sha":"abc"}`)
	append(store.EventReviewAction, `{"action":"todo_merge","origin":"user","ops_applied":1,"ops_rejected":[],"snapshot":[
		{"id":"t1","text":"open survivor","status":"open","origin_seq":2,"updated_seq":2},
		{"id":"t2","text":"closes before fold","status":"done","origin_seq":2,"updated_seq":3},
		{"id":"t3","text":"done this epoch","status":"done","origin_seq":5,"updated_seq":5}
	],"snapshot_sha":"y"}`)
	append(store.EventUserMessage, `{"text":"after"}`)
}

// journaledTodoLine parses one JSONL stdout line.
func journaledTodoLine(t *testing.T, line string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("bad JSONL line %q: %v", line, err)
	}
	return m
}

func TestTodoCLIRendersFromJournal(t *testing.T) {
	root := t.TempDir()
	seedTodoJournal(t, root)
	t.Chdir(root)

	stdout, stderr, code := captureCLI(t, func() int {
		return runTodoCLI(nil)
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	lines := nonEmptyLines(stdout)
	if len(lines) != 2 {
		t.Fatalf("visible item lines = %d, want 2 (t1 open, t3 done this epoch): %q", len(lines), stdout)
	}
	first := journaledTodoLine(t, lines[0])
	if first["id"] != "t1" || first["status"] != "open" || first["swept"] != false {
		t.Errorf("line 1 = %v, want open t1", first)
	}
	second := journaledTodoLine(t, lines[1])
	if second["id"] != "t3" || second["status"] != "done" {
		t.Errorf("line 2 = %v, want done t3", second)
	}
	// t2 swept with the fold: not in the default render, hinted on stderr.
	if strings.Contains(stdout, "t2") {
		t.Errorf("swept t2 leaked into the default render: %q", stdout)
	}
	if !strings.Contains(stderr, "1 open") || !strings.Contains(stderr, "1 done/struck this epoch") || !strings.Contains(stderr, "1 swept") {
		t.Errorf("stderr summary = %q, want open/closed/swept counts", stderr)
	}

	// --all renders the swept tail.
	stdout2, stderr2, code2 := captureCLI(t, func() int {
		return runTodoCLI([]string{"--all"})
	})
	if code2 != 0 {
		t.Fatalf("--all: exit %d, stderr %q", code2, stderr2)
	}
	lines2 := nonEmptyLines(stdout2)
	if len(lines2) != 3 {
		t.Fatalf("--all lines = %d, want 3: %q", len(lines2), stdout2)
	}
	sweptLine := journaledTodoLine(t, lines2[2])
	if sweptLine["id"] != "t2" || sweptLine["swept"] != true {
		t.Errorf("swept tail = %v, want t2 swept:true", sweptLine)
	}

	// Empty plan: no stdout, truthful stderr.
	root2 := t.TempDir()
	seedJournal(t, root2, []string{"user_message", "agent_text"})
	t.Chdir(root2)
	out, serr, code3 := captureCLI(t, func() int {
		return runTodoCLI(nil)
	})
	if code3 != 0 {
		t.Fatalf("empty plan: exit %d", code3)
	}
	if out != "" {
		t.Errorf("empty plan stdout = %q, want silent", out)
	}
	if !strings.Contains(serr, "0 open") {
		t.Errorf("empty plan stderr = %q, want 0 open", serr)
	}
}

// nonEmptyLines splits trimmed JSONL output.
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
