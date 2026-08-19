import { describe, expect, it } from "vitest";
import { isAdvisorySlash } from "./slash";

// The advisory detach and the daemon's blocking RPC must agree on every
// input or one side regresses the freeze the other fixed: a slash the
// daemon treats as advisory but the GUI doesn't would lock the composer
// for the whole consult, and the reverse would detach a prompt that never
// consults. Every case below is decided by handleSendMessage's routing in
// internal/ipc/server.go (TrimSpace → TrimPrefix(cmd) → rest starts with
// a LITERAL space or is empty); the expected values are the daemon's,
// spelled out so a daemon-side change shows up as a diff here.
describe("isAdvisorySlash mirrors the daemon's slash routing", () => {
  it.each([
    // Advisory: three commands, token followed by a literal space or end.
    ["/panel 那么要怎么优化呢?", true],
    ["/panel", true],
    ["/vision describe this screenshot", true],
    ["/preview http://localhost:1420 look at the sidebar", true],
    ["  /panel padded  ", true], // TrimSpace parity
    ["/panel  two spaces still advisory", true],
    // NOT advisory: anything else falls through to the normal send path.
    ["/panelxyz", false], // token must end at space/end, not a longer word
    ["/panel\tindented arg", false], // tab is not the daemon's delimiter
    ["/panel\nsecond line", false], // Shift+Enter newline is not either
    ["/panel nbsp arg", false], // daemon checks a literal 0x20
    ["/ panel with a gap", false], // prefix must start with the command
    ["/PANEL uppercase", false], // daemon routing is case-sensitive
    ["/loop audit", false], // /loop spawns a run — the locking path is right
    ["/vision3 models", false],
    ["plain message mentioning /panel mid-text", false],
    ["", false],
    ["/", false],
  ])("routes %j as advisory=%s", (text, expected) => {
    expect(isAdvisorySlash(text)).toBe(expected);
  });
});
