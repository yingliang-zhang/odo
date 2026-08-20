// Auto-land pipeline status (design lock: docs/design/pipeline-indicator-lock.md,
// Phase 2: daemon auto_land_started breadcrumbs close the silent window).
// Every pipeline fact the daemon produces is already
// journaled as review_action{actor:"auto_panel"} / memory_update{layer:
// "auto_land"} rows on the conversation and reaches the GUI through the
// bootstrap replay + poll stream it already holds — so the chip derives
// from events + the pending-diff list with zero new IPC and zero GUI-side
// latches. Re-derivation is total: swap the inputs, the output changes.

import type { OdoEvent } from "./types";

export type PipelinePhase =
  | "hidden" // pref off or nothing to show — the chip is absent (empty state list)
  | "queued" // pending, pref on, no auto_panel row yet
  | "in_flight" // non-terminal row newest (started/refresh rows carry the stage)
  | "landing" // moa_review{accept} newest, no accept after
  | "landed" // accept{auto_panel} — transient green flash
  | "blocked" // auto_land_blocked{reason} — sticky while pending
  | "suspended" // latest memory_update{auto_land} is ladder_suspended
  | "revise"; // auto_revise_round{round:N} newest, no terminal

export interface PipelineState {
  diffId: number;
  phase: PipelinePhase;
  reason?: string; // blocked: the journaled reason, verbatim
  round?: number; // revise: the journaled round number
  lastSeq: number; // seq of the event that drove the phase (0 = none yet)
  // landed: journaled created_at + flash window, in ms. Derivation drops
  // expired flashes outright (see below); this field remains so the chip
  // can expire an entry admitted just inside the window — render-lag
  // only, never a second data path.
  landedUntil?: number;
  refreshed?: boolean; // in_flight refinement: refresh_attempted{clean}
  // in_flight refinement (lock Phase 2): auto_land_started names the silent
  // stage just entered — "verify" | "panel". Unknown/future stage values
  // degrade to plain in_flight (forward-compat, lock rule 4).
  stage?: "verify" | "panel";
}

// The daemon's pipeline journals its rows with this actor; human
// accept/reject rows carry none and are deliberately silent here (lock
// rule 3).
const AUTO_ACTOR = "auto_panel";
// Design lock: the landed flash is transient, ≤4s from the journaled row.
export const LANDED_FLASH_MS = 4_000;

interface LadderPosture {
  // Conversation-scoped (ladderState parity, settle.go:251): suspended iff
  // the latest memory_update{layer:"auto_land"} marker is one of a
  // ladder_suspended event; a later ladder_resumed (human accept) clears
  // it.
  suspended: boolean;
  suspendedSeq: number; // seq of the governing ladder_suspended marker (0 = none)
}

function ladderPosture(events: readonly OdoEvent[]): LadderPosture {
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (e.type !== "memory_update" || e.payload?.layer !== "auto_land") continue;
    const suspended = e.payload?.cause === "ladder_suspended";
    return { suspended, suspendedSeq: suspended ? e.seq : 0 };
  }
  return { suspended: false, suspendedSeq: 0 };
}

// Derivation contract — conversation scope (lock rule 2): `events` MUST be
// the ACTIVE conversation's journal stream, the same array the transcript
// and LedgerPanel already render. The scope is a daemon guarantee, not a
// filter: bootstrap replays one conversation wholesale, poll_events
// carries an explicit conversationId, and App replaces/merges events only
// on that boundary — so nothing cross-conversation can arrive here. The
// function deliberately adds no second filter of its own; that would mask
// a violation upstream instead of surfacing it. ladderPosture therefore
// scans a per-conversation stream exactly like ladderState does
// daemon-side.
//
// Visibility contract (lock: "only render when pipelineStates.length > 0"):
// the returned list is the set of states with something to show RIGHT NOW.
// A landed flash whose 4s window ended at `now` is NOT returned — `states
// .length > 0` is the single render gate, and PipelineChip never has to
// null-render a stale derivation.
export function derivePipelineStates(
  events: readonly OdoEvent[],
  pendingDiffIds: readonly number[],
  autoApplyOn: boolean,
  now = Date.now(),
): PipelineState[] {
  if (!autoApplyOn) return [];
  const posture = ladderPosture(events);

  // Chain map (settle.go:593-598 parity): every auto_revise_round row
  // links the diff it evaluated to the chain root — round 1 carries
  // diff_id == origin_diff_id, later rounds the repair product's id with
  // the SAME origin. Blocked/moa/accept rows carry only the evaluated
  // diff_id, so root resolution is what attaches a round-N hard stop to
  // the pending diffs a human is watching (the original stays pending
  // even after a repair lands — "superseded, human-decidable").
  const rootOf = new Map<number, number>();
  for (const e of events) {
    if (e.type !== "review_action") continue;
    const p = e.payload;
    if (p?.actor !== AUTO_ACTOR || p.action !== "auto_revise_round") continue;
    if (p.diff_id != null && p.origin_diff_id != null) rootOf.set(p.diff_id, p.origin_diff_id);
  }
  const root = (diffId: number): number => rootOf.get(diffId) ?? diffId;

  // Latest auto_panel row on this diff's chain, newest-first. A round row
  // matches via either id (its diff_id is the evaluated product, its
  // origin_diff_id the root); every other action matches via root(diff_id).
  const latestChainRow = (diffId: number): OdoEvent | null => {
    const want = rootOf.get(diffId) ?? diffId;
    for (let i = events.length - 1; i >= 0; i--) {
      const e = events[i];
      if (e.type !== "review_action") continue;
      const p = e.payload;
      if (p?.actor !== AUTO_ACTOR) continue;
      // auto_revise_product is chain bookkeeping, not pipeline activity:
      // drainRun journals it (Fix B1, server.go) purely so supersedeChain
      // can find a landed repair product, and the daemon's own loop
      // tracker (loop_run.go) reads it as "revise over — the product's
      // own pipeline decides". Letting it win the scan would park the
      // origin in in_flight forever (it carries no terminal state and
      // nothing ever supersedes it ON the origin), stripping the human's
      // decide-the-superseded-original escape hatch. Skip it; the scan
      // falls through to the governing round/blocked row.
      if (p.action === "auto_revise_product") continue;
      if (p.origin_diff_id != null && p.origin_diff_id === want) return e;
      if (p.diff_id != null && root(p.diff_id) === want) return e;
    }
    return null;
  };

  const states: PipelineState[] = [];
  const tracked = new Set<number>();
  for (const diffId of [...pendingDiffIds].sort((a, b) => a - b)) {
    tracked.add(diffId);
    const latest = latestChainRow(diffId);
    if (!latest) {
      // Pref on and pending: the pipeline fires when the producing run
      // completes — "queued" is the honest label for the silent gap.
      states.push({ diffId, phase: posture.suspended ? "suspended" : "queued", lastSeq: 0 });
      continue;
    }
    const p = latest.payload!;
    const base = { diffId, lastSeq: latest.seq };
    // Suspension is the newest conversation-scoped journal fact: when it
    // governs, every tracked pending diff reports it — uniformly, not per
    // action. The ladder's own blocked{ladder_suspended} echo rows need
    // no special case; LedgerPanel holds the verbatim history (rule 9).
    if (posture.suspended) {
      states.push({ ...base, phase: "suspended" });
      continue;
    }
    switch (p.action) {
      case "accept": {
        // Landing already happened; a still-pending id here means the
        // pending list is one poll stale. Inside the window: flash. Past
        // it: drop the entry — "landed" is transient by lock (rule 7), so
        // the chip must clear, and re-reporting the older chain rows
        // behind it would resurrect stale news.
        const at = Date.parse(latest.created_at);
        const until = Number.isNaN(at) ? 0 : at + LANDED_FLASH_MS;
        if (until > now) states.push({ ...base, phase: "landed", landedUntil: until });
        break;
      }
      case "auto_land_blocked":
        // Sticky for as long as the diff stays pending; reason verbatim.
        states.push({ ...base, phase: "blocked", reason: p.reason });
        break;
      case "auto_revise_round":
        states.push({ ...base, phase: "revise", round: p.round });
        break;
      case "moa_review":
        // A unanimous accept is evidence-before-action: the land follows
        // immediately. Anything else settles into revise/blocked rows;
        // between them the truthful label is "still working".
        states.push({
          ...base,
          phase: p.consensus_verdict === "accept" ? "landing" : "in_flight",
        });
        break;
      case "auto_land_started":
        // Phase 2 liveness breadcrumb: the daemon journaled entry into a
        // silent stage, so the multi-minute window labels as running, not
        // queued. Unknown stage values stay plain in_flight.
        states.push({
          ...base,
          phase: "in_flight",
          ...(p.stage === "verify" || p.stage === "panel" ? { stage: p.stage } : {}),
        });
        break;
      default:
        // refresh_attempted + forward-compat unknowns: non-terminal rows.
        states.push({
          ...base,
          phase: "in_flight",
          refreshed: p.outcome === "clean",
        });
    }
  }

  // A landed diff leaves the pending list on the next poll — the flash
  // must outlive that removal (it is the whole point), so recently-landed
  // ids are tracked from the journal for the remainder of their window.
  // Newest-first: the NEWEST accept of a diff sets the flash (a re-landing
  // within the window must never read the older row's expiry). Same
  // Windows: flashes already expired at `now` are not returned at all
  // (visibility contract above).
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    const p = e.payload;
    if (e.type !== "review_action" || p?.actor !== AUTO_ACTOR) continue;
    if (p.action !== "accept" || p.diff_id == null || tracked.has(p.diff_id)) continue;
    // An accept journaled before the governing suspension is stale news:
    // the ladder's posture outranks its flash, never the reverse.
    if (posture.suspended && e.seq <= posture.suspendedSeq) continue;
    tracked.add(p.diff_id);
    const at = Date.parse(e.created_at);
    const until = Number.isNaN(at) ? 0 : at + LANDED_FLASH_MS;
    if (until <= now) continue;
    states.push({
      diffId: p.diff_id,
      phase: "landed",
      lastSeq: e.seq,
      landedUntil: until,
    });
  }
  return states;
}

// Phase label — ONE wording for every surface that renders a phase:
// the StatusBar chip and its popover rows, and the human-action lock
// indicator on the diff card / inbox row.
export function pipelineLabel(s: PipelineState): string {
  switch (s.phase) {
    case "queued":
      return "auto-land queued…";
    case "in_flight":
      // Phase 2 stage breadcrumbs name the running stage verbatim; a
      // pre-Phase-2 journal (or unknown stage) keeps the coarse label.
      if (s.stage === "verify") return "verify running…";
      if (s.stage === "panel") return "panel reviewing…";
      return s.refreshed ? "refreshed — verify → panel…" : "verify → panel…";
    case "landing":
      return "landing…";
    case "landed":
      return "landed";
    case "blocked":
      return `blocked: ${s.reason ?? "unknown"}`;
    case "suspended":
      return "auto-land suspended";
    case "revise":
      return `repair round ${s.round ?? "?"}`;
    default:
      return s.phase;
  }
}

// Human-action lock (misfire guard): while the daemon pipeline is
// actively working the diff's chain — verify, panel MoA review, or the
// moa-accepted → land window — a human Accept/Reject races the panel
// verdict, so the review surfaces disable their buttons and say why.
// Phases that REQUIRE a human stay actionable: blocked (hard stop reasons
// like protected_path exist precisely to hand the decision over) and the
// suspended ladder (a human accept is its only resume). queued is the
// pre-start gap — nothing is running yet. revise keeps the mid-ladder
// escape hatch: accepting the original ends the ladder early.
export function pipelineHumanLocked(s: PipelineState | undefined): boolean {
  return s != null && (s.phase === "in_flight" || s.phase === "landing");
}
