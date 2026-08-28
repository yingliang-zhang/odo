import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";

// U2.1 (docs/design/ui-layout-lock.md §U2.1) — the panel's docked↔overlay
// posture follows the MEASURED chat width (ResizeObserver on .app-main,
// 560/600px hysteresis), not a window-width breakpoint.
//
// Geometry math (sidebar 240px docked, default panel width 420px):
//   docked chat = window − 240 − 420 · overlay chat = window − 240
//   overlay enters at docked chat < 560  → window < 1220
//   overlay exits  at docked chat > 600  → window > 1260
// In overlay the panel leaves the grid; the exit fold still evaluates the
// DOCKED-equivalent width (overlay chat − panel), so the transition
// itself cannot flip the posture back (anti-oscillation).

async function openPanel(page: Page) {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
}

const panelPosition = (page: Page): Promise<string> =>
  page.evaluate(() => getComputedStyle(document.querySelector(".context-panel")!).position);

// Posture changes ride a real ResizeObserver through React state — poll.
async function expectOverlay(page: Page, on: boolean) {
  await expect.poll(() => panelPosition(page)).toBe(on ? "fixed" : "relative");
  await expect(page.locator(".panel-scrim")).toHaveCount(on ? 1 : 0);
}

test.beforeEach(async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 720 });
});

test("900px viewport: panel overlays the chat with a scrim; ⌘J closes", async ({ page }) => {
  await openPanel(page);

  // docked chat would be 900−240−420 = 240px (< 560) → overlay ON.
  await expectOverlay(page, true);

  // The scrim is click-THROUGH by spec: it dims but never eats the click —
  // the composer under it stays reachable (= nothing modal to dismiss).
  const scrim = page.locator(".panel-scrim");
  // Chromium serializes black/20 in oklab — pin the utility class and the
  // computed interaction mode, not the serialized color.
  await expect(scrim).toHaveClass(/bg-black\/20/);
  await expect(scrim).toHaveCSS("pointer-events", "none");

  // Existing ⌘J closes the overlay panel; reopening restores the posture.
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeHidden();
  await expect(page.locator(".panel-scrim")).toHaveCount(0);
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
  await expectOverlay(page, true);
});

test("widening past the hysteresis band re-docks; the band holds each side", async ({ page }) => {
  await openPanel(page);
  await expectOverlay(page, true);

  // 1150: docked chat 490 < 560 → overlay holds (band pass-through upward).
  await page.setViewportSize({ width: 1150, height: 720 });
  await expectOverlay(page, true);

  // 1400: docked chat 740 > 600 → docks (scrim gone).
  await page.setViewportSize({ width: 1400, height: 720 });
  await expectOverlay(page, false);

  // 1210: docked chat 550 → inside [560? no — 550 < 560)… re-enters overlay.
  // The dead band is 560..600 on the DOCKED width: 550 is below it.
  await page.setViewportSize({ width: 1210, height: 720 });
  await expectOverlay(page, true);
});

test("dead band: widths inside 560–600 keep the current posture", async ({ page }) => {
  // Enter overlay first at a clearly narrow width.
  await page.setViewportSize({ width: 900, height: 720 });
  await openPanel(page);
  await expectOverlay(page, true);

  // Grow to a window whose docked chat sits INSIDE the band: 1230 → 570.
  // Overlay must hold (no oscillation back to docked).
  await page.setViewportSize({ width: 1230, height: 720 });
  await expectOverlay(page, true);

  // Cross the exit: 1280 → docked chat 620 > 600.
  await page.setViewportSize({ width: 1280, height: 720 });
  await expectOverlay(page, false);

  // Shrink back into the band from above: 1230 → 570 — docked holds.
  await page.setViewportSize({ width: 1230, height: 720 });
  await expectOverlay(page, false);
});
