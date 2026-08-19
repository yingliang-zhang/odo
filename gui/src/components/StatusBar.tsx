// M9 Phase 5: StatusBar — 24px bar with session facts, clickable badges,
// and run indicator. Absorbs everything System-ish that used to weigh down
// the sidebar.

import { useEffect, useState, useCallback } from "react";
import { Check, LoaderCircle, GitCompareArrows, FileText, MapPin, Gauge, Boxes, AlertCircle, Ban, Activity } from "lucide-react";
import { Popover, PopoverContent, PopoverTrigger } from "./ui/popover";
import { cn } from "../lib/utils";
import {
  BYTES_PER_TOKEN,
  contextWindowTokens,
  formatBytes,
  formatTokens,
} from "../stats";
import type { PanelModel, PromptSnapshot } from "../stats";
import { pipelineLabel } from "../pipeline";
import type { PipelinePhase, PipelineState } from "../pipeline";
import type { PanelTab } from "./ContextPanel";
import { ompUsage } from "../api";
import type { OmpUsageMerged, OmpUsageReport, OmpUsageLimit, OdoEvent } from "../types";
import { strings } from "../strings";

// Tri-model header gap analysis: estimate bytes accumulated since the last
// receipted prompt (agent_text + tool_call args + tool_result bodies). This
// is an estimate — the daemon's true context is only known at the next send.
// Uses TextEncoder for exact UTF-8 byte counts (same pattern as stats.ts).
const utf8 = new TextEncoder();
const LIVE_RESULT_CAP = 5000; // bytes — tool results can be huge
const LIVE_ARGS_CAP = 5000; // bytes — patch payloads can be large

function estimateLiveDelta(events: readonly OdoEvent[], sinceSeq: number): number {
  let bytes = 0;
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i];
    if (ev.seq <= sinceSeq) break;
    // GLM: only count events that are actually injected into the running
    // agent's context — skip user_message (parked goals are queued, not
    // injected), review_action, memory_update, etc.
    if (ev.type !== "agent_text" && ev.type !== "agent_thinking" &&
        ev.type !== "agent_tool_call" && ev.type !== "agent_tool_result") continue;
    const p = ev.payload ?? {};
    // agent_text: the model's streaming output
    if (typeof p.text === "string") bytes += utf8.encode(p.text).length;
    // agent_tool_call: args JSON
    if (typeof p.args === "string") bytes += utf8.encode(p.args).slice(0, LIVE_ARGS_CAP).length;
    else if (p.args != null) bytes += Math.min(utf8.encode(JSON.stringify(p.args)).length, LIVE_ARGS_CAP);
    // agent_tool_result: result text (capped — a single huge result shouldn't dominate)
    if (typeof p.result === "string") bytes += Math.min(utf8.encode(p.result).length, LIVE_RESULT_CAP);
    else if (p.result != null) bytes += Math.min(utf8.encode(JSON.stringify(p.result)).length, LIVE_RESULT_CAP);
  }
  return bytes;
}

// One background run as reported by the daemon's pending_counts —
// workstreams with a live run that is NOT the one in view.
export interface BackgroundRun {
  id: number;
  name: string;
}

// GUI Wave A (#1): transient StatusBar flashes derived from the raw
// running_workstreams set (App watches the transitions). `started` tints
// the chip when a new background run appears; `finished` renders a
// completion chip even once the run list drains to zero — that transition
// is the whole point, the list itself can't show it.
export interface BackgroundNotice {
  started: string[];
  finished: string[];
}

interface Props {
  workstreamName: string | null;
  conversationId: number | null;
  epoch: number;
  projectRoot: string | null;
  agentRunning: boolean;
  // Tri-model header gap: turn start timestamp for live duration display.
  turnStartedAt: number | null;
  // Runs in other workstreams of the active project. The chip is the only
  // surface for activity the user cannot see (panel sessions, other ws) —
  // opening it lists every run, clicking a row jumps straight to it.
  backgroundRuns: BackgroundRun[];
  bgNotice: BackgroundNotice | null;
  onJumpWorkstream: (id: number) => void;
  // Wave B #5: closure of the last prompt sent to a model, derived from
  // events. Null before any receipted send — the meter hides rather than
  // fabricate occupancy.
  lastPrompt: PromptSnapshot | null;
  // Tri-model header gap: events for live context delta estimation.
  events: readonly OdoEvent[];
  // Wave B #9: coding model (window denominator) + review panel list.
  codingModel: string | null;
  reviewPanel: PanelModel[];
  // Auto-land pipeline (design lock Phase 1): per-diff status derived from
  // the journaled auto_panel rows — ONLY states with something to show
  // right now (expired flashes are dropped in derivation). Empty = pref
  // off / nothing tracked → the chip is absent.
  pipelineStates: PipelineState[];
  // Clickable badges → open panel on the matching tab. PanelTab (single
  // source in ContextPanel) already includes "review", so the pipeline
  // chip's row-jump needs no cast and no caller change — tsc enforces
  // every call site.
  pendingDiffs: number;
  wikiNoteCount: number | null;
  pendingMemoryProposals: number;
  onBadgeClick: (tab: PanelTab) => void;
}

// StatusBar popovers are Radix Popovers (Phase 6): outside-click dismiss,
// Esc dismiss, and upward collision-aware positioning are built in. The
// PopoverContent Esc gate (stopPropagation) keeps a bare Esc from reaching
// App's agent-cancel handler.

// Ring fill tiers, applied to stroke AND the inline percent text. The
// thresholds are display-only — nothing in the system reads this percent.
// 80+ is where compaction pressure gets real for a typical ratio (kimi-k3
// compactRatio 0.9, glm 0.35 — 80% keeps the middle tier wide enough that
// glm's ~35% trigger doesn't look alarming; see modelspec for the real
// per-model triggers).
const METER_TIERS = [
  { max: 50, cls: "meter-ok", color: "text-ok-text" },
  { max: 80, cls: "meter-warn", color: "text-warn" },
  { max: Infinity, cls: "meter-err", color: "text-err-text" },
];

// Chip chrome — was .status-badge in app.css (deleted). The marker class
// stays as an e2e/theme hook; the flash rules (.bg-flash-done/-new,
// .auto-land-chip.is-landed) remain in app.css and, being unlayered, win
// over these layered utilities exactly as they won over the old rules.
const STATUS_BADGE =
  "status-badge inline-flex cursor-pointer items-center gap-[3px] whitespace-nowrap rounded border border-border bg-transparent px-[5px] py-px font-mono text-[10px] leading-[1.4] text-text-dim tabular-nums hover:border-text-dim hover:bg-bg-input hover:text-text";

// Clickable popover rows — was .bg-run-row/.bg-run-name/.bg-run-state.
const BG_RUN_ROW =
  "bg-run-row flex w-full cursor-pointer items-center gap-2 rounded-sm bg-transparent px-[7px] py-[5px] text-left text-text hover:bg-bg-input";
const BG_RUN_NAME =
  "bg-run-name min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap";
const BG_RUN_STATE = "bg-run-state shrink-0 text-[10px] text-text-dim";

// Background-run status dot — was .ws-dot + .ws-dot.dot-bg in app.css.
// Base dot utilities mirror Sidebar's dotClass base.
const WS_DOT_BG =
  "ws-dot dot-bg pulse size-2 shrink-0 rounded-full bg-bg-run shadow-[0_0_0_2px_color-mix(in_srgb,var(--bg-run)_22%,transparent)]";

// Popover section title — was .ctx-pop-title. Font sizes in cn()-merged
// strings use the length: escape (twoMerge treats the bare text-micro theme
// utility as a color and would drop it next to text-text-dim).
const CTX_POP_TITLE =
  "ctx-pop-title px-1 pb-1.5 pt-0.5 text-[length:var(--text-micro)] uppercase tracking-[0.06em] text-text-dim";
// Receipt-layer list item — was .ctx-layer.
const CTX_LAYER =
  "ctx-layer wrap-anywhere py-px text-[length:var(--text-micro)] text-text";
// Usage bar fill — was .omp-bar-fill.
const OMP_BAR_FILL =
  "omp-bar-fill h-full rounded-[2px] transition-[width] duration-300 ease-[ease]";

// Wave B #5: context-pressure ring — the journaled byte total of the last
// prompt vs the coding model's context window (window tokens × ~4
// bytes/token, an estimate, hence "~"). Click expands the journaled
// composition: replay window and the receipt's verbatim layer list.
//
// Tri-model header gap analysis: the ring was frozen at last-prompt bytes
// during a running turn — exactly when the user most needs to see they're
// approaching the window limit. Now accepts an optional `liveDelta` that
// adds an in-flight estimate (bytes accumulated since the last receipted
// prompt) so the ring creeps upward during long tool-heavy turns.
function ContextMeter({
  snapshot,
  codingModel,
  liveDelta = 0,
}: {
  snapshot: PromptSnapshot;
  codingModel: string | null;
  liveDelta?: number;
}) {
  const [open, setOpen] = useState(false);

  const windowBytes = contextWindowTokens(codingModel) * BYTES_PER_TOKEN;
  // Tri-model: add liveDelta (bytes accumulated since the last receipted
  // prompt) so the ring creeps upward during a running turn instead of
  // freezing at the last send. Capped at 999% to match the old clamp.
  const liveBytes = snapshot.bytes + liveDelta;
  const pct = Math.min(999, Math.round((liveBytes / windowBytes) * 100));
  const tier = METER_TIERS.find((t) => pct < t.max)!;

  // SVG ring: r=6 in a 14px box, circumference ≈ 37.7.
  const C = 2 * Math.PI * 6;
  const fill = C * Math.min(1, pct / 100);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={cn(STATUS_BADGE, "ctx-meter gap-1", tier.cls, tier.color)}
          title={`Context: ${formatBytes(liveBytes)} of ~${formatTokens(windowBytes / BYTES_PER_TOKEN)} window${codingModel ? ` (${codingModel})` : ""}${liveDelta > 0 ? " · ~live estimate" : ""} — click for composition`}
          aria-haspopup="dialog"
          aria-expanded={open}
        >
        <Gauge size={11} aria-hidden="true" />
        <svg className="ctx-ring" width="14" height="14" viewBox="0 0 14 14" aria-hidden="true">
          <circle className="ctx-ring-track" cx="7" cy="7" r="6" />
          <circle
            className="ctx-ring-fill"
            cx="7"
            cy="7"
            r="6"
            strokeDasharray={`${fill} ${C}`}
            data-pct={pct}
          />
        </svg>
        ~{pct}%
        </button>
      </PopoverTrigger>
      <PopoverContent
        side="top"
        align="end"
        sideOffset={6}
        role="dialog"
        aria-label={strings.statusbar.promptCompositionLabel}
        // bg-runs-menu + ctx-meter-popover survive as inert identity markers
        // (e2e background-runs.spec); sizing/padding now utilities.
        className="bg-runs-menu ctx-meter-popover max-h-[60vh] min-w-[260px] overflow-y-auto p-2"
      >
          <div className={CTX_POP_TITLE}>
            last prompt — seq #{snapshot.seq}
          </div>
          <div className="ctx-row flex items-baseline gap-2 px-1 py-0.5 text-micro">
            <span className="ctx-key w-16 shrink-0 text-text-dim">total</span>
            <span className="ctx-val mono min-w-0 text-text">
              {formatBytes(snapshot.bytes)}
              {snapshot.sha16 != null && <span className="ctx-dim text-text-dim"> · sha16 {snapshot.sha16}</span>}
            </span>
          </div>
          <div className="ctx-row flex items-baseline gap-2 px-1 py-0.5 text-micro">
            <span className="ctx-key w-16 shrink-0 text-text-dim">window</span>
            <span className="ctx-val mono min-w-0 text-text">
              ~{formatTokens(contextWindowTokens(codingModel))}
              {codingModel != null && <span className="ctx-dim text-text-dim"> · {codingModel}</span>}
            </span>
          </div>
          {snapshot.replay != null && (
            <>
              <div className="ctx-row flex items-baseline gap-2 px-1 py-0.5 text-micro">
                <span className="ctx-key w-16 shrink-0 text-text-dim">replay</span>
                <span className="ctx-val mono min-w-0 text-text">
                  {formatBytes(snapshot.replay.bytes)}
                  <span className="ctx-dim text-text-dim">
                    {" "}· seqs {snapshot.replay.first_seq}–{snapshot.replay.last_seq}
                  </span>
                </span>
              </div>
              {snapshot.replay.dropped_seqs != null && snapshot.replay.dropped_seqs.length === 2 && (
                <div className="ctx-row flex items-baseline gap-2 px-1 py-0.5 text-micro">
                  <span className="ctx-key w-16 shrink-0 text-text-dim">dropped</span>
                  <span className="ctx-val mono min-w-0 text-text">
                    seqs {snapshot.replay.dropped_seqs[0]}–{snapshot.replay.dropped_seqs[1]}
                    <span className="ctx-dim text-text-dim"> · odo journal range {snapshot.replay.dropped_seqs[0]} {snapshot.replay.dropped_seqs[1]}</span>
                  </span>
                </div>
              )}
            </>
          )}
          {snapshot.recallHeldBack != null && snapshot.recallHeldBack > 0 && (
            <div className="ctx-row flex items-baseline gap-2 px-1 py-0.5 text-micro">
              <span className="ctx-key w-16 shrink-0 text-text-dim">held back</span>
              <span className="ctx-val mono min-w-0 text-text">{snapshot.recallHeldBack} recall note{snapshot.recallHeldBack === 1 ? "" : "s"}</span>
            </div>
          )}
          <div className={cn(CTX_POP_TITLE, "ctx-layers-title border-t border-stroke-tertiary pt-1.5")}>
            injected layers — receipt keys, verbatim ({snapshot.layers.length})
          </div>
          <ul className="ctx-layers m-0 max-h-[160px] list-none overflow-y-auto px-1">
            {snapshot.layers.map((k) => (
              <li key={k} className={cn(CTX_LAYER, "mono")}>{k}</li>
            ))}
            {snapshot.layers.length === 0 && <li className={cn(CTX_LAYER, "ctx-dim text-text-dim")}>no receipt journaled</li>}
          </ul>
      </PopoverContent>
    </Popover>
  );
}

// Wave B #9: review-panel chip — a read-only composition peek for the
// MoA-default-on posture. Edits belong to SettingsPanel; the chip only
// ever shows what the daemon's review list holds.
function PanelChip({ models }: { models: PanelModel[] }) {
  const [open, setOpen] = useState(false);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={cn(STATUS_BADGE, "panel-chip")}
          title={`Review panel: ${models.map((m) => m.model).join(", ")} — composition set in Settings`}
          aria-haspopup="dialog"
          aria-expanded={open}
        >
          <Boxes size={11} aria-hidden="true" />
          Panel ×{models.length}
        </button>
      </PopoverTrigger>
      <PopoverContent
        side="top"
        align="end"
        sideOffset={6}
        role="dialog"
        aria-label={strings.statusbar.reviewPanelLabel}
        className="bg-runs-menu panel-chip-popover max-h-[60vh] min-w-[260px] overflow-y-auto p-2"
      >
        <div className={CTX_POP_TITLE}>{strings.statusbar.reviewPanelReadonlyTitle}</div>
        {models.map((m) => (
           <div key={`${m.model}@${m.provider}`} className="panel-model-row flex items-center gap-2 px-1.5 py-1">
            <span className="panel-model-name mono min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap text-micro text-text" title={`${m.model}@${m.provider}`}>
              {m.model}
            </span>
            <span className="panel-model-provider shrink-0 text-[10px] text-text-dim">{m.provider}</span>
          </div>
        ))}
      </PopoverContent>
    </Popover>
  );
}

// Auto-land pipeline chip (design lock pipeline-indicator-lock, Phase 1):
// current per-diff pipeline status, derived journal-only in pipeline.ts.
// Chip copy is the dominant tracked state; the popover lists every tracked
// diff and jumps to the Review tab. The ≤4s landed flash is gated by the
// journaled created_at, re-evaluated by a local clock tick that dies with
// the last flash — a clock is not a latch: no pipeline fact ever caches
// here, only its journaled expiry crossed against now (derivation drops
// already-expired flashes itself; this tick covers the render-lag window
// between re-derivations, which happen on the ~1.5s poll cadence).
const PIPELINE_PRIORITY: Record<PipelinePhase, number> = {
  blocked: 0,
  suspended: 1,
  revise: 2,
  landing: 3,
  in_flight: 4,
  landed: 5,
  queued: 6,
  hidden: 7,
};

const ACTIVE_PHASES: Record<PipelinePhase, boolean> = {
  queued: true,
  in_flight: true,
  landing: true,
  revise: true,
  blocked: false,
  suspended: false,
  landed: false,
  hidden: false,
};

// Icon vocabulary shared by the chip and its popover rows so a phase reads
// the same in both surfaces: spinner for active phases, Check landed,
// AlertCircle blocked, Ban suspended.
function pipelineIcon(phase: PipelinePhase) {
  if (ACTIVE_PHASES[phase]) return <LoaderCircle size={11} className="spin" aria-hidden="true" />;
  if (phase === "landed") return <Check size={11} aria-hidden="true" />;
  if (phase === "blocked") return <AlertCircle size={11} aria-hidden="true" />;
  return <Ban size={11} aria-hidden="true" />;
}

// Phase tints — was .auto-land-chip.is-blocked/.is-suspended and the
// .auto-land-row.is-* descendant rules in app.css. The is-landed flash
// rule STAYS in app.css, so landed gets its marker but zero utilities.
const PHASE_CHIP_TINT: Partial<Record<PipelinePhase, string>> = {
  blocked: "border-err-text text-err-text",
  suspended: "border-warn text-warn",
};
const PHASE_ROW_TINT: Partial<Record<PipelinePhase, string>> = {
  blocked: "text-err-text",
  suspended: "text-warn",
  landed: "text-ok-text",
};

function PipelineChip({
  states,
  onOpenReview,
}: {
  states: PipelineState[];
  onOpenReview: () => void;
}) {
  const [open, setOpen] = useState(false);

  // Clock only — gates the transient landed window. One-shot timer armed
  // at the nearest expiry; when no flash remains, nothing ticks.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const pending = states
      .filter((s) => s.phase === "landed" && (s.landedUntil ?? 0) > Date.now())
      .map((s) => s.landedUntil!);
    if (pending.length === 0) return;
    const t = window.setTimeout(
      () => setNow(Date.now()),
      Math.min(...pending) - Date.now() + 25,
    );
    return () => window.clearTimeout(t);
  }, [states, now]);

  const visible = states.filter(
    (s) => s.phase !== "landed" || (s.landedUntil ?? 0) > now,
  );
  if (visible.length === 0) return null;
  const dominant = visible.reduce((a, b) =>
    PIPELINE_PRIORITY[a.phase] <= PIPELINE_PRIORITY[b.phase] ? a : b,
  );

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={cn(STATUS_BADGE, "auto-land-chip gap-1", `is-${dominant.phase}`, PHASE_CHIP_TINT[dominant.phase])}
          title={visible.map((s) => `diff ${s.diffId}: ${pipelineLabel(s)}`).join(" · ")}
          aria-haspopup="dialog"
          aria-expanded={open}
        >
          {pipelineIcon(dominant.phase)}
          {pipelineLabel(dominant)}
        </button>
      </PopoverTrigger>
      <PopoverContent
        side="top"
        align="end"
        sideOffset={6}
        role="dialog"
        aria-label="Auto-land pipeline"
        className="bg-runs-menu auto-land-popover min-w-[240px]"
      >
        <div className={CTX_POP_TITLE}>auto-land — current status</div>
        {visible.map((s) => (
          <button
            key={s.diffId}
            type="button"
            className={cn(BG_RUN_ROW, "auto-land-row", `is-${s.phase}`)}
            title={pipelineLabel(s)}
            onClick={() => {
              setOpen(false);
              onOpenReview();
            }}
          >
            <span className={cn("auto-land-row-icon inline-flex shrink-0", PHASE_ROW_TINT[s.phase])} aria-hidden="true">{pipelineIcon(s.phase)}</span>
            <span className={BG_RUN_NAME}>Diff #{s.diffId}</span>
            <span className={cn("auto-land-row-detail ml-auto max-w-[180px] shrink-0 overflow-hidden text-ellipsis whitespace-nowrap text-[length:var(--text-micro)] text-text-dim", PHASE_ROW_TINT[s.phase])}>{pipelineLabel(s)}</span>
          </button>
        ))}
      </PopoverContent>
    </Popover>
  );
}

// P2 (OMP stats): read-only chip showing provider usage limits and
// grievances count. Fetches on mount and every 60s while visible — never
// more frequent (task constraint). The popover lists each provider's
// usage bars and the grievance count. Degrades to "unavailable" when omp
// is missing or timed out.
const OMP_POLL_INTERVAL = 60_000;

// Bar tiers mirror METER_TIERS' intent (was .omp-bar-fill.meter-*).
function usageTier(pct: number): { cls: string; bar: string } {
  if (pct >= 90) return { cls: "meter-err", bar: "bg-err" };
  if (pct >= 70) return { cls: "meter-warn", bar: "bg-warn" };
  return { cls: "meter-ok", bar: "bg-ok" };
}

function formatResetsAt(resetsAt: number): string {
  const now = Date.now();
  const deltaMs = resetsAt - now;
  if (deltaMs <= 0) return "resets soon";
  const hours = Math.floor(deltaMs / 3_600_000);
  const days = Math.floor(hours / 24);
  if (days > 0) return `resets in ${days}d`;
  if (hours > 0) return `resets in ${hours}h`;
  const mins = Math.floor(deltaMs / 60_000);
  return `resets in ${mins}m`;
}

function OmpUsageChip({ projectRoot }: { projectRoot: string | null }) {
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<OmpUsageMerged | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const resp = await ompUsage(projectRoot ?? undefined);
      if (resp.ok) {
        setData(resp.omp_usage ?? null);
      } else {
        setError(resp.error ?? "fetch failed");
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [projectRoot]);

  // One-time fetch on mount + on projectRoot change, so the chip
  // summary (provider count, grievance badge) is visible without a click.
  // The 60s poll runs only while the popover is open (lazy poll).
  useEffect(() => {
    fetchData();
  }, [fetchData]);

  useEffect(() => {
    if (!open) return;
    const timer = window.setInterval(fetchData, OMP_POLL_INTERVAL);
    return () => window.clearInterval(timer);
  }, [open, fetchData]);

  // Derive chip summary: total providers + total grievances.
  const reports = data?.usage?.reports ?? [];
  const grievances = data?.grievances ?? null;
  const grievanceCount = Array.isArray(grievances) ? grievances.length : 0;
  const hasData = reports.length > 0 || grievances != null;
  const hasErrors = (data?.errors?.length ?? 0) > 0 || error != null;
  const unavailable = !hasData && hasErrors;

  if (unavailable && !open) {
    return (
      <button
        type="button"
        className={cn(STATUS_BADGE, "omp-usage-chip omp-unavailable gap-1 opacity-60")}
        title="OMP stats unavailable"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen(true)}
      >
        <Activity size={11} aria-hidden="true" />
        OMP unavailable
      </button>
    );
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={cn(STATUS_BADGE, "omp-usage-chip gap-1")}
          title={`OMP: ${reports.length} provider${reports.length !== 1 ? "s" : ""}${grievanceCount > 0 ? ` · ${grievanceCount} grievance${grievanceCount !== 1 ? "s" : ""}` : ""} — click for details`}
          aria-haspopup="dialog"
          aria-expanded={open}
        >
        <Activity size={11} aria-hidden="true" />
        OMP{reports.length > 0 && ` · ${reports.length}p`}
        {grievanceCount > 0 && <span className="omp-grievance-badge ml-[2px] rounded-sm bg-err-surface px-1 text-[9px] font-semibold leading-[14px] text-err-surface-text" aria-label={`${grievanceCount} grievance${grievanceCount !== 1 ? "s" : ""}`}>{grievanceCount}</span>}
        </button>
      </PopoverTrigger>
      <PopoverContent
        side="top"
        align="end"
        sideOffset={6}
        role="dialog"
        aria-label="OMP usage and grievances"
        className="bg-runs-menu omp-usage-popover max-h-[60vh] min-w-[300px] overflow-y-auto p-2"
      >
          <div className={CTX_POP_TITLE}>OMP usage — read-only (never journaled)</div>
          {loading && data == null && <div className="omp-section omp-loading p-1 text-micro text-text-dim">loading…</div>}
          {error && data == null && <div className="omp-section omp-error-text p-1 text-micro text-err-text">error: {error}</div>}
          {hasErrors && data?.errors && (
            <div className="omp-section omp-error-text p-1 text-micro text-err-text">
              {data.errors.map((e, i) => (
                <div key={i} className="mono omp-error-line break-words whitespace-pre-wrap">{e}</div>
              ))}
            </div>
          )}
          {reports.length === 0 && !loading && !error && (
            <div className="omp-section omp-dim p-1 text-micro text-text-dim">no usage reports</div>
          )}
          {reports.map((report: OmpUsageReport) => (
            <div key={report.provider} className="omp-provider-group border-t border-stroke-tertiary px-1 py-1.5">
              <div className="omp-provider-name mono pb-1 pt-0.5 font-semibold text-micro text-text">{report.provider}</div>
              {(report.limits ?? []).map((limit: OmpUsageLimit) => {
                const rawPct = Number.isFinite(limit.amount.usedFraction) ? limit.amount.usedFraction * 100 : 0;
                const pct = Math.round(Math.min(100, Math.max(0, rawPct)));
                const used = Number.isFinite(limit.amount.used) ? limit.amount.used : 0;
                const limitVal = Number.isFinite(limit.amount.limit) ? limit.amount.limit : 0;
                const remaining = Number.isFinite(limit.amount.remaining) ? limit.amount.remaining : 0;
                const { cls: tierCls, bar: tierBar } = usageTier(pct);
                return (
                  <div key={limit.id} className="omp-limit-row pb-1 pl-2 pt-0.5">
                    <div className="omp-limit-header flex items-baseline justify-between gap-2">
                      <span className="omp-limit-label text-micro text-text-dim">{limit.label}</span>
                      <span className="omp-limit-value mono text-micro text-text">
                        {used}/{limitVal} {limit.amount.unit ?? ""}
                        <span className="omp-limit-pct text-text-dim"> · {pct}%</span>
                      </span>
                    </div>
                    <div className="omp-bar-track mb-0.5 mt-[3px] h-1 overflow-hidden rounded-[2px] bg-bg-tertiary">
                      <div
                        className={cn(OMP_BAR_FILL, tierCls, tierBar)}
                        style={{ width: `${Math.min(100, pct)}%` }}
                      />
                    </div>
                    <div className="omp-limit-resets omp-dim mono pl-2 text-[10px] text-text-dim">
                      {remaining} remaining · {formatResetsAt(limit.window?.resetsAt ?? 0)}
                      {limit.status !== "ok" && <span className="omp-limit-status text-err-text"> · {limit.status}</span>}
                    </div>
                  </div>
                );
              })}
            </div>
          ))}
          <div className="omp-grievances-section flex items-baseline justify-between gap-2 border-t border-stroke-tertiary px-1 pb-0.5 pt-1.5">
            <span className="omp-grievances-label text-micro uppercase tracking-[0.06em] text-text-dim">grievances</span>
            <span className="omp-grievances-count mono font-semibold text-micro text-text">
              {grievances == null ? "unavailable" : `${grievanceCount}`}
            </span>
          </div>
      </PopoverContent>
    </Popover>
  );
}

export default function StatusBar({
  workstreamName,
  conversationId,
  epoch,
  projectRoot,
  agentRunning,
  turnStartedAt,
  backgroundRuns,
  bgNotice,
  onJumpWorkstream,
  lastPrompt,
  events,
  codingModel,
  reviewPanel,
  pipelineStates,
  pendingDiffs,
  wikiNoteCount,
  pendingMemoryProposals,
  onBadgeClick,
}: Props) {
  // Multi-target dropdown (Wave A #1): click opens the run list, a row
  // click jumps. Radix Popover handles dismiss/Esc (Phase 6).
  const [runsOpen, setRunsOpen] = useState(false);
  // GLM: clipboard copy feedback (2s Check, matches MessageBubble convention).
  const [pathCopied, setPathCopied] = useState(false);
  // Tri-model header gap: live turn duration tick.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!agentRunning || turnStartedAt == null) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [agentRunning, turnStartedAt]);

  // A run finishing empties the list — keep the flash clickable target
  // (and its dropdown) out of the render when nothing is left to jump to.
  useEffect(() => {
    if (backgroundRuns.length === 0) setRunsOpen(false);
  }, [backgroundRuns.length]);

  // Truncate project root to basename for brevity.
  const rootShort = projectRoot
    ? projectRoot.replace(/^.*\//, "")
    : null;

  const startedFlash = (bgNotice?.started.length ?? 0) > 0;
  const finished = bgNotice?.finished ?? [];

  return (
    <footer className="app-statusbar flex h-[var(--statusbar-height)] shrink-0 items-center gap-2 border-t border-stroke-tertiary bg-[var(--statusbar-bg)] px-3 font-mono text-micro text-text-dim tabular-nums">
      {/* Left: session facts — click to copy project root path */}
      <button
        type="button"
        className="status-item status-fact-btn inline-flex max-w-[min(40vw,360px)] cursor-pointer items-center gap-[3px] rounded bg-transparent px-1 transition-[background-color] duration-150 hover:bg-bg-input"
        title={pathCopied ? "Copied!" : projectRoot ? `Click to copy: ${projectRoot}` : "No project loaded"}
        onClick={() => {
          if (projectRoot) {
            navigator.clipboard?.writeText(projectRoot)?.then(() => {
              setPathCopied(true);
              setTimeout(() => setPathCopied(false), 2000);
            })?.catch(() => {});
          }
        }}
      >
        {pathCopied && <Check size={10} aria-hidden />}
        {/* Truncating text wrapper: flex parents can't ellipsis raw text
            nodes — min-w-0 lets the span shrink below content width. */}
        <span className="min-w-0 truncate">
          {workstreamName ?? "—"}
          {conversationId != null && ` · #${conversationId}`}
          {` · epoch ${epoch}`}
          {rootShort && ` · ${rootShort}`}
        </span>
      </button>
      <span className="status-spacer flex-1" />
      {/* Center-right: run indicators — foreground spinner, then the
          background chip (the only surface for runs outside the view). */}
      {agentRunning && (
        <span className="status-item status-run inline-flex items-center gap-[3px] overflow-hidden text-ellipsis whitespace-nowrap text-accent-user">
          <LoaderCircle size={11} className="spin" aria-hidden /> running
          {turnStartedAt != null && (
            <span className="status-turn-duration tabular-nums text-text-dim">
              {(() => {
                const secs = Math.max(0, Math.floor((now - turnStartedAt) / 1000));
                const m = Math.floor(secs / 60);
                const s = secs % 60;
                return ` ${m}:${s.toString().padStart(2, "0")}`;
              })()}
            </span>
          )}
        </span>
      )}
      {finished.length > 0 && (
        <span className={cn(STATUS_BADGE, "bg-flash-done")} role="status">
          <Check size={11} /> {finished.join(", ")} finished
        </span>
      )}
      {backgroundRuns.length > 0 && (
        <Popover open={runsOpen} onOpenChange={setRunsOpen}>
          <PopoverTrigger asChild>
            <button
              type="button"
              className={cn(STATUS_BADGE, "status-bg-runs", startedFlash && "bg-flash-new")}
              title={`Background runs: ${backgroundRuns.map((r) => r.name).join(", ")} — click to list`}
              aria-haspopup="menu"
              aria-expanded={runsOpen}
            >
              <span className={WS_DOT_BG} aria-hidden="true" />
              {backgroundRuns.length} background run{backgroundRuns.length > 1 ? "s" : ""}
            </button>
          </PopoverTrigger>
          <PopoverContent
            side="top"
            align="end"
            sideOffset={6}
            role="menu"
            className="bg-runs-menu min-w-[200px]"
          >
            {backgroundRuns.map((run) => (
              <button
                key={run.id}
                type="button"
                role="menuitem"
                className={BG_RUN_ROW}
                onClick={() => {
                  setRunsOpen(false);
                  onJumpWorkstream(run.id);
                }}
              >
                <span className={WS_DOT_BG} aria-hidden="true" />
                <span className={BG_RUN_NAME} title={run.name}>{run.name}</span>
                <span className={BG_RUN_STATE}>still running</span>
              </button>
            ))}
          </PopoverContent>
        </Popover>
      )}
      {/* Wave B: last-prompt pressure meter + review-panel peek, ahead of
          the actionable badges. */}
      {lastPrompt != null && (
        <ContextMeter
          snapshot={lastPrompt}
          codingModel={codingModel}
          liveDelta={agentRunning ? estimateLiveDelta(events, lastPrompt.seq) : 0}
        />
      )}
      {reviewPanel.length > 0 && <PanelChip models={reviewPanel} />}
      {/* Auto-land pipeline: between the panel peek and the actionable
          badges; derivation hands over only currently-visible states, so
          "any states" IS the render gate (design lock). */}
      {pipelineStates.length > 0 && (
        <PipelineChip states={pipelineStates} onOpenReview={() => onBadgeClick("review")} />
      )}
      {/* P2 (OMP stats): read-only provider usage + grievances chip.
          Lazy fetch on open, 60s poll, degrades to "unavailable". */}
      <OmpUsageChip projectRoot={projectRoot} />
      {/* Right: clickable badges */}
      {pendingDiffs > 0 && (
        <button
          type="button"
          className={STATUS_BADGE}
          aria-label={`${pendingDiffs} pending diff${pendingDiffs > 1 ? "s" : ""}`}
          title={`${pendingDiffs} pending diff${pendingDiffs > 1 ? "s" : ""}`}
          onClick={() => onBadgeClick("changes")}
        >
          <GitCompareArrows size={11} /> {pendingDiffs}
        </button>
      )}
      {wikiNoteCount != null && wikiNoteCount > 0 && (
        <button
          type="button"
          className={STATUS_BADGE}
          aria-label={`${wikiNoteCount} wiki notes`}
          title={`${wikiNoteCount} wiki notes`}
          onClick={() => onBadgeClick("wiki")}
        >
          <FileText size={11} /> {wikiNoteCount}
        </button>
      )}
      {pendingMemoryProposals > 0 && (
        <button
          type="button"
          className={STATUS_BADGE}
          aria-label={`${pendingMemoryProposals} pending memory proposals`}
          title={`${pendingMemoryProposals} pending memory proposals`}
          onClick={() => onBadgeClick("memory")}
        >
          <MapPin size={11} /> {pendingMemoryProposals}
        </button>
      )}
    </footer>
  );
}
