// P2.4 (docs/design/adoption-lock.md, 2026-08-29): the ContextPanel
// "Runs" tab body — outcome, duration, and measured tokens per run,
// folded purely from the journal (deriveRuns in ../runs.ts; zero new
// IPC). Rows keep LedgerPanel's compact tabular-receipt posture. Keep-
// alive contract: App mounts this once behind the `active` flag and
// FREEZES the events prop off-tab, so the memo'd subtree re-derives only
// when events actually change and needs no activation refetch — the
// journal is the cache.

import { memo, useMemo } from "react";
import type { KeyboardEvent } from "react";
import type { OdoEvent } from "../types";
import { cn } from "../lib/utils";
import { deriveRuns, type RunRow } from "../runs";
import { formatBytes, formatTokens } from "../stats";
import { SLOT } from "../slots";

interface Props {
  // The conversation's journaled events — App's bootstrap replay + poll
  // appends are the ONLY source (A-P0 #1 contract, no new IPC).
  events: OdoEvent[];
  // Panel-tab wiring contract (same shape as LedgerPanel); the fold is
  // conversation-scoped and App owns the daemon route.
  projectRoot: string | null;
  // Keep-alive activation edge: declared for the mounting contract. Runs
  // derive purely from the events prop, so there is no hidden-tab drift
  // to refetch (unlike the daemon-file-backed LedgerPanel).
  active: boolean;
  // No per-run model is journaled — the conversation's current coding
  // model renders once in the header, never per row.
  currentModel?: string;
  // Read-only transcript jump: row click → the run start's seq (App owns
  // the data-seq anchor scroll; P2.4 deep-link).
  onJumpToSeq?: (seq: number) => void;
}

// Status dot colors — existing tokens only (status-badge/bg-run,
// badge-accept/badge-reject all read the same ramp).
const STATUS_DOT: Record<RunRow["status"], string> = {
  running: "bg-[var(--bg-run)]",
  ok: "bg-[var(--ok)]",
  error: "bg-[var(--err)]",
};

// M3 run-status formatting (ChatSurface's formatElapsed, private there):
// `<m>m <s>s`, bare seconds under a minute ("35s").
function formatDuration(startIso: string, endIso: string): string | null {
  const ms = Date.parse(endIso) - Date.parse(startIso);
  if (Number.isNaN(ms)) return null;
  const total = Math.max(0, Math.floor(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  if (m >= 60) return `${Math.floor(m / 60)}h ${m % 60}m`;
  return m > 0 ? `${m}m ${s}s` : `${s}s`;
}

// ChatSurface's run-header clock, shape-locked: HH:MM:SS local.
function clockOf(iso: string): string | null {
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return null;
  return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

// The tokens cell's precedence: measured D3 usage wins; a fail-soft
// "unavailable" or a plain chat run falls through to the send's prompt-
// byte estimate (labeled est); nothing journaled renders "—", never a
// fabricated count.
function tokensCell(run: RunRow): { text: string; title?: string } {
  if (run.usage != null && run.usage !== "unavailable") {
    const u = run.usage;
    const cost = u.costUsd != null ? ` · $${u.costUsd.toFixed(3)}` : "";
    return {
      text: `${formatTokens(u.input)} in · ${formatTokens(u.output)} out${cost}`,
      title: `measured (loop_run_usage${u.costUsd != null ? ", cost journaled" : ""}) · cache r ${formatTokens(u.cacheRead)} · w ${formatTokens(u.cacheWrite)}`,
    };
  }
  if (run.promptBytesEst != null) {
    return {
      text: `est ${formatBytes(run.promptBytesEst)}`,
      title:
        run.usage === "unavailable"
          ? `measured usage unavailable: ${run.usageReason ?? "no reason journaled"} — prompt bytes estimate`
          : "prompt bytes estimate (no usage row journaled for this run)",
    };
  }
  return { text: "—", title: run.usageReason };
}

function RunRowView({ run, onJumpToSeq }: { run: RunRow; onJumpToSeq?: (seq: number) => void }) {
  const started = clockOf(run.startedAt);
  const finished = run.finishedAt != null ? clockOf(run.finishedAt) : null;
  const duration = run.finishedAt != null ? formatDuration(run.startedAt, run.finishedAt) : null;
  const tokens = tokensCell(run);
  const jump = onJumpToSeq != null ? () => onJumpToSeq(run.startSeq) : null;
  const onKeyDown =
    jump != null
      ? (e: KeyboardEvent) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            jump();
          }
        }
      : undefined;
  // The terminal row's evidence line: end summary (ok) or error, muted,
  // clipped — the full string rides the title.
  const evidence = run.status === "ok" ? run.endSummary : run.status === "error" ? run.endError : null;
  return (
    <div
      className={cn(
        "runs-row",
        "flex items-start gap-2 px-3 py-2 border-b border-[var(--border)] bg-[var(--bg-raised)]",
        jump != null && "cursor-pointer hover:bg-[var(--bg)]",
      )}
      data-slot={SLOT.runsRow}
      data-seq={run.startSeq}
      data-status={run.status}
      onClick={jump ?? undefined}
      onKeyDown={onKeyDown}
      role={jump != null ? "button" : undefined}
      tabIndex={jump != null ? 0 : undefined}
    >
      <span
        className={cn("runs-dot mt-1 inline-block h-2 w-2 shrink-0 rounded-full", STATUS_DOT[run.status])}
        title={run.status}
        aria-hidden
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span className={cn("runs-time mono shrink-0 tabular-nums text-[var(--text-dim)] text-[11px]")}>
            #{run.startSeq}
          </span>
          <span className="tabular-nums text-[12px] text-[var(--text-dim)]">
            {started ?? "?"}
            {run.status === "running" ? (
              <span className="text-[var(--bg-run)]"> · running</span>
            ) : (
              <>
                {finished != null && ` → ${finished}`}
                {duration != null && ` · ${duration}`}
              </>
            )}
          </span>
          <span
            className={cn(
              "runs-tokens ml-auto shrink-0 whitespace-nowrap tabular-nums",
              "text-[11px]",
              run.usage != null && run.usage !== "unavailable"
                ? "text-[var(--text)]"
                : "text-[var(--text-dim)]",
            )}
            title={tokens.title}
          >
            {tokens.text}
          </span>
        </div>
        <div
          className={cn("runs-goal truncate text-[13px] text-[var(--text)]")}
          title={run.goal}
        >
          {run.goal}
        </div>
        {evidence != null && (
          <div
            className={cn(
              "runs-evidence truncate text-[12px]",
              run.status === "error" ? "text-[var(--err-text)]" : "text-[var(--text-dim)]",
            )}
            title={evidence}
          >
            {evidence}
          </div>
        )}
      </div>
    </div>
  );
}

function RunsPanel({ events, currentModel, onJumpToSeq }: Props) {
  const runs = useMemo(() => deriveRuns(events), [events]);
  if (runs.length === 0) {
    return (
      <div className="mem-body">
        {currentModel != null && (
          <div className="mem-section-title">coding model: {currentModel}</div>
        )}
        <div className="panel-empty">No runs yet — send a prompt and it lands here.</div>
      </div>
    );
  }
  return (
    <div className="mem-body">
      {currentModel != null && (
        <div className="mem-section-title">coding model: {currentModel}</div>
      )}
      {runs.map((run) => (
        <RunRowView key={run.startSeq} run={run} onJumpToSeq={onJumpToSeq} />
      ))}
    </div>
  );
}

// Keep-alive panel (LedgerPanel convention, tri-review P2 #5): App hands
// only referentially stable props to the mounted-off-tab subtree, so the
// default shallow compare skips quiet poll ticks — no custom comparator.
export default memo(RunsPanel);
