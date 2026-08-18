// M9 Phase 1: ContextPanel — right-side panel with tabbed content.
// Phase 1: shell with 4 tabs (Changes/Wiki/Memory/Ledger), empty body.
// Phase 2: Changes tab gets DiffViewer.
// Phase 3: Wiki/Memory/Ledger tabs get their content.

import { type ReactNode, useRef, useState } from "react";
import { GitCompareArrows, FileText, MapPin, BookOpen, BookMarked, Inbox, X } from "lucide-react";
import type { PointerEvent as ReactPointerEvent } from "react";
import RunGroupBoundary from "./RunGroupBoundary";
import { Button } from "./ui/button";
import { cn } from "../lib/utils";

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
      className={cn(
        "context-panel w-[var(--panel-width)] min-w-[280px] max-w-[720px]",
        "flex flex-col min-h-0 bg-[var(--bg-raised,var(--bg))]",
        "border-l border-[var(--stroke-tertiary)] overflow-hidden relative",
        "animate-[panel-in_0.2s_var(--ease-out)]",
        // Below 1000px the panel overlays the chat (was @media max-width 999px).
        "max-[999px]:fixed max-[999px]:top-[var(--topbar-height)] max-[999px]:right-0",
        "max-[999px]:bottom-[var(--statusbar-height)] max-[999px]:z-[90]",
        "max-[999px]:shadow-[-4px_0_12px_rgba(0,0,0,0.3)]",
      )}
      aria-label="Context panel"
      style={{ "--panel-width": `${panelWidth}px` } as React.CSSProperties}
    >
      <div
        className={cn(
          "panel-resize absolute left-0 top-0 bottom-0 z-0 w-1 shrink-0",
          "cursor-col-resize bg-transparent [pointer-events:auto] touch-none",
          "hover:bg-[var(--border)]",
        )}
        aria-hidden="true"
        onPointerDown={onResizePointerDown}
        onPointerMove={onResizePointerMove}
        onPointerUp={onResizePointerUp}
      />
      <div className="panel-head flex items-center gap-1 px-2 h-8 shrink-0 border-b border-[var(--border)] overflow-hidden">
        <div className="panel-tabs flex gap-px flex-1 min-w-0 overflow-x-auto" role="tablist">
          {TABS.map((tab) => {
            const count = badges[tab.id];
            const isActive = activeTab === tab.id;
            return (
              <button
                key={tab.id}
                type="button"
                role="tab"
                aria-selected={isActive}
                className={cn(
                  "panel-tab inline-flex items-center gap-[3px] bg-transparent",
                  "border border-transparent rounded text-[var(--text-dim)]",
                  "text-[11px] px-[5px] py-[3px] cursor-pointer whitespace-nowrap",
                  "hover:text-[var(--text)] hover:bg-[var(--bg-input)]",
                  isActive && "active text-[var(--text)] bg-[var(--bg-input)] font-semibold",
                )}
                onClick={() => onTabChange(tab.id)}
              >
                {tab.icon}
                {tab.label}
                {count != null && count > 0 && (
                  <span
                    className={cn(
                      "panel-tab-badge inline-block min-w-4 h-4 leading-4 text-center",
                      "rounded-md bg-[var(--accent-user)] text-[var(--bg)]",
                      "text-[10px] font-bold px-1",
                    )}
                  >
                    {count}
                  </span>
                )}
              </button>
            );
          })}
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className={cn(
            "panel-close shrink-0 h-[18px] w-[26px] p-0 rounded",
            "hover:text-[var(--text)] hover:bg-[var(--bg-input)]",
          )}
          aria-label="Close panel"
          title="Close (⌘J)"
          onClick={onClose}
        >
          <X size={14} />
        </Button>
      </div>
      <div className="panel-body flex-1 min-h-0 overflow-y-auto p-2">
        <RunGroupBoundary resetKey={activeTab} fallbackNote="other tabs are unaffected">
          {children ?? (
            <div className="panel-empty">Select a tab to view content.</div>
          )}
        </RunGroupBoundary>
      </div>
    </aside>
  );
}
