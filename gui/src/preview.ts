// P2.1–P2.3 (docs/design/adoption-lock.md): pure preview helpers shared by
// MessageBubble (P2.1 inline images + Open-live affordances) and
// PreviewPanel (P2.2 file mode / P2.3 live mode). No DOM, no React.
//
// Design locks (2026-08-29):
//  - frame-src is limited to http(s)://localhost[:*], 127.0.0.1[:*], and
//    [::1][:*] — exact host match via WHATWG URL parsing; every other URL
//    renders a blocked note and is NEVER loaded.
//  - Inline image bytes ride read_file's forward-compat file_data_base64
//    field and are capped at PREVIEW_IMAGE_CAP decoded bytes; today's daemon
//    rejects binary reads outright, so every consumer degrades to a chip +
//    "Open" affordance when the field is absent.
//  - SVG IS treated as an inline image: rendering happens through <img>,
//    and an <img> context never executes scripts (unlike an inline <svg>
//    DOM insert) — same posture as Markdown's renderImage.

import type { ReadFileResponse } from "./api";
import { basename } from "./files";

// The panel's one preview target: a project file (read_file-backed) or a
// URL (localhost-gated sandboxed iframe). ChatSurface/App produce these.
export type PreviewTarget = { kind: "file"; path: string } | { kind: "url"; url: string };

// Raw decoded-byte cap for inline image rendering (spec: "capped at 2MB").
export const PREVIEW_IMAGE_CAP = 2 * 1024 * 1024;

// Extensions treated as inline-renderable images (case-insensitive). SVG is
// included deliberately — see the header note on <img> script safety.
export const IMAGE_EXTENSIONS = [".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg"] as const;

export function isImagePath(path: string): boolean {
  const lower = path.toLowerCase();
  return IMAGE_EXTENSIONS.some((ext) => lower.endsWith(ext));
}

// Path shape in arbitrary tool-result prose: POSIX (a/b.png), Windows
// (C:\shots\a.png), home (~/x.png), and bare filenames (shot.png). The `:`
// is admitted for drive letters; matches containing "://" (URLs) are
// filtered afterwards — URLs are live-preview territory, not byte loads.
// Run ends at a word boundary so trailing punctuation (shot.png, "shot.png.")
// is excluded from the ref.
const IMAGE_REF_RE = /[A-Za-z0-9_.~\\/:-]+\.(?:png|jpe?g|gif|webp|svg)\b/gi;

// Image file references inside free text, deduped in order of appearance.
export function findImageRefs(text: string): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const m of text.matchAll(IMAGE_REF_RE)) {
    const ref = m[0];
    if (ref.includes("://")) continue;
    if (seen.has(ref)) continue;
    seen.add(ref);
    out.push(ref);
  }
  return out;
}

// `(` and `[` stay in the match and are trimmed below — markdown links and
// parenthesized prose both wrap URLs that way.
const HTTP_URL_RE = /https?:\/\/[^\s"'`<>]+/gi;
// Trailing punctuation that almost never belongs to the URL itself.
const TRAILING_URL_PUNCT = /[.,;:!?)\]}>'"+*~\\]+$/;

// http(s) URLs inside free text, trailing-punctuation-stripped and deduped
// in order of appearance.
export function extractHttpUrls(text: string): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const m of text.matchAll(HTTP_URL_RE)) {
    const url = m[0].replace(TRAILING_URL_PUNCT, "");
    if (url === "" || seen.has(url)) continue;
    seen.add(url);
    out.push(url);
  }
  return out;
}

// The P2.3 design-lock frame-src gate: http(s) only, hostname EXACTLY
// localhost / 127.0.0.1 / [::1] (any port or none). "localhost.evil.com",
// "0.0.0.0", file:/javascript: and friends all fail. URL parsing lowercases
// scheme+host and keeps IPv6 brackets, so the comparison stays exact.
export function isLocalPreviewUrl(url: string): boolean {
  try {
    const u = new URL(url);
    if (u.protocol !== "http:" && u.protocol !== "https:") return false;
    const host = u.hostname;
    return host === "localhost" || host === "127.0.0.1" || host === "[::1]";
  } catch {
    return false;
  }
}

// MIME fallbacks by extension (the daemon's file_mime wins when present).
const EXT_MIME: Record<string, string> = {
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".svg": "image/svg+xml",
};

// Build a data: URL from read_file's forward-compat byte payload. Null when
// bytes are absent (today's daemon never sends them for binary rejects) or
// the decoded-size estimate exceeds PREVIEW_IMAGE_CAP — callers fall back to
// the chip affordance in both cases. b64len*3/4 over-estimates by the
// padding width, so the cap check errs on the safe (reject) side.
export function imageDataUrl(resp: ReadFileResponse, path: string): string | null {
  const b64 = resp.file_data_base64;
  if (b64 == null || b64 === "") return null;
  if (Math.floor((b64.length * 3) / 4) > PREVIEW_IMAGE_CAP) return null;
  const mime = resp.file_mime || EXT_MIME[path.slice(Math.max(path.lastIndexOf("."), 0)).toLowerCase()] || "image/png";
  return `data:${mime};base64,${b64}`;
}

// Same known-text-extension set as Markdown's code-span path gate (one
// convention — a chat path chip and a tool-arg button must agree).
const TEXT_EXT_RE = /\.(go|rs|ts|tsx|js|jsx|py|md|json|toml|yaml|yml|sh|sql|css|html|txt|lock|mod|sum|proto|gradle)$/i;

// Chat→panel gate (P2.2): does this tool-arg VALUE look file-path-like —
// contains a POSIX/Windows separator, or a bare filename with a known text
// extension? URLs (scheme://) and whitespace-bearing strings are excluded;
// a trailing :line or :line-range is stripped before judging, mirroring
// Markdown's CodeSpan posture.
export function looksLikeFilePath(value: unknown): boolean {
  if (typeof value !== "string") return false;
  const t = value.trim();
  if (t.length < 2 || /\s/.test(t)) return false;
  if (/^[a-z]+:\/\//i.test(t)) return false;
  const stripped = t.replace(/:\d+(-\d+)?$/, "");
  if (stripped.includes("/") || stripped.includes("\\")) return true;
  return TEXT_EXT_RE.test(basename(stripped));
}

// Header label for a preview target: the file's basename (the header also
// carries the resolved full path) or the URL verbatim.
export function previewTargetLabel(target: PreviewTarget): string {
  return target.kind === "file" ? basename(target.path) : target.url;
}
