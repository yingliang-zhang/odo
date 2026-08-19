import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Steering composer must survive stream churn. Regression contract for the
// report "steering text typed during a run gets washed away by subsequent
// updates": with a controlled textarea, React 19 ignores mid-composition
// input events, so the next re-render (350ms polls + the 1s run heartbeat)
// wrote the stale draft back and aborted the CJK IME session. The composer
// is uncontrolled now; both engine event models are pinned here.

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

const STEER_PH = "Steer the running agent… (Esc stops)";

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await setRunning(page, true);
  await journal(page, [
    { type: "agent_tool_call", payload: { tool: "read_file", args: { path: "src/a.ts" } } },
  ]);
  await page.waitForTimeout(2200); // one poll: running state + ticker in view
});

test.afterEach(async ({ page }) => {
  await setRunning(page, false);
});

test("ordinary typing survives stream churn while a run is live", async ({ page }) => {
  const ta = page.getByPlaceholder(STEER_PH);
  await ta.click();
  for (const ch of ["h", "e", "l", "l", "o"]) {
    await page.keyboard.type(ch);
    await journal(page, [
      { type: "agent_tool_call", payload: { tool: `t_${ch}`, args: { path: "x" } } },
    ]);
    await page.waitForTimeout(450); // crosses ~1 poll; heartbeat fires every 1s
  }
  await page.waitForTimeout(1200); // at least one full heartbeat
  await expect(ta).toHaveValue("hello");
});

test("composition without input events (WKWebView model) survives stream churn", async ({ page }) => {
  const ta = page.getByPlaceholder(STEER_PH);
  await ta.click();

  // Engine model: composition text lands in node.value but NO `input`
  // event reaches React mid-composition, so state trails the DOM.
  await page.evaluate(() => {
    const el = document.querySelector<HTMLTextAreaElement>("textarea");
    if (!el) throw new Error("no textarea");
    el.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "" }));
    el.value = "ni";
    el.dispatchEvent(new CompositionEvent("compositionupdate", { bubbles: true, data: "ni" }));
  });
  await journal(page, [
    { type: "agent_tool_call", payload: { tool: "t_x", args: { path: "x" } } },
  ]);
  await page.waitForTimeout(1400); // ≥1 poll + heartbeat
  await expect(ta).toHaveValue("ni");

  // Committing the composition syncs the draft…
  await page.evaluate(() => {
    const el = document.querySelector<HTMLTextAreaElement>("textarea");
    if (!el) throw new Error("no textarea");
    el.value = "你好";
    el.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "你好" }));
  });
  await journal(page, [
    { type: "agent_tool_call", payload: { tool: "t_y", args: { path: "y" } } },
  ]);
  await page.waitForTimeout(1400);
  await expect(ta).toHaveValue("你好");

  // …and Enter sends it, clearing the box (uncontrolled value reaches the
  // DOM through the send path, not a React render).
  await ta.press("Enter");
  await expect(ta).toHaveValue("");
});

test("a submit racing a stuck composition still empties the box (blue-marked-text regression)", async ({ page }) => {
  // Regression contract for "the input box stays blue, like a permanent
  // select-all" (2026-08-19): a WKWebView IME session can deliver the
  // confirming Enter keydown WITHOUT isComposing/keyCode 229, so the
  // submit fires mid-composition; compositionend then never arrives, the
  // composingRef guard blocks every value-sync, and the webview's marked
  // (blue) text is stranded in the DOM. The send path must force-clear
  // the node — the visible contract is simply "box empty after send".
  const ta = page.getByPlaceholder(STEER_PH);
  await ta.click();
  await ta.pressSequentially("aa");
  await page.evaluate(() => {
    const el = document.querySelector<HTMLTextAreaElement>("textarea");
    if (!el) throw new Error("no textarea");
    el.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "" }));
  });
  await ta.press("Enter"); // synthetic: isComposing=false — the quirk's shape
  await expect(ta).toHaveValue("");
});

test("composition with input events (React 19 compositionend model) survives stream churn", async ({ page }) => {
  const ta = page.getByPlaceholder(STEER_PH);
  await ta.click();

  await page.evaluate(() => {
    const el = document.querySelector<HTMLTextAreaElement>("textarea");
    if (!el) throw new Error("no textarea");
    el.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "" }));
    el.value = "ni";
    el.dispatchEvent(
      new InputEvent("input", {
        bubbles: true,
        data: "ni",
        inputType: "insertCompositionText",
        isComposing: true,
      }),
    );
  });
  await journal(page, [
    { type: "agent_tool_call", payload: { tool: "t_x", args: { path: "x" } } },
  ]);
  await page.waitForTimeout(1400);
  await expect(ta).toHaveValue("ni");
});
