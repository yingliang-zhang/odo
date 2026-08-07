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
    case "fanout_send": {
      const n = args?.n ?? 2;
      const newRuns = Array.from({ length: n }, (_, i) => ({
        run_id: `mock-run-${Date.now()}-${i}`,
        status: "running" as const,
        index: i,
      }));
      return { ok: true, runs: newRuns };
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
      return { ok: true, pending_counts: fx.pendingCounts, running_workstreams: fx.runningWorkstreams };
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

export { isTauri };
