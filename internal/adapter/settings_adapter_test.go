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

// TestAutoApplyPref pins the pref contract (M15 rung-0 → M20): the pref
// parses with default "main" (auto-land is the default landing canon),
// unknown non-empty values read back fail-closed as "off" (a typo narrows
// scope, never widens), and UpdateSettings rejects invalid values (writing
// nothing).
func TestAutoApplyPref(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Absent pref -> default on ("main"); the pipeline arms unarmed-silently
	// (a prefs.md without a review: line still never spends).
	if s := ReadSettings(); s.AutoApply != "main" {
		t.Errorf("AutoApply = %q, want main (no prefs.md → M20 default-on)", s.AutoApply)
	}

	// Every valid value round-trips.
	for _, v := range []string{"off", "branch", "main", "all"} {
		writePrefsForTest(t, home, "auto_apply: "+v+"\n")
		if s := ReadSettings(); s.AutoApply != v {
			t.Errorf("AutoApply = %q, want %q", s.AutoApply, v)
		}
	}

	// Unknown value in the file reads fail-closed as off.
	writePrefsForTest(t, home, "auto_apply: yolo\n")
	if s := ReadSettings(); s.AutoApply != "off" {
		t.Errorf("AutoApply = %q, want off (fail-closed on unknown)", s.AutoApply)
	}

	// UpdateSettings validates BEFORE writing.
	writePrefsForTest(t, home, "coding: glm-5.2@sudo\n")
	if err := UpdateSettings(Settings{AutoApply: "everything"}); err == nil {
		t.Fatal("UpdateSettings(invalid auto_apply) = nil error, want validation error")
	}
	if s := ReadSettings(); s.AutoApply != "main" {
		t.Errorf("after rejected update AutoApply = %q, want main (file untouched → M20 default-on)", s.AutoApply)
	}
	if err := UpdateSettings(Settings{AutoApply: "branch"}); err != nil {
		t.Fatal(err)
	}
	if s := ReadSettings(); s.AutoApply != "branch" {
		t.Errorf("after update AutoApply = %q, want branch", s.AutoApply)
	}
	if s := ReadSettings(); s.CodingModel != "glm-5.2" {
		t.Errorf("CodingModel = %q, want glm-5.2 (other keys preserved)", s.CodingModel)
	}
}

// TestK8sPrefsRoundTrip pins the k8s settings write path: the three
// UX-2 (A2-3) keys previously mapped in ReadSettings but silently
// dropped by UpdateSettings (GUI writes vanished). Writing them must
// persist verbatim and read back unchanged; charset validation stays
// with the k8s_status handler (fail loud at read time, not at write).
func TestK8sPrefsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Absent prefs read back empty (feature off).
	if s := ReadSettings(); s.K8sNamespace != "" || s.K8sContext != "" || s.K8sJobSelector != "" {
		t.Errorf("empty prefs = %+q, want all k8s keys empty", []string{s.K8sNamespace, s.K8sContext, s.K8sJobSelector})
	}

	writePrefsForTest(t, home, "coding: glm-5.2@sudo\n")
	if err := UpdateSettings(Settings{
		K8sNamespace:   "lab",
		K8sContext:     "prod",
		K8sJobSelector: "app=training",
	}); err != nil {
		t.Fatal(err)
	}
	s := ReadSettings()
	if s.K8sNamespace != "lab" {
		t.Errorf("K8sNamespace = %q, want lab", s.K8sNamespace)
	}
	if s.K8sContext != "prod" {
		t.Errorf("K8sContext = %q, want prod", s.K8sContext)
	}
	if s.K8sJobSelector != "app=training" {
		t.Errorf("K8sJobSelector = %q, want app=training", s.K8sJobSelector)
	}
	if s.CodingModel != "glm-5.2" {
		t.Errorf("CodingModel = %q, want glm-5.2 (other keys preserved)", s.CodingModel)
	}

	// In-place update of an existing key (not just append).
	if err := UpdateSettings(Settings{K8sNamespace: "lab2"}); err != nil {
		t.Fatal(err)
	}
	s = ReadSettings()
	if s.K8sNamespace != "lab2" {
		t.Errorf("after in-place update K8sNamespace = %q, want lab2", s.K8sNamespace)
	}
	if s.K8sContext != "prod" || s.K8sJobSelector != "app=training" {
		t.Errorf("context/selector = %q/%q, want prod/app=training (untouched)", s.K8sContext, s.K8sJobSelector)
	}

	// D5b (A2-4): k8s_batch_dir rides the same non-empty write branch —
	// a multi-namespace comma list PLUS the batch dir both persist
	// verbatim (A4 D1: no migration; "lab" ≡ ["lab"]).
	if err := UpdateSettings(Settings{K8sBatchDir: "/cpfs/ylzhang/batches"}); err != nil {
		t.Fatal(err)
	}
	s = ReadSettings()
	if s.K8sBatchDir != "/cpfs/ylzhang/batches" {
		t.Errorf("K8sBatchDir = %q, want /cpfs/ylzhang/batches", s.K8sBatchDir)
	}
	if s.K8sNamespace != "lab2" || s.K8sContext != "prod" || s.K8sJobSelector != "app=training" {
		t.Errorf("siblings = %+q, want lab2/prod/app=training (untouched)", []string{s.K8sNamespace, s.K8sContext, s.K8sJobSelector})
	}
}
