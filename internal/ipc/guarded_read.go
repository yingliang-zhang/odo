package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// readWithinDir reads path only after proving its symlink-resolved form
// stays inside dir (also symlink-resolved — macOS /var→/private/var). A
// plain file inside dir reads exactly as os.ReadFile does; a symlink whose
// target escapes dir returns an error naming the escape; missing files
// return the raw os error so callers keep their existing skip/IsNotExist
// behavior.
//
// Threat model (2026-08-24 tri-review P0): wiki/ and project .odo/ paths
// are committable/implantable — a checked-in symlink pointing at an
// external secret (~/.ssh, API keys) rides a bare os.ReadFile straight
// into the model prompt. Reads under those roots go through here; global
// ~/.odo files (user.md, global skills) stay outside the model and keep
// plain os.ReadFile. The check is resolution-time, not open-time: a static
// planted link is caught; an active TOCTOU swap race is out of model.
func readWithinDir(dir, path string) ([]byte, error) {
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		// dir itself unresolvable (e.g. wiki/ not yet created on a fresh
		// project): the clean form still anchors containment, and a
		// missing path below returns its raw os error unchanged.
		base = filepath.Clean(dir)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err // absent/unresolvable: raw os error — os.IsNotExist keeps discriminating
	}
	// resolved differs from the textual path only when a symlink (or a
	// symlinked ancestor) pulled it elsewhere; that is the only case that
	// can smuggle bytes from outside dir, so plain in-dir reads keep the
	// os.ReadFile fast path untouched.
	if resolved != filepath.Clean(path) {
		if resolved != base && !strings.HasPrefix(resolved, base+string(filepath.Separator)) {
			return nil, fmt.Errorf("read %s: symlink escapes %s (resolves to %s)", path, base, resolved)
		}
	}
	// Read the caller's path, not the resolved one: containment is the
	// only new behavior, everything else (permissions, races, empty
	// files) behaves exactly as the os.ReadFile it replaces.
	return os.ReadFile(path)
}

// readFileWithin is the readFileFull-shaped twin of readWithinDir for
// project-side surfaces (2026-08-24 tri-review P0): every error — a
// missing file like before, or an escaping symlink — degrades to "" so
// injection / write-basis readers never bubble.
func readFileWithin(dir, path string) string {
	b, err := readWithinDir(dir, path)
	if err != nil {
		return ""
	}
	return string(b)
}
