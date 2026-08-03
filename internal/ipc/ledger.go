package ipc

import (
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
//   - recall notes: recallCount (from lastRecallCount), citing the
//     user_message whose recall array was measured.
func distillLedgerMetrics(events []store.Event, distillEv store.Event, recallCount int) []ledgerMetric {
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

	if pe := lastReviewAction(events, "memory_propose"); pe != nil {
		var pp struct {
			Proposals []json.RawMessage `json:"proposals"`
		}
		_ = json.Unmarshal(pe.Payload, &pp)
		metrics = append(metrics, ledgerMetric{
			label: "proposals",
			value: strconv.Itoa(len(pp.Proposals)),
			event: "review_action/memory_propose",
			seq:   pe.Seq,
		})
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
