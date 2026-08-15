// M9 Phase 5: StatusBar — 24px bar with session facts, clickable badges,
// and run indicator. Absorbs everything System-ish that used to weigh down
// the sidebar.

import { useEffect, useRef, useState } from "react";
import { Check, LoaderCircle, GitCompareArrows, FileText, MapPin, Gauge, Boxes } from "lucide-react";
import {
  BYTES_PER_TOKEN,
  contextWindowTokens,
  formatBytes,
  formatTokens,
} from "../stats";
import type { PanelModel, PromptSnapshot } from "../stats";

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
  // Wave B #9: coding model (window denominator) + review panel list.
  codingModel: string | null;
  reviewPanel: PanelModel[];
  // Clickable badges → open panel on the matching tab
  pendingDiffs: number;
  wikiNoteCount: number | null;
  pendingMemoryProposals: number;
  onBadgeClick: (tab: "changes" | "wiki" | "memory") => void;
}

// One click-away + Escape closer for every StatusBar popover (bg-runs
// menu, context meter, panel picker). The open flag must change on `open`
// — the effect only arms while a menu is visible.
function useCloseOnClickAway(
  open: boolean,
  ref: { current: HTMLElement | null },
  close: () => void,
): void {
  useEffect(() => {
    if (!open) return;
    const onClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) close();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close();
    };
    document.addEventListener("mousedown", onClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, ref, close]);
}

// Ring fill tiers, applied to stroke AND the inline percent text. The
// thresholds are display-only — nothing in the system reads this percent.
// 80+ is where compaction pressure gets real for a typical ratio (kimi-k3
// compactRatio 0.9, glm 0.35 — 80% keeps the middle tier wide enough that
// glm's ~35% trigger doesn't look alarming; see modelspec for the real
// per-model triggers).
const METER_TIERS = [
  { max: 50, cls: "meter-ok" },
  { max: 80, cls: "meter-warn" },
  { max: Infinity, cls: "meter-err" },
];

// Wave B #5: context-pressure ring — the journaled byte total of the last
// prompt vs the coding model's context window (window tokens × ~4
// bytes/token, an estimate, hence "~"). Click expands the journaled
// composition: replay window and the receipt's verbatim layer list.
function ContextMeter({
  snapshot,
  codingModel,
}: {
  snapshot: PromptSnapshot;
  codingModel: string | null;
}) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLSpanElement>(null);
  useCloseOnClickAway(open, wrapRef, () => setOpen(false));

  const windowBytes = contextWindowTokens(codingModel) * BYTES_PER_TOKEN;
  const pct = Math.min(999, Math.round((snapshot.bytes / windowBytes) * 100));
  const tier = METER_TIERS.find((t) => pct < t.max)!.cls;

  // SVG ring: r=6 in a 14px box, circumference ≈ 37.7.
  const C = 2 * Math.PI * 6;
  const fill = C * Math.min(1, pct / 100);

  return (
    <span className="bg-runs-wrap ctx-meter-wrap" ref={wrapRef}>
      <button
        type="button"
        className={`status-badge ctx-meter ${tier}`}
        title={`Last prompt: ${formatBytes(snapshot.bytes)} of ~${formatTokens(windowBytes / BYTES_PER_TOKEN)} window${codingModel ? ` (${codingModel})` : ""} — click for composition`}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
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
      {open && (
        <div className="bg-runs-menu ctx-meter-popover" role="dialog" aria-label="Prompt composition">
          <div className="ctx-pop-title">
            last prompt — seq #{snapshot.seq}
          </div>
          <div className="ctx-row">
            <span className="ctx-key">total</span>
            <span className="ctx-val mono">
              {formatBytes(snapshot.bytes)}
              {snapshot.sha16 != null && <span className="ctx-dim"> · sha16 {snapshot.sha16}</span>}
            </span>
          </div>
          <div className="ctx-row">
            <span className="ctx-key">window</span>
            <span className="ctx-val mono">
              ~{formatTokens(contextWindowTokens(codingModel))}
              {codingModel != null && <span className="ctx-dim"> · {codingModel}</span>}
            </span>
          </div>
          {snapshot.replay != null && (
            <>
              <div className="ctx-row">
                <span className="ctx-key">replay</span>
                <span className="ctx-val mono">
                  {formatBytes(snapshot.replay.bytes)}
                  <span className="ctx-dim">
                    {" "}· seqs {snapshot.replay.first_seq}–{snapshot.replay.last_seq}
                  </span>
                </span>
              </div>
              {snapshot.replay.dropped_seqs != null && snapshot.replay.dropped_seqs.length === 2 && (
                <div className="ctx-row">
                  <span className="ctx-key">dropped</span>
                  <span className="ctx-val mono">
                    seqs {snapshot.replay.dropped_seqs[0]}–{snapshot.replay.dropped_seqs[1]}
                    <span className="ctx-dim"> · odo journal range {snapshot.replay.dropped_seqs[0]} {snapshot.replay.dropped_seqs[1]}</span>
                  </span>
                </div>
              )}
            </>
          )}
          {snapshot.recallHeldBack != null && snapshot.recallHeldBack > 0 && (
            <div className="ctx-row">
              <span className="ctx-key">held back</span>
              <span className="ctx-val mono">{snapshot.recallHeldBack} recall note{snapshot.recallHeldBack === 1 ? "" : "s"}</span>
            </div>
          )}
          <div className="ctx-pop-title ctx-layers-title">
            injected layers — receipt keys, verbatim ({snapshot.layers.length})
          </div>
          <ul className="ctx-layers">
            {snapshot.layers.map((k) => (
              <li key={k} className="ctx-layer mono">{k}</li>
            ))}
            {snapshot.layers.length === 0 && <li className="ctx-layer ctx-dim">no receipt journaled</li>}
          </ul>
        </div>
      )}
    </span>
  );
}

// Wave B #9: review-panel chip — a read-only composition peek for the
// MoA-default-on posture. Edits belong to SettingsPanel; the chip only
// ever shows what the daemon's review list holds.
function PanelChip({ models }: { models: PanelModel[] }) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLSpanElement>(null);
  useCloseOnClickAway(open, wrapRef, () => setOpen(false));

  return (
    <span className="bg-runs-wrap" ref={wrapRef}>
      <button
        type="button"
        className="status-badge panel-chip"
        title={`Review panel: ${models.map((m) => m.model).join(", ")} — composition set in Settings`}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <Boxes size={11} aria-hidden="true" />
        Panel ×{models.length}
      </button>
      {open && (
        <div className="bg-runs-menu panel-chip-popover" role="dialog" aria-label="Review panel">
          <div className="ctx-pop-title">review panel — read-only (⌘, to change)</div>
          {models.map((m) => (
            <div key={`${m.model}@${m.provider}`} className="panel-model-row">
              <span className="panel-model-name mono" title={`${m.model}@${m.provider}`}>
                {m.model}
              </span>
              <span className="panel-model-provider">{m.provider}</span>
            </div>
          ))}
        </div>
      )}
    </span>
  );
}

export default function StatusBar({
  workstreamName,
  conversationId,
  epoch,
  projectRoot,
  agentRunning,
  backgroundRuns,
  bgNotice,
  onJumpWorkstream,
  lastPrompt,
  codingModel,
  reviewPanel,
  pendingDiffs,
  wikiNoteCount,
  pendingMemoryProposals,
  onBadgeClick,
}: Props) {
  // Multi-target dropdown (Wave A #1): click opens the run list, a row
  // click jumps. Click-away + Escape close it (TopBar overflow precedent).
  const [runsOpen, setRunsOpen] = useState(false);
  const runsRef = useRef<HTMLSpanElement>(null);
  useCloseOnClickAway(runsOpen, runsRef, () => setRunsOpen(false));

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
    <footer className="app-statusbar">
      {/* Left: session facts */}
      <span className="status-item" title={projectRoot ?? undefined}>
        {workstreamName ?? "—"}
        {conversationId != null && ` · #${conversationId}`}
        {` · epoch ${epoch}`}
        {rootShort && ` · ${rootShort}`}
      </span>
      <span className="status-spacer" />
      {/* Center-right: run indicators — foreground spinner, then the
          background chip (the only surface for runs outside the view). */}
      {agentRunning && (
        <span className="status-item status-run">
          <LoaderCircle size={11} className="spin" /> running
        </span>
      )}
      {finished.length > 0 && (
        <span className="status-badge bg-flash-done" role="status">
          <Check size={11} /> {finished.join(", ")} finished
        </span>
      )}
      {backgroundRuns.length > 0 && (
        <span className="bg-runs-wrap" ref={runsRef}>
          <button
            type="button"
            className={`status-badge status-bg-runs${startedFlash ? " bg-flash-new" : ""}`}
            title={`Background runs: ${backgroundRuns.map((r) => r.name).join(", ")} — click to list`}
            aria-haspopup="menu"
            aria-expanded={runsOpen}
            onClick={() => setRunsOpen((v) => !v)}
          >
            <span className="ws-dot dot-bg pulse" aria-hidden="true" />
            {backgroundRuns.length} background run{backgroundRuns.length > 1 ? "s" : ""}
          </button>
          {runsOpen && (
            <div className="bg-runs-menu" role="menu">
              {backgroundRuns.map((run) => (
                <button
                  key={run.id}
                  type="button"
                  role="menuitem"
                  className="bg-run-row"
                  onClick={() => {
                    setRunsOpen(false);
                    onJumpWorkstream(run.id);
                  }}
                >
                  <span className="ws-dot dot-bg pulse" aria-hidden="true" />
                  <span className="bg-run-name" title={run.name}>{run.name}</span>
                  <span className="bg-run-state">still running</span>
                </button>
              ))}
            </div>
          )}
        </span>
      )}
      {/* Wave B: last-prompt pressure meter + review-panel peek, ahead of
          the actionable badges. */}
      {lastPrompt != null && (
        <ContextMeter snapshot={lastPrompt} codingModel={codingModel} />
      )}
      {reviewPanel.length > 0 && <PanelChip models={reviewPanel} />}
      {/* Right: clickable badges */}
      {pendingDiffs > 0 && (
        <button
          type="button"
          className="status-badge"
          title={`${pendingDiffs} pending diff${pendingDiffs > 1 ? "s" : ""}`}
          onClick={() => onBadgeClick("changes")}
        >
          <GitCompareArrows size={11} /> {pendingDiffs}
        </button>
      )}
      {wikiNoteCount != null && wikiNoteCount > 0 && (
        <button
          type="button"
          className="status-badge"
          title={`${wikiNoteCount} wiki notes`}
          onClick={() => onBadgeClick("wiki")}
        >
          <FileText size={11} /> {wikiNoteCount}
        </button>
      )}
      {pendingMemoryProposals > 0 && (
        <button
          type="button"
          className="status-badge"
          title={`${pendingMemoryProposals} pending memory proposals`}
          onClick={() => onBadgeClick("memory")}
        >
          <MapPin size={11} /> {pendingMemoryProposals}
        </button>
      )}
    </footer>
  );
}
