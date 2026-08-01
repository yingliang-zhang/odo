import { useState } from "react";
import type { Diff } from "../types";

interface Props {
  diff: Diff;
  onAccept: (diffId: number) => Promise<void>;
  onReject: (diffId: number) => Promise<void>;
}

function lineClass(line: string): string {
  if (line.startsWith("+") && !line.startsWith("+++")) return "diff-line diff-add";
  if (line.startsWith("-") && !line.startsWith("---")) return "diff-line diff-del";
  if (line.startsWith("@@")) return "diff-line diff-hunk";
  return "diff-line";
}

// Presents one diff from the daemon with Accept/Reject review actions.
// Only a `pending` diff is actionable; afterwards this becomes a record card.
export default function DiffViewer({ diff, onAccept, onReject }: Props) {
  const [acting, setActing] = useState(false);
  const pending = diff.status === "pending";

  const act = async (fn: (id: number) => Promise<void>) => {
    setActing(true);
    try {
      await fn(diff.id);
    } finally {
      setActing(false);
    }
  };

  return (
    <section className="diff-card">
      <header className="diff-header">
        <span className="diff-title">Diff #{diff.id}</span>
        {pending ? (
          <span className="diff-actions">
            <button className="btn-accept" disabled={acting} onClick={() => void act(onAccept)}>
              Accept
            </button>
            <button className="btn-reject" disabled={acting} onClick={() => void act(onReject)}>
              Reject
            </button>
          </span>
        ) : (
          <span className={`badge badge-${diff.status === "accepted" ? "accept" : "reject"}`}>
            {diff.status === "accepted" ? "Applied" : "Rejected"}
          </span>
        )}
      </header>
      {diff.content === "" ? (
        <div className="diff-empty">Diff file is empty or unreadable.</div>
      ) : (
        <pre className="diff-body">
          {diff.content.split("\n").map((line, i) => (
            <div key={i} className={lineClass(line)}>
              {line}
            </div>
          ))}
        </pre>
      )}
    </section>
  );
}
