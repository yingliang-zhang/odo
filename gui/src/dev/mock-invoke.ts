// E P2: mock invoke — replaces Tauri's invoke() in browser dev mode.
// Detects absence of __TAURI_INTERNALS__ (plain browser) and routes
// command names to fixture data. Same function signature as invoke(),
// so api.ts code is unchanged — only the transport is swapped.

import * as fx from "./fixtures";
import { deriveLoopStates } from "../loop";
import { isAdvisorySlash } from "../slash";

// Detect Tauri webview: __TAURI_INTERNALS__ is injected by Tauri v2.
function isTauri(): boolean {
  return typeof window !== "undefined" && "__TAURI_INTERNALS__" in window;
}

// The mock invoke mirrors the Tauri v2 invoke signature:
//   invoke<T>(cmd: string, args?: InvokeArgs): Promise<T>
// We use `any` for args because InvokeArgs is a Tauri internal type.
//
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export async function mockInvoke(cmd: string, args?: Record<string, any>): Promise<unknown> {
  // Small delay to simulate network/IPC latency
  const { promise: latency, resolve: latencyDone } = Promise.withResolvers<void>();
  setTimeout(latencyDone, 50);
  await latency;

  switch (cmd) {
    // ---------- Lifecycle ----------
    case "bootstrap": {
      if (fx.bootstrapCtl.fail) {
        throw new Error("bootstrap: connection refused (knob)");
      }
      if (fx.bootstrapCtl.delayMs > 0) {
        const { promise, resolve } = Promise.withResolvers<void>();
        setTimeout(resolve, fx.bootstrapCtl.delayMs);
        await promise;
      }
      const payload = fx.makeBootstrap(args?.projectRoot ?? undefined, args?.workstreamId ?? undefined);
      // Serve-time landing signal: the fail/delay knobs were already
      // consulted above, so a test observing this count knows arming a
      // failure now can only target FUTURE bootstraps.
      const key = String(args?.workstreamId ?? "");
      fx.bootstrapLandings[key] = (fx.bootstrapLandings[key] ?? 0) + 1;
      return payload;
    }
    case "list_projects": {
      return fx.projects;
    }
    case "add_project": {
      // Browser can't show a native folder picker — return null (user "cancelled")
      return null;
    }
    case "remove_project": {
      // Fresh array for the caller (React bails on Object.is-equal state)
      // while the module fixture stays mutated for later list_projects.
      const kept = fx.projects.filter(p => p.root !== args?.root);
      fx.projects.splice(0, fx.projects.length, ...kept);
      return kept;
    }
    case "open_path": {
      // Browser dev mode — no-op (can't open files). Log for dev visibility.
      console.log("[mock] open_path:", args?.path, args?.reveal ? "(reveal)" : "(open)");
      return args?.path ?? "";
    }
    case "read_file": {
      // Browser dev mode: return the fixture root path with a short stub
      // content so the preview dialog renders in dev.
      return {
        file_content: `// mock preview of ${args?.path ?? ""}\nexport const devModeStub = true;\n`,
        file_resolved: `${fx.projects[0].root}/${args?.path ?? ""}`,
        file_truncated: false,
      };
    }

    // ---------- Workstreams ----------
    case "list_workstreams": {
      const root = args?.projectRoot ?? fx.projects[0].root;
      return { ok: true, workstreams: fx.workstreams[root] ?? [] };
    }
    case "create_workstream": {
      const root = args?.projectRoot ?? fx.projects[0].root;
      const name = args?.name ?? "new-workstream";
      const wsList = fx.workstreams[root] ?? [];
      const newId = Math.max(0, ...wsList.map(w => w.id)) + 1;
      const ws = { id: newId, project_id: 1, name, branch: name, status: "active", created_at: new Date().toISOString() };
      wsList.push(ws);
      fx.workstreams[root] = wsList;
      return { ok: true, workstream: ws };
    }
    case "rename_workstream": {
      const root = args?.projectRoot ?? fx.projects[0].root;
      const wsList = fx.workstreams[root] ?? [];
      const ws = wsList.find(w => w.id === args?.workstreamId);
      if (ws) { ws.name = args?.name ?? ws.name; ws.branch = args?.name ?? ws.branch; }
      return { ok: true, workstream: ws };
    }
    case "delete_workstream": {
      const root = args?.projectRoot ?? fx.projects[0].root;
      const wsList = fx.workstreams[root] ?? [];
      fx.workstreams[root] = wsList.filter(w => w.id !== args?.workstreamId);
      return { ok: true, workstreams: fx.workstreams[root] };
    }

    // ---------- Messaging ----------
    case "send_message": {
      const convId = args?.conversationId ?? 1;
      // Advisory slash commands (/panel, /vision, /preview) route FIRST in
      // the daemon's handleSendMessage — BEFORE the park/steer branches
      // below (their request flags are ignored for a slash text; mock
      // parity) — and the RPC holds for the whole consult. Timing mirrors
      // handlePanelQuery: entry-gate refusals reject pre-journal
      // (fx.advisorySend.fail); otherwise the question journals
      // immediately (poll-visible while the consult runs), the RPC holds
      // (fx.advisorySend.hold), then the combined answer + agent_done
      // journal before ok returns — or a late refusal without answer rows
      // when released with an error.
      if (typeof args?.text === "string" && isAdvisorySlash(args.text)) {
        if (fx.advisorySend.fail != null) {
          return { ok: false, error: fx.advisorySend.fail };
        }
        const event = fx.ev("user_message", { text: args.text }, convId);
        fx.events.push(event);
        if (fx.advisorySend.hold && !fx.advisorySend.released) {
          await new Promise<void>((resolve) => fx.advisorySend.waiters.push(resolve));
        }
        if (fx.advisorySend.releaseError != null) {
          return { ok: false, error: fx.advisorySend.releaseError };
        }
        fx.events.push(
          fx.ev("agent_text", { text: "Mock panel advisory answer.", panel: true, models: [] }, convId),
          fx.ev("agent_done", { panel: true }, convId),
        );
        return { ok: true, event };
      }
      // W6 (goal queue): park journals user_message{park:true} and bumps
      // the queue depth. Mirror the daemon cap (goalQueueCap=8): over-cap
      // parks fail loud pre-journal — never silently drop a human message.
      if (args?.park) {
        if (fx.syncParkedGoals(convId) >= 8) {
          return { ok: false, error: "send_message: parked goal queue full (8)" };
        }
        const event = fx.ev("user_message", { text: args?.text ?? "", park: true }, convId);
        fx.events.push(event);
        return { ok: true, event, parked: fx.syncParkedGoals(convId) };
      }
      // Steer queue: steer journals user_message{steer:true} for the
      // running agent — pushed into the fixture journal (same pattern as
      // park above) because the steer derivation is journal-only: the
      // returned row must also be poll-visible.
      if (args?.steer) {
        const payload: Record<string, unknown> = { text: args?.text ?? "", steer: true };
        if (Array.isArray(args?.attachments) && args.attachments.length > 0) {
          payload.attachments = args.attachments;
        }
        const event = fx.ev("user_message", payload, convId);
        fx.events.push(event);
        return { ok: true, event };
      }
      return { ok: true, event: fx.ev("user_message", { text: args?.text ?? "" }, convId) };
    }
    case "cancel": {
      fx.cancelCount.n += 1;
      return { ok: true };
    }
    // M19 (/loop): chip buttons + notification receipt. Journals the
    // daemon-true row so the poll path delivers exactly what
    // loop_journal.go's journalLoop would (the fold reads payload.kind);
    // stop/resume resolve the active loop daemon-side — the mock mirrors
    // that with the same fold the GUI runs.
    case "loop_ctl": {
      const convId = args?.conversationId ?? 1;
      const action = args?.action ?? "";
      if (action === "notified") {
        const event = fx.ev("loop_event", {
          kind: "loop_notified",
          loop_id: args?.loopId ?? 0,
          terminal_kind: args?.text ?? "",
          origin: "loop_ctl",
          spent_tokens: 0,
        }, convId);
        fx.events.push(event);
        return { ok: true, event };
      }
      const live = deriveLoopStates(fx.events.filter((e) => e.conversation_id === convId))
        .filter((l) => l.status === "active" || l.status === "suspended")
        .pop();
      if (live == null) {
        return { ok: false, error: "loop: no active loop for this conversation" };
      }
      if (action === "stop") {
        const event = fx.ev("loop_event", {
          kind: "loop_stopped",
          loop_id: live.id,
          detail: "stopped from the GUI",
          origin: "loop_ctl",
          spent_tokens: live.spentTokens,
        }, convId);
        fx.events.push(event);
        return { ok: true, event };
      }
      if (action === "resume") {
        if (live.status !== "suspended") {
          return { ok: false, error: `loop: the loop is not suspended (status ${live.status})` };
        }
        const event = fx.ev("loop_event", {
          kind: "loop_resumed",
          loop_id: live.id,
          cause: live.cause,
          origin: "loop_ctl",
          spent_tokens: live.spentTokens,
        }, convId);
        fx.events.push(event);
        return { ok: true, event };
      }
      return { ok: false, error: `loop_ctl: unknown action "${action}"` };
    }
    // W6 (goal queue): a human resume journals run_prompt{origin:
    // "parked_goal", goal_seqs:[seq]} WITHOUT an actor (the daemon's
    // auto-dequeues carry one) — the poll loop delivers it, the dock
    // reconciles, and the transcript shows the receipt badge.
    case "resume_parked_goal": {
      const convId = args?.conversationId ?? 1;
      const seq = args?.goalSeq ?? 0;
      const event = fx.ev("review_action", { action: "run_prompt", origin: "parked_goal", goal_seqs: [seq] }, convId);
      fx.events.push(event);
      return { ok: true, parked: fx.syncParkedGoals(convId) };
    }
    // W6 (goal queue): a human drop journals parked_goal_dropped
    // {goal_seq}; consumption arrives through the same poll path.
    case "drop_parked_goal": {
      const convId = args?.conversationId ?? 1;
      const seq = args?.goalSeq ?? 0;
      const event = fx.ev("review_action", { action: "parked_goal_dropped", goal_seq: seq }, convId);
      fx.events.push(event);
      return { ok: true, parked: fx.syncParkedGoals(convId) };
    }
    // Steer queue: the derivation is journal-only, so the mock needs no
    // mock-side queue structure — the drop just journals
    // steer_dropped{steer_seq} and the poll loop closes the row. An
    // unknown seq mirrors the daemon's benign reconcile refusal.
    case "drop_queued_steer": {
      const convId = args?.conversationId ?? 1;
      const seq = args?.steerSeq ?? 0;
      const known = fx.events.some(
        (e) => e.conversation_id === convId && e.seq === seq && e.type === "user_message" && e.payload?.steer,
      );
      if (!known) {
        return { ok: false, error: `no queued steer with seq ${seq}` };
      }
      const event = fx.ev("review_action", { action: "steer_dropped", steer_seq: seq }, convId);
      fx.events.push(event);
      return { ok: true };
    }
    case "poll_events": {
      return fx.makePollResponse(args?.conversationId ?? 1, args?.afterSeq ?? undefined);
    }

    // ---------- Diffs ----------
    case "accept_diff": {
      fx.resolveInboxDiff(args?.diffId ?? 0);
      return { ok: true, diff_id: args?.diffId, applied: true };
    }
    case "reject_diff": {
      fx.resolveInboxDiff(args?.diffId ?? 0);
      return { ok: true, diff_id: args?.diffId, applied: false };
    }
    // P1a (review inbox): the Review tab's dataset — same rows the sidebar
    // pills count (resolveInboxDiff keeps the two in step).
    case "list_all_pending_diffs": {
      return { ok: true, all_pending_diffs: [...fx.inboxDiffs] };
    }
    case "review_diff": {
      return {
        ok: true,
        reviews: [
          { model: "kimi-k3", verdict: "accept", comments: "LGTM" },
          { model: "glm-5.2", verdict: "accept", comments: "Clean implementation" },
        ],
      };
    }
    // M15 (O-1 rung-0): a small static snapshot so the DiffViewer header
    // renders its one-liner in dev/e2e without a daemon.
    case "autonomy_status": {
      return {
        ok: true,
        autonomy: {
          project_root: args?.projectRoot ?? "",
          journal: "",
          workstreams_scanned: 1,
          conversations_scanned: 1,
          resolutions: 0,
          unreadable_diffs: 0,
          // Mirror the prefs fixture — the DiffViewer's rung-0 one-liner
          // displays the same auto_apply the settings round-trip serves.
          auto_apply: fx.defaultSettings.auto_apply,
          current_rung: 0,
          rung_thresholds: { rung_1: 10, rung_2: 30 },
          revert_check: "heuristic: >=80% mirrored lines, >=1 shared path, within 7d",
          classes: [
            { class: "C0", description: "never-auto", accepted: 0, rejected: 0, streak: 0, next_threshold: 0, eligible: "" },
            { class: "C1", description: "docs", accepted: 0, rejected: 0, streak: 0, next_threshold: 10, eligible: "" },
            { class: "C2", description: "tests", accepted: 0, rejected: 0, streak: 0, next_threshold: 10, eligible: "" },
            { class: "C3", description: "small in-scope", accepted: 0, rejected: 0, streak: 0, next_threshold: 10, eligible: "" },
            { class: "unclassified", description: "other", accepted: 0, rejected: 0, streak: 0, next_threshold: 0, eligible: "" },
          ],
        },
      };
    }

    // ---------- Settings ----------
    case "get_settings": {
      // Return a COPY — daemon parity (every fetch unmarshals fresh), and
      // Object.is bail-out safety: sharing the fixture reference would let
      // a save mutate the very object React holds, so the refetch's
      // setAppSettings would compare equal and skip the re-render.
      return { ok: true, settings: { ...fx.defaultSettings } };
    }
    case "update_settings": {
      // Mock parity: the daemon persists the prefs blob, so a save is
      // observable by the next get_settings (SettingsPanel → App refetch).
      if (args?.settings && typeof args.settings === "object") {
        Object.assign(fx.defaultSettings, args.settings);
      }
      return { ok: true };
    }

    // ---------- Wiki ----------
    case "list_wiki": {
      return { ok: true, wiki_notes: fx.wikiNotes };
    }
    case "read_wiki": {
      return { ok: true, wiki_content: fx.wikiContent };
    }
    case "list_topics": {
      return { ok: true, wiki_notes: fx.topics };
    }

    // ---------- Memory ----------
    case "read_memory": {
      return { ok: true, memory_content: fx.memoryContent, archive_content: "", user_content: fx.userContent };
    }
    case "memory_proposals": {
      return { ok: true, ...fx.memoryProposals };
    }
    case "apply_memory": {
      // M9: simulate skill writes for accepted skills proposals
      const accepted = args?.accepted ?? [];
      for (const a of accepted) {
        const proposal = fx.memoryProposals.proposals[a.index];
        if (proposal && proposal.target === "skills") {
          const name = proposal.name ?? "unknown-skill";
          fx.addMockSkill({
            name,
            description: proposal.rule.match(/^description:\s*(.+)$/m)?.[1]?.trim() ?? "",
            keywords: [],
            path: `.odo/skills/${name}.md`,
            origin: "agent-authored",
            scope: "project",
          }, proposal.rule);
        }
      }
      return { ok: true, applied: true };
    }
    case "curate": {
      return { ok: true, wiki_path: "wiki/index.md", memory_proposals: 0 };
    }
    case "pin": {
      return { ok: true, applied: true };
    }
    case "read_pins": {
      return { ok: true, memory_content: fx.pinsContent };
    }

    // ---------- Ledger ----------
    case "ledger": {
      return { ok: true, memory_content: fx.ledgerContent };
    }
    case "contradictions": {
      return { ok: true, events: [] };
    }

    // ---------- Visibility ----------
    case "pending_counts": {
      // Copy the array fields: React state holds response values directly,
      // so an in-place fixture mutation (the e2e simulation pattern) would
      // otherwise read Object.is-equal to the previous state and bail the
      // render. Real IPC payloads are always fresh objects; the mock must
      // match that identity discipline.
      // Cross-project tests key the counts per root (fx.countsByRoot) so
      // the two fixture projects can disagree; roots without an entry
      // fall back to the shared globals (legacy single-project specs).
      const ovr = fx.countsByRoot[(args?.projectRoot as string) ?? ""];
      return {
        ok: true,
        pending_counts: ovr?.pending ?? fx.pendingCounts,
        parked_goals: ovr?.parked ?? fx.parkedGoals,
        running_workstreams: [...(ovr?.running ?? fx.runningWorkstreams)],
        auto_distill: [...(fx.autoDistill ?? [])],
        distilling: false,
        distilling_convs: [],
      };
    }
    case "auto_distill_ctl": {
      return { ok: true, disarmed: true };
    }
    // M12 (D-todo): fixture events carry no todo_merge rows, so the Plan
    // chip is hidden in dev mode; the mock still answers the verb so a
    // manual call doesn't warn "unknown command".
    case "todo_update": {
      return { ok: true, event: fx.ev("review_action", { action: "todo_merge", origin: "user", ops_applied: 0, ops_rejected: [], snapshot: [], snapshot_sha: "" }, args?.conversationId ?? 1) };
    }

    // ---------- Search ----------
    case "search_events": {
      return fx.makeSearchResults(args?.text ?? "");
    }

    // ---------- M8 Skills ----------
    case "list_skills": {
      return { ok: true, skills: fx.getMockSkillsList() };
    }
    case "read_skill": {
      const path = args?.path ?? "";
      const filename = path.split("/").pop() ?? path;
      const content = fx.skillContent[filename] ?? `---\nname: ${filename.replace(".md", "")}\n---\n\nSkill content for ${filename}`;
      return { ok: true, skill_content: content };
    }
    case "update_skill": {
      const name = args?.name ?? "untitled";
      const text = args?.text ?? "";
      const scope = args?.scope ?? "project";
      const path = scope === "global"
        ? `~/.odo/skills/${name}.md`
        : `.odo/skills/${name}.md`;
      fx.addMockSkill({
        name,
        description: text.match(/^description:\s*(.+)$/m)?.[1]?.trim() ?? "",
        keywords: [],
        path,
        origin: "human",
        scope,
      }, text);
      return { ok: true };
    }
    case "delete_skill": {
      const name = args?.name ?? "";
      const scope = args?.scope ?? "project";
      fx.removeMockSkill(name, scope);
      return { ok: true };
    }

    // ---------- Distill ----------
    case "distill": {
      return { ok: true, wiki_path: "wiki/epoch-3.md", epoch: 3 };
    }

    // ---------- P2 (OMP stats) ----------
    case "omp_usage": {
      return {
        ok: true,
        omp_usage: {
          usage: {
            generatedAt: Date.now(),
            reports: [
              {
                provider: "openai-codex",
                fetchedAt: Date.now(),
                limits: [
                  {
                    id: "openai-codex:primary",
                    label: "7 days",
                    window: { id: "7d", label: "7 days", durationMs: 604800000, resetsAt: Date.now() + 604800000 },
                    amount: { used: 12, limit: 100, remaining: 88, usedFraction: 0.12, remainingFraction: 0.88, unit: "percent" },
                    status: "ok",
                  },
                ],
              },
            ],
          },
          grievances: [],
        },
      };
    }

    default: {
      console.warn(`[mock-invoke] unknown command: ${cmd}`, args);
      return { ok: false, error: `mock: unknown command ${cmd}` };
    }
  }
}

// E2E hook: in plain-browser dev mode the fixtures module IS the daemon's
// state — expose it so Playwright can simulate mid-session daemon changes
// (e.g. a background run appearing in pending_counts). Never set inside the
// real Tauri webview.
declare global {
  interface Window {
    __odoFixtures?: typeof fx;
  }
}
if (typeof window !== "undefined" && !isTauri()) {
  window.__odoFixtures = fx;
}

export { isTauri };
