import { useCallback, useEffect, useRef, useState } from "react";
import {
  acceptDiff,
  bootstrap,
  cancel,
  createWorkstream,
  curate,
  distill,
  errorMessage,
  fanoutSend,
  getSettings,
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
} from "./api";
import ChatSurface from "./components/ChatSurface";
import CommandPalette, { type PaletteAction } from "./components/CommandPalette";
import ContextPanel, { type PanelTab } from "./components/ContextPanel";
import DiffViewer from "./components/DiffViewer";
import MemoryReviewPanel, { type Tab as MemoryReviewTab } from "./components/MemoryReviewPanel";
import SettingsPanel from "./components/SettingsPanel";
import Sidebar, { type SidebarToast } from "./components/Sidebar";
import StatusBar from "./components/StatusBar";
import TopBar from "./components/TopBar";
import WikiBrowser from "./components/WikiBrowser";
import { notifyRunDone } from "./notify";
import type { BootstrapResponse, Conversation, Diff, OdoEvent, PreviewEvent, Project, RunInfo, Workstream } from "./types";

// Polling is the declared transport for M0 (no SSE/WebSocket). M7: the
// interval adapts to run state — fast while the agent streams blocks (the
// preview bubble follows the stream), slow when idle.
const POLL_INTERVAL_RUNNING_MS = 350;
const POLL_INTERVAL_IDLE_MS = 1500;

// Every transient toast — memory chips, retractions, ledger failures, and
// sidebar confirmations (distill/curate/pin) — auto-dismisses after 10 s,
// same cadence as the error banner.
const TOAST_MS = 10_000;        // sidebar confirmations (distill/curate/pin)
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

// One toast in the viewport: the sidebar-emitted payload plus its id.
interface ToastItem extends SidebarToast {
  id: number;
}

function parseRetraction(detail: string): RetractionInfo | null {
  const m = detail.match(/^(\S+) contradicted by (\S+): ([\s\S]*)$/);
  if (!m) return null;
  return { oldNote: m[1], newNote: m[2], snippet: m[3] };
}

export default function App() {
  const [project, setProject] = useState<Project | null>(null);
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
  const [settingsOpen, setSettingsOpen] = useState(false);
  // The memory review modal lives here (not in the sidebar): toasts click
  // through into it, and it must survive the sidebar collapsing to the rail.
  const [memoryReviewTab, setMemoryReviewTab] = useState<MemoryReviewTab | null>(null);
  // Belt B: chat search (⌘F) and the command palette (⌘K); the wiki
  // browser is lifted out of the Sidebar so the palette can open it too.
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [wikiOpen, setWikiOpen] = useState(false);
  const [lastDistillPath, setLastDistillPath] = useState<string | null>(null);
  const [booted, setBooted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // M3: wiki note count for the sidebar Memory section (null = unknown).
  const [wikiNoteCount, setWikiNoteCount] = useState<number | null>(null);
  // M5: topic page count beside the wiki note count (null = unknown).
  const [topicCount, setTopicCount] = useState<number | null>(null);
  // M4: pending learner-proposal count (sidebar badge) and the ephemeral
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
  // Transient confirmations emitted by the sidebar (distill/curate/pin),
  // rendered beside the chips in the toast viewport; each carries its own
  // 10 s expiry started by pushToast.
  const [toasts, setToasts] = useState<ToastItem[]>([]);
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
    try {
      const resp = await listWiki(conversationId);
      if (conversationRef.current !== conversationId) return; // switched mid-flight
      setWikiNoteCount(resp.ok ? (resp.wiki_notes?.length ?? 0) : null);
    } catch {
      if (conversationRef.current === conversationId) setWikiNoteCount(null);
    }
    // M5: topics are project-wide (not per-workstream), but the same
    // refresh hook + degrade pattern covers them.
    try {
      const topics = await listTopics();
      setTopicCount(topics.wiki_notes?.length ?? 0);
    } catch {
      setTopicCount(null);
    }
  }, []);

  // M4: refresh the pending learner-proposal count (sidebar badge).
  // Failures degrade to hidden (0), mirroring refreshWikiCount; they never
  // surface in the error banner.
  const refreshMemoryProposals = useCallback(async (conversationId: number) => {
    try {
      const resp = await memoryProposals(conversationId);
      if (conversationRef.current !== conversationId) return; // switched mid-flight
      setPendingMemoryProposals(
        (resp.epoch ?? 0) > 0 && resp.proposals ? resp.proposals.length : 0,
      );
    } catch {
      if (conversationRef.current === conversationId) setPendingMemoryProposals(0);
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

  // Session restore: bootstrap returns project/workstream/conversation plus
  // the full journaled event history and the latest diff.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = unwrap(await bootstrap());
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

  // K3 review P1: read the daemon's default_adapter on bootstrap so the
  // sidebar select reflects the persisted setting instead of hardcoded "omp".
  useEffect(() => {
    if (!booted) return;
    let cancelled = false;
    (async () => {
      try {
        const resp = unwrap(await getSettings());
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
      if (cid == null || inFlight) return;
      inFlight = true;
      try {
        const resp = unwrap(await pollEvents(cid, lastSeqRef.current));
        if (conversationRef.current !== cid) return; // workstream switched mid-flight
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

  const handleSend = useCallback(
    async (text: string, attachments: string[], steer: boolean) => {
      const cid = conversationRef.current;
      if (cid == null) throw new Error("no active conversation yet");
      try {
        const resp = unwrap(await sendMessage(cid, text, attachments, { steer, adapter }));
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
    try {
      const resp = unwrap(await fanoutSend(cid, text, n));
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
      await cancel(cid);
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

  // M9 P2: panelOpenRef must sync on panel toggle, not search toggle.
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
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [handleCancel]);

  const handleSwitchWorkstream = useCallback(
    async (workstreamId: number) => {
      if (workstreamId === workstream?.id) return;
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

  const handleDistill = useCallback(async (): Promise<string> => {
    const cid = conversationRef.current;
    if (cid == null) throw new Error("no active conversation yet");
    distillingRef.current = true;
    try {
      const resp = unwrap(await distill(cid));
      if (resp.epoch != null) {
        const epoch = resp.epoch;
        setConversation((prev) => (prev ? { ...prev, epoch } : prev));
      }
      setLastDistillPath(resp.wiki_path ?? null);
      // The distill just wrote a new note; the sidebar count follows.
      void refreshWikiCount(cid);
      // M4: the learner ran synchronously inside the same distill call, so
      // the proposal batch (if any) is already journaled — re-read it for
      // the sidebar badge.
      void refreshMemoryProposals(cid);
      setError(null);
      return resp.wiki_path ?? "";
    } catch (e) {
      setError(`distill failed: ${errorMessage(e)}`);
      throw e; // Sidebar clears its busy state; keep the toast hidden.
    } finally {
      distillingRef.current = false;
    }
  }, [refreshWikiCount, refreshMemoryProposals]);

  // M5: the curator one-shot rewrites every topic page + wiki/index.md
  // from the full epoch-note set. Blocks daemon-side like distill, so the
  // poll loop pauses; counts refresh on success.
  const handleCurate = useCallback(async () => {
    const cid = conversationRef.current;
    if (cid == null) throw new Error("no active conversation yet");
    curatingRef.current = true;
    try {
      unwrap(await curate(cid));
      void refreshWikiCount(cid);
      setError(null);
    } catch (e) {
      setError(`curate failed: ${errorMessage(e)}`);
      throw e; // Sidebar clears its busy state; keep the toast hidden.
    } finally {
      curatingRef.current = false;
    }
  }, [refreshWikiCount]);

  // M5: a pin is a verbatim user statement hoovered into .odo/pins.md — no
  // LLM processing, no poll pause (returns immediately). The journaled
  // memory_update{layer:"pins"} arrives on the next poll tick and toasts
  // through the generic recordEvents branch.
  const handlePin = useCallback(async (text: string) => {
    const cid = conversationRef.current;
    if (cid == null) throw new Error("no active conversation yet");
    try {
      unwrap(await pin(cid, text));
      setError(null);
    } catch (e) {
      setError(`pin failed: ${errorMessage(e)}`);
      throw e; // Sidebar shows the refusal (e.g. overflow names the pin).
    }
  }, []);

  const handleAccept = useCallback(async (diffId: number) => {
    try {
      const resp = unwrap(await acceptDiff(diffId));
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
      unwrap(await rejectDiff(diffId));
      setDiff((d) => (d && d.id === diffId ? { ...d, status: "rejected" } : d));
      setError(null);
    } catch (e) {
      setError(`reject failed: ${errorMessage(e)}`);
    }
  }, []);

  // The browser is read-only; closing it still re-fetches the count so a
  // note written by another client (or cleanup done by hand) shows up.
  const handleWikiBrowserClosed = useCallback(() => {
    const cid = conversationRef.current;
    if (cid != null) void refreshWikiCount(cid);
  }, [refreshWikiCount]);

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

  const openMemoryReview = useCallback((tab: MemoryReviewTab) => setMemoryReviewTab(tab), []);

  // Toast viewport lifecycle: push shows a confirmation for 10 s; either
  // the timer or a click (which also click-throughs to its panel) removes it.
  const dismissToast = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const pushToast = useCallback(
    (toast: SidebarToast) => {
      const id = ++toastSeqRef.current;
      setToasts((prev) => [...prev, { ...toast, id }]);
      window.setTimeout(() => dismissToast(id), TOAST_MS);
    },
    [dismissToast],
  );

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
      onRun: () => setWikiOpen(true),
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
      />
      <div className="app-body">
      <Sidebar
        project={project}
        workstream={workstream}
        conversationId={conversation?.id ?? null}
        workstreams={workstreams}
        agentRunning={agentRunning}
        adapter={adapter}
        onAdapterChange={setAdapter}
        onSwitchWorkstream={handleSwitchWorkstream}
        onCreateWorkstream={handleCreateWorkstream}
        onDistill={handleDistill}
        wikiNoteCount={wikiNoteCount}
        onOpenWiki={() => setWikiOpen(true)}
        onCurate={handleCurate}
        onPin={handlePin}
        topicCount={topicCount}
        pendingCounts={pendingCounts}
        runningWorkstreams={runningWorkstreams}
        pendingMemoryProposals={pendingMemoryProposals}
        onToast={pushToast}
        onOpenMemoryReview={openMemoryReview}
        collapsed={sidebarCollapsed}
        onToggleCollapsed={() => setSidebarCollapsed((v) => !v)}
        onOpenSettings={() => setSettingsOpen(true)}
      />
      {settingsOpen && <SettingsPanel onClose={() => setSettingsOpen(false)} />}
      {paletteOpen && <CommandPalette actions={paletteActions} onClose={() => setPaletteOpen(false)} />}
      {wikiOpen && conversation?.id != null && (
        <WikiBrowser
          conversationId={conversation.id}
          onClose={() => {
            setWikiOpen(false);
            handleWikiBrowserClosed();
          }}
        />
      )}
      {memoryReviewTab != null && conversation?.id != null && (
        <MemoryReviewPanel
          conversationId={conversation.id}
          workstreamName={workstream?.name}
          initialTab={memoryReviewTab}
          onClose={() => {
            setMemoryReviewTab(null);
            handleMemoryReviewClosed();
          }}
          onApplied={handleMemoryReviewClosed}
        />
      )}
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
                setMemoryReviewTab("files");
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
                setMemoryReviewTab("files");
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
                setMemoryReviewTab("ledger");
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
                key={d.id}
                diff={d}
                runLabel={d.run_index != null ? `Run ${d.run_index + 1}` : undefined}
                onAccept={handleAccept}
                onReject={handleReject}
              />
            ))
          : diff
            ? <DiffViewer diff={diff} onAccept={handleAccept} onReject={handleReject} />
            : <div className="panel-empty">No pending diffs — the next run's changes land here.</div>
        )}
        {panelTab === "wiki" && <div className="panel-empty">Wiki browser will appear here (Phase 3).</div>}
        {panelTab === "memory" && <div className="panel-empty">Memory proposals will appear here (Phase 3).</div>}
        {panelTab === "ledger" && <div className="panel-empty">Ledger will appear here (Phase 3).</div>}
      </ContextPanel>
      </div>
      <StatusBar
        workstreamName={workstream?.name ?? null}
        conversationId={conversation?.id ?? null}
        epoch={conversation?.epoch ?? 1}
        agentRunning={agentRunning}
      />
    </div>
  );
}
