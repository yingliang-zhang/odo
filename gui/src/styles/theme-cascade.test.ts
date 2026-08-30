import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// P3 adoption-lock: the theme cascade is PRIMITIVE layer → SEMANTIC layer →
// [data-theme="light"] var() re-references. Only the primitive layer (the
// top of :root) may hold #hex/rgb() literals; every light-theme declaration
// is a pure var() hop. This test locks that boundary plus the 1:1 computed
// values the refactor promised not to move.
const APP_CSS = readFileSync("src/styles/app.css", "utf8"); // vitest root = gui/
// Comment-stripped copy for structural scans — a comment at the
// message-stream section names [data-theme="light"] without being a rule.
const STRIPPED = APP_CSS.replace(/\/\*[\s\S]*?\*\//g, "");

const HEX = /#[0-9a-fA-F]{3,8}\b/;
const RGB = /\brgba?\(/;

/** Brace-matched body of the first rule whose selector contains `needle`. */
function ruleBody(css: string, needle: string): string {
  const rule = collectRules(css).find((r) => r.selector.includes(needle));
  if (!rule) throw new Error(`no rule matching ${needle}`);
  return rule.body;
}

/** Top-level (selector, body) pairs; @-blocks are consumed as opaque spans. */
function collectRules(css: string): Array<{ selector: string; body: string }> {
  const rules: Array<{ selector: string; body: string }> = [];
  let i = 0;
  while (i < css.length) {
    const open = css.indexOf("{", i);
    if (open === -1) break;
    const selector = css.slice(i, open).trim();
    let depth = 1;
    let j = open + 1;
    for (; j < css.length; j++) {
      if (css[j] === "{") depth++;
      else if (css[j] === "}") {
        depth--;
        if (depth === 0) break;
      }
    }
    if (depth !== 0) throw new Error(`unbalanced braces after ${selector}`);
    rules.push({ selector, body: css.slice(open + 1, j) });
    i = j + 1;
  }
  return rules;
}

/** `--name: value` pairs from a rule body that holds only custom props. */
function declarations(body: string): Array<[string, string]> {
  return body
    .split(";")
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => {
      const m = s.match(/^(--[\w-]+)\s*:\s*([\s\S]+)$/);
      if (!m) throw new Error(`not a custom-property declaration: ${s}`);
      return [m[1], m[2].trim()];
    });
}

// statusbar.test.tsx CSSOM pattern: inject app.css into the jsdom CSSOM and
// assert on rule declarations (cssstyle does not resolve var() to colors).
function appCssRules(): CSSStyleRule[] {
  const style = document.createElement("style");
  style.id = "app-css-test-injection";
  style.textContent = APP_CSS;
  document.head.appendChild(style);
  const rules = [...(style.sheet?.cssRules ?? [])].filter(
    (r): r is CSSStyleRule => r instanceof CSSStyleRule,
  );
  style.remove();
  return rules;
}
const findRule = (rules: CSSStyleRule[], sel: string): CSSStyleRule | undefined =>
  rules.find((r) => r.selectorText.replace(/\s+/g, " ").trim() === sel);

describe("P3 theme cascade — primitive/semantic/light layers", () => {
  it("(a) the light token block holds zero literals; every declaration is a pure var() hop", () => {
    const body = ruleBody(STRIPPED, '[data-theme="light"]');
    expect(body).not.toMatch(HEX);
    expect(body).not.toMatch(RGB);
    const decls = declarations(body);
    expect(decls.length).toBe(37); // the full theme-switch surface
    for (const [name, value] of decls) {
      expect(value, `${name} must be a var() re-reference`).toMatch(/^var\(--/);
    }
  });

  it("(b) every [data-theme=light] rule — token block and component overrides — is literal-free", () => {
    const rules = collectRules(STRIPPED).filter((r) =>
      r.selector.includes('[data-theme="light"]'),
    );
    // Lock the exact override surface: token block, .daemon-down-banner,
    // .ws-parked-pill, .bubble-agent a, .risk-high, .failure-overlay.
    expect(rules.length).toBe(6);
    for (const selector of ["daemon-down-banner", "ws-parked-pill", "risk-high", "failure-overlay"]) {
      expect(
        rules.some((r) => r.selector.includes(selector)),
        `missing [data-theme="light"] .${selector} override`,
      ).toBe(true);
    }
    for (const r of rules) {
      expect(r.body, r.selector).not.toMatch(HEX);
      expect(r.body, r.selector).not.toMatch(RGB);
    }
  });

  it("(c) light overrides are a subset of :root token names and every hop resolves", () => {
    const rootDecls = new Map(declarations(ruleBody(STRIPPED, ":root")));
    const lightDecls = declarations(ruleBody(STRIPPED, '[data-theme="light"]'));
    for (const [name, value] of lightDecls) {
      expect(rootDecls.has(name), `orphan light override ${name}`).toBe(true);
      const hop = value.match(/^var\((--[\w-]+)\)$/);
      expect(hop, `${name}: ${value} must be a single var() hop`).toBeTruthy();
      expect(rootDecls.has(hop![1]), `${name} references undeclared ${hop![1]}`).toBe(true);
    }
  });

  it("(c) the previously component-literal colors live exactly once, inside the primitive layer", () => {
    const primStart = APP_CSS.indexOf("cascade (1/3): PRIMITIVES");
    const primEnd = APP_CSS.indexOf("cascade (2/3): SEMANTIC");
    expect(primStart).toBeGreaterThan(-1);
    expect(primEnd).toBeGreaterThan(primStart);
    // The literals the component-level light overrides used to carry
    // (.daemon-down-banner/.failure-overlay, .ws-parked-pill, .risk-high).
    for (const literal of ["#92400e", "#fff", "#b91c1c"]) {
      const hits = [...APP_CSS.matchAll(new RegExp(`${literal}\\b`, "g"))].map((m) => m.index!);
      expect(hits.length, `${literal} must appear exactly once`).toBe(1);
      expect(hits[0], `${literal} must live in the primitive layer`).toBeGreaterThan(primStart);
      expect(hits[0], `${literal} must live in the primitive layer`).toBeLessThan(primEnd);
      const line = APP_CSS.slice(APP_CSS.lastIndexOf("\n", hits[0]) + 1, APP_CSS.indexOf("\n", hits[0]));
      expect(line, `${literal} must be declared on a --p- primitive`).toMatch(/^\s*--p-/);
    }
  });

  it("(d) CSSOM: :root and light --bg resolve through one primitive hop", () => {
    const rules = appCssRules();
    const root = findRule(rules, ":root");
    const light = findRule(rules, '[data-theme="light"]');
    expect(root).toBeTruthy();
    expect(light).toBeTruthy();
    // One var() hop: token → primitive → literal.
    for (const [rule, expected] of [
      [root, "#0f1115"],
      [light, "#f5f5f5"],
    ] as const) {
      const value = rule!.style.getPropertyValue("--bg").trim();
      const hop = value.match(/^var\((--[\w-]+)\)$/);
      expect(hop, `--bg must be a var() hop, got ${value}`).toBeTruthy();
      expect(root!.style.getPropertyValue(hop![1]).trim()).toBe(expected);
    }
  });

  it("(e) computed palette parity — resolved values are the pre-refactor colors", () => {
    const rootDecls = new Map(declarations(ruleBody(STRIPPED, ":root")));
    const lightDecls = new Map([...rootDecls, ...declarations(ruleBody(STRIPPED, '[data-theme="light"]'))]);
    const resolve = (decls: Map<string, string>, name: string): string => {
      let value = decls.get(name) ?? "";
      for (let i = 0; i < 10 && /var\(--/.test(value); i++) {
        value = value.replace(/var\((--[\w-]+)\)/g, (_all, dep: string) => {
          const resolved = decls.get(dep);
          if (resolved === undefined) throw new Error(`unresolvable ${dep} from ${name}`);
          return resolved;
        });
      }
      return value;
    };
    // Sampled tokens across every primitive family — the rows the refactor
    // promised to leave computed-identical.
    const expected: Array<[string, string, string]> = [
      // token, dark, light
      ["--bg", "#0f1115", "#f5f5f5"],
      ["--bg-raised", "#1a1d24", "#fff"],
      ["--text", "#d8dce4", "#1a1a1a"],
      ["--text-dim", "#7d8593", "#666"],
      ["--accent-user", "#0A84FF", "#0066CC"],
      ["--bg-run", "#a78bfa", "#7c3aed"],
      ["--ok", "#3fa35f", "#16a34a"],
      ["--err", "#c34a4a", "#dc2626"],
      ["--err-text", "#e08a8a", "#b91c1c"],
      ["--ok-text", "#6fd39f", "#15803d"],
      ["--warn-text", "#e8c876", "#a16207"],
      ["--bg-tertiary", "#2a2e37", "#e8e8e8"],
      ["--code-chip-bg", "rgba(0, 0, 0, 0.35)", "rgba(0, 0, 0, 0.07)"],
      [
        "--shadow-soft",
        "0 2px 8px rgba(0, 0, 0, 0.25), 0 0 1px rgba(0, 0, 0, 0.15)",
        "0 2px 8px rgba(0, 0, 0, 0.08), 0 0 1px rgba(0, 0, 0, 0.10)",
      ],
      [
        "--stroke-primary",
        "color-mix(in srgb, #636d83 28%, transparent)",
        "color-mix(in srgb, #636d83 40%, transparent)",
      ],
    ];
    for (const [name, dark, light] of expected) {
      expect(resolve(rootDecls, name), `${name} (dark)`).toBe(dark);
      expect(resolve(lightDecls, name), `${name} (light)`).toBe(light);
    }
  });
});
