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
)

// Diff statuses.
const (
	DiffPending  = "pending"
	DiffAccepted = "accepted"
	DiffRejected = "rejected"
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

type Workstream struct {
	ID           int64   `json:"id"`
	ProjectID    int64   `json:"project_id"`
	Name         string  `json:"name"`
	Branch       *string `json:"branch,omitempty"`
	WorktreePath *string `json:"worktree_path,omitempty"`
	Status       string  `json:"status"`
	CreatedAt    string  `json:"created_at"`
}

type Conversation struct {
	ID            int64   `json:"id"`
	WorkstreamID  int64   `json:"workstream_id"`
	Epoch         int     `json:"epoch"`
	State         string  `json:"state"`
	BaseCommitSHA *string `json:"base_commit_sha,omitempty"`
	CreatedAt     string  `json:"created_at"`
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
	Status         string  `json:"status"`
	CreatedAt      string  `json:"created_at"`
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
    branch        TEXT,
    worktree_path TEXT,
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
    status          TEXT NOT NULL DEFAULT 'pending',
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);
`

// Open opens (creating if needed) the journal at path and applies migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create db dir: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
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
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)", path)
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
	if _, err := db.Exec(schemaV1); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&n); err != nil {
		return fmt.Errorf("store: migrate: read schema_version: %w", err)
	}
	if n == 0 {
		if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES (1)`); err != nil {
			return fmt.Errorf("store: migrate: record version: %w", err)
		}
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
	err := row.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Branch, &w.WorktreePath, &w.Status, &w.CreatedAt)
	return w, err
}

// scanConversation scans a full conversations row.
func scanConversation(row interface{ Scan(...interface{}) error }) (Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.WorkstreamID, &c.Epoch, &c.State, &c.BaseCommitSHA, &c.CreatedAt)
	return c, err
}

// DB exposes the underlying handle for tests.
func (s *Store) DB() *sql.DB { return s.db }

// Ping verifies the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
