// E P2: mock invoke — replaces Tauri's invoke() in browser dev mode.
// Detects absence of __TAURI_INTERNALS__ (plain browser) and routes
// command names to fixture data. Same function signature as invoke(),
// so api.ts code is unchanged — only the transport is swapped.

import * as fx from "./fixtures";

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
  await new Promise(r => setTimeout(r, 50));

  switch (cmd) {
    // ---------- Lifecycle ----------
    case "bootstrap": {
      return fx.makeBootstrap(args?.projectRoot ?? undefined, args?.workstreamId ?? undefined);
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
      return { ok: true, event: fx.ev("user_message", { text: args?.text ?? "" }, args?.conversationId ?? 1) };
    }
    case "cancel": {
      return { ok: true };
    }
    case "poll_events": {
      return fx.makePollResponse(args?.conversationId ?? 1);
    }

    // ---------- Diffs ----------
    case "accept_diff": {
      return { ok: true, diff_id: args?.diffId, applied: true };
    }
    case "reject_diff": {
      return { ok: true, diff_id: args?.diffId, applied: false };
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
          auto_apply: "off",
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
      return { ok: true, settings: fx.defaultSettings };
    }
    case "update_settings": {
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
      return {
        ok: true,
        pending_counts: fx.pendingCounts,
        running_workstreams: fx.runningWorkstreams,
        auto_distill: fx.autoDistill ?? [],
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
