import { test, expect } from "@playwright/test";
import { KEYBINDS, comboFor } from "../src/keybinds";

// Keyboard shortcuts: ⌘B (sidebar), ⌘J (panel), ⌘K (palette), ⌘N (new workstream),
// ⌘, (settings), ⌘F (search), Esc (close overlays).

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
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

  // Esc closes it — palette overlay Esc handler now explicitly closes (App.tsx)
  await page.keyboard.press("Escape");
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

// P1.3 (docs/design/adoption-lock.md): the keybind registry is the one
// table behind the ⌘/ panel and the palette's hint strings — the panel
// literally renders KEYBINDS import-for-import.

test("⌘/ opens the shortcuts panel listing every registry row; Esc closes it", async ({ page }) => {
  await page.keyboard.press("Meta+/");
  const panel = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await expect(panel).toBeVisible();

  // One row per registry entry, each carrying its live display combo.
  await expect(panel.locator(".shortcut-row")).toHaveCount(KEYBINDS.length);
  await expect(panel).toContainText("Toggle sidebar");
  await expect(panel).toContainText("⌘B");
  await expect(panel).toContainText("Cancel run");
  await expect(panel).toContainText("Esc");

  await page.keyboard.press("Escape");
  await expect(panel).toBeHidden();
});

test("palette hints render live comboFor(id) strings from the registry", async ({ page }) => {
  await page.keyboard.press("Meta+k");
  const sidebarAction = page.locator(".palette-item", { hasText: "Toggle Sidebar" });
  await expect(sidebarAction).toContainText(comboFor("toggle-sidebar")!);
  const settingsAction = page.locator(".palette-item", { hasText: "Open Settings" });
  await expect(settingsAction).toContainText(comboFor("open-settings")!);
  await page.keyboard.press("Escape");
  await expect(page.locator(".palette-overlay")).toBeHidden();
});
