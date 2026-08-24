import {
  ChangeEvent,
  ClipboardEvent,
  DragEvent,
  FormEvent,
  Fragment,
  KeyboardEvent as ReactKeyboardEvent,
  memo,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { getCurrentWebview } from "@tauri-apps/api/webview";
import { basename } from "../files";
import { deriveTurnStats, formatBytes, formatTokens } from "../stats";
import type { TurnStats } from "../stats";
import type { AutoDistillCountdown, OdoEvent, PanelProgress, PreviewEvent } from "../types";
import MessageBubble from "./MessageBubble";
import Markdown from "./Markdown";
import PlanChip from "./PlanChip";
import LoopChip from "./LoopChip";
import QueueDock from "./QueueDock";
import { saveAttachment } from "../api";
import { deriveTodoState } from "../todo";
import { deriveParkedGoals } from "../parked";
import { deriveActivePrompt, deriveSteerQueue, latestRunSteerSeqs } from "../steer_queue";
import { isAdvisorySlash } from "../slash";
import SteerQueue from "./SteerQueue";
import type { LoopState } from "../loop";
import { detectAtQuery, registerCompletionSource, resolveCompletions } from "../completions";
import type { CompletionItem } from "../completions";
import { makeWikiSource, makeWorkstreamSource } from "../completion-sources";
import { strings } from "../strings";
import { LoaderCircle, Check, X, ChevronUp, ChevronDown, ArrowDown, Archive } from "lucide-react";
import ToolTicker from "./ToolTicker";
import RunGroupBoundary from "./RunGroupBoundary";
import ModelPill from "./ModelPill";
import { ChatSkeleton } from "./LoadingInline";
import { Button } from "./ui/button";
import { cn } from "../lib/utils";

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
  // /panel heartbeat: the daemon's live fan-out tally (N legs answered of
  // M), rendered in the spinner row. Null when no panel is in flight.
  panelProgress?: PanelProgress | null;
  sendDisabled: boolean;
  // W6 (goal queue): park queues the message as a parked goal; at most one
  // of steer/park is true (parkArmed forces steer off — the daemon refuses
  // a steer+park combination).
  onSend: (text: string, attachments: string[], steer: boolean, park?: boolean) => Promise<void>;
  // W6 (goal queue): the QueueDock's manual actions, forwarded to the
  // daemon's resume_parked_goal / drop_parked_goal.
  onResumeParked?: (seq: number) => Promise<void>;
  onDropParked?: (seq: number) => Promise<void>;
  // Steer queue: the SteerQueue panel's manual drop, forwarded to the
  // daemon's drop_queued_steer.
  onDropSteer?: (seq: number) => Promise<void>;
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
  // M19 (/loop) V1: the loop chip's journal-folded states (App derives
  // them); stop/resume ride loop_ctl and re-poll promptly like todos.
  loops?: LoopState[];
  onLoopChanged?: () => void;
  onLoopError?: (message: string) => void;
  // Model pill: shows the current coding model in the composer, lets
  // the user switch per-message without opening Settings.
  codingModel?: string | null;
  onModelChanged?: () => void;
  // Skeleton: show content-shaped placeholder while conversation loads.
  loading?: boolean;
}

// Utilities replacing the deleted .auto-distill-chip CSS rule (inline-flex
// chip, hairline border, dim mono text). QueueDock.tsx keeps an inlined copy
// for its Popover trigger — do not import across the boundary.
const AUTO_DISTILL_CHIP_BASE =
  "inline-flex items-center self-start gap-1 mx-4 rounded-lg border border-border bg-bg-input px-2.5 py-0.5 font-mono text-[length:var(--text-caption)] text-text-dim";

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
    return (
      <div
        className={cn("auto-distill-chip", AUTO_DISTILL_CHIP_BASE)}
        title="The daemon is distilling this conversation's epoch"
      >
        Distilling…
      </div>
    );
  }
  if (blocked != null) {
    return (
      <div
        className={cn("auto-distill-chip blocked", AUTO_DISTILL_CHIP_BASE, "text-warn")}
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
    <div className={cn("auto-distill-chip", AUTO_DISTILL_CHIP_BASE)} title={
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
          <button
            type="button"
            className="chip-link cursor-pointer border-none bg-transparent p-0 text-accent [font:inherit] leading-none hover:underline"
            onClick={onDisarm}
          >
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
interface Fold {
  boundarySeq: number; // visibility split: the marker's payload last_seq when carried (pinned schema — events ≤ it are folded), else the marker's own seq (legacy); mirrors the daemon's foldBoundary
  markerSeq: number; // the marker's own seq: per-fold identity (expansion key — consecutive folds can share a boundarySeq)
  // Folded epoch for the chip label ("epoch N · M events folded"). The
  // marker journals the POST-distill counter (the daemon increments before
  // writing), so the folded note's epoch is one less; undefined only when
  // the marker carries no epoch at all.
  epoch?: number;
  // Seq of the newest user_message at or below the boundary: its run stays
  // visible above the chip — the fold must never hide the most recent
  // agent run, or the user returns to bare bookkeeping rows with no
  // context. null = no user_message below the boundary → everything there
  // is folded (pre-fix behavior).
  newestUserSeq: number | null;
  // Events the fold actually hides: seq ≤ boundary, older than the kept
  // run. Older distill markers inside that range count too — the collapsed
  // surface hides them and Expand reveals them, so leaving them out would
  // under-report. The fold's own marker does NOT count: it is the chip's
  // subject, not its content.
  count: number;
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

// Collapsed tool-group summary (ui/message-stream, Hermes ToolActivityGroup
// parity): the collapsed row names what the burst ended with — last call's
// tool + a single-line arg snippet — so "N tool calls" isn't an information
// dead end. Same parse posture as MessageBubble: daemon journals args as a
// JSON string, parse defensively.
function toolCallSnippet(payload: OdoEvent["payload"] | undefined): string {
  const tool = typeof payload?.tool === "string" && payload.tool !== "" ? payload.tool : "tool";
  let args = payload?.args;
  if (typeof args === "string") {
    try { args = JSON.parse(args); } catch { /* keep raw string */ }
  }
  let snippet = "";
  if (args != null && typeof args === "object" && !Array.isArray(args)) {
    const first = Object.entries(args as Record<string, unknown>)[0];
    if (first) {
      const [k, v] = first;
      const sv = typeof v === "object" && v !== null ? JSON.stringify(v) : String(v);
      snippet = `${k}: ${sv}`;
    }
  } else if (typeof args === "string") {
    snippet = args;
  }
  const flat = snippet === "" ? tool : `${tool}(${snippet})`.replace(/\s+/g, " ").trim();
  return flat.length > 72 ? `${flat.slice(0, 71)}…` : flat;
}

// M7 streaming preview: the in-flight block below the journaled bubbles —
// dimmed and italic so it reads as provisional. Text previews end in a
// pulsing caret; tool previews lead with a spinning ⟳ plus the call's
// intent. When the block completes, the next poll journals it and this
// bubble is replaced by the real one.
function PreviewBubble({ preview, projectRoot }: { preview: PreviewEvent; projectRoot?: string | null }) {
  const p = preview.payload ?? {};
  if (preview.type === "agent_tool_call") {
    const tool = typeof p.tool === "string" && p.tool !== "" ? p.tool : "tool";
    const intent = typeof p.intent === "string" && p.intent !== "" ? ` — ${p.intent}` : "";
    return (
      <div className="bubble bubble-tool bubble-preview self-start bg-transparent text-text-dim font-mono text-caption px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)] opacity-60 italic" aria-live="polite" aria-busy="true">
        <span className="preview-spinner inline-flex items-center" aria-hidden="true">
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
    <div className="bubble bubble-agent bubble-preview w-full max-w-[var(--chat-column-width,100%)] mx-auto bg-bg-raised text-[var(--agent-text)] border border-stroke-secondary rounded-[12px_12px_12px_4px] px-3.5 py-2.5 whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)] opacity-85" aria-live="polite" aria-busy="true">
      <Markdown content={text} projectRoot={projectRoot} />
      <span className="preview-caret" aria-hidden="true" />
    </div>
  );
}

// Wave B #8: billed-usage branch — real token counts justify a real rate
// (tok/s = billed output tokens over journaled wall seconds).
function formatStatsWithTokens(stats: TurnStats): string {
  const parts: string[] = [];
  if (stats.inputTokens != null) parts.push(`in ${formatTokens(stats.inputTokens)}`);
  if (stats.outputTokens != null) {
    parts.push(`out ${formatTokens(stats.outputTokens)}`);
    const secs = stats.wallMs / 1000;
    if (secs > 0) parts.push(`${(stats.outputTokens / secs).toFixed(1)} tok/s`);
  }
  return parts.join(" · ");
}

// Run header: timestamp, tool call count, duration when the run journaled
// agent_done, and a status icon (✓ done / ✗ error / ⟳ running).
// Wave B #8: completed turns also carry a dim stats strip — wall time plus
// input/output sizes derived from journaled facts (prompt receipt bytes
// on the run's user_message; UTF-8 bytes of the agent_text bodies). When
// the payload later carries billed usage (input_tokens/output_tokens),
// the strip upgrades to tokens + tok/s — byte-only rows never show a
// fabricated rate.
function RunHeader({ group }: { group: RunGroup }) {
  const start = group.start;
  if (!start) return null;
  const toolCalls = group.events.filter((e) => e.type === "agent_tool_call").length;
  const done = group.events.find((e) => e.type === "agent_done");
  const failed = group.events.find((e) => e.type === "agent_error");
  // Item 10: surface the diff review outcome in the run header.
  const reviewAction = group.events.find(
    (e) => e.type === "review_action" && (e.payload?.action === "accept" || e.payload?.action === "reject"),
  );
  const diffOutcome = reviewAction?.payload?.action as string | undefined;
  const diffId = reviewAction?.payload?.diff_id as number | undefined;
  const status = failed ? "error" : done ? "done" : "running";
  const icon = failed ? <X size={11} aria-hidden /> : done ? <Check size={11} aria-hidden /> : <LoaderCircle size={11} aria-hidden className="spin" />;
  const startMs = Date.parse(start.created_at);
  const clock = Number.isNaN(startMs)
    ? "?"
    : new Date(startMs).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  const doneMs = done ? Date.parse(done.created_at) : NaN;
  const showDuration = done !== undefined && !Number.isNaN(startMs) && !Number.isNaN(doneMs);
  const stats = deriveTurnStats(start, group.events, done);
  return (
    <div className="run-header mt-3.5 flex items-baseline gap-2 border-t border-stroke-tertiary px-1 pt-2 pb-0.5 text-caption text-text-dim tabular-nums">
      <span
        className={cn(
          "run-status border-t-0 bg-transparent p-0 [font-family:inherit] [font-size:inherit]",
          status,
          status === "done" && "text-ok-text",
          status === "error" && "text-err-text",
          status === "running" && "text-accent-user",
        )}
      >
        {icon}
      </span>
      <span>{clock}</span>
      <span>{`${toolCalls} tool call${toolCalls === 1 ? "" : "s"}`}</span>
      {showDuration && <span>{formatElapsed(doneMs - startMs)}</span>}
      {diffOutcome && (
        <span
          className={cn(
            "run-diff-outcome ml-1 rounded px-1.5 py-px text-[10px] font-medium",
            diffOutcome === "accept" ? "bg-ok/10 text-ok-text" : "bg-err/10 text-err",
          )}
        >
          {diffOutcome === "accept" ? "✓" : "✗"} diff #{diffId ?? "?"} {diffOutcome === "accept" ? "accepted" : "rejected"}
        </span>
      )}
      {stats != null && (
        <span
          className="run-turn-stats text-[10px] text-text-dim"
          title={
            "in = journaled prompt bytes (receipt closure); out = agent text bytes." +
            (stats.outputTokens != null ? " Tokens are usage billed by the provider." : "")
          }
        >
          {stats.wallMs > 0 && stats.outputTokens != null
            ? ` · ${formatStatsWithTokens(stats)}`
            : stats.inputBytes != null
              ? ` · in ${formatBytes(stats.inputBytes)} · out ${formatBytes(stats.outputBytes)}`
              : ` · out ${formatBytes(stats.outputBytes)}`}
        </span>
      )}
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
// must match the composer textarea's max-h-[148px] utility.
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

// Slash command autocomplete (V13, Hermes-parity): typing "/" at the
// composer start opens the FULL list immediately and filtering narrows
// it; entries carry a one-line description; /loop's subcommands are
// first-class entries (mirrors the daemon's four routing prefixes in
// internal/ipc/rules_audit.go rulesAuditSlashCommands).
const SLASH_COMMANDS = [
  { cmd: "/panel",  desc: "MoA thinking — fan out to N review models",          args: " <text>" },
  { cmd: "/vision", desc: "Vision analysis — send to K3 with image content blocks", args: " <text>" },
  { cmd: "/preview", desc: "Screenshot a localhost page and analyze it", args: " <url> [prompt]" },
  { cmd: "/loop audit", desc: "Audit→fix→land until clean (SEED lands pending diffs first)", args: "" },
  { cmd: "/loop tasks", desc: "Work a task list through design-locked implement runs", args: "" },
  { cmd: "/loop status", desc: "Fold dump of every loop on this conversation", args: "" },
  { cmd: "/loop stop", desc: "Terminal stop — cancels the in-flight loop run", args: "" },
  { cmd: "/loop resume", desc: "Clear a suspend and re-tick (optional budget=T)", args: "" },
];

function ChatSurface({
  events,
  agentRunning,
  preview,
  panelThinking,
  panelProgress,
  sendDisabled,
  onSend,
  onResumeParked,
  onDropParked,
  onDropSteer,
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
  loops = [],
  onLoopChanged,
  onLoopError,
  codingModel = null,
  onModelChanged,
  loading = false,
}: Props) {
  // Message edit: refill the composer with the original text and focus.
  const handleEditMessage = useCallback((text: string) => {
    setDraft(text);
    requestAnimationFrame(() => {
      textareaRef.current?.focus();
      // Place cursor at end.
      const len = text.length;
      textareaRef.current?.setSelectionRange(len, len);
    });
  }, []);
  // M12 (D-todo): the plan layer's read side — derived from the journaled
  // event history already in memory (bootstrap replay + poll appends).
  const todoItems = useMemo(() => deriveTodoState(events), [events]);
  // W6 (goal queue): the QueueDock's read side — same derivation rule as
  // todoItems (full journal, same as the daemon), so a workstream switch
  // or daemon restart repopulates the dock on bootstrap replay.
  const parkedGoals = useMemo(() => deriveParkedGoals(events), [events]);
  // Steer queue: the busy run's pinned prompt + its journaled mid-run
  // instructions — same derivation rule as parkedGoals (full journal, same
  // as the daemon's drain ledger), so the poll loop reconciles every
  // consumption and drop without dedicated IPC. latestRunSteerSeqs shares
  // deriveActivePrompt's starter scan, so the joined-steer count can never
  // drift from the prompt it labels.
  const steerQueue = useMemo(() => deriveSteerQueue(events), [events]);
  const activePrompt = useMemo(() => deriveActivePrompt(events), [events]);
  const activeSteerCount = useMemo(() => latestRunSteerSeqs(events)?.length ?? 0, [events]);
  // W6: the composer park toggle. Armed → submit parks the goal instead of
  // sending/steering. A conversation switch disarms: park intent never
  // leaks across workstreams.
  const [parkArmed, setParkArmed] = useState(false);
  useEffect(() => setParkArmed(false), [conversationId]);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [attachments, setAttachments] = useState<string[]>([]);
  const [dragOver, setDragOver] = useState(false);
  // Slash command autocomplete menu state.
  const [slashMenuOpen, setSlashMenuOpen] = useState(false);
  const [slashFilter, setSlashFilter] = useState("");
  // Keyboard-driven active row for the slash menu (0 = first).
  const [slashIndex, setSlashIndex] = useState(0);
  // V13: the accepted command word renders as a highlighted token overlay
  // in the composer until the text runs or the token span is edited.
  const [slashToken, setSlashToken] = useState<string | null>(null);
  // P3 @-mention completion popup: resolved items for the live @query.
  const [atMenu, setAtMenu] = useState<{ items: CompletionItem[]; query: string } | null>(null);
  // Keyboard-driven active row for the @ popup.
  const [atIndex, setAtIndex] = useState(0);
  // Debounce timer + staleness seq for async @ resolution.
  const atTimerRef = useRef<number | null>(null);
  const atSeqRef = useRef(0);
  const listRef = useRef<HTMLDivElement>(null);
  const composerRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  // True between compositionstart and compositionend. React 19 ignores
  // mid-composition input events, so `draft` trails the DOM while a CJK
  // IME session is live; the value-sync effect below must not write in
  // that window (a stale write aborts the composition — the "steering
  // text washed away by stream updates" bug).
  const composingRef = useRef(false);
  // Belt A stick-to-bottom: true while the user is pinned to the newest
  // output. Only a deliberate gesture disengages — wheel-up over the
  // stream, a touch drag toward older output, or a scrollbar-thumb drag
  // upward. Scroll EVENTS alone never disengage: content-visibility size
  // resolution and scroll anchoring shift scrollTop without user intent,
  // and treating those as "scrolled away" silently killed the follow
  // during live runs ("working" shown, view frozen mid-stream).
  const stickRef = useRef(true);
  const [newOutput, setNewOutput] = useState(false);
  // Our own writes land in scroll events too; stick updates ignore scroll
  // events for a beat after each programmatic write.
  const programmaticUntilRef = useRef(0);
  // Scrollbar-thumb drag: armed by a pointerdown in the right gutter.
  // While armed, scroll events report real user intent and the auto-pin
  // stops fighting the drag.
  const dragScrollRef = useRef(false);
  const touchYRef = useRef<number | null>(null);
  const contentRef = useRef<HTMLDivElement>(null);

  const pinToBottom = useCallback(() => {
    const el = listRef.current;
    if (!el || dragScrollRef.current) return;
    programmaticUntilRef.current = performance.now() + 250;
    el.scrollTop = el.scrollHeight;
  }, []);

  const handleListScroll = () => {
    const el = listRef.current;
    if (!el) return;
    if (!dragScrollRef.current && performance.now() < programmaticUntilRef.current) return;
    // Re-engage on any landing at the bottom (scroll down, pill click,
    // drag to bottom); never disengage here — gestures do that.
    if (el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX) {
      stickRef.current = true;
      setNewOutput(false);
    }
  };

  // Disarm the scrollbar-drag latch on release anywhere (the pointer can
  // leave the gutter mid-drag).
  useEffect(() => {
    const disarm = () => { dragScrollRef.current = false; };
    window.addEventListener("pointerup", disarm);
    return () => window.removeEventListener("pointerup", disarm);
  }, []);

  // Auto-follow only while stuck; otherwise surface the pill. New events
  // while scrolled up are exactly the case the pill exists for.
  // Item 9: early-return when !stickRef to avoid the scrollHeight DOM read
  // on every 350ms poll when the user has scrolled up (tri-model perf).
  useEffect(() => {
    if (!stickRef.current) {
      setNewOutput(true);
      return;
    }
    pinToBottom();
    // Preview changes poll-by-poll without touching events.length, so it
    // joins the follow-the-tail trigger too.
  }, [events.length, preview, pinToBottom]);

  // A conversation switch (sidebar click, workstream jump) is a fresh
  // view: re-engage the follow and land on the newest events, even when
  // the previous conversation was left scrolled up. ChatSurface is NOT
  // remounted on a switch, so stick state would otherwise leak across
  // workstreams and leave the new view wherever the old one scrolled.
  useEffect(() => {
    stickRef.current = true;
    setNewOutput(false);
    pinToBottom();
  }, [conversationId, pinToBottom]);

  // A run flipping state (done banner appearing, ticker hiding) also nudges
  // the view, but never yanks a reader back down.
  useEffect(() => {
    if (stickRef.current) pinToBottom();
  }, [agentRunning, pinToBottom]);

  // Re-pin on growth the event counter can't see: content-visibility size
  // resolution, markdown/code settling, image loads, <details> toggles,
  // composer growth shrinking the viewport. Without this the initial pin
  // can land viewports above the true bottom with no later trigger.
  useEffect(() => {
    if (typeof ResizeObserver === "undefined") return; // jsdom / legacy webview
    const list = listRef.current;
    const content = contentRef.current;
    if (!list || !content) return;
    const ro = new ResizeObserver(() => {
      if (stickRef.current) pinToBottom();
    });
    ro.observe(content);
    ro.observe(list);
    return () => ro.disconnect();
  }, [pinToBottom]);

  const scrollToBottom = () => {
    stickRef.current = true;
    setNewOutput(false);
    pinToBottom();
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

  // Live mirror of the attachment list for async guards: the detached
  // advisory failure path fires long after this closure's `attachments`
  // went stale, and a late catch can land between a chip add and its
  // render commit. (The draft needs no mirror — the uncontrolled
  // textarea's DOM value reflects keystrokes synchronously.)
  const attachmentsLiveRef = useRef(attachments);
  useLayoutEffect(() => {
    attachmentsLiveRef.current = attachments;
  }, [attachments]);

  // WKWebView IME: a submit that raced a live composition (Enter to
  // confirm a candidate whose keydown arrived without isComposing /
  // keyCode 229) can strand the webview's marked text — the blue
  // "select-all" look — in the DOM: compositionend never fires, and the
  // value-sync effect's composingRef guard then refuses to write the
  // cleared draft. A successful submit ends the composition semantically,
  // so force the DOM node empty here instead of relying on events the
  // webview may never send.
  //
  // keepPark: advisory slash sends (/panel …) consult read-only and route
  // BEFORE the daemon's park branch in handleSendMessage, so the armed
  // toggle was never consumed by a queue — clearing it would silently
  // drop the parked intent. Only a submitted goal disarms the one-shot.
  const clearComposer = useCallback((opts?: { keepPark?: boolean }) => {
    const ta = textareaRef.current;
    if (ta) ta.value = "";
    composingRef.current = false;
    attachmentsLiveRef.current = [];
    setDraft("");
    setAttachments([]);
    setSlashToken(null);
    if (!opts?.keepPark) setParkArmed(false); // one-shot: park targets the submitted goal only
  }, []);

  const submitDraft = useCallback(async () => {
    const text = draft.trim();
    if (!canSend || sending || sendDisabled) return;
    // M1 steering: while the agent runs, submitting journals the message
    // with steer=true instead of starting a new run. W6: an armed park
    // toggle forces steer off (structural mutex — the daemon refuses
    // steer+park) and queues the goal instead.
    const steer = agentRunning && !parkArmed;
    // Advisory slash sends (/panel, /vision, /preview): the daemon answers
    // synchronously inside send_message and the MoA fan-out can hold the
    // RPC for minutes. Holding `sending` that long disabled the textarea
    // with the draft still in it — every workstream's composer looked
    // frozen, while the journaled question already reaches the transcript
    // via the poll loop. Detach: clear now, skip the `sending` lock, and
    // keep the promise alive only for the guarded failure restore below.
    if (isAdvisorySlash(text)) {
      const token = slashToken;
      const sent = onSend(text, attachments, steer, parkArmed);
      clearComposer({ keepPark: true });
      void sent.catch(() => {
        // The consult can fail LATE — after the user composed a follow-up
        // in the box meanwhile (slash receipt gate, daemon restart, IPC
        // drop). Restoring unconditionally would silently erase that
        // typing, so restore only an untouched composer; App's error
        // banner names the failure either way.
        const ta = textareaRef.current;
        if ((ta != null && ta.value !== "") || attachmentsLiveRef.current.length > 0) return;
        setDraft(text);
        setAttachments(attachments);
        // Match the normal path's failure posture (a fully intact
        // composer): re-highlight the command token when the restored
        // text still starts with it.
        if (token != null && (text === token || text.startsWith(token + " "))) setSlashToken(token);
      });
      return;
    }
    setSending(true);
    try {
      await onSend(text, attachments, steer, parkArmed);
      clearComposer();
    } catch {
      // onSend already surfaced the error; keep the draft (and the armed
      // toggle) for retry.
    } finally {
      setSending(false);
    }
  }, [draft, attachments, canSend, sending, sendDisabled, onSend, agentRunning, parkArmed, slashToken, clearComposer]);

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
  // WebKit counts a WRAPPED placeholder in textarea.scrollHeight (a narrow
  // composer — e.g. window-width slack eaten by the open context panel —
  // wraps the placeholder onto 2-3 lines), so the mount-time measurement
  // bakes placeholder height in and only the first keystroke corrects it.
  // Strip the placeholder for the measurement; it restores before paint.
  const fitComposer = useCallback(() => {
    const ta = textareaRef.current;
    if (!ta) return;
    const ph = ta.placeholder;
    ta.placeholder = "";
    ta.style.height = "auto";
    ta.style.height = `${Math.min(ta.scrollHeight, COMPOSER_MAX_HEIGHT)}px`;
    ta.placeholder = ph;
  }, []);
  useEffect(fitComposer, [draft, fitComposer]);
  // The composer textarea is UNCONTROLLED (defaultValue, no `value` prop):
  // React 19 drops `input` events that arrive mid-composition, so with a
  // controlled binding any re-render during a CJK IME session wrote the
  // stale draft back into the node and aborted the composition — while a
  // run streams, the 350ms polls + 1s heartbeat made that near-instant.
  // Instead, programmatic draft writes (send-clear, slash pick, edit
  // fill) reach the DOM here, and only outside an active composition;
  // compositionend realigns `draft` with the committed text.
  useLayoutEffect(() => {
    const ta = textareaRef.current;
    if (ta && !composingRef.current && ta.value !== draft) ta.value = draft;
  }, [draft]);
  // Refit on width changes too (context panel toggle, window resize,
  // sidebar collapse): the effect above only runs when the draft changes,
  // and a width change alters wrapping without touching the draft. Gate on
  // width so our own height writes don't retrigger the observer.
  const composerWidthRef = useRef(0);
  useEffect(() => {
    const ta = textareaRef.current;
    if (!ta || typeof ResizeObserver === "undefined") return; // jsdom / legacy webview
    composerWidthRef.current = ta.offsetWidth;
    const ro = new ResizeObserver(() => {
      const w = ta.offsetWidth;
      if (w === composerWidthRef.current) return;
      composerWidthRef.current = w;
      fitComposer();
    });
    ro.observe(ta);
    return () => ro.disconnect();
  }, [fitComposer]);

  // P3: @-mention sources. Workstreams are project-scoped (the project root
  // travels on each resolve ctx); wiki notes are conversation-scoped, so the
  // wiki source re-registers when the conversation changes. Registration is
  // inert until the @ popup actually resolves.
  useEffect(() => registerCompletionSource(makeWorkstreamSource()), []);
  useEffect(() => {
    if (conversationId == null) return;
    return registerCompletionSource(makeWikiSource(conversationId));
  }, [conversationId]);

  // P3: debounced @ resolution (100ms) so typing across the @query doesn't
  // spam IPC; a stale resolution (superseded by a newer keystroke) is dropped
  // by the seq guard.
  const queueAtResolve = (query: string) => {
    if (atTimerRef.current != null) window.clearTimeout(atTimerRef.current);
    const seq = ++atSeqRef.current;
    atTimerRef.current = window.setTimeout(() => {
      atTimerRef.current = null;
      void resolveCompletions({ query, projectRoot }).then((items) => {
        if (seq !== atSeqRef.current) return;
        const q = query.toLowerCase();
        const filtered =
          q === ""
            ? items
            : items.filter((it) => it.label.toLowerCase().includes(q) || it.insert.toLowerCase().includes(q));
        setAtIndex(0);
        setAtMenu(filtered.length > 0 ? { items: filtered, query } : null);
      });
    }, 100);
  };
  useEffect(
    () => () => {
      if (atTimerRef.current != null) window.clearTimeout(atTimerRef.current);
    },
    [],
  );

  const closeAtMenu = () => {
    atSeqRef.current++; // invalidate any in-flight resolution
    if (atTimerRef.current != null) window.clearTimeout(atTimerRef.current);
    setAtMenu(null);
  };

  // Replace the `@query` span ending at the caret with the picked item's
  // insert text, then restore focus after the inserted text.
  const pickAtItem = (item: CompletionItem) => {
    const caret = textareaRef.current?.selectionStart ?? draft.length;
    const before = draft.slice(0, caret);
    const at = before.lastIndexOf("@");
    if (at < 0) {
      closeAtMenu();
      return;
    }
    setDraft(before.slice(0, at) + item.insert + draft.slice(caret));
    closeAtMenu();
    requestAnimationFrame(() => {
      textareaRef.current?.focus();
      const pos = at + item.insert.length;
      textareaRef.current?.setSelectionRange(pos, pos);
    });
  };

  // Belt A composer keys (Fix 3): Enter sends, Shift+Enter newlines (the
  // default), Esc stops a run or clears the draft. Escape is swallowed here
  // so App's global handler (blur/cancel for focus elsewhere) doesn't
  // double-fire.
  const handleDraftChangeValue = (val: string, caret: number | null) => {
    setDraft(val);
    // V13 token: survives only while the draft still starts with the
    // accepted command word — an edit inside the token clears it.
    if (slashToken != null && !(val === slashToken || val.startsWith(slashToken + " "))) {
      setSlashToken(null);
    }
    // Slash command autocomplete (V13): "word [subcommand]" at the
    // composer start opens the menu — the space after the command word
    // keeps it open so /loop's subcommand entries stay reachable; args
    // territory (a second space) or a non-"/" draft closes it.
    if (/^\/(?:\S*\s?\S*)?$/.test(val)) {
      setSlashFilter(val.slice(1));
      setSlashIndex(0);
      setSlashMenuOpen(true);
    } else {
      setSlashMenuOpen(false);
    }
    // P3 @-mention completion: `@` opens the popup only at a word start
    // (line start or after whitespace) — emails and code never trigger.
    const atQuery = caret == null ? null : detectAtQuery(val, caret);
    if (atQuery != null) queueAtResolve(atQuery);
    else closeAtMenu();
  };
  const handleDraftChange = (e: ChangeEvent<HTMLTextAreaElement>) => {
    handleDraftChangeValue(e.target.value, e.target.selectionStart);
  };

  // Slash commands matching the current filter — single source for the
  // menu render, the active-row clamp, and the Enter pick.
  const slashItems = SLASH_COMMANDS.filter(
    (c) => slashFilter === "" || c.cmd.startsWith("/" + slashFilter),
  );

  // Apply a slash command (mouse or Enter/Tab): replace the draft, close
  // the menu, park the caret right after the command word, and mark the
  // command as the highlighted composer token (V13).
  const pickSlash = (cmd: string, args: string) => {
    setDraft(cmd + args);
    setSlashToken(cmd);
    setSlashMenuOpen(false);
    requestAnimationFrame(() => {
      textareaRef.current?.focus();
      const pos = cmd.length;
      textareaRef.current?.setSelectionRange(pos, pos);
    });
  };

  // Keep the keyboard-active row visible when arrowing through a long menu.
  useEffect(() => {
    document
      .querySelector(".slash-menu .slash-item.selected, .at-menu .at-item.selected")
      ?.scrollIntoView({ block: "nearest" });
  }, [slashIndex, atIndex]);

  const handleComposerKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (atMenu && e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      closeAtMenu();
      return;
    }
    if (slashMenuOpen && e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      setSlashMenuOpen(false);
      return;
    }
    // Listbox keyboard model: ArrowUp/Down move the active row, Enter picks
    // it (instead of sending the "/…" or "@…" draft as a chat message).
    // IME guard: during CJK composition, arrow keys move the caret in the
    // candidate window — don't hijack them (K3 final review S2).
    if ((atMenu || (slashMenuOpen && slashItems.length > 0)) && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return;
      e.preventDefault();
      if (atMenu) {
        const n = atMenu.items.length;
        setAtIndex((i) => (e.key === "ArrowDown" ? (i + 1) % n : (i + n - 1) % n));
      } else {
        const n = slashItems.length;
        setSlashIndex((i) => (e.key === "ArrowDown" ? (i + 1) % n : (i + n - 1) % n));
      }
      return;
    }
    if (atMenu && e.key === "Enter" && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
      if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return;
      e.preventDefault();
      const item = atMenu.items[Math.min(atIndex, atMenu.items.length - 1)];
      if (item) pickAtItem(item);
      return;
    }
    // V13: Tab accepts exactly like Enter (Hermes-parity); without the
    // preventDefault Tab would walk focus out of the composer.
    if (slashMenuOpen && slashItems.length > 0 && (e.key === "Enter" || e.key === "Tab") && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
      if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return;
      e.preventDefault();
      const c = slashItems[Math.min(slashIndex, slashItems.length - 1)];
      if (c) pickSlash(c.cmd, c.args);
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
        setSlashToken(null);
      }
    }
  };

  // M1 epoch filtering + epoch-fold provenance (root fix) + fold blind-spot
  // fix: the latest distill review_action marks the fold boundary. The
  // marker journals the folded window [first_seq, last_seq] explicitly;
  // legacy markers bound at the marker's own seq. If no distill has
  // happened there is no fold and everything shows. The newest run below
  // the boundary always stays visible (newestUserSeq): folding it away
  // would leave the user staring at bare bookkeeping rows with no context.
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
    const first = marker.payload?.first_seq;
    const last = marker.payload?.last_seq;
    let boundary: number;
    if (first == null || last == null) {
      // Legacy marker (no journaled window): bound at the marker's own
      // seq — everything journaled before it is folded.
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
    // The marker journals the post-distill counter; the folded note's
    // epoch is one less.
    const epoch = typeof marker.payload?.epoch === "number" ? marker.payload.epoch - 1 : undefined;
    // Blind-spot fix: the boundary alone would hide the newest run below
    // it too — the user returns to bare bookkeeping rows with no context.
    // newestUserSeq keeps that run above the chip. Find it by MAX seq, not
    // by array position: journal order is not guaranteed seq-ascending
    // (committed-phase rows journal after the marker, above last_seq), and
    // a positional scan could miss the true newest user_message.
    let newestUserSeq: number | null = null;
    for (const e of events) {
      if (e.seq > boundary) continue;
      if (e.type === "user_message" && (newestUserSeq == null || e.seq > newestUserSeq)) {
        newestUserSeq = e.seq;
      }
    }
    // Count only what the fold actually hides: seq ≤ boundary, older than
    // the kept run. Older distill markers in that range count — Expand
    // reveals them, so skipping them would under-report. The fold's own
    // marker (seq === marker.seq, reachable when a legacy boundary equals
    // it) is the chip's subject, not its content.
    let count = 0;
    for (const e of events) {
      if (e.seq > boundary) continue;
      if (e.seq === marker.seq) continue;
      if (newestUserSeq != null && e.seq >= newestUserSeq) continue;
      count++;
    }
    return {
      boundarySeq: boundary,
      markerSeq: marker.seq,
      epoch,
      newestUserSeq,
      count,
      notePath,
      noteName,
    };
  }, [events]);
  const lastDistillSeq = fold?.boundarySeq ?? 0;
  const foldKeepSeq = fold?.newestUserSeq ?? null;

  // Fold expansion is remembered per (conversation, boundary): a new
  // distill moves the boundary and re-collapses, and a workstream switch
  // can never display another conversation's journal unfolded by default.
  const [expandedKey, setExpandedKey] = useState<string | null>(null);
  const foldKey = fold ? `${conversationId ?? "default"}:${fold.markerSeq}` : null;
  const expanded = foldKey !== null && expandedKey === foldKey;

  // Collapsed = above the fold boundary, PLUS the newest run at or below
  // it (foldKeepSeq) so the fold never blanks the latest agent run from
  // view. Distill markers themselves are filtered too — bookkeeping, never
  // window content (the daemon's windowEvents does the same): a pinned
  // marker's own seq sits above its last_seq, and the fold chip is its
  // collapsed surface. Expanded shows the raw journal, markers included.
  const visibleEvents = useMemo(
    () =>
      expanded
        ? events
        : events.filter(
            (e) =>
              (e.seq > lastDistillSeq || (foldKeepSeq != null && e.seq >= foldKeepSeq)) &&
              !(e.type === "review_action" && e.payload?.action === "distill"),
          ),
    [events, lastDistillSeq, foldKeepSeq, expanded],
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
    // Jumping to a match is deliberate navigation away from the tail;
    // without this the follow would yank back down on the next poll.
    if (target) stickRef.current = false;
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
    <section className="chat-surface flex min-h-0 flex-1 flex-col">
      <div className="message-list-wrap relative flex min-h-0 flex-1 flex-col">
        {searchOpen && (
          <div className="search-bar absolute top-2 left-1/2 z-20 -translate-x-1/2 flex items-center gap-1.5 rounded-md border border-border bg-panel-float px-2 py-1.5 shadow-[0_4px_16px_rgba(0,0,0,0.4)] backdrop-blur-[6px]">
            <input
              ref={searchInputRef}
              className="w-[220px] rounded-sm border border-border bg-bg-input px-[9px] py-[5px] text-text [font:inherit] focus:border-accent-user focus:outline-none"
              type="text"
              value={searchQuery}
              onChange={(e) => onSearchQueryChange(e.target.value)}
              onKeyDown={handleSearchKeyDown}
              placeholder="Find in conversation"
              aria-label="Find in conversation"
            />
            <span className="search-count min-w-[44px] text-center text-caption whitespace-nowrap text-text-dim">
              {trimmedQuery === ""
                ? ""
                : matches.length === 0
                  ? "No matches"
                  : `${clampedIdx + 1}/${matches.length}`}
            </span>
            <button
              type="button"
              className="cursor-pointer rounded border border-transparent bg-transparent px-2 py-0.5 text-text-dim [font:inherit] not-disabled:hover:bg-bg-input not-disabled:hover:text-text disabled:cursor-default disabled:opacity-40"
              aria-label="Previous match"
              title="Previous match (Shift+Enter)"
              disabled={matches.length === 0}
              onClick={() => stepMatch(-1)}
            >
              <ChevronUp size={14} />
            </button>
            <button
              type="button"
              className="cursor-pointer rounded border border-transparent bg-transparent px-2 py-0.5 text-text-dim [font:inherit] not-disabled:hover:bg-bg-input not-disabled:hover:text-text disabled:cursor-default disabled:opacity-40"
              aria-label="Next match"
              title="Next match (Enter)"
              disabled={matches.length === 0}
              onClick={() => stepMatch(1)}
            >
              <ChevronDown size={14} />
            </button>
            <button
              type="button"
              className="cursor-pointer rounded border border-transparent bg-transparent px-2 py-0.5 text-text-dim [font:inherit] not-disabled:hover:bg-bg-input not-disabled:hover:text-text disabled:cursor-default disabled:opacity-40"
              aria-label="Close search"
              title="Close (Esc)"
              onClick={onSearchClose}
            >
              <X size={14} />
            </button>
          </div>
        )}
        <div
          className="message-list min-h-[120px] flex-1 overflow-y-auto px-6 py-5"
          ref={listRef}
          onScroll={handleListScroll}
          onWheel={(e) => {
            // Wheel-up over the stream is the leave-the-tail gesture —
            // except over the live ticker's own scroller, where it peeks
            // at earlier tool lines without disengaging the follow.
            if (e.deltaY < 0 && !(e.target instanceof HTMLElement && e.target.closest(".tool-ticker-list"))) {
              stickRef.current = false;
            }
          }}
          onTouchStart={(e) => { touchYRef.current = e.touches[0]?.clientY ?? null; }}
          onTouchMove={(e) => {
            // Finger dragging down reveals older output (scrollTop drops).
            const startY = touchYRef.current;
            const y = e.touches[0]?.clientY;
            if (startY != null && y != null && y - startY > 4) stickRef.current = false;
          }}
          onPointerDown={(e) => {
            // Pointer in the right gutter = scrollbar-thumb drag; until
            // pointerup the scroll events are real user intent.
            const el = listRef.current;
            if (el && e.clientX > el.getBoundingClientRect().right - 16) dragScrollRef.current = true;
          }}
          aria-live="polite"
        >
        {/* Column wrapper: carries the flex layout the scroll container
            itself used to, and gives ResizeObserver one box whose growth
            triggers the re-pin (Item 4 content-visibility size resolution
            included). */}
        <div ref={contentRef} className="flex min-h-full flex-col gap-2.5">
        {fold && (
          <div
            className="fold-chip flex max-w-full items-center gap-2 self-center rounded-[10px] border border-border bg-bg-raised px-3 py-1 text-caption text-text-dim"
            role="note"
          >
            <span className="fold-chip-text" title={fold.notePath}>
              {fold.epoch != null ? `epoch ${fold.epoch} · ` : ""}
              {fold.count} event{fold.count === 1 ? "" : "s"} folded{!expanded && " · click to expand"}
            </span>
            <button
              type="button"
              className="fold-chip-btn cursor-pointer rounded-sm border border-border bg-transparent px-2 py-px text-micro text-text-dim hover:border-accent-user hover:text-accent-user"
              aria-expanded={expanded}
              onClick={() => setExpandedKey(expanded ? null : foldKey)}
            >
              {expanded ? "Collapse" : "Expand"}
            </button>
            {fold.notePath && (
              <button
                type="button"
                className="fold-chip-btn cursor-pointer rounded-sm border border-border bg-transparent px-2 py-px text-micro text-text-dim hover:border-accent-user hover:text-accent-user"
                onClick={() => onOpenNote(fold.notePath!)}
              >
                Open note
              </button>
            )}
          </div>
        )}
        {loading && visibleEvents.length === 0 && <ChatSkeleton />}
        {visibleEvents.length === 0 && !loading && (fold ? (
          <div className="empty-state m-auto max-w-[480px] rounded-lg border border-border bg-bg-raised px-8 py-7 text-center">
            <h2 className="mb-1.5 text-[18px] text-accent-user">Everything here is folded</h2>
            <p className="dim mb-4 text-label">
              All {fold.count} events in this conversation were distilled into
              {fold.noteName ? <> <code>{fold.noteName}</code></> : " a wiki note"}.
              Nothing was lost — the journal keeps every event. Send a message
              to continue in epoch {epoch}, or reopen the folded record.
            </p>
            <div className="example-prompts mt-4 flex flex-wrap justify-center gap-2">
              <button
                type="button"
                className="example-prompt cursor-pointer rounded-[10px] border border-border bg-bg-input px-3 py-[7px] text-label text-text-dim [font:inherit] transition-colors hover:border-stroke-primary hover:text-text"
                onClick={() => setExpandedKey(foldKey)}
              >
                Expand the folded record
              </button>
              {fold.notePath && (
                <button
                  type="button"
                  className="example-prompt cursor-pointer rounded-[10px] border border-border bg-bg-input px-3 py-[7px] text-label text-text-dim [font:inherit] transition-colors hover:border-stroke-primary hover:text-text"
                  onClick={() => onOpenNote(fold.notePath!)}
                >
                  Open the note
                </button>
              )}
            </div>
          </div>
        ) : (
          <div className="empty-state m-auto max-w-[480px] rounded-lg border border-border bg-bg-raised px-8 py-7 text-center">
            <h2 className="mb-1.5 text-[18px] text-accent-user">Welcome to Odo</h2>
            <p className="dim mb-4 text-label">
              Every run is journaled, and its diff lands here for review.
            </p>
            <div className="example-prompts mt-4 flex flex-wrap justify-center gap-2">
              {EXAMPLE_PROMPTS.map((prompt) => (
                <button
                  key={prompt}
                  type="button"
                  className="example-prompt cursor-pointer rounded-[10px] border border-border bg-bg-input px-3 py-[7px] text-label text-text-dim [font:inherit] transition-colors hover:border-stroke-primary hover:text-text"
                  onClick={() => {
                    setDraft(prompt);
                    textareaRef.current?.focus();
                  }}
                >
                  {prompt}
                </button>
              ))}
            </div>
            <div className="shortcuts mt-[18px] flex justify-center gap-4 text-micro text-text-dim">
              <span>⌘K Commands</span>
              <span>⌘↵ Send</span>
              <span>⌘B Sidebar</span>
              <span>⌘F Search</span>
              <span>⌘, Settings</span>
            </div>
          </div>
        ))}
        {panelThinking && (
          <div className="mx-auto flex w-full max-w-[var(--chat-column-width,100%)] flex-col">
          <div className="panel-thinking flex items-center gap-1.5 px-4 py-2 text-label text-text-dim">
            <LoaderCircle size={14} className="spin" />
            <span>Panel consulting models…{panelProgress ? ` · ${Math.min(panelProgress.done, panelProgress.total)}/${panelProgress.total} back` : ""}</span>
          </div>
          {panelProgress?.legs && panelProgress.legs.length > 0 && (
            <ul className="panel-legs mx-4 mb-1 flex flex-col gap-0.5 rounded-md border border-border bg-bg-raised px-3 py-2 text-label">
              {panelProgress.legs.map((leg, idx) => (
                <li key={`${idx}:${leg.model}`} className="panel-leg flex items-center gap-1.5">
                  {leg.done
                    ? leg.error
                      ? <X size={12} className="panel-leg-icon shrink-0 text-[var(--err)]" />
                      : <Check size={12} className="panel-leg-icon shrink-0 text-[var(--ok)]" />
                    : <LoaderCircle size={12} className="panel-leg-icon spin shrink-0 text-text-dim" />}
                  <span className="panel-leg-model min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap">{leg.model}</span>
                  <span className="shrink-0 text-text-dim">{leg.done ? (leg.error ? "error" : "back") : "consulting"}</span>
                </li>
              ))}
            </ul>
          )}
          </div>
        )}
          {runGroups.map((group, groupIdx) => (
            <RunGroupBoundary
              key={`${conversationId ?? "none"}:${group.start?.seq ?? "preamble"}`}
              resetKey={String(conversationId ?? "none") + ":" + String(group.start?.seq ?? "preamble") + ":" + String(group.events.length)}
            >
            <div className="run-group mx-auto flex w-full max-w-[var(--chat-column-width,100%)] flex-col gap-2.5">
              <RunHeader group={group} />
              {(() => {
                const items = runRenderItems(group.events);
                return items.map((item, itemIdx) => {
                  if (item.kind === "bubble") {
                    return <MessageBubble key={item.event.seq} event={item.event} highlight={activeHighlight} onEditUserMessage={handleEditMessage} projectRoot={projectRoot} />;
                  }
                  // ui/message-stream (Hermes parity): a lone call+result
                  // renders inline — a "1 tool call" wrapper costs a click
                  // and carries no summary. ≥2 calls fold into one group
                  // whose summary names what the burst ended with.
                  if (item.calls === 1) {
                    return (
                      <Fragment key={`tools-${item.events[0].seq}`}>
                        {item.events.map((ev) => (
                          <MessageBubble key={ev.seq} event={ev} highlight={activeHighlight} onEditUserMessage={handleEditMessage} projectRoot={projectRoot} />
                        ))}
                      </Fragment>
                    );
                  }
                  // Tool groups default-collapsed; an active ⌘F search
                  // forces them open so jump-to-match still reaches tool
                  // bubbles (the <details> `open` attribute, no JS state).
                  const trailing = agentRunning && groupIdx === runGroups.length - 1 && itemIdx === items.length - 1;
                  const lastCall = [...item.events].reverse().find((e) => e.type === "agent_tool_call");
                  return (
                    <details
                      className="tool-group group my-1"
                      key={`tools-${item.events[0].seq}`}
                      open={searchActive}
                    >
                      <summary className="flex cursor-pointer items-baseline gap-1.5 rounded px-1 py-0.5 text-caption text-text-dim select-none hover:bg-bg-input hover:text-text group-open:mb-1 group-open:text-text">
                        {trailing && <LoaderCircle size={11} className="spin shrink-0 self-center" aria-hidden />}
                        <span className="shrink-0">{item.calls} tool calls</span>
                        {lastCall && (
                          <span className="tool-group-last min-w-0 truncate text-text-dim/70">
                            · {toolCallSnippet(lastCall.payload)}
                          </span>
                        )}
                      </summary>
                      {item.events.map((ev) => (
                        <MessageBubble key={ev.seq} event={ev} highlight={activeHighlight} onEditUserMessage={handleEditMessage} projectRoot={projectRoot} />
                      ))}
                    </details>
                  );
                });
              })()}
            </div>
            </RunGroupBoundary>
          ))}
          {preview && (
            // ui/message-stream: preview + live ticker share the centered
            // chat column with the run-groups; unwrapped they rendered
            // full-bleed beside the padded bubbles.
            <div className="mx-auto flex w-full max-w-[var(--chat-column-width,100%)] flex-col">
              <PreviewBubble preview={preview} projectRoot={projectRoot} />
            </div>
          )}
          <div className="mx-auto flex w-full max-w-[var(--chat-column-width,100%)] flex-col">
            <ToolTicker running={agentRunning} events={events} />
          </div>
        </div>
        </div>
        {newOutput && (
          <button
            type="button"
            className="new-output-pill absolute bottom-2 left-1/2 z-10 -translate-x-1/2 cursor-pointer rounded-[16px] border-none bg-accent-user px-3.5 py-1 text-caption text-white shadow-[0_2px_8px_rgba(0,0,0,0.3)] hover:opacity-90"
            onClick={scrollToBottom}
          >
            <ArrowDown size={12} /> new output
          </button>
        )}
      </div>
      {run && (
        <div className="run-status shrink-0 truncate border-t border-border bg-bg px-4 py-1 font-mono text-caption text-text-dim tabular-nums">
          {`running — ${formatElapsed(Number.isNaN(run.startMs) ? 0 : Date.now() - run.startMs)}`}
          {run.lastTool != null ? ` — tool: ${run.lastTool} (call ${run.calls})` : ""}
        </div>
      )}
      <div
        className={cn(
          "chat-composer flex shrink-0 flex-col gap-1.5 border-t border-stroke-tertiary bg-bg-raised px-4 pt-2.5 pb-2",
          dragOver && "drag-over shadow-[inset_0_0_0_2px_var(--accent-user)]",
        )}
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
        {/* M19 (/loop) V1: the live auto-loop's fold — active and
            suspended loops only; terminal states show as bookkeeping
            bubbles + the system notification. */}
        <LoopChip
          conversationId={conversationId}
          projectRoot={projectRoot}
          loops={loops}
          onChanged={() => onLoopChanged?.()}
          onError={(m) => onLoopError?.(m)}
        />
        <PlanChip
          conversationId={conversationId}
          projectRoot={projectRoot}
          items={todoItems}
          onChanged={() => onTodoChanged?.()}
          onError={(m) => onTodoError?.(m)}
          disabled={sendDisabled || distillLocked}
        />
        {/* Steer queue: the busy run's pinned prompt + queued steers,
            derived from the journal — the panel hides itself when neither
            is live. */}
        <SteerQueue
          activePrompt={activePrompt}
          activeSteerCount={activeSteerCount}
          pending={steerQueue}
          agentRunning={agentRunning}
          distillLocked={distillLocked}
          onDropSteer={onDropSteer}
        />
        {/* W6 (goal queue): the parked-goal FIFO for this conversation,
            derived from the journal — hidden when the queue is empty. */}
        {parkedGoals.length > 0 && (
          <QueueDock
            goals={parkedGoals}
            onResume={onResumeParked}
            onDrop={onDropParked}
            agentRunning={agentRunning}
            distillLocked={distillLocked}
          />
        )}
        {attachments.length > 0 && (
          <div className="attachment-chips flex flex-wrap gap-1.5 px-4">
            {attachments.map((path) => (
              <span
                className="attachment-chip inline-flex items-center gap-1.5 rounded-[12px] border border-border bg-bg-input px-2 py-0.5 font-mono text-caption"
                key={path}
                title={path}
              >
                <code>{basename(path)}</code>
                <button
                  type="button"
                  className="chip-remove cursor-pointer border-none bg-transparent p-0 leading-none text-text-dim [font:inherit] hover:text-err-text"
                  aria-label={`Remove ${basename(path)}`}
                  onClick={() => removeAttachment(path)}
                >
                  <X size={10} />
                </button>
              </span>
            ))}
          </div>
        )}
        <form
          className="chat-input relative flex items-end gap-2 rounded-lg border border-stroke-secondary bg-bg-input py-1.5 pr-1.5 pl-3.5 shadow-soft transition-[border-color] duration-[180ms] ease-[var(--ease-standard)] focus-within:border-[var(--stroke-primary)]"
          onSubmit={handleSubmit}
        >
          {atMenu && (
            <div
              className="at-menu absolute inset-x-0 bottom-full z-10 mb-1.5 max-h-[200px] overflow-y-auto rounded-md border border-stroke-tertiary bg-bg-elevated p-1 shadow-soft"
              role="listbox"
              aria-label={strings.composer.atMenuLabel}
            >
              {atMenu.items.map((item, i) => (
                <button
                  key={`${item.category ?? "item"}:${item.insert}:${i}`}
                  type="button"
                  className={cn(
                    "at-item flex w-full cursor-pointer items-baseline gap-2 rounded-sm border-none bg-transparent px-2.5 py-[7px] text-left text-[length:var(--text-label)] text-text hover:bg-bg-input",
                    i === Math.min(atIndex, atMenu.items.length - 1) && "selected bg-bg-input",
                  )}
                  role="option"
                  aria-selected={i === Math.min(atIndex, atMenu.items.length - 1)}
                  onMouseDown={(e) => {
                    e.preventDefault(); // don't blur the textarea
                    pickAtItem(item);
                  }}
                >
                  {item.category != null && (
                    <span className="at-category min-w-8 font-semibold whitespace-nowrap text-accent-user">{item.category}</span>
                  )}
                  <span className="at-label whitespace-nowrap">{item.label}</span>
                  {item.detail != null && (
                    <span className="at-detail truncate text-caption text-text-dim">{item.detail}</span>
                  )}
                </button>
              ))}
            </div>
          )}
          {slashMenuOpen && slashItems.length > 0 && (
            <div
              className="slash-menu absolute inset-x-0 bottom-full z-10 mb-1.5 max-h-[200px] overflow-y-auto rounded-md border border-stroke-tertiary bg-bg-elevated p-1 shadow-soft"
              role="listbox"
              aria-label="Slash commands"
            >
              {slashItems.map((c, i) => (
                  <button
                    key={c.cmd}
                    type="button"
                    role="option"
                    aria-selected={i === Math.min(slashIndex, slashItems.length - 1)}
                    className={cn(
                      "slash-item flex w-full cursor-pointer items-baseline gap-2 rounded-sm border-none bg-transparent px-2.5 py-[7px] text-left text-[length:var(--text-label)] text-text hover:bg-bg-input",
                      i === Math.min(slashIndex, slashItems.length - 1) && "selected bg-bg-input",
                    )}
                    onMouseDown={(e) => {
                      e.preventDefault(); // don't blur the textarea
                      pickSlash(c.cmd, c.args);
                    }}
                  >
                    <span className="slash-cmd min-w-[70px] font-semibold whitespace-nowrap text-accent-user">{c.cmd}</span>
                    <span className="slash-desc truncate text-caption text-text-dim">{c.desc}</span>
                  </button>
                ))}
            </div>
          )}
          {/* V13 token overlay (Hermes-parity): the accepted slash command
              renders as a highlighted pill behind the textarea's own text.
              The overlay mirrors the textarea's text flow (same font/
              leading/padding/wrap) so the pill covers exactly the command
              span; the rest of the draft is visibility:hidden — laid out,
              never painted. The textarea stacks above (z-10, transparent
              bg), so its text and caret repaint over the pill untouched. */}
          <div className="relative min-w-0 flex-1">
            {slashToken != null && draft.startsWith(slashToken) && (
              <div
                className="slash-token-overlay pointer-events-none absolute inset-0 z-0 overflow-hidden whitespace-pre-wrap break-words px-0 py-2 leading-[1.4]"
                aria-hidden="true"
              >
                <span className="composer-slash-token rounded-[3px] bg-accent-user/20">{slashToken}</span>
                <span className="invisible">{draft.slice(slashToken.length)}</span>
              </div>
            )}
            <textarea
            ref={textareaRef}
            className="relative z-10 min-h-[36px] max-h-[148px] w-full resize-none overflow-y-auto border-none bg-transparent px-0 py-2 text-text [font:inherit] leading-[1.4] focus:outline-none focus-visible:outline-none disabled:opacity-60"
            aria-label={strings.composer.messageInputLabel}
            rows={1}
            // Uncontrolled by design (see the value-sync effect above):
            // controlled `value` let any mid-composition re-render clobber
            // live CJK IME text.
            defaultValue=""
            onChange={handleDraftChange}
            onCompositionStart={() => {
              composingRef.current = true;
            }}
            onCompositionEnd={() => {
              composingRef.current = false;
              // React 19 fires no change events for composition text, so
              // sync the committed string into `draft` here.
              const ta = textareaRef.current;
              if (ta) handleDraftChangeValue(ta.value, ta.selectionStart);
            }}
            onBlur={() => {
              // Delay close so click registration on menu items fires first.
              setTimeout(() => {
                setSlashMenuOpen(false);
                setAtMenu(null);
              }, 150);
            }}
            onKeyDown={handleComposerKeyDown}
            onPaste={handlePaste}
            placeholder={
              dragOver
                ? strings.composer.placeholderDrop
                : agentRunning
                  ? strings.composer.placeholderRunning
                  : strings.composer.placeholderIdle
            }
            // M12: locked during a MANUAL distill only — an auto distill
            // is send-cancelled daemon-side and never blocks typing.
            disabled={sendDisabled || sending || distillLocked}
            autoFocus
          />
          </div>
          {agentRunning && (
            <Button
              type="button"
              variant="danger"
              size="md"
              className="stop-btn min-h-[36px]"
              title={strings.composer.stopTitle}
              onClick={onCancel}
            >
              {strings.composer.stop}
            </Button>
          )}
          {/* W6 (goal queue): arm to queue the submit as a parked goal.
              Slash commands route before the daemon's park branch, so the
              toggle disables on a "/" draft. */}
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className={cn(
              "park-toggle min-h-[36px]",
              parkArmed && "armed bg-[rgba(204,167,66,0.14)] border-[var(--warn)] text-[var(--warn)]",
            )}
            aria-pressed={parkArmed}
            aria-label={strings.composer.parkToggleLabel}
            title={parkArmed ? strings.composer.parkToggleTitleArmed : strings.composer.parkToggleTitleDisarmed}
            disabled={draft.trim().startsWith("/")}
            onClick={() => setParkArmed((v) => !v)}
          >
            <Archive size={14} />
          </Button>
          <Button type="submit" variant="default" size="md" className="send-btn min-h-[36px]" disabled={sendDisabled || sending || distillLocked || !canSend}>
            {parkArmed ? strings.composer.park : agentRunning ? strings.composer.steer : strings.composer.send}
          </Button>
          <ModelPill
            projectRoot={projectRoot}
            currentModel={codingModel}
            onModelChanged={onModelChanged}
          />
        </form>
        <div className="composer-hint px-1 text-right text-micro text-text-dim">
          ⌘↵ send · Shift+↵ newline{agentRunning ? " · Esc stop" : ""}{distillLocked ? " · distilling…" : ""}
        </div>
      </div>
    </section>
  );
}

// (tri-review P1 #4, 2026-08-24) React.memo so the 350 ms poll loop stops
// re-rendering the whole message tree on ticks where nothing here
// changed. This only pays off because App keeps every prop referentially
// stable: events/preview/panelProgress/diffs keep their previous
// reference on content-equal ticks (diff_stable comparators + the
// setPreview/setPanelProgress pattern); onTodoChanged / onTodoError /
// onLoopChanged / onLoopError / onModelChanged / onSearchClose were
// previously inline arrows — a fresh reference every render — and are now
// frozen useCallbacks; autoDistill/autoDistillBlocked are useMemo'd
// finds. handleResumeParked/handleDropParked/handleDropSteer rebuild only
// on conversation/project switches; the rest are primitives.
// NOT done (follow-up, deliberately out of scope): windowing or
// virtualizing the runGroups list itself — when `events` does change, a
// long conversation still re-maps its full history.
export default memo(ChatSurface);
