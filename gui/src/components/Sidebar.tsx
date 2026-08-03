import { FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage } from "../api";
import { basename } from "../files";
import type { Conversation, Project, Workstream } from "../types";
import SettingsPanel from "./SettingsPanel";
import WikiBrowser from "./WikiBrowser";

const DISTILL_TOAST_MS = 5000;

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
  // M3: wiki note count for the Memory section (null = unknown; the line is
  // then omitted) and a refresh hook fired when the browser closes.
  wikiNoteCount: number | null;
  onWikiBrowserClosed: () => void;
  // M3 visibility (spec §3c): per-workstream pending-diff counts and the
  // workstreams with a live run, from the daemon's pending_counts poll.
  pendingCounts: Record<number, number>;
  runningWorkstreams: number[];
}

// Shorten an absolute wiki path to "wiki/<note>.md" for display.
function shortWikiPath(path: string): string {
  const marker = "/wiki/";
  const at = path.indexOf(marker);
  return at >= 0 ? path.slice(at + 1) : basename(path);
}

// Left rail: project facts, workstream list with create/switch, the memory
// distiller, and the adapter selector (M1). All state lives in App; this
// component keeps only its own interaction state (form open/busy/toast).
type ToastTimer = number;

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
  onWikiBrowserClosed,
  pendingCounts,
  runningWorkstreams,
}: Props) {
  const [creating, setCreating] = useState(false);
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
  const [distillBusy, setDistillBusy] = useState(false);
  const [distillToast, setDistillToast] = useState<string | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [showWiki, setShowWiki] = useState(false);
  const toastTimer = useRef<ToastTimer | null>(null);

  useEffect(() => {
    return () => {
      if (toastTimer.current) clearTimeout(toastTimer.current);
    };
  }, []);

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
    setDistillToast(null);
    try {
      const wikiPath = await onDistill();
      if (wikiPath) {
        setDistillToast(`Distilled to ${shortWikiPath(wikiPath)}`);
        if (toastTimer.current) clearTimeout(toastTimer.current);
        toastTimer.current = setTimeout(() => setDistillToast(null), DISTILL_TOAST_MS);
      }
    } catch {
      // The error banner in App already carries the message.
    } finally {
      setDistillBusy(false);
    }
  };

  return (
    <aside className="sidebar">
      <div className="sidebar-head">
        <h1 className="sidebar-app">Odo</h1>
        <button
          type="button"
          className="gear-btn"
          title="Settings"
          aria-label="Settings"
          onClick={() => setShowSettings(true)}
        >
          ⚙
        </button>
      </div>
      {showSettings && <SettingsPanel onClose={() => setShowSettings(false)} />}

      <div className="sidebar-section">
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

      <div className="sidebar-section">
        <h2>Memory</h2>
        <button
          type="button"
          className="distill-btn"
          disabled={distillBusy || conversationId == null}
          title="Distill this conversation into a wiki note and start a new epoch"
          onClick={() => void handleDistill()}
        >
          {distillBusy ? "Distilling…" : "Distill"}
        </button>
        {wikiNoteCount != null && <div className="wiki-count">{wikiNoteCount} wiki notes</div>}
        <button
          type="button"
          className="distill-btn"
          disabled={conversationId == null}
          title="Browse this workstream's distilled wiki notes"
          onClick={() => setShowWiki(true)}
        >
          Browse
        </button>
        {distillToast && <div className="distill-toast">{distillToast}</div>}
        {showWiki && conversationId != null && (
          <WikiBrowser
            conversationId={conversationId}
            onClose={() => {
              setShowWiki(false);
              onWikiBrowserClosed();
            }}
          />
        )}
      </div>

      <div className="sidebar-section">
        <h2>Adapter</h2>
        <select
          className="adapter-select"
          value={adapter}
          onChange={(e) => onAdapterChange(e.target.value)}
        >
          <option value="omp">OMP</option>
          <option value="pi">Pi</option>
        </select>
      </div>

      <dl className="sidebar-facts">
        <dt>Project</dt>
        <dd>{project?.name ?? "—"}</dd>
        <dt>Root</dt>
        <dd className="mono truncate" title={project?.root_path ?? ""}>
          {project?.root_path ?? "—"}
        </dd>
        <dt>Conversation</dt>
        <dd>{conversationId != null ? `#${conversationId}` : "—"}</dd>
      </dl>
    </aside>
  );
}
