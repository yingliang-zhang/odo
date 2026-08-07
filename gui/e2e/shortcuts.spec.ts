import { test, expect } from "@playwright/test";

// Keyboard shortcuts: ⌘B (sidebar), ⌘J (panel), ⌘K (palette), ⌘N (new workstream),
// ⌘, (settings), ⌘F (search), Esc (close overlays).

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar-app")).toBeVisible();
});

test("⌘B toggles sidebar collapse", async ({ page }) => {
  // Sidebar starts expanded
  await expect(page.locator(".sidebar")).toHaveAttribute(
    "data-sidebar-state",
    "expanded"
  );

  // Collapse with ⌘B
  await page.keyboard.press("Meta+b");
  await expect(page.locator(".sidebar")).toHaveAttribute(
    "data-sidebar-state",
    "collapsed"
  );

  // Expand again
  await page.keyboard.press("Meta+b");
  await expect(page.locator(".sidebar")).toHaveAttribute(
    "data-sidebar-state",
    "expanded"
  );
});

test("⌘K opens command palette", async ({ page }) => {
  await page.keyboard.press("Meta+k");

  // Palette overlay visible
  await expect(page.locator(".palette-overlay")).toBeVisible();

  // Esc closes it
  await page.keyboard.press("Escape");
  await expect(page.locator(".palette-overlay")).toBeHidden();
});

test("⌘N opens palette in new-workstream mode", async ({ page }) => {
  await page.keyboard.press("Meta+n");

  // Palette overlay visible
  await expect(page.locator(".palette-overlay")).toBeVisible();

  // Esc closes it — press twice: first may close input focus, second closes overlay
  await page.keyboard.press("Escape");
  // Wait briefly for React state update
  await page.waitForTimeout(200);
  if (await page.locator(".palette-overlay").isVisible()) {
    await page.keyboard.press("Escape");
  }
  await expect(page.locator(".palette-overlay")).toBeHidden();
});

test("⌘, opens settings dialog", async ({ page }) => {
  await page.keyboard.press("Meta+,");

  // Settings dialog visible
  await expect(page.getByRole("dialog", { name: "Settings" })).toBeVisible();

  // Esc closes it
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Settings" })).toBeHidden();
});

test("⌘F opens chat search", async ({ page }) => {
  await page.keyboard.press("Meta+f");

  // Search bar visible (has a search input)
  await expect(page.locator('[aria-label="Find in conversation"]')).toBeVisible();

  // Esc closes it
  await page.keyboard.press("Escape");
  await expect(page.locator('[aria-label="Find in conversation"]')).toBeHidden();
});

test("⌘J toggles panel", async ({ page }) => {
  // Panel starts closed (no localStorage in fresh context)
  await expect(page.locator(".context-panel")).toBeHidden();

  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();

  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeHidden();
});
