// Package adapter — settings: reading and writing ~/.odo/prefs.md, the
// single place daemon settings live (M0.1 design). Lines not managed by the
// settings commands (comments, unknown keys) are preserved verbatim.
package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Settings is the daemon settings shape served by the get_settings and
// update_settings IPC commands.
type Settings struct {
	CodingModel          string `json:"coding_model"`
	CodingProvider       string `json:"coding_provider"`
	OrchestratorModel    string `json:"orchestrator_model"`
	OrchestratorProvider string `json:"orchestrator_provider"`
	OMPTimeout           string `json:"omp_timeout"`
	ReviewModels         string `json:"review_models"` // comma-separated model@provider entries
	// M10: auto-distill settings (opt-in, default "never")
	AutoDistill              string `json:"auto_distill"`               // "never" | "on_idle"
	AutoDistillIdleSeconds   string `json:"auto_distill_idle_seconds"`  // e.g. "30"
	AutoCurateAfterDistill   string `json:"auto_curate_after_distill"` // "true" | "false"
	// M11 P3: parallelism cap (default 4)
	MaxConcurrentRuns string `json:"max_concurrent_runs"` // e.g. "4"
}

// prefsPath returns ~/.odo/prefs.md.
func prefsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("prefs: home dir: %w", err)
	}
	return filepath.Join(home, ".odo", "prefs.md"), nil
}

// LoadPrefsRaw returns the trimmed value of the `key:` line from
// ~/.odo/prefs.md, or "" when the file is missing/unreadable or the line is
// absent.
func LoadPrefsRaw(key string) string {
	path, err := prefsPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := key + ":"
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// ParseModelProvider splits a `model@provider` value at the last `@` (model
// names may themselves contain `@`). Returns empty strings for malformed
// values.
func ParseModelProvider(val string) (model, provider string) {
	at := strings.LastIndex(val, "@")
	if at <= 0 || at == len(val)-1 {
		return "", ""
	}
	model = strings.TrimSpace(val[:at])
	provider = strings.TrimSpace(val[at+1:])
	if model == "" || provider == "" {
		return "", ""
	}
	return model, provider
}

// LoadPrefsByKey parses the `key: model@provider` line from ~/.odo/prefs.md
// (e.g. `coding: t9s/kimi-k3@sudo`).
func LoadPrefsByKey(key string) (model, provider string) {
	return ParseModelProvider(LoadPrefsRaw(key))
}

// resolveTimeout reads the `omp_timeout:` prefs line and validates it as a
// positive integer (seconds). Malformed or absent values fall back to def.
func resolveTimeout(def string) string {
	if v := LoadPrefsRaw("omp_timeout"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return v
		}
	}
	return def
}

// ReadSettings returns the effective daemon settings: prefs.md values where
// present, the compiled-in adapter defaults elsewhere.
func ReadSettings() Settings {
	s := Settings{
		OMPTimeout:     resolveTimeout(defaultTimeoutSeconds),
		ReviewModels:   LoadPrefsRaw("review"),
	}
	s.CodingModel, s.CodingProvider = LoadPrefsByKey("coding")
	if s.CodingModel == "" {
		s.CodingModel = defaultModel
	}
	if s.CodingProvider == "" {
		s.CodingProvider = defaultProvider
	}
	s.OrchestratorModel, s.OrchestratorProvider = LoadPrefsByKey("orchestrator")
	if s.OrchestratorModel == "" {
		s.OrchestratorModel = defaultModel
	}
	if s.OrchestratorProvider == "" {
		s.OrchestratorProvider = defaultProvider
	}
	// M10: auto-distill settings (opt-in, default "never")
	s.AutoDistill = LoadPrefsRaw("auto_distill")
	if s.AutoDistill == "" {
		s.AutoDistill = "never"
	}
	s.AutoDistillIdleSeconds = LoadPrefsRaw("auto_distill_idle_seconds")
	if s.AutoDistillIdleSeconds == "" {
		s.AutoDistillIdleSeconds = "30"
	}
	s.AutoCurateAfterDistill = LoadPrefsRaw("auto_curate_after_distill")
	if s.AutoCurateAfterDistill == "" {
		s.AutoCurateAfterDistill = "false"
	}
	s.MaxConcurrentRuns = LoadPrefsRaw("max_concurrent_runs")
	if s.MaxConcurrentRuns == "" {
		s.MaxConcurrentRuns = "4"
	}
	return s
}

// UpdateSettings writes the non-empty fields of up to ~/.odo/prefs.md.
// Managed key lines are updated in place first, appended when absent; all
// other lines (comments, unknown keys) pass through unchanged. A half-given
// model pair (model without provider or vice versa) keeps the current file
// value for the missing half.
func UpdateSettings(up Settings) error {
	path, err := prefsPath()
	if err != nil {
		return err
	}

	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		if content := strings.TrimRight(string(data), "\n"); content != "" {
			lines = strings.Split(content, "\n")
		}
	}
	set := func(key, val string) {
		prefix := key + ":"
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), prefix) {
				lines[i] = key + ": " + val
				return
			}
		}
		lines = append(lines, key+": "+val)
	}

	if up.CodingModel != "" || up.CodingProvider != "" {
		set("coding", mergeModelPair("coding", up.CodingModel, up.CodingProvider))
	}
	if up.OrchestratorModel != "" || up.OrchestratorProvider != "" {
		set("orchestrator", mergeModelPair("orchestrator", up.OrchestratorModel, up.OrchestratorProvider))
	}
	if up.ReviewModels != "" {
		set("review", up.ReviewModels)
	}
	if up.OMPTimeout != "" {
		set("omp_timeout", up.OMPTimeout)
	}
	// M10: auto-distill settings
	if up.AutoDistill != "" {
		set("auto_distill", up.AutoDistill)
	}
	if up.AutoDistillIdleSeconds != "" {
		set("auto_distill_idle_seconds", up.AutoDistillIdleSeconds)
	}
	if up.AutoCurateAfterDistill != "" {
		set("auto_curate_after_distill", up.AutoCurateAfterDistill)
	}
	if up.MaxConcurrentRuns != "" {
		set("max_concurrent_runs", up.MaxConcurrentRuns)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("prefs: create dir: %w", err)
	}
	// Atomic write: temp file in the same directory, then rename.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".prefs-*.tmp")
	if err != nil {
		return fmt.Errorf("prefs: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("prefs: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("prefs: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("prefs: rename: %w", err)
	}
	return nil
}

// mergeModelPair reconciles an updated model/provider pair against the
// current prefs value for key, falling back to the adapter defaults for any
// side neither the update nor the file provides.
func mergeModelPair(key, model, provider string) string {
	curModel, curProvider := LoadPrefsByKey(key)
	if model == "" {
		model = curModel
	}
	if provider == "" {
		provider = curProvider
	}
	if model == "" {
		model = defaultModel
	}
	if provider == "" {
		provider = defaultProvider
	}
	return model + "@" + provider
}
