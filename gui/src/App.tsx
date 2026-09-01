import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Plus, Sparkles, Wand2, MapPin, FileText, Settings, Square, ChevronLeft, Columns, Search, X, WifiOff } from "lucide-react";
import {
  acceptDiff,
  addProject,
  autoDistillCtl,
  bootstrap,
  cancel,
  createWorkstream,
  curate,
  deleteWorkstream,
  distill,
  errorMessage,
  getSettings,
  listAllPendingDiffs,
  listProjects,
  listTopics,
  listWiki,
  listWorkstreams,
  loopCtl,
  memoryProposals,
  renameWorkstream,
  pendingCounts as fetchPendingCounts,
  pin,
  pollEvents,
  rejectDiff,
  removeProject,
  resumeParkedGoal,
  dropParkedGoal,
  dropQueuedSteer,
  searchEvents,
  sendMessage,
  unwrap,
} from "./api";
import ChatSurface from "./components/ChatSurface";
import CommandPalette, { type PaletteAction } from "./components/CommandPalette";
import ContextPanel from "./components/ContextPanel";
import { K8S_CONTRIBUTION, PANEL_CONTRIBUTIONS, PANEL_TAB_IDS, type PanelTab } from "./contrib";
import { useK8sPoll } from "./k8s";
import { activeBatchCount, activeJobCount } from "./jobs";
import { ESC_PRIORITY, dispatchEscape, useEscLayer } from "./esc-registry";
import DiffViewer from "./components/DiffViewer";
import LearningPanel from "./components/LearningPanel";
import JobsPanel from "./components/JobsPanel";
import MemoryPanel from "./components/MemoryPanel";
import ReviewInbox from "./components/ReviewInbox";
import SkillsPanel from "./components/SkillsPanel";
import SettingsPanel from "./components/SettingsPanel";
import ShortcutsPanel from "./components/ShortcutsPanel";
import Sidebar from "./components/Sidebar";
import StatusBar from "./components/StatusBar";
import type { BackgroundNotice } from "./components/StatusBar";
import TasksPanel from "./components/TasksPanel";
import TopBar from "./components/TopBar";
import WikiBrowser from "./components/WikiBrowser";
import { basename } from "./files";
import { usePanelOverlay } from "./panel_overlay";
import { notifyRunDone, notifyRunFailed, notifyLoopTerminal } from "./notify";
import {
  captureSwitchSnapshot,
  mergeEvents,
  rollbackView,
  SwitchCache,
} from "./switch_cache";
import { deriveLoopStates, loopMode } from "./loop";
import { deriveTodoState, visibleTodoItems } from "./todo";
import { sameAutoDistillList, sameCountMap, sameDiff, sameDiffInfoExList, sameDiffList, sameIdList, sameStrandedOpsList } from "./diff_stable";
import { deriveGateDrift, derivePipelineStates } from "./pipeline";
import { isAdvisorySlash } from "./slash";
import { deriveLastPrompt, parseReviewModels } from "./stats";
import type { AutoDistillCapResume, AutoDistillCountdown, BootstrapResponse, Conversation, Diff, DiffInfoEx, OdoEvent, PanelProgress, PreviewEvent, Project, ProjectEntry, Settings as DaemonSettings, StrandedOp, Workstream } from "./types";
import { strings } from "./strings";
import { comboFor, isEditableTarget, matchKeyEvent } from "./keybinds";
import { mergeHits, type JournalHit } from "./journal_search";
import { summarizeError, classifyFailure } from "./errors";
import FailureOverlay from "./components/FailureOverlay";
import RunsPanel from "./components/RunsPanel";
import PreviewPanel from "./components/PreviewPanel";
import type { PreviewTarget } from "./preview";
import { initialParkState, mountedSet, activate, type ParkState } from "./lru";

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
const POLL_FAIL_THRESHOLD = 3; // consecutive poll failures before showing disconnect banner
// P4: jittered exponential backoff while polls keep failing —
// base * (1 + rand) * 2^min(failures, 5), so a dead daemon is hit ~4×/min,
// never at the live cadence.
const POLL_BACKOFF_CAP_MS = 15_000;
// P4: manual escape hatch — past this many consecutive failures the banner
// grows a Restart daemon button.
const POLL_FAIL_RESTART_THRESHOLD = 20;


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
  // Tri-model header gap: turn start timestamp for live duration display.
  const [turnStartedAt, setTurnStartedAt] = useState<number | null>(null);
  // Skeleton: true during bootstrap (conversation switch / first load),
  // false once events arrive. Tri-model gap analysis: Hermes has
  // skeletons.tsx; Odo had only a spinner.
  const [chatLoading, setChatLoading] = useState(true);
  // J: spinner while /panel, /vision or /preview blocks on the daemon
  // side — the consult can hold the RPC for minutes. A COUNT, not a
  // boolean: concurrent advisory sends (the composer now detaches) would
  // otherwise let the first to settle hide the spinner while a later
  // consult is still holding.
  const [panelThinking, setPanelThinking] = useState(0);
  // /panel heartbeat: the daemon's live leg tally from poll_events (never
  // journaled). Shown in the spinner row as "N/M back"; also keeps the
  // row visible when a consult outlives THIS window's send (e.g. the GUI
  // reopened mid-consult while the daemon still fans out).
  const [panelProgress, setPanelProgress] = useState<PanelProgress | null>(null);
  // M7: transient streaming preview (never journaled), rebuilt every poll.
  const [preview, setPreview] = useState<PreviewEvent | null>(null);
  const [diff, setDiff] = useState<Diff | null>(null);
  const [diffs, setDiffs] = useState<Diff[]>([]);
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
    return stored && (PANEL_TAB_IDS as readonly string[]).includes(stored) ? (stored as PanelTab) : "tasks";
    // UX-1 D2: the default tab is "tasks" (the plan layer's panel
    // surface); persisted selections stay valid — PANEL_TAB_IDS derives
    // from the registry, so an older stored id always matches. A3-2
    // (UX-4): a stored "ledger" from before the fold fails the allowlist
    // and lands on "tasks" — pinned in app_keepalive.test.tsx.
  });

  // Keep-alive panel tabs (tri-review P1 #5, 2026-08-24) + P2.4 LRU park:
  // tabs mount lazily on first activation; parkState (lru.ts) caps the
  // mounted set at active + 2 most-recent — deeper tabs unmount and show
  // a parked badge in the strip until re-activation remounts (and the
  // activation-edge refetch contract re-syncs) them. Draft-exempt tabs
  // (Memory/Wiki with unsaved input, reported via onDraftChange) are never
  // parked and mount outside the cap.
  const [parkState, setParkState] = useState<ParkState<PanelTab>>(() => initialParkState(panelTab));
  const [draftTabs, setDraftTabs] = useState<ReadonlySet<PanelTab>>(() => new Set());
  // Activation callbacks read the exemption set from the ref so openTab
  // never rememoizes on a draft flip; setTabDraft (the only writer) keeps
  // ref and state in lockstep.
  const draftTabsRef = useRef(draftTabs);
  const mountedPanelTabs = mountedSet(parkState, draftTabs);
  // The single activation path for a panel tab: tab clicks, badge jumps,
  // toast click-throughs, and the poll loop's auto-open all go through
  // here so the keep-alive mount set can never diverge from the visible
  // tab (no second convention beside setPanelTab).
  const openTab = useCallback((tab: PanelTab) => {
    setPanelTab(tab);
    setParkState((s) => activate(s, tab, draftTabsRef.current));
  }, []);
  const setTabDraft = useCallback((tab: PanelTab, dirty: boolean) => {
    // React may double-invoke a setState updater (StrictMode) — the ref
    // write must not live inside one. This callback is the only writer,
    // so the ref stays the source of truth and the state mirrors it.
    const prev = draftTabsRef.current;
    if (prev.has(tab) === dirty) return;
    const next = new Set(prev);
    if (dirty) next.add(tab); else next.delete(tab);
    draftTabsRef.current = next;
    setDraftTabs(next);
  }, []);
  const handleMemoryDraft = useCallback((d: boolean) => setTabDraft("memory", d), [setTabDraft]);
  const handleWikiDraft = useCallback((d: boolean) => setTabDraft("wiki", d), [setTabDraft]);
  // P2.1: the Preview tab's target — a file from a tool-result/arg ref,
  // or a localhost URL from an Open-live affordance. Setting a target
  // always pivots the panel to the preview tab.
  const [previewTarget, setPreviewTarget] = useState<PreviewTarget | null>(null);
  // P2.2: the Runs-tab transcript jump (nonce pattern: re-fires an
  // identical seq on later clicks via the counter). Lifecycle (grounded
  // revise R2, F2): ChatSurface reports the landing through
  // handleFocusSeqLanded (target scrolled + flash settled), which retires
  // the pin, and the effect beside it clears the pin on every conversation
  // switch — the transcript-window bound can't be defeated for the rest
  // of the session, and a stale seq can't collide with the new
  // conversation's run groups.
  const [focusSeq, setFocusSeq] = useState<{ seq: number; n: number } | null>(null);
  // M9 P3: memory sub-tab deep links (toast click-throughs). Shaped like
  // WikiBrowser's focus ({tab, n}) — the counter re-fires identical
  // requests. (tri-review P1 #5): with the panel keep-alive, MemoryPanel
  // stays mounted across switches, so a bare initialTab string would only
  // ever apply at first mount; the nonce makes later deep links apply too.
  const [memoryFocus, setMemoryFocus] = useState<{ tab: "proposals" | "files"; n: number } | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  // Belt B: chat search (⌘F) and the command palette (⌘K).
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [paletteOpen, setPaletteOpen] = useState(false);
  // P1.3: ⌘/ shortcuts panel — read-only render of keybinds.ts.
  const [shortcutsOpen, setShortcutsOpen] = useState(false);
  // P1.1 palette journal search: the palette owns its input; this mirrors
  // the live query so ONE debounced fan-out searches every registered
  // project's journal (read-only search_events, no new IPC).
  const [journalQuery, setJournalQuery] = useState("");
  const [journalHits, setJournalHits] = useState<JournalHit[] | null>(null);
  const [journalLoading, setJournalLoading] = useState(false);
  const journalSeqRef = useRef(0);
  // M11 D2: ⌘N opens palette in new-workstream prompt mode directly.
  const [paletteInitialAction, setPaletteInitialAction] = useState<string | undefined>(undefined);
  // Fold chip's "Open note": selects a specific note in the wiki browser.
  // The counter makes repeated clicks on the same note re-select it.
  const [wikiFocus, setWikiFocus] = useState<{ path: string; n: number } | null>(null);
  const [booted, setBooted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // E P2: daemon disconnect tracking — consecutive poll failures
  const [daemonDown, setDaemonDown] = useState(false);
  const pollFailRef = useRef(0);
  // P2.3 (failure taxonomy): the latest poll failure's raw string feeds
  // classifyFailure; the reference copy lets the catch arm the overlay
  // without a setState race. A dismissal is keyed to ONE class (grounded
  // revise R2, F3): identical failures keep it hidden; ANY class change —
  // an A→B→A flap included — voids the dismissal and re-arms, and an
  // explicit Reconnect resets it as a fresh start.
  const [lastPollError, setLastPollError] = useState<string | null>(null);
  const lastPollErrorRef = useRef<string | null>(null);
  const dismissedFailureRef = useRef<string | null>(null);
  // P4: render-mirror of pollFailRef — the banner's Restart button gates on
  // POLL_FAIL_RESTART_THRESHOLD, which refs alone can't re-render for.
  const [pollFailures, setPollFailures] = useState(0);
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
  // W6 (goal queue): per-workstream parked-goal depth from pending_counts
  // (the daemon's count is the authoritative depth; the QueueDock derives
  // its rows from the journal and may lag by a poll tick).
  const [parkedGoals, setParkedGoals] = useState<Record<number, number>>({});

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
  // P1a: the poll loop reads the active tab through a ref (the interval
  // callback closes over the boot cycle, not the render cycle).
  const panelTabRef = useRef<PanelTab>("tasks");
  // U2.1: the panel's docked↔overlay posture follows the MEASURED chat
  // width (ResizeObserver on .app-main, 560/600px hysteresis) — the old
  // max-[999px] window-width breakpoint is deleted; one mechanism only.
  // The element arrives via callback ref because the tree below is gated
  // behind the `booted` early-return: a useRef read at first commit is
  // null and would never re-subscribe when <main> finally mounts.
  const [appMainEl, setAppMainEl] = useState<HTMLElement | null>(null);
  const panelOverlay = usePanelOverlay(appMainEl, panelOpenRef);
  // M9 P2: track previous pending-diff count for genuine 0→1 transition,
  // and a bootstrap latch so the first poll after applyBootstrap doesn't
  // auto-open the panel on pre-existing pending diffs.
  const prevDiffsCountRef = useRef(0);
  const bootstrappedRef = useRef(false);
  // Project/workstream switch sequence: a newer switch bumps this so a
  // slower in-flight bootstrap can't resolve last and clobber newer state.
  const switchSeqRef = useRef(0);
  // Repeat switches (stale-while-revalidate): per-conversation journal
  // cache + workstream→conversation resolution. A cached target renders
  // synchronously on click; the authoritative bootstrap merges on landing
  // (the journal is append-only, so the seq-keyed union is lossless), and
  // a bootstrap failure restores the pre-flip snapshot wholesale.
  const switchCacheRef = useRef(new SwitchCache());
  // Read inside callbacks that must stay referentially stable (recordEvents
  // is a poll-effect dependency; a state dep would rebuild the interval).
  const workstreamNameRef = useRef<string | null>(null);
  const projectRootRef = useRef<string | null>(null);
  const pollTickRef = useRef(0);
  // Distill's bridge call can block for minutes. The daemon serves a
  // goroutine per connection (M11), so other clients are unaffected;
  // distillingRef (like curatingRef below) only pauses THIS client's own
  // poll loop while its distill request is in flight — each call is a
  // fresh connection, and pausing avoids queueing up requests that would
  // hit certain client-side timeout failures.
  const distillingRef = useRef(false);
  // M12: auto-distill is daemon-driven — the GUI discloses daemon state,
  // it never owns a trigger (the M10 idle timer is gone). Fed from
  // pending_counts: countdown entries (eta seconds, per conversation),
  // coverage blocks, and the in-flight distill list.
  const [autoDistill, setAutoDistill] = useState<AutoDistillCountdown[]>([]);
  const [distillingConvs, setDistillingConvs] = useState<number[]>([]);
  // Stranded memory crash-recoveries (2026-08-26 memory-replay doctrine):
  // count AND rows both ride pending_counts, project-wide (round-3
  // FIX F — the pre-FIX-F per-conversation event fold could light the
  // banner over zero actionable rows when the conflict rode a rotated
  // lane).
  const [strandedMemoryOps, setStrandedMemoryOps] = useState(0);
  const [strandedOps, setStrandedOps] = useState<StrandedOp[]>([]);
  // Auto-distill daily-cap suspension (2026-08-26 storm fix): the Memory
  // tab's "今日额度已用完 · 预计恢复" chip, riding pending_counts like the
  // countdowns; null while uncapped, past the horizon, or disabled.
  const [autoDistillCapResume, setAutoDistillCapResume] = useState<AutoDistillCapResume | null>(null);
  // M5: curate's bridge call blocks for minutes (like distill). The daemon
  // itself serves other connections throughout (M11 goroutine-per-
  // connection); curatingRef only suspends this client's own poll loop
  // while its curate request is in flight — a GUI choice, not daemon
  // serialization.
  const curatingRef = useRef(false);
  // Auto-dismiss timer for the memory_update chip.
  const memoryChipTimer = useRef<number | undefined>(undefined);
  // M6: same ephemeral-chip pattern for the retraction + ledger chips.
  const retractionChipTimer = useRef<number | undefined>(undefined);
  const ledgerChipTimer = useRef<number | undefined>(undefined);
  // Sequence for toast ids (App-level confirmations plus the chips).
  const toastSeqRef = useRef(0);
  // Pending auto-dismiss timers, tracked so unmount cancels them.
  const toastTimersRef = useRef<Set<number>>(new Set());

  const recordEvents = useCallback((incoming: OdoEvent[]) => {
    if (incoming.length === 0) return;
    setEvents((prev) => mergeEvents(prev, incoming));
    setChatLoading(false); // events arrived — hide skeleton
    lastSeqRef.current = Math.max(lastSeqRef.current, ...incoming.map((e) => e.seq));
    // M3 (spec §3b): finished runs notify when the window is hidden. Only
    // freshly polled events land here — bootstrap replaces wholesale — so
    // history replay cannot re-notify. notifyRunDone swallows its errors.
    for (const e of incoming) {
      if (e.type === "agent_done") {
        void notifyRunDone(workstreamNameRef.current ?? "?", e.payload?.summary ?? "");
      }
      // UX-3a (A2-6a): failures notify with a distinct title too. Daemon
      // advisories (agent_error with odo:true, journalRunAdvisory) are
      // NOT run failures — the first consumer of a flag nothing read.
      if (e.type === "agent_error" && e.payload?.odo !== true) {
        void notifyRunFailed(workstreamNameRef.current ?? "?", e.payload?.error ?? "");
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

  // M3 (spec §3c) + M12: one fetch feeds the sidebar badges and the
  // auto-distill disclosure (countdown chip / blocked state / in-flight
  // indicator). Extracted so send/distill/ctl paths re-fetch promptly
  // instead of waiting for the poll loop's every-4th-tick cadence.
  // P1a: pendingTotalRef mirrors the badge sum so the poll loop's inbox
  // gate can skip the IPC outright at zero pending.
  const pendingTotalRef = useRef(0);
  const refreshPendingCounts = useCallback(async () => {
    const root = projectRootRef.current;
    if (root == null) return;
    try {
      const counts = await fetchPendingCounts(root);
      if (projectRootRef.current !== root) return; // switched mid-flight — dropping stale totals
      if (!counts.ok) return;
      const pending: Record<number, number> = {};
      for (const [k, v] of Object.entries(counts.pending_counts ?? {})) {
        const id = Number(k);
        if (Number.isFinite(id)) pending[id] = v;
      }
      setPendingCounts((prev) => (sameCountMap(prev, pending) ? prev : pending));
      const parked: Record<number, number> = {};
      for (const [k, v] of Object.entries(counts.parked_goals ?? {})) {
        const id = Number(k);
        if (Number.isFinite(id)) parked[id] = v;
      }
      setParkedGoals((prev) => (sameCountMap(prev, parked) ? prev : parked));
      // tri-review P2 #5 (2026-08-24): all five setters prev-bail through the
      // diff_stable comparators — the daemon deserializes fresh references
      // every tick even when nothing changed, and the badge lists (running
      // / auto_distill / distilling_convs) arrive in randomized Go-map
      // order, so bare setState re-rendered the sidebar, StatusBar, and
      // every memo'd keep-alive panel on every refresh for zero visual
      // change. Same pattern as setDiffs below; semantics documented in
      // the diff_stable module header.
      const running = counts.running_workstreams ?? [];
      setRunningWorkstreams((prev) => (sameIdList(prev, running) ? prev : running));
      const auto = counts.auto_distill ?? [];
      setAutoDistill((prev) => (sameAutoDistillList(prev, auto) ? prev : auto));
      const distilling = counts.distilling_convs ?? [];
      setDistillingConvs((prev) => (sameIdList(prev, distilling) ? prev : distilling));
      setStrandedMemoryOps(counts.stranded_memory_ops ?? 0);
      const ops: StrandedOp[] = (counts.stranded_ops ?? []).map((r) => ({
        layer: r.layer,
        receiptSeq: r.receipt_seq,
        strandedConversation: r.conversation_id,
        detail: r.detail,
      }));
      setStrandedOps((prev) => (sameStrandedOpsList(prev, ops) ? prev : ops));
      // Daily-cap chip: value-compared like the multiset lists above —
      // the daemon deserializes a fresh object every poll, and a nil→
      // object→nil flap never re-renders equal states.
      const capResume = counts.auto_distill_cap_resume ?? null;
      setAutoDistillCapResume((prev) =>
        prev?.resume_at_unix === capResume?.resume_at_unix && (prev?.computed ?? false) === (capResume?.computed ?? false) ? prev : capResume,
      );
      pendingTotalRef.current = Object.values(pending).reduce((a, b) => a + b, 0);
    } catch {
      // Stale badges are fine; never disturb the poll loop.
    }
  }, []);

  // P1a (review inbox): every pending diff across the project's workstreams
  // for the Review tab. Fetch cadence is visibility-gated: an immediate run
  // when the tab opens, then ≥6s between runs through the poll loop, so the
  // hidden tab costs zero daemon reads (the sidebar badges' pending_counts
  // poll already supplies project-wide freshness).
  const [inboxDiffs, setInboxDiffs] = useState<DiffInfoEx[]>([]);
  const lastInboxFetchRef = useRef(0);
  const refreshInbox = useCallback(async () => {
    const root = projectRootRef.current;
    if (root == null) return;
    lastInboxFetchRef.current = Date.now();
    try {
      const resp = await listAllPendingDiffs(root);
      if (projectRootRef.current !== root) return; // project switched mid-flight
      const rows = resp.ok ? (resp.all_pending_diffs ?? []) : [];
      // Keep the previous reference on content-identical ticks (SQL-ordered
      // wire, so element-wise compare — same contract as setDiffs): the
      // memo'd ReviewInbox stays idle between gated refreshes.
      setInboxDiffs((prev) => (sameDiffInfoExList(prev, rows) ? prev : rows));
    } catch {
      // Stale rows are fine; the next gated refresh retries.
    }
  }, []);

  // P1a: the Review tab badge is the project-wide pending total (the same
  // sum the sidebar's project-row pill derives from pendingCounts).
  const pendingTotal = useMemo(
    () => Object.values(pendingCounts).reduce((a, b) => a + b, 0),
    [pendingCounts],
  );

  // GUI Wave B (#5 + #9): settings data the StatusBar needs — coding model
  // (context-window denominator) and the review panel list. get_settings
  // is a singleton IPC (daemon-merged prefs+defaults), fetched on project
  // switch and after every SettingsPanel save. A failed/null read only
  // hides the meter's model tag + panel chip — never blocks the bar.
  const [appSettings, setAppSettings] = useState<DaemonSettings | null>(null);
  const refreshSettings = useCallback(async () => {
    const root = projectRootRef.current;
    if (root == null) return;
    try {
      const resp = await getSettings(root);
      if (projectRootRef.current !== root) return; // project switched mid-flight
      setAppSettings(resp.ok ? (resp.settings ?? null) : null);
    } catch {
      setAppSettings(null);
    }
  }, []);
  useEffect(() => {
    setAppSettings(null);
    void refreshSettings();
  }, [project?.root_path, refreshSettings]);

  // ---------- D5b (A4 + A2-5): k8s gate + the ONE poller ----------
  // The TAB's presence and the POLL's master switch key on the SAME
  // settings read the chip's off detection uses (the lock's "gate the
  // tab's registry presence on the k8s setting exactly like the chip").
  const k8sConfigured = (appSettings?.k8s_namespace ?? "").trim() !== "";
  // Reports from StatusBar's fold engine: is the chip unfolded right now?
  // Default true — pre-first-measure the chip renders unfolded.
  const [jobsChipVisible, setJobsChipVisible] = useState(true);
  const jobsTabActive = k8sConfigured && panelOpen && panelTab === "jobs" && mountedPanelTabs.has("jobs");
  // one poller app-wide: chip + tab share state; gate = configured &&
  // docVisible && (chip unfolded || jobs tab active) — the hook latches
  // docVisible itself (A4 D6).
  const k8s = useK8sPoll(
    project?.root_path ?? null,
    k8sConfigured,
    jobsChipVisible || jobsTabActive,
  );
  // Configured-namespace order: the Jobs table's group order + filter
  // chips. scope edits live ONLY in Settings (A4 D4).
  const k8sNamespaces = useMemo(
    () => (appSettings?.k8s_namespace ?? "").split(",").map((s) => s.trim()).filter((s) => s !== ""),
    [appSettings],
  );
  const { activeJobsCount, activeBatchCountForBadge } = useMemo(
    () => ({
      activeJobsCount: activeJobCount(k8s.status?.jobs ?? []),
      activeBatchCountForBadge: activeBatchCount(k8s.batch?.batches ?? []),
    }),
    [k8s.status, k8s.batch],
  );
  // A3-3: static 9 vs. gated 10th — arrows at the 720px MAX are the
  // LOCKED accepted trade while k8s is configured.
  const panelContributions = useMemo(
    () => (k8sConfigured ? [...PANEL_CONTRIBUTIONS, K8S_CONTRIBUTION] : PANEL_CONTRIBUTIONS),
    [k8sConfigured],
  );

  // Wave B #5: the last prompt closure is journaled data the UI already
  // holds — the meter reads the newest carrier (user_message send/slash or
  // run_prompt continuation) straight off the event stream.
  const lastPrompt = useMemo(() => deriveLastPrompt(events), [events]);

  // M19 (/loop) V1: the chip + notification watcher fold the ACTIVE
  // conversation's journal (same scope guarantee as the pipeline chip).
  // Pure re-derivation — loops continue daemon-side while the GUI is
  // closed, so state can never be latched here.
  const loopStates = useMemo(() => deriveLoopStates(events), [events]);
  // M12 (D-todo) + UX-1 D2: the plan layer's SINGLE derive — folded once
  // per poll tick from the journaled events and feeding three consumers:
  // the composer Plan chip, the Tasks panel tab, and the strip badge.
  const todoItems = useMemo(() => deriveTodoState(events), [events]);
  // UX-1 D2 strip badge: visible (unswept) open rows — exactly what the
  // chip's "N open" label counts, so badge and surfaces never disagree.
  const openTodoCount = useMemo(
    () => visibleTodoItems(todoItems).filter((t) => t.status === "open").length,
    [todoItems],
  );
  const reviewPanel = useMemo(
    () => parseReviewModels(appSettings?.review_models ?? ""),
    [appSettings],
  );

  // Auto-land pipeline chip (design lock Phase 1): pure re-derivation off
  // the ACTIVE conversation's journaled stream (conversation scope is the
  // daemon's poll/bootstrap contract — pipeline.ts documents it) + the
  // pending-diff list. No latch: the memo's inputs are exactly the two
  // surfaces those facts arrive on.
  // 2026-08-25 review P2: the memo used to re-derive on EVERY events
  // array — each 350ms streaming batch rescanned the full journal and
  // produced a fresh pipelineStateByDiff Map, defeating memo() on the
  // hidden keep-alive ReviewInbox. The derivation reads only
  // review_action / memory_update rows, so fingerprint the stream by
  // conversation + last relevant seq: unrelated streaming events (text,
  // tool calls) reuse the previous result reference, zero waste.
  const pipelineScan = useMemo(() => {
    let last = 0;
    for (const e of events) {
      if (e.type === "review_action" || e.type === "memory_update") last = e.seq;
    }
    return `${conversation?.id ?? 0}:${last}`;
  }, [events, conversation?.id]);
  const pipelineStates = useMemo(
    () => derivePipelineStates(events, diffs.map((d) => d.id), appSettings?.auto_apply === "main"),
    // `events` is consumed through its pipelineScan fingerprint: an
    // identical (conversation, last relevant seq) pair derives identical
    // states because the derivation ignores every other event type.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [pipelineScan, diffs, appSettings],
  );
  // Per-diff lookup for the review surfaces: the Changes card and the
  // Review inbox lock their human-action buttons while the auto-land
  // pipeline is actively working that diff (misfire guard — a click must
  // not race the panel verdict). Active-conversation scope is inherited
  // from the derivation; inbox rows owned by other conversations have no
  // entry and stay actionable.
  const pipelineStateByDiff = useMemo(
    () => new Map(pipelineStates.map((s) => [s.diffId, s])),
    [pipelineStates],
  );

  // D1 gate-drift latch (gatepolicy.go): the project-wide landing freeze is
  // journaled on the same review_action/memory_update stream (one check row
  // per boot; refusal rows per landing attempt), so its fold rides the
  // pipelineScan fingerprint — no second scan per streaming batch.
  const gateDrift = useMemo(
    () => deriveGateDrift(events),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [pipelineScan],
  );

  // M19 (V11): ONE system notification per (loop, terminal kind), pref-
  // gated (loop_notify_on_complete, default ON), journaled back via
  // loop_ctl notified so a GUI reopen never re-fires. The fold's
  // terminalKinds is the first-sight set and notifiedKinds the journaled
  // receipts; the session ref covers the gap before the receipt lands on
  // a poll. Keyed per conversation — loop ids are conversation-scoped
  // seqs, so two workstreams can share an id+kind pair.
  const loopNotifiedRef = useRef<Set<string>>(new Set());
  useEffect(() => {
    if (appSettings?.loop_notify_on_complete === false) return;
    const convId = conversation?.id;
    if (convId == null) return;
    for (const st of loopStates) {
      for (const kind of st.terminalKinds) {
        const key = `${convId}:${st.id}:${kind}`;
        if (st.notifiedKinds.includes(kind) || loopNotifiedRef.current.has(key)) continue;
        loopNotifiedRef.current.add(key);
        void notifyLoopTerminal(loopMode(st), kind, st.rounds.length, st.spentTokens);
        loopCtl(convId, "notified", {
          loopId: st.id,
          text: kind,
          projectRoot: projectRootRef.current ?? undefined,
        }).catch(() => {
          // Receipt journaling failed (daemon unreachable): the session
          // ref still suppresses local re-fire today; the next GUI boot
          // finds no journaled receipt and retries. Never throw — the
          // watcher runs inside the render-effect path.
        });
      }
    }
  }, [loopStates, appSettings, conversation?.id]);

  // 2026-08-25 review P2 (ex keep-alive Ledger; A3-2 folded its receipts
  // into the Runs tab): `events` is a fresh array on every poll and
  // streaming batch; handing it straight to the CSS-hidden keep-alive
  // RunsPanel would bust its memo each tick — filter+sort over the full
  // journal for a surface nobody sees. While the runs tab is hidden,
  // freeze the prop at the array from its last active commit (the ref
  // only follows active-tab commits); the subtree renders ZERO times
  // while hidden and re-syncs in the activation render.
  const runsEventsRef = useRef(events);
  useEffect(() => {
    if (panelTab === "runs") runsEventsRef.current = events;
  });
  // Same freeze contract for MemoryPanel (UX-3c A2-6c): the backoff
  // footer derives from the journal; a hidden tab keeps its last-seen
  // events and re-syncs in the activation render.
  const memoryEventsRef = useRef(events);
  useEffect(() => {
    if (panelTab === "memory") memoryEventsRef.current = events;
  });

  // Background runs: daemon-reported running workstreams minus the one in
  // view. Invisible from the chat surface (panel sessions, other ws) — the
  // StatusBar chip is their only surface.
  const backgroundRuns = useMemo(
    () =>
      runningWorkstreams
        .filter((id) => id !== workstream?.id)
        .map((id) => ({ id, name: workstreams.find((w) => w.id === id)?.name ?? `ws ${id}` })),
    [runningWorkstreams, workstream?.id, workstreams],
  );

  // GUI Wave A (#1): background-run start/finish notices for the StatusBar
  // flashes. Watched on the RAW runningWorkstreams set so view switches
  // (jumping to/away from a running ws) never register as start/finish —
  // only true set transitions do. The workstream in view is excluded from
  // both lists: its lifecycle is already visible as the fg "running"
  // indicator. First observation only seeds the baseline (a long-running
  // bg run at launch is not "new").
  const [bgNotice, setBgNotice] = useState<BackgroundNotice | null>(null);
  const prevRunningRef = useRef<number[] | null>(null);
  const bgNoticeTimerRef = useRef<number | undefined>(undefined);
  useEffect(() => {
    const prev = prevRunningRef.current;
    prevRunningRef.current = runningWorkstreams;
    if (prev == null) return; // seed baseline; no notice on first observation
    const startedIds = runningWorkstreams.filter((id) => !prev.includes(id) && id !== workstream?.id);
    const finishedIds = prev.filter((id) => !runningWorkstreams.includes(id) && id !== workstream?.id);
    if (startedIds.length === 0 && finishedIds.length === 0) return;
    const nameOf = (id: number) => workstreams.find((w) => w.id === id)?.name ?? `ws ${id}`;
    window.clearTimeout(bgNoticeTimerRef.current);
    // UX-3a (A2-6a): the set transition knows WHO finished, not HOW. The
    // switch cache holds the journals poll events already warmed this
    // session — read the terminal status from there (no new IPC).
    setBgNotice({
      started: startedIds.map(nameOf),
      finished: finishedIds.map((id) => ({
        id,
        name: nameOf(id),
        errored: switchCacheRef.current.terminalError(projectRootRef.current ?? null, id),
      })),
    });
    // 4 s: shorter than toast confirmations (10 s) — a glanceable cue, not
    // an acknowledgment queue.
    bgNoticeTimerRef.current = window.setTimeout(() => setBgNotice(null), 4000);
  }, [runningWorkstreams, workstream?.id, workstreams]);
  useEffect(() => () => window.clearTimeout(bgNoticeTimerRef.current), []);

  // GUI Wave A (#2): foreground current-activity line for the sidebar row
  // — latest tool the run has called, when one is journaled this session.
  // Background rows get only a fixed label: their events are never polled.
  const fgRunLabel = useMemo(() => {
    const fg = agentRunning || (workstream != null && runningWorkstreams.includes(workstream.id));
    if (!fg) return null;
    for (let i = events.length - 1; i >= 0; i--) {
      const ev = events[i];
      if (ev.type === "agent_tool_call" && ev.payload?.tool) return `Running: ${ev.payload.tool}`;
    }
    return "Running";
  }, [agentRunning, workstream, runningWorkstreams, events]);

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
      // The badge counts ACTIONABLE proposals: a consumed batch (the
      // panel-gated path decides it inside the distill, or a human did)
      // is history, not work — the MemoryPanel shows its outcome instead.
      setPendingMemoryProposals(
        !resp.consumed && (resp.epoch ?? 0) > 0 && resp.proposals ? resp.proposals.length : 0,
      );
    } catch {
      if (conversationRef.current === conversationId && projectRootRef.current === root) setPendingMemoryProposals(0);
    }
  }, []);

  // Whole-context replacement (cold path) or tail merge (repeat switch):
  // bootstrap returns the target conversation's journal — full replay, or
  // only the tail when the request carried a switch-cache hint — and the
  // landing reconciles it with whatever is currently rendered.
  const applyBootstrap = useCallback(
    (resp: BootstrapResponse, opts?: { defaultTarget?: boolean }) => {
      setWorkstream(resp.workstream ?? null);
      workstreamNameRef.current = resp.workstream?.name ?? null;
      const cid = resp.conversation?.id ?? null;
      // Record the resolution BEFORE refs flip below: the next switch here
      // can then render from cache synchronously. `defaultTarget` (the
      // request carried no workstream id, so the daemon resolved its own
      // default) additionally records the project-default alias — keying
      // it off the workstream NAME "main" would go stale on rename.
      const rootNow = projectRootRef.current;
      if (rootNow != null && resp.workstream != null && cid != null) {
        switchCacheRef.current.record(rootNow, resp.workstream.id, cid, {
          defaultTarget: opts?.defaultTarget,
        });
      }
      // An optimistic (cached) switch set conversationRef to the target
      // already, so a same-conversation landing MERGES instead of
      // replacing: a poll that landed in the flip window may hold rows
      // newer than this response's tail, and a replace would drop them
      // while lastSeqRef had advanced past them — permanently lost rows.
      const sameConv = cid != null && cid === conversationRef.current;
      setConversation(resp.conversation ?? null);
      conversationRef.current = cid;
      const evs = resp.events ?? [];
      if (sameConv) {
        // Pure updater (StrictMode double-invokes it) — no ref writes
        // inside; lastSeqRef advances here from the same inputs.
        lastSeqRef.current = Math.max(
          lastSeqRef.current,
          evs.reduce((max, e) => Math.max(max, e.seq), 0),
        );
        setEvents((prev) => mergeEvents(prev, evs));
      } else {
        lastSeqRef.current = evs.reduce((max, e) => Math.max(max, e.seq), 0);
        setEvents(evs);
      }
      setChatLoading(false); // history loaded (empty or not) — show welcome or content
      setAgentRunning(resp.agent_running ?? false);
      if (resp.agent_running) setTurnStartedAt(Date.now());
      else setTurnStartedAt(null);
      setPreview(null); // bootstrap carries no preview; the next poll restores it
      setPanelProgress(null); // same for the /panel tally — the next poll restores it
      setDiff(resp.diff ?? null);
      // Seed the list from the bootstrap diff (the wire carries only the
      // singular). Clearing to [] here and letting the first poll refill
      // flips the Changes tab between its singular and list render
      // branches — an unmount/remount ~one tick after boot that destroys
      // DiffViewer-local state (an open commit editor dies mid-flow;
      // Playwright sees "element was detached"). Seeded, the first poll
      // is content-equal (sameDiffList) and the card never remounts.
      setDiffs(resp.diff ? [resp.diff] : []);
      // M9 P2: reset the bootstrap latch so the first poll after a new
      // bootstrap (switch workstream, session restore) doesn't auto-open.
      bootstrappedRef.current = false;
      prevDiffsCountRef.current = 0;
      setWikiFocus(null);
      // chatLoading was set to false above — the skeleton is gone
      // once bootstrap delivers the event history (empty or not).
      // DO NOT re-arm it here; that would overwrite the false and latch
      // the skeleton permanently (DSF final review finding).
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

  // Keep the repeat-switch cache warm with whatever is currently rendered
  // (bootstrap replace/merge and poll appends all flow through here), so
  // the next repeat switch can flip synchronously. Eviction drops
  // references only — the daemon's journal stays the source of truth.
  useEffect(() => {
    const root = projectRootRef.current;
    const cid = conversationRef.current;
    if (root == null || cid == null) return;
    switchCacheRef.current.warm(root, cid, events);
  }, [events]);

  // M11 P1 (2026-08-20 cross-project running-state leak): sidebar badges,
  // running dots, parked/distill chips and the review inbox are keyed by
  // workstream id — and ids collide across projects (every journal starts
  // at 1). Without a reset on project switch, the previous project's
  // aggregates render against the new project's rows for up to four poll
  // ticks (~6 s idle) — e.g. a running Main in the old project made the
  // new project's Main read "running" while its daemon truthfully
  // reported []. Called by both switch handlers the moment the root
  // flips; refreshPendingCounts then repopulates from the new daemon.
  const resetProjectAggregates = useCallback(() => {
    setPendingCounts({});
    pendingTotalRef.current = 0;
    setRunningWorkstreams([]);
    setParkedGoals({});
    setAutoDistill([]);
    setDistillingConvs([]);
    setAutoDistillCapResume(null);
    setInboxDiffs([]);
  }, []);

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
        applyBootstrap(resp, { defaultTarget: true }); // workstreamless request
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

  // Poll the daemon for new journal events after the last seen seq.
  const pollNowRef = useRef<() => void>(() => {});
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
        // Re-render audit of this 350 ms tick handler (tri-review P1 #4,
        // 2026-08-24): on a quiet tick NOTHING below may produce a new
        // state reference, so React's bailouts skip the whole re-render.
        // - recordEvents early-returns on an empty batch, and mergeEvents
        //   returns `prev` unchanged when every row was already seen — no
        //   new events reference on quiet ticks.
        // - setAgentRunning / setError(null→null) / setDaemonDown /
        //   setPollFailures write primitives; React's Object.is bailout
        //   absorbs same-value writes without re-rendering. Left as-is.
        recordEvents(resp.events ?? []);
        setAgentRunning(resp.agent_running ?? false);
        // Seed only on the false→true transition — re-seeding on every
        // 350ms poll kept the live turn duration permanently at 0:00.
        setTurnStartedAt((prev) => (resp.agent_running ? (prev ?? Date.now()) : null));
        // M7: transient in-flight block preview — replaced wholesale per
        // poll; renders as the dimmed preview bubble. Only update state
        // when the preview actually changed (reference equality from the
        // daemon's JSON is already stable within a poll; the tri-model
        // review found setPreview on every poll caused unnecessary
        // re-renders of the entire runGroups.map tree).
        const nextPreview = resp.preview ?? null;
        setPreview((prev) => {
          if (prev === nextPreview) return prev;
          // Shallow-compare type + text/tool to catch semantically-identical
          // previews that arrive as different object references.
          if (prev != null && nextPreview != null &&
              prev.type === nextPreview.type &&
              prev.payload?.text === nextPreview.payload?.text &&
              prev.payload?.tool === nextPreview.payload?.tool &&
              prev.payload?.intent === nextPreview.payload?.intent) {
            return prev;
          }
          return nextPreview;
        });
        // /panel heartbeat — replaced wholesale per poll; the shallow
        // compare keeps the render tree quiet on no-change ticks (the
        // preview pattern above).
        const nextPanelProgress = resp.panel_progress ?? null;
        setPanelProgress((prev) => {
          if (prev === nextPanelProgress) return prev;
          if (prev != null && nextPanelProgress != null &&
              prev.done === nextPanelProgress.done &&
              prev.total === nextPanelProgress.total) {
            return prev;
          }
          return nextPanelProgress;
        });
        // The daemon always reports the latest diff (any status); only a
        // pending one is actionable in the UI. (tri-review P1 #4,
        // 2026-08-24): resp.diff/resp.diffs arrive as FRESH JSON
        // references on every tick (an empty [] included — the hottest
        // path), so a blind setState re-rendered the entire subtree
        // 2.86×/s while the agent ran. Compare content and keep the
        // previous reference when nothing changed — the setPreview /
        // setPanelProgress stabilization pattern above, via the
        // diff_stable comparators.
        if (resp.diff) {
          const nextDiff = resp.diff;
          setDiff((prev) => (sameDiff(prev, nextDiff) ? prev : nextDiff));
        }
        const newDiffs = resp.diffs ?? [];
        setDiffs((prev) => (sameDiffList(prev, newDiffs) ? prev : newDiffs));
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
          openTab("changes");
        }
        prevDiffsCountRef.current = newDiffs.length;
        // M3 (spec §3c) + M12 (D-auto disclosure): project-wide visibility
        // every ~4th tick (~6 s idle, ~1.4 s while a run streams).
        if (pollTickRef.current % 4 === 0) {
          await refreshPendingCounts();
          // P1a: the Review tab's refresh rides this 4th-tick cadence when
          // visible, ≥6s between fetches; Σpending==0 skips the IPC and
          // clears rows locally. refreshPendingCounts just updated
          // pendingTotalRef on this same tick, so the gate reads fresh data.
          if (panelTabRef.current === "review") {
            if (pendingTotalRef.current === 0) {
              setInboxDiffs((rows) => (rows.length === 0 ? rows : []));
            } else if (Date.now() - lastInboxFetchRef.current >= 6000) {
              await refreshInbox();
            }
          }
        }
        // Clear only POLL-origin errors (2026-08-25 audit P2): an
        // unconditional clear let the ~1.5 s background tick wipe a fresh
        // switch/send error seconds after it was raised — the user saw the
        // view roll back with no cause. Those errors expire on the
        // ERROR_BANNER_MS timer like before; only the poll's own failure
        // line self-heals on the next healthy tick.
        setError((prev) =>
          prev !== null && prev.startsWith("poll failed:") ? null : prev,
        );
        // E P2: reset consecutive failure counter on success
        pollFailRef.current = 0;
        setDaemonDown(false);
        setPollFailures(0);
        // P2.3: a healthy tick clears the taxonomy lane too.
        lastPollErrorRef.current = null;
        setLastPollError(null);
        dismissedFailureRef.current = null;
      } catch (e) {
        // E P2: track consecutive poll failures for disconnect detection
        // P2.3: remember this failure's raw text; the overlay classifies
        // the SAME string the banner prefixes, never a re-derived copy.
        const pollErr = `poll failed: ${errorMessage(e)}`;
        lastPollErrorRef.current = pollErr;
        setLastPollError(pollErr);
        pollFailRef.current += 1;
        setPollFailures(pollFailRef.current);
        if (pollFailRef.current >= POLL_FAIL_THRESHOLD) {
          // Class-keyed dismissal (F3): the same class never re-arms on
          // every backoff tick; a DIFFERENT class arriving voids the
          // dismissal outright, so a later return of the dismissed class
          // (A→B→A) surfaces the overlay again. An UNCLASSIFIABLE string
          // (cls null) has no dismissal concept — it always arms the
          // legacy banner, exactly as pre-R2.
          const cls = classifyFailure(pollErr)?.cls ?? null;
          if (cls === null || cls !== dismissedFailureRef.current) {
            dismissedFailureRef.current = null;
            setDaemonDown(true);
          }
        }
        if (!distillingRef.current && !curatingRef.current) {
          setError(`poll failed: ${errorMessage(e)}`);
        }
      } finally {
        inFlight = false;
      }
    };
    // M7: 350 ms while the agent runs (block-level preview latency), 1.5 s
    // idle. The cadence resets when agentRunning flips. Self-scheduling
    // setTimeout (rather than setInterval) so consecutive failures stretch
    // the cadence with jittered exponential backoff (P4):
    // base * (1 + rand) * 2^min(fails, 5), capped at 15 s.
    // Aliveness guard: the tick().then(schedule) chain must not fire after
    // the effect cleanup runs — clearTimeout only stops an un-fired timer,
    // not a promise that already resolved (tri-model review S1 fix).
    let alive = true;
    let timer = 0;
    const schedule = () => {
      const base = agentRunning ? POLL_INTERVAL_RUNNING_MS : POLL_INTERVAL_IDLE_MS;
      const fails = pollFailRef.current;
      const delay =
        fails === 0
          ? base
          : Math.min(
              POLL_BACKOFF_CAP_MS,
              base * (1 + Math.random()) * 2 ** Math.min(fails, 5),
            );
      timer = window.setTimeout(() => {
        if (!alive) return;
        // Re-check `alive` after the await: the effect may have been
        // cleaned up while the tick was in flight.
        void tick().then(() => { if (alive) schedule(); });
      }, delay);
    };
    // M12 (D-todo): the Plan popover triggers an immediate re-poll after
    // its journaled op instead of waiting ~1.5 s for the next tick.
    // Poll-now also clears the pending (possibly backed-off) timer and
    // re-arms after the tick, so a wake-nudge recovery returns to base
    // cadence immediately instead of waiting out the stale delay
    // (tri-model review S3 fix). If a tick is already in flight, that
    // tick's own .then(schedule) re-arms once it settles — scheduling
    // here too would fork a second timer chain that `timer` can't track.
    pollNowRef.current = () => {
      if (!alive || inFlight) return;
      window.clearTimeout(timer);
      void tick().then(() => { if (alive) schedule(); });
    };
    schedule();

    return () => { alive = false; window.clearTimeout(timer); };
  }, [booted, recordEvents, agentRunning, refreshPendingCounts, refreshInbox, openTab]);

  // ChatSurface is wrapped in React.memo — every prop below must keep a
  // stable reference across renders or the memo is defeated (tri-review
  // P1 #4, 2026-08-24). onTodoChanged / onTodoError / onLoopChanged /
  // onLoopError / onModelChanged / onSearchClose were INLINE ARROWS in the
  // JSX (a fresh reference every render); pollNowRef keeps the re-poll
  // callbacks dependency-free so useCallback([]) can freeze them.
  // handleSend/handleCancel/handleOpenFoldNote were already frozen
  // useCallbacks; handleResumeParked/handleDropParked/handleDropSteer
  // only rebuild on conversation/project switches (never per poll tick),
  // events/preview/panelProgress/loops are stabilized at their setters or
  // by useMemo; the remaining props are primitives.
  const handlePollNow = useCallback(() => pollNowRef.current(), []);
  const handleSurfaceError = useCallback((m: string) => setError(m), []);
  const handleSearchClose = useCallback(() => setSearchOpen(false), []);
  const handleModelChanged = useCallback(() => {
    void refreshSettings();
  }, [refreshSettings]);
  // The .find() results were derived INLINE per render; they happen to
  // return the same element reference between counts refreshes, but a
  // memo makes that identity explicit and skips the scan on unrelated
  // renders.
  const activeAutoDistill = useMemo(
    () =>
      conversation
        ? autoDistill.find((a) => a.conversation_id === conversation.id && !a.blocked_reason)
        : undefined,
    [autoDistill, conversation],
  );
  const activeAutoDistillBlocked = useMemo(
    () =>
      conversation
        ? autoDistill.find((a) => a.conversation_id === conversation.id && a.blocked_reason != null)
        : undefined,
    [autoDistill, conversation],
  );

  // P4: wake nudge — a tab becoming visible or the network returning polls
  // immediately instead of waiting out the current (possibly backed-off)
  // delay. pollNowRef no-ops until the poll effect installs the real tick.
  useEffect(() => {
    const onVisible = () => { if (!document.hidden) pollNowRef.current(); };
    const onOnline = () => pollNowRef.current();
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener("online", onOnline);
    return () => {
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener("online", onOnline);
    };
  }, []);

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

  // M11 F8: remove a registry row (phantom/stale project escape hatch).
  // Dropping the row from `projects` state also stops the 5s cross-project
  // poll for that root (the poll effect iterates `projects`), which is what
  // broke the 2026-08-11 resurrection loop: poll → respawn daemon → daemon
  // re-registers → poll again. Active project removal is refused — the
  // sidebar hides the control, this is the belt.
  const handleRemoveProject = useCallback(
    async (root: string) => {
      if (root === activeProjectRoot) return;
      try {
        const list = await removeProject(root);
        setProjects(list);
        setCrossProjectStatus((prev) => {
          if (!(root in prev)) return prev;
          const next = { ...prev };
          delete next[root];
          return next;
        });
        setError(null);
      } catch (e) {
        setError(`remove project failed: ${errorMessage(e)}`);
      }
    },
    [activeProjectRoot],
  );

  const handleSend = useCallback(
    async (text: string, attachments: string[], steer: boolean, park?: boolean) => {
      const cid = conversationRef.current;
      if (cid == null) throw new Error("no active conversation yet");
      const root = projectRootRef.current;
      // M12: the daemon disarms/cancels auto-distill on send itself; the
      // chip just re-reads promptly.
      void refreshPendingCounts();
      // J: show a spinner for /panel, /vision and /preview while the
      // daemon blocks (a preview capture + K3 call can outlast a panel).
      const advisory = isAdvisorySlash(text);
      if (advisory) setPanelThinking((n) => n + 1);
      try {
        const resp = unwrap(
          await sendMessage(cid, text, attachments, {
            steer,
            park,
            projectRoot: root ?? undefined,
          }),
        );
        // A switch flipped the view while the daemon journaled this send:
        // the row belongs to the OLD conversation — merging it into the
        // new view (and via the warming effect, its switch-cache entry)
        // would be durable cross-journal contamination; each journal's
        // seqs restart at 1. The poll loop reconciles the old view if the
        // user switches back.
        if (conversationRef.current !== cid || projectRootRef.current !== root) return;
        if (resp.event) recordEvents([resp.event]);
        // The daemon starts the agent synchronously inside send_message.
        // Steering journals a message for the running agent; parking only
        // queues a goal — neither starts a new run here. (A park on a free
        // conversation may auto-dequeue daemon-side; the poll reconciles.)
        // Advisory slash queries spawn no run either (read-only MoA
        // consult) — marking one as running flipped the composer into
        // steer mode with a Stop button until the next poll corrected it.
        if (!steer && !park && !advisory) { setAgentRunning(true); setTurnStartedAt(Date.now()); }
        // W6: prompt reconcile — the sidebar's parked pill is sourced from
        // pending_counts, so re-read after the daemon's depth changed
        // rather than waiting for the poll loop's every-4th-tick cadence.
        if (park) void refreshPendingCounts();
        setError(null);
      } catch (e) {
        setError(`send failed: ${errorMessage(e)}`);
        throw e; // let the composer keep the draft
      } finally {
        if (advisory) setPanelThinking((n) => Math.max(0, n - 1));
      }
    },
    [recordEvents, refreshPendingCounts],
  );

  // W6 (goal queue): manual resume/drop from the QueueDock. An ok:false
  // ("no parked goal with seq N") is a benign reconcile — an auto-dequeue
  // raced the click — so it never reaches the error banner; the response
  // is journaled and flows back through the poll loop, and the counts
  // refresh moves the sidebar pill now instead of at the next tick.
  const handleResumeParked = useCallback(async (seq: number) => {
    if (!conversation) return;
    try {
      await resumeParkedGoal(conversation.id, seq, project?.root_path ?? undefined);
      void refreshPendingCounts();
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [conversation, project, refreshPendingCounts]);

  const handleDropParked = useCallback(async (seq: number) => {
    if (!conversation) return;
    try {
      await dropParkedGoal(conversation.id, seq, project?.root_path ?? undefined);
      void refreshPendingCounts();
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [conversation, project, refreshPendingCounts]);

  // Steer queue: manual drop from the SteerQueue panel. Same benign-
  // reconcile posture as the goal drop (an ok:false race against the
  // drain never reaches the banner), but no refreshPendingCounts — the
  // steer queue is journal-derived only and rides no pending_counts
  // field; the journaled steer_dropped row closes it via the poll loop.
  const handleDropSteer = useCallback(async (seq: number) => {
    if (!conversation) return;
    try {
      await dropQueuedSteer(conversation.id, seq, project?.root_path ?? undefined);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [conversation, project]);

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
  useEffect(() => {
    panelTabRef.current = panelTab;
  }, [panelTab]);
  // P1a: immediate inbox fetch when the Review tab becomes visible (the
  // poll loop's ≥6s gate owns the steady-state cadence).
  useEffect(() => {
    if (panelOpen && panelTab === "review") void refreshInbox();
  }, [panelOpen, panelTab, refreshInbox]);

  // Belt A global shortcuts. Radix Dialog/Popover/Menu layers (Phase 5/6)
  // stop Esc propagation in capture phase, so this window listener never
  // fires while one is open — they do not reach the registry either.
  // Belt B adds ⌘F (chat search) and ⌘K (command palette).
  //
  // P3.3: the Esc ladder is the esc-registry, not inline ifs. The old
  // DOM-class gates (.ws-context-menu/.at-menu/.slash-menu) are registry
  // entries owned by those components (menu priority beats panel); App
  // keeps only its own four layers, registered in the old nested-if order
  // (same priority → earliest registration wins):
  //   search-close → panel-close (M9 P3: panel before cancel) →
  //   agent-cancel → blur-fallback (always active, always last).
  useEscLayer({
    id: "search",
    priority: ESC_PRIORITY.panel,
    active: () => searchOpenRef.current,
    onEscape: () => setSearchOpen(false),
  });
  useEscLayer({
    id: "panel",
    priority: ESC_PRIORITY.panel,
    active: () => panelOpenRef.current,
    onEscape: () => setPanelOpen(false),
  });
  useEscLayer({
    id: "cancel",
    priority: ESC_PRIORITY.global,
    active: () => agentRunningRef.current,
    onEscape: () => void handleCancel(),
  });
  useEscLayer({
    id: "blur-fallback",
    priority: ESC_PRIORITY.global,
    onEscape: () => (document.activeElement as HTMLElement | null)?.blur(),
  });
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        // The registry decides who consumes Esc (menus > search/panel >
        // cancel/blur); every consumer runs its own close — nothing else
        // inline remains.
        dispatchEscape();
        return;
      }
      // P1.3: mod-combo dispatch consumes the keybinds.ts registry — one
      // table drives this switch, the ⌘/ shortcuts panel, and the palette's
      // hint strings. Registry rows without a handler here (send-message)
      // are composer-owned and fall through untouched.
      const kb = matchKeyEvent(e);
      if (kb == null) return;
      if (!kb.allowedInInput && isEditableTarget(e.target)) return;
      switch (kb.id) {
        case "toggle-sidebar":
          e.preventDefault();
          setSidebarCollapsed((v) => !v);
          break;
        case "toggle-panel":
          e.preventDefault();
          setPanelOpen((v) => !v);
          break;
        case "open-settings":
          e.preventDefault();
          setSettingsOpen(true);
          break;
        case "search-chat":
          e.preventDefault();
          setSearchOpen(true);
          break;
        case "open-palette":
          e.preventDefault();
          setPaletteOpen(true);
          setPaletteInitialAction(undefined);
          break;
        case "new-workstream":
          e.preventDefault();
          setPaletteOpen(true);
          setPaletteInitialAction("new-workstream");
          break;
        case "open-shortcuts":
          e.preventDefault();
          setShortcutsOpen(true);
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // Mount-once: Esc semantics live in the registry (closures swap
    // in-place via useEscLayer), the mod-combo switch reads stable setters
    // and the keybinds table — neither arm re-subscribes.
  }, []);

  const handleSwitchWorkstream = useCallback(
    async (workstreamId: number, projectRoot?: string) => {
      const root = projectRoot ?? project?.root_path;
      if (workstreamId === workstream?.id && projectRoot === undefined) return;
      const seq = ++switchSeqRef.current;
      // Stale-while-revalidate: a previously visited workstream renders its
      // cached journal synchronously, then the authoritative bootstrap
      // merges on landing. Refs flip in this same sync block so the next
      // poll tick targets the new conversation/daemon immediately; the
      // poll loop compares its captured (cid, root) against the live refs
      // and discards anything fired under the old target.
      // The snapshot + rollback below keep a FAILED bootstrap fully
      // reversible — refs, journal, workstream attribution, and the root
      // partition all return to the pre-flip view.
      const snap = captureSwitchSnapshot(
        switchCacheRef.current,
        projectRootRef.current,
        conversationRef.current,
        lastSeqRef.current,
      );
      const prevWS = workstream;
      const flip = root != null ? switchCacheRef.current.forWorkstream(root, workstreamId) : null;
      // Computed at flip time, not in the catch: closure state
      // (activeProjectRoot) there still reads the pre-flip value.
      const rootFlipped = flip != null && root !== snap.root;
      if (flip != null && root != null) {
        projectRootRef.current = root;
        conversationRef.current = flip.conversationId;
        lastSeqRef.current = flip.journal.lastSeq;
        setEvents(flip.journal.events);
        setChatLoading(false);
        // Transient stream chrome — the next poll refills it. Everything
        // landing-scoped (diff/diffs/wikiFocus/bootstrapped/prevDiffsCount)
        // stays UNTOUCHED here so a failed switch has nothing to restore;
        // applyBootstrap resets it on landing as before.
        setPreview(null);
        setPanelProgress(null);
        if (rootFlipped) {
          setActiveProjectRoot(root);
          resetProjectAggregates();
          void refreshPendingCounts();
        }
        if (projectRoot === undefined) {
          const wsRow = workstreams.find((w) => w.id === workstreamId);
          if (wsRow) {
            setWorkstream(wsRow);
            workstreamNameRef.current = wsRow.name;
          }
        }
      }
      try {
        // A warmed, untruncated cache lets the daemon replay only the tail
        // (repeat switches no longer resend a months-long journal); a
        // truncated cache or cold path replays fully and merges on landing.
        const cached =
          flip != null && !flip.journal.truncated
            ? { conversationId: flip.conversationId, afterSeq: flip.journal.lastSeq }
            : undefined;
        const resp = unwrap(await bootstrap(root, workstreamId, cached));
        // A newer switch started while this bootstrap was in flight — its
        // state is newer, so drop this response entirely.
        if (seq !== switchSeqRef.current) return;
        // Phase 5: when switching to a foreign project's workstream, adopt
        // the new project state BEFORE applyBootstrap so poll loop and all
        // daemon calls target the correct daemon. Without this, projectRootRef
        // stays on the old root → poll hits old daemon with new conversation
        // ID → daemon-down banner (K3 Round 3 critical bug).
        if (projectRoot && projectRoot !== activeProjectRoot) {
          setActiveProjectRoot(projectRoot);
          setProject(resp.project ?? null);
          projectRootRef.current = resp.project?.root_path ?? null;
          // Root flipped: drop the old project's aggregates in the same
          // commit, then repopulate from the new daemon immediately
          // instead of waiting out the 4-tick poll cadence.
          resetProjectAggregates();
          void refreshPendingCounts();
        }
        applyBootstrap(resp);
        if (projectRoot && projectRoot !== activeProjectRoot && resp.project) {
          const list = unwrap(await listWorkstreams(resp.project.root_path));
          if (seq !== switchSeqRef.current) return;
          setWorkstreams(list.workstreams ?? []);
        }
        setError(null);
      } catch (e) {
        if (seq !== switchSeqRef.current) return;
        if (flip != null) {
          // The optimistic flip pre-empted the live view on a target whose
          // daemon never answered — restore the old view wholesale:
          // refs, rendered journal, workstream attribution, root partition.
          // lastSeq clamps to the restored snapshot so the poll refetches
          // anything the cache cannot show (rollbackView's contract).
          const view = rollbackView(snap);
          projectRootRef.current = snap.root;
          conversationRef.current = snap.conversationId;
          lastSeqRef.current = view.lastSeq;
          setEvents(view.events);
          setWorkstream(prevWS);
          workstreamNameRef.current = prevWS?.name ?? null;
          if (rootFlipped) {
            setActiveProjectRoot(snap.root);
            resetProjectAggregates();
            void refreshPendingCounts();
          }
        }
        setError(`switch failed: ${errorMessage(e)}`);
      }
    },
    [workstream, workstreams, project?.root_path, activeProjectRoot, applyBootstrap, resetProjectAggregates, refreshPendingCounts],
  );

  // P1.1 journal search fan-out: one debounced (250 ms) read-only
  // search_events per registered project, merged newest-first with the
  // project tagged on each hit. Stale responses are dropped by seq — a
  // palette keystroke during flight never lands out of order. Failures
  // degrade to that project contributing zero rows (never the banner —
  // palette search is ambient).
  useEffect(() => {
    const q = journalQuery.trim();
    if (q.length < 2 || !paletteOpen) {
      if (!paletteOpen) setJournalQuery("");
      setJournalHits(null);
      setJournalLoading(false);
      return;
    }
    setJournalLoading(true);
    const seq = ++journalSeqRef.current;
    const timer = window.setTimeout(() => {
      void (async () => {
        const buckets = await Promise.all(
          projects.map(async (p): Promise<JournalHit[]> => {
            try {
              const resp = await searchEvents(q, p.root);
              return (resp.search_results ?? []).map((result) => ({
                root: p.root,
                projectName: p.name,
                result,
              }));
            } catch {
              return [];
            }
          }),
        );
        if (seq !== journalSeqRef.current) return;
        setJournalHits(mergeHits(buckets));
        setJournalLoading(false);
      })();
    }, 250);
    return () => {
      window.clearTimeout(timer);
    };
  }, [journalQuery, paletteOpen, projects]);

  // P1.1 Enter on a hit: one-flight foreign switch (the Sidebar.tsx:374-382
  // path — root + workstream in a single bootstrap roundtrip when the hit
  // belongs to another project), then open ⌘F prefilled with the query so
  // the row's context is one keystroke away. Switch failures surface in
  // the error banner inside handleSwitchWorkstream; search stays closed.
  const handlePickJournal = useCallback(
    async (hit: JournalHit, query: string) => {
      journalSeqRef.current++;
      setJournalHits(null);
      setJournalQuery("");
      setJournalLoading(false);
      const foreign = hit.root !== (projectRootRef.current ?? null);
      if (foreign) await handleSwitchWorkstream(hit.result.workstream_id, hit.root);
      else await handleSwitchWorkstream(hit.result.workstream_id);
      setSearchQuery(query);
      setSearchOpen(true);
    },
    [handleSwitchWorkstream],
  );

  // M11 P1: full re-bootstrap against another registry project — every
  // conversation-scoped value is replaced wholesale by applyBootstrap, and
  // the bridge spawns that project's daemon on demand. The root flips only
  // after a successful bootstrap; a failure keeps the old project bound and
  // explains itself in the error banner (same posture as workstream switch).
  const handleSwitchProject = useCallback(
    async (root: string) => {
      if (root === activeProjectRoot) return;
      const seq = ++switchSeqRef.current;
      // Stale-while-revalidate, mirroring handleSwitchWorkstream: the
      // target's default conversation (the alias recorded by a
      // workstreamless bootstrap landing) renders from cache
      // synchronously; bootstrap merges on landing, and a failure restores
      // the pre-flip view from the snapshot. The workstream row itself is
      // unknown until the landing, so neither workstream state nor
      // workstreamNameRef is flipped or restored here.
      const snap = captureSwitchSnapshot(
        switchCacheRef.current,
        projectRootRef.current,
        conversationRef.current,
        lastSeqRef.current,
      );
      const flip = switchCacheRef.current.forDefault(root);
      if (flip != null) {
        projectRootRef.current = root;
        conversationRef.current = flip.conversationId;
        lastSeqRef.current = flip.journal.lastSeq;
        setEvents(flip.journal.events);
        setChatLoading(false);
        setPreview(null);
        setPanelProgress(null);
        setActiveProjectRoot(root);
        resetProjectAggregates();
        void refreshPendingCounts();
      }
      try {
        const cached =
          flip != null && !flip.journal.truncated
            ? { conversationId: flip.conversationId, afterSeq: flip.journal.lastSeq }
            : undefined;
        const resp = unwrap(await bootstrap(root, undefined, cached));
        // A newer switch started while this bootstrap was in flight — its
        // state is newer, so drop this response entirely.
        if (seq !== switchSeqRef.current) return;
        setActiveProjectRoot(root);
        setProject(resp.project ?? null);
        projectRootRef.current = resp.project?.root_path ?? null;
        // Drop the old project's aggregates in the same commit as the root
        // flip (ids collide across projects), then repopulate from the new
        // daemon immediately instead of waiting out the 4-tick cadence.
        resetProjectAggregates();
        applyBootstrap(resp, { defaultTarget: true }); // workstreamless request
        void refreshPendingCounts();
        if (resp.project) {
          const list = unwrap(await listWorkstreams(resp.project.root_path));
          if (seq !== switchSeqRef.current) return;
          setWorkstreams(list.workstreams ?? []);
        }
        setError(null);
      } catch (e) {
        if (seq !== switchSeqRef.current) return;
        if (flip != null) {
          const view = rollbackView(snap);
          projectRootRef.current = snap.root;
          conversationRef.current = snap.conversationId;
          lastSeqRef.current = view.lastSeq;
          setEvents(view.events);
          setActiveProjectRoot(snap.root);
          resetProjectAggregates();
          void refreshPendingCounts();
        }
        setError(`project switch failed: ${errorMessage(e)}`);
      }
    },
    [activeProjectRoot, applyBootstrap, resetProjectAggregates, refreshPendingCounts],
  );

  // Phase 3.7: stable callback for lazy-fetching workstreams from non-active
  // project daemons. Must be useCallback (stable identity) so the Sidebar
  // lazy-fetch effect doesn't re-fire at poll cadence. Hoisted before the
  // conditional early return (line ~1050) to comply with Rules of Hooks.
  const handleFetchWorkstreams = useCallback(async (root: string): Promise<Workstream[]> => {
    try {
      return unwrap(await listWorkstreams(root)).workstreams ?? [];
    } catch {
      return [];
    }
  }, []);

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

  // M11 F7: rename a workstream. Refreshes the list; if renaming the active
  // workstream, re-bootstraps to pick up the new name in the TopBar.
  const handleRenameWorkstream = useCallback(
    async (workstreamId: number, name: string, projectRoot?: string) => {
      const root = projectRoot ?? project?.root_path;
      if (!root) throw new Error("no project loaded yet");
      try {
        const resp = unwrap(await renameWorkstream(root, workstreamId, name));
        // Refresh the workstream list for the target root.
        const list = unwrap(await listWorkstreams(root));
        if (root === project?.root_path) {
          setWorkstreams(list.workstreams ?? []);
        }
        // If renaming the active workstream, update local state
        if (workstream && workstream.id === workstreamId && resp.workstream && root === project?.root_path) {
          setWorkstream(resp.workstream);
        }
        setError(null);
      } catch (e) {
        setError(`rename workstream failed: ${errorMessage(e)}`);
      }
    },
    [project, workstream],
  );

  // M11 F7: delete a workstream. Refuses if pending diffs exist (daemon-side
  // check). After delete, switches to the first remaining workstream.
  // K3 ctxmenu review: accept optional projectRoot so the context menu
  // can delete a foreign project's workstream in-place (no switch).
  const handleDeleteWorkstream = useCallback(
    async (workstreamId: number, projectRoot?: string) => {
      const root = projectRoot ?? project?.root_path;
      if (!root) throw new Error("no project loaded yet");
      try {
        const resp = unwrap(await deleteWorkstream(root, workstreamId));
        const remaining = resp.workstreams ?? [];
        if (root === project?.root_path) {
          setWorkstreams(remaining);
          // If deleting the active workstream, switch to the first remaining
          if (workstream && workstream.id === workstreamId) {
            if (remaining.length > 0) {
              await handleSwitchWorkstream(remaining[0].id);
            } else {
              setWorkstream(null);
              setEvents([]);
            }
          }
        }
        // For foreign projects, the sidebar's onFetchWorkstreams will
        // refresh that project's list on the next expand/poll. The
        // deleted row simply disappears from the remote list.
        setError(null);
      } catch (e) {
        setError(`delete workstream failed: ${errorMessage(e)}`);
      }
    },
    [project, workstream, handleSwitchWorkstream],
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

  // M9 P3: every former modal affordance (Wiki, Review proposals, Ledger)
  // now pivots to the right panel on the matching tab — one helper shared
  // by the TopBar buttons, the palette, and the toast click-throughs.
  const openPanelTab = useCallback((tab: PanelTab, memSubTab?: "proposals" | "files") => {
    setPanelOpen(true);
    openTab(tab);
    // Deep-link the memory sub-tab only when the target IS the memory tab:
    // toast click-throughs pass memSubTab="files", a bare memory badge
    // click wants "proposals". With keep-alive panels the old blanket
    // reset-to-"proposals" on EVERY pivot would now yank the sub-tab out
    // from under the user on unrelated clicks (previously it applied
    // silently on the next remount, which no longer happens).
    if (tab === "memory") {
      setMemoryFocus((prev) => ({ tab: memSubTab ?? "proposals", n: (prev?.n ?? 0) + 1 }));
    }
  }, [openTab]);

  // P1.4 run-header "N files changed" chip → Changes tab. Stable ref —
  // ChatSurface is memo'd and this rides its prop list every poll tick.
  const handleOpenChanges = useCallback(() => openPanelTab("changes"), [openPanelTab]);
  // P2.1: tool-result file refs and Open-live affordances pivot the panel
  // to the Preview tab with the requested target (stable refs — ChatSurface
  // is memo'd and these ride its prop list every poll tick).
  const handlePreviewFile = useCallback(
    (path: string) => {
      setPreviewTarget({ kind: "file", path });
      openPanelTab("preview");
    },
    [openPanelTab],
  );
  const handleOpenLiveUrl = useCallback(
    (url: string) => {
      setPreviewTarget({ kind: "url", url });
      openPanelTab("preview");
    },
    [openPanelTab],
  );
  // P2.2: a Runs-row click jumps the transcript to the run's starter
  // bubble (nonce re-fires identical seqs on later clicks).
  const handleJumpToSeq = useCallback((seq: number) => {
    setFocusSeq((prev) => ({ seq, n: (prev?.n ?? 0) + 1 }));
  }, []);
  // Grounded revise R2 (F2): ChatSurface fires this once the focused
  // bubble has been scrolled into view and the flash settled — retiring
  // the request restores the transcript window bound.
  const handleFocusSeqLanded = useCallback(() => setFocusSeq(null), []);
  // …and a workstream/conversation switch retires a standing request:
  // seqs are conversation-scoped, so a carried-over pin would collide with
  // an unrelated group in the new conversation's window.
  useEffect(() => {
    setFocusSeq(null);
  }, [conversation?.id]);

  // Fold chip's "Open note": pivot to the wiki tab and focus the folded
  // epoch's note there.
  const handleOpenFoldNote = useCallback(
    (path: string) => {
      openPanelTab("wiki");
      setWikiFocus((prev) => ({ path, n: (prev?.n ?? 0) + 1 }));
    },
    [openPanelTab],
  );

  // Toast viewport lifecycle: push shows a confirmation for 10 s; either
  // the timer or a click (which also click-throughs to its panel) removes it.
  const dismissToast = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  const pushToast = useCallback(
    (toast: ToastPayload) => {
      const id = ++toastSeqRef.current;
      setToasts((prev) => [...prev, { ...toast, id }]);
      // Track the timer so unmount cancels it — otherwise a pending toast
      // dismissal fires setState on a dead component (GLM Event S2).
      const timer = window.setTimeout(() => {
        toastTimersRef.current.delete(timer);
        dismissToast(id);
      }, TOAST_MS);
      toastTimersRef.current.add(timer);
    },
    [dismissToast],
  );
  // P2.3 (failure taxonomy): the overlay's leading actions. Reconnect
  // resets the failure counters and kicks an immediate poll (the loop
  // self-heals daemonDown on the next healthy tick); past
  // POLL_FAIL_RESTART_THRESHOLD the overlay grows the same reload escape
  // hatch the legacy banner has always had (grounded revise R2, F1).
  const handleFailureReconnect = useCallback(() => {
    pollFailRef.current = 0;
    setPollFailures(0);
    setDaemonDown(false);
    // A reconnect is an explicit fresh start: any dismissal is void, so
    // the same class recurring past the threshold surfaces again (F3).
    dismissedFailureRef.current = null;
    handlePollNow();
  }, [handlePollNow]);
  // A3-2: verify_infra/panel_infra failures point at the receipts, which
  // now fold inside the Runs tab (the Ledger tab is gone).
  const handleFailureOpenJournal = useCallback(() => openPanelTab("runs"), [openPanelTab]);
  // Diagnostics snapshot: the poll counters and raw error the overlay
  // shows, plus routing context — the P2.5 design-lock JSON (daemon log
  // tail omitted: no fs-read surface without a supply-chain change).
  const handleFailureCopyDiagnostics = useCallback(() => {
    const diag = {
      pollFailures: pollFailRef.current,
      lastPollError: lastPollErrorRef.current,
      projectRoot: activeProjectRoot,
      conversationId: conversation?.id ?? null,
      timestamp: new Date().toISOString(),
    };
    navigator.clipboard
      ?.writeText(JSON.stringify(diag, null, 2))
      ?.then(() => pushToast({ text: "diagnostics copied" }))
      ?.catch(() => {});
  }, [activeProjectRoot, conversation?.id, pushToast]);
  const handleFailureDismiss = useCallback((cls: string) => {
    dismissedFailureRef.current = cls;
    setDaemonDown(false);
  }, []);
  useEffect(
    () => () => {
      for (const t of toastTimersRef.current) window.clearTimeout(t);
      toastTimersRef.current.clear();
    },
    [],
  );

  // M9 P4: owns the full distill UX — the TopBar's busy flag and the
  // success toast live here so the palette and TopBar share one path.
  // Never rejects: failures surface in the error banner only.
  const handleDistill = useCallback(async () => {
    const cid = conversationRef.current;
    if (cid == null) return; // the action buttons are disabled without a conversation
    distillingRef.current = true;
    setDistillBusy(true);
    try {
      const resp = unwrap(await distill(cid, projectRootRef.current ?? undefined));
      if (resp.epoch != null) {
        const epoch = resp.epoch;
        setConversation((prev) => (prev ? { ...prev, epoch } : prev));
      }
      // The fold chip derives boundary/note/count from the journaled
      // marker itself — no session-only mirror to keep.
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
      // The daemon's schedule/in-flight state flipped too (manual distill
      // supersedes any pending auto trigger).
      void refreshPendingCounts();
    }
  }, [refreshWikiCount, refreshMemoryProposals, pushToast, openPanelTab, refreshPendingCounts]);

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

  // M12: the countdown chip's Cancel — disarm the daemon's scheduled
  // auto-distill for the active conversation (the daemon journals the
  // disarm; any send disarms too, and an in-flight one is send-cancelled,
  // never chip-cancelled).
  const handleDisarmAutoDistill = useCallback(async () => {
    const cid = conversationRef.current;
    if (cid == null) return;
    try {
      unwrap(await autoDistillCtl(cid, "disarm", projectRootRef.current ?? undefined));
    } catch (e) {
      setError(`cancel auto-distill failed: ${errorMessage(e)}`);
    } finally {
      void refreshPendingCounts();
    }
  }, [refreshPendingCounts]);

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

  const handleAccept = useCallback(async (diffId: number, commitMessage?: string) => {
    try {
      const resp = unwrap(await acceptDiff(diffId, projectRootRef.current ?? undefined, commitMessage));
      if (resp.applied) {
        setDiff((d) => (d && d.id === diffId ? { ...d, status: "accepted" } : d));
        // The Changes tab renders from the LIST branch once the first
        // poll filled it, so the singular setDiff above is invisible
        // there: resolve the list row in the same optimistic step or the
        // record card sits "pending" until a poll contradicts it.
        setDiffs((rows) => rows.map((r) => (r.id === diffId ? { ...r, status: "accepted" } : r)));
        // P1a: the inbox row resolves instantly (accept works cross-
        // workstream by diffID); badges + dataset re-sync right behind it.
        setInboxDiffs((rows) => rows.filter((r) => r.id !== diffId));
        void refreshPendingCounts();
        void refreshInbox();
        setError(null);
      }
    } catch (e) {
      setError(`accept failed: ${errorMessage(e)}`);
    }
  }, [refreshPendingCounts, refreshInbox]);

  const handleReject = useCallback(async (diffId: number) => {
    try {
      unwrap(await rejectDiff(diffId, projectRootRef.current ?? undefined));
      setDiff((d) => (d && d.id === diffId ? { ...d, status: "rejected" } : d));
      // Same list-branch resolution as accept (Changes card parity).
      setDiffs((rows) => rows.map((r) => (r.id === diffId ? { ...r, status: "rejected" } : r)));
      // P1a: same optimistic inbox resolution as accept.
      setInboxDiffs((rows) => rows.filter((r) => r.id !== diffId));
      void refreshPendingCounts();
      void refreshInbox();
      setError(null);
    } catch (e) {
      setError(`reject failed: ${errorMessage(e)}`);
    }
  }, [refreshPendingCounts, refreshInbox]);

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
  // Keep-alive panel props (tri-review P2 #5, 2026-08-24): the six panels
  // are memo()d, so every prop reaching them needs a stable reference
  // across App's poll ticks — each inline arrow below was a fresh closure
  // per render and re-reconciled every visited hidden subtree.
  // DiffViewer's review-comment send, shared by the multi-diff list rows
  // and the single-diff fallback.
  const handleSendComments = useCallback(
    (text: string) => handleSend(text, [], agentRunning),
    [handleSend, agentRunning],
  );
  // ReviewInbox's jump to a row's owning workstream (StatusBar's
  // onJumpWorkstream inline stays — not panel-scoped).
  const handleInboxJump = useCallback(
    (id: number) => {
      void handleSwitchWorkstream(id);
    },
    [handleSwitchWorkstream],
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
  // P1.5: a errors.ts-summarized banner is STICKY — classified failures
  // wait for the explicit ×, never the 10 s fade.
  useEffect(() => {
    if (error === null) return;
    if (!booted) return; // bootstrap error — keep it visible
    if (summarizeError(error) !== null) return; // sticky (P1.5)
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
      icon: <Plus size={14} />,
      // P1.3: hints render from the registry, never a literal string.
      shortcut: comboFor("new-workstream"),
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
      icon: <Sparkles size={14} />,
      disabled: conversation == null,
      onRun: () => handleDistill(),
    },
    {
      id: "curate",
      name: "Curate Topics",
      icon: <Wand2 size={14} />,
      disabled: conversation == null,
      onRun: () => handleCurate(),
    },
    {
      id: "pin",
      name: "Pin Memory",
      icon: <MapPin size={14} />,
      prompt: "remember: …",
      disabled: conversation == null,
      onRun: (text) => handlePin(text),
    },
    {
      id: "open-wiki",
      name: "Open Wiki",
      icon: <FileText size={14} />,
      disabled: conversation == null,
      onRun: () => openPanelTab("wiki"),
    },
    {
      id: "open-settings",
      name: "Open Settings",
      icon: <Settings size={14} />,
      shortcut: comboFor("open-settings"),
      onRun: () => setSettingsOpen(true),
    },
    ...(agentRunning
      ? [
          {
            id: "cancel-run",
            name: "Cancel Run",
            icon: <Square size={14} />,
            shortcut: comboFor("cancel-run"),
            onRun: () => handleCancel(),
          } satisfies PaletteAction,
        ]
      : []),
    {
      id: "toggle-sidebar",
      name: "Toggle Sidebar",
      icon: <ChevronLeft size={14} />,
      shortcut: comboFor("toggle-sidebar"),
      onRun: () => setSidebarCollapsed((v) => !v),
    },
    {
      id: "toggle-panel",
      name: "Toggle Context Panel",
      icon: <Columns size={14} />,
      shortcut: comboFor("toggle-panel"),
      onRun: () => setPanelOpen((v) => !v),
    },
    {
      id: "search-chat",
      name: "Search Chat",
      icon: <Search size={14} />,
      shortcut: comboFor("search-chat"),
      onRun: () => setSearchOpen(true),
    },
  ];

  return (
    <div className="app-shell">
      {daemonDown && (() => {
        // P2.3 (failure taxonomy): a classifiable poll failure renders the
        // typed overlay (title + one-line cause + leading action); the raw
        // poll line rides the cause's title attr. Unclassified failures
        // keep the legacy banner below, byte for byte.
        const spec = lastPollError !== null ? classifyFailure(lastPollError) : null;
        if (spec !== null) {
          return (
            <FailureOverlay
              spec={spec}
              raw={lastPollError!}
              onReconnect={handleFailureReconnect}
              onCopyDiagnostics={handleFailureCopyDiagnostics}
              onOpenJournal={handleFailureOpenJournal}
              onDismiss={() => handleFailureDismiss(spec.cls)}
              // F1: the classified lane keeps the banner's past-threshold
              // escape hatch — at the same count the overlay grows the
              // reload affordance instead of hiding it.
              onReload={
                pollFailures >= POLL_FAIL_RESTART_THRESHOLD
                  ? () => window.location.reload()
                  : undefined
              }
            />
          );
        }
        return (
        <div className="daemon-down-banner" role="alert">
          <WifiOff size={14} />
          <span>{strings.banner.daemonDown}</span>
          {pollFailures >= POLL_FAIL_RESTART_THRESHOLD && (
            // P4: escape hatch past 20 consecutive failures. gui/src-tauri
            // has no restart/respawn command, so reload is the lever —
            // bootstrap respawns the daemon if its socket is dead.
            <button
              type="button"
              className="daemon-down-restart"
              title={strings.banner.daemonRestartTitle}
              onClick={() => window.location.reload()}
            >
              {strings.banner.daemonRestart}
            </button>
          )}
        </div>
        );
      })()}
      <TopBar
        projectName={
          projects.find((p) => p.root === activeProjectRoot)?.name ??
          project?.name ??
          null
        }
        workstreamName={workstream?.name ?? null}
        onToggleSidebar={() => setSidebarCollapsed((v) => !v)}
        sidebarCollapsed={sidebarCollapsed}
        panelOpen={panelOpen}
        onTogglePanel={() => setPanelOpen((v) => !v)}
        onDistill={handleDistill}
        onOpenWiki={() => openPanelTab("wiki")}
        onCurate={handleCurate}
        onPin={handlePin}
        onOpenSettings={() => setSettingsOpen(true)}
        // A3-2: the Ledger tab is gone — the overflow item opens ledger.md
        // through the Preview tab's file pathway instead.
        onOpenLedger={() => handlePreviewFile(".odo/ledger.md")}
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
        onRemoveProject={(root) => void handleRemoveProject(root)}
        onAddProject={() => void handleAddProject()}
        workstreams={workstreams}
        workstream={workstream}
        agentRunning={agentRunning}
        pendingCounts={pendingCounts}
        parkedCounts={parkedGoals}
        runningWorkstreams={runningWorkstreams}
        fgRunLabel={fgRunLabel}
        onSwitchWorkstream={handleSwitchWorkstream}
        onOpenForeignWorkstream={(root, wsId) => void handleSwitchWorkstream(wsId, root)}
        onCreateWorkstream={handleCreateWorkstream}
        onRenameWorkstream={handleRenameWorkstream}
        onDeleteWorkstream={handleDeleteWorkstream}
        collapsed={sidebarCollapsed}
        onToggleCollapsed={() => setSidebarCollapsed((v) => !v)}
        onFetchWorkstreams={handleFetchWorkstreams}
        onRefreshRemoteWorkstreams={handleFetchWorkstreams}
      />
      {settingsOpen && (
        <SettingsPanel
          projectRoot={project?.root_path ?? null}
          onClose={() => setSettingsOpen(false)}
          onSaved={() => void refreshSettings()}
        />
      )}
      {paletteOpen && (
        <CommandPalette
          actions={paletteActions}
          onClose={() => setPaletteOpen(false)}
          initialActionId={paletteInitialAction}
          onQueryChange={setJournalQuery}
          journal={journalHits}
          journalLoading={journalLoading}
          onPickJournal={(hit, query) => void handlePickJournal(hit, query)}
        />
      )}
      {shortcutsOpen && <ShortcutsPanel onClose={() => setShortcutsOpen(false)} />}
      <main className="app-main" ref={setAppMainEl}>
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
                openPanelTab("runs"); // A3-2: receipts live under Runs now
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
        {error && (() => {
          // P1.5 (errors.ts): classified failures render summary + action
          // with the raw string preserved on hover (title), and stay up
          // until the explicit × — "sticky". Unclassified strings keep the
          // legacy raw render (+ 10 s auto-dismiss, per the effect above).
          const classified = summarizeError(error);
          return (
            <div className="error-banner" role="alert" data-sticky={classified !== null ? "true" : undefined}>
              <span title={classified !== null ? error : undefined}>
                {classified !== null ? classified.summary : error}
                {classified?.action != null && (
                  <span className="error-action opacity-80"> — {classified.action}</span>
                )}
              </span>
              <button
                type="button"
                className="dismiss-btn"
                aria-label="Dismiss error"
                onClick={() => setError(null)}
              >
                <X size={14} />
              </button>
            </div>
          );
        })()}
        <ChatSurface
          events={events}
          agentRunning={agentRunning}
          preview={preview}
          // A daemon-side consult that outlived this window's send (GUI
          // reopened mid-/panel) still shows the spinner via the tally.
          panelThinking={panelThinking > 0 || panelProgress != null}
          panelProgress={panelProgress}
          sendDisabled={!booted}
          onSend={handleSend}
          onCancel={handleCancel}
          epoch={conversation?.epoch ?? 1}
          conversationId={conversation?.id}
          onOpenNote={handleOpenFoldNote}
          searchOpen={searchOpen}
          searchQuery={searchQuery}
          onSearchQueryChange={setSearchQuery}
          onSearchClose={handleSearchClose}
          onOpenChanges={handleOpenChanges}
          onPreviewFile={handlePreviewFile}
          onOpenLiveUrl={handleOpenLiveUrl}
          focusSeq={focusSeq}
          onFocusSeqLanded={handleFocusSeqLanded}
          // M12: the composer chip discloses the daemon's auto-distill
          // state for the active conversation; the lock covers MANUAL
          // distill only — an auto distill is send-cancelled, it never
          // blocks typing.
          autoDistill={activeAutoDistill}
          autoDistillBlocked={activeAutoDistillBlocked}
          distillInFlight={conversation != null && distillingConvs.includes(conversation.id)}
          onDisarmAutoDistill={handleDisarmAutoDistill}
          distillLocked={distillBusy}
          // M12 (D-todo) + UX-1 D2: the Plan chip reads App's single
          // journaled derive (same array the Tasks tab renders); its ops
          // re-poll promptly through the normal path.
          todoItems={todoItems}
          projectRoot={project?.root_path ?? null}
          onTodoChanged={handlePollNow}
          onTodoError={handleSurfaceError}
          // M19 (/loop) V1: the chip folds from `events` (already passed
          // above); stop/resume nudge the same prompt-repoll path.
          loops={loopStates}
          onLoopChanged={handlePollNow}
          onLoopError={handleSurfaceError}
          codingModel={appSettings?.coding_model ?? null}
          onModelChanged={handleModelChanged}
          loading={chatLoading}
          // W6 (goal queue): the composer park toggle and the QueueDock's
          // Resume/Drop; rows derive from `events` (already passed above).
          onResumeParked={handleResumeParked}
          onDropParked={handleDropParked}
          // Steer queue: the panel's Drop; rows derive from `events`
          // (already passed above).
          onDropSteer={handleDropSteer}
        />
      </main>
      <ContextPanel
        open={panelOpen}
        overlay={panelOverlay}
        activeTab={panelTab}
        onTabChange={openTab}
        parked={parkState.parked}
        contributions={panelContributions}
        badgeInput={{
          pendingDiffs: diffs.length,
          pendingReview: pendingTotal,
          wikiNotes: wikiNoteCount,
          memoryProposals: pendingMemoryProposals,
          openTodos: openTodoCount,
          activeJobs: activeJobsCount,
          activeBatches: activeBatchCountForBadge,
        }}
      >
        {/* Keep-alive (tri-review P1 #5, 2026-08-24) + P2.4 LRU park:
            each tab mounts on first activation and stays mounted only
            while it sits inside the LRU park's cap (lru.ts — active + 2
            most-recent, draft-exempt Memory/Wiki outside it); parked tabs
            unmount and re-mount/refetch on re-activation. The wrapper
            carries no classes: ContextPanel's .panel-body is a block
            scroll container (not flex), so a plain block div is
            layout-neutral for the one visible wrapper and `hidden`
            collapses the inactive ones. Existing keys/comments on the
            panels are unchanged. RunGroupBoundary in ContextPanel still
            scopes an error to its tab's subtree. */}
        <div hidden={panelTab !== "tasks"}>
          {mountedPanelTabs.has("tasks") && (conversation?.id != null ? (
            // UX-1 D2: the plan layer's panel surface — conversation-
            // scoped (the fold reads the active conversation's journal,
            // same scope guarantee as the chip). Items are App's single
            // derive handed down live: the memo'd panel skips quiet ticks
            // on the stable reference, and an events change re-renders at
            // zero re-derivation cost — no freeze ref needed (RunsPanel's
            // freeze exists to protect an inner O(journal) fold, which
            // this panel does not have).
            <TasksPanel
              conversationId={conversation.id}
              projectRoot={project?.root_path ?? null}
              items={todoItems}
              onChanged={handlePollNow}
              onError={handleSurfaceError}
              active={panelTab === "tasks"}
              disabled={!booted || distillBusy}
            />
          ) : (
            <div className="panel-empty">No active conversation.</div>
          ))}
        </div>
        <div hidden={panelTab !== "changes"}>
          {mountedPanelTabs.has("changes") && (diffs.length > 0
            ? diffs.map((d) => (
                <DiffViewer
                  key={`${projectRootRef.current ?? ""}:${d.id}`}
                  diff={d}
                  onAccept={handleAccept}
                  onReject={handleReject}
                  onSendComments={handleSendComments}
                  projectRoot={project?.root_path ?? null}
                  agentRunning={agentRunning}
                  pipelineState={pipelineStateByDiff.get(d.id)}
                />
              ))
            : diff
              ? <DiffViewer diff={diff} onAccept={handleAccept} onReject={handleReject} onSendComments={handleSendComments} projectRoot={project?.root_path ?? null} agentRunning={agentRunning} pipelineState={pipelineStateByDiff.get(diff.id)} />
              : <div className="panel-empty">No pending diffs — the next run's changes land here.</div>
          )}
        </div>
        <div hidden={panelTab !== "review"}>
          {mountedPanelTabs.has("review") && (
            // P1a: cross-workstream inbox. Rows are project-scoped, so the
            // key remounts on project switch — never render another
            // project's inbox against this one's handlers.
            <ReviewInbox
              key={project?.root_path ?? "default"}
              rows={inboxDiffs}
              onAccept={handleAccept}
              onReject={handleReject}
              projectRoot={project?.root_path ?? null}
              agentRunning={agentRunning}
              pipelineStates={pipelineStateByDiff}
              onJump={handleInboxJump}
            />
          )}
        </div>
        <div hidden={panelTab !== "wiki"}>
          {mountedPanelTabs.has("wiki") && (conversation?.id != null ? (
            // M11 P1: the key remounts the panel on project switch so no
            // cross-project state (lists, reader cache, selection) survives;
            // conversation ids can collide across projects (both are
            // per-project SQLite sequences).
            <WikiBrowser
              key={`${project?.root_path ?? "default"}:${conversation.id}`}
              conversationId={conversation.id}
              projectRoot={project?.root_path ?? null}
              focus={wikiFocus}
              active={panelTab === "wiki"}
              onDraftChange={handleWikiDraft}
            />
          ) : (
            <div className="panel-empty">No active conversation.</div>
          ))}
        </div>
        <div hidden={panelTab !== "memory"}>
          {mountedPanelTabs.has("memory") && (conversation?.id != null ? (
            <MemoryPanel
              key={`${project?.root_path ?? "default"}:${conversation.id}`}
              conversationId={conversation.id}
              workstreamName={workstream?.name}
              focus={memoryFocus}
              onApplied={handleMemoryReviewClosed}
              projectRoot={project?.root_path ?? null}
              active={panelTab === "memory"}
              strandedTotal={strandedMemoryOps}
              strandedOps={strandedOps}
              onDraftChange={handleMemoryDraft}
              onResolved={refreshPendingCounts}
              autoDistillCapResume={autoDistillCapResume}
              // FIX 3 (2026-08-26 storm fix): the chip also gates on the
              // auto-distill pref — "never" hides it even if a stale poll
              // still carries a resume time; unset settings read as the
              // daemon's default-ON posture.
              autoDistillEnabled={appSettings?.auto_distill !== "never"}
              events={panelTab === "memory" ? events : memoryEventsRef.current}
            />
          ) : (
            <div className="panel-empty">No active conversation.</div>
          ))}
        </div>
        <div hidden={panelTab !== "skills"}>
          {mountedPanelTabs.has("skills") && (
            <SkillsPanel
              key={project?.root_path ?? "default"}
              projectRoot={project?.root_path ?? null}
              active={panelTab === "skills"}
            />
          )}
        </div>
        <div hidden={panelTab !== "runs"}>
          {mountedPanelTabs.has("runs") && (conversation?.id != null ? (
            // P2.2: journal-folded runs history — pure events derive (the
            // freeze pattern above keeps a hidden tab's memo stable).
            <RunsPanel
              events={panelTab === "runs" ? events : runsEventsRef.current}
              projectRoot={project?.root_path ?? null}
              active={panelTab === "runs"}
              currentModel={appSettings?.coding_model ?? undefined}
              onJumpToSeq={handleJumpToSeq}
            />
          ) : (
            <div className="panel-empty">No active conversation.</div>
          ))}
        </div>
        <div hidden={panelTab !== "preview"}>
          {mountedPanelTabs.has("preview") && (
            // P2.1: file refs / localhost URLs from tool results land here;
            // no conversation gate — a target survives a workstream switch
            // like the read_file containment root does.
            <PreviewPanel
              target={previewTarget}
              projectRoot={project?.root_path ?? null}
              active={panelTab === "preview"}
            />
          )}
        </div>
        {k8sConfigured && (
          // D5b (A2-5 + A3-3): the gated conditional 10th tab — mounted
          // through the same keep-alive/LRU wrapper as every other body;
          // absent entirely while the setting is off-by-config.
          <div hidden={panelTab !== "jobs"}>
            {mountedPanelTabs.has("jobs") && (
              <JobsPanel k8s={k8s} namespaces={k8sNamespaces} />
            )}
          </div>
        )}
        <div hidden={panelTab !== "learning"}>
          {mountedPanelTabs.has("learning") && (
            // D9-W3 (learning control plane, pure observability):
            // project-scoped read-only fold — no conversation gate
            // (episodes/flags span conversations), keyed by project like
            // SkillsPanel so a project switch remounts and refetches.
            <LearningPanel
              key={project?.root_path ?? "default"}
              projectRoot={project?.root_path ?? null}
              active={panelTab === "learning"}
            />
          )}
        </div>
      </ContextPanel>
      </div>
      <StatusBar
        workstreamName={workstream?.name ?? null}
        conversationId={conversation?.id ?? null}
        epoch={conversation?.epoch ?? 1}
        projectRoot={project?.root_path ?? null}
        agentRunning={agentRunning}
        turnStartedAt={turnStartedAt}
        backgroundRuns={backgroundRuns}
        bgNotice={bgNotice}
        onJumpWorkstream={(id) => void handleSwitchWorkstream(id)}
        lastPrompt={lastPrompt}
        events={events}
        codingModel={appSettings?.coding_model ?? null}
        reviewPanel={reviewPanel}
        pipelineStates={pipelineStates}
        gateDrift={gateDrift}
        pendingDiffs={diffs.length}
        wikiNoteCount={wikiNoteCount}
        pendingMemoryProposals={pendingMemoryProposals}
        onBadgeClick={(tab) => openPanelTab(tab)}
        k8s={k8s}
        onOpenJobsTab={() => openPanelTab("jobs")}
        onJobsVisibilityChange={setJobsChipVisible}
      />
    </div>
  );
}
