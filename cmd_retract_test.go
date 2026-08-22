package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// 2026-08-22: `odo retract <note-basename> [reason…]` — the human
// resolution emitter for contradiction candidates, tested against the
// store fixture (never the live journal). Mirrors cmd_unretract_test.go.

// seedRetractFixture writes one project/workstream/conversation plus the
// note file and optionally journals a contradiction-pass-style CANDIDATE
// row (the advisory the retract resolves). Closes the journal so the CLI
// exercises its own store open. Returns the project root.
func seedRetractFixture(t *testing.T, noteName string) (root string) {
	t.Helper()
	root = t.TempDir()
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
	c, err := st.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if noteName != "" {
		if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "wiki", noteName+".md"), []byte("# note\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// The advisory row the human is resolving (2026-08-22 shape).
		if _, err := st.AppendEvent(ctx, c.ID, store.EventMemoryUpdate,
			`{"layer":"note","cause":"contradiction_candidate","detail":"`+noteName+` contradicted by main-epoch-3: …","before_sha":"aa","after_sha":"aa"}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestRetractCLIRoundtrip: the emitter journals
// memory_update{layer:"note", cause:"retract"} on the note's workstream
// conversation — with the sha pair bound to the exact note bytes and the
// optional reason on the detail — and the recall-path derivation
// (RetractionSetFromEvents) immediately drops the note. A second run is
// the idempotent no-op. `odo unretract` then repairs it (the two CLIs
// share the derivation).
func TestRetractCLIRoundtrip(t *testing.T) {
	root := seedRetractFixture(t, "main-epoch-2")
	if set := retractionSetAfter(t, root); set["main-epoch-2"] {
		t.Fatal("fixture broken: a candidate row must NOT enter the retraction set")
	}
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int {
		return runRetractCLI([]string{"main-epoch-2", "stale", "after", "rework"})
	})
	if code != 0 {
		t.Fatalf("retract: exit %d, stdout %q", code, stdout)
	}
	if !strings.Contains(stdout, "main-epoch-2 retracted") {
		t.Errorf("stdout = %q, want the retract confirmation", stdout)
	}
	if set := retractionSetAfter(t, root); !set["main-epoch-2"] {
		t.Errorf("note not in the retraction set after the retract row (roundtrip broken)")
	}

	// The journaled row: cause retract, reason on the detail, sha pair
	// bound to the exact file bytes (the file is never mutated).
	ctx := context.Background()
	st, err := store.OpenReadOnly(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, _ := st.GetProjectByRoot(ctx, root)
	w, _ := st.GetWorkstreamByName(ctx, p.ID, "main")
	c, _ := st.GetActiveConversation(ctx, w.ID)
	events, err := st.ListEvents(ctx, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var row string
	for _, ev := range events {
		if ev.Type == store.EventMemoryUpdate && strings.Contains(string(ev.Payload), `"cause":"retract"`) {
			if row != "" {
				t.Fatalf("multiple retract rows after one call: %q then %q", row, ev.Payload)
			}
			row = string(ev.Payload)
		}
	}
	if row == "" {
		t.Fatal("no retract row journaled")
	}
	if !strings.Contains(row, `"detail":"main-epoch-2 retracted by user: stale after rework"`) {
		t.Errorf("row detail missing the reason: %s", row)
	}
	wantSha := sha16Note([]byte("# note\n"))
	if !strings.Contains(row, `"before_sha":"`+wantSha+`"`) || !strings.Contains(row, `"after_sha":"`+wantSha+`"`) {
		t.Errorf("row sha pair not bound to the note bytes (want %s): %s", wantSha, row)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "wiki", "main-epoch-2.md")); string(got) != "# note\n" {
		t.Errorf("note file mutated (got %q) — retraction is a journal record only", got)
	}

	// Idempotent: already retracted — no new row, explicit no-op message.
	stdout, _, code = captureCLI(t, func() int { return runRetractCLI([]string{"main-epoch-2"}) })
	if code != 0 {
		t.Fatalf("second retract: exit %d, want 0 (idempotent)", code)
	}
	if !strings.Contains(stdout, "already stands retracted") {
		t.Errorf("second-run stdout = %q, want the idempotence notice", stdout)
	}

	// The unretract repair shares the derivation and undoes the row.
	stdout, _, code = captureCLI(t, func() int { return runUnretractCLI([]string{"main-epoch-2"}) })
	if code != 0 || !strings.Contains(stdout, "unretracted") {
		t.Fatalf("unretract repair: exit %d stdout %q", code, stdout)
	}
	if set := retractionSetAfter(t, root); set["main-epoch-2"] {
		t.Errorf("note still retracted after the unretract repair")
	}
}

// TestRetractCLIValidation: the note file must exist (retracting a
// vanished or hallucinated name would fabricate a retraction history),
// and the argument must be a <workstream>-epoch-<N> basename.
func TestRetractCLIValidation(t *testing.T) {
	root := seedRetractFixture(t, "main-epoch-2")
	t.Chdir(root)

	_, stderr, code := captureCLI(t, func() int { return runRetractCLI([]string{"main-epoch-9"}) })
	if code != 1 {
		t.Errorf("missing note file: exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "wiki/main-epoch-9.md") {
		t.Errorf("missing-note stderr = %q, want the file error", stderr)
	}

	_, stderr, code = captureCLI(t, func() int { return runRetractCLI([]string{"garbage"}) })
	if code != 2 {
		t.Errorf("malformed name: exit %d, want 2 (usage)", code)
	}
	if !strings.Contains(stderr, "want <workstream>-epoch-<N>") {
		t.Errorf("malformed-name stderr = %q, want the name rule", stderr)
	}

	_, _, code = captureCLI(t, func() int { return runRetractCLI([]string{"../..-epoch-3"}) })
	if code != 2 {
		t.Errorf("traversal-shaped name: exit %d, want 2", code)
	}

	if _, _, code := captureCLI(t, func() int { return runRetractCLI(nil) }); code != 2 {
		t.Errorf("no args: exit %d, want 2", code)
	}
}
