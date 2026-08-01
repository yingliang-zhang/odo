import { ClipboardEvent, DragEvent, FormEvent, useEffect, useRef, useState } from "react";
import { getCurrentWebview } from "@tauri-apps/api/webview";
import { basename } from "../files";
import type { OdoEvent } from "../types";
import MessageBubble from "./MessageBubble";
import ToolTicker from "./ToolTicker";

interface Props {
  events: OdoEvent[];
  agentRunning: boolean;
  sendDisabled: boolean;
  onSend: (text: string, attachments: string[]) => Promise<void>;
}

// Chat log plus composer. File attachments arrive via Tauri's drag-drop
// events — the only source of real file paths in the webview — and via
// clipboard paste. HTML5 drag events are kept as a fallback; they only fire
// if `dragDropEnabled` is ever disabled in tauri.conf.json, so the two paths
// never double-fire.
export default function ChatSurface({ events, agentRunning, sendDisabled, onSend }: Props) {
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [attachments, setAttachments] = useState<string[]>([]);
  const [dragOver, setDragOver] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);
  const composerRef = useRef<HTMLDivElement>(null);

  // Keep the newest event in view as the journal grows.
  useEffect(() => {
    const el = listRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [events.length, agentRunning]);

  const overComposer = (pos: { x: number; y: number }): boolean => {
    const rect = composerRef.current?.getBoundingClientRect();
    if (!rect) return true; // no layout yet: accept rather than drop silently
    return pos.x >= rect.left && pos.x <= rect.right && pos.y >= rect.top && pos.y <= rect.bottom;
  };

  const addAttachments = (paths: string[]) => {
    const clean = paths.filter((p) => p.trim() !== "");
    if (clean.length === 0) return;
    setAttachments((prev) => {
      const next = [...prev];
      for (const p of clean) {
        if (!next.includes(p)) next.push(p);
      }
      return next;
    });
  };

  // Tauri drag-drop: window-level events carrying absolute file paths.
  // Highlight the composer only while the pointer is actually over it.
  useEffect(() => {
    const unlisten = getCurrentWebview().onDragDropEvent((event) => {
      const p = event.payload;
      if (p.type === "enter" || p.type === "over") {
        setDragOver(overComposer(p.position));
      } else if (p.type === "drop") {
        setDragOver(false);
        if (overComposer(p.position)) addAttachments(p.paths);
      } else if (p.type === "leave") {
        setDragOver(false);
      }
    });
    return () => {
      void unlisten.then((u) => u());
    };
  }, []);

  // HTML5 fallback (see component doc comment for when this can fire).
  const handleDragOver = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setDragOver(true);
  };
  const handleDragLeave = () => setDragOver(false);
  const handleDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setDragOver(false);
    addAttachments(Array.from(e.dataTransfer.files).map((f) => f.name));
  };

  // Clipboard paste: file objects expose only a name in the webview, so the
  // chip carries that name; the daemon resolves paths against the project.
  const handlePaste = (e: ClipboardEvent<HTMLInputElement>) => {
    const files = Array.from(e.clipboardData?.files ?? []);
    if (files.length === 0) return; // plain text paste: let it through
    e.preventDefault();
    addAttachments(files.map((f) => f.name));
  };

  const removeAttachment = (path: string) => {
    setAttachments((prev) => prev.filter((p) => p !== path));
  };

  const canSend = draft.trim() !== "" || attachments.length > 0;

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const text = draft.trim();
    if (!canSend || sending || sendDisabled) return;
    setSending(true);
    try {
      await onSend(text, attachments);
      setDraft("");
      setAttachments([]);
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
      <div
        className={`chat-composer${dragOver ? " drag-over" : ""}`}
        ref={composerRef}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        {attachments.length > 0 && (
          <div className="attachment-chips">
            {attachments.map((path) => (
              <span className="attachment-chip" key={path} title={path}>
                <code>{basename(path)}</code>
                <button
                  type="button"
                  className="chip-remove"
                  aria-label={`Remove ${basename(path)}`}
                  onClick={() => removeAttachment(path)}
                >
                  ×
                </button>
              </span>
            ))}
          </div>
        )}
        <form className="chat-input" onSubmit={handleSubmit}>
          <input
            type="text"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onPaste={handlePaste}
            placeholder={
              dragOver
                ? "Drop files to attach them…"
                : agentRunning
                  ? "Agent is running…"
                  : "Describe the change you want…"
            }
            disabled={sendDisabled || sending}
            autoFocus
          />
          <button type="submit" disabled={sendDisabled || sending || !canSend}>
            Send
          </button>
        </form>
      </div>
    </section>
  );
}
