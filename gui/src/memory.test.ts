import { describe, expect, it } from "vitest";

import { deriveAutoBackoff } from "./memory";
import type { EventPayload, OdoEvent } from "./types";

function ev(seq: number, type: OdoEvent["type"], payload: EventPayload = {}): OdoEvent {
  return { id: seq, conversation_id: 1, seq, type, payload, created_at: "2026-08-29T00:00:00.000Z" };
}

function distill(seq: number, cause: string, detail = ""): OdoEvent {
  return ev(seq, "memory_update", { layer: "auto_distill", cause, detail });
}

function curator(seq: number, cause: string, detail = ""): OdoEvent {
  return ev(seq, "memory_update", { layer: "curator", cause, detail });
}

const NOW = new Date("2026-09-01T13:00:00Z");
const future = "2026-09-02T13:15:00Z"; // > NOW (autoCurateFailureBackoff = 24h)
const past = "2026-09-01T12:00:00Z"; // < NOW

describe("deriveAutoBackoff (UX-3c)", () => {
  it("is quiet with no events and no memory_update rows", () => {
    expect(deriveAutoBackoff([])).toEqual({ distill: null, curate: null });
    expect(
      deriveAutoBackoff([ev(1, "user_message", { text: "hi" }), ev(2, "agent_done", { summary: "s" })]),
    ).toEqual({ distill: null, curate: null });
  });

  it("surfaces the below-min-bytes idle line from a distill skip", () => {
    const b = deriveAutoBackoff([
      distill(1, "skipped", "trigger=idle window_events=3 window_bytes=120 reason=below_min_bytes"),
    ]);
    expect(b.distill).toEqual({ kind: "idle", text: "auto-distill idle — below min bytes" });
    expect(b.curate).toBeNull();
  });

  it("maps every pause-class distill reason, including the failure ladder", () => {
    const cases: [string, string][] = [
      ["reason=below_min_events", "auto-distill idle — below min events"],
      ["reason=hourly_cap", "auto-distill paused — hourly cap reached"],
      ["reason=backoff retry_after=4m59s", "auto-distill paused — failure backoff"],
      ["reason=backoff_suspended", "auto-distill paused — retries suspended after repeated failures"],
    ];
    for (const [detail, text] of cases) {
      const b = deriveAutoBackoff([distill(1, "skipped", `trigger=idle window_events=9 window_bytes=900 ${detail}`)]);
      expect(b.distill?.text).toBe(text);
    }
  });

  it("hides transient and config-fact skip reasons (no speculative states)", () => {
    for (const reason of ["disabled", "run_active", "slash_active", "distill_active", "superseded_by_urgent", "superseded_by_manual", "disarmed_by_send", "window_fresh"]) {
      const b = deriveAutoBackoff([distill(1, "skipped", `trigger=idle window_events=0 window_bytes=0 reason=${reason}`)]);
      expect(b.distill, reason).toBeNull();
    }
  });

  it("hides an unknown future reason rather than rendering raw daemon text", () => {
    const b = deriveAutoBackoff([distill(1, "skipped", "trigger=idle reason=quantum_entangled")]);
    expect(b.distill).toBeNull();
  });

  it("latest row per layer wins — a fired distill clears an earlier skip", () => {
    const b = deriveAutoBackoff([
      distill(1, "skipped", "trigger=idle window_events=1 window_bytes=8 reason=below_min_bytes"),
      distill(2, "fired", "trigger=idle window_events=42 window_bytes=9000"),
    ]);
    expect(b.distill).toBeNull();
  });

  it("a skip newer than a fired row resurfaces the line", () => {
    const b = deriveAutoBackoff([
      distill(1, "fired", "trigger=idle window_events=42 window_bytes=9000"),
      distill(2, "skipped", "trigger=idle window_events=1 window_bytes=8 reason=below_min_bytes"),
    ]);
    expect(b.distill?.text).toBe("auto-distill idle — below min bytes");
  });

  it("surfaces curator backoff with the journaled next-eligible clock time", () => {
    const b = deriveAutoBackoff(
      [curator(1, "skipped", `trigger=notes_idle notes_since=3 reason=backoff next_eligible_at=${future}`)],
      NOW,
    );
    expect(b.curate?.kind).toBe("paused");
    expect(b.curate?.text).toMatch(/^auto-curate paused — next eligible \d{1,2}:\d{2}(?:\s*[AP]M)?$/);
  });

  it("a stale curator horizon no longer shows (the gate lifted silently)", () => {
    const b = deriveAutoBackoff(
      [curator(1, "skipped", `trigger=notes_idle notes_since=3 reason=backoff next_eligible_at=${past}`)],
      NOW,
    );
    expect(b.curate).toBeNull();
  });

  it("a newer curate pass clears the pause", () => {
    const b = deriveAutoBackoff(
      [
        curator(1, "skipped", `trigger=notes_idle notes_since=3 reason=backoff next_eligible_at=${future}`),
        curator(2, "curate", "curated 4 notes"),
      ],
      NOW,
    );
    expect(b.curate).toBeNull();
  });

  it("tracks the two layers independently (both can be paused at once)", () => {
    const b = deriveAutoBackoff(
      [
        distill(1, "skipped", "trigger=idle window_events=1 window_bytes=8 reason=below_min_bytes"),
        curator(2, "skipped", `trigger=notes_idle notes_since=1 reason=backoff next_eligible_at=${future}`),
      ],
      NOW,
    );
    expect(b.distill?.text).toBe("auto-distill idle — below min bytes");
    expect(b.curate?.text).toMatch(/^auto-curate paused/);
  });

  it("ignores other memory layers (apply/learner rows are not scheduler state)", () => {
    const b = deriveAutoBackoff(
      [ev(1, "memory_update", { layer: "apply", cause: "auto_apply_failed", detail: "boom" })],
      NOW,
    );
    expect(b).toEqual({ distill: null, curate: null });
  });

  it("folds in seq order even when events arrive unsorted", () => {
    const b = deriveAutoBackoff(
      [
        distill(2, "fired", "trigger=idle window_events=4 window_bytes=400"),
        distill(1, "skipped", "trigger=idle window_events=1 window_bytes=8 reason=below_min_bytes"),
      ],
      NOW,
    );
    expect(b.distill).toBeNull();
  });

  it("missing payload fields degrade to quiet, never to a fabricated line", () => {
    const b = deriveAutoBackoff([ev(1, "memory_update", {}), distill(2, "skipped"), curator(3, "skipped")], NOW);
    expect(b).toEqual({ distill: null, curate: null });
  });
});
