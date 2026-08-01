import type { OdoEvent } from "../types";

interface Props {
  running: boolean;
  events: OdoEvent[];
}

const MAX_RECENT_TOOLS = 8;

// Activity indicator shown while the agent runs. M0's adapter emits no
// per-tool events, so the ticker usually shows a bare spinner; it lists
// tool calls when an adapter provides them.
export default function ToolTicker({ running, events }: Props) {
  if (!running) return null;
  const toolCalls = events.filter((e) => e.type === "agent_tool_call");
  const recent = toolCalls.slice(-MAX_RECENT_TOOLS);

  return (
    <div className="tool-ticker">
      <div className="tool-ticker-status">
        <span className="spinner" aria-hidden="true" />
        <span>Running…</span>
      </div>
      {recent.length > 0 && (
        <ul className="tool-ticker-list">
          {recent.map((ev) => (
            <li key={ev.seq}>
              <code>→ {ev.payload?.name ?? "tool"}</code>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
