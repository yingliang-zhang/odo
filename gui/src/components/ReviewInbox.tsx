// P1a: cross-workstream pending-review inbox. One aggregate panel listing
// every pending diff across the project's active workstreams, grouped by
// workstream. The per-conversation Changes tab stays untouched — this is
// its project-wide counterpart, fed by list_all_pending_diffs (App owns
// the fetch cadence: on tab visibility, then ≥6s-gated through the poll
// loop).
//
// Accept/Reject route through App's handleAccept/handleReject — the same
// accept_diff/reject_diff IPC the Changes tab uses, which already works
// cross-workstream by diffID alone. Rows resolve optimistically in App.

import { useMemo, useState } from "react";
import { ArrowRight, ChevronDown, ChevronRight } from "lucide-react";
import DiffViewer from "./DiffViewer";
import type { DiffInfoEx } from "../types";

interface Props {
  rows: DiffInfoEx[];
  onAccept: (diffId: number) => Promise<void>;
  onReject: (diffId: number) => Promise<void>;
  // M11 P1: review routes to this project's daemon; null = bridge default.
  projectRoot?: string | null;
  agentRunning?: boolean;
  // Jump to the group owner's workstream (sidebar switch).
  onJump?: (workstreamId: number) => void;
}

// Collapsed rows show the first lines of the diff; expand mounts the full
// DiffViewer (no comments handler — line comments route to a conversation,
// and the row's owning conversation is not necessarily in view).
const PREVIEW_LINES = 8;

function preview(content: string): string {
  if (content === "") return "(diff content unavailable)";
  const lines = content.split("\n");
  const head = lines.slice(0, PREVIEW_LINES).join("\n");
  return lines.length > PREVIEW_LINES ? `${head}\n…` : head;
}

// Group rows by workstream, preserving the IPC's order (workstream id, then
// diff id — the same order the sidebar lists workstreams). Keyed by id so a
// workstream rename during the session re-labels one group, never forks it.
interface Group {
  id: number;
  name: string;
  rows: DiffInfoEx[];
}

export default function ReviewInbox({ rows, onAccept, onReject, projectRoot, agentRunning, onJump }: Props) {
  const [expandedId, setExpandedId] = useState<number | null>(null);

  const groups = useMemo(() => {
    const byId = new Map<number, Group>();
    for (const r of rows) {
      const g = byId.get(r.workstream_id);
      if (g) {
        g.rows.push(r);
      } else {
        byId.set(r.workstream_id, { id: r.workstream_id, name: r.workstream_name, rows: [r] });
      }
    }
    return [...byId.values()];
  }, [rows]);

  if (groups.length === 0) {
    return (
      <div className="panel-empty">
        No pending diffs across workstreams — the next run's changes land here.
      </div>
    );
  }

  return (
    <section className="review-inbox" aria-label="Review inbox">
      {groups.map((g) => (
        <div className="inbox-group" key={g.id}>
          <header className="inbox-group-head">
            <span className="inbox-ws-pill">{g.name}</span>
            <span className="panel-tab-badge">{g.rows.length}</span>
            {onJump && (
              <button
                type="button"
                className="inbox-jump"
                aria-label={`Jump to ${g.name}`}
                title={`Switch to ${g.name}`}
                onClick={() => onJump(g.id)}
              >
                <ArrowRight size={12} />
              </button>
            )}
          </header>
          {g.rows.map((d) => {
            const expanded = expandedId === d.id;
            return (
              <div className="inbox-row" key={d.id}>
                <button
                  type="button"
                  className="inbox-row-head"
                  aria-expanded={expanded}
                  onClick={() => setExpandedId(expanded ? null : d.id)}
                >
                  {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                  <span className="inbox-row-title">Diff #{d.id}</span>
                </button>
                {expanded ? (
                  <DiffViewer
                    diff={d}
                    onAccept={onAccept}
                    onReject={onReject}
                    projectRoot={projectRoot}
                    agentRunning={agentRunning}
                  />
                ) : (
                  <>
                    <pre className="inbox-preview">{preview(d.content)}</pre>
                    <div className="inbox-row-actions">
                      <button
                        type="button"
                        className="inbox-reject"
                        aria-label={`Reject diff ${d.id}`}
                        onClick={() => void onReject(d.id)}
                      >
                        Reject
                      </button>
                      <button
                        type="button"
                        className="inbox-accept"
                        aria-label={`Accept diff ${d.id}`}
                        onClick={() => void onAccept(d.id)}
                      >
                        Accept
                      </button>
                    </div>
                  </>
                )}
              </div>
            );
          })}
        </div>
      ))}
    </section>
  );
}
