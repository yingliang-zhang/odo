package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

// TestMigrateV4DedupesActiveNames (2026-08-25 audit P2): a pre-v4 journal
// carrying the check-then-write race's fossil duplicates must come up with
// every active name distinct (newest id keeps the name, older rows take a
// -dup-<id> suffix) and the partial unique index enforcing it — including
// for the soft-delete-then-recreate lifecycle the partial WHERE allows.
func TestMigrateV4DedupesActiveNames(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "journal.sqlite")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(schemaV1); err != nil {
		t.Fatalf("raw schema: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_version (version) VALUES (3)`); err != nil {
		t.Fatalf("raw version: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO projects (root_path, name) VALUES ('/p', 'p')`); err != nil {
		t.Fatalf("raw project: %v", err)
	}
	// Fossil duplicates: two ACTIVE "main" rows and one deleted "main"
	// (the deleted one is a red herring — the index is partial).
	for _, stmt := range []string{
		`INSERT INTO workstreams (project_id, name) VALUES (1, 'main')`,
		`INSERT INTO workstreams (project_id, name) VALUES (1, 'main')`,
		`INSERT INTO workstreams (project_id, name, status) VALUES (1, 'main', 'deleted')`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("raw workstream: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (migrate v4): %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	ws, err := s.ListWorkstreams(ctx, 1)
	if err != nil {
		t.Fatalf("ListWorkstreams: %v", err)
	}
	if len(ws) != 2 {
		t.Fatalf("active workstreams = %d, want 2 (deleted row untouched)", len(ws))
	}
	names := map[string]bool{}
	for _, w := range ws {
		if names[w.Name] {
			t.Fatalf("duplicate active name survived migration: %+v", ws)
		}
		names[w.Name] = true
	}
	// The newest active row (highest id) keeps the plain name.
	if ws[1].Name != "main" && ws[0].Name != "main" {
		t.Errorf("no row kept the plain name: %+v", ws)
	}
	// Enforcement: a duplicate ACTIVE insert fails at the constraint now.
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO workstreams (project_id, name) VALUES (1, 'main')`); err == nil {
		t.Error("duplicate active insert succeeded — index not enforcing")
	}
	// But the partial WHERE keeps the delete-then-recreate lifecycle legal.
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO workstreams (project_id, name, status) VALUES (1, 'main', 'deleted')`); err != nil {
		t.Errorf("deleted-status insert failed: %v", err)
	}
	var version int
	if err := s.DB().QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if version != 4 {
		t.Errorf("schema_version = %d, want 4", version)
	}
}

// TestMigrateV4DupSuffixCollision (2026-08-25 review follow-up): the dedupe
// suffix -dup-<id> must not march into a name a THIRD row already holds —
// "main" duplicated beside a legitimate "main-dup-2" used to rename the
// loser onto the taken name, failing CREATE UNIQUE INDEX and wedging every
// subsequent Open on the migration. The ladder takes the next free suffix.
func TestMigrateV4DupSuffixCollision(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "journal.sqlite")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(schemaV1); err != nil {
		t.Fatalf("raw schema: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_version (version) VALUES (3)`); err != nil {
		t.Fatalf("raw version: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO projects (root_path, name) VALUES ('/p', 'p')`); err != nil {
		t.Fatalf("raw project: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO workstreams (project_id, name) VALUES (1, 'main')`,       // id 1: dup loser
		`INSERT INTO workstreams (project_id, name) VALUES (1, 'main')`,       // id 2: dup keeper
		`INSERT INTO workstreams (project_id, name) VALUES (1, 'main-dup-2')`, // id 3: pre-takes the naive suffix
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("raw workstream: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	s, err := Open(dbPath) // must NOT wedge on the CREATE UNIQUE INDEX
	if err != nil {
		t.Fatalf("Open (migrate v4): %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	ws, err := s.ListWorkstreams(ctx, 1)
	if err != nil {
		t.Fatalf("ListWorkstreams: %v", err)
	}
	if len(ws) != 3 {
		t.Fatalf("active workstreams = %+v, want all 3", ws)
	}
	byID := map[int64]string{}
	for _, w := range ws {
		byID[w.ID] = w.Name
	}
	if byID[2] != "main" {
		t.Errorf("keeper id 2 = %q, want the plain %q", byID[2], "main")
	}
	if byID[3] != "main-dup-2" {
		t.Errorf("unrelated id 3 = %q, want untouched %q", byID[3], "main-dup-2")
	}
	if byID[1] == "main" || byID[1] == "main-dup-2" || byID[1] == "" {
		t.Errorf("loser id 1 = %q, want a non-colliding -dup- ladder name", byID[1])
	}
	names := map[string]bool{}
	for _, w := range ws {
		if names[w.Name] {
			t.Errorf("duplicate active name survived migration: %+v", ws)
		}
		names[w.Name] = true
	}
}

// TestCreateOrGetWorkstreamRaceSameName (2026-08-25 audit P2): concurrent
// create-or-get with one name converges on exactly ONE active workstream —
// the v4 constraint plus the losing caller's re-read makes the old
// check-then-write double-insert impossible.
func TestCreateOrGetWorkstreamRaceSameName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p, err := s.CreateOrGetProject(ctx, "/race-project", "race")
	if err != nil {
		t.Fatalf("CreateOrGetProject: %v", err)
	}
	const callers = 8
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateOrGetWorkstream(ctx, p.ID, "raced")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent create-or-get: %v", err)
		}
	}
	ws, err := s.ListWorkstreams(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListWorkstreams: %v", err)
	}
	if len(ws) != 1 || ws[0].Name != "raced" {
		t.Fatalf("active workstreams = %+v, want exactly one %q", ws, "raced")
	}
}
