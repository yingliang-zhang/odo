package ipc

// readWithinDir unit tests (2026-08-24 tri-review P0): the guard keeps
// plain in-dir reads byte-identical to os.ReadFile, passes symlinks whose
// resolved target stays inside dir, refuses every escape shape (direct,
// chained), and preserves the raw os error for missing files so callers
// keep their skip/IsNotExist semantics. Site-level symlink regressions
// live next to the recall and learner rigs (m6_test.go / learner_test.go).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardedFixture builds dir/ with a regular file plus an external "secret"
// file OUTSIDE it (the attacker payoff an implanted symlink must never
// deliver). Returns everything the read shapes below need.
func guardedFixture(t *testing.T) (dir, plain, external, externalBody string) {
	t.Helper()
	base := t.TempDir()
	dir = filepath.Join(base, "dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plain = filepath.Join(dir, "plain.md")
	if err := os.WriteFile(plain, []byte("plain body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	external = filepath.Join(base, "secret.md")
	externalBody = "EXTERNAL-SECRET-BYTES\n"
	if err := os.WriteFile(external, []byte(externalBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, plain, external, externalBody
}

// TestGuardedReadPlainFile: a regular file inside dir reads exactly as
// os.ReadFile — the guard adds no behavior for the common path.
func TestGuardedReadPlainFile(t *testing.T) {
	dir, plain, _, _ := guardedFixture(t)
	b, err := readWithinDir(dir, plain)
	if err != nil {
		t.Fatalf("readWithinDir: %v", err)
	}
	if string(b) != "plain body\n" {
		t.Errorf("content = %q, want the file verbatim", b)
	}
}

// TestGuardedReadSymlinkWithin: a symlink inside dir pointing at another
// file inside dir resolves in-bounds and reads the target's bytes (the
// resolved≠clean fast path must not refuse containment-correct links; on
// macOS even /var→/private/var resolution must not false-positive).
func TestGuardedReadSymlinkWithin(t *testing.T) {
	dir, plain, _, _ := guardedFixture(t)
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(plain, link); err != nil {
		t.Fatal(err)
	}
	b, err := readWithinDir(dir, link)
	if err != nil {
		t.Fatalf("readWithinDir in-dir symlink: %v", err)
	}
	if string(b) != "plain body\n" {
		t.Errorf("content = %q, want the target's bytes", b)
	}
}

// TestGuardedReadSymlinkEscape: a symlink inside dir pointing outside must
// fail with an escape error that is NOT IsNotExist (a swallow-as-missing
// regression would silently pass a stronger assertion).
func TestGuardedReadSymlinkEscape(t *testing.T) {
	dir, _, external, externalBody := guardedFixture(t)
	link := filepath.Join(dir, "evil.md")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	b, err := readWithinDir(dir, link)
	if err == nil {
		t.Fatalf("escape read returned %q — external bytes crossed the boundary", string(b))
	}
	if os.IsNotExist(err) {
		t.Errorf("escape error must not masquerade as absent: %v", err)
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("escape error must name the escape: %v", err)
	}
	if string(b) == externalBody {
		t.Error("external secret bytes leaked past the guard")
	}
}

// TestGuardedReadMissing: an absent path keeps the raw os error so callers
// that skip on err — or discriminate with os.IsNotExist — behave untouched.
func TestGuardedReadMissing(t *testing.T) {
	dir, _, _, _ := guardedFixture(t)
	_, err := readWithinDir(dir, filepath.Join(dir, "absent.md"))
	if err == nil {
		t.Fatal("missing file read unexpectedly succeeded")
	}
	if !os.IsNotExist(err) {
		t.Errorf("missing-file error must satisfy os.IsNotExist (got %v)", err)
	}
}

// TestGuardedReadSymlinkChainEscape: link → link → external resolves the
// full chain; containment decides on the FINAL target, so a two-hop launder
// is refused exactly like the direct escape.
func TestGuardedReadSymlinkChainEscape(t *testing.T) {
	dir, _, external, externalBody := guardedFixture(t)
	mid := filepath.Join(dir, "mid.md")
	if err := os.Symlink(external, mid); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(mid, link); err != nil {
		t.Fatal(err)
	}
	b, err := readWithinDir(dir, link)
	if err == nil {
		t.Fatalf("chained escape read returned %q — external bytes crossed the boundary", string(b))
	}
	if os.IsNotExist(err) {
		t.Errorf("escape error must not masquerade as absent: %v", err)
	}
	if string(b) == externalBody {
		t.Error("external secret bytes leaked past the guard")
	}
}

// TestGuardedReadWithinChain: a chained symlink whose FINAL target stays
// inside dir still reads — containment is decided on resolution, never on
// hop count.
func TestGuardedReadWithinChain(t *testing.T) {
	dir, plain, _, _ := guardedFixture(t)
	mid := filepath.Join(dir, "mid.md")
	if err := os.Symlink(plain, mid); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(mid, link); err != nil {
		t.Fatal(err)
	}
	b, err := readWithinDir(dir, link)
	if err != nil {
		t.Fatalf("in-dir chained symlink: %v", err)
	}
	if string(b) != "plain body\n" {
		t.Errorf("content = %q, want the final target's bytes", b)
	}
}
