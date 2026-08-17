import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Pencil, Trash2, ClipboardCopy, ArrowRightLeft } from "lucide-react";
import type { Workstream } from "../types";

// Lightweight context menu for workstream rows. Positioned at the
// right-click coordinates, dismissed by outside-click/Escape/scroll.
// Reuses the same actions-as-data pattern as the hover icon strip.
//
// Tri-model sidebar gap analysis (3/3 K3+GLM+DSF) identified the
// missing right-click menu as a daily friction point — especially
// for remote (non-active) project rows where rename/delete are
// impossible without switching projects first.

export interface ContextMenuItem {
  label: string;
  icon: React.ReactNode;
  danger?: boolean;
  onClick: () => void;
}

interface Props {
  workstream: Workstream;
  x: number;
  y: number;
  onClose: () => void;
  onSwitch: () => void;
  onRename: () => void;
  onDelete: () => void;
}

export default function WorkstreamContextMenu({
  workstream,
  x,
  y,
  onClose,
  onSwitch,
  onRename,
  onDelete,
}: Props) {
  const menuRef = useRef<HTMLDivElement>(null);
  // Captured before the first menu item steals focus via autoFocus, so
  // closing returns focus to the element that opened the menu.
  const prevFocusRef = useRef<HTMLElement | null>(document.activeElement as HTMLElement | null);
  useEffect(() => () => prevFocusRef.current?.focus(), []);

  // Adjust position to stay in viewport (useLayoutEffect = no flash).
  const [pos, setPos] = useState({ x, y });
  useLayoutEffect(() => {
    const menu = menuRef.current;
    if (!menu) return;
    const rect = menu.getBoundingClientRect();
    let adjX = x;
    let adjY = y;
    if (x + rect.width > window.innerWidth) adjX = window.innerWidth - rect.width - 4;
    if (y + rect.height > window.innerHeight) adjY = window.innerHeight - rect.height - 4;
    setPos({ x: Math.max(4, adjX), y: Math.max(4, adjY) });
  }, [x, y]);

  // Dismiss: outside-click, Escape, scroll.
  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) onClose();
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    const onScroll = (e: Event) => {
      // K3: only dismiss on scrolls originating from the sidebar scroller,
      // not from ChatSurface's programmatic auto-scroll during streaming.
      // The menu's highest-value moment is renaming/deleting a workstream
      // while an agent runs — auto-scroll fires on every token batch and
      // would otherwise dismiss the menu immediately.
      const target = e.target as Node | null;
      if (target && menuRef.current && !menuRef.current.contains(target) && !target.contains(menuRef.current)) {
        // Scroll in an unrelated container — only close if the user
        // scrolled the sidebar itself (where the rows live).
        const sidebar = document.querySelector(".sidebar-sections");
        if (sidebar && (sidebar === target || sidebar.contains(target))) {
          onClose();
        }
      } else if (target === document) {
        onClose();
      }
    };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", onScroll, true);
    };
  }, [onClose]);

  const items: ContextMenuItem[] = [
    {
      label: "Switch to",
      icon: <ArrowRightLeft size={12} aria-hidden />,
      onClick: () => { onSwitch(); onClose(); },
    },
    {
      label: "Rename",
      icon: <Pencil size={12} aria-hidden />,
      onClick: () => { onRename(); onClose(); },
    },
    {
      label: "Copy name",
      icon: <ClipboardCopy size={12} aria-hidden />,
      onClick: () => {
        navigator.clipboard?.writeText(workstream.name)?.catch(() => {});
        onClose();
      },
    },
    {
      label: "Delete",
      icon: <Trash2 size={12} aria-hidden />,
      danger: true,
      onClick: () => { onDelete(); onClose(); },
    },
  ];

  return (
    <div
      ref={menuRef}
      className="ws-context-menu"
      style={{ left: pos.x, top: pos.y }}
      role="group"
      aria-label={`Actions for ${workstream.name}`}
    >
      {items.map((item, i) => (
        <button
          key={item.label}
          type="button"
          className={`ws-context-item${item.danger ? " danger" : ""}`}
          autoFocus={i === 0}
          onClick={item.onClick}
        >
          {item.icon}
          <span>{item.label}</span>
        </button>
      ))}
    </div>
  );
}
