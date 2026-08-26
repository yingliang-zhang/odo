import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type * as Api from "../api";

// Auto-distill daily-cap chip (2026-08-26 storm fix): the daemon
// discloses the earliest quota release on pending_counts; the Memory tab
// renders "今日额度已用完 · 预计恢复 <time>". FIX 3 pins: the chip gates
// on the auto-distill pref (disabled → hidden even with a live value,
// and it comes back on re-enable) and never renders past the horizon
// (a stale poll value dies at its own timestamp).
vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof Api>()),
  memoryProposals: vi.fn(async () => ({ ok: true })),
  readMemory: vi.fn(async () => ({ ok: true })),
  readPins: vi.fn(async () => ({ ok: true })),
}));

import MemoryPanel from "./MemoryPanel";

const inFuture = { resume_at_unix: Math.floor(Date.now() / 1000) + 7200 };

beforeEach(cleanup);

describe("MemoryPanel daily-cap chip (storm fix)", () => {
  it("renders 今日额度已用完 with the recovery time when suspended", async () => {
    render(<MemoryPanel conversationId={1} active={true} autoDistillCapResume={inFuture} autoDistillEnabled={true} />);
    await screen.findByText(/今日额度已用完 · 预计恢复/);
    expect(document.querySelector(".mem-cap-notice")).not.toBeNull();
  });

  it("marks the upgrade fallback as computed", async () => {
    render(<MemoryPanel conversationId={1} active={true} autoDistillCapResume={{ ...inFuture, computed: true }} autoDistillEnabled={true} />);
    await screen.findByText(/今日额度已用完/);
    expect(document.querySelector(".mem-cap-notice")?.getAttribute("data-computed")).toBe("true");
  });

  it("stays hidden while quiet", () => {
    render(<MemoryPanel conversationId={1} active={true} autoDistillCapResume={null} autoDistillEnabled={true} />);
    expect(document.querySelector(".mem-cap-notice")).toBeNull();
  });

  it("FIX 3: hidden while auto-distill is disabled, back on re-enable", async () => {
    const { rerender } = render(
      <MemoryPanel conversationId={1} active={true} autoDistillCapResume={inFuture} autoDistillEnabled={false} />,
    );
    expect(document.querySelector(".mem-cap-notice")).toBeNull();
    rerender(<MemoryPanel conversationId={1} active={true} autoDistillCapResume={inFuture} autoDistillEnabled={true} />);
    await screen.findByText(/今日额度已用完/);
  });

  it("FIX 3: a stale value past the horizon never renders", () => {
    render(
      <MemoryPanel
        conversationId={1}
        active={true}
        autoDistillCapResume={{ resume_at_unix: Math.floor(Date.now() / 1000) - 3600 }}
        autoDistillEnabled={true}
      />,
    );
    expect(document.querySelector(".mem-cap-notice")).toBeNull();
  });
});
