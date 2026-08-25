import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type * as Api from "../api";

// (tri-review P1 #5, 2026-08-24) Memory sub-tab deep links under the
// panel's keep-alive tabs: MemoryPanel now stays mounted across switches,
// so App's `focus` requests ({tab, n}) must keep applying AFTER first
// mount. Open questions this pins down: a request applies post-mount,
// a same-target repeat re-fires through the nonce, and an unchanged
// focus object does NOT yank the user back after a manual sub-tab click.
vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof Api>()),
  memoryProposals: vi.fn(async () => ({ ok: true })), // no pending batch
  readMemory: vi.fn(async () => ({ ok: true })),
  readPins: vi.fn(async () => ({ ok: true })),
}));

import MemoryPanel from "./MemoryPanel";
import { memoryProposals, readMemory } from "../api";

const proposalsTab = () => screen.getByRole("tab", { name: /^proposals/i });
const filesTab = () => screen.getByRole("tab", { name: /^current files$/i });

beforeEach(() => {
  cleanup();
  vi.mocked(memoryProposals).mockClear();
  vi.mocked(readMemory).mockClear();
});

describe("MemoryPanel focus deep links (tri-review P1 #5, 2026-08-24)", () => {
  it("applies a focus request that arrives after first mount", async () => {
    const { rerender } = render(
      <MemoryPanel conversationId={1} active={true} focus={{ tab: "proposals", n: 1 }} />,
    );
    await screen.findByText(/No pending memory proposals/i);
    expect(proposalsTab()).toHaveAttribute("aria-selected", "true");
    expect(vi.mocked(readMemory).mock.calls.length).toBe(0);

    rerender(<MemoryPanel conversationId={1} active={true} focus={{ tab: "files", n: 2 }} />);
    expect(filesTab()).toHaveAttribute("aria-selected", "true");
    expect(vi.mocked(readMemory).mock.calls.length).toBe(1);
  });

  it("re-fires a repeated same-target request through the nonce", async () => {
    const { rerender } = render(
      <MemoryPanel conversationId={1} active={true} focus={{ tab: "files", n: 1 }} />,
    );
    await waitFor(() => expect(vi.mocked(readMemory).mock.calls.length).toBe(1));
    expect(filesTab()).toHaveAttribute("aria-selected", "true");

    // User navigates by hand…
    fireEvent.click(proposalsTab());
    expect(proposalsTab()).toHaveAttribute("aria-selected", "true");

    // …a second toast click-through to "files" must still land.
    rerender(<MemoryPanel conversationId={1} active={true} focus={{ tab: "files", n: 2 }} />);
    expect(filesTab()).toHaveAttribute("aria-selected", "true");
  });

  it("leaves a manual sub-tab choice alone while the focus object is unchanged", async () => {
    const focus = { tab: "files", n: 1 } as const;
    const { rerender } = render(<MemoryPanel conversationId={1} active={true} focus={focus} />);
    expect(filesTab()).toHaveAttribute("aria-selected", "true");

    fireEvent.click(proposalsTab());
    expect(proposalsTab()).toHaveAttribute("aria-selected", "true");

    // A parent re-render carrying the SAME focus request must not
    // re-apply it — the user clicked "Proposals" after the request.
    rerender(<MemoryPanel conversationId={1} active={true} focus={focus} />);
    expect(proposalsTab()).toHaveAttribute("aria-selected", "true");
  });
});
