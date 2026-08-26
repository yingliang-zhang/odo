package ipc

// Boot-time, project-wide replay of stranded memory/pins intents
// (2026-08-26 memory-replay doctrine). The journal already IS the durable
// outbox — this file is its REPLAYER, replacing the pre-replayer per-lane
// heals as the authoritative crash recovery.
//
// The defect this closes: the marker-first apply protocol journals the
// consumption marker (with per-layer before/after hashes + post-state
// body) BEFORE the file writes; a crash in between leaves files lagging
// the journal. The old heal folded ONE conversation's events and returned
// silently when the file hash matched neither the receipt's after (landed)
// nor before (crashed): lane A journals receipt (X→Y) and crashes before
// the write, lane B lands its own receipt (X→Z), and A's rule stays
// journaled-consumed but absent from the projection forever, with zero
// trace. Under this doctrine the same state is EXPLICIT: A's newest
// receipt is foreign → its add-style entries entry-merge (heal_merged) or
// it becomes a heal_conflict with the stranded body embedded and a
// human-facing count.
//
// THE LOCKED ORDERING RULE: fold the full project journal per layer in
// global journal order and consider ONLY that layer's NEWEST receipt —
// the before/after-sha predicate cannot distinguish "crashed before
// write" from "landed then legitimately superseded": a clean A(X→Y),
// B(Y→Z) chain leaves disk at Z, and scanning older receipts would
// re-merge A over Z, resurrecting replaced content on every boot. A
// supersede/retraction through the apply path IS itself the newest
// receipt, so it stays authoritative; a foreign disk with no covering
// receipt is exactly what heal_conflict is for. Idempotence across boots
// follows from the landed branch (disk == after → no-op).
//
// Merge-eligibility (the add-style test):
//   - memory.md: accepted rules resolve against the crashed batch's
//     propose row, carry no reaffirm edits and no contradicts retractions,
//     and keep the merged file under memoryCap. Missing entries append in
//     the apply's own line format; already-present entries (normalized
//     compare) skip — a fully-present receipt is semantically landed and
//     journals nothing (double-boot produces zero rows).
//   - archive: the recovery body IS the append chunk; absent-on-disk →
//     pure append, present → semantically landed. Never conflicts.
//   - user.md, pins.md, skill files: whole-file layers with no structured
//     per-entry intent journaled (the pin handler records one verbatim
//     block; inventing an entry parser is worse than asking) → conflict.
//
// Coverage boundary (round-3 panel, FIX B): the fold spans every lane
// whose journal ROWS SURVIVE — LEFT JOINs from the events table keep a
// receipt on an archived/soft-deleted lane folding (soft delete is a
// status flip, rotation never retires conversation rows). A lane whose
// rows were themselves DESTROYED is unrecoverable by construction — a
// hard cascade-delete of conversations/workstreams, or `odo journal
// rotate` moving the whole SQLite file: the journal is the outbox and a
// destroyed journal row has no replay source. No such path exists today;
// this states the boundary rather than overclaiming "every conversation".
//
// Journal rows (the old cause:"recover" shape is kept for plain replay
// and stays invisible to this fold — candidate receipts are only
// memory_apply recovery blocks and pins pin receipts, plus the legacy
// pin tombstone below):
//   - replay: memory_update{layer, cause:"recover", detail}
//   - merge:  memory_update{layer, cause:"heal_merged", receipt_seq,
//             stranded_conversation, entries_added, sha16_after, detail}
//   - conflict: memory_update{layer, cause:"heal_conflict", detail,
//             stranded_receipt_seq, stranded_conversation,
//             stranded_body_sha16, stranded_body}
//   - resolution: memory_update{layer, cause:"heal_resolved",
//             receipt_seq, stranded_conversation, actor, dismissed?}
//             actor "human" = the resolve/dismiss IPC; "superseded" =
//             the lifecycle retirement — an evaluation landed the layer's
//             newer receipt (FIX D) or resolve found the file moved since
//             the conflict journaled (FIX E), closing no-longer-
//             conscientiously-resolvable conflicts as dismissed.
//
// Heal rows ride the workstream's ACTIVE conversation (the GUI's Memory
// tab folds its own conversation for them); the receipt's identity lives
// in the row fields, and ledger pairing folds by content key
// (stranded_conversation, layer, receipt seq) — never by carrier lane.
//
// Concurrency: the boot replay runs once at NewServer before serving
// (single-threaded by construction). The same predicate serves the live
// apply-retry convergence and the pin RMW basis under memMu — one engine,
// never a second scan. Runtime resolve takes memMu. No lock-order changes.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

// Receipt-kind mask for replayLaneMemReceipts: apply paths replay memory
// apply receipts, the pin handler replays pins receipts, the boot replayer
// takes both. The predicate and the rows are identical regardless.
const (
	replayApply = 1 << iota // memory_apply recovery-block receipts
	replayPin               // pins.md pin receipts
	replayAll   = replayApply | replayPin
)

// replayJournalReadPage is the boot replayer's journal page size (K3
// hygiene, 2026-08-26): large enough that a healthy boot is a handful of
// round trips, small enough that no project's journal ever materializes
// in memory — the pre-paging one-shot listing copied every payload of a
// long-lived project's events into one []Event per boot.
const replayJournalReadPage = 512

// memReceipt is one replay candidate: a memory_apply layer block
// (memory/archive/user/skill) or a pins receipt, judged per layer by the
// newest-only doctrine.
type memReceipt struct {
	layer   string // "memory" | "archive" | "user" | "pins" | "skill:<base>"
	convID  int64  // the receipt's owning conversation (stranded side)
	eventID int64  // global event id — the total-order tiebreak across lanes
	seq     int    // the receipt's per-conversation journal seq
	epoch   int    // apply epoch (0 for pins)
	before  string
	after   string
	body    string
	// legacy marks a pre-recovery pin receipt (journaled AFTER its
	// file-first write, so no after_sha/body): nothing is recoverable,
	// but the receipt IS the layer's attestation of record — a terminal
	// landed boundary. As the newest pins row it masks every older
	// receipt without itself ever becoming a replay candidate (the
	// pre-replayer pins heal's scan-terminating return, fold form).
	legacy bool
	// apply-layer merge intent (nil propose = unresolvable at merge time):
	// the marker's decision refs plus the crashed batch's propose row,
	// attached at parse time (the propose always precedes its marker).
	accepted []MemoryAccept
	propose  *proposePayload
	// entries is the memory layer's verbatim per-rule add record as the
	// live apply journaled it (nil on legacy receipts — the merge then
	// synthesizes the line, round-3 FIX C).
	entries []applyRecoveryEntry
}

// healKey pairs a heal_conflict with its heal_resolved by CONTENT (the
// keys ride every row; the carrier conversation is a display concern).
type healKey struct {
	conv  int64
	layer string
	seq   int
}

// healRow is a parsed heal_ledger entry (conflict rows also carry the
// embedded stranded body plus the disk digest recorded at journaling —
// the resolve freshness guard's basis, round-3 FIX E).
type healRow struct {
	cause   string
	key     healKey
	body    string
	sha     string
	diskSHA string // sha16 of the layer's live bytes when the conflict journaled ("" on legacy rows)
	detail  string // the conflict's human-readable reason (pending_counts row copy, FIX F)
}

// evalScope marks which fold context picked the receipt being evaluated
// (round-4 FIX 2): the boot replayer's PROJECT-WIDE pick is the only
// retirement authority — its per-layer receipt beat every lane's in
// global journal order, so its landed verdict honestly closes the layer's
// older open conflicts. A LANE-LOCAL pick (the apply-retry convergence,
// the pin RMW basis, the sweep's consumed-marker repair) sees one
// conversation's history only: its newest receipt is not necessarily the
// layer's project-wide newest, and retiring from it would close a NEWER
// conflict stranded on another lane — an older lane's landed receipt has
// no authority over a newer lane's open conflict.
type evalScope int

const (
	evalLaneLocal   evalScope = iota // one conversation's fold — repair only, never retire
	evalProjectWide                  // boot full-fold pick — retirement authority
)

// replayOutcome is one receipt's verdict.
type replayOutcome int

const (
	replayNone       replayOutcome = iota // landed, semantically present, or unactionable
	replayRestored                        // whole-file/chunk replay of a mid-write crash
	replayMerged                          // entry-merge onto a foreign projection
	replayConflicted                      // foreign + unmergeable → heal_conflict
)

// memReceiptEval is one receipt's outcome plus observability.
type memReceiptEval struct {
	outcome      replayOutcome
	entriesAdded int
	reason       string // why a foreign receipt conflicted
}

// laneMemReceiptFold is the streaming per-lane receipt fold: feed the
// lane's events in journal order, one at a time, and cand accumulates the
// newest-per-layer receipts (a later row of the same layer overwrites the
// earlier candidate). The boot replayer keeps ONE fold per lane alive
// across journal pages (K3 hygiene, 2026-08-26): propose→apply pairing
// and the newest picks are order-dependent state, not full-list
// operations, so page boundaries are invisible to the result and the
// full project journal never materializes. Bounded retention (the prune
// in feed): proposeByEpoch sheds proposes a folded apply supersedes, so
// the live footprint stays O(live candidates + the newest applied /
// pending proposes), never O(the lane's whole distill history).
type laneMemReceiptFold struct {
	proposeByEpoch map[int]*proposePayload
	cand           map[string]memReceipt
}

func newLaneMemReceiptFold() *laneMemReceiptFold {
	return &laneMemReceiptFold{proposeByEpoch: map[int]*proposePayload{}, cand: map[string]memReceipt{}}
}

// feed folds ONE event into the lane's candidates. Old cause:"recover"
// rows and the heal_merged/heal_conflict/heal_resolved family never
// become candidates — the fold sees receipts only, today as before
// (2026-08-26 doctrine). A legacy pin receipt (no after_sha) still enters
// the fold — as a terminal landed boundary, not a replayable receipt: its
// write landed file-first, so it has nothing to replay, but as the lane's
// newest pins attestation it must mask every older journal-first receipt.
// Falling through to the older receipt instead (the inversion)
// manufactured false heal_conflicts on pins the legacy write had already
// superseded.
func (f *laneMemReceiptFold) feed(ev store.Event, kinds int) {
	switch {
	case ev.Type == store.EventReviewAction && kinds&replayApply != 0:
		var head struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(ev.Payload, &head) != nil {
			return
		}
		if head.Action == "memory_propose" {
			var pp proposePayload
			if json.Unmarshal(ev.Payload, &pp) == nil {
				f.proposeByEpoch[pp.Epoch] = &pp
			}
			return
		}
		if head.Action != "memory_apply" {
			return
		}
		var p struct {
			Epoch    int            `json:"epoch"`
			Accepted []MemoryAccept `json:"accepted"`
			Recovery applyRecovery  `json:"recovery"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil {
			return
		}
		// Prune proposes this fold can never reference again (K3 hygiene):
		// an apply pairs with its OWN epoch's propose, and no LATER apply
		// pairs with an older one — a batch left unapplied past the next
		// distill is superseded, never applied later (findPendingBatch's
		// doctrine: a new distill supersedes any older batch). The current
		// epoch's own propose stays: an idempotent apply retry can
		// journal the same epoch again before dedupe catches it. Worst
		// case of a pathological out-of-order apply is the pre-existing
		// nil-propose path — a safe conflict, never a wrong merge.
		for epoch := range f.proposeByEpoch {
			if epoch < p.Epoch {
				delete(f.proposeByEpoch, epoch)
			}
		}
		base := memReceipt{
			convID:   ev.ConversationID,
			eventID:  ev.ID,
			seq:      ev.Seq,
			epoch:    p.Epoch,
			accepted: p.Accepted,
			propose:  f.proposeByEpoch[p.Epoch],
		}
		if p.Recovery.Memory != nil {
			r := base
			r.layer = "memory"
			r.before, r.after, r.body = p.Recovery.Memory.BeforeSHA, p.Recovery.Memory.AfterSHA, p.Recovery.Memory.Body
			r.entries = p.Recovery.Memory.Entries
			f.cand[r.layer] = r
		}
		if p.Recovery.Archive != nil {
			r := base
			r.layer = "archive"
			r.before, r.after, r.body = p.Recovery.Archive.BeforeSHA, p.Recovery.Archive.AfterSHA, p.Recovery.Archive.Body
			f.cand[r.layer] = r
		}
		if p.Recovery.User != nil {
			r := base
			r.layer = "user"
			r.before, r.after, r.body = p.Recovery.User.BeforeSHA, p.Recovery.User.AfterSHA, p.Recovery.User.Body
			f.cand[r.layer] = r
		}
		for _, sk := range p.Recovery.Skills {
			r := base
			r.layer = "skill:" + filepath.Base(sk.Name)
			r.before, r.after, r.body = sk.BeforeSHA, sk.AfterSHA, sk.Body
			f.cand[r.layer] = r
		}
		// Pins receipts carry whole bodies; propose rows are only consulted
		// by the memory-layer merge, so pins-only callers skip the
		// review_action family entirely here.
	case ev.Type == store.EventMemoryUpdate && kinds&replayPin != 0:
		var p struct {
			Layer     string `json:"layer"`
			Cause     string `json:"cause"`
			BeforeSHA string `json:"before_sha"`
			AfterSHA  string `json:"after_sha"`
			Body      string `json:"body"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.Layer != "pins" || p.Cause != "pin" {
			return
		}
		if p.AfterSHA == "" {
			// Legacy receipt (pre-recovery, written file-first):
			// nothing recoverable, but this row is the layer's newest
			// attestation — record it as a terminal landed boundary so
			// no OLDER journal-first receipt becomes the candidate,
			// in this lane's fold and in the project-wide event-id
			// pick (the tombstone still carries its event id).
			f.cand["pins"] = memReceipt{
				layer:   "pins",
				convID:  ev.ConversationID,
				eventID: ev.ID,
				seq:     ev.Seq,
				legacy:  true,
			}
			return
		}
		f.cand["pins"] = memReceipt{
			layer:   "pins",
			convID:  ev.ConversationID,
			eventID: ev.ID,
			seq:     ev.Seq,
			before:  p.BeforeSHA,
			after:   p.AfterSHA,
			body:    p.Body,
		}
	}
}

// parseLaneMemReceipts folds ONE lane's seq-ascending events into the
// lane's newest-per-layer receipts — the one-shot wrapper around
// laneMemReceiptFold (the live apply-retry / pin-RMW / sweep callers hold
// the lane's full event slice already; only the boot replayer streams).
func parseLaneMemReceipts(events []store.Event, kinds int) map[string]memReceipt {
	f := newLaneMemReceiptFold()
	for i := range events {
		f.feed(events[i], kinds)
	}
	return f.cand
}

// orderedLayers sorts receipt-map keys so replay and drilling are
// deterministic (map iteration order must never gate file state).
func orderedLayers[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// replayLayerKind reduces a layer name to its family and validates it:
// "memory" | "archive" | "user" | "pins" | "skill:<clean-base>".
func replayLayerKind(layer string) (string, bool) {
	switch layer {
	case "memory", "archive", "user", "pins":
		return layer, true
	}
	if strings.HasPrefix(layer, "skill:") {
		base := filepath.Base(strings.TrimPrefix(layer, "skill:"))
		if base == "" || base == "." || strings.Contains(base, "..") {
			return "", false
		}
		return "skill", true
	}
	return "", false
}

// skillReplayPath derives the replay target for a skill layer: the SAME
// path the apply path computed (filepath.Join(projectRoot, .odo, skills,
// <Base(name)> — the recovery Name is already the file's basename).
func (s *Server) skillReplayPath(layer string) string {
	return filepath.Join(s.projectRoot, ".odo", "skills",
		filepath.Base(strings.TrimPrefix(layer, "skill:")))
}

// readReplayLayer reads one layer's current projection, on the SAME digest
// basis the apply path recorded before/after hashes from (Full uncapped
// reads — the injection cap must never gate a replay decision).
func (s *Server) readReplayLayer(layer string) string {
	switch kind, _ := replayLayerKind(layer); kind {
	case "memory":
		return readFileFull(filepath.Join(s.projectRoot, ".odo", memoryFileName))
	case "archive":
		return readArchive(s.projectRoot)
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return readFileFull(filepath.Join(home, ".odo", "user.md"))
	case "pins":
		return readFileWithin(s.projectRoot, filepath.Join(s.projectRoot, ".odo"), pinsPath(s.projectRoot))
	default: // skill:<base>
		return readFileFull(s.skillReplayPath(layer))
	}
}

// writeReplayLayer writes one layer with the same containment/atomicity
// the live write paths use (writeFileWithin for project files, atomic 0600
// for the global user.md).
func (s *Server) writeReplayLayer(layer, body string) error {
	switch kind, _ := replayLayerKind(layer); kind {
	case "memory":
		return writeFileWithin(s.projectRoot, filepath.Join(s.projectRoot, ".odo", memoryFileName), body, 0o644)
	case "archive":
		return writeFileWithin(s.projectRoot, filepath.Join(s.projectRoot, ".odo", archiveFileName), body, 0o644)
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home: %w", err)
		}
		return writeFileAtomic(filepath.Join(home, ".odo", "user.md"), body, 0o600)
	case "pins":
		return writeFileWithin(s.projectRoot, pinsPath(s.projectRoot), body, 0o644)
	default: // skill:<base>
		return writeFileWithin(s.projectRoot, s.skillReplayPath(layer), body, 0o644)
	}
}

// mergeLine picks one add-style entry's line for a crashed receipt's
// merge (round-3 FIX C): the receipt's VERBATIM recorded line when the
// recovery block carries it — evidence and the original apply epoch's
// reaffirmed count exactly as the live apply wrote them (matched by
// normalized rule text so a duplicated ref resolves once). Only a legacy
// receipt (pre-entries journal shape, no recorded line) falls back to
// the synthesized floor: reaffirmed 1, never the crashed batch's epoch —
// apply epochs are per-conversation (lane-local, epoch 4 on lane A says
// nothing against lane B's epochs), so stamping a cross-lane entry merge
// with r.epoch fabricates rotation recency it never lived.
func mergeLine(r memReceipt, p MemoryProposal) string {
	nr := normalizeRule(p.Rule)
	for _, e := range r.entries {
		if normalizeRule(e.Rule) == nr {
			return e.Line
		}
	}
	return fmt.Sprintf("- %s — cites: %s; reaffirmed: %d", p.Rule, p.Evidence, 1)
}

// memoryMergePlan computes the entry-level merge for a crashed-foreign
// memory.md receipt: the add-style entries missing from the current file,
// appended in the apply's own line format and index order. reason != ""
// marks the receipt conflict-grade (retractions, edits, unresolvable
// intent, or a merged file that would exceed memoryCap — refuse, never
// silently downgrade to a whole-file overwrite or a cap breach).
func memoryMergePlan(r memReceipt, current string) (merged string, added int, reason string) {
	if r.propose == nil {
		return "", 0, "the crashed batch's propose row is missing — entry text unresolvable"
	}
	if len(r.propose.Reaffirm) > 0 {
		return "", 0, fmt.Sprintf("receipt carries %d reaffirm edit(s)", len(r.propose.Reaffirm))
	}
	type entry struct {
		rule, line string
	}
	var entries []entry
	for _, ref := range r.accepted {
		if ref.Target != "memory.md" {
			continue
		}
		if ref.Index < 0 || ref.Index >= len(r.propose.Proposals) {
			return "", 0, fmt.Sprintf("accepted ref index %d unresolvable against the propose batch (%d proposals)",
				ref.Index, len(r.propose.Proposals))
		}
		p := r.propose.Proposals[ref.Index]
		if p.Target != "memory.md" {
			return "", 0, fmt.Sprintf("accepted ref %d is target %q, not memory.md", ref.Index, p.Target)
		}
		if p.Contradicts != "" {
			return "", 0, fmt.Sprintf("receipt carries a retraction (%q)", p.Contradicts)
		}
		entries = append(entries, entry{rule: p.Rule, line: mergeLine(r, p)})
	}
	if len(entries) == 0 {
		return "", 0, "no add-style memory entries recorded"
	}
	present := map[string]bool{}
	for _, ru := range parseMemoryLines(current) {
		if !ru.opaque && ru.text != "" {
			present[normalizeRule(ru.text)] = true
		}
	}
	var missing []entry
	for _, e := range entries {
		n := normalizeRule(e.rule)
		if present[n] { // running set: a doubled ref appends once, like the apply
			continue
		}
		present[n] = true
		missing = append(missing, e)
	}
	if len(missing) == 0 {
		return current, 0, "" // semantically landed — every entry already present
	}
	base := strings.TrimRight(current, "\n")
	var sb strings.Builder
	if base != "" {
		sb.WriteString(base + "\n")
	}
	for _, m := range missing {
		sb.WriteString(m.line + "\n")
	}
	merged = sb.String()
	if len(merged) > memoryCap {
		return "", 0, fmt.Sprintf("entry merge would push memory.md to %d bytes (cap %d)", len(merged), memoryCap)
	}
	return merged, len(missing), ""
}

// journalHealRow appends one replay-ledger row onto the workstream's
// ACTIVE conversation (the GUI Memory tab folds its own conversation for
// conflicts), falling back to the receipt's own conversation when the
// workstream is deleted or unresolvable. Identity travels in the row
// fields, so the fold pairs by content key.
func (s *Server) journalHealRow(ctx context.Context, receiptConv int64, payload map[string]interface{}) {
	target := receiptConv
	if c, err := s.store.GetConversation(ctx, receiptConv); err == nil {
		if active, aerr := s.store.GetActiveConversation(ctx, c.WorkstreamID); aerr == nil {
			target = active.ID
		}
	}
	if _, err := s.store.AppendEvent(ctx, target, store.EventMemoryUpdate, mustJSON(payload)); err != nil {
		log.Printf("memory replay: journal %s row for %s: %v", payload["cause"], payload["layer"], err)
	}
}

// healLedgerRecorded reports the project's ledger state for key: an
// already-journaled (and still unresolved) conflict means the boot replay
// must NOT re-journal it on every boot (idempotence); a resolution closes
// the key forever.
func (s *Server) healLedgerRecorded(ctx context.Context, key healKey) (conflictKnown, resolved bool) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		return false, false
	}
	rows, err := s.store.ListHealLedgerRows(ctx, p.ID)
	if err != nil {
		return false, false
	}
	unresolved, done := foldHealLedger(rows)
	_, conflictKnown = unresolved[key]
	return conflictKnown, done[key]
}

// strandedOpRows projects the unresolved fold onto the pending_counts
// wire list (round-3 FIX F): sorted (conversation, layer, seq) so the
// GUI badge rows never reshuffle between polls (Go map iteration order
// would otherwise flicker the list every tick).
func strandedOpRows(unresolved map[healKey]healRow) []StrandedOp {
	rows := make([]StrandedOp, 0, len(unresolved))
	for _, h := range unresolved {
		rows = append(rows, StrandedOp{
			StrandedConversation: h.key.conv,
			Layer:                h.key.layer,
			ReceiptSeq:           h.key.seq,
			Detail:               h.detail,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StrandedConversation != rows[j].StrandedConversation {
			return rows[i].StrandedConversation < rows[j].StrandedConversation
		}
		if rows[i].Layer != rows[j].Layer {
			return rows[i].Layer < rows[j].Layer
		}
		return rows[i].ReceiptSeq < rows[j].ReceiptSeq
	})
	return rows
}

// foldHealLedger pairs heal_conflict/heal_resolved rows by content key:
// unresolved = conflicted and never resolved (rows ascending; a later
// resolution removes the conflict). pending_counts' stranded_memory_ops
// is len(unresolved) — heal_conflict minus heal_resolved, full-project.
func foldHealLedger(rows []store.Event) (unresolved map[healKey]healRow, resolved map[healKey]bool) {
	unresolved = map[healKey]healRow{}
	resolved = map[healKey]bool{}
	for _, ev := range rows {
		row, ok := parseHealRow(ev)
		if !ok {
			continue
		}
		switch row.cause {
		case "heal_conflict":
			if !resolved[row.key] {
				unresolved[row.key] = row
			}
		case "heal_resolved":
			delete(unresolved, row.key)
			resolved[row.key] = true
		}
	}
	return unresolved, resolved
}

// parseHealRow parses one memory_update ledger row (heal_conflict or
// heal_resolved); ok=false for every other row shape.
func parseHealRow(ev store.Event) (healRow, bool) {
	if ev.Type != store.EventMemoryUpdate {
		return healRow{}, false
	}
	var p struct {
		Cause   string `json:"cause"`
		Layer   string `json:"layer"`
		Conv    int64  `json:"stranded_conversation"`
		Seq     int    `json:"stranded_receipt_seq"`
		ResSeq  int    `json:"receipt_seq"`
		Body    string `json:"stranded_body"`
		BodySHA string `json:"stranded_body_sha16"`
		DiskSHA string `json:"disk_sha16_at_conflict"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(ev.Payload, &p) != nil {
		return healRow{}, false
	}
	row := healRow{cause: p.Cause, body: p.Body, sha: p.BodySHA, diskSHA: p.DiskSHA, detail: p.Detail}
	switch p.Cause {
	case "heal_conflict":
		row.key = healKey{conv: p.Conv, layer: p.Layer, seq: p.Seq}
	case "heal_resolved":
		row.key = healKey{conv: p.Conv, layer: p.Layer, seq: p.ResSeq}
	default:
		return healRow{}, false
	}
	if row.key.layer == "" || row.key.seq <= 0 {
		return healRow{}, false
	}
	return row, true
}

// conflictRecord journals one heal_conflict for a foreign-unmergeable
// receipt unless the project's ledger already knows the key (the
// idempotence rule: an open conflict is never duplicated by the next
// boot, a resolved one never resurfaces). diskSHA is the layer's live
// digest AT JOURNALING (round-3 FIX E): the resolve path refuses to
// overwrite a projection that moved since — the conflict row carries
// everything its guard needs (a boot-open conflict's digest survives
// daemon restarts; recomputing at resolve time would be the clobber the
// guard exists to stop).
func (s *Server) conflictRecord(ctx context.Context, r memReceipt, reason, diskSHA string) {
	key := healKey{conv: r.convID, layer: r.layer, seq: r.seq}
	known, done := s.healLedgerRecorded(ctx, key)
	if known || done {
		return
	}
	s.journalHealRow(ctx, r.convID, map[string]interface{}{
		"layer":                  r.layer,
		"cause":                  "heal_conflict",
		"detail":                 fmt.Sprintf("stranded %s post-crash (receipt seq %d, conversation %d): %s", r.layer, r.seq, r.convID, reason),
		"stranded_receipt_seq":   r.seq,
		"stranded_conversation":  r.convID,
		"stranded_body_sha16":    sha16([]byte(r.body)),
		"stranded_body":          r.body,
		"disk_sha16_at_conflict": diskSHA,
	})
	log.Printf("memory replay: stranded %s (receipt seq %d, conversation %d): %s — Memory tab review required",
		r.layer, r.seq, r.convID, reason)
}

// retireSupersededConflicts closes every OPEN heal_conflict on r's layer
// whose stranded receipt is NOT r (round-3 panel, FIX D): the calling
// evaluation just found the layer's NEWEST receipt landed — on disk as
// journaled, restored from its receipt, or with every journaled entry
// merged/present — so every older conflict on that layer is no longer
// conscientiously resolvable: its Resolve would overwrite the landed
// projection with a superseded body. Each closes as
// heal_resolved{actor:"superseded", dismissed:true}; r's own key skips
// (its intent IS this landing — the resolve freshness guard owns that
// posture). Best-effort like the sibling heal rows; the count converges
// on the next pending_counts tick. The scope gate
// encodes the round-4 FIX 2 authority boundary: callers pass
// evalProjectWide only from the boot full-fold pick; lane-local runtime
// evaluations leave the conflict ledger untouched (the pin handler's
// pre-RMW pass otherwise closed cross-lane conflicts its own receipt
// never outranked).
func (s *Server) retireSupersededConflicts(ctx context.Context, r memReceipt, scope evalScope) {
	if scope != evalProjectWide {
		return // lane-local evaluation: not the layer's newest authority (FIX 2)
	}
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		return
	}
	rows, err := s.store.ListHealLedgerRows(ctx, p.ID)
	if err != nil {
		return
	}
	unresolved, _ := foldHealLedger(rows)
	own := healKey{conv: r.convID, layer: r.layer, seq: r.seq}
	var keys []healKey // sorted: journal order of retirements must never ride map iteration
	for key := range unresolved {
		if key.layer == r.layer && key != own {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].conv != keys[j].conv {
			return keys[i].conv < keys[j].conv
		}
		return keys[i].seq < keys[j].seq
	})
	for _, key := range keys {
		s.journalHealRow(ctx, key.conv, map[string]interface{}{
			"layer":                 key.layer,
			"cause":                 "heal_resolved",
			"receipt_seq":           key.seq,
			"stranded_conversation": key.conv,
			"actor":                 "superseded",
			"dismissed":             true,
		})
		log.Printf("memory replay: retired open %s conflict (receipt seq %d, conversation %d) — superseded by newer landed receipt (seq %d, conversation %d)",
			key.layer, key.seq, key.conv, r.seq, r.convID)
	}
}

// evalMemReceipt applies the newest-receipt predicate to one layer:
// landed → nothing; disk at before (true mid-write crash) → replay the
// journaled body (whole-file rewrite, or chunk append for the archive);
// foreign → entry-merge add-style receipts (memory add-only, archive
// chunk) and conflict everything else with the stranded body embedded.
// Every outcome that lands (or confirms landed) the newest receipt's
// intent retires the layer's older open conflicts (FIX D). Write
// failures log and leave the state for the next boot (best-effort,
// the recovery-posture the marker-first protocol established).
// scope carries the caller's fold context (evalScope): conflict
// retirement fires only under the project-wide authority.
func (s *Server) evalMemReceipt(ctx context.Context, r memReceipt, scope evalScope) memReceiptEval {
	if r.legacy {
		// Terminal landed boundary (a legacy file-first receipt newer
		// than every replayable one): the write already landed via the
		// legacy path — nothing to restore, merge, or conflict, and no
		// row is journaled for a boundary. The boundary IS a landed
		// attestation of record, so it retires the layer's older open
		// conflicts like any landed evaluation (round-4 FIX 3), under the
		// same project-wide authority gate as every retirement.
		s.retireSupersededConflicts(ctx, r, scope)
		return memReceiptEval{outcome: replayNone}
	}
	cur := s.readReplayLayer(r.layer)
	sha := sha16([]byte(cur))
	if sha == r.after {
		// Landed (also re-boot idempotence). The layer's newest authority
		// just proved itself on disk — retire every older open conflict on
		// it (FIX D).
		s.retireSupersededConflicts(ctx, r, scope)
		return memReceiptEval{outcome: replayNone}
	}
	if sha == r.before {
		// Truly crashed mid-write: the projection still holds the receipt's
		// recorded basis — replay exactly what the receipt attests.
		body := r.body
		if r.layer == "archive" {
			body = cur + r.body
		}
		if err := s.writeReplayLayer(r.layer, body); err != nil {
			log.Printf("memory replay: restore %s (receipt seq %d, conversation %d): %v",
				r.layer, r.seq, r.convID, err)
			return memReceiptEval{outcome: replayNone}
		}
		s.journalHealRow(ctx, r.convID, map[string]interface{}{
			"layer":  r.layer,
			"cause":  "recover",
			"detail": fmt.Sprintf("restored %s after stranded receipt (seq %d, conversation %d)", r.layer, r.seq, r.convID),
		})
		log.Printf("memory replay: restored %s (receipt seq %d, conversation %d)", r.layer, r.seq, r.convID)
		s.retireSupersededConflicts(ctx, r, scope) // the restore landed this receipt's intent (FIX D)
		return memReceiptEval{outcome: replayRestored}
	}
	// Foreign: the before/after-sha predicate cannot distinguish a deeper
	// crash from a legitimate supersede — merge add-style content only,
	// never overwrite.
	switch kind, _ := replayLayerKind(r.layer); kind {
	case "memory":
		merged, added, reason := memoryMergePlan(r, cur)
		if reason != "" {
			s.conflictRecord(ctx, r, reason, sha)
			return memReceiptEval{outcome: replayConflicted, reason: reason}
		}
		if added == 0 {
			s.retireSupersededConflicts(ctx, r, scope) // every entry present: intent landed (FIX D)
			return memReceiptEval{outcome: replayNone}
		}
		if err := s.writeReplayLayer(r.layer, merged); err != nil {
			log.Printf("memory replay: entry-merge %s (receipt seq %d): %v", r.layer, r.seq, err)
			return memReceiptEval{outcome: replayNone}
		}
		s.journalHealRow(ctx, r.convID, map[string]interface{}{
			"layer":                 r.layer,
			"cause":                 "heal_merged",
			"receipt_seq":           r.seq,
			"stranded_conversation": r.convID,
			"entries_added":         added,
			"sha16_after":           sha16([]byte(merged)),
			"detail":                fmt.Sprintf("entry-merged %d entries from stranded receipt (seq %d, conversation %d)", added, r.seq, r.convID),
		})
		log.Printf("memory replay: entry-merged %d entries into %s (receipt seq %d, conversation %d)",
			added, r.layer, r.seq, r.convID)
		s.retireSupersededConflicts(ctx, r, scope) // the merge landed this receipt's intent (FIX D)
		return memReceiptEval{outcome: replayMerged, entriesAdded: added}
	case "archive":
		if strings.Contains(cur, r.body) {
			s.retireSupersededConflicts(ctx, r, scope) // chunk present: intent landed (FIX D)
			return memReceiptEval{outcome: replayNone}
		}
		if err := s.writeReplayLayer(r.layer, cur+r.body); err != nil {
			log.Printf("memory replay: append archive chunk (receipt seq %d): %v", r.seq, err)
			return memReceiptEval{outcome: replayNone}
		}
		s.journalHealRow(ctx, r.convID, map[string]interface{}{
			"layer":                 r.layer,
			"cause":                 "heal_merged",
			"receipt_seq":           r.seq,
			"stranded_conversation": r.convID,
			"entries_added":         1,
			"sha16_after":           sha16([]byte(cur + r.body)),
			"detail":                fmt.Sprintf("appended stranded archive chunk (receipt seq %d, conversation %d)", r.seq, r.convID),
		})
		s.retireSupersededConflicts(ctx, r, scope) // the append landed this receipt's intent (FIX D)
		log.Printf("memory replay: appended stranded archive chunk (receipt seq %d, conversation %d)", r.seq, r.convID)
		return memReceiptEval{outcome: replayMerged, entriesAdded: 1}
	default:
		// Whole-file layers (user.md, pins.md, skills) carry no structured
		// per-entry intent in their receipts — conflict, never invent an
		// entry parser.
		reason := "whole-file layer with a foreign projection — no per-entry intent journaled"
		s.conflictRecord(ctx, r, reason, sha)
		return memReceiptEval{outcome: replayConflicted, reason: reason}
	}
}

// replayLaneMemReceipts runs the replay predicate over ONE lane's
// newest-per-layer receipts — the live companion of the boot replayer
// (same engine, same rows, same predicate, never an independent scan).
// The ONE deliberate divergence from the boot pass is authority: a lane's
// own newest receipt is not the layer's project-wide newest, so this pass
// evaluates under evalLaneLocal and never retires heal_conflicts (round-4
// FIX 2). kinds narrows to
// the caller's basis: apply paths replayApply (retry convergence and the
// sweep's consumed-marker repair, FIX 1), the pin handler replayPin (its
// read-modify-write basis must include a crashed pin's line). Returns the
// apply epochs whose receipts this pass
// restored or entry-merged — the apply core's idempotent-retry signal: a
// journaled-consumed batch whose layers are now satisfied reports
// Applied, never "already applied". Callers hold memMu with a fresh event
// snapshot (or run single-threaded at boot).
func (s *Server) replayLaneMemReceipts(ctx context.Context, convID int64, events []store.Event, kinds int) []int {
	cand := parseLaneMemReceipts(events, kinds)
	var repaired []int
	for _, layer := range orderedLayers(cand) {
		r := cand[layer]
		out := s.evalMemReceipt(ctx, r, evalLaneLocal)
		if (out.outcome == replayRestored || out.outcome == replayMerged) && r.epoch != 0 {
			repaired = append(repaired, r.epoch)
		}
	}
	return repaired
}

// replayMemoryJournal is the boot-time project-wide replay (the doctrine
// header): fold the FULL project journal — every lane whose rows survive
// (active or soft-deleted; destroyed lane rows are unrecoverable by
// construction, the FIX B boundary on the file header) — in global
// journal order; per layer evaluate ONLY
// that layer's newest receipt. Runs once per boot, idempotent across
// boots. Best-effort like the sibling recoveries: failures log and never
// stop the daemon from serving.
func (s *Server) replayMemoryJournal(ctx context.Context) {
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("memory replay: startup scan: %v", err)
		}
		return
	}
	// Paged streaming fold (K3 hygiene): the journal arrives in
	// replayJournalReadPage-sized keyset pages; one laneMemReceiptFold per
	// lane carries the order-dependent state (propose→apply pairing,
	// newest-per-layer) across page boundaries, so memory stays bounded no
	// matter how old the project gets.
	pageSize := replayJournalReadPage
	if s.replayJournalPageSizeForTest > 0 {
		pageSize = s.replayJournalPageSizeForTest
	}
	folds := map[int64]*laneMemReceiptFold{}
	var lanes []int64 // first-appearance order, as the pre-paging code walked them
	var afterID int64
	for {
		page, err := s.store.ListProjectEventsPage(ctx, p.ID, afterID, pageSize)
		if err != nil {
			log.Printf("memory replay: startup scan: %v", err)
			return
		}
		for i := range page {
			f := folds[page[i].ConversationID]
			if f == nil {
				f = newLaneMemReceiptFold()
				folds[page[i].ConversationID] = f
				lanes = append(lanes, page[i].ConversationID)
			}
			f.feed(page[i], replayAll)
			afterID = page[i].ID
		}
		if len(page) < pageSize {
			break
		}
	}
	// Per layer, the globally newest receipt is the sole authority ("Do
	// NOT scan non-newest receipts" — supersession through the apply path
	// is itself the newest receipt and stays authoritative; the landed
	// branch keeps the fold re-boot idempotent).
	newest := map[string]memReceipt{}
	// Walk lanes in first-appearance (journal) order — the pre-paging
	// code's construction order. Strict > on globally unique event ids
	// makes ties impossible, so this is belt and braces with
	// orderedLayers below: map iteration order must never gate file
	// state.
	for _, lane := range lanes {
		for layer, r := range folds[lane].cand {
			if cur, ok := newest[layer]; !ok || r.eventID > cur.eventID {
				newest[layer] = r
			}
		}
	}
	for _, layer := range orderedLayers(newest) {
		s.evalMemReceipt(ctx, newest[layer], evalProjectWide)
	}
}

// handleResolveHealConflict closes one journaled heal_conflict (the human
// half of the doctrine): Resolve restores the layer from the embedded
// stranded body (whole-file overwrite; the archive appends its chunk when
// absent — the merge twin); Dismiss records the decision without touching
// files. Freshness (round-3 FIX E): the conflict row records the
// layer's live disk digest at journaling; a Resolve whose live read moved
// since (newer receipt or hand edit) is refused and auto-dismissed as
// superseded instead of stomping the newer state — ledger rows pre-dating
// the guard ("" digest) resolve unguarded, matching their journaled
// contract. The row is addressed by its full content key — (stranded
// conversation, layer, receipt seq): heal rows ride the workstream's
// ACTIVE conversation at journal time, so scanning only the request's
// conversation can miss a row after active-conversation rotation, and a
// cross-lane same-seq collision needs the row's own conversation identity
// to resolve the right key (2026-08-26 panel follow-up). The resolver's
// conversation only journals the closure. Receipt discipline: the
// heal_resolved row journals AFTER a successful write; a write failure
// journals nothing (the conflict stays open and the count honest).
func (s *Server) handleResolveHealConflict(ctx context.Context, req Request) (Response, error) {
	kind, ok := replayLayerKind(req.Layer)
	if req.Layer == "" || !ok {
		return Response{}, fmt.Errorf("resolve_heal_conflict: invalid layer %q", req.Layer)
	}
	if req.ReceiptSeq <= 0 {
		return Response{}, fmt.Errorf("resolve_heal_conflict: receipt_seq is required")
	}
	if req.StrandedConversation <= 0 {
		return Response{}, fmt.Errorf("resolve_heal_conflict: stranded_conversation is required")
	}
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}

	s.memMu.Lock() // runtime resolve takes memMu (the doctrine's point 7)
	defer s.memMu.Unlock()
	// The ledger lookup is project-wide (ListHealLedgerRows folds every
	// workstream) and keyed by the STRANDED conversation — never by the
	// request's carrier lane.
	p, err := s.store.GetProjectByRoot(ctx, s.projectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("resolve_heal_conflict: project: %w", err)
	}
	rows, err := s.store.ListHealLedgerRows(ctx, p.ID)
	if err != nil {
		return Response{}, fmt.Errorf("resolve_heal_conflict: read heal ledger: %w", err)
	}
	unresolved, done := foldHealLedger(rows)
	key := healKey{conv: req.StrandedConversation, layer: req.Layer, seq: req.ReceiptSeq}
	if done[key] {
		return Response{}, fmt.Errorf("resolve_heal_conflict: %s receipt %d (stranded conversation %d) already resolved",
			req.Layer, req.ReceiptSeq, req.StrandedConversation)
	}
	conflict, found := unresolved[key]
	if !found {
		return Response{}, fmt.Errorf("resolve_heal_conflict: no heal_conflict for %s receipt %d (stranded conversation %d)",
			req.Layer, req.ReceiptSeq, req.StrandedConversation)
	}
	if sha16([]byte(conflict.body)) != conflict.sha {
		return Response{}, fmt.Errorf("resolve_heal_conflict: stranded %s body digest mismatch — refusing to write (receipt %d)",
			req.Layer, req.ReceiptSeq)
	}
	// Freshness guard (round-3 panel, FIX E): the conflict journaled with
	// the layer's live digest at that moment (disk_sha16_at_conflict). A
	// newer receipt landing or a human hand edit since moved the file —
	// overwriting now would stomp that newer state with a stale stranded
	// body. Refuse AND close the conflict as superseded (same journal
	// shape as FIX D's evaluation-side retirement): no conscientious
	// resolve exists against a moved projection, and the count must drop
	// with the refusal. Legacy rows carry no digest ("" pre-fix) and keep
	// the pre-guard behavior; Dismiss is exempt — it writes nothing.
	if !req.Dismissed && conflict.diskSHA != "" {
		if live := sha16([]byte(s.readReplayLayer(req.Layer))); live != conflict.diskSHA {
			superseded := map[string]interface{}{
				"layer":                 req.Layer,
				"cause":                 "heal_resolved",
				"receipt_seq":           req.ReceiptSeq,
				"stranded_conversation": conflict.key.conv,
				"actor":                 "superseded",
				"dismissed":             true,
			}
			if err := s.appendHealResolved(ctx, c.ID, superseded); err != nil {
				return Response{}, err
			}
			return Response{}, fmt.Errorf("resolve_heal_conflict: %s moved since the conflict journaled (newer receipt or hand edit) — refusing to overwrite; the conflict auto-dismissed as superseded (receipt %d)",
				req.Layer, req.ReceiptSeq)
		}
	}
	journal := map[string]interface{}{
		"layer":                 req.Layer,
		"cause":                 "heal_resolved",
		"receipt_seq":           req.ReceiptSeq,
		"stranded_conversation": conflict.key.conv,
		"actor":                 "human",
	}
	if req.Dismissed {
		journal["dismissed"] = true
		if err := s.appendHealResolved(ctx, c.ID, journal); err != nil {
			return Response{}, err
		}
		return s.strandedResponse(ctx, true)
	}
	if kind == "archive" {
		cur := readArchive(s.projectRoot)
		if !strings.Contains(cur, conflict.body) {
			if err := s.writeReplayLayer(req.Layer, cur+conflict.body); err != nil {
				return Response{}, fmt.Errorf("resolve_heal_conflict: append archive chunk: %w", err)
			}
		}
	} else if err := s.writeReplayLayer(req.Layer, conflict.body); err != nil {
		return Response{}, fmt.Errorf("resolve_heal_conflict: write %s: %w", req.Layer, err)
	}
	if err := s.appendHealResolved(ctx, c.ID, journal); err != nil {
		return Response{}, err
	}
	return s.strandedResponse(ctx, true)
}

// appendHealResolved journals one heal_resolved row (the resolve IPC's
// close-out), err back to the caller on failure (receipt discipline).
func (s *Server) appendHealResolved(ctx context.Context, convID int64, payload map[string]interface{}) error {
	if _, err := s.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(payload)); err != nil {
		return fmt.Errorf("resolve_heal_conflict: journal heal_resolved: %w", err)
	}
	return nil
}

// strandedResponse answers a resolve with the post-action project-wide
// unresolved count, so the GUI's badge converges without a second poll.
func (s *Server) strandedResponse(ctx context.Context, applied bool) (Response, error) {
	resp := Response{Applied: applied}
	if p, err := s.store.GetProjectByRoot(ctx, s.projectRoot); err == nil {
		if rows, lerr := s.store.ListHealLedgerRows(ctx, p.ID); lerr == nil {
			unresolved, _ := foldHealLedger(rows)
			resp.StrandedMemoryOps = len(unresolved)
		}
	}
	return resp, nil
}
