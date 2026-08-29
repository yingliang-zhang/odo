import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

// P1.1 + P1.5 (docs/design/adoption-lock.md) at the App seam.
//
// P1.1: a palette journal hit whose root is NOT the active project takes
// App's one-flight foreign-switch path (bootstrap with target root + the
// hit's workstream — the Sidebar.tsx:374-382 contract), and then ⌘F opens
// prefilled with the typed query. P1.5: a poll error whose string matches
// errors.ts renders the summarized STICKY banner (raw in title, explicit ×).
//
// The api seam is mocked module-wide, same pattern as
// app_keepalive.test.tsx.
const { stubs, PURE_API } = vi.hoisted(() => ({
  stubs: new Map<string, Mock>(),
  PURE_API: new Set(["unwrap", "errorMessage"]),
}));
vi.mock("./api", async (importOriginal) => {
  const real = (await importOriginal()) as Record<string, unknown>;
  const plain: Record<string, unknown> = { ...real };
  return new Proxy(plain, {
    get(target, key, recv) {
      if (typeof key !== "string" || PURE_API.has(key) || !(key in target)) {
        return Reflect.get(target, key, recv);
      }
      let fn = stubs.get(key);
      if (!fn) {
        fn = vi.fn(async () => ({ ok: true }));
        stubs.set(key, fn);
      }
      return fn;
    },
  });
});

import App from "./App";
// jsdom lacks layout APIs the shell touches on mount (same shims as
// app_keepalive.test.tsx).
Element.prototype.scrollIntoView ??= () => {};
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const stubApi = (name: string): Mock => {
  let fn = stubs.get(name);
  if (!fn) {
    fn = vi.fn(async () => ({ ok: true }));
    stubs.set(name, fn);
  }
  return fn;
};

const PROJ1 = { root: "/tmp/proj", name: "odo", added: "2026-01-01" };
const PROJ2 = { root: "/tmp/proj2", name: "splat", added: "2026-01-02" };
const WS1 = { id: 1, project_id: 1, name: "main", status: "active" };
const WS7 = { id: 7, project_id: 2, name: "feat-diff", status: "active" };
const CONV1 = { id: 1, workstream_id: 1, epoch: 1, state: "active" };
const CONV7 = { id: 7, workstream_id: 7, epoch: 1, state: "active" };

// One journal hit living in proj2/feat-diff — Enter must switch there.
const HIT = {
  event: {
    id: 900,
    conversation_id: 7,
    seq: 41,
    type: "user_message" as const,
    payload: { text: "fold markers regression notes live here" },
    created_at: "2026-08-29T09:00:00Z",
  },
  workstream_id: 7,
  workstream_name: "feat-diff",
  conversation_id: 7,
};

beforeEach(() => {
  cleanup();
  stubs.clear();
  localStorage.clear();
  localStorage.setItem("odo-active-project", PROJ1.root);
  stubApi("listProjects").mockResolvedValue([PROJ1, PROJ2]);
  stubApi("bootstrap").mockImplementation((root?: string, wsId?: number) => {
    if (root === PROJ2.root && wsId != null) {
      return Promise.resolve({
        ok: true,
        project: { id: 2, name: PROJ2.name, root_path: PROJ2.root },
        workstream: WS7,
        conversation: CONV7,
        events: [],
      });
    }
    return Promise.resolve({
      ok: true,
      project: { id: 1, name: PROJ1.name, root_path: PROJ1.root },
      workstream: WS1,
      conversation: CONV1,
      events: [],
    });
  });
  stubApi("listWorkstreams").mockImplementation((root?: string) =>
    Promise.resolve({ ok: true, workstreams: root === PROJ2.root ? [WS7] : [WS1] }),
  );
  stubApi("searchEvents").mockImplementation((_text?: string, root?: string) =>
    Promise.resolve({ ok: true, search_results: root === PROJ2.root ? [HIT] : [] }),
  );
});

describe("P1.1 palette journal search (App seam)", () => {
  it("Enter on a hit one-flight switches project+workstream and opens ⌘F prefilled", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Describe the change you want…");

    fireEvent.keyDown(window, { key: "k", metaKey: true });
    const paletteInput = await screen.findByPlaceholderText("Type a command…");
    fireEvent.change(paletteInput, { target: { value: "fold markers" } });

    // Debounced fan-out (250 ms) → the row arrives tagged with its project.
    const row = await screen.findByText(/fold markers regression notes/, undefined, { timeout: 3000 });
    expect(row.closest(".palette-journal-row")?.textContent).toContain("splat · feat-diff · user_message");
    expect(stubApi("searchEvents")).toHaveBeenCalledWith("fold markers", PROJ2.root);
    expect(stubApi("searchEvents")).toHaveBeenCalledWith("fold markers", PROJ1.root);

    // No action matches the query → Enter selects the first journal row.
    fireEvent.keyDown(paletteInput, { key: "Enter" });

    // One-flight foreign switch: a single bootstrap carrying BOTH roots'
    // parts (target root + workstream id) — the Sidebar one-flight path.
    await waitFor(() => expect(stubApi("bootstrap")).toHaveBeenCalledWith(PROJ2.root, 7, undefined), {
      timeout: 3000,
    });

    // ⌘F opens prefilled with the typed query, over the switched transcript.
    const find = (await screen.findByLabelText("Find in conversation", undefined, { timeout: 3000 })) as HTMLInputElement;
    expect(find.value).toBe("fold markers");
  });

  it("a query shorter than 2 chars never reaches search_events", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Describe the change you want…");
    fireEvent.keyDown(window, { key: "k", metaKey: true });
    const paletteInput = await screen.findByPlaceholderText("Type a command…");
    fireEvent.change(paletteInput, { target: { value: "f" } });
    const { promise: debounceWait, resolve: debounceDone } = Promise.withResolvers<void>();
    window.setTimeout(debounceDone, 400); // past the 250 ms search debounce
    await debounceWait;
    expect(stubApi("searchEvents").mock.calls.length).toBe(0);
    expect(document.querySelector(".palette-group-label")).toBeNull();
  });
});

describe("P1.5 summarized sticky error banner (App seam)", () => {
  it("a socket-shaped poll failure renders summary + action, raw on hover, × dismiss", async () => {
    // First poll fails with the bridge's dead-socket shape…
    stubApi("pollEvents").mockRejectedValue(
      new Error("connect /tmp/proj/.odo/odo.sock: Connection refused (os error 61)"),
    );
    render(<App />);
    await screen.findByPlaceholderText("Describe the change you want…");

    // …the poll loop surfaces it as "poll failed: …" → errors.ts classifies.
    const banner = await screen.findByRole("alert", undefined, { timeout: 5000 });
    expect(banner.textContent).toContain("Daemon unreachable — its socket is dead");
    expect(banner.textContent).toContain("respawns");
    expect(banner.getAttribute("data-sticky")).toBe("true");
    const raw = banner.querySelector("span")!;
    expect(raw.getAttribute("title")).toContain("connect /tmp/proj/.odo/odo.sock");

    // The daemon-down banner is a different surface entirely.
    fireEvent.click(screen.getByLabelText("Dismiss error"));
    expect(banner.isConnected).toBe(false);
  });

  it("an unclassified error keeps the raw-text banner (no summary, no sticky)", async () => {
    stubApi("pollEvents").mockRejectedValue(new Error("frobnicate exploded"));
    render(<App />);
    await screen.findByPlaceholderText("Describe the change you want…");
    const banner = await screen.findByRole("alert", undefined, { timeout: 5000 });
    expect(banner.textContent).toContain("poll failed: frobnicate exploded");
    expect(banner.getAttribute("data-sticky")).toBeNull();
    fireEvent.click(screen.getByLabelText("Dismiss error"));
    expect(banner.isConnected).toBe(false);
  });
});
