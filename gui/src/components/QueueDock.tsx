import { useEffect, useRef, useState } from "react";
import { Play, X } from "lucide-react";
import type { ParkedGoal } from "../types";

// W6 (goal queue): the composer's "Queue · N" chip — the GUI surface of
// the durable parked-goal FIFO. Rows are DERIVED by the caller from the
// journaled events (gui/src/parked.ts, mirroring the daemon's
// deriveParkedGoals) — never from a local cache — so a daemon restart or
// a workstream switch repopulates the dock on replay. Resume/Drop go to
// the daemon (resume_parked_goal / drop_parked_goal); the journaled
// consumption row flows back through the poll loop and the row vanishes.
//
// The shape follows the Plan chip precedent: hairline chip +
// click-away popover, a busy guard against double-clicks, and an inline
// two-step confirm for the destructive op (no window.confirm).

interface Props {
  goals: ParkedGoal[];
  onResume?: (seq: number) => Promise<void>;
  onDrop?: (seq: number) => Promise<void>;
  // Resume is held while a run or a manual distill occupies the
  // conversation (the daemon would refuse); Drop stays available.
  agentRunning?: boolean;
  distillLocked?: boolean;
}

export default function QueueDock({
  goals,
  onResume,
  onDrop,
  agentRunning = false,
  distillLocked = false,
}: Props) {
  const [open, setOpen] = useState(false);
  const [busySeq, setBusySeq] = useState<number | null>(null);
  // Two-step inline drop confirm: the first click arms the row's button,
  // the second lands the drop. Rearmed per row; cleared on reconcile.
  const [dropArmedSeq, setDropArmedSeq] = useState<number | null>(null);
  const rootRef = useRef<HTMLDivElement>(null);

  // Click-away or Escape closes the popover (PlanChip precedent — the
  // slash menu's blur pattern doesn't fit non-composer buttons).
  useEffect(() => {
    if (!open) return;
    const onDocDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDocDown);
    window.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDocDown);
      window.removeEventListener("keydown", onKey);
    };
  }, [open]);

  // The journaled state caught up (or the click raced an auto-dequeue):
  // release the armed/busy rows when the derived queue changes.
  useEffect(() => {
    setDropArmedSeq(null);
  }, [goals]);

  if (goals.length === 0) return null;

  const resumeHeld = agentRunning || distillLocked;

  const resume = async (seq: number) => {
    if (busySeq != null || resumeHeld || onResume == null) return;
    setBusySeq(seq);
    try {
      await onResume(seq);
    } finally {
      // Deliberately cleared on settle, not on reconcile: a refused
      // resume (benign race) must never wedge the row, and a second
      // resume of an already-dequeued seq is harmless daemon-side.
      setBusySeq(null);
    }
  };

  const drop = async (seq: number) => {
    if (busySeq != null || onDrop == null) return;
    if (dropArmedSeq !== seq) {
      setDropArmedSeq(seq);
      return;
    }
    setBusySeq(seq);
    try {
      await onDrop(seq);
    } finally {
      setBusySeq(null);
      setDropArmedSeq(null);
    }
  };

  return (
    <div className="plan-chip-wrap queue-dock" ref={rootRef}>
      <button
        type="button"
        className={`auto-distill-chip plan-chip queue-chip${open ? " open" : ""}`}
        title={`${goals.length} parked goal${goals.length === 1 ? "" : "s"} — queued goals start when the current run finishes`}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        Queue · {goals.length}
      </button>
      {open && (
        <div className="plan-popover queue-popover" role="dialog" aria-label="Parked goals">
          <div className="queue-note">queued goals start when the current run finishes</div>
          <ul className="plan-list">
            {goals.map((g, i) => (
              <li key={g.seq} className={`plan-row queue-row${busySeq === g.seq ? " busy" : ""}`}>
                <span className="queue-pos">
                  #{i + 1}
                  {i === 0 && <span className="queue-next-tag">next</span>}
                </span>
                <span className="queue-row-text" title={g.text}>
                  {g.text}
                </span>
                <span className="queue-actions">
                  <button
                    type="button"
                    className="queue-act queue-resume"
                    title={
                      resumeHeld
                        ? "Resume is held while the conversation is busy — wait for the current run to finish"
                        : `Resume this parked goal now (seq ${g.seq})`
                    }
                    aria-label={`Resume parked goal ${i + 1}`}
                    disabled={busySeq != null || resumeHeld}
                    onClick={() => void resume(g.seq)}
                  >
                    <Play size={10} />
                    Resume
                  </button>
                  <button
                    type="button"
                    className={`queue-act queue-drop${dropArmedSeq === g.seq ? " armed" : ""}`}
                    title={
                      dropArmedSeq === g.seq
                        ? "Click again to drop this goal (the drop is journaled)"
                        : `Drop this parked goal (seq ${g.seq})`
                    }
                    aria-label={
                      dropArmedSeq === g.seq
                        ? `Confirm drop parked goal ${i + 1}`
                        : `Drop parked goal ${i + 1}`
                    }
                    disabled={busySeq != null}
                    onClick={() => void drop(g.seq)}
                  >
                    <X size={10} />
                    {dropArmedSeq === g.seq ? "Drop?" : "Drop"}
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
