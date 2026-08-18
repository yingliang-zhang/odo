import { test, expect } from "@playwright/test";

// Boot: app loads, sidebar tree visible, workstreams listed, status bar present.
// Uses mock-invoke auto-engaged in plain browser (no __TAURI_INTERNALS__).

test("app boots with sidebar tree and workstreams", async ({ page }) => {
  await page.goto("/");

  // Sidebar Projects section heading is visible
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();

  // Active project (odo) is expanded — its workstreams visible in the tree
  const sidebar = page.locator(".sidebar");
  await expect(sidebar.locator(".proj-row-active")).toContainText("odo");
  await expect(sidebar.getByText("main", { exact: true }).first()).toBeVisible();
  await expect(sidebar.getByText("feat-sidebar-tree")).toBeVisible();
  await expect(sidebar.getByText("fix-daemon-binary")).toBeVisible();

  // Second project exists
  await expect(sidebar.getByText("supersplat-hdr")).toBeVisible();

  // New project button exists in section header
  await expect(page.getByText("New", { exact: true })).toBeVisible();

  // Status bar at bottom shows workstream info
  await expect(page.locator(".app-statusbar")).toContainText("main");
});

test("app boots with chat surface and composer", async ({ page }) => {
  await page.goto("/");

  // Composer textarea visible
  await expect(page.getByPlaceholder("Describe the change you want…")).toBeVisible();

  // Send button exists (exact match — "Edit and resend" buttons also contain "Send")
  await expect(page.getByRole("button", { name: "Send", exact: true })).toBeVisible();

  // Keyboard hint visible
  await expect(page.locator("text=⌘↵ send")).toBeVisible();
});

// PR3: TopBar decluttered to Distill (labeled) + ⋯ overflow + Settings (gear).
// Wiki/Curate/Pin/Ledger moved to ⋯ overflow menu; Wiki/Ledger also in panel tabs.
test("app boots with TopBar actions", async ({ page }) => {
  await page.goto("/");

  // Distill — primary visible action (labeled)
  await expect(page.locator(".topbar-action", { hasText: "Distill" })).toBeVisible();

  // ⋯ overflow button visible (aria-label="More actions")
  await expect(page.locator(".topbar-action[aria-label='More actions']")).toBeVisible();

  // Settings — gear icon (aria-label="Settings (⌘,)")
  await expect(page.locator(".topbar-action[aria-label='Settings (⌘,)']")).toBeVisible();

  // PR3: overflow menu opens and shows Curate/Pin/Wiki/Ledger
  await page.locator(".topbar-action[aria-label='More actions']").click();
  await expect(page.locator(".topbar-overflow-menu")).toBeVisible();
  await expect(page.locator(".topbar-overflow-item", { hasText: "Curate" })).toBeVisible();
  await expect(page.locator(".topbar-overflow-item", { hasText: "Pin" })).toBeVisible();
  await expect(page.locator(".topbar-overflow-item", { hasText: "Wiki" })).toBeVisible();
  await expect(page.locator(".topbar-overflow-item", { hasText: "Ledger" })).toBeVisible();
});
