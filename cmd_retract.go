package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// 2026-08-22 (panel P0-3 follow-through): `odo retract <note-basename>
// [reason…]` — the HUMAN resolution emitter for contradiction candidates.
// Since 2026-08-22 the contradiction pass is advisory-only: it journals
// memory_update{layer:"note", cause:"contradiction_candidate"} and nothing
// automatic ever retracts. The candidate row is a flag a human resolves —
// this CLI is the resolution with a record (ADR-0004: a recorded conscious
// act):
//
//	memory_update{layer:"note", cause:"retract",
//	               detail:"<name> retracted by user[: <reason>]",
//	               before_sha == after_sha == sha16(note content)}
//
// Only cause:"retract" filters the recall injection set and the auto_age
// clock (recall.go, RetractionSetFromEvents) — the row takes the note out
// of every later prompt while the file itself is never touched (ADR-0003
// inv 2: epoch notes are append-only records; the sha pair records exactly
// the bytes judged, same as the pre-2026-08-22 contradiction pass). A
// false call is repairable with `odo unretract <note>`.
//
// Idempotent: a note already in the retraction set is a no-op (same
// derivation recall gates on, ipc.RetractionSetFromEvents). The journal is
// opened READ-WRITE through store.Open — the daemon's own open (WAL + 5s
// busy_timeout), so a live daemon is undisturbed (the append serializes on
// the single-writer WAL lock like any daemon write).
//
//	odo retract <note-basename> [reason…]   (e.g. main-epoch-3; .md optional)

const retractUsage = `usage: odo retract <note-basename> [reason…]
  <note-basename>  an epoch note under wiki/ (e.g. main-epoch-3; .md optional)
  [reason…]        optional free text, journaled on the retract row
Journals memory_update{layer:"note", cause:"retract"} on the note's
workstream conversation — the human resolution of a contradiction candidate.
Idempotent: a note that stands retracted is a no-op. Reads the journal
read-write; a live daemon is undisturbed. Undo with 'odo unretract'.`

// sha16Note reproduces the ipc package's sha16 (unexported there): hex of
// the first 8 bytes of sha256 — the same digest the pre-2026-08-22
// contradiction pass recorded on its retract rows.
func sha16Note(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// runRetractCLI implements `odo retract <note-basename> [reason…]`.
func runRetractCLI(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, retractUsage)
		return 2
	}
	name := strings.TrimSuffix(args[0], ".md")
	m := unretractNameRe.FindStringSubmatch(name)
	if m == nil {
		fmt.Fprintf(os.Stderr, "odo retract: want <workstream>-epoch-<N> (e.g. main-epoch-3), got %q\n", args[0])
		return 2
	}
	workstream := m[1]
	reason := strings.TrimSpace(strings.Join(args[1:], " "))

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo retract: resolve cwd: %v\n", err)
		return 1
	}
	root, err := journalRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo retract: %v\n", err)
		return 1
	}
	// The note file must exist: retracting a vanished or hallucinated name
	// would fabricate a retraction history the journal never had — and its
	// bytes are exactly what the sha pair records.
	content, err := os.ReadFile(filepath.Join(root, "wiki", name+".md"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo retract: wiki/%s.md: %v\n", name, err)
		return 1
	}

	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo retract: %v\n", err)
		return 1
	}
	defer st.Close()
	p, err := st.GetProjectByRoot(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo retract: project %s not in journal: %v\n", root, err)
		return 1
	}
	w, err := st.GetWorkstreamByName(ctx, p.ID, workstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo retract: workstream %q not in journal: %v\n", workstream, err)
		return 1
	}
	c, err := st.GetActiveConversation(ctx, w.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo retract: no active conversation for workstream %q: %v\n", workstream, err)
		return 1
	}

	events, err := st.ListEvents(ctx, c.ID, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo retract: %v\n", err)
		return 1
	}
	if ipc.RetractionSetFromEvents(events)[name] {
		fmt.Printf("%s already stands retracted — nothing to do\n", name)
		return 0
	}

	detail := name + " retracted by user"
	if reason != "" {
		detail += ": " + reason
	}
	sha := sha16Note(content)
	payload, _ := json.Marshal(map[string]interface{}{
		"layer":      "note",
		"cause":      "retract",
		"detail":     detail,
		"before_sha": sha,
		"after_sha":  sha,
	})
	ev, err := st.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, string(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo retract: journal: %v\n", err)
		return 1
	}
	fmt.Printf("%s retracted (memory_update seq %d on conversation %d) — recall, curation, and age accounting drop it; undo with 'odo unretract %s'\n",
		name, ev.Seq, c.ID, name)
	return 0
}
