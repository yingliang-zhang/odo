import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Diff: accept/reject the pending diff that fixtures seed for conversation 1.
// The bootstrapped "main" workstream (ws.id=1) has a pending diff (fixtures.ts:110).

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

const POLL = { timeout: 4000 };
// Latch waits straddle one full idle poll tick (~1.5 s) plus jitter.
const LATCH = { timeout: 8000 };

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

// UX-1 D2: the panel opens on the default tab (Tasks); the specs below
// assert Changes-tab content, so activate it explicitly. Badge counts
// accrue into the tab's accessible name — prefix regex (lru-park
// precedent).
async function openChangesTab(page: Page) {
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
  await page.getByRole("tab", { name: /^Changes/ }).click();
}

test("pending diff is visible in context panel", async ({ page }) => {
  // UX-1 D2 landing rule: with Tasks as the default tab, a diff ARRIVING
  // while the panel is closed auto-opens it landed on Changes — the news —
  // never on the unrelated default. App's M9 P2 transition fires only on a
  // genuine 0→1 count edge with the panel closed, and the bootstrap latch
  // suppresses the seeded diff, so drive the edge through the poll path:
  // clear the fixture list, let a poll consume the zero (the status-bar
  // chip is poll-derived, its disappearance IS the latch), then re-add.
  // No Meta+j, no tab click — the panel must open itself onto Changes.
  const chip = page.locator('[data-chip="diffs"]');
  await expect(chip).toBeVisible(POLL);
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.changesDiffs.length = 0;
  });
  await expect(chip).toHaveCount(0, LATCH);
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.changesDiffs.push(fx.pendingDiff);
  });

  // 0→1 on a closed panel: opens itself, landed on Changes (selected
  // before any manual interaction).
  await expect(page.locator(".context-panel")).toBeVisible(LATCH);
  await expect(page.getByRole("tab", { name: /^Changes/ })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".diff-card")).toBeVisible();
});

test("accept diff applies it and shows badge", async ({ page }) => {
  await openChangesTab(page);
  await expect(page.locator(".diff-card")).toBeVisible();

  // Accept is a two-step flow (DiffViewer "Tri-model right sidebar gap"): the
  // header button opens an inline editor for the commit message; accepting it
  // (button or Enter) fires accept_diff. Code intent is badge-on-resolve —
  // the resolved card persists as a record card rendering "badge badge-accept"
  // + "Applied" (DiffViewer.tsx; ui/badge.tsx documents those classes as the
  // e2e hooks) — so this test walks the full accept flow and asserts the badge.
  await page.locator(".diff-header .btn-accept").click();
  const editor = page.locator(".diff-commit-editor");
  await expect(editor).toBeVisible();
  // Editor is prefilled with the daemon default; user can edit before it lands.
  await expect(editor.locator(".diff-commit-input")).toHaveValue("odo: accept diff #1");
  await editor.locator(".btn-accept").click();

  // After accept, the diff card should show "Applied" badge
  await expect(page.locator(".badge-accept")).toBeVisible();
});

test("reject diff shows badge", async ({ page }) => {
  await openChangesTab(page);
  await expect(page.locator(".diff-card")).toBeVisible();

  // Click Reject
  await page.locator(".btn-reject").click();

  // After reject, the diff card should show "Rejected" badge
  await expect(page.locator(".badge-reject")).toBeVisible();
});

test("review button triggers tri-model review", async ({ page }) => {
  await openChangesTab(page);
  await expect(page.locator(".diff-card")).toBeVisible();

  // Click Review
  await page.locator(".btn-review").click();

  // Review results should appear with reviewer verdicts
  await expect(page.locator(".review-results")).toBeVisible();
  await expect(page.locator(".review-item")).toHaveCount(2);
});
