// UX-2 (D5 Stage 0 / A2-1): the k8s Jobs chip's three degradation states,
// verbatim from the 4/4 amendment: off-by-config → NO chip, no tab, no
// polling; on-but-broken → VISIBLE dimmed chip + reason in popover; ok →
// count-only face + job rows. The scenario is seeded pre-boot via ?k8s=
// (fixtures read it at module load — off never polls, so off→on mid-
// session is impossible by design).
import { test, expect } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

const POLL = { timeout: 12_000 }; // the degrade drills wait one 5s poll

test("k8s off-by-config renders NO Jobs chip and never polls", async ({ page }) => {
  await page.goto("/"); // default scenario is off — no ?k8s=
  await expect(page.locator(".app-statusbar")).toBeVisible();
  await expect(page.locator('[data-chip="jobs"]')).toHaveCount(0);
  const footer = page.locator(".app-statusbar");
  await expect(footer).not.toContainText("Jobs", { timeout: 5_000 });
});

test("kubectl-missing stays VISIBLE as a dimmed chip with the reason in the popover", async ({ page }) => {
  await page.goto("/?k8s=unavailable-ENOENT");
  const chip = page.locator('[data-chip="jobs"]');
  await expect(chip).toBeVisible(POLL);
  await expect(chip).toContainText("Jobs · unavailable");
  await expect(chip).toHaveClass(/jobs-unavailable/); // dimmed posture
  // A2-1: the reason is never absent — popover carries the cause class.
  await chip.click();
  const pop = page.locator(".jobs-popover");
  await expect(pop).toBeVisible();
  await expect(pop.getByText("kubectl not found on the daemon's PATH")).toBeVisible();
  await expect(pop.getByText(/no snapshot yet — retrying every 5s/)).toBeVisible();
});

test("configured cluster: count-only chip face and job rows with phase/completions/age", async ({ page }) => {
  await page.goto("/?k8s=ok");
  const chip = page.locator('[data-chip="jobs"]');
  await expect(chip).toBeVisible(POLL);
  // A2-5: count-only face — the text pattern never grows a progress bar;
  // the active-batch count rides as "+ n" (fixture: 1 non-stale batch).
  await expect(chip).toHaveText(/Jobs · 2 \+ 1$/);
  await chip.click();
  const rows = page.locator(".jobs-popover .jobs-row");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText("train-3dgs-zz42");
  await expect(rows.nth(0)).toContainText("Complete");
  await expect(rows.nth(0)).toContainText("1/1");
  await expect(rows.nth(0)).toContainText("2h");
  await expect(rows.nth(1)).toContainText("cali-blender-k7");
  await expect(rows.nth(1)).toContainText("Active");
  await expect(rows.nth(1)).toContainText("0/1");
  await expect(rows.nth(1)).toContainText("12m");
});

test("exec-shaped failures show the daemon's capped stderr tail below the reason", async ({ page }) => {
  await page.goto("/?k8s=ok");
  const chip = page.locator('[data-chip="jobs"]');
  await expect(chip).toHaveText(/Jobs · 2 \+ 1$/, POLL);

  // revise-1 (panel #2): flip mid-session to an unreachable cluster — an
  // exec-shaped failure, so the envelope carries `detail`.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.k8sStatusFixture.scenario = "unreachable";
  });
  await expect(chip).toContainText("Jobs · unavailable", POLL);
  await chip.click();
  const pop = page.locator(".jobs-popover");
  await expect(pop.getByText("cluster unreachable")).toBeVisible();
  // The canned sentence stays; the real kubectl diagnosis rides below it.
  const detail = pop.locator(".jobs-detail");
  await expect(detail).toBeVisible();
  await expect(detail).toContainText("dial tcp 10.96.0.1:443: connect: no route to host");
});

test("folded chip forks zero kubectl calls; reappearing refetches immediately", async ({ page }) => {
  await page.goto("/?k8s=ok");
  const chip = page.locator('[data-chip="jobs"]');
  await expect(chip).toHaveText(/Jobs · 2 \+ 1$/, POLL);
  const readCalls = () =>
    page.evaluate(() => {
      const fx = window.__odoFixtures;
      if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
      return fx.k8sStatusFixture.calls;
    });

  // Fold the chip: narrow the viewport until the overflow engine hides
  // rank-3 chips (ctx=0 and omp=1 fold first; jobs follows well before
  // actionable-tier diffs/wiki/memory).
  await page.setViewportSize({ width: 420, height: 900 });
  await expect(chip).toHaveClass(/chip-hidden/, POLL);
  const callsAtFold = await readCalls();
  // More than one full 5s cadence with zero growth — the interval is
  // GONE, not merely failing silently.
  await page.waitForTimeout(6_000);
  expect(await readCalls()).toBe(callsAtFold);

  // Unfold: the one-shot visibility-transition refetch lands fast (well
  // inside one 5s cadence), THEN the interval resumes.
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(chip).not.toHaveClass(/chip-hidden/, POLL);
  await expect.poll(readCalls, { timeout: 2_000 }).toBeGreaterThan(callsAtFold);
});

test("mid-session degrade keeps the last-good snapshot with its age", async ({ page }) => {
  await page.goto("/?k8s=ok");
  const chip = page.locator('[data-chip="jobs"]');
  await expect(chip).toHaveText(/Jobs · 2 \+ 1$/, POLL);

  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.k8sStatusFixture.scenario = "unavailable-ENOENT";
  });
  await expect(chip).toContainText("Jobs · unavailable", POLL);
  await expect(chip).toHaveClass(/jobs-unavailable/);

  await chip.click();
  const pop = page.locator(".jobs-popover");
  await expect(pop.getByText(/last good/)).toBeVisible(POLL);
  // The last-good table stays — a broken sensor never blanks its data.
  await expect(pop.locator(".jobs-row")).toHaveCount(2);
});

// ---------- D5b (A4 D3 + A2-5) ----------

test("popover carries one row per configured namespace (A4 D3, no third chip state)", async ({ page }) => {
  await page.goto("/?k8s=ok");
  const chip = page.locator('[data-chip="jobs"]');
  await expect(chip).toHaveText(/Jobs · 2 \+ 1$/, POLL);
  await chip.click();
  const pop = page.locator(".jobs-popover");
  // Answering ns → count header; fixture configured order default,lab.
  await expect(pop.getByText("default · 1 job")).toBeVisible();
  await expect(pop.getByText("lab · 1 job · 1 active")).toBeVisible();
  await expect(pop.locator(".jobs-row")).toHaveCount(2);
});

test("batch one-liners render under the divider and click through to the Jobs tab", async ({ page }) => {
  await page.goto("/?k8s=ok");
  const chip = page.locator('[data-chip="jobs"]');
  await expect(chip).toHaveText(/Jobs · 2 \+ 1$/, POLL);
  await chip.click();
  const pop = page.locator(".jobs-popover");
  // running w/ rate → ETA derived; stale row greys out; done row shows errs.
  await expect(pop.getByText(/dsv-transcode 72% · ETA /)).toBeVisible();
  await expect(pop.getByText(/dsv-push .*stale — last update/)).toBeVisible();
  await expect(pop.getByText("dsv-verify done · 2 errs")).toBeVisible();

  // A2-5: rows navigate to the ONE Jobs tab through App's single
  // activation path — panel opens, jobs tab active, table + batch bars.
  await pop.getByText(/dsv-transcode 72%/).click();
  const panel = page.locator(".context-panel");
  await expect(panel).toBeVisible();
  await expect(panel.getByRole("tab", { name: /Jobs/ })).toHaveAttribute("aria-selected", "true");
  const jp = panel.locator(".jobs-panel");
  await expect(jp).toBeVisible();
  // Jobs table: leading namespace column (A4 D4), configured-order rows.
  await expect(jp.locator(".jobs-table")).toBeVisible();
  await expect(jp.locator(".jobs-table-row")).toHaveCount(2);
  await expect(jp.locator(".jobs-col-ns").nth(0)).toHaveText("default");
  await expect(jp.locator(".jobs-col-ns").nth(1)).toHaveText("lab");
  // Batches: TRUE progress bars live in the tab only (A2-5).
  await expect(jp.getByRole("progressbar", { name: "dsv-transcode progress" })).toBeVisible();
  await expect(jp.getByText(/180\/250 · 5\.3\/min · ETA /)).toBeVisible();
  await expect(jp.getByText(/stale — last update/)).toBeVisible();
});

test("the in-tab namespace quick-switcher filters CLIENT-side only (zero extra IPC)", async ({ page }) => {
  await page.goto("/?k8s=ok");
  const chip = page.locator('[data-chip="jobs"]');
  await expect(chip).toHaveText(/Jobs · 2 \+ 1$/, POLL);
  await page.keyboard.press("Meta+j");
  await page.getByRole("tab", { name: /^Jobs/ }).click();
  const jp = page.locator(".jobs-panel");
  await expect(jp.locator(".jobs-table-row")).toHaveCount(2);

  const readCalls = () =>
    page.evaluate(() => {
      const fx = window.__odoFixtures;
      if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
      return fx.k8sStatusFixture.calls;
    });
  const before = await readCalls();
  await jp.getByRole("button", { name: "lab" }).click();
  await expect(jp.locator(".jobs-table-row")).toHaveCount(1);
  await expect(jp.locator(".jobs-col-ns").nth(0)).toHaveText("default");
  // Toggling the filter never widens the fetch (A4 D4).
  await page.waitForTimeout(200);
  expect(await readCalls()).toBe(before);
  await jp.getByRole("button", { name: "lab" }).click();
  await expect(jp.locator(".jobs-table-row")).toHaveCount(2);
});
