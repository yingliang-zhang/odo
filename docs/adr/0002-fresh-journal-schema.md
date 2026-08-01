# ADR-0002: Fresh Odo Journal Schema

## Status

Accepted — 2026-08-01

## Context

The predecessor project (Ananke) accumulated 15 SQLite migrations over 15 days,
building up attestation, transcript-identity, outbox, and grill tables. The
schema carried P6 hash bindings that constrained the event types.

Odo starts with a fresh schema designed for the confirmed M0 scope: conversations
as journaled events, worktree isolation, diff tracking, and session restoration.

## Decision

Fresh schema, 5 tables, no inheritance from Ananke:

```sql
CREATE TABLE projects (
    id          INTEGER PRIMARY KEY,
    root_path   TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE workstreams (
    id            INTEGER PRIMARY KEY,
    project_id    INTEGER NOT NULL REFERENCES projects(id),
    name          TEXT NOT NULL,
    branch        TEXT,
    worktree_path TEXT,
    status        TEXT NOT NULL DEFAULT 'active',
    created_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE conversations (
    id              INTEGER PRIMARY KEY,
    workstream_id   INTEGER NOT NULL REFERENCES workstreams(id),
    epoch           INTEGER NOT NULL DEFAULT 1,
    state           TEXT NOT NULL DEFAULT 'active',
    base_commit_sha TEXT,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE events (
    id              INTEGER PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    seq             INTEGER NOT NULL,
    type            TEXT NOT NULL,
    payload_json    TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(conversation_id, seq)
);

CREATE TABLE diffs (
    id              INTEGER PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id),
    path_on_disk    TEXT NOT NULL,
    base_sha        TEXT,
    status          TEXT NOT NULL DEFAULT 'pending',
    created_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);
```

## Event types (M0)

| Type | Payload | When |
|---|---|---|
| `user_message` | `{"text": "..."}` | User sends a message |
| `agent_text` | `{"text": "..."}` | Agent emits text output |
| `agent_tool_call` | `{"tool": "...", "args": "..."}` | Agent calls a tool |
| `agent_tool_result` | `{"tool": "...", "result": "..."}` | Tool returns output |
| `agent_done` | `{"summary": "..."}` | Agent completes |
| `agent_error` | `{"error": "..."}` | Agent fails |
| `review_action` | `{"action": "accept"/"reject", "diff_id": N}` | User accepts/rejects |

## Alternatives

### Reuse Ananke schema (v14)

Rejected. 15 migrations of accumulated complexity, P6 hash bindings, and tables
with no purpose in Odo (attestation, outbox, transcript-identity, grill).
Retrofitting is 10× the cost of a fresh schema.

### Embed conversations as JSON blobs in events

Rejected. Conversations need typed queries, monotonic per-run sequence,
crash-safe commits, and reconnection via `afterSeq` — all of which require
relational structure, not opaque blobs.

## Consequences

- No attestation/outbox/transcript tables until M1+ adds them behind an ADR
- Event type namespace is open — new types added as features ship
- `base_commit_sha` on conversations enables stale-diff detection at accept time
- Worktree path on workstreams (not conversations) — one binding point per
  workstream, no per-chat drift
- `events` table is append-only with per-conversation monotonic `seq`
