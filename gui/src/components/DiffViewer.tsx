import { useEffect, useMemo, useRef, useState, type UIEvent, type ReactNode } from "react";
import { autonomyStatus, errorMessage, reviewDiff, unwrap } from "../api";
import { languageFromPath, tokenize, type Language } from "../highlight";
import type { AutonomyReport, Diff, ReviewResult } from "../types";

interface Props {
  diff: Diff;
  // M11 P1: review routes to this project's daemon; null = bridge default.
  projectRoot?: string | null;
  onAccept: (diffId: number) => Promise<void>;
  onReject: (diffId: number) => Promise<void>;
  // D2: cosmetic guard — the daemon's retireRun already refuses to kill a
  // live run; disabling here just makes the safe state visible/click-proof.
  agentRunning?: boolean;
  // P1-3: fire-and-forget comment delivery — App routes through send_message
  // (steer when the agent is running). Rejects on IPC failure.
  onSendComments?: (text: string) => Promise<void>;
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

// Extract the "new" path from a git diff header. Git quotes paths with
// spaces/special chars, so we match to end-of-line and strip surrounding
// quotes. `+++ b/Y` (new file or rename) is the primary; `+++ /dev/null`
// (deleted file) is mapped to the old path via `--- a/X`.  The old `\S+`
// patterns truncated on spaces and never matched quoted-unicode paths.
const NEW_FILE_RE = /^\+\+\+ (?:b\/)?(.+)$/;
const OLD_FILE_RE = /^--- (?:a\/)?(.+)$/;

function stripQuotes(s: string): string {
  if (s.startsWith('\"') && s.endsWith('\"') && s.length >= 2) s = s.slice(1, -1);
  // Git places the a// b/ prefix OUTSIDE the quotes: `"b/my file.txt"` → strip
  // leaves `b/my file.txt`. Remove the prefix so downstream consumers (language
  // detection, file navigation labels) get a clean relative path.
  s = s.replace(/^(?:a|b)\//, "");
  return s;
}

// Resolve the display path for a file header line, preferring the new path
// (`+++ b/`), falling back to the old path (`--- a/`) for deletions.  Returns
// null when neither header is on this line.
function diffFilePath(line: string): string | null {
  let m = NEW_FILE_RE.exec(line);
  if (m) {
    const p = stripQuotes(m[1]);
    return p === "/dev/null" ? null : p; // pure deletion — no "new" file
  }
  m = OLD_FILE_RE.exec(line);
  if (m) {
    const p = stripQuotes(m[1]);
    return p === "/dev/null" ? null : p; // pure addition — old is /dev/null
  }
  return null;
}

// D0: one navigable file inside a multi-file diff. `lineIndex` targets the
// `diff --git` line (falling back to the `+++` header) so a chip click can
// scroll straight to the file's section.
interface DiffFileSegment {
  path: string;
  lineIndex: number;
  adds: number;
  dels: number;
  status: "add" | "del" | "mod" | "rename";
}

// Walk the full (untruncated) diff once, splitting it into per-file segments
// at each `--- a/…` / `+++ b/…` header pair; content `+`/`-` lines accrue to
// the current segment (headers never count). Status is inferred from which
// header read `/dev/null` and whether the old/new paths differ.
function parseFileSegments(lines: string[]): DiffFileSegment[] {
  const segments: DiffFileSegment[] = [];
  let current: DiffFileSegment | null = null;
  let oldPath: string | null = null;
  let newPath: string | null = null;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const oldMatch = OLD_FILE_RE.exec(line);
    if (oldMatch) {
      oldPath = stripQuotes(oldMatch[1]);
      if (oldPath === "/dev/null") oldPath = null;
    }
    const newMatch = NEW_FILE_RE.exec(line);
    if (newMatch) {
      newPath = stripQuotes(newMatch[1]);
      if (newPath === "/dev/null") newPath = null;

      // Scroll target: the `diff --git` above this header pair, else the
      // `+++` line itself. Never scan before the previous segment's start.
      let startIndex = i;
      const prevStart = segments.length > 0 ? segments[segments.length - 1].lineIndex : -1;
      for (let j = i - 1; j > prevStart; j--) {
        if (lines[j].startsWith("diff --git")) {
          startIndex = j;
          break;
        }
      }

      const path = newPath ?? oldPath ?? "unknown";
      let status: DiffFileSegment["status"] = "mod";
      if (oldPath === null) status = "add";
      else if (newPath === null) status = "del";
      else if (oldPath !== newPath) status = "rename";

      current = { path, lineIndex: startIndex, adds: 0, dels: 0, status };
      segments.push(current);
      oldPath = null;
      newPath = null;
    }

    // Payload only — the +++/--- header lines above are excluded.
    if (current) {
      if (line.startsWith("+") && !line.startsWith("+++")) current.adds++;
      else if (line.startsWith("-") && !line.startsWith("---")) current.dels++;
    }
  }

  return segments;
}

// #10: Parse `@@ -oldStart,oldCount +newStart,newCount @@` hunk headers.
// Returns the old and new starting line numbers, or null if not a hunk header.
const HUNK_RE = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;
function parseHunkHeader(line: string): { oldStart: number; newStart: number } | null {
  const m = HUNK_RE.exec(line);
  if (!m) return null;
  return { oldStart: parseInt(m[1], 10), newStart: parseInt(m[2], 10) };
}

// #10: One row of the inline diff with resolved line numbers.
interface InlineRow {
  line: string;
  cls: string;
  isCode: boolean;
  srcIndex: number;
  // Real file line number (from hunk header), null for non-code lines.
  oldLine: number | null;
  newLine: number | null;
  // Current file path (for comment references), null between files.
  filePath: string | null;
  // Language for syntax highlighting (tracked per file header).
  lang: Language | null;
}

// #10: Walk lines tracking old/new line counters from hunk headers.
function parseInlineRows(lines: string[]): InlineRow[] {
  const rows: InlineRow[] = [];
  let oldLine = 0;
  let newLine = 0;
  let filePath: string | null = null;
  let lang: Language | null = null;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const fp = diffFilePath(line);
    if (fp) {
      filePath = fp;
      lang = languageFromPath(fp);
    }

    // Hunk header resets the counters.
    const hunk = parseHunkHeader(line);
    if (hunk) {
      oldLine = hunk.oldStart;
      newLine = hunk.newStart;
      rows.push({
        line,
        cls: "diff-line diff-hunk",
        isCode: false,
        srcIndex: i,
        oldLine: null,
        newLine: null,
        filePath,
        lang,
      });
      continue;
    }

    const cls = lineClass(line);
    const isAdd = cls.endsWith("diff-add");
    const isDel = cls.endsWith("diff-del");
    const isCode = isAdd || isDel;

    let rowOldLine: number | null = null;
    let rowNewLine: number | null = null;
    if (isDel) {
      rowOldLine = oldLine;
      oldLine++;
    } else if (isAdd) {
      rowNewLine = newLine;
      newLine++;
    } else if (!line.startsWith("diff --git") && !line.startsWith("---") && !line.startsWith("+++") && !line.startsWith("\\") && line.length > 0) {
      // Context line — both counters advance.
      rowOldLine = oldLine;
      rowNewLine = newLine;
      oldLine++;
      newLine++;
    }

    rows.push({
      line,
      cls,
      isCode,
      srcIndex: i,
      oldLine: rowOldLine,
      newLine: rowNewLine,
      filePath,
      lang,
    });
  }

  return rows;
}

// Belt D: one side of one side-by-side row. `sign` retains the +/- marker
// for the gutter; kind only picks the tint class.
interface SplitCell {
  text: string;
  kind: "old" | "new" | "ctx";
  lang: Language | null;
  // D0: index into the diff's line array, for chip-scroll targeting
  // (undefined only on the synthetic truncation-note row).
  src?: number;
  // #10: real file line number for the gutter.
  lineNum?: number | null;
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
  let oldLine = 0;
  let newLine = 0;
  const flush = () => {
    const n = Math.max(olds.length, news.length);
    for (let i = 0; i < n; i++) {
      rows.push({ left: olds[i] ?? null, right: news[i] ?? null });
    }
    olds = [];
    news = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const fp = diffFilePath(line);
    if (fp) lang = languageFromPath(fp);

    // #10: Parse hunk header to reset line counters.
    const hunk = parseHunkHeader(line);
    if (hunk) {
      flush();
      oldLine = hunk.oldStart;
      newLine = hunk.newStart;
      continue;
    }
    if (line.startsWith("-") && !line.startsWith("---")) {
      olds.push({ text: line.slice(1), kind: "old", lang, src: i, lineNum: oldLine });
      oldLine++;
      continue;
    }
    if (line.startsWith("+") && !line.startsWith("+++")) {
      news.push({ text: line.slice(1), kind: "new", lang, src: i, lineNum: newLine });
      newLine++;
      continue;
    }
    // Context lines carry a leading space; file headers and other metadata
    // are duplicated across both columns so file boundaries stay visible.
    flush();
    const isMetadata = line.startsWith("diff --git") || line.startsWith("---") || line.startsWith("+++") || line.startsWith("\\") || line.startsWith("index ") || (line.length === 0 && !line.startsWith(" "));
    const oldCtxLineNum = isMetadata ? null : oldLine;
    const newCtxLineNum = isMetadata ? null : newLine;
    // #10: separate cells per column so left shows oldLine, right shows newLine.
    const ctxL: SplitCell = { text: line, kind: "ctx", lang, src: i, lineNum: oldCtxLineNum };
    const ctxR: SplitCell = { text: line, kind: "ctx", lang, src: i, lineNum: newCtxLineNum };
    if (!isMetadata) {
      oldLine++;
      newLine++;
    }
    rows.push({ left: ctxL, right: ctxR });
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
// autonomyHint renders the rung-0 streak suffix (" · C1 4/10 · C2 1/10")
// for the rung classes only — C0/unclassified never carry rung progress.
function autonomyHint(r: AutonomyReport): string {
  const parts = r.classes
    .filter((c) => c.class === "C1" || c.class === "C2" || c.class === "C3")
    .map((c) =>
      c.next_threshold > 0
        ? `${c.class} ${Math.min(c.streak, c.next_threshold)}/${c.next_threshold}`
        : `${c.class} ${c.streak}`,
    );
  return parts.length > 0 ? " · " + parts.join(" · ") : "";
}

// Only a `pending` diff is actionable; afterwards this becomes a record card.
export default function DiffViewer({ diff, onAccept, onReject, projectRoot, onSendComments, agentRunning }: Props) {
  const [acting, setActing] = useState(false);
  const [reviews, setReviews] = useState<ReviewResult[] | null>(null);
  const [consensus, setConsensus] = useState<string | null>(null);
  const [reviewing, setReviewing] = useState(false);
  const [reviewError, setReviewError] = useState<string | null>(null);
  // M15 (O-1 rung-0): the muted header one-liner — auto-apply pref plus
  // per-class streaks, computed daemon-side when the card opens.
  const [autonomy, setAutonomy] = useState<AutonomyReport | null>(null);
  useEffect(() => {
    let cancelled = false;
    autonomyStatus(projectRoot ?? undefined)
      .then((r) => {
        if (!cancelled) setAutonomy(unwrap(r).autonomy ?? null);
      })
      .catch(() => {
        // Observability hint only — a daemon without the command (or a
        // failed read) must never block the diff card.
      });
    return () => {
      cancelled = true;
    };
  }, [projectRoot]);
  // Belt D: inline (unified) vs split (old | new) rendering.
  const [split, setSplit] = useState(false);
  // P1-3: inline diff comments — Map<lineIndex, comment text>
  const [comments, setComments] = useState<Map<number, string>>(new Map());
  const [openLine, setOpenLine] = useState<number | null>(null);
  const [sendingComments, setSendingComments] = useState(false);
  const oldColRef = useRef<HTMLDivElement>(null);
  const newColRef = useRef<HTMLDivElement>(null);
  // D0: whichever body is mounted (inline <pre> or split grid) — chip clicks
  // query it for the target line. Callback form so <pre> and <div> both fit.
  const diffBodyRef = useRef<HTMLElement | null>(null);
  const setDiffBodyRef = (el: HTMLElement | null) => {
    diffBodyRef.current = el;
  };

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

  // #10 + A2: compose and send inline diff comments with real file:line refs.
  const sendComments = async () => {
    if (comments.size === 0 || sendingComments) return;
    // Build file:line references from the parsed inline rows.
    const body = [...comments.entries()]
      .filter(([, t]) => t.trim() !== "")
      .map(([i, t]) => {
        const row = inlineRows[i];
        const file = row?.filePath ?? "unknown";
        const lineNum = row?.newLine ?? row?.oldLine;
        const ref = lineNum != null ? `${file}:${lineNum}` : `L${i}`;
        return `- ${ref}: ${t.trim()}`;
      })
      .join("\n");
    if (body === "" || !onSendComments) return;
    setSendingComments(true);
    try {
      await onSendComments(`Diff #${diff.id} feedback:\n${body}`);
      setComments(new Map());
      setOpenLine(null);
    } finally {
      setSendingComments(false);
    }
  };

  const runReview = async () => {
    if (reviewing) return;
    setReviewing(true);
    setReviewError(null);
    try {
      const resp = unwrap(await reviewDiff(diff.id, projectRoot ?? undefined));
      setReviews(resp.reviews ?? []);
      setConsensus(resp.consensus ?? null);
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

  // D0: chip click scrolls the file's first diff line into view. In split
  // mode the row exists in both columns; the first DOM match is the old
  // column, and syncColumn mirrors its scroll to the new column.
  const scrollToFile = (lineIndex: number) => {
    const target = diffBodyRef.current;
    if (target) {
      const child = target.querySelector(`[data-line="${lineIndex}"]`);
      if (child) child.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  };

  const allLines = diff.content.split("\n");
  const truncated = allLines.length > MAX_LINES;
  const lines = truncated ? allLines.slice(0, MAX_LINES) : allLines;
  const truncatedNote = truncated
    ? `Diff truncated — showing first ${MAX_LINES} of ${allLines.length} lines.`
    : null;

  // D0: segments come from the untruncated lines so the chip row lists every
  // file even when the tail is cut off; chips past MAX_LINES render disabled.
  // Memoized on diff.content — parseFileSegments is a full linear pass.
  const fileSegments = useMemo(() => parseFileSegments(allLines), [diff.content]);

  // #10: parse inline rows with line number tracking.
  const inlineRows = useMemo(() => parseInlineRows(lines), [lines]);

  // Walk the inline rows once, rendering with line-number gutters.
  const rendered: ReactNode[] = [];
  inlineRows.forEach((row, i) => {
    const isCode = row.isCode;
    rendered.push(
      <div key={i} className={row.cls} data-line={row.srcIndex}>
        {row.cls.endsWith("diff-add") ? (
          <>
            <span className="diff-linenum diff-linenum-new">{row.newLine ?? ""}</span>
            {renderCode("+", row.line.slice(1), row.lang)}
          </>
        ) : row.cls.endsWith("diff-del") ? (
          <>
            <span className="diff-linenum diff-linenum-old">{row.oldLine ?? ""}</span>
            {renderCode("-", row.line.slice(1), row.lang)}
          </>
        ) : (
          row.line
        )}
        {pending && isCode && (
          <button
            type="button"
            className={`diff-comment-btn${comments.has(i) ? " has-comment" : ""}`}
            aria-label={`Comment on line ${row.newLine ?? row.oldLine ?? i}`}
            onClick={() => setOpenLine((o) => (o === i ? null : i))}
          >
            💬
          </button>
        )}
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

  // #11: Split view comment button — same pattern as inline, using srcIndex.
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
        <div key={key} className="diff-line diff-line-old" data-line={cell.src}>
          <span className="diff-linenum diff-linenum-old">{cell.lineNum ?? ""}</span>
          {renderCode("-", cell.text, cell.lang)}
          {pending && cell.src != null && (
            <button
              type="button"
              className={`diff-comment-btn${comments.has(cell.src) ? " has-comment" : ""}`}
              aria-label={`Comment on line ${cell.lineNum ?? cell.src}`}
              onClick={() => setOpenLine((o) => (o === cell.src ? null : cell.src!))}
            >
              💬
            </button>
          )}
        </div>
      );
    }
    if (cell.kind === "new") {
      return (
        <div key={key} className="diff-line diff-line-new" data-line={cell.src}>
          <span className="diff-linenum diff-linenum-new">{cell.lineNum ?? ""}</span>
          {renderCode("+", cell.text, cell.lang)}
          {pending && cell.src != null && (
            <button
              type="button"
              className={`diff-comment-btn${comments.has(cell.src) ? " has-comment" : ""}`}
              aria-label={`Comment on line ${cell.lineNum ?? cell.src}`}
              onClick={() => setOpenLine((o) => (o === cell.src ? null : cell.src!))}
            >
              💬
            </button>
          )}
        </div>
      );
    }
    return (
      <div key={key} className="diff-line diff-line-context" data-line={cell.src}>
        {cell.lineNum != null && (
          <span className="diff-linenum diff-linenum-ctx">{cell.lineNum}</span>
        )}
        {cell.text}
      </div>
    );
  };

  return (
    <section className={`diff-card${hasReject ? " review-rejected" : ""}`}>
      <header className="diff-header">
        <span className="diff-title">
          Diff #{diff.id}
        </span>
        {autonomy !== null && (
          <span className="diff-autonomy" title={autonomy.revert_check}>
            Auto-apply: {autonomy.auto_apply}
            {autonomyHint(autonomy)}
          </span>
        )}
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
            {comments.size > 0 && (
              <button
                type="button"
                className="btn-comments"
                disabled={sendingComments}
                onClick={() => void sendComments()}
              >
                {sendingComments ? "Sending…" : `Send comments (${comments.size})`}
              </button>
            )}
            <button
              className="btn-review"
              disabled={acting || reviewing}
              title="Ask the configured review models to grade this diff"
              onClick={() => void runReview()}
            >
              {reviewing ? "Reviewing…" : "Review"}
            </button>
            <button
              className="btn-accept"
              disabled={acting || hasReject || agentRunning}
              title={agentRunning ? "Agent is running — review after it finishes" : undefined}
              onClick={() => void act(onAccept)}
            >
              Accept
            </button>
            <button
              className="btn-reject"
              disabled={acting || agentRunning}
              title={agentRunning ? "Agent is running — review after it finishes" : undefined}
              onClick={() => void act(onReject)}
            >
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
          {reviews.length > 0 && consensus && (
            <div className="review-consensus">
              <span className={`verdict-badge verdict-${consensus}`}>
                {consensus.replace("_", " ")}
              </span>
              <span className="review-consensus-label">
                {reviews.length} reviewer{reviews.length > 1 ? "s" : ""} · 2/3 gate
              </span>
            </div>
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
      {/* D0: file navigation — chip row for multi-file diffs */}
      {fileSegments.length > 1 && (
        <div className="diff-file-nav" role="group" aria-label="Files in this diff">
          {fileSegments.map((seg, i) => {
            const basename = seg.path.split("/").pop() || seg.path;
            const isTruncated = seg.lineIndex >= MAX_LINES;
            return (
              <button
                key={`${seg.path}-${i}`}
                type="button"
                role="button"
                className={`diff-file-chip${isTruncated ? " truncated" : ""}`}
                title={seg.path}
                disabled={isTruncated}
                onClick={() => scrollToFile(seg.lineIndex)}
              >
                <span className="diff-file-status" data-status={seg.status} />
                <span className="diff-file-name">{basename}</span>
                <span className="diff-file-churn">
                  <span className="churn-add">+{seg.adds}</span>{" "}
                  <span className="churn-del">-{seg.dels}</span>
                </span>
              </button>
            );
          })}
          {fileSegments.some((s) => s.lineIndex >= MAX_LINES) && (
            <span className="diff-file-nav-note">
              Files beyond line {MAX_LINES} are not rendered (truncated diff).
            </span>
          )}
        </div>
      )}
      {diff.content === "" ? (
        <div className="diff-empty">Diff file is empty or unreadable.</div>
      ) : split ? (
        <div className="diff-split" ref={setDiffBodyRef}>
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
        <pre className="diff-body" ref={setDiffBodyRef}>
          {rendered}
          {truncatedNote !== null && (
            <div className="diff-truncated">
              {truncatedNote}
            </div>
          )}
        </pre>
      )}
      {openLine != null && pending && (
        <div className="diff-comment-box">
          <textarea
            value={comments.get(openLine) ?? ""}
            aria-label="Line comment"
            placeholder="Note for the agent…"
            onChange={(e) =>
              setComments((prev) => {
                const next = new Map(prev);
                next.set(openLine, e.target.value);
                return next;
              })
            }
          />
          <button
            type="button"
            className="diff-comment-close"
            onClick={() => setOpenLine(null)}
          >
            Done
          </button>
        </div>
      )}
    </section>
  );
}
