package store

import (
	"context"
	"database/sql"
	"fmt"
)

// CreateOrGetWorkstream returns the active workstream named name under the
// project, creating it when no active row exists. A new workstream's branch
// is its bare name (callers sanitize for git first); git consumers prefix it
// to "odo/<name>" (M11c) and the branch is created lazily on the first run.
// Existing rows are returned untouched.
func (s *Store) CreateOrGetWorkstream(ctx context.Context, projectID int64, name string) (Workstream, error) {
	w, err := scanWorkstream(s.db.QueryRowContext(ctx,
		`SELECT id, project_id, name, branch, worktree_path, status, created_at
		 FROM workstreams
		 WHERE project_id = ? AND name = ? AND status = ?
		 ORDER BY id DESC LIMIT 1`, projectID, name, WorkstreamActive))
	if err == nil {
		return w, nil
	}
	if err != sql.ErrNoRows {
		return Workstream{}, fmt.Errorf("store: get workstream: %w", err)
	}
	w = Workstream{ProjectID: projectID, Name: name, Branch: &name, Status: WorkstreamActive}
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO workstreams (project_id, name, branch) VALUES (?, ?, ?)
		 RETURNING id, created_at`, projectID, name, name).
		Scan(&w.ID, &w.CreatedAt)
	if err != nil {
		return Workstream{}, fmt.Errorf("store: create workstream: %w", err)
	}
	return w, nil
}

// GetWorkstream fetches a workstream by ID.
func (s *Store) GetWorkstream(ctx context.Context, id int64) (Workstream, error) {
	w, err := scanWorkstream(s.db.QueryRowContext(ctx,
		`SELECT id, project_id, name, branch, worktree_path, status, created_at
		 FROM workstreams WHERE id = ?`, id))
	if err != nil {
		return Workstream{}, fmt.Errorf("store: get workstream %d: %w", id, err)
	}
	return w, nil
}

// ListWorkstreams returns all active workstreams for a project, ordered by
// created_at (ties broken by id). Soft-deleted workstreams (status='deleted')
// are excluded from the list.
func (s *Store) ListWorkstreams(ctx context.Context, projectID int64) ([]Workstream, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, name, branch, worktree_path, status, created_at
		 FROM workstreams
		 WHERE project_id = ? AND status = ?
		 ORDER BY created_at ASC, id ASC`, projectID, WorkstreamActive)
	if err != nil {
		return nil, fmt.Errorf("store: list workstreams: %w", err)
	}
	defer rows.Close()

	var ws []Workstream
	for rows.Next() {
		w, err := scanWorkstream(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list workstreams: scan: %w", err)
		}
		ws = append(ws, w)
	}
	return ws, rows.Err()
}

// UpdateWorkstreamWorktree sets (or clears, when path is nil) the worktree
// path bound to a workstream. ADR-0002 keeps one binding point per workstream.
func (s *Store) UpdateWorkstreamWorktree(ctx context.Context, id int64, path *string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workstreams SET worktree_path = ? WHERE id = ?`, path, id)
	if err != nil {
		return fmt.Errorf("store: update workstream worktree: %w", err)
	}
	return nil
}

// RenameWorkstream updates the name of a workstream. The name should be
// sanitized by the caller. Returns an error if the name is empty, another
// active workstream in the same project already has that name, or the
// workstream is not found / not active.
func (s *Store) RenameWorkstream(ctx context.Context, id int64, name string) error {
	if name == "" {
		return fmt.Errorf("store: rename workstream: name is required")
	}
	// Look up the workstream to get its project_id for the collision check.
	w, err := s.GetWorkstream(ctx, id)
	if err != nil {
		return fmt.Errorf("store: rename workstream: %w", err)
	}
	// Check for name collision among active workstreams in the same project.
	var collision int64
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workstreams WHERE project_id = ? AND name = ? AND status = ? AND id != ?`,
		w.ProjectID, name, WorkstreamActive, id).Scan(&collision)
	if err != nil {
		return fmt.Errorf("store: rename workstream: collision check: %w", err)
	}
	if collision > 0 {
		return fmt.Errorf("store: rename workstream: name %q already in use", name)
	}
	// Update name and branch (branch follows name for git consistency).
	res, err := s.db.ExecContext(ctx,
		`UPDATE workstreams SET name = ?, branch = ? WHERE id = ? AND status = ?`,
		name, name, id, WorkstreamActive)
	if err != nil {
		return fmt.Errorf("store: rename workstream: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("store: rename workstream: %d not found or not active", id)
	}
	return nil
}

// DeleteWorkstream soft-deletes a workstream by setting status to "deleted".
// This preserves journal history for audit. Returns an error if the
// workstream has pending diffs (must accept/reject first).
func (s *Store) DeleteWorkstream(ctx context.Context, id int64) error {
	// Look up the workstream to get its project_id for the pending check.
	w, err := s.GetWorkstream(ctx, id)
	if err != nil {
		return fmt.Errorf("store: delete workstream: %w", err)
	}
	// Check for pending diffs scoped to this project
	pending, err := s.PendingDiffCountsByWorkstream(ctx, w.ProjectID)
	if err != nil {
		return fmt.Errorf("store: delete workstream: check pending: %w", err)
	}
	if count, ok := pending[id]; ok && count > 0 {
		return fmt.Errorf("store: delete workstream: %d has %d pending diff(s) — accept or reject first", id, count)
	}
	// Soft-delete: set status to deleted
	res, err := s.db.ExecContext(ctx,
		`UPDATE workstreams SET status = ? WHERE id = ? AND status = ?`,
		"deleted", id, WorkstreamActive)
	if err != nil {
		return fmt.Errorf("store: delete workstream: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("store: delete workstream: %d not found or not active", id)
	}
	return nil
}
