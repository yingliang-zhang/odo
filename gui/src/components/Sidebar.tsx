import { FormEvent, useEffect, useState } from "react";
import clsx from "clsx";
import { errorMessage } from "../api";
import { ChevronLeft, ChevronRight, FolderPlus, Pencil, Trash2 } from "lucide-react";
import type { ProjectEntry, Workstream } from "../types";

// Phase 3.1: Status dot priority reducer (inspired by Hermes session-row-state.ts).
// One mutually-exclusive state per workstream row, resolved from boolean signals.
type DotState = "needs-input" | "running" | "pending" | "idle";
function dotState(running: boolean, pending: number): DotState {
  if (running) return "running";
  if (pending > 0) return "pending";
  return "idle";
}
const dotClass: Record<DotState, string> = {
  "needs-input": "dot-amber pulse",
  running: "dot-accent pulse",
  pending: "dot-amber",
  idle: "dot-idle",
};

// Phase 3.4: Tail-pin truncation (inspired by Hermes LaneLabel).
// Long branch/workstream names keep their tail visible so `feat-foo-bar-baz`
// and `feat-foo-bar-qux` stay distinguishable.
function TailPin({ label, title }: { label: string; title?: string }) {
  if (label.length <= 20) return <span title={title}>{label}</span>;
  const tailLen = Math.min(12, Math.floor(label.length / 2));
  const head = label.slice(0, label.length - tailLen);
  const tail = label.slice(label.length - tailLen);
  return (
    <span className="tail-pin" title={title ?? label}>
      <span className="tail-head">{head}</span>
      <span className="tail-tail">{tail}</span>
    </span>
  );
}

// Phase 3.5: Persisted collapse state per project (inspired by Hermes useWorkspaceNodeOpen).
const COLLAPSE_KEY = "odo:sidebar:collapsed-projects";
function readCollapsedSet(): Set<string> {
  try {
    const raw = localStorage.getItem(COLLAPSE_KEY);
    return raw ? new Set(JSON.parse(raw)) : new Set();
  } catch {
    return new Set();
  }
}
function writeCollapsedSet(set: Set<string>) {
  try {
    localStorage.setItem(COLLAPSE_KEY, JSON.stringify([...set]));
  } catch { /* ignore quota */ }
}

interface Props {
  projects: ProjectEntry[];
  activeProjectRoot: string | null;
  crossProjectStatus: Record<string, { pending: number; running: boolean }>;
  onSwitchProject: (root: string) => void;
  onAddProject: () => void;
  workstreams: Workstream[];
  workstream: Workstream | null;
  agentRunning: boolean;
  pendingCounts: Record<number, number>;
  runningWorkstreams: number[];
  onSwitchWorkstream: (id: number) => void;
  onCreateWorkstream: (name: string) => Promise<void>;
  onRenameWorkstream: (workstreamId: number, name: string) => Promise<void>;
  onDeleteWorkstream: (workstreamId: number) => Promise<void>;
  collapsed: boolean;
  onToggleCollapsed: () => void;
  // Phase 3.7: lazy-fetch workstreams for non-active projects
  onFetchWorkstreams?: (root: string) => Promise<Workstream[]>;
}

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
  onRenameWorkstream,
  onDeleteWorkstream,
  collapsed,
  onToggleCollapsed,
  onFetchWorkstreams,
}: Props) {
  const [creating, setCreating] = useState(false);
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
  const [renamingId, setRenamingId] = useState<number | null>(null);

  // Phase 3.5: which projects are collapsed in the tree
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(() => {
    // Active project should be expanded by default; others follow saved state
    const saved = readCollapsedSet();
    return saved;
  });

  // Phase 3.7: lazily fetched workstreams for non-active projects
  const [remoteWorkstreams, setRemoteWorkstreams] = useState<Record<string, Workstream[]>>({});

  const toggleProject = (root: string) => {
    setCollapsedProjects((prev) => {
      const next = new Set(prev);
      if (next.has(root)) next.delete(root);
      else next.add(root);
      writeCollapsedSet(next);
      return next;
    });
  };

  // Lazy-fetch workstreams when a non-active project is expanded
  useEffect(() => {
    if (!onFetchWorkstreams) return;
    for (const p of projects) {
      const isActive = p.root === activeProjectRoot;
      const isExpanded = !collapsedProjects.has(p.root);
      if (!isActive && isExpanded && !remoteWorkstreams[p.root]) {
        onFetchWorkstreams(p.root).then(ws => {
          setRemoteWorkstreams(prev => ({ ...prev, [p.root]: ws }));
        }).catch(() => { /* daemon may not be running for this project */ });
      }
    }
  }, [projects, collapsedProjects, activeProjectRoot, remoteWorkstreams, onFetchWorkstreams]);

  const activeEntry = projects.find((p) => p.root === activeProjectRoot);
  const activeLabel = activeEntry?.name ?? projects[0]?.name ?? "Odo";

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

  // Phase 3.6: workstream actions as data (inspired by Hermes useProjectActions)
  const workstreamActions = (w: Workstream) => [
    {
      label: "Rename",
      icon: <Pencil size={12} />,
      onClick: (e: React.MouseEvent) => {
        e.stopPropagation();
        setRenamingId(w.id);
      },
    },
    {
      label: "Delete",
      icon: <Trash2 size={12} />,
      onClick: (e: React.MouseEvent) => {
        e.stopPropagation();
        if (confirm(`Delete workstream "${w.name}"? Pending diffs must be resolved first.`)) {
          void onDeleteWorkstream(w.id);
        }
      },
    },
  ];

  // Render a single workstream row (shared between active and remote projects)
  const renderWorkstream = (w: Workstream, isActiveProject: boolean) => {
    const active = w.id === workstream?.id && isActiveProject;
    const running = runningWorkstreams.includes(w.id) || (active && agentRunning);
    const pending = isActiveProject ? (pendingCounts[w.id] ?? 0) : 0;
    const ds = dotState(running, pending);
    return (
      <li
        key={w.id}
        className={clsx("ws-row", active && "ws-row-active")}
        onClick={() => {
          if (!isActiveProject) onSwitchProject(activeEntry?.root ?? "");
          onSwitchWorkstream(w.id);
        }}
      >
        {renamingId === w.id && isActiveProject ? (
          <form
            className="ws-rename-form"
            onSubmit={(e) => {
              e.preventDefault();
              const name = (e.currentTarget.elements.namedItem("name") as HTMLInputElement)?.value?.trim();
              if (name) void onRenameWorkstream(w.id, name);
              setRenamingId(null);
            }}
          >
            <input
              name="name"
              type="text"
              defaultValue={w.name}
              autoFocus
              onKeyDown={(e) => { if (e.key === "Escape") setRenamingId(null); }}
              className="ws-rename-input"
            />
          </form>
        ) : (
          <>
            <span className={clsx("ws-dot", dotClass[ds])} aria-label={ds} />
            <button
              type="button"
              className={clsx("ws-item", active && "active")}
              onClick={(e) => {
                e.stopPropagation();
                if (!isActiveProject) onSwitchProject(activeEntry?.root ?? "");
                onSwitchWorkstream(w.id);
              }}
            >
              <TailPin label={w.name} title={w.name} />
              <span className="ws-meta">
                {pending > 0 && <span className="ws-pending-pill">{pending}</span>}
              </span>
            </button>
            {isActiveProject && (
              <span className="ws-actions">
                {workstreamActions(w).map((action) => (
                  <button
                    key={action.label}
                    type="button"
                    className="ws-action-btn"
                    title={action.label}
                    aria-label={`${action.label} ${w.name}`}
                    onClick={action.onClick}
                  >
                    {action.icon}
                  </button>
                ))}
              </span>
            )}
          </>
        )}
      </li>
    );
  };

  // Render a project group with its workstreams
  const renderProject = (p: ProjectEntry) => {
    const isActive = p.root === activeProjectRoot;
    const isExpanded = !collapsedProjects.has(p.root);
    const status = isActive
      ? { pending: Object.values(pendingCounts).reduce((a, b) => a + b, 0), running: agentRunning || runningWorkstreams.length > 0 }
      : crossProjectStatus[p.root] ?? { pending: 0, running: false };
    const ds = dotState(status.running, status.pending);
    const wsList = isActive ? workstreams : (remoteWorkstreams[p.root] ?? []);

    return (
      <li key={p.root} className="proj-group">
        <div
          className={clsx("proj-row", isActive && "proj-row-active")}
          onClick={() => {
            if (!isActive) onSwitchProject(p.root);
            toggleProject(p.root);
          }}
        >
          <ChevronRight
            size={12}
            className={clsx("proj-chevron", isExpanded && "proj-chevron-open")}
          />
          <span className={clsx("ws-dot", dotClass[ds])} aria-label={ds} />
          <span className="proj-name" title={p.root}>{p.name}</span>
          {status.pending > 0 && <span className="ws-pending-pill">{status.pending}</span>}
        </div>
        {isExpanded && (
          <ul className="ws-list">
            {isActive && creating && (
              <li className="ws-row ws-create-row">
                <form className="ws-create" onSubmit={handleCreate}>
                  <input
                    type="text"
                    value={newName}
                    onChange={(e) => setNewName(e.target.value)}
                    onKeyDown={(e) => { if (e.key === "Escape") resetCreate(); }}
                    placeholder="workstream name"
                    disabled={createBusy}
                    autoFocus
                  />
                </form>
              </li>
            )}
            {isActive && createError && <li className="ws-error">{createError}</li>}
            {wsList.length === 0 && !isActive && (
              <li className="ws-empty-hint">No workstreams</li>
            )}
            {wsList.map((w) => renderWorkstream(w, isActive))}
            {isActive && (
              <li className="ws-row ws-add-row" onClick={(e) => { e.stopPropagation(); setCreateError(null); setCreating(true); }}>
                <button type="button" className="ws-add-inline" title="New workstream (⌘N)">
                  + New workstream
                </button>
              </li>
            )}
          </ul>
        )}
      </li>
    );
  };

  return (
    <aside className="sidebar" data-sidebar-state={collapsed ? "collapsed" : "expanded"}>
      <div className="sidebar-rail">
        <button
          type="button"
          title="Expand sidebar (⌘B)"
          aria-label="Expand sidebar"
          onClick={onToggleCollapsed}
        >
          <ChevronRight size={14} />
        </button>
      </div>

      <div className="sidebar-sections">
        <div className="sidebar-head">
          <h1 className="sidebar-app">{activeLabel}</h1>
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
            <h2>Projects</h2>
          </div>
          <ul className="proj-tree">
            {projects.map(renderProject)}
            <li className="proj-group proj-add-group">
              <button
                type="button"
                className="proj-add"
                onClick={onAddProject}
              >
                <FolderPlus size={12} /> Add project
              </button>
            </li>
          </ul>
          {creating && (
            <form className="ws-create" onSubmit={handleCreate}>
              <input
                type="text"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                onKeyDown={(e) => { if (e.key === "Escape") resetCreate(); }}
                placeholder="workstream name"
                disabled={createBusy}
                autoFocus
              />
            </form>
          )}
          {createError && <div className="ws-error">{createError}</div>}
        </div>
      </div>
    </aside>
  );
}
