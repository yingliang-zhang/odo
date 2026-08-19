// M3 visibility (spec §3b): desktop notification when a run finishes while
// the window is hidden. Spec §3b mandates a lazy import() of the plugin so
// the initial bundle and startup never pay for it (exception to the static-
// import default); every failure is swallowed — notifications are a nicety
// that must never reach the poll loop.

const BODY_LEN = 80;

// Permission is asked once, lazily, on the first qualifying event; a denial
// is not re-litigated mid-session.
let permission: "unknown" | "granted" | "denied" = "unknown";

export async function notifyRunDone(workstreamName: string, summary: string): Promise<void> {
  try {
    if (typeof document === "undefined" || !document.hidden) return;
    const mod = await import("@tauri-apps/plugin-notification");
    if (permission === "unknown") {
      let granted = await mod.isPermissionGranted();
      if (!granted) {
        granted = (await mod.requestPermission()) === "granted";
      }
      permission = granted ? "granted" : "denied";
    }
    if (permission !== "granted") return;
    mod.sendNotification({
      title: `Odo: run finished in ${workstreamName}`,
      body: summary.length > BODY_LEN ? summary.slice(0, BODY_LEN) : summary,
    });
  } catch {
    // Best-effort by contract: never throw into the caller (the poll loop).
  }
}

// E2E seam (same posture as __odoFixtures): when a test installs the hook
// it receives the exact notification payload instead of the OS — the real
// plugin never loads in a browser. Absent in production builds.
export interface LoopNotifyPayload {
  title: string;
  body: string;
}
declare global {
  interface Window {
    __odoLoopNotify?: (payload: LoopNotifyPayload) => void;
  }
}

// M19 (V11): ONE system notification on a loop's first-sight terminal
// kind ("Odo /loop <mode>: <terminal kind> (rounds N, tokens T)"). The
// caller (App's watcher) dedups session-local and journals the
// loop_notified receipt; this helper only paints. Same lazy-import +
// swallow posture as notifyRunDone — the plugin is a webview-only module
// (spec §3b exception to the static-import default: a plain browser must
// never load or crash on it); firing requires the GUI open (honest limit
// — the daemon never touches OS services).
export async function notifyLoopTerminal(mode: string, kind: string, rounds: number, spentTokens: number): Promise<void> {
  const payload: LoopNotifyPayload = {
    title: "Odo",
    body: `Odo /loop ${mode}: ${kind} (rounds ${rounds}, tokens ${spentTokens})`,
  };
  if (window.__odoLoopNotify != null) {
    window.__odoLoopNotify(payload);
    return;
  }
  try {
    const mod = await import("@tauri-apps/plugin-notification");
    if (permission === "unknown") {
      let granted = await mod.isPermissionGranted();
      if (!granted) {
        granted = (await mod.requestPermission()) === "granted";
      }
      permission = granted ? "granted" : "denied";
    }
    if (permission !== "granted") return;
    mod.sendNotification(payload);
  } catch {
    // Best-effort by contract: a failed fire never breaks the receipt
    // journaling path in the caller.
  }
}
