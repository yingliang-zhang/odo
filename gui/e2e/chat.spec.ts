import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";
import { SLOT, slotSel } from "../src/slots";

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

// Chat: send message, user bubble appears, composer clears after send.
const POLL = { timeout: 10_000 };

// Push journaled rows through the mock's afterSeq poll tail (loop.spec
// injection contract) — conv 1 by default.
type JournalRow = { type: Parameters<typeof fixtures.ev>[0]; payload: Record<string, unknown>; conv?: number };
async function journal(page: Page, rows: JournalRow[]) {
  await page.evaluate((r: JournalRow[]) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    for (const row of r) {
      const e = fx.ev(row.type, row.payload, row.conv ?? 1);
      e.created_at = new Date().toISOString();
      fx.events.push(e);
    }
  }, rows);
}

const SAMPLE_DIFF = [
  "diff --git a/src/alpha.ts b/src/alpha.ts",
  "index 1a1..2b2 100644",
  "--- a/src/alpha.ts",
  "+++ b/src/alpha.ts",
  "@@ -1,2 +1,2 @@",
  " keep();",
  "-oldCall();",
  "+newCall();",
  "diff --git a/src/beta.ts b/src/beta.ts",
  "index 3c3..4d4 100644",
  "--- a/src/beta.ts",
  "+++ b/src/beta.ts",
  "@@ -4,1 +4,2 @@",
  " tail();",
  "+added();",
].join("\n");

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("send message creates user bubble", async ({ page }) => {
  const textarea = page.getByPlaceholder("Describe the change you want…");
  const sendBtn = page.getByRole("button", { name: "Send", exact: true });

  // Send button is disabled when textarea is empty
  await expect(sendBtn).toBeDisabled();

  // Type a message
  await textarea.fill("Add a GFM table renderer");
  await expect(sendBtn).toBeEnabled();

  // Count existing user bubbles
  const beforeCount = await page.locator(".bubble-user").count();

  // Send via ⌘+Enter
  await textarea.press("Meta+Enter");

  // A new user bubble with the message text appears
  await expect(page.locator(".bubble-user").last()).toContainText("Add a GFM table renderer");
  await expect(page.locator(".bubble-user")).toHaveCount(beforeCount + 1);

  // Textarea cleared after send
  await expect(textarea).toHaveValue("");
});

test("send message via Send button click", async ({ page }) => {
  const textarea = page.getByPlaceholder("Describe the change you want…");
  const sendBtn = page.getByRole("button", { name: "Send", exact: true });

  await textarea.fill("Fix the alignment bug");
  await sendBtn.click();

  await expect(page.locator(".bubble-user").last()).toContainText("Fix the alignment bug");
});

// Conversation 1 fixtures carry one lone call (Markdown.tsx read) and one
// two-call burst ("Run the checks and report"): a lone call renders inline
// while a burst folds behind a summary naming the last call.
test("lone tool call renders inline; burst folds with named summary", async ({ page }) => {
  await expect(page.locator(".tool-call-line", { hasText: "read_file" }).first()).toBeVisible();
  // `.tool-group > summary` — direct child only: expanded tool-result
  // bubbles carry their own <summary> descendants inside the group.
  await expect(page.locator(".tool-group > summary", { hasText: "1 tool call" })).toHaveCount(0);

  const groupSummary = page.locator(".tool-group > summary");
  await expect(groupSummary).toHaveCount(1);
  await expect(groupSummary).toContainText("2 tool calls");
  await expect(groupSummary).toContainText("read_file(path: gui/src/App.tsx)");
});

// P1.4 (docs/design/adoption-lock.md): a tool result whose body is a
// unified diff renders compact read-only hunks (DiffViewer parser), and
// the run-group header's "N files changed" chip opens the Changes tab.

test("diff result renders hunks; files chip pivots to the Changes tab", async ({ page }) => {
  // A fresh run whose run_command result IS a unified diff (convs-local,
  // no fixture churn).
  await journal(page, [
    { type: "user_message", payload: { text: "Show the workspace diff" } },
    { type: "agent_tool_call", payload: { tool: "run_command", args: { cmd: "git diff" } } },
    { type: "agent_tool_result", payload: { tool: "run_command", result: SAMPLE_DIFF } },
    { type: "agent_done", payload: { summary: "Diff reported" } },
  ]);

  // Hunks stay behind the result's <details>; opening them shows one
  // compact card per touched file, provenance counts included.
  await expect(page.locator(".bubble-tool details summary", { hasText: "run_command" }).last()).toBeVisible(POLL);
  await page.locator(".bubble-tool details summary", { hasText: "run_command" }).last().click();
  const cards = page.locator(".tool-diff-file");
  await expect(cards).toHaveCount(2);
  await expect(cards.first()).toContainText("src/alpha.ts");
  await expect(cards.first()).toContainText("+1 −1");
  await expect(page.locator(".run-files-chip")).toContainText("2 files changed");

  await page.locator(".run-files-chip").click();
  // Changes tab preselected; accept/reject stays the real DiffViewer's job.
  await expect(page.locator(slotSel(SLOT.panelTabs)).getByRole("tab", { name: /Changes/ })).toHaveAttribute(
    "aria-selected",
    "true",
  );
});

test("Shift+Enter inserts newline", async ({ page }) => {
  const textarea = page.getByPlaceholder("Describe the change you want…");

  await textarea.fill("Line 1");
  await textarea.press("Shift+Enter");
  await textarea.type("Line 2");

  // Textarea contains newline
  const value = await textarea.inputValue();
  expect(value).toContain("Line 1");
  expect(value).toContain("Line 2");
  expect(value).toContain("\n");
});
