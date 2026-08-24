// M9 Phase 1: ContextPanel — right-side panel with tabbed content.
// Phase 1: shell with 4 tabs (Changes/Wiki/Memory/Ledger), empty body.
// Phase 2: Changes tab gets DiffViewer.
// Phase 3: Wiki/Memory/Ledger tabs get their content.

import { type ReactNode, useEffect, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, GitCompareArrows, FileText, MapPin, BookOpen, BookMarked, Inbox } from "lucide-react";
import type { PointerEvent as ReactPointerEvent } from "react";
import RunGroupBoundary from "./RunGroupBoundary";
import { cn } from "../lib/utils";

export type PanelTab = "changes" | "review" | "wiki" | "memory" | "ledger" | "skills";

interface Props {
  open: boolean;
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

  // Review finding 8 (2026-08-24) — tab strip overflow. Measured in Chromium
  // at the 380px default: six tabs span scrollWidth 419-457px (varies with
  // which tab carries the active semibold) vs clientWidth 363px, with no
  // navigation; at the 280px MIN the clip reaches ~194px. Two failure modes:
  //   a) activeTab changes (incl. programmatic, e.g. TopBar toggle selections)
  //      never moved the strip's scrollLeft, leaving the active tab off-view;
  //   b) with no scroll affordance a real user could not reach off-view tabs.
  // On the reported "clicks intercepted by the tab strip itself": Playwright
  // and CDP clicks in this env always succeeded after auto-scroll and
  // hit-tests never showed content over a button, so an exact repro was NOT
  // possible [推断]: the one platform-dependent setup with a real
  // interception mechanism is classic (non-overlay) scrollbars — app.css
  // `*::-webkit-scrollbar { height: 6px }` then carves a 6px band out of the
  // 30px-tall strip whose pixels are scrollbar-owned and silently swallow
  // clicks. Fixes below cover every mode: (a) scrollIntoView on activeTab
  // change, (b) ResizeObserver-rendered ‹ › buttons — real <button>s placed
  // in .panel-head's flex flow, OUTSIDE the scroll container and clear of
  // the absolutely positioned .panel-resize grip (head px-2 keeps them off
  // the grip's 4px strip at the panel's left edge).
  const tabsRef = useRef<HTMLDivElement>(null);
  const tabRefs = useRef<Partial<Record<PanelTab, HTMLButtonElement | null>>>({});
  const [tabsOverflow, setTabsOverflow] = useState(false);
  useEffect(() => {
    tabRefs.current[activeTab]?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [activeTab]);
  useEffect(() => {
    const el = tabsRef.current;
    if (!el) return;
    // +1px: sub-pixel overflow (fractional tab widths) is invisible yet would
    // render the ‹ › controls; same predicate at all three call sites.
    const readsOverflow = () => el.scrollWidth > el.clientWidth + 1;
    setTabsOverflow(readsOverflow());
    const ro = new ResizeObserver(() => setTabsOverflow(readsOverflow()));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  // Post-render check for content-driven width shifts (badge fills, the
  // semibold active-tab swap): an unchanged predicate bails out of render,
  // so the extra run is free.
  useEffect(() => {
    const el = tabsRef.current;
    if (el) setTabsOverflow(el.scrollWidth > el.clientWidth + 1);
  });
  const scrollTabs = (dir: 1 | -1) => {
    const el = tabsRef.current;
    // 80% of a viewport width per click — the last off-view tab is revealed
    // within 1-2 clicks; the engine clamps the delta at the scroll ends.
    el?.scrollBy({ left: dir * el.clientWidth * 0.8 });
  };

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
        {tabsOverflow && (
          <button
            type="button"
            aria-label="Scroll tabs left"
            className={cn(
              "shrink-0 inline-flex items-center justify-center p-[3px] rounded",
              "bg-transparent border-none text-[var(--text-dim)] cursor-pointer",
              "hover:text-[var(--text)] hover:bg-[var(--bg-input)]",
            )}
            onClick={() => scrollTabs(-1)}
          >
            <ChevronLeft size={12} />
          </button>
        )}
        <div className="panel-tabs flex gap-px flex-1 min-w-0 overflow-x-auto" role="tablist" ref={tabsRef}>
          {TABS.map((tab) => {
            const count = badges[tab.id];
            const isActive = activeTab === tab.id;
            return (
              <button
                key={tab.id}
                type="button"
                role="tab"
                ref={(el) => {
                  tabRefs.current[tab.id] = el;
                }}
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
        {tabsOverflow && (
          <button
            type="button"
            aria-label="Scroll tabs right"
            className={cn(
              "shrink-0 inline-flex items-center justify-center p-[3px] rounded",
              "bg-transparent border-none text-[var(--text-dim)] cursor-pointer",
              "hover:text-[var(--text)] hover:bg-[var(--bg-input)]",
            )}
            onClick={() => scrollTabs(1)}
          >
            <ChevronRight size={12} />
          </button>
        )}
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
