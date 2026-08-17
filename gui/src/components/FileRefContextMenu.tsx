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
import FilePreview from "./FilePreview";

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

  // Dismiss on outside-click, Escape, or any scroll outside the menu.
  useEffect(() => {
    if (previewing) return; // the overlay owns dismissal while mounted
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) onClose();
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
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
    window.addEventListener("keydown", onKey);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
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

  if (previewing) {
    return createPortal(
      <FilePreview
        path={path}
        projectRoot={projectRoot}
        onClose={onClose}
      />,
      document.body,
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
      className="ws-context-menu file-ref-menu"
      style={{ left: pos.x, top: pos.y }}
      role="group"
      aria-label={`File actions for ${path}`}
    >
      <div className="file-ref-path" title={path}>{path}</div>
      {error != null && <div className="file-ref-error" role="alert">{error}</div>}
      {items.map((item, i) => (
        <button
          key={item.label}
          type="button"
          className="ws-context-item"
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
