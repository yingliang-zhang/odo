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
	// Fresh journals are created at v2 directly (schemaV1 DDL is the
	// current shape; migrateV2 only upgrades pre-v2 journals).
	if version != 2 {
		t.Fatalf("schema_version = %d, want 2", version)
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

// TestMigrateV1ToV2 covers the upgrade every pre-v2 (live) journal takes on
// first boot under this code: workstreams loses branch + worktree_path,
// diffs gains worktree_path (NULL on existing rows — the sweeper treats
// NULL as long-retired, I10), and the whole journal's rows survive.
func TestMigrateV1ToV2(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "journal.sqlite")

	// Hand-build a v1-shape journal (the pre-redesign DDL) with one row in
	// every table that migrateV2 rewrites.
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	v1 := []string{
		`CREATE TABLE schema_version (version INTEGER NOT NULL)`,
		`INSERT INTO schema_version (version) VALUES (1)`,
		`CREATE TABLE projects (id INTEGER PRIMARY KEY, root_path TEXT NOT NULL UNIQUE, name TEXT NOT NULL, created_at DATETIME NOT NULL DEFAULT (datetime('now')))`,
		`INSERT INTO projects (id, root_path, name) VALUES (1, '/p', 'p')`,
		`CREATE TABLE workstreams (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL REFERENCES projects(id), name TEXT NOT NULL, branch TEXT, worktree_path TEXT, status TEXT NOT NULL DEFAULT 'active', created_at DATETIME NOT NULL DEFAULT (datetime('now')))`,
		`INSERT INTO workstreams (id, project_id, name, branch, worktree_path) VALUES (1, 1, 'main', 'odo/main', '/p/.odo/worktrees/old')`,
		`CREATE TABLE conversations (id INTEGER PRIMARY KEY, workstream_id INTEGER NOT NULL REFERENCES workstreams(id), epoch INTEGER NOT NULL DEFAULT 1, state TEXT NOT NULL DEFAULT 'active', base_commit_sha TEXT, created_at DATETIME NOT NULL DEFAULT (datetime('now')))`,
		`INSERT INTO conversations (id, workstream_id) VALUES (1, 1)`,
		`CREATE TABLE events (id INTEGER PRIMARY KEY, conversation_id INTEGER NOT NULL REFERENCES conversations(id), seq INTEGER NOT NULL, type TEXT NOT NULL, payload_json TEXT NOT NULL, created_at DATETIME NOT NULL DEFAULT (datetime('now')), UNIQUE(conversation_id, seq))`,
		`INSERT INTO events (conversation_id, seq, type, payload_json) VALUES (1, 1, 'user_message', '{"text":"x"}')`,
		`CREATE TABLE diffs (id INTEGER PRIMARY KEY, conversation_id INTEGER NOT NULL REFERENCES conversations(id), path_on_disk TEXT NOT NULL, base_sha TEXT, status TEXT NOT NULL DEFAULT 'pending', created_at DATETIME NOT NULL DEFAULT (datetime('now')))`,
		`INSERT INTO diffs (id, conversation_id, path_on_disk, base_sha) VALUES (1, 1, '/p/.odo/diffs/1.diff', 'abc')`,
	}
	for _, q := range v1 {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("v1 fixture: %s: %v", q, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open over v1 journal: %v", err)
	}
	defer s.Close()

	var version int
	if err := s.DB().QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if version != 2 {
		t.Fatalf("schema_version = %d, want 2 after upgrade", version)
	}

	// Rows survive the upgrade with the new binding NULL on pre-v2 diffs.
	d, err := s.GetDiff(ctx, 1)
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if d.WorktreePath != nil {
		t.Errorf("pre-v2 diff worktree_path = %v, want NULL (long-retired)", *d.WorktreePath)
	}
	if d.BaseSHA == nil || *d.BaseSHA != "abc" {
		t.Errorf("pre-v2 diff base_sha = %v, want abc", d.BaseSHA)
	}
	w, err := s.GetWorkstream(ctx, 1)
	if err != nil {
		t.Fatalf("GetWorkstream: %v", err)
	}
	if w.Name != "main" {
		t.Errorf("workstream name = %q, want main", w.Name)
	}

	// The dropped columns are really gone; the added column is writable.
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO workstreams (project_id, name, branch) VALUES (1, 'x', 'odo/x')`); err == nil {
		t.Error("INSERT with dropped branch column: want unknown-column error, got nil")
	}
	if _, err := s.InsertDiff(ctx, 1, "/p/.odo/diffs/2.diff", "def", "/p/.odo/worktrees/run-2"); err != nil {
		t.Errorf("InsertDiff with worktree binding after upgrade: %v", err)
	}

	// The upgrade is idempotent: reopen is a no-op.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen after upgrade: %v", err)
	}
	defer s2.Close()
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

	d, err := s.InsertDiff(ctx, c.ID, "/repo/d/.odo/diffs/1.diff", "base0", "/repo/d/.odo/worktrees/run-1")
	if err != nil {
		t.Fatalf("InsertDiff: %v", err)
	}
	if d.Status != DiffPending {
		t.Errorf("new diff status = %q, want pending", d.Status)
	}
	if d.BaseSHA == nil || *d.BaseSHA != "base0" {
		t.Errorf("base_sha = %v, want base0", d.BaseSHA)
	}
	if d.WorktreePath == nil || *d.WorktreePath != "/repo/d/.odo/worktrees/run-1" {
		t.Errorf("worktree_path = %v, want the run's worktree", d.WorktreePath)
	}
	// WorktreeRefs: the pending binding shows in both folds.
	referenced, pending, err := s.WorktreeRefs(ctx)
	if err != nil {
		t.Fatalf("WorktreeRefs: %v", err)
	}
	if !referenced["/repo/d/.odo/worktrees/run-1"] || !pending["/repo/d/.odo/worktrees/run-1"] {
		t.Errorf("refs = %v/%v, want the run-1 binding in both folds", referenced, pending)
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

func TestListAllPendingDiffs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pA, _ := s.CreateOrGetProject(ctx, "/repo/ri-a", "ri-a")
	pB, _ := s.CreateOrGetProject(ctx, "/repo/ri-b", "ri-b")

	w1, _ := s.CreateOrGetWorkstream(ctx, pA.ID, "main")
	w2, _ := s.CreateOrGetWorkstream(ctx, pA.ID, "feature-x")
	wB, _ := s.CreateOrGetWorkstream(ctx, pB.ID, "main")
	c1, _ := s.CreateConversation(ctx, w1.ID, "")
	c2, _ := s.CreateConversation(ctx, w2.ID, "")
	cB, _ := s.CreateConversation(ctx, wB.ID, "")

	d1a, err := s.InsertDiff(ctx, c1.ID, "/repo/ri-a/.odo/diffs/1.diff", "", "")
	if err != nil {
		t.Fatalf("InsertDiff d1a: %v", err)
	}
	d1b, err := s.InsertDiff(ctx, c1.ID, "/repo/ri-a/.odo/diffs/2.diff", "", "")
	if err != nil {
		t.Fatalf("InsertDiff d1b: %v", err)
	}
	d2, err := s.InsertDiff(ctx, c2.ID, "/repo/ri-a/.odo/diffs/3.diff", "", "")
	if err != nil {
		t.Fatalf("InsertDiff d2: %v", err)
	}
	// Noise: an accepted diff in w1 and a pending diff in the foreign project —
	// both must be excluded from pA's inbox.
	dAcc, err := s.InsertDiff(ctx, c1.ID, "/repo/ri-a/.odo/diffs/4.diff", "", "")
	if err != nil {
		t.Fatalf("InsertDiff dAcc: %v", err)
	}
	if err := s.UpdateDiffStatus(ctx, dAcc.ID, DiffAccepted); err != nil {
		t.Fatalf("UpdateDiffStatus: %v", err)
	}
	dB, err := s.InsertDiff(ctx, cB.ID, "/repo/ri-b/.odo/diffs/1.diff", "", "")
	if err != nil {
		t.Fatalf("InsertDiff dB: %v", err)
	}

	rows, err := s.ListAllPendingDiffs(ctx, pA.ID)
	if err != nil {
		t.Fatalf("ListAllPendingDiffs: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (accepted + foreign excluded): %+v", len(rows), rows)
	}
	// Order: w1's diffs by id, then w2's.
	wantIDs := []int64{d1a.ID, d1b.ID, d2.ID}
	wantWS := []struct {
		id   int64
		name string
	}{{w1.ID, "main"}, {w1.ID, "main"}, {w2.ID, "feature-x"}}
	for i, r := range rows {
		if r.ID != wantIDs[i] {
			t.Errorf("row %d ID = %d, want %d", i, r.ID, wantIDs[i])
		}
		if r.WorkstreamID != wantWS[i].id || r.WorkstreamName != wantWS[i].name {
			t.Errorf("row %d workstream = (%d,%q), want (%d,%q)",
				i, r.WorkstreamID, r.WorkstreamName, wantWS[i].id, wantWS[i].name)
		}
		if r.Status != DiffPending {
			t.Errorf("row %d status = %q, want pending", i, r.Status)
		}
		if r.ConversationID != c1.ID && r.ConversationID != c2.ID {
			t.Errorf("row %d conversation = %d, want %d or %d", i, r.ConversationID, c1.ID, c2.ID)
		}
	}
	for _, r := range rows {
		if r.ID == dB.ID {
			t.Errorf("foreign project diff %d leaked into pA inbox", dB.ID)
		}
	}

	// Empty project: no rows, no error.
	if rows, err := s.ListAllPendingDiffs(ctx, 4242); err != nil || len(rows) != 0 {
		t.Errorf("ListAllPendingDiffs on empty project = (%v, %v), want empty", rows, err)
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

// TestUpdateDiffBaseSHA (P0a): a successful refresh moves the diff's base
// pointer while the row stays pending — round-trips through GetDiff — and
// an unknown id reports ErrNoRows like every other diff update.
func TestUpdateDiffBaseSHA(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p, _ := s.CreateOrGetProject(ctx, "/repo/r", "r")
	w, _ := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	c, _ := s.CreateConversation(ctx, w.ID, "")

	d, err := s.InsertDiff(ctx, c.ID, "/repo/r/.odo/diffs/1.diff", "base0", "")
	if err != nil {
		t.Fatalf("InsertDiff: %v", err)
	}
	if d.BaseSHA == nil || *d.BaseSHA != "base0" {
		t.Fatalf("setup: base_sha = %v, want base0", d.BaseSHA)
	}

	if err := s.UpdateDiffBaseSHA(ctx, d.ID, "base1"); err != nil {
		t.Fatalf("UpdateDiffBaseSHA: %v", err)
	}
	got, err := s.GetDiff(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if got.BaseSHA == nil || *got.BaseSHA != "base1" {
		t.Errorf("base_sha after refresh = %v, want base1", got.BaseSHA)
	}
	if got.Status != DiffPending {
		t.Errorf("status after refresh = %q, want pending (only the base moves)", got.Status)
	}

	if err := s.UpdateDiffBaseSHA(ctx, 4242, "base1"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("update missing diff: err = %v, want sql.ErrNoRows", err)
	}
}
