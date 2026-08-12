package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// M17 F3: `odo unretract <note-basename>` — the false-positive retraction
// repair emitter, tested against the store fixture (never the live journal).

// seedUnretractFixture writes one project/workstream/conversation plus the
// note file, journals a contradiction-pass-style retract row for it, and
// closes the journal so the CLI exercises its own store open. Returns the
// project root.
func seedUnretractFixture(t *testing.T, noteName string) (root string) {
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
		// The contradiction pass's own row shape (detail: "<name> …").
		if _, err := st.AppendEvent(ctx, c.ID, store.EventMemoryUpdate,
			`{"layer":"note","cause":"retract","detail":"`+noteName+` contradicted by main-epoch-3: …"}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

// retractionSetAfter reopens the journal and derives the retraction set
// with the SAME derivation the recall path gates on.
func retractionSetAfter(t *testing.T, root string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenReadOnly(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p, err := st.GetProjectByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.GetWorkstreamByName(ctx, p.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.GetActiveConversation(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := st.ListEvents(ctx, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return ipc.RetractionSetFromEvents(events)
}

// TestUnretractCLIRoundtrip: the emitter journals
// memory_update{layer:"note", cause:"unretract"} on the note's workstream
// conversation, and the recall-path derivation (RetractionSetFromEvents)
// immediately sees the note back in the live set — emitter/consumer
// roundtrip. A second run is the idempotent no-op.
func TestUnretractCLIRoundtrip(t *testing.T) {
	root := seedUnretractFixture(t, "main-epoch-2")
	if set := retractionSetAfter(t, root); !set["main-epoch-2"] {
		t.Fatal("fixture broken: the seeded retract did not land in the retraction set")
	}
	t.Chdir(root)

	stdout, _, code := captureCLI(t, func() int { return runUnretractCLI([]string{"main-epoch-2"}) })
	if code != 0 {
		t.Fatalf("unretract: exit %d, stdout %q", code, stdout)
	}
	if !strings.Contains(stdout, "main-epoch-2 unretracted") {
		t.Errorf("stdout = %q, want the unretract confirmation", stdout)
	}
	if set := retractionSetAfter(t, root); set["main-epoch-2"] {
		t.Errorf("note still retracted after the unretract journal row (roundtrip broken)")
	}

	// Idempotent: already unretracted — no new row, explicit no-op message.
	stdout, _, code = captureCLI(t, func() int { return runUnretractCLI([]string{"main-epoch-2"}) })
	if code != 0 {
		t.Fatalf("second unretract: exit %d, want 0 (idempotent)", code)
	}
	if !strings.Contains(stdout, "already stands unretracted") {
		t.Errorf("second-run stdout = %q, want the idempotence notice", stdout)
	}
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
	unretracts := 0
	for _, ev := range events {
		if ev.Type == store.EventMemoryUpdate && strings.Contains(string(ev.Payload), `"cause":"unretract"`) {
			unretracts++
		}
	}
	if unretracts != 1 {
		t.Errorf("unretract rows = %d, want exactly 1 (idempotent)", unretracts)
	}
}

// TestUnretractCLIValidation: the note file must exist (unretracting a
// vanished or hallucinated name would fabricate a retraction history), and
// the argument must be a <workstream>-epoch-<N> basename.
func TestUnretractCLIValidation(t *testing.T) {
	root := seedUnretractFixture(t, "main-epoch-2")
	t.Chdir(root)

	_, stderr, code := captureCLI(t, func() int { return runUnretractCLI([]string{"main-epoch-9"}) })
	if code != 1 {
		t.Errorf("missing note file: exit %d, want 1", code)
	}
	if !strings.Contains(stderr, "wiki/main-epoch-9.md") {
		t.Errorf("missing-note stderr = %q, want the file error", stderr)
	}

	_, stderr, code = captureCLI(t, func() int { return runUnretractCLI([]string{"garbage"}) })
	if code != 2 {
		t.Errorf("malformed name: exit %d, want 2 (usage)", code)
	}
	if !strings.Contains(stderr, "want <workstream>-epoch-<N>") {
		t.Errorf("malformed-name stderr = %q, want the name rule", stderr)
	}

	// Traversal is impossible by construction (the name regex refuses
	// separators before any path is built).
	_, _, code = captureCLI(t, func() int { return runUnretractCLI([]string{"../..-epoch-3"}) })
	if code != 2 {
		t.Errorf("traversal-shaped name: exit %d, want 2", code)
	}

	// Usage arity.
	if _, _, code := captureCLI(t, func() int { return runUnretractCLI(nil) }); code != 2 {
		t.Errorf("no args: exit %d, want 2", code)
	}
}
