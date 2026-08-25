import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";
import { TRANSCRIPT_WINDOW_GROUPS } from "../src/components/ChatSurface";

// GUI perf Phase 1: transcript windowing e2e, seeded through the mandated
// fixtures seam (fx.ev + fx.events.push — the mock's poll tail delivers
// appended rows exactly like daemon-journaled ones; same idiom as
// pipeline-chip.spec / loop.spec). Asserts the render-only cut: a bounded
// mount count, one stepwise expansion chip keyed per conversation, and ⌘F
// still matching a term in a HIDDEN group — matches are computed over the
// full visible events array, never the render window (jump-to-match
// force-reveals the matched group). Every expectation is DERIVED from the
// boot render (baseGroups), so fixture edits can never silently invalidate
// the math; the window constant is imported from ChatSurface — the single
// source of truth.

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

const N = TRANSCRIPT_WINDOW_GROUPS; // 25
const EXTRA = 4; // seed N + EXTRA groups atop the boot groups
const SEEDS = N + EXTRA; // 29
const NEEDLE = "needle-anchor-93f7b2";
const POLL = { timeout: 10_000 };

// One user_message per push: each opens its own run group by construction.
// The FIRST seeded group carries the needle — it sits exactly EXTRA groups
// above the default window's cut for ANY boot fixture, so zero clicks hide
// it and one click reveals it (no baseGroups precondition required).
async function seedRuns(page: Page, count: number) {
  await page.evaluate(
    ({ n, needle }) => {
      const fx = window.__odoFixtures;
      if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
      for (let i = 1; i <= n; i++) {
        const e = fx.ev("user_message", { text: i === 1 ? `mark-1 ${needle}` : `mark-${i}` }, 1);
        e.created_at = new Date().toISOString();
        fx.events.push(e);
      }
    },
    { n: count, needle: NEEDLE },
  );
  // The seed is durable once a poll cycles the rows through ChatSurface.
  await expect(page.locator(".bubble-user").last()).toContainText(`mark-${count}`, POLL);
}

test("transcript windowing: tail window, stepwise expand, full-array search", async ({ page }) => {
  test.setTimeout(60_000);
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();

  const runGroups = page.locator(".message-list .run-group");
  const chip = page.locator(".transcript-window-chip");
  const chipBtn = page.locator(".transcript-window-chip-btn");
  const needleBubble = page.locator(".bubble-user", { hasText: `mark-1 ${NEEDLE}` });
  const lastUserBubble = page.locator(".bubble-user").last();
  const baseGroups = await runGroups.count();
  const total = baseGroups + SEEDS; // groups after seeding
  const hiddenText = (n: number) => `${n} earlier run group${n === 1 ? "" : "s"} hidden`;

  await seedRuns(page, SEEDS);

  // Default: only the LAST N run groups mount — the newest group is never
  // windowed out; hidden groups stay out of the DOM entirely.
  // hidden = total − N = baseGroups + EXTRA > 0 for any fixture.
  await expect(runGroups, "tail window mounts only N groups").toHaveCount(N, POLL);
  await expect(chip).toContainText(hiddenText(total - N));
  await expect(lastUserBubble).toContainText(`mark-${SEEDS}`);
  await expect(needleBubble).toHaveCount(0);

  // ⌘F matches the FULL visible events, not the window: the needle lives
  // in a hidden group, still counts 1/1, and jump-to-match force-reveals
  // its group — plus everything below it, so the live tail stays mounted
  // mid-search.
  await page.keyboard.press("Meta+f");
  await page.locator('[aria-label="Find in conversation"]').fill(NEEDLE);
  await expect(page.locator(".search-count")).toHaveText("1/1");
  await expect(needleBubble).toBeAttached();
  await expect(lastUserBubble).toContainText(`mark-${SEEDS}`);
  await page.keyboard.press("Escape");
  await expect(page.locator('[aria-label="Find in conversation"]')).toBeHidden();
  // Force-reveal is derived, not sticky: closing the search snaps back to
  // the user's chosen expansion.
  await expect(runGroups).toHaveCount(N);

  // Stepwise expansion: after k clicks the mount count is
  // min(total, N·(1+k)) — one click reveals exactly the PREVIOUS N groups,
  // never everything at once; the chip's hidden count drops in lockstep
  // until it disappears.
  let clicks = 0;
  for (;;) {
    const expected = Math.min(total, N * (1 + clicks + 1));
    await chipBtn.click();
    clicks += 1;
    await expect(runGroups).toHaveCount(expected);
    const hidden = total - expected;
    if (clicks === 1) {
      // The needle enters on the FIRST expansion step by construction —
      // it sits EXTRA ≤ N groups above the cut for any fixture size.
      await expect(needleBubble).toBeAttached();
    }
    if (hidden === 0) break;
    await expect(chip).toContainText(hiddenText(hidden));
  }
  await expect(chip).toHaveCount(0);
  await expect(needleBubble).toBeAttached();

  // A workstream switch must never leak another conversation's expansion:
  // feat-sidebar-tree renders alone, with no window chip. Switching back
  // remembers the expansion (the state key carries the conversation id).
  await page.locator(".sidebar .ws-item", { hasText: "feat-sidebar-tree" }).click();
  await expect(page.locator(".app-statusbar")).toContainText("feat-sidebar-tree");
  await expect(chip).toHaveCount(0);
  await expect(needleBubble).toHaveCount(0);

  // Two projects carry a "main" workstream — scope to the odo proj-group.
  await page
    .locator(".sidebar .proj-group", { has: page.locator(".proj-row", { hasText: "odo" }) })
    .locator(".ws-item", { hasText: "main" })
    .click();
  await expect(page.locator(".app-statusbar")).toContainText("main");
  await expect(lastUserBubble).toContainText(`mark-${SEEDS}`, POLL);
  await expect(runGroups).toHaveCount(total);
  await expect(chip).toHaveCount(0);
});
