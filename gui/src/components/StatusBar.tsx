// M9 Phase 5: StatusBar — 24px bar with session facts, adapter selector,
// clickable badges, and run indicator. Absorbs everything System-ish that
// used to weigh down the sidebar.

interface Props {
  workstreamName: string | null;
  conversationId: number | null;
  epoch: number;
  projectRoot: string | null;
  agentRunning: boolean;
  runningCount: number;
  // Adapter selector (moved from sidebar)
  adapter: string;
  onAdapterChange: (adapter: string) => void;
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
  adapter,
  onAdapterChange,
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
          ⟳ running{runningCount > 1 ? ` (${runningCount})` : ""}
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
          ±{pendingDiffs}
        </button>
      )}
      {wikiNoteCount != null && wikiNoteCount > 0 && (
        <button
          type="button"
          className="status-badge"
          title={`${wikiNoteCount} wiki notes`}
          onClick={() => onBadgeClick("wiki")}
        >
          ❑{wikiNoteCount}
        </button>
      )}
      {pendingMemoryProposals > 0 && (
        <button
          type="button"
          className="status-badge"
          title={`${pendingMemoryProposals} pending memory proposals`}
          onClick={() => onBadgeClick("memory")}
        >
          ◈{pendingMemoryProposals}
        </button>
      )}
      {/* Adapter selector */}
      <select
        className="status-adapter"
        value={adapter}
        onChange={(e) => onAdapterChange(e.target.value)}
        aria-label="Adapter"
        title="Agent adapter"
      >
        <option value="omp">OMP</option>
        <option value="pi">Pi</option>
      </select>
    </footer>
  );
}
