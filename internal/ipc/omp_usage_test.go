package ipc

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestHandleOmpUsage exercises the omp_usage IPC handler with a mock omp
// binary on PATH. It verifies:
//   - the merged JSON shape {usage, grievances}
//   - graceful degradation when omp exits non-zero (errors array)
//   - graceful degradation when omp is not found
//
// The handler shells out to `omp` via exec.Command, so the test installs a
// shell script as `omp` in a temp PATH dir — the standard pattern for
// exec-based handler tests in this package (cf. ODO_OMP_WRAPPER).

// installMockOmp writes a shell script named "omp" into a temp dir and
// prepends that dir to PATH. The script handles "usage" and "grievances"
// subcommands, emitting the given JSON for each. A non-empty failUsage or
// failGrievances string makes the corresponding subcommand exit 1 with that
// message on stderr.
func installMockOmp(t *testing.T, usageJSON, grievancesJSON, failUsage, failGrievances string) {
	t.Helper()
	dir := t.TempDir()

	script := `#!/bin/sh
case "$1" in
  usage)
    if [ -n "` + failUsage + `" ]; then
      echo "` + failUsage + `" >&2
      exit 1
    fi
    cat <<'USAGE_EOF'
` + usageJSON + `
USAGE_EOF
    ;;
  grievances)
    if [ -n "` + failGrievances + `" ]; then
      echo "` + failGrievances + `" >&2
      exit 1
    fi
    cat <<'GRIEF_EOF'
` + grievancesJSON + `
GRIEF_EOF
    ;;
  *)
    echo "unknown subcommand: $1" >&2
    exit 1
    ;;
esac
`
	ompPath := filepath.Join(dir, "omp")
	if err := os.WriteFile(ompPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Prepend the temp dir to PATH so exec.Command("omp", ...) finds it.
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)
}

func TestHandleOmpUsageSuccess(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	usageJSON := `{"generatedAt":1786937084408,"reports":[{"provider":"test-provider","fetchedAt":1786937080436,"limits":[{"id":"test:primary","label":"7 days","window":{"id":"7d","label":"7 days","durationMs":604800000,"resetsAt":1787541881000},"amount":{"used":42,"limit":100,"remaining":58,"usedFraction":0.42,"remainingFraction":0.58,"unit":"percent"},"status":"ok"}]}]}`
	grievancesJSON := `[]`

	installMockOmp(t, usageJSON, grievancesJSON, "", "")

	rig := startRig(t, root)
	defer rig.stop(t)

	resp := rig.call(t, Request{Cmd: CmdOmpUsage, ProjectRoot: root})
	if resp.OmpUsage == nil {
		t.Fatal("omp_usage: response OmpUsage is nil")
	}

	var merged map[string]interface{}
	if err := json.Unmarshal(resp.OmpUsage, &merged); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}

	// Usage must be present and non-nil.
	usage, ok := merged["usage"]
	if !ok || usage == nil {
		t.Fatal("usage field missing or nil")
	}
	usageMap, ok := usage.(map[string]interface{})
	if !ok {
		t.Fatalf("usage is not an object: %T", usage)
	}
	reports, ok := usageMap["reports"].([]interface{})
	if !ok || len(reports) != 1 {
		t.Fatalf("expected 1 report, got %v", usageMap["reports"])
	}

	// Grievances must be present (empty array is fine).
	grievances, ok := merged["grievances"]
	if !ok {
		t.Fatal("grievances field missing")
	}
	grievancesArr, ok := grievances.([]interface{})
	if !ok {
		t.Fatalf("grievances is not an array: %T", grievances)
	}
	if len(grievancesArr) != 0 {
		t.Errorf("expected 0 grievances, got %d", len(grievancesArr))
	}

	// No errors expected.
	if errs, hasErr := merged["errors"]; hasErr && errs != nil {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestHandleOmpUsageGrievancesWithIssues(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	usageJSON := `{"reports":[]}`
	grievancesJSON := `[{"id":"g1","title":"slow response"},{"id":"g2","title":"rate limit"}]`

	installMockOmp(t, usageJSON, grievancesJSON, "", "")

	rig := startRig(t, root)
	defer rig.stop(t)

	resp := rig.call(t, Request{Cmd: CmdOmpUsage, ProjectRoot: root})
	if resp.OmpUsage == nil {
		t.Fatal("omp_usage: response OmpUsage is nil")
	}

	var merged map[string]interface{}
	if err := json.Unmarshal(resp.OmpUsage, &merged); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}

	grievances, ok := merged["grievances"].([]interface{})
	if !ok {
		t.Fatalf("grievances is not an array: %T", merged["grievances"])
	}
	if len(grievances) != 2 {
		t.Errorf("expected 2 grievances, got %d", len(grievances))
	}
}

func TestHandleOmpUsageGracefulDegradation(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	// Both subcommands fail — the handler should still return ok:true
	// with null usage/grievances and an errors array.
	installMockOmp(t, "", "", "omp usage failed", "omp grievances failed")

	rig := startRig(t, root)
	defer rig.stop(t)

	resp := rig.call(t, Request{Cmd: CmdOmpUsage, ProjectRoot: root})
	if resp.OmpUsage == nil {
		t.Fatal("omp_usage: response OmpUsage is nil")
	}

	var merged map[string]interface{}
	if err := json.Unmarshal(resp.OmpUsage, &merged); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}

	// Both sections should be absent (failed, omitempty).
	if _, ok := merged["usage"]; ok {
		t.Errorf("expected usage absent (omitempty), got %v", merged["usage"])
	}
	if _, ok := merged["grievances"]; ok {
		t.Errorf("expected grievances absent (omitempty), got %v", merged["grievances"])
	}

	// Errors array should have 2 entries.
	errs, ok := merged["errors"].([]interface{})
	if !ok {
		t.Fatalf("errors is not an array: %T", merged["errors"])
	}
	if len(errs) != 2 {
		t.Errorf("expected 2 errors, got %d: %v", len(errs), errs)
	}
}

func TestHandleOmpUsageOmpNotFound(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	// Clear PATH so `omp` is not found — exec.Command returns an error.
	// We use a path that does not contain any omp binary.
	dir := t.TempDir()
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

	// Also check that exec.LookPath fails as expected.
	if _, err := exec.LookPath("omp"); err == nil {
		t.Skip("omp found in inherited PATH — cannot test not-found path reliably")
	}

	rig := startRig(t, root)
	defer rig.stop(t)

	resp := rig.call(t, Request{Cmd: CmdOmpUsage, ProjectRoot: root})
	if resp.OmpUsage == nil {
		t.Fatal("omp_usage: response OmpUsage is nil")
	}

	var merged map[string]interface{}
	if err := json.Unmarshal(resp.OmpUsage, &merged); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}

	// Both should be absent (omitempty), errors should be present.
	if _, ok := merged["usage"]; ok {
		t.Errorf("expected usage absent, got %v", merged["usage"])
	}
	if _, ok := merged["grievances"]; ok {
		t.Errorf("expected nil grievances, got %v", merged["grievances"])
	}
	errs, ok := merged["errors"].([]interface{})
	if !ok || len(errs) != 2 {
		t.Errorf("expected 2 errors (usage + grievances), got %v", merged["errors"])
	}
}

func TestHandleOmpUsageInvalidJSON(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	// Emit invalid JSON for usage, valid for grievances.
	usageJSON := `{not valid json`
	grievancesJSON := `[]`

	installMockOmp(t, usageJSON, grievancesJSON, "", "")

	rig := startRig(t, root)
	defer rig.stop(t)

	resp := rig.call(t, Request{Cmd: CmdOmpUsage, ProjectRoot: root})
	if resp.OmpUsage == nil {
		t.Fatal("omp_usage: response OmpUsage is nil")
	}

	var merged map[string]interface{}
	if err := json.Unmarshal(resp.OmpUsage, &merged); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}

	// Usage should be absent (invalid JSON → omitted), grievances should still work.
	if _, ok := merged["usage"]; ok {
		t.Errorf("expected usage absent (invalid JSON), got %v", merged["usage"])
	}
	grievances, ok := merged["grievances"].([]interface{})
	if !ok {
		t.Fatalf("grievances should still be a valid array: %T", merged["grievances"])
	}
	if len(grievances) != 0 {
		t.Errorf("expected 0 grievances, got %d", len(grievances))
	}

	// Should have an error about invalid usage JSON.
	errs, ok := merged["errors"].([]interface{})
	if !ok || len(errs) == 0 {
		t.Errorf("expected at least 1 error for invalid usage JSON, got %v", merged["errors"])
	}
}

func TestHandleOmpUsageContextCancel(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))

	// Install a mock omp that sleeps longer than the test's patience.
	// The handler's runOmpJSON uses exec.CommandContext with a 10s
	// timeout — when the context is cancelled, the subprocess is
	// killed and runOmpJSON returns an error, which the handler
	// degrades into a null field + errors entry.
	dir := t.TempDir()
	script := `#!/bin/sh
sleep 30
echo "should not reach here"
`
	ompPath := filepath.Join(dir, "omp")
	if err := os.WriteFile(ompPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

	rig := startRig(t, root)
	defer rig.stop(t)

	// Call the handler directly with a context that we cancel AFTER
	// resolveProject completes. resolveProject is the first thing the
	// handler does; if we cancel before it runs, it fails with a store
	// error (correct — the context is dead). Instead, give the handler
	// a fresh context and cancel it after a short delay so the omp
	// subprocesses get killed mid-flight.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Give resolveProject time to complete (~50ms), then cancel
		// during the omp subprocess execution.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	resp, err := rig.server.handleOmpUsage(ctx, Request{ProjectRoot: root})
	// The handler may return either:
	//  - an error (if resolveProject raced with the cancel), or
	//  - a response with nil fields + errors (if omp subprocesses were
	//    killed by the context cancel).
	// Both are acceptable — the point is the handler doesn't hang.
	if err != nil {
		// resolveProject lost the race — acceptable.
		return
	}
	if resp.OmpUsage == nil {
		t.Fatal("response OmpUsage is nil (expected degradation blob)")
	}
	var merged map[string]interface{}
	if err := json.Unmarshal(resp.OmpUsage, &merged); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	// With context cancelled during omp exec, usage should be absent (omitempty).
	if _, ok := merged["usage"]; ok {
		t.Errorf("expected usage absent (context cancelled), got %v", merged["usage"])
	}
}
