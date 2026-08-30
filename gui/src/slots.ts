// P1.2 (docs/design/adoption-lock.md): central selector map. One typed SLOT
// constant per probe consumer; components tag the DOM node with
// `data-slot="<value>"` ALONGSIDE the existing class/aria markers — the new
// map is additive, and no existing selector was rewritten to consume it.
// e2e specs import SLOT/slotSel for NEW assertions; old selectors stay.
export const SLOT = {
  // ChatSurface .chat-composer container (textarea + send/stop/park rows).
  composer: "composer",
  // StatusBar footer — the U1.1 chip row's host (the fold engine measures it).
  statusbar: "statusbar",
  // ContextPanel .panel-tabs tablist row.
  panelTabs: "panel-tabs",
  // DiffViewer .diff-card root.
  diffCard: "diff-card",
  // CommandPalette DialogContent.
  palette: "palette",
  // ShortcutsPanel (⌘/) DialogContent.
  shortcuts: "shortcuts",
  // P2.1: inline image rendered in a tool-result/attachment card (click
  // opens the full lightbox) and the chip fallback when bytes are
  // unavailable; Open-live affordance + sandboxed iframe in Preview tab.
  previewImage: "preview-image",
  previewChip: "preview-chip",
  previewLive: "preview-live",
  previewFrame: "preview-frame",
  // P2.2: one row in the Runs history tab.
  runsRow: "runs-row",
  // P2.3: typed failure overlay root (daemon-down taxonomy).
  failureOverlay: "failure-overlay",
  // Reload escape hatch the overlay grows past POLL_FAIL_RESTART_THRESHOLD.
  failureReload: "failure-reload",
  // P2.4: parked badge on a keep-alive LRU-parked ContextPanel tab.
  parkedBadge: "parked-badge",
} as const;

export type SlotName = keyof typeof SLOT;
export type SlotValue = (typeof SLOT)[SlotName];

// CSS attribute selector for one slot — the only form specs should write.
export function slotSel(slot: SlotValue): string {
  return `[data-slot="${slot}"]`;
}
