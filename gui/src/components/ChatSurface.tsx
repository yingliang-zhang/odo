import { ClipboardEvent, DragEvent, FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { getCurrentWebview } from "@tauri-apps/api/webview";
import { basename } from "../files";
import type { OdoEvent } from "../types";
import MessageBubble from "./MessageBubble";
import ToolTicker from "./ToolTicker";

interface Props {
  events: OdoEvent[];
  agentRunning: boolean;
  sendDisabled: boolean;
  onSend: (text: string, attachments: string[], steer: boolean) => Promise<void>;
  // M1 memory distiller: current epoch (banner shown when > 1) and the wiki
  // path of the most recent distill, when known this session.
  epoch: number;
  distilledTo?: string | null;
}

// Chat log plus composer. File attachments arrive via Tauri's drag-drop
// events — the only source of real file paths in the webview — and via
// clipboard paste. HTML5 drag events are kept as a fallback; they only fire
// if `dragDropEnabled` is ever disabled in tauri.conf.json, so the two paths
// never double-fire.
export default function ChatSurface({ events, agentRunning, sendDisabled, onSend, epoch, distilledTo }: Props) {
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
  // The composer is the only sensible drop target in M0.1, so we accept
  // all drops without position checking — Tauri 2's PhysicalPosition
  // coordinate space is inconsistent across platforms and DPI modes.
  useEffect(() => {
    const unlisten = getCurrentWebview().onDragDropEvent((event) => {
      const p = event.payload;
      if (p.type === "enter" || p.type === "over") {
        setDragOver(true);
      } else if (p.type === "drop") {
        setDragOver(false);
        addAttachments(p.paths);
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
      // M1 steering: while the agent runs, the button is a Steer button and
      // the same message journals with steer=true instead of a new run.
      await onSend(text, attachments, agentRunning);
      setDraft("");
      setAttachments([]);
    } catch {
      // onSend already surfaced the error; keep the draft for retry.
    } finally {
      setSending(false);
    }
  };

  // M1 epoch filtering: show only events from the current epoch.
  // The last distill review_action marks the epoch boundary; events after
  // it belong to the current epoch. If no distill has happened, show all.
  const lastDistillSeq = useMemo(() => {
    for (let i = events.length - 1; i >= 0; i--) {
      const e = events[i];
      if (e.type === "review_action" && e.payload?.action === "distill") {
        return e.seq;
      }
    }
    return 0;
  }, [events]);

  const visibleEvents = events.filter((e) => e.seq > lastDistillSeq);

  return (
    <section className="chat-surface">
      <div className="message-list" ref={listRef}>
        {epoch > 1 && (
          <div className="epoch-banner">
            Epoch {epoch}
            {distilledTo ? (
              <>
                {" — previous epoch distilled to "}
                <code>{distilledTo}</code>
              </>
            ) : (
              " — previous epochs distilled to the wiki"
            )}
          </div>
        )}
        {visibleEvents.length === 0 && (
          <div className="empty-hint">
            No messages yet. Ask the agent to change something — every run is journaled
            and its diff lands here for review.
          </div>
        )}
        {visibleEvents.map((ev) => (
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
                  ? "Steer the running agent…"
                  : "Describe the change you want…"
            }
            disabled={sendDisabled || sending}
            autoFocus
          />
          <button type="submit" disabled={sendDisabled || sending || !canSend}>
            {agentRunning ? "Steer" : "Send"}
          </button>
        </form>
      </div>
    </section>
  );
}
