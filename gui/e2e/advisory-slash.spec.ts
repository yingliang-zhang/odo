import { test, expect } from "@playwright/test";

// Advisory slash sends (/panel, /vision, /preview): the daemon answers
// them synchronously inside send_message and the /panel MoA fan-out can
// hold the RPC for minutes. The composer must detach — clear immediately
// and stay typeable for the whole consult — while the journaled question
// and eventual answer flow through the poll loop. The fixture latch
// (advisorySend.hold) simulates the daemon's hold; a plain release
// simulates the consult completing, a release WITH an error models the
// late failures (slash receipt gate, daemon restart, IPC drop) that
// arrive after the question already journaled.

// Window for everything that rides the event poll loop: journaled
// question/answer bubbles, the heartbeat N/M tally and .panel-leg rows,
// queue-dock rows (deriveParkedGoals over the polled journal), and the
// first test's final spinner disappearance (there the row is held open
// by polled progress, not the RPC). The advisory hold never flips the
// conversation out of idle cadence (1.5 s ticks + mock latency), and
// under post-boot full-suite load a single crushed tick can outlast the
// bare 5 s default — seen 2026-08-24 in the diff-#42 auto-land verify
// (auto_land_blocked reason=verify_failed, journaled at seq 11099): the
// 117-spec run in 5.1 min failed exactly one poll-dependent assertion
// here (spec:113), while the same case passed 6/6 standalone and 5/5
// under --repeat-each=5. 12 s matches the sibling REFRESH window
// (parked-goals, steer-queue, background-runs, review-inbox, sidebar) —
// the repo's existing poll-loop budget, not a new convention.
// Deliberately left on the default: assertions the advisory RPC itself
// resolves — textarea values, the error banner, park aria state, and
// spinner presence/counts in tests whose fixture never injects
// panelProgress (visibility is then App.tsx's local setPanelThinking
// counter alone, no poll tick involved).
const POLL = { timeout: 12_000 };

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test.afterEach(async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) return;
    fx.releaseAdvisorySends(); // drain any parked waiter so the page unhangs
    fx.advisorySend.hold = false;
    fx.advisorySend.fail = null;
    fx.advisorySend.released = false;
    fx.advisorySend.releaseError = null;
    fx.panelProgressState.current = null;
  });
});

test("the spinner row shows the panel's live N/M progress from poll heartbeats", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.advisorySend.hold = true;
  });

  const textarea = page.getByPlaceholder("Describe the change you want…");
  await textarea.fill("/panel show me progress");
  await textarea.press("Meta+Enter");

  // No heartbeat yet: the row renders without a tally.
  const spinner = page.locator(".panel-thinking");
  await expect(spinner).toBeVisible();
  await expect(spinner).toHaveText(/Panel consulting models…$/);

  // The daemon's poll-side tally arrives on the next heartbeat tick —
  // with per-leg rows naming who is still out and who is back.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.panelProgressState.current = {
      done: 1,
      total: 3,
      legs: [
        { model: "kimi-k3@sudo", done: true },
        { model: "glm-5.2@sudo", done: false },
        { model: "deepseek-v4-flash@sudo", done: false },
      ],
    };
  });
  await expect(spinner).toContainText("1/3 back", POLL);
  const legs = page.locator(".panel-leg");
  await expect(legs).toHaveCount(3, POLL);
  await expect(legs.nth(0)).toContainText("kimi-k3@sudo");
  await expect(legs.nth(0)).toContainText("back");
  await expect(legs.nth(1)).toContainText("consulting");

  // A daemon-side consult also lights the row on its own — the tally
  // arriving after this window's RPC cleared keeps the spinner alive.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.panelProgressState.current = {
      done: 2,
      total: 3,
      legs: [
        { model: "kimi-k3@sudo", done: true },
        { model: "glm-5.2@sudo", done: true },
        { model: "deepseek-v4-flash@sudo", done: false },
      ],
    };
    fx.releaseAdvisorySends();
  });
  await expect(spinner).toContainText("2/3 back", POLL);

  // A failed leg answers too: error icon + label, done tally counts it.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.panelProgressState.current = {
      done: 3,
      total: 3,
      legs: [
        { model: "kimi-k3@sudo", done: true },
        { model: "glm-5.2@sudo", done: true },
        { model: "deepseek-v4-flash@sudo", done: true, error: true },
      ],
    };
  });
  await expect(legs.nth(2)).toContainText("error", POLL);

  // Consult over: tally gone, answer journaled, row cleared.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.panelProgressState.current = null;
  });
  await expect(page.locator(".bubble-agent", { hasText: "Mock panel advisory answer." })).toBeVisible(POLL);
  await expect(spinner).toHaveCount(0, POLL);
});

test("/panel clears the composer immediately and typing stays live while the panel consults", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.advisorySend.hold = true;
  });

  const textarea = page.getByPlaceholder("Describe the change you want…");
  await textarea.fill("/panel 那么要怎么优化呢?");
  await textarea.press("Meta+Enter");

  // Detached: the draft clears and the textarea never disables, even
  // though the daemon-side consult is still in flight.
  await expect(textarea).toHaveValue("");
  await expect(textarea).toBeEnabled();

  // The question is journaled daemon-side up front — it reaches the
  // transcript through the poll loop while the RPC is still held.
  await expect(page.locator(".bubble-user", { hasText: "/panel 那么要怎么优化呢?" })).toBeVisible(POLL);
  await expect(page.locator(".panel-thinking")).toContainText("Panel consulting");

  // A follow-up composes and sends normally mid-consult.
  await textarea.fill("follow-up question");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(page.locator(".bubble-user", { hasText: "follow-up question" })).toBeVisible(POLL);

  // Consult completes: the advisory answer lands, the spinner clears.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.releaseAdvisorySends();
  });
  await expect(page.locator(".bubble-agent", { hasText: "Mock panel advisory answer." })).toBeVisible(POLL);
  await expect(page.locator(".panel-thinking")).toHaveCount(0);
});

test("a fast daemon refusal restores the draft and keeps the armed park", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.advisorySend.fail = "No review models configured for /panel. Set the 'review:' line in prefs.md.";
  });

  // Park armed BEFORE the slash draft (the toggle disables while the
  // draft starts with "/" but stays armed) — a slash routes before the
  // daemon's park branch, so the refusal must not consume the one-shot.
  const parkToggle = page.locator(".park-toggle");
  await parkToggle.click();
  await expect(parkToggle).toHaveAttribute("aria-pressed", "true");

  const textarea = page.getByPlaceholder("Describe the change you want…");
  await textarea.fill("/panel question");
  await page.getByRole("button", { name: "Park", exact: true }).click();

  // Fast refusal (pre-journal entry gate): full composer state back for a
  // one-edit retry — draft restored, park still armed, no spinner.
  await expect(page.locator(".error-banner")).toContainText("No review models configured");
  await expect(textarea).toHaveValue("/panel question");
  await expect(parkToggle).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(".panel-thinking")).toHaveCount(0);

  // The retry with the armed toggle actually parks the edited goal (the
  // dock's rows live inside the chip's popover; the seeded fixture goal
  // sits ahead at position one).
  await textarea.fill("Queue the ledger backfill");
  await page.getByRole("button", { name: "Park", exact: true }).click();
  await expect(page.locator(".queue-chip")).toContainText("Queue · 2", POLL);
  await page.locator(".queue-chip").click();
  await expect(page.locator(".queue-row-text", { hasText: "Queue the ledger backfill" })).toBeVisible(POLL);
  await expect(parkToggle).toHaveAttribute("aria-pressed", "false");
});

test("a late refusal never clobbers an in-progress follow-up", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.advisorySend.hold = true;
  });

  const textarea = page.getByPlaceholder("Describe the change you want…");
  await textarea.fill("/panel 那么要怎么优化呢?");
  await textarea.press("Meta+Enter");
  await expect(textarea).toHaveValue("");
  await expect(page.locator(".panel-thinking")).toBeVisible();

  // The user types a follow-up mid-consult — the whole point of the
  // detach. A LATE daemon failure arriving now must leave that typing
  // untouched: the error is already named in the banner and the question
  // is journaled, so a stale restore would only destroy newer work.
  await textarea.fill("scratch follow-up");
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.releaseAdvisorySends("daemon restarted mid-consult");
  });

  await expect(page.locator(".error-banner")).toContainText("daemon restarted mid-consult");
  await expect(textarea).toHaveValue("scratch follow-up");
  await expect(page.locator(".bubble-user", { hasText: "/panel 那么要怎么优化呢?" })).toBeVisible(POLL);
  await expect(page.locator(".panel-thinking")).toHaveCount(0);
});

test("a late refusal on an untouched composer still offers the retry restore", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.advisorySend.hold = true;
  });

  const textarea = page.getByPlaceholder("Describe the change you want…");
  await textarea.fill("/panel question");
  await page.getByRole("button", { name: "Send", exact: true }).click();
  await expect(textarea).toHaveValue("");

  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.releaseAdvisorySends("IPC dropped mid-consult");
  });

  // Box left untouched since the send → the restore is a pure affordance
  // and cannot destroy anything: bring the question back for a retry.
  await expect(page.locator(".error-banner")).toContainText("IPC dropped mid-consult");
  await expect(textarea).toHaveValue("/panel question");
  await expect(page.locator(".panel-thinking")).toHaveCount(0);
});

test("park stays armed through a successful advisory consult and the next goal parks", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.advisorySend.hold = true;
  });

  const parkToggle = page.locator(".park-toggle");
  await parkToggle.click();
  await expect(parkToggle).toHaveAttribute("aria-pressed", "true");

  const textarea = page.getByPlaceholder("Describe the change you want…");
  await textarea.fill("/panel is this queued?");
  await page.getByRole("button", { name: "Park", exact: true }).click();

  // The toggle disables while a slash draft sits in the box, but the
  // consult must NOT disarm it: the daemon routed the slash before its
  // park branch, so nothing was ever queued on the user's behalf.
  await expect(textarea).toHaveValue("");
  await expect(parkToggle).toHaveAttribute("aria-pressed", "true");

  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.releaseAdvisorySends();
  });
  await expect(page.locator(".bubble-agent", { hasText: "Mock panel advisory answer." })).toBeVisible(POLL);
  await expect(parkToggle).toHaveAttribute("aria-pressed", "true");

  // The armed intent carries to the next real goal, which really parks
  // (rows live in the dock chip's popover).
  await textarea.fill("Queue the nav refactor");
  await page.getByRole("button", { name: "Park", exact: true }).click();
  await expect(page.locator(".queue-chip")).toContainText("Queue · 2", POLL);
  await page.locator(".queue-chip").click();
  await expect(page.locator(".queue-row-text", { hasText: "Queue the nav refactor" })).toBeVisible(POLL);
  await expect(parkToggle).toHaveAttribute("aria-pressed", "false");
});
