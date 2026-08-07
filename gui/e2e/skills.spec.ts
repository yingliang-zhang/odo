import { test, expect } from "@playwright/test";

// M8 (Skills): skills tab, list, preview, create, edit.
// Uses mock-invoke with 3 fixture skills (tdd-workflow, systematic-debugging, deploy-checklist).

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar-app")).toBeVisible();
});

async function openSkillsTab(page: import("@playwright/test").Page) {
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
  const skillsTab = page.getByRole("tab", { name: /Skills/ });
  await skillsTab.click();
  await expect(skillsTab).toHaveAttribute("aria-selected", "true");
}

test("skills tab shows skill list", async ({ page }) => {
  await openSkillsTab(page);

  // 3 fixture skills should be visible
  await expect(page.locator(".skills-panel")).toBeVisible();
  await expect(page.locator(".skill-row")).toHaveCount(3);
  await expect(page.locator(".skill-row", { hasText: "tdd-workflow" })).toBeVisible();
  await expect(page.locator(".skill-row", { hasText: "systematic-debugging" })).toBeVisible();
  await expect(page.locator(".skill-row", { hasText: "deploy-checklist" })).toBeVisible();
});

test("click skill shows preview", async ({ page }) => {
  await openSkillsTab(page);

  // Click tdd-workflow
  await page.locator(".skill-row", { hasText: "tdd-workflow" }).click();

  // Preview should show the skill name and body content
  await expect(page.locator(".skill-preview-name")).toContainText("tdd-workflow");
  await expect(page.locator(".skill-body")).toContainText("TDD Workflow");
  await expect(page.locator(".skill-body")).toContainText("RED");
});

test("skill scope dots are visible", async ({ page }) => {
  await openSkillsTab(page);

  // Global skills have blue dots, project skills have green dots
  const globalDots = page.locator(".skill-scope-dot.scope-global");
  await expect(globalDots).toHaveCount(2); // tdd-workflow + systematic-debugging

  const projectDots = page.locator(".skill-scope-dot.scope-project");
  await expect(projectDots).toHaveCount(1); // deploy-checklist
});

test("new skill button opens editor", async ({ page }) => {
  await openSkillsTab(page);

  // Click New button
  await page.locator(".skills-add-btn").click();

  // Editor textarea visible with template content
  await expect(page.locator(".skill-editor-textarea")).toBeVisible();
  await expect(page.locator(".skill-editor-textarea")).toContainText("new-skill");
});

test("edit existing skill opens editor with content", async ({ page }) => {
  await openSkillsTab(page);

  // Click a skill to preview it
  await page.locator(".skill-row", { hasText: "systematic-debugging" }).click();
  await expect(page.locator(".skill-preview-name")).toContainText("systematic-debugging");

  // Wait for content to load before clicking Edit
  await expect(page.locator(".skill-body")).toContainText("Systematic Debugging");

  // Click Edit button
  await page.locator(".skill-edit-btn").click();

  // Editor textarea visible with existing content (use toHaveValue for textarea)
  await expect(page.locator(".skill-editor-textarea")).toBeVisible();
  await expect(page.locator(".skill-editor-textarea")).toHaveValue(/Systematic Debugging/);
});

test("skill keywords are visible in list", async ({ page }) => {
  await openSkillsTab(page);

  // tdd-workflow has keywords: tdd, test, testing — use exact match to avoid "test" matching "testing"
  const tddRow = page.locator(".skill-row", { hasText: "tdd-workflow" });
  await expect(tddRow.locator(".skill-kw", { hasText: "tdd" })).toBeVisible();
  // Use exact text to disambiguate "test" from "testing"
  await expect(tddRow.locator(".skill-kw").filter({ hasText: /^test$/ })).toBeVisible();
});

test("skills count shows total", async ({ page }) => {
  await openSkillsTab(page);

  // Should show "3 skills"
  await expect(page.locator(".skills-count")).toContainText("3");
});

test("save new skill appears in list", async ({ page }) => {
  await openSkillsTab(page);

  // Click New
  await page.locator(".skills-add-btn").click();
  await expect(page.locator(".skill-editor-textarea")).toBeVisible();

  // Clear and type a new skill
  await page.locator(".skill-editor-textarea").fill("---\nname: e2e-test-skill\ndescription: Created by E2E test\nkeywords: [e2e]\norigin: human\n---\n\n# E2E Test Skill\n\nThis skill was created by an E2E test.");

  // Click Save
  await page.locator(".skill-save-btn").click();

  // Editor closes and skill appears in list
  await expect(page.locator(".skill-editor-textarea")).toBeHidden({ timeout: 3000 });
  await expect(page.locator(".skill-row", { hasText: "e2e-test-skill" })).toBeVisible();
  await expect(page.locator(".skills-count")).toContainText("4");
});

test("delete skill removes from list", async ({ page }) => {
  await openSkillsTab(page);

  // First create a skill to delete
  await page.locator(".skills-add-btn").click();
  await page.locator(".skill-editor-textarea").fill("---\nname: to-delete-e2e\ndescription: Will be deleted\n---\n\nBody");
  await page.locator(".skill-save-btn").click();
  await expect(page.locator(".skill-row", { hasText: "to-delete-e2e" })).toBeVisible();

  // Select the skill
  await page.locator(".skill-row", { hasText: "to-delete-e2e" }).click();
  await expect(page.locator(".skill-preview-name")).toContainText("to-delete-e2e");

  // Click Delete
  await page.locator(".skill-delete-btn").click();

  // Confirmation appears
  await expect(page.locator(".skill-delete-confirm")).toBeVisible();

  // Confirm deletion
  await page.locator(".skill-delete-confirm-btn").click();

  // Skill removed from list
  await expect(page.locator(".skill-row", { hasText: "to-delete-e2e" })).toBeHidden({ timeout: 3000 });
  await expect(page.locator(".skills-count")).toContainText("3"); // back to original 3
});

test("scope selector visible on create", async ({ page }) => {
  await openSkillsTab(page);

  // Click New
  await page.locator(".skills-add-btn").click();

  // Scope selector should be visible (only in create mode)
  await expect(page.locator(".skill-scope-selector")).toBeVisible();
  await expect(page.locator(".skill-scope-opt", { hasText: "Project" })).toBeVisible();
  await expect(page.locator(".skill-scope-opt", { hasText: "Global" })).toBeVisible();

  // Default is Project
  await expect(page.locator(".skill-scope-opt.active", { hasText: "Project" })).toBeVisible();
});

test("scope selector not visible when editing", async ({ page }) => {
  await openSkillsTab(page);

  // Click a skill and edit
  await page.locator(".skill-row", { hasText: "tdd-workflow" }).click();
  await page.locator(".skill-edit-btn").click();

  // Scope selector should NOT be visible in edit mode
  await expect(page.locator(".skill-scope-selector")).toBeHidden();
});

test("cancel delete returns to preview", async ({ page }) => {
  await openSkillsTab(page);

  // Select a skill
  await page.locator(".skill-row", { hasText: "tdd-workflow" }).click();
  await expect(page.locator(".skill-preview-name")).toContainText("tdd-workflow");

  // Click Delete
  await page.locator(".skill-delete-btn").click();
  await expect(page.locator(".skill-delete-confirm")).toBeVisible();

  // Click Cancel
  await page.locator(".skill-delete-cancel-btn").click();

  // Confirmation gone, preview back
  await expect(page.locator(".skill-delete-confirm")).toBeHidden();
  await expect(page.locator(".skill-preview-name")).toContainText("tdd-workflow");
});
