import {
  ChangeEvent,
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
import type { AutoDistillCountdown, OdoEvent, PreviewEvent } from "../types";
import MessageBubble from "./MessageBubble";
import PlanChip from "./PlanChip";
import { saveAttachment } from "../api";
import { deriveTodoState } from "../todo";
import { LoaderCircle, Check, X, ChevronUp, ChevronDown, ArrowDown } from "lucide-react";
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
  // J: spinner shown while /panel or /vision blocks on the daemon side.
  panelThinking?: boolean;
  sendDisabled: boolean;
  onSend: (text: string, attachments: string[], steer: boolean) => Promise<void>;
  // Belt A: abort the running agent (Stop button / Esc).
  onCancel: () => void;
  // M1 memory distiller: the conversation's current epoch, surfaced by the
  // folded-all empty state ("continue in epoch N").
  epoch: number;
  // Conversation identity for the fold chip's expand memory: expansion is
  // tracked per (conversation, fold boundary) so a workstream switch or a
  // new distill never inherits a stale expanded view.
  conversationId?: number;
  // "Open note" on the fold chip — opens the wiki panel focused on the
  // folded epoch's note.
  onOpenNote: (path: string) => void;
  // Belt B: conversation-local search (⌘F). State is owned by App so the
  // command palette can open it too; matching happens here, over the events
  // already in memory — no IPC.
  searchOpen: boolean;
  searchQuery: string;
  onSearchQueryChange: (query: string) => void;
  onSearchClose: () => void;
  // M12 (D-auto): the daemon's auto-distill disclosure for the active
  // conversation — pending countdown (chip with Cancel), coverage-honesty
  // block (paused until a manual fold), and the in-flight indicator.
  autoDistill?: AutoDistillCountdown;
  autoDistillBlocked?: AutoDistillCountdown;
  distillInFlight?: boolean;
  onDisarmAutoDistill?: () => void;
  // Composer lock during a MANUAL distill only: an auto distill is
  // send-cancelled (cancel-before-note) and never blocks typing.
  distillLocked?: boolean;
  // M12 (D-todo): the composer "Plan" chip — read from the journaled
  // events, written via todo_update. projectRoot routes the IPC in the
  // multi-project case; onTodoChanged re-polls promptly after an op.
  projectRoot?: string | null;
  onTodoChanged?: () => void;
  onTodoError?: (message: string) => void;
}

// AutoDistillChip discloses the daemon's auto-distill state above the
// composer: scheduled countdown with a Cancel (auto_distill_ctl disarm),
// an in-flight "Distilling…" while the fold runs, or the coverage-honesty
// pause when the window outgrew the distill prompt budget. Data comes from
// pending_counts — the GUI never owns the trigger.
function AutoDistillChip({
  entry,
  blocked,
  inFlight,
  onDisarm,
}: {
  entry?: AutoDistillCountdown;
  blocked?: AutoDistillCountdown;
  inFlight: boolean;
  onDisarm?: () => void;
}) {
  const [nowUnix, setNowUnix] = useState(() => Math.floor(Date.now() / 1000));
  useEffect(() => {
    if (entry == null) return;
    const timer = window.setInterval(() => setNowUnix(Math.floor(Date.now() / 1000)), 1000);
    return () => window.clearInterval(timer);
  }, [entry]);
  if (inFlight) {
    return <div className="auto-distill-chip" title="The daemon is distilling this conversation's epoch">Distilling…</div>;
  }
  if (blocked != null) {
    return (
      <div
        className="auto-distill-chip blocked"
        title="The un-folded window outgrew the distill prompt budget — auto-distill never claims coverage it did not see. Run a manual Distill."
      >
        Auto-distill paused — window exceeds prompt budget · distill manually
      </div>
    );
  }
  if (entry == null) return null;
  const remaining = Math.max(0, entry.eta_unix - nowUnix);
  const m = Math.floor(remaining / 60);
  const s = remaining % 60;
  return (
    <div className="auto-distill-chip" title={
      entry.trigger === "urgent"
        ? "The conversation window crossed the size trigger — the daemon will fold it now"
        : entry.trigger === "startup"
          ? "Compensating for idle time while the app was closed"
          : "The daemon will fold this conversation's epoch after the idle quiet period"
    }>
      Distilling in {m}:{String(s).padStart(2, "0")}
      {onDisarm && (
        <>
          {" · "}
          <button type="button" className="chip-link" onClick={onDisarm}>
            Cancel
          </button>
        </>
      )}
    </div>
  );
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

// The latest distill's fold, derived from the journal events in memory.
// count comes from the marker's explicit window when present (the daemon
// journals first_seq/last_seq/note_sha), else from the derived boundary
// arithmetic — identical because per-conversation seqs are gap-free.
interface Fold {
  boundarySeq: number; // visibility split: the marker's payload last_seq when carried (pinned schema — events ≤ it are folded), else the marker's own seq (legacy); mirrors the daemon's foldBoundary
  markerSeq: number; // the marker's own seq: per-fold identity (expansion key — consecutive folds can share a boundarySeq)
  count: number; // events folded out of view
  notePath?: string; // folded epoch's wiki note, when the marker names one
  noteName?: string; // its basename without .md, for display
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
      <div className="bubble bubble-tool bubble-preview" aria-live="polite" aria-busy="true">
        <span className="preview-spinner" aria-hidden="true">
          <LoaderCircle size={12} className="spin" />
        </span>{" "}
        {tool}
        {intent}
      </div>
    );
  }
  const text = typeof p.text === "string" ? p.text : "";
  if (text === "") return null;
  return (
    <div className="bubble bubble-agent bubble-preview" aria-live="polite" aria-busy="true">
      {text}
      <span className="preview-caret" aria-hidden="true" />
    </div>
  );
}

// Run header: timestamp, tool call count, duration when the run journaled
// agent_done, and a status icon (✓ done / ✗ error / ⟳ running).
function RunHeader({ group }: { group: RunGroup }) {
  const start = group.start;
  if (!start) return null;
  const toolCalls = group.events.filter((e) => e.type === "agent_tool_call").length;
  const done = group.events.find((e) => e.type === "agent_done");
  const failed = group.events.find((e) => e.type === "agent_error");
  const status = failed ? "error" : done ? "done" : "running";
  const icon = failed ? <X size={11} /> : done ? <Check size={11} /> : <LoaderCircle size={11} className="spin" />;
  const startMs = Date.parse(start.created_at);
  const clock = Number.isNaN(startMs)
    ? "?"
    : new Date(startMs).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  const doneMs = done ? Date.parse(done.created_at) : NaN;
  const showDuration = done !== undefined && !Number.isNaN(startMs) && !Number.isNaN(doneMs);
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

// Slash command autocomplete: / prefix shows available commands.
const SLASH_COMMANDS = [
  { cmd: "/panel",  desc: "MoA thinking — fan out to N review models",          args: " <text>" },
  { cmd: "/vision", desc: "Vision analysis — send to K3 with image content blocks", args: " <text>" },
];

export default function ChatSurface({
  events,
  agentRunning,
  preview,
  panelThinking,
  sendDisabled,
  onSend,
  onCancel,
  epoch,
  conversationId,
  onOpenNote,
  searchOpen,
  searchQuery,
  onSearchQueryChange,
  onSearchClose,
  autoDistill,
  autoDistillBlocked,
  distillInFlight = false,
  onDisarmAutoDistill,
  distillLocked = false,
  projectRoot = null,
  onTodoChanged,
  onTodoError,
}: Props) {
  // M12 (D-todo): the plan layer's read side — derived from the journaled
  // event history already in memory (bootstrap replay + poll appends).
  const todoItems = useMemo(() => deriveTodoState(events), [events]);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [attachments, setAttachments] = useState<string[]>([]);
  const [dragOver, setDragOver] = useState(false);
  // Slash command autocomplete menu state.
  const [slashMenuOpen, setSlashMenuOpen] = useState(false);
  const [slashFilter, setSlashFilter] = useState("");
  const listRef = useRef<HTMLDivElement>(null);
  const composerRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  // Belt A stick-to-bottom: true while the user is pinned to the newest
  // output; scrolling up disengages, the "↓ new output" pill re-engages.
  const stickRef = useRef(true);
  const [newOutput, setNewOutput] = useState(false);

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
  // E P2: skip in browser dev mode — getCurrentWebview() requires
  // __TAURI_INTERNALS__ which only exists in the Tauri webview.
  useEffect(() => {
    if (typeof window !== "undefined" && !("__TAURI_INTERNALS__" in window)) return;
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

  // A1: Clipboard paste — webview File objects expose only filenames, not
  // real paths. Read as data URL → send base64 to daemon → get real path back.
  const handlePaste = async (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const files = Array.from(e.clipboardData?.files ?? []);
    if (files.length === 0) return; // plain text paste: let it through
    e.preventDefault();
    const paths: string[] = [];
    for (const file of files) {
      try {
        const dataUrl = await new Promise<string>((resolve, reject) => {
          const reader = new FileReader();
          reader.onload = () => resolve(reader.result as string);
          reader.onerror = () => reject(reader.error);
          reader.readAsDataURL(file);
        });
        // Strip "data:image/png;base64," prefix → raw base64.
        const base64 = dataUrl.split(",")[1] ?? "";
        const resp = await saveAttachment(file.name, base64);
        if (resp.ok && resp.path) {
          paths.push(resp.path);
        }
      } catch {
        // FileReader or save failed — skip this file silently.
      }
    }
    if (paths.length > 0) {
      addAttachments(paths);
    }
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

  // Belt A: ⌘/Ctrl+Enter sends from anywhere (Fix 4),
  // outside the textarea; the textarea's own keydown leaves the
  // modifier-Enter alone so this listener fires exactly once.
  const submitRef = useRef(submitDraft);
  useEffect(() => {
    submitRef.current = submitDraft;
  }, [submitDraft]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "Enter" && !e.isComposing && e.keyCode !== 229) {
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
  const handleDraftChange = (e: ChangeEvent<HTMLTextAreaElement>) => {
    const val = e.target.value;
    setDraft(val);
    // Slash command autocomplete: show menu when typing /word (no space yet)
    if (val.startsWith("/") && !val.includes(" ") && val.indexOf("/") === 0) {
      setSlashFilter(val.slice(1));
      setSlashMenuOpen(true);
    } else {
      setSlashMenuOpen(false);
    }
  };

  const handleComposerKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (slashMenuOpen && e.key === "Escape") {
      e.preventDefault();
      setSlashMenuOpen(false);
      return;
    }
    if (e.key === "Enter" && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
      // IME composition: if the user is composing CJK text, Enter confirms
      // the candidate — don't send the message. isComposing is true during
      // composition and becomes false on the compositionend event. The
      // keyCode 229 check is a legacy fallback for older webviews.
      if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) {
        return;
      }
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

  // M1 epoch filtering + epoch-fold provenance (root fix): the latest
  // distill review_action marks the fold boundary. The marker journals the
  // folded window [first_seq, last_seq] explicitly; legacy markers derive
  // it from journal order (previous marker seq+1 … marker seq−1). If no
  // distill has happened there is no fold and everything shows.
  const fold = useMemo<Fold | null>(() => {
    let latestIdx = -1;
    for (let i = events.length - 1; i >= 0; i--) {
      const e = events[i];
      if (e.type === "review_action" && e.payload?.action === "distill") {
        latestIdx = i;
        break;
      }
    }
    if (latestIdx < 0) return null;
    const marker = events[latestIdx];
    let first = marker.payload?.first_seq;
    let last = marker.payload?.last_seq;
    let boundary: number;
    if (first == null || last == null) {
      // Legacy marker (no journaled window): derive the window from
      // journal order and bound at the marker's own seq.
      first = 1;
      for (let i = latestIdx - 1; i >= 0; i--) {
        const e = events[i];
        if (e.type === "review_action" && e.payload?.action === "distill") {
          first = e.seq + 1;
          break;
        }
      }
      last = marker.seq - 1;
      boundary = marker.seq;
    } else {
      // Pinned schema (K3): the fold claims exactly [first_seq, last_seq].
      // Rows journaled during the committed phase land in
      // (last_seq, marker_seq) — the fold never rendered them, so they
      // stay visible above the chip (daemon foldBoundary semantics).
      boundary = last;
    }
    const notePath = marker.payload?.wiki_path || undefined;
    const noteName = notePath ? basename(notePath).replace(/\.md$/, "") : undefined;
    return {
      boundarySeq: boundary,
      markerSeq: marker.seq,
      count: Math.max(0, last - first + 1),
      notePath,
      noteName,
    };
  }, [events]);
  const lastDistillSeq = fold?.boundarySeq ?? 0;

  // Fold expansion is remembered per (conversation, boundary): a new
  // distill moves the boundary and re-collapses, and a workstream switch
  // can never display another conversation's journal unfolded by default.
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const foldKey = fold ? `${conversationId ?? "default"}:${fold.markerSeq}` : null;
  const expanded = foldKey !== null && expandedKey === foldKey;

  // Collapsed = above the fold boundary. Distill markers themselves are
  // filtered too — bookkeeping, never window content (the daemon's
  // windowEvents does the same): a pinned marker's own seq sits above its
  // last_seq, and the fold chip is its collapsed surface. Expanded shows
  // the raw journal, markers included.
  const visibleEvents = useMemo(
    () =>
      expanded
        ? events
        : events.filter(
            (e) => e.seq > lastDistillSeq && !(e.type === "review_action" && e.payload?.action === "distill"),
          ),
    [events, lastDistillSeq, expanded],
  );

  // Belt C (§Fix 1): group the visible events into runs — each
  // user_message opens a new group; everything until the next one belongs
  // to it.
  const runGroups = useMemo<RunGroup[]>(() => {
    const groups: RunGroup[] = [];
    for (const ev of visibleEvents) {
      if (ev.type === "user_message") {
        groups.push({ start: ev, events: [ev] });
      } else if (groups.length === 0) {
        groups.push({ start: null, events: [ev] });
      } else {
        groups[groups.length - 1].events.push(ev);
      }
    }
    return groups;
  }, [visibleEvents]);

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
              <ChevronUp size={14} />
            </button>
            <button
              type="button"
              aria-label="Next match"
              title="Next match (Enter)"
              disabled={matches.length === 0}
              onClick={() => stepMatch(1)}
            >
              <ChevronDown size={14} />
            </button>
            <button type="button" aria-label="Close search" title="Close (Esc)" onClick={onSearchClose}>
              <X size={14} />
            </button>
          </div>
        )}
        <div
          className="message-list"
          ref={listRef}
          onScroll={handleListScroll}
          aria-live="polite"
        >
        {fold && (
          <div className="fold-chip" role="note">
            <span className="fold-chip-text" title={fold.notePath}>
              {fold.count} event{fold.count === 1 ? "" : "s"} folded
              {fold.noteName ? ` → ${fold.noteName}` : ""}
            </span>
            <button
              type="button"
              className="fold-chip-btn"
              aria-expanded={expanded}
              onClick={() => setExpandedKey(expanded ? null : foldKey)}
            >
              {expanded ? "Collapse" : "Expand"}
            </button>
            {fold.notePath && (
              <button
                type="button"
                className="fold-chip-btn"
                onClick={() => onOpenNote(fold.notePath!)}
              >
                Open note
              </button>
            )}
          </div>
        )}
        {visibleEvents.length === 0 && (fold ? (
          <div className="empty-state">
            <h2>Everything here is folded</h2>
            <p className="dim">
              All {fold.count} events in this conversation were distilled into
              {fold.noteName ? <> <code>{fold.noteName}</code></> : " a wiki note"}.
              Nothing was lost — the journal keeps every event. Send a message
              to continue in epoch {epoch}, or reopen the folded record.
            </p>
            <button
              type="button"
              className="example-prompt"
              onClick={() => setExpandedKey(foldKey)}
            >
              Expand the folded record
            </button>
            {fold.notePath && (
              <button
                type="button"
                className="example-prompt"
                onClick={() => onOpenNote(fold.notePath!)}
              >
                Open the note
              </button>
            )}
          </div>
        ) : (
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
        ))}
        {panelThinking && (
          <div className="panel-thinking">
            <LoaderCircle size={14} className="spin" />
            <span>Panel consulting models…</span>
          </div>
        )}
          {runGroups.map((group) => (
            <div className="run-group" key={group.start?.seq ?? "preamble"}>
              <RunHeader group={group} />
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
          {preview && <PreviewBubble preview={preview} />}
          <ToolTicker running={agentRunning} events={events} />
        </div>
        {newOutput && (
          <button type="button" className="new-output-pill" onClick={scrollToBottom}>
            <ArrowDown size={12} /> new output
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
        <AutoDistillChip
          entry={autoDistill}
          blocked={autoDistillBlocked}
          inFlight={distillInFlight}
          onDisarm={onDisarmAutoDistill}
        />
        <PlanChip
          conversationId={conversationId}
          projectRoot={projectRoot}
          items={todoItems}
          onChanged={() => onTodoChanged?.()}
          onError={(m) => onTodoError?.(m)}
          disabled={sendDisabled || distillLocked}
        />
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
                  <X size={10} />
                </button>
              </span>
            ))}
          </div>
        )}
        <form className="chat-input" onSubmit={handleSubmit}>
          {slashMenuOpen && (
            <div className="slash-menu">
              {SLASH_COMMANDS
                .filter((c) => slashFilter === "" || c.cmd.startsWith("/" + slashFilter))
                .map((c) => (
                  <button
                    key={c.cmd}
                    type="button"
                    className="slash-item"
                    onMouseDown={(e) => {
                      e.preventDefault(); // don't blur the textarea
                      const newText = c.cmd + c.args;
                      setDraft(newText);
                      setSlashMenuOpen(false);
                      // Focus textarea and put cursor after the command
                      requestAnimationFrame(() => {
                        textareaRef.current?.focus();
                        const pos = c.cmd.length;
                        textareaRef.current?.setSelectionRange(pos, pos);
                      });
                    }}
                  >
                    <span className="slash-cmd">{c.cmd}</span>
                    <span className="slash-desc">{c.desc}</span>
                  </button>
                ))}
            </div>
          )}
          <textarea
            ref={textareaRef}
            aria-label="Message input"
            rows={1}
            value={draft}
            onChange={handleDraftChange}
            onBlur={() => {
              // Delay close so click registration on menu items fires first.
              setTimeout(() => setSlashMenuOpen(false), 150);
            }}
            onKeyDown={handleComposerKeyDown}
            onPaste={handlePaste}
            placeholder={
              dragOver
                ? "Drop files to attach them…"
                : agentRunning
                  ? "Steer the running agent… (Esc stops)"
                  : "Describe the change you want…"
            }
            // M12: locked during a MANUAL distill only — an auto distill
            // is send-cancelled daemon-side and never blocks typing.
            disabled={sendDisabled || sending || distillLocked}
            autoFocus
          />
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
          <button type="submit" disabled={sendDisabled || sending || distillLocked || !canSend}>
            {agentRunning ? "Steer" : "Send"}
          </button>
        </form>
        <div className="composer-hint">
          ⌘↵ send · Shift+↵ newline{agentRunning ? " · Esc stop" : ""}{distillLocked ? " · distilling…" : ""}
        </div>
      </div>
    </section>
  );
}
