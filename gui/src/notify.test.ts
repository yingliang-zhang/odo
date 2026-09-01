// UX-3a (A2-6a): the run-failure notification must be OBSERVABLE in e2e
// without the OS plugin (the __odoRunNotify seam), distinct from the run-
// done title, and immune to the hidden-window gate when the hook is set.

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { notifyRunDone, notifyRunFailed } from "./notify";

describe("run-terminal notifications (UX-3a)", () => {
  const captured: { title: string; body: string }[] = [];

  beforeEach(() => {
    captured.length = 0;
    window.__odoRunNotify = (p) => captured.push(p);
    vi.unstubAllGlobals();
  });
  afterEach(() => {
    delete window.__odoRunNotify;
    vi.unstubAllGlobals();
  });

  it("fires a distinct failure title with the error's first line", async () => {
    await notifyRunFailed("feat-x", "adapter exploded\nstack frame 1\nstack frame 2");
    expect(captured).toEqual([{ title: "Odo: run failed in feat-x", body: "adapter exploded" }]);
  });

  it("never confuses a failure with a completion", async () => {
    await notifyRunDone("feat-x", "did the thing");
    await notifyRunFailed("feat-x", "boom");
    expect(captured[0]?.title).toBe("Odo: run finished in feat-x");
    expect(captured[1]?.title).toBe("Odo: run failed in feat-x");
  });

  it("an empty or whitespace error degrades to 'unknown error'", async () => {
    await notifyRunFailed("main", "   ");
    expect(captured[0]?.body).toBe("unknown error");
  });

  it("caps the body at 80 chars", async () => {
    await notifyRunFailed("main", "x".repeat(200));
    expect(captured[0]?.body).toHaveLength(80);
  });

  it("the hook sees payloads regardless of the hidden-window gate", async () => {
    expect(document.hidden).toBe(false); // jsdom default: visible
    await notifyRunFailed("main", "boom");
    expect(captured).toHaveLength(1); // visible window would gate the OS fire
  });

  it("without the hook, a visible window fires nothing (no OS round-trip)", async () => {
    delete window.__odoRunNotify;
    expect(document.hidden).toBe(false);
    // No hook → the hidden gate returns before the plugin even loads; the
    // promise just resolves with no observable side effect.
    await expect(notifyRunFailed("main", "boom")).resolves.toBeUndefined();
    await expect(notifyRunDone("main", "done")).resolves.toBeUndefined();
  });
});
