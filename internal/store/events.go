package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// AppendEvent journals an event with the next per-conversation sequence
// number (monotonic, gap-free: the store's single sqlite connection
// serializes seq allocation even under M11's goroutine-per-connection
// IPC serving). payloadJSON must be valid JSON; an empty payload is
// stored as "{}".
func (s *Store) AppendEvent(ctx context.Context, conversationID int64, eventType, payloadJSON string) (Event, error) {
	if payloadJSON == "" {
		payloadJSON = "{}"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("store: append event: begin: %w", err)
	}
	defer tx.Rollback()

	var seq int
	err = tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM events WHERE conversation_id = ?`,
		conversationID).Scan(&seq)
	if err != nil {
		return Event{}, fmt.Errorf("store: append event: next seq: %w", err)
	}

	e := Event{
		ConversationID: conversationID,
		Seq:            seq,
		Type:           eventType,
		Payload:        json.RawMessage(payloadJSON),
	}
	err = tx.QueryRowContext(ctx,
		`INSERT INTO events (conversation_id, seq, type, payload_json)
		 VALUES (?, ?, ?, ?)
		 RETURNING id, created_at`,
		conversationID, seq, eventType, payloadJSON).Scan(&e.ID, &e.CreatedAt)
	if err != nil {
		return Event{}, fmt.Errorf("store: append event: insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("store: append event: commit: %w", err)
	}
	return e, nil
}

// ListEvents returns events for a conversation with seq > afterSeq,
// ordered by seq ascending. Pass afterSeq=0 for the full history.
func (s *Store) ListEvents(ctx context.Context, conversationID int64, afterSeq int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, seq, type, payload_json, created_at
		 FROM events
		 WHERE conversation_id = ? AND seq > ?
		 ORDER BY seq ASC`, conversationID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.ID, &e.ConversationID, &e.Seq, &e.Type, &payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: list events: scan: %w", err)
		}
		e.Payload = json.RawMessage(payload)
		events = append(events, e)
	}
	return events, rows.Err()
}

// ListProjectEventsPage returns one page of events across all
// conversations of a project (workstreams of any status, conversations of
// any state), in global journal order (row id — the single-connection
// insertion order, the only total order comparable across conversations;
// per-conversation seqs are not): rows with e.id > afterID (pass 0 to
// start at the journal head), oldest first, capped at limit. limit must
// be positive: there is deliberately NO unbounded full-list path — the
// one-shot listing this replaced materialized every payload of a
// long-lived project's journal per boot, so a limit <= 0 call is a
// programming error, refused rather than silently resurrecting that
// one-shot behavior. Keyset pagination (e.id > ?), never
// OFFSET: page k costs the same regardless of journal length, and a row
// appended mid-scan can never duplicate or displace a row already
// returned.
//
// Paged by construction (2026-08-26 K3 hygiene): the boot-time
// memory-intent replayer folds the journal to pick each layer's newest
// receipt, and that reduce is streaming — the one-shot full listing it
// replaced materialized every payload of a long-lived project's journal
// in memory at every boot. There is deliberately no unbounded full-list
// API; consumers that fold the whole journal page through this.
//
// A receipt on an archived/soft-deleted lane keeps folding, but NOT
// because of the JOIN flavor (round-4 FIX 5): the WHERE predicate
// filters a right-side column (w.project_id = ?), which collapses LEFT
// to INNER whenever a lane row exists — and drops the events row under
// either form when it doesn't. The archived-lane guarantee comes from
// the data lifecycle, stated plainly: workstream delete is a STATUS
// flip, conversation rotation never retires the old row, and these
// joins carry NO status predicate — every surviving lane row still
// produces its events here. The coverage boundary stands: events whose
// lane rows were DESTROYED (a hard cascade-delete of
// conversations/workstreams, or `odo journal rotate` moving the whole
// SQLite file out from under the store) are unrecoverable by
// construction — the journal is the outbox, and a destroyed journal row
// has no replay source. No such path exists today (delete is soft,
// rotate takes the daemon offline first).
func (s *Store) ListProjectEventsPage(ctx context.Context, projectID, afterID int64, limit int) ([]Event, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store: list project events page: limit must be positive, got %d", limit)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, e.conversation_id, e.seq, e.type, e.payload_json, e.created_at
		 FROM events e
		 LEFT JOIN conversations c ON e.conversation_id = c.id
		 LEFT JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ? AND e.id > ?
		 ORDER BY e.id ASC
		 LIMIT ?`, projectID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list project events page: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.ID, &e.ConversationID, &e.Seq, &e.Type, &payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: list project events page: scan: %w", err)
		}
		e.Payload = json.RawMessage(payload)
		events = append(events, e)
	}
	return events, rows.Err()
}

// ListHealLedgerRows returns the project's heal_conflict/heal_resolved
// memory_update rows in global journal order — the stranded_memory_ops
// fold's input. Payload LIKE filters (the markers.go convention) keep the
// poll cheap: these rows are rare marker rows, and the alternative is
// re-folding the full journal on every pending_counts tick. An open
// conflict journaled on an archived/soft-deleted lane still counts and
// still resolves — for the same lifecycle reason as ListProjectEventsPage
// (delete is a status flip and the join carries no status predicate;
// the JOIN flavor itself is semantically INNER here, and destroyed lane
// rows drop out by construction — see that function's boundary note,
// round-4 FIX 5).
func (s *Store) ListHealLedgerRows(ctx context.Context, projectID int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, e.conversation_id, e.seq, e.type, e.payload_json, e.created_at
		 FROM events e
		 LEFT JOIN conversations c ON e.conversation_id = c.id
		 LEFT JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ? AND e.type = ?
		   AND (e.payload_json LIKE '%"cause":"heal_conflict"%'
		        OR e.payload_json LIKE '%"cause":"heal_resolved"%')
		 ORDER BY e.id ASC`, projectID, EventMemoryUpdate)
	if err != nil {
		return nil, fmt.Errorf("store: list heal ledger rows: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.ID, &e.ConversationID, &e.Seq, &e.Type, &payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: list heal ledger rows: scan: %w", err)
		}
		e.Payload = json.RawMessage(payload)
		events = append(events, e)
	}
	return events, rows.Err()
}

// ListApplyRevertRows returns one lane's D4 rollback-family memory_update
// rows — memory_update{layer:"apply", cause:"revert"} (the human
// `odo memory revert <epoch>` receipt) and cause
// "revert_suppressed_recovery" (the replay engine's suppression
// visibility row) — in seq order. Reverts are lane-local like the epochs
// they name, so the ledger folds per lane. LIKE filters follow the
// markers.go convention: these rows are rare (at most one per human
// rollback plus one suppression row per receipt layer), and the replay
// engine's evaluate-time authority must never materialize a lane-sized
// list to consult them.
func (s *Store) ListApplyRevertRows(ctx context.Context, conversationID int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, seq, type, payload_json, created_at
		 FROM events
		 WHERE conversation_id = ? AND type = ?
		   AND payload_json LIKE '%"layer":"apply"%'
		   AND (payload_json LIKE '%"cause":"revert"%'
		        OR payload_json LIKE '%"cause":"revert_suppressed_recovery"%')
		 ORDER BY seq ASC`, conversationID, EventMemoryUpdate)
	if err != nil {
		return nil, fmt.Errorf("store: list apply revert rows: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(&e.ID, &e.ConversationID, &e.Seq, &e.Type, &payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: list apply revert rows: scan: %w", err)
		}
		e.Payload = json.RawMessage(payload)
		events = append(events, e)
	}
	return events, rows.Err()
}

// SearchResult is one event match from SearchEvents, carrying the event
// plus its workstream/conversation context for display.
type SearchResult struct {
	Event          Event  `json:"event"`
	WorkstreamID   int64  `json:"workstream_id"`
	WorkstreamName string `json:"workstream_name"`
	ConversationID int64  `json:"conversation_id"`
}

// SearchEvents searches event payloads across all active workstreams in a
// project for the given query string (case-insensitive LIKE match on
// payload_json). Returns matches ordered by created_at descending (newest
// first). Limited to maxResults (default 100).
func (s *Store) SearchEvents(ctx context.Context, projectID int64, query string, maxResults int) ([]SearchResult, error) {
	if maxResults <= 0 {
		maxResults = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, e.conversation_id, e.seq, e.type, e.payload_json, e.created_at,
		        c.workstream_id, w.name
		 FROM events e
		 JOIN conversations c ON e.conversation_id = c.id
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ? AND w.status = 'active'
		   AND e.payload_json LIKE '%' || ? || '%'
		 ORDER BY e.created_at DESC
		 LIMIT ?`, projectID, query, maxResults)
	if err != nil {
		return nil, fmt.Errorf("store: search events: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var payload string
		if err := rows.Scan(&r.Event.ID, &r.Event.ConversationID, &r.Event.Seq,
			&r.Event.Type, &payload, &r.Event.CreatedAt,
			&r.WorkstreamID, &r.WorkstreamName); err != nil {
			return nil, fmt.Errorf("store: search events: scan: %w", err)
		}
		r.Event.Payload = json.RawMessage(payload)
		r.ConversationID = r.Event.ConversationID
		results = append(results, r)
	}
	return results, rows.Err()
}
