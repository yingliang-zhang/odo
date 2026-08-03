import { FormEvent, useEffect, useRef, useState } from "react";
import { errorMessage, listTopics } from "../api";
import { basename } from "../files";
import type { Conversation, Project, Workstream } from "../types";
import MemoryReviewPanel from "./MemoryReviewPanel";
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
  // M5 (spec §8): curator pass + pin affordance handlers, and the topic
  // page count (null = unknown; the line is then omitted). All owned by App.
  onCurate: () => Promise<void>;
  onPin: (text: string) => Promise<void>;
  topicCount: number | null;
  // M3 visibility (spec §3c): per-workstream pending-diff counts and the
  // workstreams with a live run, from the daemon's pending_counts poll.
  pendingCounts: Record<number, number>;
  runningWorkstreams: number[];
  // M4 learning (spec §7/§8): pending learner proposals badge, the ephemeral
  // memory_update chip state, and their handlers. All owned by App.
  pendingMemoryProposals: number;
  lastMemoryUpdate: { layer: string; detail?: string } | null;
  onMemoryChipDismiss: () => void;
  onMemoryReviewClosed: () => void;
  // Belt A: sidebar collapse (Cmd+B toggle)
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
  onCurate,
  onPin,
  topicCount,
  pendingCounts,
  runningWorkstreams,
  pendingMemoryProposals,
  lastMemoryUpdate,
  onMemoryChipDismiss,
  onMemoryReviewClosed,
  collapsed,
  onToggleCollapsed,
  onOpenSettings,
}: Props) {
  const [creating, setCreating] = useState(false);
  const [createBusy, setCreateBusy] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
  const [distillBusy, setDistillBusy] = useState(false);
  const [distillToast, setDistillToast] = useState<string | null>(null);
  // M5: the curate busy/toast mirror the distill pair; the pin form keeps
  // its own error line because refusals (e.g. overflow) name the pin text.
  const [curateBusy, setCurateBusy] = useState(false);
  const [curateToast, setCurateToast] = useState<string | null>(null);
  const [pinText, setPinText] = useState("");
  const [pinBusy, setPinBusy] = useState(false);
  const [pinError, setPinError] = useState<string | null>(null);
  const [pinToast, setPinToast] = useState<string | null>(null);
  const [showWiki, setShowWiki] = useState(false);
  const [showMemoryReview, setShowMemoryReview] = useState(false);
  // Review opens the proposal tab; the memory-updated chip opens the reader.
  const [memoryReviewTab, setMemoryReviewTab] = useState<"proposals" | "files">("proposals");
  const toastTimer = useRef<ToastTimer | null>(null);
  const curateToastTimer = useRef<ToastTimer | null>(null);
  const pinToastTimer = useRef<ToastTimer | null>(null);

  useEffect(() => {
    return () => {
      clearTimeout(toastTimer.current ?? undefined);
      clearTimeout(curateToastTimer.current ?? undefined);
      clearTimeout(pinToastTimer.current ?? undefined);
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

  // M4: the chip is ephemeral (App clears its state); it doubles as a
  // shortcut into the review panel's reader tab.
  const handleMemoryChipClick = () => {
    onMemoryChipDismiss();
    setMemoryReviewTab("files");
    setShowMemoryReview(true);
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

  // M5: on success the toast names the topic count. App's topicCount prop
  // may not have re-rendered yet, so read the daemon (the single source of
  // truth) directly for a deterministic number.
  const handleCurate = async () => {
    if (curateBusy) return;
    setCurateBusy(true);
    setCurateToast(null);
    try {
      await onCurate();
      const topics = await listTopics();
      setCurateToast(`Curated ${topics.wiki_notes?.length ?? 0} topics`);
      clearTimeout(curateToastTimer.current ?? undefined);
      curateToastTimer.current = setTimeout(() => setCurateToast(null), DISTILL_TOAST_MS);
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
      setPinToast(`Pinned: ${text}`);
      clearTimeout(pinToastTimer.current ?? undefined);
      pinToastTimer.current = setTimeout(() => setPinToast(null), DISTILL_TOAST_MS);
    } catch (err) {
      setPinError(errorMessage(err));
    } finally {
      setPinBusy(false);
    }
  };

  return (
    <aside className={`sidebar${collapsed ? " sidebar-collapsed" : ""}`}>
      <div className="sidebar-head">
        <h1 className="sidebar-app">Odo</h1>
        <button
          type="button"
          className="collapse-btn"
          title={collapsed ? "Expand (⌘B)" : "Collapse (⌘B)"}
          aria-label="Toggle sidebar"
          onClick={onToggleCollapsed}
        >
          {collapsed ? "›" : "‹"}
        </button>
        <button
          type="button"
          className="gear-btn"
          title="Settings"
          aria-label="Settings"
          onClick={onOpenSettings}
        >
          ⚙
        </button>
      </div>


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
        {pendingMemoryProposals > 0 && (
          <button
            type="button"
            className="mem-propose-btn"
            title="Review the learner's proposed memory rules"
            onClick={() => {
              setMemoryReviewTab("proposals");
              setShowMemoryReview(true);
            }}
          >
            {pendingMemoryProposals} memory proposed — Review
          </button>
        )}
        {lastMemoryUpdate && (
          <>
            <button
              type="button"
              className="mem-chip"
              title={lastMemoryUpdate.detail ?? `${lastMemoryUpdate.layer} memory changed`}
              onClick={handleMemoryChipClick}
            >
              memory updated
            </button>
            {lastMemoryUpdate.detail && (
              <div className="mem-chip-detail" title={lastMemoryUpdate.detail}>
                {lastMemoryUpdate.detail}
              </div>
            )}
          </>
        )}
        <button
          type="button"
          className="distill-btn"
          disabled={conversationId == null}
          title="Browse this workstream's distilled wiki notes"
          onClick={() => setShowWiki(true)}
        >
          Browse
        </button>
        <button
          type="button"
          className="distill-btn"
          disabled={curateBusy || conversationId == null}
          title="Rewrite wiki topic pages + index from all epoch notes"
          onClick={() => void handleCurate()}
        >
          {curateBusy ? "Curating…" : "Curate"}
        </button>
        {topicCount != null && <div className="wiki-count">{topicCount} topics</div>}
        <form className="pin-form" onSubmit={handlePin}>
          <input
            type="text"
            className="pin-input"
            value={pinText}
            onChange={(e) => setPinText(e.target.value)}
            placeholder="remember: Never deploy on Fridays"
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
        {pinToast && <div className="distill-toast">{pinToast}</div>}
        {curateToast && <div className="distill-toast">{curateToast}</div>}
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
        {showMemoryReview && conversationId != null && (
          <MemoryReviewPanel
            conversationId={conversationId}
            workstreamName={workstream?.name}
            initialTab={memoryReviewTab}
            onClose={() => {
              setShowMemoryReview(false);
              onMemoryReviewClosed();
            }}
            onApplied={onMemoryReviewClosed}
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
