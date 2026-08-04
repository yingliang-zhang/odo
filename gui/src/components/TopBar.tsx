// M9 Phase 1: TopBar scaffold — 32px bar above the main content area.
// Currently a placeholder showing the Odo brand + workstream name.
// Phase 4 will add action buttons (Distill, Curate, Pin, Wiki, Settings).

interface Props {
  workstreamName: string | null;
  onToggleSidebar: () => void;
  sidebarCollapsed: boolean;
}

export default function TopBar({ workstreamName, onToggleSidebar, sidebarCollapsed }: Props) {
  return (
    <header className="app-topbar">
      <button
        type="button"
        className="topbar-nav-btn"
        aria-label="Toggle sidebar"
        title="Toggle sidebar (⌘B)"
        onClick={onToggleSidebar}
      >
        {sidebarCollapsed ? "☰" : "‹"}
      </button>
      <span className="topbar-brand">Odo</span>
      {workstreamName && (
        <>
          <span className="topbar-sep">·</span>
          <span className="topbar-workstream">{workstreamName}</span>
        </>
      )}
    </header>
  );
}
