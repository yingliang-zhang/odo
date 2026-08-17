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
  memoryProposals,
  renameWorkstream,
  pendingCounts as fetchPendingCounts,
  pin,
  pollEvents,
  rejectDiff,
  removeProject,
  resumeParkedGoal,
  dropParkedGoal,
  sendMessage,
  unwrap,
} from "./api";
import ChatSurface from "./components/ChatSurface";
import CommandPalette, { type PaletteAction } from "./components/CommandPalette";
import ContextPanel, { type PanelTab } from "./components/ContextPanel";
import DiffViewer from "./components/DiffViewer";
import LedgerPanel from "./components/LedgerPanel";
import MemoryPanel from "./components/MemoryPanel";
import ReviewInbox from "./components/ReviewInbox";
import SkillsPanel from "./components/SkillsPanel";
import SettingsPanel from "./components/SettingsPanel";
import Sidebar from "./components/Sidebar";
import StatusBar from "./components/StatusBar";
import TopBar from "./components/TopBar";
import WikiBrowser from "./components/WikiBrowser";
import { basename } from "./files";
import { notifyRunDone } from "./notify";
import { derivePipelineStates } from "./pipeline";
import { deriveLastPrompt, parseReviewModels } from "./stats";
import type { AutoDistillCountdown, BootstrapResponse, Conversation, Diff, DiffInfoEx, OdoEvent, PreviewEvent, Project, ProjectEntry, Settings as DaemonSettings, Workstream } from "./types";

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
  // Skeleton: true during bootstrap (conversation switch / first load),
  // false once events arrive. Tri-model gap analysis: Hermes has
  // skeletons.tsx; Odo had only a spinner.
  const [chatLoading, setChatLoading] = useState(true);
  // J: spinner for /panel and /vision while the daemon consults models.
  const [panelThinking, setPanelThinking] = useState(false);
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
    const VALID: PanelTab[] = ["changes", "review", "wiki", "memory", "ledger", "skills"];
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
  // Fold chip's "Open note": selects a specific note in the wiki browser.
  // The counter makes repeated clicks on the same note re-select it.
  const [wikiFocus, setWikiFocus] = useState<{ path: string; n: number } | null>(null);
  const [booted, setBooted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // E P2: daemon disconnect tracking — consecutive poll failures
  const [daemonDown, setDaemonDown] = useState(false);
  const pollFailRef = useRef(0);
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
  const panelTabRef = useRef<PanelTab>("changes");
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
      if (!counts.ok) return;
      const pending: Record<number, number> = {};
      for (const [k, v] of Object.entries(counts.pending_counts ?? {})) {
        const id = Number(k);
        if (Number.isFinite(id)) pending[id] = v;
      }
      setPendingCounts(pending);
      const parked: Record<number, number> = {};
      for (const [k, v] of Object.entries(counts.parked_goals ?? {})) {
        const id = Number(k);
        if (Number.isFinite(id)) parked[id] = v;
      }
      setParkedGoals(parked);
      setRunningWorkstreams(counts.running_workstreams ?? []);
      setAutoDistill(counts.auto_distill ?? []);
      setDistillingConvs(counts.distilling_convs ?? []);
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
      setInboxDiffs(resp.ok ? (resp.all_pending_diffs ?? []) : []);
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

  // Wave B #5: the last prompt closure is journaled data the UI already
  // holds — the meter reads the newest carrier (user_message send/slash or
  // run_prompt continuation) straight off the event stream.
  const lastPrompt = useMemo(() => deriveLastPrompt(events), [events]);
  const reviewPanel = useMemo(
    () => parseReviewModels(appSettings?.review_models ?? ""),
    [appSettings],
  );

  // Auto-land pipeline chip (design lock Phase 1): pure re-derivation off
  // the ACTIVE conversation's journaled stream (conversation scope is the
  // daemon's poll/bootstrap contract — pipeline.ts documents it) + the
  // pending-diff list. No latch: the memo's inputs are exactly the two
  // surfaces those facts arrive on.
  const pipelineStates = useMemo(
    () => derivePipelineStates(events, diffs.map((d) => d.id), appSettings?.auto_apply === "main"),
    [events, diffs, appSettings],
  );

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
  const [bgNotice, setBgNotice] = useState<{ started: string[]; finished: string[] } | null>(null);
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
    setBgNotice({ started: startedIds.map(nameOf), finished: finishedIds.map(nameOf) });
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
      setChatLoading(false); // history loaded (empty or not) — show welcome or content
      setAgentRunning(resp.agent_running ?? false);
      setPreview(null); // bootstrap carries no preview; the next poll restores it
      setDiff(resp.diff ?? null);
      setDiffs([]);
      // M9 P2: reset the bootstrap latch so the first poll after a new
      // bootstrap (switch workstream, session restore) doesn't auto-open.
      bootstrappedRef.current = false;
      prevDiffsCountRef.current = 0;
      setWikiFocus(null);
      // chatLoading was set to false at line 519 — the skeleton is gone
      // once bootstrap delivers the event history (empty or not).
      // DO NOT re-arm it here; that would overwrite the false and latch
      // the skeleton permanently (DSF final review finding).
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
        recordEvents(resp.events ?? []);
        setAgentRunning(resp.agent_running ?? false);
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
        // The daemon always reports the latest diff (any status); only a
        // pending one is actionable in the UI.
        if (resp.diff) setDiff(resp.diff);
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
        setError(null);
        // E P2: reset consecutive failure counter on success
        pollFailRef.current = 0;
        setDaemonDown(false);
      } catch (e) {
        // E P2: track consecutive poll failures for disconnect detection
        pollFailRef.current += 1;
        if (pollFailRef.current >= POLL_FAIL_THRESHOLD) {
          setDaemonDown(true);
        }
        if (!distillingRef.current && !curatingRef.current) {
          setError(`poll failed: ${errorMessage(e)}`);
        }
      } finally {
        inFlight = false;
      }
    };
    // M12 (D-todo): the Plan popover triggers an immediate re-poll after
    // its journaled op instead of waiting ~1.5 s for the next tick.
    pollNowRef.current = () => void tick();
    // M7: 350 ms while the agent runs (block-level preview latency), 1.5 s
    // idle. The interval resets when agentRunning flips.
    const timer = setInterval(
      () => void tick(),
      agentRunning ? POLL_INTERVAL_RUNNING_MS : POLL_INTERVAL_IDLE_MS,
    );
    return () => clearInterval(timer);
  }, [booted, recordEvents, agentRunning, refreshPendingCounts, refreshInbox]);

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
      // M12: the daemon disarms/cancels auto-distill on send itself; the
      // chip just re-reads promptly.
      void refreshPendingCounts();
      // J: show a spinner for /panel and /vision while the daemon blocks.
      const isPanel = text.trim().startsWith("/panel") || text.trim().startsWith("/vision");
      if (isPanel) setPanelThinking(true);
      try {
        const resp = unwrap(
          await sendMessage(cid, text, attachments, {
            steer,
            park,
            projectRoot: projectRootRef.current ?? undefined,
          }),
        );
        if (resp.event) recordEvents([resp.event]);
        // The daemon starts the agent synchronously inside send_message.
        // Steering journals a message for the running agent; parking only
        // queues a goal — neither starts a new run here. (A park on a free
        // conversation may auto-dequeue daemon-side; the poll reconciles.)
        if (!steer && !park) setAgentRunning(true);
        // W6: prompt reconcile — the sidebar's parked pill is sourced from
        // pending_counts, so re-read after the daemon's depth changed
        // rather than waiting for the poll loop's every-4th-tick cadence.
        if (park) void refreshPendingCounts();
        setError(null);
      } catch (e) {
        setError(`send failed: ${errorMessage(e)}`);
        throw e; // let the composer keep the draft
      } finally {
        if (isPanel) setPanelThinking(false);
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

  // Belt A global shortcuts. Modals close themselves on Escape through
  // their own window listeners; the overlay check keeps a bare Escape from
  // also acting on the composer while a dialog is up. Belt B adds ⌘F (chat
  // search) and ⌘K (command palette); Esc closes the search bar before it
  // reaches blur/cancel.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        // Modal/overlay takes priority — don't cancel the agent when closing a dialog
        if (document.querySelector(".settings-overlay") != null) return;
        if (document.querySelector(".palette-overlay") != null) {
          setPaletteOpen(false);
          return;
        }
        // Image lightbox (ZoomableImage) — its own Esc listener closes it,
        // but without this gate a bare Esc would also cancel the agent.
        if (document.querySelector(".md-img-lightbox") != null) return;
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
    async (workstreamId: number, projectRoot?: string) => {
      const root = projectRoot ?? project?.root_path;
      if (workstreamId === workstream?.id && projectRoot === undefined) return;
      try {
        const resp = unwrap(await bootstrap(root, workstreamId));
        // Phase 5: when switching to a foreign project's workstream, adopt
        // the new project state BEFORE applyBootstrap so poll loop and all
        // daemon calls target the correct daemon. Without this, projectRootRef
        // stays on the old root → poll hits old daemon with new conversation
        // ID → daemon-down banner (K3 Round 3 critical bug).
        if (projectRoot && projectRoot !== activeProjectRoot) {
          setActiveProjectRoot(projectRoot);
          setProject(resp.project ?? null);
          projectRootRef.current = resp.project?.root_path ?? null;
        }
        applyBootstrap(resp);
        if (projectRoot && projectRoot !== activeProjectRoot && resp.project) {
          const list = unwrap(await listWorkstreams(resp.project.root_path));
          setWorkstreams(list.workstreams ?? []);
        }
        setError(null);
      } catch (e) {
        setError(`switch failed: ${errorMessage(e)}`);
      }
    },
    [workstream?.id, project?.root_path, activeProjectRoot, applyBootstrap],
  );

  // M11 P1: full re-bootstrap against another registry project — every
  // conversation-scoped value is replaced wholesale by applyBootstrap, and
  // the bridge spawns that project's daemon on demand. The root flips only
  // after a successful bootstrap; a failure keeps the old project bound and
  // explains itself in the error banner (same posture as workstream switch).
  const handleSwitchProject = useCallback(
    async (root: string) => {
      if (root === activeProjectRoot) return;
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
    async (workstreamId: number, name: string) => {
      const root = project?.root_path;
      if (!root) throw new Error("no project loaded yet");
      try {
        const resp = unwrap(await renameWorkstream(root, workstreamId, name));
        // Refresh the workstream list
        const list = unwrap(await listWorkstreams(root));
        setWorkstreams(list.workstreams ?? []);
        // If renaming the active workstream, update local state
        if (workstream && workstream.id === workstreamId && resp.workstream) {
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
  const handleDeleteWorkstream = useCallback(
    async (workstreamId: number) => {
      const root = project?.root_path;
      if (!root) throw new Error("no project loaded yet");
      try {
        const resp = unwrap(await deleteWorkstream(root, workstreamId));
        const remaining = resp.workstreams ?? [];
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
    setPanelTab(tab);
    // Toast click-throughs pass memSubTab="files"; default reset to "proposals".
    setMemorySubTab(memSubTab ?? "proposals");
  }, []);

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

  const handleAccept = useCallback(async (diffId: number) => {
    try {
      const resp = unwrap(await acceptDiff(diffId, projectRootRef.current ?? undefined));
      if (resp.applied) {
        setDiff((d) => (d && d.id === diffId ? { ...d, status: "accepted" } : d));
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
      icon: <Plus size={14} />,
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
      shortcut: "⌘,",
      onRun: () => setSettingsOpen(true),
    },
    ...(agentRunning
      ? [
          {
            id: "cancel-run",
            name: "Cancel Run",
            icon: <Square size={14} />,
            shortcut: "Esc",
            onRun: () => handleCancel(),
          } satisfies PaletteAction,
        ]
      : []),
    {
      id: "toggle-sidebar",
      name: "Toggle Sidebar",
      icon: <ChevronLeft size={14} />,
      shortcut: "⌘B",
      onRun: () => setSidebarCollapsed((v) => !v),
    },
    {
      id: "toggle-panel",
      name: "Toggle Context Panel",
      icon: <Columns size={14} />,
      shortcut: "⌘J",
      onRun: () => setPanelOpen((v) => !v),
    },
    {
      id: "search-chat",
      name: "Search Chat",
      icon: <Search size={14} />,
      shortcut: "⌘F",
      onRun: () => setSearchOpen(true),
    },
  ];

  return (
    <div className="app-shell">
      {daemonDown && (
        <div className="daemon-down-banner" role="alert">
          <WifiOff size={14} />
          <span>Daemon connection lost — retrying…</span>
        </div>
      )}
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
      />
      {settingsOpen && (
        <SettingsPanel
          projectRoot={project?.root_path ?? null}
          onClose={() => setSettingsOpen(false)}
          onSaved={() => void refreshSettings()}
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
              <X size={14} />
            </button>
          </div>
        )}
        <ChatSurface
          events={events}
          agentRunning={agentRunning}
          preview={preview}
          panelThinking={panelThinking}
          sendDisabled={!booted}
          onSend={handleSend}
          onCancel={handleCancel}
          epoch={conversation?.epoch ?? 1}
          conversationId={conversation?.id}
          onOpenNote={handleOpenFoldNote}
          searchOpen={searchOpen}
          searchQuery={searchQuery}
          onSearchQueryChange={setSearchQuery}
          onSearchClose={() => setSearchOpen(false)}
          // M12: the composer chip discloses the daemon's auto-distill
          // state for the active conversation; the lock covers MANUAL
          // distill only — an auto distill is send-cancelled, it never
          // blocks typing.
          autoDistill={conversation ? autoDistill.find((a) => a.conversation_id === conversation.id && !a.blocked_reason) : undefined}
          autoDistillBlocked={conversation ? autoDistill.find((a) => a.conversation_id === conversation.id && a.blocked_reason != null) : undefined}
          distillInFlight={conversation != null && distillingConvs.includes(conversation.id)}
          onDisarmAutoDistill={handleDisarmAutoDistill}
          distillLocked={distillBusy}
          // M12 (D-todo): the Plan chip reads the journaled events and its
          // ops re-poll promptly through the normal path.
          projectRoot={project?.root_path ?? null}
          onTodoChanged={() => pollNowRef.current()}
          onTodoError={(m) => setError(m)}
          codingModel={appSettings?.coding_model ?? null}
          onModelChanged={() => {
            void refreshSettings();
          }}
          loading={chatLoading}
          // W6 (goal queue): the composer park toggle and the QueueDock's
          // Resume/Drop; rows derive from `events` (already passed above).
          onResumeParked={handleResumeParked}
          onDropParked={handleDropParked}
        />
      </main>
      <ContextPanel
        open={panelOpen}
        onClose={() => setPanelOpen(false)}
        activeTab={panelTab}
        onTabChange={setPanelTab}
        changesBadge={diffs.length > 0 ? diffs.length : undefined}
        reviewBadge={pendingTotal > 0 ? pendingTotal : undefined}
        wikiBadge={wikiNoteCount ?? undefined}
        memoryBadge={pendingMemoryProposals > 0 ? pendingMemoryProposals : undefined}
      >
        {panelTab === "changes" && (diffs.length > 0
          ? diffs.map((d) => (
              <DiffViewer
                key={`${projectRootRef.current ?? ""}:${d.id}`}
                diff={d}
                onAccept={handleAccept}
                onReject={handleReject}
                onSendComments={(text) => handleSend(text, [], agentRunning)}
                projectRoot={project?.root_path ?? null}
                agentRunning={agentRunning}
              />
            ))
          : diff
            ? <DiffViewer diff={diff} onAccept={handleAccept} onReject={handleReject} onSendComments={(text) => handleSend(text, [], agentRunning)} projectRoot={project?.root_path ?? null} agentRunning={agentRunning} />
            : <div className="panel-empty">No pending diffs — the next run's changes land here.</div>
        )}
        {panelTab === "review" && (
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
            onJump={(id) => void handleSwitchWorkstream(id)}
          />
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
            focus={wikiFocus}
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
            // A-P0 #1: the review-action cells read the same journaled
            // events ChatSurface renders — live, no extra IPC.
            events={events}
          />
        ) : (
          <div className="panel-empty">No active conversation.</div>
        ))}
        {panelTab === "skills" && (
          <SkillsPanel
            key={project?.root_path ?? "default"}
            projectRoot={project?.root_path ?? null}
          />
        )}
      </ContextPanel>
      </div>
      <StatusBar
        workstreamName={workstream?.name ?? null}
        conversationId={conversation?.id ?? null}
        epoch={conversation?.epoch ?? 1}
        projectRoot={project?.root_path ?? null}
        agentRunning={agentRunning}
        backgroundRuns={backgroundRuns}
        bgNotice={bgNotice}
        onJumpWorkstream={(id) => void handleSwitchWorkstream(id)}
        lastPrompt={lastPrompt}
        codingModel={appSettings?.coding_model ?? null}
        reviewPanel={reviewPanel}
        pipelineStates={pipelineStates}
        pendingDiffs={diffs.length}
        wikiNoteCount={wikiNoteCount}
        pendingMemoryProposals={pendingMemoryProposals}
        onBadgeClick={(tab) => openPanelTab(tab)}
      />
    </div>
  );
}
