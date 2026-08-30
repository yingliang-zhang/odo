package ipc

// D9-W3a verify_ms pins: the verify gate's wall time rides EVERY journal
// row it feeds — auto_land_blocked on gate failure, the moa_review
// evidence row on a panel accept. Additive key only (ADR-0002): no
// consumer branches on it, and rows from before W3 simply lack it.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestAutoLandVerifyFailedJournalesVerifyMs: a failing verify gate blocks
// with the gate's duration on the row (previously unjournaled — the D9
// legs surfaced the gap).
func TestAutoLandVerifyFailedJournalesVerifyMs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	// Failing gate: the pipeline never reaches the panel. One line — the
	// .odo-verify parser keeps only the FIRST fallback command (a second
	// plain line would be ignored, turning this into a zero-evidence
	// block instead of a verify failure).
	if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo NOPE && exit 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "v.diff", patchSrc("src/a.go", 1, 1, false))

	s.autoLand(context.Background(), d, root, "goal", false, "")

	sc := scanSettle(t, f.st, f.c.ID)
	blocked := sc.blocked
	if len(blocked) != 1 || blocked[0]["reason"] != "verify_failed" {
		t.Fatalf("blocked rows = %v, want one verify_failed", blocked)
	}
	ms, ok := blocked[0]["verify_ms"].(float64)
	if !ok {
		t.Fatalf("verify_ms missing on the verify_failed blocked row: %v", blocked[0])
	}
	if ms < 0 {
		t.Fatalf("verify_ms = %v — a duration is never negative", ms)
	}
	if len(sc.moaRows) != 0 {
		t.Fatalf("panel ran past a failed verify: %v", sc.moaRows)
	}
}

// TestAutoLandAcceptMoaReviewJournalesVerifyMs: the landed moa_review
// attests the exact verify that passed — command, tail, log, and its
// wall time.
func TestAutoLandAcceptMoaReviewJournalesVerifyMs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	root, _ := visualAutolandRepo(t) // .odo-verify = `echo PASS`
	s := &Server{store: f.st, projectRoot: root}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "gui", "src", "app.ts"), []byte("export const x = 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))

	s.autoLand(context.Background(), d, root, "goal", false, "")

	sc := scanSettle(t, f.st, f.c.ID)
	if len(sc.moaRows) != 1 || sc.moaRows[0]["consensus_verdict"] != "accept" {
		t.Fatalf("moa_review rows = %v, want one unanimous-accept evidence row", sc.moaRows)
	}
	if _, ok := sc.moaRows[0]["verify_ms"].(float64); !ok {
		t.Fatalf("verify_ms missing on the landed moa_review: %v", sc.moaRows[0])
	}
	// Byte-shape pinning of the pre-W3 keys is unchanged: the duration is
	// purely additive beside them.
	if sc.moaRows[0]["verify_cmd"] != "echo PASS" {
		t.Fatalf("verify_cmd = %v — the W3a key must not disturb existing attestation", sc.moaRows[0]["verify_cmd"])
	}
}
