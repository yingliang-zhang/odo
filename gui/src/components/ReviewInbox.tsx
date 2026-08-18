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
import { Button } from "./ui/button";
import { cn } from "../lib/utils";
import type { DiffInfoEx } from "../types";

interface Props {
  rows: DiffInfoEx[];
  onAccept: (diffId: number, commitMessage?: string) => Promise<void>;
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
    <section className="review-inbox p-2" aria-label="Review inbox">
      {groups.map((g) => (
        <div className="inbox-group mb-3" key={g.id}>
          <header
            className={cn(
              "inbox-group-head flex items-center gap-2 px-1 pt-1 pb-1.5",
              "border-b border-[var(--border)] mb-1.5",
            )}
          >
            <span
              className={cn(
                "inbox-ws-pill text-caption font-semibold text-[var(--text)]",
                "bg-[var(--bg-raised)] border border-[var(--border)] rounded-sm",
                "px-2 py-0.5 max-w-[180px] overflow-hidden text-ellipsis whitespace-nowrap",
              )}
            >
              {g.name}
            </span>
            <span
              className={cn(
                "panel-tab-badge inline-block min-w-4 h-4 leading-4 text-center",
                "rounded-md bg-[var(--accent-user)] text-[var(--bg)]",
                "text-[10px] font-bold px-1",
              )}
            >
              {g.rows.length}
            </span>
            {onJump && (
              <button
                type="button"
                className={cn(
                  "inbox-jump ml-auto bg-transparent border-none text-[var(--text-dim)]",
                  "cursor-pointer px-1.5 py-0.5 rounded-sm leading-none",
                  "hover:text-[var(--text)] hover:bg-[var(--bg-input)]",
                )}
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
              <div
                className={cn(
                  "inbox-row flex flex-wrap border border-[var(--border)] rounded-md",
                  "bg-[var(--bg-raised)] mb-1.5 overflow-hidden",
                  // Expanded DiffViewer must fill the row width so the diff
                  // body scrolls instead of clipping (K3 final review S2).
                  "[&>.diff-card]:w-full [&>.diff-card]:min-w-0",
                )}
                key={d.id}
              >
                <button
                  type="button"
                  className={cn(
                    "inbox-row-head flex items-center gap-1.5 w-full px-2.5 py-1.5",
                    "bg-transparent border-none text-[var(--text)] cursor-pointer",
                    "text-caption text-left hover:bg-[var(--bg-hover)]",
                  )}
                  aria-expanded={expanded}
                  onClick={() => setExpandedId(expanded ? null : d.id)}
                >
                  {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                  <span className="inbox-row-title font-semibold">Diff #{d.id}</span>
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
                    <pre
                      className={cn(
                        "inbox-preview basis-full m-0 px-2.5 py-1.5 max-h-[140px]",
                        "overflow-auto bg-[var(--bg)] border-t border-[var(--border)]",
                        "font-mono text-micro text-[var(--text-dim)]",
                        "whitespace-pre-wrap wrap-anywhere",
                      )}
                    >
                      {preview(d.content)}
                    </pre>
                    <div
                      className={cn(
                        "inbox-row-actions basis-full flex justify-end gap-1.5",
                        "px-2.5 py-1.5 border-t border-[var(--border)]",
                      )}
                    >
                      <Button
                        type="button"
                        variant="danger"
                        size="sm"
                        className="inbox-reject"
                        aria-label={`Reject diff ${d.id}`}
                        onClick={() => void onReject(d.id)}
                      >
                        Reject
                      </Button>
                      <Button
                        type="button"
                        variant="default"
                        size="sm"
                        className="inbox-accept"
                        aria-label={`Accept diff ${d.id}`}
                        onClick={() => void onAccept(d.id)}
                      >
                        Accept
                      </Button>
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
