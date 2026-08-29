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
export interface ErrorSummary {
  summary: string;
  action?: string;
}

interface ErrorRule extends ErrorSummary {
  pattern: RegExp;
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
  },
  {
    pattern: /did not answer on .* within/,
    summary: "Daemon failed to start in time",
    action: "See the daemon.log path in the raw error for the wedged startup.",
  },
  {
    pattern: /closed the connection without responding/,
    summary: "Daemon dropped mid-request",
    action: "Retry the action — the daemon may have restarted under it.",
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
  },
  {
    // Broad transport row LAST — it must not shadow the specific ones above.
    pattern: /timed? ?out|deadline exceeded/i,
    summary: "Request timed out",
    action: "The daemon is busy or stalled — retry; check daemon.log if it repeats.",
  },
];

// First match wins; null = no rule owns this string (legacy banner path).
export function summarizeError(raw: string): ErrorSummary | null {
  for (const { pattern, summary, action } of ERROR_RULES) {
    if (pattern.test(raw)) return action === undefined ? { summary } : { summary, action };
  }
  return null;
}
