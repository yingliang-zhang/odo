import { FormEvent, useState, type ReactNode } from "react";
import { errorMessage } from "../api";
import { Sparkles, FileText, Wand2, MapPin, BookOpen, Settings, PanelLeft, ChevronLeft } from "lucide-react";

// M9 Phase 1: TopBar — 32px bar above the main content area.
// M9 Phase 4: owns the action row that used to live in the sidebar
// (Distill/Wiki/Curate/Pin/Ledger/Settings). Success/failure feedback is
// produced by App's handlers (toasts + error banner); this component keeps
// only its own interaction state (curate busy, pin popover).

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
        <ActionButton
          icon={<Sparkles size={14} />}
          label={distillBusy ? "Distilling…" : "Distill"}
          badge={pendingMemoryProposals > 0 ? pendingMemoryProposals : null}
          disabled={distillBusy || actionsDisabled}
          title="Distill this conversation into a wiki note and start a new epoch"
          onClick={onDistill}
        />
        <ActionButton
          icon={<FileText size={14} />}
          label="Wiki"
          badge={wikiNoteCount}
          disabled={actionsDisabled}
          title="Browse this workstream's distilled wiki notes"
          onClick={onOpenWiki}
        />
        <ActionButton
          icon={<Wand2 size={14} />}
          label={curateBusy ? "Curating…" : "Curate"}
          disabled={curateBusy || actionsDisabled}
          title="Rewrite wiki topic pages + index from all epoch notes"
          onClick={() => void handleCurate()}
        />
        <div className="topbar-pin">
          <ActionButton
            icon={<MapPin size={14} />}
            label="Pin"
            disabled={actionsDisabled}
            title="Store a verbatim pin in .odo/pins.md (always injected, human-owned)"
            onClick={togglePin}
          />
          {pinOpen && (
            <form className="topbar-pin-popover" onSubmit={handlePinSubmit}>
              <input
                type="text"
                className="pin-input"
                value={pinText}
                onChange={(e) => setPinText(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
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
          )}
        </div>
        <ActionButton
          icon={<BookOpen size={14} />}
          label="Ledger"
          disabled={actionsDisabled}
          title="Open .odo/ledger.md — daemon-written verified metrics (durations, proposals, accept/reject)"
          onClick={onOpenLedger}
        />
        <ActionButton
          icon={<Settings size={14} />}
          label="Settings"
          title="Settings (⌘,)"
          onClick={onOpenSettings}
        />
      </div>
    </header>
  );
}
