package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// The autonomy CLI is a thin wrapper over ipc.ComputeAutonomy (covered by
// the ipc battery) — these tests pin the wrapper: no-data copy, --json
// shape, usage errors, and that the read-only journal path sees seeded
// resolutions end to end.

// seedAutonomyJournal plants one conversation with two resolved docs
// diffs (the autonomy audit classifies patch content, so the diffs carry
// real patch text).
func seedAutonomyJournal(t *testing.T, root string, withData bool) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		t.Fatal(err)
	}
	if withData {
		patch := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1,2 @@\n+docs line\n"
		diffPath := filepath.Join(t.TempDir(), "r.diff")
		if err := os.WriteFile(diffPath, []byte(patch), 0o644); err != nil {
			t.Fatal(err)
		}
		w, err := st.CreateOrGetWorkstream(ctx, p.ID, "main")
		if err != nil {
			t.Fatal(err)
		}
		c, err := st.CreateConversation(ctx, w.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			d, err := st.InsertDiff(ctx, c.ID, diffPath, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.AppendEvent(ctx, c.ID, store.EventReviewAction,
				fmt.Sprintf(`{"action":"accept","diff_id":%d}`, d.ID)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAutonomyAuditNoData(t *testing.T) {
	root := t.TempDir()
	seedAutonomyJournal(t, root, false)
	t.Setenv("HOME", t.TempDir()) // auto_apply reads the M20 default ("main"); never a real ~/.odo/prefs.md
	t.Chdir(root)

	stdout, stderr, code := captureCLI(t, func() int {
		return runAutonomyCLI([]string{"audit"})
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "no data") {
		t.Errorf("stdout %q, want the no-data line", stdout)
	}
	if !strings.Contains(stdout, "auto-apply: main") {
		t.Errorf("stdout %q, want the auto-apply pref line (M20 default-on)", stdout)
	}
}

func TestAutonomyAuditJSON(t *testing.T) {
	root := t.TempDir()
	seedAutonomyJournal(t, root, true)
	t.Chdir(root)

	stdout, stderr, code := captureCLI(t, func() int {
		return runAutonomyCLI([]string{"audit", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	var report ipc.AutonomyReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("--json: %v\n%s", err, stdout)
	}
	if report.Resolutions != 2 {
		t.Errorf("resolutions = %d, want 2", report.Resolutions)
	}
	for _, c := range report.Classes {
		if c.Class == "C1" {
			if c.Accepted != 2 || c.Streak != 2 {
				t.Errorf("C1 = %+v, want 2 accepted / streak 2", c)
			}
			return
		}
	}
	t.Errorf("C1 row missing: %+v", report.Classes)
}

// TestAutonomyAuditSettleHeader (B6): the header line carries the settle
// ladder's journal facts in one compact line — no per-row noise.
func TestAutonomyAuditSettleHeader(t *testing.T) {
	root := t.TempDir()
	seedAutonomyJournal(t, root, true)
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetActiveConversation(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	add := func(eventType string, payload string) {
		t.Helper()
		if _, err := st.AppendEvent(ctx, c.ID, eventType, payload); err != nil {
			t.Fatal(err)
		}
	}
	add(store.EventReviewAction, `{"action":"auto_revise_round","actor":"auto_panel","round":1,"diff_id":1,"origin_diff_id":1}`)
	add(store.EventReviewAction, `{"action":"auto_land_blocked","actor":"auto_panel","reason":"revise_no_progress","diff_id":1}`)
	add(store.EventReviewAction, `{"action":"auto_land_blocked","actor":"auto_panel","reason":"human_gate_visual","diff_id":2}`)
	add(store.EventReviewAction, `{"action":"auto_land_blocked","actor":"auto_panel","reason":"human_gate_visual","diff_id":3}`)
	add(store.EventReviewAction, `{"action":"auto_land_blocked","actor":"auto_panel","reason":"verify_no_evidence","diff_id":4}`)
	add(store.EventMemoryUpdate, `{"layer":"auto_land","cause":"ladder_suspended","detail":"cap"}`)
	add(store.EventMemoryUpdate, `{"layer":"auto_land","cause":"ladder_resumed","detail":"human accept"}`)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)
	stdout, stderr, code := captureCLI(t, func() int {
		return runAutonomyCLI([]string{"audit"})
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	want := "settle ladder: 1 revise round(s) · 1 suspension(s) · 1 resume(s) · 1 no-progress · 2 visual-gate block(s)"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout missing the settle header %q:\n%s", want, stdout)
	}

	// --json exposes the same surface for the GUI/pipeline consumers.
	stdout, stderr, code = captureCLI(t, func() int {
		return runAutonomyCLI([]string{"audit", "--json"})
	})
	if code != 0 {
		t.Fatalf("--json exit %d, stderr %q", code, stderr)
	}
	var report ipc.AutonomyReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("--json: %v\n%s", err, stdout)
	}
	if want := (ipc.SettleTallies{ReviseRounds: 1, Suspensions: 1, Resumes: 1, ReviseNoProgress: 1, VisualGateBlocks: 2}); report.Settle != want {
		t.Errorf("Settle = %+v, want %+v", report.Settle, want)
	}
}

func TestAutonomyAuditUsage(t *testing.T) {
	_, stderr, code := captureCLI(t, func() int {
		return runAutonomyCLI([]string{"bogus"})
	})
	if code != 2 || !strings.Contains(stderr, "usage: odo autonomy audit") {
		t.Errorf("exit %d stderr %q, want usage on exit 2", code, stderr)
	}
}
