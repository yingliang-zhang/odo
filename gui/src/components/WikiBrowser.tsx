import { memo, useEffect, useRef, useState } from "react";
import { contradictions, errorMessage, listTopics, listWiki, readWiki } from "../api";
import type { WikiNoteInfo } from "../types";
import { cn } from "../lib/utils";
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

// M12: citations are workstream-qualified — a bullet must trace to its
// source note via a trailing "(<ws>-epoch-N)" citation; bullets without
// one are flagged "uncited". The captured note name jumps directly (no
// cross-workstream collision: epoch numbering restarts per workstream).
const CITATION_RE = /\(([A-Za-z0-9][A-Za-z0-9._-]*-epoch-\d+)\)$/;
// Legacy pre-M12 pages may still carry bare "(epoch-N)" citations.
const LEGACY_CITATION_RE = /\(epoch-(\d+)\)$/;

// Note/topic list rows (old .wiki-row + :hover/:last-child), translated 1:1.
// The `.selected` hook's styling is applied conditionally at the call site.
const WIKI_ROW_UTIL =
  "block w-full text-left px-2.5 py-2 cursor-pointer last:border-b-0 " +
  "border-b border-[var(--border)] text-[var(--text)] bg-transparent hover:bg-[var(--bg-input)]";

interface Props {
  conversationId: number;
  // M11 P1: reads route to this project's daemon; null = bridge default.
  // App remounts the browser on project switch, so no cross-project state
  // (note list, reader cache, selection) can survive.
  projectRoot?: string | null;
  // Fold chip's "Open note": select + read this note. The counter lets a
  // repeated request for the same path re-select it even when the browser
  // stayed mounted (object identity changes per request).
  focus?: { path: string; n: number } | null;
  // Keep-alive activation edge (2026-08-25 review P1): App mounts the
  // browser once and CSS-hides it afterwards; the daemon-side surfaces it
  // renders (epoch notes, retractions, topic pages) keep changing while
  // hidden. On the inactive→active edge the panel re-fetches every list
  // and drops cached page bodies; the search query and the selected path
  // deliberately survive (draft state), only bytes are refreshed.
  active: boolean;
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
function WikiBrowser({ conversationId, projectRoot, focus, active }: Props) {
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

  // listNonce bumps re-run the list fetch wave; readerNonce forces the
  // reader past its cache hit. Both are bumped by the activation edge
  // below (the ONLY producers besides "never").
  const [listNonce, setListNonce] = useState(0);
  const [readerNonce, setReaderNonce] = useState(0);
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
  }, [conversationId, projectRoot, listNonce]);

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

  // Activation edge (the Props contract): the panel renders from
  // daemon-side files that keep changing while it is hidden — a fresh
  // distill adds epoch notes, a curate rewrites topic pages, the learner
  // edits memory. On inactive→active, re-fetch the lists, drop EVERY
  // cached page body (stale bytes are worse than a re-pull), force the
  // reader to re-fetch its selection, and clear the topics cache so the
  // lazy Topics effect re-pulls on demand. Selection + query stay.
  const wasActiveRef = useRef(active);
  useEffect(() => {
    if (!wasActiveRef.current && active) {
      cache.current.clear();
      setListNonce((n) => n + 1);
      setReaderNonce((n) => n + 1);
      setTopics(null);
      setTopicsError(null);
    }
    wasActiveRef.current = active;
  }, [active]);

  // External focus requests (the fold chip's "Open note") select the note
  // directly — the reader below picks the selection up through `selected`.
  useEffect(() => {
    if (focus) {
      setTab("notes");
      setSelected(focus.path);
    }
  }, [focus]);

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
  }, [selected, projectRoot, readerNonce]);

  // M12: a qualified citation names its note exactly — the click jumps to
  // it, cross-workstream included. A bare legacy citation jumps to the
  // source epoch note in the Notes tab ONLY when exactly one note matches
  // (several workstreams can share an epoch; both degrade to a no-op
  // rather than jumping to the wrong note).
  const jumpToNote = (name: string) => {
    const target = (notes ?? []).find((n) => n.name === name);
    if (!target) return;
    setTab("notes");
    setSelected(target.path);
  };
  const jumpToEpoch = (epoch: number) => {
    const matches = notes?.filter((n) => n.epoch === epoch) ?? [];
    if (matches.length !== 1) return;
    setTab("notes");
    setSelected(matches[0].path);
  };

  const isTopicPage = selected.includes(TOPICS_MARKER);

  return (
    <div className={cn("wiki-panel", "h-full flex flex-col")}>
      <div className={cn("wiki-tabs", "flex gap-1.5 mb-3")} role="tablist" aria-label="Wiki sections">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "notes"}
          className={cn(
            "wiki-tab",
            "bg-[var(--bg-input)] border border-[var(--border)] rounded-[6px]",
            "px-3 py-[5px] text-[12px] cursor-pointer text-[var(--text-dim)] hover:text-[var(--text)]",
            tab === "notes" && "active text-[var(--text)] border-[var(--accent-user)]",
          )}
          onClick={() => setTab("notes")}
        >
          Notes
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "topics"}
          className={cn(
            "wiki-tab",
            "bg-[var(--bg-input)] border border-[var(--border)] rounded-[6px]",
            "px-3 py-[5px] text-[12px] cursor-pointer text-[var(--text-dim)] hover:text-[var(--text)]",
            tab === "topics" && "active text-[var(--text)] border-[var(--accent-user)]",
          )}
          onClick={() => setTab("topics")}
        >
          Topics
        </button>
      </div>

      <div className={cn("wiki-search", "mb-3")}>
        <input
          type="text"
          className="w-full px-2.5 py-1.5 text-[13px] text-[var(--text)] bg-[var(--bg-input)] border border-[var(--border)] rounded-[6px] focus:outline-none focus:border-[var(--accent-user)]"
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

      <div
        className={cn(
          "wiki-body",
          "grid grid-rows-[minmax(0,1fr)_minmax(0,2fr)] gap-3 flex-1 min-h-0",
        )}
      >
        <div
          className={cn(
            "wiki-list",
            "overflow-y-auto self-stretch border border-[var(--border)] rounded-[8px]",
          )}
        >
          {tab === "notes" && (
            <>
              {matchesQuery("user.md (global)", USER_MD_PATH) && (
                <button
                  type="button"
                  className={cn(
                    "wiki-row",
                    WIKI_ROW_UTIL,
                    selected === USER_MD_PATH && "selected bg-[var(--bg-input)] shadow-[inset_2px_0_0_var(--accent-user)]",
                  )}
                  onClick={() => setSelected(USER_MD_PATH)}
                >
                  <span className={cn("wiki-row-name", "block font-semibold truncate")}>
                    {highlightText("user.md (global)", needle, "umd")}
                  </span>
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
                  className={cn(
                    "wiki-row",
                    WIKI_ROW_UTIL,
                    selected === n.path && "selected bg-[var(--bg-input)] shadow-[inset_2px_0_0_var(--accent-user)]",
                  )}
                  title={n.path}
                  onClick={() => setSelected(n.path)}
                >
                  <span className={cn("wiki-row-name", "block font-semibold truncate")}>
                    {highlightText(n.name, needle, `n-${n.path}`)}
                    {retracted.has(n.name) && (
                      <span
                        className={cn("wiki-retracted-badge", "ml-2 text-[11px] text-[var(--err-text)]")}
                        title="Retracted by the contradiction pass — still readable, no longer injected"
                      >
                        ⚠ retracted
                      </span>
                    )}
                  </span>
                  <span className={cn("wiki-row-meta", "block mt-0.5 text-[11px] text-[var(--text-dim)]")}>
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
                  className={cn(
                    "wiki-row",
                    WIKI_ROW_UTIL,
                    selected === topic.path && "selected bg-[var(--bg-input)] shadow-[inset_2px_0_0_var(--accent-user)]",
                  )}
                  title={topic.path}
                  onClick={() => setSelected(topic.path)}
                >
                  <span className={cn("wiki-row-name", "block font-semibold truncate")}>
                    {highlightText(topic.name, needle, `t-${topic.path}`)}
                  </span>
                  <span className={cn("wiki-row-meta", "block mt-0.5 text-[11px] text-[var(--text-dim)]")}>
                    {relativeTime(topic.modified_at) !== "" ? relativeTime(topic.modified_at) : ""}
                  </span>
                </button>
              ))}
            </>
          )}
        </div>

        <div
          className={cn(
            "wiki-reader",
            "overflow-auto px-3 py-[2px] bg-[var(--bg)] border border-[var(--border)] rounded-[8px]",
          )}
        >
          {contentLoading && <LoadingInline />}
          {!contentLoading && content !== null && content !== "" && !isTopicPage && (
            <Markdown content={content} className="wiki-content" projectRoot={projectRoot} />
          )}
          {!contentLoading && content !== null && content !== "" && isTopicPage && (
            <div className="wiki-content wiki-topic-content">
              {content.split("\n").map((line, i) => (
                <TopicLine key={i} line={line} onJumpToNote={jumpToNote} onJumpToEpoch={jumpToEpoch} />
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
// "(<ws>-epoch-N)" (M12; bare "(epoch-N)" on legacy pages) gets a
// clickable citation that jumps to the source note; a bullet without a
// citation is flagged "⚠ uncited" (still injected into prompts — the flag
// is the user's verification surface, spec §9).
function TopicLine({
  line,
  onJumpToNote,
  onJumpToEpoch,
}: {
  line: string;
  onJumpToNote: (name: string) => void;
  onJumpToEpoch: (epoch: number) => void;
}) {
  if (!line.startsWith("- ")) {
    return <div className={cn("wiki-topic-line", "min-h-[1.5em]")}>{line}</div>;
  }
  const match = line.match(CITATION_RE) ?? line.match(LEGACY_CITATION_RE);
  if (!match) {
    return (
      <div
        className={cn(
          "wiki-topic-line wiki-line-uncited",
          "min-h-[1.5em] pl-1.5 -ml-2 bg-[rgba(195,74,74,0.12)] border-l-2 border-l-[var(--err)]",
        )}
      >
        {line}{" "}
        <span className={cn("wiki-uncited-badge", "text-[11px] text-[var(--err-text)]")}>
          ⚠ uncited
        </span>
      </div>
    );
  }
  const qualified = match[1].includes("-epoch-");
  // trimEnd drops the spacer before the citation — otherwise the bullet
  // text ends with a space AND the JSX renders one between text and the
  // citation button (a visible double space).
  const text = line.slice(0, match.index).trimEnd();
  return (
    <div className={cn("wiki-topic-line", "min-h-[1.5em]")}>
      {text}
      <button
        type="button"
        className={cn(
          "wiki-epoch-link",
          "p-0 bg-transparent border-0 cursor-pointer underline text-[var(--accent-user)] hover:brightness-125",
        )}
        title={`Jump to the ${match[1]} source note (Notes tab)`}
        onClick={() => (qualified ? onJumpToNote(match[1]) : onJumpToEpoch(Number(match[1])))}
      >
        {match[0]}
      </button>
    </div>
  );
}

// Keep-alive panel (tri-review P2 #5, 2026-08-24): App keeps this
// component mounted under the ContextPanel `hidden` tabs and hands it
// only referentially stable props (useCallback handlers + diff_stable
// prev-bails), so the default shallow compare skips re-rendering the
// hidden subtree on quiet poll ticks. Same convention as
// memo(ChatSurface) — no custom comparator.
export default memo(WikiBrowser);
