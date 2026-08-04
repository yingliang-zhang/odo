import type { ReactNode } from "react";
import { basename } from "../files";
import type { OdoEvent, RecallItem } from "../types";
import Markdown, { highlightText } from "./Markdown";

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
export default function MessageBubble({ event, highlight }: { event: OdoEvent; highlight?: string }) {
  const p = event.payload ?? {};

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

    case "agent_tool_call":
      body = (
        <div className="bubble bubble-tool">
          <code>
            → {highlightText(p.tool ?? "tool", highlight, "tc")}{" "}
            {highlightText(p.args != null ? JSON.stringify(p.args) : "", highlight, "ta")}
          </code>
        </div>
      );
      break;

    case "agent_tool_result":
      body = (
        <div className="bubble bubble-tool">
          <details>
            <summary>
              <code>← {highlightText(p.tool ?? "result", highlight, "tr")}</code>
            </summary>
            <pre>
              {highlightText(
                typeof p.result === "string" ? p.result : JSON.stringify(p.result, null, 2),
                highlight,
                "tb",
              )}
            </pre>
          </details>
        </div>
      );
      break;

    case "agent_done":
      body = (
        <div className="bubble bubble-done">
          <span className="bubble-icon">✓</span> {highlightText(p.summary ?? "Agent finished", highlight, "d")}
        </div>
      );
      break;

    case "agent_error":
      body = (
        <div className="bubble bubble-error">
          <span className="bubble-icon">✗</span> {highlightText(p.error ?? "Agent failed", highlight, "e")}
        </div>
      );
      break;

    case "review_action":
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
}
