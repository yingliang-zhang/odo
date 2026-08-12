package ipc

// run_verdict (epoch-8, outstanding #1): an exit-0 run is not proof of
// work. The kimi-k3 false stop — thinking-replay loss through the gateway
// transport chain, model silently stalls — makes OMP exit 0 with ZERO
// output, and the adapter's doneSummary fallback ("agent completed",
// omp.go) used to journal it as a clean success. This file is the
// mechanical post-mortem layered on drainRun's terminal handling:
//
//	false_stop — agent_done with zero agent_text AND zero agent_tool_call
//	             (dead on arrival: no answer, no work). One automatic
//	             retry, loop-bound by the retry run's isRetry mark.
//	no_text    — agent_done with tool calls but zero agent_text (the work
//	             happened, the summary died). No retry (side effects may
//	             be real); instead the auto-land pipeline hard-blocks any
//	             diff the tainted run produced (autoland.go).
//
// Every classification journals memory_update{layer:"run_verdict"} — a
// ledger row, never a forged agent_error: the chat history stays truthful
// about what the agent emitted (nothing). Coverage-honesty row content:
// the tallies the verdict was computed from, and retry_fired.
//
// The thinkings tally rides along as PURE telemetry (panel 2026-08-12,
// unanimous on 2/2: zero-thinking is NOT a sound verdict class — short
// acks legitimately skip thinking). Nothing branches on it today; it must
// never become a retry/land predicate without a distribution study.

import (
	"context"
	"log"

	"github.com/yingliang-zhang/odo/internal/store"
)

// Verdict classes. verdictNone ("") means clean: no row is written (a
// healthy run deserves no journal noise, same posture as pref-off
// maybeAutoLand).
const (
	verdictNone      = ""
	verdictFalseStop = "false_stop"
	verdictNoText    = "no_text"
)

// journalRunAdvisory appends one daemon-authored, labeled error row — the
// transcript-visible human-wait surface (round-2 panel parity rule: no
// code path may leave a false stop ledger-only). The odo:true flag marks
// provenance; precedent: the cancel-by-user error row. Best-effort: a
// journal failure is logged, never fatal.
func (s *Server) journalRunAdvisory(ctx context.Context, conversationID int64, msg string) {
	if _, err := s.store.AppendEvent(ctx, conversationID, store.EventAgentError,
		mustJSON(map[string]interface{}{"error": "odo: " + msg, "odo": true})); err != nil {
		log.Printf("run-verdict: journal advisory for conversation %d: %v", conversationID, err)
	}
}

// journalRunVerdict writes the memory_update{layer:"run_verdict"} row for
// one classified run. retry_fired=true on the row that spawned the single
// automatic retry; the retry's own verdict row (when it fails again)
// carries retry_fired=false, so the loop bound is auditable in the ledger.
// A journal failure is logged (never wedges the drain tail, same posture
// as journalAuto).
func (s *Server) journalRunVerdict(ctx context.Context, meta *runMeta, verdict string, retryFired bool) {
	if _, err := s.store.AppendEvent(ctx, meta.conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":       "run_verdict",
		"verdict":     verdict,
		"texts":       meta.texts,
		"tool_calls":  meta.toolCalls,
		"thinkings":   meta.thinkings,
		"is_retry":    meta.isRetry,
		"retry_fired": retryFired,
	})); err != nil {
		log.Printf("run-verdict: journal %s for conversation %d: %v", verdict, meta.conversationID, err)
	}
}
