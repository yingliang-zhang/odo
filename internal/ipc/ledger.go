package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// M6 (Precision + Ledger) §5: .odo/ledger.md — the daemon-only, append-only
// record of verified metrics. Every row quotes a journaled event's payload
// (no LLM in the metric data path, ADR-0003 inv 4); every bullet cites the
// event type + seq its number came from. The ledger is never injected into
// prompts (pull-only: `odo wiki read ledger`, the `ledger` IPC command).

const ledgerFileName = "ledger.md"

// ledgerMetric is one daemon-computed metric row, written to ledger.md as
// `- <label>: <value> (<event citation> seq <S>)`. The citation is `<type>`
// for plain events (user_message) or `<type>/<action>` for review_action
// rows (review_action/distill, review_action/memory_propose,
// review_action/memory_apply). seq == 0 renders `(no <event> event)` — the
// absence of the source event is itself the record.
type ledgerMetric struct {
	label string
	value string
	event string // event citation, e.g. "review_action/memory_apply"
	seq   int    // the cited event's seq (0 when the event is absent)
}

// ledgerPath returns the ledger location (same .odo dir as memory.md).
func ledgerPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".odo", ledgerFileName)
}

// appendLedger appends one `## <header> — <RFC3339 UTC>` section to
// .odo/ledger.md (created 0644 when absent), separated from existing content
// by a blank line. The header is `epoch <N>` for distill sections and
// `epoch <N> (apply)` for apply sections — the file is append-only and a
// later epoch's distill section may precede an older epoch's apply, so
// bullets are never spliced into an older section.
func appendLedger(projectRoot, header string, metrics []ledgerMetric) error {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s — %s\n", header, time.Now().UTC().Format(time.RFC3339))
	for _, m := range metrics {
		if m.seq > 0 {
			fmt.Fprintf(&b, "- %s: %s (%s seq %d)\n", m.label, m.value, m.event, m.seq)
		} else {
			fmt.Fprintf(&b, "- %s: %s (no %s event)\n", m.label, m.value, m.event)
		}
	}
	path := ledgerPath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("append ledger: create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("append ledger: open: %w", err)
	}
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		if _, err := f.WriteString("\n"); err != nil {
			f.Close()
			return fmt.Errorf("append ledger: separator: %w", err)
		}
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return fmt.Errorf("append ledger: write: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("append ledger: close: %w", err)
	}
	return nil
}

// formatLedgerDuration renders a daemon-measured duration: sub-second as
// "<N>ms", otherwise seconds ("187s", "1.5s").
func formatLedgerDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%gs", float64(ms)/1000)
}

// lastReviewAction scans events newest-first for the last review_action
// with the given action. nil when absent.
// lastReviewActionByEpoch finds the last review_action event with the given
// action AND epoch field. This prevents cross-epoch misattribution in the
// ledger (e.g., a zero-proposal distill inheriting the previous epoch's
// memory_propose count).
func lastReviewActionByEpoch(events []store.Event, action string, epoch int) *store.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
			Epoch  int    `json:"epoch"`
		}
		if err := json.Unmarshal(events[i].Payload, &p); err != nil {
			continue
		}
		if p.Action == action && p.Epoch == epoch {
			return &events[i]
		}
	}
	return nil
}

func lastReviewAction(events []store.Event, action string) *store.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(events[i].Payload, &p); err != nil {
			continue
		}
		if p.Action == action {
			return &events[i]
		}
	}
	return nil
}

// distillLedgerMetrics builds the distill section's rows from the
// just-journaled events (not re-scans — the caller re-lists after the
// learner + distill marker so the propose event and the last recall are in):
//
//   - distill duration: the duration_ms key the daemon measured and journaled
//     on distillEv (wall time from distill start to note write — NOT a
//     timestamp delta; the last user message may be hours old).
//   - proposals: the length of the proposals array on the learner's
//     memory_propose event; "0 (no memory_propose event)" when the learner
//     proposed nothing (the absence is the record).
//   - learner vetoes: kept/dropped counts from the memory_propose event's
//     stats field (memory_kept/dropped, user_kept/dropped) — the ratio
//     exposes learner quality without a separate audit.
//   - recall notes: recallCount (from lastRecallCount), citing the
//     user_message whose recall array was measured.
func distillLedgerMetrics(events []store.Event, distillEv store.Event, recallCount int, distillEpoch int) []ledgerMetric {
	var p struct {
		DurationMs int64 `json:"duration_ms"`
	}
	_ = json.Unmarshal(distillEv.Payload, &p)
	metrics := []ledgerMetric{{
		label: "distill duration",
		value: formatLedgerDuration(p.DurationMs),
		event: "review_action/distill",
		seq:   distillEv.Seq,
	}}

	// M6 fix: filter by epoch to prevent cross-epoch misattribution.
	// A zero-proposal distill must show "0", not inherit the previous
	// epoch's memory_propose count.
	if pe := lastReviewActionByEpoch(events, "memory_propose", distillEpoch); pe != nil {
		var pp struct {
			Proposals []json.RawMessage `json:"proposals"`
			Stats     vetoStats         `json:"stats"`
		}
		_ = json.Unmarshal(pe.Payload, &pp)
		metrics = append(metrics, ledgerMetric{
			label: "proposals",
			value: strconv.Itoa(len(pp.Proposals)),
			event: "review_action/memory_propose",
			seq:   pe.Seq,
		})
		// Learner veto breakdown: shows how many of the learner's
		// raw proposals survived daemon-side evidence vetting.
		totalKept := pp.Stats.MemoryKept + pp.Stats.UserKept + pp.Stats.ProceduresKept
		totalDropped := pp.Stats.MemoryDropped + pp.Stats.UserDropped + pp.Stats.ProceduresDropped
		if totalKept > 0 || totalDropped > 0 {
			metrics = append(metrics, ledgerMetric{
				label: "learner vetoes",
				value: fmt.Sprintf("kept: %d, dropped: %d (mem %d/%d, user %d/%d)",
					totalKept, totalDropped,
					pp.Stats.MemoryKept, pp.Stats.MemoryDropped,
					pp.Stats.UserKept, pp.Stats.UserDropped),
				event: "review_action/memory_propose",
				seq:   pe.Seq,
			})
		}
	} else {
		metrics = append(metrics, ledgerMetric{label: "proposals", value: "0", event: "memory_propose"})
	}

	recall := ledgerMetric{label: "recall notes", value: strconv.Itoa(recallCount), event: "user_message"}
	if re := lastRecallEvent(events); re != nil {
		recall.seq = re.Seq
	}
	metrics = append(metrics, recall)
	return metrics
}

// applyLedgerMetrics builds the `(apply)` section's rows from the
// memory_apply marker's own payload: the daemon-computed metrics.accepted /
// metrics.rejected counts journaled at batch consumption.
func applyLedgerMetrics(applyEv store.Event) []ledgerMetric {
	var p struct {
		Metrics map[string]int `json:"metrics"`
	}
	_ = json.Unmarshal(applyEv.Payload, &p)
	return []ledgerMetric{{
		label: "accepted",
		value: fmt.Sprintf("%d, rejected: %d", p.Metrics["accepted"], p.Metrics["rejected"]),
		event: "review_action/memory_apply",
		seq:   applyEv.Seq,
	}}
}

// journalCurateLedger writes a curate section to ledger.md after a
// successful curate pass. Best-effort like journalDistillLedger: a
// write failure journals memory_update{layer:ledger,cause:write_failed}
// and never blocks the curate pipeline.
func (s *Server) journalCurateLedger(ctx context.Context, conversationID int64, curateEv store.Event) {
	wsName := "unknown"
	if conv, err := s.store.GetConversation(ctx, conversationID); err == nil {
		if ws, err := s.store.GetWorkstream(ctx, conv.WorkstreamID); err == nil {
			wsName = ws.Name
		}
	}
	if err := appendLedger(s.projectRoot, fmt.Sprintf("%s/curate", wsName),
		curateLedgerMetrics(curateEv)); err != nil {
		_, _ = s.store.AppendEvent(ctx, conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "ledger",
			"cause":  "write_failed",
			"detail": "curate: " + err.Error(),
		}))
	}
}

// curateLedgerMetrics builds the curate section's rows from the curate
// review_action event's payload:
//
//   - topics: how many topic pages were rewritten
//   - notes read: how many epoch notes fed the curator
//   - stripped citations: ghost-cited lines removed (M17 F4)
//   - trigger: manual / auto_notes / auto_age
func curateLedgerMetrics(curateEv store.Event) []ledgerMetric {
	var p struct {
		Topics            int               `json:"topics"`
		NotesRead         []json.RawMessage `json:"notes_read"`
		StrippedCitations []string          `json:"stripped_citations"`
		Trigger           string            `json:"trigger"`
	}
	_ = json.Unmarshal(curateEv.Payload, &p)
	metrics := []ledgerMetric{
		{
			label: "topics rewritten",
			value: strconv.Itoa(p.Topics),
			event: "review_action/curate",
			seq:   curateEv.Seq,
		},
		{
			label: "notes read",
			value: strconv.Itoa(len(p.NotesRead)),
			event: "review_action/curate",
			seq:   curateEv.Seq,
		},
	}
	if len(p.StrippedCitations) > 0 {
		metrics = append(metrics, ledgerMetric{
			label: "ghost citations stripped",
			value: strconv.Itoa(len(p.StrippedCitations)),
			event: "review_action/curate",
			seq:   curateEv.Seq,
		})
	}
	if p.Trigger != "" {
		metrics = append(metrics, ledgerMetric{
			label: "trigger",
			value: p.Trigger,
			event: "review_action/curate",
			seq:   curateEv.Seq,
		})
	}
	return metrics
}

// verifyLedgerQuote is the inv-4 substring gate: quote must be a verbatim
// substring of the referenced event's payload (normalized: trim + collapse
// whitespace, case-sensitive on the payload's JSON). The haystack is
// string(event.Payload) verbatim. Any future LLM-selected ledger row must
// pass this before being written; a fabricated number is rejected (and
// journaled as memory_update{layer:"ledger", cause:"verify_failed"} by the
// caller selection path).
func verifyLedgerQuote(quote string, event store.Event) bool {
	collapse := func(s string) string {
		return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	}
	q := collapse(quote)
	if q == "" {
		return false
	}
	return strings.Contains(collapse(string(event.Payload)), q)
}
