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
  projectRoot: string;
  isActiveProject: boolean;
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
    const onScroll = () => onClose();
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
      {items.map((item) => (
        <button
          key={item.label}
          type="button"
          className={`ws-context-item${item.danger ? " danger" : ""}`}
          role="menuitem"
          onClick={item.onClick}
        >
          {item.icon}
          <span>{item.label}</span>
        </button>
      ))}
    </div>
  );
}
