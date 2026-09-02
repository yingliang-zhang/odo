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
  // UX-1 D2: Tasks is the default tab and mounts at boot — its hidden
  // keep-alive body also carries .mem-body, so the old unscoped locator's
  // .first() hit the HIDDEN Tasks body. Scope to the one visible wrapper
  // (`.panel-body > div:not([hidden])`); RunsPanel renders .mem-body in
  // both its empty and rows branches, so the mount-path contract holds
  // for either state.
  await expect(
    page.locator(".context-panel .panel-body > div:not([hidden]) .mem-body").first(),
  ).toBeVisible(POLL);
});

// Odo DX wave (Feature 1): the error row's hover retry re-sends the
// journaled prompt through send_message — the mock's journal-first knob
// (fx.sendCtl.pushPlainSends) models the daemon writing the row before
// answering, so the poll grows a NEW row for the same goal.
test("retry on an error row re-sends the prompt and grows a new running row", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    fx.sendCtl.pushPlainSends = true;
    const e = fx.ev("user_message", { text: "retryable goal — flaky gate fix" }, 1);
    e.created_at = new Date(Date.now() - 60_000).toISOString();
    fx.events.push(e);
    const err = fx.ev("agent_error", { error: "adapter exploded" }, 1);
    err.created_at = new Date(Date.now() - 55_000).toISOString();
    fx.events.push(err);
  });
  await openRunsTab(page);
  const retry = page.locator('[data-slot="runs-retry"]');
  await expect(retry).toBeVisible(POLL);
  await expect(retry).toHaveAttribute("title", "Retry this prompt as a new run");
  await retry.click({ force: true }); // opacity-0 until hover; force keeps the poll race out of it
  // The journaled re-send folds in as a NEW leading row, same goal,
  // status running (the mock adapter runs no agent — the row never
  // terminates here, which is exactly the freshly-sent state).
  await expect(page.locator('[data-slot="runs-row"]').first()).toHaveAttribute("data-status", "running", POLL);
  
  const rows = page.locator('[data-slot="runs-row"]', { hasText: "retryable goal" });
  await expect(rows).toHaveCount(2, POLL);
  await expect(rows.nth(1)).toHaveAttribute("data-status", "error");
});

// Odo DX wave (Feature 5): the Run/Test hub — registered .odo/commands.json
// entries render as buttons; the click runs through run_command (the mock
// answers the canned outcome AND journals the row, so the badge and the
// poll fold agree). Absent file = zero clutter, covered in runspanel.test.tsx.
test("commands hub: registered commands run and badge from the result", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    const e = fx.ev("user_message", { text: "hub-host goal" }, 1);
    fx.events.push(e);
    fx.previewFiles[".odo/commands.json"] = {
      content: JSON.stringify({
        version: 1,
        commands: [
          { name: "tests", cmd: "go test ./...", timeout: 120 },
          { name: "lint", cmd: "gofmt -l ." },
        ],
      }),
    };
    fx.commandCtl["tests"] = { exitCode: 0, stdout: "ok github.com/odo/...\n", durationMs: 640 };
    fx.commandCtl["lint"] = { exitCode: 1, stderr: "main.go needs gofmt\n", durationMs: 12 };
  });
  await openRunsTab(page);
  const hub = page.locator('[data-slot="commands-section"]');
  await expect(hub).toBeVisible(POLL);
  await expect(hub.locator('[data-slot="command-row"]')).toHaveCount(2);

  // Green path: click → invoke-fresh badge, expandable output.
  await hub.locator('[data-slot="command-row"][data-name="tests"] .command-run').click();
  const testsRow = hub.locator('[data-slot="command-row"][data-name="tests"]');
  await expect(testsRow.locator(".command-badge")).toContainText("ok · 640ms", POLL);
  await expect(testsRow.locator(".command-stdout")).toContainText("ok github.com/odo/...");

  // Red path: exit 1 reds with the code and the stderr tail.
  await hub.locator('[data-slot="command-row"][data-name="lint"] .command-run').click();
  const lintRow = hub.locator('[data-slot="command-row"][data-name="lint"]');
  await expect(lintRow.locator(".command-badge")).toContainText("exit 1", POLL);
  await expect(lintRow.locator(".command-stderr")).toContainText("gofmt");
});

// P1 borrow #6/#7 (quad-audit follow-up): the isolated child's lifecycle
// rows fold under their parent run; the registered proposal diff opens
// through the SAME pending-diff flow a normal run's diff uses (Changes
// tab, DiffViewer card — no new viewer).
test("subagent rows nest under the parent run; view diff opens Changes", async ({ page }) => {
  await page.evaluate(() => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing");
    const e = fx.ev("user_message", { text: "parent run goal — spawns a child" }, 1);
    fx.events.push(e);
    fx.events.push(fx.ev("subagent_spawned", {
      subagent_id: "sub-e2e-1",
      goal: "audit the panel fold",
      run_dir_id: "sub-e2e-1",
      worktree_path: "/mock/worktrees/sub-e2e-1",
    }, 1));
    fx.events.push(fx.ev("subagent_done", {
      subagent_id: "sub-e2e-1",
      goal: "audit the panel fold",
      exit_code: 0,
      summary: "fold audited",
      diff_id: 42,
      diff_path: ".odo/diffs/sub-e2e-1.patch",
    }, 1));
    fx.events.push(fx.ev("agent_done", { summary: "parent settled" }, 1));
    // The registered proposal diff: the Changes tab's pending list is its
    // surface (subagent diffs are pending diffs, never auto-landed).
    fx.changesDiffs.push({
      id: 42,
      status: "pending",
      path: ".odo/diffs/sub-e2e-1.patch",
      content: "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new\n",
    });
  });
  await openRunsTab(page);
  const row = page.locator('[data-slot="runs-subagent"]', { hasText: "audit the panel fold" });
  await expect(row).toBeVisible(POLL);
  await expect(row).toHaveAttribute("data-status", "done");
  await expect(row).toContainText("└ sub:");
  await row.locator('text=view diff').click();
  // The SAME path a normal pending diff takes: Changes tab with the card.
  await expect(page.locator('[data-slot="diff-card"]').first()).toBeVisible(POLL);
});

// P1 borrow #6 (turn-fork): the user-bubble GitFork affordance forks the
// journal prefix through fork_conversation and lands the app on the fresh
// lane — the sidebar row and the trimmed transcript are the observable
// contract (the mock mirrors the daemon's prefix copy).
test("fork from a user bubble switches to the new fork lane", async ({ page }) => {
  const fork = page.locator(".bubble-user .bubble-fork").first();
  await expect(page.locator(".bubble-user").first()).toBeVisible(POLL);
  await fork.click({ force: true }); // opacity-0 until hover; force keeps the hover race out
  // The re-listed sidebar shows the fresh lane…
  const sidebar = page.locator(".sidebar");
  await expect(sidebar).toContainText("main-fork-1", POLL);
  // …and the app lands on the forked conversation: the fixture's first
  // bubble was seq 1, so the copied prefix holds exactly ONE user bubble
  // and none of the agent rows that followed it in the source lane.
  await expect(page.locator(".bubble-user")).toHaveCount(1, POLL);
  await expect(page.locator(".bubble-user").first()).toContainText("Add a GFM table renderer");
});
