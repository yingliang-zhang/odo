import { test, expect } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// UX-4 / A3-2 (ux-batch-lock-amendment-a3, 2026-09-01): the Ledger TAB is
// gone from the ContextPanel strip. Its review_action receipts (A-P0 #1,
// Guardian risk taxonomy — actor Auto|Human, outcome badge, risk-class
// badges, honest unrated for pre-W5 rows, timed-out chip) folded into the
// Runs tab under the same .ledger-review-row identity classes; the
// ledger.md file view retargeted to the TopBar overflow's "Ledger" item,
// which opens it through the Preview tab's read_file pathway. Fixture
// story unchanged: one auto-land cycle (review → conflict refresh →
// blocked → landed) then human rows, rendered newest-first.

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

test.describe("receipts fold into the Runs tab", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
    // The receipt fixtures ride fix-daemon-binary (conv 3) — conv 1's
    // transcript is kept clean of review bubbles for the inbox/diff specs.
    await page.locator(".sidebar .ws-row", { hasText: "fix-daemon-binary" }).click();
    await page.keyboard.press("Meta+j");
    await page.getByRole("tab", { name: /Runs/ }).click();
    await page.locator(".ledger-review-row").first().waitFor();
  });

  // The pipeline-chip fixtures append a settle-ladder tail to the same conv 3
  // story (daemon-true chain shape, settle.go:593-598; cap 2 since 2026-08-23):
  // two auto_revise_round rows on the diff 8→9 chain, then the ladder's
  // suspension marker (a memory_update — conversation-scoped, NOT a review
  // receipt), then the blocked{ladder_suspended} echo on diff 10 — the
  // three newest review rows.
  // Row order below is pure per-conversation seq order (reviewReceipts sorts
  // by seq), so the pre-aged accept's real-time created_at cannot reorder it.
  test("review actions list newest-first with actor + outcome badges", async ({ page }) => {
    const rows = page.locator(".ledger-review-row");
    await expect(rows).toHaveCount(10);

    // Newest row = the round-cap suspension's echo block.
    await expect(rows.nth(0)).toContainText("Blocked");
    await expect(rows.nth(0).locator(".ledger-review-detail")).toHaveText("ladder_suspended");
    await expect(rows.nth(0).locator(".badge-actor-auto")).toHaveText("Auto");
    await expect(rows.nth(1)).toContainText("Revise round 2");

    // The pre-W5 human accept slid to fourth newest.
    await expect(rows.nth(3)).toContainText("Accepted");
    await expect(rows.nth(3).locator(".badge-actor-human")).toHaveText("Human");
    await expect(rows.nth(3).locator(".risk-unrated")).toHaveText("unrated");
    await expect(rows.nth(3).locator(".risk-clean")).toHaveCount(0);

    // Oldest row = the auto-panel's unanimous review that opened the cycle.
    await expect(rows.nth(9)).toContainText("Review · accept");
    await expect(rows.nth(9).locator(".badge-actor-auto")).toHaveText("Auto");

    // Actor provenance across the story: 7 auto, 3 human.
    await expect(page.locator(".ledger-review-row .badge-actor-auto")).toHaveCount(7);
    await expect(page.locator(".ledger-review-row .badge-actor-human")).toHaveCount(3);
  });

  test("Guardian risk badges are color-classed per severity rank", async ({ page }) => {
    // credential_probe → critical (red) — on both the blocked and the landed diff.
    await expect(page.locator(".risk-badge.risk-critical")).toHaveCount(2);
    await expect(page.locator(".risk-badge.risk-critical").first()).toHaveText("credential_probe");

    // Multi-label accept row: critical + low (supply_chain), applied in order.
    const landed = page.locator('.ledger-review-row[data-action="accept"]').filter({
      has: page.locator(".badge-actor-auto"),
    });
    await expect(landed.locator(".risk-badge.risk-low")).toHaveText("supply_chain");

    // security_weakening → medium (yellow); ["none"] renders the one clean chip per row.
    await expect(page.locator(".risk-badge.risk-medium")).toHaveText("security_weakening");
    // Clean chips: opening review, human reject, two revise rounds, the
    // ladder stop — five rows rate ["none"].
    await expect(page.locator(".risk-badge.risk-clean")).toHaveCount(5);

    // Trigger artifact rides the tooltip (journal receipt, one per class).
    await expect(page.locator(".risk-badge.risk-medium")).toHaveAttribute(
      "title",
      "+InsecureSkipVerify: true at net/client.go:88",
    );
  });

  test("outcome variants: blocked reason, refresh phase, timed-out review", async ({ page }) => {
    // Two blocked rows share the story: the ladder suspension (newest) and
    // the stale-base stop from the original auto-land cycle.
    const blocked = page.locator('.ledger-review-row[data-action="auto_land_blocked"]');
    await expect(blocked).toHaveCount(2);
    await expect(blocked.nth(0).locator(".badge-blocked")).toHaveText("Blocked");
    await expect(blocked.nth(0).locator(".ledger-review-detail")).toHaveText("ladder_suspended");
    await expect(blocked.nth(1).locator(".ledger-review-detail")).toHaveText("base_stale");

    const refresh = page.locator('.ledger-review-row[data-action="refresh_attempted"]');
    await expect(refresh.locator(".badge-refresh")).toHaveText("Refresh conflict");
    await expect(refresh.locator(".ledger-review-detail")).toHaveText("pre_spend_probe");
    // No risk receipt, and no fake "unrated": a refresh never rates.
    await expect(refresh.locator(".risk-badge")).toHaveCount(0);

    // The mixed panel that timed out keeps both marks.
    const mixed = page.locator('.ledger-review-row[data-action="moa_review"]', {
      hasText: "Review · mixed",
    });
    await expect(mixed.locator(".risk-timeout")).toHaveText("timed out");
    await expect(page.locator(".risk-timeout")).toHaveCount(1);
  });
});

test("ledger.md opens through TopBar overflow → Preview tab (A3-2)", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.previewFiles[".odo/ledger.md"] = { content: fx.ledgerContent };
  });
  await page.locator(".topbar-action[aria-label='More actions']").click();
  await page.locator(".topbar-overflow-item", { hasText: "Ledger" }).click();

  const panel = page.locator(".context-panel");
  await expect(panel).toBeVisible();
  await expect(panel.locator('[role="tab"][aria-selected="true"]', { hasText: "Preview" })).toBeVisible();
  await expect(panel.locator(".preview-head-path")).toContainText(".odo/ledger.md");
  await expect(panel.locator(".preview-code")).toContainText("# Ledger");
});

test("the Ledger tab is gone from the strip — 9 tabs post-diet (A3-1)", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
  await expect(page.locator(".panel-tab")).toHaveCount(9);
  await expect(page.getByRole("tab", { name: "Ledger", exact: true })).toHaveCount(0);
});
