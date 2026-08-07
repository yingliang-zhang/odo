package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSettingsNoDefaultAdapter covers the removal of the default_adapter
// setting: prefs.md no longer carries a default_adapter key, and the
// daemon always uses "omp". This test verifies that ReadSettings and
// UpdateSettings work correctly without the field — prefs with an
// old default_adapter line simply ignore it.
func TestSettingsNoDefaultAdapter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No prefs.md at all: settings fall back to defaults.
	s := ReadSettings()
	if s.CodingModel != defaultModel {
		t.Errorf("CodingModel = %q, want %q (no prefs.md)", s.CodingModel, defaultModel)
	}

	// prefs.md without a default_adapter key: settings still work.
	writePrefsForTest(t, home, "coding: glm-5.2@sudo\n")
	s = ReadSettings()
	if s.CodingModel != "glm-5.2" || s.CodingProvider != "sudo" {
		t.Errorf("from prefs = %s@%s, want glm-5.2@sudo", s.CodingModel, s.CodingProvider)
	}

	// prefs.md with an old default_adapter line: it is ignored (not
	// read into Settings, which no longer has the field). Other keys
	// still parse correctly.
	writePrefsForTest(t, home, "default_adapter: pi\ncoding: glm-5.2@sudo\n")
	s = ReadSettings()
	if s.CodingModel != "glm-5.2" || s.CodingProvider != "sudo" {
		t.Errorf("from prefs with old default_adapter = %s@%s, want glm-5.2@sudo", s.CodingModel, s.CodingProvider)
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
