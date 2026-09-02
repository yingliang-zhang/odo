import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";

// P2.1–P2.3 additions (docs/design/adoption-lock.md) mock readFile/openPath:
// image attachments + tool-result refs attempt byte loads; Open/open-live
// affordances invoke the OS/panel callbacks.

const { readFileMock, openPathMock } = vi.hoisted(() => ({
  readFileMock: vi.fn(),
  openPathMock: vi.fn(),
}));
vi.mock("../api", async (importOriginal) => {
  const real = (await importOriginal()) as Record<string, unknown>;
  return { ...real, readFile: readFileMock, openPath: openPathMock };
});

// U2.5 (docs/design/ui-layout-lock.md §U2.5) — the bubble timestamp strip
// shrinks from pb-[26px] to pb-[20px] (the .bubble-time footprint is ~18px).
// Token-exact class checks on BOTH reserved-padding bubble variants.

import MessageBubble from "./MessageBubble";
import type { OdoEvent } from "../types";

// jsdom has no layout engine — the markdown linkify jumps scrollIntoView.
Element.prototype.scrollIntoView ??= () => {};

function event(type: string, text: string): OdoEvent {
  return {
    id: 1,
    conversation_id: 1,
    seq: 1,
    type: type as OdoEvent["type"],
    payload: { text },
    created_at: "2026-08-29T10:00:00Z",
  };
}

function eventWithPayload(type: string, payload: Record<string, unknown>): OdoEvent {
  return {
    id: 1,
    conversation_id: 1,
    seq: 1,
    type: type as OdoEvent["type"],
    payload,
    created_at: "2026-08-29T10:00:00Z",
  };
}

beforeEach(() => {
  cleanup();
  readFileMock.mockReset();
  readFileMock.mockResolvedValue({}); // default: no forward-compat bytes
  openPathMock.mockReset();
  openPathMock.mockResolvedValue("opened");
});

describe("bubble timestamp reservation (U2.5)", () => {
  it("user bubble reserves pb-[20px], not pb-[26px]", () => {
    const { container } = render(<MessageBubble event={event("user_message", "hello")} />);
    const bubble = container.querySelector<HTMLElement>(".bubble-user")!;
    expect(bubble.classList.contains("pb-[20px]")).toBe(true);
    expect(bubble.classList.contains("pb-[26px]")).toBe(false);
  });

  it("agent bubble reserves pb-[20px], not pb-[26px]", () => {
    const { container } = render(<MessageBubble event={event("agent_text", "hello")} />);
    const bubble = container.querySelector<HTMLElement>(".bubble-agent")!;
    expect(bubble.classList.contains("pb-[20px]")).toBe(true);
    expect(bubble.classList.contains("pb-[26px]")).toBe(false);
  });
});

describe("UX-3b (A2-6b): daemon advisory styling", () => {
  it("agent_error with odo:true renders an amber advisory bubble, never the red failure one", () => {
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("agent_error", { error: "odo: 1 parked goal remains queued", odo: true })}
      />,
    );
    const advisory = container.querySelector<HTMLElement>(".bubble-advisory");
    expect(advisory).not.toBeNull();
    expect(advisory!.classList.contains("border-warn")).toBe(true);
    expect(advisory!.classList.contains("text-warn-text")).toBe(true);
    expect(container.querySelector(".bubble-error")).toBeNull();
    expect(container.querySelector(".bubble-advisory-label")?.textContent).toBe("odo advisory");
    expect(advisory!.textContent).toContain("odo: 1 parked goal remains queued");
  });

  it("a plain agent_error stays the red failure bubble with no advisory label", () => {
    const { container } = render(
      <MessageBubble event={eventWithPayload("agent_error", { error: "adapter exploded" })} />,
    );
    const error = container.querySelector<HTMLElement>(".bubble-error");
    expect(error).not.toBeNull();
    expect(error!.classList.contains("bg-err-surface")).toBe(true);
    expect(container.querySelector(".bubble-advisory")).toBeNull();
    expect(container.querySelector(".bubble-advisory-label")).toBeNull();
  });
});

describe("P2.1 image attachments (adoption-lock)", () => {
  it("renders loaded bytes inline as a zoomable image", async () => {
    readFileMock.mockResolvedValue({ file_data_base64: "aGk=", file_mime: "image/png" });
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("user_message", { text: "", attachments: [".odo/attachments/shot.png"] })}
        projectRoot="/root"
      />,
    );
    await waitFor(() => expect(container.querySelector('[data-slot="preview-image"] img')).not.toBeNull());
    expect(readFileMock).toHaveBeenCalledWith(".odo/attachments/shot.png", "/root");
    expect(container.querySelector('[data-slot="preview-image"] img')!.getAttribute("src")).toBe(
      "data:image/png;base64,aGk=",
    );
  });

  it("falls back to the chip + Open button when bytes are absent", async () => {
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("user_message", { text: "", attachments: ["docs/shot.png"] })}
        projectRoot="/root"
      />,
    );
    await waitFor(() => expect(readFileMock).toHaveBeenCalledTimes(1));
    const chip = container.querySelector('[data-slot="preview-chip"]')!;
    expect(chip.textContent).toContain("shot.png");
    fireEvent.click(chip.querySelector("button")!);
    expect(openPathMock).toHaveBeenCalledWith("docs/shot.png", false, "/root");
  });

  it("leaves non-image attachments as plain chips (regression pin)", () => {
    const { container } = render(
      <MessageBubble event={eventWithPayload("user_message", { text: "", attachments: ["notes/thing.txt"] })} />,
    );
    expect(container.querySelector(".attachment-chip")!.textContent).toContain("thing.txt");
    expect(container.querySelector('[data-slot^="preview-"]')).toBeNull();
    expect(readFileMock).not.toHaveBeenCalled();
  });
});

describe("P2.1/P2.3 tool-result affordances (adoption-lock)", () => {
  it("renders one capped image-ref row attempting byte loads", async () => {
    readFileMock.mockResolvedValue({ file_data_base64: "aGk=", file_mime: "image/png" });
    const result = "wrote /tmp/a.png /tmp/b.jpg /tmp/c.webp /tmp/d.png and /tmp/e.png";
    const { container } = render(
      <MessageBubble event={eventWithPayload("agent_tool_result", { tool: "bash", result })} projectRoot="/root" />,
    );
    // Row capped at the first 3 refs; the title lists all five.
    await waitFor(() => expect(container.querySelectorAll('[data-slot="preview-image"] img').length).toBe(3));
    const row = container.querySelector<HTMLElement>(".image-refs-row")!;
    expect(row.getAttribute("title")).toContain("/tmp/e.png");
    expect(row.textContent).toContain("+2 more");
  });

  it("shows Open-live per local URL in the result and forwards the URL", () => {
    const onOpenLiveUrl = vi.fn();
    const result = "Dev server ready at http://localhost:5173/app (logs at https://example.com/x)";
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("agent_tool_result", { tool: "bash", result })}
        onOpenLiveUrl={onOpenLiveUrl}
      />,
    );
    const buttons = container.querySelectorAll<HTMLElement>('[data-slot="preview-live"]');
    expect(buttons.length).toBe(1); // example.com is gated out
    expect(buttons[0].textContent).toContain("localhost:5173");
    fireEvent.click(buttons[0]);
    expect(onOpenLiveUrl).toHaveBeenCalledWith("http://localhost:5173/app");
  });

  it("shows no Open-live affordance for non-local URLs", () => {
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("agent_tool_result", { tool: "bash", result: "see https://example.com/docs" })}
        onOpenLiveUrl={vi.fn()}
      />,
    );
    expect(container.querySelector('[data-slot="preview-live"]')).toBeNull();
  });

  it("renders no new UI for a plain-text result (regression pin)", async () => {
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("agent_tool_result", { tool: "bash", result: "all good" })}
        onOpenLiveUrl={vi.fn()}
      />,
    );
    await waitFor(() => expect(container.textContent).toContain("all good"));
    expect(container.querySelector('[data-slot^="preview-"]')).toBeNull();
    expect(readFileMock).not.toHaveBeenCalled();
  });
});

describe("P2.3 preview_captured badge (adoption-lock)", () => {
  it("adds Open-live next to the badge when the URL passes the gate", () => {
    const onOpenLiveUrl = vi.fn();
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("preview_captured", { url: "http://localhost:3000", bytes: 12800, sha256: "deadbeef" })}
        onOpenLiveUrl={onOpenLiveUrl}
      />,
    );
    expect(container.textContent).toContain("preview · http://localhost:3000"); // receipt unchanged
    const btn = container.querySelector<HTMLElement>('[data-slot="preview-live"]')!;
    expect(btn.textContent).toContain("localhost:3000");
    fireEvent.click(btn);
    expect(onOpenLiveUrl).toHaveBeenCalledWith("http://localhost:3000");
  });

  it("shows no Open-live without the callback or for a non-local URL", () => {
    const withoutCb = render(
      <MessageBubble event={eventWithPayload("preview_captured", { url: "http://localhost:3000", sha256: "x" })} />,
    );
    expect(withoutCb.container.querySelector('[data-slot="preview-live"]')).toBeNull();
    cleanup();
    const remote = render(
      <MessageBubble
        event={eventWithPayload("preview_captured", { url: "https://example.com", sha256: "x" })}
        onOpenLiveUrl={vi.fn()}
      />,
    );
    expect(remote.container.querySelector('[data-slot="preview-live"]')).toBeNull();
  });
});

describe("P2.2 tool-arg Preview-in-panel affordance (adoption-lock)", () => {
  it("turns a read_file path arg into a button calling onPreviewFile", () => {
    const onPreviewFile = vi.fn();
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("agent_tool_call", { tool: "read_file", args: JSON.stringify({ path: "src/main.go" }) })}
        onPreviewFile={onPreviewFile}
      />,
    );
    const btn = container.querySelector<HTMLElement>('button[title="Preview in panel"]')!;
    expect(btn.textContent).toBe("src/main.go");
    fireEvent.click(btn);
    expect(onPreviewFile).toHaveBeenCalledWith("src/main.go");
  });

  it("keeps args without path shape as plain text (regression pin)", () => {
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("agent_tool_call", { tool: "grep", args: JSON.stringify({ pattern: "helloWorld" }) })}
        onPreviewFile={vi.fn()}
      />,
    );
    expect(container.querySelector('button[title="Preview in panel"]')).toBeNull();
    expect(container.textContent).toContain("helloWorld");
  });

  it("keeps path args as plain text when no callback is threaded", () => {
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("agent_tool_call", { tool: "read_file", args: JSON.stringify({ path: "src/main.go" }) })}
      />,
    );
    expect(container.querySelector('button[title="Preview in panel"]')).toBeNull();
    expect(container.textContent).toContain("src/main.go");
  });
});

// Odo DX wave (Feature 4): ANSI agent_text — SGR payloads render via
// renderAnsi → dangerouslySetInnerHTML with escaped entities; plain text
// keeps the markdown path (regression pin).
describe("ANSI agent_text (Odo DX wave, Feature 4)", () => {
  it("renders SGR colors as inline-style spans", () => {
    const { container } = render(<MessageBubble event={event("agent_text", "\x1b[1;32m100% tests passed\x1b[0m")} />);
    const ansi = container.querySelector<HTMLElement>(".ansi-text")!;
    expect(ansi).not.toBeNull();
    const span = ansi.querySelector("span")!;
    expect(span.getAttribute("style")).toContain("font-weight");
    expect(span.getAttribute("style")).toContain("#4e9a06");
    expect(span.textContent).toBe("100% tests passed");
  });

  it("escapes HTML entities inside colored output (no injection)", () => {
    const { container } = render(<MessageBubble event={event("agent_text", "\x1b[31m<b>not-bold</b> & \"q\"\x1b[0m")} />);
    const ansi = container.querySelector<HTMLElement>(".ansi-text")!;
    // The source tag survives as inert TEXT, never a real <b> element.
    expect(ansi.querySelector("b")).toBeNull();
    expect(ansi.textContent).toBe('<b>not-bold</b> & "q"');
  });

  it("keeps plain text on the markdown path (regression pin)", () => {
    const { container } = render(<MessageBubble event={event("agent_text", "plain **bold** text")} />);
    expect(container.querySelector(".ansi-text")).toBeNull();
  });
});

// P1 borrow #6 (turn-fork, quad-audit follow-up): the hover GitFork
// affordance on user_message bubbles — mount gating, the seq argument,
// the busy flip, and the disabled-while-running guard (Retry pattern).
describe("fork affordance (bubble-fork)", () => {
  it("renders on user bubbles only when onForkMessage is threaded", () => {
    const withCb = render(
      <MessageBubble event={eventWithPayload("user_message", { text: "forkable" })} onForkMessage={() => {}} />,
    );
    expect(withCb.container.querySelector(".bubble-fork")).not.toBeNull();
    withCb.unmount();

    const withoutCb = render(<MessageBubble event={eventWithPayload("user_message", { text: "no fork" })} />);
    expect(withoutCb.container.querySelector(".bubble-fork")).toBeNull();
    withoutCb.unmount();

    const agent = render(
      <MessageBubble event={event("agent_text", "agent bubble")} onForkMessage={() => {}} />,
    );
    expect(agent.container.querySelector(".bubble-fork")).toBeNull();
  });

  it("reveal rides group-hover/bubble like the copy chip", () => {
    const { container } = render(
      <MessageBubble event={eventWithPayload("user_message", { text: "hover me" })} onForkMessage={() => {}} />,
    );
    const btn = container.querySelector<HTMLElement>(".bubble-fork")!;
    expect(btn.className).toContain("opacity-0");
    expect(btn.className).toContain("group-hover/bubble:opacity-100");
  });

  it("hands the bubble's OWN seq to the handler and flips to the busy label until it settles", async () => {
    let resolve: () => void = () => {};
    const onFork = vi.fn(() => new Promise<void>((r) => { resolve = r; }));
    const evseq = { ...eventWithPayload("user_message", { text: "branch here" }), seq: 41 };
    const { container } = render(<MessageBubble event={evseq} onForkMessage={onFork} />);
    const btn = container.querySelector<HTMLButtonElement>(".bubble-fork")!;
    expect(btn.getAttribute("aria-label")).toBe("Fork conversation from message #41");
    fireEvent.click(btn);
    expect(onFork).toHaveBeenCalledTimes(1);
    expect(onFork).toHaveBeenCalledWith(41);
    expect(btn.getAttribute("aria-label")).toBe("Forking…");
    expect(btn.disabled).toBe(true);
    resolve();
    await waitFor(() => expect(btn.getAttribute("aria-label")).toBe("Fork conversation from message #41"));
    expect(btn.disabled).toBe(false);
  });

  it("disables with the Retry-pattern busy tooltip while the agent runs", () => {
    const onFork = vi.fn();
    const { container } = render(
      <MessageBubble
        event={eventWithPayload("user_message", { text: "mid run" })}
        onForkMessage={onFork}
        agentRunning={true}
      />,
    );
    const btn = container.querySelector<HTMLButtonElement>(".bubble-fork")!;
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("title")).toContain("Agent busy");
    fireEvent.click(btn);
    expect(onFork).not.toHaveBeenCalled();
  });

  it("clears the busy label on a rejected fork (the refusal path keeps the button alive)", async () => {
    const onFork = vi.fn(() => Promise.reject(new Error("fork_conversation: past the journal end")));
    const { container } = render(
      <MessageBubble event={eventWithPayload("user_message", { text: "refused" })} onForkMessage={onFork} />,
    );
    const btn = container.querySelector<HTMLButtonElement>(".bubble-fork")!;
    fireEvent.click(btn);
    await waitFor(() => expect(btn.disabled).toBe(false));
    expect(btn.getAttribute("aria-label")).toContain("Fork conversation");
  });
});
