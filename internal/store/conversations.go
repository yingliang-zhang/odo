package store

import (
	"context"
	"fmt"
)

// CreateConversation always creates a new active conversation under the
// workstream. baseSHA is the repo HEAD at creation time ("" stores NULL);
// it anchors stale-diff detection at accept time (M1+).
func (s *Store) CreateConversation(ctx context.Context, workstreamID int64, baseSHA string) (Conversation, error) {
	c := Conversation{WorkstreamID: workstreamID, State: ConversationActive}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO conversations (workstream_id, base_commit_sha) VALUES (?, ?)
		 RETURNING id, epoch, created_at`, workstreamID, nullString(baseSHA)).
		Scan(&c.ID, &c.Epoch, &c.CreatedAt)
	if err != nil {
		return Conversation{}, fmt.Errorf("store: create conversation: %w", err)
	}
	if baseSHA != "" {
		c.BaseCommitSHA = &baseSHA
	}
	return c, nil
}

// ForkConversation branch-copies a conversation (turn-fork): a NEW
// conversation row under workstreamID carrying forked_from=srcID, with
// the source's journal prefix events (seq 1..fromSeq) copied verbatim —
// same type, same payload_json, same seqs (a COPY; the source lane is
// never touched, honoring the append-only invariant). baseSHA anchors
// the new lane's base pointer; it is the repo HEAD at fork time (the
// source's accepted diffs landed there), read by the daemon.
//
// Returns the new conversation and the number of copied events. One
// transaction — a fork row never exists without its full prefix.
func (s *Store) ForkConversation(ctx context.Context, srcID int64, fromSeq int, workstreamID int64, baseSHA string) (Conversation, int, error) {
	if fromSeq < 1 {
		return Conversation{}, 0, fmt.Errorf("store: fork conversation: from_seq %d is below the journal floor", fromSeq)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Conversation{}, 0, fmt.Errorf("store: fork conversation: begin: %w", err)
	}
	defer tx.Rollback()

	var srcWorkstream int64
	if err := tx.QueryRowContext(ctx,
		`SELECT workstream_id FROM conversations WHERE id = ?`, srcID).Scan(&srcWorkstream); err != nil {
		return Conversation{}, 0, fmt.Errorf("store: fork conversation: source: %w", err)
	}
	c := Conversation{WorkstreamID: workstreamID, State: ConversationActive}
	err = tx.QueryRowContext(ctx,
		`INSERT INTO conversations (workstream_id, base_commit_sha, forked_from) VALUES (?, ?, ?)
		 RETURNING id, epoch, created_at`, workstreamID, nullString(baseSHA), srcID).
		Scan(&c.ID, &c.Epoch, &c.CreatedAt)
	if err != nil {
		return Conversation{}, 0, fmt.Errorf("store: fork conversation: insert: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO events (conversation_id, seq, type, payload_json)
		 SELECT ?, seq, type, payload_json FROM events
		 WHERE conversation_id = ? AND seq <= ?
		 ORDER BY seq`, c.ID, srcID, fromSeq)
	if err != nil {
		return Conversation{}, 0, fmt.Errorf("store: fork conversation: copy events: %w", err)
	}
	n, _ := res.RowsAffected()
	copied := int(n)
	if copied < fromSeq {
		return Conversation{}, 0, fmt.Errorf("store: fork conversation: source journal ends at seq %d — cannot fork from %d", copied, fromSeq)
	}
	if baseSHA != "" {
		c.BaseCommitSHA = &baseSHA
	}
	c.ForkedFrom = &srcID
	if err := tx.Commit(); err != nil {
		return Conversation{}, 0, fmt.Errorf("store: fork conversation: commit: %w", err)
	}
	return c, copied, nil
}

// GetConversation fetches a conversation by ID.
func (s *Store) GetConversation(ctx context.Context, id int64) (Conversation, error) {
	c, err := scanConversation(s.db.QueryRowContext(ctx,
		`SELECT id, workstream_id, epoch, state, base_commit_sha, forked_from, created_at
		 FROM conversations WHERE id = ?`, id))
	if err != nil {
		return Conversation{}, fmt.Errorf("store: get conversation %d: %w", id, err)
	}
	return c, nil
}

// IncrementEpoch bumps the conversation's epoch by one (a distill splits the
// journal into epochs) and returns the new value.
func (s *Store) IncrementEpoch(ctx context.Context, conversationID int64) (int, error) {
	var epoch int
	err := s.db.QueryRowContext(ctx,
		`UPDATE conversations SET epoch = epoch + 1 WHERE id = ?
		 RETURNING epoch`, conversationID).Scan(&epoch)
	if err != nil {
		return 0, fmt.Errorf("store: increment epoch for conversation %d: %w", conversationID, err)
	}
	return epoch, nil
}

// GetActiveConversation returns the most recent active conversation for a
// workstream, or an error wrapping sql.ErrNoRows when none exists.
func (s *Store) GetActiveConversation(ctx context.Context, workstreamID int64) (Conversation, error) {
	c, err := scanConversation(s.db.QueryRowContext(ctx,
		`SELECT id, workstream_id, epoch, state, base_commit_sha, forked_from, created_at
		 FROM conversations
		 WHERE workstream_id = ? AND state = ?
		 ORDER BY id DESC LIMIT 1`, workstreamID, ConversationActive))
	if err != nil {
		return Conversation{}, err
	}
	return c, nil
}
