package ipc

// Drills for the 2026-08-26 memory-replay doctrine: the boot replayer's
// ordering rule (newest receipt per layer, project-wide), the foreign
// branch (entry-merge add-style, conflict everything else), idempotence
// across boots, the skill-layer path derivation, the resolve/dismiss IPC,
// and its wire shape.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// seedApplyReceipt journals one memory_apply marker (with its recovery
// block and, when proposals != nil, the preceding propose row the merge
// resolves entry text from) on convID, exactly in the live apply's shape.
// Returns the marker's journal seq — the handle resolve_heal_conflict and
// the heal rows carry.
func seedApplyReceipt(t *testing.T, rig *testRig, convID int64, epoch int, proposals []MemoryProposal, reaffirm []string, accepted []MemoryAccept, recovery applyRecovery) store.Event {
	t.Helper()
	ctx := context.Background()
	if proposals != nil {
		if _, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action":    "memory_propose",
			"epoch":     epoch,
			"proposals": proposals,
			"reaffirm":  reaffirm,
		})); err != nil {
			t.Fatalf("seed propose: %v", err)
		}
	}
	ev, err := rig.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":   "memory_apply",
		"epoch":    epoch,
		"accepted": accepted,
		"recovery": recovery,
	}))
	if err != nil {
		t.Fatalf("seed apply marker: %v", err)
	}
	return ev
}

// seedPinReceipt journals one journal-first pin receipt on convID and
// returns its journal seq.
func seedPinReceipt(t *testing.T, rig *testRig, convID int64, old, content string) store.Event {
	t.Helper()
	ev, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":      "pins",
		"cause":      "pin",
		"detail":     "seeded crash drill",
		"before_sha": sha16([]byte(old)),
		"after_sha":  sha16([]byte(content)),
		"body":       content,
	}))
	if err != nil {
		t.Fatalf("seed pin receipt: %v", err)
	}
	return ev
}

// seedLegacyPinReceipt journals one PRE-recovery pin receipt (the legacy
// file-first shape: no after_sha, no body) on convID — the terminal landed
// boundary the FIX-1 drill postdates a crashed journal-first receipt with.
func seedLegacyPinReceipt(t *testing.T, rig *testRig, convID int64) store.Event {
	t.Helper()
	ev, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":  "pins",
		"cause":  "pin",
		"detail": "legacy file-first pin (pre-recovery journal)",
	}))
	if err != nil {
		t.Fatalf("seed legacy pin receipt: %v", err)
	}
	return ev
}

// padLaneToSeq appends neutral (non-receipt) memory_update rows to convID
// until the lane's next appended event will carry per-conversation seq ==
// target — two lanes' receipts then collide on the same seq (a no-op when
// the lane is already there: both default bootstraps journal identically).
func padLaneToSeq(t *testing.T, rig *testRig, convID int64, target int) {
	t.Helper()
	for {
		evs, err := rig.store.ListEvents(context.Background(), convID, 0)
		if err != nil {
			t.Fatalf("list events for padding: %v", err)
		}
		next := 1
		if len(evs) > 0 {
			next = evs[len(evs)-1].Seq + 1
		}
		if next >= target {
			return
		}
		if _, err := rig.store.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "pins",
			"cause":  "lane-pad",
			"detail": "collision-drill padding (never a receipt candidate)",
		})); err != nil {
			t.Fatalf("pad lane: %v", err)
		}
	}
}

// projectHeals decodes every memory_update payload of one cause across the
// whole project journal (location-agnostic: heal rows ride the active
// conversation of their workstream).
func projectHeals(t *testing.T, rig *testRig, cause string) []map[string]interface{} {
	t.Helper()
	p, err := rig.store.GetProjectByRoot(context.Background(), rig.root)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	// Page through the journal deliberately small: every heal-drill then
	// exercises ListProjectEventsPage's boundaries too.
	var events []store.Event
	var afterID int64
	for {
		page, err := rig.store.ListProjectEventsPage(context.Background(), p.ID, afterID, 4)
		if err != nil {
			t.Fatalf("list project events page: %v", err)
		}
		if len(page) == 0 {
			break
		}
		events = append(events, page...)
		afterID = page[len(page)-1].ID
		if len(page) < 4 {
			break
		}
	}
	var out []map[string]interface{}
	for _, ev := range events {
		if ev.Type != store.EventMemoryUpdate {
			continue
		}
		var payload map[string]interface{}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("memory_update payload: %v", err)
		}
		if payload["cause"] == cause {
			out = append(out, payload)
		}
	}
	return out
}

// TestReplayJournalPagingEquivalence (2026-08-26 K3 hygiene drill): the
// boot replayer streams the project journal in bounded keyset pages, and
// page-boundary placement must be INVISIBLE to the outcome. The same
// synthetic journal is built twice — receipts AND heal rows straddling
// page boundaries (paged fold: 2-row pages, ≥3 pages, proposes separated
// from their applies across the cut) — then folded once with the default
// 512-row page (a single page: the pre-paging full-list fold) and once
// paged. Required identical: the recover / heal_merged / heal_conflict /
// heal_resolved payloads, their interleaved journal order, the layer
// projections, and re-boot idempotence.
func TestReplayJournalPagingEquivalence(t *testing.T) {
	causes := []string{"recover", "heal_merged", "heal_conflict", "heal_resolved"}
	type snapshot struct {
		order []string                            // interleaved "eventID:cause" of ledger rows
		rows  map[string][]map[string]interface{} // per-cause payloads in journal order
		files map[string]string                   // layer projections
		total int                                 // project journal length
	}

	// build seeds ONE deterministic synthetic journal (fresh store →
	// identical event ids) exercising every outcome class in a single
	// pass: memory entry-merge + conflict retirement, archive chunk
	// append, pins conflict, skill restore, and a superseded lane-A
	// receipt the newest-per-layer pick must skip.
	build := func(pageSize int) (*testRig, string) {
		root := initRepo(t)
		t.Setenv("HOME", t.TempDir())
		rig := startRig(t, root)
		t.Cleanup(func() { rig.stop(t) })
		rig.server.replayJournalPageSizeForTest = pageSize
		convA := bootstrapConv(t, rig, root)
		ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "beta"})
		convB := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID}).Conversation.ID

		foreign := "- hand-authored rule — cites: hand; reaffirmed: 1\n"
		writeProjFile(t, root, ".odo/memory.md", foreign)
		writeProjFile(t, root, ".odo/pins.md", "- a human pins edit\n")
		arcOld := "- kept old archive line\n"
		arcForeign := arcOld + "- later hand line\n" // foreign: past the receipt's basis, chunk absent
		writeProjFile(t, root, ".odo/memory-archive.md", arcForeign)

		// Lane A (older): a legitimate memory receipt — superseded by lane
		// B's later apply; the fold must never evaluate it. (before "" =
		// the crashed batch's long-gone basis; the foreign projection is
		// what makes lane B's twin a MERGE candidate below.)
		markerA := seedApplyReceipt(t, rig, convA, 1, []MemoryProposal{
			{Target: "memory.md", Rule: "lane-a rule", Evidence: "e1"},
		}, nil, []MemoryAccept{{Target: "memory.md", Index: 0}},
			applyRecovery{Memory: memLayer("", "- lane-a rule — cites: e1; reaffirmed: 1\n")})

		// A pre-existing open conflict for lane A's receipt: the landed
		// memory merge must RETIRE it (heal_resolved, actor superseded).
		if _, err := rig.store.AppendEvent(context.Background(), convA, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer": "memory", "cause": "heal_conflict", "detail": "pre-seeded open conflict for retirement",
			"stranded_receipt_seq": markerA.Seq, "stranded_conversation": convA,
			"stranded_body":       "- lane-a rule — cites: e1; reaffirmed: 1\n",
			"stranded_body_sha16": sha16([]byte("- lane-a rule — cites: e1; reaffirmed: 1\n")),
		})); err != nil {
			t.Fatalf("seed open conflict: %v", err)
		}
		// A stray merge row from an earlier boot: fold skips heal rows
		// wholesale; it must be invisible identically under both pagings.
		if _, err := rig.store.AppendEvent(context.Background(), convA, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer": "archive", "cause": "heal_merged", "receipt_seq": 1, "stranded_conversation": convA,
			"entries_added": 1, "detail": "stray ledger noise from an earlier boot",
		})); err != nil {
			t.Fatalf("seed stray heal row: %v", err)
		}

		// Lane B (newest authority): memory merge + archive append in one
		// apply, verbatim recorded entries so the merged line is the
		// receipt's own (round-3 FIX C determinism).
		mergedLine := "- beta merged rule — cites: e2; reaffirmed: 4\n"
		recM := memLayer("", mergedLine)
		recM.Entries = []applyRecoveryEntry{{Rule: "beta merged rule", Line: strings.TrimSuffix(mergedLine, "\n")}}
		chunk := "\n## 2026-08-26 — rotated from memory.md (overflow)\n- rotated rule — cites: e2; reaffirmed: 1\n"
		recA := &applyRecoveryLayer{BeforeSHA: sha16([]byte(arcOld)), AfterSHA: sha16([]byte(arcOld + chunk)), Body: chunk}
		seedApplyReceipt(t, rig, convB, 2, []MemoryProposal{
			{Target: "memory.md", Rule: "beta merged rule", Evidence: "e2"},
		}, nil, []MemoryAccept{{Target: "memory.md", Index: 0}},
			applyRecovery{Memory: recM, Archive: recA})
		seedPinReceipt(t, rig, convB, "", "- a human pins edit\n- stranded pin\n")
		skillBody := "---\nname: paging-skill\ndescription: paging drill\n---\n\nFold in pages.\n"
		seedApplyReceipt(t, rig, convB, 3, nil, nil, nil,
			applyRecovery{Skills: []applyRecoverySkill{{
				Name: "paging-skill.md", BeforeSHA: sha16([]byte("")), AfterSHA: sha16([]byte(skillBody)), Body: skillBody,
			}}})
		return rig, root
	}

	// capture folds the journal + projections into a comparable snapshot;
	// the read itself pages at a THIRD size (3), proving read-side
	// boundary-agnosticism too.
	capture := func(rig *testRig, root string) snapshot {
		p, err := rig.store.GetProjectByRoot(context.Background(), rig.root)
		if err != nil {
			t.Fatalf("project: %v", err)
		}
		snap := snapshot{rows: map[string][]map[string]interface{}{}, files: map[string]string{}}
		var afterID int64
		for {
			page, err := rig.store.ListProjectEventsPage(context.Background(), p.ID, afterID, 3)
			if err != nil {
				t.Fatalf("capture page: %v", err)
			}
			if len(page) == 0 {
				break
			}
			snap.total += len(page)
			for _, ev := range page {
				if ev.Type != store.EventMemoryUpdate {
					continue
				}
				var payload map[string]interface{}
				if json.Unmarshal(ev.Payload, &payload) != nil {
					continue
				}
				cause, _ := payload["cause"].(string)
				for _, want := range causes {
					if cause == want {
						snap.order = append(snap.order, fmt.Sprintf("%d:%s", ev.ID, cause))
					}
				}
			}
			afterID = page[len(page)-1].ID
			if len(page) < 3 {
				break
			}
		}
		for _, c := range causes {
			snap.rows[c] = projectHeals(t, rig, c)
		}
		snap.files["memory"] = readFileStr(t, filepath.Join(root, ".odo", "memory.md"))
		snap.files["pins"] = readFileStr(t, filepath.Join(root, ".odo", "pins.md"))
		snap.files["archive"] = readFileStr(t, filepath.Join(root, ".odo", "memory-archive.md"))
		snap.files["skill"] = readFileStr(t, filepath.Join(root, ".odo", "skills", "paging-skill.md"))
		return snap
	}

	// Full-list fold: the default 512-row page swallows the whole journal.
	rigFull, rootFull := build(0)
	rigFull.server.replayMemoryJournal(context.Background())
	full := capture(rigFull, rootFull)
	if (full.total+1)/2 < 3 {
		t.Fatalf("synthetic journal = %d events, want ≥5 so 2-row pages span ≥3 pages", full.total)
	}

	// Paged fold: 2-row pages, proposes separated from their applies.
	rigPaged, rootPaged := build(2)
	rigPaged.server.replayMemoryJournal(context.Background())
	paged := capture(rigPaged, rootPaged)

	if !reflect.DeepEqual(full, paged) {
		t.Errorf("paged replay diverged from the full-list fold\n full: order=%v rows=%v files=%v\npaged: order=%v rows=%v files=%v",
			full.order, full.rows, full.files, paged.order, paged.rows, paged.files)
	}

	// Sanity: every outcome class actually fired (a degenerate no-op
	// journal would also be "equivalent").
	wantCounts := map[string]int{"recover": 1, "heal_merged": 3, "heal_conflict": 2, "heal_resolved": 1}
	for _, c := range causes {
		if got := len(paged.rows[c]); got != wantCounts[c] {
			t.Errorf("paged %s rows = %d, want %d (stray seed + replay outcomes)", c, got, wantCounts[c])
		}
	}

	// Re-boot idempotence under paging: a second pass journals nothing.
	rigPaged.server.replayMemoryJournal(context.Background())
	if again := capture(rigPaged, rootPaged); !reflect.DeepEqual(paged, again) {
		t.Errorf("paged re-boot diverged (idempotence broken)\nfirst: order=%v rows=%v\nagain: order=%v rows=%v",
			paged.order, paged.rows, again.order, again.rows)
	}
}

// TestLaneMemReceiptFoldProposePrune (2026-08-26 K3 hygiene): the
// streaming fold's propose retention must stay O(the newest applied
// epoch + the unapplied tail), never O(the lane's distill history) —
// an apply fold prunes every older epoch (a superseded batch is never
// applied later) while keeping its OWN epoch's propose (same-epoch retry
// pairing) — and the pruning never breaks the newest candidate's
// propose→apply pairing.
func TestLaneMemReceiptFoldProposePrune(t *testing.T) {
	propose := func(epoch int) store.Event {
		return store.Event{Type: store.EventReviewAction, ConversationID: 1, Payload: json.RawMessage(mustJSON(map[string]interface{}{
			"action":    "memory_propose",
			"epoch":     epoch,
			"proposals": []MemoryProposal{{Target: "memory.md", Rule: fmt.Sprintf("rule %d", epoch), Evidence: "e"}},
		}))}
	}
	apply := func(epoch int) store.Event {
		return store.Event{Type: store.EventReviewAction, ConversationID: 1, Payload: json.RawMessage(mustJSON(map[string]interface{}{
			"action":   "memory_apply",
			"epoch":    epoch,
			"accepted": []MemoryAccept{{Target: "memory.md", Index: 0}},
			"recovery": applyRecovery{Memory: memLayer("", fmt.Sprintf("- rule %d\n", epoch))},
		}))}
	}

	f := newLaneMemReceiptFold()
	f.feed(propose(1), replayApply)
	f.feed(apply(1), replayApply)
	if len(f.proposeByEpoch) != 1 || f.proposeByEpoch[1] == nil {
		t.Fatalf("after apply 1: proposes = %v, want exactly {1} (own epoch retained for same-epoch retry pairing)", f.proposeByEpoch)
	}
	f.feed(propose(2), replayApply)
	f.feed(apply(2), replayApply)
	if len(f.proposeByEpoch) != 1 || f.proposeByEpoch[2] == nil {
		t.Fatalf("after apply 2: proposes = %v, want exactly {2} (superseded epoch 1 pruned)", f.proposeByEpoch)
	}
	// An unapplied tail (failed applies distilling onward) persists
	// unpruned UNTIL a newer apply folds — the retention bound.
	f.feed(propose(3), replayApply)
	f.feed(propose(4), replayApply)
	if len(f.proposeByEpoch) != 3 {
		t.Fatalf("with two pending batches: proposes = %v, want {2 3 4}", f.proposeByEpoch)
	}
	f.feed(apply(4), replayApply)
	if len(f.proposeByEpoch) != 1 || f.proposeByEpoch[4] == nil {
		t.Fatalf("after apply 4: proposes = %v, want exactly {4} (the tail pruned by the newer apply)", f.proposeByEpoch)
	}
	// The newest candidate still resolves its own propose after pruning.
	cand := f.cand["memory"]
	if cand.propose == nil || len(cand.propose.Proposals) != 1 || cand.propose.Proposals[0].Rule != "rule 4" {
		t.Errorf("newest memory candidate's propose = %+v, want the epoch-4 batch (rule 4)", cand.propose)
	}
}

// healRowCounts returns all four replay-ledger cause counts project-wide.
func healRowCounts(t *testing.T, rig *testRig) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, cause := range []string{"recover", "heal_merged", "heal_conflict", "heal_resolved"} {
		counts[cause] = len(projectHeals(t, rig, cause))
	}
	return counts
}

// memLayer builds a whole-file recovery block for content before→after.
func memLayer(before, after string) *applyRecoveryLayer {
	return &applyRecoveryLayer{
		BeforeSHA: sha16([]byte(before)),
		AfterSHA:  sha16([]byte(after)),
		Body:      after,
	}
}

func writeProjFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMemoryReplayHealthySupersedeChain (drill 1): receipts A(X→Y) and
// B(Y→Z) both landed, newest is B — the pass must journal ZERO rows and
// leave the projection alone (a naive per-receipt scan would re-merge A
// over Z, resurrecting replaced content on every boot).
func TestMemoryReplayHealthySupersedeChain(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	X := "- old lane rule — cites: main-epoch-1; reaffirmed: 1\n"
	Y := X + "- alpha rule — cites: main-epoch-1; reaffirmed: 1\n"
	Z := Y + "- beta rule — cites: main-epoch-2; reaffirmed: 2\n"
	writeProjFile(t, root, ".odo/memory.md", Z)

	// Lane A: applied (X→Y). Epoch and proposals are lane-local; both lanes
	// use the apply shape a real applyResolvedBatch journals.
	seedApplyReceipt(t, rig, convID, 1, []MemoryProposal{
		{Target: "memory.md", Rule: "alpha rule", Evidence: "main-epoch-1"},
	}, nil, []MemoryAccept{{Target: "memory.md", Index: 0}},
		applyRecovery{Memory: memLayer(X, Y)})

	// Lane B on a second workstream: applied (Y→Z) — its RMW basis already
	// contains A's line, so the supersede is legitimate.
	ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "beta"})
	boot2 := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
	convB := boot2.Conversation.ID
	seedApplyReceipt(t, rig, convB, 1, []MemoryProposal{
		{Target: "memory.md", Rule: "beta rule", Evidence: "main-epoch-2"},
	}, nil, []MemoryAccept{{Target: "memory.md", Index: 0}},
		applyRecovery{Memory: memLayer(Y, Z)})

	rig.server.replayMemoryJournal(context.Background())

	if got := healRowCounts(t, rig); got["recover"] != 0 || got["heal_merged"] != 0 || got["heal_conflict"] != 0 || got["heal_resolved"] != 0 {
		t.Errorf("heal rows on a healthy chain = %v, want all zero (newest-receipt doctrine: B's receipt supersedes A's, nothing is stranded)", got)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != Z {
		t.Errorf("memory.md = %q, want untouched %q", got, Z)
	}
}

// TestMemoryReplayRetractionNewestWins (drill 2): A crashed (X→r1, never
// written), B landed (X→Z with a receipt), retraction receipt C (Z→W
// removing B's r2) landed — the pass sees newest=C with disk==W=after_C
// and journals ZERO rows (never resurrect r2, never re-merge A).
func TestMemoryReplayRetractionNewestWins(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	X := "- base rule — cites: main-epoch-1; reaffirmed: 1\n"
	Y1 := X + "- stranded r1 — cites: main-epoch-1; reaffirmed: 1\n"
	Z := X + "- doomed r2 — cites: main-epoch-1; reaffirmed: 1\n"
	W := X // C removes r2, leaving exactly X

	// A journals then crashes (file never moves off X in this lane's view).
	seedApplyReceipt(t, rig, convID, 1, []MemoryProposal{
		{Target: "memory.md", Rule: "stranded r1", Evidence: "main-epoch-1"},
	}, nil, []MemoryAccept{{Target: "memory.md", Index: 0}},
		applyRecovery{Memory: memLayer(X, Y1)})
	// B lands (X→Z): its receipt is newer, until C lands on top of it.
	seedApplyReceipt(t, rig, convID, 2, []MemoryProposal{
		{Target: "memory.md", Rule: "doomed r2", Evidence: "main-epoch-1"},
	}, nil, []MemoryAccept{{Target: "memory.md", Index: 0}},
		applyRecovery{Memory: memLayer(X, Z)})
	// C lands the retraction (Z→W); W is the on-disk truth now.
	seedApplyReceipt(t, rig, convID, 3, []MemoryProposal{
		{Target: "memory.md", Rule: "replacement", Evidence: "main-epoch-2", Contradicts: "doomed r2"},
	}, nil, []MemoryAccept{{Target: "memory.md", Index: 0}},
		applyRecovery{Memory: memLayer(Z, W)})
	writeProjFile(t, root, ".odo/memory.md", W)

	rig.server.replayMemoryJournal(context.Background())

	if got := healRowCounts(t, rig); got["recover"] != 0 || got["heal_merged"] != 0 || got["heal_conflict"] != 0 || got["heal_resolved"] != 0 {
		t.Errorf("heal rows = %v, want all zero — a landed retraction receipt is every earlier receipt's authority", got)
	}
	got := readFileStr(t, filepath.Join(root, ".odo", "memory.md"))
	if got != W {
		t.Errorf("memory.md = %q, want %q (r2's retraction authoritative, r1 never resurrected)", got, W)
	}
	if strings.Contains(got, "stranded r1") || strings.Contains(got, "doomed r2") {
		t.Errorf("memory.md = %q — no resurrection allowed", got)
	}
}

// TestMemoryReplayForeignEntryMerge (drill 3, merge branch): the newest
// receipts crashed and the projection moved on by hand (foreign) — the
// replayer entry-merges ONLY the missing entries of the add-style batch,
// normalized-equal already-present rules skip, and the heal_merged row
// records exactly what it did.
//
// Round-3 FIX C pins BOTH line sources: a receipt carrying the live
// apply's verbatim per-rule lines (recovery entries) merges them
// byte-exact — evidence and the original epoch's reaffirmed count as the
// apply wrote them, never re-stamped; a legacy receipt WITHOUT recorded
// entries falls back to the synthesized reaffirmed: 1 floor (a cross-lane
// merge fabricates no recency: apply epochs are lane-local).
func TestMemoryReplayForeignEntryMerge(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	// The crashed batch wanted r1 (already hand-landed in a normalized
	// variant) + r2 (genuinely missing). The receipt carries the LIVE
	// apply's verbatim per-rule lines (recovery entries) — FIX C.
	proposals := []MemoryProposal{
		{Target: "memory.md", Rule: "always run go test", Evidence: "e1"},
		{Target: "memory.md", Rule: "verify before claiming done", Evidence: "e2"},
	}
	// Foreign disk: a human edited around (opaque line) and wrote r1 by
	// hand in a different cite/case shape (normalized compare must still
	// recognize it).
	foreign := "- a hand-authored opaque note\n- ALWAYS RUN GO TEST — cites: hand; reaffirmed: 9\n"
	writeProjFile(t, root, ".odo/memory.md", foreign)
	X := "" // the receipt's recorded basis (an empty file, long gone)
	Y1 := "- always run go test — cites: e1; reaffirmed: 4\n- verify before claiming done — cites: e2; reaffirmed: 4\n"
	rec := memLayer(X, Y1)
	rec.Entries = []applyRecoveryEntry{
		{Rule: "always run go test", Line: "- always run go test — cites: e1; reaffirmed: 4"},
		{Rule: "verify before claiming done", Line: "- verify before claiming done — cites: e2; reaffirmed: 4"},
	}
	marker := seedApplyReceipt(t, rig, convID, 4, proposals, nil,
		[]MemoryAccept{{Target: "memory.md", Index: 0}, {Target: "memory.md", Index: 1}},
		applyRecovery{Memory: rec})

	rig.server.replayMemoryJournal(context.Background())

	// Preserved metadata (FIX C): the merged line IS the receipt's recorded
	// epoch-4 line, byte-exact — no reaffirmed re-stamp, no evidence drift.
	want := foreign + "- verify before claiming done — cites: e2; reaffirmed: 4\n"
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != want {
		t.Fatalf("memory.md after merge = %q, want %q (foreign content kept, only the missing entry appended)", got, want)
	}
	merges := projectHeals(t, rig, "heal_merged")
	if len(merges) != 1 {
		t.Fatalf("heal_merged rows = %d, want 1", len(merges))
	}
	m := merges[0]
	if m["layer"] != "memory" {
		t.Errorf("heal_merged layer = %v, want memory", m["layer"])
	}
	if m["receipt_seq"] == nil || int(m["receipt_seq"].(float64)) != marker.Seq {
		t.Errorf("heal_merged receipt_seq = %v, want %d", m["receipt_seq"], marker.Seq)
	}
	if m["entries_added"] == nil || int(m["entries_added"].(float64)) != 1 {
		t.Errorf("heal_merged entries_added = %v, want 1 (r1 skipped on normalized compare)", m["entries_added"])
	}
	if m["sha16_after"] != sha16([]byte(want)) {
		t.Errorf("heal_merged sha16_after = %v, want %s", m["sha16_after"], sha16([]byte(want)))
	}
	if m["stranded_conversation"] == nil || int64(m["stranded_conversation"].(float64)) != convID {
		t.Errorf("heal_merged stranded_conversation = %v, want %d", m["stranded_conversation"], convID)
	}
	if got := healRowCounts(t, rig); got["recover"] != 0 || got["heal_conflict"] != 0 {
		t.Errorf("other heal rows = %v, want zero recover/conflict from a clean merge", got)
	}

	// Round-3 FIX C, fallback half: a LEGACY receipt (no recorded entries)
	// still synthesizes the reaffirmed: 1 floor — never the crashed
	// batch's lane-local epoch (a merged rule fabricates no recency).
	seedApplyReceipt(t, rig, convID, 5, []MemoryProposal{
		{Target: "memory.md", Rule: "third rule from legacy receipt", Evidence: "e3"},
	}, nil, []MemoryAccept{{Target: "memory.md", Index: 0}},
		applyRecovery{Memory: memLayer("", "- third rule from legacy receipt — cites: e3; reaffirmed: 5\n")})
	rig.server.replayMemoryJournal(context.Background())
	want2 := want + "- third rule from legacy receipt — cites: e3; reaffirmed: 1\n"
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != want2 {
		t.Fatalf("memory.md after legacy merge = %q, want %q (verbatim epoch-4 line kept, legacy entry floors to reaffirmed: 1)", got, want2)
	}
	merges = projectHeals(t, rig, "heal_merged")
	if len(merges) != 2 {
		t.Fatalf("heal_merged rows = %d, want 2 (one per merge)", len(merges))
	}
	if got := healRowCounts(t, rig); got["recover"] != 0 || got["heal_conflict"] != 0 {
		t.Errorf("heal rows after legacy merge = %v, want zero recover/conflict", got)
	}
}

// TestMemoryReplayForeignConflict (drill 3, conflict branch): the crashed
// receipt carries a retraction — entry-merge is unsafe, so the replayer
// journals one heal_conflict with the stranded body embedded and the
// pending_counts badge reports it. The projection is untouched.
func TestMemoryReplayForeignConflict(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	foreign := "- old rule — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", foreign)
	Y1 := foreign + "- replacement rule — cites: e1; reaffirmed: 5\n"
	marker := seedApplyReceipt(t, rig, convID, 5, []MemoryProposal{
		{Target: "memory.md", Rule: "replacement rule", Evidence: "e1", Contradicts: "old rule"},
	}, nil, []MemoryAccept{{Target: "memory.md", Index: 0}},
		applyRecovery{Memory: memLayer("", Y1)})

	rig.server.replayMemoryJournal(context.Background())

	conflicts := projectHeals(t, rig, "heal_conflict")
	if len(conflicts) != 1 {
		t.Fatalf("heal_conflict rows = %d, want 1", len(conflicts))
	}
	c := conflicts[0]
	if c["layer"] != "memory" {
		t.Errorf("conflict layer = %v, want memory", c["layer"])
	}
	if c["stranded_receipt_seq"] == nil || int(c["stranded_receipt_seq"].(float64)) != marker.Seq {
		t.Errorf("conflict stranded_receipt_seq = %v, want %d", c["stranded_receipt_seq"], marker.Seq)
	}
	if c["stranded_body"] != Y1 {
		t.Errorf("conflict stranded_body = %q, want the embedded receipt body %q", c["stranded_body"], Y1)
	}
	if c["stranded_body_sha16"] != sha16([]byte(Y1)) {
		t.Errorf("conflict stranded_body_sha16 = %v, want %s", c["stranded_body_sha16"], sha16([]byte(Y1)))
	}
	if c["stranded_conversation"] == nil || int64(c["stranded_conversation"].(float64)) != convID {
		t.Errorf("conflict stranded_conversation = %v, want %d", c["stranded_conversation"], convID)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != foreign {
		t.Errorf("memory.md after conflict = %q, want untouched %q (a conflict never clobbers foreign state)", got, foreign)
	}
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.StrandedMemoryOps != 1 {
		t.Errorf("pending_counts stranded_memory_ops = %d, want 1", pc.StrandedMemoryOps)
	}
}

// TestMemoryReplayDoubleBootZeroRows (drill 4): a second boot pass after
// both a merge and a conflict produces ZERO new rows — the merge became
// semantically landed, the conflict is ledger-suppressed, and the badge
// count holds.
func TestMemoryReplayDoubleBootZeroRows(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	// Mergeable posture on memory.md (adds-only, foreign disk).
	proposals := []MemoryProposal{{Target: "memory.md", Rule: "fresh rule", Evidence: "e1"}}
	// The foreign projection (not before "" and not after) is what makes
	// this a merge rather than a restore.
	writeProjFile(t, root, ".odo/memory.md", "- a hand memory note\n")
	seedApplyReceipt(t, rig, convID, 1, proposals, nil,
		[]MemoryAccept{{Target: "memory.md", Index: 0}},
		applyRecovery{Memory: memLayer("", "- fresh rule — cites: e1; reaffirmed: 1\n")})
	// Conflict posture on pins.md (whole-file layer, foreign disk).
	writeProjFile(t, root, ".odo/pins.md", "- a human pins edit\n")
	seedPinReceipt(t, rig, convID, "", "- a human pins edit\n- stranded pin\n")

	rig.server.replayMemoryJournal(context.Background())
	first := healRowCounts(t, rig)
	if first["heal_merged"] != 1 || first["heal_conflict"] != 1 || first["recover"] != 0 {
		t.Fatalf("post-boot rows = %v, want one merge + one conflict", first)
	}
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.StrandedMemoryOps != 1 {
		t.Fatalf("stranded count after boot = %d, want 1", pc.StrandedMemoryOps)
	}

	// Second and third boots: exact same fold, zero new rows.
	rig.server.replayMemoryJournal(context.Background())
	rig.server.replayMemoryJournal(context.Background())
	second := healRowCounts(t, rig)
	for _, k := range []string{"recover", "heal_merged", "heal_conflict", "heal_resolved"} {
		if second[k] != first[k] {
			t.Errorf("double-boot %s rows = %d, want %d (idempotent)", k, second[k], first[k])
		}
	}
	pc = rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.StrandedMemoryOps != 1 {
		t.Errorf("stranded count after double boot = %d, want 1 (an open conflict is never duplicated)", pc.StrandedMemoryOps)
	}
}

// TestMemoryReplaySkillPathDerivation (drill 5): a skill receipt's replay
// writes the SAME absolute path the live apply path would have written
// (.odo/skills/<Base(name)>) — the prefix differs but the tail must be
// byte-identical.
func TestMemoryReplaySkillPathDerivation(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	body := "---\nname: go-test\ndescription: how to run the tests\n---\n\nRun go test ./... .\n"
	// The live apply derives its target as filepath.Join(projectRoot,
	// ".odo", "skills", fname) with fname = Base(name) ("+.md" when
	// missing) — the recorded recovery Name is already that basename.
	wantPath := filepath.Join(root, ".odo", "skills", "go-test.md")
	seedApplyReceipt(t, rig, convID, 1, []MemoryProposal{
		{Target: "skills", Rule: body, Name: "go-test"},
	}, nil, []MemoryAccept{{Target: "skills", Index: 0}},
		applyRecovery{Skills: []applyRecoverySkill{{
			Name: "go-test.md", BeforeSHA: sha16([]byte("")), AfterSHA: sha16([]byte(body)), Body: body,
		}}})

	if got := rig.server.skillReplayPath("skill:go-test.md"); got != wantPath {
		t.Fatalf("skillReplayPath = %q, want %q (the apply path's absolute target)", got, wantPath)
	}
	rig.server.replayMemoryJournal(context.Background())
	if got := readFileStr(t, wantPath); got != body {
		t.Errorf("replayed skill body = %q, want %q", got, body)
	}
	recovers := projectHeals(t, rig, "recover")
	if len(recovers) != 1 {
		t.Errorf("recover rows = %d, want 1 (the replay row, cause untouched by the fold)", len(recovers))
	}
	// Double boot: the landed branch makes the second pass a no-op.
	rig.server.replayMemoryJournal(context.Background())
	if got := len(projectHeals(t, rig, "recover")); got != 1 {
		t.Errorf("recover rows after double boot = %d, want 1", got)
	}
}

// TestMemoryReplayArchiveLayer drills the archive decision: replay appends
// the recorded chunk onto a still-before archive; a foreign archive that
// already contains the chunk is semantically landed; a foreign archive
// missing it takes the chunk by append (never a whole-file overwrite).
func TestMemoryReplayArchiveLayer(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)
	arcPath := filepath.Join(root, ".odo", "memory-archive.md")

	chunk := "\n## 2026-08-26 — rotated from memory.md (overflow)\n- rotated rule — cites: e1; reaffirmed: 1\n"

	// Replay branch: archive at before, chunk absent → appended.
	writeProjFile(t, root, ".odo/memory-archive.md", "- kept old archive line\n")
	oldArc := readFileStr(t, arcPath)
	seedApplyReceipt(t, rig, convID, 1, nil, nil, nil, applyRecovery{
		Archive: &applyRecoveryLayer{
			BeforeSHA: sha16([]byte(oldArc)),
			AfterSHA:  sha16([]byte(oldArc + chunk)),
			Body:      chunk,
		},
	})
	rig.server.replayMemoryJournal(context.Background())
	if got := readFileStr(t, arcPath); got != oldArc+chunk {
		t.Fatalf("archive after replay = %q, want %q (chunk appended, not overwritten)", got, oldArc+chunk)
	}

	// Foreign-with-chunk: hand-moved archive retaining the chunk →
	// semantically landed, zero rows.
	writeProjFile(t, root, ".odo/memory-archive.md", chunk+"- later rotation — cites: e2; reaffirmed: 2\n")
	seedApplyReceipt(t, rig, convID, 1, nil, nil, nil, applyRecovery{
		Archive: &applyRecoveryLayer{
			BeforeSHA: sha16([]byte(oldArc)),
			AfterSHA:  sha16([]byte(oldArc + chunk)),
			Body:      chunk,
		},
	})
	rig.server.replayMemoryJournal(context.Background())
	mergesAfterPresent := len(projectHeals(t, rig, "heal_merged"))
	if got := readFileStr(t, arcPath); got != chunk+"- later rotation — cites: e2; reaffirmed: 2\n" {
		t.Fatalf("archive with present chunk = %q, want untouched", got)
	}

	// Foreign-missing-chunk: the last receipt's chunk is absent → appended
	// with a heal_merged row, never a whole-file write.
	writeProjFile(t, root, ".odo/memory-archive.md", "- hand archive\n")
	marker := seedApplyReceipt(t, rig, convID, 1, nil, nil, nil, applyRecovery{
		Archive: &applyRecoveryLayer{
			BeforeSHA: sha16([]byte(oldArc)),
			AfterSHA:  sha16([]byte(oldArc + chunk)),
			Body:      chunk,
		},
	})
	rig.server.replayMemoryJournal(context.Background())
	if got := readFileStr(t, arcPath); got != "- hand archive\n"+chunk {
		t.Fatalf("archive after foreign merge = %q, want hand content + chunk", got)
	}
	merges := projectHeals(t, rig, "heal_merged")
	if len(merges) != mergesAfterPresent+1 {
		t.Fatalf("heal_merged rows = %d, want %d (foreign-merge appended one chunk)", len(merges), mergesAfterPresent+1)
	}
	m := merges[len(merges)-1]
	if m["layer"] != "archive" || m["entries_added"] == nil || int(m["entries_added"].(float64)) != 1 {
		t.Errorf("archive heal_merged = %v, want layer archive with entries_added 1", m)
	}
	if m["receipt_seq"] == nil || int(m["receipt_seq"].(float64)) != marker.Seq {
		t.Errorf("archive heal_merged receipt_seq = %v, want %d", m["receipt_seq"], marker.Seq)
	}
}

// TestMemoryReplayUserLayer: user.md is the global whole-file layer — a
// mid-write crash replays exactly from the receipt's body; a foreign
// projection conflicts (never an entry parser invented for a file whose
// receipt records no structured intent).
func TestMemoryReplayUserLayer(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)
	userPath := filepath.Join(home, ".odo", "user.md")

	// Replay branch: user.md absent ("" == recorded before), receipt
	// attests its post-write body.
	body := "- prefer boring solutions — seen: odo\n"
	seedApplyReceipt(t, rig, convID, 1, nil, nil, nil,
		applyRecovery{User: memLayer("", body)})
	rig.server.replayMemoryJournal(context.Background())
	if got := readFileStr(t, userPath); got != body {
		t.Fatalf("user.md after replay = %q, want %q", got, body)
	}
	if got := len(projectHeals(t, rig, "recover")); got != 1 {
		t.Errorf("recover rows = %d, want 1", got)
	}

	// Foreign branch: disk moved elsewhere with the receipt as newest →
	// heal_conflict with the body embedded; count reports it.
	if err := os.WriteFile(userPath, []byte("- human rewrite — seen: hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedApplyReceipt(t, rig, convID, 1, nil, nil, nil,
		applyRecovery{User: memLayer("", "- crashed user rule — seen: odo\n")})
	rig.server.replayMemoryJournal(context.Background())
	if got := readFileStr(t, userPath); got != "- human rewrite — seen: hand\n" {
		t.Errorf("user.md after conflict = %q, want the human rewrite untouched", got)
	}
	conflicts := projectHeals(t, rig, "heal_conflict")
	if len(conflicts) != 1 || conflicts[0]["layer"] != "user" || conflicts[0]["stranded_body"] != "- crashed user rule — seen: odo\n" {
		t.Errorf("heal_conflict rows = %+v, want one user-layer conflict with the stranded body", conflicts)
	}
}

// TestResolveHealConflict (drill 3 + resolution): a pins receipt crashes
// onto a foreign projection — boot conflicts it, resolve_heal_conflict
// restores the stranded body, journals heal_resolved, drains the badge,
// and rejects replays of the resolved key.
func TestResolveHealConflict(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)
	pinsPathStr := pinsPath(root)

	before := "- human pins edit\n"
	writeProjFile(t, root, ".odo/pins.md", before)
	receipt := seedPinReceipt(t, rig, convID, "", "- stranded recovered pin\n")
	rig.server.replayMemoryJournal(context.Background())
	if got := len(projectHeals(t, rig, "heal_conflict")); got != 1 {
		t.Fatalf("heal_conflict rows = %d, want 1 (whole-file layer, foreign projection)", got)
	}

	// Resolve: overwrite from the embedded stranded body.
	res := rig.call(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convID,
		Layer:                "pins",
		ReceiptSeq:           receipt.Seq,
		StrandedConversation: convID,
	})
	if !res.Applied {
		t.Fatalf("resolve_heal_conflict applied = false (resp %+v)", res)
	}
	if got := readFileStr(t, pinsPathStr); got != "- stranded recovered pin\n" {
		t.Errorf("pins.md after resolve = %q, want the stranded body restored", got)
	}
	resolved := projectHeals(t, rig, "heal_resolved")
	if len(resolved) != 1 {
		t.Fatalf("heal_resolved rows = %d, want 1", len(resolved))
	}
	r := resolved[0]
	if r["receipt_seq"] == nil || int(r["receipt_seq"].(float64)) != receipt.Seq {
		t.Errorf("heal_resolved receipt_seq = %v, want %d", r["receipt_seq"], receipt.Seq)
	}
	if r["actor"] != "human" {
		t.Errorf("heal_resolved actor = %v, want human", r["actor"])
	}
	if r["dismissed"] != nil {
		t.Errorf("heal_resolved dismissed = %v, want absent on a resolve", r["dismissed"])
	}
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.StrandedMemoryOps != 0 {
		t.Errorf("stranded count after resolve = %d, want 0", pc.StrandedMemoryOps)
	}
	if res.StrandedMemoryOps != 0 {
		t.Errorf("resolve response stranded_memory_ops = %d, want 0", res.StrandedMemoryOps)
	}

	// A resolved key never resolves twice.
	resp := rig.callExpectErr(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convID,
		Layer:                "pins",
		ReceiptSeq:           receipt.Seq,
		StrandedConversation: convID,
	})
	if !strings.Contains(resp.Error, "already resolved") {
		t.Errorf("second resolve error = %q, want the already-resolved refusal", resp.Error)
	}
	if got := readFileStr(t, pinsPathStr); got != "- stranded recovered pin\n" {
		t.Errorf("pins.md after refused re-resolve = %q, want unchanged", got)
	}
	if got := len(projectHeals(t, rig, "heal_resolved")); got != 1 {
		t.Errorf("heal_resolved rows after refusal = %d, want 1", got)
	}

	// An unrelated key refuses by name.
	resp = rig.callExpectErr(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convID,
		Layer:                "memory",
		ReceiptSeq:           receipt.Seq,
		StrandedConversation: convID,
	})
	if !strings.Contains(resp.Error, "no heal_conflict") {
		t.Errorf("unknown-key resolve error = %q, want the no-such refusal", resp.Error)
	}
}

// TestResolveHealConflictDismiss: the dismiss half records the human's
// decision without touching files or the ledger's integrity — the badge
// drains, a later resolve of the same key refuses.
func TestResolveHealConflictDismiss(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	writeProjFile(t, root, ".odo/pins.md", "- human pins edit\n")
	receipt := seedPinReceipt(t, rig, convID, "", "- stranded pin to dismiss\n")
	rig.server.replayMemoryJournal(context.Background())
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 1 {
		t.Fatalf("pre-dismiss count = %d, want 1", got.StrandedMemoryOps)
	}

	res := rig.call(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convID,
		Layer:                "pins",
		ReceiptSeq:           receipt.Seq,
		Dismissed:            true,
		StrandedConversation: convID,
	})
	if !res.Applied {
		t.Fatalf("dismiss applied = false (resp %+v)", res)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "pins.md")); got != "- human pins edit\n" {
		t.Errorf("pins.md after dismiss = %q, want the foreign projection untouched", got)
	}
	resolved := projectHeals(t, rig, "heal_resolved")
	if len(resolved) != 1 || resolved[0]["dismissed"] != true {
		t.Fatalf("heal_resolved rows = %+v, want one dismissed row", resolved)
	}
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 0 {
		t.Errorf("post-dismiss count = %d, want 0", got.StrandedMemoryOps)
	}
	resp := rig.callExpectErr(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convID,
		Layer:                "pins",
		ReceiptSeq:           receipt.Seq,
		StrandedConversation: convID,
	})
	if !strings.Contains(resp.Error, "already resolved") {
		t.Errorf("resolve-after-dismiss error = %q, want already resolved", resp.Error)
	}

	// A dismissed conflict is also boot-idempotent: the resolution row
	// bars a fresh conflict for the same key on the next pass.
	rig.server.replayMemoryJournal(context.Background())
	if got := len(projectHeals(t, rig, "heal_conflict")); got != 1 {
		t.Errorf("heal_conflict rows after re-boot = %d, want 1 (dismissal sticks)", got)
	}
}

// TestResolveHealConflictWireShape (drill 6): the daemon Request marshals
// onto exactly the wire shape the GUI sends through the Rust bridge
// (Tauri maps camelCase args onto snake_case params that serialize to
// these keys), and decodes back losslessly.
func TestResolveHealConflictWireShape(t *testing.T) {
	req := Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       7,
		Layer:                "pins",
		ReceiptSeq:           3,
		Dismissed:            true,
		StrandedConversation: 9,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"cmd":"resolve_heal_conflict","conversation_id":7,"layer":"pins","receipt_seq":3,"dismissed":true,"stranded_conversation":9}`
	if string(data) != want {
		t.Errorf("wire JSON = %s, want %s", data, want)
	}
	var back Request
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if back.Cmd != req.Cmd || back.ConversationID != req.ConversationID || back.Layer != req.Layer ||
		back.ReceiptSeq != req.ReceiptSeq || back.Dismissed != req.Dismissed ||
		back.StrandedConversation != req.StrandedConversation {
		t.Errorf("round-trip = %+v, want %+v", back, req)
	}

	// The non-dismissed (restore) variant's wire shape.
	data, err = json.Marshal(Request{Cmd: CmdResolveHealConflict, ConversationID: 7, Layer: "memory", ReceiptSeq: 3, StrandedConversation: 9})
	if err != nil {
		t.Fatalf("marshal resolve: %v", err)
	}
	want = `{"cmd":"resolve_heal_conflict","conversation_id":7,"layer":"memory","receipt_seq":3,"stranded_conversation":9}`
	if string(data) != want {
		t.Errorf("restore wire JSON = %s, want %s", data, want)
	}
}

// TestFoldHealLedger pairs conflicts with resolutions by content key —
// the pending_counts stranded_memory_ops semantic (conflict minus
// resolved, project-wide, tuple-deduped).
func TestFoldHealLedger(t *testing.T) {
	row := func(seq int, conv int64, cause, layer string, rSeq int) store.Event {
		payload := map[string]interface{}{
			"cause":                 cause,
			"layer":                 layer,
			"stranded_conversation": conv,
		}
		if cause == "heal_conflict" {
			payload["stranded_receipt_seq"] = rSeq
		} else {
			payload["receipt_seq"] = rSeq
		}
		return store.Event{Seq: seq, Type: store.EventMemoryUpdate, Payload: json.RawMessage(mustJSON(payload))}
	}
	rows := []store.Event{
		row(1, 3, "heal_conflict", "pins", 5),
		row(2, 3, "heal_conflict", "memory", 6),
		row(3, 3, "heal_resolved", "pins", 5),
	}
	unresolved, resolved := foldHealLedger(rows)
	if len(unresolved) != 1 {
		t.Errorf("unresolved = %v, want the memory conflict only", unresolved)
	}
	if _, ok := unresolved[healKey{conv: 3, layer: "memory", seq: 6}]; !ok {
		t.Errorf("unresolved keys = %v, want {(3,memory,6)}", unresolved)
	}
	if !resolved[healKey{conv: 3, layer: "pins", seq: 5}] {
		t.Errorf("resolved = %v, want the pins key", resolved)
	}
}

// TestMemoryReplayLiveApplyRetryConverges: within one daemon lifetime a
// write-failed apply (marker journaled, memory.md sabotaged) converges
// through the consumed-branch engine pass — the retry replays the
// receipt's own layers and reports Applied instead of the bare consumed
// refusal (the TestUserMemoryIdempotency contract, engine form).
func TestMemoryReplayLiveApplyRetryConverges(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	const rule = "Retry converges through the replay engine."
	seedProposeBatch(t, rig, convID, 1, []MemoryProposal{
		{Target: "memory.md", Rule: rule, Evidence: "main-epoch-1"},
	}, nil)
	applyReq := Request{Cmd: CmdApplyMemory, ConversationID: convID, Epoch: 1,
		Accepted: []MemoryAccept{{Target: "memory.md", Index: 0}}}
	memPath := filepath.Join(root, ".odo", "memory.md")

	// Sabotage all writes: a directory at memory.md's path makes the rename
	// fail AFTER the marker journaled (batch consumed, file lagging).
	if err := os.MkdirAll(memPath, 0o755); err != nil {
		t.Fatal(err)
	}
	resp := rig.callExpectErr(t, applyReq)
	if !strings.Contains(resp.Error, "write memory.md") {
		t.Fatalf("sabotaged apply error = %q, want the memory.md write failure", resp.Error)
	}
	if err := os.Remove(memPath); err != nil {
		t.Fatal(err)
	}

	// Retry: the consumed branch replays this batch's own marker (disk ==
	// before → restore) and reports Applied.
	retry := rig.call(t, applyReq)
	if !retry.Applied {
		t.Fatal("retry after write failure: applied must be true — the engine converged the batch")
	}
	want := "- " + rule + " — cites: main-epoch-1; reaffirmed: 1\n"
	if got := readFileStr(t, memPath); got != want {
		t.Errorf("memory.md after convergent retry = %q, want %q", got, want)
	}
	if got := len(projectHeals(t, rig, "recover")); got != 1 {
		t.Errorf("recover rows = %d, want 1 (the engine journaled the restore)", got)
	}
}

// TestResolveHealConflictValidation pins the request guards the handler
// enforces before touching files or the ledger.
func TestResolveHealConflictValidation(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	bad := []struct {
		name string
		req  Request
	}{
		{"missing layer", Request{Cmd: CmdResolveHealConflict, ConversationID: convID, ReceiptSeq: 1}},
		{"unknown layer", Request{Cmd: CmdResolveHealConflict, ConversationID: convID, Layer: "ledger", ReceiptSeq: 1}},
		{"traversal skill layer", Request{Cmd: CmdResolveHealConflict, ConversationID: convID, Layer: "skill:../..", ReceiptSeq: 1}},
		{"missing receipt_seq", Request{Cmd: CmdResolveHealConflict, ConversationID: convID, Layer: "pins"}},
		{"missing stranded_conversation", Request{Cmd: CmdResolveHealConflict, ConversationID: convID, Layer: "pins", ReceiptSeq: 1}},
	}
	for _, tc := range bad {
		resp := rig.callExpectErr(t, tc.req)
		if !strings.Contains(resp.Error, "resolve_heal_conflict") {
			t.Errorf("%s: error = %q, want the handler's refusal", tc.name, resp.Error)
		}
	}
	if got := healRowCounts(t, rig); got["heal_conflict"] != 0 || got["heal_resolved"] != 0 {
		t.Errorf("heal rows = %v, want zero — validation failures journal nothing", got)
	}
}

// TestMemoryReplayLegacyPinTerminalBoundary (FIX-1 drill): a legacy pin
// receipt (journaled file-first, no after_sha/body) NEWER than a crashed
// journal-first receipt is a terminal landed boundary — the legacy write
// already landed, so the older replayable receipt must never become the
// candidate. The inverted fold (continue past the legacy row) fell
// through to the older receipt and manufactured a false heal_conflict on
// superseded/already-present pins. Drilled same-lane and cross-lane — the
// tombstone masks via lane order in the first, via its global event id in
// the second.
func TestMemoryReplayLegacyPinTerminalBoundary(t *testing.T) {
	const landed = "- legacy landed pin\n"

	t.Run("same lane", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("HOME", t.TempDir())
		rig := startRig(t, root)
		defer rig.stop(t)
		convID := bootstrapConv(t, rig, root)

		writeProjFile(t, root, ".odo/pins.md", landed)
		// The crashed journal-first receipt in the false-conflict posture
		// (disk is neither its before nor its after).
		seedPinReceipt(t, rig, convID, "", landed+"- stranded pin\n")
		seedLegacyPinReceipt(t, rig, convID) // postdates it on the same lane

		rig.server.replayMemoryJournal(context.Background())
		if got := healRowCounts(t, rig); got["recover"] != 0 || got["heal_merged"] != 0 || got["heal_conflict"] != 0 || got["heal_resolved"] != 0 {
			t.Errorf("heal rows = %v, want all zero — the legacy receipt is the layer's terminal landed boundary", got)
		}
		if got := readFileStr(t, filepath.Join(root, ".odo", "pins.md")); got != landed {
			t.Errorf("pins.md = %q, want the legacy-landed file untouched %q", got, landed)
		}
	})

	t.Run("cross lane", func(t *testing.T) {
		root := initRepo(t)
		t.Setenv("HOME", t.TempDir())
		rig := startRig(t, root)
		defer rig.stop(t)
		convA := bootstrapConv(t, rig, root)

		writeProjFile(t, root, ".odo/pins.md", landed)
		seedPinReceipt(t, rig, convA, "", landed+"- stranded pin\n") // older global event id
		// The project's TRUE newest pins row lives on lane B: a legacy
		// receipt — it outranks lane A's crashed receipt in the
		// event-id pick.
		ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "legacy-lane"})
		bootB := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
		seedLegacyPinReceipt(t, rig, bootB.Conversation.ID)

		rig.server.replayMemoryJournal(context.Background())
		if got := healRowCounts(t, rig); got["recover"] != 0 || got["heal_merged"] != 0 || got["heal_conflict"] != 0 || got["heal_resolved"] != 0 {
			t.Errorf("heal rows = %v, want all zero — the cross-lane legacy receipt masks the older crashed one", got)
		}
		if got := readFileStr(t, filepath.Join(root, ".odo", "pins.md")); got != landed {
			t.Errorf("pins.md = %q, want the legacy-landed file untouched %q", got, landed)
		}
	})
}

// TestMemoryReplayTwoLaneNewestPerLayer (drills 2/3, two-lane variant):
// the project-wide newest-per-layer pick under a real lane split. Lane A
// crashes (X→r1, never written); lane B lands r2 and then a legitimate
// retraction-as-newest (back to X). The boot fold must journal ZERO rows
// and never resurrect A's r1 — a per-lane scan WOULD restore it (A's lane
// still shows disk at before), which is exactly the cross-lane
// supersession hole the doctrine closes. The conflict half attributes
// lane A's stranded pins receipt to lane A even with lane B's ordinary
// events newer (attribution follows the receipt, never lane order).
func TestMemoryReplayTwoLaneNewestPerLayer(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convA := bootstrapConv(t, rig, root)

	X := "- base rule — cites: main-epoch-1; reaffirmed: 1\n"
	Y1 := X + "- stranded r1 — cites: main-epoch-1; reaffirmed: 1\n"
	Z := X + "- doomed r2 — cites: main-epoch-1; reaffirmed: 1\n"

	// Lane A: journals then crashes (disk never leaves X).
	seedApplyReceipt(t, rig, convA, 1, []MemoryProposal{
		{Target: "memory.md", Rule: "stranded r1", Evidence: "main-epoch-1"},
	}, nil, []MemoryAccept{{Target: "memory.md", Index: 0}},
		applyRecovery{Memory: memLayer(X, Y1)})

	// Lane B: r2 lands, then r2's retraction lands — the project's newest
	// receipt is the retraction, and disk == its after (X).
	ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "retraction-lane"})
	bootB := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
	convB := bootB.Conversation.ID
	seedApplyReceipt(t, rig, convB, 1, nil, nil, nil,
		applyRecovery{Memory: memLayer(X, Z)})
	seedApplyReceipt(t, rig, convB, 2, nil, nil, nil,
		applyRecovery{Memory: memLayer(Z, X)})
	writeProjFile(t, root, ".odo/memory.md", X)

	rig.server.replayMemoryJournal(context.Background())
	if got := healRowCounts(t, rig); got["recover"] != 0 || got["heal_merged"] != 0 || got["heal_conflict"] != 0 || got["heal_resolved"] != 0 {
		t.Errorf("heal rows = %v, want all zero — lane B's landed retraction is lane A's authority", got)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != X {
		t.Errorf("memory.md = %q, want %q (r2's retraction authoritative, A's r1 never resurrected)", got, X)
	}

	// Conflict attribution across lanes: lane A strands a pins receipt on
	// a foreign projection; lane B then appends a NEWER ordinary
	// (non-receipt) event. The conflict must name lane A — the receipt's
	// own conversation, never whichever lane folded last.
	const humanPins = "- human pins edit\n"
	writeProjFile(t, root, ".odo/pins.md", humanPins)
	receiptA := seedPinReceipt(t, rig, convA, "", "- stranded pin from crashed lane\n")
	if _, err := rig.store.AppendEvent(context.Background(), convB, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":  "pins",
		"cause":  "lane-pad",
		"detail": "newer-than-the-conflict ordinary row",
	})); err != nil {
		t.Fatalf("append lane-B ordinary row: %v", err)
	}

	rig.server.replayMemoryJournal(context.Background())
	conflicts := projectHeals(t, rig, "heal_conflict")
	if len(conflicts) != 1 {
		t.Fatalf("heal_conflict rows = %d, want 1 for lane A's stranded pins receipt", len(conflicts))
	}
	c := conflicts[0]
	if c["layer"] != "pins" {
		t.Errorf("conflict layer = %v, want pins", c["layer"])
	}
	if c["stranded_conversation"] == nil || int64(c["stranded_conversation"].(float64)) != convA {
		t.Errorf("conflict stranded_conversation = %v, want %d (lane A — attribution follows the receipt)", c["stranded_conversation"], convA)
	}
	if c["stranded_receipt_seq"] == nil || int(c["stranded_receipt_seq"].(float64)) != receiptA.Seq {
		t.Errorf("conflict stranded_receipt_seq = %v, want %d", c["stranded_receipt_seq"], receiptA.Seq)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "pins.md")); got != humanPins {
		t.Errorf("pins.md = %q, want the foreign projection untouched %q", got, humanPins)
	}
}

// TestResolveHealConflictTwoLaneCollision (FIX-2 drill): two crashed
// lanes strand pins receipts at the SAME per-conversation seq. Each
// conflict is its own ledger row keyed (stranded_conversation, layer,
// seq) — the GUI's row identity — resolvable from EITHER lane's
// conversation with the count decrementing per closure. Under
// carrier-conversation routing the first resolve from lane B would have
// matched lane B's OWN same-seq row (wrong key); under
// request-conversation-only scans an active-conversation rotation would
// have hidden lane A's row entirely. Round-3 FIX E governs the second
// closure: resolving A moves the file, so B's same-seq conflict is stale
// by then — the freshness guard refuses the write and closes B as
// superseded rather than clobbering A's landed body.
func TestResolveHealConflictTwoLaneCollision(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convA := bootstrapConv(t, rig, root)
	pinsFile := filepath.Join(root, ".odo", "pins.md")

	const foreign = "- human pins edit\n"
	bodyA := foreign + "- stranded pin from lane A\n"
	bodyB := foreign + "- stranded pin from lane B\n"
	writeProjFile(t, root, ".odo/pins.md", foreign)

	// Lane A crashes its receipt (disk is neither before "" nor after —
	// foreign posture on a whole-file layer); boot pass 1 conflicts it
	// (A is the project's only pins receipt, hence the newest).
	receiptA := seedPinReceipt(t, rig, convA, "", bodyA)
	rig.server.replayMemoryJournal(context.Background())
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 1 {
		t.Fatalf("stranded count after pass 1 = %d, want 1", got.StrandedMemoryOps)
	}

	// Lane B crashes ITS receipt at the same per-conversation seq (the
	// pad makes the collision exact); boot pass 2 conflicts it as the
	// newer receipt while A's open conflict stays ledger-suppressed.
	ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "collision-b"})
	bootB := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
	convB := bootB.Conversation.ID
	padLaneToSeq(t, rig, convB, receiptA.Seq)
	receiptB := seedPinReceipt(t, rig, convB, "", bodyB)
	if receiptA.Seq != receiptB.Seq {
		t.Fatalf("receipt seqs = %d vs %d — the drill requires the cross-lane same-seq collision", receiptA.Seq, receiptB.Seq)
	}
	rig.server.replayMemoryJournal(context.Background())
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 2 {
		t.Fatalf("stranded count after pass 2 = %d, want 2 (both lanes' rows remain individually open)", got.StrandedMemoryOps)
	}

	// Both rows carry the same (layer, seq) and differ ONLY on the
	// stranded conversation — the identity half the fix routes by.
	conflicts := projectHeals(t, rig, "heal_conflict")
	if len(conflicts) != 2 {
		t.Fatalf("heal_conflict rows = %d, want 2", len(conflicts))
	}
	seen := map[int64]bool{}
	for _, c := range conflicts {
		if c["layer"] != "pins" || c["stranded_receipt_seq"] == nil || int(c["stranded_receipt_seq"].(float64)) != receiptA.Seq {
			t.Fatalf("conflict row = %+v, want (pins, seq %d) on both rows", c, receiptA.Seq)
		}
		conv := int64(c["stranded_conversation"].(float64))
		seen[conv] = true
	}
	if !seen[convA] || !seen[convB] {
		t.Fatalf("conflict attribution = %v, want rows for lanes %d and %d", seen, convA, convB)
	}

	// Resolve lane A's row FROM lane B's conversation (resolver lane is
	// never the identity): A's body lands, A's key closes, B's row stays.
	res := rig.call(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convB,
		Layer:                "pins",
		ReceiptSeq:           receiptA.Seq,
		StrandedConversation: convA,
	})
	if !res.Applied {
		t.Fatalf("resolve lane A applied = false (resp %+v)", res)
	}
	if res.StrandedMemoryOps != 1 {
		t.Errorf("resolve response stranded_memory_ops = %d, want 1 (lane B still open)", res.StrandedMemoryOps)
	}
	if got := readFileStr(t, pinsFile); got != bodyA {
		t.Errorf("pins.md after resolving A = %q, want lane A's stranded body %q", got, bodyA)
	}
	resolved := projectHeals(t, rig, "heal_resolved")
	if len(resolved) != 1 || resolved[0]["stranded_conversation"] == nil ||
		int64(resolved[0]["stranded_conversation"].(float64)) != convA {
		t.Fatalf("heal_resolved rows = %+v, want exactly lane A's closure", resolved)
	}

	// A's key refuses a second close while B's identical (layer, seq)
	// stays open.
	resp := rig.callExpectErr(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convB,
		Layer:                "pins",
		ReceiptSeq:           receiptA.Seq,
		StrandedConversation: convA,
	})
	if !strings.Contains(resp.Error, "already resolved") {
		t.Errorf("re-resolve A error = %q, want the already-resolved refusal", resp.Error)
	}

	// Round-3 FIX E flips the second act: A's resolve MOVED pins.md to
	// bodyA, so B's conflict (journaled against the shared foreign file)
	// is stale — resolving it would stomp A's landed body with a stale
	// stranded body. The freshness guard refuses AND auto-dismisses B as
	// superseded (the count still drains to zero, the projection never
	// clobbers). Carrier-independence of the routing is unchanged; the
	// refusal is about the FILE, not the lane.
	resp = rig.callExpectErr(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convA,
		Layer:                "pins",
		ReceiptSeq:           receiptB.Seq,
		StrandedConversation: convB,
	})
	if !strings.Contains(resp.Error, "moved since the conflict journaled") {
		t.Errorf("resolve lane B error = %q, want the freshness refusal (A's body landed first)", resp.Error)
	}
	if got := readFileStr(t, pinsFile); got != bodyA {
		t.Errorf("pins.md after refused resolve B = %q, want lane A's landed body %q (B's stale body never clobbers)", got, bodyA)
	}
	resolved = projectHeals(t, rig, "heal_resolved")
	if len(resolved) != 2 {
		t.Fatalf("heal_resolved rows = %d, want 2 (A human + B superseded)", len(resolved))
	}
	b := resolved[1]
	if b["actor"] != "superseded" || b["dismissed"] != true ||
		b["stranded_conversation"] == nil || int64(b["stranded_conversation"].(float64)) != convB {
		t.Errorf("lane B closure row = %+v, want superseded dismissal naming conversation %d", b, convB)
	}
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 0 {
		t.Errorf("pending_counts after closure chain = %d, want 0 (refusal closed B as superseded)", got.StrandedMemoryOps)
	}

	// B's key closed with the refusal — a retry names the closure, never
	// re-writes.
	resp = rig.callExpectErr(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convA,
		Layer:                "pins",
		ReceiptSeq:           receiptB.Seq,
		StrandedConversation: convB,
	})
	if !strings.Contains(resp.Error, "already resolved") {
		t.Errorf("re-resolve B error = %q, want the already-resolved refusal", resp.Error)
	}
}

// TestMemoryReplayArchivedWorkstreamFolds (round-3 FIX B drill): a
// receipt journaled on a workstream that is then soft-deleted still
// folds — the events/heal-ledger jobs LEFT JOIN from the events table,
// so surviving lane rows feed replay AND the stranded count regardless
// of workstream status. The drill covers all three surfaces: a restore
// (memory.md replayed from the archived lane's receipt), a conflict on
// the same lane (stranded count + pending_counts rows), and the heal
// rows' own journaling on the archived lane.
func TestMemoryReplayArchivedWorkstreamFolds(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	bootstrapConv(t, rig, root) // main lane: survives the drill

	X := "- base rule — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", X)
	writeProjFile(t, root, ".odo/pins.md", "- human pins edit\n")

	// The doomed lane: journal BOTH a restore-grade memory receipt (disk
	// still at its before) and a conflict-grade pins receipt (foreign
	// disk), then soft-delete the workstream before any replay pass.
	ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "doomed-lane"})
	bootD := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
	convD := bootD.Conversation.ID
	Y := X + "- restored from archived lane — cites: main-epoch-1; reaffirmed: 1\n"
	seedApplyReceipt(t, rig, convD, 1, nil, nil, nil,
		applyRecovery{Memory: memLayer(X, Y)})
	pinReceipt := seedPinReceipt(t, rig, convD, "", "- human pins edit\n- stranded pin from archived lane\n")
	if err := rig.store.DeleteWorkstream(context.Background(), ws.Workstream.ID); err != nil {
		t.Fatalf("soft-delete doomed lane: %v", err)
	}

	// BOOT: the archived lane's receipts still fold into the fold's
	// newest-per-layer pick.
	rig.server.replayMemoryJournal(context.Background())
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != Y {
		t.Errorf("memory.md after boot = %q, want %q (archived lane's stranded receipt replayed)", got, Y)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "pins.md")); got != "- human pins edit\n" {
		t.Errorf("pins.md after boot = %q, want the foreign projection untouched", got)
	}
	recovers := projectHeals(t, rig, "recover")
	if len(recovers) != 1 || recovers[0]["layer"] != "memory" {
		t.Errorf("recover rows = %+v, want the archived lane's memory restore", recovers)
	}
	conflicts := projectHeals(t, rig, "heal_conflict")
	if len(conflicts) != 1 {
		t.Fatalf("heal_conflict rows = %d, want 1 for the archived lane's pins receipt", len(conflicts))
	}
	c := conflicts[0]
	if c["stranded_conversation"] == nil || int64(c["stranded_conversation"].(float64)) != convD {
		t.Errorf("conflict attribution = %v, want the ARCHIVED lane's conversation %d", c["stranded_conversation"], convD)
	}
	if c["disk_sha16_at_conflict"] != sha16([]byte("- human pins edit\n")) {
		t.Errorf("conflict disk_sha16_at_conflict = %v, want the foreign pins digest", c["disk_sha16_at_conflict"])
	}

	// The stranded count and the pending_counts rows see the archived
	// lane too (the heal-ledger LEFT JOIN + round-3 FIX F rows).
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.StrandedMemoryOps != 1 {
		t.Fatalf("stranded count with archived lane = %d, want 1", pc.StrandedMemoryOps)
	}
	if len(pc.StrandedOps) != 1 {
		t.Fatalf("stranded rows = %+v, want the archived lane's one row", pc.StrandedOps)
	}
	op := pc.StrandedOps[0]
	if op.StrandedConversation != convD || op.Layer != "pins" || op.ReceiptSeq != pinReceipt.Seq {
		t.Errorf("stranded row = %+v, want (conv %d, pins, seq %d)", op, convD, pinReceipt.Seq)
	}
	if !strings.Contains(op.Detail, "stranded pins post-crash") {
		t.Errorf("stranded row detail = %q, want the conflict's reason copy", op.Detail)
	}
}

// TestMemoryReplaySupersessionRetiresConflict (round-3 FIX D drill): an
// open conflict is no longer conscientiously resolvable once a NEWER
// receipt lands the layer — the next evaluation (here: boot) closes it
// as heal_resolved{actor:"superseded", dismissed:true} and the stranded
// count returns to 0, without touching the landed projection.
func TestMemoryReplaySupersessionRetiresConflict(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)
	pinsFile := filepath.Join(root, ".odo", "pins.md")

	const foreign = "- human pins edit\n"
	const landed = "- human pins edit\n- landed pin v2 (legitimate newer receipt)\n"
	writeProjFile(t, root, ".odo/pins.md", foreign)

	// Boot 1: the crashed receipt strands on the foreign projection.
	stranded := seedPinReceipt(t, rig, convID, "", "- stranded superseded pin\n")
	rig.server.replayMemoryJournal(context.Background())
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 1 {
		t.Fatalf("count after boot 1 = %d, want 1", got.StrandedMemoryOps)
	}

	// A NEWER receipt legitimately lands (before=foreign on disk is what
	// makes the receipt's own landing consistent), so disk == its after.
	writeProjFile(t, root, ".odo/pins.md", landed)
	seedPinReceipt(t, rig, convID, foreign, landed)
	rig.server.replayMemoryJournal(context.Background())

	if got := readFileStr(t, pinsFile); got != landed {
		t.Errorf("pins.md = %q, want the landed projection untouched %q", got, landed)
	}
	resolved := projectHeals(t, rig, "heal_resolved")
	if len(resolved) != 1 {
		t.Fatalf("heal_resolved rows = %d, want 1 (the supersession retirement)", len(resolved))
	}
	r := resolved[0]
	if r["actor"] != "superseded" || r["dismissed"] != true {
		t.Errorf("retirement row = %+v, want actor superseded + dismissed", r)
	}
	if r["receipt_seq"] == nil || int(r["receipt_seq"].(float64)) != stranded.Seq {
		t.Errorf("retirement receipt_seq = %v, want %d (the stranded receipt)", r["receipt_seq"], stranded.Seq)
	}
	if r["stranded_conversation"] == nil || int64(r["stranded_conversation"].(float64)) != convID {
		t.Errorf("retirement stranded_conversation = %v, want %d", r["stranded_conversation"], convID)
	}
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 0 {
		t.Errorf("count after supersession = %d, want 0", got.StrandedMemoryOps)
	}
	// Retired means retired: resolve names the closure, and a third boot
	// journals nothing new.
	resp := rig.callExpectErr(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convID,
		Layer:                "pins",
		ReceiptSeq:           stranded.Seq,
		StrandedConversation: convID,
	})
	if !strings.Contains(resp.Error, "already resolved") {
		t.Errorf("resolve after supersession = %q, want the already-resolved refusal", resp.Error)
	}
	rig.server.replayMemoryJournal(context.Background())
	if got := healRowCounts(t, rig); got["heal_conflict"] != 1 || got["heal_resolved"] != 1 || got["recover"] != 0 {
		t.Errorf("third boot rows = %v, want the conflict+retirement pair only, no new rows", got)
	}
}

// TestResolveHealConflictFreshnessGuard (round-3 FIX E drill): the
// conflict journaled with the file's live digest; a hand edit since
// makes Resolve refuse the overwrite AND close the conflict as
// superseded — the stranded body never stomps the human's newer bytes.
func TestResolveHealConflictFreshnessGuard(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)
	pinsFile := filepath.Join(root, ".odo", "pins.md")

	writeProjFile(t, root, ".odo/pins.md", "- human pins edit\n")
	receipt := seedPinReceipt(t, rig, convID, "", "- stranded recovered pin\n")
	rig.server.replayMemoryJournal(context.Background())
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 1 {
		t.Fatalf("pre-edit count = %d, want 1", got.StrandedMemoryOps)
	}

	// The human edits pins.md by hand (without resolving) — the file
	// moved since the conflict journaled.
	writeProjFile(t, root, ".odo/pins.md", "- human pins edit v2 (hand)\n")
	resp := rig.callExpectErr(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convID,
		Layer:                "pins",
		ReceiptSeq:           receipt.Seq,
		StrandedConversation: convID,
	})
	if !strings.Contains(resp.Error, "moved since the conflict journaled") {
		t.Fatalf("stale resolve error = %q, want the freshness refusal", resp.Error)
	}
	if got := readFileStr(t, pinsFile); got != "- human pins edit v2 (hand)\n" {
		t.Errorf("pins.md after refused resolve = %q, want the hand edit intact (no clobber)", got)
	}
	resolved := projectHeals(t, rig, "heal_resolved")
	if len(resolved) != 1 || resolved[0]["actor"] != "superseded" || resolved[0]["dismissed"] != true {
		t.Fatalf("heal_resolved rows = %+v, want one superseded dismissal from the refusal", resolved)
	}
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 0 {
		t.Errorf("count after auto-dismissal = %d, want 0 (the refusal dropped the badge)", got.StrandedMemoryOps)
	}
	// The key closed with the refusal; Dismiss is equally late.
	resp = rig.callExpectErr(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convID,
		Layer:                "pins",
		ReceiptSeq:           receipt.Seq,
		Dismissed:            true,
		StrandedConversation: convID,
	})
	if !strings.Contains(resp.Error, "already resolved") {
		t.Errorf("dismiss after refusal = %q, want already resolved", resp.Error)
	}
}

// TestPendingCountsStrandedOpsConflictRows (round-3 FIX F, daemon half):
// pending_counts exposes the open heal-ledger rows PROJECT-WIDE as
// (conversation_id, layer, receipt_seq) — count/rows consistency for
// lanes that rotated away from the viewer — sorted deterministically,
// and dropping per resolution.
func TestPendingCountsStrandedOpsConflictRows(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convA := bootstrapConv(t, rig, root)

	const foreign = "- human pins edit\n"
	writeProjFile(t, root, ".odo/pins.md", foreign)
	receiptA := seedPinReceipt(t, rig, convA, "", "- stranded pin A\n")
	ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "row-lane-b"})
	bootB := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
	convB := bootB.Conversation.ID

	// Boot 1 conflicts A; B's NEWER pins receipt then re-conflicts on the
	// still-foreign disk (A's stays open — its receipt is no longer
	// newest, and nothing landed).
	rig.server.replayMemoryJournal(context.Background())
	receiptB := seedPinReceipt(t, rig, convB, "", "- stranded pin B\n")
	rig.server.replayMemoryJournal(context.Background())

	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.StrandedMemoryOps != 2 {
		t.Fatalf("stranded count = %d, want 2", pc.StrandedMemoryOps)
	}
	if len(pc.StrandedOps) != 2 {
		t.Fatalf("stranded rows = %+v, want both lanes' rows", pc.StrandedOps)
	}
	if got := pc.StrandedOps[0]; got.StrandedConversation != convA || got.Layer != "pins" || got.ReceiptSeq != receiptA.Seq {
		t.Errorf("row[0] = %+v, want lane A (conv %d, pins, seq %d) — sorted conversation-first", got, convA, receiptA.Seq)
	}
	if got := pc.StrandedOps[1]; got.StrandedConversation != convB || got.Layer != "pins" || got.ReceiptSeq != receiptB.Seq {
		t.Errorf("row[1] = %+v, want lane B (conv %d, pins, seq %d)", got, convB, receiptB.Seq)
	}
	if !strings.Contains(pc.StrandedOps[0].Detail, "stranded pins post-crash") {
		t.Errorf("row[0] detail = %q, want the conflict's reason copy", pc.StrandedOps[0].Detail)
	}

	// Resolving A (routed by A's own conversation, the FIX-F route) drops
	// count AND rows to B's alone.
	res := rig.call(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convA,
		Layer:                "pins",
		ReceiptSeq:           receiptA.Seq,
		StrandedConversation: convA,
	})
	if !res.Applied || res.StrandedMemoryOps != 1 {
		t.Fatalf("resolve A = %+v, want applied with count 1", res)
	}
	pc = rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.StrandedMemoryOps != 1 || len(pc.StrandedOps) != 1 {
		t.Fatalf("post-resolve pending_counts = (%d, %+v), want count 1 with lane B's row", pc.StrandedMemoryOps, pc.StrandedOps)
	}
	if got := pc.StrandedOps[0]; got.StrandedConversation != convB || got.ReceiptSeq != receiptB.Seq {
		t.Errorf("surviving row = %+v, want lane B only", got)
	}
}

// TestSweepConsumedBatchRepairs (round-4 FIX 1 drill): a consumed batch
// whose marker outlived its file writes (failed-write crash) is STILL the
// only replayable intent for those files — and the pre-fix sweep returned
// on batch.consumed without evaluating anything, leaving files lagging
// until the next manual apply or daemon restart. The sweep's consumed
// branch now routes the marker through the replay engine's
// newest-per-layer discipline: one sweep repairs and journals the
// recover row immediately. No new apply runs (no second memory_apply
// marker), no restart (replayMemoryJournal never called here).
func TestSweepConsumedBatchRepairs(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)
	ctx := context.Background()

	const X = "- base rule — cites: main-epoch-1; reaffirmed: 1\n"
	Y := X + "- sweeps repair consumed markers — cites: main-epoch-1; reaffirmed: 1\n"
	writeProjFile(t, root, ".odo/memory.md", X)

	// Propose + consume (marker journaled), then the write "fails": disk
	// stays at the receipt's before.
	seedProposeBatch(t, rig, convID, 1, []MemoryProposal{
		{Target: "memory.md", Rule: "sweeps repair consumed markers", Evidence: "main-epoch-1"},
	}, nil)
	seedApplyReceipt(t, rig, convID, 1, nil, nil,
		[]MemoryAccept{{Target: "memory.md", Index: 0}},
		applyRecovery{Memory: memLayer(X, Y)})

	c, err := rig.store.GetConversation(ctx, convID)
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	w, err := rig.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		t.Fatalf("workstream: %v", err)
	}

	// The sweep fires with a consumed batch — the engine pass repairs.
	rig.server.sweepPendingBatch(ctx, c, w, nil)
	if got := readFileStr(t, filepath.Join(root, ".odo", "memory.md")); got != Y {
		t.Errorf("memory.md after sweep = %q, want %q (the consumed marker's own restore)", got, Y)
	}
	if got := healRowCounts(t, rig); got["recover"] != 1 || got["heal_conflict"] != 0 || got["heal_merged"] != 0 {
		t.Errorf("heal rows after sweep = %v, want exactly one recover row", got)
	}

	// No new apply ran: the batch stays consumed-with-one-marker, and the
	// sweep decided nothing (no fresh gate, no second consumption).
	events, err := rig.store.ListEvents(ctx, convID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if got := len(payloadsByAction(t, events, "memory_apply")); got != 1 {
		t.Errorf("memory_apply markers = %d, want 1 (the sweep repaired, never re-applied)", got)
	}
	if got := len(payloadsByAction(t, events, "memory_gate")); got != 0 {
		t.Errorf("memory_gate rows = %d, want 0 (a consumed batch is never re-decided)", got)
	}

	// A second sweep is a no-op: the restore landed the receipt (disk ==
	// after), the landed branch journals nothing.
	rig.server.sweepPendingBatch(ctx, c, w, nil)
	if got := healRowCounts(t, rig); got["recover"] != 1 || got["heal_conflict"] != 0 || got["heal_resolved"] != 0 {
		t.Errorf("heal rows after second sweep = %v, want unchanged (idempotent convergence)", got)
	}
}

// TestReplayLaneLocalSkipsRetirement (round-4 FIX 2 drill): conflict
// retirement is a PROJECT-WIDE authority. Lane A (older) lands its
// receipt while lane B's NEWER conflict is open: the runtime lane-local
// engine pass must leave B's conflict untouched (pre-fix it closed B as
// superseded — an older lane's landed receipt outranking a newer lane's
// open conflict). The boot full-fold pick retires it afterwards, when a
// receipt newer than B's has legitimately landed.
func TestReplayLaneLocalSkipsRetirement(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convA := bootstrapConv(t, rig, root)

	const foreign = "- human pins edit\n"
	writeProjFile(t, root, ".odo/pins.md", foreign)

	// Lane A's receipt (OLDER global event id), then lane B's crashed
	// receipt (NEWER). Boot 1 sees lane B as the layer's authority:
	// foreign projection → B's conflict journals, count 1.
	landedA := "- human pins edit\n- lane A base pin\n"
	seedPinReceipt(t, rig, convA, foreign, landedA)
	ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "lane-b-newer"})
	bootB := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
	convB := bootB.Conversation.ID
	receiptB := seedPinReceipt(t, rig, convB, "", "- stranded newer pin B\n")
	rig.server.replayMemoryJournal(context.Background())
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 1 {
		t.Fatalf("count after boot 1 = %d, want 1 (lane B's open conflict)", got.StrandedMemoryOps)
	}

	// Lane A's writer later completes: disk now holds A's after. The
	// runtime lane-local pass (the pin/apply callers' shape) evaluates
	// lane A's newest receipt LANDED — and must NOT retire lane B's
	// newer open conflict.
	writeProjFile(t, root, ".odo/pins.md", landedA)
	eventsA, err := rig.store.ListEvents(context.Background(), convA, 0)
	if err != nil {
		t.Fatalf("list lane A events: %v", err)
	}
	rig.server.memMu.Lock()
	rig.server.replayLaneMemReceipts(context.Background(), convA, eventsA, replayPin)
	rig.server.memMu.Unlock()
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 1 {
		t.Fatalf("count after lane-local landed pass = %d, want 1 — "+
			"lane A's receipt has no authority over lane B's NEWER open conflict (FIX 2)", got.StrandedMemoryOps)
	}
	if got := len(projectHeals(t, rig, "heal_resolved")); got != 0 {
		t.Fatalf("heal_resolved rows = %d, want 0 after the lane-local pass (retirement is project-wide only)", got)
	}

	// The project-wide newest retires it: a receipt NEWER than B's lands
	// legitimately, and the boot full-fold closes B's open conflict as
	// superseded (the round-3 FIX D path, now sole owner of retirement).
	landedC := landedA + "- landed pin v2 (legitimate newest)\n"
	writeProjFile(t, root, ".odo/pins.md", landedC)
	seedPinReceipt(t, rig, convA, landedA, landedC)
	rig.server.replayMemoryJournal(context.Background())
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 0 {
		t.Fatalf("count after boot full-fold = %d, want 0 (the landed newest retired B's conflict)", got.StrandedMemoryOps)
	}
	resolved := projectHeals(t, rig, "heal_resolved")
	if len(resolved) != 1 || resolved[0]["actor"] != "superseded" || resolved[0]["dismissed"] != true {
		t.Fatalf("heal_resolved rows = %+v, want one superseded dismissal for lane B", resolved)
	}
	if resolved[0]["receipt_seq"] == nil || int(resolved[0]["receipt_seq"].(float64)) != receiptB.Seq ||
		resolved[0]["stranded_conversation"] == nil || int64(resolved[0]["stranded_conversation"].(float64)) != convB {
		t.Errorf("retirement key = (%v, seq %v), want (conv %d, seq %d)",
			resolved[0]["stranded_conversation"], resolved[0]["receipt_seq"], convB, receiptB.Seq)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "pins.md")); got != landedC {
		t.Errorf("pins.md = %q, want the landed newest untouched %q", got, landedC)
	}
}

// TestMemoryReplayLegacyRetiresConflict (round-4 FIX 3 drill): a legacy
// pin receipt (file-first shape, terminal landed boundary) as the layer's
// newest must retire the layer's older open conflicts on the next
// evaluation — before the fix the legacy branch returned replayNone
// BEFORE the retirement ran, so a legacy terminal boundary never closed
// conflicts its own landing superseded.
func TestMemoryReplayLegacyRetiresConflict(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	convID := bootstrapConv(t, rig, root)

	const foreign = "- human pins edit\n"
	writeProjFile(t, root, ".odo/pins.md", foreign)

	// Boot 1: the crashed journal-first receipt strands on the foreign
	// projection (open conflict, count 1).
	stranded := seedPinReceipt(t, rig, convID, "", "- stranded pin v1\n")
	rig.server.replayMemoryJournal(context.Background())
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 1 {
		t.Fatalf("count after boot 1 = %d, want 1", got.StrandedMemoryOps)
	}

	// A PRE-recovery pin receipt lands file-first and journals its legacy
	// row — the layer's newest attestation of record. On the NEXT
	// evaluation the legacy boundary retires the older open conflict.
	seedLegacyPinReceipt(t, rig, convID)
	rig.server.replayMemoryJournal(context.Background())
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 0 {
		t.Fatalf("count after legacy-boundary evaluation = %d, want 0 (FIX 3)", got.StrandedMemoryOps)
	}
	resolved := projectHeals(t, rig, "heal_resolved")
	if len(resolved) != 1 || resolved[0]["actor"] != "superseded" || resolved[0]["dismissed"] != true {
		t.Fatalf("heal_resolved rows = %+v, want one superseded dismissal from the legacy boundary", resolved)
	}
	if resolved[0]["receipt_seq"] == nil || int(resolved[0]["receipt_seq"].(float64)) != stranded.Seq {
		t.Errorf("retirement receipt_seq = %v, want %d (the stranded receipt)", resolved[0]["receipt_seq"], stranded.Seq)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "pins.md")); got != foreign {
		t.Errorf("pins.md = %q, want %q (a boundary retires, never writes)", got, foreign)
	}
	// Idempotent: a third boot journals nothing new.
	rig.server.replayMemoryJournal(context.Background())
	if got := healRowCounts(t, rig); got["heal_conflict"] != 1 || got["heal_resolved"] != 1 || got["recover"] != 0 {
		t.Errorf("third boot rows = %v, want the conflict+retirement pair only", got)
	}
}

// TestResolveHealConflictArchivedLaneActionable (round-4 FIX 4 drill):
// the stranded rows the FIX F pending_counts payload surfaces are
// ACTIONABLE on exactly the lanes they were built for — a conflict
// stranded on an ARCHIVED (soft-deleted) workstream Resolves and
// Dismisses through the IPC handler from the archived lane's own
// conversation, and the project-wide count drops per closure. Nothing
// behind checkConversation may refuse a soft-deleted workstream's
// conversation (GetWorkstream carries no status filter; delete is a
// status flip — the same lifecycle the fold guarantee rides).
func TestResolveHealConflictArchivedLaneActionable(t *testing.T) {
	root := initRepo(t)
	t.Setenv("HOME", t.TempDir())
	rig := startRig(t, root)
	defer rig.stop(t)
	bootstrapConv(t, rig, root) // main lane: survives the drill

	writeProjFile(t, root, ".odo/pins.md", "- human pins edit\n")

	// The doomed lane strands TWO whole-file conflicts: a pins receipt
	// and a skill-file receipt (both foreign projections), then the
	// workstream is soft-deleted before any replay pass.
	ws := rig.call(t, Request{Cmd: CmdCreateWorkstream, ProjectRoot: root, Name: "doomed-resolve-lane"})
	bootD := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root, WorkstreamID: ws.Workstream.ID})
	convD := bootD.Conversation.ID
	pinReceipt := seedPinReceipt(t, rig, convD, "", "- stranded pin from archived lane\n")
	skillBefore := "- old skill body\n"
	skillAfter := "- old skill body\n- stranded skill step\n"
	writeProjFile(t, root, ".odo/skills/probe.md", "- foreign skill edit\n")
	skillReceipt := seedApplyReceipt(t, rig, convD, 1, nil, nil, nil,
		applyRecovery{Skills: []applyRecoverySkill{{
			Name:      "probe.md",
			BeforeSHA: sha16([]byte(skillBefore)),
			AfterSHA:  sha16([]byte(skillAfter)),
			Body:      skillAfter,
		}}})
	if err := rig.store.DeleteWorkstream(context.Background(), ws.Workstream.ID); err != nil {
		t.Fatalf("soft-delete doomed lane: %v", err)
	}

	rig.server.replayMemoryJournal(context.Background())
	pc := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.StrandedMemoryOps != 2 || len(pc.StrandedOps) != 2 {
		t.Fatalf("stranded state = (%d, %+v), want 2 rows on the archived lane", pc.StrandedMemoryOps, pc.StrandedOps)
	}
	for _, op := range pc.StrandedOps {
		if op.StrandedConversation != convD {
			t.Errorf("stranded row = %+v, want attribution to the archived lane's conversation %d", op, convD)
		}
	}

	// Resolve the pins conflict FROM the archived conversation — the
	// MemoryPanel's route for surfaced rows.
	res := rig.call(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convD,
		Layer:                "pins",
		ReceiptSeq:           pinReceipt.Seq,
		StrandedConversation: convD,
	})
	if !res.Applied || res.StrandedMemoryOps != 1 {
		t.Fatalf("resolve pins on archived lane = %+v, want applied with count 1", res)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "pins.md")); got != "- stranded pin from archived lane\n" {
		t.Errorf("pins.md after resolve = %q, want the archived lane's stranded body restored", got)
	}
	pc = rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root})
	if pc.StrandedMemoryOps != 1 || len(pc.StrandedOps) != 1 || pc.StrandedOps[0].Layer != "skill:probe.md" {
		t.Fatalf("stranded rows after pins resolve = (%d, %+v), want the skill row only", pc.StrandedMemoryOps, pc.StrandedOps)
	}

	// Dismiss the skill conflict the same way — the projection untouched.
	res = rig.call(t, Request{
		Cmd:                  CmdResolveHealConflict,
		ConversationID:       convD,
		Layer:                "skill:probe.md",
		ReceiptSeq:           skillReceipt.Seq,
		Dismissed:            true,
		StrandedConversation: convD,
	})
	if !res.Applied || res.StrandedMemoryOps != 0 {
		t.Fatalf("dismiss skill on archived lane = %+v, want applied with count 0", res)
	}
	if got := readFileStr(t, filepath.Join(root, ".odo", "skills", "probe.md")); got != "- foreign skill edit\n" {
		t.Errorf("probe.md after dismiss = %q, want the foreign projection untouched", got)
	}
	if got := rig.call(t, Request{Cmd: CmdPendingCounts, ProjectRoot: root}); got.StrandedMemoryOps != 0 {
		t.Errorf("count after both closures = %d, want 0", got.StrandedMemoryOps)
	}

	resolved := projectHeals(t, rig, "heal_resolved")
	if len(resolved) != 2 {
		t.Fatalf("heal_resolved rows = %d, want 2 (resolve + dismiss)", len(resolved))
	}
	var sawResolve, sawDismiss bool
	for _, r := range resolved {
		if r["actor"] != "human" || r["stranded_conversation"] == nil || int64(r["stranded_conversation"].(float64)) != convD {
			t.Errorf("closure row = %+v, want actor human on the archived lane %d", r, convD)
		}
		if r["dismissed"] == true {
			sawDismiss = true
		} else {
			sawResolve = true
		}
	}
	if !sawResolve || !sawDismiss {
		t.Errorf("closures = %+v, want one plain resolve and one dismissal", resolved)
	}
}
