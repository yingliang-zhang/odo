// M3 visibility (spec §3b): desktop notification when a run finishes while
// the window is hidden. Spec §3b mandates a lazy import() of the plugin so
// the initial bundle and startup never pay for it (exception to the static-
// import default); every failure is swallowed — notifications are a nicety
// that must never reach the poll loop.

const BODY_LEN = 80;

// Permission is asked once, lazily, on the first qualifying event; a denial
// is not re-litigated mid-session.
let permission: "unknown" | "granted" | "denied" = "unknown";

// UX-3a (A2-6a) e2e seam (the __odoLoopNotify precedent): when a test
// installs the hook it receives the exact run-notification payload instead
// of the OS — the real plugin never loads in a browser. Absent in
// production builds.
export interface RunNotifyPayload {
  title: string;
  body: string;
}
declare global {
  interface Window {
    __odoRunNotify?: (payload: RunNotifyPayload) => void;
  }
}

// Shared paint path for run-terminal notifications (done + failed): the
// e2e hook short-circuits EVERYTHING (production never installs it),
// otherwise the M3 spec §3b contract holds — notify only while hidden,
// lazy-import the plugin, never throw into the poll loop.
async function sendRunNotification(title: string, body: string): Promise<void> {
  const payload: RunNotifyPayload = {
    title,
    body: body.length > BODY_LEN ? body.slice(0, BODY_LEN) : body,
  };
  try {
    if (typeof window !== "undefined" && window.__odoRunNotify != null) {
      window.__odoRunNotify(payload);
      return;
    }
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
    mod.sendNotification(payload);
  } catch {
    // Best-effort by contract: never throw into the caller (the poll loop).
  }
}

export async function notifyRunDone(workstreamName: string, summary: string): Promise<void> {
  await sendRunNotification(`Odo: run finished in ${workstreamName}`, summary);
}

// UX-3a (A2-6a): a failed background run notifies with a DISTINCT title —
// "run failed" must never read like a completion. Body is the error's
// first line only (multiline adapter dumps stay in the transcript).
export async function notifyRunFailed(workstreamName: string, error: string): Promise<void> {
  const firstLine = error.split("\n")[0]?.trim() ?? "";
  await sendRunNotification(`Odo: run failed in ${workstreamName}`, firstLine === "" ? "unknown error" : firstLine);
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
