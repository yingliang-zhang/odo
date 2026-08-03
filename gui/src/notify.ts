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
