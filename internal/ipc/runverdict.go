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
	"path/filepath"

	"github.com/yingliang-zhang/odo/internal/adapter"
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
// journal failure is logged and returned, never fatal — most callers stay
// fire-and-forget, but the verify-setup advisory releases its debounce on
// failure so the next blocked diff retries instead of losing the boot's
// one reminder (verify_advisory.go).
func (s *Server) journalRunAdvisory(ctx context.Context, conversationID int64, msg string) error {
	if _, err := s.store.AppendEvent(ctx, conversationID, store.EventAgentError,
		mustJSON(map[string]interface{}{"error": "odo: " + msg, "odo": true})); err != nil {
		log.Printf("run-verdict: journal advisory for conversation %d: %v", conversationID, err)
		return err
	}
	return nil
}

// journalRunUsage writes the D9-W3a measured-cost receipt
// (memory_update{layer:"run_usage"}) for one drained REGULAR run — loop
// runs carry the D3 loop_run_usage receipt instead (drainRun's defer
// skips loopID≠0 so the same spend is never double-journaled). The OMP
// session transcript's per-message usage blocks are summed LLM-free by
// adapter.SessionUsage (journalLoopRunUsage precedent, D3).
//
// Fail-soft: a non-OMP adapter or a missing/unusable transcript journals
// usage_available:false + reason — numbers are NEVER fabricated.
// journalRunUsage must not wedge the drain tail either.
func (s *Server) journalRunUsage(ctx context.Context, meta *runMeta) {
	payload := map[string]interface{}{
		"layer":  "run_usage",
		"run_id": meta.runID,
	}
	reason := ""
	usage, ok := adapter.Usage{}, false
	if ad, found := s.adapterNamed(meta.adapter); found && ad != nil {
		if _, isOMP := ad.(*adapter.OMP); !isOMP {
			reason = "non-OMP adapter: no session transcript contract"
		}
	}
	if reason == "" {
		usage, ok = adapter.SessionUsage(filepath.Join(s.mgr.StateDir(), "sessions", meta.runID))
		if !ok {
			reason = "no session transcript (missing JSONL or no usage records)"
		}
	}
	if !ok {
		payload["usage_available"] = false
		payload["reason"] = reason
	} else {
		payload["usage_available"] = true
		payload["input_tokens"] = usage.InputTokens
		payload["output_tokens"] = usage.OutputTokens
		payload["cache_read_tokens"] = usage.CacheReadTokens
		payload["cache_write_tokens"] = usage.CacheWriteTokens
		payload["cost_usd"] = usage.CostUSD
	}
	if _, err := s.store.AppendEvent(ctx, meta.conversationID, store.EventMemoryUpdate, mustJSON(payload)); err != nil {
		log.Printf("run-usage: journal for run %s (conversation %d): %v", meta.runID, meta.conversationID, err)
	}
}

// journalRunVerdict writes the memory_update{layer:"run_verdict"} row for
// one classified run. retry_fired=true on the row that spawned the single
// automatic retry; the retry's own verdict row (when it fails again)
// carries retry_fired=false, so the loop bound is auditable in the ledger.
// A journal failure is logged (never wedges the drain tail, same posture
// as journalAuto).
func (s *Server) journalRunVerdict(ctx context.Context, meta *runMeta, verdict string, retryFired bool) {
	row := map[string]interface{}{
		"layer":       "run_verdict",
		"verdict":     verdict,
		"texts":       meta.texts,
		"tool_calls":  meta.toolCalls,
		"thinkings":   meta.thinkings,
		"is_retry":    meta.isRetry,
		"retry_fired": retryFired,
	}
	// P0 (prewalk): journal model identity so the audit trail knows which
	// model produced the run. When prewalk is active, this records both
	// the main and prewalk models.
	if meta.goal != "" {
		settings := adapter.ReadSettings()
		row["model"] = settings.CodingModel
		if settings.PrewalkModel != "" {
			row["prewalk_model"] = settings.PrewalkModel
		}
	}
	if _, err := s.store.AppendEvent(ctx, meta.conversationID, store.EventMemoryUpdate, mustJSON(row)); err != nil {
		log.Printf("run-verdict: journal %s for conversation %d: %v", verdict, meta.conversationID, err)
	}
}
