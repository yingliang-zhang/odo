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
	// M12: auto-distill settings (default FLIPPED to "on_idle"; explicit
	// "never" is preserved). M10's auto_curate_after_distill is removed —
	// auto-curate is daemon-conditional now (notes ≥ threshold OR age ≥ max)
	// and shares this switch: "never" disables auto-curate too (fail-closed).
	AutoDistill            string `json:"auto_distill"`              // "never" | "on_idle"
	AutoDistillIdleSeconds string `json:"auto_distill_idle_seconds"` // e.g. "120"
	// M11 P3: parallelism cap (default 4)
	MaxConcurrentRuns string `json:"max_concurrent_runs"` // e.g. "4"
	// M15 (O-1 rung-0): autonomy pref. off|branch|main|all, default off.
	// M16 (O-1 v2): "main" IS consumed — diffs of clean runs go through
	// the auto-land pipeline (internal/ipc/autoland.go). "branch"/"all"
	// stay parsed-and-displayed only. Fail-closed parse unchanged: a
	// typo must never silently widen apply scope.
	AutoApply string `json:"auto_apply"`
	// Prewalk: opt-in cheap model for implementation phase after plan.
	// Empty = off (use coding model for entire run). When set, the adapter
	// passes --prewalk --prewalk-into=<model> to OMP, switching to the
	// smol model after the plan's todo list exists.
	PrewalkModel string `json:"prewalk_model"`
	// M19 (V11): loop_notify_on_complete (default on) — the GUI fires ONE
	// system notification on a loop's first terminal row and journals
	// loop_notified. Read-only over IPC (UpdateSettings never writes it;
	// the pref is hand-edited in prefs.md).
	LoopNotifyOnComplete bool `json:"loop_notify_on_complete"`
}

// loopNotifyOff marks the loop_notify_on_complete: off pref values.
func loopNotifyOff(v string) bool {
	switch strings.ToLower(v) {
	case "off", "false", "0", "no", "never":
		return true
	}
	return false
}

// autoApplyValues are the valid auto_apply pref values (rung-0 contract).
var autoApplyValues = map[string]bool{"off": true, "branch": true, "main": true, "all": true}

// ValidAutoApply reports whether v is a valid auto_apply pref value.
func ValidAutoApply(v string) bool { return autoApplyValues[v] }

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
		OMPTimeout:   resolveTimeout(defaultTimeoutSeconds),
		ReviewModels: LoadPrefsRaw("review"),
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
	// M12: auto-distill defaults flipped to on (explicit "never" preserved).
	s.AutoDistill = LoadPrefsRaw("auto_distill")
	if s.AutoDistill == "" {
		s.AutoDistill = "on_idle"
	}
	s.AutoDistillIdleSeconds = LoadPrefsRaw("auto_distill_idle_seconds")
	if s.AutoDistillIdleSeconds == "" {
		s.AutoDistillIdleSeconds = "120"
	}
	s.MaxConcurrentRuns = LoadPrefsRaw("max_concurrent_runs")
	if s.MaxConcurrentRuns == "" {
		s.MaxConcurrentRuns = "4"
	}
	// M15 (O-1 rung-0) → M20: auto-land is the DEFAULT landing canon, so
	// an ABSENT value reads "main" (on). An unknown non-empty value still
	// reads "off" — the fail-closed direction survived the flip: a typo
	// narrows scope to manual review, never widens it. "main"/"branch"/
	// "all" are back-compatible on-values; only "off" disables.
	s.AutoApply = LoadPrefsRaw("auto_apply")
	if s.AutoApply == "" {
		s.AutoApply = "main"
	} else if !ValidAutoApply(s.AutoApply) {
		s.AutoApply = "off"
	}
	s.PrewalkModel = LoadPrefsRaw("prewalk_model")
	// M19 (V11): default on; any explicit off-shape value disables.
	s.LoopNotifyOnComplete = !loopNotifyOff(LoadPrefsRaw("loop_notify_on_complete"))
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
	// M12: auto-distill settings
	if up.AutoDistill != "" {
		set("auto_distill", up.AutoDistill)
	}
	if up.AutoDistillIdleSeconds != "" {
		set("auto_distill_idle_seconds", up.AutoDistillIdleSeconds)
	}
	if up.MaxConcurrentRuns != "" {
		set("max_concurrent_runs", up.MaxConcurrentRuns)
	}
	// M15 (O-1 rung-0): auto_apply is validated BEFORE anything is
	// written — reject unknown values outright rather than persisting a
	// value this daemon will read back as "off" while the file claims
	// otherwise.
	if up.AutoApply != "" {
		if !ValidAutoApply(up.AutoApply) {
			return fmt.Errorf("prefs: auto_apply must be one of off|branch|main|all, got %q", up.AutoApply)
		}
		set("auto_apply", up.AutoApply)
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

// RemovePrefsKey deletes the `key:` line from ~/.odo/prefs.md (all other
// lines preserved verbatim, same atomic write as UpdateSettings) and
// reports whether a line was removed. Used by migrations that retire a
// managed pref (M12: auto_curate_after_distill).
func RemovePrefsKey(key string) (bool, error) {
	path, err := prefsPath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	prefix := key + ":"
	var kept []string
	removed := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return false, nil
	}
	content := ""
	if len(kept) > 0 && !(len(kept) == 1 && kept[0] == "") {
		content = strings.Join(kept, "\n") + "\n"
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".prefs-*.tmp")
	if err != nil {
		return false, fmt.Errorf("prefs: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return false, fmt.Errorf("prefs: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return false, fmt.Errorf("prefs: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return false, fmt.Errorf("prefs: rename: %w", err)
	}
	return true, nil
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
