import { useEffect, useRef, useState } from "react";
import { errorMessage, listWiki, readWiki } from "../api";
import type { WikiNoteInfo } from "../types";

// The daemon allows exactly one path outside <project>/wiki/: the pinned
// global user memory, shown as the always-present first row.
const USER_MD_PATH = "~/.odo/user.md";
const USER_MD_HINT =
  "No ~/.odo/user.md yet — create it to give agents your durable principles.";

interface Props {
  conversationId: number;
  onClose: () => void;
}

// Compact relative timestamp for the note list ("45s ago", "3h ago", …).
function relativeTime(iso: string): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "";
  const secs = Math.max(0, Math.floor((Date.now() - then) / 1000));
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

// M3 wiki browser (spec §2b): modal with the wiki note list on the left —
// a pinned user.md (global) row first, then the workstream's notes newest
// epoch first — and a dependency-free preformatted reader on the right.
export default function WikiBrowser({ conversationId, onClose }: Props) {
  const [notes, setNotes] = useState<WikiNoteInfo[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string>(USER_MD_PATH);
  const [content, setContent] = useState<string | null>(null);
  const [contentLoading, setContentLoading] = useState(true);
  const cache = useRef(new Map<string, string>());

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await listWiki(conversationId);
        if (cancelled) return;
        if (resp.ok) {
          setNotes(resp.wiki_notes ?? []);
        } else {
          setListError(resp.error ?? "list failed");
        }
      } catch (e) {
        if (!cancelled) setListError(errorMessage(e));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [conversationId]);

  // Reader: fetch the selected entry once, then serve from the cache.
  useEffect(() => {
    const cached = cache.current.get(selected);
    if (cached !== undefined) {
      setContent(cached);
      setContentLoading(false);
      return;
    }
    let cancelled = false;
    setContent(null);
    setContentLoading(true);
    (async () => {
      try {
        const resp = await readWiki(selected);
        if (cancelled) return;
        const text = resp.ok ? (resp.wiki_content ?? "") : `read failed: ${resp.error ?? "unknown error"}`;
        cache.current.set(selected, text);
        setContent(text);
      } catch (e) {
        if (!cancelled) setContent(`read failed: ${errorMessage(e)}`);
      } finally {
        if (!cancelled) setContentLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [selected]);

  // Escape closes, like every other modal affordance.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="settings-overlay" onClick={onClose}>
      <div
        className="wiki-panel"
        role="dialog"
        aria-modal="true"
        aria-label="Wiki browser"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="wiki-head">
          <h2 className="settings-title">Wiki</h2>
          <button type="button" className="settings-close" onClick={onClose}>
            Close
          </button>
        </div>

        <div className="wiki-body">
          <div className="wiki-list">
            <button
              type="button"
              className={`wiki-row${selected === USER_MD_PATH ? " selected" : ""}`}
              onClick={() => setSelected(USER_MD_PATH)}
            >
              <span className="wiki-row-name">user.md (global)</span>
            </button>
            {notes === null && !listError && <div className="wiki-hint">Loading…</div>}
            {listError && <div className="wiki-hint">list failed: {listError}</div>}
            {notes !== null && notes.length === 0 && (
              <div className="wiki-hint">No wiki notes yet — Distill writes the first one.</div>
            )}
            {notes?.map((n) => (
              <button
                type="button"
                key={n.path}
                className={`wiki-row${selected === n.path ? " selected" : ""}`}
                title={n.path}
                onClick={() => setSelected(n.path)}
              >
                <span className="wiki-row-name">{n.name}</span>
                <span className="wiki-row-meta">
                  epoch {n.epoch}
                  {relativeTime(n.modified_at) !== "" ? ` · ${relativeTime(n.modified_at)}` : ""}
                </span>
              </button>
            ))}
          </div>

          <div className="wiki-reader">
            {contentLoading && <div className="wiki-hint">Loading…</div>}
            {!contentLoading && content !== null && content !== "" && (
              <pre className="wiki-content">{content}</pre>
            )}
            {!contentLoading && content === "" && selected === USER_MD_PATH && (
              <div className="wiki-hint">{USER_MD_HINT}</div>
            )}
            {!contentLoading && content === "" && selected !== USER_MD_PATH && (
              <div className="wiki-hint">(empty note)</div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
