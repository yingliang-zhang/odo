import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Steer queue (Hermes-style busy-composer queue): the panel reads the
// journaled events only, so its whole lifecycle — steer, drop, drain —
// reconciles through the poll loop like the QueueDock's does. Seqs are
// read back from the fixture journal rather than assumed (injection and
// optimistic rows share the same seq counter).

// Test hook installed by src/dev/mock-invoke.ts (plain-browser dev only).
// e2e sits outside the app tsconfig, so mirror the Window member here.
declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

// Poll-driven reconciliations (drops, drains, running flips) wait on the
// poll loop, not the click — same budget the parked-goals spec allows.
const REFRESH = { timeout: 12_000 };

interface InjectedRow {
  type: string;
  payload: Record<string, unknown>;
}

async function journal(page: Page, rows: InjectedRow[]) {
  await page.evaluate((r) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    for (const row of r) {
      const e = fx.ev(row.type, row.payload, 1);
      e.created_at = new Date().toISOString();
      fx.events.push(e);
    }
  }, rows);
}

async function setRunning(page: Page, on: boolean) {
  await page.evaluate((v) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.runState.foreground = v;
  }, on);
}

// Seqs of the steers the fixture journal still counts as queued (open
// user_message{steer:true} rows minus every journaled closure) — the same
// ledger the derivation runs, reconstructed fixture-side.
async function queuedSteerSeqs(page: Page): Promise<number[]> {
  return page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    const closed = new Set<number>();
    for (const e of fx.events) {
      if (e.type !== "review_action") continue;
      if (e.payload?.action === "steer_dropped") {
        if (e.payload.steer_seq != null) closed.add(e.payload.steer_seq);
        for (const s of e.payload.steer_seqs ?? []) closed.add(s);
      } else if (e.payload?.action === "run_prompt") {
        for (const s of e.payload.steer_seqs ?? []) closed.add(s);
      }
    }
    return fx.events
      .filter((e) => e.conversation_id === 1 && e.type === "user_message" && e.payload?.steer && !closed.has(e.seq))
      .map((e) => e.seq);
  });
}

const STEER_PH = "Steer the running agent… (Esc stops)";
const RUN_PROMPT = "Implement the steer-queue panel";

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  // The original run + its prompt, so the active card has something to
  // pin; the poll then flips the composer into steer mode.
  await setRunning(page, true);
  await journal(page, [
    { type: "user_message", payload: { text: RUN_PROMPT } },
    { type: "agent_tool_call", payload: { tool: "read_file", args: { path: "src/steer.ts" } } },
  ]);
  await page.waitForTimeout(2200); // one poll: running state + prompt in view
});

test.afterEach(async ({ page }) => {
  await setRunning(page, false);
});

// Panel diff #9 finding: the two-step drop used to rearm on every poll
// tick (the derived `pending` array gets a fresh identity on ANY journaled
// event), silently disarming between the arm click and the confirm click —
// exactly while the run streams. The arm must survive unrelated traffic
// and die only when its own row leaves the queue.
test("drop confirm survives poll-tick re-derivation", async ({ page }) => {
  const ta = page.getByPlaceholder(STEER_PH);
  const submit = page.locator('.chat-input button[type="submit"]');
  const rows = page.getByTestId("steer-queue-row");

  await ta.fill("Steer under noisy traffic");
  await submit.click();
  await expect(rows).toHaveCount(1);

  // Arm the drop.
  await rows.nth(0).getByRole("button", { name: "Drop queued steer 1" }).click();
  await expect(rows.nth(0).getByRole("button", { name: "Confirm drop queued steer 1" })).toBeVisible();

  // Unrelated journal traffic + one poll cycle: the old effect released
  // the arm here.
  await journal(page, [
    { type: "agent_text", payload: { text: "still thinking about the first file" } },
    { type: "agent_tool_call", payload: { tool: "bash", args: { cmd: "go test ./..." } } },
  ]);
  await page.waitForTimeout(2200);

  // The arm survived: the confirm click lands the drop on the first try.
  await expect(rows.nth(0).getByRole("button", { name: "Confirm drop queued steer 1" })).toBeVisible();
  await rows.nth(0).getByRole("button", { name: "Confirm drop queued steer 1" }).click();
  await expect(rows).toHaveCount(0, REFRESH);
});

// Panel diff #9 finding: a STEERLESS retry receipt (the pre-queue
// false-stop path) must not wear the drained-steer chip — it claims work
// that never happened. Receipts WITH steer linkage keep their quiet chips.
test("receipt chips require steer linkage", async ({ page }) => {
  const ta = page.getByPlaceholder(STEER_PH);
  const submit = page.locator('.chat-input button[type="submit"]');

  await ta.fill("Steer for the retry linkage check");
  await submit.click();
  await expect(page.getByTestId("steer-queue-row")).toHaveCount(1);

  const seqs = await queuedSteerSeqs(page);
  expect(seqs).toHaveLength(1);
  await journal(page, [
    { type: "review_action", payload: { action: "run_prompt", origin: "retry" } },
    { type: "review_action", payload: { action: "run_prompt", origin: "retry", actor: "auto_panel", steer_seqs: seqs } },
  ]);

  // The steerless retry renders the generic receipt badge, never the
  // drained-steer "Retry" chip; the steer-carrying retry keeps its chip.
  await expect(page.locator(".badge", { hasText: /^Retry$/ })).toHaveCount(1, REFRESH);
  await expect(page.locator(".badge", { hasText: "run_prompt" })).toBeVisible();
});

test("steer lifecycle: queue, drop, drain, unmount", async ({ page }) => {
  const ta = page.getByPlaceholder(STEER_PH);
  const submit = page.locator('.chat-input button[type="submit"]');
  const panel = page.getByTestId("steer-queue-panel");
  const rows = page.getByTestId("steer-queue-row");
  const active = page.getByTestId("steer-queue-active");

  // (a) The busy composer steers; two sends queue above the composer.
  await expect(submit).toHaveText("Steer");
  await ta.fill("First steer: prefer the queue's own component");
  await submit.click();
  await ta.fill("Second steer: keep the panel journal-derived");
  await submit.click();

  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0).locator(".queue-pos")).toContainText("#1");
  await expect(rows.nth(0).locator(".queue-next-tag")).toHaveText("next");
  await expect(rows.nth(1).locator(".queue-next-tag")).toHaveCount(0);
  await expect(panel).toContainText("Queued steers · 2");

  // The active card pins the original run's prompt behind a spinner.
  await expect(active).toContainText("Processing");
  await expect(active).toContainText(RUN_PROMPT);
  await expect(active.locator(".queue-row-text")).toHaveAttribute("title", RUN_PROMPT);

  // The composer is untouched: still steering, same placeholder.
  await expect(submit).toHaveText("Steer");
  await expect(ta).toHaveAttribute("placeholder", STEER_PH);

  // The sent steers sit in the transcript wearing the steer tag.
  await expect(page.locator(".steer-tag")).toHaveCount(2);

  // (b) Two-step armed drop of the second steer: first click arms.
  await rows.nth(1).getByRole("button", { name: "Drop queued steer 2" }).click();
  await rows.nth(1).getByRole("button", { name: "Confirm drop queued steer 2" }).click();
  await expect(rows).toHaveCount(1, REFRESH);
  await expect(rows.first().locator(".queue-row-text")).toHaveText(
    "First steer: prefer the queue's own component",
  );

  // Queue a third steer so the drain consumes a BATCH (one node of the
  // joined-continuation card).
  await ta.fill("Third steer: ride the follow-up prompt");
  await submit.click();
  await expect(rows).toHaveCount(2);

  // (c) The drain: a journaled continuation receipt referencing both
  // remaining seqs empties the queue and re-labels the active card.
  const seqs = await queuedSteerSeqs(page);
  expect(seqs).toHaveLength(2);
  await journal(page, [
    {
      type: "review_action",
      payload: { action: "run_prompt", origin: "continuation", actor: "auto_panel", steer_seqs: seqs },
    },
  ]);

  await expect(rows).toHaveCount(0, REFRESH);
  await expect(active).toContainText("Processing 2 queued steers", REFRESH);
  await expect(active).toContainText("First steer: prefer the queue's own component");
  await expect(active).toContainText("Third steer: ride the follow-up prompt");

  // The drain receipt leaves the quiet system chip in the transcript.
  await expect(page.locator(".badge", { hasText: "Steer follow-up" })).toBeVisible(REFRESH);

  // (d) Run finishes: with the queue already drained, the whole panel
  // unmounts — nothing lingers above the composer.
  await setRunning(page, false);
  await expect(panel).toHaveCount(0, REFRESH);
});
