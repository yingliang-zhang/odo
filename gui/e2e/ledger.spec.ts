import { test, expect } from "@playwright/test";

// A-P0 #1 (Guardian risk taxonomy): the Ledger tab renders review_action
// journal receipts as decision cells — actor (Auto|Human), outcome badge,
// Guardian risk-class badges, honest unrated for pre-W5 rows, timed-out
// chip. Fixture story: one auto-land cycle (review → conflict refresh →
// blocked → landed) then human rows; rendered newest-first.

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  // The receipt fixtures ride fix-daemon-binary (conv 3) — conv 1's
  // transcript is kept clean of review bubbles for the inbox/diff specs.
  await page.locator(".sidebar .ws-row", { hasText: "fix-daemon-binary" }).click();
  await page.keyboard.press("Meta+j");
  await page.getByRole("tab", { name: /Ledger/ }).click();
  await page.locator(".ledger-review-row").first().waitFor();
});

// The pipeline-chip fixtures append a settle-ladder tail to the same conv 3
// story (daemon-true chain shape, settle.go:593-598): three auto_revise_round
// rows on the diff 8→9→10 chain, then the ladder's suspension marker (a
// memory_update — conversation-scoped, NOT a Ledger review row), then the
// blocked{ladder_suspended} echo on diff 11 — the four newest review rows.
// Row order below is pure per-conversation seq order (LedgerPanel sorts by
// seq), so the pre-aged accept's real-time created_at cannot reorder it.
test("review actions list newest-first with actor + outcome badges", async ({ page }) => {
  const rows = page.locator(".ledger-review-row");
  await expect(rows).toHaveCount(11);

  // Newest row = the round-cap suspension's echo block.
  await expect(rows.nth(0)).toContainText("Blocked");
  await expect(rows.nth(0).locator(".ledger-review-detail")).toHaveText("ladder_suspended");
  await expect(rows.nth(0).locator(".badge-actor-auto")).toHaveText("Auto");
  await expect(rows.nth(1)).toContainText("Revise round 3");

  // The pre-W5 human accept slid to fifth newest.
  await expect(rows.nth(4)).toContainText("Accepted");
  await expect(rows.nth(4).locator(".badge-actor-human")).toHaveText("Human");
  await expect(rows.nth(4).locator(".risk-unrated")).toHaveText("unrated");
  await expect(rows.nth(4).locator(".risk-clean")).toHaveCount(0);

  // Oldest row = the auto-panel's unanimous review that opened the cycle.
  await expect(rows.nth(10)).toContainText("Review · accept");
  await expect(rows.nth(10).locator(".badge-actor-auto")).toHaveText("Auto");

  // Actor provenance across the story: 8 auto, 3 human.
  await expect(page.locator(".ledger-review-row .badge-actor-auto")).toHaveCount(8);
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
  // Clean chips: opening review, human reject, three revise rounds, the
  // ladder stop — six rows rate ["none"].
  await expect(page.locator(".risk-badge.risk-clean")).toHaveCount(6);

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

test("ledger.md rendering is untouched below the cells", async ({ page }) => {
  await expect(page.locator(".mem-section-title").last()).toContainText("ledger.md");
  await expect(page.locator(".mem-file")).toBeVisible();
});
