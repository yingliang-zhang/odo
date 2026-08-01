// Dependency-free, regex-based token highlighting for the diff viewer.
// One combined alternation per language: at each position the first
// alternative wins, so comments beat strings and strings beat keywords.
// Token classes map onto the .tok-* CSS rules in styles/app.css.

export type Language = "go" | "rust" | "ts" | "python";

export type TokenClass =
  | "tok-comment"
  | "tok-string"
  | "tok-number"
  | "tok-keyword"
  | "tok-fn";

export interface Token {
  text: string;
  cls: TokenClass | null; // null = unstyled source text
}

const KEYWORDS: Record<Language, string[]> = {
  go: "break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var".split(" "),
  rust: "as async await break const continue crate dyn else enum extern false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while".split(" "),
  ts: "abstract any as async await boolean break case catch class const continue debugger declare default delete do else enum export extends false finally for from function get if implements import in instanceof interface keyof let namespace never new null number of private protected public readonly return set static string super unknown switch this throw true try type typeof undefined var void while with yield".split(" "),
  python: "False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield".split(" "),
};

// Per-language pattern fragments. Order in the alternation is precedence.
// Keywords are assembled from KEYWORDS in patternFor, so they are absent here.
const PARTS: Record<Language, Record<Exclude<TokenClass, "tok-keyword">, string>> = {
  go: {
    "tok-comment": String.raw`//[^\n]*|/\*.*?\*/`,
    "tok-string": String.raw`"(?:\\.|[^"\\\n])*"|` + "`[^`]*`",
    "tok-number": String.raw`\b(?:0x[0-9a-fA-F_]+|\d[\d_]*(?:\.\d+)?)\b`,
    "tok-fn": String.raw`\b[A-Za-z_]\w*(?=\s*\()`,
  },
  rust: {
    "tok-comment": String.raw`//[^\n]*|/\*.*?\*/`,
    "tok-string": String.raw`"(?:\\.|[^"\\\n])*"`,
    "tok-number": String.raw`\b(?:0x[0-9a-fA-F_]+|\d[\d_]*(?:\.\d+)?)\b`,
    "tok-fn": String.raw`\b[A-Za-z_]\w*(?=\s*\()|macro_rules!|println!|format!|vec!`,
  },
  ts: {
    "tok-comment": String.raw`//[^\n]*|/\*.*?\*/`,
    "tok-string": String.raw`"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'|` + "`(?:\\\\.|[^`\\\\])*`",
    "tok-number": String.raw`\b(?:0x[0-9a-fA-F]+|\d+(?:\.\d+)?)\b`,
    "tok-fn": String.raw`\b[A-Za-z_$][\w$]*(?=\s*\()`,
  },
  python: {
    "tok-comment": String.raw`#[^\n]*`,
    "tok-string": String.raw`"""[\s\S]*?"""|"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'`,
    "tok-number": String.raw`\b(?:0x[0-9a-fA-F]+|\d+(?:\.\d+)?)\b`,
    "tok-fn": String.raw`\b[A-Za-z_]\w*(?=\s*\()`,
  },
};

// One regex per language, built lazily. Fixed Language keys → Record.
const regexCache: Partial<Record<Language, RegExp>> = {};

// One regex per language: group 1 comment, 2 string, 3 number, 4 keyword,
// 5 function call. The alternation is ordered — first match at a position
// wins — so `// "not a string"` highlights as a comment.
function patternFor(lang: Language): RegExp {
  let re = regexCache[lang];
  if (!re) {
    const p = PARTS[lang];
    const kw = KEYWORDS[lang].join("|");
    re = new RegExp(
      `(${p["tok-comment"]})|(${p["tok-string"]})|(${p["tok-number"]})|(\\b(?:${kw})\\b)|(${p["tok-fn"]})`,
      "g",
    );
    regexCache[lang] = re;
  }
  re.lastIndex = 0;
  return re;
}

const EXT_TO_LANGUAGE: Record<string, Language> = {
  ".go": "go",
  ".rs": "rust",
  ".ts": "ts",
  ".tsx": "ts",
  ".mts": "ts",
  ".cts": "ts",
  ".js": "ts",
  ".jsx": "ts",
  ".mjs": "ts",
  ".cjs": "ts",
  ".py": "python",
  ".pyi": "python",
};

// Language hint from a file path (diff `+++ b/path` or `diff --git` lines).
export function languageFromPath(path: string): Language | null {
  const dot = path.lastIndexOf(".");
  if (dot < 0) return null;
  return EXT_TO_LANGUAGE[path.slice(dot).toLowerCase()] ?? null;
}

// Split one source line into styled tokens. Consecutive unstyled runs are
// merged so callers emit few spans. Lines longer than needed are rare; the
// regex is linear against a single line, so cost stays trivial.
export function tokenize(line: string, lang: Language | null): Token[] {
  if (lang === null || line === "") {
    return [{ text: line, cls: null }];
  }
  const re = patternFor(lang);
  const tokens: Token[] = [];
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(line)) !== null) {
    if (m.index > last) {
      pushMerged(tokens, { text: line.slice(last, m.index), cls: null });
    }
    const cls: TokenClass =
      m[1] !== undefined
        ? "tok-comment"
        : m[2] !== undefined
          ? "tok-string"
          : m[3] !== undefined
            ? "tok-number"
            : m[4] !== undefined
              ? "tok-keyword"
              : "tok-fn";
    tokens.push({ text: m[0], cls });
    last = m.index + m[0].length;
    if (m[0] === "") re.lastIndex++; // guard: zero-width match
  }
  if (last < line.length) {
    pushMerged(tokens, { text: line.slice(last), cls: null });
  }
  return tokens;
}

function pushMerged(tokens: Token[], tok: Token) {
  const prev = tokens[tokens.length - 1];
  if (prev && prev.cls === tok.cls && tok.cls === null) {
    prev.text += tok.text;
  } else {
    tokens.push(tok);
  }
}
