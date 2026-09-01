// UX-3a (A2-6a): a run ending in agent_error fires a DISTINGUISHABLE
// failure notification ("run failed" + error first line) — notifyRunDone
// previously fired on agent_done only, so failures in hidden-window runs
// were silent. The __odoRunNotify seam captures the exact payload instead
// of the OS (journal conventions mirror pipeline-chip.spec).
import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
    __odoRunNotify?: (payload: { title: string; body: string }) => void;
    __runNotifies?: { title: string; body: string }[];
  }
}

interface InjectedRow {
  type: string;
  payload: Record<string, unknown>;
}

async function journal(page: Page, rows: InjectedRow[]) {
  await page.evaluate((r) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    for (const row of r) {
      const e = fx.ev(row.type, row.payload, 1); // conv 1 = the boot journal
      e.created_at = new Date().toISOString();
      fx.events.push(e);
    }
  }, rows);
}

test.beforeEach(async ({ page }) => {
  // Capture BEFORE the page mounts: recordEvents only fires for freshly
  // polled events, and the e2e seam preempts the plugin entirely.
  await page.addInitScript(() => {
    window.__runNotifies = [];
    window.__odoRunNotify = (p) => window.__runNotifies!.push(p);
  });
  await page.goto("/");
  await expect(page.locator(".app-statusbar")).toBeVisible();
});

test("agent_error notifies 'run failed' with the error's first line, distinct from agent_done", async ({ page }) => {
  await journal(page, [
    { type: "agent_error", payload: { error: "adapter exploded: wrapper exited 1\nstderr tail line one" } },
    { type: "agent_done", payload: { summary: "landed the batch" } },
  ]);

  await expect
    .poll(
      async () => page.evaluate(() => window.__runNotifies ?? []),
      { timeout: 8_000 },
    )
    .toEqual([
      { title: "Odo: run failed in main", body: "adapter exploded: wrapper exited 1" },
      { title: "Odo: run finished in main", body: "landed the batch" },
    ]);
});

test("daemon advisories (agent_error with odo:true) never notify a failure", async ({ page }) => {
  await journal(page, [
    { type: "agent_error", payload: { error: "odo: 1 parked goal remains queued — the last run errored", odo: true } },
  ]);

  // Nothing to poll for — assert the latch stays empty across several
  // journal ticks instead of waiting out a fixed sleep.
  const settlesAt = Date.now() + 4_000;
  while (Date.now() < settlesAt) {
    await page.waitForTimeout(250);
    expect(await page.evaluate(() => window.__runNotifies ?? [])).toEqual([]);
  }
});
