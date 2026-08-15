// M9 Phase 5: StatusBar — 24px bar with session facts, clickable badges,
// and run indicator. Absorbs everything System-ish that used to weigh down
// the sidebar.

import { useEffect, useRef, useState } from "react";
import { Check, LoaderCircle, GitCompareArrows, FileText, MapPin } from "lucide-react";

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
  // Clickable badges → open panel on the matching tab
  pendingDiffs: number;
  wikiNoteCount: number | null;
  pendingMemoryProposals: number;
  onBadgeClick: (tab: "changes" | "wiki" | "memory") => void;
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
  pendingDiffs,
  wikiNoteCount,
  pendingMemoryProposals,
  onBadgeClick,
}: Props) {
  // Multi-target dropdown (Wave A #1): click opens the run list, a row
  // click jumps. Click-away + Escape close it (TopBar overflow precedent).
  const [runsOpen, setRunsOpen] = useState(false);
  const runsRef = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!runsOpen) return;
    const onClick = (e: MouseEvent) => {
      if (runsRef.current && !runsRef.current.contains(e.target as Node)) {
        setRunsOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setRunsOpen(false);
    };
    document.addEventListener("mousedown", onClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [runsOpen]);

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
