// P3.3 (docs/design/adoption-lock.md): the Esc priority registry — pins
// the three behaviors the old inline ladder encoded implicitly: priority
// order (overlay > menu > panel > global), insertion-order tie-breaks
// inside one priority (App's search-before-panel), and disposer semantics
// (unmount removes ownership). Hook pins: closure updates must NOT churn
// the registration slot, and unmount deregisters.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render } from "@testing-library/react";
import {
  ESC_PRIORITY,
  __resetEscLayersForTests,
  dispatchEscape,
  registerEscLayer,
  useEscLayer,
} from "./esc-registry";

beforeEach(() => {
  __resetEscLayersForTests();
  cleanup();
});

describe("registerEscLayer + dispatchEscape", () => {
  it("priority order wins over registration order", () => {
    const calls: string[] = [];
    registerEscLayer({ id: "global", priority: ESC_PRIORITY.global, onEscape: () => calls.push("global") });
    registerEscLayer({ id: "menu", priority: ESC_PRIORITY.menu, onEscape: () => calls.push("menu") });
    registerEscLayer({ id: "overlay", priority: ESC_PRIORITY.overlay, onEscape: () => calls.push("overlay") });
    expect(dispatchEscape()).toBe(true);
    expect(calls).toEqual(["overlay"]);
  });

  it("same priority: earliest registration consumes", () => {
    const calls: string[] = [];
    registerEscLayer({ id: "search", priority: ESC_PRIORITY.panel, onEscape: () => calls.push("search") });
    registerEscLayer({ id: "panel", priority: ESC_PRIORITY.panel, onEscape: () => calls.push("panel") });
    dispatchEscape();
    expect(calls).toEqual(["search"]);
  });

  it("inactive layers are skipped; the topmost ACTIVE layer owns the key", () => {
    const calls: string[] = [];
    registerEscLayer({ id: "menu", priority: ESC_PRIORITY.menu, active: () => false, onEscape: () => calls.push("menu") });
    registerEscLayer({ id: "panel", priority: ESC_PRIORITY.panel, onEscape: () => calls.push("panel") });
    dispatchEscape();
    expect(calls).toEqual(["panel"]);
  });

  it("a missing active predicate means always-active", () => {
    const onEscape = vi.fn();
    registerEscLayer({ id: "menu", priority: ESC_PRIORITY.menu, onEscape });
    expect(dispatchEscape()).toBe(true);
    expect(onEscape).toHaveBeenCalledTimes(1);
  });

  it("the disposer removes exactly its own registration", () => {
    const calls: string[] = [];
    const dispose = registerEscLayer({ id: "menu", priority: ESC_PRIORITY.menu, onEscape: () => calls.push("menu") });
    registerEscLayer({ id: "panel", priority: ESC_PRIORITY.panel, onEscape: () => calls.push("panel") });
    dispose();
    dispatchEscape();
    expect(calls).toEqual(["panel"]);
    // Double-dispose is a no-op, not a splice of an innocent entry.
    dispose();
    dispatchEscape();
    expect(calls).toEqual(["panel", "panel"]);
  });

  it("duplicate ids coexist (registration identity, not id, owns disposal)", () => {
    const calls: string[] = [];
    registerEscLayer({ id: "menu", priority: ESC_PRIORITY.menu, onEscape: () => calls.push("a") });
    registerEscLayer({ id: "menu", priority: ESC_PRIORITY.menu, onEscape: () => calls.push("b") });
    dispatchEscape();
    expect(calls).toEqual(["a"]);
  });

  it("no layers → false; nothing consumed", () => {
    expect(dispatchEscape()).toBe(false);
  });
});

describe("useEscLayer", () => {
  it("closure updates swap in place — the registration tie-break never churns", () => {
    const calls: string[] = [];
    function Harness({ searchActive }: { searchActive: boolean }) {
      // Registered FIRST: on equal priority this layer must keep winning
      // across re-renders that flip its active predicate.
      useEscLayer({
        id: "search",
        priority: ESC_PRIORITY.panel,
        active: () => searchActive,
        onEscape: () => calls.push("search"),
      });
      useEscLayer({
        id: "panel",
        priority: ESC_PRIORITY.panel,
        onEscape: () => calls.push("panel"),
      });
      return null;
    }
    const { rerender } = render(<Harness searchActive={false} />);
    dispatchEscape();
    expect(calls).toEqual(["panel"]); // search inactive → panel owns it
    rerender(<Harness searchActive={true} />); // state flip, no re-register
    dispatchEscape();
    expect(calls).toEqual(["panel", "search"]); // search active AND still first
  });

  it("unmount deregisters — the key falls to the next layer", () => {
    const calls: string[] = [];
    function Menu() {
      useEscLayer({ id: "menu", priority: ESC_PRIORITY.menu, onEscape: () => calls.push("menu") });
      return null;
    }
    function Below() {
      useEscLayer({ id: "panel", priority: ESC_PRIORITY.panel, onEscape: () => calls.push("panel") });
      return null;
    }
    const { unmount } = render(
      <>
        <Menu />
        <Below />
      </>,
    );
    dispatchEscape();
    expect(calls).toEqual(["menu"]);
    unmount();
    cleanup();
    render(<Below />);
    dispatchEscape();
    expect(calls).toEqual(["menu", "panel"]);
  });
});
