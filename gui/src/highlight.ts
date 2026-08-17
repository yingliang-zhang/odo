// Dependency-free, regex-based token highlighting for the diff viewer.
// One combined alternation per language: at each position the first
// alternative wins, so comments beat strings and strings beat keywords.
// Token classes map onto the .tok-* CSS rules in styles/app.css.

export type Language = "go" | "rust" | "ts" | "python" | "bash" | "json" | "yaml";

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
  bash: "break case continue do done elif else esac exit fi for function if in return then until while".split(" "),
  json: [],  // JSON has no keywords — only strings, numbers, booleans
  yaml: "true false null yes no on off".split(" "),
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
  bash: {
    "tok-comment": String.raw`#[^\n]*`,
    "tok-string": String.raw`"(?:\\.|[^"\\\n])*"|'(?:[^'\\\n])*'`,
    "tok-number": String.raw`\b\d+(?:\.\d+)?\b`,
    "tok-fn": String.raw`\b[A-Za-z_]\w*(?=\s*\()`,
  },
  json: {
    "tok-comment": String.raw``,  // JSON has no comments
    "tok-string": String.raw`"(?:\\.|[^"\\\n])*"`,
    "tok-number": String.raw`\b(?:-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null)\b`,
    "tok-fn": String.raw``,
  },
  yaml: {
    "tok-comment": String.raw`#[^\n]*`,
    "tok-string": String.raw`"(?:\\.|[^"\\\n])*"|'(?:[^'\\\n])*'`,
    "tok-number": String.raw`\b\d+(?:\.\d+)?\b`,
    "tok-fn": String.raw``,
  },
};

// One regex per language, built lazily. Fixed Language keys → Record.
interface CompiledPattern {
  re: RegExp;
  classes: TokenClass[]; // capture group i+1 → its class
}
const regexCache: Partial<Record<Language, CompiledPattern>> = {};

// One regex per language: the alternation is ordered — first match at a
// position wins — so `// "not a string"` highlights as a comment. Empty
// fragments (json has no comments/keywords/fns) are SKIPPED, not kept as
// `()` groups: an empty group matches zero-width before every real token
// and would emit a garbage empty-classified token per position.
function patternFor(lang: Language): CompiledPattern {
  let entry = regexCache[lang];
  if (!entry) {
    const p = PARTS[lang];
    const frags: string[] = [];
    const classes: TokenClass[] = [];
    const push = (frag: string, cls: TokenClass) => {
      if (frag === "") return;
      frags.push(`(${frag})`);
      classes.push(cls);
    };
    push(p["tok-comment"], "tok-comment");
    push(p["tok-string"], "tok-string");
    push(p["tok-number"], "tok-number");
    const kw = KEYWORDS[lang].join("|");
    if (kw !== "") {
      frags.push(`(\\b(?:${kw})\\b)`);
      classes.push("tok-keyword");
    }
    push(p["tok-fn"], "tok-fn");
    entry = { re: new RegExp(frags.join("|"), "g"), classes };
    regexCache[lang] = entry;
  }
  entry.re.lastIndex = 0;
  return entry;
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
  ".sh": "bash",
  ".bash": "bash",
  ".zsh": "bash",
  ".json": "json",
  ".yaml": "yaml",
  ".yml": "yaml",
  ".toml": "yaml",  // close enough for keyword highlighting
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
  const { re, classes } = patternFor(lang);
  const tokens: Token[] = [];
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(line)) !== null) {
    if (m.index > last) {
      pushMerged(tokens, { text: line.slice(last, m.index), cls: null });
    }
    let cls: TokenClass = "tok-fn"; // unreachable: a match implies one group matched
    for (let g = 0; g < classes.length; g++) {
      if (m[g + 1] !== undefined) {
        cls = classes[g];
        break;
      }
    }
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
