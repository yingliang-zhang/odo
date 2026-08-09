import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";

// Epoch-fold chip (root fix): the latest distill folds older journal events
// out of the chat log behind a persistent chip instead of silently clearing
// the surface. Fixtures: conversation 3 has a partial fold (2 folded events
// + explicit window) with epoch-2 activity after it; conversation 2 has
// everything folded behind a legacy marker (derived window).

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

test("partial fold: chip announces count + note, Expand reveals and Collapse re-hides", async ({ page }) => {
  await switchToWorkstream(page, "fix-daemon-binary");

  const chip = page.locator(".fold-chip");
  await expect(chip).toBeVisible();
  await expect(chip).toContainText("2 events folded");
  await expect(chip).toContainText("fix-daemon-binary-epoch-1");

  // Post-fold events are visible; pre-fold events are not.
  await expect(page.locator(".bubble-user", { hasText: "socket perms" })).toBeVisible();
  await expect(page.locator("text=Patch the daemon launch path")).toHaveCount(0);

  // Expand reveals the folded record, including the distill marker itself.
  await chip.getByRole("button", { name: "Expand" }).click();
  await expect(page.locator("text=Patch the daemon launch path")).toBeVisible();
  await expect(page.locator("text=Distilled · epoch 2")).toBeVisible();

  await chip.getByRole("button", { name: "Collapse" }).click();
  await expect(page.locator("text=Patch the daemon launch path")).toHaveCount(0);
  await expect(page.locator(".bubble-user", { hasText: "socket perms" })).toBeVisible();
});

test("Open note pivots to the wiki panel and reads the folded note", async ({ page }) => {
  await switchToWorkstream(page, "fix-daemon-binary");
  await page.locator(".fold-chip").getByRole("button", { name: "Open note" }).click();

  // The context panel opens on the wiki tab and the reader shows content
  // (mock read_wiki serves the fixture note body).
  await expect(page.locator(".wiki-panel")).toBeVisible();
  await expect(page.locator(".wiki-content")).toContainText("Epoch 2");
});

test("folded-all: the empty state says folded, not welcome; Expand reveals the record", async ({ page }) => {
  await switchToWorkstream(page, "feat-sidebar-tree");

  // The dichotomy: this is a folded conversation, not a fresh one.
  await expect(page.locator(".fold-chip")).toContainText("2 events folded");
  const empty = page.locator(".empty-state");
  await expect(empty).toContainText("Everything here is folded");
  await expect(empty).toContainText("All 2 events");
  await expect(page.getByRole("heading", { name: "Welcome to Odo" })).toHaveCount(0);

  await empty.getByRole("button", { name: "Expand the folded record" }).click();
  await expect(page.locator("text=Initial sidebar tree layout")).toBeVisible();
});
