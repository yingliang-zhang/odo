// P2.4 (docs/design/adoption-lock.md, 2026-08-29): the ContextPanel
// "Runs" tab body — outcome, duration, and measured tokens per run,
// folded purely from the journal (deriveRuns in ../runs.ts; zero new
// IPC), plus the A3-2 receipts section (ux-batch-lock-amendment-a3,
// UX-4): the review_action journal receipts that owned the deleted
// Ledger tab render here as a second section (ReviewReceipts.tsx), off
// the same frozen events prop — the two folds were adjacent journal refs
// in App already (ex ledgerEventsRef/runsEventsRef). Keep-alive
// contract: App mounts this once behind the `active` flag and FREEZES
// the events prop off-tab, so the memo'd subtree re-derives only when
// events actually change and needs no activation refetch — the journal
// is the cache.

import { memo, useMemo } from "react";
import type { KeyboardEvent } from "react";
import type { OdoEvent } from "../types";
import { cn } from "../lib/utils";
import { deriveRuns, type RunRow } from "../runs";
import { formatBytes, formatTokens } from "../stats";
import { SLOT } from "../slots";
import { reviewReceipts, ReviewRow } from "./ReviewReceipts";
import { useCallback, useEffect, useState } from "react";
import { Loader2, Play, RotateCcw } from "lucide-react";
import { errorMessage, readFile, runCommand, sendMessage } from "../api";
import type { CommandResult } from "../types";
import {
  commandBadge,
  formatCommandDuration,
  latestCommandResults,
  parseCommandsConfig,
  type CommandSpec,
} from "../commands";

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
  // Odo DX wave (Feature 1): retry affordance guard — while a run is
  // live the button renders disabled with the busy tooltip (a second
  // send behind the daemon's queue would collide at the next write).
  agentRunning?: boolean;
  // Odo DX wave (Feature 5): the Run/Test hub's journal lane — the
  // click target for run_command. Null conversation = section absent.
  conversationId?: number | null;
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

function RunRowView({ run, onJumpToSeq, agentRunning, retrying, onRetry }: {
  run: RunRow;
  onJumpToSeq?: (seq: number) => void;
  agentRunning?: boolean;
  retrying?: boolean;
  onRetry?: (run: RunRow) => void;
}) {
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
  // Feature 1 (retry): only terminal-failure rows with a replayable
  // prompt text grow the affordance; a live agent renders it disabled
  // with the busy tooltip. Hover-reveal rides group/run (the bubble
  // copy-button pattern).
  const retryable =
    run.status === "error" && (run.promptText ?? "") !== "" && run.conversationId != null && onRetry != null;
  return (
    <div
      className={cn(
        "runs-row",
        "flex items-start gap-2 px-3 py-2 border-b border-[var(--border)] bg-[var(--bg-raised)]",
        "group/run",
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
          {retryable && (
            // Feature 1: re-send the run's prompt through the composer's
            // OWN command (send_message — deliberately no new IPC): the
            // new run lands as a fresh row off the next poll. Click is
            // swallowed so the row's transcript jump never fires.
            <button
              type="button"
              className="runs-retry inline-flex shrink-0 cursor-pointer items-center gap-1 rounded border border-[var(--border)] bg-transparent px-1.5 py-px text-[10px] text-[var(--text-dim)] opacity-0 transition-opacity hover:border-[var(--accent)] hover:text-[var(--text)] group-hover/run:opacity-100 focus-visible:opacity-100 disabled:cursor-not-allowed disabled:opacity-40"
              data-slot={SLOT.runsRetry}
              title={agentRunning ? "Agent busy — wait for current run to finish" : "Retry this prompt as a new run"}
              aria-label={agentRunning ? "Agent busy — wait for current run to finish" : `Retry run #${run.startSeq}`}
              disabled={agentRunning === true || retrying === true}
              onClick={(e) => {
                e.stopPropagation();
                onRetry?.(run);
              }}
            >
              <RotateCcw size={10} aria-hidden />
              {retrying === true ? "sending…" : "retry"}
            </button>
          )}
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

function RunsPanel({ events, currentModel, onJumpToSeq, agentRunning, conversationId, projectRoot }: Props) {
  const runs = useMemo(() => deriveRuns(events), [events]);
  const receipts = useMemo(() => reviewReceipts(events), [events]);
  const hub = conversationId != null ? (
    <CommandsSection conversationId={conversationId} projectRoot={projectRoot} events={events} />
  ) : null;
  // Feature 1: the retry send rides the in-flight startSeq (at most one
  // retry at a time — the daemon serializes runs anyway) and surfaces a
  // thrown refusal inline (run already active, daemon down).
  const [retryBusy, setRetryBusy] = useState<number | null>(null);
  const [retryError, setRetryError] = useState<string | null>(null);
  const handleRetry = useCallback(
    (run: RunRow) => {
      if (agentRunning === true || retryBusy != null) return;
      const prompt = run.promptText;
      const conversation = run.conversationId;
      if (prompt == null || prompt === "" || conversation == null) return;
      setRetryBusy(run.startSeq);
      setRetryError(null);
      sendMessage(conversation, prompt, [], { projectRoot: projectRoot ?? undefined })
        // Nothing appends locally: the daemon starts the run inside
        // send_message, and the poll's afterSeq tail delivers the fresh
        // user_message + agent rows within a tick — the new row grows
        // out of the journal like every other run.
        .catch((e) => setRetryError(`retry failed: ${errorMessage(e)}`))
        .finally(() => setRetryBusy(null));
    },
    [agentRunning, retryBusy, projectRoot],
  );
  if (runs.length === 0 && receipts.length === 0) {
    return (
      <div className="mem-body">
        {currentModel != null && (
          <div className="mem-section-title">coding model: {currentModel}</div>
        )}
        <div className="panel-empty">No runs yet — send a prompt and it lands here.</div>
        {hub}
      </div>
    );
  }
  return (
    <div className="mem-body">
      {currentModel != null && (
        <div className="mem-section-title">coding model: {currentModel}</div>
      )}
      {runs.map((run) => (
        <RunRowView
          key={run.startSeq}
          run={run}
          onJumpToSeq={onJumpToSeq}
          agentRunning={agentRunning}
          retrying={retryBusy === run.startSeq}
          onRetry={handleRetry}
        />
      ))}
      {retryError != null && <div className="settings-error">{retryError}</div>}
      {hub}
      {receipts.length > 0 && (
        // A3-2: the Ledger tab's review-receipts fold. Section title kept
        // verbatim from the deleted LedgerPanel so row identity classes
        // (.ledger-review-*) stay e2e-stable.
        <>
          <div className="mem-section-title">review actions — journal receipts, newest first</div>
          {receipts.map((ev) => (
            <ReviewRow key={ev.seq} event={ev} />
          ))}
        </>
      )}
    </div>
  );
}

// Odo DX wave (Feature 5 — Run/Test hub): registered .odo/commands.json
// entries as run buttons below the run rows. The GUI LISTS by parsing the
// file itself through read_file (zero new IPC for discovery); EXECUTION
// goes to the daemon's run_command, which re-parses authoritatively, runs
// at the project root with EnrichedEnv, and journals command_result — the
// rows' badges fold out of that journal (plus the fresh invoke response,
// so the flip is instant instead of poll-latency). A missing/empty file
// renders NOTHING (zero clutter contract); a file that exists but fails
// the schema names its defect on one line.
function CommandsSection({
  conversationId,
  projectRoot,
  events,
}: {
  conversationId: number;
  projectRoot: string | null;
  events: OdoEvent[];
}) {
  const [specs, setSpecs] = useState<CommandSpec[] | null>(null);
  const [configError, setConfigError] = useState<string | null>(null);
  const [busy, setBusy] = useState<Record<string, true>>({});
  const [fresh, setFresh] = useState<Record<string, CommandResult>>({});
  const [runError, setRunError] = useState<string | null>(null);

  // Config fetch: once per mount/root. The LRU park remounts (refetch);
  // the section claims no activation refetch of its own (commands.json is
  // user-owned config, not journal state — the frozen-events contract is
  // untouched by it).
  useEffect(() => {
    let alive = true;
    readFile(".odo/commands.json", projectRoot)
      .then((resp) => {
        if (!alive) return;
        const parsed = parseCommandsConfig(resp.file_content ?? "");
        setSpecs(parsed.specs);
        setConfigError(parsed.error ?? null);
      })
      .catch(() => {
        // Absent file (the daemon's read_file refusal) → zero clutter.
        if (!alive) return;
        setSpecs(null);
        setConfigError(null);
      });
    return () => {
      alive = false;
    };
  }, [projectRoot]);

  // Newest-wins fold of the journaled command_result rows — results
  // survive remounts/reloads (bootstrap replay re-derives them); the
  // invoke-fresh map only wins until a journaled row for the same name
  // supersedes it on the poll.
  const journalResults = useMemo(() => latestCommandResults(events), [events]);

  if (specs == null || specs.length === 0) {
    return configError != null ? <div className="settings-error">{configError}</div> : null;
  }

  const runOne = (name: string) => {
    if (busy[name] === true) return;
    setBusy((prev) => ({ ...prev, [name]: true }));
    setRunError(null);
    runCommand(conversationId, name, projectRoot ?? undefined)
      .then((resp) => {
        if (resp.command_result != null) {
          setFresh((prev) => ({ ...prev, [name]: resp.command_result as CommandResult }));
        }
      })
      .catch((e) => setRunError(`${name}: ${errorMessage(e)}`))
      .finally(() => {
        setBusy((prev) => {
          const next = { ...prev };
          delete next[name];
          return next;
        });
      });
  };

  return (
    <>
      <div className="mem-section-title">commands — .odo/commands.json</div>
      <div data-slot={SLOT.commandsSection}>
        {specs.map((spec) => {
          const res = fresh[spec.name] ?? journalResults.get(spec.name);
          const badge = res != null ? commandBadge(res) : null;
          const isBusy = busy[spec.name] === true;
          return (
            <div
              key={spec.name}
              className="command-row border-b border-[var(--border)] bg-[var(--bg-raised)]"
              data-slot={SLOT.commandRow}
              data-name={spec.name}
            >
              <div className="flex items-center gap-2 px-3 py-2">
                <button
                  type="button"
                  className="command-run inline-flex shrink-0 cursor-pointer items-center gap-1.5 rounded-md border border-[var(--border)] bg-[var(--bg-input)] px-2.5 py-1 text-[12px] text-[var(--text)] disabled:cursor-not-allowed disabled:opacity-50"
                  disabled={isBusy}
                  title={`run: ${spec.cmd}`}
                  onClick={() => runOne(spec.name)}
                >
                  {isBusy ? (
                    <Loader2 size={12} className="animate-spin" aria-hidden />
                  ) : (
                    <Play size={12} aria-hidden />
                  )}
                  {spec.name}
                </button>
                <span
                  className="command-cmd min-w-0 flex-1 truncate font-mono text-[11px] text-[var(--text-dim)]"
                  title={spec.cwd != null || spec.timeout != null ? `${spec.cmd}${spec.cwd != null ? ` · cwd ${spec.cwd}` : ""}${spec.timeout != null ? ` · timeout ${spec.timeout}s` : ""}` : spec.cmd}
                >
                  {spec.cmd}
                </span>
                {res != null && badge != null && (
                  <span
                    className={cn(
                      "command-badge shrink-0 rounded-md px-1.5 py-px text-[10px] font-semibold",
                      badge.ok
                        ? "text-[var(--ok-text)] bg-[rgba(63,163,95,0.15)]"
                        : "text-[var(--err-text)] bg-[rgba(195,74,74,0.12)]",
                    )}
                    title={res.timed_out === true ? "hit the timeout" : undefined}
                  >
                    {badge.text} · {formatCommandDuration(res.duration_ms)}
                  </span>
                )}
              </div>
              {res != null &&
                ((res.stdout_tail ?? "") !== "" || (res.stderr_tail ?? "") !== "") && (
                  <details className="command-output px-3 pb-2">
                    <summary className="cursor-pointer text-[11px] text-[var(--text-dim)]">output</summary>
                    {(res.stdout_tail ?? "") !== "" && (
                      <pre className="wiki-content mem-file command-stdout mt-1 max-h-[160px] overflow-y-auto">
                        {res.stdout_tail}
                      </pre>
                    )}
                    {(res.stderr_tail ?? "") !== "" && (
                      <pre className="wiki-content mem-file command-stderr mt-1 max-h-[160px] overflow-y-auto text-[var(--err-text)]">
                        {res.stderr_tail}
                      </pre>
                    )}
                  </details>
                )}
            </div>
          );
        })}
        {runError != null && <div className="settings-error">{runError}</div>}
      </div>
    </>
  );
}

// Keep-alive panel (tri-review P2 #5, ex LedgerPanel convention): App
// hands only referentially stable props to the mounted-off-tab subtree,
// so the default shallow compare skips quiet poll ticks — no custom
// comparator.
export default memo(RunsPanel);
