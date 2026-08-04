// M9 Phase 1: ContextPanel — right-side panel with tabbed content.
// Phase 1: shell with 4 tabs (Changes/Wiki/Memory/Ledger), empty body.
// Phase 2: Changes tab gets DiffViewer.
// Phase 3: Wiki/Memory/Ledger tabs get their content.

import { type ReactNode } from "react";

export type PanelTab = "changes" | "wiki" | "memory" | "ledger";

interface Props {
  open: boolean;
  onClose: () => void;
  activeTab: PanelTab;
  onTabChange: (tab: PanelTab) => void;
  // Badge counts for each tab (null/undefined = no badge)
  changesBadge?: number;
  wikiBadge?: number | null;
  memoryBadge?: number;
  ledgerBadge?: number | null;
  // Tab content (rendered as keep-alive: mounted but hidden when inactive)
  children?: ReactNode;
}

const TABS: { id: PanelTab; label: string; icon: string }[] = [
  { id: "changes", label: "Changes", icon: "±" },
  { id: "wiki", label: "Wiki", icon: "❑" },
  { id: "memory", label: "Memory", icon: "◈" },
  { id: "ledger", label: "Ledger", icon: "▤" },
];

export default function ContextPanel({
  open,
  onClose,
  activeTab,
  onTabChange,
  changesBadge,
  wikiBadge,
  memoryBadge,
  ledgerBadge,
  children,
}: Props) {
  if (!open) return null;

  const badges: Record<PanelTab, number | null | undefined> = {
    changes: changesBadge,
    wiki: wikiBadge,
    memory: memoryBadge,
    ledger: ledgerBadge,
  };

  return (
    <aside className="context-panel" aria-label="Context panel">
      <div className="panel-resize" aria-hidden="true" />
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
          ×
        </button>
      </div>
      <div className="panel-body">
        {children ?? (
          <div className="panel-empty">Select a tab to view content.</div>
        )}
      </div>
    </aside>
  );
}
