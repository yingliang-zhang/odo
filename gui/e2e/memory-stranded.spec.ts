import { test, expect, type Page } from "@playwright/test";
import type * as fixtures from "../src/dev/fixtures";

// Stranded crash-recoveries (2026-08-26 memory-replay doctrine): the boot
// replayer journals heal_conflict for a foreign-unmergeable receipt; the
// Memory tab's banner count AND actionable rows both ride the
// project-wide pending_counts fold (round-3 FIX F — the pre-FIX-F
// per-conversation event fold could light the banner over zero actionable
// rows when the conflict rode a rotated lane), and Resolve/Dismiss close
// them via resolve_heal_conflict routed by the row's owning conversation.
// Rows and number share one payload now, but pending_counts polls only
// every 4th tick (App.tsx:961) — numbered assertions use the POLL window
// like every other poll-converged surface (a 5s expect marginal-flakes
// under full-suite load).

// Test hook installed by src/dev/mock-invoke.ts (plain-browser dev only).
declare global {
  interface Window {
    __odoFixtures?: typeof fixtures;
  }
}

const POLL = { timeout: 12_000 };

interface InjectedRow {
  type: string;
  payload: Record<string, unknown>;
  conv?: number;
}

async function journal(page: Page, rows: InjectedRow[]) {
  await page.evaluate((r) => {
    const fx = window.__odoFixtures;
    if (!fx) throw new Error("__odoFixtures hook missing — mock invoke not engaged");
    for (const row of r) {
      const e = fx.ev(row.type, row.payload, row.conv ?? 1);
      e.created_at = new Date().toISOString();
      fx.events.push(e);
    }
  }, rows);
}

function conflictRow(receiptSeq: number): InjectedRow {
  return {
    type: "memory_update",
    payload: {
      layer: "pins",
      cause: "heal_conflict",
      detail: `stranded pins post-crash (receipt seq ${receiptSeq}, conversation 1): whole-file layer with a foreign projection — no per-entry intent journaled`,
      stranded_receipt_seq: receiptSeq,
      stranded_conversation: 1,
      stranded_body_sha16: "0123abcd0123abcd",
      stranded_body: "- recovered stranded pin\n",
    },
  };
}

async function openMemoryTab(page: Page) {
  await page.keyboard.press("Meta+j");
  await expect(page.locator(".context-panel")).toBeVisible();
  const memoryTab = page.getByRole("tab", { name: /Memory/ });
  await memoryTab.click();
  await expect(memoryTab).toHaveAttribute("aria-selected", "true");
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".sidebar .proj-tree")).toBeVisible();
});

test("banner counts a stranded conflict; Resolve restores and closes it", async ({ page }) => {
  await journal(page, [conflictRow(5)]);
  await openMemoryTab(page);

  const banner = page.locator(".mem-stranded");
  await expect(banner).toBeVisible(POLL);
  await expect(banner).toContainText("1 stranded crash-recoveries — open to review", POLL);
  const row = banner.locator(".mem-stranded-row");
  await expect(row).toHaveCount(1);
  await expect(row).toContainText("pins");
  await expect(row.getByRole("button", { name: "Resolve" })).toBeVisible();
  await expect(row.getByRole("button", { name: "Dismiss" })).toBeVisible();

  // Resolve: the mock journals heal_resolved; the row and the badge count
  // drain by poll like every other journal surface.
  await row.getByRole("button", { name: "Resolve" }).click();
  await expect(banner).toBeHidden(POLL);
});

test("Dismiss records the decision without resurrecting anything", async ({ page }) => {
  await journal(page, [conflictRow(7)]);
  await openMemoryTab(page);

  const banner = page.locator(".mem-stranded");
  await expect(banner).toBeVisible(POLL);
  const row = banner.locator(".mem-stranded-row");
  await expect(row).toContainText("receipt seq 7");
  await row.getByRole("button", { name: "Dismiss" }).click();
  await expect(banner).toBeHidden(POLL);
});

test("two lanes, two conflicts: badge 2 AND two actionable rows; resolving one drops both", async ({ page }) => {
  // Round-3 FIX F (count/rows consistency): strandedTotal is project-wide,
  // so the rows come from the same project-wide pending_counts list — a
  // conflict riding conversation 2 (workstream feat-sidebar-tree) renders
  // HERE, and resolving it routes by its owning conversation. The pre-
  // FIX-F event fold lit the banner over zero actionable rows.
  await journal(page, [
    conflictRow(5),
    {
      type: "memory_update",
      conv: 2,
      payload: {
        layer: "memory",
        cause: "heal_conflict",
        detail: "stranded memory post-crash (receipt seq 9, conversation 2): receipt carries a retraction",
        stranded_receipt_seq: 9,
        stranded_conversation: 2,
        stranded_body_sha16: "feedbeefcafe0001",
        stranded_body: "- crashed rule — cites: e1; reaffirmed: 3\n",
      },
    },
  ]);
  await openMemoryTab(page);

  const banner = page.locator(".mem-stranded");
  await expect(banner).toBeVisible(POLL);
  await expect(banner).toContainText("2 stranded crash-recoveries — open to review", POLL);
  const rows = banner.locator(".mem-stranded-row");
  await expect(rows).toHaveCount(2, POLL);
  // The foreign lane's row is fully actionable from this tab.
  const foreignRow = banner.locator(".mem-stranded-row", { hasText: "receipt seq 9, conversation 2" });
  await expect(foreignRow).toBeVisible();
  await foreignRow.getByRole("button", { name: "Resolve" }).click();
  await expect(banner).toContainText("1 stranded crash-recoveries — open to review", POLL);
  await expect(banner.locator(".mem-stranded-row")).toHaveCount(1, POLL);
  // The surviving row is this lane's own conflict — the resolved one closed.
  await expect(banner.locator(".mem-stranded-row")).toContainText("conversation 1");
});
