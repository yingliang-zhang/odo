import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";

// Review finding 8 (2026-08-24): context-panel tab strip overflow.
// UX-4 / A3-3 (2026-09-01): the diet dropped the Ledger tab — NINE tabs
// measure ≈665px of strip content (badge-laden fixtures, measured live
// 2026-09-01) vs a 219px client at the 280px MIN (clip ≈ 446px) and
// ~359px at the 420px default — but the 720px MAX leaves the strip a
// 703px client with the controls unmounted: the "every tab fits" state
// is reachable again at the CSS ceiling and the no-arrow resting posture
// at MAX is pinned below (UX-1 D2's 10-tab, ~730px-at-MAX posture — fit
// unreachable at 659 — is archived in the epoch notes).

async function openPanel(page: Page) {
  return openPanelUrl(page, "/");
}

async function openPanelUrl(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
  // The aside slides in over ~0.2s (panel-in: translateX(10px)) and the
  // absolute-positioned grip rides with it: a grip box sampled before the
  // entrance finishes is stale by mousedown, pointerdown misses the 8px hit
  // strip, and the drag silently never engages (panel stuck at 420px). An
  // x-stability probe cannot tell "pre-animation-start" from "settled"
  // (both read two equal frames) — drain the element's animations instead:
  // the CSS animation object exists the moment style applies, so finished
  // covers the whole window.
  await page.locator(".context-panel").evaluate(async (el) => {
    await Promise.allSettled(el.getAnimations().map((a) => a.finished));
  });
}

async function dragGripBy(page: Page, dx: number) {
  const gb = await page.locator(".panel-resize").boundingBox();
  if (!gb) throw new Error("no resize grip");
  const cx = gb.x + gb.width / 2;
  await page.mouse.move(cx, 400);
  await page.mouse.down();
  await page.mouse.move(cx + dx, 400, { steps: 8 });
  await page.mouse.up();
}

function panelWidth(page: Page): Promise<string> {
  return page.evaluate(
    () => document.querySelector(".context-panel")?.style.getPropertyValue("--panel-width") ?? "",
  );
}

function scrollLeft(page: Page): Promise<number> {
  return page.evaluate(() => Math.round(document.querySelector(".panel-tabs")?.scrollLeft ?? -1));
}

// The active tab's box must lie inside the tab strip's visible viewport.
// Poll (not a single-shot read): scrollIntoView runs in a post-commit
// effect, so the first frame after a click can lag the assertion under
// machine load — the epoch-34 strip-scroll flake. The contract is "the
// strip settles with the active tab visible", not "by this microtask".
async function expectActiveTabInStrip(page: Page) {
  await expect
    .poll(async () => {
      const inside = await page.evaluate(() => {
        const tabs = document.querySelector(".panel-tabs")?.getBoundingClientRect();
        const active = document
          .querySelector('.panel-tab[aria-selected="true"]')
          ?.getBoundingClientRect();
        if (!tabs || !active) return { ok: false, reason: "missing nodes" as const };
        return { ok: active.left >= tabs.left - 1 && active.right <= tabs.right + 1 };
      });
      return inside.ok;
    })
    .toBe(true);
}

test("‹ › controls appear at 280px and move the strip; active tab stays in view", async ({ page }) => {
  await openPanel(page);

  // Drag the panel to the 280px MIN: ~511px of tabs clip without navigation.
  await dragGripBy(page, 160); // 160px past the 420px default, into the MIN clamp
  await expect.poll(() => panelWidth(page)).toBe("280px");

  const navLeft = page.getByRole("button", { name: "Scroll tabs left" });
  const navRight = page.getByRole("button", { name: "Scroll tabs right" });
  await expect(navLeft).toBeVisible();
  await expect(navRight).toBeVisible();

  // Rightmost tab: real click → selected AND visible inside the strip.
  await page.getByRole("tab", { name: "Learning", exact: true }).click();
  await expect(page.getByRole("tab", { name: "Learning", exact: true })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expectActiveTabInStrip(page);

  // ‹ moves the strip back (scrollBy clamps at the right end, so › at the
  // right end is a no-op — exercise the reverse direction instead).
  await expect.poll(() => scrollLeft(page)).toBeGreaterThan(0);
  const sl0 = await scrollLeft(page);
  await navLeft.click();
  await expect.poll(() => scrollLeft(page)).toBeLessThan(sl0);

  // Leftmost tab: real click → scrollIntoView pulls it back into view.
  await page.getByRole("tab", { name: /^Changes/ }).click();
  await expectActiveTabInStrip(page);
});

test("‹ › controls DISAPPEAR at MAX — 9 tabs fit the 720px ceiling, every tab visible at rest", async ({ page }) => {
  // Measured truth (UX-4 / A3-3): nine tabs lay out ≈665px vs the 703px
  // strip client at the 720px MAX with the controls unmounted (U2.1
  // ceiling; the 10-tab posture overflowed at ANY viewport — ~730px vs
  // the 659px with-controls client). Controls hide; reachability is the
  // resting layout, not scrollIntoView — pinned WITHOUT widening past
  // 1440px because the drag clamp rides the CSS ceiling, not the window.
  await page.setViewportSize({ width: 1440, height: 900 });
  await openPanel(page);

  await dragGripBy(page, 160); // → 280px MIN, overflowing
  await expect.poll(() => panelWidth(page)).toBe("280px");
  await expect(page.getByRole("button", { name: "Scroll tabs right" })).toBeVisible();

  // Drag far past MAX: the grip clamps at the U2.1 CSS ceiling — 720px at
  // this viewport — and the 9-tab strip fits its 659px client, so the
  // controls unmount entirely.
  await dragGripBy(page, -520);
  await expect.poll(() => panelWidth(page)).toBe("720px");
  await expect(page.getByRole("button", { name: "Scroll tabs right" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Scroll tabs left" })).toHaveCount(0);

  // Every tab is visible at rest: the rightmost needs no scroll to reach,
  // and clicking it keeps the strip settled (scrollLeft stays clamped 0).
  const learning = page.getByRole("tab", { name: "Learning", exact: true });
  await expect(learning).toBeVisible();
  await learning.click();
  await expect(learning).toHaveAttribute("aria-selected", "true");
  await expectActiveTabInStrip(page);
  await expect.poll(() => scrollLeft(page)).toBe(0);

  // …and the leftmost tab likewise sits in view at rest.
  await expect(page.getByRole("tab", { name: /^Tasks/ })).toBeVisible();
});

// ---------- D5b (A3-3's conditional 10th) ----------
// k8s off (fixture default): the static NINE tabs, jobs absent — the
// A3-3 budget holds by construction in the no-k8s posture.
// k8s on (?k8s=ok): TEN tabs render — arrows at the strip's max are the
// LOCKED accepted trade (A3-3), so this asserts COUNT and clickability,
// never the no-arrow fit.

test("k8s off keeps NINE tabs and no Jobs entry (A3-3 static budget)", async ({ page }) => {
  await openPanel(page);
  await expect(page.getByRole("tab")).toHaveCount(9);
  await expect(page.getByRole("tab", { name: /Jobs/ })).toHaveCount(0);
});

test("k8s on renders TEN tabs (arrows accepted at MAX) and the Jobs tab activates", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openPanelUrl(page, "/?k8s=ok");
  await expect(page.getByRole("tab")).toHaveCount(10);
  const jobs = page.getByRole("tab", { name: /Jobs/ });
  await expect(jobs).toBeVisible();
  await jobs.click();
  await expect(jobs).toHaveAttribute("aria-selected", "true");
  await expectActiveTabInStrip(page);
  // The Jobs panel body renders the two locked sections (table + batches).
  const jp = page.locator(".jobs-panel");
  await expect(jp.locator(".jobs-table")).toBeVisible();
  await expect(jp.getByText("batches", { exact: true })).toBeVisible();
});
