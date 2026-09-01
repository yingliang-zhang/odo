// UX-3b (A2-6b): daemon advisories journal as agent_error with odo:true
// (journalRunAdvisory). Before this batch nothing in the GUI read the
// flag, so housekeeping rows rendered as red run failures and taught the
// user to ignore red. These specs pin:
//   1. the transcript bubble is AMBER + labeled, never the red failure
//      bubble (run-notify.spec pins the notification exclusion);
//   2. an advisory landing after a run's agent_done does NOT flip that
//      run's header to ✗ (ChatSurface folds only real agent_errors).
// Journal injection mirrors run-notify.spec (fx.ev + fx.events.push).
import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
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
  await page.goto("/");
  await expect(page.locator(".app-statusbar")).toBeVisible();
});

test("a daemon advisory renders as an amber labeled bubble, never the red failure one", async ({ page }) => {
  await journal(page, [
    { type: "agent_error", payload: { error: "odo: verify unconfigured for this worktree", odo: true } },
  ]);

  const advisory = page.locator(".bubble-advisory");
  await expect(advisory).toBeVisible({ timeout: 8_000 });
  await expect(advisory.locator(".bubble-advisory-label")).toHaveText("odo advisory");
  await expect(advisory).toContainText("odo: verify unconfigured");
  // The red failure treatment must not touch the advisory row.
  await expect(page.locator(".bubble-error", { hasText: "verify unconfigured" })).toHaveCount(0);
});

test("an advisory after agent_done leaves the run header green (no fabricated ✗)", async ({ page }) => {
  // Sanity: the boot journal's runs all closed ok, so every header is a
  // done header before the injection.
  await expect(page.locator(".run-header .run-status.done").first()).toBeVisible();
  await expect(page.locator(".run-header .run-status.error")).toHaveCount(0);

  await journal(page, [
    { type: "agent_error", payload: { error: "odo: 1 parked goal remains queued", odo: true } },
  ]);
  await expect(page.locator(".bubble-advisory")).toBeVisible({ timeout: 8_000 });

  await expect(page.locator(".run-header .run-status.error")).toHaveCount(0);
});

test("a real agent_error still gets the red failure bubble (contrast)", async ({ page }) => {
  await journal(page, [{ type: "agent_error", payload: { error: "adapter exploded" } }]);

  await expect(page.locator(".bubble-error", { hasText: "adapter exploded" })).toBeVisible({ timeout: 8_000 });
  await expect(page.locator(".bubble-advisory")).toHaveCount(0);
});
