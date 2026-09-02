package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestMigrateV5AddsColumns (schema v5, turn-fork + subagent waves): a v4
// journal gains conversations.forked_from and diffs.subagent_id via
// migrateV5, records version 5, and a fresh journal lands the same two
// columns through the same migration path.
func TestMigrateV5AddsColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "journal.sqlite")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(schemaV1); err != nil {
		t.Fatalf("raw schema: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_version (version) VALUES (4)`); err != nil {
		t.Fatalf("raw version: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO projects (root_path, name) VALUES ('/p', 'p')`); err != nil {
		t.Fatalf("raw project: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	s, err := Open(dbPath) // migrateV5 runs here
	if err != nil {
		t.Fatalf("Open (migrate v5): %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	for _, tc := range []struct{ table, col string }{
		{"conversations", "forked_from"},
		{"diffs", "subagent_id"},
	} {
		var n int
		if err := s.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, tc.table, tc.col).Scan(&n); err != nil {
			t.Fatalf("pragma %s: %v", tc.table, err)
		}
		if n != 1 {
			t.Errorf("%s.%s missing after migrateV5", tc.table, tc.col)
		}
	}
	var version int
	if err := s.DB().QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if version != 5 {
		t.Errorf("schema_version = %d, want 5", version)
	}

	// Fresh journals land the same columns (openTestStore → Open → v5 runs).
	fresh := openTestStore(t)
	defer fresh.Close()
	var n int
	if err := fresh.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('conversations') WHERE name = 'forked_from'`).Scan(&n); err != nil {
		t.Fatalf("pragma fresh: %v", err)
	}
	if n != 1 {
		t.Error("fresh journal missing conversations.forked_from — migrateV5 did not run")
	}
}

// TestForkConversationCopiesPrefix is the turn-fork store contract: the
// fork copies seq 1..fromSeq VERBATIM (same type, same payload), the
// source lane keeps its full journal untouched, and the new row carries
// the provenance (forked_from + base SHA).
func TestForkConversationCopiesPrefix(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	p, err := s.CreateOrGetProject(ctx, "/fork-project", "fork")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	w, err := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatalf("workstream: %v", err)
	}
	src, err := s.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	payloads := []string{
		`{"text":"first goal"}`,
		`{"text":"agent reply one"}`,
		`{"text":"second goal"}`,
		`{"text":"agent reply two"}`,
	}
	types := []string{EventUserMessage, EventAgentText, EventUserMessage, EventAgentText}
	for i, pj := range payloads {
		if _, err := s.AppendEvent(ctx, src.ID, types[i], pj); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	fw, err := s.CreateOrGetWorkstream(ctx, p.ID, "main-fork-1")
	if err != nil {
		t.Fatalf("fork workstream: %v", err)
	}
	dst, copied, err := s.ForkConversation(ctx, src.ID, 2, fw.ID, "deadbeefcafe")
	if err != nil {
		t.Fatalf("ForkConversation: %v", err)
	}
	if copied != 2 {
		t.Errorf("copied = %d, want 2", copied)
	}
	if dst.ID == src.ID {
		t.Error("fork returned the source conversation's id")
	}
	if dst.ForkedFrom == nil || *dst.ForkedFrom != src.ID {
		t.Errorf("forked_from = %v, want %d", dst.ForkedFrom, src.ID)
	}
	if dst.BaseCommitSHA == nil || *dst.BaseCommitSHA != "deadbeefcafe" {
		t.Errorf("base sha = %v, want deadbeefcafe", dst.BaseCommitSHA)
	}
	if dst.Epoch != 1 {
		t.Errorf("epoch = %d, want 1 (forks start a fresh epoch)", dst.Epoch)
	}

	// The new journal holds EXACTLY the prefix, payloads byte-identical.
	evs, err := s.ListEvents(ctx, dst.ID, 0)
	if err != nil {
		t.Fatalf("list fork events: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("fork journal = %d events, want the 2-row prefix", len(evs))
	}
	for i, ev := range evs {
		if ev.Seq != i+1 {
			t.Errorf("fork seq = %d at index %d, want %d", ev.Seq, i, i+1)
		}
		if ev.Type != types[i] {
			t.Errorf("fork type = %q at index %d, want %q", ev.Type, i, types[i])
		}
		if string(ev.Payload) != payloads[i] {
			t.Errorf("fork payload = %s at index %d, want %s", ev.Payload, i, payloads[i])
		}
	}

	// The source lane keeps its FULL journal — nothing moved, nothing edited.
	srcEvs, err := s.ListEvents(ctx, src.ID, 0)
	if err != nil {
		t.Fatalf("list source events: %v", err)
	}
	if len(srcEvs) != 4 {
		t.Fatalf("source journal = %d events after fork, want the untouched 4", len(srcEvs))
	}

	// Post-fork writes land independently in each journal.
	if _, err := s.AppendEvent(ctx, dst.ID, EventUserMessage, `{"text":"fork continues"}`); err != nil {
		t.Fatalf("append to fork: %v", err)
	}
	dstEvs, _ := s.ListEvents(ctx, dst.ID, 0)
	if len(dstEvs) != 3 || dstEvs[2].Seq != 3 {
		t.Errorf("fork append: journal = %d rows, tail seq %d — want 3 rows, tail seq 3", len(dstEvs), dstEvs[len(dstEvs)-1].Seq)
	}
	srcEvs, _ = s.ListEvents(ctx, src.ID, 0)
	if len(srcEvs) != 4 {
		t.Errorf("fork write leaked into the source journal: %d rows", len(srcEvs))
	}
}

// TestForkConversationBoundaryRefusals: from_seq below 1 and past the
// journal's end refuse without writing any fork state (one transaction).
func TestForkConversationBoundaryRefusals(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	p, err := s.CreateOrGetProject(ctx, "/fork-refusal", "forkr")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	w, err := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatalf("workstream: %v", err)
	}
	src, err := s.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	for range 2 {
		if _, err := s.AppendEvent(ctx, src.ID, EventUserMessage, `{"text":"m"}`); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	fw, err := s.CreateOrGetWorkstream(ctx, p.ID, "main-fork-1")
	if err != nil {
		t.Fatalf("fork workstream: %v", err)
	}

	if _, _, err := s.ForkConversation(ctx, src.ID, 0, fw.ID, ""); err == nil {
		t.Error("from_seq 0 forked — below-floor refusal missing")
	}
	if _, _, err := s.ForkConversation(ctx, src.ID, 3, fw.ID, ""); err == nil {
		t.Error("from_seq 3 forked a 2-row journal — end-boundary refusal missing")
	}
	// Nothing landed: the lane holds no conversation at all.
	if _, err := s.GetActiveConversation(ctx, fw.ID); err == nil {
		t.Error("refused forks still wrote a conversation row")
	}
	// Missing source refuses too.
	if _, _, err := s.ForkConversation(ctx, 999, 1, fw.ID, ""); err == nil {
		t.Error("forked from a nonexistent conversation")
	}
}

// TestInsertSubagentDiffRidesReviewerSurface: a subagent diff is a normal
// pending row on every review surface (Get/List/ListPending/ListAll) with
// its provenance marker intact; InsertDiff rows stay NULL (no marker).
func TestInsertSubagentDiffRidesReviewerSurface(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	p, err := s.CreateOrGetProject(ctx, "/sub-project", "sub")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	w, err := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatalf("workstream: %v", err)
	}
	c, err := s.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	regular, err := s.InsertDiff(ctx, c.ID, "/tmp/a.diff", "base1", "/tmp/wt1", "goal a")
	if err != nil {
		t.Fatalf("InsertDiff: %v", err)
	}
	sub, err := s.InsertSubagentDiff(ctx, c.ID, "/tmp/b.diff", "base2", "/tmp/wt2", "sub goal", "sub-1")
	if err != nil {
		t.Fatalf("InsertSubagentDiff: %v", err)
	}
	if sub.SubagentID == nil || *sub.SubagentID != "sub-1" {
		t.Errorf("subagent_id = %v, want sub-1", sub.SubagentID)
	}
	if regular.SubagentID != nil {
		t.Errorf("regular diff marked subagent: %v", regular.SubagentID)
	}
	got, err := s.GetDiff(ctx, sub.ID)
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if got.SubagentID == nil || *got.SubagentID != "sub-1" {
		t.Errorf("GetDiff lost the marker: %+v", got)
	}
	pending, err := s.ListPendingDiffs(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListPendingDiffs: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d rows, want both diffs listed", len(pending))
	}
	all, err := s.ListAllPendingDiffs(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListAllPendingDiffs: %v", err)
	}
	var subRow *PendingDiffRow
	for i := range all {
		if all[i].ID == sub.ID {
			subRow = &all[i]
		}
	}
	if subRow == nil || subRow.SubagentID == nil || *subRow.SubagentID != "sub-1" {
		t.Errorf("cross-workstream inbox row lost the marker: %+v", all)
	}
	if _, err := s.InsertSubagentDiff(ctx, c.ID, "/tmp/c.diff", "", "", "", ""); err == nil {
		t.Error("InsertSubagentDiff with an empty marker succeeded")
	}
}

// TestListWorkstreamsForkProvenance: the sidebar's list query surfaces
// forked_from on fork-created lanes only.
func TestListWorkstreamsForkProvenance(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	ctx := context.Background()
	p, err := s.CreateOrGetProject(ctx, "/fork-list", "forkl")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	w, err := s.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatalf("workstream: %v", err)
	}
	src, err := s.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if _, err := s.AppendEvent(ctx, src.ID, EventUserMessage, `{"text":"m"}`); err != nil {
		t.Fatalf("append: %v", err)
	}
	fw, err := s.CreateOrGetWorkstream(ctx, p.ID, "main-fork-1")
	if err != nil {
		t.Fatalf("fork workstream: %v", err)
	}
	if _, _, err := s.ForkConversation(ctx, src.ID, 1, fw.ID, ""); err != nil {
		t.Fatalf("fork: %v", err)
	}
	ws, err := s.ListWorkstreams(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListWorkstreams: %v", err)
	}
	if len(ws) != 2 {
		t.Fatalf("lanes = %d, want 2", len(ws))
	}
	byID := map[int64]Workstream{}
	for _, lane := range ws {
		byID[lane.ID] = lane
	}
	if byID[w.ID].ForkedFrom != nil {
		t.Errorf("plain lane carries provenance: %+v", byID[w.ID])
	}
	if byID[fw.ID].ForkedFrom == nil || *byID[fw.ID].ForkedFrom != src.ID {
		t.Errorf("fork lane provenance = %v, want %d", byID[fw.ID].ForkedFrom, src.ID)
	}
	// json wire shape: forked_from rides the row, omitted when NULL.
	wb, _ := json.Marshal(byID[w.ID])
	fb, _ := json.Marshal(byID[fw.ID])
	if json.Valid(wb) == false || json.Valid(fb) == false {
		t.Fatal("wire marshal broke")
	}
	if contains := string(fb); !jsonContains(fb, "forked_from") {
		t.Errorf("fork lane wire row lacks forked_from: %s", contains)
	}
	if jsonContains(wb, "forked_from") {
		t.Errorf("plain lane wire row carries forked_from: %s", string(wb))
	}
}

func jsonContains(b []byte, key string) bool {
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
