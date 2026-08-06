import {
  ClipboardEvent,
  DragEvent,
  FormEvent,
  KeyboardEvent as ReactKeyboardEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { getCurrentWebview } from "@tauri-apps/api/webview";
import { basename } from "../files";
import type { OdoEvent, PreviewEvent, RunInfo } from "../types";
import MessageBubble from "./MessageBubble";
import ToolTicker from "./ToolTicker";

// M3 run-status formatting (spec §3a): `<m>m <s>s`, bare seconds under a
// minute ("35s").
function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return m > 0 ? `${m}m ${s}s` : `${s}s`;
}

interface Props {
  events: OdoEvent[];
  agentRunning: boolean;
  // M7: transient in-flight block preview from poll_events (never
  // journaled). Rendered as the dimmed preview bubble; replaced wholesale
  // per poll, null when the stream is between blocks or done.
  preview?: PreviewEvent | null;
  // M8: fan-out per-run state from the daemon. When non-empty, the run
  // group shows per-lane tabs; selecting a lane filters the event stream.
  runs?: RunInfo[];
  sendDisabled: boolean;
  onSend: (text: string, attachments: string[], steer: boolean) => Promise<void>;
  // M2 fan-out: run the prompt through N parallel runs; resolves to the
  // number of runs the daemon started. Rejects on failure.
  onFanout: (text: string, n: number) => Promise<number>;
  // Belt A: abort the running agent (Stop button / Esc).
  onCancel: () => void;
  // M1 memory distiller: current epoch (banner shown when > 1) and the wiki
  // path of the most recent distill, when known this session.
  epoch: number;
  distilledTo?: string | null;
  // Belt B: conversation-local search (⌘F). State is owned by App so the
  // command palette can open it too; matching happens here, over the events
  // already in memory — no IPC.
  searchOpen: boolean;
  searchQuery: string;
  onSearchQueryChange: (query: string) => void;
  onSearchClose: () => void;
}

// Belt B: the text a search query is matched against per event. Kept in
// sync with what MessageBubble renders — agent/user text, tool call names
// and args, tool result summaries, run outcomes.
function searchableText(e: OdoEvent): string {
  const p = e.payload ?? {};
  switch (e.type) {
    case "user_message":
    case "agent_text":
    case "agent_thinking":
      return p.text ?? "";
    case "agent_tool_call":
      return `${p.tool ?? ""} ${p.args != null ? JSON.stringify(p.args) : ""}`;
    case "agent_tool_result": {
      const body = typeof p.result === "string" ? p.result : JSON.stringify(p.result ?? "");
      return `${p.tool ?? ""} ${body}`;
    }
    case "agent_done":
      return p.summary ?? "";
    case "agent_error":
      return p.error ?? "";
    default:
      return "";
  }
}

// Belt C (§Fix 1): a run starts at each user_message and ends at the
// next user_message (or end of log). Events before the first user_message
// in the epoch window (rare — a run spanning a distill boundary loses its
// start message to the epoch filter) render as a headerless preamble
// group.
interface RunGroup {
  start: OdoEvent | null;
  events: OdoEvent[];
}

// One unit of rendered output inside a run: a plain bubble, or a bundle of
// consecutive tool events collapsed under a "N tool calls" <details>
// toggle. Bundling only consecutive tool events keeps the toggle at the
// calls' position in the run's narrative.
type RunRenderItem =
  | { kind: "bubble"; event: OdoEvent }
  | { kind: "tools"; events: OdoEvent[]; calls: number };

function runRenderItems(events: OdoEvent[]): RunRenderItem[] {
  const items: RunRenderItem[] = [];
  let tools: OdoEvent[] = [];
  const flush = () => {
    if (tools.length > 0) {
      const calls = tools.filter((e) => e.type === "agent_tool_call").length;
      items.push({ kind: "tools", events: tools, calls });
      tools = [];
    }
  };
  for (const ev of events) {
    if (ev.type === "agent_tool_call" || ev.type === "agent_tool_result") {
      tools.push(ev);
    } else {
      flush();
      items.push({ kind: "bubble", event: ev });
    }
  }
  flush();
  return items;
}

// M7 streaming preview: the in-flight block below the journaled bubbles —
// dimmed and italic so it reads as provisional. Text previews end in a
// pulsing caret; tool previews lead with a spinning ⟳ plus the call's
// intent. When the block completes, the next poll journals it and this
// bubble is replaced by the real one.
function PreviewBubble({ preview }: { preview: PreviewEvent }) {
  const p = preview.payload ?? {};
  if (preview.type === "agent_tool_call") {
    const tool = typeof p.tool === "string" && p.tool !== "" ? p.tool : "tool";
    const intent = typeof p.intent === "string" && p.intent !== "" ? ` — ${p.intent}` : "";
    return (
      <div className="bubble bubble-tool bubble-preview" aria-live="polite">
        <span className="preview-spinner" aria-hidden="true">
          ⟳
        </span>{" "}
        {tool}
        {intent}
      </div>
    );
  }
  const text = typeof p.text === "string" ? p.text : "";
  if (text === "") return null;
  return (
    <div className="bubble bubble-agent bubble-preview" aria-live="polite">
      {text}
      <span className="preview-caret" aria-hidden="true" />
    </div>
  );
}

// M8: per-lane tabs for fan-out runs. Renders "All · Run 1 ✓ · Run 2 ⟳ · …"
// with status icons and a ● badge for lanes with a pending diff. Clicking a
// tab sets the selected lane; "All" shows the interleaved journal.
function RunTabs({
  runs,
  selectedRunId,
  onSelect,
}: {
  runs: RunInfo[];
  selectedRunId: string | null;
  onSelect: (id: string | null) => void;
}) {
  return (
    <div className="run-tabs" role="tablist">
      <button
        type="button"
        role="tab"
        aria-selected={selectedRunId === null}
        className={`run-tab${selectedRunId === null ? " is-active" : ""}`}
        onClick={() => onSelect(null)}
      >
        All
      </button>
      {runs.map((r) => {
        const icon = r.status === "error" ? "✗" : r.status === "done" ? "✓" : "⟳";
        const isActive = selectedRunId === r.run_id;
        const hasDiff = r.diff_id != null;
        return (
          <button
            key={r.run_id}
            type="button"
            role="tab"
            aria-selected={isActive}
            className={`run-tab${isActive ? " is-active" : ""}${hasDiff ? " has-diff" : ""}`}
            onClick={() => onSelect(r.run_id)}
          >
            {`Run ${r.index + 1}`} {icon}
            {hasDiff && <span className="run-tab-dot" aria-label="pending diff" />}
          </button>
        );
      })}
    </div>
  );
}

// Run header: timestamp, tool call count, duration when the run journaled
// agent_done, and a status icon (✓ done / ✗ error / ⟳ running).
// M8: when fan-out runs are provided, the group is running while ANY lane
// runs (the first lane's agent_done no longer flips the group to done).
function RunHeader({ group, runs }: { group: RunGroup; runs?: RunInfo[] }) {
  const start = group.start;
  if (!start) return null;
  const toolCalls = group.events.filter((e) => e.type === "agent_tool_call").length;
  const done = group.events.find((e) => e.type === "agent_done");
  const failed = group.events.find((e) => e.type === "agent_error");
  // M8: fan-out status — running while any lane is still live.
  const fanoutRunning = runs && runs.some((r) => r.status === "running");
  const status = fanoutRunning ? "running" : failed ? "error" : done ? "done" : "running";
  const icon = fanoutRunning ? "⟳" : failed ? "✗" : done ? "✓" : "⟳";
  const startMs = Date.parse(start.created_at);
  const clock = Number.isNaN(startMs)
    ? "?"
    : new Date(startMs).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  const doneMs = done ? Date.parse(done.created_at) : NaN;
  const showDuration = !fanoutRunning && done !== undefined && !Number.isNaN(startMs) && !Number.isNaN(doneMs);
  return (
    <div className="run-header">
      <span className={`run-status ${status}`}>{icon}</span>
      <span>{clock}</span>
      <span>{`${toolCalls} tool call${toolCalls === 1 ? "" : "s"}`}</span>
      {showDuration && <span>{formatElapsed(doneMs - startMs)}</span>}
    </div>
  );
}

// Chat log plus composer. File attachments arrive via Tauri's drag-drop
// events — the only source of real file paths in the webview — and via
// clipboard paste. HTML5 drag events are kept as a fallback; they only fire
// if `dragDropEnabled` is ever disabled in tauri.conf.json, so the two paths
// never double-fire.
// Belt A: the composer auto-grows from one row up to six, then scrolls
// internally. 6 rows * 21px (14px/1.5) + padding/border ≈ 148px; this cap
// must match .chat-input textarea's max-height in app.css.
const COMPOSER_MAX_HEIGHT = 148;

// Auto-follow engages only while the user is within this many px of the
// scroll bottom (Belt A stick-to-bottom).
const NEAR_BOTTOM_PX = 80;

// Belt D: first-run empty-state examples — a click fills the composer.
const EXAMPLE_PROMPTS = [
  "Describe the change you want to make",
  "Review the latest diff and suggest improvements",
  "Distill a summary of recent decisions",
];

export default function ChatSurface({
  events,
  agentRunning,
  preview,
  runs,
  sendDisabled,
  onSend,
  onFanout,
  onCancel,
  epoch,
  distilledTo,
  searchOpen,
  searchQuery,
  onSearchQueryChange,
  onSearchClose,
}: Props) {
  // M8: selected run lane for per-run filtering. null = "All" (interleaved).
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null);
  // Reset stale selection when runs change or the selected lane disappears
  // (accept/review clears fanoutRuns → runs becomes []).
  useEffect(() => {
    if (selectedRunId == null) return;
    if (!runs || runs.length === 0 || !runs.some((r) => r.run_id === selectedRunId)) {
      setSelectedRunId(null);
    }
  }, [runs, selectedRunId]);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [attachments, setAttachments] = useState<string[]>([]);
  const [dragOver, setDragOver] = useState(false);
  // M2 fan-out composer state: N picker open, chosen N.
  const [fanoutOpen, setFanoutOpen] = useState(false);
  const [fanoutN, setFanoutN] = useState(2);
  const [fanoutBusy, setFanoutBusy] = useState(false);
  const [, setFanoutActive] = useState<number | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const composerRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  // Belt A stick-to-bottom: true while the user is pinned to the newest
  // output; scrolling up disengages, the "↓ new output" pill re-engages.
  const stickRef = useRef(true);
  const [newOutput, setNewOutput] = useState(false);

  // Fan-out runs report through the same agent_running signal; when the
  // daemon goes quiet the indicator has served its purpose.
  useEffect(() => {
    if (!agentRunning) setFanoutActive(null);
  }, [agentRunning]);

  // Belt A stick-to-bottom (spec §Fix 2): track whether the user is pinned
  // to the newest output. Programmatic scrolls fire this handler too; they
  // land at the bottom, so sticking survives our own auto-scroll.
  const handleListScroll = () => {
    const el = listRef.current;
    if (!el) return;
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX;
    stickRef.current = nearBottom;
    if (nearBottom) setNewOutput(false);
  };

  // Auto-follow only while stuck; otherwise surface the pill. New events
  // while scrolled up are exactly the case the pill exists for.
  useEffect(() => {
    const el = listRef.current;
    if (!el) return;
    if (stickRef.current) {
      el.scrollTop = el.scrollHeight;
    } else {
      setNewOutput(true);
    }
    // Preview changes poll-by-poll without touching events.length, so it
    // joins the follow-the-tail trigger too.
  }, [events.length, preview]);

  // A run flipping state (done banner appearing, ticker hiding) also nudges
  // the view, but never yanks a reader back down.
  useEffect(() => {
    const el = listRef.current;
    if (el && stickRef.current) el.scrollTop = el.scrollHeight;
  }, [agentRunning]);

  const scrollToBottom = () => {
    stickRef.current = true;
    setNewOutput(false);
    const el = listRef.current;
    if (el) el.scrollTop = el.scrollHeight;
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
  const handlePaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const files = Array.from(e.clipboardData?.files ?? []);
    if (files.length === 0) return; // plain text paste: let it through
    e.preventDefault();
    addAttachments(files.map((f) => f.name));
  };

  const removeAttachment = (path: string) => {
    setAttachments((prev) => prev.filter((p) => p !== path));
  };

  const canSend = draft.trim() !== "" || attachments.length > 0;

  const submitDraft = useCallback(async () => {
    const text = draft.trim();
    if (!canSend || sending || sendDisabled) return;
    setSending(true);
    try {
      // M1 steering: while the agent runs, submitting journals the message
      // with steer=true instead of starting a new run.
      await onSend(text, attachments, agentRunning);
      setDraft("");
      setAttachments([]);
    } catch {
      // onSend already surfaced the error; keep the draft for retry.
    } finally {
      setSending(false);
    }
  }, [draft, attachments, canSend, sending, sendDisabled, onSend, agentRunning]);

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    void submitDraft();
  };

  // Belt A: ⌘/Ctrl+Enter sends from anywhere (Fix 4), including focus
  // outside the textarea; the textarea's own keydown leaves the
  // modifier-Enter alone so this listener fires exactly once.
  const submitRef = useRef(submitDraft);
  useEffect(() => {
    submitRef.current = submitDraft;
  }, [submitDraft]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
        e.preventDefault();
        void submitRef.current();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // Belt A: auto-grow the composer with the draft up to the 6-row cap.
  useEffect(() => {
    const ta = textareaRef.current;
    if (!ta) return;
    ta.style.height = "auto";
    ta.style.height = `${Math.min(ta.scrollHeight, COMPOSER_MAX_HEIGHT)}px`;
  }, [draft]);

  // Belt A composer keys (Fix 3): Enter sends, Shift+Enter newlines (the
  // default), Esc stops a run or clears the draft. Escape is swallowed here
  // so App's global handler (blur/cancel for focus elsewhere) doesn't
  // double-fire.
  const handleComposerKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      void submitDraft();
      return;
    }
    if (e.key === "Escape") {
      e.stopPropagation();
      if (agentRunning) {
        onCancel();
      } else {
        setDraft("");
      }
    }
  };

  const handleFanout = async () => {
    const text = draft.trim();
    const n = Math.max(2, Math.floor(fanoutN));
    if (text === "" || fanoutBusy || sendDisabled) return;
    setFanoutBusy(true);
    try {
      const started = await onFanout(text, n);
      setFanoutActive(started > 0 ? started : n);
      setDraft("");
      setAttachments([]);
      setFanoutOpen(false);
    } catch {
      // App already surfaced the error; keep the draft for retry.
    } finally {
      setFanoutBusy(false);
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

  const visibleEvents = useMemo(
    () => events.filter((e) => e.seq > lastDistillSeq),
    [events, lastDistillSeq],
  );

  // M8: filter visible events by the selected run lane. Events without a
  // run_id (the initial user_message, review_action, etc.) belong to all
  // lanes and are always shown. When "All" is selected (null), no filter.
  const laneFilteredEvents = useMemo(() => {
    if (selectedRunId == null) return visibleEvents;
    return visibleEvents.filter((e) => {
      const rid = e.payload?.run_id;
      return rid == null || rid === selectedRunId;
    });
  }, [visibleEvents, selectedRunId]);

  // Belt C (§Fix 1): group the visible events into runs — each
  // user_message opens a new group; everything until the next one belongs
  // to it.
  const runGroups = useMemo<RunGroup[]>(() => {
    const groups: RunGroup[] = [];
    for (const ev of laneFilteredEvents) {
      if (ev.type === "user_message") {
        groups.push({ start: ev, events: [ev] });
      } else if (groups.length === 0) {
        groups.push({ start: null, events: [ev] });
      } else {
        groups[groups.length - 1].events.push(ev);
      }
    }
    return groups;
  }, [laneFilteredEvents]);

  // M8: effective preview — when a specific lane is selected, use that
  // run's preview from RunInfo; otherwise the top-level poll preview.
  const effectivePreview = useMemo(() => {
    if (selectedRunId == null || !runs) return preview;
    return runs.find((r) => r.run_id === selectedRunId)?.preview ?? null;
  }, [preview, runs, selectedRunId]) as PreviewEvent | null;

  // Belt B search: one entry per occurrence, in display order — the current
  // match index resolves to the seq of its bubble for scroll-into-view.
  const trimmedQuery = searchQuery.trim();
  const searchActive = searchOpen && trimmedQuery !== "";
  const activeHighlight = searchActive ? trimmedQuery : undefined;
  const matches = useMemo(() => {
    if (!searchOpen || trimmedQuery === "") return [];
    const needle = trimmedQuery.toLowerCase();
    const out: { seq: number }[] = [];
    for (const ev of visibleEvents) {
      const hay = searchableText(ev).toLowerCase();
      let at = hay.indexOf(needle);
      while (at !== -1) {
        out.push({ seq: ev.seq });
        at = hay.indexOf(needle, at + needle.length);
      }
    }
    return out;
  }, [searchOpen, trimmedQuery, visibleEvents]);
  const [matchIdx, setMatchIdx] = useState(0);
  // A new query restarts at the first match; clamp when the match count
  // shrinks under the cursor (events arrive while searching).
  useEffect(() => setMatchIdx(0), [trimmedQuery]);
  const clampedIdx = matches.length === 0 ? 0 : Math.min(matchIdx, matches.length - 1);
  const searchInputRef = useRef<HTMLInputElement>(null);

  // Focus the field when the bar opens; ⌘F while open refocuses instead
  // of reopening (App's global handler owns the ⌘F open path).
  useEffect(() => {
    if (searchOpen) {
      searchInputRef.current?.focus();
      searchInputRef.current?.select();
    }
  }, [searchOpen]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "f" && searchOpen) {
        e.preventDefault();
        searchInputRef.current?.focus();
        searchInputRef.current?.select();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [searchOpen]);

  const stepMatch = useCallback(
    (delta: number) => {
      if (matches.length === 0) return;
      setMatchIdx((i) => (Math.min(i, matches.length - 1) + delta + matches.length) % matches.length);
    },
    [matches.length],
  );

  // Jump-to-match: center the current match's bubble in the scroll view.
  useEffect(() => {
    if (!searchOpen || trimmedQuery === "") return;
    if (matches.length === 0) return;
    const target = listRef.current?.querySelector(`[data-seq="${matches[clampedIdx].seq}"] .bubble`) || listRef.current?.querySelector(`[data-seq="${matches[clampedIdx].seq}"]`);
    target?.scrollIntoView({ block: "center" });
  }, [searchOpen, trimmedQuery, matches, clampedIdx]);

  const handleSearchKeyDown = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      // Swallow so App's global handler (blur/cancel) doesn't also fire.
      e.stopPropagation();
      onSearchClose();
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      stepMatch(e.shiftKey ? -1 : 1);
    }
  };

  // M3 run status (spec §3a): the run window starts at the most recent
  // user_message in the current epoch; the first agent_tool_call in that
  // window marks the true start (else the message itself). Last tool and
  // call count come from the same window.
  const run = useMemo(() => {
    if (!agentRunning) return null;
    const evs = events.filter((e) => e.seq > lastDistillSeq);
    let startIdx = -1;
    for (let i = evs.length - 1; i >= 0; i--) {
      if (evs[i].type === "user_message") {
        startIdx = i;
        break;
      }
    }
    const window = startIdx >= 0 ? evs.slice(startIdx) : evs;
    const toolCalls = window.filter((e) => e.type === "agent_tool_call");
    // Elapsed anchors to the run's user_message (spec §3a); the first tool
    // call is only a fallback when the window has no message.
    const startEvent = (startIdx >= 0 ? evs[startIdx] : undefined) ?? toolCalls[0];
    const startMs = startEvent ? Date.parse(startEvent.created_at) : NaN;
    const lastTool =
      toolCalls.length > 0 ? (toolCalls[toolCalls.length - 1].payload?.tool ?? "tool") : null;
    return { startMs, lastTool, calls: toolCalls.length };
  }, [agentRunning, events, lastDistillSeq]);

  // 1s heartbeat to keep the elapsed display ticking while a run is live.
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!agentRunning) return;
    const timer = setInterval(() => setTick((n) => n + 1), 1000);
    return () => clearInterval(timer);
  }, [agentRunning]);

  return (
    <section className="chat-surface">
      <div className="message-list-wrap">
        {searchOpen && (
          <div className="search-bar">
            <input
              ref={searchInputRef}
              type="text"
              value={searchQuery}
              onChange={(e) => onSearchQueryChange(e.target.value)}
              onKeyDown={handleSearchKeyDown}
              placeholder="Find in conversation"
              aria-label="Find in conversation"
            />
            <span className="search-count">
              {trimmedQuery === ""
                ? ""
                : matches.length === 0
                  ? "No matches"
                  : `${clampedIdx + 1}/${matches.length}`}
            </span>
            <button
              type="button"
              aria-label="Previous match"
              title="Previous match (Shift+Enter)"
              disabled={matches.length === 0}
              onClick={() => stepMatch(-1)}
            >
              ↑
            </button>
            <button
              type="button"
              aria-label="Next match"
              title="Next match (Enter)"
              disabled={matches.length === 0}
              onClick={() => stepMatch(1)}
            >
              ↓
            </button>
            <button type="button" aria-label="Close search" title="Close (Esc)" onClick={onSearchClose}>
              ×
            </button>
          </div>
        )}
        <div
          className="message-list"
          ref={listRef}
          onScroll={handleListScroll}
          aria-live="polite"
        >
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
          <div className="empty-state">
            <h2>Welcome to Odo</h2>
            <p className="dim">
              Every run is journaled, and its diff lands here for review.
            </p>
            {EXAMPLE_PROMPTS.map((prompt) => (
              <button
                key={prompt}
                type="button"
                className="example-prompt"
                onClick={() => {
                  setDraft(prompt);
                  textareaRef.current?.focus();
                }}
              >
                {prompt}
              </button>
            ))}
            <div className="shortcuts">
              <span>⌘K Commands</span>
              <span>⌘↵ Send</span>
              <span>⌘B Sidebar</span>
              <span>⌘F Search</span>
              <span>⌘, Settings</span>
            </div>
          </div>
        )}
          {runGroups.map((group) => (
            <div className="run-group" key={group.start?.seq ?? "preamble"}>
              <RunHeader group={group} runs={runs} />
              {runs && runs.length > 0 && (
                <RunTabs runs={runs} selectedRunId={selectedRunId} onSelect={setSelectedRunId} />
              )}
              {runRenderItems(group.events).map((item) =>
                item.kind === "bubble" ? (
                  <MessageBubble key={item.event.seq} event={item.event} highlight={activeHighlight} />
                ) : (
                  // Tool calls default-collapsed; an active ⌘F search
                  // forces them open so jump-to-match still reaches tool
                  // bubbles (the <details> `open` attribute, no JS state).
                  <details
                    className="tool-group"
                    key={`tools-${item.events[0].seq}`}
                    open={searchActive}
                  >
                    <summary>
                      {item.calls} tool call{item.calls === 1 ? "" : "s"}
                    </summary>
                    {item.events.map((ev) => (
                      <MessageBubble key={ev.seq} event={ev} highlight={activeHighlight} />
                    ))}
                  </details>
                ),
              )}
            </div>
          ))}
          {effectivePreview && <PreviewBubble preview={effectivePreview} />}
          <ToolTicker running={agentRunning} events={events} />
        </div>
        {newOutput && (
          <button type="button" className="new-output-pill" onClick={scrollToBottom}>
            ↓ new output
          </button>
        )}
      </div>
      {run && (
        <div className="run-status">
          {`running — ${formatElapsed(Number.isNaN(run.startMs) ? 0 : Date.now() - run.startMs)}`}
          {run.lastTool != null ? ` — tool: ${run.lastTool} (call ${run.calls})` : ""}
        </div>
      )}
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
        {(runs ?? []).length > 0 && (() => {
          const allRuns = runs ?? [];
          const running = allRuns.filter((r) => r.status === "running").length;
          return running > 0 ? (
            <div className="fanout-indicator">
              {running} of {allRuns.length} runs still running
            </div>
          ) : null;
        })()}
        <form className="chat-input" onSubmit={handleSubmit}>
          <textarea
            ref={textareaRef}
            rows={1}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={handleComposerKeyDown}
            onPaste={handlePaste}
            placeholder={
              dragOver
                ? "Drop files to attach them…"
                : agentRunning
                  ? "Steer the running agent… (Esc stops)"
                  : "Describe the change you want…"
            }
            disabled={sendDisabled || sending}
            autoFocus
          />
          {fanoutOpen && (
            <span className="fanout-picker">
              <label htmlFor="fanout-n">×</label>
              <input
                id="fanout-n"
                type="number"
                min={2}
                max={8}
                value={fanoutN}
                onChange={(e) => setFanoutN(Number(e.target.value))}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    void handleFanout();
                  } else if (e.key === "Escape") {
                    setFanoutOpen(false);
                  }
                }}
                disabled={fanoutBusy}
                autoFocus
              />
            </span>
          )}
          <button
            type="button"
            className="fanout-btn"
            disabled={sendDisabled || sending || fanoutBusy || draft.trim() === ""}
            title="Run this prompt through N parallel agents"
            onClick={() => {
              if (fanoutOpen) {
                void handleFanout();
              } else {
                setFanoutOpen(true);
              }
            }}
          >
            {fanoutBusy ? "Starting…" : fanoutOpen ? "Start" : "Fan-out"}
          </button>
          {agentRunning && (
            <button
              type="button"
              className="stop-btn"
              title="Stop the running agent (Esc)"
              onClick={onCancel}
            >
              Stop
            </button>
          )}
          <button type="submit" disabled={sendDisabled || sending || !canSend}>
            {agentRunning ? "Steer" : "Send"}
          </button>
        </form>
        <div className="composer-hint">
          ⌘↵ send · Shift+↵ newline{agentRunning ? " · Esc stop" : ""}
        </div>
      </div>
    </section>
  );
}
