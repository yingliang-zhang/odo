package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// 2026-08-28 (D4, design-lock W2 ruling ④): `odo memory revert <epoch>` —
// the HUMAN rollback for one epoch's memory apply (memory.md + archive;
// this wave's scope). The engine (ipc.RevertMemoryEpoch) locates the
// epoch's memory_apply receipt, verifies the layers still hold the
// receipt's post-state, rebuilds the pre-image from the lane's receipt
// chain, writes it back, and journals the rollback receipt
// memory_update{layer:"apply", cause:"revert", epoch, actor:"human"} on
// the receipt's lane. The replay fold retires the epoch's receipts on
// that row, so the next boot does not "repair" the reverted bytes back
// to the post-state.
//
// Fail-closed on every ambiguity (ambiguous epoch across lanes, moved
// files, unreconstructable pre-image, user.md/skill layers, second
// revert): the command refuses and names the reason — nothing is
// written. Revert-of-revert is a re-apply and belongs to the normal
// path (apply_memory).
//
// The journal opens READ-WRITE through store.Open — WAL + 5s
// busy_timeout coexists with a live daemon (unretract precedent).
//
//	odo memory revert <epoch>

const memoryUsage = `usage: odo memory revert <epoch>
  revert <epoch>  restore the pre-image of one epoch's memory apply
                 (memory.md + memory-archive.md). Human-only: refuses
                 ambiguous epochs, moved files, user/skill layers, and a
                 second revert of the same epoch. Fail-closed: files
                 never change without the verify; then journals
                 memory_update{layer:"apply", cause:"revert",
                 actor:"human"}.`

// runMemoryCLI dispatches `odo memory <sub>`.
func runMemoryCLI(args []string) int {
	if len(args) != 2 || args[0] != "revert" {
		fmt.Fprintln(os.Stderr, memoryUsage)
		return 2
	}
	epoch, err := strconv.Atoi(args[1])
	if err != nil || epoch <= 0 {
		fmt.Fprintf(os.Stderr, "odo memory revert: want a positive epoch number, got %q\n", args[1])
		return 2
	}

	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo memory revert: resolve cwd: %v\n", err)
		return 1
	}
	root, err := journalRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo memory revert: %v\n", err)
		return 1
	}
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo memory revert: %v\n", err)
		return 1
	}
	defer st.Close()
	p, err := st.GetProjectByRoot(ctx, root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo memory revert: project %s not in journal: %v\n", root, err)
		return 1
	}

	report, err := ipc.RevertMemoryEpoch(ctx, st, p, epoch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo memory revert: %v\n", err)
		return 1
	}
	note := ""
	if report.AlreadyThere {
		note = " (files already held the pre-image — journaling completed the close-out)"
	}
	fmt.Printf("epoch %d reverted%s: %s restored from apply seq %d (conversation %d); revert row journaled at seq %d\n",
		report.Epoch, note, strings.Join(report.Layers, ", "), report.ApplySeq, report.Conversation, report.RowSeq)
	return 0
}
