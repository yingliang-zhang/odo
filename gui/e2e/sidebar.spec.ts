import { test, expect } from "@playwright/test";

// Sidebar: project tree expand/collapse, workstream switch, create, rename, delete,
// project switch (without collapsing tree state).

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("expand/collapse active project", async ({ page }) => {
  const sidebar = page.locator(".sidebar");

  // odo is expanded by default — workstreams visible
  await expect(sidebar.getByText("main", { exact: true }).first()).toBeVisible();

  // Click odo project row to collapse (the first .proj-row button)
  const odoRow = sidebar.locator(".proj-row-active");
  await odoRow.click();

  // Workstreams should be hidden (no .ws-list visible under odo)
  await expect(sidebar.getByText("feat-sidebar-tree")).toBeHidden();

  // Click again to expand
  await odoRow.click();
  await expect(sidebar.getByText("feat-sidebar-tree")).toBeVisible();
});

test("switch workstream updates status bar", async ({ page }) => {
  const sidebar = page.locator(".sidebar");

  // Click feat-sidebar-tree workstream (use .ws-item to narrow)
  await sidebar.locator(".ws-item", { hasText: "feat-sidebar-tree" }).click();

  // Status bar should show new workstream name
  await expect(page.locator(".app-statusbar")).toContainText("feat-sidebar-tree");
});

test("create new workstream via + button", async ({ page }) => {
  const sidebar = page.locator(".sidebar");

  // Click "+ New workstream"
  await sidebar.getByText("+ New workstream").click();

  // Input field appears
  const input = sidebar.locator(".ws-create input");
  await expect(input).toBeVisible();

  // Type name and submit
  await input.fill("test-e2e-workstream");
  await input.press("Enter");

  // New workstream appears in sidebar
  await expect(sidebar.locator(".ws-item", { hasText: "test-e2e-workstream" })).toBeVisible();
});

test("rename workstream via hover action", async ({ page }) => {
  const sidebar = page.locator(".sidebar");

  // Hover over a workstream row to reveal actions — target the first ws-row
  const wsRow = sidebar.locator(".ws-row").first();
  await wsRow.hover();

  // Click rename button (has aria-label starting with "Rename")
  await wsRow.getByRole("button", { name: /Rename/ }).click();

  // Rename input appears with current name
  const input = page.locator(".ws-rename-input");
  await expect(input).toBeVisible();
  await expect(input).toHaveValue("main");

  // Type new name and submit
  await input.fill("main-renamed");
  await input.press("Enter");

  // New name visible in sidebar (use .ws-item to avoid matching action buttons)
  await expect(sidebar.locator(".ws-item", { hasText: "main-renamed" })).toBeVisible();
});

test("delete workstream via hover action", async ({ page }) => {
  const sidebar = page.locator(".sidebar");

  // First create a workstream to delete (avoid deleting fixtures)
  await sidebar.getByText("+ New workstream").click();
  const input = sidebar.locator(".ws-create input");
  await input.fill("to-delete");
  await input.press("Enter");
  await expect(sidebar.locator(".ws-item", { hasText: "to-delete" })).toBeVisible();

  // Hover and click delete
  const wsRow = sidebar.locator(".ws-row", { hasText: "to-delete" });
  await wsRow.hover();

  // Set up dialog handler before clicking delete
  page.once("dialog", (d) => d.accept());
  await wsRow.getByRole("button", { name: /Delete/ }).click();

  // Workstream removed
  await expect(sidebar.locator(".ws-item", { hasText: "to-delete" })).toBeHidden();
});

test("switch to non-active project does not collapse tree", async ({ page }) => {
  const sidebar = page.locator(".sidebar");

  // odo workstreams are visible
  await expect(sidebar.getByText("feat-sidebar-tree")).toBeVisible();

  // Click supersplat-hdr project row (not active)
  const hdrRow = sidebar.locator(".proj-row", { hasText: "supersplat-hdr" });
  await hdrRow.click();

  // odo project should still be expanded
  await expect(sidebar.getByText("feat-sidebar-tree")).toBeVisible();
});
