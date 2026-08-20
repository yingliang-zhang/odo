import { useEffect, useState } from "react";
import { LoaderCircle, X } from "lucide-react";
import type { QueuedSteer } from "../types";
import { strings } from "../strings";

// Steer queue: the composer-area stack for a busy run — the prompt the
// agent is currently processing pinned as a card, with the journaled
// mid-run instructions queued beneath it. Rows are DERIVED by the caller
// from the journaled events (gui/src/steer_queue.ts) — never from a local
// cache — so a daemon restart or a workstream switch repopulates the panel
// on replay. Drop goes to the daemon (drop_queued_steer); the journaled
// steer_dropped row flows back through the poll loop and the row vanishes.
//
// The drop follows the QueueDock precedent: a busy guard against
// double-clicks and an inline two-step confirm for the destructive op
// (no window.confirm), rearmed whenever the derived queue changes.

interface Props {
  // The current run's prompt (journal-derived); pinned as the active card
  // only while the run is live.
  activePrompt: string | null;
  // Steers the current run consumed (>1 → the card labels itself as a
  // joined continuation instead of a single prompt).
  activeSteerCount: number;
  pending: QueuedSteer[];
  agentRunning: boolean;
  // Drop is held while a manual distill occupies the conversation (the
  // daemon would refuse — a distill owns the conversation like a run).
  distillLocked: boolean;
  onDropSteer?: (seq: number) => Promise<void>;
}

export default function SteerQueue({
  activePrompt,
  activeSteerCount,
  pending,
  agentRunning = false,
  distillLocked = false,
  onDropSteer,
}: Props) {
  const [busySeq, setBusySeq] = useState<number | null>(null);
  // Two-step inline drop confirm: the first click arms the row's button,
  // the second lands the drop. Rearmed per row; cleared on reconcile.
  const [dropArmedSeq, setDropArmedSeq] = useState<number | null>(null);

  // The journaled state caught up (or the click raced the drain): release
  // the arm ONLY when the armed row actually left the queue — never on a
  // mere re-derivation. `pending` is memoized on the events array, so any
  // polled stream event hands us a fresh identity; keying the release on
  // identity used to silently disarm between the two confirm clicks
  // exactly while the panel was usable (panel diff #9 finding).
  useEffect(() => {
    setDropArmedSeq((armed) =>
      armed != null && !pending.some((s) => s.seq === armed) ? null : armed,
    );
  }, [pending]);

  // The panel exists only while something is in flight: a live run with a
  // resolvable prompt, or journaled steers waiting for the drain.
  const showActive = activePrompt != null && agentRunning;
  if (!showActive && pending.length === 0) return null;

  const drop = async (seq: number) => {
    if (busySeq != null || distillLocked || onDropSteer == null) return;
    if (dropArmedSeq !== seq) {
      setDropArmedSeq(seq);
      return;
    }
    setBusySeq(seq);
    try {
      await onDropSteer(seq);
    } finally {
      // Deliberately cleared on settle, not on reconcile: a refused drop
      // (a benign race against the drain) must never wedge the row, and a
      // second drop of an already-consumed seq is harmless daemon-side.
      setBusySeq(null);
      setDropArmedSeq(null);
    }
  };

  return (
    <div data-testid="steer-queue-panel" className="steer-queue flex flex-col gap-1.5">
      {showActive && (
        <div
          data-testid="steer-queue-active"
          className="steer-active flex items-start gap-1.5 rounded-lg border border-border bg-bg-input px-2.5 py-1.5 font-mono text-caption text-text-dim shadow-soft"
        >
          <LoaderCircle size={11} className="spin mt-[3px] shrink-0" aria-hidden />
          <span className="shrink-0 font-semibold">
            {activeSteerCount > 1 ? strings.steerQueue.activeJoined(activeSteerCount) : strings.steerQueue.activeLabel}
          </span>
          <span className="queue-row-text text-text" title={activePrompt ?? undefined}>
            {activePrompt}
          </span>
        </div>
      )}
      {pending.length > 0 && (
        <div className="steer-queue-list rounded-lg border border-border bg-bg-input px-2.5 py-1.5 shadow-soft">
          <div className="queue-note">{strings.steerQueue.title(pending.length)}</div>
          <ul className="plan-list">
            {pending.map((s, i) => (
              <li
                key={s.seq}
                data-testid="steer-queue-row"
                className={`plan-row queue-row${busySeq === s.seq ? " busy" : ""}`}
              >
                <span className="queue-pos">
                  #{i + 1}
                  {i === 0 && <span className="queue-next-tag">next</span>}
                </span>
                <span className="queue-row-text" title={s.text}>
                  {s.text}
                </span>
                <span className="queue-actions">
                  <button
                    type="button"
                    className={`queue-act queue-drop${dropArmedSeq === s.seq ? " armed" : ""}`}
                    title={strings.steerQueue.dropTitle}
                    aria-label={
                      dropArmedSeq === s.seq
                        ? `Confirm drop queued steer ${i + 1}`
                        : `Drop queued steer ${i + 1}`
                    }
                    disabled={busySeq != null || distillLocked}
                    onClick={() => void drop(s.seq)}
                  >
                    <X size={10} />
                    {dropArmedSeq === s.seq ? strings.steerQueue.dropConfirm : strings.steerQueue.drop}
                  </button>
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
