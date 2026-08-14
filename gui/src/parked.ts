// W6 (goal queue): the QueueDock's read side — a TypeScript port of the
// daemon's deriveParkedGoals (internal/ipc/parked.go). The durable queue
// is journal-derived: user_message{park:true, non-empty text} rows minus
// seqs consumed by any run_prompt{goal_seqs} or parked_goal_dropped
// {goal_seq} row, in seq order. Deriving from the events already in
// memory (bootstrap replay + poll appends) means a daemon restart or a
// workstream switch repopulates the dock with no extra IPC.

import type { OdoEvent, ParkedGoal } from "./types";

export function deriveParkedGoals(events: OdoEvent[]): ParkedGoal[] {
  const goals: ParkedGoal[] = [];
  const consumed = new Set<number>();
  for (const e of events) {
    if (e.type === "user_message" && e.payload?.park && (e.payload?.text ?? "").trim() !== "") {
      goals.push({ seq: e.seq, text: e.payload.text! });
    } else if (e.type === "review_action") {
      if (e.payload?.action === "run_prompt") {
        for (const s of e.payload.goal_seqs ?? []) consumed.add(s);
      } else if (e.payload?.action === "parked_goal_dropped" && e.payload.goal_seq != null) {
        consumed.add(e.payload.goal_seq);
      }
    }
  }
  return goals.filter((g) => !consumed.has(g.seq));
}
