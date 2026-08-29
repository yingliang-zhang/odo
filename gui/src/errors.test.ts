import { describe, expect, it } from "vitest";

// P1.5 (docs/design/adoption-lock.md): the ordered summarizer map. Row
// ORDER is the classifier — tests pin both the grounded bridge shapes and
// the precedence (a wrapped string must hit its specific row, never the
// generic one below it).

import { ERROR_RULES, summarizeError } from "./errors";

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
