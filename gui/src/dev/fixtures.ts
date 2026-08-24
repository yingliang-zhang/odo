// E P2: fixture data for browser dev mode. Seeds the mock-invoke adapter
// with a realistic multi-project, multi-workstream state so the GUI can
// run in a plain browser (npm run dev) without the Tauri webview or a
// live daemon. Add ?mock=0 to force the real invoke even in a browser.

import { deriveParkedGoals } from "../parked";
import type {
  AcceptDiffResponse,
  AutoDistillCountdown,
  BootstrapResponse,
  CancelResponse,
  ContradictionsResponse,
  CreateWorkstreamResponse,
  CurateResponse,
  Diff,
  DiffInfoEx,
  DistillResponse,
  GetSettingsResponse,
  LedgerResponse,
  ListTopicsResponse,
  ListWikiResponse,
  ListWorkstreamsResponse,
  MemoryProposal,
  MemoryProposalsResponse,
  OdoEvent,
  PanelProgress,
  PendingCountsResponse,
  PinResponse,
  PollEventsResponse,
  PreviewEvent,
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
  // Epochs are post-distill (a marker bumped 3 to 2; conv 2 went through
  // two distills, below, so it sits at 3).
  2: { id: 2, workstream_id: 2, epoch: 3, state: "active", created_at: "2026-08-05T09:00:00Z" },
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

  // ---------- Events (conversation 2) — two-distill legacy fold ----------
  // Full history: an epoch-1 distill folded the sketch run, then a second
  // (legacy schema — no first_seq/last_seq) distill folded everything
  // before its marker. The newest run below the boundary stays above the
  // chip (fold blind-spot fix); the older sketch run and BOTH markers are
  // hidden — and the chip counts the older marker too, because Expand
  // reveals it (3 hidden). The subject marker is the chip itself, not
  // counted. Markers carry post-distill counters, so badges read
  // epoch 2 (first) and epoch 3 (subject).
  ev("user_message", { text: "Sketch the sidebar sections" }, 2),
  ev("agent_done", { summary: "Sidebar sections sketched" }, 2),
  ev("review_action", { action: "distill", epoch: 2 }, 2),
  ev("user_message", { text: "Initial sidebar tree layout" }, 2),
  ev("agent_done", { summary: "Sidebar tree landed" }, 2),
  ev("review_action", {
    action: "distill",
    epoch: 3,
    wiki_path: "/Users/yingliangzhang/Projects/odo/wiki/feat-sidebar-tree-epoch-2.md",
  }, 2),

  // ---------- Events (conversation 3) — partial fold, epoch 2 active ----------
  // Explicit schema marker (first_seq/last_seq/note_sha): the UI prefers
  // the journaled window. Post-fold activity stays visible. Two runs sit
  // inside the pinned window: the older shim run is hidden by the fold,
  // the newest (patch) run stays above the chip (fold blind-spot fix).
  ev("user_message", { text: "Bootstrap the daemon shims" }, 3),
  ev("agent_done", { summary: "Daemon shims bootstrapped" }, 3),
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
    last_seq: 4,
    note_sha: "c3d4e5f60718293a",
  }, 3),
  ev("user_message", { text: "Now fix the socket perms" }, 3),
  ev("agent_text", { text: "Adjusting the socket chmod to 0600." }, 3),
  ev("agent_done", { summary: "Socket permissions fixed" }, 3),

  // ui/message-stream: one multi-call burst on CONVERSATION 1 — exercises
  // the folded "N tool calls" group summary (lone calls render inline).
  ev("user_message", { text: "Run the checks and report" }),
  ev("agent_tool_call", { tool: "run_command", args: { cmd: "npm test" } }),
  ev("agent_tool_result", { tool: "run_command", result: "0 failed" }),
  ev("agent_tool_call", { tool: "read_file", args: { path: "gui/src/App.tsx" } }),
  ev("agent_tool_result", { tool: "read_file", result: "1525 lines" }),
  ev("agent_done", { summary: "Checks passed; App.tsx reviewed" }),

  // W6 (goal queue): one parked goal waiting in the default conversation,
  // so dev mode and e2e see the QueueDock (and the sidebar pill) on first
  // paint. Conv 1's seq numbering stays gap-free.
  ev("user_message", { text: "Parked: sweep the flaky sidebar selector", park: true }),

  // A-P0 #1 (Guardian risk taxonomy): one full story of review_action
  // receipts on CONVERSATION 3 (seqs 10-16) — deliberate: conv 1's
  // transcript must stay review-bubble-free or existing diff/inbox specs
  // collide on `.badge-accept` / GetByText("Diff #N"). Rendered newest-
  // first, so row order below is the reverse of what the Ledger panel
  // shows. One auto-land cycle (review → conflict refresh → blocked →
  // landed), then human rows covering clean, timed-out, and the pre-W5
  // unrated gap. Each render branch has a row.
  ev("review_action", {
    action: "moa_review",
    diff_id: 1,
    actor: "auto_panel",
    consensus_verdict: "accept",
    risk_class: ["none"],
    risk_classifier: "mechanical",
  }, 3),
  ev("review_action", {
    action: "refresh_attempted",
    diff_id: 1,
    actor: "auto_panel",
    outcome: "conflict",
    phase: "pre_spend_probe",
    base_sha: "abc123def4567890",
    target_sha: "4567890abcdef123",
  }, 3),
  ev("review_action", {
    action: "auto_land_blocked",
    diff_id: 1,
    actor: "auto_panel",
    reason: "base_stale",
    risk_class: ["credential_probe"],
    risk_evidence: { credential_probe: "+process.env.OPENAI_API_KEY at gui/secrets.ts:7" },
    risk_classifier: "mechanical",
  }, 3),
  {
    // Pre-aged created_at: ev() backdates by id, so rows past id 24 drift
    // into the FUTURE at page load — and a stand-in auto accept inside the
    // pipeline chip's ≤4s landed window would wrongly flash on boot (as
    // would this one). This row is history; stamp it real-time, outside
    // the window. Ledger order is unaffected: LedgerPanel sorts review
    // rows by per-conversation seq (LedgerPanel.tsx), never created_at.
    ...ev("review_action", {
      action: "accept",
      diff_id: 2,
      actor: "auto_panel",
      risk_class: ["credential_probe", "supply_chain"],
      risk_evidence: {
        credential_probe: "+os.Getenv(\"AWS_SECRET_ACCESS_KEY\") at daemon/main.go:42",
        supply_chain: "package-lock.json",
      },
      risk_classifier: "mechanical",
    }, 3),
    created_at: new Date(Date.now() - 10 * 60000).toISOString(),
  },
  ev("review_action", {
    action: "reject",
    diff_id: 3,
    risk_class: ["none"],
    risk_classifier: "mechanical",
  }, 3),
  ev("review_action", {
    action: "moa_review",
    diff_id: 3,
    consensus_verdict: "mixed",
    timed_out: true,
    risk_class: ["security_weakening"],
    risk_evidence: { security_weakening: "+InsecureSkipVerify: true at net/client.go:88" },
    risk_classifier: "mechanical",
  }, 3),
  // Pre-W5 shape: no risk receipt keys at all → the panel must render the
  // honest "unrated" chip, never a false clean.
  ev("review_action", {
    action: "accept",
    diff_id: 4,
  }, 3),

  // ---------- GUI Wave B: telemetry fixtures on CONVERSATION 3 ----------
  // Turn 1 (billed-usage branch): NOTHING writes input/output tokens
  // today — the OMP adapter drops stream usage. This fixture pre-exercises
  // the defensive render (timed_out precedent): when a payload carries
  // billed numbers the strip shows tokens + tok/s, not bytes.
  ev("user_message", {
    text: "One-line fix: pin the socket path in the launcher",
    total_prompt_bytes: 20480,
    prompt_sha16: "b2c3d4e5f6071829",
  }, 3),
  ev("agent_text", { text: "Pinned." }, 3),
  ev("agent_done", {
    summary: "Socket path pinned in the daemon launcher",
    input_tokens: 5120,
    output_tokens: 846,
  }, 3),

  // Turn 2 (byte-branch, NEWEST prompt → drives the meter at 86%): the
  // user_message carries the full M18 W2 receipt closure —
  // total_prompt_bytes, prompt_sha16, verbatim receipt keys, replay
  // sub-receipt with a dropped window, recall_held_back. 1,204,000 bytes
  // vs the fixture coding model (t9s/kimi-k3 → 350k tok × ~4 B/tok ≈
  // 1,400,000 B) = ~86% → red tier, and the popover exercises every row.
  ev("user_message", {
    text: "Fold the fold-marker regression notes into the epoch summary",
    total_prompt_bytes: 1204000,
    prompt_sha16: "a1b2c3d4e5f60718",
    recall_held_back: 3,
    replay: { after_seq: 6, first_seq: 10, last_seq: 16, bytes: 62450, dropped_seqs: [7, 9] },
    receipt: {
      "~/.odo/user.md": "0123456789abcdef",
      ".odo/memory.md": "1123456789abcdef",
      ".odo/pins.md": "2123456789abcdef",
      "wiki/index.md": "3123456789abcdef",
      "odo#memory-map": "4123456789abcdef",
      "journal#todo": "5123456789abcdef",
      "wiki/fix-daemon-binary-epoch-1.md#open-loops": "6123456789abcdef",
      "wiki/main-epoch-6.md": "7123456789abcdef",
      "wiki/topics/auto-land-pipeline.md": "8123456789abcdef",
      "~/.odo/skills/playwright.md": "9123456789abcdef",
    },
  }, 3),
  ev("agent_text", { text: "Reading the fold markers first, then I'll draft the epoch summary." }, 3),
  ev("agent_tool_call", { tool: "read_file", args: { path: "wiki/fix-daemon-binary-epoch-1.md" } }, 3),
  ev("agent_tool_result", { tool: "read_file", result: "88 lines — fold marker regression notes" }, 3),
  ev("agent_text", { text: "Drafted the summary — the regression was a stale last_seq on the legacy marker, fixed by the pinned window." }, 3),
  ev("agent_done", { summary: "Folded fold-marker regression notes into the epoch summary" }, 3),

  // ---------- Pipeline chip (design lock Phase 1): M18 settle-ladder tail ----------
  // Daemon-true chain shape (settle.go): EACH round row names the diff it
  // just evaluated; round 1 carries diff_id == origin_diff_id (chain
  // start), later rounds the product id with the SAME origin. A fresh id
  // family (8-10) keeps the chain disjoint from the earlier diff 1-4
  // story. Cap 2 (2026-08-23): the 3rd evaluation (diff 10, the round-2
  // product) is terminal — the ladder journals its suspension marker
  // FIRST (memory_update — conversation-scoped, not a Ledger review row)
  // and then the blocked{ladder_suspended} echo on the evaluated diff
  // (blocked rows carry only that diff — no origin).
  ev("review_action", {
    action: "auto_revise_round",
    diff_id: 8,
    origin_diff_id: 8,
    actor: "auto_panel",
    round: 1,
    patch_sha16: "0123456789abcdef",
    comments_sha16: "fedcba9876543210",
    risk_class: ["none"],
    risk_classifier: "mechanical",
  }, 3),
  ev("review_action", {
    action: "auto_revise_round",
    diff_id: 9,
    origin_diff_id: 8,
    actor: "auto_panel",
    round: 2,
    patch_sha16: "10abcdef01234567",
    comments_sha16: "ef012345679abcde",
    risk_class: ["none"],
    risk_classifier: "mechanical",
  }, 3),
  ev("memory_update", {
    layer: "auto_land",
    cause: "ladder_suspended",
    detail: "2 consecutive revise rounds ended without landing; ladder suspended until a landing (diff 10 pending)",
  }, 3),
  ev("review_action", {
    action: "auto_land_blocked",
    diff_id: 10,
    actor: "auto_panel",
    reason: "ladder_suspended",
    risk_class: ["none"],
    risk_classifier: "mechanical",
  }, 3),
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

// P1a (review inbox): the same two pending diffs the Review tab lists —
// diff 1 on workstream main (the Changes tab's fixture), diff 2 on
// feat-sidebar-tree, so dev/e2e exercise the cross-workstream accept path.
// pendingCounts below is their per-workstream aggregate; the mock
// accept/reject cases keep the two in step via resolveInboxDiff (mock
// parity: the row list is the authority, the count is derived).
export const inboxDiffs: DiffInfoEx[] = [
  { ...pendingDiff, conversation_id: 1, workstream_id: 1, workstream_name: "main" },
  {
    id: 2,
    status: "pending",
    path: "/tmp/odo-diff-2.patch",
    content: `diff --git a/README.md b/README.md
index 123456..789abc 100644
--- a/README.md
+++ b/README.md
@@ -3,3 +3,5 @@ 
 some context
+## Cross-workstream change
+Added by the feat-sidebar-tree run.
`,
    conversation_id: 2,
    workstream_id: 2,
    workstream_name: "feat-sidebar-tree",
  },
];

// Resolve an inbox row (accept or reject): drop it and derive the ws count.
export function resolveInboxDiff(diffId: number): void {
  const idx = inboxDiffs.findIndex((d) => d.id === diffId);
  if (idx < 0) return;
  const [removed] = inboxDiffs.splice(idx, 1);
  const key = String(removed.workstream_id);
  const next = Math.max(0, (pendingCounts[key] ?? 1) - 1);
  if (next === 0) delete pendingCounts[key];
  else pendingCounts[key] = next;
}

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
  // Pipeline-chip dev surface (design lock: GUI-only Phase 1; lock rule 6
  // hides it unless main). The daemon pref default stays "off"; fixtures
  // run "main" so the auto-land status chip is visible in npm run dev —
  // the e2e's pref-off case is the exercised deviation. The mock's
  // get/update_settings round-trip this key.
  auto_apply: "main",
  // M19 (V11): mirrors the daemon's fail-to-default — on unless prefs.md
  // carries an explicit off-shape value. loop.spec.ts's pref-off case
  // mutates this through the same settings round-trip.
  loop_notify_on_complete: true,
};

// E2E probe for the dual Esc gate: the mock's cancel case increments this,
// so a menu-open Esc that leaked to the app-level handler is observable
// (background: 16 prior regressions where Esc cancelled the running agent).
export const cancelCount = { n: 0 };

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

// P1a: ws2's count is 1 — the inboxDiffs row on feat-sidebar-tree is part
// of the same baseline (the Review tab's rows must equal these pills).
export const pendingCounts: Record<string, number> = { 1: 1, 2: 1, 3: 0, 10: 0 };
export const runningWorkstreams: number[] = [];
// Per-root pending_counts override: cross-project regression tests need
// the two fixture daemons to DISAGREE (project A running / B idle) — the
// shared globals above can't express that. The mock consults an entry
// first and falls back to the globals, so single-project specs are
// unaffected. Keys are ProjectEntry.root values.
export const countsByRoot: Record<
  string,
  { pending?: Record<string, number>; running?: number[]; parked?: Record<string, number> }
> = {};
// E2E lever: pretend the daemon reports a live run on the polled
// conversation (agent_running). Background runs go through
// runningWorkstreams; this covers the FOREGROUND case (steer/park mutex).
export const runState = { foreground: false };

// Switch-phase knobs (same pattern as runState): the switch cache's
// stale-while-revalidate flip and failure rollback are only observable
// while a bootstrap response is still in flight. delayMs holds the mock's
// reply so a test can sample the pre-landing DOM deterministically; fail
// rejects like an unreachable daemon ("bootstrap: connection refused").
export const bootstrapCtl = { delayMs: 0, fail: false };

// M7 preview knob (same pattern as runState): the mock's poll response
// mirrors this so e2e can simulate a streaming in-flight block — never
// journaled, replaced wholesale per poll like the daemon's.
export const previewState: { current: PreviewEvent | null } = { current: null };

// /panel heartbeat knob (same pattern as previewState): the daemon reports
// a live leg tally in poll_events while a consult fans out — memory-only,
// gone the moment the consult ends. E2E sets it to drive the spinner
// row's N/M counter independently of the advisorySend hold.
export const panelProgressState: { current: PanelProgress | null } = { current: null };

// Advisory-slash knob (/panel, /vision, /preview — same pattern as
// runState): the daemon answers those synchronously inside send_message,
// so the RPC outlasts the whole consult. hold parks the mock's reply
// until a test releases it; fail rejects PRE-journal like the daemon's
// entry gates (run in flight / no review models configured) do; releasing
// WITH an error models a LATE failure — the question already journaled,
// then the RPC rejects (slash receipt gate / daemon restart / IPC drop).
// The release LATCHES (released + releaseError): a test that releases
// before the held RPC reaches the mock's hold gate must still unblock
// it — queueing remember-only waiters made that order a hang race.
export const advisorySend = {
  hold: false,
  fail: null as string | null,
  released: false,
  releaseError: null as string | null,
  waiters: [] as Array<() => void>,
};

export function releaseAdvisorySends(error?: string) {
  advisorySend.released = true;
  advisorySend.releaseError = error ?? null;
  const waiters = advisorySend.waiters.splice(0);
  for (const w of waiters) w();
}

// W6 (goal queue): per-workstream parked-goal depth, keyed like
// pendingCounts. Kept in step with the journaled events by
// syncParkedGoals (the same derivation the daemon applies — mock parity:
// the journal is the authority, the count is derived, never incremented
// by hand).
export const parkedGoals: Record<string, number> = {};

export function syncParkedGoals(conversationId: number): number {
  const depth = deriveParkedGoals(events.filter((e) => e.conversation_id === conversationId)).length;
  const wsId = conversations[conversationId]?.workstream_id;
  if (wsId != null) {
    if (depth > 0) parkedGoals[String(wsId)] = depth;
    else delete parkedGoals[String(wsId)];
  }
  return depth;
}

// Initial sync for the seeded parked event in the default conversation.
syncParkedGoals(1);
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

// afterSeq mirrors the daemon's poll cursor: only journal rows NEWER than
// the client's watermark arrive. Bootstrap replays the whole journal and
// sets the watermark, so fixture events appended mid-session (mock park /
// resume / drop) flow in exactly like daemon-journaled rows; callers that
// omit afterSeq (legacy) get the old empty pull.
export function makePollResponse(convId: number, afterSeq?: number): PollEventsResponse {
  return {
    ok: true,
    events: events.filter((e) => e.conversation_id === convId && e.seq > (afterSeq ?? Number.MAX_SAFE_INTEGER)),
    agent_running: runState.foreground,
    preview: previewState.current,
    streaming: false,
    panel_progress: panelProgressState.current,
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
