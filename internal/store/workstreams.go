package store

import (
	"context"
	"database/sql"
	"fmt"
)

// CreateOrGetWorkstream returns the active workstream named name under the
// project, creating it when no active row exists. A new workstream's branch
// is its name (callers sanitize for git first); existing rows are returned
// untouched.
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

// ListWorkstreams returns all workstreams for a project, ordered by
// created_at (ties broken by id).
func (s *Store) ListWorkstreams(ctx context.Context, projectID int64) ([]Workstream, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, name, branch, worktree_path, status, created_at
		 FROM workstreams
		 WHERE project_id = ?
		 ORDER BY created_at ASC, id ASC`, projectID)
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
