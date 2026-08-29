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
} as const;

export type SlotName = keyof typeof SLOT;
export type SlotValue = (typeof SLOT)[SlotName];

// CSS attribute selector for one slot — the only form specs should write.
export function slotSel(slot: SlotValue): string {
  return `[data-slot="${slot}"]`;
}
