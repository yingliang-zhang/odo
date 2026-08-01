package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// AppendEvent journals an event with the next per-conversation sequence
// number (monotonic, gap-free under the store's single connection).
// payloadJSON must be valid JSON; an empty payload is stored as "{}".
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
