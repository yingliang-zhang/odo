import { FormEvent, useState } from "react";
import { errorMessage, listTopics } from "../api";
import { basename } from "../files";
import type { Conversation, Project, Workstream } from "../types";
import type { PanelTab } from "./ContextPanel";

// A transient confirmation (distill/curate/pin result) for App's toast
// viewport; the sidebar owns none of the toast lifecycle.
export interface SidebarToast {
  text: string;
  title?: string;
  // Click-through: opens the panel the toast is about (owned by App).
  onClick?: () => void;
}

interface Props {
  project: Project | null;
  workstream: Workstream | null;
  conversationId: Conversation["id"] | null;
  workstreams: Workstream[];
  agentRunning: boolean;
  adapter: string;
  onAdapterChange: (adapter: string) => void;
  onSwitchWorkstream: (id: Workstream["id"]) => void;
  onCreateWorkstream: (name: string) => Promise<void>;
  // Resolves to the wiki path of the distilled note; rejects on failure.
  onDistill: () => Promise<string>;
  // M3: wiki note count for the Knowledge section badge (null = unknown;
  // the badge is then omitted). The browser itself is owned by App (the
  // command palette opens it too) — the row just asks App to open it.
  wikiNoteCount: number | null;
  onOpenWiki: () => void;
  // M5 (spec §8): curator pass + pin affordance handlers, and the topic
  // page count (null = unknown; the badge is then omitted). All owned by App.
  onCurate: () => Promise<void>;
  onPin: (text: string) => Promise<void>;
  topicCount: number | null;
  // M3 visibility (spec §3c): per-workstream pending-diff counts and the
  // workstreams with a live run, from the daemon's pending_counts poll.
  pendingCounts: Record<number, number>;
  runningWorkstreams: number[];
  // M4 learning (spec §7/§8): pending learner-proposal count — badge on
  // the Distill row, and the gate for the Review proposals row.
  pendingMemoryProposals: number;
  onToast: (toast: SidebarToast) => void;
  // M9 P3: memory review is the right panel's Memory tab, owned by App
  // (toasts click through to it, and it must survive the sidebar
  // collapsing to the icon rail). The Ledger row passes "ledger".
  onOpenMemoryReview: (tab: PanelTab) => void;
  // Belt A: sidebar collapse (Cmd+B toggle) — collapse is a 48px icon
  // rail, not a hidden 0px column.
  collapsed: boolean;
  onToggleCollapsed: () => void;
  // Belt A: settings shortcut (Cmd+,)
  onOpenSettings: () => void;
}

// Shorten an absolute wiki path to "wiki/<note>.md" for display.
function shortWikiPath(path: string): string {
  const marker = "/wiki/";
  const at = path.indexOf(marker);
  return at >= 0 ? path.slice(at + 1) : basename(path);
}

// Unified sidebar row: icon + label + optional trailing count badge. The
// Capture/Knowledge/System sections are built entirely from these rows.
function MenuRow({
  icon,
  label,
  badge,
  disabled,
  title,
  onClick,
}: {
  icon: string;
  label: string;
  badge?: number | string | null;
  disabled?: boolean;
  title?: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className="menu-row"
      title={title}
      disabled={disabled}
      onClick={onClick}
    >
      <span className="menu-row-icon" aria-hidden="true">
        {icon}
      </span>
      <span className="menu-row-label">{label}</span>
      {badge != null && <span className="menu-row-badge">{badge}</span>}
    </button>
  );
}

// Left rail. Four sections in a column: Workstreams (grows, always
// visible), Capture and Knowledge (collapsible <details>), System (pinned
// footer). Collapsed mode renders only the icon rail; the sections stay
// mounted but hidden so workstream-create and pin form state survive the
// toggle. All panel/toast state lives in App; this component keeps only
// its own interaction state (form open/busy/error).
export default function Sidebar({
  project,
  workstream,
  conversationId,
  workstreams,
  agentRunning,
  adapter,
  onAdapterChange,
  onSwitchWorkstream,
  onCreateWorkstream,
  onDistill,
  wikiNoteCount,
  onOpenWiki,
  onCurate,
  onPin,
  topicCount,
  pendingCounts,
  runningWorkstreams,
  pendingMemoryProposals,
  onToast,
  onOpenMemoryReview,
  collapsed,
  onToggleCollapsed,
  onOpenSettings,
}: Props) {
  const [creating, setCreating] = useState(false);
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
  const [distillBusy, setDistillBusy] = useState(false);
  const [curateBusy, setCurateBusy] = useState(false);
  const [pinText, setPinText] = useState("");
  const [pinBusy, setPinBusy] = useState(false);
  const [pinError, setPinError] = useState<string | null>(null);

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

  const handleDistill = async () => {
    if (distillBusy) return;
    setDistillBusy(true);
    try {
      const wikiPath = await onDistill();
      if (wikiPath) {
        onToast({ text: `Distilled to ${shortWikiPath(wikiPath)}`, onClick: onOpenWiki });
      }
    } catch {
      // The error banner in App already carries the message.
    } finally {
      setDistillBusy(false);
    }
  };

  // M5: on success the toast names the topic count. App's topicCount prop
  // may not have re-rendered yet, so read the daemon (the single source of
  // truth) directly for a deterministic number.
  const handleCurate = async () => {
    if (curateBusy) return;
    setCurateBusy(true);
    try {
      await onCurate();
      const topics = await listTopics();
      onToast({
        text: `Curated ${topics.wiki_notes?.length ?? 0} topics`,
        onClick: onOpenWiki,
      });
    } catch {
      // The error banner in App already carries the message.
    } finally {
      setCurateBusy(false);
    }
  };

  // M5: store the pin verbatim; on success clear the input and toast. A
  // refusal (overflow names the pin text) shows in the pin error line.
  const handlePin = async (e: FormEvent) => {
    e.preventDefault();
    const text = pinText.trim();
    if (text === "" || pinBusy || conversationId == null) return;
    setPinBusy(true);
    setPinError(null);
    try {
      await onPin(text);
      setPinText("");
      onToast({ text: `Pinned: ${text}` });
    } catch (err) {
      setPinError(errorMessage(err));
    } finally {
      setPinBusy(false);
    }
  };

  return (
    <aside className="sidebar" data-sidebar-state={collapsed ? "collapsed" : "expanded"}>
      {/* Collapsed icon rail; mirrors the section actions with tooltip
          labels so every shortcut stays reachable at 48px. */}
      <div className="sidebar-rail">
        <button
          type="button"
          title="Expand sidebar (⌘B)"
          aria-label="Expand sidebar"
          onClick={onToggleCollapsed}
        >
          ›
        </button>
        <button
          type="button"
          title={`Workstream: ${workstream?.name ?? "—"} (expand to switch)`}
          aria-label="Expand sidebar"
          onClick={onToggleCollapsed}
        >
          ☰
        </button>
        <button
          type="button"
          title="Distill to wiki"
          aria-label="Distill to wiki"
          disabled={distillBusy || conversationId == null}
          onClick={() => void handleDistill()}
        >
          ✦
        </button>
        <button
          type="button"
          title="Browse wiki"
          aria-label="Browse wiki"
          disabled={conversationId == null}
          onClick={onOpenWiki}
        >
          ❑
        </button>
        <button
          type="button"
          title="Settings (⌘,)"
          aria-label="Settings"
          onClick={onOpenSettings}
        >
          ⚙
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

        <details className="sidebar-section" open>
          <summary className="sidebar-section-header">
            Capture
            <span className="caret" aria-hidden="true">
              ▸
            </span>
          </summary>
          <MenuRow
            icon="✦"
            label={distillBusy ? "Distilling…" : "Distill to wiki"}
            badge={pendingMemoryProposals > 0 ? pendingMemoryProposals : null}
            disabled={distillBusy || conversationId == null}
            title="Distill this conversation into a wiki note and start a new epoch"
            onClick={() => void handleDistill()}
          />
          {pendingMemoryProposals > 0 && (
            <MenuRow
              icon="✓"
              label="Review proposals"
              title="Review the learner's proposed memory rules"
              onClick={() => onOpenMemoryReview("memory")}
            />
          )}
        </details>

        <details className="sidebar-section" open>
          <summary className="sidebar-section-header">
            Knowledge
            <span className="caret" aria-hidden="true">
              ▸
            </span>
          </summary>
          <MenuRow
            icon="❑"
            label="Browse wiki"
            badge={wikiNoteCount}
            disabled={conversationId == null}
            title="Browse this workstream's distilled wiki notes"
            onClick={onOpenWiki}
          />
          <MenuRow
            icon="✣"
            label={curateBusy ? "Curating…" : "Curate topics"}
            badge={topicCount}
            disabled={curateBusy || conversationId == null}
            title="Rewrite wiki topic pages + index from all epoch notes"
            onClick={() => void handleCurate()}
          />
          <MenuRow
            icon="▤"
            label="Ledger"
            disabled={conversationId == null}
            title="Open .odo/ledger.md — daemon-written verified metrics (durations, proposals, accept/reject)"
            onClick={() => onOpenMemoryReview("ledger")}
          />
          <form className="pin-form" onSubmit={handlePin}>
            <span className="menu-row-icon" aria-hidden="true">
              ◈
            </span>
            <input
              type="text"
              className="pin-input"
              value={pinText}
              onChange={(e) => setPinText(e.target.value)}
              placeholder="remember: …"
              disabled={conversationId == null}
              title="Store a verbatim pin in .odo/pins.md (always injected, human-owned)"
            />
            <button
              type="submit"
              className="pin-btn"
              disabled={pinBusy || conversationId == null || pinText.trim() === ""}
              title="Store a verbatim pin in .odo/pins.md"
            >
              Pin
            </button>
          </form>
          {pinError && <div className="pin-error">{pinError}</div>}
        </details>

        <div className="sidebar-footer">
          <h2 className="sidebar-section-header">System</h2>
          <MenuRow
            icon="⚙"
            label="Settings"
            title="Settings (⌘,)"
            onClick={onOpenSettings}
          />
          <select
            className="adapter-select"
            aria-label="Adapter"
            value={adapter}
            onChange={(e) => onAdapterChange(e.target.value)}
          >
            <option value="omp">OMP</option>
            <option value="pi">Pi</option>
          </select>
          <dl className="sidebar-facts">
            <dt>Project</dt>
            <dd className="truncate" title={project?.name ?? ""}>{project?.name ?? "—"}</dd>
            <dt>Root</dt>
            <dd className="mono truncate" title={project?.root_path ?? ""}>
              {project?.root_path ?? "—"}
            </dd>
            <dt>Conversation</dt>
            <dd className="truncate" title={conversationId != null ? `#${conversationId}` : ""}>
              {conversationId != null ? `#${conversationId}` : "—"}
            </dd>
          </dl>
        </div>
      </div>
    </aside>
  );
}
