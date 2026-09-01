// A4 D1: the k8s_namespace chips input — RFC-1123 per-chip + N≤5 client
// validation (daemon re-validates fail-loud), add/remove chips round-
// tripping to the ONE comma string through update_settings, plus the
// context/selector/batch-dir plain-text persistence.

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import SettingsPanel from "./SettingsPanel";
import { getSettings, updateSettings } from "../api";
import type { Settings } from "../types";
import type * as ApiModule from "../api";

vi.mock("../api", async (importOriginal) => {
  const mod = await importOriginal<typeof ApiModule>();
  return {
    ...mod,
    getSettings: vi.fn(),
    updateSettings: vi.fn(),
  };
});
const mockedGet = vi.mocked(getSettings);
const mockedUpdate = vi.mocked(updateSettings);

// vitest runs without globals:true, so RTL's auto-cleanup never fires —
// dialogs would stack across tests in one file (duplicate Save buttons,
// duplicate chip inputs).
afterEach(() => cleanup());

const BASE_SETTINGS: Settings = {
  coding_model: "t9s/kimi-k3",
  coding_provider: "sudo",
  orchestrator_model: "t9s/glm-5.2",
  orchestrator_provider: "sudo",
  omp_timeout: "600",
  review_models: "",
  auto_distill: "on_idle",
  auto_distill_idle_seconds: "120",
  max_concurrent_runs: "4",
  auto_apply: "main",
  k8s_namespace: "default,lab",
  k8s_context: "",
  k8s_job_selector: "app=dsv",
  k8s_batch_dir: "/cpfs/ylzhang/batches",
};

beforeEach(() => {
  vi.clearAllMocks();
  mockedGet.mockResolvedValue({ ok: true, settings: { ...BASE_SETTINGS } });
  mockedUpdate.mockResolvedValue({ ok: true });
});

function renderPanel() {
  return render(<SettingsPanel onClose={() => {}} onSaved={() => {}} />);
}

describe("k8s namespaces chips input", () => {
  it("renders one chip per configured namespace and round-trips removals", async () => {
    renderPanel();
    // General category is active by default; settings load async.
    await waitFor(() => expect(screen.getByText("default")).toBeInTheDocument());
    expect(screen.getByText("lab")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("/cpfs/…/batches")).toHaveValue("/cpfs/ylzhang/batches");

    fireEvent.click(screen.getByRole("button", { name: "Remove lab" }));
    expect(screen.queryByText("lab")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockedUpdate).toHaveBeenCalledTimes(1));
    const sent = mockedUpdate.mock.calls[0][0] as Settings;
    expect(sent.k8s_namespace).toBe("default");
    expect(sent.k8s_batch_dir).toBe("/cpfs/ylzhang/batches");
    expect(sent.k8s_job_selector).toBe("app=dsv");
  });

  it("adds a valid chip and round-trips the joined string", async () => {
    mockedGet.mockResolvedValue({ ok: true, settings: { ...BASE_SETTINGS, k8s_namespace: "default" } });
    renderPanel();
    const input = await screen.findByPlaceholderText("Add namespace…");
    fireEvent.change(input, { target: { value: "gpu-2" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByText("gpu-2")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockedUpdate).toHaveBeenCalledTimes(1));
    expect((mockedUpdate.mock.calls[0][0] as Settings).k8s_namespace).toBe("default,gpu-2");
  });

  it("rejects a non-RFC-1123 chip with a visible error and never commits it", async () => {
    renderPanel();
    const input = await screen.findByPlaceholderText("Add namespace…");
    fireEvent.change(input, { target: { value: "Bad_NS!" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByText(/not an RFC-1123 namespace/)).toBeInTheDocument();
    expect(screen.queryByText("Bad_NS!")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mockedUpdate).toHaveBeenCalledTimes(1));
    expect((mockedUpdate.mock.calls[0][0] as Settings).k8s_namespace).toBe("default,lab");
  });

  it("declines the 6th namespace loudly (the 5-cap is daemon-enforced too)", async () => {
    mockedGet.mockResolvedValue({ ok: true, settings: { ...BASE_SETTINGS, k8s_namespace: "a,b,c,d,e" } });
    renderPanel();
    const input = await screen.findByPlaceholderText("Add namespace…");
    fireEvent.change(input, { target: { value: "f" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByText(/at most 5 namespaces/)).toBeInTheDocument();
    expect(screen.queryByText("f")).toBeNull();
  });
});
