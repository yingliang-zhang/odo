// Tri-model open file / reveal in folder evaluation (3/3 K3+GLM+DSF):
// Right-click context menu for file-path references in chat and diffs.
// Mirrors the WorkstreamContextMenu pattern: positioned at click coords,
// viewport-clamped via useLayoutEffect, dismissed on outside-click/Escape/
// sidebar-scroll. Actions: Preview (in-app), Open (default app),
// Reveal in Folder. The preview renders through the daemon's read_file IPC
// (same containment rule as open_path) via a self-contained FilePreview —
// selecting Preview hides the menu and mounts the overlay; closing the
// overlay ends the whole interaction.

import { useEffect, useLayoutEffect, useRef, useState } from "react";
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

  // Viewport-clamp position (useLayoutEffect = no flash, same as WorkstreamContextMenu).
  const [pos, setPos] = useState({ x, y });
  useLayoutEffect(() => {
    const menu = menuRef.current;
    if (!menu) return;
    const rect = menu.getBoundingClientRect();
    const adjX = Math.min(pos.x, window.innerWidth - rect.width - 4);
    const adjY = Math.min(pos.y, window.innerHeight - rect.height - 4);
    setPos({ x: Math.max(4, adjX), y: Math.max(4, adjY) });
  }, [x, y]);

  // Dismiss on outside-click, Escape, or sidebar scroll.
  useEffect(() => {
    if (previewing) return; // the overlay owns dismissal while mounted
    const onDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) onClose();
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    const onScroll = (e: Event) => {
      const target = e.target as Node | null;
      if (target && menuRef.current && !menuRef.current.contains(target)) {
        const sidebar = document.querySelector(".sidebar-sections");
        if (sidebar && (sidebar === target || sidebar.contains(target))) onClose();
      } else if (target === document) onClose();
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
    void openPath(path, reveal, projectRoot).catch((e) => {
      console.warn("open_path failed:", e);
    });
    onClose();
  };

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
    { label: "Preview", icon: <BookOpen size={12} />, onClick: () => setPreviewing(true) },
    { label: "Open", icon: <Eye size={12} />, onClick: () => handleOpen(false) },
    { label: "Reveal in Folder", icon: <FolderOpen size={12} />, onClick: () => handleOpen(true) },
  ];

  return (
    <div
      ref={menuRef}
      className="ws-context-menu file-ref-menu"
      style={{ left: pos.x, top: pos.y }}
      role="group"
      aria-label={`File actions for ${path}`}
    >
      <div className="file-ref-path" title={path}>{path}</div>
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
    </div>
  );
}
