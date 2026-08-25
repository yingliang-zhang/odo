import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

// Repeat project/workstream switches: the per-conversation journal cache
// renders synchronously on click (stale-while-revalidate), and a failed
// bootstrap restores the pre-flip view wholesale.

const MAIN_MARKER = ".bubble-user:has-text('Add a GFM table renderer')";
const FEAT_MARKER = ".bubble-user:has-text('Initial sidebar tree layout')";

async function setBootstrapCtl(
  page: Page,
  ctl: { delayMs?: number; fail?: boolean },
) {
  await page.evaluate((next) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    if (next.delayMs !== undefined) fx.bootstrapCtl.delayMs = next.delayMs;
    if (next.fail !== undefined) fx.bootstrapCtl.fail = next.fail;
  }, ctl);
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(MAIN_MARKER)).toBeVisible();
});

test.afterEach(async ({ page }) => {
  await setBootstrapCtl(page, { delayMs: 0, fail: false });
});

test("repeat workstream switch renders the cached journal before bootstrap lands", async ({ page }) => {
  const sidebar = page.locator(".sidebar");
  const feat = sidebar.locator(".ws-item", { hasText: "feat-sidebar-tree" });
  const main = sidebar.locator(".ws-item", { hasText: "main" });

  // Warm both sides of the cache with authoritative landings.
  await feat.click();
  await expect(page.locator(FEAT_MARKER)).toBeVisible();
  await main.first().click();
  await expect(page.locator(MAIN_MARKER)).toBeVisible();

  // Hold the bootstrap answer; nothing else can produce the cached rows.
  await setBootstrapCtl(page, { delayMs: 2500 });
  await feat.click();

  // Synchronous samples — no web-first retry, so a hit can only come from
  // the switch cache's optimistic flip, not the (held) bootstrap.
  expect(await page.locator(FEAT_MARKER).count()).toBeGreaterThan(0);
  expect(await page.locator(MAIN_MARKER).count()).toBe(0);

  // … and the landing keeps the same rows once the held bootstrap merges.
  await setBootstrapCtl(page, { delayMs: 0 });
  await expect(page.locator(FEAT_MARKER)).toBeVisible({ timeout: 8_000 });
  await expect(page.locator(".app-statusbar")).toContainText("feat-sidebar-tree");
});

test("failed repeat switch restores the pre-flip view and stays retryable", async ({ page }) => {
  const sidebar = page.locator(".sidebar");
  const feat = sidebar.locator(".ws-item", { hasText: "feat-sidebar-tree" });

  // Warm feat's cache entry.
  await feat.click();
  await expect(page.locator(FEAT_MARKER)).toBeVisible();
  // MAIN_MARKER below is the optimistic flip's synchronous cache render —
  // main's authoritative bootstrap is still in flight behind the mock's
  // fixed 50ms latency, and arming fail BEFORE it lands fails the MAIN
  // switch (its rollback correctly restores feat) instead of feat's. The
  // serve-time counter proves the knobs were already consulted for it.
  const mainLandings = () =>
    page.evaluate(() => {
      const fx = window.__odoFixtures;
      if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
      return fx.bootstrapLandings["1"] ?? 0;
    });
  const landedBefore = await mainLandings();
  await sidebar.locator(".ws-item", { hasText: "main" }).first().click();
  await expect(page.locator(MAIN_MARKER)).toBeVisible();
  await expect.poll(mainLandings, { timeout: 8_000 }).toBe(landedBefore + 1);

  // Daemon unreachable for the target: the optimistic flip renders……
  // The delay pins a window the reject cannot land inside (the mock
  // consults it before the fail knob), so the synchronous sample below is
  // as race-free as test 1's. Without it the immediate reject can roll the
  // flip back before the sample lands under load.
  await setBootstrapCtl(page, { delayMs: 2500, fail: true });
  await feat.click();
  expect(await page.locator(FEAT_MARKER).count()).toBeGreaterThan(0);
  // Release the delay for later bootstraps; the already-held one still
  // fails when its timer fires (the web-first banner wait covers that).
  await setBootstrapCtl(page, { delayMs: 0 });

  // …then the failure rolls the whole view back — pre-flip journal,
  // workstream attribution, error explained in the banner. The banner is
  // web-first here: it explicitly waits out the in-flight bootstrap.
  await expect(page.locator(".error-banner")).toContainText("switch failed", { timeout: 8_000 });
  expect(await page.locator(MAIN_MARKER).count()).toBeGreaterThan(0);
  expect(await page.locator(FEAT_MARKER).count()).toBe(0);
  await expect(page.locator(".app-statusbar")).toContainText("main");

  // The failed target must not latch as "current": clicking it again (as
  // soon as the daemon recovers) retries instead of no-oping on the
  // workstreamId === workstream?.id guard.
  await setBootstrapCtl(page, { fail: false });
  await feat.click();
  await expect(page.locator(FEAT_MARKER)).toBeVisible({ timeout: 8_000 });
});
