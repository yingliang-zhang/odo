import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// P2.3 (docs/design/adoption-lock.md): the typed failure overlay replaces
// the single daemon-down banner. Three consecutive poll failures arm it,
// keyed by classification (errors.ts ERROR_RULES + FAILURE_TAXONOMY):
// distinct title + one-line cause + the class's leading action. Dismiss
// hides the overlay until the CLASS changes; unclassified strings keep
// the legacy banner. The poll loop's backoff stretches ONE 3-failure arm
// to worst-case ~25s in the mock — each test's timeout must cover its own
// arm count (the dismiss test arms twice) plus the 8s dismissed-window
// assertion. Timeouts account for it, never sleeps.

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

const ARM = { timeout: 45_000 };
const HEAL = { timeout: 10_000 };

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test.afterEach(async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (fx) {
      fx.pollCtl.fail = false;
      fx.pollCtl.error = "closed the connection without responding";
    }
  });
});

async function armPoll(page: Page, error: string) {
  await page.evaluate((err) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.pollCtl.error = err;
    fx.pollCtl.fail = true;
  }, error);
}

test("socket-closed classification: overlay with title, cause, Reconnect", async ({ page }) => {
  await armPoll(page, "closed the connection without responding");
  const overlay = page.locator('[data-slot="failure-overlay"]');
  await expect(overlay).toBeVisible(ARM);
  await expect(overlay.locator(".failure-overlay-title")).toHaveText("Daemon socket closed");
  await expect(overlay.locator(".failure-overlay-cause")).toContainText("socket");
  // socket_closed's leading action is Reconnect; the other two actions
  // are not rendered for this class.
  await expect(overlay.locator(".failure-overlay-action")).toHaveText("Reconnect");
  await expect(page.locator(".daemon-down-banner")).toHaveCount(0);

  // Recovery: unarm, click Reconnect → counters reset, next healthy tick
  // hides the overlay.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (fx) fx.pollCtl.fail = false;
  });
  await overlay.locator(".failure-overlay-action").click();
  await expect(overlay).toHaveCount(0, HEAL);
});

test("dismiss hides the overlay until the failure CLASS changes", async ({ page }) => {
  // Two back-to-back arms (≤ ~25s each under poll backoff) + the 8s
  // dismissed window + assertion tails exceed the default 30s cap —
  // observed as a first-attempt timeout on the second arm under load.
  test.setTimeout(90_000);
  await armPoll(page, "closed the connection without responding");
  const overlay = page.locator('[data-slot="failure-overlay"]');
  await expect(overlay).toBeVisible(ARM);
  await overlay.locator(".failure-overlay-dismiss").click();
  await expect(overlay).toHaveCount(0);
  // Same class keeps failing: dismissed — nothing re-arms (observed across
  // two backoff intervals).
  await expect(overlay).toHaveCount(0, { timeout: 8_000 });
  // Class change re-arms: heartbeat timeout is a different taxonomy row.
  await armPoll(page, "daemon did not answer on /tmp/t/odo.sock within 3000ms");
  await expect(overlay).toBeVisible(ARM);
  await expect(overlay.locator(".failure-overlay-title")).toHaveText("Daemon stopped answering");
  await expect(overlay.locator(".failure-overlay-action")).toHaveText("Reconnect");
});

test("unclassified poll failures keep the legacy banner (no overlay)", async ({ page }) => {
  await armPoll(page, "kaboom: something entirely novel broke");
  await expect(page.locator(".daemon-down-banner")).toBeVisible(ARM);
  await expect(page.locator('[data-slot="failure-overlay"]')).toHaveCount(0);
});

test("version-mismatch classification leads with Copy diagnostics", async ({ page }) => {
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await armPoll(page, "invalid daemon response: bad shape");
  const overlay = page.locator('[data-slot="failure-overlay"]');
  await expect(overlay).toBeVisible(ARM);
  await expect(overlay.locator(".failure-overlay-title")).toHaveText("GUI/daemon version mismatch");
  const action = overlay.locator(".failure-overlay-action");
  await expect(action).toHaveText("Copy diagnostics");
  await action.click();
  const pasted = await page.evaluate(() => navigator.clipboard.readText());
  expect(pasted).toContain('"pollFailures"');
  expect(pasted).toContain("invalid daemon response");
});
