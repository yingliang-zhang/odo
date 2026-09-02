// Odo DX wave (Feature 5): commands.ts contract — the one-version schema
// fail-loud classes (mirroring the daemon's loadCommands), the newest-wins
// journal fold, and badge/duration derivations.
import { describe, expect, it } from "vitest";
import { commandBadge, formatCommandDuration, latestCommandResults, parseCommandsConfig } from "./commands";
import type { OdoEvent } from "./types";

const ev = (seq: number, payload: Record<string, unknown>): OdoEvent => ({
  id: seq,
  conversation_id: 1,
  seq,
  type: "command_result",
  payload,
  created_at: "2026-09-02T10:00:00Z",
});

describe("parseCommandsConfig", () => {
  it("parses a version-1 config with defaults elided", () => {
    const out = parseCommandsConfig(
      JSON.stringify({
        version: 1,
        commands: [
          { name: "tests", cmd: "go test ./...", cwd: ".", timeout: 120 },
          { name: "build", cmd: "go build ./..." },
        ],
      }),
    );
    expect(out.error).toBeUndefined();
    expect(out.specs).toEqual([
      { name: "tests", cmd: "go test ./...", cwd: ".", timeout: 120 },
      { name: "build", cmd: "go build ./..." },
    ]);
  });

  it("rejects malformed JSON and non-1 versions with named errors", () => {
    expect(parseCommandsConfig("{").error).toContain("not valid JSON");
    expect(parseCommandsConfig("{}").error).toContain("unsupported version");
    expect(parseCommandsConfig('{"version":2,"commands":[]}').error).toContain("unsupported version");
    expect(parseCommandsConfig('"nope"').error).toContain("top level");
  });

  it("rejects entry defects fail-loud like the daemon", () => {
    expect(parseCommandsConfig('{"version":1,"commands":"nope"}').error).toContain("commands must be an array");
    expect(parseCommandsConfig('{"version":1,"commands":[42]}').error).toContain("must be objects");
    expect(parseCommandsConfig('{"version":1,"commands":[{"cmd":"true"}]}').error).toContain("non-empty name");
    expect(parseCommandsConfig('{"version":1,"commands":[{"name":"a"}]}').error).toContain("command a has an empty cmd");
    expect(
      parseCommandsConfig('{"version":1,"commands":[{"name":"a","cmd":"true"},{"name":"a","cmd":"false"}]}').error,
    ).toContain('duplicate command name');
  });

  it("accepts an empty registry as a zero-command file (renders nothing)", () => {
    const out = parseCommandsConfig('{"version":1,"commands":[]}');
    expect(out.specs).toEqual([]);
    expect(out.error).toBeUndefined();
  });
});

describe("latestCommandResults", () => {
  it("folds newest-wins per name and skips malformed rows", () => {
    const events: OdoEvent[] = [
      ev(1, { name: "tests", exit_code: 1, stderr_tail: "fail" }),
      ev(2, { name: "tests", exit_code: 0, stdout_tail: "pass", duration_ms: 900 }),
      ev(3, { name: "lint", exit_code: 2 }),
      ev(4, { name: "", exit_code: 0 }), // malformed: no name
      { ...ev(5, { exit_code: 0 }), type: "user_message" }, // wrong type
    ];
    const out = latestCommandResults(events);
    expect(out.get("tests")).toMatchObject({ exit_code: 0, stdout_tail: "pass", duration_ms: 900 });
    expect(out.get("lint")).toMatchObject({ exit_code: 2 });
    expect(out.size).toBe(2);
  });
});

describe("commandBadge / formatCommandDuration", () => {
  it("greens only on exit 0 and names the timeout", () => {
    expect(commandBadge({ name: "a", exit_code: 0, duration_ms: 1 })).toEqual({ ok: true, text: "ok" });
    expect(commandBadge({ name: "a", exit_code: 3, duration_ms: 1 })).toEqual({ ok: false, text: "exit 3" });
    expect(commandBadge({ name: "a", exit_code: -1, duration_ms: 1, timed_out: true })).toEqual({
      ok: false,
      text: "timed out",
    });
  });

  it("formats durations", () => {
    expect(formatCommandDuration(640)).toBe("640ms");
    expect(formatCommandDuration(1200)).toBe("1s");
    expect(formatCommandDuration(65000)).toBe("1m 5s");
  });
});
