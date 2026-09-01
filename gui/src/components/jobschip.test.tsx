// D5b (A4 D3 + A2-5): the JobsChip as a dumb shared-state consumer —
// per-namespace popover rows (answering ns → count header, failed ns →
// dimmed reason row + capped tail), batch one-liners with click-through
// to the Jobs tab, and pollNow fired on popover open.

import { describe, expect, it, vi, afterEach } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { JobsChip } from "./StatusBar";
import type { K8sPoll, K8sPollState } from "../k8s";
import type { K8sStatus } from "../types";

// Radix portals need the same jsdom stubs ContextPanel tests install.
Element.prototype.scrollIntoView ??= () => {};
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

// vitest runs without globals:true, so RTL's auto-cleanup never fires —
// a chip's radix popover DOM would leak into the next test's queries.
afterEach(() => cleanup());

const IDLE: K8sPollState = {
  status: null,
  unavailable: null,
  detail: null,
  transportErr: null,
  daemonOff: false,
  batch: null,
  batchTransportErr: null,
};

function makeK8s(over: Partial<K8sPollState>): K8sPoll {
  return { ...IDLE, ...over, pollNow: vi.fn() };
}

function makeSnap(over: Partial<K8sStatus> = {}): K8sStatus {
  return {
    available: true,
    jobs: [
      {
        metadata: { name: "train-3dgs-zz42", namespace: "default", creationTimestamp: "2026-09-01T10:00:00Z" },
        status: { succeeded: 1, conditions: [{ type: "Complete", status: "True" }] },
      },
      {
        metadata: { name: "cali-blender-k7", namespace: "lab", creationTimestamp: "2026-09-01T11:50:00Z" },
        status: { active: 1 },
      },
    ],
    pods: [],
    truncated: false,
    fetched_unix: Math.floor(Date.now() / 1000),
    ...over,
  };
}

function renderChip(k8s: K8sPoll, onOpenJobsTab = vi.fn()) {
  const res = render(
    <JobsChip k8s={k8s} fold={{ key: "jobs", hidden: false }} onOpenJobsTab={onOpenJobsTab} />,
  );
  return { ...res, onOpenJobsTab };
}

describe("JobsChip", () => {
  it("fires pollNow when the popover opens (A4 D6)", () => {
    const k8s = makeK8s({ status: makeSnap() });
    renderChip(k8s);
    fireEvent.click(screen.getByRole("button", { name: /Jobs · 2/ }));
    expect(screen.getByRole("dialog", { name: "K8s jobs" })).toBeVisible();
    expect(k8s.pollNow).toHaveBeenCalledTimes(1);
  });

  it("renders one row per CONFIGURED namespace: counts on answering, reason on failed", () => {
    const k8s = makeK8s({
      status: makeSnap({
        namespaces: [
          { name: "default", ok: true, job_count: 1 },
          { name: "lab", ok: true, job_count: 1 },
          { name: "gpu", ok: false, reason: "auth", detail: "Forbidden: jobs.batch is forbidden" },
        ],
      }),
    });
    renderChip(k8s);
    fireEvent.click(screen.getByRole("button", { name: /Jobs/ }));

    expect(screen.getByText("default · 1 job")).toBeVisible();
    expect(screen.getByText("lab · 1 job · 1 active")).toBeVisible();
    // Partial failure degrades PER-ROW — no third chip state (A4 D3).
    expect(screen.getByText("gpu")).toBeVisible();
    expect(screen.getByText(/cluster rejected the credentials/)).toBeVisible();
    expect(screen.getByText("Forbidden: jobs.batch is forbidden")).toBeVisible();
  });

  it("chip face stays count-only; batch count rides as + n (A2-5)", () => {
    const k8s = makeK8s({
      status: makeSnap(),
      batch: {
        available: true,
        batches: [
          { batch: "dsv-transcode", total: 250, done: 180, rate_per_min: 5.3, status: "running", stale: false },
          { batch: "frozen", total: 10, done: 2, status: "running", stale: true, updated_unix: 1 },
        ],
        truncated: false,
      },
    });
    renderChip(k8s);
    const chip = screen.getByRole("button", { name: /Jobs/ });
    expect(chip.textContent).toBe("Jobs · 2 + 1"); // frozen batch ≠ active
    expect(chip.querySelector(".job-progress")).toBeNull();
  });

  it("batch one-liners render under the divider and click through to the Jobs tab", () => {
    const onOpenJobsTab = vi.fn();
    const k8s = makeK8s({
      status: makeSnap(),
      batch: {
        available: true,
        batches: [
          { batch: "dsv-transcode", total: 250, done: 180, rate_per_min: 5.3, status: "running", stale: false },
        ],
        truncated: false,
      },
    });
    renderChip(k8s, onOpenJobsTab);
    fireEvent.click(screen.getByRole("button", { name: /Jobs/ }));
    const row = screen.getByText("dsv-transcode 72% · ETA 14m");
    expect(row).toBeVisible();
    fireEvent.click(row);
    expect(onOpenJobsTab).toHaveBeenCalledTimes(1);
    // Popover closes on navigation (rows and the dialog are gone).
    expect(screen.queryByRole("dialog", { name: "K8s jobs" })).toBeNull();
  });

  it("the 'Open Jobs tab' affordance navigates exactly like a batch row", () => {
    const onOpenJobsTab = vi.fn();
    const k8s = makeK8s({ status: makeSnap() });
    renderChip(k8s, onOpenJobsTab);
    fireEvent.click(screen.getByRole("button", { name: /Jobs/ }));
    fireEvent.click(screen.getByText("Open Jobs tab"));
    expect(onOpenJobsTab).toHaveBeenCalledTimes(1);
  });

  it("renders nothing while daemonOff, and still shows the broken reason row when unavailable with no snapshot", () => {
    renderChip(makeK8s({ daemonOff: true }));
    expect(screen.queryByRole("button", { name: /Jobs/ })).toBeNull();

    renderChip(makeK8s({ unavailable: "ENOENT" }));
    const chip = screen.getByRole("button", { name: /unavailable/ });
    fireEvent.click(chip);
    expect(screen.getByText("kubectl not found on the daemon's PATH")).toBeVisible();
    expect(screen.getByText(/no snapshot yet — retrying every 5s/)).toBeVisible();
  });
});
