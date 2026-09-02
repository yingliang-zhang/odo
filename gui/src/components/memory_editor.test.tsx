import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type * as Api from "../api";

// Odo DX wave (Feature 3): the memory.md / pins.md direct editor. Pins the
// edit affordance placement (project layers only — never user.md or the
// archive), the textarea draft, the write_memory payload, the post-save
// refresh + toast, and the inline refusal path (the draft survives).

const { writeMemoryMock } = vi.hoisted(() => ({ writeMemoryMock: vi.fn() }));
vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof Api>()),
  memoryProposals: vi.fn(async () => ({ ok: true })), // no pending batch
  readMemory: vi.fn(async () => ({
    ok: true,
    memory_content: "- keep rules lean\n",
    archive_content: "- archived\n",
    user_content: "- global rule\n",
  })),
  readPins: vi.fn(async () => ({ ok: true, memory_content: "- always run tests\n" })),
  writeMemory: writeMemoryMock,
}));

import MemoryPanel from "./MemoryPanel";
import { readMemory, readPins } from "../api";

const filesTab = () => screen.getByRole("tab", { name: /^current files$/i });

beforeEach(() => {
  cleanup();
  writeMemoryMock.mockReset();
  vi.mocked(readMemory).mockClear();
  vi.mocked(readPins).mockClear();
  render(<MemoryPanel conversationId={1} active={true} onToast={vi.fn()} />);
});

async function openFilesTab() {
  fireEvent.click(filesTab());
  await screen.findByText("memory.md (current)");
}

describe("MemoryPanel direct editor (Odo DX wave, Feature 3)", () => {
  it("offers edit on memory.md and pins.md — never on user.md or the archive", async () => {
    await openFilesTab();
    expect(screen.getByRole("button", { name: "Edit memory.md" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Edit pins.md" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Edit user.md" })).toBeNull();
    // The archive/user sections carry no edit affordance at all.
    expect(screen.getAllByRole("button", { name: /^Edit / }).length).toBe(2);
  });

  it("drafts the current content, saves through write_memory, refreshes, and toasts", async () => {
    const onToast = vi.fn();
    cleanup();
    render(<MemoryPanel conversationId={1} active={true} onToast={onToast} />);
    await openFilesTab();

    fireEvent.click(screen.getByRole("button", { name: "Edit memory.md" }));
    const area = screen.getByLabelText("Edit memory.md content") as HTMLTextAreaElement;
    expect(area.value).toBe("- keep rules lean\n");
    fireEvent.change(area, { target: { value: "- edited rule\n" } });

    expect(vi.mocked(readMemory).mock.calls.length).toBe(1); // the files load
    writeMemoryMock.mockResolvedValue({ ok: true, applied: true });
    fireEvent.click(screen.getByRole("button", { name: "Save memory.md" }));

    await waitFor(() => expect(writeMemoryMock).toHaveBeenCalledWith("memory.md", "- edited rule\n", undefined));
    expect(screen.queryByLabelText("Edit memory.md content")).toBeNull(); // editor closed
    expect(vi.mocked(readMemory).mock.calls.length).toBe(2); // post-save refresh
    expect(onToast).toHaveBeenCalledWith({ text: "saved .odo/memory.md" });
  });

  it("saves pins.md through the same path", async () => {
    writeMemoryMock.mockResolvedValue({ ok: true, applied: true });
    await openFilesTab();
    fireEvent.click(screen.getByRole("button", { name: "Edit pins.md" }));
    const area = screen.getByLabelText("Edit pins.md content") as HTMLTextAreaElement;
    expect(area.value).toBe("- always run tests\n");
    fireEvent.click(screen.getByRole("button", { name: "Save pins.md" }));
    await waitFor(() => expect(writeMemoryMock).toHaveBeenCalledWith("pins.md", "- always run tests\n", undefined));
  });

  it("shows a daemon refusal inline and keeps the draft open for retry", async () => {
    writeMemoryMock.mockRejectedValue(new Error("write_memory: pins.md would exceed 2048 bytes (3000 written)"));
    await openFilesTab();
    fireEvent.click(screen.getByRole("button", { name: "Edit pins.md" }));
    fireEvent.click(screen.getByRole("button", { name: "Save pins.md" }));
    await screen.findByText(/would exceed 2048 bytes/);
    expect(screen.getByLabelText("Edit pins.md content")).toBeTruthy(); // draft survives
  });

  it("Cancel abandons the draft without a write", async () => {
    await openFilesTab();
    fireEvent.click(screen.getByRole("button", { name: "Edit memory.md" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByLabelText("Edit memory.md content")).toBeNull();
    expect(writeMemoryMock).not.toHaveBeenCalled();
  });

  it("marks an open draft as LRU park-protected via onDraftChange", async () => {
    const onDraftChange = vi.fn();
    cleanup();
    render(<MemoryPanel conversationId={1} active={true} onDraftChange={onDraftChange} />);
    await openFilesTab();
    onDraftChange.mockClear();
    fireEvent.click(screen.getByRole("button", { name: "Edit memory.md" }));
    expect(onDraftChange).toHaveBeenLastCalledWith(true);
  });
});
