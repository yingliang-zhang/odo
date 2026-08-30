// P2.5 (docs/design/adoption-lock.md, 2026-08-29): typed failure overlay.
// Past the poll-failure threshold App replaces the single socket-down
// banner with this taxonomy-driven panel: gui/src/errors.ts' classifyFailure
// owns the class (transport blips stay on the old banner), this component
// only renders the FailureSpec it is handed — the title, the one-line cause
// (raw error string preserved in the title attribute, same disclosure
// channel as the P1.5 banner), the ONE leading action the class names, and
// an explicit × dismiss. Past the restart-failure threshold the parent ALSO
// hands onReload — the same escape hatch the legacy banner grows (grounded
// revise R2, F1) — rendered for every class. Esc wiring is the parent's
// ladder (the overlay is a DOM-class gate there); onDismiss is the only
// exit the leaf owns.
//
// Props are callbacks, never imports of App state — the parent mounts this
// keep-alive and decides what Reconnect / Copy diagnostics / Open journal
// do. Styling rides the .daemon-down-banner CSS family (amber severity
// strip, compact ghost action); the new .failure-overlay* rules live in
// styles/app.css alongside it.
import {
  AlertTriangle,
  BookOpen,
  ClipboardCopy,
  HeartPulse,
  RefreshCw,
  RotateCcw,
  ShieldAlert,
  Users,
  WifiOff,
  X,
  type LucideIcon,
} from "lucide-react";
import { cn } from "../lib/utils";
import type { FailureClass, FailureSpec } from "../errors";
import { SLOT } from "../slots";
import { strings } from "../strings";

export interface FailureOverlayProps {
  // The classified failure — title/cause text AND which of the three
  // action callbacks is rendered. The other two callbacks are inert for
  // this spec (a version skew never offers a misleading Reconnect).
  spec: FailureSpec;
  // The unclassified error string; surfaced only via the cause line's
  // title attribute — never as visible text (the cause stays one line).
  raw: string;
  onReconnect: () => void;
  onCopyDiagnostics: () => void;
  onOpenJournal: () => void;
  onDismiss: () => void;
  // Optional past-threshold escape hatch (presence = render): the same
  // reload lever the legacy banner grows at POLL_FAIL_RESTART_THRESHOLD —
  // a daemon the bridge cannot respawn must stay reachable from the
  // classified lane too.
  onReload?: () => void;
}

// One glyph per class, following the daemon-down banner's single WifiOff
// lead: the taxonomy stays scannable without growing the strip's height.
const CLASS_ICONS: Record<FailureClass, LucideIcon> = {
  socket_closed: WifiOff,
  heartbeat_timeout: HeartPulse,
  version_mismatch: AlertTriangle,
  verify_infra: ShieldAlert,
  panel_infra: Users,
};

// Labels/icons for the three possible leading actions. `spec.action`
// selects exactly one entry — the overlay renders that button only.
const ACTIONS: Record<FailureSpec["action"], { label: string; icon: LucideIcon }> = {
  reconnect: { label: "Reconnect", icon: RefreshCw },
  copy_diagnostics: { label: "Copy diagnostics", icon: ClipboardCopy },
  open_journal: { label: "Open journal", icon: BookOpen },
};

export default function FailureOverlay({
  spec,
  raw,
  onReconnect,
  onCopyDiagnostics,
  onOpenJournal,
  onDismiss,
  onReload,
}: FailureOverlayProps) {
  const ClassIcon = CLASS_ICONS[spec.cls];
  const { label, icon: ActionIcon } = ACTIONS[spec.action];
  const runAction =
    spec.action === "reconnect" ? onReconnect : spec.action === "copy_diagnostics" ? onCopyDiagnostics : onOpenJournal;
  return (
    <div
      className={cn("failure-overlay", `failure-overlay-${spec.cls}`)}
      data-slot={SLOT.failureOverlay}
      role="alert"
    >
      <ClassIcon size={14} className="failure-overlay-icon" aria-hidden />
      <span className="failure-overlay-title">{spec.title}</span>
      <span className="failure-overlay-cause" title={raw}>
        {spec.cause}
      </span>
      <button type="button" className="failure-overlay-action" onClick={runAction}>
        <ActionIcon size={12} aria-hidden />
        {label}
      </button>
      {onReload != null && (
        <button
          type="button"
          className="failure-overlay-reload"
          data-slot={SLOT.failureReload}
          title={strings.banner.daemonRestartTitle}
          onClick={onReload}
        >
          <RotateCcw size={12} aria-hidden />
          {strings.banner.daemonRestart}
        </button>
      )}
      <button
        type="button"
        className="failure-overlay-dismiss"
        aria-label="Dismiss failure overlay"
        title="Dismiss"
        onClick={onDismiss}
      >
        <X size={14} aria-hidden />
      </button>
    </div>
  );
}
