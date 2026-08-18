import { useEffect, useRef } from "react";
import type { OdoEvent } from "../types";

interface Props {
  running: boolean;
  events: OdoEvent[];
}

const MAX_ARGS_LEN = 80;

// GLM B5: use the same key:value summary as MessageBubble so the live
// ticker and the journaled bubble show the same format. Parse string
// args (daemon journals as JSON string) to match the bubble path.
function briefArgs(args: unknown): string {
  if (args == null) return "";
  let parsed = args;
  if (typeof parsed === "string") {
    try { parsed = JSON.parse(parsed); } catch { /* keep raw */ }
  }
  if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
    const entries = Object.entries(parsed as Record<string, unknown>);
    if (entries.length === 0) return "";
    const parts = entries.slice(0, 3).map(([k, v]) => {
      const sv = typeof v === "object" && v !== null ? JSON.stringify(v) : String(v);
      return `${k}: ${sv.slice(0, MAX_ARGS_LEN)}`;
    });
    const suffix = entries.length > 3 ? ` +${entries.length - 3}` : "";
    return parts.join(" · ") + suffix;
  }
  const raw = typeof parsed === "string" ? parsed : JSON.stringify(parsed);
  return raw.length > MAX_ARGS_LEN ? raw.slice(0, MAX_ARGS_LEN) + "…" : raw;
}

// Activity indicator shown only while the agent runs. Displays each
// agent_tool_call event as it arrives; with none, degrades to a bare
// spinner. Historical calls are the run group header's job (collapsed
// tool-group details) — rendering them here at idle duplicated both.
//
// P1-P4: styles migrated to Tailwind utilities; class names survive as
// inert identity markers. The ring spinner is the old `.spinner` geometry
// (12px, 2px border, bright top edge) animated by the shared `.spin`
// rule (its CSS stays for StatusBar).
export default function ToolTicker({ running, events }: Props) {
  const listRef = useRef<HTMLUListElement>(null);
  const toolCalls = events.filter((e) => e.type === "agent_tool_call");

  // Auto-scroll to the newest tool call as the list grows.
  useEffect(() => {
    const el = listRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [toolCalls.length]);

  if (!running) return null;

  return (
    <div className="tool-ticker self-start text-[var(--text-dim)] p-1">
      <div className="tool-ticker-status flex items-center gap-2">
        <span
          className="spinner spin h-3 w-3 rounded-full border-2 border-[var(--border)] border-t-[var(--text)]"
          aria-hidden="true"
        />
        <span>Working...</span>
      </div>
      {toolCalls.length > 0 && (
        <ul
          className="tool-ticker-list list-none m-0 mt-1.5 ml-6 p-0 font-[var(--mono)] text-[12px] flex flex-col gap-0.5 max-h-[200px] overflow-y-auto"
          ref={listRef}
        >
          {toolCalls.map((ev) => (
            <li key={ev.seq}>
              <span className="tool-arrow text-[var(--text-dim)] opacity-60" aria-hidden>→</span>{" "}
              <span className="tool-name font-bold text-[var(--text)]">{ev.payload?.tool ?? "tool"}</span>
              {ev.payload?.args != null && (
                <span className="tool-args text-[var(--text-dim)]"> {briefArgs(ev.payload.args)}</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
