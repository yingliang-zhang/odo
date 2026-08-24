import { test, expect, type Page } from "@playwright/test";

// P1a: the cross-workstation pending-review inbox — one aggregate Review tab
// listing every pending diff across workstreams, grouped by workstream name,
// with accept/reject by diffID (works on non-active workstreams without a
// switch). Fixture baseline: diff 1 on main, diff 2 on feat-sidebar-tree
// (gui/src/dev/fixtures.inboxDiffs keeps rows and pending_counts in step).

// Badge refreshes ride pending_counts' every-4th-tick cadence (~6s idle),
// so pill/badge assertions use the same REFRESH window as background-runs.
const REFRESH = { timeout: 12_000 };

// Row-removal assertions MUST be scoped to .review-inbox: under the
// keep-alive panel tabs (tri-review P1 #5) the hidden Changes tab stays
// mounted, and per daemon semantics (store.ListDiffs has no status filter)
// it keeps rendering resolved diffs as record cards titled "Diff #N".
// A page-global getByText("Diff #N") therefore matches the hidden record
// card forever and toHaveCount(0) can never pass — deterministic, not
// load (verify block journal seq 11484: locator stuck at 1 for the full
// 5s after the inbox row was already gone).

async function openReviewTab(page: Page) {
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
  await page.getByRole("tab", { name: /Review/ }).click();
  await expect(page.locator(".review-inbox")).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("review tab lists pending diffs grouped by workstream", async ({ page }) => {
  await openReviewTab(page);

  const inbox = page.locator(".review-inbox");
  const groups = inbox.locator(".inbox-group-head");
  await expect(groups).toHaveCount(2);
  // Sidebar order: main first, then feat-sidebar-tree.
  await expect(groups.nth(0)).toContainText("main");
  await expect(groups.nth(1)).toContainText("feat-sidebar-tree");

  // One collapsed row per diff, each with a preview and action buttons.
  await expect(inbox.getByText("Diff #1")).toBeVisible();
  await expect(inbox.getByText("Diff #2")).toBeVisible();
  await expect(inbox.locator(".inbox-preview").nth(0)).toContainText("Markdown.tsx");
  await expect(inbox.locator(".inbox-preview").nth(1)).toContainText("Cross-workstream change");
  await expect(page.getByRole("button", { name: "Accept diff 1" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Reject diff 2" })).toBeVisible();

  // The tab badge is the project-wide pending total (1 + 1).
  await expect(
    page.getByRole("tab", { name: /Review/ }).locator(".panel-tab-badge"),
  ).toHaveText("2", REFRESH);
});

test("accept a non-active workstream's diff without switching", async ({ page }) => {
  await openReviewTab(page);
  // Foreground is main; diff 2 belongs to feat-sidebar-tree.
  await expect(page.locator(".app-statusbar")).toContainText("main");

  await page.getByRole("button", { name: "Accept diff 2" }).click();

  // Row resolves optimistically; the other workstream's row is untouched.
  await expect(page.locator(".review-inbox").getByText("Diff #2")).toHaveCount(0);
  await expect(page.locator(".review-inbox").getByText("Diff #1")).toBeVisible();

  // The sidebar pill on feat-sidebar-tree decrements 1 → hidden.
  const featRow = page.locator(".sidebar .ws-row", { hasText: "feat-sidebar-tree" });
  await expect(featRow.locator(".ws-pending-pill")).toHaveCount(0, REFRESH);

  // No workstream switch happened.
  await expect(page.locator(".app-statusbar")).toContainText("main");
  await expect(page.locator(".app-statusbar")).not.toContainText("feat-sidebar-tree");
});

test("reject path mirrors accept: row removed, pill decrements", async ({ page }) => {
  await openReviewTab(page);
  const mainRow = page.locator(".sidebar .ws-row", { hasText: "main" });
  await expect(mainRow.locator(".ws-pending-pill")).toHaveText("1", REFRESH);

  await page.getByRole("button", { name: "Reject diff 1" }).click();

  // main's row is gone; feat-sidebar-tree's row survives.
  await expect(page.locator(".review-inbox").getByText("Diff #1")).toHaveCount(0);
  await expect(page.locator(".review-inbox").getByText("Diff #2")).toBeVisible();
  await expect(mainRow.locator(".ws-pending-pill")).toHaveCount(0, REFRESH);
  // The review badge is now the surviving row alone.
  await expect(
    page.getByRole("tab", { name: /Review/ }).locator(".panel-tab-badge"),
  ).toHaveText("1", REFRESH);
});

test("resolving the final row shows the empty state", async ({ page }) => {
  await openReviewTab(page);

  await page.getByRole("button", { name: "Accept diff 1" }).click();
  await page.getByRole("button", { name: "Reject diff 2" }).click();

  // Inbox collapses to the project-wide empty copy; badge disappears.
  await expect(page.locator(".context-panel .panel-empty")).toContainText(
    "No pending diffs across workstreams",
  );
  await expect(
    page.getByRole("tab", { name: /Review/ }).locator(".panel-tab-badge"),
  ).toHaveCount(0, REFRESH);
});
