import { memo, useCallback, useEffect, useRef, useState } from "react";
import { errorMessage, learningStatus, unwrap } from "../api";
import type { LearningContextRow, LearningOutcomeRow, LearningStatus } from "../types";
import LoadingInline from "./LoadingInline";
import { Badge } from "./ui/badge";

// D9-W3 (learning control plane, pure observability): the FIRST flagged-rules
// surface — design-lock correction #1: MemoryPanel never landed wave-4 flag
// display, so the rules audit's memory_audit_flag rows were journaled but
// never rendered anywhere. This tab renders the daemon's single `learning_status`
// fold (GUI never re-folds — dual-fold skew is a locked failure mode):
// flagged rules with the thresholds they cleared, per-episode outcome
// accounting, and the candidate lifecycle (an empty state until W4 writes
// .odo/learning/candidates.jsonl). Read-only — no decision path consumes it.
//
// Class hooks: learning-panel / learning-section / learning-flag-row /
// learning-episode-row / learning-candidate-row; section headers reuse the
// shared mem-section-title, empty states wiki-hint, the error banner
// settings-error — no new app.css rules needed (Tailwind utilities carry
// layout, same P1-P4 doctrine as MemoryPanel).

interface Props {
  // All reads route to this project's daemon; null = bridge default. App
  // keys the panel by project root so a project switch remounts it.
  projectRoot?: string | null;
  // Keep-alive activation edge (MemoryPanel precedent): App mounts the
  // panel once and CSS-hides it; refetch on mount and on the
  // inactive→active edge (via wasActiveRef below).
  active: boolean;
}

// Display order + labels for the compact outcome line. accepts/rejects fold
// their auto subset inline ("accepts 3 (auto 2)"); every other non-zero key
// renders as "label N" with the journaled key as the label.
const OUTCOME_LABELS = [
  ["accepted", "accepts"],
  ["rejected", "rejects"],
  ["weak_rejected", "weak"],
  ["verify_failed", "verify_failed"],
  ["panel_mixed", "panel_mixed"],
  ["panel_minority_reject", "panel_minority_reject"],
  ["revise_rounds_spawned", "revise_rounds_spawned"],
  ["revise_landed", "revise_landed"],
  ["ladder_suspended", "ladder_suspended"],
  ["revise_no_progress", "revise_no_progress"],
  ["agent_errors", "agent_errors"],
  ["false_stops", "false_stops"],
  ["no_texts", "no_texts"],
  ["human_reverts", "human_reverts"],
] as const satisfies readonly (readonly [keyof LearningOutcomeRow, string])[];

// Compact non-zero outcome line for one episode: outcome keys first, then
// the visible context counters — attribution_lost when >0 (window-boundary
// reconciliation is a locked disclosure), memory_free_outcomes when the
// daemon emitted it (it only ever appears with a non-zero value).
// All-zero rows read as "no outcomes" instead of an empty string.
function outcomeLine(o: LearningOutcomeRow, c: LearningContextRow): string {
  const parts: string[] = [];
  for (const [key, label] of OUTCOME_LABELS) {
    const n = o[key];
    if (n === 0) continue;
    if (key === "accepted" && o.auto_accepted > 0) {
      parts.push(`accepts ${n} (auto ${o.auto_accepted})`);
    } else if (key === "rejected" && o.auto_rejected > 0) {
      parts.push(`rejects ${n} (auto ${o.auto_rejected})`);
    } else {
      parts.push(`${label} ${n}`);
    }
  }
  if (c.attribution_lost > 0) parts.push(`attribution_lost ${c.attribution_lost}`);
  if (c.memory_free_outcomes != null && c.memory_free_outcomes > 0) {
    parts.push(`memory_free_outcomes ${c.memory_free_outcomes}`);
  }
  return parts.length > 0 ? parts.join(" · ") : "no outcomes";
}

function LearningPanel({ projectRoot, active }: Props) {
  const [status, setStatus] = useState<LearningStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Unmount guard: a late fetch resolution must not setState on a dead
  // component. Reset inside the setup so StrictMode's dev double-invoke
  // leaves it true (MemoryPanel precedent).
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const refresh = useCallback(async () => {
    try {
      const resp = await learningStatus(projectRoot ?? undefined);
      if (!mountedRef.current) return;
      setStatus(unwrap(resp).learning ?? null);
      setError(null);
    } catch (e) {
      if (!mountedRef.current) return;
      setError(`learning status failed: ${errorMessage(e)}`);
      setStatus(null);
    } finally {
      if (mountedRef.current) setLoading(false);
    }
  }, [projectRoot]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Activation edge: the panel survives tab switches under App's
  // keep-alive wrapper, so new episodes/flags accumulate while hidden —
  // refetch on inactive→active (initial mount is covered above).
  const wasActiveRef = useRef(active);
  useEffect(() => {
    if (!wasActiveRef.current && active) {
      void refresh();
    }
    wasActiveRef.current = active;
  }, [active, refresh]);

  const t = status?.flag_thresholds;
  const totals = status?.episode_totals;

  return (
    <div className="learning-panel h-full flex flex-col">
      {error && <div className="settings-error">{error}</div>}
      {loading && <LoadingInline />}
      {!loading && !error && status == null && (
        <div className="wiki-hint">
          Learning status unavailable — the daemon did not return a learning report.
        </div>
      )}
      {!loading && status != null && t != null && totals != null && (
        <>
          <div className="learning-section mb-4">
            <div className="mem-section-title">Flagged rules</div>
            <div className="learning-thresholds px-3 py-2 text-[11px] text-[var(--text-dim)]">
              {`harmful: injections≥${t.min_injections} rejects≥${t.min_rejects} conversations≥${t.min_reject_conversations} reject-rate≥${t.rate_factor}× baseline · effective: accept-rate≥${t.rate_factor}× baseline`}
            </div>
            {status.flags.length === 0 ? (
              <div className="wiki-hint">
                No flagged rules — the rules audit has not emitted memory_audit_flag rows.
              </div>
            ) : (
              // Flags arrive newest-first by seq (daemon-side order).
              status.flags.map((f) => (
                <div
                  key={f.seq}
                  className="learning-flag-row flex items-start justify-between gap-3 px-3 py-2.5 border-b border-[var(--border)] bg-[var(--bg-raised)]"
                >
                  <div className="min-w-0">
                    <Badge
                      variant={f.verdict === "effective" ? "accept" : f.verdict === "harmful" ? "reject" : "other"}
                      className={`learning-flag-verdict learning-flag-${f.verdict} capitalize`}
                    >
                      {f.verdict}
                    </Badge>
                    <div className="learning-flag-rule whitespace-pre-wrap [word-break:break-word] mt-1">{f.rule}</div>
                  </div>
                  <div className="learning-flag-stats shrink-0 text-[11px] text-[var(--text-dim)]">
                    {`inj ${f.injections} · rej ${f.rejects} · convs ${f.reject_conversations} · seq ${f.seq}`}
                  </div>
                </div>
              ))
            )}
          </div>

          <div className="learning-section mb-4">
            <div className="mem-section-title">Episodes</div>
            <div className="learning-episode-totals px-3 py-2 text-[11px] text-[var(--text-dim)]">
              {`${status.episode_count} episodes recorded · totals: accepts ${totals.accepted} (auto ${totals.auto_accepted}) · rejects ${totals.rejected} (auto ${totals.auto_rejected}) · weak ${totals.weak_rejected} · verify_failed ${totals.verify_failed}`}
            </div>
            {status.episodes.length === 0 ? (
              <div className="wiki-hint">
                No learning episodes recorded yet — the episode fold appends learning_episode rows at each distill tail.
              </div>
            ) : (
              // Already newest-first daemon-side; render all the packet
              // carries (capped at 50).
              status.episodes.map((ep) => (
                <div
                  key={ep.seq}
                  className="learning-episode-row px-3 py-2.5 border-b border-[var(--border)] bg-[var(--bg-raised)]"
                >
                  <div className="learning-episode-head text-[12px] text-[var(--text)]">
                    {`${ep.workstream} · epoch ${ep.epoch} · window [${ep.window.first_seq}–${ep.window.last_seq}]`}
                  </div>
                  <div className="learning-episode-outcomes mt-[3px] text-[11px] text-[var(--text-dim)]">
                    {outcomeLine(ep.outcomes, ep.context)}
                  </div>
                </div>
              ))
            )}
          </div>

          <div className="learning-section mb-4">
            <div className="mem-section-title">Candidates</div>
            {status.candidates.length === 0 ? (
              <div className="wiki-hint">
                No learning candidates yet — the candidate lifecycle lands in wave W4; this list will read
                .odo/learning/candidates.jsonl + stage rows.
              </div>
            ) : (
              // Forward-compat (W4+): nothing writes candidates in W3, but
              // the locked shape renders without a GUI change then.
              status.candidates.map((c) => (
                <div
                  key={c.artifact_hash}
                  className="learning-candidate-row flex items-center gap-2 px-3 py-2.5 border-b border-[var(--border)] bg-[var(--bg-raised)]"
                >
                  <Badge variant="other" className="capitalize">
                    {c.stage}
                  </Badge>
                  <span className="learning-candidate-hash font-mono text-[11px] text-[var(--text-dim)]">
                    {c.artifact_hash.slice(0, 12)}
                  </span>
                  <span className="learning-candidate-scope text-[11px] text-[var(--text-dim)]">{c.scope}</span>
                  <span className="learning-candidate-seq text-[11px] text-[var(--text-dim)]">{`seq ${c.created_seq}`}</span>
                  {c.invalid && (
                    <span className="learning-candidate-invalid text-[11px] text-[var(--err-text)]">invalid</span>
                  )}
                </div>
              ))
            )}
          </div>
        </>
      )}
    </div>
  );
}

// Keep-alive panel: App keeps this mounted under ContextPanel's `hidden`
// tabs and hands it stable props, so the default shallow compare skips the
// hidden subtree on quiet poll ticks (MemoryPanel precedent).
export default memo(LearningPanel);
