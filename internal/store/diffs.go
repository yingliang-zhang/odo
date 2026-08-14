package store

import (
	"context"
	"database/sql"
	"fmt"
)

// InsertDiff records a diff file extracted from a run's worktree.
// baseSHA is the commit the diff was generated against ("" stores NULL);
// worktreePath is the producing run's worktree ("" stores NULL) — the
// per-run binding the sweeper and retire paths derive hold/reclaim
// decisions from (schema v2, I8/I10).
func (s *Store) InsertDiff(ctx context.Context, conversationID int64, pathOnDisk, baseSHA, worktreePath string) (Diff, error) {
	d := Diff{
		ConversationID: conversationID,
		PathOnDisk:     pathOnDisk,
		Status:         DiffPending,
	}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO diffs (conversation_id, path_on_disk, base_sha, worktree_path) VALUES (?, ?, ?, ?)
		 RETURNING id, created_at`, conversationID, pathOnDisk, nullString(baseSHA), nullString(worktreePath)).
		Scan(&d.ID, &d.CreatedAt)
	if err != nil {
		return Diff{}, fmt.Errorf("store: insert diff: %w", err)
	}
	if baseSHA != "" {
		d.BaseSHA = &baseSHA
	}
	if worktreePath != "" {
		d.WorktreePath = &worktreePath
	}
	return d, nil
}

// GetDiff fetches a diff by ID.
func (s *Store) GetDiff(ctx context.Context, diffID int64) (Diff, error) {
	d, err := s.scanDiff(s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, path_on_disk, base_sha, worktree_path, status, created_at
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
		`SELECT id, conversation_id, path_on_disk, base_sha, worktree_path, status, created_at
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

// UpdateDiffBaseSHA sets a diff's base_sha after a successful refresh
// rebased the diff onto a newer HEAD (P0a). The diff stays pending; only
// the base pointer moves.
func (s *Store) UpdateDiffBaseSHA(ctx context.Context, diffID int64, baseSHA string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE diffs SET base_sha = ? WHERE id = ?`, nullString(baseSHA), diffID)
	if err != nil {
		return fmt.Errorf("store: update diff base sha: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("store: update diff base sha: %w", sql.ErrNoRows)
	}
	return nil
}

// ListDiffs returns every diff for a conversation, ordered by id (insertion
// order). Read-only consumers (outcome audits) use it to join diff rows to
// the run events that produced them.
func (s *Store) ListDiffs(ctx context.Context, conversationID int64) ([]Diff, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, path_on_disk, base_sha, worktree_path, status, created_at
		 FROM diffs WHERE conversation_id = ? ORDER BY id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("store: list diffs: %w", err)
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

// ListPendingDiffs returns all pending diffs for a conversation, ordered by id.
func (s *Store) ListPendingDiffs(ctx context.Context, conversationID int64) ([]Diff, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, path_on_disk, base_sha, worktree_path, status, created_at
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

// PendingDiffRow is one pending diff annotated with its owning workstream
// (P1a review inbox). The SQL JOIN's column order matches Diff's scanner
// fields followed by the two workstream columns.
type PendingDiffRow struct {
	Diff
	WorkstreamID   int64
	WorkstreamName string
}

// ListAllPendingDiffs returns every pending diff across all active
// workstreams of a project, ordered by workstream id then diff id (matches
// sidebar workstream ordering + per-conversation diff ordering). The JOIN
// scope intentionally mirrors PendingDiffCountsByWorkstream — all
// conversations, active workstreams only — so inbox rows never desync from
// sidebar pills. Read-only; single query, no Go iteration over workstreams.
func (s *Store) ListAllPendingDiffs(ctx context.Context, projectID int64) ([]PendingDiffRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.id, d.conversation_id, d.path_on_disk, d.base_sha,
		        d.worktree_path, d.status, d.created_at,
		        w.id, w.name
		 FROM diffs d
		 JOIN conversations c ON d.conversation_id = c.id
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE d.status = ? AND w.project_id = ? AND w.status = ?
		 ORDER BY w.id, d.id`,
		DiffPending, projectID, WorkstreamActive)
	if err != nil {
		return nil, fmt.Errorf("store: list all pending diffs: %w", err)
	}
	defer rows.Close()
	var out []PendingDiffRow
	for rows.Next() {
		var r PendingDiffRow
		if err := rows.Scan(&r.ID, &r.ConversationID, &r.PathOnDisk, &r.BaseSHA,
			&r.WorktreePath, &r.Status, &r.CreatedAt,
			&r.WorkstreamID, &r.WorkstreamName); err != nil {
			return nil, fmt.Errorf("store: list all pending diffs: scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingDiffCountsByWorkstream returns the count of pending diffs per
// workstream of a project (read-only; feeds the sidebar badge IPC).
func (s *Store) PendingDiffCountsByWorkstream(ctx context.Context, projectID int64) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.workstream_id, COUNT(*) FROM diffs d
		 JOIN conversations c ON d.conversation_id = c.id
		 JOIN workstreams w ON c.workstream_id = w.id
		 WHERE d.status = ? AND w.project_id = ?
		 GROUP BY c.workstream_id`, DiffPending, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: pending diff counts: %w", err)
	}
	defer rows.Close()
	counts := map[int64]int{}
	for rows.Next() {
		var wsID int64
		var n int
		if err := rows.Scan(&wsID, &n); err != nil {
			return nil, fmt.Errorf("store: pending diff counts: scan: %w", err)
		}
		counts[wsID] = n
	}
	return counts, rows.Err()
}

func (s *Store) scanDiff(row interface{ Scan(...interface{}) error }) (Diff, error) {
	var d Diff
	err := row.Scan(&d.ID, &d.ConversationID, &d.PathOnDisk, &d.BaseSHA, &d.WorktreePath, &d.Status, &d.CreatedAt)
	return d, err
}

// WorktreeRefs folds every diffs.worktree_path binding into two sets:
// `pending` (a live review holds the worktree — sweeper must keep it) and
// `referenced` (any row at all mentions the dir). Unreferenced dirs are
// orphans (crashed/killed runs, F1); referenced-but-concluded dirs are
// leftovers of failed retire paths. Paths appear exactly as inserted
// (absolute).
func (s *Store) WorktreeRefs(ctx context.Context) (referenced, pending map[string]bool, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT worktree_path, status FROM diffs WHERE worktree_path IS NOT NULL`)
	if err != nil {
		return nil, nil, fmt.Errorf("store: worktree refs: %w", err)
	}
	defer rows.Close()
	referenced, pending = map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var p, status string
		if err := rows.Scan(&p, &status); err != nil {
			return nil, nil, fmt.Errorf("store: worktree refs: scan: %w", err)
		}
		referenced[p] = true
		if status == DiffPending {
			pending[p] = true
		}
	}
	return referenced, pending, rows.Err()
}
