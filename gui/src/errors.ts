// P1.5 (docs/design/adoption-lock.md): ordered error-summarizer map. App's
// banner renders the first matching rule's summary (raw string preserved in
// the title attribute) and turns STICKY — explicit × dismiss only, no 10 s
// auto-dismiss. Unmatched errors keep the legacy behavior (raw text +
// auto-dismiss), so a novel failure mode is never masked by a wrong summary.
// Toast confirmations are untouched (ambient auto-dismiss).
//
// Rows are ordered most-specific → most-general; pattern order IS the
// classification, so keep specific bridge shapes above the generic
// timeout/io rows. Patterns are grounded in the Tauri bridge's format!
// strings (src-tauri/src/lib.rs: "connect <socket>: …", "daemon did not
// answer on …", "closed the connection without responding", path-gate
// errors) plus the GUI's own "poll failed:"/"send failed:" prefixes.
//
// P2.5 (docs/design/adoption-lock.md, 2026-08-29): the same ordered map
// gains a CLASSIFICATION channel. Rows for the failure-taxonomy classes
// carry `cls`; classifyFailure reuses THIS table (first classified rule
// wins — never a second map) and hands the matching FAILURE_TAXONOMY spec
// to the typed failure overlay that replaces the single socket-down banner
// at the poll-failure threshold. Rows without `cls` (path gates, registry,
// permissions, respawn failure) stay summary-only: they are not daemon-
// health failures, so the overlay never claims them.
export interface ErrorSummary {
  summary: string;
  action?: string;
}

// The five failure classes of P2.5. Transport classes ride the Tauri
// bridge's format! strings; verify_infra/panel_infra ride the auto-land
// pipeline's journaled phrases (internal/ipc/autoland.go, grounded.go).
export type FailureClass = "socket_closed" | "heartbeat_timeout" | "version_mismatch" | "verify_infra" | "panel_infra";

export interface FailureSpec {
  cls: FailureClass;
  title: string;
  cause: string;
  // Exactly ONE leading action per class — the overlay renders only that
  // button, so a version skew is never offered a misleading "Reconnect".
  action: "reconnect" | "copy_diagnostics" | "open_journal";
}

interface ErrorRule extends ErrorSummary {
  pattern: RegExp;
  // Optional P2.5 classification — rows without it are unclassifiable by
  // the overlay (classifyFailure skips them even when they match).
  cls?: FailureClass;
}

export const ERROR_RULES: readonly ErrorRule[] = [
  {
    // Bridge wraps a dead socket as "connect <sock>: … (daemon restart
    // failed: …)" — that form must escape the generic connect row below.
    pattern: /daemon restart failed/,
    summary: "Daemon died and could not be respawned",
    action: "Check .odo/daemon.log, then run `odo` in the project to restart it.",
  },
  {
    // Bridge: "connect <socket>: <os err>".
    pattern: /connect .*\.sock/,
    summary: "Daemon unreachable — its socket is dead",
    action: "The bridge respawns it on retry; wait a few seconds.",
    cls: "socket_closed",
  },
  {
    pattern: /did not answer on .* within/,
    summary: "Daemon failed to start in time",
    action: "See the daemon.log path in the raw error for the wedged startup.",
    cls: "heartbeat_timeout",
  },
  {
    pattern: /closed the connection without responding/,
    summary: "Daemon dropped mid-request",
    action: "Retry the action — the daemon may have restarted under it.",
    cls: "socket_closed",
  },
  {
    // Bridge read_file / open_path containment gate.
    pattern: /Path outside project root/,
    summary: "Blocked a path escape",
    action: "Files must live inside the project or ~/.odo.",
  },
  {
    // Bridge: "Path not found: … (os err)" and ENOENT shapes.
    pattern: /Path not found|no such file or directory/i,
    summary: "Path not found",
    action: "The file may have moved — refresh and check the path.",
  },
  {
    pattern: /not found in registry/,
    summary: "Project missing from the registry",
    action: "Re-add the project (Sidebar → New project).",
  },
  {
    pattern: /permission denied|EACCES/i,
    summary: "Permission denied",
    action: "Check ownership and mode on the path in the raw error.",
  },
  {
    pattern: /invalid daemon response/,
    summary: "Daemon replied with garbled data",
    action: "GUI and daemon are version-skewed — rebuild `odo` and reload.",
    cls: "version_mismatch",
  },
  {
    // Verify-gate infrastructure (P2.5): the auto-land verify gate's
    // fail-closed blocks — grounded on the daemon's own phrases:
    // "no usable .odo-verify at the repo root — the verify gate is
    // mandatory for auto-land" (internal/ipc/autoland.go runVerifyGate,
    // reason verify_unconfigured), the verify_no_evidence block, and the
    // advisory text "the verify gate is mandatory (fail-closed by design)"
    // (internal/ipc/verify_advisory.go). None of these match transport
    // rows, so this row cannot shadow them; it must sit ABOVE the broad
    // timeout row (a gate error tail can carry "timed out").
    pattern: /\.odo-verify|verify gate|verify_(unconfigured|no_evidence)/i,
    summary: "Auto-land blocked at the verify gate",
    action: "The committed .odo-verify resolved no usable command or evidence — the journal names the exact block.",
    cls: "verify_infra",
  },
  {
    // Review-panel infrastructure (P2.5): a leg that died on transport/
    // auth/timeout is not a verdict. Grounded on the journaled phrases:
    // blocked reason panel_infra with detail "a review leg failed on
    // transport/auth/timeout — infra failures are not verdicts"
    // (internal/ipc/autoland.go settle), degraded-leg comment "grounded
    // review failed: …" (internal/ipc/grounded.go), plus the pipeline's
    // vocabulary (moa_review, panel fan-out). Sits ABOVE the broad
    // timeout row — the panel_infra detail itself names "timeout".
    pattern: /panel[ _]infra|review leg failed|grounded review failed|moa|fan-?out|panel consensus/i,
    summary: "Review panel failed on infrastructure",
    action: "A review leg died on transport/auth/timeout — infra is not a verdict; the pipeline re-fires it.",
    cls: "panel_infra",
  },
  {
    // Broad transport row LAST — it must not shadow the specific ones above.
    pattern: /timed? ?out|deadline exceeded/i,
    summary: "Request timed out",
    action: "The daemon is busy or stalled — retry; check daemon.log if it repeats.",
    cls: "heartbeat_timeout",
  },
];

// First match wins; null = no rule owns this string (legacy banner path).
export function summarizeError(raw: string): ErrorSummary | null {
  for (const { pattern, summary, action } of ERROR_RULES) {
    if (pattern.test(raw)) return action === undefined ? { summary } : { summary, action };
  }
  return null;
}
// P2.5: the taxonomy the overlay renders. Titles are distinct (the
// overlay's headline), causes are one line and read standalone, and each
// class pins its locked leading action — reconnect for transport classes,
// diagnostics for skew, journal for pipeline-infra classes (their
// evidence already lives in the journal's blocked rows; there is nothing
// to blindly retry).
export const FAILURE_TAXONOMY: Record<FailureClass, FailureSpec> = {
  socket_closed: {
    cls: "socket_closed",
    title: "Daemon socket closed",
    cause: "The daemon's socket is dead or dropped mid-request — the bridge respawns it on retry.",
    action: "reconnect",
  },
  heartbeat_timeout: {
    cls: "heartbeat_timeout",
    title: "Daemon stopped answering",
    cause: "The daemon missed its reply window — it is stalled, busy, or restarting.",
    action: "reconnect",
  },
  version_mismatch: {
    cls: "version_mismatch",
    title: "GUI/daemon version mismatch",
    cause: "The daemon answered with data this GUI cannot parse — the two are version-skewed.",
    action: "copy_diagnostics",
  },
  verify_infra: {
    cls: "verify_infra",
    title: "Verify-gate infrastructure failure",
    cause: "The auto-land verify gate blocked on .odo-verify setup or evidence — not on the diff itself.",
    action: "open_journal",
  },
  panel_infra: {
    cls: "panel_infra",
    title: "Review-panel infrastructure failure",
    cause: "A review leg failed on transport/auth/timeout — infra failures are not verdicts.",
    action: "open_journal",
  },
};

// First CLASSIFIED matching rule owns the string; rows without `cls` are
// skipped mid-scan and unowned strings return null, so the caller falls
// back to the plain sticky banner exactly as P1.5 did.
export function classifyFailure(raw: string): FailureSpec | null {
  for (const rule of ERROR_RULES) {
    if (rule.cls !== undefined && rule.pattern.test(raw)) return FAILURE_TAXONOMY[rule.cls];
  }
  return null;
}
