import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";

// Review finding 8 (2026-08-24): context-panel tab strip overflow.
// Six tabs total ~457px vs 363px at the default 380px panel width (and 194px
// of clipping at the 280px MIN) — active-tab scrollIntoView + ‹ › controls.

async function openPanel(page: Page) {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
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

  // Drag the panel to the 280px MIN: 194px of tabs clip without navigation.
  await dragGripBy(page, 120);
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

test("‹ › controls disappear once every tab fits", async ({ page }) => {
  await openPanel(page);

  await dragGripBy(page, 120); // → 280px, overflowing
  await expect.poll(() => panelWidth(page)).toBe("280px");
  await expect(page.getByRole("button", { name: "Scroll tabs right" })).toBeVisible();

  // Drag far past MAX: the grip clamps at 600px, where all tabs fit.
  await dragGripBy(page, -340);
  await expect.poll(() => panelWidth(page)).toBe("600px");
  await expect(page.getByRole("button", { name: "Scroll tabs left" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Scroll tabs right" })).toHaveCount(0);
  await expectActiveTabInStrip(page);
});
