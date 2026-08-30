import { describe, expect, it } from "vitest";

// P1.5 (docs/design/adoption-lock.md): the ordered summarizer map. Row
// ORDER is the classifier — tests pin both the grounded bridge shapes and
// the precedence (a wrapped string must hit its specific row, never the
// generic one below it).

import {
  ERROR_RULES,
  FAILURE_TAXONOMY,
  classifyFailure,
  summarizeError,
  type FailureClass,
  type FailureSpec,
} from "./errors";

describe("error summarizer (P1.5)", () => {
  it("classifies a dead daemon socket", () => {
    const s = summarizeError("poll failed: connect /tmp/proj/.odo/odo.sock: Connection refused (os error 61)");
    expect(s?.summary).toBe("Daemon unreachable — its socket is dead");
    expect(s?.action).toContain("respawns");
  });

  it("respawn-failure string escapes the generic connect row (order matters)", () => {
    const s = summarizeError(
      "connect /tmp/proj/.odo/odo.sock: Connection refused (daemon restart failed: spawn daemon /usr/local/bin/odo: exit status 3)",
    );
    expect(s?.summary).toBe("Daemon died and could not be respawned");
  });

  it("classifies daemon startup timeout", () => {
    const s = summarizeError("daemon did not answer on /tmp/x/.odo/odo.sock within 10s (see /tmp/x/.odo/daemon.log)");
    expect(s?.summary).toBe("Daemon failed to start in time");
  });

  it("classifies mid-request drop", () => {
    expect(summarizeError("send failed: daemon closed the connection without responding")?.summary).toBe(
      "Daemon dropped mid-request",
    );
  });

  it("classifies path-gate and missing-path bridge errors", () => {
    expect(summarizeError("Path outside project root: /etc/passwd (resolved to /etc/passwd)")?.summary).toBe(
      "Blocked a path escape",
    );
    expect(summarizeError("Path not found: /tmp/gone (No such file or directory)")?.summary).toBe("Path not found");
  });

  it("the broad timeout row is last — specific rows win first", () => {
    // A string containing BOTH a specific shape and a timeout word must
    // land on the specific row.
    const s = summarizeError("connect /tmp/a/odo.sock: operation timed out");
    expect(s?.summary).toBe("Daemon unreachable — its socket is dead");
    // …while a naked transport timeout still classifies.
    expect(summarizeError("read /tmp/a/odo.sock: timed out waiting for response")?.summary).toBe("Request timed out");
  });

  it("returns null for unclassified strings — legacy banner path", () => {
    expect(summarizeError("frobnicate exploded")).toBeNull();
    expect(summarizeError("create workstream failed: name already taken")).toBeNull();
  });

  it("every rule carries a summary; actions are user-guidance sentences", () => {
    for (const r of ERROR_RULES) {
      expect(r.summary.length).toBeGreaterThan(0);
      if (r.action !== undefined) expect(r.action.length).toBeGreaterThan(0);
    }
  });
});

// P2.5 (docs/design/adoption-lock.md): the classification channel on top
// of the same ordered map. Fixtures are the real shapes: bridge format!
// strings for the transport classes, the journaled auto-land phrases for
// the infra classes (internal/ipc/autoland.go, verify_advisory.go,
// grounded.go). ORDER is the classifier here too — the order pins below
// fire exactly when a row is reordered.
describe("failure taxonomy (P2.5)", () => {
  it("classifies a dead daemon socket", () => {
    expect(classifyFailure("connect /tmp/.odo/odo.sock: connection refused")?.cls).toBe("socket_closed");
  });

  it("classifies a mid-request drop as socket_closed", () => {
    expect(classifyFailure("send failed: daemon closed the connection without responding")?.cls).toBe(
      "socket_closed",
    );
  });

  it("classifies a startup no-answer as heartbeat_timeout", () => {
    expect(classifyFailure("daemon did not answer on /tmp/.odo/odo.sock within 10s (see daemon.log)")?.cls).toBe(
      "heartbeat_timeout",
    );
  });

  it("classifies a garbled reply as version_mismatch", () => {
    expect(classifyFailure("invalid daemon response: unexpected EOF decoding frame")?.cls).toBe(
      "version_mismatch",
    );
  });

  it("classifies a verify-gate block as verify_infra", () => {
    // autoland.go runVerifyGate verify_unconfigured detail, verbatim shape.
    expect(
      classifyFailure("no usable .odo-verify at the repo root — the verify gate is mandatory for auto-land")?.cls,
    ).toBe("verify_infra");
  });

  it("classifies a review-leg transport death as panel_infra", () => {
    // autoland.go settle: blocked{reason:"panel_infra", detail:…} verbatim.
    expect(
      classifyFailure(
        "auto_land_blocked reason=panel_infra: a review leg failed on transport/auth/timeout — infra failures are not verdicts",
      )?.cls,
    ).toBe("panel_infra");
  });

  it("a degraded grounded leg classifies as panel_infra", () => {
    // grounded.go: rr.Comments = "grounded review failed: " + err — the
    // round then fails closed as panel_infra, never as verify infra.
    expect(classifyFailure("grounded review failed: gateway returned 502")?.cls).toBe("panel_infra");
  });

  it("unclassified strings and classified-row-less matches both return null", () => {
    expect(classifyFailure("frobnicate exploded")).toBeNull();
    // Path-gate rows carry no cls: matching them is not a daemon failure.
    expect(classifyFailure("Path outside project root: /etc/passwd")).toBeNull();
  });

  it("ORDER pin: a socket string never classifies as heartbeat_timeout", () => {
    expect(classifyFailure("connect /tmp/a/odo.sock: operation timed out")?.cls).toBe("socket_closed");
  });

  it("ORDER pin: a naked timeout classifies as heartbeat_timeout, never socket_closed", () => {
    expect(classifyFailure("read /tmp/a/odo.sock: timed out waiting for response")?.cls).toBe(
      "heartbeat_timeout",
    );
  });

  it("ORDER pin: the panel_infra detail names 'timeout' yet never classifies as heartbeat_timeout", () => {
    expect(classifyFailure("a review leg failed on transport/auth/timeout")?.cls).toBe("panel_infra");
  });

  it("taxonomy is complete: distinct titles, one-line causes, locked leading actions", () => {
    const want: Record<FailureClass, FailureSpec["action"]> = {
      socket_closed: "reconnect",
      heartbeat_timeout: "reconnect",
      version_mismatch: "copy_diagnostics",
      verify_infra: "open_journal",
      panel_infra: "open_journal",
    };
    const titles = new Set<string>();
    for (const cls of Object.keys(want) as FailureClass[]) {
      const spec = FAILURE_TAXONOMY[cls];
      expect(spec.cls).toBe(cls);
      expect(spec.action).toBe(want[cls]);
      expect(spec.title.length).toBeGreaterThan(0);
      expect(spec.cause.length).toBeGreaterThan(0);
      titles.add(spec.title);
    }
    expect(titles.size).toBe(Object.keys(want).length);
  });
});
