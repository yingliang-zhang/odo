import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Test hook installed by src/dev/mock-invoke.ts (plain-browser dev only).
// e2e sits outside the app tsconfig, so mirror the Window member here.
declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

// W6 (goal queue): the QueueDock derives its rows from the journaled
// events; resume/drop reconcile when the daemon's consumption row flows
// back through the poll loop, and the sidebar pill follows pending_counts
// on the every-4th-tick refresh (idle tick 1.5s — up to ~6s).
const REFRESH = { timeout: 12_000 };

// Arm the park toggle, submit one goal, wait for the composer to clear.
async function parkGoal(page: Page, text: string) {
  const textarea = page.locator(".chat-input textarea");
  await page.locator(".park-toggle").click();
  await textarea.fill(text);
  await page.locator('.chat-input button[type="submit"]').click();
  await expect(textarea).toHaveValue("");
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  // Seeded fixture: one parked goal waits in the default conversation.
  await expect(page.locator(".queue-chip")).toContainText("Queue · 1");
});

test("park toggle parks a goal and clears the composer", async ({ page }) => {
  const parkToggle = page.locator(".park-toggle");
  const submit = page.locator('.chat-input button[type="submit"]');
  const textarea = page.locator(".chat-input textarea");

  await expect(parkToggle).toHaveAttribute("aria-pressed", "false");
  await expect(submit).toHaveText("Send");

  await parkToggle.click();
  await expect(parkToggle).toHaveAttribute("aria-pressed", "true");
  await expect(submit).toHaveText("Park");

  // Slash commands route before the daemon's park branch — the toggle
  // refuses while the draft is a command, but stays armed for after.
  await textarea.fill("/panel");
  await expect(parkToggle).toBeDisabled();

  await textarea.fill("Queue the ledger backfill");
  await expect(parkToggle).toBeEnabled();
  await submit.click();

  // Composer cleared; the toggle is one-shot (disarmed after the park).
  await expect(textarea).toHaveValue("");
  await expect(parkToggle).toHaveAttribute("aria-pressed", "false");
  await expect(submit).toHaveText("Send");

  // The dock gained the row, and the goal sits in the transcript.
  await expect(page.locator(".queue-chip")).toContainText("Queue · 2");
  await expect(page.locator(".bubble-user", { hasText: "Queue the ledger backfill" })).toBeVisible();
});

test("queue dock lists goals FIFO with next tag", async ({ page }) => {
  await parkGoal(page, "Second queued goal");
  await parkGoal(page, "Third queued goal");

  await page.locator(".queue-chip").click();
  const popover = page.locator(".queue-popover");
  await expect(popover).toBeVisible();
  await expect(popover.locator(".queue-note")).toContainText("queued goals start when the current run finishes");

  const rows = popover.locator(".queue-row");
  await expect(rows).toHaveCount(3);

  // FIFO position + the seeded goal leads the queue.
  await expect(rows.nth(0).locator(".queue-pos")).toContainText("#1");
  await expect(rows.nth(1).locator(".queue-pos")).toContainText("#2");
  await expect(rows.nth(2).locator(".queue-pos")).toContainText("#3");
  await expect(rows.nth(0).locator(".queue-row-text")).toHaveText("Parked: sweep the flaky sidebar selector");
  await expect(rows.nth(1).locator(".queue-row-text")).toHaveText("Second queued goal");
  await expect(rows.nth(2).locator(".queue-row-text")).toHaveText("Third queued goal");

  // Only the head row is tagged `next`.
  await expect(rows.nth(0).locator(".queue-next-tag")).toHaveText("next");
  await expect(rows.nth(1).locator(".queue-next-tag")).toHaveCount(0);
  await expect(rows.nth(2).locator(".queue-next-tag")).toHaveCount(0);
});

test("park while running overrides steer", async ({ page }) => {
  // Daemon reports a live run on this conversation; the poll picks it up.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runState.foreground = true;
  });
  await expect(page.locator(".stop-btn")).toBeVisible(REFRESH);
  const submit = page.locator('.chat-input button[type="submit"]');
  await expect(submit).toHaveText("Steer");

  // Arming park flips the submit to Park — steer is structurally forced
  // off (the daemon refuses a steer+park combination).
  await page.locator(".park-toggle").click();
  await expect(submit).toHaveText("Park");

  await page.locator(".chat-input textarea").fill("Park while it runs");
  await submit.click();

  // Proof the park branch won: a steer would journal user_message WITHOUT
  // park:true (dock unchanged); the dock grew instead.
  await expect(page.locator(".queue-chip")).toContainText("Queue · 2");
  await expect(page.locator(".bubble-user", { hasText: "Park while it runs" })).toBeVisible();
});

test("drop removes row and journals", async ({ page }) => {
  await parkGoal(page, "Drop me");

  await page.locator(".queue-chip").click();
  const rows = page.locator(".queue-row");
  await expect(rows).toHaveCount(2);

  // Two-step inline confirm (no window.confirm): first click arms.
  await rows.nth(1).getByRole("button", { name: "Drop parked goal 2" }).click();
  await rows.nth(1).getByRole("button", { name: "Confirm drop parked goal 2" }).click();

  // The daemon's consumption row flows back through the poll loop and the
  // derived queue drops the row — the seeded head survives.
  await expect(page.locator(".queue-row")).toHaveCount(1, REFRESH);
  await expect(page.locator(".queue-row").first().locator(".queue-row-text")).toHaveText(
    "Parked: sweep the flaky sidebar selector",
  );
  await expect(page.locator(".queue-chip")).toContainText("Queue · 1");

  // … and the drop is journaled as a transcript receipt badge.
  await expect(page.locator(".badge", { hasText: "dropped parked goal" })).toBeVisible(REFRESH);
});

test("resume clears head and shows receipt", async ({ page }) => {
  await page.locator(".queue-chip").click();
  await expect(page.locator(".queue-row")).toHaveCount(1);
  await expect(page.locator(".queue-next-tag")).toHaveText("next");

  // Conversation free → Resume is enabled; one click dequeues the head.
  await page.getByRole("button", { name: "Resume parked goal 1" }).click();

  // The run_prompt consumption row arrives via poll: the queue is empty,
  // the dock hides, and the human resume leaves a receipt badge (a
  // daemon auto-dequeue — actor set — would render nothing).
  await expect(page.locator(".queue-chip")).toHaveCount(0, REFRESH);
  await expect(page.locator(".badge", { hasText: "resumed parked goal" })).toBeVisible(REFRESH);
});

test("sidebar parked pill reflects depth", async ({ page }) => {
  // Active project, "main" row only: the daemon's parked_goals count is
  // the authoritative depth. Remote-project rows never show a pill.
  const activeProj = page.locator(".sidebar .proj-group", { has: page.locator(".proj-row-active") });
  const pill = activeProj.locator(".ws-row", { hasText: "main" }).locator(".ws-parked-pill");

  await expect(pill).toHaveText("1", REFRESH);
  await expect(pill).toHaveAttribute("title", "1 parked goal");
  await expect(page.locator(".ws-parked-pill")).toHaveCount(1);

  // Depth follows the park without a reload.
  await parkGoal(page, "One more queued goal");
  await expect(pill).toHaveText("2", REFRESH);
  await expect(pill).toHaveAttribute("title", "2 parked goals");
});

test("full queue error keeps draft", async ({ page }) => {
  // Fill the queue to the daemon cap (8): the seeded goal + 7 journaled
  // fixture-side (mock parity with handleParkGoal's pre-journal count).
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    for (let i = 0; i < 7; i++) {
      fx.events.push(fx.ev("user_message", { text: `filler goal ${i}`, park: true }, 1));
    }
  });

  const parkToggle = page.locator(".park-toggle");
  const textarea = page.locator(".chat-input textarea");
  await parkToggle.click();
  await textarea.fill("The ninth goal does not fit");
  await page.locator('.chat-input button[type="submit"]').click();

  // Over-cap parks fail loud: the daemon error surfaces and the composer
  // keeps the draft (and the armed toggle) so no message is ever lost.
  await expect(page.locator(".error-banner")).toContainText("queue full");
  await expect(textarea).toHaveValue("The ninth goal does not fit");
  await expect(parkToggle).toHaveAttribute("aria-pressed", "true");

  // The rejected send journaled nothing; the dock converges to the
  // journaled 8, never 9.
  await expect(page.locator(".queue-chip")).toContainText("Queue · 8", REFRESH);
});
