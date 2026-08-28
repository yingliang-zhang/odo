package ipc

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// D4 (2026-08-28): `odo memory revert <epoch>` — the human rollback of one
// epoch's memory apply. The engine restores byte-exact pre-images from the
// lane's receipt chain, journals the revert receipt, refuses a second
// revert, and teaches the boot replay that the reverted bytes are NOT a
// mid-write crash (the fold's retirement).

func TestRevertEpoch(t *testing.T) {
	root := initRepo(t)
	// Hermetic user layer (the memory_replay_test.go convention): the
	// replay engine's user.md reads/writes resolve through $HOME, and the
	// epoch-5 user receipt below must never see (or mutate) the real one.
	home := t.TempDir()
	t.Setenv("HOME", home)
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	// Two chained epochs across BOTH in-scope layers:
	//   epoch 1: memory ""→A, archive ""→c1
	//   epoch 2: memory A→B, archive c1→c1c2
	A := "- rule one — cites: main-epoch-1; reaffirmed: 1\n"
	B := A + "- rule two — cites: main-epoch-2; reaffirmed: 2\n"
	c1 := "\n## 2026-08-01 — rotated from memory.md (overflow)\n- old rule — cites: e0; reaffirmed: 1\n"
	c2 := "\n## 2026-08-02 — rotated from memory.md (overflow)\n- older rule — cites: e1; reaffirmed: 2\n"
	seedApplyReceipt(t, rig, convID, 1, nil, nil, nil, applyRecovery{
		Memory:  &applyRecoveryLayer{BeforeSHA: sha16(nil), AfterSHA: sha16([]byte(A)), Body: A},
		Archive: &applyRecoveryLayer{BeforeSHA: sha16(nil), AfterSHA: sha16([]byte(c1)), Body: c1},
	})
	seedApplyReceipt(t, rig, convID, 2, nil, nil, nil, applyRecovery{
		Memory:  &applyRecoveryLayer{BeforeSHA: sha16([]byte(A)), AfterSHA: sha16([]byte(B)), Body: B},
		Archive: &applyRecoveryLayer{BeforeSHA: sha16([]byte(c1)), AfterSHA: sha16([]byte(c1 + c2)), Body: c2},
	})
	writeProjFile(t, root, ".odo/memory.md", B)
	writeProjFile(t, root, ".odo/memory-archive.md", c1+c2)

	p, err := rig.store.GetProjectByRoot(ctx, root)
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	// Fail-closed probes first — every refusal leaves the files alone.
	if _, err := RevertMemoryEpoch(ctx, rig.store, p, 9); err == nil || !strings.Contains(err.Error(), "no memory_apply receipt") {
		t.Errorf("revert epoch 9 = %v, want no-receipt refusal", err)
	}
	writeProjFile(t, root, ".odo/memory.md", B+"- hand edit\n")
	if _, err := RevertMemoryEpoch(ctx, rig.store, p, 2); err == nil || !strings.Contains(err.Error(), "moved since the apply") {
		t.Errorf("revert with hand-moved memory.md = %v, want the moved-file refusal", err)
	}
	writeProjFile(t, root, ".odo/memory.md", B) // restore the attested post-state
	seedApplyReceipt(t, rig, convID, 5, nil, nil, nil, applyRecovery{
		User: &applyRecoveryLayer{BeforeSHA: sha16(nil), AfterSHA: sha16([]byte("x\n")), Body: "x\n"},
	})
	// The receipt attests a landed apply: seed user.md at its
	// AFTER-state so the boot replay below reads it as landed everywhere.
	// Without this the receipt stays a live replay candidate whose
	// outcome rides the ambient $HOME — an empty/absent user.md (the
	// verify sandbox's state) hashes to before-sha, so the replay
	// "restores" x onto it and journals the recover row the final
	// assertion counts.
	writeProjFile(t, home, ".odo/user.md", "x\n")
	if _, err := RevertMemoryEpoch(ctx, rig.store, p, 5); err == nil || !strings.Contains(err.Error(), "user.md") {
		t.Errorf("revert of a user.md batch = %v, want the scope refusal", err)
	}

	// The revert: epoch 2's pre-image restores byte-exactly.
	report, err := RevertMemoryEpoch(ctx, rig.store, p, 2)
	if err != nil {
		t.Fatalf("RevertMemoryEpoch(2): %v", err)
	}
	if report.Epoch != 2 || report.Conversation != convID || len(report.Layers) != 2 ||
		report.Layers[0] != "archive" || report.Layers[1] != "memory" {
		t.Errorf("report = %+v, want epoch 2 on the lane with [archive, memory]", report)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != A {
		t.Errorf("memory.md after revert = %q, want pre-image %q", got, A)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory-archive.md")); got != c1 {
		t.Errorf("archive after revert = %q, want pre-image %q", got, c1)
	}

	// The journaled revert receipt.
	var reverts []map[string]interface{}
	for _, mu := range memoryUpdatesByCause(t, allEvents(t, rig, convID), "revert") {
		if mu["layer"] == "apply" {
			reverts = append(reverts, mu)
		}
	}
	if len(reverts) != 1 {
		t.Fatalf("apply-layer revert rows = %+v, want exactly one", reverts)
	}
	row := reverts[0]
	if row["epoch"] != float64(2) || row["actor"] != "human" ||
		row["before_sha16"] != sha16([]byte(B)) || row["after_sha16"] != sha16([]byte(A)) ||
		row["archive_before_sha16"] != sha16([]byte(c1+c2)) || row["archive_after_sha16"] != sha16([]byte(c1)) {
		t.Errorf("revert row = %+v — hashes must name the post→pre transition on both layers", row)
	}

	// Boot replay must NOT "repair" the reverted bytes back to B/c2: the
	// revert row retired epoch 2's receipts from the fold.
	rig.server.replayMemoryJournal(ctx)
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != A {
		t.Errorf("memory.md after boot replay = %q — the replay resurrected the post-state", got)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory-archive.md")); got != c1 {
		t.Errorf("archive after boot replay = %q — the replay resurrected the post-state", got)
	}
	if recs := projectHeals(t, rig, "recover"); len(recs) != 0 {
		t.Errorf("replay journaled %d recover rows for a reverted epoch, want 0: %+v", len(recs), recs)
	}

	// Second revert: refused; files untouched.
	if _, err := RevertMemoryEpoch(ctx, rig.store, p, 2); err == nil || !strings.Contains(err.Error(), "already reverted") {
		t.Errorf("second revert = %v, want the already-reverted refusal", err)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != A {
		t.Errorf("memory.md after refused second revert = %q, want unchanged %q", got, A)
	}

	// Epoch 1 stays revertable (its own receipt chain rooted at empty).
	if _, err := RevertMemoryEpoch(ctx, rig.store, p, 1); err != nil {
		t.Fatalf("RevertMemoryEpoch(1): %v", err)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != "" {
		t.Errorf("memory.md after reverting epoch 1 = %q, want empty (first receipt's before)", got)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory-archive.md")); got != "" {
		t.Errorf("archive after reverting epoch 1 = %q, want empty", got)
	}
}

// TestRevertSuppressedRecovery (D4 revise, 2026-08-28): the evaluate-time
// authority behind the fold retirement. A live replay pass folds a
// CALLER-TAKEN event snapshot — a human revert landing between the
// snapshot and the evaluation leaves the epoch's receipt in the fold,
// and the reverted pre-image on disk hashes to the receipt's before-sha:
// indistinguishable from a mid-write crash, so a pre-fix pass restored
// the post-state back over the human's revert. The pass must instead
// consult the lane's revert ledger fresh: skip the receipt (no write, no
// conflict), journal exactly one revert_suppressed_recovery row
// (idempotently across passes). A ledger lookup error fails CLOSED — no
// write, no row: a possible human revert is never re-applied over.
func TestRevertSuppressedRecovery(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	ctx := context.Background()

	M := "- rule — cites: main-epoch-1; reaffirmed: 1\n"
	applyEv := seedApplyReceipt(t, rig, convID, 3, nil, nil, nil, applyRecovery{
		Memory: memLayer("", M),
	})
	writeProjFile(t, root, ".odo/memory.md", M)

	// The STALE snapshot a live pass folds: taken before the revert row
	// exists, so the fold keeps epoch 3's receipt as a candidate.
	stale := allEvents(t, rig, convID)

	// The human revert (the real path): pre-image restored, revert
	// receipt journaled on the lane.
	p, err := rig.store.GetProjectByRoot(ctx, root)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if _, err := RevertMemoryEpoch(ctx, rig.store, p, 3); err != nil {
		t.Fatalf("RevertMemoryEpoch(3): %v", err)
	}

	// The stale pass must not restore: the ledger says epoch 3 is
	// terminal, and disk already holds its pre-image.
	rig.server.memMu.Lock()
	repaired := rig.server.replayLaneMemReceipts(ctx, convID, stale, replayApply)
	rig.server.memMu.Unlock()
	if len(repaired) != 0 {
		t.Errorf("repaired epochs = %v, want none — a reverted epoch never replays", repaired)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != "" {
		t.Errorf("memory.md after the stale pass = %q — the pass restored the post-state over the human revert", got)
	}
	if got := projectHeals(t, rig, "heal_conflict"); len(got) != 0 {
		t.Errorf("heal_conflict rows = %+v, want none — a suppressed receipt must not strand for review", got)
	}

	// Exactly one visibility row, naming the suppressed receipt.
	sups := memoryUpdatesByCause(t, allEvents(t, rig, convID), "revert_suppressed_recovery")
	if len(sups) != 1 {
		t.Fatalf("revert_suppressed_recovery rows = %+v, want exactly one", sups)
	}
	row := sups[0]
	if row["layer"] != "apply" || row["epoch"] != float64(3) ||
		row["receipt_layer"] != "memory" || row["receipt_seq"] != float64(applyEv.Seq) {
		t.Errorf("suppression row = %+v — want layer apply, epoch 3, receipt_layer memory, receipt_seq %d", row, applyEv.Seq)
	}

	// Idempotent: a repeat of the same stale pass journals no duplicate.
	rig.server.memMu.Lock()
	rig.server.replayLaneMemReceipts(ctx, convID, stale, replayApply)
	rig.server.memMu.Unlock()
	if sups := memoryUpdatesByCause(t, allEvents(t, rig, convID), "revert_suppressed_recovery"); len(sups) != 1 {
		t.Errorf("suppression rows after a repeat pass = %d, want 1 (the lane ledger dedupes)", len(sups))
	}

	// Fail-closed: a broken ledger lookup skips the recovery — no write,
	// no new row.
	dead, cancel := context.WithCancel(ctx)
	cancel()
	rig.server.memMu.Lock()
	rig.server.replayLaneMemReceipts(dead, convID, stale, replayApply)
	rig.server.memMu.Unlock()
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != "" {
		t.Errorf("memory.md after a failed lookup = %q — fail-closed means no write", got)
	}
	if sups := memoryUpdatesByCause(t, allEvents(t, rig, convID), "revert_suppressed_recovery"); len(sups) != 1 {
		t.Errorf("suppression rows after a failed lookup = %d, want 1", len(sups))
	}
}
