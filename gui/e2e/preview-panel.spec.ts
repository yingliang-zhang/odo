import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// P2.1 (docs/design/adoption-lock.md): three preview tiers —
//   1. image attachments/tool-result refs render inline (data-slot=preview-image)
//      when read_file serves bytes (the mock arms the forward-compat
//      file_data_base64 field), chip fallback otherwise;
//   2. read_file-style tool args open a Preview tab file pane (daemon read_file,
//      syntax-highlighted);
//   3. localhost URLs in tool results get an Open-live affordance loading a
//      sandboxed iframe (sandbox="allow-scripts", localhost-only lock).
// Selectors come from slots.ts (P1.2 contract).

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

const POLL = { timeout: 4000 };

async function addRows(page: Page, rows: { type: string; payload: Record<string, unknown> }[]) {
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

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("inline image: attachment chip renders the PNG when bytes are served", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.previewFiles["reports/shot.png"] = { dataBase64: fx.TINY_PNG_BASE64, mime: "image/png" };
  });
  await addRows(page, [
    { type: "user_message", payload: { text: "/preview http://localhost:3000/ — how does it look?", attachments: ["reports/shot.png"] } },
  ]);
  // ZoomableImage wrapper carries the slot after the read resolves.
  await expect(page.locator('[data-slot="preview-image"] img')).toBeVisible(POLL);
  await expect(page.locator('[data-slot="preview-image"] img')).toHaveAttribute("src", /^data:image\/png;base64,iVBOR/);
});

test("Open live: localhost URL in a tool result opens the sandboxed frame pane", async ({ page }) => {
  // Daemon wire shape: a call precedes its result. The pair is a 1-call
  // burst so ChatSurface renders it INLINE — a bare orphan result (0-call
  // burst) folds into the collapsed "0 tool calls" <details> and its own
  // summary never becomes actionable.
  await addRows(page, [
    { type: "agent_tool_call", payload: { tool: "run_command", args: JSON.stringify({ cmd: "npm run dev" }) } },
    {
      type: "agent_tool_result",
      payload: { tool: "run_command", result: "vite dev server ready\nServing on http://localhost:5173/app\n2 files changed" },
    },
  ]);
  // The Open-live affordance lives inside the result's <details>.
  const resultCard = page.locator(".bubble-tool", { hasText: "run_command" }).last();
  // Playwright pins a click to its first DOM resolution; before the poll
  // ingests this pair, .last() is the fixture's folded tool-group card —
  // an invisible dead end. Wait for THIS card's summary first.
  await expect(resultCard.locator("summary")).toBeVisible(POLL);
  await resultCard.locator("summary").click();
  const openLive = resultCard.locator('[data-slot="preview-live"]').first();
  await expect(openLive).toBeVisible(POLL);
  await openLive.click();

  const panel = page.locator(".context-panel");
  await expect(panel).toBeVisible();
  await expect(panel.locator('[role="tab"][aria-selected="true"]', { hasText: "Preview" })).toBeVisible();
  const frame = panel.locator('[data-slot="preview-frame"]');
  await expect(frame).toBeVisible();
  await expect(frame).toHaveAttribute("sandbox", "allow-scripts");
  await expect(frame).toHaveAttribute("src", "http://localhost:5173/app");
});

test("file arg: read_file tool call opens the Preview tab file pane with highlighting", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    // First keyword must be "const" (the assertion below): "export" is a ts
    // keyword too and would claim the .first() slot.
    fx.previewFiles["src/tokens.ts"] = { content: "const seed = 42;\n" };
  });
  await addRows(page, [
    { type: "agent_tool_call", payload: { tool: "read_file", args: JSON.stringify({ path: "src/tokens.ts" }) } },
    { type: "agent_tool_result", payload: { tool: "read_file", result: "1 lines" } },
  ]);
  const previewBtn = page.locator(".tool-arg-preview", { hasText: "src/tokens.ts" }).first();
  await expect(previewBtn).toBeVisible(POLL);
  await previewBtn.click();

  const panel = page.locator(".context-panel");
  await expect(panel.locator('[role="tab"][aria-selected="true"]', { hasText: "Preview" })).toBeVisible();
  await expect(panel.locator(".preview-head-path")).toContainText("src/tokens.ts");
  // ts highlighting: the const keyword token proves the tokenizer ran.
  await expect(panel.locator(".preview-code .tok-keyword").first()).toHaveText("const");
});

test("non-local URLs never get an Open-live affordance (frame-src lock)", async ({ page }) => {
  // Same wire-shape rationale as the Open-live test: call+result pair.
  await addRows(page, [
    { type: "agent_tool_call", payload: { tool: "fetch_page", args: JSON.stringify({ url: "https://example.com/docs" }) } },
    { type: "agent_tool_result", payload: { tool: "fetch_page", result: "see https://example.com/docs for the API" } },
  ]);
  const resultCard = page.locator(".bubble-tool", { hasText: "fetch_page" }).last();
  // Same first-resolution pin as the Open-live test above.
  await expect(resultCard.locator("summary")).toBeVisible(POLL);
  await resultCard.locator("summary").click();
  await expect(resultCard.locator('[data-slot="preview-live"]')).toHaveCount(0);
});

// Odo DX wave (Feature 2): the focus-hint banner — with a run live
// (fx.runState.foreground arms the poll's agent_running) the run starter's
// goal renders as the dismissible amber bar above the preview body.
test("focus hint renders the active run's goal and dismisses per goal", async ({ page }) => {
  
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.events.push(fx.ev("user_message", { text: "stabilize the sidebar e2e suite before landing" }, 1));
    fx.runState.foreground = true;
  });
  await page.keyboard.press("Meta+j");
  await page.locator('.context-panel [role="tab"]', { hasText: "Preview" }).click();

  const banner = page.locator('[data-slot="preview-focus-hint"]');
  await expect(banner).toBeVisible({ timeout: 4000 });
  await expect(banner).toContainText("Focus: stabilize the sidebar e2e suite before landing");
  // Zero clutter for the body — no target, so the placeholder copy stays.
  await expect(page.locator(".context-panel")).toContainText("Nothing to preview");

  await banner.locator("button").click();
  await expect(banner).toHaveCount(0);
});
