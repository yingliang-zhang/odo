import { Fragment, memo, useState, type ReactNode } from "react";
import { Check } from "lucide-react";
import { tokenize, type Language } from "../highlight";

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

// Combined inline alternation: **bold** | *italic* | ~~strike~~ | `code` | [text](url).
// Bold runs first so `*` inside bold is parsed by the recursive call.
// A shared /g regex would be corrupted by the recursion (a nested call
// resets lastIndex while an outer exec loop is mid-scan) — each call
// gets a fresh instance.
const INLINE_SOURCE =
  String.raw`(\*\*[\s\S]+?\*\*)|(\*[^*\n]+\*)|(~~[^~\n]+~~)|(` + "`" + `[^` + "`" + `\n]+` + "`" + `)|(\[([^]\n]+)\]\([^)\s]+\))`;

function parseInline(text: string, highlight: string | undefined, keyPrefix: string): ReactNode[] {
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
      nodes.push(<strong key={key}>{parseInline(m[1].slice(2, -2), highlight, key)}</strong>);
    } else if (m[2] !== undefined) {
      nodes.push(<em key={key}>{parseInline(m[2].slice(1, -1), highlight, key)}</em>);
    } else if (m[3] !== undefined) {
      // ~~strikethrough~~
      nodes.push(<del key={key}>{parseInline(m[3].slice(2, -2), highlight, key)}</del>);
    } else if (m[4] !== undefined) {
      nodes.push(
        <code key={key} className="bubble-inline-code">
          {highlightText(m[4].slice(1, -1), highlight, key)}
        </code>,
      );
    } else {
      nodes.push(renderLink(m[6], m[7], highlight, key));
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) {
    nodes.push(highlightText(text.slice(last), highlight, `${keyPrefix}-t${k++}`));
  }
  return nodes;
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
    <a key={key} href={url} target="_blank" rel="noreferrer">
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
    <div className="bubble-code-wrap" key={key}>
      <div className="bubble-code-header">
        {langLabel && <span className="bubble-code-lang">{langLabel}</span>}
        <button type="button" className="bubble-code-copy" onClick={copy} aria-label="Copy code">
          {copied ? <Check size={11} /> : "Copy"}
        </button>
      </div>
      <pre className="bubble-code">
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

function renderBlock(block: Block, index: number, highlight: string | undefined): ReactNode {
  const key = `b${index}`;
  switch (block.kind) {
    case "code": {
      return <CodeBlock key={key} block={block} highlight={highlight} index={index} />;
    }
    case "heading": {
      const Tag = `h${block.level}` as "h1" | "h2" | "h3" | "h4";
      return <Tag key={key}>{parseInline(block.text, highlight, key)}</Tag>;
    }
    case "ul":
      return (
        <ul key={key}>
          {block.items.map((item, ii) => {
            // GFM task list: - [ ] or - [x]
            const taskMatch = item.match(/^\[([ x])\]\s+(.*)$/i);
            if (taskMatch) {
              const checked = taskMatch[1].toLowerCase() === "x";
              return (
                <li key={ii} className="md-task-item">
                  <input type="checkbox" checked={checked} readOnly aria-label={`Task: ${taskMatch[2]}`} />
                  <span className={checked ? "md-task-done" : ""}>{parseInline(taskMatch[2], highlight, `${key}-${ii}`)}</span>
                </li>
              );
            }
            return <li key={ii}>{parseInline(item, highlight, `${key}-${ii}`)}</li>;
          })}
        </ul>
      );
    case "ol":
      return (
        <ol key={key}>
          {block.items.map((item, ii) => (
            <li key={ii}>{parseInline(item, highlight, `${key}-${ii}`)}</li>
          ))}
        </ol>
      );
    case "quote":
      return <blockquote key={key}>{parseInline(block.text, highlight, key)}</blockquote>;
    case "alert": {
      // GFM alert callout: colored left-border box matching the alert type.
      return (
        <div key={key} className={`md-alert md-alert-${block.alertType}`}>
          <div className="md-alert-label">{block.alertType.toUpperCase()}</div>
          <div className="md-alert-body">{parseInline(block.text, highlight, key)}</div>
        </div>
      );
    }
    case "hr":
      return <hr key={key} />;
    case "table": {
      return (
        <table key={key} className="md-table">
          <thead>
            <tr>
              {block.headers.map((h, hi) => (
                <th key={hi}>{parseInline(h, highlight, `${key}-h${hi}`)}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {block.rows.map((row, ri) => (
              <tr key={ri}>
                {block.headers.map((_, ci) => (
                  <td key={ci}>{parseInline(row[ci] ?? "", highlight, `${key}-r${ri}c${ci}`)}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      );
    }
    case "para":
      return <p key={key}>{parseInline(block.text, highlight, key)}</p>;
  }
}

export default memo(function Markdown({ content, className, highlight }: Props) {
  const blocks = parseBlocks(content);
  return (
    <div className={className !== undefined ? `markdown ${className}` : "markdown"}>
      {blocks.map((b, bi) => renderBlock(b, bi, highlight))}
    </div>
  );
});
