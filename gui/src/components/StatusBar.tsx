// M9 Phase 1: StatusBar scaffold — 24px bar at the bottom of the window.
// Currently a placeholder showing basic session info.
// Phase 5 will add adapter select, badges, and run indicator.

interface Props {
  workstreamName: string | null;
  conversationId: number | null;
  epoch: number;
  agentRunning: boolean;
}

export default function StatusBar({
  workstreamName,
  conversationId,
  epoch,
  agentRunning,
}: Props) {
  return (
    <footer className="app-statusbar">
      <span className="status-item">
        {workstreamName ?? "—"}
        {conversationId != null && ` · #${conversationId}`}
        {` · epoch ${epoch}`}
      </span>
      <span className="status-spacer" />
      <span className="status-item status-run">
        {agentRunning ? "⟳ running" : "idle"}
      </span>
    </footer>
  );
}
