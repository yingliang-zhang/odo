import { test, expect } from "@playwright/test";

// GUI Wave B (audit §3 #5/#8/#9 — plan row #11): context-pressure meter,
// per-turn stats strip, MoA panel picker. Data sources are all journaled
// or settings facts the GUI already holds — zero new IPC:
//   #5 meter ← user_message prompt-receipt closure (total_prompt_bytes
//             + receipt keys + replay window), window mirror of modelspec
//   #8 strip ← run group events (wall from journaled timestamps; sizes
//             from prompt bytes + agent text bytes; billed usage when a
//             payload carries it — defensive branch, nothing writes it yet)
//   #9 chip  ← get_settings review_models (parsed model@provider)
// Fixtures live in conv 3 (fix-daemon-binary): conv 1's transcript stays
// free of receipt closures so diff/inbox specs never see the meter or the
// stats strip (Wave A collision precedent).

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
  await page.locator(".sidebar .ws-row", { hasText: "fix-daemon-binary" }).click();
});

test("context meter shows occupancy of the newest journaled prompt", async ({ page }) => {
  const meter = page.locator(".ctx-meter");
  await expect(meter).toBeVisible();
  // 1,204,000 B vs t9s/kimi-k3 (350k tok × ~4 B/tok ≈ 1.4 MB) = 86% → red.
  await expect(meter).toContainText("~86%");
  await expect(meter).toHaveClass(/meter-err/);
  await expect(meter.locator(".ctx-ring-fill")).toHaveAttribute("data-pct", "86");
  await expect(meter).toHaveAttribute("title", /t9s\/kimi-k3/);
});

test("meter popover lists the journaled composition, verbatim", async ({ page }) => {
  await page.locator(".ctx-meter").click();
  const pop = page.locator(".ctx-meter-popover");
  await expect(pop).toBeVisible();

  await expect(pop).toContainText("1.1 MB");
  await expect(pop).toContainText("sha16 a1b2c3d4e5f60718");
  await expect(pop).toContainText("~350.0k tok");
  await expect(pop).toContainText("61.0 KB");
  await expect(pop).toContainText("odo journal range 7 9");
  await expect(pop).toContainText("3 recall notes");

  // The layer list is the receipt's key set, ungrouped — including the
  // synthetic block keys the daemon attests.
  const layers = pop.locator(".ctx-layer");
  await expect(layers).toHaveCount(10);
  await expect(pop).toContainText("journal#todo");
  await expect(pop).toContainText("odo#memory-map");

  // Escape closes (bg-runs menu precedent).
  await page.keyboard.press("Escape");
  await expect(pop).toHaveCount(0);
});

test("per-turn stats strip: byte-derived sizes, then billed tokens+tok/s", async ({ page }) => {
  // Four completed runs stay visible: the once-folded "Patch" run (fold
  // blind-spot fix — the newest run below the fold boundary is never
  // hidden), the pre-receipt socket run, then the two Wave B turns.
  const strips = page.locator(".run-turn-stats");
  await expect(strips).toHaveCount(4);

  // The kept run leads: it journaled no agent_text → an honest out 0 B.
  await expect(strips.nth(0)).toContainText("out 0 B");
  await expect(strips.nth(0)).not.toContainText("in ");

  // Pre-receipt runs: out bytes only — the input number is never fabricated.
  await expect(strips.nth(1)).toContainText(/out \d+ B/);
  await expect(strips.nth(1)).not.toContainText("in ");

  // Billed branch: real token counts → real tok/s over journaled wall time.
  const billed = page.locator(".run-header", { hasText: "out 846 tok" });
  await expect(billed).toContainText("in 5.1k tok");
  await expect(billed).toContainText("tok/s");

  // Byte branch: in = prompt receipt bytes, out = agent text bytes, no
  // fabricated rate.
  const sized = page.locator(".run-header", { hasText: "in 1.1 MB" });
  await expect(sized).toContainText(/out \d+ B/);
  await expect(sized).not.toContainText("tok/s");
});

test("review panel chip lists every configured model, read-only", async ({ page }) => {
  const chip = page.locator(".panel-chip");
  await expect(chip).toBeVisible();
  await expect(chip).toHaveText(/Panel ×3/);

  await chip.click();
  const pop = page.locator(".panel-chip-popover");
  await expect(pop).toBeVisible();
  const rows = pop.locator(".panel-model-row");
  await expect(rows).toHaveCount(3);
  await expect(pop).toContainText("t9s/kimi-k3");
  await expect(pop).toContainText("t9s/glm-5.2");
  await expect(pop).toContainText("t9s/deepseek-v4-flash");
  await expect(pop.locator(".panel-model-provider").first()).toHaveText("sudo");
  // Read-only: no buttons inside the rows.
  await expect(pop.locator(".panel-model-row button")).toHaveCount(0);

  // Click-away closes.
  await page.mouse.click(320, 240);
  await expect(pop).toHaveCount(0);
});

test("no receipted prompt: meter hides, strips stay out-only (conv 1)", async ({ page }) => {
  await page.locator(".sidebar .ws-row", { hasText: "main" }).first().click();
  // No journaled closure → no occupancy number, ever.
  await expect(page.locator(".ctx-meter")).toHaveCount(0);
  // Completed runs still carry real out sizes — byte sizes are journaled
  // facts — but the absent input receipt stays absent (no fabrication).
  const strips = page.locator(".run-turn-stats");
  await expect(strips.first()).toBeVisible();
  await expect(strips.first()).toContainText(/out \d+(\.\d+)? (B|KB)/);
  await expect(strips.first()).not.toContainText("in ");
  // The panel chip follows settings, not the conversation — still there.
  await expect(page.locator(".panel-chip")).toBeVisible();
});
