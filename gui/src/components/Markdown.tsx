import { Fragment, memo, useState, type ReactNode } from "react";
import { Check } from "lucide-react";
import { tokenize, type Language } from "../highlight";
import FileRefContextMenu from "./FileRefContextMenu";
import { Dialog, DialogContent } from "./ui/dialog";
import { cn } from "../lib/utils";

// Belt B: a small dependency-free markdown renderer for agent output and
// wiki notes. Line-based block parsing (headings, fenced code, lists,
// blockquotes, hr, paragraphs) plus a minimal inline parser (bold, italic,
// inline code, links). Security: every string is emitted as a React text
// child (auto-escaped) — there is deliberately no dangerouslySetInnerHTML
// anywhere, and link hrefs are scheme-allowlisted.

interface Props {
  content: string;
  className?: string;
  // Belt B chat search: case-insensitive substring wrapped in <mark>.
  highlight?: string;
  // Tri-model open file: project root for resolving file paths in inline code spans.
  projectRoot?: string | null;
}

// Fence info strings mapped onto the diff viewer's tokenizer languages;
// anything else renders unstyled.
const FENCE_LANGS: Record<string, Language> = {
  go: "go",
  golang: "go",
  rs: "rust",
  rust: "rust",
  ts: "ts",
  tsx: "ts",
  typescript: "ts",
  js: "ts",
  jsx: "ts",
  javascript: "ts",
  mts: "ts",
  cts: "ts",
  mjs: "ts",
  cjs: "ts",
  py: "python",
  python: "python",
  bash: "bash",
  sh: "bash",
  shell: "bash",
  zsh: "bash",
  json: "json",
  yaml: "yaml",
  yml: "yaml",
  toml: "yaml",
  sql: "yaml",  // close enough for keyword highlighting
  dockerfile: "bash",
};

// Wrap occurrences of `query` (case-insensitive) in <mark>. Shared with
// MessageBubble for its plain-text bubbles (user messages, tool lines).
export function highlightText(
  text: string,
  query: string | undefined,
  keyPrefix: string,
): ReactNode {
  if (query === undefined || query === "") return text;
  const needle = query.toLowerCase();
  const out: ReactNode[] = [];
  let rest = text;
  let k = 0;
  for (;;) {
    const at = rest.toLowerCase().indexOf(needle);
    if (at < 0) break;
    if (at > 0) out.push(rest.slice(0, at));
    out.push(<mark key={`${keyPrefix}-${k++}`}>{rest.slice(at, at + needle.length)}</mark>);
    rest = rest.slice(at + needle.length);
  }
  if (out.length === 0) return text;
  if (rest !== "") out.push(rest);
  return <Fragment key={keyPrefix}>{out}</Fragment>;
}

// Combined inline alternation: **bold** | *italic* | ~~strike~~ | `code` | ![alt](url) | [text](url).
// Bold runs first so `*` inside bold is parsed by the recursive call.
// Image `![alt](url)` runs before link `[text](url)` to avoid the link
// pattern matching `![...]` (the `!` prefix is the image discriminator).
// A shared /g regex would be corrupted by the recursion (a nested call
// resets lastIndex while an outer exec loop is mid-scan) — each call
// gets a fresh instance.
//
// GLM batch review CRITICAL: the concatenated template segments after
// the first String.raw tag were cooked (escapes like \n \s \] \( became
// literal chars), producing a non-compiling regex. Fixed: the entire
// pattern is one String.raw template, with the backtick character
// injected via a const to avoid breaking the template literal.
const BT = "`";
const INLINE_SOURCE = String.raw`(\*\*[\s\S]+?\*\*)|(\*[^*\n]+\*)|(~~[^~\n]+~~)|(` + BT + String.raw`[^` + BT + String.raw`\n]+` + BT + String.raw`)|(!\[([^\]\n]*)\]\(([^)\s]+)\))|(\[([^\]\n]+)\]\(([^)\s]+)\))`;

// Tri-model open file: conservative path detection for inline code spans.
// Gate loosely (JS) — the Rust side validates existence + containment.
// Matches: src/main.go, src/main.go:42, /abs/path/x.ts, ~/path, wiki/note.md
// Rejects: URLs (https://), Go package names (encoding/json), bare words.
const FILE_EXT = /\.(go|rs|ts|tsx|js|jsx|py|md|json|toml|yaml|yml|sh|sql|css|html|txt|lock|mod|sum|proto|gradle)$/i;
function looksLikeFilePath(text: string): boolean {
  const t = text.trim();
  if (t.length < 2 || /\s/.test(t)) return false;
  // Reject URL schemes.
  if (/^[a-z]+:\/\//i.test(t)) return false;
  // Strip trailing :line or :line-range.
  const stripped = t.replace(/:\d+(-\d+)?$/, "");
  // Anchored paths: absolute, home-relative, or explicit relative (./ ../).
  if (stripped.startsWith("/") || stripped.startsWith("~") ||
      stripped.startsWith("./") || stripped.startsWith("../")) return true;
  // Slash-containing paths: require a known file extension to avoid
  // false positives like Go import paths (encoding/json, net/http).
  if (stripped.includes("/") && FILE_EXT.test(stripped)) return true;
  // Bare filenames with known extensions (go.mod, package.json).
  if (FILE_EXT.test(stripped)) return true;
  return false;
}

// Strip trailing :line or :line-range from a path for resolution.
function stripLineRef(text: string): string {
  return text.trim().replace(/:\d+(-\d+)?$/, "");
}

// CodeSpan: an inline code element that offers a right-click context menu
// (Open / Reveal in Folder) when the text looks like a file path.
function CodeSpan({ text, highlight, highlightKey, projectRoot }: {
  text: string;
  highlight?: string;
  highlightKey: string;
  projectRoot?: string | null;
}) {
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  return (
    <>
      <code
        className="bubble-inline-code file-ref-span rounded-[4px] bg-code-chip-bg px-1 font-mono text-[0.92em]"
        onContextMenu={(e) => {
          e.preventDefault();
          setMenu({ x: e.clientX, y: e.clientY });
        }}
      >
        {highlightText(text, highlight, highlightKey)}
      </code>
      {menu && (
        <FileRefContextMenu
          path={stripLineRef(text)}
          projectRoot={projectRoot ?? null}
          x={menu.x}
          y={menu.y}
          onClose={() => setMenu(null)}
        />
      )}
    </>
  );
}

function parseInline(text: string, highlight: string | undefined, keyPrefix: string, projectRoot?: string | null): ReactNode[] {
  const re = new RegExp(INLINE_SOURCE, "g");
  const nodes: ReactNode[] = [];
  let last = 0;
  let k = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) {
      nodes.push(highlightText(text.slice(last, m.index), highlight, `${keyPrefix}-t${k++}`));
    }
    const key = `${keyPrefix}-i${k++}`;
    if (m[1] !== undefined) {
      nodes.push(<strong key={key}>{parseInline(m[1].slice(2, -2), highlight, key, projectRoot)}</strong>);
    } else if (m[2] !== undefined) {
      nodes.push(<em key={key}>{parseInline(m[2].slice(1, -1), highlight, key, projectRoot)}</em>);
    } else if (m[3] !== undefined) {
      // ~~strikethrough~~
      nodes.push(<del key={key} className="text-text-dim">{parseInline(m[3].slice(2, -2), highlight, key, projectRoot)}</del>);
    } else if (m[4] !== undefined) {
      const codeText = m[4].slice(1, -1);
      if (looksLikeFilePath(codeText)) {
        nodes.push(
          <CodeSpan key={key} text={codeText} highlight={highlight} highlightKey={key} projectRoot={projectRoot} />,
        );
      } else {
        nodes.push(
          <code key={key} className="bubble-inline-code rounded-[4px] bg-code-chip-bg px-1 font-mono text-[0.92em]">
            {highlightText(codeText, highlight, key)}
          </code>,
        );
      }
    } else if (m[6] !== undefined) {
      // ![alt](url) — inline image with click-to-zoom.
      nodes.push(renderImage(m[6], m[7], key));
    } else {
      nodes.push(renderLink(m[9], m[10], highlight, key));
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) {
    nodes.push(highlightText(text.slice(last), highlight, `${keyPrefix}-t${k++}`));
  }
  return nodes;
}

// ZoomableImage: click to open a full-screen lightbox dialog (Radix, Phase 5).
// Esc, overlay click, or clicking the zoomed image closes. The Dialog handles
// focus, portal, and the App Esc gate.
function ZoomableImage({ src, alt }: { src: string; alt: string }) {
  const [zoomed, setZoomed] = useState(false);
  return (
    <>
      <img
        src={src}
        alt={alt}
        className="md-inline-img my-1 max-w-full cursor-zoom-in rounded-md border border-border transition-opacity duration-150 hover:opacity-90"
        loading="lazy"
        tabIndex={0}
        role="button"
        aria-label={`Image: ${alt}. Press Enter to zoom.`}
        onClick={() => setZoomed(true)}
        onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setZoomed(true); } }}
      />
      <Dialog open={zoomed} onOpenChange={setZoomed}>
        <DialogContent
          aria-label={alt}
          className="flex cursor-zoom-out items-center justify-center border-none bg-transparent p-0 shadow-none"
          onClick={() => setZoomed(false)}
        >
          <img src={src} alt={alt} className="md-img-zoomed max-h-[90vh] max-w-[90vw] rounded-[4px] object-contain" />
        </DialogContent>
      </Dialog>
    </>
  );
}

// renderImage: renders ![alt](url) as a ZoomableImage. Scheme-allowlisted
// (https:, mailto:, #, /, data:image/ — same as renderLink plus data:image
// for base64-embedded images. <img> context does not execute scripts, so
// data:image/svg+xml is safe here).
function renderImage(alt: string, url: string, key: string): ReactNode {
  if (!/^(https?:|mailto:|#|\/|data:image\/)/i.test(url)) {
    return <Fragment key={key}>{highlightText(`![${alt}](${url})`, undefined, key)}</Fragment>;
  }
  return <ZoomableImage key={key} src={url} alt={alt} />;
}

// javascript:/data: URLs never render as links — the raw text stays.
function renderLink(
  label: string,
  url: string,
  highlight: string | undefined,
  key: string,
): ReactNode {
  if (!/^(https?:|mailto:|#|\/)/i.test(url)) {
    return <Fragment key={key}>{highlightText(`${label}(${url})`, highlight, key)}</Fragment>;
  }
  return (
    <a key={key} href={url} target="_blank" rel="noreferrer" className="text-link">
      {parseInline(label, highlight, key)}
    </a>
  );
}

type Block =
  | { kind: "code"; lang: Language | null; text: string }
  | { kind: "heading"; level: 1 | 2 | 3 | 4; text: string }
  | { kind: "ul"; items: string[] }
  | { kind: "ol"; items: string[] }
  | { kind: "quote"; text: string }
  | { kind: "alert"; alertType: string; text: string }
  | { kind: "hr" }
  | { kind: "table"; headers: string[]; rows: string[][] }
  | { kind: "para"; text: string };

const FENCE_RE = /^```(\S*)\s*$/;
const HEADING_RE = /^#{1,4}\s+/;
const HR_RE = /^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/;
const UL_RE = /^\s*[-*+]\s+/;
const OL_RE = /^\s*\d+[.)]\s+/;
const QUOTE_RE = /^>\s?/;

// A line that interrupts a paragraph: fence, heading, hr, list, quote.
function isBlockStart(line: string): boolean {
  return (
    FENCE_RE.test(line) ||
    HEADING_RE.test(line) ||
    HR_RE.test(line) ||
    UL_RE.test(line) ||
    OL_RE.test(line) ||
    QUOTE_RE.test(line)
  );
}

function parseBlocks(content: string): Block[] {
  const lines = content.replace(/\r\n/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.trim() === "") {
      i++;
      continue;
    }
    const fence = line.match(FENCE_RE);
    if (fence) {
      const buf: string[] = [];
      i++;
      while (i < lines.length && !/^```\s*$/.test(lines[i])) {
        buf.push(lines[i]);
        i++;
      }
      i++; // closing fence (or EOF when unterminated)
      blocks.push({ kind: "code", lang: FENCE_LANGS[fence[1].toLowerCase()] ?? null, text: buf.join("\n") });
      continue;
    }
    const heading = line.match(/^(#{1,4})\s+(.*)$/);
    if (heading) {
      blocks.push({ kind: "heading", level: heading[1].length as 1 | 2 | 3 | 4, text: heading[2] });
      i++;
      continue;
    }
    if (HR_RE.test(line)) {
      blocks.push({ kind: "hr" });
      i++;
      continue;
    }
    // GFM table: header row | --- | data rows
    if (line.includes("|") && i + 1 < lines.length && /^\|?[\s:]*-{2,}[\s:]*-?/.test(lines[i + 1]) && lines[i + 1].includes("|")) {
      // Split on pipes outside inline code spans (backtick-aware).
      // GFM: a pipe inside a code span must not delimit a cell.
      const parseRow = (row: string): string[] => {
        let r = row.trim();
        if (r.startsWith("|")) r = r.slice(1);
        if (r.endsWith("|")) r = r.slice(0, -1);
        const cells: string[] = [];
        let cur = "";
        let inCode = false;
        for (let c = 0; c < r.length; c++) {
          if (r[c] === "`") inCode = !inCode;
          if (r[c] === "|" && !inCode) {
            cells.push(cur.trim());
            cur = "";
          } else {
            cur += r[c];
          }
        }
        cells.push(cur.trim());
        return cells;
      };
      const h = parseRow(line);
      i += 2; // skip header + separator
      const rows: string[][] = [];
      while (i < lines.length && lines[i].trim() !== "" && lines[i].includes("|") && !isBlockStart(lines[i])) {
        rows.push(parseRow(lines[i]));
        i++;
      }
      blocks.push({ kind: "table", headers: h, rows });
      continue;
    }
    if (UL_RE.test(line)) {
      const items: string[] = [];
      let it: RegExpMatchArray | null;
      while (i < lines.length && (it = lines[i].match(/^\s*[-*+]\s+(.*)$/)) !== null) {
        items.push(it[1]);
        i++;
      }
      blocks.push({ kind: "ul", items });
      continue;
    }
    if (OL_RE.test(line)) {
      const items: string[] = [];
      let it: RegExpMatchArray | null;
      while (i < lines.length && (it = lines[i].match(/^\s*\d+[.)]\s+(.*)$/)) !== null) {
        items.push(it[1]);
        i++;
      }
      blocks.push({ kind: "ol", items });
      continue;
    }
    if (QUOTE_RE.test(line)) {
      const qlines: string[] = [];
      let q: RegExpMatchArray | null;
      while (i < lines.length && (q = lines[i].match(/^>\s?(.*)$/)) !== null) {
        qlines.push(q[1]);
        i++;
      }
      // GFM alert: first line matches > [!NOTE] / [!WARNING] / [!TIP] / [!IMPORTANT] / [!CAUTION]
      const alertMatch = qlines[0]?.match(/^\s*\[!(NOTE|WARNING|TIP|IMPORTANT|CAUTION)\s*\]\s*$/i);
      if (alertMatch) {
        const alertType = alertMatch[1].toLowerCase();
        const alertText = qlines.slice(1).join("\n");
        blocks.push({ kind: "alert", alertType, text: alertText });
      } else {
        blocks.push({ kind: "quote", text: qlines.join("\n") });
      }
      continue;
    }
    const para: string[] = [];
    while (i < lines.length && lines[i].trim() !== "" && !isBlockStart(lines[i])) {
      para.push(lines[i]);
      i++;
    }
    blocks.push({ kind: "para", text: para.join("\n") });
  }
  return blocks;
}

// Heading typography per level (was the .markdown h1-h4 rules). First block
// zeroes its top margin via the mt-0 conditional (was :first-child rules).
const HEADING_CLS: Record<1 | 2 | 3 | 4, string> = {
  1: "mb-1 mt-2.5 text-[18px] font-semibold leading-[1.3]",
  2: "mb-1 mt-2.5 text-[16px] font-semibold leading-[1.3]",
  3: "mb-1 mt-2.5 text-[15px] font-semibold leading-[1.3]",
  4: "mb-1 mt-2.5 text-[14px] font-semibold leading-[1.3]",
};

// GFM alert callout tones (was .md-alert-note/-warning/-important/-tip/-caution).
// box = left border + tinted background; label = label text color.
const ALERT_TONE: Record<string, { box: string; label: string }> = {
  note: { box: "border-l-link bg-link/6", label: "text-link" },
  warning: { box: "border-l-warn bg-warn/6", label: "text-warn" },
  important: { box: "border-l-warn bg-warn/6", label: "text-warn" },
  tip: { box: "border-l-ok-text bg-ok/6", label: "text-ok-text" },
  caution: { box: "border-l-err bg-err/6", label: "text-err" },
};

// M11 F3: code block with language label + copy button.
function CodeBlock({ block, highlight, index }: { block: Extract<Block, { kind: "code" }>; highlight: string | undefined; index: number }) {
  const key = `b${index}`;
  const [copied, setCopied] = useState(false);
  const langLabel = block.lang ?? "";

  const copy = () => {
    navigator.clipboard.writeText(block.text).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }).catch(() => {});
  };

  return (
    <div className={cn("bubble-code-wrap my-1.5 overflow-hidden rounded-sm border border-border", index === 0 && "mt-0")} key={key}>
      <div className="bubble-code-header flex items-center justify-between border-b border-border bg-bg-raised px-2 py-0.5 text-micro">
        {langLabel && <span className="bubble-code-lang font-mono text-[10px] uppercase tracking-[0.04em] text-text-dim">{langLabel}</span>}
        <button type="button" className="bubble-code-copy cursor-pointer rounded-[4px] border-none bg-transparent px-1.5 py-0.5 text-micro text-text-dim hover:bg-bg hover:text-text" onClick={copy} aria-label="Copy code">
          {copied ? <Check size={11} /> : "Copy"}
        </button>
      </div>
      <pre className="bubble-code m-0 overflow-x-auto bg-bg px-2.5 py-2 font-mono text-caption">
        <code>
          {block.text.split("\n").map((line, li) => (
            <Fragment key={li}>
              {li > 0 ? "\n" : ""}
              {tokenize(line, block.lang).map((t, ti) =>
                t.cls !== null ? (
                  <span key={ti} className={t.cls}>
                    {highlightText(t.text, highlight, `${key}-${li}-${ti}`)}
                  </span>
                ) : (
                  <Fragment key={ti}>
                    {highlightText(t.text, highlight, `${key}-${li}-${ti}`)}
                  </Fragment>
                ),
              )}
            </Fragment>
          ))}
        </code>
      </pre>
    </div>
  );
}

function renderBlock(block: Block, index: number, highlight: string | undefined, projectRoot?: string | null): ReactNode {
  const key = `b${index}`;
  switch (block.kind) {
    case "code": {
      return <CodeBlock key={key} block={block} highlight={highlight} index={index} />;
    }
    case "heading": {
      const Tag = `h${block.level}` as "h1" | "h2" | "h3" | "h4";
      return <Tag key={key} className={cn(HEADING_CLS[block.level], index === 0 && "mt-0")}>{parseInline(block.text, highlight, key, projectRoot)}</Tag>;
    }
    case "ul":
      return (
        <ul key={key} className={cn("my-1 pl-[22px]", index === 0 && "mt-0")}>
          {block.items.map((item, ii) => {
            // GFM task list: - [ ] or - [x]
            const taskMatch = item.match(/^\[([ x])\]\s+(.*)$/i);
            if (taskMatch) {
              const checked = taskMatch[1].toLowerCase() === "x";
              return (
                <li key={ii} className="md-task-item my-0.5 flex list-none items-baseline gap-1.5">
                  <input type="checkbox" className="m-0 accent-ok-text" checked={checked} readOnly aria-label={`Task: ${taskMatch[2]}`} />
                  <span className={checked ? "md-task-done text-text-dim line-through" : ""}>{parseInline(taskMatch[2], highlight, `${key}-${ii}`, projectRoot)}</span>
                </li>
              );
            }
            return <li key={ii} className="my-0.5">{parseInline(item, highlight, `${key}-${ii}`, projectRoot)}</li>;
          })}
        </ul>
      );
    case "ol":
      return (
        <ol key={key} className={cn("my-1 pl-[22px]", index === 0 && "mt-0")}>
          {block.items.map((item, ii) => (
            <li key={ii} className="my-0.5">{parseInline(item, highlight, `${key}-${ii}`, projectRoot)}</li>
          ))}
        </ol>
      );
    case "quote":
      return <blockquote key={key} className={cn("my-1.5 border-l-[3px] border-solid border-l-[color:var(--blockquote-border)] px-2.5 py-0.5 text-text-dim", index === 0 && "mt-0")}>{parseInline(block.text, highlight, key, projectRoot)}</blockquote>;
    case "alert": {
      // GFM alert callout: colored left-border box matching the alert type.
      const tone = ALERT_TONE[block.alertType] ?? ALERT_TONE.note;
      return (
        <div key={key} className={cn(`md-alert md-alert-${block.alertType} my-2 rounded-sm border-l-[3px] border-solid px-3 py-1.5`, tone.box)}>
          <div className={cn("md-alert-label mb-0.5 text-micro font-semibold tracking-[0.05em]", tone.label)}>{block.alertType.toUpperCase()}</div>
          <div className="md-alert-body text-label text-text-dim">{parseInline(block.text, highlight, key, projectRoot)}</div>
        </div>
      );
    }
    case "hr":
      return <hr key={key} className="my-2 border-0 border-t border-border" />;
    case "table": {
      return (
        <table key={key} className="md-table my-1.5 w-full border-collapse text-caption">
          <thead>
            <tr>
              {block.headers.map((h, hi) => (
                <th key={hi} className="border border-border bg-bg-raised px-2 py-1 text-left font-semibold">{parseInline(h, highlight, `${key}-h${hi}`)}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {block.rows.map((row, ri) => (
              <tr key={ri} className="even:bg-bg-raised">
                {block.headers.map((_, ci) => (
                  <td key={ci} className="border border-border px-2 py-1 text-left">{parseInline(row[ci] ?? "", highlight, `${key}-r${ri}c${ci}`)}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      );
    }
    case "para":
      return <p key={key} className={cn("my-1.5", index === 0 && "mt-0")}>{parseInline(block.text, highlight, key, projectRoot)}</p>;
  }
}

export default memo(function Markdown({ content, className, highlight, projectRoot }: Props) {
  const blocks = parseBlocks(content);
  return (
    <div className={cn("markdown", className)}>
      {blocks.map((b, bi) => renderBlock(b, bi, highlight, projectRoot))}
    </div>
  );
});
