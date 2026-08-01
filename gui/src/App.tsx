import { useCallback, useEffect, useRef, useState } from "react";
import { acceptDiff, bootstrap, errorMessage, pollEvents, rejectDiff, sendMessage, unwrap } from "./api";
import ChatSurface from "./components/ChatSurface";
import DiffViewer from "./components/DiffViewer";
import Sidebar from "./components/Sidebar";
import type { Conversation, Diff, OdoEvent, Project, Workstream } from "./types";

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
  const [conversation, setConversation] = useState<Conversation | null>(null);
  const [events, setEvents] = useState<OdoEvent[]>([]);
  const [agentRunning, setAgentRunning] = useState(false);
  const [diff, setDiff] = useState<Diff | null>(null);
  const [booted, setBooted] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const lastSeqRef = useRef(0);
  const conversationRef = useRef<number | null>(null);

  const recordEvents = useCallback((incoming: OdoEvent[]) => {
    if (incoming.length === 0) return;
    setEvents((prev) => mergeEvents(prev, incoming));
    lastSeqRef.current = Math.max(lastSeqRef.current, ...incoming.map((e) => e.seq));
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
        setWorkstream(resp.workstream ?? null);
        setConversation(resp.conversation ?? null);
        conversationRef.current = resp.conversation?.id ?? null;
        recordEvents(resp.events ?? []);
        setAgentRunning(resp.agent_running ?? false);
        setDiff(resp.diff ?? null);
        setBooted(true);
      } catch (e) {
        if (!cancelled) setError(`bootstrap failed: ${errorMessage(e)}`);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [recordEvents]);

  // Poll the daemon for new journal events after the last seen seq.
  useEffect(() => {
    if (!booted) return;
    let inFlight = false;
    const tick = async () => {
      const cid = conversationRef.current;
      if (cid == null || inFlight) return;
      inFlight = true;
      try {
        const resp = unwrap(await pollEvents(cid, lastSeqRef.current));
        recordEvents(resp.events ?? []);
        setAgentRunning(resp.agent_running ?? false);
        // The daemon always reports the latest diff (any status); only a
        // pending one is actionable in the UI.
        if (resp.diff) setDiff(resp.diff);
        setError(null);
      } catch (e) {
        setError(`poll failed: ${errorMessage(e)}`);
      } finally {
        inFlight = false;
      }
    };
    const timer = setInterval(() => void tick(), POLL_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [booted, recordEvents]);

  const handleSend = useCallback(
    async (text: string, attachments: string[]) => {
      const cid = conversationRef.current;
      if (cid == null) throw new Error("no active conversation yet");
      try {
        const resp = unwrap(await sendMessage(cid, text, attachments));
        if (resp.event) recordEvents([resp.event]);
        // The daemon starts the agent synchronously inside send_message.
        setAgentRunning(true);
        setError(null);
      } catch (e) {
        setError(`send failed: ${errorMessage(e)}`);
        throw e; // let the composer keep the draft
      }
    },
    [recordEvents],
  );

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
      <Sidebar project={project} workstream={workstream} conversationId={conversation?.id ?? null} />
      <main className="app-main">
        {error && <div className="error-banner">{error}</div>}
        <ChatSurface
          events={events}
          agentRunning={agentRunning}
          sendDisabled={!booted || agentRunning}
          onSend={handleSend}
        />
        {diff && <DiffViewer diff={diff} onAccept={handleAccept} onReject={handleReject} />}
      </main>
    </div>
  );
}
