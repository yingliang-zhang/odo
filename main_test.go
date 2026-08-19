package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SIGQUIT immunity: the daemon must survive a stray kill -3 (the runtime's
// default action would dump goroutines and exit mid-consult — 2026-08-19
// it took a live /panel down unnoticed). The test drives the production
// installer and asserts the signal ARRIVES on the immune channel, i.e. the
// default fatal action was displaced; the process reaching the assertion
// at all proves nobody acted on the default.
func TestSIGQUITImmunity(t *testing.T) {
	delivered := installSIGQUITImmunity()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGQUIT); err != nil {
		t.Fatalf("kill self: %v", err)
	}
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGQUIT never reached the immunity handler — default action still armed")
	}
}

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

// TestInstanceLockGuard pins the single-instance contract (epoch-8
// outstanding #4): one daemon per project state dir — a second acquire on
// a held lock fails loudly naming the lock, an error other than contention
// is distinguishable, and closing the holder releases for the next daemon
// (crash-stale locks are impossible via flock semantics).
func TestInstanceLockGuard(t *testing.T) {
	dir := t.TempDir()

	first, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "odo.lock"))
	if err != nil {
		t.Fatalf("lock file exists: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("lock mode = %o, want 0600", info.Mode().Perm())
	}

	// Contention: a live holder blocks the second daemon, naming the lock,
	// and unwraps to the sentinel main routes to exit code 3 (Tauri
	// attach-to-live).
	second, err := acquireInstanceLock(dir)
	if err == nil {
		_ = second.Close()
		t.Fatal("second acquire succeeded while the first holder is live")
	}
	if !errors.Is(err, errDaemonAlreadyRunning) {
		t.Errorf("contention error = %v, want errors.Is errDaemonAlreadyRunning", err)
	}
	if !strings.Contains(err.Error(), "another odo daemon serves this project") {
		t.Errorf("contention error = %q, want the refused-start message", err)
	}

	// Release: the holder's exit (file close) frees the lock for the next
	// daemon — the stale-socket path then belongs to it alone.
	if err := first.Close(); err != nil {
		t.Fatalf("close holder: %v", err)
	}
	third, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("acquire after holder exit: %v", err)
	}
	_ = third.Close()

	// Exclusivity (round-2 panel): IO/open failures must NOT unwrap to the
	// sentinel — Tauri maps exit 3 to attach-to-live, so a permissions/IO
	// fault must never masquerade as a live peer. A stateDir that is a
	// FILE makes OpenFile fail.
	notDir := filepath.Join(t.TempDir(), "statedir")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireInstanceLock(notDir); err == nil {
		t.Fatal("lock acquired on a non-directory stateDir")
	} else if errors.Is(err, errDaemonAlreadyRunning) {
		t.Errorf("IO failure mapped to errDaemonAlreadyRunning — exit-3 exclusivity broken: %v", err)
	}
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
