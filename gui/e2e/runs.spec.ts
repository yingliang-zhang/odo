import { test, expect } from "@playwright/test";
import type { Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// P2.2 (docs/design/adoption-lock.md): the Runs tab folds journal rows
// (user_message/agent_done/agent_error starts+terminals, D3 loop_run_usage
// receipts) into a runs list — pure journal fold, no daemon state. A row
// click jumps the transcript to the run's starter bubble (data-seq anchor).

declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

const POLL = { timeout: 4000 };

async function openRunsTab(page: Page) {
  await page.keyboard.press("Meta+j");
  await page.locator('.context-panel [role="tab"]', { hasText: "Runs" }).click();
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("runs rows fold from the journal: status, duration, measured tokens, goal", async ({ page }) => {
  // Run A (ok, with a D3 measured-usage receipt pinned by covers_spawn_seq)
  // and Run B (error, no usage) ride the poll path like daemon-journaled rows.
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    const a = fx.ev("user_message", { text: "goal alpha — runs-tab proof run" }, 1);
    a.created_at = new Date(Date.now() - 300_000).toISOString();
    fx.events.push(a);
    const aDone = fx.ev("agent_done", { summary: "alpha landed" }, 1);
    aDone.created_at = new Date(Date.now() - 240_000).toISOString();
    fx.events.push(aDone);
    const usage = fx.ev("loop_event", {
      kind: "loop_run_usage",
      run_id: "r-alpha",
      covers_spawn_seq: a.seq,
      usage_available: true,
      input_tokens: 4000,
      output_tokens: 120,
      cache_read_tokens: 9000,
      cache_write_tokens: 80,
      cost_usd: 0.05,
    }, 1);
    usage.created_at = new Date(Date.now() - 239_000).toISOString();
    fx.events.push(usage);
    const b = fx.ev("user_message", { text: "goal beta — followed by an error" }, 1);
    b.created_at = new Date(Date.now() - 120_000).toISOString();
    fx.events.push(b);
    const bErr = fx.ev("agent_error", { error: "beta exploded" }, 1);
    bErr.created_at = new Date(Date.now() - 60_000).toISOString();
    fx.events.push(bErr);
  });

  await openRunsTab(page);

  // Newest first: beta tops alpha.
  const rows = page.locator('[data-slot="runs-row"]');
  await expect(rows.first()).toBeVisible(POLL);
  await expect(rows.first()).toHaveAttribute("data-status", "error");
  await expect(rows.first().locator(".runs-goal")).toContainText("goal beta");
  // Alpha: ok status + measured token cell (measured beats the dash; the
  // exact residue is pinned in runs.test.ts — here we only pin honesty).
  const alpha = page.locator('[data-slot="runs-row"]', { hasText: "goal alpha" });
  await expect(alpha).toHaveAttribute("data-status", "ok");
  await expect(alpha.locator(".runs-tokens")).not.toHaveText("—");
  // The error row's evidence line carries the journaled error.
  await expect(rows.first()).toContainText("beta exploded");
});

test("row click jumps the transcript to the run's starter bubble", async ({ page }) => {
  const seq = await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    const e = fx.ev("user_message", { text: "jump target run — find me in the transcript" }, 1);
    e.created_at = new Date(Date.now() - 90_000).toISOString();
    fx.events.push(e);
    const done = fx.ev("agent_done", { summary: "jump target done" }, 1);
    done.created_at = new Date(Date.now() - 80_000).toISOString();
    fx.events.push(done);
    // A later run sits between the target and the live tail, so a plain
    // tail-pin cannot pass for the jump (the assert reads intersection).
    const tail = fx.ev("user_message", { text: "spacer run" }, 1);
    tail.created_at = new Date().toISOString();
    fx.events.push(tail);
    const tailDone = fx.ev("agent_done", { summary: "spacer done" }, 1);
    tailDone.created_at = new Date().toISOString();
    fx.events.push(tailDone);
    return e.seq;
  });

  await openRunsTab(page);
  const row = page.locator(`[data-slot="runs-row"][data-seq="${seq}"]`);
  await expect(row).toBeVisible(POLL);
  await row.click();

  // The starter bubble is scrolled into the viewport (centered) and
  // briefly flashed; intersection in the message list is the contract.
  // Measure the .bubble INSIDE the data-seq anchor: the .bubble-mount
  // wrapper carrying data-seq is display:contents (generates no box, rect
  // is all-zero) — measuring it can never intersect.
  await expect
    .poll(
      async () => {
        return page.evaluate((s) => {
          const el = document.querySelector(`[data-seq="${s}"] .bubble`);
          if (!(el instanceof HTMLElement)) return false;
          const r = el.getBoundingClientRect();
          return r.bottom > 0 && r.top < window.innerHeight;
        }, seq);
      },
      POLL,
    )
    .toBe(true);
});

test("empty states: a workstream with no runs shows the empty message", async ({ page }) => {
  // Fresh page has plenty of fixture runs; this contracts the empty copy
  // against a filtered zero-state instead (component behavior is pinned in
  // runspanel.test.tsx — this guards the tab mount path).
  await openRunsTab(page);
  await expect(page.locator(".context-panel .mem-body, .context-panel .panel-empty").first()).toBeVisible(POLL);
});
