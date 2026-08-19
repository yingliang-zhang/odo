// Steer queue (Hermes-style busy-composer queue): the SteerQueue panel's
// read side — the TypeScript counterpart of the daemon's steer ledger
// (internal/ipc/server.go: handleSteering queues, drainRun continues). The
// queue is journal-derived: user_message{steer:true, non-empty text} rows
// minus seqs consumed by any run_prompt{steer_seqs} receipt (a
// continuation or retry run picked them up) or closed by
// steer_dropped{steer_seqs | steer_seq} (drain abandonment — the cause
// field — or a human drop). Deriving from the events already in memory
// (bootstrap replay + poll appends) means a daemon restart or a
// workstream switch repopulates the panel with no extra IPC.

import type { OdoEvent, QueuedSteer } from "./types";

export function deriveSteerQueue(events: OdoEvent[]): QueuedSteer[] {
  const steers: QueuedSteer[] = [];
  const consumed = new Set<number>();
  for (const e of events) {
    if (e.type === "user_message" && e.payload?.steer && (e.payload?.text ?? "").trim() !== "") {
      steers.push({ seq: e.seq, text: e.payload.text! });
    } else if (e.type === "review_action") {
      if (e.payload?.action === "run_prompt") {
        for (const s of e.payload.steer_seqs ?? []) consumed.add(s);
      } else if (e.payload?.action === "steer_dropped") {
        // Two closure shapes share one derivation: the drain's batch
        // abandonment (steer_seqs, with a cause) and the human drop
        // (single steer_seq), so an agent-side drop of several queued
        // steers reconciles exactly like repeating the human one.
        if (e.payload.steer_seq != null) consumed.add(e.payload.steer_seq);
        for (const s of e.payload.steer_seqs ?? []) consumed.add(s);
      }
    }
  }
  return steers.filter((s) => !consumed.has(s.seq));
}

// The latest run-STARTING row's INDEX: a plain composer send (steers and
// parked goals never start runs) or a journaled run_prompt receipt (a
// continuation, retry, or parked-goal dequeue). deriveActivePrompt and
// latestRunSteerSeqs share this scan so the pinned card's text and its
// "Processing N queued steers" count always describe the same run; the
// index additionally lets the retry case re-derive its predecessor's
// prompt from the journal prefix.
function latestStarterIndex(events: OdoEvent[]): number {
  for (let i = events.length - 1; i >= 0; i -= 1) {
    const e = events[i];
    if (e.type === "user_message" && !e.payload?.steer && !e.payload?.park && (e.payload?.text ?? "").trim() !== "") {
      return i;
    }
    if (e.type === "review_action" && e.payload?.action === "run_prompt") {
      return i;
    }
  }
  return -1;
}

// Steer seqs the current run consumed, when it started from a receipt —
// null when the starter was a human prompt (the count belongs to the run
// that DRAINED the queue, never to a later plain send).
export function latestRunSteerSeqs(events: OdoEvent[]): number[] | null {
  const i = latestStarterIndex(events);
  if (i < 0 || events[i].type !== "review_action") return null;
  return events[i].payload?.steer_seqs ?? null;
}

// Text of the prompt the current run is processing. A human send speaks
// for itself; a receipt resolves what the daemon actually sent, mirroring
// drainRun's assembly (internal/ipc/server.go):
//   - continuation: the drained steers, joined with "\n\n";
//   - retry: the predecessor's own prompt (re-derived from the journal
//     prefix — the drain texts are [prior goal, steers]) plus the steers
//     dead-queued at the false stop;
//   - parked-goal dequeue: the goal itself.
// Unknown refs are skipped — a pre-steer-queue journal never carries
// them. Null when nothing qualifies; the caller gates display on
// agentRunning.
export function deriveActivePrompt(events: OdoEvent[]): string | null {
  const i = latestStarterIndex(events);
  if (i < 0) return null;
  const starter = events[i];
  if (starter.type === "user_message") return starter.payload.text ?? null;
  const p = starter.payload;
  const parts: string[] = [];
  if (p.origin === "retry") {
    // daemon: texts = append([meta.goal], steerTexts(queued)...) — the
    // goal the dead run was processing, re-derived here from the journal
    // prefix (its own human send / continuation / parked shape folds
    // through this same function). Without the prefix leg a retry card
    // claimed to process only the steer tail (panel diff #9 R2), and a
    // steerless retry showed no card at all (R3).
    const prior = deriveActivePrompt(events.slice(0, i));
    if (prior != null) parts.push(prior);
  }
  if (p.steer_seqs != null && p.steer_seqs.length > 0) {
    const steers = joinReferencedTexts(events, p.steer_seqs, false);
    if (steers != null) parts.push(steers);
  }
  if (p.goal_seqs != null && p.goal_seqs.length > 0) {
    const goal = joinReferencedTexts(events, p.goal_seqs, true);
    if (goal != null) parts.push(goal);
  }
  return parts.length > 0 ? parts.join("\n\n") : null;
}

function joinReferencedTexts(events: OdoEvent[], seqs: number[], parkedOnly: boolean): string | null {
  const wanted = new Set(seqs);
  const texts: string[] = [];
  for (const e of events) {
    if (e.type !== "user_message" || !wanted.has(e.seq)) continue;
    const text = e.payload?.text ?? "";
    if (text.trim() === "") continue;
    if (parkedOnly && !e.payload?.park) continue;
    texts.push(text);
  }
  return texts.length > 0 ? texts.join("\n\n") : null;
}
