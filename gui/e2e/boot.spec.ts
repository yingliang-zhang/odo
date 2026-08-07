import { test, expect } from "@playwright/test";

// Boot: app loads, sidebar tree visible, workstreams listed, status bar present.
// Uses mock-invoke auto-engaged in plain browser (no __TAURI_INTERNALS__).

test("app boots with sidebar tree and workstreams", async ({ page }) => {
  await page.goto("/");

  // Sidebar app title shows active project name
  await expect(page.locator(".sidebar-app")).toHaveText("odo");

  // Projects section heading
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();

  // Active project (odo) is expanded — its workstreams visible in the tree
  const sidebar = page.locator(".sidebar");
  await expect(sidebar.locator(".proj-row-active")).toContainText("odo");
  await expect(sidebar.getByText("main", { exact: true }).first()).toBeVisible();
  await expect(sidebar.getByText("feat-sidebar-tree")).toBeVisible();
  await expect(sidebar.getByText("fix-daemon-binary")).toBeVisible();

  // Second project exists
  await expect(sidebar.getByText("supersplat-hdr")).toBeVisible();

  // Add project button exists
  await expect(page.getByText("Add project")).toBeVisible();

  // Status bar at bottom shows workstream info
  await expect(page.locator(".app-statusbar")).toContainText("main");
});

test("app boots with chat surface and composer", async ({ page }) => {
  await page.goto("/");

  // Composer textarea visible
  await expect(page.getByPlaceholder("Describe the change you want…")).toBeVisible();

  // Send button exists
  await expect(page.getByRole("button", { name: "Send" })).toBeVisible();

  // Keyboard hint visible
  await expect(page.locator("text=⌘↵ send")).toBeVisible();
});

test("app boots with TopBar actions", async ({ page }) => {
  await page.goto("/");

  // TopBar buttons visible — use text content (aria-label is the full tooltip)
  await expect(page.locator(".topbar-action", { hasText: "Distill" })).toBeVisible();
  await expect(page.locator(".topbar-action", { hasText: "Wiki" })).toBeVisible();
  await expect(page.locator(".topbar-action", { hasText: "Curate" })).toBeVisible();
  await expect(page.locator(".topbar-action", { hasText: "Pin" })).toBeVisible();
  await expect(page.locator(".topbar-action", { hasText: "Ledger" })).toBeVisible();
  await expect(page.locator(".topbar-action", { hasText: "Settings" })).toBeVisible();
});
