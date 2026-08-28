// M19 (/loop) — TS mirror of the daemon's pure fold in
// internal/ipc/loop_journal.go (design lock C1: journal-derived,
// restart-proof, zero in-memory authority). The GUI owns no loop state:
// bootstrap replay + poll batches re-derive wholesale, so a loop that
// ran GUI-closed renders identically on reopen. Behavior drift between
// the two folds is pinned by loop.test.ts against the Go tests.
//
// Wire contract (loop_journal.go header): ONE event type ("loop_event")
// discriminated by payload.kind, with kind/loop_id/mode/actor/
// spent_tokens on every row. The loop's id is the loop_started row's
// SEQ (that row carries loop_id 0 — the journal is append-only); every
// later row carries the real id in its payload. The daemon is the only
// writer of these rows, so keys are absent-or-well-typed: `?? default`
// reads mirror the Go fold's "absent keys read as zero values".
//
// Mirror scope: every fold fact the GUI consumes (chip phase, stop /
// resume affordance, notification watcher). Daemon-tick internals that
// never drive GUI state (infra streak, hold severity, seed diffs) stay
// daemon-owned and are deliberately not folded here.

import type { EventPayload, OdoEvent } from "./types";

// The daemon stamps loop rows and loop-owned pipeline rows with this
// actor (loop_journal.go loopActor).
const LOOP_ACTOR = "auto_loop";
// AUTO_ACTOR is the auto-land panel pipeline actor (loop_journal.go
// autoActor): post-D1 a Mode A fix lands through the full auto-land
// path as auto_panel, so fold attribution must accept both actors —
// gated by the loop_diff_bound{round} row (never by wall-clock).
const AUTO_ACTOR = "auto_panel";

// Kinds that end a loop's autonomous operation (design lock: the
// notification watcher fires on first sight of any of these per loop).
// loop_suspended and loop_budget_exceeded fold to status "suspended" —
// resumable, but the human must act, so they notify like the terminal
// pair.
export const LOOP_TERMINAL_KINDS = [
  "loop_completed",
  "loop_stopped",
  "loop_suspended",
  "loop_budget_exceeded",
] as const;
export type LoopTerminalKind = (typeof LOOP_TERMINAL_KINDS)[number];

// The chip's round denominator fallback: loop_started journals
// max_rounds, but a pre-contract or corrupt row leaves it absent —
// mirror the daemon's loopMaxRounds default (display only, never a gate).
export const DEFAULT_LOOP_MAX_ROUNDS = 10;

export type LoopStatus = "active" | "suspended" | "completed" | "stopped";

// One audit round's fold-relevant record (loopRound parity).
export interface LoopRound {
  seq: number;
  round: number;
  subjectSha16: string;
  verdict: string; // loop_verdict closes the round ("" while open)
}

// One Mode B task's fold state (loopTask parity).
export interface LoopTask {
  n: number;
  text: string;
  spawned: boolean;
  done: boolean;
  doneStatus: string;
  designLockSeq: number; // seq of the design lock awaiting the human gate (0 none)
}

// Per-loop state, mirror of loopState. phase is loopPhase()'s output,
// computed at fold end (pure function of the other fields).
export interface LoopState {
  id: number; // seq of loop_started
  mode: string; // audit | tasks (loopMode() flips tasks→tasks_final)
  status: LoopStatus;
  cause: string; // latest suspend cause
  base: string;
  maxRounds: number; // 0 when loop_started lacked max_rounds — chip falls back
  budgetTokens: number;
  tasks: LoopTask[];
  finalAudit: boolean; // all tasks done — remaining rows ride tasks_final
  rounds: LoopRound[];
  latestVerdict: string; // newest round's verdict ("" before any)
  awaitingFixSpawn: boolean; // latest verdict fix, no fix spawn yet
  fixOpen: boolean; // a fix spawn has no accept/blocked/suspend after
  fixOutcome: string; // "landed" | "unlanded" | "" (open)
  fixesLanded: number; // accept rows attributed to a Mode A fix (D1 mirror)
  // D3 measured-cost fold parity (loop_journal.go estPending rule): a
  // spawn row's prompt_tokens_est stays in the cumulative ONLY until its
  // covering loop_run_usage receipt lands; the receipt subtracts the
  // pending estimate and adds the measured input+output+cache_write.
  // usageBySpawn dedups duplicate receipts per covers_spawn_seq (newest
  // wins — identical cumulative on re-fold); spawnRound/spawnTask
  // resolve a receipt whose covers_spawn_seq is 0 by round/task.
  estPending: Map<number, number>;
  usageBySpawn: Map<number, number>;
  spawnRound: Map<number, number>;
  spawnTask: Map<number, number>;
  // D1 attribution (loop_journal.go parity): the drain-journaled
  // loop⇄diff map. An accept/blocked review row closes the Mode A fix
  // phase only when its diff is round-bound here — no binding, no
  // attribution (fail-closed). Task bindings resolve in the task fold.
  boundDiffs: Set<number>;
  boundTasks: Map<number, number>;
  spentTokens: number; // cumulative ledger (max wins; usage rows replace)
  notifiedKinds: string[]; // journaled loop_notified receipts (V11 dedup)
  terminalKinds: string[]; // first-sight set of LOOP_TERMINAL_KINDS seen
  lastKind: string;
  lastSeq: number;
  phase: string;
}

// loopPhase renders the chip's phase word for the current fold state:
// terminal and suspended are status-derived; active derives from the fix
// pipeline and the round ledger. "seeding" covers everything before the
// first audit round (Mode A's SEED phase, Mode B's task stage — the
// design lock's closed enum, deliberately coarse).
export function loopPhase(st: LoopState): string {
  if (st.status === "stopped") return "stopped";
  if (st.status === "completed") return "completed";
  if (st.status === "suspended") return `suspended: ${st.cause}`;
  if (st.rounds.length === 0) return "seeding";
  if (st.awaitingFixSpawn || st.fixOpen) return "fixing";
  const last = st.rounds[st.rounds.length - 1];
  const current = last.verdict === "" ? st.rounds.length : st.rounds.length + 1;
  return `auditing round ${current}`;
}

// fmtLoopMode parity: Mode B rows flip to tasks_final once the task list
// drains (the chip buckets on it).
export function loopMode(st: LoopState): string {
  return st.mode === "tasks" && st.finalAudit ? "tasks_final" : st.mode;
}

// trackSpawn records one spawn row's estimate + phase keys for the D3
// estPending rule (Go parity with loop_journal.go trackSpawn).
function trackSpawn(st: LoopState, ev: OdoEvent, round: number, task: number): void {
  if (round > 0) st.spawnRound.set(ev.seq, round);
  if (task > 0) st.spawnTask.set(ev.seq, task);
  const est = ev.payload?.prompt_tokens_est ?? 0;
  if (est > 0) st.estPending.set(ev.seq, est);
}

// spawnSeqFor resolves a usage receipt whose covers_spawn_seq is 0
// (unknown) to the NEWEST spawn of its round/task — the lock's fallback
// match (Go parity).
function spawnSeqFor(st: LoopState, round: number, task: number): number {
  let best = 0;
  if (round > 0) for (const [s, r] of st.spawnRound) if (r === round && s > best) best = s;
  if (task > 0) for (const [s, t] of st.spawnTask) if (t === task && s > best) best = s;
  return best;
}

// foldRunUsage folds one drain-side loop_run_usage receipt (D3, Go
// parity): the covered spawn's estimate comes OUT of the cumulative, the
// measured input+output+cache_write goes IN (cache_read journaled,
// never budgeted). usage_available:false rows touch nothing (fail-soft —
// the estimate stays pending). Duplicate receipts per spawn fold
// newest-wins: the identical cumulative on re-fold.
function foldRunUsage(st: LoopState, p: EventPayload): void {
  if (p.usage_available !== true) return;
  const usage = (p.input_tokens ?? 0) + (p.output_tokens ?? 0) + (p.cache_write_tokens ?? 0);
  let s = p.covers_spawn_seq ?? 0;
  if (s <= 0) s = spawnSeqFor(st, p.round ?? 0, p.task ?? 0);
  if (s <= 0) {
    // No spawn to retire (partial journal): plain additive truth.
    st.spentTokens += usage;
    return;
  }
  st.spentTokens -= st.estPending.get(s) ?? 0;
  st.estPending.delete(s);
  st.spentTokens -= st.usageBySpawn.get(s) ?? 0;
  st.usageBySpawn.set(s, usage);
  st.spentTokens += usage;
}

// newestFoldLoop parity: review_action pipeline rows attribute to the
// newest non-terminal loop (one active loop per conversation, C10).
function newestLiveLoop(order: readonly LoopState[]): LoopState | null {
  for (let i = order.length - 1; i >= 0; i--) {
    const st = order[i];
    if (st.status !== "completed" && st.status !== "stopped") return st;
  }
  return null;
}

// foldLoopStarted builds the initial state from a loop_started payload.
function foldLoopStarted(ev: OdoEvent, id: number): LoopState {
  const p = ev.payload ?? {};
  return {
    id,
    mode: p.mode ?? "",
    status: "active",
    cause: "",
    base: p.base ?? "",
    maxRounds: p.max_rounds ?? 0,
    budgetTokens: p.budget_tokens ?? 0,
    tasks: Array.isArray(p.tasks)
      ? p.tasks
          .filter((t): t is string => typeof t === "string")
          .map((text, i) => ({ n: i + 1, text, spawned: false, done: false, doneStatus: "", designLockSeq: 0 }))
      : [],
    finalAudit: false,
    rounds: [],
    latestVerdict: "",
    awaitingFixSpawn: false,
    fixOpen: false,
    fixOutcome: "",
    fixesLanded: 0,
    boundDiffs: new Set<number>(),
    boundTasks: new Map<number, number>(),
    estPending: new Map<number, number>(),
    usageBySpawn: new Map<number, number>(),
    spawnRound: new Map<number, number>(),
    spawnTask: new Map<number, number>(),
    spentTokens: p.spent_tokens ?? 0,
    notifiedKinds: [],
    terminalKinds: [],
    lastKind: "loop_started",
    lastSeq: ev.seq,
    phase: "",
  };
}

// foldLoopRow folds one subsequent loop_event row into st (mirror of the
// same-named Go function — same status transitions, same fix tracking).
function foldLoopRow(st: LoopState, ev: OdoEvent, kind: string): void {
  const p = ev.payload ?? {};
  // Stamp max-wins EXCEPT on loop_run_usage (Go parity): the fold
  // performs the estPending replacement itself there — a measured cost
  // can be LOWER than the estimate it retires, and a max would resurrect
  // the stale estimate.
  if (kind !== "loop_run_usage") {
    const spent = p.spent_tokens ?? 0;
    if (spent > st.spentTokens) st.spentTokens = spent;
  }
  st.lastKind = kind;
  st.lastSeq = ev.seq;
  // First-sight tracking for the notification watcher (V11): the closed
  // terminal-kind set dedups by construction — a re-suspend never
  // re-notifies, a distinct terminal kind does.
  if ((LOOP_TERMINAL_KINDS as readonly string[]).includes(kind) && !st.terminalKinds.includes(kind)) {
    st.terminalKinds.push(kind);
  }
  switch (kind) {
    case "loop_suspended":
      st.status = "suspended";
      st.cause = p.cause ?? "";
      st.fixOpen = false;
      st.awaitingFixSpawn = false;
      break;
    case "loop_budget_exceeded":
      st.status = "suspended";
      st.cause = "budget_exceeded";
      st.fixOpen = false;
      st.awaitingFixSpawn = false;
      break;
    case "loop_completed":
      st.status = "completed";
      break;
    case "loop_stopped":
      st.status = "stopped";
      break;
    case "loop_resumed": {
      st.status = "active";
      st.cause = "";
      if ((p.budget ?? 0) > 0) st.budgetTokens = p.budget ?? 0;
      st.fixOpen = false;
      st.awaitingFixSpawn = false;
      const latestFix =
        st.rounds.length > 0 && st.rounds[st.rounds.length - 1].verdict === "fix";
      if (latestFix && st.fixOutcome === "") {
        switch (p.cause ?? "") {
          case "fix_no_diff":
          case "run_tainted":
          case "restart_mid_run":
            // The lock's "one automatic re-spawn on resume": the
            // interrupted fix respawns from the SAME findings.
            st.awaitingFixSpawn = true;
            break;
          default:
            // Every other cause resolves to a RE-AUDIT on resume:
            // reality changed, the fixpoint re-derives it.
            st.fixOutcome = "unlanded";
        }
      }
      break;
    }
    case "loop_recovered":
      st.status = "active";
      st.cause = "";
      break;
    case "loop_audit_round":
      // The round row may carry prior-facts — fix phase closed either way.
      st.rounds.push({ seq: ev.seq, round: p.round ?? 0, subjectSha16: p.subject_sha16 ?? "", verdict: "" });
      st.fixOpen = false;
      break;
    case "loop_verdict": {
      const v = p.verdict ?? "";
      if (st.rounds.length > 0) st.rounds[st.rounds.length - 1].verdict = v;
      if (v === "fix") {
        st.awaitingFixSpawn = true;
        st.fixOutcome = "";
      }
      break;
    }
    case "loop_fix_spawn":
      st.awaitingFixSpawn = false;
      st.fixOpen = true;
      st.fixOutcome = "";
      trackSpawn(st, ev, p.round ?? 0, 0);
      break;
    case "loop_diff_bound": {
      // P1 #13 / D1: the drain-journaled loop⇄diff binding (parity with
      // the Go foldSwitch — every bound id accumulates).
      const diffID = p.diff_id ?? 0;
      if (diffID > 0) {
        st.boundDiffs.add(diffID);
        const t = p.task ?? 0;
        if (t >= 1) st.boundTasks.set(diffID, t);
      }
      break;
    }
    case "loop_design_lock": {
      const t = p.task ?? 0;
      if (t >= 1 && t <= st.tasks.length) {
        st.tasks[t - 1].designLockSeq = ev.seq;
      }
      break;
    }
    case "loop_task_spawn": {
      const t = p.task ?? 0;
      if (t >= 1 && t <= st.tasks.length) {
        st.tasks[t - 1].spawned = true;
        st.tasks[t - 1].designLockSeq = 0;
      }
      trackSpawn(st, ev, 0, t);
      break;
    }
    case "loop_run_usage":
      foldRunUsage(st, p);
      break;
    case "loop_task_done": {
      const t = p.task ?? 0;
      if (t >= 1 && t <= st.tasks.length) {
        st.tasks[t - 1].done = true;
        st.tasks[t - 1].doneStatus = p.status ?? "";
        st.tasks[t - 1].designLockSeq = 0;
      }
      if (st.mode === "tasks" && st.tasks.length > 0 && st.tasks.every((x) => x.done)) {
        st.finalAudit = true;
      }
      break;
    }
    case "loop_notified": {
      const k = p.terminal_kind ?? "";
      if (k !== "" && !st.notifiedKinds.includes(k)) st.notifiedKinds.push(k);
      break;
    }
    // Wire receipts, spilled-body path keys, and unknown forward-compat
    // kinds touch no GUI fold fact.
  }
}

// deriveLoopStates folds one conversation's seq-ascending journal into
// every loop's state, in start order (the Go deriveLoopStates parity —
// event-stream scope is the daemon's guarantee; pipeline.ts documents
// why no conversation filter belongs here).
export function deriveLoopStates(events: readonly OdoEvent[]): LoopState[] {
  const order: LoopState[] = [];
  const byID = new Map<number, LoopState>();
  for (const ev of events) {
    if (ev.type === "loop_event") {
      const kind = ev.payload?.kind ?? "";
      if (kind === "") continue;
      // loop_started carries loop_id 0 — the row's own seq IS the id.
      const id = kind === "loop_started" ? ev.seq : (ev.payload?.loop_id ?? 0);
      let st = byID.get(id);
      if (st == null && kind === "loop_started") {
        st = foldLoopStarted(ev, id);
        byID.set(id, st);
        order.push(st);
      }
      if (st == null) continue; // row of an unknown loop (corrupt journal) — skip
      if (kind !== "loop_started") foldLoopRow(st, ev, kind);
    } else if (ev.type === "review_action") {
      // Loop-owned pipeline facts close the open fix phase of the loop
      // that spawned them (review_action purity — V1).
      const p = ev.payload ?? {};
      const st = newestLiveLoop(order);
      if (st == null) continue;
      // D1 attribution parity (loop_journal.go): an accept/blocked row
      // closes the Mode A fix phase IFF (a) the actor is a pipeline actor
      // (auto_loop or, post-reroute, auto_panel) AND (b) a
      // loop_diff_bound{round} row names the diff. No binding ⇒ no
      // attribution, fail-closed — a human accept of an unrelated inbox
      // diff never resolves the fix phase. Task bindings resolve in the
      // task adjudication lane, never here.
      const pipelineActor = p.actor === LOOP_ACTOR || p.actor === AUTO_ACTOR;
      const diffID = p.diff_id ?? 0;
      const boundRound = st.boundDiffs.has(diffID) && !st.boundTasks.has(diffID);
      if (!pipelineActor || !boundRound) continue;
      if (p.action === "accept") {
        st.fixesLanded += 1;
        if (st.fixOpen) {
          st.fixOpen = false;
          st.fixOutcome = "landed";
        }
      } else if (p.action === "auto_land_blocked") {
        st.fixOpen = false;
        st.fixOutcome = "unlanded";
      }
    }
  }
  for (const st of order) {
    st.latestVerdict = st.rounds.length > 0 ? st.rounds[st.rounds.length - 1].verdict : "";
    st.phase = loopPhase(st);
  }
  return order;
}

// actionableLoop is the chip's render gate (at most one active loop per
// conversation, C10 — suspended is actionable: stop + resume). Terminal
// loops have no chip: their bookkeeping bubble and the notification are
// the surfaces.
export function actionableLoop(loops: readonly LoopState[]): LoopState | null {
  for (let i = loops.length - 1; i >= 0; i--) {
    const st = loops[i];
    if (st.status === "active" || st.status === "suspended") return st;
  }
  return null;
}

// Short forms for the bookkeeping bubble (kinds are long wire names).
const KIND_SHORT: Record<string, string> = {
  loop_started: "started",
  loop_design_lock: "design lock",
  loop_task_spawn: "task spawn",
  loop_task_done: "task done",
  loop_audit_round: "audit round",
  loop_verdict: "verdict",
  loop_fix_spawn: "fix spawn",
  loop_suspended: "suspended",
  loop_completed: "completed",
  loop_stopped: "stopped",
  loop_budget_exceeded: "budget exceeded",
  loop_run_usage: "run usage",
  loop_recovered: "recovered",
  loop_resumed: "resumed",
  loop_notified: "notified",
};

// loopEventLabel renders one loop_event row's compact bookkeeping bubble
// (V1: kind + key fields, never agent text). The title carries the full
// journaled payload verbatim so the bubble never hides what the journal
// holds.
export function loopEventLabel(ev: OdoEvent): { label: string; title: string } {
  const p = ev.payload ?? {};
  const kind = p.kind ?? "";
  const id = kind === "loop_started" ? ev.seq : (p.loop_id ?? 0);
  const short = KIND_SHORT[kind] ?? (kind !== "" ? kind : "?");
  let extra = "";
  switch (kind) {
    case "loop_started":
      extra = ` · ${p.mode ?? "?"} · base ${(p.base ?? "").slice(0, 8) || "?"}`;
      break;
    case "loop_audit_round":
      extra = ` · round ${p.round ?? "?"} · ${p.findings_count ?? 0} findings`;
      break;
    case "loop_verdict":
      extra = ` · ${p.verdict ?? "?"}`;
      break;
    case "loop_fix_spawn":
      extra = ` · round ${p.round ?? "?"} · ${p.findings_count ?? 0} findings`;
      break;
    case "loop_suspended":
    case "loop_resumed":
      extra = (p.cause ?? "") !== "" ? ` · ${p.cause}` : "";
      break;
    case "loop_budget_exceeded":
      extra = ` · ${p.spent_tokens ?? 0}/${p.budget_tokens ?? 0}`;
      break;
    case "loop_run_usage":
      extra =
        p.usage_available === true
          ? ` · ${(p.input_tokens ?? 0) + (p.output_tokens ?? 0) + (p.cache_write_tokens ?? 0)} tok measured`
          : ` · usage pending (${p.reason ?? "no transcript"})`;
      break;
    case "loop_completed":
      extra = ` · rounds ${p.rounds ?? 0} · fixes ${p.fixes_landed ?? 0}`;
      break;
    case "loop_stopped":
      extra = (p.detail ?? "") !== "" ? ` · ${p.detail}` : "";
      break;
    case "loop_design_lock":
    case "loop_task_spawn":
      extra = ` · task ${p.task ?? "?"}`;
      break;
    case "loop_task_done":
      extra = ` · task ${p.task ?? "?"} · ${p.status ?? "?"}`;
      break;
    case "loop_notified":
      extra = ` · ${p.terminal_kind ?? "?"}`;
      break;
    case "loop_recovered":
      extra = (p.action ?? "") !== "" ? ` · ${p.action}` : "";
      break;
  }
  return {
    label: `loop #${id} · ${short}${extra}`,
    title: JSON.stringify(p),
  };
}
