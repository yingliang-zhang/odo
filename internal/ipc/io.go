package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// guardProjectWritePath is the write-side twin of the read guard
// (2026-08-25 review P0, same tri-review threat model): before any daemon
// write lands inside the committable project tree (.odo/**, wiki/**),
// prove path is textually under projectRoot AND every EXISTING component
// from the first under-root node down to path itself is a real directory
// or file, never a symlink. The walk covers the .odo / wiki ROOT NODE
// (refused as a link outright — daemon-owned roots are never links), any
// symlinked intermediate (wiki -> /external would pull the write outside
// the project), and a symlinked final component (os.WriteFile would
// follow it onto an external file; writeFileAtomic's rename would merely
// replace the link, but the uniform refusal keeps one rule). The walk
// stops at the first non-existent component — deeper entries cannot
// exist, so the textual path then IS the physical location and
// MkdirAll+rename can only create real directories beneath a proven-real
// ancestor. The project root itself MAY be a symlink (a registered
// project path elsewhere is legitimate): only components BELOW it are
// policed, so containment never depends on resolving the anchor here.
func guardProjectWritePath(projectRoot, path string) error {
	root, clean := filepath.Clean(projectRoot), filepath.Clean(path)
	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return fmt.Errorf("guarded write %s is not under project root %s", path, projectRoot)
	}
	rel, err := filepath.Rel(root, clean)
	if err != nil {
		return fmt.Errorf("guarded write %s: %w", path, err)
	}
	cur := root
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == "." || seg == "" {
			break // path == root itself
		}
		cur = filepath.Join(cur, seg)
		fi, lerr := os.Lstat(cur)
		if lerr != nil {
			if os.IsNotExist(lerr) {
				return nil // nothing deeper can exist: textual == physical
			}
			return fmt.Errorf("guarded write %s: stat %s: %w", path, cur, lerr)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("guarded write %s: symlinked component %s — the committable tree holds real directories only", path, cur)
		}
	}
	return nil
}

// writeFileWithin is the guarded form of writeFileAtomic for daemon-owned
// files inside the committable project tree (.odo/memory.md, wiki
// notes/topics, skill files): the path is proven symlink-free under
// projectRoot first, then written atomically.
func writeFileWithin(projectRoot, path, content string, mode os.FileMode) error {
	if err := guardProjectWritePath(projectRoot, path); err != nil {
		return err
	}
	return writeFileAtomic(path, content, mode)
}

// writeFileAtomic writes content to path atomically with the given mode:
// temp file in the same directory, chmod, then rename. Mirrors the
// UpdateSettings pattern (internal/adapter/settings.go) but shared — 0644
// for project memory files, 0600 for ~/.odo/ files.
func writeFileAtomic(path, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
