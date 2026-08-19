import { test, expect } from "@playwright/test";

// Diff: accept/reject the pending diff that fixtures seed for conversation 1.
// The bootstrapped "main" workstream (ws.id=1) has a pending diff (fixtures.ts:110).

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("pending diff is visible in context panel", async ({ page }) => {
  // Open the context panel (closed by default in fresh context)
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();

  // The Changes tab should show the pending diff
  // Fixtures seed a diff with path "gui/src/components/Sidebar.tsx"
  await expect(page.locator(".diff-card")).toBeVisible();
});

test("accept diff applies it and shows badge", async ({ page }) => {
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".diff-card")).toBeVisible();

  // Accept is a two-step flow (DiffViewer "Tri-model right sidebar gap"): the
  // header button opens an inline editor for the commit message; accepting it
  // (button or Enter) fires accept_diff. Code intent is badge-on-resolve —
  // the resolved card persists as a record card rendering "badge badge-accept"
  // + "Applied" (DiffViewer.tsx; ui/badge.tsx documents those classes as the
  // e2e hooks) — so this test walks the full accept flow and asserts the badge.
  await page.locator(".diff-header .btn-accept").click();
  const editor = page.locator(".diff-commit-editor");
  await expect(editor).toBeVisible();
  // Editor is prefilled with the daemon default; user can edit before it lands.
  await expect(editor.locator(".diff-commit-input")).toHaveValue("odo: accept diff #1");
  await editor.locator(".btn-accept").click();

  // After accept, the diff card should show "Applied" badge
  await expect(page.locator(".badge-accept")).toBeVisible();
});

test("reject diff shows badge", async ({ page }) => {
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".diff-card")).toBeVisible();

  // Click Reject
  await page.locator(".btn-reject").click();

  // After reject, the diff card should show "Rejected" badge
  await expect(page.locator(".badge-reject")).toBeVisible();
});

test("review button triggers tri-model review", async ({ page }) => {
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".diff-card")).toBeVisible();

  // Click Review
  await page.locator(".btn-review").click();

  // Review results should appear with reviewer verdicts
  await expect(page.locator(".review-results")).toBeVisible();
  await expect(page.locator(".review-item")).toHaveCount(2);
});
