import { test, expect, type Page } from "@playwright/test";

// M19 (/loop) GUI wave — design-lock items V1 (LoopChip + TS fold), V11
// (loop_notify_on_complete), V13 (slash autocomplete Hermes-parity).
// loop_event rows arrive through the same __odoFixtures → poll path the
// daemon's journalLoop uses; the mock answers loop_ctl with daemon-true
// rows (mock-invoke.ts comments). Conventions follow pipeline-chip.spec.
//
// Pinned contracts:
//   - V13 immediate FULL list at first "/" keystroke, descriptions, arrow
//     navigation, Tab/Enter accept, accepted command as highlighted token,
//     and Esc's DUAL gate (menu closes; the running agent is never
//     cancelled — fx.cancelCount is the observable).
//   - V1 chip re-derives purely from the journal: phase / round N-max /
//     spent tokens shift row by row; stop + resume wire loop_ctl and the
//     chip leaves on the terminal row.
//   - V11: FIRST sight of a terminal kind fires ONE notification (the
//     __odoLoopNotify seam) and journals loop_notified; a page reload
//     replays the receipt and never re-fires.

const POLL = { timeout: 10_000 };
const chip = (page: Page) => page.locator(".loop-chip");
const composer = (page: Page) => page.getByRole("textbox", { name: /message/i });

interface InjectedRow {
  type: string;
  payload: Record<string, unknown>;
  conv?: number;
}

// Same injection contract as pipeline-chip.spec's journal().
async function journal(page: Page, rows: InjectedRow[]) {
  await page.evaluate((r) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    for (const row of r) {
      const e = fx.ev(row.type, row.payload, row.conv ?? 1);
      e.created_at = new Date().toISOString();
      fx.events.push(e);
    }
  }, rows);
}

const started = (base: string) => ({
  type: "loop_event",
  payload: {
    kind: "loop_started",
    loop_id: 0,
    mode: "audit",
    base,
    max_rounds: 10,
    budget_tokens: 2_000_000,
    spent_tokens: 0,
  },
});

// Journal a loop_started and return its seq (= the loop's id).
async function startLoop(page: Page): Promise<number> {
  await journal(page, [started("a97bd3d")]);
  return page.evaluate(() => {
    const fx = window.__odoFixtures;
    const e = fx?.events.find((ev) => ev.type === "loop_event" && ev.payload?.kind === "loop_started");
    if (!e) throw new Error("no loop_started journaled");
    return e.seq;
  });
}

// The notification seam, recorded per page into a plain array. Installed
// via addInitScript so it precedes the app bundle.
async function installNotifySpy(page: Page) {
  await page.addInitScript(() => {
    (window as unknown as { __loopNotifyCalls: { title: string; body: string }[] }).__loopNotifyCalls = [];
    (window as unknown as { __odoLoopNotify: (p: { title: string; body: string }) => void }).__odoLoopNotify = (p) => {
      (window as unknown as { __loopNotifyCalls: { title: string; body: string }[] }).__loopNotifyCalls.push(p);
    };
  });
}

const notifyCalls = (page: Page): Promise<{ title: string; body: string }[]> =>
  page.evaluate(() => (window as unknown as { __loopNotifyCalls: { title: string; body: string }[] }).__loopNotifyCalls);

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("V13: '/' opens the full list immediately with descriptions", async ({ page }) => {
  await composer(page).click();
  await composer(page).pressSequentially("/");
  const menu = page.locator(".slash-menu");
  await expect(menu).toBeVisible();
  // Unfiltered at the first keystroke — every registry entry, /loop
  // subcommands included.
  const items = menu.locator(".slash-item");
  await expect(items).toHaveCount(8);
  await expect(menu.getByRole("option", { name: /\/loop audit/ })).toBeVisible();
  await expect(menu.getByRole("option", { name: /\/loop resume/ })).toBeVisible();
  // One-line description rides every entry.
  await expect(menu.getByRole("option", { name: /\/panel/ })).toContainText("MoA thinking");
  await expect(menu.getByRole("option", { name: /\/loop stop/ })).toContainText("Terminal stop");
});

test("V13: typing narrows; arrows navigate; Tab accepts as a token", async ({ page }) => {
  await composer(page).click();
  await composer(page).pressSequentially("/lo");
  const menu = page.locator(".slash-menu");
  const items = menu.locator(".slash-item");
  await expect(items).toHaveCount(5); // only the /loop entries survive the filter
  // ArrowDown onto the second entry, Tab accepts it — draft becomes the
  // command word and the token overlay highlights it.
  await page.keyboard.press("ArrowDown");
  await expect(items.nth(1)).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Tab");
  await expect(menu).toHaveCount(0);
  await expect(composer(page)).toHaveValue("/loop tasks");
  const token = page.locator(".composer-slash-token");
  await expect(token).toBeVisible();
  await expect(token).toHaveText("/loop tasks");
  // It is a background pill, not invisible text.
  const bg = await token.evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(bg).not.toBe("rgba(0, 0, 0, 0)");
  // The caret sits right after the command word; typed args keep the token.
  await composer(page).pressSequentially(" 1. fix the flake");
  await expect(token).toHaveText("/loop tasks");
  await expect(composer(page)).toHaveValue("/loop tasks 1. fix the flake");
  // Deleting INTO the command word clears it — it was a command only while
  // the draft's prefix was intact.
  for (let i = 0; i < " 1. fix the flake".length + 1; i++) await page.keyboard.press("Backspace");
  await expect(composer(page)).toHaveValue("/loop task");
  await expect(page.locator(".composer-slash-token")).toHaveCount(0);
});

test("V13: Enter accepts the active row exactly like Tab", async ({ page }) => {
  await composer(page).click();
  await composer(page).pressSequentially("/loop st");
  const menu = page.locator(".slash-menu");
  await expect(menu.locator(".slash-item")).toHaveCount(2); // status | stop
  await page.keyboard.press("Enter");
  await expect(menu).toHaveCount(0);
  await expect(composer(page)).toHaveValue("/loop status");
  await expect(page.locator(".composer-slash-token")).toHaveText("/loop status");
});

test("V13: Esc closes the menu and never cancels the running agent", async ({ page }) => {
  // Foreground run live: any leaked Esc would call cancel() (16 prior
  // regressions; fx.cancelCount is the observable).
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.runState.foreground = true;
    fx.cancelCount.n = 0;
  });
  await expect(page.locator(".stop-btn")).toBeVisible(POLL);

  await composer(page).click();
  await composer(page).pressSequentially("/lo");
  await expect(page.locator(".slash-menu")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.locator(".slash-menu")).toHaveCount(0);
  // The draft stays (menu Esc is not the draft-clear Esc), the run still
  // stands, and cancel never fired through either gate layer.
  await expect(composer(page)).toHaveValue("/lo");
  await expect(page.locator(".stop-btn")).toBeVisible();
  expect(await page.evaluate(() => window.__odoFixtures?.cancelCount.n ?? -1)).toBe(0);
});

test("V1: chip follows the journal — phase, round N/max, spent; stop ends it", async ({ page }) => {
  // The V11 watcher is live here but harmless: the suspend/stop rows fire
  // through the plugin import, which throws in a plain browser (swallowed
  // per contract), and the journaled receipt bubbles don't collide with
  // the assertions below.
  await expect(chip(page)).toHaveCount(0);
  const loopId = await startLoop(page);
  await expect(chip(page)).toBeVisible(POLL);
  await expect(chip(page)).toContainText("loop · audit · seeding · round 0/10 · 0 tok");

  await journal(page, [
    { type: "loop_event", payload: { kind: "loop_audit_round", loop_id: loopId, round: 1, spent_tokens: 4000 } },
  ]);
  await expect(chip(page)).toContainText("auditing round 1", POLL);
  await expect(chip(page)).toContainText("round 1/10");
  await expect(chip(page)).toContainText("4.0k tok");

  await journal(page, [
    { type: "loop_event", payload: { kind: "loop_verdict", loop_id: loopId, round: 1, verdict: "fix", spent_tokens: 5200 } },
  ]);
  await expect(chip(page)).toContainText("fixing", POLL);
  await expect(chip(page)).toContainText("5.2k tok");

  await journal(page, [
    { type: "loop_event", payload: { kind: "loop_suspended", loop_id: loopId, cause: "stall", spent_tokens: 5200 } },
  ]);
  await expect(chip(page)).toContainText("suspended: stall", POLL);
  await expect(chip(page)).toHaveClass(/is-suspended/);
  await expect(chip(page).getByRole("button", { name: "Resume loop" })).toBeVisible();

  // Resume clears the suspend; a stall resume converts the open fix to a
  // re-audit (the fold's default resume rule), so the next round shows.
  await chip(page).getByRole("button", { name: "Resume loop" }).click();
  await expect(chip(page)).toContainText("auditing round 2", POLL);
  await expect(chip(page)).toHaveClass(/is-active/);

  // Stop is terminal: the mock journals loop_stopped and the chip clears.
  await chip(page).getByRole("button", { name: "Stop loop" }).click();
  await expect(chip(page)).toHaveCount(0, POLL);
  // Terminal + resume rows render as bookkeeping bubbles, never agent text.
  await expect(page.locator(".loop-event-bubble").getByText(/resumed · stall/)).toBeVisible();
  await expect(page.locator(".loop-event-bubble").getByText(/stopped · stopped from the GUI/)).toBeVisible();
});

test("V11: first terminal kind fires once, journals the receipt, never re-fires on reopen", async ({ page }) => {
  await installNotifySpy(page);
  await page.goto("/");

  const loopId = await startLoop(page);
  await journal(page, [
    // The summary's "rounds N" is the fold's rounds seen — the daemon-
    // true stream carries the round rows the counter derives from.
    { type: "loop_event", payload: { kind: "loop_audit_round", loop_id: loopId, round: 1, spent_tokens: 20_000 } },
    { type: "loop_event", payload: { kind: "loop_audit_round", loop_id: loopId, round: 2, spent_tokens: 42_000 } },
    { type: "loop_event", payload: { kind: "loop_completed", loop_id: loopId, rounds: 2, fixes_landed: 1, spent_tokens: 42_000 } },
  ]);

  // ONE fire with the design-lock summary shape.
  await expect.poll(async () => (await notifyCalls(page)).length, POLL).toBe(1);
  expect((await notifyCalls(page))[0]).toEqual({
    title: "Odo",
    body: "Odo /loop audit: loop_completed (rounds 2, tokens 42000)",
  });

  // The receipt rides the poll back in (the mock journals loop_notified
  // on the loop_ctl notified call).
  await expect(page.locator(".loop-event-bubble").getByText(/notified · loop_completed/)).toBeVisible(POLL);

  // Reopen: a browser reload re-mints the in-memory fixture journal, so
  // re-seed the whole conversation prefix (started → completed → the
  // journaled receipt) in ONE evaluate — a single fold application never
  // splits the receipt from the terminal row. Outliving two idle-poll
  // ticks would catch any re-fire on the fresh page's spy array.
  await page.reload();
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    const s = fx.ev("loop_event", { kind: "loop_started", loop_id: 0, mode: "audit", base: "a97bd3d", max_rounds: 10, budget_tokens: 2_000_000, spent_tokens: 0 }, 1);
    s.created_at = new Date().toISOString();
    fx.events.push(s);
    const c = fx.ev("loop_event", { kind: "loop_completed", loop_id: s.seq, rounds: 2, fixes_landed: 1, spent_tokens: 42_000 }, 1);
    c.created_at = new Date().toISOString();
    fx.events.push(c);
    const n = fx.ev("loop_event", { kind: "loop_notified", loop_id: s.seq, terminal_kind: "loop_completed", origin: "loop_ctl", spent_tokens: 42_000 }, 1);
    n.created_at = new Date().toISOString();
    fx.events.push(n);
  });
  await page.waitForTimeout(3_400);
  expect((await notifyCalls(page)).length).toBe(0);
});

test("V11: pref off suppresses the fire entirely", async ({ page }) => {
  await installNotifySpy(page);
  await page.goto("/");
  // The watcher reads appSettings, fetched at bootstrap — a bare fixture
  // mutation arrives too late. Drive the same Settings save round-trip a
  // human would (the mock persists; App refetches), then journal.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.defaultSettings.loop_notify_on_complete = false;
  });
  await page.keyboard.press("Meta+,");
  const panel = page.locator(".settings-panel");
  await expect(panel).toBeVisible();
  await panel.getByRole("button", { name: "Save" }).click();
  await expect(panel.locator(".settings-toast")).toBeVisible();
  await panel.getByRole("button", { name: "Close" }).click();
  await expect(panel).toHaveCount(0);

  const loopId = await startLoop(page);
  await journal(page, [
    { type: "loop_event", payload: { kind: "loop_completed", loop_id: loopId, rounds: 1, fixes_landed: 0, spent_tokens: 5 } },
  ]);
  await page.waitForTimeout(3_400); // two idle-poll ticks — nothing fires
  expect((await notifyCalls(page)).length).toBe(0);
});
