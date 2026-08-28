import { test, expect } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Test hook installed by src/dev/mock-invoke.ts (plain-browser dev only).
// e2e sits outside the app tsconfig, so mirror the Window member here.
declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

// U1.1 (ui-layout-lock) — StatusBar hide-by-priority overflow, real pixels.
// The fold's priorities, +N accounting and row behavior are covered by
// vitest; this spec pins the e2e invariants the design lock calls out:
// hidden chips stay MOUNTED (`.status-badge` hooks survive a fold), the +N
// chip replaces them with live values, and widening the window un-folds.
//
// Scene (conv 3, fix-daemon-binary): meter + panel + OMP chips are always
// on; wiki(2) + memory chips come from the fixtures; one seeded background
// run brings the footer past overflow at a 430px viewport. Fold math at
// 430px (mock-measured approximate): zone ≈ 484px > available ≈ 222px —
// ctx/omp/panel + the bg-runs chip (ranks 0/1/2 + 4) fold into +4 with
// double-digit margins; the two count chips (rank 5, LAST) survive.
const POLL = { timeout: 12_000 }; // bg runs arrive on the pending_counts cadence
const FOLD = { timeout: 5_000 }; // RO + 50ms debounce + a paint

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await page.locator(".sidebar .ws-row", { hasText: "fix-daemon-binary" }).click();
  // Wide baseline: every chip visible, nothing folded.
  await expect(page.locator(".ctx-meter")).toBeVisible();
  await expect(page.locator(".panel-chip")).toBeVisible();
  await expect(page.locator(".omp-usage-chip")).toBeVisible();
  await expect(page.locator('[data-chip="wiki"]')).toBeVisible();
  await expect(page.locator('[data-chip="memory"]')).toBeVisible();
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runningWorkstreams.push(2);
  });
  await expect(page.locator('[data-chip="bgruns"]')).toContainText("1 background run", POLL);
});

test("narrow window folds telemetry into +N — chips stay mounted, rows are live", async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 720 });

  // Hide order: read-only telemetry first (ctx 0, omp 1, panel 2), then the
  // bg-runs chip (rank 4) — the actionable count chips (rank 5) survive.
  await expect(page.locator(".ctx-meter")).toHaveClass(/chip-hidden/, FOLD);
  await expect(page.locator(".omp-usage-chip")).toHaveClass(/chip-hidden/);
  await expect(page.locator(".panel-chip")).toHaveClass(/chip-hidden/);
  await expect(page.locator('[data-chip="bgruns"]')).toHaveClass(/chip-hidden/);
  await expect(page.locator('[data-chip="wiki"]')).not.toHaveClass(/chip-hidden/);
  await expect(page.locator('[data-chip="memory"]')).not.toHaveClass(/chip-hidden/);

  // The e2e invariant the lock calls out: folded, never unmounted.
  await expect(page.locator(".ctx-meter")).toBeAttached();
  await expect(page.locator(".ctx-meter")).not.toBeVisible();
  await expect(page.locator('[data-chip="bgruns"]')).toBeAttached();

  // The +N chip collapses the fold and lists the hidden chips live.
  const more = page.locator(".status-overflow-chip");
  await expect(more).toHaveText("+4");
  await more.click();
  const pop = page.locator(".status-overflow-popover");
  await expect(pop).toBeVisible();
  await expect(pop).toContainText("~86% of context");
  await expect(pop).toContainText("Panel ×3");
  await expect(pop).toContainText("OMP · 1p");
  await expect(pop.locator(".bg-run-row", { hasText: "feat-sidebar-tree" })).toContainText("still running");
});

test("widening un-folds every chip — nothing sticks hidden", async ({ page }) => {
  await page.setViewportSize({ width: 430, height: 720 });
  const more = page.locator(".status-overflow-chip");
  await expect(more).toHaveText("+4", FOLD);

  await page.setViewportSize({ width: 1280, height: 720 });
  await expect(page.locator(".ctx-meter")).toBeVisible(FOLD);
  await expect(page.locator(".panel-chip")).toBeVisible();
  await expect(page.locator(".omp-usage-chip")).toBeVisible();
  await expect(page.locator('[data-chip="bgruns"]')).toBeVisible();
  await expect(page.locator(".chip-hidden")).toHaveCount(0);
  await expect(more).toHaveCount(0);
});
