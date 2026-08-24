import { test, expect, type Page } from "@playwright/test";

// Auto-land pipeline chip (design lock pipeline-indicator-lock.md; Phase 2:
// daemon auto_land_started stage breadcrumbs): StatusBar status derived
// journal-only from review_action
// {actor:"auto_panel"} rows + the pending-diff list. Fixtures boot with
// auto_apply:"main" and conv 1's diff #1 pending-but-unevaluated; other
// states are journaled mid-session through the __odoFixtures lever (the
// mock's afterSeq poll tail delivers fixture-appended rows exactly like
// daemon-journaled ones). Shortcut/styling conventions follow the existing
// specs (Meta+…, status-badge chrome).
//
// Two review-driven contracts this spec pins:
//   - per-conversation scope (lock rule 2): pipeline derivations read only
//     the ACTIVE conversation's stream — a suspension journaled in another
//     conversation must never leak into the tracked one (see test 2);
//   - visibility contract: derivePipelineStates returns only currently-
//     visible states (expired flashes dropped), so chip absence comes from
//     an EMPTY derivation, not from a component null-render fallback.

// Idle poll is 1.5s plus mock latency — state assertions wait generously.
const PIPE_POLL = { timeout: 10_000 };
const chip = (page: Page) => page.locator(".auto-land-chip");

interface InjectedRow {
  type: string;
  payload: Record<string, unknown>;
  // Conversation the row belongs to (default 1, the boot conversation).
  conv?: number;
}

// Append journaled rows as of NOW (fx.ev backdates by id; the landed
// flash's ≤4s window reads created_at verbatim, so injections stamp real
// time for a deterministic expiry).
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

// Flip the pref end-to-end: mutate the fixture, then save through the real
// SettingsPanel path so App.refetchSettings (get_settings round trip)
// observes it — the same trigger a human save causes. The mock serves a
// clone per fetch, so the new value survives React's Object.is bail-out.
async function setAutoApply(page: Page, value: "off" | "main") {
  await page.evaluate((v) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.defaultSettings.auto_apply = v;
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

test("pref off hides the chip; re-enabling re-derives it", async ({ page }) => {
  // Baseline: pref on → the queued chip is up for diff #1.
  await expect(chip(page)).toBeVisible(PIPE_POLL);

  await setAutoApply(page, "off");
  await expect(chip(page)).toHaveCount(0);

  // No latch in either direction: restoring the pref re-derives the chip.
  await setAutoApply(page, "main");
  await expect(chip(page)).toBeVisible(PIPE_POLL);
  await expect(chip(page)).toContainText("queued");
});

test("conversation scope: a suspension elsewhere never leaks in", async ({ page }) => {
  await expect(chip(page)).toContainText("queued", PIPE_POLL);

  // Journal a ladder suspension into conversation 3 — daemon-true marker
  // shape, real-time stamp, WRONG conversation for the chip.
  await journal(page, [
    {
      type: "memory_update",
      payload: { layer: "auto_land", cause: "ladder_suspended", detail: "cross-conversation leak probe" },
      conv: 3,
    },
  ]);

  // Outlast two idle-poll ticks (2 × 1.5s): the marker is never delivered
  // to conv 1's stream (poll_events carries conversationId), so the chip
  // stays queued. A derivation reading a global stream would have flipped
  // it to "auto-land suspended" within one tick.
  await page.waitForTimeout(3_400);
  await expect(chip(page)).toContainText("queued");
  await expect(chip(page)).not.toContainText("suspended");

  // And in conv 3 itself: the fixture's settle-ladder story (revise chain,
  // suspension marker, blocked echo) plus the pre-aged auto accept, but
  // NO pending diff — nothing is tracked, derivation is EMPTY, the chip
  // never appears (the aged accept must not boot-flash either).
  await page.locator(".sidebar .ws-row", { hasText: "fix-daemon-binary" }).click();
  await expect(page.locator(".app-statusbar")).toContainText("fix-daemon-binary");
  await page.waitForTimeout(3_400);
  await expect(chip(page)).toHaveCount(0);
});

test("queued spinner while a pending diff awaits evaluation", async ({ page }) => {
  await expect(chip(page)).toBeVisible(PIPE_POLL);
  await expect(chip(page)).toHaveClass(/is-queued/);
  await expect(chip(page)).toContainText("queued");
  await expect(chip(page).locator(".spin")).toBeVisible();
});

test("phase-2 stage breadcrumbs label the running stage, then land", async ({ page }) => {
  // Daemon Phase 2 journal order (autoland.go): started{verify} before the
  // .odo-verify gate, started{panel} before the fan-out, accept on land.
  // The chip must not hold "queued" through either silent stage.
  await journal(page, [
    { type: "review_action", payload: { action: "auto_land_started", diff_id: 1, actor: "auto_panel", stage: "verify", patch_sha16: "0123456789abcdef" } },
  ]);
  await expect(chip(page)).toContainText("verify running…", PIPE_POLL);
  await expect(chip(page)).toHaveClass(/is-in_flight/);
  await expect(chip(page).locator(".spin")).toBeVisible();

  await journal(page, [
    { type: "review_action", payload: { action: "auto_land_started", diff_id: 1, actor: "auto_panel", stage: "panel", patch_sha16: "0123456789abcdef" } },
  ]);
  await expect(chip(page)).toContainText("panel reviewing…", PIPE_POLL);

  await journal(page, [
    { type: "review_action", payload: { action: "accept", diff_id: 1, actor: "auto_panel" } },
  ]);
  await expect(chip(page)).toContainText("landed", PIPE_POLL);
  // And the breadcrumbs must NOT appear as transcript badges: the chip is
  // their only surface (LedgerPanel keeps the verbatim rows).
  await expect(page.locator(".bubble-review", { hasText: "auto_land_started" })).toHaveCount(0);
});

test("blocked shows the journaled reason, sticky", async ({ page }) => {
  await journal(page, [
    { type: "review_action", payload: { action: "auto_land_blocked", diff_id: 1, actor: "auto_panel", reason: "verify_failed" } },
  ]);
  await expect(chip(page)).toContainText("blocked: verify_failed", PIPE_POLL);
  await expect(chip(page)).toHaveClass(/is-blocked/);
  // Sticky, not a flash: still there after the transient window passes.
  await page.waitForTimeout(4_500);
  await expect(chip(page)).toContainText("blocked: verify_failed");
});

test("auto accept flashes landed green, then the chip clears", async ({ page }) => {
  await journal(page, [
    { type: "review_action", payload: { action: "accept", diff_id: 1, actor: "auto_panel" } },
  ]);
  await expect(chip(page)).toContainText("landed", PIPE_POLL);
  await expect(chip(page)).toHaveClass(/is-landed/);
  // Transient ≤4s from the journaled row — the chip fully clears, no
  // latch: the entry expires out of the derivation itself (now-gated),
  // and the tick covers render-lag in between.
  await expect(chip(page)).toHaveCount(0, PIPE_POLL);
});

test("revise round shows the journaled round number", async ({ page }) => {
  // Daemon round-2 shape (settle.go): the row evaluates the round-1
  // product (diff 2) and links it to the chain root (diff 1) — the ONE
  // pending diff shows the chain's round.
  await journal(page, [
    { type: "review_action", payload: { action: "auto_revise_round", diff_id: 2, origin_diff_id: 1, actor: "auto_panel", round: 2 } },
  ]);
  await expect(chip(page)).toContainText("repair round 2", PIPE_POLL);
});

test("a chain hard stop propagates blocked to the chain's pending diff", async ({ page }) => {
  // The round evaluates diff 2 (rooted at diff 1); its hard stop targets
  // diff 2 only — but diff 1 IS the same chain, so its chip state is the
  // chain's blocked, never a stale "repair round 2".
  await journal(page, [
    { type: "review_action", payload: { action: "auto_revise_round", diff_id: 2, origin_diff_id: 1, actor: "auto_panel", round: 2 } },
    { type: "review_action", payload: { action: "auto_land_blocked", diff_id: 2, actor: "auto_panel", reason: "revise_no_progress" } },
  ]);
  await expect(chip(page)).toContainText("blocked: revise_no_progress", PIPE_POLL);
  await expect(chip(page)).toHaveClass(/is-blocked/);
});

test("ladder suspension overrides every tracked per-diff state", async ({ page }) => {
  await journal(page, [
    { type: "review_action", payload: { action: "auto_land_blocked", diff_id: 1, actor: "auto_panel", reason: "revise_no_progress" } },
  ]);
  await expect(chip(page)).toContainText("blocked: revise_no_progress", PIPE_POLL);

  // Suspension markers are conversation-scoped (settle.go journalLadder):
  // newest marker wins uniformly — including over a blocked row.
  await journal(page, [
    { type: "memory_update", payload: { layer: "auto_land", cause: "ladder_suspended", detail: "2 consecutive revise rounds ended without landing" } },
  ]);
  await expect(chip(page)).toContainText("auto-land suspended", PIPE_POLL);
  await expect(chip(page)).toHaveClass(/is-suspended/);

  // A human accept journals the resume marker and the ladder clears.
  await journal(page, [
    { type: "memory_update", payload: { layer: "auto_land", cause: "ladder_resumed", detail: "human accepted diff 1; ladder resumed" } },
  ]);
  await expect(chip(page)).toContainText("blocked: revise_no_progress", PIPE_POLL);
});

test("panel reviewing locks the human-action buttons on card and inbox row", async ({ page }) => {
  // Misfire-guard contract: while the daemon pipeline is actively working
  // a diff (in_flight/landing — here stage:panel), the Changes card and
  // the inbox row disable Review/Accept/Reject and name the running
  // stage, so a stray click can't race the panel verdict. Hard stops hand
  // the decision back: blocked re-enables everything.
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".diff-card")).toBeVisible();
  // Baseline (queued — nothing running yet): fully actionable.
  await expect(page.locator(".diff-header .btn-accept")).toBeEnabled();
  await expect(page.locator(".panel-lock")).toHaveCount(0);

  await journal(page, [
    { type: "review_action", payload: { action: "auto_land_started", diff_id: 1, actor: "auto_panel", stage: "panel", patch_sha16: "0123456789abcdef" } },
  ]);
  // Poll-derived (~1.5s cadence), same as the chip.
  await expect(page.locator(".panel-lock")).toBeVisible(PIPE_POLL);
  await expect(page.locator(".panel-lock")).toContainText("panel reviewing…");
  await expect(page.locator(".panel-lock .spin")).toBeVisible();
  await expect(page.locator(".diff-header .btn-accept")).toBeDisabled();
  await expect(page.locator(".diff-header .btn-reject")).toBeDisabled();
  await expect(page.locator(".diff-header .btn-review")).toBeDisabled();

  // The inbox row for the same diff locks in lockstep (collapsed surface).
  await page.getByRole("tab", { name: /Review/ }).click();
  await expect(page.locator(".review-inbox")).toBeVisible();
  const row = page.locator(".inbox-row", { hasText: "Diff #1" });
  await expect(row.locator(".panel-lock")).toBeVisible(PIPE_POLL);
  await expect(row.locator(".inbox-accept")).toBeDisabled();
  await expect(row.locator(".inbox-reject")).toBeDisabled();

  // Hard stop hands the diff back to the human: the lock lifts in both
  // surfaces and the stage indicator clears.
  await journal(page, [
    { type: "review_action", payload: { action: "auto_land_blocked", diff_id: 1, actor: "auto_panel", reason: "panel_mixed" } },
  ]);
  await expect(row.locator(".inbox-accept")).toBeEnabled(PIPE_POLL);
  await expect(row.locator(".inbox-reject")).toBeEnabled();
  await expect(row.locator(".panel-lock")).toHaveCount(0);
  await page.getByRole("tab", { name: /Changes/ }).click();
  await expect(page.locator(".diff-header .btn-accept")).toBeEnabled();
  await expect(page.locator(".panel-lock")).toHaveCount(0);
});

test("popover lists tracked diffs and the row opens the Review tab", async ({ page }) => {
  await journal(page, [
    { type: "review_action", payload: { action: "auto_land_blocked", diff_id: 1, actor: "auto_panel", reason: "panel_mixed" } },
  ]);
  await expect(chip(page)).toBeVisible(PIPE_POLL);
  await chip(page).click();

  const pop = page.locator(".auto-land-popover");
  await expect(pop).toBeVisible();
  const rows = pop.locator(".auto-land-row");
  await expect(rows).toHaveCount(1);
  await expect(rows.nth(0)).toContainText("Diff #1");
  await expect(rows.nth(0)).toContainText("blocked: panel_mixed");

  await rows.nth(0).click();
  await expect(pop).toHaveCount(0);
  await expect(page.locator(".context-panel")).toBeVisible();
  await expect(page.getByRole("tab", { name: /Review/ })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".review-inbox")).toBeVisible();
});
