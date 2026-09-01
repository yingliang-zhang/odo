import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

// (tri-review P1 #5, 2026-08-24) Keep-alive panel tabs: switching tabs
// must hide, never unmount, the previously active panel.
//
// The api seam is mocked module-wide: every exported function defaults
// to a benign `{ ok: true }` stub, with unwrap/errorMessage keeping their
// real (pure) implementations. Individual stubs are overridden per test
// below. The stub map is created via vi.hoisted because vi.mock factory
// registration runs before any module-body statement executes; the
// proxy target is a PLAIN OBJECT — a Proxy over the module namespace
// object is illegal (namespace properties are non-configurable, so the
// get trap could never return a different value).
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
// jsdom has no layout engine — scrollIntoView is unimplemented, and the
// panel's tab strip scrolls the active tab into view on every switch.
Element.prototype.scrollIntoView ??= () => {};
// Same for ResizeObserver (the tab strip's overflow meter); with no
// layout nothing overflows, so a silent stub is faithful.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

// Same lazy creation as the factory, so a pre-render override lands in
// the exact stub the component will call.
const stubApi = (name: string): Mock => {
  let fn = stubs.get(name);
  if (!fn) {
    fn = vi.fn(async () => ({ ok: true }));
    stubs.set(name, fn);
  }
  return fn;
};

const PROJECT = { id: 1, name: "proj", root_path: "/tmp/proj" };
const WORKSTREAM = { id: 1, project_id: 1, name: "main", status: "ready" };
const CONVERSATION = { id: 1, workstream_id: 1, epoch: 1, state: "idle" };

beforeEach(() => {


  cleanup();
  stubs.clear();
  // localStorage is the in-memory shim from test-setup.ts (see its
  // comment: Node ≥22's experimental global shadows jsdom 30's window
  // under vitest 4).
  localStorage.clear();
  // Boot straight into a session with the panel open on the Wiki tab, so
  // the keep-alive mount set contains exactly {wiki} on first render.
  localStorage.setItem("odo-panel-open", "true");
  localStorage.setItem("odo-panel-tab", "wiki");
  localStorage.setItem("odo-active-project", PROJECT.root_path);
  stubApi("listProjects").mockResolvedValue([
    { root: PROJECT.root_path, name: PROJECT.name, added: "2026-01-01" },
  ]);
  stubApi("bootstrap").mockResolvedValue({
    ok: true,
    project: PROJECT,
    workstream: WORKSTREAM,
    conversation: CONVERSATION,
    events: [],
  });
  stubApi("listWorkstreams").mockResolvedValue({ ok: true, workstreams: [WORKSTREAM] });
});

describe("keep-alive panel tabs (tri-review P1 #5, 2026-08-24)", () => {
  it("lazy-mounts the selected panel and hides—not remounts—it on tab switch", async () => {
    render(<App />);
    // Wiki panel mounts on first activation; readWiki fires for the
    // default user.md reader selection.
    const input = (await screen.findByLabelText("Search wiki")) as HTMLInputElement;

    fireEvent.change(input, { target: { value: "keepme" } });
    expect(input).toHaveValue("keepme");

    // Switching to Changes renders it for the first time (lazy mount)…
    fireEvent.click(screen.getByRole("tab", { name: /^changes/i }));
    await screen.findByText(/No pending diffs/i);
    // …while the wiki panel is hidden, NOT unmounted: the very same DOM
    // node stays connected under a hidden wrapper, draft text intact.
    expect(input.isConnected).toBe(true);
    expect(input.closest("[hidden]")).not.toBeNull();

    // Switching back re-shows the same instance — draft intact — and (the
    // one contract change of 2026-08-25 review P1) re-reads the selection
    // exactly ONCE on the activation edge: hidden keep-alive panels used
    // to serve stale bytes forever; state survives, bytes refresh.
    const readWikiCallsBefore = stubApi("readWiki").mock.calls.length;
    fireEvent.click(screen.getByRole("tab", { name: /^wiki/i }));
    const again = screen.getByLabelText("Search wiki") as HTMLInputElement;
    expect(again).toBe(input);
    expect(again).toHaveValue("keepme");
    expect(stubApi("readWiki").mock.calls.length).toBe(readWikiCallsBefore + 1);
  });

  it("first switch to a tab mounts it exactly once (no eager mounting)", async () => {
    render(<App />);
    await screen.findByLabelText("Search wiki");
    // Memory renders its empty conversation state only after activation;
    // before that, no memory proposals fetch for the panel body (the
    // badge refresh is a separate App-level call) and no files read.
    expect(stubApi("readMemory").mock.calls.length).toBe(0);
    fireEvent.click(screen.getByRole("tab", { name: /^memory/i }));
    await screen.findByText(/No pending memory proposals/i);
    // Back and forth: the second activation re-fetches the batch exactly
    // once (2026-08-25 review P1 activation refresh) — never zero, the
    // old stale-forever behavior, and never a remount's reload.
    const batchCalls = stubApi("memoryProposals").mock.calls.length;
    fireEvent.click(screen.getByRole("tab", { name: /^wiki/i }));
    fireEvent.click(screen.getByRole("tab", { name: /^memory/i }));
    expect(stubApi("memoryProposals").mock.calls.length).toBe(batchCalls + 1);
  });

  it("A3-2 (UX-4): a stored pre-fold 'ledger' selection orphans onto the tasks default", async () => {
    // The allowlist (PANEL_TAB_IDS) derives from the registry — "ledger"
    // left it in the fold, so the panelTab initializer's fallback ("tasks")
    // takes over for anyone holding the old value. One line, per A3-2.4.
    localStorage.setItem("odo-panel-tab", "ledger");
    render(<App />);
    const tasks = await screen.findByRole("tab", { name: /^tasks/i });
    expect(tasks.getAttribute("aria-selected")).toBe("true");
  });
});

describe("panel activation refresh (2026-08-25 review P1)", () => {
  // Every keep-alive panel used to freeze at first-mount bytes: a daemon
  // distill adding an epoch note, a new memory batch, or an externally
  // added skill never surfaced because nothing refetched. On the
  // inactive→active edge each panel re-pulls its surface — these cases
  // pin the three contracts down on the real App.

  const note = (n: number) => ({
    path: `/tmp/proj/wiki/main-epoch-${n}.md`,
    name: `main-epoch-${n}`,
    epoch: n,
    modified_at: "2026-01-01T00:00:00Z",
  });

  it("wiki: notes added while hidden surface on re-activation", async () => {
    stubApi("listWiki").mockResolvedValue({ ok: true, wiki_notes: [note(1)] });
    render(<App />);
    await screen.findByLabelText("Search wiki"); // bootstrap settled
    await screen.findByText("main-epoch-1");
    expect(screen.queryByText("main-epoch-2")).toBeNull();

    fireEvent.click(screen.getByRole("tab", { name: /^changes/i }));
    await screen.findByText(/No pending diffs/i);
    // The daemon writes a new epoch note while the tab is hidden…
    stubApi("listWiki").mockResolvedValue({ ok: true, wiki_notes: [note(1), note(2)] });
    const listCalls = stubApi("listWiki").mock.calls.length;
    fireEvent.click(screen.getByRole("tab", { name: /^wiki/i }));
    await screen.findByText("main-epoch-2");
    expect(stubApi("listWiki").mock.calls.length).toBe(listCalls + 1);
  });

  it("memory: an unchanged batch keeps un-applied Accept/Reject decisions", async () => {
    stubApi("memoryProposals").mockResolvedValue({
      ok: true,
      epoch: 2,
      seq: 5,
      consumed: false,
      proposals: [{ target: "memory.md", rule: "Always test before done." }],
    });
    render(<App />);
    await screen.findByLabelText("Search wiki");
    fireEvent.click(screen.getByRole("tab", { name: /^memory/i }));
    fireEvent.click(await screen.findByRole("button", { name: /^reject$/i }));
    expect((await screen.findByRole("button", { name: /^reject$/i })).className).toContain("selected");

    // Hidden, then re-activated against the SAME batch identity: the
    // refresh must not wipe the in-flight decision.
    fireEvent.click(screen.getByRole("tab", { name: /^wiki/i }));
    fireEvent.click(screen.getByRole("tab", { name: /^memory/i }));
    await screen.findByText("Always test before done.");
    expect((await screen.findByRole("button", { name: /^reject$/i })).className).toContain("selected");
  });

  it("memory: a NEW batch identity resets decisions to the default", async () => {
    const batch = (epoch: number) => ({
      ok: true as const,
      epoch,
      seq: 5,
      consumed: false,
      proposals: [{ target: "memory.md", rule: "Always test before done." }],
    });
    stubApi("memoryProposals").mockResolvedValue(batch(2));
    render(<App />);
    await screen.findByLabelText("Search wiki");
    fireEvent.click(screen.getByRole("tab", { name: /^memory/i }));
    fireEvent.click(await screen.findByRole("button", { name: /^reject$/i }));
    expect((await screen.findByRole("button", { name: /^reject$/i })).className).toContain("selected");

    // The NEXT distill epoch proposes a fresh batch while hidden:
    // decisions for the old one are stale news — default wins.
    stubApi("memoryProposals").mockResolvedValue(batch(3));
    fireEvent.click(screen.getByRole("tab", { name: /^wiki/i }));
    fireEvent.click(screen.getByRole("tab", { name: /^memory/i }));
    await screen.findByText("Always test before done.");
    expect((await screen.findByRole("button", { name: /^reject$/i })).className).not.toContain("selected");
  });

  // A3-2 (UX-4): the ledger.md re-read contract moved to PreviewPanel's
  // activation edge (pinned in previewpanel.test.tsx); the ledger.md file
  // itself opens through the TopBar overflow → Preview pathway.

  it("skills: skills added while hidden re-list on re-activation", async () => {
    stubApi("listSkills").mockResolvedValue({ ok: true, skills: [] });
    render(<App />);
    await screen.findByLabelText("Search wiki");
    fireEvent.click(screen.getByRole("tab", { name: /^skills/i }));
    await screen.findByText(/No skills yet/i);

    fireEvent.click(screen.getByRole("tab", { name: /^wiki/i }));
    stubApi("listSkills").mockResolvedValue({
      ok: true,
      skills: [{ name: "fresh-skill", scope: "project", path: "/tmp/proj/.odo/skills/fresh-skill.md" }],
    });
    fireEvent.click(screen.getByRole("tab", { name: /^skills/i }));
    await screen.findByText("fresh-skill");
  });
});
