import { useState, type ReactNode } from "react";
import { languageFromPath, tokenize, type Language } from "../highlight";
import type { Diff } from "../types";

interface Props {
  diff: Diff;
  onAccept: (diffId: number) => Promise<void>;
  onReject: (diffId: number) => Promise<void>;
}

// Rendering ceiling: diffs are normally well under 500 lines; past this we
// truncate rather than strain the DOM with unbounded spans.
const MAX_LINES = 1000;

function lineClass(line: string): string {
  if (line.startsWith("+") && !line.startsWith("+++")) return "diff-line diff-add";
  if (line.startsWith("-") && !line.startsWith("---")) return "diff-line diff-del";
  if (line.startsWith("@@")) return "diff-line diff-hunk";
  return "diff-line";
}

const DIFF_GIT_RE = /^diff --git a\/\S+ b\/(\S+)/;
const NEW_FILE_RE = /^\+\+\+ b\/(\S+)/;

// Highlight the payload of an added/removed line; the +/- marker stays a
// plain span so it never picks up a token color.
function renderCode(prefix: string, code: string, lang: Language | null): ReactNode {
  const tokens = tokenize(code, lang);
  return (
    <>
      <span className="diff-gutter">{prefix}</span>
      {tokens.map((t, i) =>
        t.cls === null ? (
          t.text
        ) : (
          <span key={i} className={t.cls}>
            {t.text}
          </span>
        ),
      )}
    </>
  );
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

  const allLines = diff.content.split("\n");
  const truncated = allLines.length > MAX_LINES;
  const lines = truncated ? allLines.slice(0, MAX_LINES) : allLines;

  // Walk the lines once, tracking the language of the file currently being
  // diffed (multi-file diffs switch at each `diff --git` / `+++ b/` header).
  const rendered: ReactNode[] = [];
  let lang: Language | null = null;
  lines.forEach((line, i) => {
    const gitMatch = DIFF_GIT_RE.exec(line);
    if (gitMatch) lang = languageFromPath(gitMatch[1]);
    const newMatch = NEW_FILE_RE.exec(line);
    if (newMatch) lang = languageFromPath(newMatch[1]);

    const cls = lineClass(line);
    const isCode = cls.endsWith("diff-add") || cls.endsWith("diff-del");
    rendered.push(
      <div key={i} className={cls}>
        {isCode ? renderCode(line.slice(0, 1), line.slice(1), lang) : line}
      </div>,
    );
  });

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
          {rendered}
          {truncated && (
            <div className="diff-truncated">
              Diff truncated — showing first {MAX_LINES} of {allLines.length} lines.
            </div>
          )}
        </pre>
      )}
    </section>
  );
}
