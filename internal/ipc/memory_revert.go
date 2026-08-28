package ipc

// D4 (2026-08-28, design-lock W2): human-only epoch rollback —
// `odo memory revert <epoch>` (CLI in cmd_memory.go). The marker-first
// apply protocol already journals every memory/archive write's pre- and
// post-state (the memory_apply recovery block, memory_replay.go): the
// revert locates the epoch's receipt, verifies the layer still holds the
// receipt's POST-state (nothing landed on top and no hand edit moved it),
// reconstructs the pre-image from the lane's receipt chain (never from a
// re-derivation — the journal IS the source of truth), writes it back,
// and journals the rollback receipt:
//
//	memory_update{layer:"apply", cause:"revert", epoch, actor:"human",
//	               before_sha16, after_sha16, apply_seq,
//	               memory_before_sha16?/memory_after_sha16?,
//	               archive_before_sha16?/archive_after_sha16?}
//
// before_sha16/after_sha16 name the memory layer when it reverted (the
// primary surface), else the archive; the additive per-layer keys cover
// the other (ADR-0002-immune). The replay fold retires the epoch's
// receipts on this row (the reverted pre-image bytes would otherwise
// read as a mid-write crash — see memory_replay.go's D4 note).
//
// Fail-closed on every ambiguity: an epoch with apply receipts on more
// than one lane, a layer whose live bytes match neither receipt hash, a
// pre-image the receipt chain cannot reconstruct (a human hand-edit
// seeded a state the journal never recorded), an apply that touched
// user.md or skill files (whole-file layers outside this wave's scope —
// revert them by hand), or a second revert of the same epoch all REFUSE
// with the reason named. Revert-of-revert is exactly a re-apply, which
// the normal path (apply_memory) already owns. A crash between the two
// file writes is recoverable by re-running: a layer already at its
// pre-image is skipped, a layer at the post-state is written, the row
// journals once both layers hold the pre-image.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

// MemoryRevertReport is the engine's outcome (the CLI prints it).
type MemoryRevertReport struct {
	Epoch        int
	Conversation int64    // the receipt's owning lane (the revert row journals here)
	ApplySeq     int      // the reverted memory_apply row's per-lane seq
	RowSeq       int      // the journaled revert row's seq
	Layers       []string // reverted layers, sorted ("archive", "memory")
	AlreadyThere bool     // files already held the pre-image (crash-before-journal retry)
}

// revertChainBlock is one layer receipt in a lane's memory_apply chain:
// the before/after digests plus the post-state body (whole file for
// memory, append chunk for archive).
type revertChainBlock struct {
	seq           int
	before, after string
	body          string
}

// revertMatch is the located epoch receipt: its lane, journal row, and
// recovery block.
type revertMatch struct {
	convID   int64
	ev       store.Event
	recovery applyRecovery
}

// RevertMemoryEpoch restores the pre-image of one epoch's memory apply
// (memory.md + the archive's appended chunk; this wave's scope). st is a
// READ-WRITE journal open (the CLI's own, coexisting with a live daemon
// via WAL + busy_timeout — the cmd_retract.go precedent); project binds
// the store to the project root whose files revert.
func RevertMemoryEpoch(ctx context.Context, st *store.Store, project store.Project, epoch int) (MemoryRevertReport, error) {
	report := MemoryRevertReport{Epoch: epoch}

	// Locate the epoch's apply receipt project-wide; epochs are
	// lane-local counters, so exactly ONE lane may own it — two is an
	// ambiguity no amount of inspection resolves (fail-closed).
	matches, laneEvents, err := locateRevertMatch(ctx, st, project, epoch)
	if err != nil {
		return report, err
	}
	m := matches[0]
	report.Conversation = m.convID
	report.ApplySeq = m.ev.Seq

	// Second revert of the same epoch is refused: the journaled revert
	// row IS the epoch's close-out; re-landing belongs to the normal
	// apply path.
	for _, ev := range laneEvents {
		if isMemRevertRow(ev.Payload) {
			var p struct {
				Epoch int `json:"epoch"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil && p.Epoch == epoch {
				return report, fmt.Errorf("epoch %d already reverted (revert row seq %d) — re-apply through apply_memory instead of reverting a revert", epoch, ev.Seq)
			}
		}
	}

	// Whole-file layers outside this wave's scope refuse the whole call:
	// half-reverting a batch would leave the journal claiming a rollback
	// the files never took.
	if m.recovery.User != nil {
		return report, fmt.Errorf("epoch %d apply touched user.md — the global layer reverts by hand only (human-written, forever)", epoch)
	}
	if len(m.recovery.Skills) > 0 {
		names := make([]string, 0, len(m.recovery.Skills))
		for _, sk := range m.recovery.Skills {
			names = append(names, sk.Name)
		}
		return report, fmt.Errorf("epoch %d apply wrote skill file(s) %s — skill layers revert by hand only (delete the file)", epoch, strings.Join(names, ", "))
	}
	if m.recovery.Memory == nil && m.recovery.Archive == nil {
		return report, fmt.Errorf("epoch %d apply receipt carries no memory/archive recovery block — nothing to revert", epoch)
	}

	// Per layer: verify the live bytes hold the receipt's post-state
	// (before-sha verified at write time by construction), rebuild the
	// pre-image from the lane's receipt chain.
	root := project.RootPath
	plans := map[string]revertChainBlock{} // layer → pre-image plan (seq = apply seq)
	if m.recovery.Memory != nil {
		tgt := m.recovery.Memory
		pre, err := memoryPreimage(laneEvents, m.ev.Seq, tgt)
		if err != nil {
			return report, fmt.Errorf("epoch %d memory layer: %w", epoch, err)
		}
		plans["memory"] = revertChainBlock{seq: m.ev.Seq, before: tgt.BeforeSHA, after: tgt.AfterSHA, body: pre}
	}
	if m.recovery.Archive != nil {
		tgt := m.recovery.Archive
		pre, err := archivePreimage(laneEvents, m.ev.Seq, tgt)
		if err != nil {
			return report, fmt.Errorf("epoch %d archive layer: %w", epoch, err)
		}
		plans["archive"] = revertChainBlock{seq: m.ev.Seq, before: tgt.BeforeSHA, after: tgt.AfterSHA, body: pre}
	}

	for _, layer := range orderedLayers(plans) {
		plan := plans[layer]
		cur := revertReadLayer(root, layer)
		curSHA := sha16([]byte(cur))
		switch {
		case curSHA == plan.after:
			// Post-state on disk, journal-verified: the revert writes the
			// pre-image.
		case curSHA == plan.before:
			// Already at the pre-image: idempotent completion of a
			// crash-before-journal revert (or of another layer's partial
			// write). Mark and skip the write.
			report.AlreadyThere = true
			continue
		default:
			return report, fmt.Errorf("epoch %d %s layer moved since the apply (journal after %s, disk %s) — refusing; resolve by hand",
				epoch, layer, plan.after, curSHA)
		}
	}

	// Writes: archive first, memory last (the apply's own order, so a
	// crash window leaves the previous memory.md intact).
	for _, layer := range orderedLayers(plans) {
		plan := plans[layer]
		if cur := sha16([]byte(revertReadLayer(root, layer))); cur == plan.before {
			continue // idempotent skip recorded above
		}
		if err := revertWriteLayer(root, layer, plan.body); err != nil {
			return report, fmt.Errorf("epoch %d %s layer: %w", epoch, layer, err)
		}
		if got := sha16([]byte(revertReadLayer(root, layer))); got != plan.before {
			return report, fmt.Errorf("epoch %d %s layer: post-write digest %s != pre-image %s — file moved under the revert", epoch, layer, got, plan.before)
		}
	}

	// Journal the rollback receipt on the receipt's lane (epoch identity
	// lives there). after_sha16 is the restored pre-image digest.
	payload := map[string]interface{}{
		"layer":     "apply",
		"cause":     "revert",
		"epoch":     epoch,
		"actor":     "human",
		"apply_seq": m.ev.Seq,
	}
	if p, ok := plans["memory"]; ok {
		payload["before_sha16"] = p.after
		payload["after_sha16"] = p.before
		payload["memory_before_sha16"] = p.after
		payload["memory_after_sha16"] = p.before
	}
	if p, ok := plans["archive"]; ok {
		if _, hasMem := plans["memory"]; !hasMem {
			payload["before_sha16"] = p.after
			payload["after_sha16"] = p.before
		}
		payload["archive_before_sha16"] = p.after
		payload["archive_after_sha16"] = p.before
	}
	ev, err := st.AppendEvent(ctx, m.convID, store.EventMemoryUpdate, mustJSON(payload))
	if err != nil {
		return report, fmt.Errorf("epoch %d: journal revert row: %w (files already reverted — re-running completes the journal)", epoch, err)
	}
	report.RowSeq = ev.Seq
	for layer := range plans {
		report.Layers = append(report.Layers, layer)
	}
	sort.Strings(report.Layers)
	return report, nil
}

// locateRevertMatch scans every active workstream conversation for the
// epoch's memory_apply receipt. Exactly one must exist.
func locateRevertMatch(ctx context.Context, st *store.Store, project store.Project, epoch int) ([]revertMatch, []store.Event, error) {
	streams, err := st.ListWorkstreams(ctx, project.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("list workstreams: %w", err)
	}
	var matches []revertMatch
	var laneEvents []store.Event
	for _, w := range streams {
		c, err := st.GetActiveConversation(ctx, w.ID)
		if err != nil {
			continue // empty workstream has no lane to scan
		}
		events, err := st.ListEvents(ctx, c.ID, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("list events (conversation %d): %w", c.ID, err)
		}
		for i := range events {
			if events[i].Type != store.EventReviewAction {
				continue
			}
			var p struct {
				Action   string        `json:"action"`
				Epoch    int           `json:"epoch"`
				Recovery applyRecovery `json:"recovery"`
			}
			if json.Unmarshal(events[i].Payload, &p) != nil || p.Action != "memory_apply" || p.Epoch != epoch {
				continue
			}
			matches = append(matches, revertMatch{convID: c.ID, ev: events[i], recovery: p.Recovery})
			laneEvents = events
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil, fmt.Errorf("no memory_apply receipt for epoch %d", epoch)
	case 1:
		return matches, laneEvents, nil
	default:
		lanes := make([]string, 0, len(matches))
		for _, mm := range matches {
			lanes = append(lanes, fmt.Sprintf("conversation %d (seq %d)", mm.convID, mm.ev.Seq))
		}
		return nil, nil, fmt.Errorf("epoch %d apply receipt is ambiguous — %d lanes claim it: %s (epochs are lane-local); refusing",
			epoch, len(matches), strings.Join(lanes, ", "))
	}
}

// revertApplyChain folds one lane's journal into the receipt chain of a
// single layer ("memory" | "archive"): every memory_apply recovery block
// in seq order.
func revertApplyChain(events []store.Event, layer string) []revertChainBlock {
	var chain []revertChainBlock
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action   string        `json:"action"`
			Recovery applyRecovery `json:"recovery"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.Action != "memory_apply" {
			continue
		}
		var blk *applyRecoveryLayer
		switch layer {
		case "memory":
			blk = p.Recovery.Memory
		case "archive":
			blk = p.Recovery.Archive
		}
		if blk == nil {
			continue
		}
		chain = append(chain, revertChainBlock{seq: ev.Seq, before: blk.BeforeSHA, after: blk.AfterSHA, body: blk.Body})
	}
	return chain
}

// sha16Empty is the digest of the absent/empty projection a first apply
// records as its before-sha.
var sha16Empty = sha16(nil)

// memoryPreimage reconstructs the memory.md pre-image for the target
// receipt: the newest earlier receipt whose post-state IS the target's
// recorded before (its body is the bytes), or the empty projection when
// the target is the layer's first receipt and attests an empty before.
// Anything else is unreconstructable — a hand-seeded state the journal
// never recorded — and refuses.
func memoryPreimage(events []store.Event, applySeq int, tgt *applyRecoveryLayer) (string, error) {
	if tgt.BeforeSHA == sha16Empty {
		return "", nil
	}
	pre := ""
	found := false
	for _, blk := range revertApplyChain(events, "memory") {
		if blk.seq >= applySeq {
			break
		}
		if blk.after == tgt.BeforeSHA {
			if sha16([]byte(blk.body)) != blk.after {
				return "", fmt.Errorf("receipt seq %d body digest mismatch — refusing to trust the journal block", blk.seq)
			}
			pre, found = blk.body, true
		}
	}
	if !found {
		return "", fmt.Errorf("pre-image unreconstructable (before-sha %s matches no earlier receipt) — a hand edit seeded a state the journal never recorded", tgt.BeforeSHA)
	}
	return pre, nil
}

// archivePreimage reconstructs the archive pre-image: the archive's
// receipts are append chunks, so the pre-image is the concatenation of
// every chunk older than the target — valid only under a COMPLETE chain
// (first receipt attests the empty projection and consecutive receipts
// chain digests). Any gap or hand-seeded prefix refuses.
func archivePreimage(events []store.Event, applySeq int, tgt *applyRecoveryLayer) (string, error) {
	var older []revertChainBlock
	for _, blk := range revertApplyChain(events, "archive") {
		if blk.seq >= applySeq {
			break
		}
		older = append(older, blk)
	}
	if len(older) == 0 {
		if tgt.BeforeSHA != sha16Empty {
			return "", fmt.Errorf("pre-image unreconstructable (before-sha %s with no earlier archive receipts)", tgt.BeforeSHA)
		}
		return "", nil
	}
	if older[0].before != sha16Empty {
		return "", fmt.Errorf("pre-image unreconstructable (archive predates the journaled chain: first receipt before-sha %s)", older[0].before)
	}
	for i := 1; i < len(older); i++ {
		if older[i].before != older[i-1].after {
			return "", fmt.Errorf("pre-image unreconstructable (archive chain broken between receipt seqs %d and %d)", older[i-1].seq, older[i].seq)
		}
	}
	if older[len(older)-1].after != tgt.BeforeSHA {
		return "", fmt.Errorf("pre-image unreconstructable (archive chain tip %s != target before %s)", older[len(older)-1].after, tgt.BeforeSHA)
	}
	var b strings.Builder
	for _, blk := range older {
		b.WriteString(blk.body)
	}
	pre := b.String()
	if sha16([]byte(pre)) != tgt.BeforeSHA {
		return "", fmt.Errorf("reconstructed pre-image digest mismatch — consulting a human is cheaper than trusting a broken chain")
	}
	return pre, nil
}

// revertReadLayer reads the layer's live bytes on the apply path's own
// digest basis (FULL uncapped — the injection cap must never gate a
// rollback decision).
func revertReadLayer(projectRoot, layer string) string {
	switch layer {
	case "memory":
		return readFileFull(filepath.Join(projectRoot, ".odo", memoryFileName))
	case "archive":
		return readArchive(projectRoot)
	default:
		return ""
	}
}

// revertWriteLayer writes the pre-image with the apply path's own
// containment + atomicity.
func revertWriteLayer(projectRoot, layer, body string) error {
	switch layer {
	case "memory":
		return writeFileWithin(projectRoot, filepath.Join(projectRoot, ".odo", memoryFileName), body, 0o644)
	case "archive":
		return writeFileWithin(projectRoot, filepath.Join(projectRoot, ".odo", archiveFileName), body, 0o644)
	default:
		return fmt.Errorf("unknown revert layer %q", layer)
	}
}
