package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// M12 Batch 3a (D-semantic step 0) tests: the recall miss-audit CLI. A
// fixture journal exercises the miss classification (≥3 extracted query
// terms AND zero matched notes), the --last window, the stable --json
// shape, and the empty-journal "no data" path.

// seedAuditJournal builds a journal at root with two workstreams ("main",
// "exp"), then appends the given user_message payloads verbatim —
// mainPayloads to main's conversation, expPayloads to exp's. Returns after
// CLOSING the store so the read-only CLI path is exercised (the
// cmd_journal_test.go discipline).
func seedAuditJournal(t *testing.T, root string, mainPayloads, expPayloads []string) {
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
	appendTo := func(ws string, payloads []string) {
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
	appendTo("main", mainPayloads)
	if expPayloads != nil {
		appendTo("exp", expPayloads)
	}
}

// auditFixture is the reference corpus: 5 user messages across two
// conversations —
//
//	main seq 1: 3 EN terms, one note item without matched_terms ⇒ MISS
//	main seq 2: 3 EN terms, one note WITH matched_terms + one fixed layer ⇒ hit
//	main seq 3: 1 term ("hi"), no recall items ⇒ not a miss (too few terms)
//	exp  seq 1: 5 CJK bigram terms, no recall items ⇒ MISS
//	exp  seq 2: no recall field at all (pre-M6 shape), 1 term ⇒ not a miss
func auditFixture() (mainPayloads, expPayloads []string) {
	main := []string{
		`{"text":"tokenizer config v2","recall":[{"path":"/root/wiki/main-epoch-1.md"}]}`,
		`{"text":"fix fold bug","recall":[{"path":"~/.odo/user.md"},{"path":"/root/wiki/main-epoch-2.md","matched_terms":["fold"]}]}`,
		`{"text":"hi","recall":[]}`,
	}
	exp := []string{
		`{"text":"折叠逻辑修复了吗","recall":[]}`,
		`{"text":"ok"}`,
	}
	return main, exp
}

func TestRecallAuditMissClassification(t *testing.T) {
	root := t.TempDir()
	main, exp := auditFixture()
	seedAuditJournal(t, root, main, exp)
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int {
		return runRecallCLI([]string{"audit", "--json"})
	})
	if code != 0 {
		t.Fatalf("audit --json: exit %d", code)
	}
	var r recallAuditReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("--json output not a valid report: %v\n%s", err, stdout)
	}
	if r.UserMessages != 5 {
		t.Errorf("user_messages = %d, want 5 (all conversations)", r.UserMessages)
	}
	if r.ConversationsScanned != 2 || r.WorkstreamsScanned != 2 {
		t.Errorf("scanned %d conv / %d ws, want 2/2", r.ConversationsScanned, r.WorkstreamsScanned)
	}
	// THE MISS CLASS: main seq 1 (3 terms, zero matched) and exp seq 1
	// (5 CJK bigram terms, zero matched); seq 3/2 fall below the term
	// floor, seq 2 matched.
	if r.Miss.Count != 2 {
		t.Errorf("miss.count = %d, want 2", r.Miss.Count)
	}
	if got := r.Miss.Rate; got < 0.399 || got > 0.401 {
		t.Errorf("miss.rate = %v, want 0.4", got)
	}
	if len(r.MissQueries) != 2 {
		t.Fatalf("miss_queries = %d, want 2", len(r.MissQueries))
	}
	// Labeled pool: journal coordinates + truncated query, newest first.
	for _, q := range r.MissQueries {
		if q.ConversationID == 0 || q.Seq == 0 || q.Workstream == "" {
			t.Errorf("miss query missing coordinates: %+v", q)
		}
	}
	// Buckets sum to the message count; specific pins from the fixture.
	if got := r.MatchedTermsPerMessage["0"]; got != 4 {
		t.Errorf("matched_terms 0-bucket = %d, want 4 (only seq-2 matched)", got)
	}
	if bucketSum := r.ItemsPerMessage["0"] + r.ItemsPerMessage["1"] + r.ItemsPerMessage["2"] + r.ItemsPerMessage["3-5"] + r.ItemsPerMessage["6+"]; bucketSum != 5 {
		t.Errorf("items buckets sum = %d, want 5", bucketSum)
	}
	// Note tallies: the matched note vs the never-matched fallback note.
	if len(r.TopMatchedNotes) != 1 || r.TopMatchedNotes[0].Name != "main-epoch-2.md" || r.TopMatchedNotes[0].Messages != 1 {
		t.Errorf("top_matched_notes = %+v, want [{main-epoch-2.md 1}]", r.TopMatchedNotes)
	}
	if len(r.NeverMatchedNotes) != 1 || r.NeverMatchedNotes[0].Name != "main-epoch-1.md" {
		t.Errorf("never_matched_notes = %+v, want [{main-epoch-1.md …}]", r.NeverMatchedNotes)
	}
}

// TestRecallAuditHumanAndLast: the human table prints the compact
// classification, and --last N narrows each conversation to its newest N
// user messages (miss set changes accordingly).
func TestRecallAuditHumanAndLast(t *testing.T) {
	root := t.TempDir()
	main, exp := auditFixture()
	seedAuditJournal(t, root, main, exp)
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int {
		return runRecallCLI([]string{"audit"})
	})
	if code != 0 {
		t.Fatalf("audit: exit %d", code)
	}
	for _, want := range []string{
		"user messages: 5",
		"miss class (≥3 query terms, zero matched notes): 2/5 (40.0%)",
		"top matched notes (messages):",
		"main-epoch-2.md",
		"never-matched notes (fallback inclusions):",
		"main-epoch-1.md",
		"miss query pool (2 most recent",
		"[main conv 1 seq 1]",
		"[exp conv 2 seq 1]",
		"tokenizer config v2",
		"折叠逻辑修复了吗",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human report missing %q:\n%s", want, stdout)
		}
	}

	// --last 1: only each conversation's newest user message — main seq 3
	// (not a miss) and exp seq 2 (not a miss).
	stdout, _, code = captureCLI(t, func() int {
		return runRecallCLI([]string{"audit", "--last", "1", "--json"})
	})
	if code != 0 {
		t.Fatalf("audit --last 1: exit %d", code)
	}
	var r recallAuditReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("--json output: %v", err)
	}
	if r.UserMessages != 2 || r.LastPerConversation != 1 {
		t.Errorf("--last 1: messages=%d last=%d, want 2/1", r.UserMessages, r.LastPerConversation)
	}
	if r.Miss.Count != 0 {
		t.Errorf("--last 1 miss.count = %d, want 0 (only the newest messages scanned)", r.Miss.Count)
	}
}

// TestRecallAuditEmptyJournal pins the floor case: no user_message events
// anywhere ⇒ "no data", exit 0, and --json still emits a stable,
// zero-valued object (never a crash).
func TestRecallAuditEmptyJournal(t *testing.T) {
	root := t.TempDir()
	seedAuditJournal(t, root, nil, nil)
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int {
		return runRecallCLI([]string{"audit"})
	})
	if code != 0 {
		t.Fatalf("empty audit: exit %d, want 0 (a valid empty report)", code)
	}
	if !strings.Contains(stdout, "no data") {
		t.Errorf("empty audit stdout missing 'no data': %q", stdout)
	}

	stdout, _, code = captureCLI(t, func() int {
		return runRecallCLI([]string{"audit", "--json"})
	})
	if code != 0 {
		t.Fatalf("empty audit --json: exit %d", code)
	}
	var r recallAuditReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("empty --json not a valid report: %v\n%s", err, stdout)
	}
	if r.UserMessages != 0 || r.Miss.Count != 0 || r.MissQueries != nil {
		t.Errorf("empty report not zero-valued: %+v", r)
	}
}

// TestRecallAuditUsageError: a missing or unknown subcommand prints usage
// to stderr and exits 2 — no journal access, no crash. A bad --last value
// also exits 2, with its own message.
func TestRecallAuditUsageError(t *testing.T) {
	for _, args := range [][]string{{}, {"bogus"}} {
		_, stderr, code := captureCLI(t, func() int {
			return runRecallCLI(args)
		})
		if code != 2 {
			t.Errorf("runRecallCLI(%v): exit %d, want 2", args, code)
		}
		if !strings.Contains(stderr, "usage:") {
			t.Errorf("runRecallCLI(%v): stderr missing usage: %q", args, stderr)
		}
	}
	_, stderr, code := captureCLI(t, func() int {
		return runRecallCLI([]string{"audit", "--last", "x"})
	})
	if code != 2 || !strings.Contains(stderr, "--last") {
		t.Errorf("bad --last: exit %d stderr %q, want 2 + --last message", code, stderr)
	}
}

// TestTruncateRunes pins the query-pool truncation: 80-runе clip with an
// ellipsis, rune-safe on CJK.
func TestTruncateRunes(t *testing.T) {
	short := "short query"
	if got := truncateRunes(short, 80); got != short {
		t.Errorf("truncateRunes short = %q, want unchanged", got)
	}
	long := strings.Repeat("折叠逻辑", 30) // 120 runes
	got := truncateRunes(long, 80)
	if runes := []rune(got); len(runes) != 81 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncateRunes long = %d runes, want 80+ellipsis", len(runes))
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) {
		t.Error("truncateRunes must keep a strict prefix")
	}
}

// TestParseRecallMsgLegacyShape: pre-M6 journals stored recall as
// []string — entries carry no match signal and must parse to nothing
// rather than crash the audit.
func TestParseRecallMsgLegacyShape(t *testing.T) {
	text, items := parseRecallMsg(json.RawMessage(`{"text":"legacy","recall":["/wiki/main-epoch-1.md"]}`))
	if text != "legacy" {
		t.Errorf("text = %q, want legacy", text)
	}
	if len(items) != 0 {
		t.Errorf("legacy string entries = %v, want skipped (no match signal)", items)
	}
	if _, garbage := parseRecallMsg(json.RawMessage(`{not json`)); garbage != nil {
		t.Error("unparseable payload must yield no items")
	}
}

// TestRecallAuditSlashExclusion (F1): slash messages journal no recall
// key (receipt+context_scope+total_prompt_bytes only), yet their text
// tokenizes to ≥3 terms — without the exclusion every /panel or /vision
// message inflates the miss class (in the dogfood journal 4 of 7 misses
// were slash). They are excluded and reported in their own bucket; the
// miss rate counts only evidence-bearing messages.
func TestRecallAuditSlashExclusion(t *testing.T) {
	root := t.TempDir()
	main := []string{
		// /panel payload: 5 tokenized terms, NO recall key — the live
		// false positive; not a miss, goes to the excluded bucket.
		`{"text":"/panel compare sqlite wal modes","context_scope":"full","total_prompt_bytes":1234,"receipt":{}}`,
		// Plain miss: 3 terms, zero matched notes ⇒ miss count 1.
		`{"text":"tokenizer config v2","recall":[{"path":"/root/wiki/main-epoch-1.md"}]}`,
		// A hit: one matched note ⇒ 2 evidence-bearing messages, 1 miss.
		`{"text":"fix fold bug","recall":[{"path":"/root/wiki/main-epoch-2.md","matched_terms":["fold"]}]}`,
	}
	seedAuditJournal(t, root, main, nil)
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int {
		return runRecallCLI([]string{"audit", "--json"})
	})
	if code != 0 {
		t.Fatalf("audit --json: exit %d", code)
	}
	var r recallAuditReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("--json output: %v\n%s", err, stdout)
	}
	if r.UserMessages != 3 {
		t.Errorf("user_messages = %d, want 3 (slash stays in the scan)", r.UserMessages)
	}
	if r.ExcludedSlashMessages != 1 {
		t.Errorf("excluded_slash_messages = %d, want 1 (the /panel payload)", r.ExcludedSlashMessages)
	}
	if r.Miss.Count != 1 {
		t.Errorf("miss.count = %d, want 1 — the slash payload must NOT count as a miss", r.Miss.Count)
	}
	if r.Miss.Rate != 0.5 {
		t.Errorf("miss.rate = %v, want 0.5 — 1 miss over 2 evidence-bearing messages", r.Miss.Rate)
	}
	if len(r.MissQueries) != 1 || r.MissQueries[0].Query != "tokenizer config v2" {
		t.Errorf("miss_queries = %+v, want only the plain miss (slash never enters the pool)", r.MissQueries)
	}

	// Human report: evidence-bearing denominator + its own bucket line.
	stdout, _, code = captureCLI(t, func() int {
		return runRecallCLI([]string{"audit"})
	})
	if code != 0 {
		t.Fatalf("audit: exit %d", code)
	}
	for _, want := range []string{
		"miss class (≥3 query terms, zero matched notes): 1/2 (50.0%)",
		"excluded slash (no recall evidence journaled): 1",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human report missing %q:\n%s", want, stdout)
		}
	}
}

// TestIsSlashMessage pins the routing mirror: the daemon accepts
// "<cmd>" and "<cmd> <args>" for /panel and /vision — and nothing else
// (e.g. /panels is a normal message).
func TestIsSlashMessage(t *testing.T) {
	for text, want := range map[string]bool{
		"/panel":                  true,
		"/panel analyze this":     true,
		"  /vision what is here":  true,
		"/panels are great tools": false, // prefix is not the command
		"/panelx":                 false,
		"what does /panel do?":    false, // must START with the command
		"tokenizer config v2":     false,
	} {
		if got := isSlashMessage(text); got != want {
			t.Errorf("isSlashMessage(%q) = %v, want %v", text, got, want)
		}
	}
}
