import { test, expect } from "@playwright/test";

// Context panel: ⌘J toggle, tab switching between Changes/Wiki/Memory/Runs.
// Panel is NOT open by default (localStorage empty in fresh browser context).

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("⌘J toggles context panel", async ({ page }) => {
  // Panel starts closed (no localStorage)
  await expect(page.locator(".context-panel")).toBeHidden();

  // Open with ⌘J
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();

  // Toggle closed
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeHidden();
});

test("switch panel tabs", async ({ page }) => {
  // Open panel first
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();

  // Click Wiki tab
  const wikiTab = page.getByRole("tab", { name: /Wiki/ });
  await wikiTab.click();
  await expect(wikiTab).toHaveAttribute("aria-selected", "true");

  // Click Memory tab
  const memoryTab = page.getByRole("tab", { name: /Memory/ });
  await memoryTab.click();
  await expect(memoryTab).toHaveAttribute("aria-selected", "true");

  // Click Runs tab
  const runsTab = page.getByRole("tab", { name: /Runs/ });
  await runsTab.click();
  await expect(runsTab).toHaveAttribute("aria-selected", "true");

  // Click Changes tab
  const changesTab = page.getByRole("tab", { name: /Changes/ });
  await changesTab.click();
  await expect(changesTab).toHaveAttribute("aria-selected", "true");
});

test("topbar toggle opens and closes the panel", async ({ page }) => {
  // The panel's in-header close X was replaced by a TopBar toggle button
  // mirroring the left sidebar's.
  const toggle = page.getByRole("button", { name: "Toggle panel" });
  await expect(page.locator(".context-panel")).toBeHidden();

  await toggle.click();
  await expect(page.locator(".context-panel")).toBeVisible();

  await toggle.click();
  await expect(page.locator(".context-panel")).toBeHidden();
});
