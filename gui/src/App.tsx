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
import DiffViewer from "./components/DiffViewer";
import SettingsPanel from "./components/SettingsPanel";
import Sidebar from "./components/Sidebar";
import { notifyRunDone } from "./notify";
import type { BootstrapResponse, Conversation, Diff, OdoEvent, Project, Workstream } from "./types";

// Polling is the declared transport for M0 (no SSE/WebSocket).
const POLL_INTERVAL_MS = 1500;

// M4 (spec §8): the "memory updated" chip auto-dismisses after 30 s.
const MEMORY_CHIP_MS = 30_000;

function mergeEvents(prev: OdoEvent[], next: OdoEvent[]): OdoEvent[] {
  if (next.length === 0) return prev;
  const seen = new Set(prev.map((e) => e.seq));
  const fresh = next.filter((e) => !seen.has(e.seq));
  if (fresh.length === 0) return prev;
  return [...prev, ...fresh].sort((a, b) => a.seq - b.seq);
}

export default function App() {
  const [project, setProject] = useState<Project | null>(null);
  const [workstream, setWorkstream] = useState<Workstream | null>(null);
  const [workstreams, setWorkstreams] = useState<Workstream[]>([]);
  const [conversation, setConversation] = useState<Conversation | null>(null);
  const [events, setEvents] = useState<OdoEvent[]>([]);
  const [agentRunning, setAgentRunning] = useState(false);
  const [diff, setDiff] = useState<Diff | null>(null);
  const [adapter, setAdapter] = useState("omp");
  // Belt A: sidebar collapse (⌘B) and the settings modal, lifted out of the
  // Sidebar so ⌘, opens it regardless of sidebar visibility.
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
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
  // M3 visibility (spec §3c): project-wide pending-diff counts and running
  // workstreams, refreshed every few poll ticks via pending_counts.
  const [pendingCounts, setPendingCounts] = useState<Record<number, number>>({});
  const [runningWorkstreams, setRunningWorkstreams] = useState<number[]>([]);

  const lastSeqRef = useRef(0);
  const conversationRef = useRef<number | null>(null);
  // Belt A: the forever-installed global shortcut listener reads the current
  // run state through this ref instead of re-registering every flip.
  const agentRunningRef = useRef(false);
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
      // M4 (spec §8): memory_update chips the sidebar. Deliberately NOT
      // handled in applyBootstrap — bootstrap replays history wholesale and
      // a stale memory_update must not re-chip. This callback only ever
      // sees freshly polled events.
      if (e.type === "memory_update") {
        setLastMemoryUpdate({ layer: e.payload?.layer ?? "memory", detail: e.payload?.detail });
        clearTimeout(memoryChipTimer.current);
        memoryChipTimer.current = window.setTimeout(() => setLastMemoryUpdate(null), MEMORY_CHIP_MS);
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
      setDiff(resp.diff ?? null);
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
        // The daemon always reports the latest diff (any status); only a
        // pending one is actionable in the UI.
        if (resp.diff) setDiff(resp.diff);
        // M3 (spec §3c): project-wide visibility every ~4th tick (~6s).
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
    const timer = setInterval(() => void tick(), POLL_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [booted, recordEvents]);

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

  // Belt A global shortcuts. Modals close themselves on Escape through
  // their own window listeners; the .settings-overlay check keeps a bare
  // Escape from also acting on the composer while a dialog is up. ⌘K is
  // reserved for the Belt B command palette — swallowed here for now.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        if (agentRunningRef.current) {
          void handleCancel();
          return;
        }
        if (document.querySelector(".settings-overlay") != null) return;
        (document.activeElement as HTMLElement | null)?.blur();
        return;
      }
      if (!(e.metaKey || e.ctrlKey)) return;
      switch (e.key.toLowerCase()) {
        case "b":
          e.preventDefault();
          setSidebarCollapsed((v) => !v);
          break;
        case ",":
          e.preventDefault();
          setSettingsOpen(true);
          break;
        case "k":
          e.preventDefault();
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
  // memory_update{layer:"pins"} arrives on the next poll tick and chips
  // the sidebar through the generic recordEvents branch.
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

  // M4: clicking the chip dismisses it (the auto-dismiss also runs on a
  // 30 s timer); closing/applying in the review panel re-reads the badge.
  const handleMemoryChipDismiss = useCallback(() => {
    if (memoryChipTimer.current !== undefined) {
      clearTimeout(memoryChipTimer.current);
      memoryChipTimer.current = undefined;
    }
    setLastMemoryUpdate(null);
  }, []);

  const handleMemoryReviewClosed = useCallback(() => {
    const cid = conversationRef.current;
    if (cid != null) void refreshMemoryProposals(cid);
  }, [refreshMemoryProposals]);

  // Drop the chip's dismiss timer on unmount.
  useEffect(() => {
    return () => clearTimeout(memoryChipTimer.current);
  }, []);

  if (!booted && !error) {
    return <div className="app-loading">Connecting to the Odo daemon…</div>;
  }

  return (
    <div className="app-shell">
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
        onWikiBrowserClosed={handleWikiBrowserClosed}
        onCurate={handleCurate}
        onPin={handlePin}
        topicCount={topicCount}
        pendingCounts={pendingCounts}
        runningWorkstreams={runningWorkstreams}
        pendingMemoryProposals={pendingMemoryProposals}
        lastMemoryUpdate={lastMemoryUpdate}
        onMemoryChipDismiss={handleMemoryChipDismiss}
        onMemoryReviewClosed={handleMemoryReviewClosed}
        collapsed={sidebarCollapsed}
        onToggleCollapsed={() => setSidebarCollapsed((v) => !v)}
        onOpenSettings={() => setSettingsOpen(true)}
      />
      {settingsOpen && <SettingsPanel onClose={() => setSettingsOpen(false)} />}
      <main className="app-main">
        {error && <div className="error-banner">{error}</div>}
        <ChatSurface
          events={events}
          agentRunning={agentRunning}
          sendDisabled={!booted}
          onSend={handleSend}
          onFanout={handleFanout}
          onCancel={handleCancel}
          epoch={conversation?.epoch ?? 1}
          distilledTo={lastDistillPath}
        />
        {diff && <DiffViewer diff={diff} onAccept={handleAccept} onReject={handleReject} />}
      </main>
    </div>
  );
}
