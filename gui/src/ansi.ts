// Odo DX wave (Feature 4): dependency-free SGR (ANSI escape) → inline-style
// HTML converter for agent_text bubbles. OMP --mode json tool results carry
// colored test output; rendering the raw codes made bubbles unreadable.
//
// Security contract: the plain-text chunks are HTML-escaped (& < >) BEFORE
// the style spans are inserted, and the only markup ever emitted is
// <span style="...">/style-only attributes built from the fixed tables
// below — user text can never inject a tag (MessageBubble mounts the
// result via dangerouslySetInnerHTML, so this module is the one choke
// point). Fast path: text without "\x1b[" returns untouched (the common
// case costs one includes()).
//
// Scope (brief): basic fg 30–37 / bg 40–47, bright 90–97 / 100–107, bold
// (1) = font-weight 600, dim (2) = opacity 0.6, reset (0) closes the
// span; 256-color (38;5;N / 48;5;N) and truecolor (38;2;R;G;B /
// 48;2;R;G;B). Every other sequence (underline, cursor moves, …) is
// stripped silently.

// Tango/xterm palette — the classic terminal defaults; bright variants for
// 90–97 (and the 256 table's 8–15 range). Elements indexed by code offset.
const BASE_COLORS = [
  "#2e3436", "#cc0000", "#4e9a06", "#c4a000",
  "#3465a4", "#75507b", "#06989a", "#d3d7cf",
];
const BRIGHT_COLORS = [
  "#555753", "#ef2929", "#8ae234", "#fce94f",
  "#729fcf", "#ad7fa8", "#34e2e2", "#eeeeec",
];

// 256-color cube levels (xterm): indices 16–231 map r/g/b each 0–5.
const CUBE_LEVELS = [0, 95, 135, 175, 215, 255];

function hex2(n: number): string {
  const s = Math.max(0, Math.min(255, Math.round(n))).toString(16);
  return s.length === 1 ? `0${s}` : s;
}

// xterm 256-color index → "#rrggbb": 0–7 base, 8–15 bright, 16–231 the
// 6×6×6 cube, 232–255 the 24-step gray ramp.
function hex256(n: number): string {
  if (n < 0) n = 0;
  if (n < 8) return BASE_COLORS[n];
  if (n < 16) return BRIGHT_COLORS[n - 8];
  if (n > 255) n = 255;
  if (n < 232) {
    const c = n - 16;
    const r = Math.floor(c / 36), g = Math.floor((c % 36) / 6), b = c % 6;
    return `#${hex2(CUBE_LEVELS[r])}${hex2(CUBE_LEVELS[g])}${hex2(CUBE_LEVELS[b])}`;
  }
  const v = 8 + (n - 232) * 10;
  return `#${hex2(v)}${hex2(v)}${hex2(v)}`;
}

function escapeHtml(text: string): string {
  return text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

// One open span's style at this point in the stream. fg/bg are resolved
// hex strings; absent = inherit (the default terminal colors).
interface AnsiStyle {
  bold: boolean;
  dim: boolean;
  fg: string | null;
  bg: string | null;
}

const RESET_STYLE: AnsiStyle = { bold: false, dim: false, fg: null, bg: null };

function styleCss(s: AnsiStyle): string {
  const rules: string[] = [];
  if (s.bold) rules.push("font-weight:600");
  if (s.dim) rules.push("opacity:0.6");
  if (s.fg !== null) rules.push(`color:${s.fg}`);
  if (s.bg !== null) rules.push(`background-color:${s.bg}`);
  return rules.join(";");
}

// Parse one SGR parameter list (e.g. "1;38;5;196" or "" — empty = reset),
// folding it into the running style. Extended-color descriptors consume
// their following parameters in place.
function applySgr(state: AnsiStyle, params: string): AnsiStyle {
  const codes = params === "" ? [0] : params.split(";").map((c) => (c === "" ? 0 : parseInt(c, 10)));
  const next: AnsiStyle = { ...state };
  for (let i = 0; i < codes.length; i++) {
    const code = codes[i];
    if (Number.isNaN(code)) continue; // garbage param — strip, keep state
    if (code === 0) {
      next.bold = false;
      next.dim = false;
      next.fg = null;
      next.bg = null;
    } else if (code === 1) {
      next.bold = true;
    } else if (code === 2) {
      next.dim = true;
    } else if (code === 22) {
      // Terminal semantics: 22 clears BOTH weight and faint.
      next.bold = false;
      next.dim = false;
    } else if (code >= 30 && code <= 37) {
      next.fg = BASE_COLORS[code - 30];
    } else if (code >= 90 && code <= 97) {
      next.fg = BRIGHT_COLORS[code - 90];
    } else if (code >= 40 && code <= 47) {
      next.bg = BASE_COLORS[code - 40];
    } else if (code >= 100 && code <= 107) {
      next.bg = BRIGHT_COLORS[code - 100];
    } else if (code === 39) {
      next.fg = null; // default fg
    } else if (code === 49) {
      next.bg = null; // default bg
    } else if (code === 38 || code === 48) {
      const isFg = code === 38;
      const mode = codes[i + 1];
      if (mode === 5 && typeof codes[i + 2] === "number") {
        const color = hex256(codes[i + 2]);
        if (isFg) next.fg = color; else next.bg = color;
        i += 2;
      } else if (
        mode === 2 &&
        typeof codes[i + 2] === "number" &&
        typeof codes[i + 3] === "number" &&
        typeof codes[i + 4] === "number"
      ) {
        const color = `#${hex2(codes[i + 2])}${hex2(codes[i + 3])}${hex2(codes[i + 4])}`;
        if (isFg) next.fg = color; else next.bg = color;
        i += 4;
      }
      // Malformed extended descriptor (truncated params): strip silently.
    }
    // All other codes (underline 4, reverse 7, …): ignored — stripped.
  }
  return next;
}

// Convert SGR-colored text to an HTML string whose <span style> runs carry
// the colors/weights; plain text is entity-escaped. Text without ESC
// returns unchanged (value identity — callers stay on the fast path).
export function renderAnsi(text: string): string {
  if (!text.includes("\x1b[")) return text;
  let html = "";
  let style: AnsiStyle = { ...RESET_STYLE };
  let spanOpen = false;
  let last = 0;
  const re = /\x1b\[([0-9;]*)m/g;
  const closeSpan = () => {
    if (spanOpen) {
      html += "</span>";
      spanOpen = false;
    }
  };
  let m: RegExpExecArray | null;
  while ((m = re.exec(text)) !== null) {
    html += escapeHtml(text.slice(last, m.index));
    last = re.lastIndex;
    style = applySgr(style, m[1]);
    // Flush the previous run and open the new one (only when styled — a
    // reset at plain state emits no empty span).
    closeSpan();
    const css = styleCss(style);
    if (css !== "") {
      html += `<span style="${css}">`;
      spanOpen = true;
    }
  }
  html += escapeHtml(text.slice(last));
  closeSpan();
  return html;
}
