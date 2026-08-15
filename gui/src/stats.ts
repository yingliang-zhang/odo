// GUI Wave B (audit §3 #5/#8/#9): telemetry derivations over data the GUI
// already holds. NO new IPC — the context meter reads the journaled prompt
// receipt closure (total_prompt_bytes + receipt + replay, M18 W2), the
// per-turn stats derive from the run group's own events, and the panel
// chip parses the get_settings review list.

import type { EventPayload, OdoEvent } from "./types";

// ---------- context window table (mirror) ----------

// Context windows mirror internal/modelspec/modelspec.go — the daemon's
// table is the source of truth; a model missing here falls back to the
// same conservative 200K the daemon uses. Drift between the tables is
// cosmetic only (the ring's percent shifts, nothing gates on it).
export const MODEL_CONTEXT_WINDOWS: Record<string, number> = {
  "kimi-k3": 350_000,
  "deepseek-v4-flash": 1_000_000,
  "glm-5.2": 1_000_000,
};
export const FALLBACK_CONTEXT_WINDOW = 200_000;

// The daemon thinks in BYTES (total_prompt_bytes, replay KB caps); the
// window is stated in TOKENS. 4 bytes/token is the standard English/code
// estimate — the ring is a glanceable approximation, so the percent is
// displayed with "~" and never used for gating.
export const BYTES_PER_TOKEN = 4;

export function contextWindowTokens(model: string | null): number {
  if (model == null) return FALLBACK_CONTEXT_WINDOW;
  // Provider prefix stripped to the modelspec convention ("t9s/kimi-k3"
  // keys as "kimi-k3").
  const slash = model.lastIndexOf("/");
  const bare = (slash >= 0 ? model.slice(slash + 1) : model).toLowerCase();
  return MODEL_CONTEXT_WINDOWS[bare] ?? FALLBACK_CONTEXT_WINDOW;
}

// ---------- formatting ----------

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function formatTokens(n: number): string {
  if (n < 1000) return `${n} tok`;
  return `${(n / 1000).toFixed(1)}k tok`;
}

// ---------- last prompt snapshot (context meter) ----------

// The journaled closure of the most recent prompt sent to a model, as
// seen by this conversation's event stream.
export interface PromptSnapshot {
  bytes: number;
  sha16?: string;
  seq: number;
  // Verbatim receipt keys = the injected layers (paths + synthetic keys
  // like journal#todo, odo#memory-map). Presence-only: no per-layer byte
  // counts exist on the wire — the popover lists the layers themselves.
  layers: string[];
  recallHeldBack?: number;
  replay?: { first_seq: number; last_seq: number; bytes: number; dropped_seqs?: number[] };
}

function closureFromPayload(ev: OdoEvent): PromptSnapshot | null {
  const p: EventPayload = ev.payload ?? {};
  const bytes = p.total_prompt_bytes;
  if (typeof bytes !== "number" || bytes <= 0) return null;
  return {
    bytes,
    sha16: p.prompt_sha16,
    seq: ev.seq,
    layers: Object.keys(p.receipt ?? {}),
    recallHeldBack: p.recall_held_back,
    replay:
      p.replay != null
        ? {
            first_seq: p.replay.first_seq,
            last_seq: p.replay.last_seq,
            bytes: p.replay.bytes,
            dropped_seqs: p.replay.dropped_seqs,
          }
        : undefined,
  };
}

// Reverse scan: send and /panel//vision journal the closure on
// user_message; continuations converge it onto review_action{run_prompt}.
// Either way the newest carrier wins — the meter shows the last prompt a
// model actually saw, not a live counter.
export function deriveLastPrompt(events: readonly OdoEvent[]): PromptSnapshot | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const snap = closureFromPayload(events[i]);
    if (snap != null) return snap;
  }
  return null;
}

// ---------- per-turn stats (Wave B #8) ----------

export interface TurnStats {
  // Real wall time from journaled timestamps (run start → agent_done).
  wallMs: number;
  // Sizes: input = journaled prompt bytes on the run's start message;
  // output = UTF-8 bytes of the run's agent_text bodies. NULL when the
  // journal carries no number — an absent value is never fabricated.
  inputBytes: number | null;
  outputBytes: number;
  // Billed usage, only when the payload carries it (nothing writes these
  // yet — the OMP adapter drops stream usage; see types.ts).
  inputTokens?: number;
  outputTokens?: number;
  toolsCalls: number;
}

const utf8 = new TextEncoder();

export function deriveTurnStats(
  start: OdoEvent | null,
  events: readonly OdoEvent[],
  done: OdoEvent | undefined,
): TurnStats | null {
  if (done == null) return null;
  const endMs = Date.parse(done.created_at);
  const startMs = start != null ? Date.parse(start.created_at) : NaN;
  if (Number.isNaN(endMs) || Number.isNaN(startMs)) return null;
  let outputBytes = 0;
  let toolsCalls = 0;
  for (const ev of events) {
    if (ev.type === "agent_text" && typeof ev.payload?.text === "string") {
      outputBytes += utf8.encode(ev.payload.text).length;
    } else if (ev.type === "agent_tool_call") {
      toolsCalls++;
    }
  }
  const p = done.payload ?? {};
  return {
    wallMs: endMs - startMs,
    inputBytes:
      start != null && typeof start.payload?.total_prompt_bytes === "number"
        ? start.payload.total_prompt_bytes
        : null,
    outputBytes,
    inputTokens: typeof p.input_tokens === "number" ? p.input_tokens : undefined,
    outputTokens: typeof p.output_tokens === "number" ? p.output_tokens : undefined,
    toolsCalls,
  };
}

// ---------- review panel composition (Wave B #9) ----------

export interface PanelModel {
  model: string;
  provider: string;
}

// Parse the comma-separated `model@provider` review list exactly as the
// daemon does (adapter.ParseModelProvider: split at the LAST @; malformed
// entries are dropped, never guessed).
export function parseReviewModels(raw: string): PanelModel[] {
  const out: PanelModel[] = [];
  for (const entry of raw.split(",")) {
    const trimmed = entry.trim();
    if (trimmed === "") continue;
    const at = trimmed.lastIndexOf("@");
    if (at <= 0 || at === trimmed.length - 1) continue;
    const model = trimmed.slice(0, at).trim();
    const provider = trimmed.slice(at + 1).trim();
    if (model === "" || provider === "") continue;
    out.push({ model, provider });
  }
  return out;
}
