import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Repro: user sends a prompt, the run streams tool calls, and the view
// must keep following the newest output without manual scrolling.
// Samples (distFromBottom, pill) at 100ms while bursts are journaled.

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
    __samples?: Sample[];
  }
}

interface Sample {
  t: number;
  dist: number;
  pill: boolean;
}

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
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    fx.runState.foreground = v;
  }, on);
}

// Tall history so the message list overflows before the run starts.
async function fillHistory(page: Page) {
  const rows: InjectedRow[] = [];
  for (let i = 0; i < 12; i++) {
    rows.push({ type: "user_message", payload: { text: `history question ${i}` } });
    rows.push({
      type: "agent_text",
      payload: { text: `history answer ${i}\n\n${"long line of prior output that pads the bubble. ".repeat(6)}` },
    });
  }
  await journal(page, rows);
  // Let the idle poll deliver and the follow effect settle at the bottom.
  await page.waitForTimeout(2200);
}

test("auto-follow keeps the tail pinned while a run streams tool calls", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await fillHistory(page);

  // Sanity: booted and pinned to the bottom.
  const dist0 = await page.evaluate(() => {
    const el = document.querySelector(".message-list");
    if (!(el instanceof HTMLElement)) throw new Error("message-list missing");
    return el.scrollHeight - el.scrollTop - el.clientHeight;
  });
  expect(dist0).toBeLessThan(80);

  // Install the sampler before the run starts.
  await page.evaluate(() => {
    const el = document.querySelector(".message-list");
    if (!(el instanceof HTMLElement)) throw new Error("message-list missing");
    window.__samples = [];
    const t0 = performance.now();
    setInterval(() => {
      window.__samples?.push({
        t: Math.round(performance.now() - t0),
        dist: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
        pill: !!document.querySelector(".new-output-pill"),
      });
    }, 100);
  });

  // Send a real prompt through the composer.
  const textarea = page.getByPlaceholder("Describe the change you want…");
  await textarea.fill("stream a pile of tool calls");
  await textarea.press("Meta+Enter");
  await setRunning(page, true);

  // Stream bursts of tool calls roughly at poll cadence.
  for (let burst = 0; burst < 12; burst++) {
    await journal(page, [
      { type: "agent_tool_call", payload: { tool: `tool_${burst}_a`, args: { path: `src/file${burst}a.ts` } } },
      { type: "agent_tool_call", payload: { tool: `tool_${burst}_b`, args: { path: `src/file${burst}b.ts` } } },
      { type: "agent_tool_call", payload: { tool: `tool_${burst}_c`, args: { path: `src/file${burst}c.ts` } } },
    ]);
    await page.waitForTimeout(450);
  }
  await setRunning(page, false);
  await page.waitForTimeout(800);

  const samples = await page.evaluate(() => window.__samples ?? []);
  const breaks = samples.filter((s) => s.pill || s.dist > 400);
  // The contract: while pinned, the tail never drifts more than one
  // growth batch away (the next poll re-pins), and the pill never shows.
  expect(breaks).toEqual([]);

  // Final state: still at the bottom, no pill.
  const distEnd = await page.evaluate(() => {
    const el = document.querySelector(".message-list");
    if (!(el instanceof HTMLElement)) throw new Error("message-list missing");
    return el.scrollHeight - el.scrollTop - el.clientHeight;
  });
  expect(distEnd).toBeLessThan(80);
  await expect(page.locator(".new-output-pill")).toHaveCount(0);
});

test("wheel-up disengages the follow; the pill re-engages it", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await fillHistory(page);

  const list = page.locator(".message-list");
  const gap = () => list.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight);
  expect(await gap()).toBeLessThan(80);

  // Deliberate gesture: wheel up over the stream. The follow disengages.
  await list.hover();
  await page.mouse.wheel(0, -600);
  await page.waitForTimeout(300);
  expect(await gap()).toBeGreaterThan(80);

  // New output while scrolled up: pill shows, the view is NOT yanked down.
  await journal(page, [{ type: "agent_text", payload: { text: "arrived while you read" } }]);
  await page.waitForTimeout(2200);
  await expect(page.locator(".new-output-pill")).toBeVisible();
  expect(await gap()).toBeGreaterThan(80);

  // The pill re-engages: pinned again, and the next arrival follows.
  await page.locator(".new-output-pill").click();
  await expect(page.locator(".new-output-pill")).toHaveCount(0);
  expect(await gap()).toBeLessThan(80);
  await journal(page, [{ type: "agent_text", payload: { text: "post-repin arrival" } }]);
  await page.waitForTimeout(2200);
  expect(await gap()).toBeLessThan(80);
});
