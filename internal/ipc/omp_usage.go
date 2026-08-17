package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// ompUsageTimeout bounds each omp subprocess so a hung omp never blocks
// the daemon's IPC thread. 10s matches the task spec.
const ompUsageTimeout = 10 * time.Second

// runOmpJSON executes `omp <subcommand> --json` with a bounded timeout and
// returns the raw JSON bytes. A missing binary or non-zero exit yields an
// error (the caller degrades gracefully). The output is NOT validated as
// valid JSON — the handler merges it into a passthrough blob and the GUI
// guards against malformed shapes at render time.
func runOmpJSON(ctx context.Context, subcommand string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, ompUsageTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "omp", subcommand, "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("omp %s: %w%s", subcommand, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// handleOmpUsage fetches `omp usage --json` and `omp grievances --json`,
// merges them into a single JSON object, and returns the raw bytes as a
// passthrough. The merged shape is:
//
//	{"usage": <omp usage JSON>, "grievances": <omp grievances JSON>}
//
// When omp is unavailable or either command fails, the corresponding field
// is set to null and an "errors" array collects the error strings — the GUI
// renders "unavailable" for failed sections rather than failing the whole
// request. The data is NEVER journaled (pure display, P2 constraint).
func (s *Server) handleOmpUsage(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("omp_usage: %w", err)
	}

	// Fetch both in parallel — neither depends on the other.
	type result struct {
		data  json.RawMessage
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
		usageCh <- result{data: json.RawMessage(data)}
	}()
	go func() {
		data, err := runOmpJSON(ctx, "grievances")
		if err != nil {
			grievancesCh <- result{error: err.Error()}
			return
		}
		grievancesCh <- result{data: json.RawMessage(data)}
	}()

	usage := <-usageCh
	grievances := <-grievancesCh

	// Build the merged JSON object. Failed sections degrade to null.
	merged := map[string]interface{}{
		"usage":      nil,
		"grievances": nil,
	}
	if usage.data != nil {
		// Unmarshal/re-marshal to embed as a proper JSON value (not a
		// string). If the output isn't valid JSON, treat it as an error.
		var raw interface{}
		if err := json.Unmarshal(usage.data, &raw); err != nil {
			merged["usage"] = nil
			merged["errors"] = appendErrors(merged["errors"], "usage: invalid JSON from omp")
		} else {
			merged["usage"] = raw
		}
	} else if usage.error != "" {
		merged["errors"] = appendErrors(merged["errors"], usage.error)
	}
	if grievances.data != nil {
		var raw interface{}
		if err := json.Unmarshal(grievances.data, &raw); err != nil {
			merged["grievances"] = nil
			merged["errors"] = appendErrors(merged["errors"], "grievances: invalid JSON from omp")
		} else {
			merged["grievances"] = raw
		}
	} else if grievances.error != "" {
		merged["errors"] = appendErrors(merged["errors"], grievances.error)
	}

	blob, err := json.Marshal(merged)
	if err != nil {
		return Response{}, fmt.Errorf("omp_usage: marshal merged: %w", err)
	}
	return Response{OmpUsage: json.RawMessage(blob)}, nil
}

// appendErrors is a small helper that initializes the errors slice on first
// use (Go nil-slice append works but this keeps the JSON shape clean: an
// empty array serializes as [], not null, only when the key is set).
func appendErrors(current interface{}, msg string) []string {
	slice, _ := current.([]string)
	return append(slice, msg)
}
