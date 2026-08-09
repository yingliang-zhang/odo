package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// seedJournal builds a journal at root with one conversation on the "main"
// workstream, then appends events verbatim: each string is an event type,
// with "distill" / "distill_legacy" expanding to distill review_action
// markers (explicit first_seq/last_seq schema vs the pre-schema shape).
// Returns after CLOSING the store so the read-only CLI path is exercised.
func seedJournal(t *testing.T, root string, kinds []string) {
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
	for i, kind := range kinds {
		typ, payload := kind, fmt.Sprintf(`{"text":"event %d"}`, i+1)
		switch kind {
		case "distill":
			typ = store.EventReviewAction
			payload = `{"action":"distill","epoch":2,"first_seq":1,"last_seq":3,"note_sha":"abc"}`
		case "distill2":
			typ = store.EventReviewAction
			payload = `{"action":"distill","epoch":3,"first_seq":5,"last_seq":6,"note_sha":"def"}`
		case "distill_legacy":
			typ = store.EventReviewAction
			payload = `{"action":"distill","epoch":2}`
		}
		if _, err := st.AppendEvent(ctx, c.ID, typ, payload); err != nil {
			t.Fatal(err)
		}
	}
}

// stdoutSeqs parses the JSONL stdout of a journal CLI run into seq numbers.
func stdoutSeqs(t *testing.T, stdout string) []int {
	t.Helper()
	var seqs []int
	for line := range strings.Lines(strings.TrimRight(stdout, "\n")) {
		if line == "" {
			continue
		}
		var e store.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad JSONL line %q: %v", line, err)
		}
		seqs = append(seqs, e.Seq)
	}
	return seqs
}

func TestJournalCLIFolded(t *testing.T) {
	root := t.TempDir()
	// 3 events, marker(1..3), 2 events, marker(5..6): both markers explicit.
	seedJournal(t, root, []string{"user_message", "agent_text", "agent_done", "distill", "user_message", "agent_done", "distill2"})
	t.Chdir(root)

	stdout, stderr, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"folded"})
	})
	if code != 0 {
		t.Fatalf("folded: exit %d, stderr %q", code, stderr)
	}
	seqs := stdoutSeqs(t, stdout)
	if fmt.Sprint(seqs) != "[5 6]" {
		t.Errorf("folded seqs = %v, want [5 6] (latest marker's explicit window)", seqs)
	}
	if !strings.Contains(stderr, "seq 5..6") {
		t.Errorf("stderr %q, want the window summary", stderr)
	}
}

func TestJournalCLIFoldedLegacyMarker(t *testing.T) {
	root := t.TempDir()
	// Legacy markers (no first_seq/last_seq) derive: prev marker seq+1 …
	// latest marker seq−1 → seqs 5..6, identical to the explicit schema.
	seedJournal(t, root, []string{"user_message", "agent_text", "agent_done", "distill_legacy", "user_message", "agent_done", "distill_legacy"})
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"folded"})
	})
	if code != 0 {
		t.Fatalf("folded legacy: exit %d", code)
	}
	if seqs := stdoutSeqs(t, stdout); fmt.Sprint(seqs) != "[5 6]" {
		t.Errorf("folded legacy seqs = %v, want [5 6] (derived window)", seqs)
	}
}

func TestJournalCLIFoldedNoMarker(t *testing.T) {
	root := t.TempDir()
	seedJournal(t, root, []string{"user_message", "agent_text"})
	t.Chdir(root)

	_, stderr, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"folded"})
	})
	if code != 1 || !strings.Contains(stderr, "nothing folded") {
		t.Errorf("no marker: exit %d stderr %q; want 1 + 'nothing folded'", code, stderr)
	}
}

func TestJournalCLIRangeAndTail(t *testing.T) {
	root := t.TempDir()
	seedJournal(t, root, []string{"user_message", "agent_text", "agent_done", "user_message", "agent_done"})
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"range", "2", "4"})
	})
	if code != 0 {
		t.Fatalf("range: exit %d", code)
	}
	if seqs := stdoutSeqs(t, stdout); fmt.Sprint(seqs) != "[2 3 4]" {
		t.Errorf("range 2 4 seqs = %v, want [2 3 4]", seqs)
	}

	stdout, _, code = captureCLI(t, func() int {
		return runJournalCLI([]string{"range", "4"})
	})
	if code != 0 {
		t.Fatalf("range open: exit %d", code)
	}
	if seqs := stdoutSeqs(t, stdout); fmt.Sprint(seqs) != "[4 5]" {
		t.Errorf("range 4 seqs = %v, want [4 5] (to end)", seqs)
	}

	stdout, _, code = captureCLI(t, func() int {
		return runJournalCLI([]string{"tail", "2"})
	})
	if code != 0 {
		t.Fatalf("tail: exit %d", code)
	}
	if seqs := stdoutSeqs(t, stdout); fmt.Sprint(seqs) != "[4 5]" {
		t.Errorf("tail 2 seqs = %v, want [4 5]", seqs)
	}

	// Payloads pass through verbatim (the agent sees the real record).
	if !strings.Contains(stdout, `"text":"event 4"`) {
		t.Errorf("tail stdout missing verbatim payload: %q", stdout)
	}
}

func TestJournalCLIRejectsBadArgs(t *testing.T) {
	root := t.TempDir()
	seedJournal(t, root, []string{"user_message"})
	t.Chdir(root)

	for _, args := range [][]string{
		{},
		{"bogus"},
		{"range", "0"},
		{"range", "5", "2"},
		{"tail", "0"},
		{"folded", "extra"},
	} {
		if _, _, code := captureCLI(t, func() int { return runJournalCLI(args) }); code != 2 {
			t.Errorf("args %v: exit %d, want 2 (usage)", args, code)
		}
	}
}

func TestJournalCLIFromRunWorktree(t *testing.T) {
	root := t.TempDir()
	seedJournal(t, root, []string{"user_message", "agent_text", "agent_done", "distill_legacy"})
	// An agent's cwd inside its run worktree resolves the parent project.
	wt := filepath.Join(root, ".odo", "worktrees", "abc123-1-abcdef")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wt)

	stdout, _, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"folded"})
	})
	if code != 0 {
		t.Fatalf("folded from worktree: exit %d", code)
	}
	if seqs := stdoutSeqs(t, stdout); fmt.Sprint(seqs) != "[1 2 3]" {
		t.Errorf("folded from worktree seqs = %v, want [1 2 3]", seqs)
	}
}

func TestJournalCLIReadOnlyNeverWrites(t *testing.T) {
	root := t.TempDir()
	seedJournal(t, root, []string{"user_message"})
	t.Chdir(root)

	// An unknown workstream errors (exit 1) and must NOT appear in the
	// journal afterwards — OpenReadOnly makes creation impossible.
	if _, _, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"tail", "1", "--workstream", "ghost"})
	}); code != 1 {
		t.Fatalf("unknown workstream: exit %d, want 1", code)
	}
	st, err := store.OpenReadOnly(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	p, err := st.GetProjectByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorkstreamByName(ctx, p.ID, "ghost"); err == nil {
		t.Error("ghost workstream exists — the CLI created a row despite read-only mode")
	}

	// No journal at all → clean error, not a created empty database.
	empty := t.TempDir()
	t.Chdir(empty)
	if _, stderr, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"tail", "1"})
	}); code != 1 || !strings.Contains(stderr, "no .odo/journal.sqlite") {
		t.Errorf("no journal: exit %d stderr %q; want 1 naming the missing journal", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(empty, ".odo")); !os.IsNotExist(err) {
		t.Error(".odo dir created by the read-only CLI")
	}
}
