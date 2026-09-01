import type { OdoEvent } from "./types";

// UX-3c (A2-6c): MemoryPanel's auto-curate/auto-distill backoff line.
//
// Source rows (conversation journal, the SAME events the poll already
// holds — no new IPC, A-P0 #1 contract):
//   memory_update{layer:"curator", cause:"skipped",
//     detail:"trigger=… notes_since=N reason=backoff next_eligible_at=<RFC3339>"}
//     — auto.go journalCurateSkip: a curate failure gates auto retries for
//     autoCurateFailureBackoff (24h); the suppression is derived from the
//     journal, so the row is the durable answer to "why no curate?".
//   memory_update{layer:"auto_distill", cause:"skipped",
//     detail:"trigger=… window_events=N window_bytes=B reason=<reason>"}
//     — auto.go journalAuto: the scheduler's skip decisions (below-min idle,
//     failure ladder "backoff"/"backoff_suspended", "hourly_cap").
//
// Fold: LATEST row per layer wins. A newer fired/scheduled/passed row clears
// a previous skip (the daemon's own "success resets" semantics). Reasons
// outside the pause set (disabled, run_active, slash_active, superseded_*,
// disarmed_*, window_fresh) are transient or config facts — they hide the
// line rather than render noise ("no speculative states", A2-6c). An
// unknown future reason hides too: raw daemon text never ships unfiltered.

export interface BackoffLine {
  kind: "idle" | "paused";
  text: string;
}

export interface AutoBackoff {
  distill: BackoffLine | null;
  curate: BackoffLine | null;
}

const DISTILL_PAUSE: Record<string, BackoffLine> = {
  below_min_bytes: { kind: "idle", text: "auto-distill idle — below min bytes" },
  below_min_events: { kind: "idle", text: "auto-distill idle — below min events" },
  hourly_cap: { kind: "paused", text: "auto-distill paused — hourly cap reached" },
  backoff: { kind: "paused", text: "auto-distill paused — failure backoff" },
  backoff_suspended: { kind: "paused", text: "auto-distill paused — retries suspended after repeated failures" },
};

function detailField(detail: string, key: string): string | null {
  const m = detail.match(new RegExp(`(?:^|\\s)${key}=(\\S+)`));
  return m?.[1] ?? null;
}

export function deriveAutoBackoff(events: OdoEvent[], now: Date = new Date()): AutoBackoff {
  const sorted = [...events].sort((a, b) => a.seq - b.seq);
  let distill: { cause: string; detail: string } | null = null;
  let curate: { cause: string; detail: string } | null = null;
  for (const ev of sorted) {
    if (ev.type !== "memory_update") continue;
    const layer = ev.payload?.layer;
    if (layer !== "auto_distill" && layer !== "curator") continue;
    const row = {
      cause: typeof ev.payload?.cause === "string" ? ev.payload.cause : "",
      detail: typeof ev.payload?.detail === "string" ? ev.payload.detail : "",
    };
    if (layer === "auto_distill") distill = row;
    else curate = row;
  }

  let distillLine: BackoffLine | null = null;
  if (distill?.cause === "skipped") {
    const reason = detailField(distill.detail, "reason");
    distillLine = reason != null ? (DISTILL_PAUSE[reason] ?? null) : null;
  }

  let curateLine: BackoffLine | null = null;
  if (curate?.cause === "skipped" && detailField(curate.detail, "reason") === "backoff") {
    const at = detailField(curate.detail, "next_eligible_at");
    const ms = at != null ? Date.parse(at) : NaN;
    // A stale horizon (next-eligible already past) means the gate lifted
    // without a newer row — showing it would lie.
    if (!Number.isNaN(ms) && ms > now.getTime()) {
      const clock = new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
      curateLine = { kind: "paused", text: `auto-curate paused — next eligible ${clock}` };
    }
  }

  return { distill: distillLine, curate: curateLine };
}
