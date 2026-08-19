import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// ui/message-stream follow-up: every row in the message stream shares the
// centered chat column (--chat-column-width). Regression contract for the
// report "Working... and the streaming preview render full-bleed while
// the bubbles sit inside the padded column".
//
// Viewport 1400px > 1100px column cap, so any full-bleed row is provably
// wider/misaligned (the column's left edge sits at (1400-1100)/2 plus
// chrome insets).

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
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
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.runState.foreground = v;
  }, on);
}

async function setPreview(page: Page, preview: InjectedRow | null) {
  await page.evaluate((p) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.previewState.current = p;
  }, preview);
}

function columnLeft(page: Page) {
  return page.evaluate(() => {
    const group = document.querySelector(".run-group");
    if (!(group instanceof HTMLElement)) throw new Error("run-group missing");
    return Math.round(group.getBoundingClientRect().x);
  });
}

test.beforeEach(async ({ page }) => {
  await page.setViewportSize({ width: 1400, height: 900 });
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await journal(page, [
    { type: "user_message", payload: { text: "start a run" } },
    { type: "agent_tool_call", payload: { tool: "read_file", args: { path: "src/a.ts" } } },
  ]);
  await setRunning(page, true);
  await setPreview(page, null);
  await page.waitForTimeout(2200); // one poll delivers the journaled rows
});

test.afterEach(async ({ page }) => {
  await setRunning(page, false);
  await setPreview(page, null);
});

test("live tool ticker shares the chat column's left edge", async ({ page }) => {
  await expect(page.locator(".tool-ticker")).toBeVisible();
  const groupX = await columnLeft(page);
  const tickerX = await page.evaluate(() => {
    const el = document.querySelector(".tool-ticker");
    if (!(el instanceof HTMLElement)) throw new Error("tool-ticker missing");
    return Math.round(el.getBoundingClientRect().x);
  });
  expect(tickerX).toBe(groupX);
});

test("streaming text preview shares the chat column's left edge", async ({ page }) => {
  await setPreview(page, { type: "agent_text", payload: { text: "streaming partial block…" } });
  await expect(page.locator(".bubble-preview")).toBeVisible();
  const groupX = await columnLeft(page);
  const previewX = await page.evaluate(() => {
    const el = document.querySelector(".bubble-preview");
    if (!(el instanceof HTMLElement)) throw new Error("preview missing");
    return Math.round(el.getBoundingClientRect().x);
  });
  expect(previewX).toBe(groupX);
});

test("streaming tool preview shares the chat column's left edge", async ({ page }) => {
  await setPreview(page, { type: "agent_tool_call", payload: { tool: "write_file", intent: "apply fix" } });
  await expect(page.locator(".bubble-preview")).toBeVisible();
  const groupX = await columnLeft(page);
  const previewX = await page.evaluate(() => {
    const el = document.querySelector(".bubble-preview");
    if (!(el instanceof HTMLElement)) throw new Error("preview missing");
    return Math.round(el.getBoundingClientRect().x);
  });
  expect(previewX).toBe(groupX);
});
