// Package store implements the SQLite journal: the single source of truth
// for projects, workstreams, conversations, events, and diffs (ADR-0002).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, no CGO)
)

// Event types (ADR-0002, M0).
const (
	EventUserMessage     = "user_message"
	EventAgentText       = "agent_text"
	EventAgentThinking   = "agent_thinking"
	EventAgentToolCall   = "agent_tool_call"
	EventAgentToolResult = "agent_tool_result"
	EventAgentDone       = "agent_done"
	EventAgentError      = "agent_error"
	EventReviewAction    = "review_action"
	EventMemoryUpdate    = "memory_update"
	// /preview journals its headless capture facts (url, bytes, sha256 of
	// the PNG, wait_ms) as one receipt event after the slash user_message.
	EventPreviewCaptured = "preview_captured"
	// M19 (/loop): ONE discriminated event type for every loop row —
	// loop_started through loop_notified — split by the payload's kind
	// key (docs/design/loop-design-lock.md V1).
	EventLoopEvent = "loop_event"
	// Odo DX wave (Run/Test hub): run_command journals its outcome as a
	// run artifact (name, exit_code, output tails, duration) — journaled
	// BY DESIGN, unlike the k8s status pollers (cluster state never
	// enters the journal); a command's result is project history.
	EventCommandResult = "command_result"
	// P1 borrow (subagent wave): subagent runs journal in the PARENT
	// conversation with a subagent_id payload key on their agent events;
	// the lifecycle endpoints are these two dedicated types — spawned
	// carries {subagent_id, goal, worktree_path, parent_run_id?}, done
	// carries {subagent_id, goal, exit_code, summary?, diff_path?,
	// diff_id?, error?}.
	EventSubagentSpawned = "subagent_spawned"
	EventSubagentDone    = "subagent_done"
)

// Diff statuses.
const (
	DiffPending  = "pending"
	DiffAccepted = "accepted"
	DiffRejected = "rejected"
	// DiffConflict: an accept attempt failed mid-apply and the daemon
	// rolled the main checkout back to pre-accept state (I7). Terminal —
	// the diff stays out of the review queue; the run's worktree is
	// retired by the sweeper rules like any concluded row.
	DiffConflict = "conflict"
	// DiffSuperseded: a newer diff in the same revise chain landed,
	// making this older pending diff obsolete. NOT rejected — the diff
	// stays on disk for audit; it's just no longer actionable.
	DiffSuperseded = "superseded"
)

// Conversation states.
const (
	ConversationActive = "active"
)

// Workstream statuses.
const (
	WorkstreamActive = "active"
)

type Project struct {
	ID        int64  `json:"id"`
	RootPath  string `json:"root_path"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// Workstream: a named conversation lane. Schema v2 (detach-only, B-class
// workstream↔git design) dropped branch and worktree_path: workstreams own
// NO git refs (N:0), and worktree bindings live per-run on the diffs row
// the run produces (the single-slot column was the Q6 cardinality bug —
// every run overwrote it, so retire paths aimed at the wrong worktree).
type Workstream struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	// ForkedFrom (schema v5, turn-fork): the source conversation id when
	// this lane was created by fork_conversation. Populated ONLY by
	// ListWorkstreams' join (the sidebar's data source); plain row
	// fetches leave it nil — the canonical provenance lives on the
	// conversation row (Conversation.ForkedFrom).
	ForkedFrom *int64 `json:"forked_from,omitempty"`
}

type Conversation struct {
	ID            int64   `json:"id"`
	WorkstreamID  int64   `json:"workstream_id"`
	Epoch         int     `json:"epoch"`
	State         string  `json:"state"`
	BaseCommitSHA *string `json:"base_commit_sha,omitempty"`
	// ForkedFrom (schema v5): the conversation this one was fork-copied
	// from (turn-fork: a journal COPY of the source's prefix events,
	// never a move — the source conversation is untouched). NULL on
	// ordinary conversations.
	ForkedFrom *int64 `json:"forked_from,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// Event is one journaled entry. Payload is the raw JSON payload_json column,
// re-emitted verbatim in IPC responses.
type Event struct {
	ID             int64           `json:"id"`
	ConversationID int64           `json:"conversation_id"`
	Seq            int             `json:"seq"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      string          `json:"created_at"`
}

type Diff struct {
	ID             int64   `json:"id"`
	ConversationID int64   `json:"conversation_id"`
	PathOnDisk     string  `json:"path_on_disk"`
	BaseSHA        *string `json:"base_sha,omitempty"`
	// WorktreePath binds the producing run's worktree (schema v2, I8/I10):
	// the sweeper derives live/hold/reclaim decisions from these rows, and
	// reject/accept retires exactly this dir. NULL on pre-v2 rows — the
	// sweeper treats NULL as long-retired and never reclaims for them.
	WorktreePath *string `json:"worktree_path,omitempty"`
	// Goal is the producing run's review objective, verbatim (schema v3):
	// the panel judges the diff against THIS, never against whatever the
	// newest human message in the conversation happens to be at review
	// time (the 2026-08-22 #34 false objective-mismatch rejection). For a
	// revise-chain product it is the chain's origin goal. NULL on pre-v3
	// rows — readers fall back to conversation-derived originGoal.
	Goal      *string `json:"goal,omitempty"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
	// SubagentID (schema v5): non-empty marks a SUBAGENT diff — a
	// proposal produced by an isolated spawn_subagent run and journaled
	// back into the parent conversation. It is never an auto-land
	// candidate (recoverPendingDiffs excludes it; the pipeline's
	// "not auto-landed" contract) — the parent conversation's human or
	// agent decides via the ordinary accept/reject path.
	SubagentID *string `json:"subagent_id,omitempty"`
}

// Store owns the journal database. SQLite is opened with a single
// connection (a DB-handle choice — all access serializes on it regardless
// of who calls; the IPC layer is goroutine-per-connection since M11).
type Store struct {
	db *sql.DB
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
    id          INTEGER PRIMARY KEY,
    root_path   TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS workstreams (
    id            INTEGER PRIMARY KEY,
    project_id    INTEGER NOT NULL REFERENCES projects(id),
    name          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS conversations (
    id              INTEGER PRIMARY KEY,
    workstream_id   INTEGER NOT NULL REFERENCES workstreams(id),
    epoch           INTEGER NOT NULL DEFAULT 1,
    state           TEXT NOT NULL DEFAULT 'active',
    base_commit_sha TEXT,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS events (
    id              INTEGER PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    seq             INTEGER NOT NULL,
    type            TEXT NOT NULL,
    payload_json    TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(conversation_id, seq)
);

CREATE TABLE IF NOT EXISTS diffs (
    id              INTEGER PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    path_on_disk    TEXT NOT NULL,
    base_sha        TEXT,
    worktree_path   TEXT,
    goal            TEXT,
    status          TEXT NOT NULL DEFAULT 'pending',
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);
`

// Open opens (creating if needed) the journal at path and applies migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create db dir: %w", err)
	}
	// mmap_size(0): the journaled therapy of record for the recurring
	// modernc.org/sqlite WAL-recovery SIGBUS (UI-epoch-10/11,
	// bug-fix-epoch-4) — therapy, not a proven root-cause fix; the reader
	// path below disables mmap for the same crash class. synchronous(FULL)
	// makes commit durability explicit instead of relying on build defaults.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=mmap_size(0)&_pragma=synchronous(FULL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// One connection => serialized writes, no "database is locked" under WAL.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the journal.
func (s *Store) Close() error {
	return s.db.Close()
}

// OpenReadOnly opens the journal at path for pure reads (`odo journal`
// CLI): no directory creation, no migrations, query_only — so it can never
// mutate a journal a live daemon owns. Errors when the file is absent.
func OpenReadOnly(path string) (*Store, error) {
	// mmap_size(0) here too — the CLI reader maps the same live WAL the
	// daemon writes, so it shares the SIGBUS crash class (Open above).
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)&_pragma=mmap_size(0)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	// schemaV1 (kept name; its DDL is the CURRENT shape through v4 —
	// v5's conversations.forked_from / diffs.subagent_id arrive via
	// migrateV5 for every database: fresh seeds record version 3 so
	// v4's index must still apply, which leaves v5's ALTERs their one
	// uniform add path) creates fresh databases at v3.
	// Existing v1/v2 journals upgrade via migrateV2/V3.
	if _, err := db.Exec(schemaV1); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	var version int
	err := db.QueryRow(`SELECT version FROM schema_version ORDER BY rowid DESC LIMIT 1`).Scan(&version)
	if err == sql.ErrNoRows {
		if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES (3)`); err != nil {
			return fmt.Errorf("store: migrate: record version: %w", err)
		}
		version = 3 // fall through: fresh databases get v4's index below
	} else if err != nil {
		return fmt.Errorf("store: migrate: read schema_version: %w", err)
	}
	if version < 2 {
		if err := migrateV2(db); err != nil {
			return err
		}
	}
	if version < 3 {
		if err := migrateV3(db); err != nil {
			return err
		}
	}
	if version < 4 {
		if err := migrateV4(db); err != nil {
			return err
		}
	}
	if version < 5 {
		if err := migrateV5(db); err != nil {
			return err
		}
	}
	return nil
}

// migrateV5 (P1 borrow, turn-fork + subagent waves): conversations gain
// forked_from (the fork provenance; NULL on ordinary lanes), diffs gain
// subagent_id (NULL = regular run diff; non-empty marks a subagent
// proposal the auto-land recovery must never pipeline). Atomic like
// V2/V3: a crash retries the whole upgrade.
func migrateV5(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: migrate v5: begin: %w", err)
	}
	defer tx.Rollback()
	stmts := []string{
		`ALTER TABLE conversations ADD COLUMN forked_from INTEGER REFERENCES conversations(id)`,
		`ALTER TABLE diffs ADD COLUMN subagent_id TEXT`,
		`UPDATE schema_version SET version = 5`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("store: migrate v5: %s: %w", q, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migrate v5: commit: %w", err)
	}
	return nil
}

// migrateV4 (2026-08-25 audit P2): active workstream names gain a partial
// unique index — create/rename were check-then-write, so two racing IPC
// goroutines could both pass the SELECT and write the same active name.
// The index is NOT in schemaV1's unconditional DDL: an existing database
// may carry the race's fossil duplicates, and those must be renamed
// (newest id keeps the name, older rows take a -dup-<id> suffix) before
// the index can build. Atomic like V2/V3: a crash retries the upgrade.
//
// Collision-free rename (2026-08-25 review follow-up): -dup-<id> can
// itself collide with a legitimately named active row — "main"
// duplicated beside an existing "main-dup-2" would rename the loser
// straight INTO the taken name, the CREATE UNIQUE INDEX would fail, and
// every subsequent Open would wedge on the migration. Names are computed
// in Go against the taken set (duplicates are a rarity; per-row updates
// are cheap), with a -dup-<id>-<n> ladder on collision.
func migrateV4(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: migrate v4: begin: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, project_id, name FROM workstreams WHERE status = 'active' ORDER BY id`)
	if err != nil {
		return fmt.Errorf("store: migrate v4: scan workstreams: %w", err)
	}
	type wsRow struct {
		id, projectID int64
		name          string
	}
	type wsName struct {
		projectID int64
		name      string
	}
	var active []wsRow
	for rows.Next() {
		var r wsRow
		if err := rows.Scan(&r.id, &r.projectID, &r.name); err != nil {
			rows.Close()
			return fmt.Errorf("store: migrate v4: scan row: %w", err)
		}
		active = append(active, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("store: migrate v4: rows: %w", err)
	}
	rows.Close()
	// taken = every active (project, name); keeper = the newest id per
	// group (it keeps the plain name). A loser's old name stays taken —
	// the keeper still holds it.
	taken := map[wsName]bool{}
	keeper := map[wsName]int64{}
	for _, r := range active {
		k := wsName{r.projectID, r.name}
		taken[k] = true
		if keeper[k] < r.id {
			keeper[k] = r.id
		}
	}
	for _, r := range active {
		if keeper[wsName{r.projectID, r.name}] == r.id {
			continue // newest of the group (or the only row) keeps the name
		}
		cand := fmt.Sprintf("%s-dup-%d", r.name, r.id)
		for n := 1; taken[wsName{r.projectID, cand}]; n++ {
			cand = fmt.Sprintf("%s-dup-%d-%d", r.name, r.id, n)
		}
		if _, err := tx.Exec(`UPDATE workstreams SET name = ? WHERE id = ?`, cand, r.id); err != nil {
			return fmt.Errorf("store: migrate v4: dedupe workstream %d: %w", r.id, err)
		}
		taken[wsName{r.projectID, cand}] = true
	}
	stmts := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_workstreams_active_name ON workstreams(project_id, name) WHERE status = 'active'`,
		`UPDATE schema_version SET version = 4`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("store: migrate v4: %s: %w", q, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migrate v4: commit: %w", err)
	}
	return nil
}

// migrateV3 (review objective-anchor fix, 2026-08-22): diffs gain the
// producing run's verbatim goal so review anchors to the diff's own
// provenance instead of the conversation's newest human message. Pre-v3
// rows stay NULL; readers fall back to the conversation-derived goal.
// Atomic: a crash between statements retries the whole upgrade next boot.
func migrateV3(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: migrate v3: begin: %w", err)
	}
	defer tx.Rollback()
	stmts := []string{
		`ALTER TABLE diffs ADD COLUMN goal TEXT`,
		`UPDATE schema_version SET version = 3`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("store: migrate v3: %s: %w", q, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migrate v3: commit: %w", err)
	}
	return nil
}

// migrateV2 (B-class workstream↔git redesign, I8/I10): per-run worktree
// binding moves onto the diffs row; workstreams drop the branch and
// single-slot worktree_path columns (workstreams own no git refs, N:0).
// Pre-v2 diffs keep worktree_path NULL — the sweeper treats NULL as
// long-retired and never reclaims on their behalf. Atomic: a crash between
// statements retries the whole upgrade next boot.
func migrateV2(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: migrate v2: begin: %w", err)
	}
	defer tx.Rollback()
	stmts := []string{
		`ALTER TABLE diffs ADD COLUMN worktree_path TEXT`,
		`ALTER TABLE workstreams DROP COLUMN branch`,
		`ALTER TABLE workstreams DROP COLUMN worktree_path`,
		`UPDATE schema_version SET version = 2`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("store: migrate v2: %s: %w", q, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migrate v2: commit: %w", err)
	}
	return nil
}

// nullString returns nil (SQL NULL) for an empty string, else the string.
func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// scanWorkstream scans a full workstreams row.
func scanWorkstream(row interface{ Scan(...interface{}) error }) (Workstream, error) {
	var w Workstream
	err := row.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Status, &w.CreatedAt)
	return w, err
}

// scanWorkstreamForked scans a ListWorkstreams row (the plain columns plus
// the joined forked_from provenance column).
func scanWorkstreamForked(row interface{ Scan(...interface{}) error }) (Workstream, error) {
	var w Workstream
	err := row.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Status, &w.CreatedAt, &w.ForkedFrom)
	return w, err
}

// scanConversation scans a full conversations row.
func scanConversation(row interface{ Scan(...interface{}) error }) (Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.WorkstreamID, &c.Epoch, &c.State, &c.BaseCommitSHA, &c.ForkedFrom, &c.CreatedAt)
	return c, err
}

// DB exposes the underlying handle for tests.
func (s *Store) DB() *sql.DB { return s.db }

// Ping verifies the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
