import { FormEvent, useEffect, useMemo, useState } from "react";
import clsx from "clsx";
import { errorMessage } from "../api";
import { ChevronLeft, ChevronRight, FolderPlus, Pencil, Trash2 } from "lucide-react";
import type { ProjectEntry, Workstream } from "../types";
import WorkstreamContextMenu from "./WorkstreamContextMenu";

// Phase 3.1: Status dot priority reducer (inspired by Hermes session-row-state.ts).
// One mutually-exclusive state per workstream row, resolved from boolean signals.
// "running" splits by visibility: foreground (the workstream in view, blue)
// vs background (a run anywhere else, purple) — a run the user cannot see
// must still surface, differently.
type DotState = "running" | "background" | "pending" | "idle";
function dotState(fg: boolean, bg: boolean, pending: number): DotState {
  if (fg) return "running";
  if (bg) return "background";
  if (pending > 0) return "pending";
  return "idle";
}
const dotClass: Record<DotState, string> = {
  running: "dot-accent pulse",
  background: "dot-bg pulse",
  pending: "dot-amber",
  idle: "dot-idle",
};
const dotLabel: Record<DotState, string> = {
  running: "Running",
  background: "Running in background",
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
  // M11 F8: drop a stale registry row (never offered on the active project).
  onRemoveProject: (root: string) => void;
  workstreams: Workstream[];
  workstream: Workstream | null;
  agentRunning: boolean;
  pendingCounts: Record<number, number>;
  // W6 (goal queue): per-workstream parked-goal depth (pending_counts.
  // parked_goals) — the daemon's count is the authoritative queue depth.
  parkedCounts: Record<number, number>;
  runningWorkstreams: number[];
  // Wave A (#2): current-activity line for the running foreground row
  // ("Running: <tool>") — App derives it from the polled events, which
  // exist for the conversation in view only; null when the fg ws is idle.
  fgRunLabel: string | null;
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
  onRemoveProject,
  workstreams,
  workstream,
  agentRunning,
  pendingCounts,
  parkedCounts,
  runningWorkstreams,
  fgRunLabel,
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
  // P2: inline delete confirm — replaces native window.confirm
  const [deletingId, setDeletingId] = useState<number | null>(null);
  // Context menu state: which workstream + position + project root.
  const [ctxMenu, setCtxMenu] = useState<{
    ws: Workstream;
    projectRoot: string;
    isActiveProject: boolean;
    x: number;
    y: number;
  } | null>(null);

  // M11 F8: project-row removal arm state (two-step inline confirm, same
  // pattern as workstream delete — no native dialogs).
  const [removingRoot, setRemovingRoot] = useState<string | null>(null);

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

  // GUI Wave A (#2): attention ordering for the ACTIVE project's
  // workstream list — Needs-input (pending diffs) → Working (running)
  // → Idle. A "Done" tier lands with a daemon-observable done signal;
  // pending_counts + running_workstreams can't see one yet, and inventing
  // client-side state for it would lie. Ties keep the daemon's created_at
  // order (stable sort). Remote-project rows and the project tree itself
  // are never reordered (audit guard: rank only inside the ws section).
  const attentionOrdered = useMemo(
    () =>
      [...workstreams].sort((a, b) => {
        const rankOf = (w: Workstream) => {
          const running = runningWorkstreams.includes(w.id) || (w.id === workstream?.id && agentRunning);
          return (pendingCounts[w.id] ?? 0) > 0 ? 0 : running ? 1 : 2;
        };
        return rankOf(a) - rankOf(b);
      }),
    [workstreams, pendingCounts, runningWorkstreams, agentRunning, workstream?.id],
  );

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
        setDeletingId(w.id);
      },
    },
  ];

  // Render a single workstream row (shared between active and remote projects)
  const renderWorkstream = (w: Workstream, isActiveProject: boolean, projectRoot: string) => {
    const active = w.id === workstream?.id && isActiveProject;
    // Remote-project rows have no per-workstream run data (cross-project
    // polls are project aggregates) — they stay idle rather than borrowing
    // the active project's run list (ws ids collide across projects).
    const daemonRunning = isActiveProject && runningWorkstreams.includes(w.id);
    const fg = active && (agentRunning || daemonRunning);
    const pending = isActiveProject ? (pendingCounts[w.id] ?? 0) : 0;
    // W6: parked goals share the pending pill's active-project scoping —
    // remote rows have no per-workstream queue data.
    const parked = isActiveProject ? (parkedCounts[w.id] ?? 0) : 0;
    const ds = dotState(fg, daemonRunning && !active, pending);
    // Wave A (#2): per-row current-activity line while running. The fg row
    // shows App's live label (latest tool); a bg run gets a fixed "still
    // running" — its events are never polled, so anything richer would be
    // fabricated.
    const activity = fg ? fgRunLabel : daemonRunning ? "still running" : null;
    return (
      <li
        key={w.id}
        className={clsx("ws-row", active && "ws-row-active")}
        onContextMenu={(e) => {
          e.preventDefault();
          setCtxMenu({ ws: w, projectRoot, isActiveProject, x: e.clientX, y: e.clientY });
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
            <span className="ws-item-body">
              <span className="ws-item-line">
                <TailPin label={w.name} title={w.name} />
                <span className="ws-meta">
                  {pending > 0 && <span className="ws-pending-pill">{pending}</span>}
                  {parked > 0 && (
                    <span className="ws-parked-pill" title={`${parked} parked goal${parked > 1 ? "s" : ""}`}>
                      {parked}
                    </span>
                  )}
                </span>
              </span>
              {activity && <span className="ws-activity-line">{activity}</span>}
            </span>
          </button>
            {isActiveProject && (
              deletingId === w.id ? (
                <span className="ws-delete-confirm">
                  <span className="ws-delete-confirm-text">Delete?</span>
                  <button
                    type="button"
                    className="ws-action-btn ws-action-delete"
                    title="Confirm delete"
                    aria-label={`Confirm delete ${w.name}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      setDeletingId(null);
                      void onDeleteWorkstream(w.id);
                    }}
                  >
                    <Trash2 size={12} />
                  </button>
                  <button
                    type="button"
                    className="ws-action-btn"
                    title="Cancel"
                    aria-label="Cancel delete"
                    onClick={(e) => {
                      e.stopPropagation();
                      setDeletingId(null);
                    }}
                  >
                    ✕
                  </button>
                </span>
              ) : (
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
              )
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
    // Project aggregate: foreground only when the viewed workstream runs;
    // a run in any other workstream (or anywhere in a remote project) reads
    // as background.
    const fg = isActive
      ? agentRunning || (workstream != null && runningWorkstreams.includes(workstream.id))
      : false;
    const bg = isActive
      ? runningWorkstreams.some((id) => id !== workstream?.id)
      : (crossProjectStatus[p.root]?.running ?? false);
    const pending = isActive
      ? Object.values(pendingCounts).reduce((a, b) => a + b, 0)
      : (crossProjectStatus[p.root]?.pending ?? 0);
    const ds = dotState(fg, bg, pending);
    const wsList = isActive ? attentionOrdered : (remoteWorkstreams[p.root] ?? []);

    return (
      <li key={p.root} className="proj-group">
        <div className="proj-row-head">
        <button
          type="button"
          className={clsx("proj-row", isActive && "proj-row-active")}
          aria-expanded={isExpanded}
          onClick={() => {
            if (!isActive) {
              onSwitchProject(p.root);
              // Switching to a new project: ensure it's expanded, don't toggle
              if (collapsedProjects.has(p.root)) {
                setCollapsedProjects((prev) => {
                  const next = new Set(prev);
                  next.delete(p.root);
                  return next;
                });
                setCollapsedProjects((prev) => { writeCollapsedSet(prev); return prev; });
              }
            } else {
              // Clicking the already-active project: toggle expand/collapse
              toggleProject(p.root);
            }
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
          {pending > 0 && <span className="ws-pending-pill">{pending}</span>}
        </button>
        {!isActive && (
          removingRoot === p.root ? (
            <span className="ws-delete-confirm">
              <span className="ws-delete-confirm-text">Remove?</span>
              <button
                type="button"
                className="ws-action-btn ws-action-delete"
                title="Confirm removal"
                aria-label={`Confirm remove ${p.name}`}
                onClick={(e) => {
                  e.stopPropagation();
                  setRemovingRoot(null);
                  // Drop any persisted collapse state for the dead row too,
                  // so a same-named project re-added later starts clean.
                  setCollapsedProjects((prev) => {
                    if (!prev.has(p.root)) return prev;
                    const next = new Set(prev);
                    next.delete(p.root);
                    writeCollapsedSet(next);
                    return next;
                  });
                  onRemoveProject(p.root);
                }}
              >
                <Trash2 size={12} />
              </button>
              <button
                type="button"
                className="ws-action-btn"
                title="Cancel"
                aria-label="Cancel remove"
                onClick={(e) => {
                  e.stopPropagation();
                  setRemovingRoot(null);
                }}
              >
                ✕
              </button>
            </span>
          ) : (
            <span className="ws-actions">
              <button
                type="button"
                className="ws-action-btn"
                title="Remove from project list (registry only; files untouched)"
                aria-label={`Remove ${p.name} from list`}
                onClick={(e) => {
                  e.stopPropagation();
                  setRemovingRoot(p.root);
                }}
              >
                <Trash2 size={12} />
              </button>
            </span>
          )
        )}
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
            <button
              type="button"
              className="proj-add-btn"
              onClick={onAddProject}
              title="New project"
            >
              <FolderPlus size={12} /> New
            </button>
          </div>
          <ul className="proj-tree">
            {projects.map(renderProject)}
          </ul>
        </div>
      </div>
      {ctxMenu && (
        <WorkstreamContextMenu
          workstream={ctxMenu.ws}
          projectRoot={ctxMenu.projectRoot}
          isActiveProject={ctxMenu.isActiveProject}
          x={ctxMenu.x}
          y={ctxMenu.y}
          onClose={() => setCtxMenu(null)}
          onSwitch={() => {
            if (ctxMenu.isActiveProject) {
              onSwitchWorkstream(ctxMenu.ws.id);
            } else if (onOpenForeignWorkstream) {
              onOpenForeignWorkstream(ctxMenu.projectRoot, ctxMenu.ws.id);
            } else {
              onSwitchProject(ctxMenu.projectRoot);
              onSwitchWorkstream(ctxMenu.ws.id);
            }
          }}
          onRename={() => {
            // For remote projects, switch first then enter rename mode.
            if (!ctxMenu.isActiveProject) {
              if (onOpenForeignWorkstream) onOpenForeignWorkstream(ctxMenu.projectRoot, ctxMenu.ws.id);
              else { onSwitchProject(ctxMenu.projectRoot); onSwitchWorkstream(ctxMenu.ws.id); }
            }
            // Defer setRenamingId so the switch + re-render lands first.
            setTimeout(() => setRenamingId(ctxMenu.ws.id), 50);
          }}
          onDelete={() => {
            // For remote projects, switch first then arm delete confirm.
            if (!ctxMenu.isActiveProject) {
              if (onOpenForeignWorkstream) onOpenForeignWorkstream(ctxMenu.projectRoot, ctxMenu.ws.id);
              else { onSwitchProject(ctxMenu.projectRoot); onSwitchWorkstream(ctxMenu.ws.id); }
            }
            setTimeout(() => setDeletingId(ctxMenu.ws.id), 50);
          }}
        />
      )}
    </aside>
  );
}
