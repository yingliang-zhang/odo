import { useCallback, useEffect, useRef, useState } from "react";
import {
  acceptDiff,
  bootstrap,
  createWorkstream,
  distill,
  errorMessage,
  fanoutSend,
  listWorkstreams,
  pollEvents,
  rejectDiff,
  sendMessage,
  unwrap,
} from "./api";
import ChatSurface from "./components/ChatSurface";
import DiffViewer from "./components/DiffViewer";
import Sidebar from "./components/Sidebar";
import type { BootstrapResponse, Conversation, Diff, OdoEvent, Project, Workstream } from "./types";

// Polling is the declared transport for M0 (no SSE/WebSocket).
const POLL_INTERVAL_MS = 1500;

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
  const [lastDistillPath, setLastDistillPath] = useState<string | null>(null);
  const [booted, setBooted] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const lastSeqRef = useRef(0);
  const conversationRef = useRef<number | null>(null);
  // The daemon serves one connection at a time and distill can block for
  // minutes; polling is paused for the duration instead of queueing up
  // certain timeout failures.
  const distillingRef = useRef(false);

  const recordEvents = useCallback((incoming: OdoEvent[]) => {
    if (incoming.length === 0) return;
    setEvents((prev) => mergeEvents(prev, incoming));
    lastSeqRef.current = Math.max(lastSeqRef.current, ...incoming.map((e) => e.seq));
  }, []);

  // Whole-context replacement: bootstrap (initial or workstream switch)
  // returns the target conversation's full journal, which supersedes any
  // events accumulated under the previous workstream.
  const applyBootstrap = useCallback((resp: BootstrapResponse) => {
    setWorkstream(resp.workstream ?? null);
    setConversation(resp.conversation ?? null);
    conversationRef.current = resp.conversation?.id ?? null;
    const evs = resp.events ?? [];
    lastSeqRef.current = evs.reduce((max, e) => Math.max(max, e.seq), 0);
    setEvents(evs);
    setAgentRunning(resp.agent_running ?? false);
    setDiff(resp.diff ?? null);
    setLastDistillPath(null);
  }, []);

  // Session restore: bootstrap returns project/workstream/conversation plus
  // the full journaled event history and the latest diff.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = unwrap(await bootstrap());
        if (cancelled) return;
        setProject(resp.project ?? null);
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
      if (distillingRef.current) return;
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
        setError(null);
      } catch (e) {
        if (!distillingRef.current) {
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
      setError(null);
      return resp.wiki_path ?? "";
    } catch (e) {
      setError(`distill failed: ${errorMessage(e)}`);
      throw e; // Sidebar clears its busy state; keep the toast hidden.
    } finally {
      distillingRef.current = false;
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
      />
      <main className="app-main">
        {error && <div className="error-banner">{error}</div>}
        <ChatSurface
          events={events}
          agentRunning={agentRunning}
          sendDisabled={!booted}
          onSend={handleSend}
          onFanout={handleFanout}
          epoch={conversation?.epoch ?? 1}
          distilledTo={lastDistillPath}
        />
        {diff && <DiffViewer diff={diff} onAccept={handleAccept} onReject={handleReject} />}
      </main>
    </div>
  );
}
