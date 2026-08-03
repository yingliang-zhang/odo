package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultAdapterFallback covers the M2 F5 follow-up (M3 3d): an empty
// adapter name resolves the prefs.md default_adapter key, falling back to
// the compiled-in "omp" when the key (or the file) is absent. A non-empty
// name passes through unchanged.
func TestDefaultAdapterFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No prefs.md at all: compiled-in default.
	if got := ResolveAdapter(""); got != "omp" {
		t.Errorf("ResolveAdapter(\"\") = %q, want %q (no prefs.md)", got, "omp")
	}

	// prefs.md without the key: still the compiled-in default.
	writePrefsForTest(t, home, "coding: glm-5.2@sudo\n")
	if got := ResolveAdapter(""); got != "omp" {
		t.Errorf("ResolveAdapter(\"\") = %q, want %q (key absent)", got, "omp")
	}

	// prefs.md with the key: the empty name resolves it.
	writePrefsForTest(t, home, "default_adapter: pi\n")
	if got := ResolveAdapter(""); got != "pi" {
		t.Errorf("ResolveAdapter(\"\") = %q, want %q (prefs default_adapter)", got, "pi")
	}

	// A non-empty name is returned unchanged.
	if got := ResolveAdapter("pi"); got != "pi" {
		t.Errorf("ResolveAdapter(%q) = %q, want %q", "pi", got, "pi")
	}
}

// writePrefsForTest writes ~/.odo/prefs.md under the test home directory.
func writePrefsForTest(t *testing.T, home, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".odo", "prefs.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
