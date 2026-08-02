package store

import (
	"context"
	"database/sql"
	"fmt"
)

// InsertDiff records a diff file extracted from a run's worktree.
// baseSHA is the commit the diff was generated against ("" stores NULL).
func (s *Store) InsertDiff(ctx context.Context, conversationID int64, pathOnDisk, baseSHA string) (Diff, error) {
	d := Diff{
		ConversationID: conversationID,
		PathOnDisk:     pathOnDisk,
		Status:         DiffPending,
	}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO diffs (conversation_id, path_on_disk, base_sha) VALUES (?, ?, ?)
		 RETURNING id, created_at`, conversationID, pathOnDisk, nullString(baseSHA)).
		Scan(&d.ID, &d.CreatedAt)
	if err != nil {
		return Diff{}, fmt.Errorf("store: insert diff: %w", err)
	}
	if baseSHA != "" {
		d.BaseSHA = &baseSHA
	}
	return d, nil
}

// GetDiff fetches a diff by ID.
func (s *Store) GetDiff(ctx context.Context, diffID int64) (Diff, error) {
	d, err := s.scanDiff(s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, path_on_disk, base_sha, status, created_at
		 FROM diffs WHERE id = ?`, diffID))
	if err != nil {
		return Diff{}, fmt.Errorf("store: get diff %d: %w", diffID, err)
	}
	return d, nil
}

// LatestDiff returns the most recently created diff for a conversation,
// or sql.ErrNoRows when none exists.
func (s *Store) LatestDiff(ctx context.Context, conversationID int64) (Diff, error) {
	d, err := s.scanDiff(s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, path_on_disk, base_sha, status, created_at
		 FROM diffs
		 WHERE conversation_id = ?
		 ORDER BY id DESC LIMIT 1`, conversationID))
	if err != nil {
		return Diff{}, err
	}
	return d, nil
}

// UpdateDiffStatus sets a diff's status (pending/accepted/rejected).
func (s *Store) UpdateDiffStatus(ctx context.Context, diffID int64, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE diffs SET status = ? WHERE id = ?`, status, diffID)
	if err != nil {
		return fmt.Errorf("store: update diff status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("store: update diff status: %w", sql.ErrNoRows)
	}
	return nil
}

// ListPendingDiffs returns all pending diffs for a conversation, ordered by id.
func (s *Store) ListPendingDiffs(ctx context.Context, conversationID int64) ([]Diff, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, path_on_disk, base_sha, status, created_at
		 FROM diffs WHERE conversation_id = ? AND status = ? ORDER BY id`,
		conversationID, DiffPending)
	if err != nil {
		return nil, fmt.Errorf("store: list pending diffs: %w", err)
	}
	defer rows.Close()
	var diffs []Diff
	for rows.Next() {
		d, err := s.scanDiff(rows)
		if err != nil {
			return nil, err
		}
		diffs = append(diffs, d)
	}
	return diffs, rows.Err()
}

func (s *Store) scanDiff(row interface{ Scan(...interface{}) error }) (Diff, error) {
	var d Diff
	err := row.Scan(&d.ID, &d.ConversationID, &d.PathOnDisk, &d.BaseSHA, &d.Status, &d.CreatedAt)
	return d, err
}
