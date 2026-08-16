import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";

// Epoch-fold chip (root fix + blind-spot follow-up): the latest distill
// folds older journal events out of the chat log behind a persistent chip
// instead of silently clearing the surface — except the newest run at or
// below the boundary, which always stays visible so the fold never blanks
// the most recent agent run. Fixtures: conversation 3 has a pinned fold
// window (2 events hidden + the kept patch run) with epoch-2 activity
// after it; conversation 2 carries a two-distill history (legacy marker) —
// the newest layout run is kept while the sketch run AND the older distill
// marker count toward the chip (3 hidden), because expansion reveals all
// three.

async function switchToWorkstream(page: Page, name: string) {
  await page.locator(".sidebar .ws-item", { hasText: name }).click();
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("no fold, no chip — the default workstream shows its full log", async ({ page }) => {
  await expect(page.locator(".fold-chip")).toHaveCount(0);
  await expect(page.locator(".bubble-user").first()).toBeVisible();
});

test("partial fold: newest pre-fold run stays visible; chip counts only hidden events", async ({ page }) => {
  await switchToWorkstream(page, "fix-daemon-binary");

  const chip = page.locator(".fold-chip");
  await expect(chip).toBeVisible();
  // The folded epoch rides the label; the note path rides the hover.
  await expect(chip).toContainText("epoch 1 · 2 events folded · click to expand");
  await expect(chip.locator(".fold-chip-text")).toHaveAttribute(
    "title",
    /fix-daemon-binary-epoch-1\.md$/,
  );

  // The fold's dual behavior: the older folded run stays hidden…
  await expect(page.locator("text=Bootstrap the daemon shims")).toHaveCount(0);
  // … while the newest pre-fold run survives the fold.
  await expect(page.locator(".bubble-user", { hasText: "Patch the daemon launch path" })).toBeVisible();
  await expect(page.locator("text=Daemon launch path patched")).toBeVisible();

  // The committed-phase row sits BELOW the marker in journal order but
  // above its pinned last_seq — it stays visible above the chip.
  await expect(page.locator(".bubble-user", { hasText: "stale socket" })).toBeVisible();
  // Post-fold activity is visible.
  await expect(page.locator(".bubble-user", { hasText: "socket perms" })).toBeVisible();
  // … while the pinned marker itself stays bookkeeping: chip, not badge.
  await expect(page.locator("text=Distilled · epoch 2")).toHaveCount(0);

  // Expand reveals the raw journal: folded run, kept run, marker badge.
  await chip.getByRole("button", { name: "Expand" }).click();
  await expect(page.locator("text=Bootstrap the daemon shims")).toBeVisible();
  await expect(page.locator("text=Distilled · epoch 2")).toBeVisible();
  await expect(page.locator(".bubble-user", { hasText: "Patch the daemon launch path" })).toBeVisible();

  // Collapse re-hides the folded run and the marker — the kept run does
  // not fold away again.
  await chip.getByRole("button", { name: "Collapse" }).click();
  await expect(page.locator("text=Bootstrap the daemon shims")).toHaveCount(0);
  await expect(page.locator("text=Distilled · epoch 2")).toHaveCount(0);
  await expect(page.locator(".bubble-user", { hasText: "Patch the daemon launch path" })).toBeVisible();
});

test("Open note pivots to the wiki panel and reads the folded note", async ({ page }) => {
  await switchToWorkstream(page, "fix-daemon-binary");
  await page.locator(".fold-chip").getByRole("button", { name: "Open note" }).click();

  // The context panel opens on the wiki tab and the reader shows content
  // (mock read_wiki serves the fixture note body).
  await expect(page.locator(".wiki-panel")).toBeVisible();
  await expect(page.locator(".wiki-content")).toContainText("Epoch 2");
});

test("two-distill fold: the older marker counts, the last run stays above the chip", async ({ page }) => {
  await switchToWorkstream(page, "feat-sidebar-tree");

  // Derived boundary (no journaled window): everything at or below the
  // subject marker's seq folds except the newest run. The chip counts the
  // sketch run AND the older distill marker — 3 hidden — because
  // expansion reveals all three.
  const chip = page.locator(".fold-chip");
  await expect(chip).toContainText("epoch 2 · 3 events folded · click to expand");
  await expect(page.locator("text=Sketch the sidebar sections")).toHaveCount(0);
  await expect(page.locator("text=Distilled · epoch 2")).toHaveCount(0); // older marker badge stays hidden
  await expect(page.locator(".bubble-user", { hasText: "Initial sidebar tree layout" })).toBeVisible();
  await expect(page.locator("text=Sidebar tree landed")).toBeVisible();

  // The dichotomy: this remains a folded conversation, not a fresh one —
  // and the kept run means no empty state is needed either.
  await expect(page.locator(".empty-state")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Welcome to Odo" })).toHaveCount(0);

  // Expand opens the raw journal — folded run and both marker badges.
  await chip.getByRole("button", { name: "Expand" }).click();
  await expect(page.locator("text=Sketch the sidebar sections")).toBeVisible();
  await expect(page.locator("text=Distilled · epoch 2")).toBeVisible();
  await expect(page.locator("text=Distilled · epoch 3")).toBeVisible();

  // Collapse folds the older run away again; the kept run never folds.
  await chip.getByRole("button", { name: "Collapse" }).click();
  await expect(page.locator("text=Sketch the sidebar sections")).toHaveCount(0);
  await expect(page.locator("text=Distilled · epoch 2")).toHaveCount(0);
  await expect(page.locator(".bubble-user", { hasText: "Initial sidebar tree layout" })).toBeVisible();
});
