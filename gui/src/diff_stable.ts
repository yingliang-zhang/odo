import type { Diff } from "./types";

// (tri-review P1 #4, 2026-08-24) Reference stabilization for the 350 ms
// poll loop: the daemon's JSON deserializes fresh Diff / Diff[] references
// every tick even when nothing changed, and every setState holding a new
// reference re-rendered the whole App subtree (ChatSurface's runGroups
// tree included). The poll handler routes its diff updates through these
// comparators and keeps the previous reference when the content is
// identical, so React's Object.is bailout skips the render entirely on
// quiet ticks — the same pattern App already applies to `preview` and
// `panel_progress`, factored out here for unit tests.
//
// The compared fields are the COMPLETE Diff wire shape (types.ts): id +
// status + path + content. A same-id re-patch with different content
// therefore still lands as a new reference; ordering of the array is
// significant (the daemon's list order is the render order).

export function sameDiff(a: Diff | null | undefined, b: Diff | null | undefined): boolean {
  if (a == null || b == null) return a === b;
  return (
    a.id === b.id &&
    a.status === b.status &&
    a.path === b.path &&
    a.content === b.content
  );
}

export function sameDiffList(a: Diff[], b: Diff[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (!sameDiff(a[i], b[i])) return false;
  }
  return true;
}
