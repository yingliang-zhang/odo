package ipc

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestDefaultFSDenyCredentialEntries verifies the default deny list
// contains the M17 audit's credential entries (Hole 1).
func TestDefaultFSDenyCredentialEntries(t *testing.T) {
	want := map[string]bool{
		".ssh": true, ".aws": true, ".gnupg": true,
		".netrc": true, ".kube": true, ".docker": true,
		".npmrc": true, ".pypirc": true,
		".git-credentials": true,
	}
	for _, d := range defaultFSDeny {
		delete(want, d)
	}
	if len(want) > 0 {
		t.Errorf("defaultFSDeny missing: %v", want)
	}
}

// TestFSDenyBlocksCredentialPaths verifies check() rejects every
// default deny entry (both files and dirs).
func TestFSDenyBlocksCredentialPaths(t *testing.T) {
	home := t.TempDir()
	deny := make([]string, 0, len(defaultFSDeny))
	for _, d := range defaultFSDeny {
		deny = append(deny, filepath.Join(home, d))
	}
	exec := &fsToolExecutor{root: home, home: home, deny: deny}

	for _, d := range defaultFSDeny {
		p := filepath.Join(home, d)
		if err := exec.check(p); err == nil {
			t.Errorf("check(%q) should be denied, got nil", d)
		}
	}
	// Child path under a denied dir is blocked (prefix match).
	if err := exec.check(filepath.Join(home, ".kube", "config")); err == nil {
		t.Error("check(~/.kube/config) should be denied")
	}
	// Case-variant denied paths are blocked (macOS APFS case-insensitivity).
	for _, d := range []string{".SSH", ".AWS", ".KUBE", ".Netrc", ".NPMRC"} {
		p := filepath.Join(home, d)
		if err := exec.check(p); err == nil {
			t.Errorf("check(%q) should be denied (case-fold)", d)
		}
	}
	// Non-denied path is allowed.
	if err := exec.check(filepath.Join(home, "project", "main.go")); err != nil {
		t.Errorf("check(~/project/main.go) = %v, want nil", err)
	}
}

// TestDefaultFSDenyNewEntries verifies the 2026-08 SEC audit batch entries
// are refused with the moa_fs_deny error — pure check() on every entry,
// plus on-disk touched paths through resolve() for a sample.
func TestDefaultFSDenyNewEntries(t *testing.T) {
	home := t.TempDir()
	deny := make([]string, 0, len(defaultFSDeny))
	for _, d := range defaultFSDeny {
		deny = append(deny, filepath.Join(home, d))
	}
	exec := &fsToolExecutor{root: home, home: home, deny: deny}

	// Every new batch entry (and one child path per dir-style entry) is
	// refused with the moa_fs_deny error.
	batch := []string{
		".claude", "CLAUDE.md", "Makefile", ".cargo", ".rustup",
		".thunderbird", "trustdb.gpg", "ages", ".gnupg/private-keys-v1.d",
		"pip", "__pycache__", ".venv", "venv", "node_modules", "swap",
		// Children under denied dirs inherit the prefix refusal.
		".claude/settings.json",
		"node_modules/pkg/index.js",
		".gnupg/private-keys-v1.d/some.key",
	}
	for _, d := range batch {
		p := filepath.Join(home, d)
		err := exec.check(p)
		if err == nil {
			t.Errorf("check(~/%s) should be denied (2026-08 batch), got nil", d)
			continue
		}
		if !strings.Contains(err.Error(), "moa_fs_deny") {
			t.Errorf("check(~/%s) error %q should mention moa_fs_deny", d, err)
		}
	}

	// Touched paths on disk are refused through resolve() as well.
	touchedFiles := []string{"CLAUDE.md", "Makefile", "swap"}
	for _, d := range touchedFiles {
		p := filepath.Join(home, d)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := exec.resolve(p); err == nil || !strings.Contains(err.Error(), "moa_fs_deny") {
			t.Errorf("resolve(~/%s) should refuse with moa_fs_deny, got %v", d, err)
		}
	}
	touchedDirs := []string{".claude", "node_modules", ".venv"}
	for _, d := range touchedDirs {
		p := filepath.Join(home, d)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := exec.resolve(p); err == nil || !strings.Contains(err.Error(), "moa_fs_deny") {
			t.Errorf("resolve(~/%s) should refuse with moa_fs_deny, got %v", d, err)
		}
	}
}

// TestParseFSDenyDefaultsOnEmpty: an absent/whitespace prefs value yields
// exactly the built-in defaults, in declared order (ADR-0004 merge
// semantics; there is no syntax for an empty list).
func TestParseFSDenyDefaultsOnEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t "} {
		if got := parseFSDeny(raw); !slices.Equal(got, defaultFSDeny) {
			t.Errorf("parseFSDeny(%q) = %v, want defaultFSDeny %v", raw, got, defaultFSDeny)
		}
	}
}

// TestParseFSDenyUnion: bare tokens extend the defaults (fail-closed —
// the old replace parse dropped every default on the first prefs token);
// restated defaults and case variants dedupe to one entry.
func TestParseFSDenyUnion(t *testing.T) {
	got := parseFSDeny("tmp, .ssh, TMP")
	if len(got) != len(defaultFSDeny)+1 {
		t.Fatalf("parseFSDeny union len = %d, want %d: %v", len(got), len(defaultFSDeny)+1, got)
	}
	if !slices.Equal(got[:len(defaultFSDeny)], defaultFSDeny) {
		t.Errorf("defaults must lead in declared order, got %v", got)
	}
	if got[len(defaultFSDeny)] != "tmp" {
		t.Errorf("addition appended in file order = %q, want tmp", got[len(defaultFSDeny)])
	}
	n := 0
	for _, d := range got {
		if strings.EqualFold(d, "tmp") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("tmp/TMP must dedupe case-insensitively, got %d entries in %v", n, got)
	}
}

// TestParseFSDenyRemoval: a -name token subtracts exactly the named
// default; an unknown -name is a no-op.
func TestParseFSDenyRemoval(t *testing.T) {
	got := parseFSDeny("-node_modules")
	if slices.Contains(got, "node_modules") {
		t.Errorf("-node_modules should remove the default, got %v", got)
	}
	if !slices.Contains(got, ".ssh") || !slices.Contains(got, "Makefile") {
		t.Errorf("other defaults must survive a removal, got %v", got)
	}
	if len(got) != len(defaultFSDeny)-1 {
		t.Errorf("len = %d, want %d (only the named entry removed)", len(got), len(defaultFSDeny)-1)
	}
	if got := parseFSDeny("-nope"); !slices.Equal(got, defaultFSDeny) {
		t.Errorf("-nope should be a no-op, got %v", got)
	}
}

// TestParseFSDenyRemovalCaseInsensitive: removal names fold like check()
// does on macOS APFS.
func TestParseFSDenyRemovalCaseInsensitive(t *testing.T) {
	got := parseFSDeny("-NODE_MODULES")
	if slices.Contains(got, "node_modules") {
		t.Errorf("-NODE_MODULES should remove node_modules (case-fold), got %v", got)
	}
	if len(got) != len(defaultFSDeny)-1 {
		t.Errorf("len = %d, want %d", len(got), len(defaultFSDeny)-1)
	}
}

// TestParseFSDenyContradictionDenies: a name both added and removed stays
// denied, in either token order — the surprising direction is the safe
// direction.
func TestParseFSDenyContradictionDenies(t *testing.T) {
	for _, raw := range []string{"foo, -foo", "-foo, foo"} {
		if got := parseFSDeny(raw); !slices.Contains(got, "foo") {
			t.Errorf("parseFSDeny(%q) = %v: contradictory name must stay denied", raw, got)
		}
	}
	// Same rule when the name is also a built-in default.
	for _, raw := range []string{"-node_modules, node_modules", "node_modules, -node_modules"} {
		if got := parseFSDeny(raw); !slices.Contains(got, "node_modules") {
			t.Errorf("parseFSDeny(%q) = %v: contradictory default must stay denied", raw, got)
		}
	}
}

// TestParseFSDenyNoiseTokens: noise-only values yield the full defaults.
// Regression pin for the live fail-open hole: the old parser treated
// "moa_fs_deny: ," as replace-with-nothing and denied ZERO paths.
func TestParseFSDenyNoiseTokens(t *testing.T) {
	for _, raw := range []string{",,,", " - "} {
		if got := parseFSDeny(raw); !slices.Equal(got, defaultFSDeny) {
			t.Errorf("parseFSDeny(%q) = %v, want full defaultFSDeny", raw, got)
		}
	}
}

// TestNewFSToolExecutorDenyMerge is the headline fail-open regression: a
// prefs line adding one dir must NOT drop the credential defaults. Fails
// on the pre-W4 replace parser (which denied only ~/tmp here).
func TestNewFSToolExecutorDenyMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "moa_fs_deny: tmp\n")
	exec := newFSToolExecutor()

	if err := exec.check(filepath.Join(exec.root, ".ssh")); err == nil {
		t.Error("prefs 'tmp' must not drop the .ssh default (fail-open regression)")
	}
	if err := exec.check(filepath.Join(exec.root, "tmp", "scratch.txt")); err == nil {
		t.Error("prefs addition ~/tmp should be denied")
	} else if !strings.Contains(err.Error(), "moa_fs_deny") {
		t.Errorf("~/tmp error %q should name moa_fs_deny", err)
	}
	if err := exec.check(filepath.Join(exec.root, "main.go")); err != nil {
		t.Errorf("non-denied path = %v, want nil", err)
	}
}

// TestNewFSToolExecutorDenyReset: absent and empty prefs values both
// resolve to exactly the resolved defaults (factory reset = clear the
// line; the empty-value path is also the GUI "clear the field" flow).
func TestNewFSToolExecutorDenyReset(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prefs string
	}{
		{"absent", "# mine\ncoding: x@y\n"},
		{"empty", "moa_fs_deny:\n"},
		// The pre-merge live hole: a noise-only value built entries=nil —
		// zero paths denied. Pin it at the executor level (GLM review ⚠️2).
		{"noise", "moa_fs_deny: ,,,\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			writePrefs(t, home, tc.prefs)
			exec := newFSToolExecutor()
			want := make([]string, 0, len(defaultFSDeny))
			for _, d := range defaultFSDeny {
				want = append(want, filepath.Clean(filepath.Join(exec.root, d)))
			}
			if !slices.Equal(exec.deny, want) {
				t.Errorf("deny = %v, want resolved defaults %v", exec.deny, want)
			}
			if err := exec.check(filepath.Join(exec.root, ".aws")); err == nil {
				t.Error(".aws default should still be denied")
			}
		})
	}
}

// TestParseFSDenyRemovalAnyEntry pins the locked NO-floor semantics: every
// entry, credentials included, is removable by an explicit `-` token (a
// recorded conscious act, ADR-0004). A future protected-floor ADR MUST
// change this test — if it still passes, the floor is not implemented.
func TestParseFSDenyRemovalAnyEntry(t *testing.T) {
	got := parseFSDeny("-.ssh")
	if slices.Contains(got, ".ssh") {
		t.Errorf("-.ssh must remove .ssh (no protected floor), got %v", got)
	}
	if len(got) != len(defaultFSDeny)-1 {
		t.Errorf("len = %d, want %d", len(got), len(defaultFSDeny)-1)
	}
}
