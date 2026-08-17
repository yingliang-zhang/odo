package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/yingliang-zhang/odo/internal/adapter"
)

// ompUsageTimeout bounds each omp subprocess so a hung omp never blocks
// the daemon's IPC thread. 10s matches the task spec.
const ompUsageTimeout = 10 * time.Second

// ompStderrCap limits how much of omp's stderr is folded into the error
// message — prevents a chatty omp from dumping config/secrets onto the
// GUI's popover. The adapter's tailBuffer uses a similar 4KB cap.
const ompStderrCap = 1024

// runOmpJSON executes `omp <subcommand> --json` with a bounded timeout and
// returns the raw JSON bytes. A missing binary or non-zero exit yields an
// error (the caller degrades gracefully). The output is validated as JSON
// by the caller — this function returns raw bytes.
//
// EnrichedEnv is used so the subprocess finds `omp` even when the daemon
// is launched from a .app bundle (macOS GUI apps inherit a minimal PATH
// like /usr/bin:/bin — homebrew, ~/.omp/bin, etc. are missing).
func runOmpJSON(ctx context.Context, subcommand string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, ompUsageTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "omp", subcommand, "--json")
	cmd.Env = adapter.EnrichedEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Cap stderr to avoid dumping large/chatty output into the GUI.
		stderrStr := stderr.String()
		if len(stderrStr) > ompStderrCap {
			stderrStr = stderrStr[:ompStderrCap] + "…(truncated)"
		}
		return nil, fmt.Errorf("omp %s: %w%s", subcommand, err, stderrStr)
	}
	return stdout.Bytes(), nil
}

// ompUsageMerged is the typed merged blob returned to the GUI. Using a
// struct (not map[string]interface{}) avoids float64 coercion of large
// integers and keeps the JSON shape deterministic.
type ompUsageMerged struct {
	Usage      json.RawMessage `json:"usage,omitempty"`
	Grievances json.RawMessage `json:"grievances,omitempty"`
	Errors     []string        `json:"errors,omitempty"`
}

// handleOmpUsage fetches `omp usage --json` and `omp grievances --json`,
// merges them into a single JSON object, and returns the raw bytes as a
// passthrough. The merged shape is:
//
//	{"usage": <omp usage JSON>, "grievances": <omp grievances JSON>,
//	 "errors": ["..."]}
//
// When omp is unavailable or either command fails, the corresponding field
// is omitted (omitempty → absent, not null) and an "errors" array collects
// the error strings — the GUI renders "unavailable" for failed sections.
// The data is NEVER journaled (pure display, P2 constraint).
func (s *Server) handleOmpUsage(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("omp_usage: %w", err)
	}

	// Fetch both in parallel — neither depends on the other.
	type result struct {
		data  []byte
		error string
	}
	usageCh := make(chan result, 1)
	grievancesCh := make(chan result, 1)

	go func() {
		data, err := runOmpJSON(ctx, "usage")
		if err != nil {
			usageCh <- result{error: err.Error()}
			return
		}
		usageCh <- result{data: data}
	}()
	go func() {
		data, err := runOmpJSON(ctx, "grievances")
		if err != nil {
			grievancesCh <- result{error: err.Error()}
			return
		}
		grievancesCh <- result{data: data}
	}()

	usage := <-usageCh
	grievances := <-grievancesCh

	merged := ompUsageMerged{}

	// Validate and embed raw JSON — no unmarshal/re-marshal, preserving
	// omp's original field order and integer precision.
	if usage.data != nil {
		if json.Valid(usage.data) {
			merged.Usage = json.RawMessage(usage.data)
		} else {
			merged.Errors = append(merged.Errors, "usage: invalid JSON from omp")
		}
	} else if usage.error != "" {
		merged.Errors = append(merged.Errors, usage.error)
	}
	if grievances.data != nil {
		if json.Valid(grievances.data) {
			merged.Grievances = json.RawMessage(grievances.data)
		} else {
			merged.Errors = append(merged.Errors, "grievances: invalid JSON from omp")
		}
	} else if grievances.error != "" {
		merged.Errors = append(merged.Errors, grievances.error)
	}

	blob, err := json.Marshal(merged)
	if err != nil {
		return Response{}, fmt.Errorf("omp_usage: marshal merged: %w", err)
	}
	return Response{OmpUsage: json.RawMessage(blob)}, nil
}
