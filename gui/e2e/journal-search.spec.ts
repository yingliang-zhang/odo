import { test, expect } from "@playwright/test";
import { SLOT, slotSel } from "../src/slots";

// P1.1 (docs/design/adoption-lock.md): ⌘K typed-only "Journal search"
// group. "stale last_seq" appears in exactly one fixture payload — a conv-3
// (fix-daemon-binary) agent_text row — so the first row is deterministic:
// Enter must one-flight switch the sidebar onto fix-daemon-binary and open
// ⌘F prefilled with the typed query (never the row's own text).

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("typed journal group: provenance row → Enter switches workstream and opens ⌘F prefilled", async ({ page }) => {
  await page.keyboard.press("Meta+k");
  await expect(page.locator(slotSel(SLOT.palette))).toBeVisible();

  await page.locator(".palette-input").fill("stale last_seq");
  const group = page.locator(".palette-journal-group");
  const row = page.locator(".palette-journal-row");
  await expect(group).toBeVisible();
  await expect(row).toHaveCount(1);
  // Rows name the owning project · workstream · event type, plus a snippet.
  await expect(row).toContainText("odo · fix-daemon-binary · agent_text");
  await expect(row).toContainText("stale last_seq");

  await page.keyboard.press("Enter");

  // One-flight switch: fix-daemon-binary becomes the active workstream…
  const ws = page.locator(".sidebar .ws-item", { hasText: "fix-daemon-binary" });
  await expect(ws).toHaveClass(/active/, { timeout: 5000 });
  // …and the in-conversation search opens, prefilled with the typed query.
  const findBar = page.getByLabel("Find in conversation");
  await expect(findBar).toBeVisible();
  await expect(findBar).toHaveValue("stale last_seq");
  // The hit's own text is highlighted in the switched transcript.
  await expect(page.locator(".bubble-agent", { hasText: "stale last_seq" })).toBeVisible();
});

test("under 2 chars the journal group never renders", async ({ page }) => {
  await page.keyboard.press("Meta+k");
  await page.locator(".palette-input").fill("s");
  await expect(page.locator(".palette-journal-group")).toHaveCount(0);
});

test("read-only: a query with no journal match shows an empty state, never an error", async ({ page }) => {
  await page.keyboard.press("Meta+k");
  await page.locator(".palette-input").fill("zzz-no-such-token");
  await expect(page.locator(".palette-journal-group")).toContainText("No journal matches");
  await expect(page.locator(".error-banner")).toHaveCount(0);
});
