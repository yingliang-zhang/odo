import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Pencil, Trash2, ClipboardCopy, ArrowRightLeft } from "lucide-react";
import type { Workstream } from "../types";
import { cn } from "../lib/utils";

/**
 * WorkstreamContextMenu — right-click menu for workstream rows.
 *
 * Phase 4: styles migrated to Tailwind utilities via Odo tokens.
 * Still uses manual positioning (not Radix ContextMenu) because Sidebar's
 * ctxMenu state passes x/y coordinates. Full Radix ContextMenu migration
 * requires restructuring Sidebar's workstream <li> as a ContextMenuTrigger.
 * Deferred to a later phase.
 *
 * Esc gate: the window keydown listener calls stopPropagation before onClose
 * to prevent App's global Esc handler from also firing.
 */

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
  const prevFocusRef = useRef<HTMLElement | null>(document.activeElement as HTMLElement | null);
  useEffect(() => () => prevFocusRef.current?.focus(), []);

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

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    const onScroll = (e: Event) => {
      const target = e.target as Node | null;
      if (target && menuRef.current && !menuRef.current.contains(target) && !target.contains(menuRef.current)) {
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
      className={cn(
        "fixed z-[200] min-w-[160px] ws-context-menu",
        "bg-[var(--bg-elevated)] border border-[var(--border)]",
        "rounded-[var(--radius-md)] p-1 shadow-[var(--shadow-panel)]",
      )}
      style={{ left: pos.x, top: pos.y }}
      role="group"
      aria-label={`Actions for ${workstream.name}`}
    >
      {items.map((item, i) => (
        <button
          key={item.label}
          type="button"
          className={cn(
            "flex items-center gap-2 w-full px-2 py-1.5 text-xs",
            "rounded-[var(--radius-sm)] cursor-pointer text-left",
            "bg-transparent border-none text-[var(--text)]",
            "hover:bg-[var(--bg-hover)] transition-colors",
            item.danger && "text-[var(--err-text)]",
          )}
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
