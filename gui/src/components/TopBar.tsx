import { FormEvent, useState, type ReactNode, useEffect, useRef } from "react";
import { errorMessage } from "../api";
import { Sparkles, FileText, Wand2, MapPin, BookOpen, Settings, PanelLeft, ChevronLeft, MoreHorizontal } from "lucide-react";

// M9 Phase 1: TopBar — 32px bar above the main content area.
// M9 Phase 4: owns the action row that used to live in the sidebar
// (Distill/Curate/Pin/Settings). Success/failure feedback is
// produced by App's handlers (toasts + error banner); this component keeps
// only its own interaction state (curate busy, pin popover).
//
// PR3: TopBar decluttered to 3 visible controls + overflow menu.
// Visible: Distill (labeled), ⋯ overflow, Settings (gear icon only).
// Overflow: Curate, Pin, Wiki (opens panel), Ledger (opens panel).

interface Props {
  workstreamName: string | null;
  sidebarCollapsed: boolean;
  onToggleSidebar: () => void;
  // Actions (moved from the Sidebar in P4). onDistill/onCurate never
  // reject — failures surface in App's error banner. onPin rejects so the
  // popover can show the refusal inline (e.g. overflow names the pin).
  onDistill: () => void;
  onOpenWiki: () => void;
  onCurate: () => Promise<void>;
  onPin: (text: string) => Promise<void>;
  onOpenSettings: () => void;
  onOpenLedger: () => void;
  // Badges
  wikiNoteCount: number | null;
  pendingMemoryProposals: number;
  distillBusy: boolean;
  // Capture actions (distill/wiki/curate/pin/ledger) need an active
  // conversation — App passes conversation == null here.
  actionsDisabled: boolean;
}

// One action button: icon + label + optional trailing count badge (the
// TopBar analogue of the old sidebar MenuRow).
function ActionButton({
  icon,
  label,
  badge,
  disabled,
  title,
  onClick,
}: {
  icon: ReactNode;
  label: string;
  badge?: number | null;
  disabled?: boolean;
  title?: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className="topbar-action"
      title={title}
      aria-label={title ?? label}
      disabled={disabled}
      onClick={onClick}
    >
      <span className="topbar-action-icon" aria-hidden="true">
        {icon}
      </span>
      <span className="topbar-action-label">{label}</span>
      {badge != null && <span className="topbar-badge">{badge}</span>}
    </button>
  );
}

// Icon-only button (for Settings gear — no visible label).
function IconButton({
  icon,
  disabled,
  title,
  onClick,
}: {
  icon: ReactNode;
  disabled?: boolean;
  title: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className="topbar-action topbar-action-icon-only"
      title={title}
      aria-label={title}
      disabled={disabled}
      onClick={onClick}
    >
      <span className="topbar-action-icon" aria-hidden="true">
        {icon}
      </span>
    </button>
  );
}

export default function TopBar({
  workstreamName,
  sidebarCollapsed,
  onToggleSidebar,
  onDistill,
  onOpenWiki,
  onCurate,
  onPin,
  onOpenSettings,
  onOpenLedger,
  wikiNoteCount,
  pendingMemoryProposals,
  distillBusy,
  actionsDisabled,
}: Props) {
  const [curateBusy, setCurateBusy] = useState(false);
  const [pinOpen, setPinOpen] = useState(false);
  const [pinText, setPinText] = useState("");
  const [pinBusy, setPinBusy] = useState(false);
  const [pinError, setPinError] = useState<string | null>(null);
  const [overflowOpen, setOverflowOpen] = useState(false);
  const overflowRef = useRef<HTMLDivElement>(null);

  const handleCurate = async () => {
    if (curateBusy) return;
    setCurateBusy(true);
    try {
      await onCurate();
    } finally {
      setCurateBusy(false);
    }
  };

  const togglePin = () => {
    setPinOpen((open) => {
      if (open) setPinError(null); // closing clears any stale refusal
      return !open;
    });
  };

  // M5: store the pin verbatim; on success clear the input and close the
  // popover (App toasts the confirmation). A refusal (overflow names the
  // pin text) shows in the popover's error line.
  const handlePinSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const text = pinText.trim();
    if (text === "" || pinBusy || actionsDisabled) return;
    setPinBusy(true);
    setPinError(null);
    try {
      await onPin(text);
      setPinText("");
      setPinOpen(false);
    } catch (err) {
      setPinError(errorMessage(err));
    } finally {
      setPinBusy(false);
    }
  };

  // PR3: Close overflow menu on outside click or Escape.
  useEffect(() => {
    if (!overflowOpen) return;
    const onClick = (e: MouseEvent) => {
      if (overflowRef.current && !overflowRef.current.contains(e.target as Node)) {
        setOverflowOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOverflowOpen(false);
    };
    document.addEventListener("mousedown", onClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [overflowOpen]);

  return (
    <header className="app-topbar">
      <button
        type="button"
        className="topbar-nav-btn"
        aria-label="Toggle sidebar"
        title="Toggle sidebar (⌘B)"
        onClick={onToggleSidebar}
      >
        {sidebarCollapsed ? <PanelLeft size={14} /> : <ChevronLeft size={14} />}
      </button>
      <span className="topbar-brand">Odo</span>
      {workstreamName && (
        <>
          <span className="topbar-sep">·</span>
          <span className="topbar-workstream">{workstreamName}</span>
        </>
      )}

      <div className="topbar-actions">
        {/* PR3: Distill — primary visible action (labeled) */}
        <ActionButton
          icon={<Sparkles size={14} />}
          label={distillBusy ? "Distilling…" : "Distill"}
          badge={pendingMemoryProposals > 0 ? pendingMemoryProposals : null}
          disabled={distillBusy || actionsDisabled}
          title="Distill this conversation into a wiki note and start a new epoch"
          onClick={onDistill}
        />

        {/* PR3: Overflow menu — Curate, Pin, Wiki, Ledger */}
        <div className="topbar-overflow" ref={overflowRef}>
          <button
            type="button"
            className="topbar-action topbar-action-icon-only"
            title="More actions (⌘K for command palette)"
            aria-label="More actions"
            aria-haspopup="menu"
            aria-expanded={overflowOpen}
            onClick={() => setOverflowOpen((v) => !v)}
          >
            <span className="topbar-action-icon" aria-hidden="true">
              <MoreHorizontal size={14} />
            </span>
          </button>
          {overflowOpen && (
            <div className="topbar-overflow-menu" role="menu">
              <button
                type="button"
                className="topbar-overflow-item"
                role="menuitem"
                disabled={curateBusy || actionsDisabled}
                title="Rewrite wiki topic pages + index from all epoch notes"
                onClick={() => {
                  setOverflowOpen(false);
                  void handleCurate();
                }}
              >
                <Wand2 size={14} />
                <span>{curateBusy ? "Curating…" : "Curate"}</span>
              </button>
              <button
                type="button"
                className="topbar-overflow-item"
                role="menuitem"
                disabled={actionsDisabled}
                title="Store a verbatim pin in .odo/pins.md"
                onClick={() => {
                  setOverflowOpen(false);
                  togglePin();
                }}
              >
                <MapPin size={14} />
                <span>Pin</span>
              </button>
              <div className="topbar-overflow-sep" role="separator" />
              <button
                type="button"
                className="topbar-overflow-item"
                role="menuitem"
                disabled={actionsDisabled}
                title="Browse this workstream's distilled wiki notes"
                onClick={() => {
                  setOverflowOpen(false);
                  onOpenWiki();
                }}
              >
                <FileText size={14} />
                <span>Wiki</span>
                {wikiNoteCount != null && (
                  <span className="topbar-overflow-badge">{wikiNoteCount}</span>
                )}
              </button>
              <button
                type="button"
                className="topbar-overflow-item"
                role="menuitem"
                disabled={actionsDisabled}
                title="Open .odo/ledger.md — daemon-written verified metrics"
                onClick={() => {
                  setOverflowOpen(false);
                  onOpenLedger();
                }}
              >
                <BookOpen size={14} />
                <span>Ledger</span>
              </button>
            </div>
          )}

          {/* PR3: Pin popover — anchored under the overflow button */}
          {pinOpen && (
            <div className="topbar-pin">
              <form className="topbar-pin-popover" onSubmit={handlePinSubmit}>
                <input
                  type="text"
                  className="pin-input"
                  value={pinText}
                  onChange={(e) => setPinText(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Escape") {
                      e.stopPropagation();
                      setPinOpen(false);
                      setPinError(null);
                    }
                  }}
                  placeholder="remember: …"
                  disabled={pinBusy}
                  autoFocus
                />
                <button
                  type="submit"
                  className="pin-btn"
                  disabled={pinBusy || pinText.trim() === ""}
                  title="Store a verbatim pin in .odo/pins.md"
                >
                  Pin
                </button>
                {pinError && <div className="topbar-pin-error">{pinError}</div>}
              </form>
            </div>
          )}
        </div>

        {/* PR3: Settings — gear icon only (no label) */}
        <IconButton
          icon={<Settings size={14} />}
          title="Settings (⌘,)"
          onClick={onOpenSettings}
        />
      </div>
    </header>
  );
}
