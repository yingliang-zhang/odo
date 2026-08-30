// Tri-model open file / reveal in folder evaluation (3/3 K3+GLM+DSF):
// Right-click context menu for file-path references in chat and diffs.
// Mirrors the WorkstreamContextMenu pattern: positioned at click coords,
// viewport-clamped via useLayoutEffect, dismissed on outside-click/Escape/
// scroll outside the menu. Actions: Preview (in-app), Open (default app),
// Reveal in Folder. The preview renders through the daemon's read_file IPC
// (same containment rule as open_path) via a self-contained FilePreview —
// selecting Preview hides the menu and mounts the overlay; closing the
// overlay ends the whole interaction.

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { FolderOpen, Eye, BookOpen } from "lucide-react";
import { openPath } from "../api";
import { cn } from "../lib/utils";
import FilePreview from "./FilePreview";
import { ESC_PRIORITY, useEscLayer } from "../esc-registry";

interface Props {
  /** Relative or absolute path to the file. */
  path: string;
  /** Project root for resolving relative paths. */
  projectRoot: string | null;
  x: number;
  y: number;
  onClose: () => void;
}

export default function FileRefContextMenu({
  path,
  projectRoot,
  x,
  y,
  onClose,
}: Props) {
  const menuRef = useRef<HTMLDivElement>(null);
  // Captured before the first menu item steals focus via autoFocus, so
  // closing returns focus to the element that opened the menu.
  const prevFocusRef = useRef<HTMLElement | null>(document.activeElement as HTMLElement | null);
  useEffect(() => () => prevFocusRef.current?.focus(), []);
  // Preview follows the menu: selecting Preview swaps the context menu for
  // the overlay (menu dismissed first so its Esc/outside-click handlers die
  // cleanly); the overlay's own Esc-gated close ends the interaction.
  const [previewing, setPreviewing] = useState(false);
  // Inline surface for open_path IPC failures (shown 2.5s, then closes).
  const [error, setError] = useState<string | null>(null);
  // Timer ref for the error auto-close — cleared on preview/unmount to avoid
  // killing a FilePreview opened during the error window (closure review 3/3).
  const errTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Clear any pending error timer on unmount.
  useEffect(() => {
    return () => {
      if (errTimerRef.current != null) clearTimeout(errTimerRef.current);
    };
  }, []);

  // Viewport-clamp position (useLayoutEffect = no flash, same as WorkstreamContextMenu).
  const [pos, setPos] = useState({ x, y });
  useLayoutEffect(() => {
    const menu = menuRef.current;
    if (!menu) return;
    const rect = menu.getBoundingClientRect();
    const adjX = Math.min(x, window.innerWidth - rect.width - 4);
    const adjY = Math.min(y, window.innerHeight - rect.height - 4);
    setPos({ x: Math.max(4, adjX), y: Math.max(4, adjY) });
  }, [x, y]);

  // P3.3: menu-priority Esc — the FilePreview overlay owns dismissal while
  // mounted (same gate the old effect applied to its listeners).
  useEscLayer({
    id: "fileref-context-menu",
    priority: ESC_PRIORITY.menu,
    active: () => !previewing,
    onEscape: onClose,
  });
  // Dismiss on outside-click or any scroll outside the menu.
  useEffect(() => {
    if (previewing) return; // the overlay owns dismissal while mounted
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) onClose();
    };
    // This menu is portaled to document.body (to escape .run-group's
    // content-visibility containment). Dismiss on any scroll not
    // originating from inside the menu itself — the menu is fixed to
    // the viewport, so any scroll means the user (or auto-scroll) moved
    // the content the menu was anchored to.
    const onScroll = (e: Event) => {
      const target = e.target as Node | null;
      if (!target || !menuRef.current) return;
      if (menuRef.current.contains(target)) return;
      onClose();
    };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("scroll", onScroll, true);
    };
  }, [onClose, previewing]);

  const handleOpen = (reveal: boolean) => {
    void openPath(path, reveal, projectRoot)
      .then(() => onClose())
      .catch((e) => {
        // Don't fail silently — show the reason inline for 2.5s, then close.
        console.warn("open_path failed:", e);
        setError(String(e?.message ?? e ?? "open failed"));
        if (errTimerRef.current != null) clearTimeout(errTimerRef.current);
        errTimerRef.current = setTimeout(() => onClose(), 2500);
      });
  };

  // Opening Preview cancels the error auto-close timer — otherwise the
  // pending setTimeout fires onClose mid-preview (closure review 3/3).
  const handlePreview = () => {
    if (errTimerRef.current != null) {
      clearTimeout(errTimerRef.current);
      errTimerRef.current = null;
    }
    setError(null);
    setPreviewing(true);
  };

  // FilePreview is a Radix Dialog (Phase 5): it portals itself, so no
  // createPortal wrapper here.
  if (previewing) {
    return (
      <FilePreview
        path={path}
        projectRoot={projectRoot}
        onClose={onClose}
      />
    );
  }

  const items = [
    { label: "Preview", icon: <BookOpen size={12} />, onClick: handlePreview },
    { label: "Open", icon: <Eye size={12} />, onClick: () => handleOpen(false) },
    { label: "Reveal in Folder", icon: <FolderOpen size={12} />, onClick: () => handleOpen(true) },
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
      aria-label={`File actions for ${path}`}
    >
      <div
        className={cn(
          "px-2 pt-1 pb-0.5 text-[10px] max-w-[280px]",
          "text-[var(--text-dim)] whitespace-nowrap overflow-hidden text-ellipsis",
        )}
        title={path}
      >
        {path}
      </div>
      {error != null && (
        <div
          className="px-2 py-0.5 text-[10px] text-[var(--err-text)] break-words"
          role="alert"
        >
          {error}
        </div>
      )}
      {items.map((item, i) => (
        <button
          key={item.label}
          type="button"
          className={cn(
            "flex items-center gap-2 w-full px-2 py-1.5 text-xs",
            "rounded-[var(--radius-sm)] cursor-pointer text-left",
            "bg-transparent border-none text-[var(--text)]",
            "hover:bg-[var(--bg-hover)] transition-colors",
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
