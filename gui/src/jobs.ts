// UX-2 (D5 Stage 0 / A2-1, A2-5): pure derivations for the StatusBar Jobs
// chip — phase classification, age/completion formatting, and the chip
// face label. Kept pure so the chip never reinterprets kubectl payloads
// inline and the table is unit-testable without a DOM (the stats.ts
// posture).

import type { K8sBatch, K8sJob, K8sStatus } from "./types";

// Phase vocabulary k8s itself uses (v1.28+ conditions): Complete,
// FailureTarget (terminal), SuccessCriteriaMet, Suspended. kubectl counts
// these straight from .status.conditions.
export function jobPhase(job: K8sJob): string {
  const conds = job.status?.conditions ?? [];
  // kubectl evaluates conditions oldest-first; the LAST terminal tag wins.
  for (let i = conds.length - 1; i >= 0; i--) {
    const c = conds[i];
    if (c.status !== "True" || c.type == null) continue;
    if (c.type === "FailureTarget") return "Failed";
    return c.type;
  }
  if ((job.status?.active ?? 0) > 0) return "Active";
  if ((job.status?.failed ?? 0) > 0) return "Failed";
  return "Pending";
}

// Relative age of a creation timestamp: "45s", "12m", "3h", "1d". Never
// negative (clock skew rounds to 0s) — the popover annotates, not audits.
export function formatAge(nowUnixMs: number, creationTimestamp?: string): string {
  const created = creationTimestamp != null ? Date.parse(creationTimestamp) : NaN;
  if (!Number.isFinite(created)) return "?";
  const secs = Math.max(0, Math.floor((nowUnixMs - created) / 1000));
  if (secs < 60) return `${secs}s`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

// "succeeded/completions" — the wire's own pair. jobs.spec.completions is
// commonly absent for parallel/queue jobs (compressible → no target);
// show just the succeeded count there rather than invent a denominator.
export function formatCompletions(job: K8sJob): string {
  const target = job.spec?.completions;
  const succeeded = job.status?.succeeded ?? 0;
  return target != null ? `${succeeded}/${target}` : `${succeeded}/—`;
}

// Active-phase jobs drive the badge (A2-5: badge = active jobs + active
// batches; batches arrive with D5b, not this diff). "Active" + "Pending"
// are the two count-able live phases — Completed/Failed rows stay visible
// in the popover but don't badge.
export function activeJobCount(jobs: readonly K8sJob[]): number {
  return jobs.filter((j) => {
    const p = jobPhase(j);
    return p === "Active" || p === "Pending";
  }).length;
}

// Chip face, count-only (A2-5: progress bars NEVER in the chip face —
// 24px bar + fold-to-hidden = useless). "+" marks the daemon's 50-row cap.
export function chipLabel(jobs: readonly K8sJob[], truncated: boolean): string {
  return `Jobs · ${jobs.length}${truncated ? "+" : ""}`;
}

// ---------- A4 (multi-ns): grouped derivations over flat payloads ----------
// The daemon flat-merges answering namespaces; every kubectl row carries
// metadata.namespace. Grouping is a VIEW derivation — never new IPC (the
// deriveTodoState precedent: one fetch, many views).

// Active jobs inside ONE namespace (flat payload filtered client-side).
export function nsActiveCount(jobs: readonly K8sJob[], ns: string): number {
  return activeJobCount(jobs.filter((j) => j.metadata?.namespace === ns));
}

// Configured-order sort, then age newest-first inside each group (A4 D4:
// NO per-ns section headers in the table; the sort carries the grouping).
// Jobs whose namespace isn't in the configured list (selector mismatches,
// legacy payloads) sink after the known groups, still newest-first.
export function sortJobsForTable(jobs: readonly K8sJob[], nsOrder: readonly string[]): K8sJob[] {
  const rank = new Map(nsOrder.map((ns, i) => [ns, i]));
  const age = (j: K8sJob): number => {
    const t = Date.parse(j.metadata?.creationTimestamp ?? "");
    return Number.isFinite(t) ? t : 0;
  };
  return [...jobs].sort((a, b) => {
    const ra = rank.get(a.metadata?.namespace ?? "") ?? Number.MAX_SAFE_INTEGER;
    const rb = rank.get(b.metadata?.namespace ?? "") ?? Number.MAX_SAFE_INTEGER;
    if (ra !== rb) return ra - rb;
    return age(b) - age(a); // newest first
  });
}

// ---------- D5b (A2-4/A2-5): batch progress derivations ----------

// A2-5's badge half: active = running AND fresh — a stale heartbeat is
// UNKNOWN (B4: a frozen file is unknown, never progress), so it can't
// badge.
export function activeBatchCount(batches: readonly K8sBatch[]): number {
  return batches.filter((b) => b.reason == null && b.status === "running" && b.stale !== true).length;
}

// N/M completion fraction; null when the file can't tell (no total).
export function batchFraction(b: K8sBatch): number | null {
  const total = b.total ?? 0;
  if (total <= 0) return null;
  const done = b.done ?? 0;
  return Math.min(1, Math.max(0, done / total));
}

// Derived ETA annotation (A4: hide when rate_per_min <= 0 — a stalled or
// unknown rate inventing a time is a lie). "8m" / "1h5m".
export function batchEta(b: K8sBatch): string | null {
  const rate = b.rate_per_min ?? 0;
  const total = b.total ?? 0;
  const done = b.done ?? 0;
  if (rate <= 0 || total <= done) return null;
  const mins = Math.ceil((total - done) / rate);
  if (mins < 60) return `${mins}m`;
  const h = Math.floor(mins / 60);
  const m = mins % 60;
  return m > 0 ? `${h}h${m}m` : `${h}h`;
}

// Heartbeat annotation for stale rows: "stale — last update 2m" (the
// daemon ships the >90s flag; the AGE is the GUI's own lastGoodAge).
export function staleLabel(b: K8sBatch, nowUnix: number): string {
  return `stale — last update ${lastGoodAge(nowUnix, b.updated_unix) ?? "?"}`;
}

// The popover's one-line batch summary (A2-5: rate + ETA are derived
// annotations; progress bars stay tab-only): "transcode 72% · ETA 8m",
// done rows surface their error count, degraded rows their reason.
export function batchOneLiner(b: K8sBatch, nowUnix: number): string {
  if (b.reason != null) return `${b.batch} — ${b.reason}`;
  if (b.status === "done") return `${b.batch} done${(b.errs ?? 0) > 0 ? ` · ${b.errs} errs` : ""}`;
  if (b.status === "failed") return `${b.batch} failed${(b.errs ?? 0) > 0 ? ` · ${b.errs} errs` : ""}`;
  const frac = batchFraction(b);
  const parts: string[] = [frac != null ? `${Math.round(frac * 100)}%` : "…"];
  const eta = batchEta(b);
  if (eta != null) parts.push(`ETA ${eta}`);
  if (b.stale === true) parts.push(staleLabel(b, nowUnix));
  return `${b.batch} ${parts.join(" · ")}`;
}

// Age of the last-good snapshot for the degrade path: "(2m ago)" so a
// stale table is never mistaken for a live one (A2-2: keep last-good).
export function lastGoodAge(nowUnix: number, fetchedUnix?: number): string | null {
  if (fetchedUnix == null || fetchedUnix <= 0) return null;
  const secs = Math.max(0, nowUnix - fetchedUnix);
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

// The popover's reason line — one honest canned sentence per cause class.
// kubectl's raw stderr rides the envelope separately as `detail` (1KB
// daemon capture cap, 240-char display cap) and renders BELOW this line.
export function reasonLabel(reason: K8sStatus["reason"]): string {
  switch (reason) {
    case "ENOENT":
      return "kubectl not found on the daemon's PATH";
    case "timeout":
      return "kubectl timed out (10s)";
    case "auth":
      return "cluster rejected the credentials (auth)";
    case "bad_namespace":
      return "k8s_namespace pref rejected by the daemon (bad_namespace)";
    case "unreachable":
      return "cluster unreachable";
    default:
      return "k8s status unavailable";
  }
}
