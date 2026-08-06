// E P2: fixture data for browser dev mode. Seeds the mock-invoke adapter
// with a realistic multi-project, multi-workstream state so the GUI can
// run in a plain browser (npm run dev) without the Tauri webview or a
// live daemon. Add ?mock=0 to force the real invoke even in a browser.

import type {
  AcceptDiffResponse,
  BootstrapResponse,
  CancelResponse,
  ContradictionsResponse,
  CreateWorkstreamResponse,
  CurateResponse,
  Diff,
  DistillResponse,
  FanoutSendResponse,
  GetSettingsResponse,
  LedgerResponse,
  ListTopicsResponse,
  ListWikiResponse,
  ListWorkstreamsResponse,
  MemoryProposalsResponse,
  OdoEvent,
  PendingCountsResponse,
  PinResponse,
  PollEventsResponse,
  Project,
  ProjectEntry,
  ReadMemoryResponse,
  ReadPinsResponse,
  ReadWikiResponse,
  RejectDiffResponse,
  ReviewDiffResponse,
  RunInfo,
  SearchResult,
  SearchEventsResponse,
  SendMessageResponse,
  Settings,
  UpdateSettingsResponse,
  WikiNoteInfo,
  Workstream,
  Conversation,
} from "../types";

// Re-export unused type to satisfy TS6196
export type { Project };

// ---------- Projects ----------

export const projects: ProjectEntry[] = [
  { root: "/Users/yingliangzhang/Projects/odo", name: "odo", added: "2026-07-20T10:00:00Z" },
  { root: "/Users/yingliangzhang/Projects/Sudo/supersplat-hdr", name: "supersplat-hdr", added: "2026-08-01T14:00:00Z" },
];

// ---------- Workstreams ----------

export const workstreams: Record<string, Workstream[]> = {
  "/Users/yingliangzhang/Projects/odo": [
    { id: 1, project_id: 1, name: "main", branch: "main", status: "active", created_at: "2026-07-20T10:01:00Z" },
    { id: 2, project_id: 1, name: "feat-sidebar-tree", branch: "feat-sidebar-tree", status: "active", created_at: "2026-08-05T09:00:00Z" },
    { id: 3, project_id: 1, name: "fix-daemon-binary", branch: "fix-daemon-binary", status: "active", created_at: "2026-08-06T15:00:00Z" },
  ],
  "/Users/yingliangzhang/Projects/Sudo/supersplat-hdr": [
    { id: 10, project_id: 2, name: "main", branch: "main", status: "active", created_at: "2026-08-01T14:01:00Z" },
  ],
};

// ---------- Conversations ----------

export const conversations: Record<number, Conversation> = {
  1: { id: 1, workstream_id: 1, epoch: 2, state: "active", base_commit_sha: "abc123", created_at: "2026-07-20T10:01:00Z" },
  2: { id: 2, workstream_id: 2, epoch: 1, state: "active", created_at: "2026-08-05T09:00:00Z" },
  3: { id: 3, workstream_id: 3, epoch: 1, state: "active", created_at: "2026-08-06T15:00:00Z" },
  10: { id: 10, workstream_id: 10, epoch: 1, state: "active", created_at: "2026-08-01T14:01:00Z" },
};

// ---------- Events (conversation 1) ----------

let seqCounter = 0;
export function ev(
  type: OdoEvent["type"],
  payload: Record<string, unknown>,
  convId = 1,
): OdoEvent {
  seqCounter += 1;
  return {
    id: seqCounter,
    conversation_id: convId,
    seq: seqCounter,
    type,
    payload: payload as OdoEvent["payload"],
    created_at: new Date(Date.now() - (20 - seqCounter) * 60000).toISOString(),
  };
}

export const events: OdoEvent[] = [
  ev("user_message", { text: "Add a GFM table renderer to the Markdown component" }),
  ev("agent_thinking", { text: "The user wants GFM table support. I need to:\n1. Detect table blocks in parseBlocks (header row + separator row)\n2. Parse rows with backtick-aware splitting\n3. Render as <table> in renderBlock\nLet me check the existing block types first." }),
  ev("agent_text", { text: "I'll add GFM table support to the Markdown renderer. Let me trace the existing block parsing logic first." }),
  ev("agent_tool_call", { tool: "read_file", args: { path: "gui/src/components/Markdown.tsx" } }),
  ev("agent_tool_result", { tool: "read_file", result: "280 lines — parseBlocks at line 110, renderBlock at line 221" }),
  ev("agent_text", { text: "Now I'll add the table block type and parser." }),
  ev("agent_done", { summary: "Added GFM table rendering with backtick-aware cell parsing" }),
  ev("user_message", { text: "Looks good — now add the CSS for `.md-table`" }),
  ev("agent_text", { text: "Adding table styles to app.css — borders, padding, header background." }),
  ev("agent_done", { summary: "Added .md-table CSS with th/td borders, padding, and header styling" }),
];

// ---------- Diffs ----------

export const pendingDiff: Diff = {
  id: 1,
  status: "pending",
  path: "/tmp/odo-diff-1.patch",
  content: `diff --git a/gui/src/components/Markdown.tsx b/gui/src/components/Markdown.tsx
index abc123..def456 100644
--- a/gui/src/components/Markdown.tsx
+++ b/gui/src/components/Markdown.tsx
@@ -110,6 +110,8 @@ function parseBlocks(lines: string[]): Block[] {
   const line = lines[i];
   if (line.startsWith("# ")) { blocks.push({kind:"heading", level:1, text: line.slice(2)}); i++; continue; }
+  if (isTableStart(lines, i)) { const [block, next] = parseTable(lines, i); blocks.push(block); i = next; continue; }
+
   if (line.startsWith("\`\`\`")) { ... }
diff --git a/gui/src/styles/app.css b/gui/src/styles/app.css
index 789abc..012def 100644
--- a/gui/src/styles/app.css
+++ b/gui/src/styles/app.css
@@ -755,6 +755,20 @@ 
+.md-table { border-collapse: collapse; width: 100%; font-size: 12px; }
+.md-table th, .md-table td { border: 1px solid var(--border); padding: 4px 8px; }
+.md-table thead { background: var(--bg-input); }
`,
};

// ---------- Wiki ----------

export const wikiNotes: WikiNoteInfo[] = [
  { path: "wiki/epoch-1.md", name: "Epoch 1 — Initial setup", epoch: 1, modified_at: "2026-07-20T12:00:00Z" },
  { path: "wiki/epoch-2.md", name: "Epoch 2 — GUI features F1-F7", epoch: 2, modified_at: "2026-08-06T20:00:00Z" },
];

export const wikiContent = `# Epoch 2 — GUI Features F1-F7

## Summary
Completed all 7 features from the Hermes UX borrowing action list:
- F1: Add project (native folder picker)
- F2: Thinking blocks (collapsible \`<details>\`)
- F3: Code block copy button + language label
- F4: GFM table rendering
- F5: lucide-react SVG icons
- F6: Toast animation + loading spinner
- F7: Workstream rename + delete

## Decisions
- Soft-delete workstreams (status='deleted') to preserve journal history
- Collision check on rename (same project + active + name)
- Pending-diff guard on delete (refuse if unreviewed diffs)
`;

// ---------- Settings ----------

export const defaultSettings: Settings = {
  coding_model: "kimi-k3",
  coding_provider: "sudo",
  orchestrator_model: "glm-5.2",
  orchestrator_provider: "sudo",
  omp_timeout: "600",
  default_adapter: "omp",
  review_models: "kimi-k3,glm-5.2,deepseek-v4-flash",
  auto_distill: "true",
  auto_distill_idle_seconds: "300",
  auto_curate_after_distill: "false",
  max_concurrent_runs: "3",
};

// ---------- Memory ----------

export const memoryContent = `# MEMORY.md

- Odo: Tauri 2+React+Go desktop GUI for AI coding agent. Root ~/Projects/odo/
- Dev: npm run tauri:dev:1420
- F1-F7 all complete and tri-model reviewed
- GUI dev efficiency: mock-invoke adapter is the keystone improvement
`;

export const userContent = `# USER.md

- Prefers concise Chinese output with Markdown tables
- Code/comments in English
- Practical guarded MVPs over speculative rewrites
`;

// ---------- Pending counts ----------

export const pendingCounts: Record<string, number> = { 1: 1, 2: 0, 3: 0, 10: 0 };
export const runningWorkstreams: number[] = [];

// ---------- Runs ----------

export const runs: RunInfo[] = [];

// ---------- Topics ----------

export const topics: WikiNoteInfo[] = [
  { path: "wiki/topics/gui-development.md", name: "GUI Development", epoch: 0, modified_at: "2026-08-06T20:00:00Z" },
  { path: "wiki/topics/workstream-management.md", name: "Workstream Management", epoch: 0, modified_at: "2026-08-06T20:00:00Z" },
];

// ---------- Ledger ----------

export const ledgerContent = `# Ledger

| Date | Action | Target | SHA Before | SHA After |
|------|--------|--------|------------|-----------|
| 2026-08-06 | apply | memory.md | abc123 | def456 |
| 2026-08-06 | apply | user.md | 789abc | 012def |
`;

// ---------- Pins ----------

export const pinsContent = `# Pins

- Always run tests after changes
- Never commit secrets
- Use \`git pull --rebase\` before push
`;

// ---------- Memory proposals ----------

export const memoryProposals = {
  epoch: 2,
  seq: 10,
  proposals: [
    { target: "memory.md" as const, rule: "Odo sidebar uses CSS tree view for project/workstream hierarchy", evidence: "Sidebar.tsx tree implementation" },
    { target: "user.md" as const, rule: "User prefers browser dev mode with mock fixtures for GUI iteration" },
  ],
  reaffirm: ["Odo: Tauri 2+React+Go"],
};

// ---------- Helpers ----------

export function makeBootstrap(root?: string, workstreamId?: number): BootstrapResponse {
  const projectRoot = root ?? projects[0].root;
  const wsList = workstreams[projectRoot] ?? workstreams[projects[0].root];
  const ws = wsList.find(w => w.id === workstreamId) ?? wsList[0];
  const conv = conversations[ws.id] ?? conversations[1];
  return {
    ok: true,
    project: { id: 1, name: projects.find(p => p.root === projectRoot)?.name ?? "odo", root_path: projectRoot },
    workstream: ws,
    conversation: conv,
    events: events.filter(e => e.conversation_id === conv.id),
    agent_running: false,
    diff: ws.id === 1 ? pendingDiff : null,
  };
}

export function makePollResponse(convId: number): PollEventsResponse {
  return {
    ok: true,
    events: [],
    agent_running: false,
    preview: null,
    streaming: false,
    diff: convId === 1 ? pendingDiff : null,
    diffs: convId === 1 ? [pendingDiff] : [],
    runs,
  };
}

export function makeSearchResults(query: string): SearchEventsResponse {
  const lower = query.toLowerCase();
  const results: SearchResult[] = events
    .filter(e => JSON.stringify(e.payload).toLowerCase().includes(lower))
    .map(e => ({
      event: e,
      workstream_id: 1,
      workstream_name: "main",
      conversation_id: e.conversation_id,
    }));
  return { ok: true, search_results: results };
}

// Export all response builders for the mock adapter
export {
  type AcceptDiffResponse,
  type CancelResponse,
  type ContradictionsResponse,
  type CreateWorkstreamResponse,
  type CurateResponse,
  type DistillResponse,
  type FanoutSendResponse,
  type GetSettingsResponse,
  type LedgerResponse,
  type ListTopicsResponse,
  type ListWikiResponse,
  type ListWorkstreamsResponse,
  type MemoryProposalsResponse,
  type PendingCountsResponse,
  type PinResponse,
  type ReadMemoryResponse,
  type ReadPinsResponse,
  type ReadWikiResponse,
  type RejectDiffResponse,
  type ReviewDiffResponse,
  type SendMessageResponse,
  type UpdateSettingsResponse,
};
