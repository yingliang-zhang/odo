package store

import (
	"context"
	"database/sql"
	"fmt"
)

// CreateOrGetProject returns the project for rootPath, creating it with the
// given name when no row exists.
func (s *Store) CreateOrGetProject(ctx context.Context, rootPath, name string) (Project, error) {
	p, err := s.getProjectByRoot(ctx, rootPath)
	if err == nil {
		return p, nil
	}
	if err != sql.ErrNoRows {
		return Project{}, err
	}
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO projects (root_path, name) VALUES (?, ?)
		 RETURNING id, created_at`, rootPath, name).
		Scan(&p.ID, &p.CreatedAt)
	if err != nil {
		return Project{}, fmt.Errorf("store: create project: %w", err)
	}
	p.RootPath = rootPath
	p.Name = name
	return p, nil
}

// GetProject fetches a project by ID.
func (s *Store) GetProject(ctx context.Context, id int64) (Project, error) {
	var p Project
	err := s.db.QueryRowContext(ctx,
		`SELECT id, root_path, name, created_at FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.RootPath, &p.Name, &p.CreatedAt)
	if err != nil {
		return Project{}, fmt.Errorf("store: get project %d: %w", id, err)
	}
	return p, nil
}

// GetProjectByRoot fetches the project registered for rootPath (error
// wrapping sql.ErrNoRows when none). Exported for the read-only CLIs.
func (s *Store) GetProjectByRoot(ctx context.Context, rootPath string) (Project, error) {
	var p Project
	err := s.db.QueryRowContext(ctx,
		`SELECT id, root_path, name, created_at FROM projects WHERE root_path = ?`, rootPath).
		Scan(&p.ID, &p.RootPath, &p.Name, &p.CreatedAt)
	if err != nil {
		return Project{}, err
	}
	return p, nil
}

func (s *Store) getProjectByRoot(ctx context.Context, rootPath string) (Project, error) {
	return s.GetProjectByRoot(ctx, rootPath)
}
