// M9 Phase 5: StatusBar — 24px bar with session facts, clickable badges,
// and run indicator. Absorbs everything System-ish that used to weigh down
// the sidebar.

import { LoaderCircle, GitCompareArrows, FileText, MapPin } from "lucide-react";

interface Props {
  workstreamName: string | null;
  conversationId: number | null;
  epoch: number;
  projectRoot: string | null;
  agentRunning: boolean;
  runningCount: number;
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
  runningCount,
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
      {/* Center-right: run indicator */}
      {agentRunning && (
        <span className="status-item status-run">
          <LoaderCircle size={11} className="spin" /> running{runningCount > 1 ? ` (${runningCount})` : ""}
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
