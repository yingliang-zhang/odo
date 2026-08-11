// M9 Phase 5: StatusBar — 24px bar with session facts, clickable badges,
// and run indicator. Absorbs everything System-ish that used to weigh down
// the sidebar.

import { LoaderCircle, GitCompareArrows, FileText, MapPin } from "lucide-react";

// One background run as reported by the daemon's pending_counts —
// workstreams with a live run that is NOT the one in view.
export interface BackgroundRun {
  id: number;
  name: string;
}

interface Props {
  workstreamName: string | null;
  conversationId: number | null;
  epoch: number;
  projectRoot: string | null;
  agentRunning: boolean;
  // Runs in other workstreams of the active project. The chip is the only
  // surface for activity the user cannot see (panel sessions, other ws) —
  // clicking jumps to the first one.
  backgroundRuns: BackgroundRun[];
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
  onJumpWorkstream,
  pendingDiffs,
  wikiNoteCount,
  pendingMemoryProposals,
  onBadgeClick,
}: Props) {
  // Truncate project root to basename for brevity.
  const rootShort = projectRoot
    ? projectRoot.replace(/^.*\//, "")
    : null;

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
      {backgroundRuns.length > 0 && (
        <button
          type="button"
          className="status-badge status-bg-runs"
          title={`Background runs: ${backgroundRuns.map((r) => r.name).join(", ")} — click to open`}
          onClick={() => onJumpWorkstream(backgroundRuns[0].id)}
        >
          <span className="ws-dot dot-bg pulse" aria-hidden="true" />
          {backgroundRuns.length} background run{backgroundRuns.length > 1 ? "s" : ""}
        </button>
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
