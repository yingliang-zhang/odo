package ipc

import (
	"path/filepath"
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
