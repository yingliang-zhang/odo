package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// canonGuardRoot resolves the trust anchor for the project-side guards:
// the project root with symlinks evaluated (macOS /var→/private/var, a
// project registered through a linked path). Falls back to the clean
// textual form when the root itself does not resolve yet.
func canonGuardRoot(projectRoot string) string {
	if root, err := filepath.EvalSymlinks(projectRoot); err == nil {
		return root
	}
	return filepath.Clean(projectRoot)
}

// guardedBase resolves the containment base for a guarded read, anchored
// at the CANONICAL project root (2026-08-25 review P0): dir is always one
// of the daemon-owned roots (.odo, wiki, wiki/topics, .odo/skills,
// .odo/attachments, a loop dir) joined onto projectRoot. Two refusals
// close what resolution-against-dir alone could not:
//
//  1. dir itself is a symlink — daemon-owned root nodes are never links.
//     A checked-in `wiki -> ~/.ssh` used to seed base with the attacker's
//     target tree, so a read of <project>/wiki/id_rsa passed containment
//     inside it verbatim.
//  2. dir resolves OUTSIDE the canonical project root — a symlinked
//     ancestor (wiki/ -> /external while dir = wiki/topics) is judged
//     against the anchor, not against dir itself.
//
// A dir that does not resolve (fresh project: wiki/ not yet created)
// falls back to its clean TEXTUAL form contained under the textual
// project root — nothing exists to read yet, so resolution-time tricks
// are moot, and the textual check only guards against callsite mistakes.
func guardedBase(projectRoot, dir string) (string, error) {
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("guarded root %s is itself a symlink — daemon-owned dirs are never links", dir)
	}
	base, err := filepath.EvalSymlinks(dir)
	if err != nil {
		root, clean := filepath.Clean(projectRoot), filepath.Clean(dir)
		if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return "", fmt.Errorf("guarded root %s is not under project root %s", dir, projectRoot)
		}
		return clean, nil
	}
	root := canonGuardRoot(projectRoot)
	if base != root && !strings.HasPrefix(base, root+string(filepath.Separator)) {
		return "", fmt.Errorf("guarded root %s escapes project root %s (resolves to %s)", dir, root, base)
	}
	return base, nil
}

// readWithinDir reads path only after proving its symlink-resolved form
// stays inside dir — itself proven a real directory tree inside the
// canonical projectRoot via guardedBase. A plain file inside dir reads
// exactly as os.ReadFile does; a symlink whose target escapes dir returns
// an error naming the escape; missing files return the raw os error so
// callers keep their existing skip/IsNotExist behavior.
//
// Threat model (2026-08-24 tri-review P0; anchor + root-node link refusal
// 2026-08-25 review P0): wiki/ and project .odo/ paths are
// committable/implantable — a checked-in symlink pointing at an external
// secret (~/.ssh, API keys) rides a bare os.ReadFile straight into the
// model prompt. Reads under those roots go through here; global ~/.odo
// files (user.md, global skills) stay outside the model and keep plain
// os.ReadFile. The check is resolution-time, not open-time: a static
// planted link is caught; an active TOCTOU swap race is out of model.
func readWithinDir(projectRoot, dir, path string) ([]byte, error) {
	base, err := guardedBase(projectRoot, dir)
	if err != nil {
		return nil, err
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
func readFileWithin(projectRoot, dir, path string) string {
	b, err := readWithinDir(projectRoot, dir, path)
	if err != nil {
		return ""
	}
	return string(b)
}
