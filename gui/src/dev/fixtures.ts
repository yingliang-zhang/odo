// E P2: fixture data for browser dev mode. Seeds the mock-invoke adapter
// with a realistic multi-project, multi-workstream state so the GUI can
// run in a plain browser (npm run dev) without the Tauri webview or a
// live daemon. Add ?mock=0 to force the real invoke even in a browser.

import type {
  AcceptDiffResponse,
  AutoDistillCountdown,
  BootstrapResponse,
  CancelResponse,
  ContradictionsResponse,
  CreateWorkstreamResponse,
  CurateResponse,
  Diff,
  DistillResponse,
  GetSettingsResponse,
  LedgerResponse,
  ListTopicsResponse,
  ListWikiResponse,
  ListWorkstreamsResponse,
  MemoryProposal,
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
  SearchResult,
  SearchEventsResponse,
  SendMessageResponse,
  Settings,
  SkillInfo,
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
  // Epochs are post-distill (a marker bumped each of 2 and 3 to 2).
  2: { id: 2, workstream_id: 2, epoch: 2, state: "active", created_at: "2026-08-05T09:00:00Z" },
  3: { id: 3, workstream_id: 3, epoch: 2, state: "active", created_at: "2026-08-06T15:00:00Z" },
  10: { id: 10, workstream_id: 10, epoch: 1, state: "active", created_at: "2026-08-01T14:01:00Z" },
};

// ---------- Events (conversation 1) ----------

// IDs are global; seqs are PER-CONVERSATION and gap-free — same shape the
// daemon's journal guarantees, and the fold chip's derived-window fallback
// relies on it.
let idCounter = 0;
const seqByConv: Record<number, number> = {};
export function ev(
  type: OdoEvent["type"],
  payload: Record<string, unknown>,
  convId = 1,
): OdoEvent {
  idCounter += 1;
  seqByConv[convId] = (seqByConv[convId] ?? 0) + 1;
  return {
    id: idCounter,
    conversation_id: convId,
    seq: seqByConv[convId],
    type,
    payload: payload as OdoEvent["payload"],
    created_at: new Date(Date.now() - (24 - idCounter) * 60000).toISOString(),
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
  // M12 (D-todo): the composer "Plan · N open" chip reads this journaled
  // merge (snapshot = open + not-yet-swept items; no fold markers here, so
  // nothing sweeps or stales).
  ev("review_action", {
    action: "todo_merge",
    origin: "agent",
    ops_applied: 3,
    ops_rejected: [],
    snapshot: [
      { id: "t1", text: "Verify table rendering edge cases", status: "open", origin_seq: 3, updated_seq: 3 },
      { id: "t2", text: "Add keyboard navigation to the plan popover", status: "open", origin_seq: 3, updated_seq: 3 },
      { id: "t3", text: "Wire the todo_update IPC through the bridge", status: "done", origin_seq: 3, updated_seq: 5 },
    ],
    snapshot_sha: "deadbeefcafe0123",
  }),

  // ---------- Events (conversation 2) — everything folded ----------
  // Legacy marker (no first_seq/last_seq): the UI derives the window from
  // journal order. Covers the folded-all empty state.
  ev("user_message", { text: "Initial sidebar tree layout" }, 2),
  ev("agent_done", { summary: "Sidebar tree landed" }, 2),
  ev("review_action", {
    action: "distill",
    epoch: 2,
    wiki_path: "/Users/yingliangzhang/Projects/odo/wiki/feat-sidebar-tree-epoch-1.md",
  }, 2),

  // ---------- Events (conversation 3) — partial fold, epoch 2 active ----------
  // Explicit schema marker (first_seq/last_seq/note_sha): the UI prefers
  // the journaled window. Post-fold activity stays visible.
  ev("user_message", { text: "Patch the daemon launch path" }, 3),
  ev("agent_done", { summary: "Daemon launch path patched" }, 3),
  // Committed-phase shape (K3): this message journaled while the fold's
  // learner pass slept — above the pinned last_seq but BELOW the marker in
  // journal order. It must stay visible above the fold chip (the fold
  // never rendered it).
  ev("user_message", { text: "Wait — also handle the stale socket" }, 3),
  ev("review_action", {
    action: "distill",
    epoch: 2,
    wiki_path: "/Users/yingliangzhang/Projects/odo/wiki/fix-daemon-binary-epoch-1.md",
    first_seq: 1,
    last_seq: 2,
    note_sha: "c3d4e5f60718293a",
  }, 3),
  ev("user_message", { text: "Now fix the socket perms" }, 3),
  ev("agent_text", { text: "Adjusting the socket chmod to 0600." }, 3),
  ev("agent_done", { summary: "Socket permissions fixed" }, 3),
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
  coding_model: "t9s/kimi-k3",
  coding_provider: "sudo",
  orchestrator_model: "t9s/glm-5.2",
  orchestrator_provider: "sudo",
  omp_timeout: "600",
  review_models: "t9s/kimi-k3@sudo,t9s/glm-5.2@sudo,t9s/deepseek-v4-flash@sudo",
  auto_distill: "on_idle",
  auto_distill_idle_seconds: "120",
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
// M12: no scheduled auto-distill in the default fixture — the composer
// chip stays hidden, matching pre-M12 screens. E2E adds its own entry when
// it wants the countdown visible.
export const autoDistill: AutoDistillCountdown[] = [];

// ---------- M8 Skills ----------

export const skills: SkillInfo[] = [
  {
    name: "tdd-workflow",
    description: "Use when writing new features or fixing bugs. Enforces RED-GREEN-REFACTOR.",
    keywords: ["tdd", "test", "testing", "red-green", "refactor"],
    path: "~/.odo/skills/tdd-workflow.md",
    origin: "ported",
    scope: "global",
  },
  {
    name: "systematic-debugging",
    description: "Use when debugging a non-trivial issue. 4-phase root cause methodology.",
    keywords: ["debug", "debugging", "root-cause", "trace", "fix"],
    path: "~/.odo/skills/systematic-debugging.md",
    origin: "ported",
    scope: "global",
  },
  {
    name: "deploy-checklist",
    description: "Use when deploying to production. Pre-deploy verification steps.",
    keywords: ["deploy", "production", "release", "ship", "verify"],
    path: ".odo/skills/deploy-checklist.md",
    origin: "human",
    scope: "project",
  },
];

// Stateful skills store for mock — supports create/delete round-trip in E2E.
let mockSkills: SkillInfo[] | null = null;
function getMockSkills(): SkillInfo[] {
  if (!mockSkills) {
    mockSkills = [...skills]; // clone the fixture array
  }
  return mockSkills;
}
export function resetMockSkills() {
  mockSkills = null;
}
export function getMockSkillsList(): SkillInfo[] {
  return getMockSkills();
}
export function addMockSkill(skill: SkillInfo, content: string) {
  const list = getMockSkills();
  // Replace if name+scope exists, else add
  const idx = list.findIndex((s) => s.name === skill.name && s.scope === skill.scope);
  if (idx >= 0) {
    list[idx] = skill;
  } else {
    list.push(skill);
  }
  skillContent[skill.name + ".md"] = content;
}
export function removeMockSkill(name: string, scope: string) {
  const list = getMockSkills();
  const idx = list.findIndex((s) => s.name === name && s.scope === scope);
  if (idx >= 0) {
    list.splice(idx, 1);
  }
}

export const skillContent: Record<string, string> = {
  "tdd-workflow.md": `---
name: tdd-workflow
description: Use when writing new features or fixing bugs. Enforces RED-GREEN-REFACTOR.
keywords: [tdd, test, testing, red-green, refactor]
origin: ported
---

# TDD Workflow

## RED — Write a failing test first
1. Write one test that describes the desired behavior
2. Run it — it must fail for the right reason (not a compile error)
3. Commit the test

## GREEN — Make it pass with minimal code
1. Write the simplest code that passes the test
2. Run all tests — confirm green
3. Commit the implementation

## REFACTOR — Improve the code without changing behavior
1. Extract duplication, rename, simplify
2. Run all tests — confirm still green
3. Commit the refactor

## Pitfalls
- Don't skip RED — a test that passes before code exists is testing the wrong thing
- Don't combine GREEN and REFACTOR — they're separate commits for bisect
- Don't write more than one test at a time — incremental is the point
`,
  "systematic-debugging.md": `---
name: systematic-debugging
description: Use when debugging a non-trivial issue. 4-phase root cause methodology.
keywords: [debug, debugging, root-cause, trace, fix]
origin: ported
---

# Systematic Debugging

## Phase 1: Reproduce
- Find the minimal, deterministic reproduction
- If you can't reproduce it, you can't fix it

## Phase 2: Localize
- Binary search the cause: bisect commits, comment out code, add prints
- Narrow to the smallest change that triggers the bug

## Phase 3: Root Cause
- Ask "why?" 5 times — surface symptoms are not root causes
- Fix the cause, not the symptom — check sibling call paths for the same flaw

## Phase 4: Verify
- Write a regression test that fails without your fix
- Run the full test suite
- Check for similar bugs in the same area
`,
  "deploy-checklist.md": `---
name: deploy-checklist
description: Use when deploying to production. Pre-deploy verification steps.
keywords: [deploy, production, release, ship, verify]
origin: human
---

# Deploy Checklist

## Pre-deploy
- [ ] All tests pass (unit + integration)
- [ ] No uncommitted changes in the working tree
- [ ] Linter clean (golangci-lint, eslint)
- [ ] Version bumped in go.mod / package.json

## Deploy
- [ ] Build artifact: \`go build -o odo ./cmd/odo\`
- [ ] Dry-run the deployment command
- [ ] Deploy to staging first, verify
- [ ] Deploy to production

## Post-deploy
- [ ] Health check passes
- [ ] Monitor logs for 10 minutes
- [ ] Tag the release: \`git tag v0.x.x\`
`,
};

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

// M9: skill proposals with tri-model gate reviews.
export const skillProposals: MemoryProposal[] = [
  {
    target: "skills",
    rule: "---\nname: run-tests-before-commit\ndescription: Use when claiming work is done.\nkeywords: [test, commit, verify]\norigin: agent-authored\n---\n\n# Run Tests Before Commit\n\n1. Run `go test ./...`\n2. Run `npx tsc --noEmit`\n3. Only commit if both pass",
    name: "run-tests-before-commit",
    evidence: "main-epoch-2.md",
    reviews: [
      { model: "kimi-k3@sudo", verdict: "accept", comments: "Clear and actionable" },
      { model: "glm-5.2@sudo", verdict: "accept", comments: "Good trigger conditions" },
      { model: "deepseek-v4-flash@sudo", verdict: "reject", comments: "Too rigid for exploratory work" },
    ],
  },
];

export const memoryProposals = {
  epoch: 2,
  seq: 10,
  proposals: [
    { target: "memory.md" as const, rule: "Odo sidebar uses CSS tree view for project/workstream hierarchy", evidence: "Sidebar.tsx tree implementation" },
    { target: "user.md" as const, rule: "User prefers browser dev mode with mock fixtures for GUI iteration" },
    ...skillProposals,
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
