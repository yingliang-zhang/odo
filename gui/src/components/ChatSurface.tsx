import { FormEvent, useEffect, useRef, useState } from "react";
import type { OdoEvent } from "../types";
import MessageBubble from "./MessageBubble";
import ToolTicker from "./ToolTicker";

interface Props {
  events: OdoEvent[];
  agentRunning: boolean;
  sendDisabled: boolean;
  onSend: (text: string) => Promise<void>;
}

export default function ChatSurface({ events, agentRunning, sendDisabled, onSend }: Props) {
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);

  // Keep the newest event in view as the journal grows.
  useEffect(() => {
    const el = listRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [events.length, agentRunning]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const text = draft.trim();
    if (text === "" || sending || sendDisabled) return;
    setSending(true);
    try {
      await onSend(text);
      setDraft("");
    } catch {
      // onSend already surfaced the error; keep the draft for retry.
    } finally {
      setSending(false);
    }
  };

  return (
    <section className="chat-surface">
      <div className="message-list" ref={listRef}>
        {events.length === 0 && (
          <div className="empty-hint">
            No messages yet. Ask the agent to change something — every run is journaled
            and its diff lands here for review.
          </div>
        )}
        {events.map((ev) => (
          <MessageBubble key={ev.seq} event={ev} />
        ))}
        <ToolTicker running={agentRunning} events={events} />
      </div>
      <form className="chat-input" onSubmit={handleSubmit}>
        <input
          type="text"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={agentRunning ? "Agent is running…" : "Describe the change you want…"}
          disabled={sendDisabled || sending}
          autoFocus
        />
        <button type="submit" disabled={sendDisabled || sending || draft.trim() === ""}>
          Send
        </button>
      </form>
    </section>
  );
}
