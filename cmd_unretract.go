package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// M17 F3: `odo unretract <note-basename>` — the false-positive retraction
// repair emitter. Only curated/human paths journal
// memory_update{layer:"note", cause:"retract"} (the contradiction pass has
// been advisory-only since 2026-08-22 — it journals candidate rows, never
// retracts), and the recall path has always CONSUMED cause:"unretract"
// (retractedNoteSet deletes the name). This CLI is the manual emitter for
// a tri-model-audited false positive (e.g. the F2 production case, seqs
// 5144–5149, retracted on the pre-M17 barn door):
//
//	memory_update{layer:"note", cause:"unretract",
//	               detail:"<name> unretracted by user (repairs false-positive retraction)"}
//
// The note file itself is never touched (ADR-0003 inv 2: epoch notes are
// append-only records) — the unretract is a journal record like the
// retract it undoes. Idempotent: a note that already stands unretracted is
// a no-op (the retraction set is derived via ipc.RetractionSetFromEvents,
// the same derivation recall gates on).
//
// The journal is opened READ-WRITE through store.Open — the daemon's own
// open (WAL + 5s busy_timeout) — unlike the read-only audit CLIs: this is
// a write command, and store.Open coexists with a live daemon (the
// append serializes on the single-writer WAL lock like any daemon write).
//
//	odo unretract <note-basename>   (e.g. main-epoch-3; .md optional)

const unretractUsage = `usage: odo unretract <note-basename>
  <note-basename>  an epoch note under wiki/ (e.g. main-epoch-3; .md optional)
Journals memory_update{layer:"note", cause:"unretract"} on the note's
workstream conversation. Idempotent: a note that stands unretracted is a
no-op. Reads the journal read-write; a live daemon is undisturbed.`

// unretractNameRe validates the basename: <workstream>-epoch-<N> with the
// workstream class of every other note consumer (no separators, no
// traversal — the path is always exactly <root>/wiki/<name>.md).
var unretractNameRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)-epoch-(\d+)$`)

// runUnretractCLI implements `odo unretract <note-basename>`.
func runUnretractCLI(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, unretractUsage)
		return 2
	}
	name := strings.TrimSuffix(args[0], ".md")
	m := unretractNameRe.FindStringSubmatch(name)
	if m == nil {
		fmt.Fprintf(os.Stderr, "odo unretract: want <workstream>-epoch-<N> (e.g. main-epoch-3), got %q\n", args[0])
		return 2
	}
	workstream := m[1]

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo unretract: resolve cwd: %v\n", err)
		return 1
	}
	root, err := journalRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo unretract: %v\n", err)
		return 1
	}
	// The note file must exist: unretracting a vanished or hallucinated
	// name would fabricate a retraction history the journal never had.
	if _, err := os.Stat(filepath.Join(root, "wiki", name+".md")); err != nil {
		fmt.Fprintf(os.Stderr, "odo unretract: wiki/%s.md: %v\n", name, err)
		return 1
	}

	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo unretract: %v\n", err)
		return 1
	}
	defer st.Close()
	p, err := st.GetProjectByRoot(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo unretract: project %s not in journal: %v\n", root, err)
		return 1
	}
	w, err := st.GetWorkstreamByName(ctx, p.ID, workstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo unretract: workstream %q not in journal: %v\n", workstream, err)
		return 1
	}
	c, err := st.GetActiveConversation(ctx, w.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo unretract: no active conversation for workstream %q: %v\n", workstream, err)
		return 1
	}

	events, err := st.ListEvents(ctx, c.ID, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo unretract: %v\n", err)
		return 1
	}
	if !ipc.RetractionSetFromEvents(events)[name] {
		fmt.Printf("%s already stands unretracted — nothing to do\n", name)
		return 0
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"layer":  "note",
		"cause":  "unretract",
		"detail": fmt.Sprintf("%s unretracted by user (repairs false-positive retraction)", name),
	})
	ev, err := st.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, string(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo unretract: journal: %v\n", err)
		return 1
	}
	fmt.Printf("%s unretracted (memory_update seq %d on conversation %d) — recall, curation, and age accounting see it again\n",
		name, ev.Seq, c.ID)
	return 0
}
