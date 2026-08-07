import { test, expect } from "@playwright/test";

// Chat: send message, user bubble appears, composer clears after send.

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar-app")).toBeVisible();
});

test("send message creates user bubble", async ({ page }) => {
  const textarea = page.getByPlaceholder("Describe the change you want…");
  const sendBtn = page.getByRole("button", { name: "Send" });

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
  const sendBtn = page.getByRole("button", { name: "Send" });

  await textarea.fill("Fix the alignment bug");
  await sendBtn.click();

  await expect(page.locator(".bubble-user").last()).toContainText("Fix the alignment bug");
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

test("Fan-out button is visible and labeled", async ({ page }) => {
  await expect(page.getByRole("button", { name: "Fan-out" })).toBeVisible();
});
