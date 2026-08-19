import { memo, useState } from "react";
import type { ReactNode } from "react";
import { Check, Copy, X } from "lucide-react";
import { basename } from "../files";
import { loopEventLabel } from "../loop";
import type { OdoEvent, RecallItem } from "../types";
import Markdown, { highlightText } from "./Markdown";
import { Badge } from "./ui/badge";

// Display limits for tool call/result rendering (K3 F3: named consts
// instead of magic numbers scattered in the switch arm).
const INLINE_ARG_MAX = 80;   // chars per arg value in inline mode
const DETAIL_ARG_MAX = 200;   // chars per arg value in collapsible details
const RESULT_CLAMP = 2000;    // chars of tool result shown; full via copy

const REVIEW_LABEL: Record<string, string> = {
  accept: "Accepted",
  reject: "Rejected",
};

// M3 recall: the daemon journals injected memory paths on user_message;
// M4 adds .odo/memory.md between user.md and the note paths; M5 adds
// .odo/pins.md and wiki/index.md between memory.md and the note paths.
// These are fixed markers — everything else is a recalled wiki note.
const USER_MD_PATH = "~/.odo/user.md";
const PROJECT_MEM_PATH = ".odo/memory.md";
const PINS_PATH = ".odo/pins.md";
const INDEX_PATH = "wiki/index.md";

// Tooltip lists the injected sources with wiki paths shortened to
// "wiki/<basename>" (spec §1b); the fixed markers render as their literal
// labels.
function shortRecallPath(path: string): string {
  if (path === USER_MD_PATH) return "user.md";
  if (path === PROJECT_MEM_PATH) return "memory.md";
  if (path === PINS_PATH) return "pins";
  if (path === INDEX_PATH) return "index";
  const marker = "/wiki/";
  const at = path.indexOf(marker);
  return at >= 0 ? path.slice(at + 1) : basename(path);
}

// M6 (spec risk #4): the recall payload changed from string[] to
// RecallItem[]; pre-M6 journal events still carry bare strings. Normalize
// both shapes to RecallItem before rendering.
function normalizeRecall(recall: readonly unknown[]): RecallItem[] {
  const out: RecallItem[] = [];
  for (const item of recall) {
    if (typeof item === "string") {
      out.push({ path: item });
    } else if (item != null && typeof item === "object" && typeof (item as RecallItem).path === "string") {
      out.push(item as RecallItem);
    }
  }
  return out;
}

// M4/M5 (spec §8/§10): the chip label is presence-conditioned — only layers
// actually in `recall` render, e.g. "memory: user.md + memory.md + pins +
// index + 2 note(s)", "memory: memory.md + index + 1 note(s)", "memory:
// 2 note(s)". M6 (§10): when any recalled note has matched_terms, the
// unique terms suffix the label as "(keyword: auth, authentication)".
function recallChipLabel(recall: RecallItem[]): string {
  const paths = new Set(recall.map((it) => it.path));
  const hasUser = paths.has(USER_MD_PATH);
  const hasMem = paths.has(PROJECT_MEM_PATH);
  const hasPins = paths.has(PINS_PATH);
  const hasIndex = paths.has(INDEX_PATH);
  const notes = recall.filter(
    (it) =>
      it.path !== USER_MD_PATH && it.path !== PROJECT_MEM_PATH && it.path !== PINS_PATH && it.path !== INDEX_PATH,
  );
  const parts: string[] = [];
  if (hasUser) parts.push("user.md");
  if (hasMem) parts.push("memory.md");
  if (hasPins) parts.push("pins");
  if (hasIndex) parts.push("index");
  if (notes.length > 0) parts.push(`${notes.length} note(s)`);
  const terms: string[] = [];
  for (const it of notes) {
    for (const term of it.matched_terms ?? []) {
      if (!terms.includes(term)) terms.push(term);
    }
  }
  const suffix = terms.length > 0 ? ` (keyword: ${terms.join(", ")})` : "";
  return `memory: ${parts.join(" + ")}${suffix}`;
}

// M6 (§10): the tooltip lists each injected source on its own line, with
// the note's matched terms in brackets when non-empty:
//   wiki/main-epoch-1.md [auth, authentication]
function recallTooltip(recall: RecallItem[]): string {
  return recall
    .map((it) => {
      const terms = it.matched_terms ?? [];
      return shortRecallPath(it.path) + (terms.length > 0 ? ` [${terms.join(", ")}]` : "");
    })
    .join("\n");
}

// ui/message-stream (Hermes-parity): hover affordances on content bubbles —
// copy the raw message text (top-right) and a small timestamp (in a bottom
// strip the bubble reserves via pb-[26px], so no layout shift on reveal).
// State lives per-rendered event: CopyBubbleButton is a component, not a
// render closure, so the feedback flip survives the memo without prop drill.
function CopyBubbleButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="bubble-copy absolute top-1.5 right-2 z-10 inline-flex cursor-pointer items-center rounded-md border border-border bg-panel-float p-1 text-text-dim opacity-0 transition-opacity hover:text-text group-hover/bubble:opacity-100 focus-visible:opacity-100"
      aria-label={copied ? "Copied" : "Copy message"}
      title={copied ? "Copied" : "Copy message"}
      onClick={() => {
        navigator.clipboard?.writeText(text)?.then(() => {
          setCopied(true);
          setTimeout(() => setCopied(false), 2000);
        })?.catch(() => {});
      }}
    >
      {copied ? <Check size={12} aria-hidden /> : <Copy size={12} aria-hidden />}
    </button>
  );
}

// Hover timestamp: short clock label, absolute date-time on the tooltip.
// Invalid/missing created_at (never expected from the journal) renders nothing.
function BubbleTime({ when, side }: { when: string; side: "left" | "right" }) {
  const ms = Date.parse(when);
  if (Number.isNaN(ms)) return null;
  const d = new Date(ms);
  return (
    <span
      className={`bubble-time pointer-events-none absolute bottom-1.5 ${side === "right" ? "right-2.5" : "left-3.5"} text-micro leading-none text-text-dim opacity-0 transition-opacity select-none group-hover/bubble:opacity-100`}
      title={d.toLocaleString([], { dateStyle: "medium", timeStyle: "short" })}
      aria-hidden
    >
      {d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
    </span>
  );
}

// Renders one journaled event. Payloads come from the daemon verbatim; every
// field is optional in the type, so render defensively.
// Belt B: agent_text renders as markdown; `highlight` wraps occurrences of
// the chat-search query in <mark>. The .bubble-mount wrapper carries the
// data-seq jump anchor without disturbing the flex layout (display: contents).
export default memo(function MessageBubble({ event, highlight, onEditUserMessage, projectRoot }: { event: OdoEvent; highlight?: string; onEditUserMessage?: (text: string) => void; projectRoot?: string | null }) {
  const p = event.payload ?? {};
  // GLM B4: copy feedback for tool results (mirrors CodeBlock pattern).
  const [copied, setCopied] = useState(false);

  let body: ReactNode;
  switch (event.type) {
    case "user_message":
      body = (
        <div className="bubble bubble-user group/bubble relative self-end bg-accent-user text-white flex flex-col rounded-[12px_12px_4px_12px] shadow-[0_1px_2px_rgba(0,0,0,0.25)] max-w-[82%] px-3.5 pt-2.5 pb-[26px] whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <CopyBubbleButton text={p.text ?? ""} />
          <div className="bubble-text">{highlightText(p.text ?? "", highlight, "u")}</div>
          {p.attachments != null && p.attachments.length > 0 && (
            <div className="attachment-chips flex flex-wrap gap-1.5 pt-1.5">
              {p.attachments.map((a) => (
                <span
                  className="attachment-chip inline-flex items-center gap-1.5 rounded-[12px] border border-border bg-bg-input px-2 py-0.5 font-mono text-caption"
                  key={a}
                  title={a}
                >
                  <code>{basename(a)}</code>
                </span>
              ))}
            </div>
          )}
          {p.recall != null && p.recall.length > 0 && (
            <div className="recall-chip inline-block mt-1.5 rounded-lg border border-white/28 bg-white/8 px-2 py-px font-mono text-micro text-white/72" title={recallTooltip(normalizeRecall(p.recall))}>
              {recallChipLabel(normalizeRecall(p.recall))}
            </div>
          )}
          {onEditUserMessage && (p.text ?? "") !== "" && (
            <button
              type="button"
              className="bubble-edit-btn mt-1 self-end rounded border border-border bg-transparent px-1.5 py-px text-[10px] text-text-dim cursor-pointer opacity-0 transition-opacity group-hover/bubble:opacity-100 focus-visible:opacity-100 hover:text-text hover:border-accent"
              aria-label="Edit and resend this message"
              title="Edit and resend"
              onClick={() => onEditUserMessage(p.text ?? "")}
            >
              Edit
            </button>
          )}
          <BubbleTime when={event.created_at} side="right" />
        </div>
      );
      break;

    case "agent_text":
      body = (
        <div className="bubble bubble-agent group/bubble relative w-full max-w-[var(--chat-column-width,100%)] mx-auto bg-bg-raised text-[var(--agent-text)] border border-stroke-secondary rounded-[12px_12px_12px_4px] shadow-[0_1px_2px_rgba(0,0,0,0.18)] px-3.5 pt-2.5 pb-[26px] whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <CopyBubbleButton text={p.text ?? ""} />
          <div className="bubble-text">
            <Markdown content={p.text ?? ""} highlight={highlight} projectRoot={projectRoot} />
          </div>
          <BubbleTime when={event.created_at} side="left" />
        </div>
      );
      break;

    case "agent_thinking":
      body = (
        <div className="bubble bubble-thinking self-start bg-transparent text-text-dim text-caption px-1 py-0.5 w-full max-w-[82%] rounded-lg whitespace-pre-wrap break-words leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <details className="group/thinking">
            <summary className="cursor-pointer text-text-dim italic select-none group-open/thinking:mb-1">Thinking…</summary>
            <div className="bubble-thinking-text whitespace-pre-wrap font-mono text-text-dim opacity-80 py-1 px-2 border-l-2 border-border ml-1 max-h-[300px] overflow-auto">
              <Markdown content={p.text ?? ""} highlight={highlight} projectRoot={projectRoot} />
            </div>
          </details>
        </div>
      );
      break;

    case "agent_tool_call": {
      // Tri-model review Item 3: render tool args as key:value pairs
      // instead of raw JSON.stringify for readability.
      // DSF finding: daemon journals args as a JSON string, not object —
      // parse it first so the key:value path is reachable in production.
      const toolName = p.tool ?? "tool";
      let args: unknown = p.args;
      if (typeof args === "string") {
        try { args = JSON.parse(args); } catch { /* keep raw string */ }
      }
      let argsSummary: ReactNode;
      if (args == null) {
        argsSummary = null;
      } else if (typeof args === "object" && !Array.isArray(args)) {
        // Object args: render key:value pairs
        const entries = Object.entries(args as Record<string, unknown>);
        if (entries.length === 0) {
          argsSummary = null;
        } else if (entries.length <= 3) {
          // Short: inline "key: value · key: value"
          argsSummary = entries.map(([k, v], i) => {
            // K3 F1: String(v) on objects yields [object Object] — use JSON.stringify
            const sv = typeof v === "object" && v !== null ? JSON.stringify(v) : String(v);
            const truncated = sv.slice(0, INLINE_ARG_MAX);
            const ellipsis = sv.length > INLINE_ARG_MAX ? "…" : "";
            return (
              <span key={k}>
                {i > 0 && " · "}
                <span className="tool-arg-key text-tok-keyword">{k}</span>: <span className="tool-arg-val text-tok-string">{highlightText(truncated + ellipsis, highlight, `ta${i}`)}</span>
              </span>
            );
          });
        } else {
          // Long: show count + collapsible
          argsSummary = (
            <details className="tool-args-details inline">
              <summary className="inline cursor-pointer text-text-dim text-micro">{entries.length} args</summary>
              <div className="tool-args-list mt-1 ml-4 flex flex-col gap-0.5">
                {entries.map(([k, v], i) => {
                  const sv = typeof v === "object" && v !== null ? JSON.stringify(v) : String(v);
                  return (
                    <div key={k} className="tool-arg-row flex gap-1.5 text-micro" title={sv}>
                      <span className="tool-arg-key shrink-0 min-w-[60px] text-tok-keyword">{k}</span>
                      <span className="tool-arg-val text-tok-string">{highlightText(sv.slice(0, DETAIL_ARG_MAX), highlight, `ta${i}`)}</span>
                    </div>
                  );
                })}
              </div>
            </details>
          );
        }
      } else {
        // Non-object (string, array): render verbatim, not re-encoded
        const rawStr = typeof args === "string" ? args : JSON.stringify(args);
        argsSummary = <span className="tool-arg-raw text-text-dim">{highlightText(rawStr.slice(0, INLINE_ARG_MAX), highlight, "ta")}</span>;
      }
      body = (
        <div className="bubble bubble-tool self-start bg-transparent text-text-dim font-mono text-caption px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <code className="tool-call-line flex items-baseline gap-0.5 flex-wrap">
            <span className="tool-arrow text-text-dim opacity-60 shrink-0" aria-hidden>→</span>{" "}
            <span className="tool-name text-tok-fn font-medium">{highlightText(toolName, highlight, "tc")}</span>
            {argsSummary != null && <span className="tool-args text-text-dim"> {argsSummary}</span>}
          </code>
        </div>
      );
      break;
    }

    case "agent_tool_result": {
      const toolName = p.tool ?? "result";
      const resultText = typeof p.result === "string" ? p.result : JSON.stringify(p.result ?? "", null, 2);
      // Item 8: clamp display to 2000 chars; full text via copy button.
      const resultBytes = resultText.length;
      const clamped = resultBytes > RESULT_CLAMP
        ? resultText.slice(0, RESULT_CLAMP) + `\n… (${(resultBytes - RESULT_CLAMP).toLocaleString()} more chars)`
        : resultText;
      body = (
        <div className="bubble bubble-tool self-start bg-transparent text-text-dim font-mono text-caption px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <details>
            <summary>
              <code>
                <span className="tool-arrow text-text-dim opacity-60 shrink-0" aria-hidden>←</span>{" "}
                {highlightText(toolName, highlight, "tr")}
                {resultBytes > 0 && <span className="tool-result-size text-text-dim text-[10px]"> · {(resultBytes > 1024 ? `${(resultBytes / 1024).toFixed(1)} KB` : `${resultBytes} B`)}</span>}
              </code>
            </summary>
            <pre className="mt-1.5 max-h-[240px] overflow-auto bg-bg-raised border border-border rounded-sm p-2">{highlightText(clamped, highlight, "tb")}</pre>
            {resultBytes > RESULT_CLAMP && (
              <button
                type="button"
                className="tool-result-copy mt-1 bg-transparent border border-border rounded text-text-dim text-[10px] px-2 py-0.5 cursor-pointer hover:text-text hover:border-accent"
                title="Copy full result"
                onClick={() => {
                  navigator.clipboard?.writeText(resultText)?.then(() => {
                    setCopied(true);
                    setTimeout(() => setCopied(false), 2000);
                  })?.catch(() => {});
                }}
              >
                {copied ? <Check size={10} aria-hidden /> : `Copy full (${resultBytes.toLocaleString()} chars)`}
              </button>
            )}
          </details>
        </div>
      );
      break;
    }

    case "agent_done":
      body = (
        <div className="bubble bubble-done self-start bg-transparent text-ok-text border border-ok max-w-[82%] px-3.5 py-2.5 rounded-lg whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <span className="bubble-icon font-bold mr-1"><Check size={14} /></span> {highlightText(p.summary ?? "Agent finished", highlight, "d")}
        </div>
      );
      break;

    case "agent_error":
      body = (
        <div className="bubble bubble-error self-start bg-err-surface border border-err text-err-surface-text max-w-[82%] px-3.5 py-2.5 rounded-lg whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <span className="bubble-icon font-bold mr-1"><X size={14} /></span> {highlightText(p.error ?? "Agent failed", highlight, "e")}
        </div>
      );
      break;

    case "review_action":
      // M12 (D-todo): plan merges are journaled bookkeeping, not chat —
      // the composer "Plan" chip is their surface; a per-merge badge would
      // spam one bubble per agent plan update.
      if (p.action === "todo_merge") {
        return null;
      }
      // Phase 2 stage breadcrumbs are PipelineChip liveness, not chat —
      // two rows per landing attempt would spam the transcript (todo_merge
      // posture). LedgerPanel keeps the verbatim history (lock rule 9).
      if (p.action === "auto_land_started") {
        return null;
      }
      // The memory distiller journals its epoch bump as a review_action with
      // action "distill" and no diff (ADR-0002); render it as a memory event.
      if (p.action === "distill") {
        body = (
          <div className="bubble bubble-review self-center bg-transparent px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
            <Badge variant="other" className="badge badge-other" title={p.wiki_path ?? "distilled to wiki"}>
              Distilled · epoch {p.epoch ?? "?"}
            </Badge>
          </div>
        );
      } else if (p.action === "curate") {
        // M5: the curator journals its pass the same way (ADR-0002) — render
        // it as a memory event, not the diff-style "curate diff #?".
        body = (
          <div className="bubble bubble-review self-center bg-transparent px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
            <Badge variant="other" className="badge badge-other" title="curator rewrote wiki topics + index.md">
              Curated {p.topics ?? "?"} topics
            </Badge>
          </div>
        );
      } else if (p.action === "run_prompt" && p.origin === "parked_goal") {
        // W6 (goal queue): a parked goal left the queue. Automatic dequeues
        // carry the pipeline's actor — the run they start streams visibly,
        // so the receipt row renders nothing (same posture as todo_merge);
        // a human resume (no actor) leaves a one-line receipt badge.
        if (p.actor != null && p.actor !== "") {
          return null;
        }
        body = (
          <div className="bubble bubble-review self-center bg-transparent px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
            <Badge variant="other" className="badge badge-other" title="a parked goal was resumed into a run">
              resumed parked goal
            </Badge>
          </div>
        );
      } else if (p.action === "parked_goal_dropped") {
        // W6: the human dropped one queued goal; the drop is journaled.
        body = (
          <div className="bubble bubble-review self-center bg-transparent px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
            <Badge variant="other" className="badge badge-other" title="a parked goal was dropped from the queue">
              dropped parked goal
            </Badge>
          </div>
        );
      } else {
        body = (
          <div className="bubble bubble-review self-center bg-transparent px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
            <Badge
              variant={REVIEW_LABEL[p.action ?? ""] ? (p.action as "accept" | "reject") : "other"}
              className={`badge badge-${REVIEW_LABEL[p.action ?? ""] ? p.action : "other"}`}
            >
              {REVIEW_LABEL[p.action ?? ""] ?? p.action ?? "reviewed"} diff #{p.diff_id ?? "?"}
            </Badge>
          </div>
        );
      }
      break;

    case "memory_update":
      // M4–M6: the daemon journals memory layer changes (memory, user,
      // learner, curator, index, pins, note, ledger). Render as a subtle
      // system-event badge instead of raw JSON (K3 review P1 fix).
      body = (
        <div className="bubble bubble-review self-center bg-transparent px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <Badge variant="other" className="badge badge-other" title={p.detail ?? ""}>
            memory · {p.layer ?? "unknown"}{p.cause ? ` · ${p.cause}` : ""}
          </Badge>
        </div>
      );
      break;

    case "loop_event": {
      // M19 (/loop) V1: the discriminated loop journal's ONE bubble case —
      // a compact bookkeeping badge (kind + key fields via loopEventLabel),
      // never agent text. Like the other system rows it survives the
      // distill fold filter untouched (only distill markers collapse).
      const { label, title } = loopEventLabel(event);
      body = (
        <div className="bubble bubble-review loop-event-bubble self-center bg-transparent px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <Badge variant="other" className="badge badge-other" title={title}>
            {label}
          </Badge>
        </div>
      );
      break;
    }

    case "preview_captured":
      // /preview's capture receipt: subtle system-event badge (the
      // memory_update pattern) — the captured PNG itself renders as the
      // user_message's attachment chip above it.
      body = (
        <div className="bubble bubble-review self-center bg-transparent px-1 py-0.5 max-w-[82%] rounded-lg whitespace-pre-wrap break-words text-body leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <Badge variant="other" className="badge badge-other" title={`${p.url ?? ""}\nsha256 ${p.sha256 ?? ""}`}>
            preview · {p.url ?? "?"}{p.bytes != null ? ` · ${(p.bytes / 1024).toFixed(1)} KB` : ""}{p.wait_ms != null ? ` · ${p.wait_ms} ms` : ""}
          </Badge>
        </div>
      );
      break;

    default:
      body = (
        <div className="bubble bubble-unknown self-start bg-bg-raised text-text-dim font-mono text-caption max-w-[82%] px-3.5 py-2.5 rounded-lg whitespace-pre-wrap break-words leading-[1.6] animate-[bubble-in_0.18s_var(--ease-out)]">
          <code>
            {event.type}: {JSON.stringify(p)}
          </code>
        </div>
      );
  }
  return (
    <div className="bubble-mount" data-seq={event.seq}>
      {body}
    </div>
  );
});
