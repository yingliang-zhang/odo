// UX-2 (D5 Stage 0 / A2-5): the Jobs chip's pure derivations — phase
// classification from wire conditions, completion/age formatting, the
// count-only chip face, and the degrade-path labels.

import { describe, expect, it } from "vitest";
import type { K8sJob } from "./types";
import {
  activeJobCount,
  chipLabel,
  formatAge,
  formatCompletions,
  jobPhase,
  lastGoodAge,
  reasonLabel,
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

describe("reasonLabel", () => {
  it("covers every non-off cause class with one sentence", () => {
    expect(reasonLabel("ENOENT")).toContain("kubectl");
    expect(reasonLabel("timeout")).toContain("timed out");
    expect(reasonLabel("auth")).toContain("credentials");
    expect(reasonLabel("unreachable")).toContain("unreachable");
    expect(reasonLabel("bad_namespace")).toContain("bad_namespace");
  });
});
