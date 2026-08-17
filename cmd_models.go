package main

// cmd_models.go — `odo models bench` CLI: wraps `omp bench --json` to
// benchmark models (TTFT + tokens/s). Calibrates modelspec + genTokPerSecFloor.
// Read-only dev tool; zero daemon state.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func runModelsCLI(args []string) int {
	if len(args) == 0 {
		fmt.Println("Usage: odo models <subcommand>")
		fmt.Println("Subcommands:")
		fmt.Println("  bench <model> [model...]  Benchmark models (TTFT + tokens/s)")
		fmt.Println("  list                       List configured models from prefs")
		return 0
	}
	switch args[0] {
	case "bench":
		return runModelsBench(args[1:])
	case "list":
		return runModelsList()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		return 1
	}
}

func runModelsBench(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "bench: at least one model required")
		return 1
	}
	// Resolve the omp binary path (same logic as adapter/omp.go defaultWrapperPath)
	ompBin, err := exec.LookPath("omp")
	if err != nil {
		// Fallback: homebrew path
		ompBin = "/opt/homebrew/bin/omp"
	}
	// Build omp bench args
	benchArgs := []string{"bench", "--json"}
	benchArgs = append(benchArgs, args...)
	cmd := exec.Command(ompBin, benchArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// omp bench exits non-zero on partial failures; still print what we got
		if _, ok := err.(*exec.ExitError); ok {
			return err.(*exec.ExitError).ExitCode()
		}
		fmt.Fprintf(os.Stderr, "omp bench failed: %v\n", err)
		return 1
	}
	return 0
}

func runModelsList() int {
	s := readModelsSettings()
	w := os.Stdout
	fmt.Fprintln(w, "Configured models:")
	fmt.Fprintf(w, "  coding:       %s@%s\n", s.CodingModel, s.CodingProvider)
	fmt.Fprintf(w, "  orchestrator: %s@%s\n", s.OrchestratorModel, s.OrchestratorProvider)
	if s.ReviewModels != "" {
		fmt.Fprintf(w, "  review:       %s\n", s.ReviewModels)
	}
	if s.PrewalkModel != "" {
		fmt.Fprintf(w, "  prewalk:      %s\n", s.PrewalkModel)
	}
	return 0
}

// settingsStub mirrors adapter.Settings for the CLI path (avoids importing
// the adapter package which creates a daemon dependency).
type settingsStub struct {
	CodingModel           string `json:"coding_model"`
	CodingProvider        string `json:"coding_provider"`
	OrchestratorModel     string `json:"orchestrator_model"`
	OrchestratorProvider  string `json:"orchestrator_provider"`
	ReviewModels          string `json:"review_models"`
	PrewalkModel          string `json:"prewalk_model"`
}

func readModelsSettings() settingsStub {
	// Read ~/.odo/prefs.md — simple key: value parser (same as adapter.LoadPrefsRaw)
	home, _ := os.UserHomeDir()
	data, _ := os.ReadFile(home + "/.odo/prefs.md")
	s := settingsStub{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch key {
		case "coding":
			s.CodingModel, s.CodingProvider = parseModelProvider(val)
		case "orchestrator":
			s.OrchestratorModel, s.OrchestratorProvider = parseModelProvider(val)
		case "review":
			s.ReviewModels = val
		case "prewalk_model":
			s.PrewalkModel = val
		}
	}
	return s
}

func parseModelProvider(val string) (model, provider string) {
	if idx := strings.Index(val, "@"); idx > 0 {
		return val[:idx], val[idx+1:]
	}
	return val, ""
}

// keep the json import used (future: parse omp bench --json output)
var _ = json.Unmarshal
