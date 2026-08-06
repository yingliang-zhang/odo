import { useEffect, useRef, useState } from "react";
import { contradictions, errorMessage, listTopics, listWiki, readWiki } from "../api";
import type { WikiNoteInfo } from "../types";
import Markdown, { highlightText } from "./Markdown";
import LoadingInline from "./LoadingInline";

// M6: the retracted-note set names notes filtered out of recall by the
// contradiction pass — parsed from each retraction event's detail (the
// first token, per the daemon's "<old> contradicted by <new>: …" format).
function retractedNames(events: { payload: { detail?: string } }[]): Set<string> {
  const out = new Set<string>();
  for (const e of events) {
    const first = (e.payload?.detail ?? "").split(" ", 1)[0];
    if (first) out.add(first);
  }
  return out;
}

// The daemon allows exactly one path outside <project>/wiki/: the pinned
// global user memory, shown as the always-present first row of the Notes
// tab.
const USER_MD_PATH = "~/.odo/user.md";
const USER_MD_HINT =
  "No ~/.odo/user.md yet — create it to give agents your durable principles.";

// M5: topic pages live under wiki/topics/ — the reader flags their bullets.
const TOPICS_MARKER = "/wiki/topics/";

// M5 (spec §9): a topic-page bullet must trace to a source epoch note via a
// trailing "(epoch-N)" citation; bullets without one are flagged "uncited".
const CITATION_RE = /\(epoch-(\d+)\)$/;

interface Props {
  conversationId: number;
  // M11 P1: reads route to this project's daemon; null = bridge default.
  // App remounts the browser on project switch, so no cross-project state
  // (note list, reader cache, selection) can survive.
  projectRoot?: string | null;
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

// M3 wiki browser (spec §2b) + M5 Topics tab (spec §9) — "Notes" (the
// workstream's epoch notes, user.md pinned first) and "Topics" (the
// curator's project-wide topic pages) — with a dependency-free reader.
// Topic pages render line-by-line so uncited bullets are flagged and
// (epoch-N) citations are clickable.
//
// M9 P3: the browser renders inline inside the right panel's Wiki tab; the
// list and reader stack vertically and scroll independently while the tabs
// and search stay pinned. Closing is the panel's job (⌘J).
export default function WikiBrowser({ conversationId, projectRoot }: Props) {
  const [tab, setTab] = useState<"notes" | "topics">("notes");
  const [notes, setNotes] = useState<WikiNoteInfo[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  // M6 (§12): names of notes retracted by the contradiction pass — they
  // stay readable (records) but get a "⚠ retracted" badge.
  const [retracted, setRetracted] = useState<Set<string>>(new Set());
  const [topics, setTopics] = useState<WikiNoteInfo[] | null>(null);
  const [topicsError, setTopicsError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string>(USER_MD_PATH);
  const [content, setContent] = useState<string | null>(null);
  const [contentLoading, setContentLoading] = useState(true);
  const cache = useRef(new Map<string, string>());
  // Belt C (§Fix 3): client-side list filter — matches title, plus the
  // note's content when it has already been read into cache this session
  // (no IPC; unread notes match by title only).
  const [query, setQuery] = useState("");
  const trimmed = query.trim();
  const needle = trimmed === "" ? undefined : trimmed;
  const matchesQuery = (name: string, path: string): boolean => {
    if (needle === undefined) return true;
    const lower = needle.toLowerCase();
    if (name.toLowerCase().includes(lower)) return true;
    return cache.current.get(path)?.toLowerCase().includes(lower) ?? false;
  };

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await listWiki(conversationId, projectRoot ?? undefined);
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
    // M6: retraction badges ride the same fetch wave; a failure degrades
    // to no badges (the note list is still fully usable).
    (async () => {
      try {
        const resp = await contradictions(conversationId, projectRoot ?? undefined);
        if (!cancelled) setRetracted(retractedNames(resp.events ?? []));
      } catch {
        // Badges are optional surface; never disturb the browser.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [conversationId, projectRoot]);

  // M5: topics are project-wide (not per-workstream) — fetched lazily on
  // the first switch to the Topics tab.
  useEffect(() => {
    if (tab !== "topics" || topics !== null || topicsError !== null) return;
    let cancelled = false;
    (async () => {
      try {
        const resp = await listTopics(projectRoot ?? undefined);
        if (!cancelled) setTopics(resp.wiki_notes ?? []);
      } catch (e) {
        if (!cancelled) setTopicsError(errorMessage(e));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [tab, topics, topicsError, projectRoot]);

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
        const resp = await readWiki(selected, projectRoot ?? undefined);
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
  }, [selected, projectRoot]);

  // M5 (spec §9): a citation click jumps to the source epoch note in the
  // Notes tab ONLY when exactly one note in the current workstream matches
  // — curation is project-wide, so a citation can name an epoch no note
  // here carries, or an epoch several workstreams share; both degrade to a
  // no-op (spec risk #3) rather than jumping to the wrong note.
  const jumpToEpoch = (epoch: number) => {
    const matches = notes?.filter((n) => n.epoch === epoch) ?? [];
    if (matches.length !== 1) return;
    setTab("notes");
    setSelected(matches[0].path);
  };

  const isTopicPage = selected.includes(TOPICS_MARKER);

  return (
    <div className="wiki-panel">
      <div className="wiki-tabs" role="tablist" aria-label="Wiki sections">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "notes"}
          className={`wiki-tab${tab === "notes" ? " active" : ""}`}
          onClick={() => setTab("notes")}
        >
          Notes
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "topics"}
          className={`wiki-tab${tab === "topics" ? " active" : ""}`}
          onClick={() => setTab("topics")}
        >
          Topics
        </button>
      </div>

      <div className="wiki-search">
        <input
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            // Esc never leaves this input's surface — clear a non-empty
            // query, blur an empty one, but never let it reach the global
            // handler (which cancels a running agent).
            if (e.key === "Escape") {
              e.stopPropagation();
              if (query !== "") {
                setQuery("");
              } else {
                e.currentTarget.blur();
              }
            }
          }}
          placeholder="Search wiki…"
          aria-label="Search wiki"
        />
      </div>

      <div className="wiki-body">
        <div className="wiki-list">
          {tab === "notes" && (
            <>
              {matchesQuery("user.md (global)", USER_MD_PATH) && (
                <button
                  type="button"
                  className={`wiki-row${selected === USER_MD_PATH ? " selected" : ""}`}
                  onClick={() => setSelected(USER_MD_PATH)}
                >
                  <span className="wiki-row-name">{highlightText("user.md (global)", needle, "umd")}</span>
                </button>
              )}
              {notes === null && !listError && <LoadingInline />}
              {listError && <div className="wiki-hint">list failed: {listError}</div>}
              {notes !== null && notes.length === 0 && (
                <div className="wiki-hint">No wiki notes yet — Distill writes the first one.</div>
              )}
              {notes !== null && needle !== undefined && notes.filter((n) => matchesQuery(n.name, n.path)).length === 0 && !matchesQuery("user.md (global)", USER_MD_PATH) && (
                <div className="wiki-hint">No notes match “{needle}”.</div>
              )}
              {notes?.filter((n) => matchesQuery(n.name, n.path)).map((n) => (
                <button
                  type="button"
                  key={n.path}
                  className={`wiki-row${selected === n.path ? " selected" : ""}`}
                  title={n.path}
                  onClick={() => setSelected(n.path)}
                >
                  <span className="wiki-row-name">
                    {highlightText(n.name, needle, `n-${n.path}`)}
                    {retracted.has(n.name) && (
                      <span
                        className="wiki-retracted-badge"
                        title="Retracted by the contradiction pass — still readable, no longer injected"
                      >
                        ⚠ retracted
                      </span>
                    )}
                  </span>
                  <span className="wiki-row-meta">
                    epoch {n.epoch}
                    {relativeTime(n.modified_at) !== "" ? ` · ${relativeTime(n.modified_at)}` : ""}
                  </span>
                </button>
              ))}
            </>
          )}
          {tab === "topics" && (
            <>
              {topics === null && !topicsError && <LoadingInline />}
              {topicsError && <div className="wiki-hint">list failed: {topicsError}</div>}
              {topics !== null && topics.length === 0 && (
                <div className="wiki-hint">No topic pages yet — Curate writes the first set.</div>
              )}
              {topics !== null && needle !== undefined && topics.filter((t) => matchesQuery(t.name, t.path)).length === 0 && (
                <div className="wiki-hint">No topics match “{needle}”.</div>
              )}
              {topics?.filter((t) => matchesQuery(t.name, t.path)).map((topic) => (
                <button
                  type="button"
                  key={topic.path}
                  className={`wiki-row${selected === topic.path ? " selected" : ""}`}
                  title={topic.path}
                  onClick={() => setSelected(topic.path)}
                >
                  <span className="wiki-row-name">{highlightText(topic.name, needle, `t-${topic.path}`)}</span>
                  <span className="wiki-row-meta">
                    {relativeTime(topic.modified_at) !== "" ? relativeTime(topic.modified_at) : ""}
                  </span>
                </button>
              ))}
            </>
          )}
        </div>

        <div className="wiki-reader">
          {contentLoading && <LoadingInline />}
          {!contentLoading && content !== null && content !== "" && !isTopicPage && (
            <Markdown content={content} className="wiki-content" />
          )}
          {!contentLoading && content !== null && content !== "" && isTopicPage && (
            <div className="wiki-content wiki-topic-content">
              {content.split("\n").map((line, i) => (
                <TopicLine key={i} line={line} onJumpToEpoch={jumpToEpoch} />
              ))}
            </div>
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
  );
}

// TopicLine renders one line of a topic page. A bullet ending in
// "(epoch-N)" gets a clickable citation that jumps to the source note; a
// bullet without a citation is flagged "⚠ uncited" (still injected into
// prompts — the flag is the user's verification surface, spec §9).
function TopicLine({
  line,
  onJumpToEpoch,
}: {
  line: string;
  onJumpToEpoch: (epoch: number) => void;
}) {
  if (!line.startsWith("- ")) {
    return <div className="wiki-topic-line">{line}</div>;
  }
  const match = line.match(CITATION_RE);
  if (!match) {
    return (
      <div className="wiki-topic-line wiki-line-uncited">
        {line} <span className="wiki-uncited-badge">⚠ uncited</span>
      </div>
    );
  }
  const epoch = Number(match[1]);
  // trimEnd drops the spacer before "(epoch-N)" — otherwise the bullet
  // text ends with a space AND the JSX renders one between text and the
  // citation button (a visible double space).
  const text = line.slice(0, match.index).trimEnd();
  return (
    <div className="wiki-topic-line">
      {text}
      <button
        type="button"
        className="wiki-epoch-link"
        title={`Jump to the epoch ${epoch} source note (Notes tab)`}
        onClick={() => onJumpToEpoch(epoch)}
      >
        {match[0]}
      </button>
    </div>
  );
}
