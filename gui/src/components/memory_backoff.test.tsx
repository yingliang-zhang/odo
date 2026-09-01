import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type * as Api from "../api";
import type { EventPayload, OdoEvent } from "../types";

// UX-3c (A2-6c): the Memory tab's auto-curate/auto-distill backoff footer
// derives from the conversation's journaled memory_update rows (the events
// prop — App's poll, no new IPC). Skipped/backoff rows render one dim line
// each; quiet journals render nothing (no speculative states).
vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof Api>()),
  memoryProposals: vi.fn(async () => ({ ok: true })),
  readMemory: vi.fn(async () => ({ ok: true })),
  readPins: vi.fn(async () => ({ ok: true })),
}));

import MemoryPanel from "./MemoryPanel";

function ev(seq: number, payload: EventPayload): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type: "memory_update", payload, created_at: "2026-09-01T00:00:00.000Z" };
}

// 24h out — always inside autoCurateFailureBackoff regardless of wall time.
const future = new Date(Date.now() + 24 * 3600 * 1000).toISOString();

beforeEach(cleanup);

describe("MemoryPanel backoff footer (UX-3c)", () => {
  it("shows the curator pause with the journaled next-eligible time", async () => {
    render(
      <MemoryPanel
        conversationId={1}
        active={true}
        events={[ev(1, { layer: "curator", cause: "skipped", detail: `trigger=notes_idle notes_since=3 reason=backoff next_eligible_at=${future}` })]}
      />,
    );
    await screen.findByText(/^auto-curate paused — next eligible /);
    expect(document.querySelector(".mem-backoff")).not.toBeNull();
  });

  it("shows the distill idle line for a below-min-bytes skip", async () => {
    render(
      <MemoryPanel
        conversationId={1}
        active={true}
        events={[ev(1, { layer: "auto_distill", cause: "skipped", detail: "trigger=idle window_events=3 window_bytes=120 reason=below_min_bytes" })]}
      />,
    );
    await screen.findByText("auto-distill idle — below min bytes");
  });

  it("stays hidden on a quiet journal and with no events prop at all", () => {
    const { rerender } = render(<MemoryPanel conversationId={1} active={true} />);
    expect(document.querySelector(".mem-backoff")).toBeNull();
    rerender(
      <MemoryPanel
        conversationId={1}
        active={true}
        events={[
          ev(1, { layer: "auto_distill", cause: "fired", detail: "trigger=idle window_events=42 window_bytes=9000" }),
          ev(2, { layer: "curator", cause: "curate", detail: "curated 4 notes" }),
        ]}
      />,
    );
    expect(document.querySelector(".mem-backoff")).toBeNull();
  });
});
