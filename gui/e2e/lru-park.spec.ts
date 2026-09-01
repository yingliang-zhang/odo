import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";

// P2.4 (docs/design/adoption-lock.md): the ContextPanel keep-alive LRU
// park. At most 3 tabs stay mounted (active + 2 most-recent); deeper tabs
// unmount and show a parked badge in the strip until re-activation
// remounts them. Memory/Wiki tabs with unsaved input are draft-exempt.

const POLL = { timeout: 4000 };

async function openPanel(page: Page) {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
}

async function clickTab(page: Page, name: string) {
  await page.getByRole("tab", { name: new RegExp(`^${name}`) }).click();
}

// Tab strip buttons accrue badge counts into their accessible name, so
// label prefixed regexes are the robust locator (context-panel-tabs.spec
// precedent).
function tabButton(page: Page, name: string) {
  return page.getByRole("tab", { name: new RegExp(`^${name}`) });
}

// Mounted panel bodies: .panel-body children (one wrapper per tab) that
// have any rendered descendant. Parked tabs keep the wrapper but drop the
// subtree, so this count IS the kept-alive tally.
async function mountedBodyCount(page: Page): Promise<number> {
  return page.evaluate(
    () =>
      Array.from(document.querySelectorAll(".context-panel .panel-body > div"))
        .filter((el) => el instanceof HTMLElement && el.childElementCount > 0)
        .length,
  );
}

test("park beyond 3 mounted: badge appears; re-activation remounts and clears it", async ({ page }) => {
  await openPanel(page);

  // tasks is the seed tab (UX-1 D2 default). Activate three more in
  // sequence: mru = skills, review, wiki, tasks → tasks parks.
  await clickTab(page, "Wiki");
  await clickTab(page, "Review");
  await clickTab(page, "Skills");

  await expect(tabButton(page, "Tasks").locator('[data-slot="parked-badge"]')).toBeVisible(POLL);
  await expect.poll(() => mountedBodyCount(page), POLL).toBe(3);

  // Re-activation remounts the body and clears the badge; the LRU tail
  // (Wiki) parks instead.
  await clickTab(page, "Tasks");
  await expect(tabButton(page, "Tasks").locator('[data-slot="parked-badge"]')).toHaveCount(0);
  await expect(tabButton(page, "Wiki").locator('[data-slot="parked-badge"]')).toBeVisible(POLL);
  await expect.poll(() => mountedBodyCount(page), POLL).toBe(3);
});

test("draft-exempt: a Wiki search draft keeps the tab mounted outside the cap", async ({ page }) => {
  await openPanel(page);

  // Arm the Wiki draft (non-empty search query is the draft contract).
  await clickTab(page, "Wiki");
  await page.locator(".context-panel input").first().fill("epoch");
  // Push three more activations deep: Review → Skills → Runs (A3-2:
  // the Ledger tab is gone; Runs carries the receipts fold now).
  await clickTab(page, "Review");
  await clickTab(page, "Skills");
  await clickTab(page, "Runs");

  // Wiki never parks: no badge, and the browser stayed mounted (the
  // typed query still sits in its input). 4 bodies mount (cap 3 + exempt).
  await expect.poll(() => tabButton(page, "Wiki").locator('[data-slot="parked-badge"]').count(), POLL).toBe(0);
  await expect(page.locator(".context-panel input").first()).toHaveValue("epoch");
  await expect.poll(() => mountedBodyCount(page), POLL).toBe(4);
  // Wiki was exempt, so the evicted depth-3 seat belongs to the seed tab
  // (Tasks — UX-1 D2 default; Changes was never mounted in this flow).
  await expect(tabButton(page, "Tasks").locator('[data-slot="parked-badge"]')).toBeVisible(POLL);
});
