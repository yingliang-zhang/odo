// D5b (A2-5 + A4 D4): the ONE Jobs tab — two stacked sections:
//   1. Jobs table — FLAT (no per-ns section headers, they break cross-ns
//      sort) with a leading namespace column; default sort = configured
//      namespace order, then age newest-first. The in-tab quick-switcher
//      is a CLIENT-SIDE filter within the configured set only (never IPC,
//      never widens what the daemon fetches).
//   2. Batches — true N/M progress bars + rate + ETA as a derived
//      annotation (hidden when rate_per_min <= 0) + status + staleness
//      grey ("stale — last update 2m" — a frozen heartbeat is UNKNOWN,
//      never progress, B4).
// Data arrives via App's shared useK8sPoll (ONE poller app-wide; the gate
// is set by App from chip-fold and tab-active signals). Progress bars
// live HERE ONLY — the chip face is count-only.

import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { Container, RefreshCw, TriangleAlert } from "lucide-react";
import type { K8sBatch, K8sJob } from "../types";
import type { K8sPoll } from "../k8s";
import {
  batchEta,
  batchFraction,
  formatAge,
  formatCompletions,
  jobPhase,
  lastGoodAge,
  reasonLabel,
  sortJobsForTable,
  staleLabel,
} from "../jobs";
import { cn } from "../lib/utils";

// Bar chrome — the OmpUsageChip bar posture (track + percentage fill),
// tinted by phase: accent for running, ok for done, err for failed.
const BATCH_BAR_TRACK = "jobs-batch-track h-1.5 overflow-hidden rounded-[2px] bg-bg-tertiary";
const BATCH_BAR_FILL: Record<string, string> = {
  running: "bg-accent-user",
  done: "bg-ok-text",
  failed: "bg-err-text",
};

function BatchCard({ b, nowUnix }: { b: K8sBatch; nowUnix: number }) {
  if (b.reason != null) {
    return (
      <div className="jobs-batch-card flex flex-col gap-1 rounded border border-stroke-tertiary px-2 py-1.5 opacity-70">
        <div className="flex items-baseline justify-between gap-2">
          <span className="mono text-micro text-text">{b.batch}</span>
          <span className="jobs-batch-reason text-[10px] text-err-text">{b.reason}</span>
        </div>
      </div>
    );
  }
  const frac = batchFraction(b);
  const eta = batchEta(b);
  const status = b.status ?? "running";
  const fill = BATCH_BAR_FILL[status] ?? BATCH_BAR_FILL.running;
  return (
    <div className={cn("jobs-batch-card flex flex-col gap-1 rounded border border-stroke-tertiary px-2 py-1.5", b.stale === true && "opacity-70")}>
      <div className="flex items-baseline justify-between gap-2">
        <span className="mono text-micro text-text">{b.batch}</span>
        <span className="jobs-batch-facts shrink-0 text-[10px] text-text-dim">
          {b.stage != null && b.stage !== "" && <span>{b.stage} · </span>}
          {status}
          {(b.errs ?? 0) > 0 && <span className="text-err-text"> · {b.errs} errs</span>}
        </span>
      </div>
      {frac != null && status !== "done" && (
        <>
          <div className={BATCH_BAR_TRACK} role="progressbar" aria-valuenow={Math.round(frac * 100)} aria-valuemin={0} aria-valuemax={100} aria-label={`${b.batch} progress`}>
            <div className={cn("h-full", fill)} style={{ width: `${frac * 100}%` }} />
          </div>
          <div className="jobs-batch-rate text-[10px] text-text-dim">
            {b.done ?? 0}/{b.total ?? 0}
            {(b.rate_per_min ?? 0) > 0 && <span> · {b.rate_per_min}/min</span>}
            {eta != null && <span> · ETA {eta}</span>}
          </div>
        </>
      )}
      {b.stale === true && (
        <div className="jobs-batch-stale flex items-center gap-1 text-[10px] text-warn">
          <TriangleAlert size={10} aria-hidden />
          {staleLabel(b, nowUnix)}
        </div>
      )}
    </div>
  );
}

export default function JobsPanel({
  k8s,
  namespaces,
}: {
  k8s: K8sPoll;
  // The CONFIGURED namespace set in configured order — the default sort's
  // group order and the quick-switcher's option list (scope truth lives
  // in settings; this panel never edits it).
  namespaces: string[];
}) {
  const { status: snap, unavailable, detail, transportErr, batch } = k8s;
  // Client-side filter (A4 D4): all configured ns shown by default;
  // toggling re-renders the already-fetched flat payload — zero IPC.
  const [hiddenNs, setHiddenNs] = useState<ReadonlySet<string>>(new Set());

  const jobs = snap?.jobs ?? [];
  const sorted = useMemo(
    () => sortJobsForTable(jobs.filter((j) => !hiddenNs.has(j.metadata?.namespace ?? "")), namespaces),
    [jobs, hiddenNs, namespaces],
  );

  const broken = unavailable != null || transportErr != null;
  const reason = unavailable ?? (transportErr != null ? ("unreachable" as const) : undefined);
  const batches = batch?.batches ?? [];
  const nowUnix = Math.floor(Date.now() / 1000);
  const snapAge = lastGoodAge(nowUnix, snap?.fetched_unix);

  // The batches section's four honest states (A2-4 contract at the
  // section level): bridge off → hint; broken → reason; empty → empty;
  // rows → cards. A schema_mismatch/reason row stays a VISIBLE card.
  let batchSection: ReactNode;
  if (batch == null || (batch.available !== true && batch.reason === "off")) {
    batchSection = <div className="panel-empty">no batch bridge — set k8s_batch_dir to read status.json rows</div>;
  } else if (batch.available !== true) {
    batchSection = (
      <div className="jobs-batch-error rounded border border-stroke-tertiary px-2 py-1.5 text-err-text">
        batches — {batch.reason}
        {batch.detail != null && <span className="block text-[10px] text-text-dim">{batch.detail}</span>}
      </div>
    );
  } else if (batches.length === 0) {
    batchSection = <div className="panel-empty">no status.json rows under k8s_batch_dir</div>;
  } else {
    batchSection = (
      <div className="jobs-batches flex flex-col gap-1.5">
        {batches.map((b) => (
          <BatchCard key={`${b.batch}-${b.updated_unix ?? 0}`} b={b} nowUnix={nowUnix} />
        ))}
      </div>
    );
  }

  const toggleNs = (ns: string) => {
    setHiddenNs((prev) => {
      const next = new Set(prev);
      if (next.has(ns)) next.delete(ns);
      else next.add(ns);
      return next;
    });
  };

  return (
    <div className="jobs-panel flex flex-col gap-2 p-3 text-micro">
      <div className="jobs-panel-head flex items-center justify-between gap-2">
        <span className="flex items-center gap-1.5 text-[10px] uppercase tracking-[0.06em] text-text-dim">
          <Container size={11} aria-hidden />
          k8s jobs — read-only (never journaled)
        </span>
        <button
          type="button"
          className="jobs-refresh inline-flex cursor-pointer items-center gap-1 rounded border-none bg-transparent px-1 py-0.5 text-[10px] text-text-dim hover:bg-bg-hover hover:text-text"
          title="Refresh now"
          onClick={k8s.pollNow}
        >
          <RefreshCw size={10} aria-hidden />
          refresh
        </button>
      </div>

      {broken && (
        <div className="jobs-reason flex flex-col gap-1 rounded border border-stroke-tertiary px-2 py-1.5 text-err-text">
          <span>{reasonLabel(reason)}</span>
          {detail != null && <span className="mono break-words whitespace-pre-wrap text-[10px] text-text-dim">{detail}</span>}
        </div>
      )}

      {namespaces.length > 1 && (
        <div className="jobs-ns-switcher flex flex-wrap items-center gap-1" role="group" aria-label="Namespace filter">
          {namespaces.map((ns) => {
            const on = !hiddenNs.has(ns);
            const row = snap?.namespaces?.find((r) => r.name === ns);
            return (
              <button
                key={ns}
                type="button"
                aria-pressed={on}
                className={cn(
                  "jobs-ns-chip inline-flex cursor-pointer items-center gap-1 rounded-md border border-stroke-tertiary px-1.5 py-0.5 text-[10px]",
                  on ? "text-text" : "text-text-dim opacity-60",
                  row != null && !row.ok && "jobs-ns-chip-down",
                )}
                title={row != null && !row.ok ? `${ns}: ${reasonLabel(row.reason ?? "unreachable")}` : ns}
                onClick={() => toggleNs(ns)}
              >
                {ns}
                {row != null && !row.ok && <TriangleAlert size={9} aria-hidden className="text-err-text" />}
              </button>
            );
          })}
        </div>
      )}

      <div className="jobs-table-wrap overflow-x-auto">
        {snap == null && !broken ? (
          <div className="panel-empty">Fetching k8s_status…</div>
        ) : sorted.length === 0 ? (
          <div className="panel-empty">
            {jobs.length === 0 ? "no jobs — the cluster answered empty" : "no jobs in the filtered namespace set"}
          </div>
        ) : (
          <table className="jobs-table w-full border-collapse text-left">
            <thead>
              <tr className="jobs-table-head border-b border-stroke-tertiary text-[10px] uppercase tracking-[0.06em] text-text-dim">
                <th className="px-1 py-1 font-normal">namespace</th>
                <th className="px-1 py-1 font-normal">name</th>
                <th className="px-1 py-1 font-normal">phase</th>
                <th className="px-1 py-1 font-normal">age</th>
                <th className="px-1 py-1 text-right font-normal">completions</th>
              </tr>
            </thead>
            <tbody>
              {sorted.map((job: K8sJob, i) => {
                const phase = jobPhase(job);
                return (
                  <tr key={`${job.metadata?.namespace ?? ""}-${job.metadata?.name ?? "job"}-${i}`} className="jobs-table-row border-b border-stroke-tertiary">
                    <td className="jobs-col-ns px-1 py-1 text-[10px] text-text-dim">{job.metadata?.namespace ?? "?"}</td>
                    <td className="px-1 py-1">
                      <span className="mono text-micro text-text">{job.metadata?.name ?? "?"}</span>
                    </td>
                    <td className={cn("px-1 py-1", phase === "Complete" && "text-ok-text", phase === "Failed" && "text-err-text")}>{phase}</td>
                    <td className="px-1 py-1 text-text-dim">{formatAge(Date.now(), job.metadata?.creationTimestamp)}</td>
                    <td className="px-1 py-1 text-right text-text-dim">{formatCompletions(job)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
      {snap?.truncated === true && (
        <div className="jobs-truncated text-[10px] text-text-dim">first 50 rows per namespace — set k8s_job_selector to narrow</div>
      )}

      <div className="jobs-panel-divider pt-2 text-[10px] uppercase tracking-[0.06em] text-text-dim">
        batches
      </div>
      {batchSection}
      {snap != null && snapAge != null && (
        <div className="jobs-panel-foot pt-1 text-right text-[10px] text-text-dim">fetched {snapAge}</div>
      )}
    </div>
  );
}
