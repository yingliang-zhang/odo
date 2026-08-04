import { useRef, useState, type UIEvent, type ReactNode } from "react";
import { errorMessage, reviewDiff, unwrap } from "../api";
import { languageFromPath, tokenize, type Language } from "../highlight";
import type { Diff, ReviewResult } from "../types";

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

// Belt D: one side of one side-by-side row. `sign` retains the +/- marker
// for the gutter; kind only picks the tint class.
interface SplitCell {
  text: string;
  kind: "old" | "new" | "ctx";
  lang: Language | null;
}

interface SplitRow {
  left: SplitCell | null;
  right: SplitCell | null;
}

// Parse the unified diff into aligned old/new column pairs. Removed lines
// go left, added lines right, context (and non-payload lines such as
// `diff --git` / `\\ No newline`) goes to both; `@@` hunk headers are
// skipped. After each contiguous change block the shorter side is padded
// so the columns stay line-aligned for the rest of the file.
function parseSplitRows(lines: string[]): SplitRow[] {
  const rows: SplitRow[] = [];
  let lang: Language | null = null;
  let olds: SplitCell[] = [];
  let news: SplitCell[] = [];
  const flush = () => {
    const n = Math.max(olds.length, news.length);
    for (let i = 0; i < n; i++) {
      rows.push({ left: olds[i] ?? null, right: news[i] ?? null });
    }
    olds = [];
    news = [];
  };
  for (const line of lines) {
    const gitMatch = DIFF_GIT_RE.exec(line);
    if (gitMatch) lang = languageFromPath(gitMatch[1]);
    const newMatch = NEW_FILE_RE.exec(line);
    if (newMatch) lang = languageFromPath(newMatch[1]);

    if (line.startsWith("@@")) {
      flush();
      continue;
    }
    if (line.startsWith("-") && !line.startsWith("---")) {
      olds.push({ text: line.slice(1), kind: "old", lang });
      continue;
    }
    if (line.startsWith("+") && !line.startsWith("+++")) {
      news.push({ text: line.slice(1), kind: "new", lang });
      continue;
    }
    // Context lines carry a leading space; file headers and other metadata
    // are duplicated across both columns so file boundaries stay visible.
    flush();
    const ctx: SplitCell = { text: line, kind: "ctx", lang };
    rows.push({ left: ctx, right: ctx });
  }
  flush();
  return rows;
}

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
  const [reviews, setReviews] = useState<ReviewResult[] | null>(null);
  const [reviewing, setReviewing] = useState(false);
  const [reviewError, setReviewError] = useState<string | null>(null);
  // Belt D: inline (unified) vs split (old | new) rendering.
  const [split, setSplit] = useState(false);
  const oldColRef = useRef<HTMLDivElement>(null);
  const newColRef = useRef<HTMLDivElement>(null);

  // Split columns scroll together: drive the other column's scrollTop from
  // whichever fired. The equality guard ends the one-event feedback loop a
  // programmatic assignment would otherwise start.
  const syncColumn = (from: "old" | "new") => (e: UIEvent<HTMLDivElement>) => {
    const src = e.currentTarget;
    const dst = (from === "old" ? newColRef : oldColRef).current;
    if (dst && dst.scrollTop !== src.scrollTop) dst.scrollTop = src.scrollTop;
  };
  const pending = diff.status === "pending";
  // Any rejecting reviewer flags the whole card so it cannot be missed.
  const hasReject = reviews?.some((r) => r.verdict === "reject") ?? false;

  const runReview = async () => {
    if (reviewing) return;
    setReviewing(true);
    setReviewError(null);
    try {
      const resp = unwrap(await reviewDiff(diff.id));
      setReviews(resp.reviews ?? []);
    } catch (e) {
      setReviewError(errorMessage(e));
    } finally {
      setReviewing(false);
    }
  };

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
  const truncatedNote = truncated
    ? `Diff truncated — showing first ${MAX_LINES} of ${allLines.length} lines.`
    : null;

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

  // Belt D: the split model is built on demand — parsing pays only when
  // the user actually asks for the second layout.
  const splitRows = split ? parseSplitRows(lines) : [];
  if (split && truncatedNote !== null) {
    const ctx: SplitCell = { text: truncatedNote, kind: "ctx", lang: null };
    splitRows.push({ left: ctx, right: ctx });
  }
  const renderSplitCell = (cell: SplitCell | null, key: number): ReactNode => {
    if (cell === null) {
      // Padding row: nbsp so the empty line keeps its height for alignment.
      return (
        <div key={key} className="diff-line diff-line-empty">
          {"\u00a0"}
        </div>
      );
    }
    if (cell.kind === "old") {
      return (
        <div key={key} className="diff-line diff-line-old">
          {renderCode("-", cell.text, cell.lang)}
        </div>
      );
    }
    if (cell.kind === "new") {
      return (
        <div key={key} className="diff-line diff-line-new">
          {renderCode("+", cell.text, cell.lang)}
        </div>
      );
    }
    return (
      <div key={key} className="diff-line diff-line-context">
        {cell.text}
      </div>
    );
  };

  return (
    <section className={`diff-card${hasReject ? " review-rejected" : ""}`}>
      <header className="diff-header">
        <span className="diff-title">Diff #{diff.id}</span>
        {diff.content !== "" && (
          <span className="diff-toggle" role="group" aria-label="Diff view mode">
            <button
              type="button"
              className={split ? "" : "active"}
              aria-pressed={!split}
              onClick={() => setSplit(false)}
            >
              Inline
            </button>
            <button
              type="button"
              className={split ? "active" : ""}
              aria-pressed={split}
              onClick={() => setSplit(true)}
            >
              Split
            </button>
          </span>
        )}
        {pending ? (
          <span className="diff-actions">
            <button
              className="btn-review"
              disabled={acting || reviewing}
              title="Ask the configured review models to grade this diff"
              onClick={() => void runReview()}
            >
              {reviewing ? "Reviewing…" : "Review"}
            </button>
            <button className="btn-accept" disabled={acting || hasReject} onClick={() => void act(onAccept)}>
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
      {reviewError && <div className="review-error">{reviewError}</div>}
      {reviews && (
        <div className="review-results">
          {reviews.length === 0 && (
            <div className="review-empty">No reviewers returned a verdict.</div>
          )}
          {reviews.map((r, i) => (
            <div className="review-item" key={`${r.model}-${i}`}>
              <div className="review-item-head">
                <span className="review-model">{r.model}</span>
                <span className={`verdict-badge verdict-${r.verdict}`}>
                  {r.verdict.replace("_", " ")}
                </span>
              </div>
              {r.comments !== "" && <p className="review-comments">{r.comments}</p>}
            </div>
          ))}
        </div>
      )}
      {diff.content === "" ? (
        <div className="diff-empty">Diff file is empty or unreadable.</div>
      ) : split ? (
        <div className="diff-split">
          <div
            className="diff-split-col"
            ref={oldColRef}
            onScroll={syncColumn("old")}
            aria-label="Old version"
          >
            {splitRows.map((row, i) => renderSplitCell(row.left, i))}
          </div>
          <div
            className="diff-split-col"
            ref={newColRef}
            onScroll={syncColumn("new")}
            aria-label="New version"
          >
            {splitRows.map((row, i) => renderSplitCell(row.right, i))}
          </div>
        </div>
      ) : (
        <pre className="diff-body">
          {rendered}
          {truncatedNote !== null && (
            <div className="diff-truncated">
              {truncatedNote}
            </div>
          )}
        </pre>
      )}
    </section>
  );
}
