// P1.1 (docs/design/adoption-lock.md): palette journal search support.
// JournalHit tags a daemon search row with the project it came from — the
// palette searches EVERY registered project (one read-only search_events
// invoke each) and App's foreign-switch path needs the owning root to
// one-flight (bootstrap target root + workstream in a single roundtrip,
// the Sidebar.tsx:374-382 contract).
import type { SearchResult } from "./types";

export interface JournalHit {
  // Project the hit's daemon answered for; handleSwitchWorkstream treats
  // root !== activeProjectRoot as a foreign switch.
  root: string;
  projectName: string;
  result: SearchResult;
}

// Cap the palette group — the daemon returns up to 100 per project and the
// palette is a launcher, not a browser.
export const JOURNAL_HIT_CAP = 8;

// Merge per-project buckets into one newest-first list (daemon order is
// per-project; created_at compare mirrors store.SearchEvents ORDER BY).
export function mergeHits(buckets: JournalHit[][]): JournalHit[] {
  return buckets
    .flat()
    .sort((a, b) => b.result.event.created_at.localeCompare(a.result.event.created_at))
    .slice(0, JOURNAL_HIT_CAP);
}

// Single-line excerpt around the first query hit. Payloads are JSON in the
// journal; prefer the semantic text fields over a raw stringify so the row
// reads like prose, then window ±radius chars around the match.
export function snippetFor(payload: unknown, query: string, radius = 40): string {
  const p = (payload ?? {}) as Record<string, unknown>;
  const primary =
    (typeof p.text === "string" && p.text) ||
    (typeof p.summary === "string" && p.summary) ||
    (typeof p.error === "string" && p.error) ||
    (typeof p.result === "string" && p.result) ||
    JSON.stringify(p);
  const flat = primary.replace(/\s+/g, " ").trim();
  const idx = flat.toLowerCase().indexOf(query.trim().toLowerCase());
  if (idx < 0 || flat.length <= radius * 2) return flat.slice(0, radius * 2) + (flat.length > radius * 2 ? "…" : "");
  const start = Math.max(0, idx - radius);
  const end = Math.min(flat.length, idx + query.length + radius);
  return (start > 0 ? "…" : "") + flat.slice(start, end) + (end < flat.length ? "…" : "");
}
