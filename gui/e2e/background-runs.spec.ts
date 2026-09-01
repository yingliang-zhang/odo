import { test, expect } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Test hook installed by src/dev/mock-invoke.ts (plain-browser dev only).
// e2e sits outside the app tsconfig, so mirror the Window member here.
declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

// GUI Wave A: background-work visibility (audit §3 #1/#2).
// StatusBar: multi-target dropdown + start/completion flashes — both
// driven by transitions of the daemon's running_workstreams set. Sidebar:
// attention ordering (Needs-input → Working → Idle) + per-row activity
// line. Daemon state is simulated by mutating the fixture module; the app
// picks changes up on the pending_counts refresh cadence (every 4th poll
// tick, ~6 s idle).
const BG_REFRESH = { timeout: 12_000 };
const FG_REFRESH = { timeout: 7_000 };

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("bg chip opens dropdown listing still-running workstreams; row click jumps", async ({ page }) => {
  const chip = page.locator(".status-bg-runs");
  await expect(chip).toHaveCount(0); // no runs → no chrome

  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runningWorkstreams.push(2);
  });
  await expect(chip).toBeVisible(BG_REFRESH);
  await expect(chip).toContainText("1 background run");

  await chip.click();
  const menu = page.locator(".runs-menu");
  const row = menu.locator(".bg-run-row", { hasText: "feat-sidebar-tree" });
  await expect(row).toHaveCount(1);
  await expect(row).toContainText("still running");

  await row.click();
  await expect(menu).toHaveCount(0); // row click closes the menu
  await expect(page.locator(".app-statusbar")).toContainText("feat-sidebar-tree");
});

test("multiple bg runs all listed in the dropdown; click-away closes without jumping", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runningWorkstreams.push(2, 3);
  });
  const chip = page.locator(".status-bg-runs");
  await expect(chip).toContainText("2 background runs", BG_REFRESH);

  await chip.click();
  const rows = page.locator(".runs-menu .bg-run-row");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText("feat-sidebar-tree");
  await expect(rows.nth(1)).toContainText("fix-daemon-binary");

  await page.locator(".app-main").click({ position: { x: 5, y: 5 } });
  await expect(page.locator(".runs-menu")).toHaveCount(0);
  await expect(page.locator(".app-statusbar")).toContainText("main");
});

test("completion flash chips the finished run even as the list drains to zero", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runningWorkstreams.push(2, 3);
  });
  const chip = page.locator(".status-bg-runs");
  await expect(chip).toContainText("2 background runs", BG_REFRESH);

  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runningWorkstreams.length = 0;
  });
  const flash = page.locator(".bg-flash-done");
  await expect(flash).toBeVisible(BG_REFRESH);
  await expect(flash).toContainText("feat-sidebar-tree, fix-daemon-binary finished");
  // The completion chip is the only surface of a drained list — the runs
  // chip must not linger with nothing to jump to.
  await expect(chip).toHaveCount(0);
});

test("start flash tints the chip when a run appears mid-session", async ({ page }) => {
  await expect(page.locator(".status-bg-runs")).toHaveCount(0);
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runningWorkstreams.push(3);
  });
  await expect(page.locator(".status-bg-runs.bg-flash-new")).toBeVisible(BG_REFRESH);
});

test("UX-3a: finished flash tints ✗ when the drained run's cached terminal is agent_error", async ({ page }) => {
  // Warm the switch cache: view the workstream whose run will fail, and
  // let the poll deliver its agent_error mid-session (cache warming is
  // the warm-on-events-update effect — exactly what the tint reads).
  await page.locator(".sidebar .ws-item", { hasText: "fix-daemon-binary" }).click();
  await expect(page.locator(".app-statusbar")).toContainText("fix-daemon-binary");

  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    const e = fx.ev("agent_error", { error: "kaboom: wrapper exited 1" }, 3);
    e.created_at = new Date().toISOString();
    fx.events.push(e);
  });
  // The error renders in-chat — proof the journal tail (and client-side
  // cache) carries the terminal before we leave this view.
  await expect(page.locator(".bubble-error", { hasText: "kaboom" })).toBeVisible(FG_REFRESH);

  // Back to main; fix-daemon-binary becomes a background run, then drains.
  await page.locator(".sidebar .ws-item", { hasText: "main" }).first().click();
  await expect(page.locator(".app-statusbar")).toContainText("main");
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runningWorkstreams.push(3);
  });
  const chip = page.locator(".status-bg-runs");
  await expect(chip).toContainText("1 background run", BG_REFRESH);

  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runningWorkstreams.length = 0;
  });
  const errFlash = page.locator(".bg-flash-error");
  await expect(errFlash).toBeVisible(BG_REFRESH);
  await expect(errFlash).toContainText("fix-daemon-binary finished");
  // The ok-tint twin must NOT render for a failed run.
  await expect(page.locator(".bg-flash-done")).toHaveCount(0);
});

test("sidebar orders needs-input → working → idle, stable ties", async ({ page }) => {
  const items = page.locator(".proj-group").first().locator(".ws-list .ws-item");
  await expect(items).toHaveCount(3);

  // Fixture baseline: ws1 pending, ws2 pending, ws3 idle → created order.
  const base = (await items.allTextContents()).map((t) => t.trim());
  expect(base[0]).toContain("main");
  expect(base[1]).toContain("feat-sidebar-tree");
  expect(base[2]).toContain("fix-daemon-binary");

  // ws3 running (rank 1) + only ws2 keeps pending (rank 0) → ws3 outranks
  // the now-idle ws1 but still follows the needs-input row.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.pendingCounts["1"] = 0;
    fx.runningWorkstreams.push(3);
  });
  await expect
    .poll(async () => (await items.allTextContents()).map((t) => t.trim()), BG_REFRESH)
    .toEqual([
      expect.stringContaining("feat-sidebar-tree"),
      expect.stringContaining("fix-daemon-binary"),
      expect.stringContaining("main"),
    ]);

  // Per-row "still running" line on the bg run only.
  const ws3 = page.locator(".ws-item", { hasText: "fix-daemon-binary" });
  await expect(ws3.locator(".ws-activity-line")).toHaveText("still running");
  await expect(page.locator(".ws-item", { hasText: "main" }).locator(".ws-activity-line")).toHaveCount(0);
});

test("foreground running row shows latest tool; line clears when run ends", async ({ page }) => {
  const mainRow = page.locator(".ws-item", { hasText: "main" });
  await expect(mainRow.locator(".ws-activity-line")).toHaveCount(0);

  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runState.foreground = true;
  });
  // Conv 1's last journaled agent_tool_call is read_file (fixtures).
  await expect(mainRow.locator(".ws-activity-line")).toHaveText("Running: read_file", FG_REFRESH);

  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runState.foreground = false;
  });
  await expect(mainRow.locator(".ws-activity-line")).toHaveCount(0, FG_REFRESH);
});
