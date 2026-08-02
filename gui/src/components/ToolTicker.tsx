import { useEffect, useRef } from "react";
import type { OdoEvent } from "../types";

interface Props {
  running: boolean;
  events: OdoEvent[];
}

const MAX_ARGS_LEN = 80;

function briefArgs(args: unknown): string {
  if (args == null) return "";
  const raw = typeof args === "string" ? args : JSON.stringify(args);
  return raw.length > MAX_ARGS_LEN ? raw.slice(0, MAX_ARGS_LEN) + "…" : raw;
}

// Activity indicator shown while the agent runs. Displays each agent_tool_call
// event as it arrives; with none, degrades to a bare spinner.
export default function ToolTicker({ running, events }: Props) {
  const listRef = useRef<HTMLUListElement>(null);
  const toolCalls = events.filter((e) => e.type === "agent_tool_call");

  // Auto-scroll to the newest tool call as the list grows.
  useEffect(() => {
    const el = listRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [toolCalls.length]);

  if (!running && toolCalls.length === 0) return null;

  return (
    <div className="tool-ticker">
      <div className="tool-ticker-status">
        <span className="spinner" aria-hidden="true" />
        <span>Working...</span>
      </div>
      {toolCalls.length > 0 && (
        <ul className="tool-ticker-list" ref={listRef}>
          {toolCalls.map((ev) => (
            <li key={ev.seq}>
              → <span className="tool-name">{ev.payload?.tool ?? "tool"}</span>
              {ev.payload?.args != null && (
                <span className="tool-args">: {briefArgs(ev.payload.args)}</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
