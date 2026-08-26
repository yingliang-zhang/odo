import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Auto-distill daily-cap chip (2026-08-26 storm fix): when the project's
// daily distill quota is spent, the daemon suspends scheduling and
// discloses the earliest quota release on pending_counts; the Memory tab
// shows "今日额度已用完 · 预计恢复 <time>" until the horizon passes.
// Pinned contracts:
//   - the chip renders the disclosed resume time (with the time text);
//   - the upgrade fallback (pre-suspension-row journal) is marked
//     computed — daemon-seeded fixtures exercise the same wire path;
//   - FIX 3: auto_distill=never hides the chip even with a live
//     suspension value (and a re-enable brings it back) — driven through
//     the real Settings save round-trip like pipeline-chip.spec's pref
//     flip;
//   - clearing the daemon value hides the chip again (no latch).
// pending_counts polls every 4th tick — all assertions use the poll
// window (memory-stranded.spec precedent).

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

const POLL = { timeout: 12_000 };

async function setCapResume(page: Page, value: { resume_at_unix: number; computed?: boolean } | null) {
  await page.evaluate((v) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.autoDistillCap.resume = v;
  }, value);
}

async function openMemoryTab(page: Page) {
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
  const memoryTab = page.getByRole("tab", { name: /Memory/ });
  await memoryTab.click();
  await expect(memoryTab).toHaveAttribute("aria-selected", "true");
}

// Flip the auto_distill pref end-to-end: mutate the fixture, then save
// through the real SettingsPanel so App.refreshSettings observes it (the
// mock persists update_settings; pipeline-chip.spec's setAutoApply shape).
async function setAutoDistillPref(page: Page, value: string) {
  await page.evaluate((v) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.defaultSettings.auto_distill = v;
  }, value);
  await page.keyboard.press("Meta+,");
  const panel = page.locator(".settings-panel");
  await expect(panel).toBeVisible();
  await panel.getByRole("button", { name: "Save" }).click();
  await expect(panel.locator(".settings-toast")).toBeVisible();
  await panel.getByRole("button", { name: "Close" }).click();
  await expect(panel).toHaveCount(0);
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("suspended project shows the recovery chip; clearing it hides the chip", async ({ page }) => {
  await setCapResume(page, { resume_at_unix: Math.floor(Date.now() / 1000) + 7200 });
  await openMemoryTab(page);
  const chip = page.locator(".mem-cap-notice");
  await expect(chip).toBeVisible(POLL);
  await expect(chip).toContainText("今日额度已用完 · 预计恢复");
  await expect(chip).not.toHaveAttribute("data-computed", "true");

  await setCapResume(page, null);
  await expect(chip).toBeHidden(POLL);
});

test("upgrade fallback rides the same wire, marked computed", async ({ page }) => {
  await setCapResume(page, { resume_at_unix: Math.floor(Date.now() / 1000) + 3600, computed: true });
  await openMemoryTab(page);
  const chip = page.locator(".mem-cap-notice");
  await expect(chip).toBeVisible(POLL);
  await expect(chip).toHaveAttribute("data-computed", "true");
});

test("FIX 3: auto_distill=never hides the chip; re-enabling restores it", async ({ page }) => {
  await setCapResume(page, { resume_at_unix: Math.floor(Date.now() / 1000) + 7200 });
  await openMemoryTab(page);
  const chip = page.locator(".mem-cap-notice");
  await expect(chip).toBeVisible(POLL);

  await setAutoDistillPref(page, "never");
  await expect(chip).toBeHidden(POLL);

  // No latch in either direction: restoring the pref re-derives the chip.
  await setAutoDistillPref(page, "on_idle");
  await expect(chip).toBeVisible(POLL);
});
