import { test, expect } from "@playwright/test";

// Context panel: ⌘J toggle, tab switching between Changes/Wiki/Memory/Ledger.
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

  // Click Ledger tab
  const ledgerTab = page.getByRole("tab", { name: /Ledger/ });
  await ledgerTab.click();
  await expect(ledgerTab).toHaveAttribute("aria-selected", "true");

  // Click Changes tab
  const changesTab = page.getByRole("tab", { name: /Changes/ });
  await changesTab.click();
  await expect(changesTab).toHaveAttribute("aria-selected", "true");
});

test("panel close button works", async ({ page }) => {
  // Open panel first
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();

  // Click close button
  await page.getByRole("button", { name: "Close panel" }).click();
  await expect(page.locator(".context-panel")).toBeHidden();
});
