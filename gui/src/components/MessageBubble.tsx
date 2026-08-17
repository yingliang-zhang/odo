import { memo, useState } from "react";
import type { ReactNode } from "react";
import { Check, X } from "lucide-react";
import { basename } from "../files";
import type { OdoEvent, RecallItem } from "../types";
import Markdown, { highlightText } from "./Markdown";

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

// Renders one journaled event. Payloads come from the daemon verbatim; every
// field is optional in the type, so render defensively.
// Belt B: agent_text renders as markdown; `highlight` wraps occurrences of
// the chat-search query in <mark>. The .bubble-mount wrapper carries the
// data-seq jump anchor without disturbing the flex layout (display: contents).
export default memo(function MessageBubble({ event, highlight, onEditUserMessage }: { event: OdoEvent; highlight?: string; onEditUserMessage?: (text: string) => void }) {
  const p = event.payload ?? {};
  // GLM B4: copy feedback for tool results (mirrors CodeBlock pattern).
  const [copied, setCopied] = useState(false);

  let body: ReactNode;
  switch (event.type) {
    case "user_message":
      body = (
        <div className="bubble bubble-user">
          <div className="bubble-text">{highlightText(p.text ?? "", highlight, "u")}</div>
          {p.attachments != null && p.attachments.length > 0 && (
            <div className="attachment-chips">
              {p.attachments.map((a) => (
                <span className="attachment-chip" key={a} title={a}>
                  <code>{basename(a)}</code>
                </span>
              ))}
            </div>
          )}
          {p.recall != null && p.recall.length > 0 && (
            <div className="recall-chip" title={recallTooltip(normalizeRecall(p.recall))}>
              {recallChipLabel(normalizeRecall(p.recall))}
            </div>
          )}
          {onEditUserMessage && (p.text ?? "") !== "" && (
            <button
              type="button"
              className="bubble-edit-btn"
              aria-label="Edit and resend this message"
              title="Edit and resend"
              onClick={() => onEditUserMessage(p.text ?? "")}
            >
              Edit
            </button>
          )}
        </div>
      );
      break;

    case "agent_text":
      body = (
        <div className="bubble bubble-agent">
          <div className="bubble-text">
            <Markdown content={p.text ?? ""} highlight={highlight} />
          </div>
        </div>
      );
      break;

    case "agent_thinking":
      body = (
        <div className="bubble bubble-thinking">
          <details>
            <summary>Thinking…</summary>
            <div className="bubble-thinking-text">
              <Markdown content={p.text ?? ""} highlight={highlight} />
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
                <span className="tool-arg-key">{k}</span>: <span className="tool-arg-val">{highlightText(truncated + ellipsis, highlight, `ta${i}`)}</span>
              </span>
            );
          });
        } else {
          // Long: show count + collapsible
          argsSummary = (
            <details className="tool-args-details">
              <summary>{entries.length} args</summary>
              <div className="tool-args-list">
                {entries.map(([k, v], i) => {
                  const sv = typeof v === "object" && v !== null ? JSON.stringify(v) : String(v);
                  return (
                    <div key={k} className="tool-arg-row" title={sv}>
                      <span className="tool-arg-key">{k}</span>
                      <span className="tool-arg-val">{highlightText(sv.slice(0, DETAIL_ARG_MAX), highlight, `ta${i}`)}</span>
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
        argsSummary = <span className="tool-arg-raw">{highlightText(rawStr.slice(0, INLINE_ARG_MAX), highlight, "ta")}</span>;
      }
      body = (
        <div className="bubble bubble-tool">
          <code className="tool-call-line">
            <span className="tool-arrow" aria-hidden>→</span>{" "}
            <span className="tool-name">{highlightText(toolName, highlight, "tc")}</span>
            {argsSummary != null && <span className="tool-args"> {argsSummary}</span>}
          </code>
        </div>
      );
      break;
    }

    case "agent_tool_result": {
      const toolName = p.tool ?? "result";
      const resultText = typeof p.result === "string" ? p.result : JSON.stringify(p.result, null, 2);
      // Item 8: clamp display to 2000 chars; full text via copy button.
      const resultBytes = resultText.length;
      const clamped = resultBytes > RESULT_CLAMP
        ? resultText.slice(0, RESULT_CLAMP) + `\n… (${(resultBytes - RESULT_CLAMP).toLocaleString()} more chars)`
        : resultText;
      body = (
        <div className="bubble bubble-tool">
          <details>
            <summary>
              <code>
                <span className="tool-arrow" aria-hidden>←</span>{" "}
                {highlightText(toolName, highlight, "tr")}
                {resultBytes > 0 && <span className="tool-result-size"> · {(resultBytes > 1024 ? `${(resultBytes / 1024).toFixed(1)} KB` : `${resultBytes} B`)}</span>}
              </code>
            </summary>
            <pre>{highlightText(clamped, highlight, "tb")}</pre>
            {resultBytes > RESULT_CLAMP && (
              <button
                type="button"
                className="tool-result-copy"
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
        <div className="bubble bubble-done">
          <span className="bubble-icon"><Check size={14} /></span> {highlightText(p.summary ?? "Agent finished", highlight, "d")}
        </div>
      );
      break;

    case "agent_error":
      body = (
        <div className="bubble bubble-error">
          <span className="bubble-icon"><X size={14} /></span> {highlightText(p.error ?? "Agent failed", highlight, "e")}
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
      // The memory distiller journals its epoch bump as a review_action with
      // action "distill" and no diff (ADR-0002); render it as a memory event.
      if (p.action === "distill") {
        body = (
          <div className="bubble bubble-review">
            <span className="badge badge-other" title={p.wiki_path ?? "distilled to wiki"}>
              Distilled · epoch {p.epoch ?? "?"}
            </span>
          </div>
        );
      } else if (p.action === "curate") {
        // M5: the curator journals its pass the same way (ADR-0002) — render
        // it as a memory event, not the diff-style "curate diff #?".
        body = (
          <div className="bubble bubble-review">
            <span className="badge badge-other" title="curator rewrote wiki topics + index.md">
              Curated {p.topics ?? "?"} topics
            </span>
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
          <div className="bubble bubble-review">
            <span className="badge badge-other" title="a parked goal was resumed into a run">
              resumed parked goal
            </span>
          </div>
        );
      } else if (p.action === "parked_goal_dropped") {
        // W6: the human dropped one queued goal; the drop is journaled.
        body = (
          <div className="bubble bubble-review">
            <span className="badge badge-other" title="a parked goal was dropped from the queue">
              dropped parked goal
            </span>
          </div>
        );
      } else {
        body = (
          <div className="bubble bubble-review">
            <span className={`badge badge-${REVIEW_LABEL[p.action ?? ""] ? p.action : "other"}`}>
              {REVIEW_LABEL[p.action ?? ""] ?? p.action ?? "reviewed"} diff #{p.diff_id ?? "?"}
            </span>
          </div>
        );
      }
      break;

    case "memory_update":
      // M4–M6: the daemon journals memory layer changes (memory, user,
      // learner, curator, index, pins, note, ledger). Render as a subtle
      // system-event badge instead of raw JSON (K3 review P1 fix).
      body = (
        <div className="bubble bubble-review">
          <span className="badge badge-other" title={p.detail ?? ""}>
            memory · {p.layer ?? "unknown"}{p.cause ? ` · ${p.cause}` : ""}
          </span>
        </div>
      );
      break;

    default:
      body = (
        <div className="bubble bubble-unknown">
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
