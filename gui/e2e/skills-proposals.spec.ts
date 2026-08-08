import { test, expect } from "@playwright/test";

// M9 (Skill Distillation + Three-Tier Gating): skill proposal review E2E tests.
// Uses mock-invoke with skill proposals in the memory_proposals fixture.
// The fixture has 3 proposals: memory.md (idx 0), user.md (idx 1), skills (idx 2).

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

async function openMemoryTab(page: import("@playwright/test").Page) {
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
  const memoryTab = page.getByRole("tab", { name: /Memory/ });
  await memoryTab.click();
  await expect(memoryTab).toHaveAttribute("aria-selected", "true");
}

async function openSkillsTab(page: import("@playwright/test").Page) {
  const skillsTab = page.getByRole("tab", { name: /Skills/ });
  await skillsTab.click();
  await expect(skillsTab).toHaveAttribute("aria-selected", "true");
}

test("skill proposal visible in memory proposals", async ({ page }) => {
  await openMemoryTab(page);

  // The skills section should be visible
  await expect(page.locator(".mem-section-title", { hasText: "skills (proposed)" })).toBeVisible();
  // The skill name should be visible
  await expect(page.locator(".mem-skill-name", { hasText: "run-tests-before-commit" })).toBeVisible();
});

test("verdict badges render for skill proposal", async ({ page }) => {
  await openMemoryTab(page);

  // Verdict badges should be visible (2 accept + 1 reject = 3 badges)
  await expect(page.locator(".mem-verdicts")).toBeVisible();
  const acceptBadges = page.locator(".verdict-badge.verdict-accept");
  await expect(acceptBadges).toHaveCount(2);
  const rejectBadges = page.locator(".verdict-badge.verdict-reject");
  await expect(rejectBadges).toHaveCount(1);
});

test("reject-by-default: skills start rejected", async ({ page }) => {
  await openMemoryTab(page);

  // Find the skill proposal row and check that Reject is selected (not Accept)
  const skillRow = page.locator(".mem-row-skill");
  await expect(skillRow).toBeVisible();
  // The reject button should have the "selected" class
  await expect(skillRow.locator(".mem-decision.reject.selected")).toBeVisible();
  // The accept button should NOT have the "selected" class
  await expect(skillRow.locator(".mem-decision.accept.selected")).toBeHidden();
});

test("accept skill proposal → apply → skill appears in SkillsPanel", async ({ page }) => {
  await openMemoryTab(page);

  // Click Accept on the skill proposal (reject-by-default, so must actively accept)
  const skillRow = page.locator(".mem-row-skill");
  await skillRow.locator(".mem-decision.accept").click();

  // Click Apply
  await page.locator(".mem-foot .settings-save").click();

  // Wait for apply to complete
  await expect(page.locator(".mem-result")).toBeVisible({ timeout: 5000 });

  // Switch to Skills tab
  await openSkillsTab(page);

  // The skill should appear in the list
  await expect(page.locator(".skill-row", { hasText: "run-tests-before-commit" })).toBeVisible({ timeout: 5000 });
  // Total should be 4 (3 original + 1 new)
  await expect(page.locator(".skills-count")).toContainText("4");
});

test("reject skill proposal → apply → skill does NOT appear in SkillsPanel", async ({ page }) => {
  await openMemoryTab(page);

  // Skill is reject-by-default, so just click Apply without accepting
  await page.locator(".mem-foot .settings-save").click();

  // Wait for apply to complete
  await expect(page.locator(".mem-result")).toBeVisible({ timeout: 5000 });

  // Switch to Skills tab
  await openSkillsTab(page);

  // The skill should NOT appear in the list (still 3 original skills)
  await expect(page.locator(".skill-row", { hasText: "run-tests-before-commit" })).toBeHidden({ timeout: 5000 });
  await expect(page.locator(".skills-count")).toContainText("3");
});

test("collapsible full SKILL.md content view", async ({ page }) => {
  await openMemoryTab(page);

  // The details element should be present
  const details = page.locator(".mem-skill-details");
  await expect(details).toBeVisible();

  // The summary should be visible
  await expect(details.locator("summary")).toContainText("Full SKILL.md");

  // Expand the details
  await details.locator("summary").click();

  // The full content should be visible
  await expect(details.locator("pre")).toBeVisible();
  await expect(details.locator("pre")).toContainText("Run Tests Before Commit");
});
