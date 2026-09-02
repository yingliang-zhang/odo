// Odo DX wave (Feature 5 — Run/Test hub): .odo/commands.json parsing, the
// journaled command_result fold, and row derivations for the Runs tab's
// Commands section. Pure module (jobs.ts convention) — RunsPanel stays a
// dumb renderer; every piece of logic here is vitest-covered.

import type { CommandResult, EventPayload, OdoEvent } from "./types";

// One registered command row (after validation), mirroring the daemon's
// commandSpec — the GUI parses the same file to LIST the buttons; the
// daemon re-parses at exec time and refuses anything this parser accepted
// optimistically (the exec side is authoritative).
export interface CommandSpec {
  name: string;
  cmd: string;
  cwd?: string;
  timeout?: number;
}

// Parse the hub's config body against the one-version schema — the same
// fail-loud classes as the daemon's loadCommands, so a file that passes
// here executes there. specs null means "render nothing" (missing or
// 0-command file per the zero-clutter contract); the error string is for
// a file that EXISTS but is broken, which the user wants named.
export function parseCommandsConfig(content: string): { specs: CommandSpec[] | null; error?: string } {
  let raw: unknown;
  try {
    raw = JSON.parse(content);
  } catch (e) {
    return { specs: null, error: `commands.json is not valid JSON: ${e instanceof Error ? e.message : String(e)}` };
  }
  if (raw === null || typeof raw !== "object") {
    return { specs: null, error: "commands.json: top level must be an object" };
  }
  const cfg = raw as { version?: unknown; commands?: unknown };
  if (cfg.version !== 1) {
    return { specs: null, error: `commands.json: unsupported version ${String(cfg.version)} (want version 1)` };
  }
  if (!Array.isArray(cfg.commands)) {
    return { specs: null, error: "commands.json: commands must be an array" };
  }
  const specs: CommandSpec[] = [];
  for (const entry of cfg.commands) {
    if (entry === null || typeof entry !== "object") {
      return { specs: null, error: "commands.json: command entries must be objects" };
    }
    const spec = entry as { name?: unknown; cmd?: unknown; cwd?: unknown; timeout?: unknown };
    if (typeof spec.name !== "string" || spec.name.trim() === "") {
      return { specs: null, error: "commands.json: every command needs a non-empty name" };
    }
    if (typeof spec.cmd !== "string" || spec.cmd.trim() === "") {
      return { specs: null, error: `commands.json: command ${spec.name} has an empty cmd` };
    }
    if (specs.some((s) => s.name === spec.name)) {
      return { specs: null, error: `commands.json: duplicate command name "${spec.name}"` };
    }
    specs.push({
      name: spec.name,
      cmd: spec.cmd,
      ...(typeof spec.cwd === "string" && spec.cwd !== "" ? { cwd: spec.cwd } : {}),
      ...(typeof spec.timeout === "number" ? { timeout: spec.timeout } : {}),
    });
  }
  return { specs };
}

// latestCommandResults folds the journal's command_result rows per command
// name — iteration is seq order, so a later row overwrites the earlier and
// the map ends newest-wins. The Runs tab badges survive a remount (LRU
// park) and a reload (bootstrap replay) without re-running anything.
export function latestCommandResults(events: OdoEvent[]): Map<string, CommandResult> {
  const out = new Map<string, CommandResult>();
  for (const ev of events) {
    if (ev.type !== "command_result") continue;
    const p: EventPayload = ev.payload ?? {};
    if (typeof p.name !== "string" || p.name === "") continue;
    out.set(p.name, {
      name: p.name,
      // Quad-audit P3: default 1, never 0 — a malformed row folding as
      // GREEN would lie about a command nobody saw succeed. Corrupt rows
      // must fail visible (the badge reds as "exit 1").
      exit_code: p.exit_code ?? 1,
      stdout_tail: p.stdout_tail,
      stderr_tail: p.stderr_tail,
      duration_ms: p.duration_ms ?? 0,
      timed_out: p.timed_out,
    });
  }
  return out;
}

// The badge derivation: green on exit 0, red otherwise; a deadline kill
// reds with "timed out" regardless of the (taxonomic) -1 code.
export function commandBadge(res: CommandResult): { ok: boolean; text: string } {
  if (res.timed_out === true) return { ok: false, text: "timed out" };
  if (res.exit_code === 0) return { ok: true, text: "ok" };
  return { ok: false, text: `exit ${res.exit_code}` };
}

// Wall-time formatting for command rows (ms in, glanceable label out):
// bare ms under a second, seconds under a minute, m+s after — same spirit
// as RunsPanel's formatDuration (ISO-based, hence private there).
export function formatCommandDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const total = Math.floor(ms / 1000);
  if (total < 60) return `${total}s`;
  return `${Math.floor(total / 60)}m ${total % 60}s`;
}
