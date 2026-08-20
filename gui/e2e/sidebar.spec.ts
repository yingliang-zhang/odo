import { test, expect } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";
import { strings } from "../src/strings";

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

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

test("create input dismisses on click-away", async ({ page }) => {
  const sidebar = page.locator(".sidebar");

  await sidebar.getByText("+ New workstream").click();
  const input = sidebar.locator(".ws-create input");
  await expect(input).toBeVisible();

  // Clicking anywhere outside the input must dismiss it — Esc alone is
  // undiscoverable, and an accidental "+ New workstream" click must not
  // leave a stuck row.
  await page.locator(".app-topbar").click();
  await expect(sidebar.locator(".ws-create input")).toHaveCount(0);
});

test("create input does not follow across project switch", async ({ page }) => {
  const sidebar = page.locator(".sidebar");

  // Open the create input under odo, then switch to supersplat-hdr.
  await sidebar.getByText("+ New workstream").click();
  await expect(sidebar.locator(".ws-create input")).toBeVisible();

  // Name-anchored regex so the "Remove supersplat-hdr from list" action
  // button doesn't match.
  const hdrRow = sidebar.getByRole("button", { name: /^(Idle|Pending review) supersplat-hdr/ });
  await hdrRow.click();
  // The first click may only dismiss the input (popover-dismiss swallows
  // the switch gesture) — a second click guarantees the root flip.
  await hdrRow.click();
  await expect(page.locator(".app-topbar")).toContainText("supersplat-hdr");

  // The input died with the switch — it must not render under the new
  // active project.
  await expect(sidebar.locator(".ws-create input")).toHaveCount(0);

  // And it stays gone when switching back.
  const odoRow = sidebar.getByRole("button", { name: /review odo/ });
  await odoRow.click();
  await expect(page.locator(".app-topbar")).toContainText("odo");
  await expect(sidebar.locator(".ws-create input")).toHaveCount(0);
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
  await wsRow.getByRole("button", { name: /Delete/ }).click();

  // P2: inline delete confirm — click confirm to actually delete
  await expect(wsRow.locator(".ws-delete-confirm-text")).toBeVisible();
  await wsRow.getByRole("button", { name: /Confirm delete/ }).click();

  // Workstream removed
  await expect(sidebar.locator(".ws-item", { hasText: "to-delete" })).toBeHidden();
});

test("remove non-active project via hover action; active project has no affordance", async ({ page }) => {
  const sidebar = page.locator(".sidebar");

  // Active project row (odo): no remove control even on hover.
  const activeHead = sidebar.locator(".proj-row-head", { hasText: "odo" });
  await activeHead.hover();
  await expect(activeHead.getByRole("button", { name: /Remove/ })).toHaveCount(0);

  // Non-active row: two-step inline confirm, mirroring workstream delete.
  const hdrHead = sidebar.locator(".proj-row-head", { hasText: "supersplat-hdr" });
  await hdrHead.hover();
  await hdrHead.getByRole("button", { name: "Remove supersplat-hdr from list" }).click();
  await expect(hdrHead.locator(".ws-delete-confirm-text")).toHaveText("Remove?");
  // Derive the confirm aria-label from the strings source (Sidebar.tsx builds
  // it as `${confirmRemoveTitle} ${p.name}`) — don't hand-copy the literal.
  await hdrHead.getByRole("button", { name: `${strings.sidebar.confirmRemoveTitle} supersplat-hdr` }).click();

  // Row gone from the tree; active project untouched.
  await expect(sidebar.locator(".proj-row", { hasText: "supersplat-hdr" })).toHaveCount(0);
  await expect(sidebar.locator(".proj-row-active", { hasText: "odo" })).toBeVisible();
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

const ODO_ROOT = "/Users/yingliangzhang/Projects/odo";
const HDR_ROOT = "/Users/yingliangzhang/Projects/Sudo/supersplat-hdr";
// Aggregate cadence: pending_counts refreshes on every 4th poll tick
// (~6 s idle); the cross-project badge loop runs every 5 s.
const AGG_REFRESH = { timeout: 12_000 };

// 2026-08-20 regression: the active project's aggregates (running dots,
// pending/parked pills) are keyed by workstream id — and ids collide
// across projects (every journal starts at 1). Without a reset on project
// switch, the previous project's rows rendered against the new project's
// workstreams until the 4th poll tick corrected them: odo's running Main
// (real) made supersplat-hdr's Main read "running" (its daemon reported
// []). The mock answers pending_counts per root (fx.countsByRoot) so the
// two fixture daemons disagree exactly like the real ones did.
test("project switch drops the previous project's running/pending aggregates", async ({ page }) => {
  const staleWindowProbe = async (root: () => Promise<number>) => expect(await root()).toBe(0);
  await page.evaluate(([odo, hdr]) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    // odo daemon: Main (ws 1) running, plus pending/parked aimed at ws 10 —
    // an id only the OTHER project's Main has, so the pills are invisible
    // before the switch and can only appear as cross-project leakage.
    fx.countsByRoot[odo] = { pending: { 10: 3 }, parked: { 10: 2 }, running: [1] };
    // supersplat-hdr daemon: idle.
    fx.countsByRoot[hdr] = { pending: {}, parked: {}, running: [] };
    // supersplat-hdr gains a workstream whose id collides with odo's
    // running Main — the exact 2026-08-20 shape.
    fx.workstreams[hdr].push({
      id: 1, project_id: 2, name: "legacy-main", branch: "legacy-main",
      status: "active", created_at: "2026-08-01T14:02:00Z",
    });
  }, [ODO_ROOT, HDR_ROOT]);

  const sidebar = page.locator(".sidebar");
  const odoGroup = sidebar.locator(".proj-group", { has: page.locator(".proj-row", { hasText: "odo" }) });
  // Baseline truth: odo's Main reads Running (pre-condition, from its own daemon).
  await expect(odoGroup.locator(".ws-row", { hasText: "main" }).getByText("Running", { exact: true }))
    .toBeVisible(AGG_REFRESH);

  await sidebar.locator(".proj-row", { hasText: "supersplat-hdr" }).click();
  // Commit 2 marker: the new project's workstream list has landed.
  const hdrGroup = sidebar.locator(".proj-group", { has: page.locator(".proj-row", { hasText: "supersplat-hdr" }) });
  await expect(hdrGroup.getByText("legacy-main")).toBeVisible();

  // Synchronous sample — no web-first retry. Without the switch-time
  // reset the stale aggregates persist ≥1.5 s past this point, so a
  // single count() settles the contract deterministically.
  await staleWindowProbe(() => hdrGroup.getByText("Running", { exact: true }).count());
  await staleWindowProbe(() => hdrGroup.getByText("Running in background", { exact: true }).count());
  await staleWindowProbe(() => hdrGroup.getByText("still running", { exact: true }).count());
  await staleWindowProbe(() => hdrGroup.locator(".ws-pending-pill").count());
  await staleWindowProbe(() => hdrGroup.locator(".ws-parked-pill").count());

  // Stays clean after the aggregate cadence catches up — supersplat-hdr's
  // own pending_counts is genuinely idle, so any relapse means the clear
  // regressed rather than the daemon lying.
  await page.waitForTimeout(7_000);
  await staleWindowProbe(() => hdrGroup.getByText("Running in background", { exact: true }).count());
  await staleWindowProbe(() => hdrGroup.locator(".ws-pending-pill").count());
  await staleWindowProbe(() => hdrGroup.locator(".ws-parked-pill").count());

  // Positive control: the now-non-active odo still truthfully reports its
  // running Main via the 5 s cross-project badge loop — the reset must
  // not overwrite real state from a sibling daemon.
  await expect(odoGroup.getByText("Running in background", { exact: true })).toBeVisible(AGG_REFRESH);
});
