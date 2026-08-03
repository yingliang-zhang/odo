package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// M6 (§9) test 12: `odo wiki read <page>` runs against plain files in the
// cwd — no daemon. Covers the happy path (exit 0, exact content on stdout)
// and the path guard (traversal exits 1 naming the wiki/-only rule).

// captureCLI runs fn with os.Stdout/os.Stderr redirected into pipes and
// returns what was written plus fn's exit code.
func captureCLI(t *testing.T, fn func() int) (stdout, stderr string, code int) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	code = fn()

	if err := wOut.Close(); err != nil {
		t.Fatal(err)
	}
	if err := wErr.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = oldOut, oldErr
	var sb, eb strings.Builder
	if _, err := io.Copy(&sb, rOut); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&eb, rErr); err != nil {
		t.Fatal(err)
	}
	return sb.String(), eb.String(), code
}

func TestCLIRunsFromWorktree(t *testing.T) {
	root := t.TempDir()
	content := "# main-epoch-1\n\nAuthentication uses JWT with refresh tokens.\n"
	if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wiki", "main-epoch-1.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".odo"), 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := "## epoch 1 — 2026-08-15T14:32:01Z\n- distill duration: 187s (review_action/distill seq 42)\n"
	if err := os.WriteFile(filepath.Join(root, ".odo", "ledger.md"), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}
	// Agents cd into the project first; t.Chdir scopes the cwd change.
	t.Chdir(root)

	// Happy path: the note's content on stdout, exit 0.
	stdout, _, code := captureCLI(t, func() int {
		return runWikiCLI([]string{"read", "main-epoch-1"})
	})
	if code != 0 {
		t.Errorf("wiki read main-epoch-1: exit = %d, want 0", code)
	}
	if stdout != content {
		t.Errorf("stdout = %q, want the file content %q", stdout, content)
	}

	// The ledger friendly name resolves to .odo/ledger.md.
	stdout, _, code = captureCLI(t, func() int {
		return runWikiCLI([]string{"read", "ledger"})
	})
	if code != 0 || stdout != ledger {
		t.Errorf("wiki read ledger: exit %d, stdout %q; want 0 + ledger content", code, stdout)
	}

	// Traversal is rejected by the guard (the error names the rule).
	_, stderr, code := captureCLI(t, func() int {
		return runWikiCLI([]string{"read", "../../etc/passwd"})
	})
	if code != 1 {
		t.Errorf("traversal: exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "only files under wiki/") {
		t.Errorf("traversal stderr = %q, want it to name the wiki/-only guard", stderr)
	}

	// A missing page is an error, not empty stdout.
	_, _, code = captureCLI(t, func() int {
		return runWikiCLI([]string{"read", "does-not-exist"})
	})
	if code != 1 {
		t.Errorf("missing page: exit = %d, want 1", code)
	}

	// odo ledger tail N prints only the last N sections.
	_, _, code = captureCLI(t, func() int {
		return runLedgerCLI([]string{"tail", "1"})
	})
	if code != 0 {
		t.Errorf("ledger tail 1: exit = %d, want 0", code)
	}
}
