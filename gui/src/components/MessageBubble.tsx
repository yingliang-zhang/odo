import { basename } from "../files";
import type { OdoEvent } from "../types";

const REVIEW_LABEL: Record<string, string> = {
  accept: "Accepted",
  reject: "Rejected",
};

// Renders one journaled event. Payloads come from the daemon verbatim; every
// field is optional in the type, so render defensively.
export default function MessageBubble({ event }: { event: OdoEvent }) {
  const p = event.payload ?? {};

  switch (event.type) {
    case "user_message":
      return (
        <div className="bubble bubble-user">
          <div className="bubble-text">{p.text ?? ""}</div>
          {p.attachments != null && p.attachments.length > 0 && (
            <div className="attachment-chips">
              {p.attachments.map((a) => (
                <span className="attachment-chip" key={a} title={a}>
                  <code>{basename(a)}</code>
                </span>
              ))}
            </div>
          )}
        </div>
      );

    case "agent_text":
      return (
        <div className="bubble bubble-agent">
          <div className="bubble-text">{p.text ?? ""}</div>
        </div>
      );

    case "agent_tool_call":
      return (
        <div className="bubble bubble-tool">
          <code>
            → {p.tool ?? "tool"} {p.args != null ? JSON.stringify(p.args) : ""}
          </code>
        </div>
      );

    case "agent_tool_result":
      return (
        <div className="bubble bubble-tool">
          <details>
            <summary>
              <code>← {p.tool ?? "result"}</code>
            </summary>
            <pre>{typeof p.result === "string" ? p.result : JSON.stringify(p.result, null, 2)}</pre>
          </details>
        </div>
      );

    case "agent_done":
      return (
        <div className="bubble bubble-done">
          <span className="bubble-icon">✓</span> {p.summary ?? "Agent finished"}
        </div>
      );

    case "agent_error":
      return (
        <div className="bubble bubble-error">
          <span className="bubble-icon">✗</span> {p.error ?? "Agent failed"}
        </div>
      );

    case "review_action":
      // The memory distiller journals its epoch bump as a review_action with
      // action "distill" and no diff (ADR-0002); render it as a memory event.
      if (p.action === "distill") {
        return (
          <div className="bubble bubble-review">
            <span className="badge badge-other" title={p.wiki_path ?? "distilled to wiki"}>
              Distilled · epoch {p.epoch ?? "?"}
            </span>
          </div>
        );
      }
      return (
        <div className="bubble bubble-review">
          <span className={`badge badge-${REVIEW_LABEL[p.action ?? ""] ? p.action : "other"}`}>
            {REVIEW_LABEL[p.action ?? ""] ?? p.action ?? "reviewed"} diff #{p.diff_id ?? "?"}
          </span>
        </div>
      );

    default:
      return (
        <div className="bubble bubble-unknown">
          <code>
            {event.type}: {JSON.stringify(p)}
          </code>
        </div>
      );
  }
}
