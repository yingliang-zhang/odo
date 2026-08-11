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
