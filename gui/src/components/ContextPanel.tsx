// M9 Phase 1: ContextPanel — right-side panel with tabbed content.
// Phase 1: shell with 4 tabs (Changes/Wiki/Memory/Ledger), empty body.
// Phase 2: Changes tab gets DiffViewer.
// Phase 3: Wiki/Memory/Ledger tabs get their content.

import { type ReactNode, useRef, useState } from "react";
import { GitCompareArrows, FileText, MapPin, BookOpen, BookMarked, Inbox, X } from "lucide-react";
import type { PointerEvent as ReactPointerEvent } from "react";
import RunGroupBoundary from "./RunGroupBoundary";

export type PanelTab = "changes" | "review" | "wiki" | "memory" | "ledger" | "skills";

interface Props {
  open: boolean;
  onClose: () => void;
  activeTab: PanelTab;
  onTabChange: (tab: PanelTab) => void;
  // Badge counts for each tab (null/undefined = no badge)
  changesBadge?: number;
  reviewBadge?: number;
  wikiBadge?: number | null;
  memoryBadge?: number;
  ledgerBadge?: number | null;
  // Tab content (rendered as keep-alive: mounted but hidden when inactive)
  children?: ReactNode;
}

const TABS: { id: PanelTab; label: string; icon: ReactNode }[] = [
  { id: "changes", label: "Changes", icon: <GitCompareArrows size={12} /> },
  // P1a: cross-workstream pending-review inbox (Changes stays per-conversation).
  { id: "review", label: "Review", icon: <Inbox size={12} /> },
  { id: "wiki", label: "Wiki", icon: <FileText size={12} /> },
  { id: "memory", label: "Memory", icon: <MapPin size={12} /> },
  { id: "skills", label: "Skills", icon: <BookMarked size={12} /> },
  { id: "ledger", label: "Ledger", icon: <BookOpen size={12} /> },
];

export default function ContextPanel({
  open,
  onClose,
  activeTab,
  onTabChange,
  changesBadge,
  reviewBadge,
  wikiBadge,
  memoryBadge,
  ledgerBadge,
  children,
}: Props) {
  const MIN_WIDTH = 280;
  const MAX_WIDTH = 600;
  const [panelWidth, setPanelWidth] = useState(380);
  const dragRef = useRef<{ startX: number; startW: number } | null>(null);

  if (!open) return null;

  const onResizePointerDown = (e: ReactPointerEvent) => {
    dragRef.current = { startX: e.clientX, startW: panelWidth };
    e.currentTarget.setPointerCapture(e.pointerId);
  };
  const onResizePointerMove = (e: ReactPointerEvent) => {
    const d = dragRef.current;
    if (!d) return;
    // Grip is on the left edge of a right-docked panel: dragging left
    // (clientX decreases) must widen the panel.
    setPanelWidth(Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, d.startW + (d.startX - e.clientX))));
  };
  const onResizePointerUp = () => {
    dragRef.current = null;
  };

  const badges: Record<PanelTab, number | null | undefined> = {
    changes: changesBadge,
    review: reviewBadge,
    wiki: wikiBadge,
    memory: memoryBadge,
    skills: undefined,
    ledger: ledgerBadge,
  };

  return (
    <aside
      className="context-panel"
      aria-label="Context panel"
      style={{ "--panel-width": `${panelWidth}px` } as React.CSSProperties}
    >
      <div
        className="panel-resize"
        aria-hidden="true"
        onPointerDown={onResizePointerDown}
        onPointerMove={onResizePointerMove}
        onPointerUp={onResizePointerUp}
      />
      <div className="panel-head">
        <div className="panel-tabs" role="tablist">
          {TABS.map((tab) => {
            const count = badges[tab.id];
            const isActive = activeTab === tab.id;
            return (
              <button
                key={tab.id}
                type="button"
                role="tab"
                aria-selected={isActive}
                className={`panel-tab${isActive ? " active" : ""}`}
                onClick={() => onTabChange(tab.id)}
              >
                {tab.icon}
                {tab.label}
                {count != null && count > 0 && (
                  <span className="panel-tab-badge">{count}</span>
                )}
              </button>
            );
          })}
        </div>
        <button
          type="button"
          className="panel-close"
          aria-label="Close panel"
          title="Close (⌘J)"
          onClick={onClose}
        >
          <X size={14} />
        </button>
      </div>
      <div className="panel-body">
        <RunGroupBoundary resetKey={activeTab} fallbackNote="other tabs are unaffected">
          {children ?? (
            <div className="panel-empty">Select a tab to view content.</div>
          )}
        </RunGroupBoundary>
      </div>
    </aside>
  );
}
