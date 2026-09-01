// D5b (A4 D6 + A2-4): the shared useK8sPoll hook — poll contract pins:
// visibility gate (no consumer ⇒ no forks), the REQUIRED in-flight guard
// (two rapid polls ⇒ one invoke), the one-shot visible-edge refetch, the
// sticky off-latch, and k8s_batch_status riding the same fan.

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { useK8sPoll, K8S_POLL_INTERVAL } from "./k8s";
import { k8sStatus, k8sBatchStatus } from "./api";
import type { K8sStatusResponse, K8sBatchStatusResponse } from "./types";

vi.mock("./api", () => ({
  k8sStatus: vi.fn(),
  k8sBatchStatus: vi.fn(),
}));
const mockedStatus = vi.mocked(k8sStatus);
const mockedBatch = vi.mocked(k8sBatchStatus);

const OFF_STATUS: K8sStatusResponse = { ok: true, k8s_status: { available: false, reason: "off" } };
const OFF_BATCH: K8sBatchStatusResponse = { ok: true, k8s_batch_status: { available: false, reason: "off" } };

beforeEach(() => {
  vi.useFakeTimers();
  mockedBatch.mockResolvedValue(OFF_BATCH);
});
afterEach(() => {
  // RTL auto-cleanup doesn't run without globals:true — each hook
  // instance's listener set would leak into later tests' timers.
  cleanup();
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("useK8sPoll", () => {
  it("forks nothing while the gate is closed (off-by-config ⇒ no polling)", async () => {
    mockedStatus.mockResolvedValue(OFF_STATUS);
    renderHook(() => useK8sPoll("/p", /* configured */ false, /* visible */ true));
    await act(async () => {});
    await act(async () => { vi.advanceTimersByTime(3 * K8S_POLL_INTERVAL); });
    expect(mockedStatus).not.toHaveBeenCalled();
    expect(mockedBatch).not.toHaveBeenCalled();
  });

  it("in-flight guard: an unresolved fetch skips the tick AND pollNow (A4 D6)", async () => {
    let release!: (v: K8sStatusResponse) => void;
    mockedStatus.mockImplementation(() => new Promise<K8sStatusResponse>((r) => { release = r; }));
    const { result } = renderHook(() => useK8sPoll("/p", true, true));
    await act(async () => {});
    expect(mockedStatus).toHaveBeenCalledTimes(1); // the true-edge immediate fetch

    // Two rapid polls + a full cadence while in-flight ⇒ still ONE invoke.
    act(() => result.current.pollNow());
    act(() => result.current.pollNow());
    await act(async () => { vi.advanceTimersByTime(2 * K8S_POLL_INTERVAL); });
    expect(mockedStatus).toHaveBeenCalledTimes(1);

    // Release: the next cadence resumes normally.
    await act(async () => release({ ok: true, k8s_status: { available: true, jobs: [], pods: [], truncated: false, fetched_unix: 1 } }));
    await act(async () => { vi.advanceTimersByTime(K8S_POLL_INTERVAL); });
    expect(mockedStatus).toHaveBeenCalledTimes(2);
  });

  it("visible edge refetches immediately, hidden edge stops the cadence", async () => {
    mockedStatus.mockResolvedValue(OFF_STATUS);
    const { rerender } = renderHook(({ visible }) => useK8sPoll("/p", true, visible), {
      initialProps: { visible: false },
    });
    await act(async () => { vi.advanceTimersByTime(2 * K8S_POLL_INTERVAL); });
    expect(mockedStatus).not.toHaveBeenCalled();

    await act(async () => { rerender({ visible: true }); });
    expect(mockedStatus).toHaveBeenCalledTimes(1); // one-shot transition fetch

    await act(async () => { rerender({ visible: false }); });
    await act(async () => { vi.advanceTimersByTime(3 * K8S_POLL_INTERVAL); });
    expect(mockedStatus).toHaveBeenCalledTimes(1); // cadence stopped
  });

  it("daemon off latches sticky: polling stops after the first off answer", async () => {
    mockedStatus.mockResolvedValue(OFF_STATUS);
    const { result } = renderHook(() => useK8sPoll("/p", true, true));
    await act(async () => {});
    expect(mockedStatus).toHaveBeenCalledTimes(1);
    expect(result.current.daemonOff).toBe(true);
    await act(async () => { vi.advanceTimersByTime(3 * K8S_POLL_INTERVAL); });
    expect(mockedStatus).toHaveBeenCalledTimes(1);
  });

  it("k8s_batch_status rides the same fan once per tick", async () => {
    mockedStatus.mockResolvedValue(OFF_STATUS);
    mockedBatch.mockResolvedValue({ ok: true, k8s_batch_status: { available: true, batches: [], truncated: false } });
    renderHook(() => useK8sPoll("/p", true, true));
    await act(async () => {});
    expect(mockedBatch).toHaveBeenCalledTimes(1);
    await act(async () => { vi.advanceTimersByTime(K8S_POLL_INTERVAL); });
    // ...except the status payload's off latch freezes BOTH polls (the
    // daemon-off stop is whole-fan, not per-endpoint).
    expect(mockedBatch).toHaveBeenCalledTimes(1);
  });
});
