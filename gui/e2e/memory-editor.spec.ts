import { test, expect } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Odo DX wave (Feature 3): the memory.md / pins.md direct editor — edit
// affordance on the two PROJECT layers only, draft → write_memory save →
// files re-read lands the new body → App toast confirms; the mock's
// memoryWrites ledger is the payload assertion. user.md and the archive
// stay read-only by design.

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

const POLL = { timeout: 4000 };

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await page.keyboard.press("Meta+j");
  await page.locator('.context-panel [role="tab"]', { hasText: "Memory" }).click();
  await page.getByRole("tab", { name: /current files/i }).click();
  // The fixture memory body proves loadFiles landed before we act.
  await expect(page.getByText("GUI dev efficiency: mock-invoke adapter")).toBeVisible(POLL);
});

test("memory.md: draft → save → refreshed section + saved toast, with the payload journaled in the mock", async ({ page }) => {
  // Edit is offered on the two project layers and NOWHERE else.
  await expect(page.getByRole("button", { name: "Edit memory.md" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Edit pins.md" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Edit user.md" })).toHaveCount(0);

  await page.getByRole("button", { name: "Edit memory.md" }).click();
  const area = page.getByLabel("Edit memory.md content");
  await expect(area).toBeVisible();
  await expect(area).toHaveValue(/GUI dev efficiency/);

  await area.fill("- hand edited rule\n");
  await page.getByRole("button", { name: "Save memory.md" }).click();

  // The App toast confirms; the editor closed; the re-read section shows
  // the new body (the mock's override serves it post-write).
  await expect(page.getByText("saved .odo/memory.md")).toBeVisible(POLL);
  await expect(page.getByLabel("Edit memory.md content")).toHaveCount(0);
  await expect(page.getByText("- hand edited rule")).toBeVisible(POLL);

  const writes = await page.evaluate(() => window.__odoFixtures?.memoryWrites ?? []);
  expect(writes).toEqual([{ file: "memory.md", content: "- hand edited rule\n" }]);
});

test("pins.md: save rides the same write path; Cancel abandons without a write", async ({ page }) => {
  await page.getByRole("button", { name: "Edit pins.md" }).click();
  const area = page.getByLabel("Edit pins.md content");
  await expect(area).toHaveValue(/Always run tests/);
  await area.fill("- never ship on friday\n");
  await page.getByRole("button", { name: "Save pins.md" }).click();
  await expect(page.getByText("saved .odo/pins.md")).toBeVisible(POLL);
  await expect(page.getByText("- never ship on friday")).toBeVisible(POLL);

  // Cancel: a reopened edit closed without saving records nothing new.
  await page.getByRole("button", { name: "Edit memory.md" }).click();
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByLabel("Edit memory.md content")).toHaveCount(0);
  const writes = await page.evaluate(() => window.__odoFixtures?.memoryWrites ?? []);
  expect(writes).toEqual([{ file: "pins.md", content: "- never ship on friday\n" }]);
});
