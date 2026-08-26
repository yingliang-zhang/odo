package store

import (
	"context"
	"database/sql"
	"fmt"
)

// M12 D-auto: journal-derived marker queries for the auto-distill and
// auto-curate schedulers. Frequency caps, the auto-curate notes/age
// triggers, and restart-surviving state are all derived from the journal
// (ADR-0003: the journal is the only state that outlives the daemon).
//
// Payload discrimination uses LIKE on the serialized JSON the daemon
// writes (fixed key order from encoding/json maps is not guaranteed, but
// the marker payloads are small flat objects whose keys appear verbatim).

// autoDistillPayloadMatch is the WHERE fragment selecting AUTO distill
// markers: review_action{action:"distill"} carrying a trigger field that is
// not "manual". Legacy pre-M12 markers (no trigger key) are excluded —
// every distill before the field existed was manual by construction.
const autoDistillPayloadMatch = `e.type = 'review_action'
   AND e.payload_json LIKE '%"action":"distill"%'
   AND e.payload_json LIKE '%"trigger":%'
   AND e.payload_json NOT LIKE '%"trigger":"manual"%'`

// ListActiveConversations returns every active conversation of a project
// (joined through active workstreams), newest first — the auto-distill
// startup compensation scan's input.
func (s *Store) ListActiveConversations(ctx context.Context, projectID int64) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.workstream_id, c.epoch, c.state, c.base_commit_sha, c.created_at
		 FROM conversations c
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ? AND w.status = ? AND c.state = ?
		 ORDER BY c.id`, projectID, WorkstreamActive, ConversationActive)
	if err != nil {
		return nil, fmt.Errorf("store: list active conversations: %w", err)
	}
	defer rows.Close()
	var convs []Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		convs = append(convs, c)
	}
	return convs, rows.Err()
}

// CountAutoDistillsForConversation counts the conversation's AUTO distill
// markers journaled since sinceTs (YYYY-MM-DD HH:MM:SS, UTC; "" = all
// time). The ≤N/hour/conversation frequency cap reads this.
func (s *Store) CountAutoDistillsForConversation(ctx context.Context, conversationID int64, sinceTs string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events e
		 WHERE e.conversation_id = ? AND `+autoDistillPayloadMatch+`
		   AND e.created_at > ?`, conversationID, sinceTs).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count auto distills for conversation %d: %w", conversationID, err)
	}
	return n, nil
}

// CountAutoDistillsForProject counts AUTO distill markers across every
// conversation of the project since sinceTs ("" = all time). The
// ≤N/day/project frequency cap reads this.
func (s *Store) CountAutoDistillsForProject(ctx context.Context, projectID int64, sinceTs string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events e
		 JOIN conversations c ON e.conversation_id = c.id
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ? AND `+autoDistillPayloadMatch+`
		   AND e.created_at > ?`, projectID, sinceTs).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count auto distills for project %d: %w", projectID, err)
	}
	return n, nil
}

// AutoDistillTimesForProject returns the created_at of every AUTO distill
// marker across the project's conversations since sinceTs, OLDEST first —
// the daily-cap quota ledger. The suspension horizon reads it: the cap
// releases the moment enough of the oldest counted markers age out of the
// 24h window.
func (s *Store) AutoDistillTimesForProject(ctx context.Context, projectID int64, sinceTs string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.created_at FROM events e
		 JOIN conversations c ON e.conversation_id = c.id
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ? AND `+autoDistillPayloadMatch+`
		   AND e.created_at > ?
		 ORDER BY e.created_at ASC, e.id ASC`, projectID, sinceTs)
	if err != nil {
		return nil, fmt.Errorf("store: auto distill times for project %d: %w", projectID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			return nil, fmt.Errorf("store: scan auto distill time for project %d: %w", projectID, err)
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// LatestAutoCapSuspension returns the payload of the NEWEST
// memory_update{layer:"auto_distill", cause:"cap_suspended_until"} row
// project-wide — nil when the journal carries none (pre-suspension-row
// journals). The ipc layer decodes the detail (the suspension's RFC3339
// resume timestamp) with the same struct it wrote; the badge leverage and
// the boot-time suspension restore read it.
func (s *Store) LatestAutoCapSuspension(ctx context.Context, projectID int64) (*string, error) {
	var payload sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT e.payload_json FROM events e
		 JOIN conversations c ON e.conversation_id = c.id
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ?
		   AND e.type = 'memory_update'
		   AND e.payload_json LIKE '%"layer":"auto_distill"%'
		   AND e.payload_json LIKE '%"cause":"cap_suspended_until"%'
		 ORDER BY e.created_at DESC, e.id DESC
		 LIMIT 1`, projectID).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: latest cap suspension for project %d: %w", projectID, err)
	}
	return &payload.String, nil
}

// AutoCurateState derives the auto-curate trigger inputs: how many distill
// markers (any trigger, manual included — every new note feeds the
// curator) landed since the latest PASSING curate marker project-wide,
// and when that latest passing curate happened (nil when never curated —
// treated as infinitely stale by the age trigger). gate:"failed" curate
// markers do NOT reset the age clock: a failed gate keeps the curate
// overdue so the next evaluation retries (dead citations usually age out
// as new notes land).
func (s *Store) AutoCurateState(ctx context.Context, projectID int64) (distillsSince int, lastCurateAt *string, err error) {
	const passingCurate = `e.type = 'review_action'
	   AND e.payload_json LIKE '%"action":"curate"%'
	   AND (e.payload_json LIKE '%"gate":"pass"%' OR e.payload_json NOT LIKE '%"gate":%')`
	var last sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT MAX(e.created_at) FROM events e
		 JOIN conversations c ON e.conversation_id = c.id
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ? AND `+passingCurate, projectID).Scan(&last)
	if err != nil {
		return 0, nil, fmt.Errorf("store: latest curate for project %d: %w", projectID, err)
	}
	since := ""
	if last.Valid {
		since = last.String
		lastCurateAt = &since
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events e
		 JOIN conversations c ON e.conversation_id = c.id
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ?
		   AND e.type = 'review_action'
		   AND e.payload_json LIKE '%"action":"distill"%'
		   AND e.created_at > ?`, projectID, since).Scan(&distillsSince)
	if err != nil {
		return 0, nil, fmt.Errorf("store: distills since latest curate for project %d: %w", projectID, err)
	}
	return distillsSince, lastCurateAt, nil
}

// LatestCurateFailureAt returns the created_at of the newest curator
// FAILURE row (memory_update{layer:"curator", cause:"failed" |
// "gate_failed"}) project-wide — nil when no failure is journaled. M17
// F4: the auto-curate failure backoff derives from it plus
// AutoCurateState's last-passing-curate timestamp (a success newer than
// the newest failure resets the backoff).
func (s *Store) LatestCurateFailureAt(ctx context.Context, projectID int64) (*string, error) {
	var last sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(e.created_at) FROM events e
		 JOIN conversations c ON e.conversation_id = c.id
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ?
		   AND e.type = 'memory_update'
		   AND e.payload_json LIKE '%"layer":"curator"%'
		   AND (e.payload_json LIKE '%"cause":"failed"%'
		        OR e.payload_json LIKE '%"cause":"gate_failed"%')`, projectID).Scan(&last)
	if err != nil {
		return nil, fmt.Errorf("store: latest curate failure for project %d: %w", projectID, err)
	}
	if !last.Valid {
		return nil, nil
	}
	v := last.String
	return &v, nil
}

// DistillMarkerExistsForEpoch reports whether ANY workstream of the
// project journaled a distill marker for epoch N — the journal half of
// the curator's ghost-citation check (M17 F4): a citation naming an epoch
// with no note file AND no marker never existed (ghost → the line is
// stripped); a marker with no note file is a real dangling reference
// (the file vanished after the fold — the citation gate still aborts).
// The match keys on the marker's note_path suffix ("…-epoch-N.md\""), so
// numeric prefixes never collide ("epoch-2" vs "epoch-21").
func (s *Store) DistillMarkerExistsForEpoch(ctx context.Context, projectID int64, epoch int) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events e
		 JOIN conversations c ON e.conversation_id = c.id
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE w.project_id = ?
		   AND e.type = 'review_action'
		   AND e.payload_json LIKE '%"action":"distill"%'
		   AND e.payload_json LIKE ?`, projectID,
		fmt.Sprintf(`%%-epoch-%d.md"%%`, epoch)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: distill marker for epoch %d, project %d: %w", epoch, projectID, err)
	}
	return n > 0, nil
}
