// P2.4 (docs/design/adoption-lock.md, 2026-08-29): the Runs tab's read
// side — a pure fold over the journaled event stream, same journal-replay
// posture as parked.ts / steer_queue.ts (zero new IPC, zero new daemon
// state; a daemon restart or workstream switch re-derives the table from
// the bootstrap replay alone). Run grammar: a run OPENS on a plain
// composer send (user_message — mirrors steer_queue.ts's
// latestStarterIndex: steers and parked goals never start runs) or a
// review_action{action:"run_prompt"} receipt (continuation / retry /
// parked-goal dequeue), and CLOSES on the next agent_done{summary} |
// agent_error{error} — an agent_error carrying odo:true is a daemon
// advisory (journalRunAdvisory), not a terminal: it closes nothing and
// produces no row (same semantic as switch_cache.terminalError, which
// walks past advisory rows; UX-3b). Measured tokens exist ONLY on D3 loop_run_usage
// receipts (internal/ipc/loop_journal.go) — plain chat runs journal no
// usage row, so they fall back to the send's total_prompt_bytes as a
// clearly-labeled estimate, never a fabricated token count.

import type { EventPayload, OdoEvent } from "./types";

// One folded run. Times are the journaled ISO strings verbatim (the panel
// parses them for clocks/duration); goal is a single-line excerpt.
export interface RunRow {
  startSeq: number;
  endSeq?: number;
  startedAt: string;
  finishedAt?: string;
  status: "running" | "ok" | "error";
  goal: string;
  origin?: string;
  endSummary?: string;
  endError?: string;
  promptBytesEst?: number;
  // Measured D3 usage when a loop_run_usage receipt landed (duplicate
  // receipts per covers_spawn_seq fold newest-wins, Go parity);
  // "unavailable" is the fail-soft row (usage_available:false) — the
  // estimate stays pending and usageReason names why.
  usage?:
    | { input: number; output: number; cacheRead: number; cacheWrite: number; costUsd?: number }
    | "unavailable";
  usageReason?: string;
}

const GOAL_EXCERPT = 80;

// Single-line, 80-char clip of a send's text — the row's glanceable goal.
function excerpt(text: string): string {
  const flat = text.trim().replace(/\s+/g, " ");
  return flat.length > GOAL_EXCERPT ? `${flat.slice(0, GOAL_EXCERPT)}…` : flat;
}

// run_prompt receipts journal NO text field (the goal is the drained
// steer / parked goal, which steer_queue.ts reconstructs where it
// matters); the row falls back to the origin label, e.g.
// "continuation of seq 12".
function originLabel(p: EventPayload): string {
  const origin = (p.origin ?? "run prompt").replace(/_/g, " ");
  const seq = p.steer_seqs?.[0] ?? p.goal_seqs?.[0];
  return seq != null ? `${origin} of seq ${seq}` : origin;
}

function startRun(ev: OdoEvent, p: EventPayload): RunRow {
  const run: RunRow = {
    startSeq: ev.seq,
    startedAt: ev.created_at,
    status: "running",
    goal: p.action === "run_prompt" ? originLabel(p) : excerpt(p.text ?? ""),
  };
  if (typeof p.origin === "string") run.origin = p.origin;
  if (typeof p.total_prompt_bytes === "number" && p.total_prompt_bytes > 0) {
    run.promptBytesEst = p.total_prompt_bytes;
  }
  return run;
}

// Receipt target (loop_journal.go): covers_spawn_seq names the spawn row
// whose estimate the receipt REPLACES — an exact startSeq match when one
// journaled as a run start here. covers_spawn_seq 0 ⇒ the round/task
// fallback: the run open when the receipt journaled, else the latest run
// started before it (loop spawns journal their own loop_event kinds —
// loop_task_spawn / loop_fix_spawn — so plain runs resolve this way).
function usageTarget(
  runs: RunRow[],
  byStart: Map<number, RunRow>,
  coversSpawn: number,
  usageSeq: number,
): RunRow | null {
  if (coversSpawn > 0) {
    const exact = byStart.get(coversSpawn);
    if (exact != null) return exact;
  }
  let candidate: RunRow | null = null;
  for (const run of runs) {
    // startSeq ascending
    if (run.startSeq >= usageSeq) break;
    if (run.endSeq == null || run.endSeq > usageSeq) return run; // open at that seq
    candidate = run;
  }
  return candidate;
}

export function deriveRuns(events: OdoEvent[], cap: number = 100): RunRow[] {
  // seq order, defensive against an unsorted prop (poll appends arrive
  // ordered, bootstrap replay is ordered — the fold asserts nothing).
  const sorted = [...events].sort((a, b) => a.seq - b.seq);
  const runs: RunRow[] = []; // fold order = startSeq ascending
  const byStart = new Map<number, RunRow>();
  const receipts: { seq: number; p: EventPayload }[] = [];
  let open: RunRow | null = null;

  for (const ev of sorted) {
    const p: EventPayload = ev.payload ?? {};
    if (ev.type === "user_message" && !p.steer && !p.park && (p.text ?? "").trim() !== "") {
      open = startRun(ev, p);
      runs.push(open);
      byStart.set(ev.seq, open);
    } else if (ev.type === "review_action" && p.action === "run_prompt") {
      open = startRun(ev, p);
      runs.push(open);
      byStart.set(ev.seq, open);
    } else if (ev.type === "agent_done" || (ev.type === "agent_error" && p.odo !== true)) {
      // A terminal closes the currently open run; consecutive terminals
      // (double drain receipts, replay overlap) close nothing — defensive
      // no-op, never an error. Advisory rows (odo:true) are filtered at
      // the arm above: closing an open run on a daemon advisory would
      // fabricate a failure row for a run that later ends fine.
      if (open == null) continue;
      open.endSeq = ev.seq;
      open.finishedAt = ev.created_at;
      if (ev.type === "agent_done") {
        open.status = "ok";
        if (typeof p.summary === "string" && p.summary !== "") open.endSummary = p.summary;
      } else {
        open.status = "error";
        if (typeof p.error === "string" && p.error !== "") open.endError = p.error;
      }
      open = null;
    } else if (ev.type === "loop_event" && p.kind === "loop_run_usage") {
      receipts.push({ seq: ev.seq, p });
    }
  }

  for (const { seq, p } of receipts) {
    const target = usageTarget(runs, byStart, p.covers_spawn_seq ?? 0, seq);
    if (target == null) continue;
    if (p.usage_available === false) {
      target.usage = "unavailable";
      target.usageReason = typeof p.reason === "string" ? p.reason : undefined;
    } else {
      target.usage = {
        input: p.input_tokens ?? 0,
        output: p.output_tokens ?? 0,
        cacheRead: p.cache_read_tokens ?? 0,
        cacheWrite: p.cache_write_tokens ?? 0,
        ...(typeof p.cost_usd === "number" ? { costUsd: p.cost_usd } : {}),
      };
      // A measured receipt landing after a fail-soft one retires the reason.
      target.usageReason = undefined;
    }
  }

  // Newest first — the same scan posture LedgerPanel renders.
  runs.sort((a, b) => b.startSeq - a.startSeq);
  return runs.slice(0, cap);
}
