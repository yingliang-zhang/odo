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

// GetConversation fetches a conversation by ID.
func (s *Store) GetConversation(ctx context.Context, id int64) (Conversation, error) {
	c, err := scanConversation(s.db.QueryRowContext(ctx,
		`SELECT id, workstream_id, epoch, state, base_commit_sha, created_at
		 FROM conversations WHERE id = ?`, id))
	if err != nil {
		return Conversation{}, fmt.Errorf("store: get conversation %d: %w", id, err)
	}
	return c, nil
}

// GetActiveConversation returns the most recent active conversation for a
// workstream, or an error wrapping sql.ErrNoRows when none exists.
func (s *Store) GetActiveConversation(ctx context.Context, workstreamID int64) (Conversation, error) {
	c, err := scanConversation(s.db.QueryRowContext(ctx,
		`SELECT id, workstream_id, epoch, state, base_commit_sha, created_at
		 FROM conversations
		 WHERE workstream_id = ? AND state = ?
		 ORDER BY id DESC LIMIT 1`, workstreamID, ConversationActive))
	if err != nil {
		return Conversation{}, err
	}
	return c, nil
}
