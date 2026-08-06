package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// openTestStore opens a journal in a fresh temp dir.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenMigrates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	var version int
	if err := s.DB().QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema_version = %d, want 1", version)
	}

	for _, table := range []string{"projects", "workstreams", "conversations", "events", "diffs"} {
		var name string
		err := s.DB().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}

	// WAL mode is active.
	var mode string
	if err := s.DB().QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	// Foreign keys are enforced: an event referencing a missing conversation fails.
	_, err := s.AppendEvent(ctx, 9999, EventUserMessage, `{"text":"x"}`)
	if err == nil {
		t.Error("AppendEvent with bogus conversation_id: want foreign-key error, got nil")
	}
}

// TestReopenIdempotent covers restart restore at the store level: reopening
// an existing journal preserves rows.
func TestReopenIdempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "journal.sqlite")

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p1, err := s1.CreateOrGetProject(ctx, "/tmp/x", "x")
	if err != nil {
		t.Fatalf("CreateOrGetProject: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	p2, err := s2.CreateOrGetProject(ctx, "/tmp/x", "x")
	if err != nil {
		t.Fatalf("CreateOrGetProject after reopen: %v", err)
	}
	if p2.ID != p1.ID {
		t.Errorf("project ID changed across reopen: %d -> %d", p1.ID, p2.ID)
	}
}

func TestCreateOrGetProjectIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a, err := s.CreateOrGetProject(ctx, "/repo/a", "a")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	b, err := s.CreateOrGetProject(ctx, "/repo/a", "a-renamed")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if a.ID != b.ID {
		t.Errorf("same root_path returned different IDs %d and %d", a.ID, b.ID)
	}
	c, err := s.CreateOrGetProject(ctx, "/repo/b", "b")
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if c.ID == a.ID {
		t.Error("different root_path reused same project ID")
	}

	got, err := s.GetProject(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.RootPath != "/repo/a" || got.Name != "a" {
		t.Errorf("GetProject = %+v", got)
	}
}

func TestCreateOrGetWorkstreamIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p, _ := s.CreateOrGetProject(ctx, "/repo/w", "w")

	w1, err := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w1.Status != WorkstreamActive {
		t.Errorf("status = %q, want active", w1.Status)
	}
	w2, err := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if w1.ID != w2.ID {
		t.Errorf("same project+name gave IDs %d and %d", w1.ID, w2.ID)
	}
	w3, err := s.CreateOrGetWorkstream(ctx, p.ID, "feature")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	if w3.ID == w1.ID {
		t.Error("different name reused same workstream ID")
	}
}

func TestAppendEventMonotonicSeqPerConversation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p, _ := s.CreateOrGetProject(ctx, "/repo/e", "e")
	w, _ := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	c1, _ := s.CreateConversation(ctx, w.ID, "")
	c2, _ := s.CreateConversation(ctx, w.ID, "")

	// Interleave appends across two conversations; seq must be an
	// independent 1..N sequence per conversation.
	want := []struct {
		conv int64
		seq  int
	}{
		{c1.ID, 1}, {c1.ID, 2}, {c2.ID, 1}, {c1.ID, 3}, {c2.ID, 2},
	}
	for i, wnt := range want {
		e, err := s.AppendEvent(ctx, wnt.conv, EventAgentText, `{"text":"hi"}`)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if e.Seq != wnt.seq {
			t.Errorf("append %d: seq = %d, want %d", i, e.Seq, wnt.seq)
		}
		if string(e.Payload) != `{"text":"hi"}` {
			t.Errorf("append %d: payload = %s", i, e.Payload)
		}
		if e.ID == 0 || e.CreatedAt == "" {
			t.Errorf("append %d: id or created_at unset: %+v", i, e)
		}
	}
}

func TestListEventsAfterSeq(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p, _ := s.CreateOrGetProject(ctx, "/repo/l", "l")
	w, _ := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	c, _ := s.CreateConversation(ctx, w.ID, "")

	for i := 0; i < 5; i++ {
		if _, err := s.AppendEvent(ctx, c.ID, EventAgentText, `{"n":1}`); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	all, err := s.ListEvents(ctx, c.ID, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("after_seq=0: got %d events, want 5", len(all))
	}
	for i, e := range all {
		if e.Seq != i+1 {
			t.Errorf("events not ordered by seq: index %d has seq %d", i, e.Seq)
		}
	}

	tail, err := s.ListEvents(ctx, c.ID, 3)
	if err != nil {
		t.Fatalf("ListEvents after 3: %v", err)
	}
	if len(tail) != 2 || tail[0].Seq != 4 || tail[1].Seq != 5 {
		t.Errorf("after_seq=3: got seqs %v, want [4 5]", seqs(tail))
	}

	none, err := s.ListEvents(ctx, c.ID, 5)
	if err != nil {
		t.Fatalf("ListEvents after 5: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("after_seq=5: got %d events, want 0", len(none))
	}
}

func seqs(events []Event) []int {
	out := make([]int, len(events))
	for i, e := range events {
		out[i] = e.Seq
	}
	return out
}

func TestConversationLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p, _ := s.CreateOrGetProject(ctx, "/repo/c", "c")
	w, _ := s.CreateOrGetWorkstream(ctx, p.ID, "main")

	_, err := s.GetActiveConversation(ctx, w.ID)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetActiveConversation on empty workstream: err = %v, want sql.ErrNoRows", err)
	}

	c1, err := s.CreateConversation(ctx, w.ID, "abc123")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if c1.State != ConversationActive || c1.Epoch != 1 {
		t.Errorf("new conversation = state %q epoch %d, want active/1", c1.State, c1.Epoch)
	}
	if c1.BaseCommitSHA == nil || *c1.BaseCommitSHA != "abc123" {
		t.Errorf("base_commit_sha = %v, want abc123", c1.BaseCommitSHA)
	}

	c2, err := s.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatalf("CreateConversation 2: %v", err)
	}
	if c2.BaseCommitSHA != nil {
		t.Errorf("empty baseSHA should be NULL, got %q", *c2.BaseCommitSHA)
	}

	active, err := s.GetActiveConversation(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetActiveConversation: %v", err)
	}
	if active.ID != c2.ID {
		t.Errorf("active = %d, want most recent %d", active.ID, c2.ID)
	}

	got, err := s.GetConversation(ctx, c1.ID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.WorkstreamID != w.ID {
		t.Errorf("GetConversation workstream = %d, want %d", got.WorkstreamID, w.ID)
	}
}

func TestDiffLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p, _ := s.CreateOrGetProject(ctx, "/repo/d", "d")
	w, _ := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	c, _ := s.CreateConversation(ctx, w.ID, "base0")

	d, err := s.InsertDiff(ctx, c.ID, "/repo/d/.odo/diffs/1.diff", "base0")
	if err != nil {
		t.Fatalf("InsertDiff: %v", err)
	}
	if d.Status != DiffPending {
		t.Errorf("new diff status = %q, want pending", d.Status)
	}
	if d.BaseSHA == nil || *d.BaseSHA != "base0" {
		t.Errorf("base_sha = %v, want base0", d.BaseSHA)
	}

	got, err := s.GetDiff(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if got.PathOnDisk != "/repo/d/.odo/diffs/1.diff" {
		t.Errorf("path_on_disk = %q", got.PathOnDisk)
	}

	if err := s.UpdateDiffStatus(ctx, d.ID, DiffAccepted); err != nil {
		t.Fatalf("UpdateDiffStatus: %v", err)
	}
	got, _ = s.GetDiff(ctx, d.ID)
	if got.Status != DiffAccepted {
		t.Errorf("status after update = %q, want accepted", got.Status)
	}

	if err := s.UpdateDiffStatus(ctx, 4242, DiffAccepted); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("update missing diff: err = %v, want sql.ErrNoRows", err)
	}

	latest, err := s.LatestDiff(ctx, c.ID)
	if err != nil {
		t.Fatalf("LatestDiff: %v", err)
	}
	if latest.ID != d.ID {
		t.Errorf("latest = %d, want %d", latest.ID, d.ID)
	}
	if _, err := s.LatestDiff(ctx, 7777); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("LatestDiff on empty conversation: err = %v, want sql.ErrNoRows", err)
	}
}

func TestListWorkstreams(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p, _ := s.CreateOrGetProject(ctx, "/repo/lws", "lws")
	other, _ := s.CreateOrGetProject(ctx, "/repo/lws2", "lws2")

	w1, err := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatalf("create main: %v", err)
	}
	// New workstreams get their name as the git branch.
	if w1.Branch == nil || *w1.Branch != "main" {
		t.Errorf("branch = %v, want \"main\"", w1.Branch)
	}
	w2, _ := s.CreateOrGetWorkstream(ctx, p.ID, "feature-x")
	if _, err := s.CreateOrGetWorkstream(ctx, other.ID, "elsewhere"); err != nil {
		t.Fatalf("create other project workstream: %v", err)
	}

	got, err := s.ListWorkstreams(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListWorkstreams: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListWorkstreams returned %d, want 2 (other project's workstream must not leak)", len(got))
	}
	if got[0].ID != w1.ID || got[1].ID != w2.ID {
		t.Errorf("order = [%d %d], want [%d %d]", got[0].ID, got[1].ID, w1.ID, w2.ID)
	}

	if got, err := s.ListWorkstreams(ctx, 4242); err != nil || len(got) != 0 {
		t.Errorf("ListWorkstreams on empty project = (%v, %v), want empty", got, err)
	}
}

func TestIncrementEpoch(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p, _ := s.CreateOrGetProject(ctx, "/repo/ep", "ep")
	w, _ := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	c, _ := s.CreateConversation(ctx, w.ID, "")
	if c.Epoch != 1 {
		t.Fatalf("new conversation epoch = %d, want 1", c.Epoch)
	}

	for i, want := range []int{2, 3} {
		got, err := s.IncrementEpoch(ctx, c.ID)
		if err != nil {
			t.Fatalf("IncrementEpoch %d: %v", i, err)
		}
		if got != want {
			t.Errorf("IncrementEpoch returned %d, want %d", got, want)
		}
	}
	// The epoch column persisted, not just the return value.
	got, err := s.GetConversation(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.Epoch != 3 {
		t.Errorf("stored epoch = %d, want 3", got.Epoch)
	}
	if _, err := s.IncrementEpoch(ctx, 4242); err == nil {
		t.Error("IncrementEpoch on missing conversation: want error, got nil")
	}
}

func TestSearchEvents(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p, _ := s.CreateOrGetProject(ctx, "/repo/s", "s")
	w1, _ := s.CreateOrGetWorkstream(ctx, p.ID, "alpha")
	w2, _ := s.CreateOrGetWorkstream(ctx, p.ID, "beta")
	c1, _ := s.CreateConversation(ctx, w1.ID, "")
	c2, _ := s.CreateConversation(ctx, w2.ID, "")

	// c1 has a text event matching "hello world"
	if _, err := s.AppendEvent(ctx, c1.ID, EventAgentText, `{"text":"hello world"}`); err != nil {
		t.Fatalf("append c1: %v", err)
	}
	// c1 also has a non-matching event
	if _, err := s.AppendEvent(ctx, c1.ID, EventAgentText, `{"text":"bye"}`); err != nil {
		t.Fatalf("append c1b: %v", err)
	}
	// c2 has a matching event in a different workstream
	if _, err := s.AppendEvent(ctx, c2.ID, EventAgentText, `{"text":"hello again"}`); err != nil {
		t.Fatalf("append c2: %v", err)
	}

	// Search for "hello" — should find 2 results (from c1 and c2)
	results, err := s.SearchEvents(ctx, p.ID, "hello", 100)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}

	// Results should carry workstream context
	names := map[string]bool{}
	for _, r := range results {
		names[r.WorkstreamName] = true
		if r.Event.ConversationID == 0 {
			t.Error("event conversation_id is zero")
		}
	}
	if len(names) != 2 {
		t.Errorf("expected 2 distinct workstream names, got %v", names)
	}

	// Search for non-existent — 0 results
	empty, err := s.SearchEvents(ctx, p.ID, "nonexistent", 100)
	if err != nil {
		t.Fatalf("SearchEvents empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 results for nonexistent query, got %d", len(empty))
	}

	// Limit respected
	if _, err := s.AppendEvent(ctx, c1.ID, EventAgentText, `{"text":"hello 1"}`); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := s.AppendEvent(ctx, c1.ID, EventAgentText, `{"text":"hello 2"}`); err != nil {
		t.Fatalf("append: %v", err)
	}
	limited, err := s.SearchEvents(ctx, p.ID, "hello", 2)
	if err != nil {
		t.Fatalf("SearchEvents limited: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("expected 2 results with limit=2, got %d", len(limited))
	}

	// Deleted workstream excluded
	if err := s.DeleteWorkstream(ctx, w2.ID); err != nil {
		t.Fatalf("DeleteWorkstream: %v", err)
	}
	afterDelete, err := s.SearchEvents(ctx, p.ID, "hello", 100)
	if err != nil {
		t.Fatalf("SearchEvents after delete: %v", err)
	}
	// Only c1's "hello world" remains (c2's "hello again" excluded)
	if len(afterDelete) != 3 { // "hello world", "hello 1", "hello 2"
		t.Errorf("expected 3 results after delete (c1 only), got %d: %+v", len(afterDelete), afterDelete)
	}
	for _, r := range afterDelete {
		if r.WorkstreamName == "beta" {
			t.Error("deleted workstream 'beta' should not appear in search results")
		}
	}
}
