import { useCallback, useEffect, useRef, useState } from "react";
import {
  acceptDiff,
  addProject,
  bootstrap,
  cancel,
  createWorkstream,
  curate,
  distill,
  errorMessage,
  fanoutSend,
  getSettings,
  listProjects,
  listTopics,
  listWiki,
  listWorkstreams,
  memoryProposals,
  pendingCounts as fetchPendingCounts,
  pin,
  pollEvents,
  rejectDiff,
  sendMessage,
  unwrap,
  updateSettings,
} from "./api";
import ChatSurface from "./components/ChatSurface";
import CommandPalette, { type PaletteAction } from "./components/CommandPalette";
import ContextPanel, { type PanelTab } from "./components/ContextPanel";
import DiffViewer from "./components/DiffViewer";
import LedgerPanel from "./components/LedgerPanel";
import MemoryPanel from "./components/MemoryPanel";
import SettingsPanel from "./components/SettingsPanel";
import Sidebar from "./components/Sidebar";
import StatusBar from "./components/StatusBar";
import TopBar from "./components/TopBar";
import WikiBrowser from "./components/WikiBrowser";
import { basename } from "./files";
import { notifyRunDone } from "./notify";
import type { BootstrapResponse, Conversation, Diff, OdoEvent, PreviewEvent, Project, ProjectEntry, RunInfo, Workstream } from "./types";

// Polling is the declared transport for M0 (no SSE/WebSocket). M7: the
// interval adapts to run state — fast while the agent streams blocks (the
// preview bubble follows the stream), slow when idle.
const POLL_INTERVAL_RUNNING_MS = 350;
const POLL_INTERVAL_IDLE_MS = 1500;

// Every transient toast — memory chips, retractions, ledger failures, and
// action confirmations (distill/curate/pin) — auto-dismisses after 10 s,
// same cadence as the error banner.
const TOAST_MS = 10_000;        // action confirmations (distill/curate/pin)
const DAEMON_CHIP_MS = 30_000;  // daemon-sourced chips (memory/retraction/ledger) — longer, per M4 spec
// Belt C (§Fix 2): the error banner auto-dismisses after 10 s.
const ERROR_BANNER_MS = 10_000;

function mergeEvents(prev: OdoEvent[], next: OdoEvent[]): OdoEvent[] {
  if (next.length === 0) return prev;
  const seen = new Set(prev.map((e) => e.seq));
  const fresh = next.filter((e) => !seen.has(e.seq));
  if (fresh.length === 0) return prev;
  return [...prev, ...fresh].sort((a, b) => a.seq - b.seq);
}

// M6: a note retraction rides memory_update{layer:"note", cause:"retract"}
// with detail "<oldNote> contradicted by <newNote>: <snippet>".
export interface RetractionInfo {
  oldNote: string;
  newNote: string;
  snippet: string;
}

// A transient confirmation (distill/curate/pin result) for the toast
// viewport; click-through opens the panel the toast is about.
interface ToastPayload {
  text: string;
  title?: string;
  onClick?: () => void;
}

// One toast in the viewport: the payload plus its id.
interface ToastItem extends ToastPayload {
  id: number;
}

function parseRetraction(detail: string): RetractionInfo | null {
  const m = detail.match(/^(\S+) contradicted by (\S+): ([\s\S]*)$/);
  if (!m) return null;
  return { oldNote: m[1], newNote: m[2], snippet: m[3] };
}

// Shorten an absolute wiki path to "wiki/<note>.md" for display.
function shortWikiPath(path: string): string {
  const marker = "/wiki/";
  const at = path.indexOf(marker);
  return at >= 0 ? path.slice(at + 1) : basename(path);
}

export default function App() {
  const [project, setProject] = useState<Project | null>(null);
  // M11 P1: the daemon-owned global registry (read once at boot) and the
  // registry root currently bound in the UI; null = bridge default, which
  // is exactly the pre-M11 single-project behavior.
  const [projects, setProjects] = useState<ProjectEntry[]>([]);
  const [activeProjectRoot, setActiveProjectRoot] = useState<string | null>(
    () => localStorage.getItem("odo-active-project"),
  );
  // M11 P2: cross-project status badges — { root: { pending, running } }
  const [crossProjectStatus, setCrossProjectStatus] = useState<Record<string, { pending: number; running: boolean }>>({});
  const [workstream, setWorkstream] = useState<Workstream | null>(null);
  const [workstreams, setWorkstreams] = useState<Workstream[]>([]);
  const [conversation, setConversation] = useState<Conversation | null>(null);
  const [events, setEvents] = useState<OdoEvent[]>([]);
  const [agentRunning, setAgentRunning] = useState(false);
  // M7: transient streaming preview (never journaled), rebuilt every poll.
  const [preview, setPreview] = useState<PreviewEvent | null>(null);
  const [diff, setDiff] = useState<Diff | null>(null);
  // M8: fan-out per-run state from the daemon (resp.runs + resp.diffs).
  const [runs, setRuns] = useState<RunInfo[]>([]);
  const [diffs, setDiffs] = useState<Diff[]>([]);
  const [adapter, setAdapter] = useState("omp");
  // Belt A: sidebar collapse (⌘B) and the settings modal, lifted out of the
  // Sidebar so ⌘, opens it regardless of sidebar visibility. The collapse
  // state persists across launches.
  const [sidebarCollapsed, setSidebarCollapsed] = useState(
    () => localStorage.getItem("odo-sidebar-collapsed") === "true",
  );
  // M9 Phase 1: right context panel — ⌘J toggle, persisted open/closed + active tab.
  const [panelOpen, setPanelOpen] = useState(
    () => localStorage.getItem("odo-panel-open") === "true",
  );
  const [panelTab, setPanelTab] = useState<PanelTab>(() => {
    const stored = localStorage.getItem("odo-panel-tab");
    const VALID: PanelTab[] = ["changes", "wiki", "memory", "ledger"];
    return stored && (VALID as readonly string[]).includes(stored) ? (stored as PanelTab) : "changes";
  });
  // M9 P3: memory sub-tab for toast click-throughs (files vs proposals).
  const [memorySubTab, setMemorySubTab] = useState<"proposals" | "files">("proposals");
  const [settingsOpen, setSettingsOpen] = useState(false);
  // Belt B: chat search (⌘F) and the command palette (⌘K).
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [paletteOpen, setPaletteOpen] = useState(false);
  // M11 D2: ⌘N opens palette in new-workstream prompt mode directly.
  const [paletteInitialAction, setPaletteInitialAction] = useState<string | undefined>(undefined);
  const [lastDistillPath, setLastDistillPath] = useState<string | null>(null);
  const [booted, setBooted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // M3: wiki note count — TopBar badge + panel wiki tab (null = unknown).
  const [wikiNoteCount, setWikiNoteCount] = useState<number | null>(null);
  // M4: pending learner-proposal count (TopBar distill badge + panel
  // memory tab badge) and the ephemeral
  // "memory updated" chip state. Both live beside wikiNoteCount.
  const [pendingMemoryProposals, setPendingMemoryProposals] = useState(0);
  const [lastMemoryUpdate, setLastMemoryUpdate] = useState<{
    layer: string;
    detail?: string;
  } | null>(null);
  // M6: the contradiction pass toasts when it retracts a stale note; a
  // ledger write failure toasts the same way (rare, journaled).
  const [lastRetraction, setLastRetraction] = useState<RetractionInfo | null>(null);
  const [lastLedgerFailure, setLastLedgerFailure] = useState<string | null>(null);
  // Transient confirmations emitted by the action handlers
  // (distill/curate/pin), rendered beside the chips in the toast viewport;
  // each carries its own 10 s expiry started by pushToast.
  const [toasts, setToasts] = useState<ToastItem[]>([]);
  // M9 P4: the TopBar Distill button's disabled/spinner state. The
  // matching distillingRef pauses the poll loop; this is the UI twin.
  const [distillBusy, setDistillBusy] = useState(false);
  // M3 visibility (spec §3c): project-wide pending-diff counts and running
  // workstreams, refreshed every few poll ticks via pending_counts.
  const [pendingCounts, setPendingCounts] = useState<Record<number, number>>({});
  const [runningWorkstreams, setRunningWorkstreams] = useState<number[]>([]);

  // Belt D: persisted theme applies on mount regardless of which surface
  // (settings dialog vs. fresh launch) wrote it. Absent/invalid values
  // leave <html> untouched, which resolves to the dark :root defaults.
  useEffect(() => {
    const saved = localStorage.getItem("odo-theme");
    if (saved === "dark" || saved === "light") {
      document.documentElement.dataset.theme = saved;
    }
  }, []);

  // Sidebar collapse persistence, mirroring the theme pattern above.
  useEffect(() => {
    localStorage.setItem("odo-sidebar-collapsed", String(sidebarCollapsed));
  }, [sidebarCollapsed]);

  // M11 P1: the selected project survives relaunches; null (no registry
  // entries) removes the key so the next boot uses the bridge default.
  useEffect(() => {
    if (activeProjectRoot != null) {
      localStorage.setItem("odo-active-project", activeProjectRoot);
    } else {
      localStorage.removeItem("odo-active-project");
    }
  }, [activeProjectRoot]);

  // M9 Phase 1: panel open/closed + active tab persistence.
  useEffect(() => {
    localStorage.setItem("odo-panel-open", String(panelOpen));
  }, [panelOpen]);
  useEffect(() => {
    localStorage.setItem("odo-panel-tab", panelTab);
  }, [panelTab]);

  const lastSeqRef = useRef(0);
  const conversationRef = useRef<number | null>(null);
  // Belt A: the forever-installed global shortcut listener reads the current
  // run state through this ref instead of re-registering every flip.
  const agentRunningRef = useRef(false);
  // Belt B: same pattern for the search bar (Esc closes it).
  const searchOpenRef = useRef(false);
  const panelOpenRef = useRef(false);
  // M9 P2: track previous pending-diff count for genuine 0→1 transition,
  // and a bootstrap latch so the first poll after applyBootstrap doesn't
  // auto-open the panel on pre-existing pending diffs.
  const prevDiffsCountRef = useRef(0);
  const bootstrappedRef = useRef(false);
  // Read inside callbacks that must stay referentially stable (recordEvents
  // is a poll-effect dependency; a state dep would rebuild the interval).
  const workstreamNameRef = useRef<string | null>(null);
  const projectRootRef = useRef<string | null>(null);
  const pollTickRef = useRef(0);
  // The daemon serves one connection at a time and distill can block for
  // minutes; polling is paused for the duration instead of queueing up
  // certain timeout failures.
  const distillingRef = useRef(false);
  // M10: auto-distill idle timer. Armed when agentRunning flips false;
  // cancelled on send, workstream/project switch, or manual distill.
  const autoDistillTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // M5: curate blocks daemon-side like distill (one connection at a time),
  // so the poll loop pauses for the curator one-shot the same way.
  const curatingRef = useRef(false);
  // Auto-dismiss timer for the memory_update chip.
  const memoryChipTimer = useRef<number | undefined>(undefined);
  // M6: same ephemeral-chip pattern for the retraction + ledger chips.
  const retractionChipTimer = useRef<number | undefined>(undefined);
  const ledgerChipTimer = useRef<number | undefined>(undefined);
  // Sequence for toast ids (App-level confirmations plus the chips).
  const toastSeqRef = useRef(0);

  const recordEvents = useCallback((incoming: OdoEvent[]) => {
    if (incoming.length === 0) return;
    setEvents((prev) => mergeEvents(prev, incoming));
    lastSeqRef.current = Math.max(lastSeqRef.current, ...incoming.map((e) => e.seq));
    // M3 (spec §3b): finished runs notify when the window is hidden. Only
    // freshly polled events land here — bootstrap replaces wholesale — so
    // history replay cannot re-notify. notifyRunDone swallows its errors.
    for (const e of incoming) {
      if (e.type === "agent_done") {
        void notifyRunDone(workstreamNameRef.current ?? "?", e.payload?.summary ?? "");
      }
      // M4 (spec §8): memory_update pops a toast. Deliberately NOT
      // handled in applyBootstrap — bootstrap replays history wholesale and
      // a stale memory_update must not re-chip. This callback only ever
      // sees freshly polled events.
      if (e.type === "memory_update") {
        const layer = e.payload?.layer ?? "memory";
        // M6 (§12/§13): note-layer retractions and ledger write failures
        // get their own chips; everything else keeps the generic chip.
        if (layer === "note" && e.payload?.cause === "retract") {
          const r = parseRetraction(e.payload?.detail ?? "");
          if (r) {
            setLastRetraction(r);
            clearTimeout(retractionChipTimer.current);
            retractionChipTimer.current = window.setTimeout(
              () => setLastRetraction(null),
              TOAST_MS,
            );
          }
          continue;
        }
        if (layer === "ledger") {
          setLastLedgerFailure(e.payload?.detail ?? "ledger write failed");
          clearTimeout(ledgerChipTimer.current);
          ledgerChipTimer.current = window.setTimeout(
            () => setLastLedgerFailure(null),
            TOAST_MS,
          );
          continue;
        }
        setLastMemoryUpdate({ layer, detail: e.payload?.detail });
        clearTimeout(memoryChipTimer.current);
        memoryChipTimer.current = window.setTimeout(() => setLastMemoryUpdate(null), DAEMON_CHIP_MS);
      }
    }
  }, []);

  // Wiki note + topic counts for the sidebar. Failures degrade to
  // "unknown" (the lines are omitted); they never surface in the error
  // banner.
  const refreshWikiCount = useCallback(async (conversationId: number) => {
    const root = projectRootRef.current;
    try {
      const resp = await listWiki(conversationId, root ?? undefined);
      if (conversationRef.current !== conversationId || projectRootRef.current !== root) return; // switched mid-flight
      setWikiNoteCount(resp.ok ? (resp.wiki_notes?.length ?? 0) : null);
    } catch {
      if (conversationRef.current === conversationId) setWikiNoteCount(null);
    }
  }, []);

  // M4: refresh the pending learner-proposal count (TopBar distill badge).
  // Failures degrade to hidden (0), mirroring refreshWikiCount; they never
  // surface in the error banner.
  const refreshMemoryProposals = useCallback(async (conversationId: number) => {
    const root = projectRootRef.current;
    try {
      const resp = await memoryProposals(conversationId, root ?? undefined);
      if (conversationRef.current !== conversationId || projectRootRef.current !== root) return; // switched mid-flight
      setPendingMemoryProposals(
        (resp.epoch ?? 0) > 0 && resp.proposals ? resp.proposals.length : 0,
      );
    } catch {
      if (conversationRef.current === conversationId && projectRootRef.current === root) setPendingMemoryProposals(0);
    }
  }, []);

  // Whole-context replacement: bootstrap (initial or workstream switch)
  // returns the target conversation's full journal, which supersedes any
  // events accumulated under the previous workstream.
  const applyBootstrap = useCallback(
    (resp: BootstrapResponse) => {
      setWorkstream(resp.workstream ?? null);
      workstreamNameRef.current = resp.workstream?.name ?? null;
      setConversation(resp.conversation ?? null);
      conversationRef.current = resp.conversation?.id ?? null;
      const evs = resp.events ?? [];
      lastSeqRef.current = evs.reduce((max, e) => Math.max(max, e.seq), 0);
      setEvents(evs);
      setAgentRunning(resp.agent_running ?? false);
      setPreview(null); // bootstrap carries no preview; the next poll restores it
      setDiff(resp.diff ?? null);
      setRuns([]);
      setDiffs([]);
      // M9 P2: reset the bootstrap latch so the first poll after a new
      // bootstrap (switch workstream, session restore) doesn't auto-open.
      bootstrappedRef.current = false;
      prevDiffsCountRef.current = 0;
      setLastDistillPath(null);
      const cid = resp.conversation?.id;
      if (cid != null) {
        void refreshWikiCount(cid);
        void refreshMemoryProposals(cid);
      } else {
        setWikiNoteCount(null);
        setPendingMemoryProposals(0);
      }
    },
    [refreshWikiCount, refreshMemoryProposals],
  );

  // Session restore: the project registry is read first (M11 P1) — the
  // persisted selection wins when it still resolves to a registry entry,
  // else the first entry; an empty registry keeps the bridge default.
  // bootstrap then returns project/workstream/conversation plus the full
  // journaled event history and the latest diff.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        // Best-effort: an unreadable/corrupt registry must not block boot —
        // the switcher stays empty and the bridge default project loads.
        let initialRoot: string | null = null;
        try {
          const entries = await listProjects();
          if (cancelled) return;
          setProjects(entries);
          if (entries.length > 0) {
            const stored = localStorage.getItem("odo-active-project");
            initialRoot = entries.find((p) => p.root === stored)?.root ?? entries[0].root;
            setActiveProjectRoot(initialRoot);
          } else {
            setActiveProjectRoot(null);
          }
        } catch {
          // Switcher unavailable; single-project flow continues.
        }
        const resp = unwrap(await bootstrap(initialRoot ?? undefined));
        if (cancelled) return;
        setProject(resp.project ?? null);
        projectRootRef.current = resp.project?.root_path ?? null;
        applyBootstrap(resp);
        if (resp.project) {
          const list = unwrap(await listWorkstreams(resp.project.root_path));
          if (!cancelled) setWorkstreams(list.workstreams ?? []);
        }
        setBooted(true);
      } catch (e) {
        if (!cancelled) setError(`bootstrap failed: ${errorMessage(e)}`);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [applyBootstrap]);

  // K3 review P1: read the daemon's default_adapter on bootstrap so sends
  // use the persisted setting instead of hardcoded "omp" (the adapter
  // selector itself moves to the StatusBar in M9 P5).
  useEffect(() => {
    if (!booted) return;
    let cancelled = false;
    (async () => {
      try {
        const resp = unwrap(await getSettings(projectRootRef.current ?? undefined));
        if (!cancelled && resp.settings?.default_adapter) {
          setAdapter(resp.settings.default_adapter);
        }
      } catch {
        // Degrade silently — the hardcoded "omp" default stays.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [booted]);

  // Poll the daemon for new journal events after the last seen seq.
  useEffect(() => {
    if (!booted) return;
    let inFlight = false;
    const tick = async () => {
      if (distillingRef.current || curatingRef.current) return;
      pollTickRef.current += 1;
      const cid = conversationRef.current;
      const root = projectRootRef.current;
      if (cid == null || inFlight) return;
      inFlight = true;
      try {
        const resp = unwrap(
          await pollEvents(cid, lastSeqRef.current, root ?? undefined),
        );
        if (conversationRef.current !== cid || projectRootRef.current !== root) return; // switched mid-flight
        recordEvents(resp.events ?? []);
        setAgentRunning(resp.agent_running ?? false);
        // M7: transient in-flight block preview — replaced wholesale per
        // poll; renders as the dimmed preview bubble.
        setPreview(resp.preview ?? null);
        // The daemon always reports the latest diff (any status); only a
        // pending one is actionable in the UI.
        if (resp.diff) setDiff(resp.diff);
        // M8: store per-run state from the daemon.
        setRuns(resp.runs ?? []);
        const newDiffs = resp.diffs ?? [];
        setDiffs(newDiffs);
        // M9 P2: auto-open the panel on a genuine 0→1 pending-diff
        // transition (not level-based), only when closed, and only after
        // the first real poll (not bootstrap replay).
        if (!bootstrappedRef.current) {
          // First poll after bootstrap: latch the initial diff count
          // without auto-opening, so session restore stays quiet.
          prevDiffsCountRef.current = newDiffs.length;
          bootstrappedRef.current = true;
        } else if (
          !panelOpenRef.current &&
          prevDiffsCountRef.current === 0 &&
          newDiffs.length > 0
        ) {
          setPanelOpen(true);
          setPanelTab("changes");
        }
        prevDiffsCountRef.current = newDiffs.length;
        // M3 (spec §3c): project-wide visibility every ~4th tick (~6 s
        // idle, ~1.4 s while a run streams).
        // Guarded: a daemon without the command (or any failure) leaves the
        // previous badge state untouched.
        if (pollTickRef.current % 4 === 0 && projectRootRef.current != null) {
          try {
            const counts = await fetchPendingCounts(projectRootRef.current);
            if (counts.ok) {
              const pending: Record<number, number> = {};
              for (const [k, v] of Object.entries(counts.pending_counts ?? {})) {
                const id = Number(k);
                if (Number.isFinite(id)) pending[id] = v;
              }
              setPendingCounts(pending);
              setRunningWorkstreams(counts.running_workstreams ?? []);
            }
          } catch {
            // Stale badges are fine; never disturb the poll loop.
          }
        }
        setError(null);
      } catch (e) {
        if (!distillingRef.current && !curatingRef.current) {
          setError(`poll failed: ${errorMessage(e)}`);
        }
      } finally {
        inFlight = false;
      }
    };
    // M7: 350 ms while the agent runs (block-level preview latency), 1.5 s
    // idle. The interval resets when agentRunning flips.
    const timer = setInterval(
      () => void tick(),
      agentRunning ? POLL_INTERVAL_RUNNING_MS : POLL_INTERVAL_IDLE_MS,
    );
    return () => clearInterval(timer);
  }, [booted, recordEvents, agentRunning]);

  // M11 P2: cross-project status poll — every 5s, fetch pending_counts for
  // all registered projects except the active one (whose counts come from the
  // main poll loop). Failures degrade to hidden badges (no error banner).
  useEffect(() => {
    if (projects.length <= 1) return; // no cross-project badges with ≤1 project
    const timer = setInterval(() => {
      for (const p of projects) {
        if (p.root === activeProjectRoot) continue;
        fetchPendingCounts(p.root).then((counts) => {
          if (!counts.ok) return;
          const pending = Object.values(counts.pending_counts ?? {}).reduce((a, b) => a + b, 0);
          const running = (counts.running_workstreams ?? []).length > 0;
          setCrossProjectStatus((prev) => ({
            ...prev,
            [p.root]: { pending, running },
          }));
        }).catch(() => {});
      }
    }, 5000);
    return () => clearInterval(timer);
  }, [projects, activeProjectRoot]);

  const handleSend = useCallback(
    async (text: string, attachments: string[], steer: boolean) => {
      const cid = conversationRef.current;
      if (cid == null) throw new Error("no active conversation yet");
      cancelAutoDistill(); // M10: user sent a message — cancel pending auto-distill
      try {
        const resp = unwrap(
          await sendMessage(cid, text, attachments, {
            steer,
            adapter,
            projectRoot: projectRootRef.current ?? undefined,
          }),
        );
        if (resp.event) recordEvents([resp.event]);
        // The daemon starts the agent synchronously inside send_message.
        // A steering message does not start a run.
        if (!steer) setAgentRunning(true);
        setError(null);
      } catch (e) {
        setError(`send failed: ${errorMessage(e)}`);
        throw e; // let the composer keep the draft
      }
    },
    [recordEvents, adapter],
  );

  // M2 fan-out: N parallel runs on one prompt. Resolves to the number of
  // runs the daemon started so the composer can show the indicator.
  const handleFanout = useCallback(async (text: string, n: number): Promise<number> => {
    const cid = conversationRef.current;
    if (cid == null) throw new Error("no active conversation yet");
    cancelAutoDistill(); // M10: cancel pending auto-distill
    try {
      const resp = unwrap(await fanoutSend(cid, text, n, projectRootRef.current ?? undefined));
      const started = resp.runs?.length ?? 0;
      if (started > 0) setAgentRunning(true);
      setError(null);
      return started;
    } catch (e) {
      setError(`fan-out failed: ${errorMessage(e)}`);
      throw e; // let the composer keep the draft
    }
  }, []);

  // Belt A: stop the running agent. ok:false ("no active run") is the
  // expected race against a run that finished on its own — the next poll
  // tick reconciles the UI, so only transport failures reach the banner.
  // The daemon-side kill lands asynchronously; agentRunning flips when the
  // drain path journals the terminal event.
  const handleCancel = useCallback(async () => {
    const cid = conversationRef.current;
    if (cid == null) return;
    try {
      await cancel(cid, projectRootRef.current ?? undefined);
      setError(null);
    } catch (e) {
      setError(`cancel failed: ${errorMessage(e)}`);
    }
  }, []);

  // Keep the shortcut listener's view of run state current.
  useEffect(() => {
    agentRunningRef.current = agentRunning;
  }, [agentRunning]);
  useEffect(() => {
    searchOpenRef.current = searchOpen;
  }, [searchOpen]);

  // M9 P3: refresh wiki count when the panel closes or leaves the wiki tab,
  // so notes written while the panel was open don't leave a stale badge.
  useEffect(() => {
    if (!panelOpen || panelTab !== "wiki") {
      if (conversation?.id != null) void refreshWikiCount(conversation.id);
    }
  }, [panelOpen, panelTab]);
  useEffect(() => {
    panelOpenRef.current = panelOpen;
  }, [panelOpen]);

  // Belt A global shortcuts. Modals close themselves on Escape through
  // their own window listeners; the overlay check keeps a bare Escape from
  // also acting on the composer while a dialog is up. Belt B adds ⌘F (chat
  // search) and ⌘K (command palette); Esc closes the search bar before it
  // reaches blur/cancel.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        // Modal/overlay takes priority — don't cancel the agent when closing a dialog
        if (document.querySelector(".settings-overlay, .palette-overlay") != null) return;
        if (searchOpenRef.current) {
          setSearchOpen(false);
          return;
        }
        // M9 P3: panel open takes priority over cancel — matches old modal UX.
        if (panelOpenRef.current) {
          setPanelOpen(false);
          return;
        }
        if (agentRunningRef.current) {
          void handleCancel();
          return;
        }
        (document.activeElement as HTMLElement | null)?.blur();
        return;
      }
      if (!(e.metaKey || e.ctrlKey)) return;
      switch (e.key.toLowerCase()) {
        case "b":
          e.preventDefault();
          setSidebarCollapsed((v) => !v);
          break;
        case "j":
          e.preventDefault();
          setPanelOpen((v) => !v);
          break;
        case ",":
          e.preventDefault();
          setSettingsOpen(true);
          break;
        case "f":
          e.preventDefault();
          setSearchOpen(true);
          break;
        case "k":
          e.preventDefault();
          setPaletteOpen(true);
          setPaletteInitialAction(undefined);
          break;
        case "n":
          e.preventDefault();
          setPaletteOpen(true);
          setPaletteInitialAction("new-workstream");
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [handleCancel]);

  const handleSwitchWorkstream = useCallback(
    async (workstreamId: number) => {
      if (workstreamId === workstream?.id) return;
      cancelAutoDistill(); // M10: switching workstreams cancels pending auto-distill
      try {
        const resp = unwrap(await bootstrap(project?.root_path, workstreamId));
        applyBootstrap(resp);
        setError(null);
      } catch (e) {
        setError(`switch failed: ${errorMessage(e)}`);
      }
    },
    [workstream?.id, project?.root_path, applyBootstrap],
  );

  // M11 P1: full re-bootstrap against another registry project — every
  // conversation-scoped value is replaced wholesale by applyBootstrap, and
  // the bridge spawns that project's daemon on demand. The root flips only
  // after a successful bootstrap; a failure keeps the old project bound and
  // explains itself in the error banner (same posture as workstream switch).
  const handleSwitchProject = useCallback(
    async (root: string) => {
      if (root === activeProjectRoot) return;
      cancelAutoDistill(); // M10: switching projects cancels pending auto-distill
      try {
        const resp = unwrap(await bootstrap(root));
        setActiveProjectRoot(root);
        setProject(resp.project ?? null);
        projectRootRef.current = resp.project?.root_path ?? null;
        applyBootstrap(resp);
        if (resp.project) {
          const list = unwrap(await listWorkstreams(resp.project.root_path));
          setWorkstreams(list.workstreams ?? []);
        }
        setError(null);
      } catch (e) {
        setError(`project switch failed: ${errorMessage(e)}`);
      }
    },
    [activeProjectRoot, applyBootstrap],
  );

  // M11 F1: add a new project via native folder picker. The bridge
  // ensures the daemon is running (auto-registers it), then we refresh
  // the project list and switch to the new project.
  const handleAddProject = useCallback(async () => {
    try {
      const entry = await addProject();
      if (!entry) return; // user cancelled the folder picker
      // Refresh the project list.
      const list = await listProjects();
      setProjects(list);
      // Switch to the new project.
      await handleSwitchProject(entry.root);
    } catch (e) {
      setError(`add project failed: ${errorMessage(e)}`);
    }
  }, [handleSwitchProject]);

  const handleCreateWorkstream = useCallback(
    async (name: string) => {
      const root = project?.root_path;
      if (!root) throw new Error("no project loaded yet");
      const created = unwrap(await createWorkstream(root, name));
      try {
        // Re-list rather than trusting the created row: a duplicate name
        // returns the pre-existing workstream, and the list is the single
        // source of truth for order and contents.
        const list = unwrap(await listWorkstreams(root));
        setWorkstreams(list.workstreams ?? []);
      } catch {
        setWorkstreams((prev) =>
          created.workstream && !prev.some((w) => w.id === created.workstream?.id)
            ? [...prev, created.workstream]
            : prev,
        );
      }
      if (created.workstream) {
        await handleSwitchWorkstream(created.workstream.id);
      }
      setError(null);
    },
    [project?.root_path, handleSwitchWorkstream],
  );

  // M9 P3: every former modal affordance (Wiki, Review proposals, Ledger)
  // now pivots to the right panel on the matching tab — one helper shared
  // by the TopBar buttons, the palette, and the toast click-throughs.
  const openPanelTab = useCallback((tab: PanelTab, memSubTab?: "proposals" | "files") => {
    setPanelOpen(true);
    setPanelTab(tab);
    // Toast click-throughs pass memSubTab="files"; default reset to "proposals".
    setMemorySubTab(memSubTab ?? "proposals");
  }, []);

  // Toast viewport lifecycle: push shows a confirmation for 10 s; either
  // the timer or a click (which also click-throughs to its panel) removes it.
  const dismissToast = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const pushToast = useCallback(
    (toast: ToastPayload) => {
      const id = ++toastSeqRef.current;
      setToasts((prev) => [...prev, { ...toast, id }]);
      window.setTimeout(() => dismissToast(id), TOAST_MS);
    },
    [dismissToast],
  );

  // M9 P4: owns the full distill UX — the TopBar's busy flag and the
  // success toast live here so the palette and TopBar share one path.
  // Never rejects: failures surface in the error banner only.
  const handleDistill = useCallback(async () => {
    const cid = conversationRef.current;
    if (cid == null) return; // the action buttons are disabled without a conversation
    cancelAutoDistill(); // M10: manual distill cancels any pending auto-distill
    distillingRef.current = true;
    setDistillBusy(true);
    try {
      const resp = unwrap(await distill(cid, projectRootRef.current ?? undefined));
      if (resp.epoch != null) {
        const epoch = resp.epoch;
        setConversation((prev) => (prev ? { ...prev, epoch } : prev));
      }
      setLastDistillPath(resp.wiki_path ?? null);
      // The distill just wrote a new note; the TopBar count follows.
      void refreshWikiCount(cid);
      // M4: the learner ran synchronously inside the same distill call, so
      // the proposal batch (if any) is already journaled — re-read it for
      // the TopBar badge.
      void refreshMemoryProposals(cid);
      setError(null);
      if (resp.wiki_path) {
        pushToast({
          text: `Distilled to ${shortWikiPath(resp.wiki_path)}`,
          onClick: () => openPanelTab("wiki"),
        });
      }
    } catch (e) {
      // The error banner carries the message; the toast stays hidden.
      setError(`distill failed: ${errorMessage(e)}`);
    } finally {
      distillingRef.current = false;
      setDistillBusy(false);
    }
  }, [refreshWikiCount, refreshMemoryProposals, pushToast, openPanelTab]);

  // M5: the curator one-shot rewrites every topic page + wiki/index.md
  // from the full epoch-note set. Blocks daemon-side like distill, so the
  // poll loop pauses; counts refresh on success.
  const handleCurate = useCallback(async () => {
    const cid = conversationRef.current;
    if (cid == null) return; // the action buttons are disabled without a conversation
    curatingRef.current = true;
    try {
      unwrap(await curate(cid, projectRootRef.current ?? undefined));
      void refreshWikiCount(cid);
      setError(null);
      // The toast names the topic count, read straight from the daemon
      // (the single source of truth). That read is toast-only: if it
      // fails after a successful pass, skip the toast rather than banner
      // "curate failed" for a pass that worked.
      try {
        const topics = await listTopics(projectRootRef.current ?? undefined);
        pushToast({
          text: `Curated ${topics.wiki_notes?.length ?? 0} topics`,
          onClick: () => openPanelTab("wiki"),
        });
      } catch {
        /* toast skipped */
      }
    } catch (e) {
      // The error banner carries the message; the toast stays hidden.
      setError(`curate failed: ${errorMessage(e)}`);
    } finally {
      curatingRef.current = false;
    }
  }, [refreshWikiCount, pushToast, openPanelTab]);

  // M10: auto-distill — arm idle timer ONLY on genuine agentRunning true→false.
  // Cancel on send, workstream/project switch, or manual distill.
  const cancelAutoDistill = useCallback(() => {
    if (autoDistillTimerRef.current) {
      clearTimeout(autoDistillTimerRef.current);
      autoDistillTimerRef.current = null;
    }
  }, []);

  // Track previous agentRunning to detect true→false transitions only.
  const prevAgentRunningRef = useRef(false);

  useEffect(() => {
    const wasRunning = prevAgentRunningRef.current;
    prevAgentRunningRef.current = agentRunning;

    if (agentRunning) {
      // Agent started — cancel any pending auto-distill.
      cancelAutoDistill();
      return;
    }
    // Only arm on genuine true→false transition, not initial load or switch.
    if (!wasRunning || !conversation?.id) return;
    const armCid = conversation.id;
    let idleSec = 30;
    let cancelled = false;
    getSettings(projectRootRef.current ?? undefined).then((raw) => {
      if (cancelled) return;
      const s = unwrap(raw);
      if (!s.settings || s.settings.auto_distill !== "on_idle") return;
      if (s.settings.auto_distill_idle_seconds) {
        const n = parseInt(s.settings.auto_distill_idle_seconds, 10);
        if (!isNaN(n) && n >= 5) idleSec = n;
      }
      cancelAutoDistill();
      autoDistillTimerRef.current = setTimeout(async () => {
        autoDistillTimerRef.current = null;
        // Re-check: same conversation, no new run, not distilling/curating.
        if (conversationRef.current !== armCid || agentRunningRef.current || distillingRef.current || curatingRef.current) return;
        await handleDistill();
        // Chain auto-curate if enabled.
        if (s.settings?.auto_curate_after_distill === "true" && conversationRef.current === armCid) {
          await handleCurate();
        }
      }, idleSec * 1000);
    }).catch(() => {});
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentRunning, conversation?.id, cancelAutoDistill, handleDistill, handleCurate]);

  // M5: a pin is a verbatim user statement hoovered into .odo/pins.md — no
  // LLM processing, no poll pause (returns immediately). The journaled
  // memory_update{layer:"pins"} arrives on the next poll tick and toasts
  // through the generic recordEvents branch.
  const handlePin = useCallback(
    async (text: string) => {
      const cid = conversationRef.current;
      if (cid == null) throw new Error("no active conversation yet");
      try {
        unwrap(await pin(cid, text, projectRootRef.current ?? undefined));
        setError(null);
        pushToast({ text: `Pinned: ${text}` });
      } catch (e) {
        setError(`pin failed: ${errorMessage(e)}`);
        throw e; // The TopBar pin popover shows the refusal inline.
      }
    },
    [pushToast],
  );

  const handleAccept = useCallback(async (diffId: number) => {
    try {
      const resp = unwrap(await acceptDiff(diffId, projectRootRef.current ?? undefined));
      if (resp.applied) {
        setDiff((d) => (d && d.id === diffId ? { ...d, status: "accepted" } : d));
        setError(null);
      }
    } catch (e) {
      setError(`accept failed: ${errorMessage(e)}`);
    }
  }, []);

  const handleReject = useCallback(async (diffId: number) => {
    try {
      unwrap(await rejectDiff(diffId, projectRootRef.current ?? undefined));
      setDiff((d) => (d && d.id === diffId ? { ...d, status: "rejected" } : d));
      setError(null);
    } catch (e) {
      setError(`reject failed: ${errorMessage(e)}`);
    }
  }, []);

  // M4: clicking the toast dismisses it (the auto-dismiss also runs on a
  // 10 s timer); closing/applying in the review panel re-reads the badge.
  const handleMemoryChipDismiss = useCallback(() => {
    if (memoryChipTimer.current !== undefined) {
      clearTimeout(memoryChipTimer.current);
      memoryChipTimer.current = undefined;
    }
    setLastMemoryUpdate(null);
  }, []);

  // M6: same dismiss pattern for the retraction + ledger chips.
  const handleRetractionDismiss = useCallback(() => {
    clearTimeout(retractionChipTimer.current);
    retractionChipTimer.current = undefined;
    setLastRetraction(null);
  }, []);

  const handleLedgerFailureDismiss = useCallback(() => {
    clearTimeout(ledgerChipTimer.current);
    ledgerChipTimer.current = undefined;
    setLastLedgerFailure(null);
  }, []);

  const handleMemoryReviewClosed = useCallback(() => {
    const cid = conversationRef.current;
    if (cid != null) void refreshMemoryProposals(cid);
  }, [refreshMemoryProposals]);

  // Drop the chips' dismiss timers on unmount.
  useEffect(() => {
    return () => {
      clearTimeout(memoryChipTimer.current);
      clearTimeout(retractionChipTimer.current);
      clearTimeout(ledgerChipTimer.current);
    };
  }, []);

  // Belt C (§Fix 2): the error banner auto-dismisses; a new error
  // restarts the clock (effect cleanup drops the previous timer), and
  // manual dismiss / unmount clear it the same way.
  // Guard: don't auto-dismiss bootstrap failures — if the daemon is
  // unreachable, the error must persist so the user can retry.
  useEffect(() => {
    if (error === null) return;
    if (!booted) return; // bootstrap error — keep it visible
    const timer = window.setTimeout(() => setError(null), ERROR_BANNER_MS);
    return () => clearTimeout(timer);
  }, [error, booted]);

  if (!booted && !error) {
    return <div className="app-loading">Connecting to the Odo daemon…</div>;
  }

  // Belt B (⌘K): every action rides a handler that already exists above,
  // so the palette is pure UI. Conversation-bound actions are disabled
  // (greyed, skipped by arrows) until a conversation exists; Cancel Run
  // only appears mid-run. Prompt actions switch the palette into
  // text-entry mode (workstream name, pin text).
  const paletteActions: PaletteAction[] = [
    {
      id: "new-workstream",
      name: "New Workstream",
      icon: "＋",
      shortcut: "⌘N",
      prompt: "Workstream name…",
      onRun: async (name) => {
        try {
          await handleCreateWorkstream(name);
        } catch (e) {
          setError(`create workstream failed: ${errorMessage(e)}`);
        }
      },
    },
    {
      id: "distill",
      name: "Distill to Wiki",
      icon: "✦",
      disabled: conversation == null,
      onRun: () => handleDistill(),
    },
    {
      id: "curate",
      name: "Curate Topics",
      icon: "✣",
      disabled: conversation == null,
      onRun: () => handleCurate(),
    },
    {
      id: "pin",
      name: "Pin Memory",
      icon: "◈",
      prompt: "remember: …",
      disabled: conversation == null,
      onRun: (text) => handlePin(text),
    },
    {
      id: "open-wiki",
      name: "Open Wiki",
      icon: "❑",
      disabled: conversation == null,
      onRun: () => openPanelTab("wiki"),
    },
    {
      id: "open-settings",
      name: "Open Settings",
      icon: "⚙",
      shortcut: "⌘,",
      onRun: () => setSettingsOpen(true),
    },
    ...(agentRunning
      ? [
          {
            id: "cancel-run",
            name: "Cancel Run",
            icon: "■",
            shortcut: "Esc",
            onRun: () => handleCancel(),
          } satisfies PaletteAction,
        ]
      : []),
    {
      id: "toggle-sidebar",
      name: "Toggle Sidebar",
      icon: "⇤",
      shortcut: "⌘B",
      onRun: () => setSidebarCollapsed((v) => !v),
    },
    {
      id: "toggle-panel",
      name: "Toggle Context Panel",
      icon: "⫿",
      shortcut: "⌘J",
      onRun: () => setPanelOpen((v) => !v),
    },
    {
      id: "search-chat",
      name: "Search Chat",
      icon: "⌕",
      shortcut: "⌘F",
      onRun: () => setSearchOpen(true),
    },
  ];

  return (
    <div className="app-shell">
      <TopBar
        workstreamName={workstream?.name ?? null}
        onToggleSidebar={() => setSidebarCollapsed((v) => !v)}
        sidebarCollapsed={sidebarCollapsed}
        onDistill={handleDistill}
        onOpenWiki={() => openPanelTab("wiki")}
        onCurate={handleCurate}
        onPin={handlePin}
        onOpenSettings={() => setSettingsOpen(true)}
        onOpenLedger={() => openPanelTab("ledger")}
        wikiNoteCount={wikiNoteCount}
        pendingMemoryProposals={pendingMemoryProposals}
        distillBusy={distillBusy}
        actionsDisabled={conversation == null}
      />
      <div className="app-body">
      <Sidebar
        projects={projects}
        activeProjectRoot={activeProjectRoot}
        crossProjectStatus={crossProjectStatus}
        onSwitchProject={(root) => void handleSwitchProject(root)}
        onAddProject={() => void handleAddProject()}
        workstreams={workstreams}
        workstream={workstream}
        agentRunning={agentRunning}
        pendingCounts={pendingCounts}
        runningWorkstreams={runningWorkstreams}
        onSwitchWorkstream={handleSwitchWorkstream}
        onCreateWorkstream={handleCreateWorkstream}
        collapsed={sidebarCollapsed}
        onToggleCollapsed={() => setSidebarCollapsed((v) => !v)}
      />
      {settingsOpen && (
        <SettingsPanel
          projectRoot={project?.root_path ?? null}
          onClose={() => setSettingsOpen(false)}
          onSaved={() => {
            // M9 P4: re-read adapter from daemon after settings save
            // (the sidebar adapter selector was removed; SettingsPanel
            // is now the only mid-session path to change it until P5
            // adds the StatusBar selector).
            void (async () => {
              try {
                const resp = unwrap(await getSettings(projectRootRef.current ?? undefined));
                if (resp.settings?.default_adapter) setAdapter(resp.settings.default_adapter);
              } catch { /* degrade silently */ }
            })();
          }}
        />
      )}
      {paletteOpen && <CommandPalette actions={paletteActions} onClose={() => setPaletteOpen(false)} initialActionId={paletteInitialAction} />}
      <main className="app-main">
        {/* Toast viewport: the transient chips the sidebar used to host,
            plus sidebar confirmations. Click-through opens the panel the
            toast is about; every toast auto-dismisses after TOAST_MS. */}
        <div className="toast-viewport">
          {lastMemoryUpdate && (
            <button
              type="button"
              className="toast-item"
              title={lastMemoryUpdate.detail ?? `${lastMemoryUpdate.layer} memory changed`}
              onClick={() => {
                handleMemoryChipDismiss();
                openPanelTab("memory", "files");
              }}
            >
              memory updated
              {lastMemoryUpdate.detail && (
                <span className="toast-detail">{lastMemoryUpdate.detail}</span>
              )}
            </button>
          )}
          {lastRetraction && (
            <button
              type="button"
              className="toast-item"
              title={`${lastRetraction.oldNote} contradicted by ${lastRetraction.newNote}: ${lastRetraction.snippet}`}
              onClick={() => {
                handleRetractionDismiss();
                openPanelTab("memory", "files");
              }}
            >
              ⚠ {lastRetraction.oldNote} retracted (contradicts {lastRetraction.newNote})
            </button>
          )}
          {lastLedgerFailure && (
            <button
              type="button"
              className="toast-item"
              title={lastLedgerFailure}
              onClick={() => {
                handleLedgerFailureDismiss();
                openPanelTab("ledger");
              }}
            >
              ⚠ ledger write failed
            </button>
          )}
          {toasts.map((t) => (
            <button
              key={t.id}
              type="button"
              className="toast-item"
              title={t.title}
              onClick={() => {
                dismissToast(t.id);
                t.onClick?.();
              }}
            >
              {t.text}
            </button>
          ))}
        </div>
        {error && (
          <div className="error-banner" role="alert">
            <span>{error}</span>
            <button
              type="button"
              className="dismiss-btn"
              aria-label="Dismiss error"
              onClick={() => setError(null)}
            >
              ×
            </button>
          </div>
        )}
        <ChatSurface
          events={events}
          agentRunning={agentRunning}
          preview={preview}
          runs={runs}
          sendDisabled={!booted}
          onSend={handleSend}
          onFanout={handleFanout}
          onCancel={handleCancel}
          epoch={conversation?.epoch ?? 1}
          distilledTo={lastDistillPath}
          searchOpen={searchOpen}
          searchQuery={searchQuery}
          onSearchQueryChange={setSearchQuery}
          onSearchClose={() => setSearchOpen(false)}
        />
      </main>
      <ContextPanel
        open={panelOpen}
        onClose={() => setPanelOpen(false)}
        activeTab={panelTab}
        onTabChange={setPanelTab}
        changesBadge={diffs.length > 0 ? diffs.length : undefined}
        wikiBadge={wikiNoteCount ?? undefined}
        memoryBadge={pendingMemoryProposals > 0 ? pendingMemoryProposals : undefined}
      >
        {panelTab === "changes" && (diffs.length > 0
          ? diffs.map((d) => (
              <DiffViewer
                key={`${projectRootRef.current ?? ""}:${d.id}`}
                diff={d}
                runLabel={d.run_index != null ? `Run ${d.run_index + 1}` : undefined}
                onAccept={handleAccept}
                onReject={handleReject}
                projectRoot={project?.root_path ?? null}
              />
            ))
          : diff
            ? <DiffViewer diff={diff} onAccept={handleAccept} onReject={handleReject} projectRoot={project?.root_path ?? null} />
            : <div className="panel-empty">No pending diffs — the next run's changes land here.</div>
        )}
        {panelTab === "wiki" && (conversation?.id != null ? (
          // M11 P1: the key remounts the panel on project switch so no
          // cross-project state (lists, reader cache, selection) survives;
          // conversation ids can collide across projects (both are
          // per-project SQLite sequences).
          <WikiBrowser
            key={`${project?.root_path ?? "default"}:${conversation.id}`}
            conversationId={conversation.id}
            projectRoot={project?.root_path ?? null}
          />
        ) : (
          <div className="panel-empty">No active conversation.</div>
        ))}
        {panelTab === "memory" && (conversation?.id != null ? (
          <MemoryPanel
            key={`${project?.root_path ?? "default"}:${conversation.id}`}
            conversationId={conversation.id}
            workstreamName={workstream?.name}
            initialTab={memorySubTab}
            onApplied={handleMemoryReviewClosed}
            projectRoot={project?.root_path ?? null}
          />
        ) : (
          <div className="panel-empty">No active conversation.</div>
        ))}
        {panelTab === "ledger" && (conversation?.id != null ? (
          <LedgerPanel
            key={`${project?.root_path ?? "default"}:${conversation.id}`}
            conversationId={conversation.id}
            projectRoot={project?.root_path ?? null}
          />
        ) : (
          <div className="panel-empty">No active conversation.</div>
        ))}
      </ContextPanel>
      </div>
      <StatusBar
        workstreamName={workstream?.name ?? null}
        conversationId={conversation?.id ?? null}
        epoch={conversation?.epoch ?? 1}
        projectRoot={project?.root_path ?? null}
        agentRunning={agentRunning}
        runningCount={runs.filter((r) => r.status === "running").length}
        adapter={adapter}
        onAdapterChange={async (a) => {
          setAdapter(a);
          // M9 P5: persist to daemon so the choice survives relaunch.
          try {
            const s = unwrap(await getSettings(projectRootRef.current ?? undefined));
            unwrap(
              await updateSettings({ ...s, default_adapter: a }, projectRootRef.current ?? undefined),
            );
          } catch { /* degrade silently */ }
        }}
        pendingDiffs={diffs.length}
        wikiNoteCount={wikiNoteCount}
        pendingMemoryProposals={pendingMemoryProposals}
        onBadgeClick={(tab) => openPanelTab(tab)}
      />
    </div>
  );
}
