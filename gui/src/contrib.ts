// P3.4 (docs/design/adoption-lock.md): the panel contribution registry.
// The right panel's tab strip used to be a hardcoded TABS table inside
// ContextPanel with five per-tab badge props threaded from App (one of
// them — ledgerBadge — dead at every call site). Panels are now declared
// HERE as data: id, title, icon, and the badge derivation. ContextPanel
// renders the strip from this table and `PanelTab` derives FROM it, so a
// future panel (e.g. OMP stats) is one registry entry away from appearing
// in the strip — the seam the adoption lock reserves.
//
// What the registry deliberately does NOT carry: the tab BODY (render
// function / component). Bodies are prop-heavy and heterogeneous — DiffViewer
// needs diffs+handlers, WikiBrowser a conversation id and focus nonce, Runs
// the journaled events — and they mount through App's keep-alive/LRU-park
// wrapper (hidden divs, draft-exemption), which contributes layout and
// lifecycle, not just components. A `component` field here would be dead
// metadata drifting beside App's real wiring, so the body seam stays in
// App.tsx's children block, which references this table via the mount set.
//
// Internal-only per the lock: no external plugin loading. Same pattern as
// keybinds.ts (static table > per-call-site strings).

import type { LucideIcon } from "lucide-react";
import {
  BookMarked,
  BookOpen,
  Eye,
  FileText,
  GitCompareArrows,
  GraduationCap,
  History,
  Inbox,
  ListChecks,
  MapPin,
} from "lucide-react";

// Raw counts App computes from its own state; the registry derives each
// tab's badge FROM this (the derivation used to live inline in App's JSX:
// e.g. `diffs.length > 0 ? diffs.length : undefined`).
export interface PanelBadgeInput {
  // Pending diffs in this conversation's Changes tab.
  pendingDiffs: number;
  // Cross-workstream pending-review rows (ReviewInbox total).
  pendingReview: number;
  // Wiki note count from pending_counts — null = unavailable (no badge).
  wikiNotes: number | null;
  // Unapplied memory proposals.
  memoryProposals: number;
  // Open (visible, unswept) todo items from the lifted deriveTodoState —
  // UX-1 D2: the chip and the Tasks tab read one derive, so the badge
  // counts exactly what the tabs render.
  openTodos: number;
}

// Zero state renders no badge — every derivation funnels through this so
// the rule lives in one place (the old inline `> 0 ?` per JSX prop).
const positive = (n: number): number | undefined => (n > 0 ? n : undefined);

export interface PanelContribution {
  // Stable id; PanelTab derives from these, so renaming is a compile-
  // breaking change across App/StatusBar. Persisted in localStorage
  // ("odo-panel-tab") — renaming orphans stored selections.
  id: string;
  // Strip label — a tab renders `<icon> <title> [parked] [badge]`.
  title: string;
  // Lucide glyph, rendered at size 12 by ContextPanel.
  icon: LucideIcon;
  // Badge derivation (null/undefined = no badge — read via badgeFor).
  // Entries without one never badge — skills/ledger/runs/preview today.
  badge?: (input: PanelBadgeInput) => number | null | undefined;
}

const CONTRIBUTIONS = [
  // UX-1 D2 (ux-batch-lock-2026-09-01): the plan layer's panel surface —
  // journal SSOT via deriveTodoState, zero new IPC. FIRST entry: the
  // default tab (Changes stays — accept/reject flows through it; just
  // deprioritized by position).
  { id: "tasks", title: "Tasks", icon: ListChecks, badge: (i: PanelBadgeInput) => positive(i.openTodos) },
  { id: "changes", title: "Changes", icon: GitCompareArrows, badge: (i: PanelBadgeInput) => positive(i.pendingDiffs) },
  // P1a: cross-workstream pending-review inbox (Changes stays per-conversation).
  { id: "review", title: "Review", icon: Inbox, badge: (i: PanelBadgeInput) => positive(i.pendingReview) },
  // passthrough of pending_counts' wiki notes — null (unavailable) → no badge.
  { id: "wiki", title: "Wiki", icon: FileText, badge: (i: PanelBadgeInput) => i.wikiNotes ?? undefined },
  { id: "memory", title: "Memory", icon: MapPin, badge: (i: PanelBadgeInput) => positive(i.memoryProposals) },
  { id: "skills", title: "Skills", icon: BookMarked },
  { id: "ledger", title: "Ledger", icon: BookOpen },
  // P2.2: journal-folded runs history (pure journal read, no daemon IPC).
  { id: "runs", title: "Runs", icon: History },
  // P2.1: file/URL preview surface (read_file text + sandboxed localhost
  // live frame).
  { id: "preview", title: "Preview", icon: Eye },
  // D9-W3 (learning control plane, pure observability): the first-ever
  // flagged-rules surface + episode/candidate fold (daemon learning_status).
  { id: "learning", title: "Learning", icon: GraduationCap },
] as const satisfies readonly PanelContribution[];

// Exported AS the const tuple (not widened to PanelContribution[]): strip/
// breadcrumbs index by `tab.id` and need the literal union, which a widened
// export would erase. The `satisfies` above already forced the shape.
export const PANEL_CONTRIBUTIONS = CONTRIBUTIONS;

// Uniform badge read — under `as const`, entries without a badge fn leave
// the PROPERTY ABSENT (union member lacks `badge`), so direct `.badge`
// access doesn't type-check; reads route through here.
// Param widened to the interface: the tuple-union type lacks `badge` on
// badgeless members, but every member satisfies PanelContribution.
export function badgeFor(
  c: PanelContribution,
  input: PanelBadgeInput,
): number | null | undefined {
  return c.badge?.(input);
}

// The tab union derives from the registry — ContextPanel/App/StatusBar can
// never name a tab the strip cannot render.
export type PanelTab = (typeof CONTRIBUTIONS)[number]["id"];

// Id list for validators (App's odo-panel-tab localStorage allowlist used
// to be a hand-maintained duplicate of the union).
export const PANEL_TAB_IDS: readonly PanelTab[] = CONTRIBUTIONS.map((c) => c.id);
