// UX-2 (D5 Stage 0 / A2-5): the Jobs chip's pure derivations — phase
// classification from wire conditions, completion/age formatting, the
// count-only chip face, and the degrade-path labels.

import { describe, expect, it } from "vitest";
import type { K8sBatch, K8sJob } from "./types";
import {
  activeBatchCount,
  activeJobCount,
  batchEta,
  batchFraction,
  batchOneLiner,
  chipLabel,
  formatAge,
  formatCompletions,
  jobPhase,
  lastGoodAge,
  nsActiveCount,
  reasonLabel,
  sortJobsForTable,
  staleLabel,
} from "./jobs";

function job(over: Partial<K8sJob> = {}): K8sJob {
  return { metadata: { name: "j", creationTimestamp: "2026-09-01T00:00:00Z" }, ...over };
}

describe("jobPhase", () => {
  it("reads the last True terminal condition", () => {
    expect(
      jobPhase(job({ status: { conditions: [{ type: "Complete", status: "True" }] } })),
    ).toBe("Complete");
    expect(
      jobPhase(job({ status: { conditions: [{ type: "FailureTarget", status: "True" }] } })),
    ).toBe("Failed");
    expect(
      jobPhase(
        job({
          status: {
            conditions: [
              { type: "Complete", status: "False" },
              { type: "SuccessCriteriaMet", status: "True" },
            ],
          },
        }),
      ),
    ).toBe("SuccessCriteriaMet");
  });

  it("falls through active → failed → Pending with no conditions", () => {
    expect(jobPhase(job({ status: { active: 1 } }))).toBe("Active");
    expect(jobPhase(job({ status: { failed: 1 } }))).toBe("Failed");
    expect(jobPhase(job({}))).toBe("Pending");
  });

  it("terminal tags outrank active", () => {
    expect(
      jobPhase(
        job({
          status: {
            active: 1,
            conditions: [{ type: "Complete", status: "True" }],
          },
        }),
      ),
    ).toBe("Complete");
  });
});

describe("formatAge", () => {
  const NOW = Date.parse("2026-09-01T12:00:00Z");
  it("buckets s/m/h/d and never goes negative", () => {
    expect(formatAge(NOW, "2026-09-01T11:59:15Z")).toBe("45s");
    expect(formatAge(NOW, "2026-09-01T11:48:00Z")).toBe("12m");
    expect(formatAge(NOW, "2026-09-01T08:00:00Z")).toBe("4h");
    expect(formatAge(NOW, "2026-08-29T12:00:00Z")).toBe("3d");
    expect(formatAge(NOW, "2026-09-02T00:00:00Z")).toBe("0s"); // skew → 0
  });

  it("degrades to ? on unparsable input", () => {
    expect(formatAge(NOW, "not-a-date")).toBe("?");
    expect(formatAge(NOW, undefined)).toBe("?");
  });
});

describe("formatCompletions", () => {
  it("renders succeeded/target, and succeeded alone for compressible jobs", () => {
    expect(
      formatCompletions(job({ spec: { completions: 1 }, status: { succeeded: 1 } })),
    ).toBe("1/1");
    expect(
      formatCompletions(job({ spec: { completions: 4 }, status: { succeeded: 0 } })),
    ).toBe("0/4");
    expect(formatCompletions(job({ status: { succeeded: 2 } }))).toBe("2/—");
    expect(formatCompletions(job({ spec: { completions: 3 } }))).toBe("0/3");
  });
});

describe("activeJobCount + chipLabel", () => {
  const jobs: K8sJob[] = [
    job({ metadata: { name: "active" }, status: { active: 1 } }),
    job({ metadata: { name: "done" }, status: { conditions: [{ type: "Complete", status: "True" }] } }),
    job({ metadata: { name: "wait" } }),
  ];

  it("counts only live phases", () => {
    expect(activeJobCount(jobs)).toBe(2);
  });

  it("chip face is count-only with a truncation marker, never a bar", () => {
    expect(chipLabel(jobs, false)).toBe("Jobs · 3");
    expect(chipLabel(jobs, true)).toBe("Jobs · 3+");
    expect(chipLabel([], false)).toBe("Jobs · 0");
  });
});

describe("lastGoodAge", () => {
  it("formats the stale-window age, null without a fetch stamp", () => {
    expect(lastGoodAge(1_000_000, 1_000_000 - 90)).toBe("1m ago");
    expect(lastGoodAge(1_000_000, 1_000_000 - 30)).toBe("30s ago");
    expect(lastGoodAge(1_000_000, 1_000_000 - 72_000)).toBe("20h ago");
    expect(lastGoodAge(1_000_000, 1_000_000 - 400_000)).toBe("4d ago");
    expect(lastGoodAge(1_000_000, undefined)).toBeNull();
    expect(lastGoodAge(1_000_000, 0)).toBeNull();
  });
});

// ---------- A4 (multi-ns) + D5b (batch bridge) ----------

function nsJob(ns: string, name: string, createdAt: string, over: Partial<K8sJob> = {}): K8sJob {
  return { metadata: { name, namespace: ns, creationTimestamp: createdAt }, ...over };
}

describe("nsActiveCount", () => {
  it("counts live phases inside ONE namespace of a flat payload", () => {
    const jobs = [
      nsJob("default", "a", "2026-09-01T00:00:00Z", { status: { active: 1 } }),
      nsJob("lab", "b", "2026-09-01T00:00:00Z", { status: { active: 1 } }),
      nsJob("lab", "c", "2026-09-01T00:00:00Z", { status: { conditions: [{ type: "Complete", status: "True" }] } }),
    ];
    expect(nsActiveCount(jobs, "default")).toBe(1);
    expect(nsActiveCount(jobs, "lab")).toBe(1);
    expect(nsActiveCount(jobs, "absent")).toBe(0);
  });
});

describe("sortJobsForTable", () => {
  it("orders by configured namespace order, then age newest-first", () => {
    const jobs = [
      nsJob("lab", "old-lab", "2026-09-01T00:00:00Z"),
      nsJob("default", "new-default", "2026-09-01T02:00:00Z"),
      nsJob("lab", "new-lab", "2026-09-01T03:00:00Z"),
      nsJob("default", "newer-default", "2026-09-01T04:00:00Z"),
    ];
    const sorted = sortJobsForTable(jobs, ["default", "lab"]);
    expect(sorted.map((j) => j.metadata?.name)).toEqual([
      "newer-default",
      "new-default",
      "new-lab",
      "old-lab",
    ]);
  });

  it("sinks rows outside the configured set after the known groups", () => {
    const jobs = [
      nsJob("default", "known", "2026-09-01T00:00:00Z"),
      nsJob("stray", "stray", "2026-09-02T00:00:00Z"), // newer but unknown-ns
    ];
    const sorted = sortJobsForTable(jobs, ["default"]);
    expect(sorted.map((j) => j.metadata?.name)).toEqual(["known", "stray"]);
  });
});

function batch(over: Partial<K8sBatch>): K8sBatch {
  return { batch: "b", status: "running", ...over };
}

describe("activeBatchCount", () => {
  it("counts only running & fresh rows — stale and degraded never badge", () => {
    expect(activeBatchCount([
      batch({ status: "running", stale: false }),
      batch({ status: "running", stale: true }),   // frozen = unknown (B4)
      batch({ status: "done" }),
      batch({ reason: "schema_mismatch" }),
    ])).toBe(1);
  });
});

describe("batchFraction + batchEta", () => {
  it("clamps the fraction and hides on unknown totals", () => {
    expect(batchFraction(batch({ total: 100, done: 72 }))).toBeCloseTo(0.72);
    expect(batchFraction(batch({ total: 0, done: 3 }))).toBeNull();
    expect(batchFraction(batch({ total: 10, done: 42 }))).toBe(1);
  });

  it("derives a CEIL'd ETA, hidden when the rate can't carry it", () => {
    expect(batchEta(batch({ total: 250, done: 180, rate_per_min: 5.3 }))).toBe("14m");
    expect(batchEta(batch({ total: 100, done: 100, rate_per_min: 5 }))).toBeNull();
    expect(batchEta(batch({ total: 100, done: 0, rate_per_min: 0 }))).toBeNull();
    expect(batchEta(batch({ total: 100, done: 50, rate_per_min: 0.5 }))).toBe("1h40m");
    expect(batchEta(batch({ total: 100, done: 80, rate_per_min: 0.5 }))).toBe("40m");
  });
});

describe("batchOneLiner + staleLabel", () => {
  it("composes the running line with ETA, never a bar", () => {
    expect(batchOneLiner(batch({ batch: "dsv", total: 250, done: 180, rate_per_min: 5.3 }), 1_000_000)).toBe("dsv 72% · ETA 14m");
  });

  it("flags staleness in the running line", () => {
    const b = batch({ batch: "frozen", total: 10, done: 2, rate_per_min: 0, updated_unix: 1_000_000 - 300, stale: true });
    expect(batchOneLiner(b, 1_000_000)).toBe("frozen 20% · stale — last update 5m ago");
  });

  it("done rows surface err counts; reason rows surface the class", () => {
    expect(batchOneLiner(batch({ batch: "d", status: "done", errs: 3 }), 0)).toBe("d done · 3 errs");
    expect(batchOneLiner(batch({ batch: "d", status: "done", errs: 0 }), 0)).toBe("d done");
    expect(batchOneLiner(batch({ batch: "bad.json", reason: "schema_mismatch" }), 0)).toBe("bad.json — schema_mismatch");
  });

  it("staleLabel formats the heartbeat age", () => {
        expect(staleLabel(batch({ updated_unix: 1_000_000 - 120 }), 1_000_000)).toBe("stale — last update 2m ago");
  });
});

describe("reasonLabel", () => {
  it("covers every non-off cause class with one sentence", () => {
    expect(reasonLabel("ENOENT")).toContain("kubectl");
    expect(reasonLabel("timeout")).toContain("timed out");
    expect(reasonLabel("auth")).toContain("credentials");
    expect(reasonLabel("unreachable")).toContain("unreachable");
    expect(reasonLabel("bad_namespace")).toContain("bad_namespace");
  });
});
