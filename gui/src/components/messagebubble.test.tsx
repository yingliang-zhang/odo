import { beforeEach, describe, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";

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

beforeEach(() => {
  cleanup();
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
