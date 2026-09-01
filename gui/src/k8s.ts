// D5b (A4 D6 + A2-4): the ONE k8s poller for the whole app. The chip and
// the Jobs tab share this hook's state — one fetch fan per tick, two
// surfaces (GLM D6: "one truth per fetch"), zero stacked calls.
//
// Poll contract (verbatim from the locks):
// - 5s cadence, VISIBILITY-GATED: docVisible && configured && (chip
//   unfolded || jobs tab active) — a folded chip + closed window forks
//   NOTHING (every tick is an N-fork kubectl fan).
// - In-flight guard: a slow cluster (>5s handler, up to 10s) must not
//   stack concurrent k8s_status invokes (A4 D6 REQUIRED fix).
// - pollNow on popover-open / tab-open: one guarded one-shot outside the
//   cadence (D6 + the todo-ops poke precedent).
// - "off" is sticky for the page lifetime once the daemon says so
//   (pref is hand-edited, never hot-flipped mid-page).
// - k8s_batch_status rides the same gate + guard (one fan per tick);
//   batch local reads are cheap, pod-fallback only fires when the CPFS
//   mount is missing.

import { useCallback, useEffect, useRef, useState } from "react";
import { k8sStatus, k8sBatchStatus } from "./api";
import type { K8sStatus, K8sBatchStatus, K8sUnavailableReason } from "./types";

export const K8S_POLL_INTERVAL = 5_000;
const K8S_TRANSPORT_ERR_CAP = 240;

export function capK8sErr(msg: string): string {
  return msg.length > K8S_TRANSPORT_ERR_CAP ? `${msg.slice(0, K8S_TRANSPORT_ERR_CAP)}…` : msg;
}

export interface K8sPollState {
  // Last-good k8s_status (kept through mid-session degrade — A2-2).
  status: K8sStatus | null;
  unavailable: K8sUnavailableReason | null;
  detail: string | null;
  transportErr: string | null;
  // Daemon-reported off latch (sticky); App's settings-level configured
  // flag is passed back through so the chip needn't re-derive it.
  daemonOff: boolean;
  // Last-good batch rows (available:false shapes kept separately so a
  // broken batch sensor never blanks the jobs table and vice versa).
  batch: K8sBatchStatus | null;
  batchTransportErr: string | null;
}

export interface K8sPoll extends K8sPollState {
  // Guarded immediate fetch — the popover-open and tab-open pokes.
  pollNow: () => void;
}

const EMPTY: K8sPollState = {
  status: null,
  unavailable: null,
  detail: null,
  transportErr: null,
  daemonOff: false,
  batch: null,
  batchTransportErr: null,
};

export function useK8sPoll(
  projectRoot: string | null,
  configured: boolean,
  visible: boolean,
): K8sPoll {
  const [state, setState] = useState<K8sPollState>(EMPTY);
  // The guard is a ref (not state): the tick must read it without
  // re-running effects, and an in-flight flag change never re-renders.
  const inFlightRef = useRef(false);
  const daemonOffRef = useRef(false);

  const fetchAll = useCallback(async () => {
    if (daemonOffRef.current || inFlightRef.current) return;
    inFlightRef.current = true;
    try {
      const [stResult, batchResult] = await Promise.allSettled([
        k8sStatus(projectRoot ?? undefined),
        k8sBatchStatus(projectRoot ?? undefined),
      ]);
      setState((prev) => {
        let next = prev;
        if (stResult.status === "fulfilled") {
          const st = stResult.value.ok ? (stResult.value.k8s_status ?? null) : null;
          if (st == null) {
            next = {
              ...next,
              transportErr: capK8sErr(stResult.value.ok ? "empty k8s_status payload" : (stResult.value.error ?? "fetch failed")),
            };
          } else if (!st.available) {
            if (st.reason === "off") {
              daemonOffRef.current = true;
              next = { ...next, daemonOff: true };
            } else {
              next = {
                ...next,
                unavailable: st.reason ?? "unreachable",
                detail: st.detail ?? null,
              };
            }
          } else {
            next = {
              ...next,
              status: st,
              unavailable: null,
              transportErr: null,
              detail: null,
            };
          }
        } else {
          next = {
            ...next,
            transportErr: capK8sErr(stResult.reason instanceof Error ? stResult.reason.message : String(stResult.reason)),
          };
        }
        if (batchResult.status === "fulfilled") {
          const bs = batchResult.value.ok ? (batchResult.value.k8s_batch_status ?? null) : null;
          if (bs != null) {
            next = { ...next, batch: bs, batchTransportErr: null };
          }
        } else {
          next = {
            ...next,
            batchTransportErr: capK8sErr(batchResult.reason instanceof Error ? batchResult.reason.message : String(batchResult.reason)),
          };
        }
        return next;
      });
    } finally {
      inFlightRef.current = false;
    }
  }, [projectRoot]);

  const pollNow = useCallback(() => void fetchAll(), [fetchAll]);

  // Document visibility is not reactive — latch it (the App onVisible
  // posture) so a backgrounded window pauses the fork.
  const [docVisible, setDocVisible] = useState(() => !document.hidden);
  useEffect(() => {
    const onVis = () => setDocVisible(!document.hidden);
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, []);

  // The single gate: pref-configured AND a visible consumer AND a
  // foregrounded window AND not daemon-off. A false edge stops the
  // interval (folded chip + closed tab + backgrounded = zero forks);
  // a true edge refetches instantly (one-shot transition fetch) then
  // resumes the cadence.
  const polling = configured && visible && docVisible && !state.daemonOff;
  useEffect(() => {
    if (!polling) return;
    void fetchAll();
    const timer = window.setInterval(() => void fetchAll(), K8S_POLL_INTERVAL);
    return () => window.clearInterval(timer);
  }, [polling, fetchAll]);

  return { ...state, pollNow };
}
