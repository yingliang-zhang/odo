import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";

// P2.2/P2.3 (docs/design/adoption-lock.md): Preview tab body. Pins the
// activation-refetch contract (mount + false→true edge, never true→false),
// the image byte-load / chip fallback posture, the syntax-highlighted text
// render, and the design-lock sandbox attributes on the live iframe.

const { readFileMock, openPathMock } = vi.hoisted(() => ({
  readFileMock: vi.fn(),
  openPathMock: vi.fn(),
}));
vi.mock("../api", async (importOriginal) => {
  const real = (await importOriginal()) as Record<string, unknown>;
  return { ...real, readFile: readFileMock, openPath: openPathMock };
});

import PreviewPanel from "./PreviewPanel";
import { SLOT } from "../slots";

Element.prototype.scrollIntoView ??= () => {};

beforeEach(() => {
  cleanup();
  readFileMock.mockReset();
  openPathMock.mockReset();
  openPathMock.mockResolvedValue("opened");
});

describe("PreviewPanel empty + file targets", () => {
  it("renders the panel-empty message without fetching on a null target", () => {
    const { container } = render(<PreviewPanel target={null} projectRoot="/root" active />);
    expect(container.querySelector(".panel-empty")).not.toBeNull();
    expect(readFileMock).not.toHaveBeenCalled();
  });

  it("fetches a file target on mount", async () => {
    readFileMock.mockResolvedValue({ file_content: "hello world", file_resolved: "/root/notes.txt" });
    const { container } = render(<PreviewPanel target={{ kind: "file", path: "notes.txt" }} projectRoot="/root" active />);
    await waitFor(() => expect(container.textContent).toContain("hello world"));
    expect(readFileMock).toHaveBeenCalledTimes(1);
    expect(readFileMock).toHaveBeenCalledWith("notes.txt", "/root");
    // file_resolved surfaces in the header.
    expect(container.textContent).toContain("/root/notes.txt");
  });

  it("refetches on the active false→true edge only (activation contract)", async () => {
    readFileMock.mockResolvedValue({ file_content: "v1", file_resolved: "/root/notes.txt" });
    const target = { kind: "file", path: "notes.txt" } as const;
    const { rerender } = render(<PreviewPanel target={target} projectRoot="/root" active={false} />);
    await waitFor(() => expect(readFileMock).toHaveBeenCalledTimes(1)); // mount

    rerender(<PreviewPanel target={target} projectRoot="/root" active />);
    await waitFor(() => expect(readFileMock).toHaveBeenCalledTimes(2)); // activation edge

    rerender(<PreviewPanel target={target} projectRoot="/root" active={false} />);
    rerender(<PreviewPanel target={target} projectRoot="/root" active />);
    await waitFor(() => expect(readFileMock).toHaveBeenCalledTimes(3)); // second edge
    // No extra fetch from the true→false hides: exactly one per edge + mount.
  });

  it("renders inline image bytes with the P2.1 slot when the daemon serves them", async () => {
    readFileMock.mockResolvedValue({ file_data_base64: "aGk=", file_mime: "image/png", file_resolved: "/root/shot.png" });
    const { container } = render(<PreviewPanel target={{ kind: "file", path: "shot.png" }} projectRoot="/root" active />);
    await waitFor(() => expect(container.querySelector(`[data-slot="${SLOT.previewImage}"] img`)).not.toBeNull());
    const img = container.querySelector<HTMLImageElement>(`[data-slot="${SLOT.previewImage}"] img`)!;
    expect(img.getAttribute("src")).toBe("data:image/png;base64,aGk=");
  });

  it("falls back to a chip + Open in OS when image bytes are absent", async () => {
    readFileMock.mockResolvedValue({ file_resolved: "/root/shot.png" }); // today's daemon: no bytes
    const { container } = render(<PreviewPanel target={{ kind: "file", path: "shot.png" }} projectRoot="/root" active />);
    await waitFor(() => expect(container.querySelector(`[data-slot="${SLOT.previewChip}"]`)).not.toBeNull());
    expect(container.querySelector(`[data-slot="${SLOT.previewImage}"] img`)).toBeNull();
    fireEvent.click(container.querySelector(`[data-slot="${SLOT.previewChip}"] button`)!);
    expect(openPathMock).toHaveBeenCalledWith("shot.png", false, "/root");
  });

  it("renders text files syntax-highlighted via the shared tokenizer", async () => {
    readFileMock.mockResolvedValue({ file_content: "package main", file_resolved: "/root/main.go" });
    const { container } = render(<PreviewPanel target={{ kind: "file", path: "main.go" }} projectRoot="/root" active />);
    await waitFor(() => expect(container.querySelector("span.tok-keyword")).not.toBeNull());
    expect(container.querySelector("span.tok-keyword")!.textContent).toBe("package");
  });

  it("renders read_file errors inline instead of throwing", async () => {
    readFileMock.mockRejectedValue(new Error("daemon down"));
    const { container } = render(<PreviewPanel target={{ kind: "file", path: "x.txt" }} projectRoot="/root" active />);
    await waitFor(() => expect(container.textContent).toContain("daemon down"));
  });
});

describe("PreviewPanel url targets (P2.3 design lock)", () => {
  it("mounts a sandboxed iframe for a localhost URL", () => {
    const { container } = render(
      <PreviewPanel target={{ kind: "url", url: "http://localhost:3000/app" }} projectRoot={null} active />,
    );
    const frame = container.querySelector<HTMLIFrameElement>(`iframe[data-slot="${SLOT.previewFrame}"]`)!;
    expect(frame).not.toBeNull();
    expect(frame.getAttribute("src")).toBe("http://localhost:3000/app");
    expect(frame.getAttribute("sandbox")).toBe("allow-scripts");
    expect(frame.getAttribute("sandbox")).not.toContain("same-origin");
    expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(frame.getAttribute("title")).toBe("http://localhost:3000/app");
    expect(container.textContent).toContain("http://localhost:3000/app"); // header
  });

  it("renders the localhost-only lock note and NO iframe for a remote URL", () => {
    const { container } = render(
      <PreviewPanel target={{ kind: "url", url: "https://example.com" }} projectRoot={null} active />,
    );
    expect(container.querySelector("iframe")).toBeNull();
    expect(container.textContent).toContain("localhost");
    expect(container.textContent).toContain("never requested");
  });
});
