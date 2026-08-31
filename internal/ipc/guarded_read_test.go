package ipc

// readWithinDir / guardProjectWritePath unit tests (2026-08-24 tri-review
// P0; anchor + root-node cases 2026-08-25 review P0): the guard keeps
// plain in-dir reads byte-identical to os.ReadFile, passes symlinks whose
// resolved target stays inside dir, refuses every escape shape (direct,
// chained, a symlinked ROOT NODE, a symlinked ancestor judged against the
// canonical project root), and preserves the raw os error for missing
// files so callers keep their skip/IsNotExist semantics. The write-side
// walk refuses any symlinked component from the first under-root node to
// the file itself. Site-level symlink regressions live next to the recall
// and learner rigs (m6_test.go / learner_test.go).
// The capped twin (2026-08-26 audit P2) pins the stat→read growth window:
// at-cap reads stay byte-identical, a file grown past the cap refuses
// with the errFileTooLarge sentinel after reading only max+1 bytes, and
// every escape shape refuses identically through the shared prologue.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardedFixture builds dir/ under a project root with a regular file
// plus an external "secret" file OUTSIDE the tree (the attacker payoff an
// implanted symlink must never deliver). Returns everything the read
// shapes below need.
func guardedFixture(t *testing.T) (root, dir, plain, external, externalBody string) {
	t.Helper()
	root = t.TempDir()
	dir = filepath.Join(root, "dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plain = filepath.Join(dir, "plain.md")
	if err := os.WriteFile(plain, []byte("plain body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	extBase := t.TempDir()
	external = filepath.Join(extBase, "secret.md")
	externalBody = "EXTERNAL-SECRET-BYTES\n"
	if err := os.WriteFile(external, []byte(externalBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, dir, plain, external, externalBody
}

// TestGuardedReadPlainFile: a regular file inside dir reads exactly as
// os.ReadFile — the guard adds no behavior for the common path.
func TestGuardedReadPlainFile(t *testing.T) {
	t.Parallel()
	root, dir, plain, _, _ := guardedFixture(t)
	b, err := readWithinDir(root, dir, plain)
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
	t.Parallel()
	root, dir, plain, _, _ := guardedFixture(t)
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(plain, link); err != nil {
		t.Fatal(err)
	}
	b, err := readWithinDir(root, dir, link)
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
	t.Parallel()
	root, dir, _, external, externalBody := guardedFixture(t)
	link := filepath.Join(dir, "evil.md")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	b, err := readWithinDir(root, dir, link)
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
	t.Parallel()
	root, dir, _, _, _ := guardedFixture(t)
	_, err := readWithinDir(root, dir, filepath.Join(dir, "absent.md"))
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
	t.Parallel()
	root, dir, _, external, externalBody := guardedFixture(t)
	mid := filepath.Join(dir, "mid.md")
	if err := os.Symlink(external, mid); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(mid, link); err != nil {
		t.Fatal(err)
	}
	b, err := readWithinDir(root, dir, link)
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
	t.Parallel()
	root, dir, plain, _, _ := guardedFixture(t)
	mid := filepath.Join(dir, "mid.md")
	if err := os.Symlink(plain, mid); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(mid, link); err != nil {
		t.Fatal(err)
	}
	b, err := readWithinDir(root, dir, link)
	if err != nil {
		t.Fatalf("in-dir chained symlink: %v", err)
	}
	if string(b) != "plain body\n" {
		t.Errorf("content = %q, want the final target's bytes", b)
	}
}

// --- 2026-08-25 review P0: the anchor cases ------------------------------

// TestGuardedReadRootNodeSymlinkRefused: dir ITSELF as a symlink used to
// seed the containment base with the attacker's target tree (checked-in
// `wiki -> ~/.ssh`, then <project>/wiki/id_rsa "contained" inside it).
// The root node is refused as a link outright — even pointed at a
// legitimate in-project directory, because daemon-owned dirs are never
// links and the stronger rule needs no intent judgment.
func TestGuardedReadRootNodeSymlinkRefused(t *testing.T) {
	t.Parallel()
	root, _, _, external, externalBody := guardedFixture(t)

	// Case A: root node -> external dir holding a same-named file. The
	// pre-anchor guard called this "contained" and served the bytes.
	extDir := t.TempDir()
	if err := os.Symlink(external, filepath.Join(extDir, "decoy.md")); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "wiki")
	if err := os.Symlink(extDir, linkDir); err != nil {
		t.Fatal(err)
	}
	if b, err := readWithinDir(root, linkDir, filepath.Join(linkDir, "decoy.md")); err == nil {
		t.Fatalf("root-node symlink read returned %q — external bytes crossed the boundary", string(b))
	} else {
		if !strings.Contains(err.Error(), "symlink") {
			t.Errorf("refusal = %v, want the symlinked root node named", err)
		}
		if os.IsNotExist(err) {
			t.Errorf("refusal must not masquerade as absent: %v", err)
		}
		if string(b) == externalBody {
			t.Error("external secret bytes leaked past the guard")
		}
	}

	// Case B: root node -> a legitimate in-project directory. Refused all
	// the same: .odo/wiki-style roots are daemon-owned real directories.
	inner := filepath.Join(root, "real-wiki")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "n.md"), []byte("in-project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link2 := filepath.Join(root, "wiki2")
	if err := os.Symlink(inner, link2); err != nil {
		t.Fatal(err)
	}
	if _, err := readWithinDir(root, link2, filepath.Join(link2, "n.md")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("in-project root-node link: err = %v, want the symlink refusal", err)
	}
}

// TestGuardedReadAncestorSymlinkCaught: a symlinked ANCESTOR between the
// project root and dir (wiki/ -> /external with dir = wiki/topics, or the
// topics node itself) resolves outside the canonical project root — the
// anchor refuses where resolution-against-dir saw a coherent tree.
func TestGuardedReadAncestorSymlinkCaught(t *testing.T) {
	t.Parallel()
	root, _, _, _, _ := guardedFixture(t)

	// wiki -> external: dir = wiki/topics must not contain against the
	// external tree.
	extWiki := t.TempDir()
	extTopics := filepath.Join(extWiki, "topics")
	if err := os.MkdirAll(extTopics, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extTopics, "page.md"), []byte("external page\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(extWiki, filepath.Join(root, "wiki")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "wiki", "topics")
	if _, err := readWithinDir(root, dir, filepath.Join(dir, "page.md")); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("symlinked ancestor: err = %v, want the project-root escape named", err)
	}

	// .odo -> external: the .odo root node itself is caught by the same
	// ancestor resolution from any dir beneath it.
	extOdo := t.TempDir()
	if err := os.WriteFile(filepath.Join(extOdo, "memory.md"), []byte("external rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(extOdo, filepath.Join(root, ".odo")); err != nil {
		t.Fatal(err)
	}
	odoDir := filepath.Join(root, ".odo")
	if _, err := readWithinDir(root, odoDir, filepath.Join(odoDir, "memory.md")); err == nil {
		t.Error(".odo symlinked out: read succeeded — external bytes would feed the prompt")
	}
}

// TestGuardedReadCanonicalRootAnchor: a project registered THROUGH a
// symlinked path is legitimate — the canonical anchor resolves it, so
// ordinary in-tree reads under the linked registration path still work.
func TestGuardedReadCanonicalRootAnchor(t *testing.T) {
	t.Parallel()
	root, dir, plain, _, _ := guardedFixture(t)
	linkRoot := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(linkRoot, "dir")
	linkedPlain := filepath.Join(linkRoot, "dir", filepath.Base(plain))
	b, err := readWithinDir(linkRoot, linkedDir, linkedPlain)
	if err != nil {
		t.Fatalf("read through symlinked project registration: %v", err)
	}
	if string(b) != "plain body\n" {
		t.Errorf("content = %q, want the file verbatim", b)
	}
	_ = dir
}

// TestGuardedWritePath: the write-side walk refuses any symlinked
// component from the first under-root node (the .odo/wiki ROOT NODE)
// through the file itself, passes real trees, and lets a symlinked
// PROJECT ROOT (a legitimate registration shape) write in-tree.
func TestGuardedWritePath(t *testing.T) {
	t.Parallel()
	root, _, _, _, _ := guardedFixture(t)
	ext := t.TempDir()

	// .odo root node as a link: every write beneath refuses.
	if err := os.Symlink(ext, filepath.Join(root, ".odo")); err != nil {
		t.Fatal(err)
	}
	if err := guardProjectWritePath(root, filepath.Join(root, ".odo", "memory.md")); err == nil || !strings.Contains(err.Error(), "symlinked component") {
		t.Errorf(".odo link: err = %v, want the symlinked component named", err)
	}
	os.Remove(filepath.Join(root, ".odo"))

	// Symlinked INTERMEDIATE (wiki -> external) while writing
	// wiki/topics/page.md: refuses at the intermediate component.
	if err := os.Symlink(ext, filepath.Join(root, "wiki")); err != nil {
		t.Fatal(err)
	}
	if err := guardProjectWritePath(root, filepath.Join(root, "wiki", "topics", "page.md")); err == nil || !strings.Contains(err.Error(), "symlinked component") {
		t.Errorf("wiki link: err = %v, want the symlinked component named", err)
	}
	os.Remove(filepath.Join(root, "wiki"))

	// Symlinked FINAL component: os.WriteFile would follow it.
	if err := os.MkdirAll(filepath.Join(root, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ext, "target.md"), filepath.Join(root, ".odo", "pins.md")); err != nil {
		t.Fatal(err)
	}
	if err := guardProjectWritePath(root, filepath.Join(root, ".odo", "pins.md")); err == nil || !strings.Contains(err.Error(), "symlinked component") {
		t.Errorf("final-component link: err = %v, want the symlinked component named", err)
	}
	os.Remove(filepath.Join(root, ".odo", "pins.md"))

	// Real tree, including not-yet-existing depth: passes, and the atomic
	// twin actually writes.
	for _, p := range []string{
		filepath.Join(root, ".odo", "memory.md"),
		filepath.Join(root, "wiki", "topics", "fresh.md"),
		filepath.Join(root, ".odo", "loop", "7", "body.txt"),
	} {
		if err := guardProjectWritePath(root, p); err != nil {
			t.Errorf("real path %s: %v", p, err)
		}
	}
	target := filepath.Join(root, "wiki", "topics", "fresh.md")
	if err := writeFileWithin(root, target, "body\n", 0o644); err != nil {
		t.Fatalf("writeFileWithin: %v", err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "body\n" {
		t.Errorf("written content = %q, %v", b, err)
	}

	// Outside the root entirely: refused as a callsite bug, loudly.
	if err := guardProjectWritePath(root, filepath.Join(ext, "x.md")); err == nil || !strings.Contains(err.Error(), "not under project root") {
		t.Errorf("outside-root path: err = %v, want the not-under refusal", err)
	}

	// The root itself MAY be a symlink (registered through a link): the
	// walk starts BELOW it, so in-tree writes pass.
	linkRoot := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Fatal(err)
	}
	if err := guardProjectWritePath(linkRoot, filepath.Join(linkRoot, ".odo", "memory.md")); err != nil {
		t.Errorf("write beneath symlinked registration root: %v", err)
	}
}

// --- readWithinDirCapped (2026-08-26 audit P2) --------------------------

// TestGuardedReadCappedPlainFile: the capped twin reads an in-cap file
// byte-identically — AT the cap included, since max+1 reads classify
// only "over", never truncate "exactly at".
func TestGuardedReadCappedPlainFile(t *testing.T) {
	t.Parallel()
	root, dir, plain, _, _ := guardedFixture(t)
	const body = "plain body\n"
	b, err := readWithinDirCapped(root, dir, plain, int64(len(body)))
	if err != nil {
		t.Fatalf("at-cap read: %v — exactly-at-cap must succeed", err)
	}
	if string(b) != body {
		t.Errorf("content = %q, want the file verbatim", b)
	}
	// One byte past the cap: refused with the sentinel, file named.
	if _, err := readWithinDirCapped(root, dir, plain, int64(len(body))-1); err == nil || !errors.Is(err, errFileTooLarge) {
		t.Errorf("one-over-cap read: err = %v, want the errFileTooLarge sentinel", err)
	}
}

// TestGuardedReadCappedGrowthRace is the audit P2 drill: a file under
// the cap at a caller's stat pre-check that keeps growing lands in the
// read as an unbounded allocation unless the read itself is bounded.
// The cappedReadPreOpenHook seam grows the file after the containment
// proof and before the open — the exact stat→read window,
// deterministically (no sleeps); the twin must refuse with the sentinel
// having read only max+1 bytes: the megabyte beyond never allocates.
func TestGuardedReadCappedGrowthRace(t *testing.T) {
	t.Parallel()
	root, dir, _, _, _ := guardedFixture(t)
	grower := filepath.Join(dir, "grow.md")
	if err := os.WriteFile(grower, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	const maxB = 16
	armed := true
	cappedReadPreOpenHook = func(path string) {
		if !armed || path != grower {
			return
		}
		armed = false // one-shot: fires exactly inside this read's window
		if err := os.WriteFile(grower, []byte(strings.Repeat("x", 1<<20)), 0o644); err != nil {
			t.Errorf("grow fixture: %v", err)
		}
	}
	defer func() { cappedReadPreOpenHook = nil }()

	_, err := readWithinDirCapped(root, dir, grower, maxB)
	if err == nil || !errors.Is(err, errFileTooLarge) {
		t.Fatalf("grew-past-stat read: err = %v, want the errFileTooLarge sentinel", err)
	}
	if !strings.Contains(err.Error(), "17B read exceeds the 16B cap") {
		t.Errorf("err = %q, want the bounded byte count (17B read, cap 16B) proving the megabyte growth never allocated", err)
	}
	if armed {
		t.Error("seam never fired — the drill tested nothing")
	}
}

// TestGuardedReadCappedEscape: the capped twin shares resolveWithinDir
// with readWithinDir, so the escape refusal and the raw missing-file
// error land identically (the full escape matrix above already guards
// the shared prologue).
func TestGuardedReadCappedEscape(t *testing.T) {
	t.Parallel()
	root, dir, _, external, _ := guardedFixture(t)
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := readWithinDirCapped(root, dir, link, 64); err == nil || !strings.Contains(err.Error(), "symlink escapes") {
		t.Errorf("escaping symlink via capped read: err = %v, want the containment refusal", err)
	}
	if _, err := readWithinDirCapped(root, dir, filepath.Join(dir, "absent.md"), 64); err == nil || !os.IsNotExist(err) {
		t.Errorf("missing file via capped read: err = %v, want the raw not-exist os error", err)
	}
}
