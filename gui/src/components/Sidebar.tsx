import { FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage } from "../api";
import { ChevronLeft } from "lucide-react";
import type { ProjectEntry, Workstream } from "../types";

interface Props {
  // M11 P1: the global project registry and the currently bound root. The
  // picker renders only when the registry has entries; an empty registry
  // keeps the static "Odo" title (pre-M11 look, bridged default project).
  projects: ProjectEntry[];
  activeProjectRoot: string | null;
  // M11 P2: cross-project status for non-active projects in the dropdown.
  crossProjectStatus: Record<string, { pending: number; running: boolean }>;
  onSwitchProject: (root: string) => void;
  // M11 F1: add a new project via native folder picker.
  onAddProject: () => void;
  workstreams: Workstream[];
  workstream: Workstream | null;
  agentRunning: boolean;
  // M3 visibility (spec §3c): per-workstream pending-diff counts and the
  // workstreams with a live run, from the daemon's pending_counts poll.
  pendingCounts: Record<number, number>;
  runningWorkstreams: number[];
  onSwitchWorkstream: (id: number) => void;
  onCreateWorkstream: (name: string) => Promise<void>;
  // Belt A: sidebar collapse (⌘B) — collapse is a 48px icon rail, not a
  // hidden 0px column.
  collapsed: boolean;
  onToggleCollapsed: () => void;
}

// Left rail. WORKSTREAMS is the only section — M9 P4 moved every action
// (distill/wiki/curate/pin/ledger/settings) to the TopBar, so the
// collapsed rail carries only the expand button. The sections stay
// mounted but hidden when collapsed so workstream-create form state
// survives the toggle; all panel/toast state lives in App.
export default function Sidebar({
  projects,
  activeProjectRoot,
  crossProjectStatus,
  onSwitchProject,
  onAddProject,
  workstreams,
  workstream,
  agentRunning,
  pendingCounts,
  runningWorkstreams,
  onSwitchWorkstream,
  onCreateWorkstream,
  collapsed,
  onToggleCollapsed,
}: Props) {
  const [creating, setCreating] = useState(false);
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
  // M11 P1: project picker dropdown state; closes on outside click or Esc.
  const [pickerOpen, setPickerOpen] = useState(false);
  const pickerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!pickerOpen) return;
    const onDown = (e: MouseEvent) => {
      if (!pickerRef.current?.contains(e.target as Node)) setPickerOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      // Claim the Esc here: the global shortcut handler listens on window
      // (after document in the bubble path) and would also close the panel
      // or cancel a running agent on the same keypress.
      if (e.key === "Escape") {
        e.stopPropagation();
        setPickerOpen(false);
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [pickerOpen]);

  // Badges exist only for the active project: pending_counts is the only
  // cross-workstream view (spec §3c), and no daemon of an inactive project
  // is ever queried. Other rows show name + root tooltip only.
  const activeEntry = projects.find((p) => p.root === activeProjectRoot);
  const activeLabel = activeEntry?.name ?? projects[0]?.name ?? "Odo";
  const pendingTotal = Object.values(pendingCounts).reduce((a, b) => a + b, 0);
  const runningAny = agentRunning || runningWorkstreams.length > 0;

  const resetCreate = () => {
    setCreating(false);
    setNewName("");
    setCreateError(null);
    setCreateBusy(false);
  };

  const handleCreate = async (e: FormEvent) => {
    e.preventDefault();
    const name = newName.trim();
    if (name === "" || createBusy) return;
    setCreateBusy(true);
    setCreateError(null);
    try {
      await onCreateWorkstream(name);
      resetCreate();
    } catch (err) {
      setCreateError(errorMessage(err));
      setCreateBusy(false);
    }
  };

  return (
    <aside className="sidebar" data-sidebar-state={collapsed ? "collapsed" : "expanded"}>
      {/* Collapsed icon rail: the workstreams themselves have no icon
          representation, so the rail is just the expand button. */}
      <div className="sidebar-rail">
        <button
          type="button"
          title="Expand sidebar (⌘B)"
          aria-label="Expand sidebar"
          onClick={onToggleCollapsed}
        >
          ›
        </button>
      </div>

      <div className="sidebar-sections">
        <div className="sidebar-head">
          {projects.length > 0 ? (
            <div className="project-picker" ref={pickerRef}>
              <button
                type="button"
                className={`project-toggle${pickerOpen ? " open" : ""}`}
                title={activeEntry?.root ?? "Switch project"}
                aria-haspopup="listbox"
                aria-expanded={pickerOpen}
                onClick={() => setPickerOpen((v) => !v)}
              >
                <span aria-hidden="true" className="project-caret">
                  ▾
                </span>
                <span className="project-name">{activeLabel}</span>
                {pendingTotal > 0 && <span className="ws-pending-pill">{pendingTotal}</span>}
                {runningAny && <span className="ws-running-dot" />}
              </button>
              {pickerOpen && (
                <div className="project-menu" role="group" aria-label="Projects">
                <ul role="listbox">
                  {projects.map((p) => {
                    const isActive = p.root === activeProjectRoot;
                    return (
                      <li key={p.root}>
                        <button
                          type="button"
                          role="option"
                          aria-selected={isActive}
                          className={`ws-item${isActive ? " active" : ""}`}
                          title={p.root}
                          onClick={() => {
                            setPickerOpen(false);
                            onSwitchProject(p.root);
                          }}
                        >
                          <span className="ws-name">{p.name}</span>
                          <span className="ws-meta">
                            {isActive && pendingTotal > 0 && (
                              <span className="ws-pending-pill">{pendingTotal}</span>
                            )}
                            {isActive && runningAny && <span className="ws-running-dot" />}
                            {!isActive && crossProjectStatus[p.root]?.pending > 0 && (
                              <span className="ws-pending-pill">{crossProjectStatus[p.root].pending}</span>
                            )}
                            {!isActive && crossProjectStatus[p.root]?.running && (
                              <span className="ws-running-dot" />
                            )}
                          </span>
                        </button>
                      </li>
                    );
                  })}
                </ul>
                {/* M11 F1: add a new project via native folder picker */}
                <button
                  type="button"
                  className="ws-item project-add"
                  onClick={() => {
                    setPickerOpen(false);
                    onAddProject();
                  }}
                >
                  <span className="ws-name">+ Add project…</span>
                </button>
                </div>
              )}
            </div>
          ) : (
            <h1 className="sidebar-app">Odo</h1>
          )}
          <button
            type="button"
            className="collapse-btn"
            title="Collapse (⌘B)"
            aria-label="Collapse sidebar"
            onClick={onToggleCollapsed}
          >
            <ChevronLeft size={14} />
          </button>
        </div>

        <div className="sidebar-section sidebar-section-grow">
          <div className="sidebar-section-head">
            <h2>Workstreams</h2>
            <button
              type="button"
              className="ws-add"
              title="New workstream (⌘N)"
              aria-label="New workstream (⌘N)"
              onClick={() => {
                setCreateError(null);
                setCreating(true);
              }}
            >
              +
            </button>
          </div>
          {creating && (
            <form className="ws-create" onSubmit={handleCreate}>
              <input
                type="text"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") resetCreate();
                }}
                placeholder="workstream name"
                disabled={createBusy}
                autoFocus
              />
            </form>
          )}
          {createError && <div className="ws-error">{createError}</div>}
          <ul className="ws-list">
            {workstreams.map((w) => {
              const active = w.id === workstream?.id;
              // M3: the daemon's running set covers other workstreams; the
              // active one also reports through the poll loop directly.
              const running = runningWorkstreams.includes(w.id) || (active && agentRunning);
              const pending = pendingCounts[w.id] ?? 0;
              return (
                <li key={w.id}>
                  <button
                    type="button"
                    className={`ws-item${active ? " active" : ""}`}
                    onClick={() => onSwitchWorkstream(w.id)}
                  >
                    <span className="ws-name" title={w.name}>
                      {w.name}
                    </span>
                    <span className="ws-meta">
                      {pending > 0 && <span className="ws-pending-pill">{pending}</span>}
                      {running && <span className="ws-running-dot" />}
                      <span className={`ws-status${running ? " running" : ""}`}>
                        {running ? "running" : "idle"}
                      </span>
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      </div>
    </aside>
  );
}
