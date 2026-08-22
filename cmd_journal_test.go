package main

import (
	"context"
	"crypto/sha256"
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

// seedJournalTwoStreams builds a journal with "main" and "exp" workstreams
// so search's cross-workstream span is exercised. Returns after CLOSING the
// store (read-only CLI path).
func seedJournalTwoStreams(t *testing.T, root string) {
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
	appendTo := func(ws string, payloads ...string) {
		w, err := st.CreateOrGetWorkstream(ctx, p.ID, ws)
		if err != nil {
			t.Fatal(err)
		}
		c, err := st.CreateConversation(ctx, w.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, pl := range payloads {
			if _, err := st.AppendEvent(ctx, c.ID, "user_message", pl); err != nil {
				t.Fatal(err)
			}
		}
	}
	appendTo("main", `{"text":"tokenizer config v2"}`, `{"text":"unrelated"}`)
	appendTo("exp", `{"text":"tokenizer ablations"}`)
}

// stdoutSearchHits parses search JSONL into (workstream, payload) pairs.
func stdoutSearchHits(t *testing.T, stdout string) []store.SearchResult {
	t.Helper()
	var hits []store.SearchResult
	for line := range strings.Lines(strings.TrimRight(stdout, "\n")) {
		if line == "" {
			continue
		}
		var r store.SearchResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("bad JSONL line %q: %v", line, err)
		}
		hits = append(hits, r)
	}
	return hits
}

func TestJournalCLISearch(t *testing.T) {
	root := t.TempDir()
	seedJournalTwoStreams(t, root)
	t.Chdir(root)

	stdout, stderr, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"search", "tokenizer"})
	})
	if code != 0 {
		t.Fatalf("search: exit %d, stderr %q", code, stderr)
	}
	hits := stdoutSearchHits(t, stdout)
	if len(hits) != 2 {
		t.Fatalf("search hits = %d, want 2 (one per workstream): %q", len(hits), stdout)
	}
	streams := map[string]bool{}
	for _, h := range hits {
		streams[h.WorkstreamName] = true
		if h.ConversationID == 0 || h.Event.Seq == 0 {
			t.Errorf("hit missing conversation/seq context: %+v", h)
		}
	}
	if !streams["main"] || !streams["exp"] {
		t.Errorf("streams = %v, want both main and exp (cross-workstream span)", streams)
	}
	if !strings.Contains(string(hits[0].Event.Payload), "tokenizer") {
		t.Errorf("payload not verbatim: %s", hits[0].Event.Payload)
	}
	if !strings.Contains(stderr, "2 match(es)") {
		t.Errorf("stderr %q, want the match count", stderr)
	}
}

func TestJournalCLISearchLimitJoinAndNoMatch(t *testing.T) {
	root := t.TempDir()
	seedJournalTwoStreams(t, root)
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"search", "tokenizer", "--limit", "1"})
	})
	if code != 0 || len(stdoutSearchHits(t, stdout)) != 1 {
		t.Errorf("search --limit 1: exit %d, stdout %q; want 1 hit", code, stdout)
	}

	// Multiple positional terms join into one phrase query.
	stdout, _, code = captureCLI(t, func() int {
		return runJournalCLI([]string{"search", "tokenizer", "config"})
	})
	if code != 0 {
		t.Fatalf("joined search: exit %d", code)
	}
	hits := stdoutSearchHits(t, stdout)
	if len(hits) != 1 || !strings.Contains(string(hits[0].Event.Payload), "tokenizer config v2") {
		t.Errorf("joined terms hits = %v, want only the \"tokenizer config v2\" event", hits)
	}

	// No match is a normal outcome: exit 0, empty stdout, count on stderr.
	stdout, stderr, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"search", "zzz-nothing"})
	})
	if code != 0 || stdout != "" || !strings.Contains(stderr, "0 match(es)") {
		t.Errorf("no match: exit %d stdout %q stderr %q; want 0 / empty / '0 match(es)'", code, stdout, stderr)
	}
}

func TestJournalCLISearchFromRunWorktree(t *testing.T) {
	root := t.TempDir()
	seedJournalTwoStreams(t, root)
	wt := filepath.Join(root, ".odo", "worktrees", "abc123-1-abcdef")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wt)

	// Project-wide search resolves the root from an agent worktree cwd —
	// and needs no active conversation on the default workstream.
	stdout, _, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"search", "tokenizer", "--limit=5"})
	})
	if code != 0 || len(stdoutSearchHits(t, stdout)) != 2 {
		t.Errorf("search from worktree: exit %d stdout %q; want 2 hits", code, stdout)
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
		{"search"},
		{"search", "q", "--limit", "0"},
		{"search", "q", "--limit=x"},
		{"search", "q", "--limit"},
		{"rotate", "extra"},
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

// fileDigest pins rotate's move-not-delete contract: archived bytes are
// the SAME bytes (a rename), not a rewrite.
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// TestJournalCLIRotateRefusesLiveDaemon pins the fail-closed half: while
// the instance lock says a daemon is live (main.go's own liveness
// authority — the TestInstanceLockGuard fake), rotate refuses cleanly
// naming the stop path and disturbs nothing (journal intact, no archive
// dir created by the refusal).
func TestJournalCLIRotateRefusesLiveDaemon(t *testing.T) {
	root := t.TempDir()
	seedJournal(t, root, []string{"user_message"})
	t.Chdir(root)

	lock, err := acquireInstanceLock(filepath.Join(root, ".odo"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	_, stderr, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"rotate"})
	})
	if code != 1 {
		t.Fatalf("rotate under a live daemon: exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "live odo daemon") || !strings.Contains(stderr, "stop the daemon first") {
		t.Errorf("refusal stderr %q, want it to name the live daemon and how to stop it", stderr)
	}
	if _, err := os.Stat(filepath.Join(root, ".odo", "journal.sqlite")); err != nil {
		t.Errorf("journal disturbed by a refused rotate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".odo", "archive")); !os.IsNotExist(err) {
		t.Error("archive dir created by a refused rotate — refuse must precede any filesystem act")
	}
}

// TestJournalCLIRotateOffline pins the offline path: journal.sqlite and
// every present sidecar move-not-delete into .odo/archive/ under one
// timestamped basename, stdout prints what happened, and the journal the
// daemon recreates on its next start is empty (store.Open's own migrate —
// rotate never pre-creates one).
func TestJournalCLIRotateOffline(t *testing.T) {
	root := t.TempDir()
	seedJournal(t, root, []string{"user_message", "agent_text", "agent_done"})
	t.Chdir(root)

	dbPath := filepath.Join(root, ".odo", "journal.sqlite")
	dbSum := fileDigest(t, dbPath)
	// The sidecars the store's WAL DSN can leave, plus pre-WAL rollback
	// residue — all planted with known bytes so byte-identity is checkable.
	for _, sfx := range []string{"-wal", "-shm", "-journal"} {
		if err := os.WriteFile(dbPath+sfx, []byte("sidecar-bytes"+sfx), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stdout, stderr, code := captureCLI(t, func() int {
		return runJournalCLI([]string{"rotate"})
	})
	if code != 0 {
		t.Fatalf("offline rotate: exit %d, stderr %q", code, stderr)
	}

	entries, err := os.ReadDir(filepath.Join(root, ".odo", "archive"))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("archive holds %d files, want 4 (db + 3 sidecars): %v", len(entries), entries)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	// One shared timestamp across the family: journal-<stamp>.sqlite(+*).
	var base string
	for n := range names {
		if strings.HasSuffix(n, ".sqlite") {
			base = strings.TrimSuffix(n, ".sqlite")
		}
	}
	if !strings.HasPrefix(base, "journal-") {
		t.Fatalf("archived db name %q, want journal-<stamp>.sqlite", base+".sqlite")
	}
	for _, sfx := range []string{".sqlite", ".sqlite-wal", ".sqlite-shm", ".sqlite-journal"} {
		if !names[base+sfx] {
			t.Errorf("archive missing %s", base+sfx)
		}
		// The pre-rotate path must be GONE (moved, not copied).
		if _, err := os.Stat(dbPath + strings.TrimPrefix(sfx, ".sqlite")); !os.IsNotExist(err) {
			t.Errorf("journal.sqlite%s still in .odo/ after rotate", strings.TrimPrefix(sfx, ".sqlite"))
		}
	}
	if got := fileDigest(t, filepath.Join(root, ".odo", "archive", base+".sqlite")); got != dbSum {
		t.Errorf("archived db digest changed — rotate must MOVE bytes, got %s want %s", got, dbSum)
	}
	b, err := os.ReadFile(filepath.Join(root, ".odo", "archive", base+".sqlite-wal"))
	if err != nil || string(b) != "sidecar-bytes-wal" {
		t.Errorf("archived wal bytes = %q, want the planted sidecar verbatim", b)
	}
	if !strings.Contains(stdout, base+".sqlite") || !strings.Contains(stdout, "recreates an empty journal") {
		t.Errorf("stdout %q, want the archived name + the recreate note", stdout)
	}

	// Next-start behavior: store.Open recreates a schema-complete EMPTY
	// journal at the old path (no project row) — rotate pre-created nothing.
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("recreated journal open: %v", err)
	}
	if _, err := st.GetProjectByRoot(context.Background(), root); err == nil {
		t.Error("recreated journal should be empty (project row present)")
	}
	st.Close()
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
