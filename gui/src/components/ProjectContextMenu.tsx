import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ArrowRightLeft, Trash2 } from "lucide-react";

// Project header context menu — mirrors WorkstreamContextMenu's pattern:
// positioned at click coords, viewport-clamped via useLayoutEffect,
// dismissed by outside-click/Escape/scroll. Portaled to document.body.
// Actions: Switch to (non-active only), Remove project.

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
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    const onScroll = () => { onClose(); };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
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
      className="ws-context-menu"
      style={{ left: pos.x, top: pos.y }}
      role="group"
      aria-label={`Actions for project ${name}`}
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
    </div>,
    document.body,
  );
}
