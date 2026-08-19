import { FormEvent, useState, type ReactNode } from "react";
import { errorMessage } from "../api";
import { Sparkles, FileText, Wand2, MapPin, BookOpen, Settings, PanelLeft, PanelRight, ChevronLeft, ChevronRight, MoreHorizontal } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import { Button } from "./ui/button";

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
  // Right context panel — the left sidebar toggle's mirror (⌘J).
  panelOpen: boolean;
  onTogglePanel: () => void;
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
  panelOpen,
  onTogglePanel,
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

  // PR3 overflow menu: Radix DropdownMenu (Phase 6) — outside click, Esc
  // dismiss, and keyboard nav built in.

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
        <div className="topbar-overflow">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                className="topbar-action topbar-action-icon-only"
                title="More actions (⌘K for command palette)"
                aria-label="More actions"
              >
                <span className="topbar-action-icon" aria-hidden="true">
                  <MoreHorizontal size={14} />
                </span>
              </button>
            </DropdownMenuTrigger>
            {/* topbar-overflow-menu/-item survive as inert identity markers
                (e2e boot.spec); their CSS is deleted in app.css. */}
            <DropdownMenuContent
              align="end"
              sideOffset={4}
              className="topbar-overflow-menu flex flex-col gap-px p-1.5"
            >
              <DropdownMenuItem
                className="topbar-overflow-item data-[disabled]:pointer-events-none data-[disabled]:opacity-40"
                disabled={curateBusy || actionsDisabled}
                title="Rewrite wiki topic pages + index from all epoch notes"
                onSelect={() => void handleCurate()}
              >
                <Wand2 size={14} />
                <span>{curateBusy ? "Curating…" : "Curate"}</span>
              </DropdownMenuItem>
              <DropdownMenuItem
                className="topbar-overflow-item data-[disabled]:pointer-events-none data-[disabled]:opacity-40"
                disabled={actionsDisabled}
                title="Store a verbatim pin in .odo/pins.md"
                onSelect={togglePin}
              >
                <MapPin size={14} />
                <span>Pin</span>
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                className="topbar-overflow-item data-[disabled]:pointer-events-none data-[disabled]:opacity-40"
                disabled={actionsDisabled}
                title="Browse this workstream's distilled wiki notes"
                onSelect={onOpenWiki}
              >
                <FileText size={14} />
                <span>Wiki</span>
                {wikiNoteCount != null && (
                  <span className="topbar-overflow-badge">{wikiNoteCount}</span>
                )}
              </DropdownMenuItem>
              <DropdownMenuItem
                className="topbar-overflow-item data-[disabled]:pointer-events-none data-[disabled]:opacity-40"
                disabled={actionsDisabled}
                title="Open .odo/ledger.md — daemon-written verified metrics"
                onSelect={onOpenLedger}
              >
                <BookOpen size={14} />
                <span>Ledger</span>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>

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
                <Button
                  type="submit"
                  variant="ghost"
                  size="sm"
                  className="pin-btn"
                  disabled={pinBusy || pinText.trim() === ""}
                  title="Store a verbatim pin in .odo/pins.md"
                >
                  Pin
                </Button>
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

        {/* Right context panel toggle — mirrors the sidebar toggle on the
            left (same nav-btn chrome); replaces the panel's old in-header
            close X. */}
        <button
          type="button"
          className="topbar-nav-btn"
          aria-label="Toggle panel"
          title="Toggle panel (⌘J)"
          onClick={onTogglePanel}
        >
          {panelOpen ? <ChevronRight size={14} /> : <PanelRight size={14} />}
        </button>
      </div>
    </header>
  );
}
