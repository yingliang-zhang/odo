import { FormEvent, useState } from "react";
import { errorMessage } from "../api";
import type { Workstream } from "../types";

interface Props {
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
          <h1 className="sidebar-app">Odo</h1>
          <button
            type="button"
            className="collapse-btn"
            title="Collapse (⌘B)"
            aria-label="Collapse sidebar"
            onClick={onToggleCollapsed}
          >
            ‹
          </button>
        </div>

        <div className="sidebar-section sidebar-section-grow">
          <div className="sidebar-section-head">
            <h2>Workstreams</h2>
            <button
              type="button"
              className="ws-add"
              title="New workstream"
              aria-label="New workstream"
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
