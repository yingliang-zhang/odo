import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";

// Review finding 8 (2026-08-24): context-panel tab strip overflow.
// UX-1 D2 (2026-09-01): TEN tabs now measure ~730px of strip content
// (measured live, mock fixtures) vs ~359px of strip at the default 420px
// (U2.3) panel width — ~511px of clipping at the 280px MIN, and ~71px even
// at the 720px MAX (clientWidth 659). The "every tab fits" state is
// UNREACHABLE at the CSS ceiling with ten tabs — active-tab scrollIntoView
// + ‹ › controls are the permanent resting posture.

async function openPanel(page: Page) {
  await page.goto("/");
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
  await page.getByRole("tab", { name: "Ledger", exact: true }).click();
  await expect(page.getByRole("tab", { name: "Ledger", exact: true })).toHaveAttribute(
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

test("‹ › controls persist at MAX — 10 tabs overflow the 720px ceiling, every tab stays reachable", async ({ page }) => {
  // Measured truth (UX-1 D2): ten tabs lay out ~730px wide but the widest
  // achievable strip is 720−61 = 659px (U2.1 CSS ceiling) — the fit state
  // the old test pinned no longer exists at ANY viewport, so the controls
  // must stay and reachability comes from scrollIntoView, never from fit.
  await page.setViewportSize({ width: 1440, height: 900 });
  await openPanel(page);

  await dragGripBy(page, 160); // → 280px, overflowing
  await expect.poll(() => panelWidth(page)).toBe("280px");
  await expect(page.getByRole("button", { name: "Scroll tabs right" })).toBeVisible();

  // Drag far past MAX: the grip clamps at the U2.1 CSS ceiling —
  // min(720, window − sidebar − 400) = 720px at this 1440px viewport —
  // strip width 720−61 = 659px < ~730px of tabs: controls persist.
  await dragGripBy(page, -520);
  await expect.poll(() => panelWidth(page)).toBe("720px");
  await expect(page.getByRole("button", { name: "Scroll tabs right" })).toBeVisible();

  // The rightmost tab (deepest in the overflow) is still exactly one
  // click away: real click → selected AND pulled inside the strip.
  await page.getByRole("tab", { name: "Learning", exact: true }).click();
  await expect(page.getByRole("tab", { name: "Learning", exact: true })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expectActiveTabInStrip(page);

  // …and the leftmost tab pulls back into view the same way.
  await page.getByRole("tab", { name: /^Tasks/ }).click();
  await expectActiveTabInStrip(page);
});
