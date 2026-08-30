import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ArrowRightLeft, Trash2 } from "lucide-react";
import { cn } from "../lib/utils";
import { ESC_PRIORITY, useEscLayer } from "../esc-registry";

// Project header context menu — mirrors WorkstreamContextMenu's pattern:
// positioned at click coords, viewport-clamped via useLayoutEffect,
// dismissed by outside-click/Escape/scroll. Portaled to document.body.
// Actions: Switch to (non-active only), Remove project.
// Esc (P3.3): esc-registry menu layer — mounting while open is the
// active predicate, App's dispatcher routes the keystroke here.

interface Props {
  name: string;
  isActive: boolean;
  x: number;
  y: number;
  onClose: () => void;
  onSwitch: () => void;
  onRemove: () => void;
}

export default function ProjectContextMenu({
  name,
  isActive,
  x,
  y,
  onClose,
  onSwitch,
  onRemove,
}: Props) {
  const menuRef = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ x, y });
  // Captured before the first menu item steals focus via autoFocus, so
  // closing returns focus to the element that opened the menu.
  const prevFocusRef = useRef<HTMLElement | null>(document.activeElement as HTMLElement | null);
  useEffect(() => () => prevFocusRef.current?.focus(), []);
  // P3.3: menu-priority Esc ownership (replaces the window keydown listener).
  useEscLayer({ id: "project-context-menu", priority: ESC_PRIORITY.menu, onEscape: onClose });

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
    const onScroll = () => { onClose(); };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("scroll", onScroll, true);
    };
  }, [onClose]);

  const items = [
    ...(isActive ? [] : [{
      label: "Switch to",
      icon: <ArrowRightLeft size={12} aria-hidden />,
      onClick: () => { onSwitch(); },
    }]),
    {
      label: "Remove project",
      icon: <Trash2 size={12} aria-hidden />,
      danger: true,
      onClick: () => { onRemove(); },
    },
  ];

  return createPortal(
    <div
      ref={menuRef}
      className={cn(
        "fixed z-[200] min-w-[160px] ws-context-menu",
        "bg-[var(--bg-elevated)] border border-[var(--border)]",
        "rounded-[var(--radius-md)] p-1 shadow-[var(--shadow-panel)]",
      )}
      style={{ left: pos.x, top: pos.y }}
      role="group"
      aria-label={`Actions for project ${name}`}
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
    </div>,
    document.body,
  );
}
