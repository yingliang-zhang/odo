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
  "inline-flex items-center self-start gap-1 mt-2.5 mx-4 rounded-lg border border-border bg-bg-input px-2.5 py-0.5 font-mono text-[length:var(--text-caption)] text-text-dim";

export default function LoopChip({ conversationId, projectRoot, loops, onChanged, onError }: Props) {
  const [busy, setBusy] = useState<"stop" | "resume" | null>(null);
  const st = actionableLoop(loops);
  if (st == null || conversationId == null) return null;

  const suspended = st.status === "suspended";

  const run = async (action: "stop" | "resume") => {
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
      title={`loop #${st.id} — folds from the journal; daemon-owned`}
      data-loop-id={st.id}
    >
      <LoaderCircle size={11} aria-hidden className={busy != null || st.status === "active" ? "spin" : "invisible"} />
      <span className="loop-chip-label">
        loop · {loopMode(st)} · {st.phase} · round {st.rounds.length}/{maxRounds} · {formatTokens(st.spentTokens)}
      </span>
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
