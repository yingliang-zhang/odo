import { FormEvent, useEffect, useState } from "react";
import clsx from "clsx";
import { errorMessage } from "../api";
import { ChevronLeft, ChevronRight, FolderPlus, Pencil, Trash2 } from "lucide-react";
import type { ProjectEntry, Workstream } from "../types";

// Phase 3.1: Status dot priority reducer (inspired by Hermes session-row-state.ts).
// One mutually-exclusive state per workstream row, resolved from boolean signals.
type DotState = "running" | "pending" | "idle";
function dotState(running: boolean, pending: number): DotState {
  if (running) return "running";
  if (pending > 0) return "pending";
  return "idle";
}
const dotClass: Record<DotState, string> = {
  running: "dot-accent pulse",
  pending: "dot-amber",
  idle: "dot-idle",
};
const dotLabel: Record<DotState, string> = {
  running: "Running",
  pending: "Pending review",
  idle: "Idle",
};

// Phase 3.4: Tail-pin truncation (inspired by Hermes LaneLabel).
// Long branch/workstream names keep their tail visible so `feat-foo-bar-baz`
// and `feat-foo-bar-qux` stay distinguishable.
// Uses array spread for code-point-aware splitting (handles emoji/surrogates).
function TailPin({ label, title }: { label: string; title?: string }) {
  if (label.length <= 20) return <span title={title}>{label}</span>;
  const chars = [...label];
  if (chars.length <= 20) return <span title={title}>{label}</span>;
  const tailLen = Math.min(12, Math.floor(chars.length / 2));
  const head = chars.slice(0, chars.length - tailLen).join("");
  const tail = chars.slice(chars.length - tailLen).join("");
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
  // Phase 5: single-call handler for clicking a workstream in a non-active
  // project — avoids the two-call race (switch-project + switch-workstream)
  // by bootstrapping target root + wsId in one daemon roundtrip.
  onOpenForeignWorkstream?: (root: string, wsId: number) => void;
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
  onOpenForeignWorkstream,
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

  // Phase 3.5: which projects are collapsed in the tree.
  // Active project is force-expanded; non-active projects follow saved state
  // (default: collapsed unless user previously expanded them).
  const [collapsedProjects, setCollapsedProjects] = useState<Set<string>>(() => {
    const saved = readCollapsedSet();
    // Ensure active project is never collapsed on mount
    if (activeProjectRoot && saved.has(activeProjectRoot)) {
      saved.delete(activeProjectRoot);
      writeCollapsedSet(saved);
    }
    return saved;
  });

  // Phase 3.7: lazily fetched workstreams for non-active projects.
  // `remoteWorkstreams` caches successful fetches; `fetchAttempted` tracks
  // roots that have been fetched (success or failure) to prevent re-fetching
  // at poll cadence when the daemon is unavailable for that project.
  const [remoteWorkstreams, setRemoteWorkstreams] = useState<Record<string, Workstream[]>>({});
  const [fetchAttempted, setFetchAttempted] = useState<Set<string>>(new Set());

  const toggleProject = (root: string) => {
    setCollapsedProjects((prev) => {
      const next = new Set(prev);
      if (next.has(root)) {
        next.delete(root);
        // Reset fetch state on collapse so re-expand retries
        setFetchAttempted((fa) => { const nfa = new Set(fa); nfa.delete(root); return nfa; });
      } else {
        next.add(root);
      }
      return next;
    });
    // Persist after state update (not inside updater — StrictMode safe)
    setCollapsedProjects((prev) => {
      writeCollapsedSet(prev);
      return prev;
    });
  };

  // Lazy-fetch workstreams when a non-active project is expanded
  useEffect(() => {
    if (!onFetchWorkstreams) return;
    for (const p of projects) {
      const isActive = p.root === activeProjectRoot;
      const isExpanded = !collapsedProjects.has(p.root);
      if (!isActive && isExpanded && !fetchAttempted.has(p.root)) {
        setFetchAttempted((prev) => new Set(prev).add(p.root));
        onFetchWorkstreams(p.root).then(ws => {
          setRemoteWorkstreams(prev => ({ ...prev, [p.root]: ws }));
        }).catch(() => {
          // Already marked as attempted; won't retry until collapse→re-expand
        });
      }
    }
  }, [projects, collapsedProjects, activeProjectRoot, fetchAttempted, onFetchWorkstreams]);

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
  const renderWorkstream = (w: Workstream, isActiveProject: boolean, projectRoot: string) => {
    const active = w.id === workstream?.id && isActiveProject;
    const running = runningWorkstreams.includes(w.id) || (active && agentRunning);
    const pending = isActiveProject ? (pendingCounts[w.id] ?? 0) : 0;
    const ds = dotState(running, pending);
    return (
      <li
        key={w.id}
        className={clsx("ws-row", active && "ws-row-active")}
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
          <button
            type="button"
            className={clsx("ws-item", active && "active")}
            onClick={() => {
              if (!isActiveProject) {
                if (onOpenForeignWorkstream) onOpenForeignWorkstream(projectRoot, w.id);
                else { onSwitchProject(projectRoot); onSwitchWorkstream(w.id); }
              } else {
                onSwitchWorkstream(w.id);
              }
            }}
          >
            <span className={clsx("ws-dot", dotClass[ds])} aria-hidden="true" />
            <span className="sr-only">{dotLabel[ds]}</span>
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
        <button
          type="button"
          className={clsx("proj-row", isActive && "proj-row-active")}
          aria-expanded={isExpanded}
          onClick={() => {
            if (!isActive) onSwitchProject(p.root);
            toggleProject(p.root);
          }}
        >
          <ChevronRight
            size={12}
            className={clsx("proj-chevron", isExpanded && "proj-chevron-open")}
            aria-hidden="true"
          />
          <span className={clsx("ws-dot", dotClass[ds])} aria-hidden="true" />
          <span className="sr-only">{dotLabel[ds]}</span>
          <span className="proj-name" title={p.root}>{p.name}</span>
          {status.pending > 0 && <span className="ws-pending-pill">{status.pending}</span>}
        </button>
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
            {wsList.map((w) => renderWorkstream(w, isActive, p.root))}
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
        </div>
      </div>
    </aside>
  );
}
