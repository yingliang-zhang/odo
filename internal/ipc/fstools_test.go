package ipc

import (
	"os"
	"path/filepath"
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
