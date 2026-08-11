import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Test hook installed by src/dev/mock-invoke.ts (plain-browser dev only).
// e2e sits outside the app tsconfig, so mirror the Window member here.
declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

// Background runs: a run in a workstream that is NOT in view must surface —
// purple sidebar dot + clickable StatusBar chip — because the chat surface
// shows nothing for it (panel sessions, fan-outs in other workstreams).
// Foreground runs keep the existing blue pulse; pending review stays amber.

// pending_counts refreshes on the poll loop's every-4th tick and the idle
// tick is 1.5s, so a fixture mutation takes up to ~6s to show.
const REFRESH = { timeout: 12_000 };

function setRunningWorkstreams(page: Page, ids: number[]) {
  return page.evaluate((runIds) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runningWorkstreams.length = 0;
    fx.runningWorkstreams.push(...runIds);
  }, ids);
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("background run: purple dot + chip; chip jumps to the workstream", async ({ page }) => {
  const sidebar = page.locator(".sidebar");
  const bgRow = sidebar.locator(".ws-row", { hasText: "feat-sidebar-tree" });

  // Quiet baseline: fixtures start with nothing running, so no chip.
  await expect(page.locator(".status-bg-runs")).toHaveCount(0);

  // Daemon reports a run on ws2 (e.g. /panel fanning out in another ws).
  await setRunningWorkstreams(page, [2]);

  // Sidebar: ws2's dot turns purple; the active project row also reads
  // background (the viewed ws1 is not running).
  await expect(bgRow.locator(".ws-dot")).toHaveClass(/dot-bg/, REFRESH);
  await expect(sidebar.locator(".proj-row-active .ws-dot")).toHaveClass(/dot-bg/);

  // StatusBar: one clickable chip, title names the workstream.
  const chip = page.locator(".status-bg-runs");
  await expect(chip).toContainText("1 background run");
  await expect(chip).toHaveAttribute("title", /feat-sidebar-tree/);

  // Click jumps: ws2 becomes the view → its dot flips to foreground blue
  // (still daemon-running), and the chip disappears.
  await chip.click();
  await expect(page.locator(".app-statusbar")).toContainText("feat-sidebar-tree");
  await expect(bgRow.locator(".ws-dot")).toHaveClass(/dot-accent/);
  await expect(page.locator(".status-bg-runs")).toHaveCount(0);
});

test("foreground beats background; running beats pending review", async ({ page }) => {
  const sidebar = page.locator(".sidebar");
  // "main" exists in BOTH fixture projects — scope to the active one (its
  // remote twin deliberately stays idle; ws ids collide across projects).
  const activeProj = sidebar.locator(".proj-group", { has: page.locator(".proj-row-active") });
  const fgDot = activeProj.locator(".ws-row", { hasText: "main" }).locator(".ws-dot");
  const bgDot = sidebar.locator(".ws-row", { hasText: "feat-sidebar-tree" }).locator(".ws-dot");

  // Daemon reports runs on ws1 (in view) and ws2 (not). ws1 also holds the
  // seeded pending diff — running must still win over amber.
  await setRunningWorkstreams(page, [1, 2]);

  await expect(fgDot).toHaveClass(/dot-accent/, REFRESH);
  await expect(bgDot).toHaveClass(/dot-bg/);
  // The chip counts background runs only.
  await expect(page.locator(".status-bg-runs")).toContainText("1 background run");
});
