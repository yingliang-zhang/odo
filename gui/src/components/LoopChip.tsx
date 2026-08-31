import { useState } from "react";
import { LoaderCircle } from "lucide-react";
import type { LoopState } from "../loop";
import { actionableLoop, loopMode, DEFAULT_LOOP_MAX_ROUNDS } from "../loop";
import { formatTokens } from "../stats";
import { loopCtl } from "../api";
import { cn } from "../lib/utils";

// M19 (/loop) V1: the conversation's live loop chip above the composer —
// folded journal-only by App (deriveLoopStates) and re-derived wholesale
// on every event batch, so a loop that ran GUI-closed renders identically
// on reopen (C11). Renders the newest actionable loop (active |
// suspended; at most one per conversation, C10); terminal loops have no
// chip — their bookkeeping bubble and the notification are the surfaces.
//
// Stop / resume wire to the daemon's loop_ctl exactly like /loop
// stop|resume (design lock: GUI-only IPC): the daemon journals the row
// and the next poll re-derives the chip. busy is cleared by the IPC
// settling — never by an events-identity change (PlanChip Q6 precedent:
// poll batches during a live run would kill the spinner early).
//
// Design gate (loop_design_gate: human — loop.go:19, loopDesignCtl): a
// tasks-mode loop parks at the locked design until the human gates it.
// The fold exposes the wait as tasks[].designLockSeq; approve/veto call
// the SAME loop_ctl verbs the daemon resolves task-side ("the first
// not-done task with a design lock" — at most one can be open, so the
// chip names no task). amend_design stays unwired here: the daemon verb
// needs the amended design TEXT, which is an editor surface, not a chip
// button; approve the journaled lock or veto the task instead.

interface Props {
  conversationId?: number;
  projectRoot?: string | null;
  loops: LoopState[];
  onChanged: () => void;
  onError: (message: string) => void;
}

// Inline-flex chip, hairline border, dim mono text — the
// AUTO_DISTILL_CHIP_BASE shape. ChatSurface owns its own copy; component
// files never import one-liner class strings across the boundary.
const LOOP_CHIP_BASE =
  "inline-flex items-center self-start gap-1 mx-4 rounded-lg border border-border bg-bg-input px-2.5 py-0.5 font-mono text-[length:var(--text-caption)] text-text-dim";

type ChipAction = "stop" | "resume" | "approve_design" | "veto_design";

export default function LoopChip({ conversationId, projectRoot, loops, onChanged, onError }: Props) {
  const [busy, setBusy] = useState<ChipAction | null>(null);
  const st = actionableLoop(loops);
  if (st == null || conversationId == null) return null;

  const suspended = st.status === "suspended";
  // The human design gate is open exactly while a tasks-mode loop holds a
  // locked design on a not-done task (loopDesignCtl's pending predicate,
  // mirrored — the daemon is authoritative on dispatch).
  const designTask =
    st.status === "active" && loopMode(st) === "tasks"
      ? st.tasks.find((t) => !t.done && t.designLockSeq > 0)
      : undefined;

  const run = async (action: ChipAction) => {
    setBusy(action);
    try {
      const resp = await loopCtl(conversationId, action, { projectRoot: projectRoot ?? undefined });
      if (!resp.ok) {
        onError(resp.error ?? `loop ${action} failed`);
      } else {
        // The journaled row arrives on the next poll — nudge it instead
        // of waiting out the idle cadence.
        onChanged();
      }
    } catch (e) {
      onError(`loop ${action} failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setBusy(null);
    }
  };

  const maxRounds = st.maxRounds > 0 ? st.maxRounds : DEFAULT_LOOP_MAX_ROUNDS;
  return (
    <div
      className={cn("loop-chip", LOOP_CHIP_BASE, suspended ? "is-suspended text-warn" : "is-active")}
      title={
        designTask != null
          ? `loop #${st.id} — task ${designTask.n} awaits the human design gate (folds from the journal; daemon-owned)`
          : `loop #${st.id} — folds from the journal; daemon-owned`
      }
      data-loop-id={st.id}
    >
      {/* The spinner means "the loop is working": busy, or active with no
          pending gate. Parked at the design gate the loop is waiting, so
          the spinner goes invisible. */}
      <LoaderCircle
        size={11}
        aria-hidden
        className={busy != null || (st.status === "active" && designTask == null) ? "spin" : "invisible"}
      />
      <span className="loop-chip-label">
        loop · {loopMode(st)} · {designTask != null ? `design gate · task ${designTask.n}` : st.phase} · round{" "}
        {st.rounds.length}/{maxRounds} · {formatTokens(st.spentTokens)}
      </span>
      {designTask != null && (
        <>
          <button
            type="button"
            className="loop-approve chip-link cursor-pointer border-none bg-transparent p-0 text-accent [font:inherit] leading-none hover:underline"
            aria-label={`Approve task ${designTask.n} design`}
            title="Spawn the implement run for the journaled design lock (loop_ctl approve_design)"
            disabled={busy != null}
            onClick={() => void run("approve_design")}
          >
            Approve design
          </button>
          <button
            type="button"
            className="loop-veto chip-link cursor-pointer border-none bg-transparent p-0 text-accent [font:inherit] leading-none hover:underline"
            aria-label={`Veto task ${designTask.n} design`}
            title="Skip this task without implementing (loop_ctl veto_design)"
            disabled={busy != null}
            onClick={() => void run("veto_design")}
          >
            Veto
          </button>
        </>
      )}
      {suspended && (
        <button
          type="button"
          className="loop-resume chip-link cursor-pointer border-none bg-transparent p-0 text-accent [font:inherit] leading-none hover:underline"
          aria-label="Resume loop"
          title="Clear the suspend and re-tick (loop_ctl resume)"
          disabled={busy != null}
          onClick={() => void run("resume")}
        >
          Resume
        </button>
      )}
      <button
        type="button"
        className="loop-stop chip-link cursor-pointer border-none bg-transparent p-0 text-accent [font:inherit] leading-none hover:underline"
        aria-label="Stop loop"
        title="Terminal stop — cancels the in-flight run; landed diffs stay landed (loop_ctl stop)"
        disabled={busy != null}
        onClick={() => void run("stop")}
      >
        Stop
      </button>
    </div>
  );
}
