// Shared types mirroring the daemon's IPC shapes (internal/ipc/protocol.go)
// and store structs (internal/store/store.go). JSON field names match the Go
// struct tags exactly — in particular, Event payloads ride under `payload`
// as a nested JSON object, NOT a stringified `payload_json`.

export interface Project {
  id: number;
  name: string;
  root_path: string;
  created_at?: string;
}

// M11 P1: one row of the daemon-owned global registry
// (~/.odo/projects.json) for the project switcher — read-only bridge view
// via list_projects. Distinct from Project, which is the daemon's DB row.
export interface ProjectEntry {
  root: string;
  name: string;
  added: string;
}

export interface Workstream {
  id: number;
  project_id: number;
  name: string;
  branch?: string | null;
  worktree_path?: string | null;
  status: string;
  created_at?: string;
}

export interface Conversation {
  id: number;
  workstream_id: number;
  epoch: number;
  state: string;
  base_commit_sha?: string | null;
  created_at?: string;
}

export type EventType =
  | "user_message"
  | "agent_text"
  | "agent_thinking"
  | "agent_tool_call"
  | "agent_tool_result"
  | "agent_done"
  | "agent_error"
  | "review_action"
  | "memory_update"
  | (string & {});

// Payload keys by event type (ADR-0002):
//   user_message      { text, attachments? }
//   agent_text        { text }
//   agent_tool_call   { tool, args }
//   agent_tool_result { tool, result }
//   agent_done        { summary }
//   agent_error       { error }
//   review_action     { action: "accept" | "reject", diff_id }
//                       — also "distill" { epoch, wiki_path } (M1; plus
//                       first_seq, last_seq, note_sha — explicit fold
//                       provenance) and "curate" { action, topics,
//                       notes_read } (M5).
//   memory_update     { layer, cause, before_sha?, after_sha?, detail? }
//                       layer: "memory" | "user" | "learner" (M4) |
//                       "curator" | "index" | "pins" (M5) |
//                       "note" | "ledger" (M6); cause:
//                       "apply" | "rotate" | "retract" | "failed" (M4) |
//                       "curate" | "pin" (M5) |
//                       "write_failed" | "verify_failed" (M6).
// M6: one recall payload entry. Fixed markers (user.md/memory.md/pins.md/
// index.md) carry path only; keyword-selected notes carry matched_terms
// (omitted when the note ranked in purely by newest-first fallback).
export interface RecallItem {
  path: string;
  matched_terms?: string[];
}

export interface EventPayload {
  text?: string;
  summary?: string;
  error?: string;
  tool?: string;
  args?: unknown;
  result?: unknown;
  action?: string;
  diff_id?: number;
  attachments?: string[];
  // M3: memory recall — user_message journals what was injected into the
  // prompt. Fixed markers come first in daemon order: ~/.odo/user.md →
  // .odo/memory.md (M4) → .odo/pins.md (M5) → wiki/index.md (M5), then the
  // recalled wiki notes. M6 shape change: string[] → RecallItem[]; pre-M6
  // events still carry bare strings, so consumers normalize both shapes
  // (spec risk #4).
  recall?: RecallItem[];
  // M4: injection receipt — content hashes (sha256[:16]) of the exact
  // blocks injected, keyed by the same path strings used in `recall` (M5
  // adds the .odo/pins.md and wiki/index.md fixed markers).
  receipt?: Record<string, string>;
  // memory_update payload fields (M4, extended M5): which layer changed
  // (memory | user | learner | curator | index | pins), why
  // (apply | rotate | retract | failed | curate | pin), and a
  // human-readable summary.
  layer?: string;
  cause?: string;
  detail?: string;
  // review_action when action == "distill" (M1 memory distiller). The fold
  // window [first_seq, last_seq] is journaled explicitly (epoch-fold root
  // fix) alongside the note's sha16; older markers omit them and consumers
  // derive (previous marker seq+1 … marker seq−1).
  epoch?: number;
  wiki_path?: string;
  first_seq?: number;
  last_seq?: number;
  note_sha?: string;
  // review_action when action == "curate" (M5 curator pass): how many
  // topic pages were rewritten and how many epoch notes were read.
  topics?: number;
  notes_read?: number;
  // M12 (D-todo): review_action when action == "todo_merge" — the full
  // post-merge live snapshot (open + not-yet-swept items), its origin, and
  // op bookkeeping. Derived stale/swept flags are recomputed by consumers
  // from the fold markers, never journaled.
  origin?: string;
  ops_applied?: number;
  ops_rejected?: { op?: string; id?: string; reason: string }[];
  snapshot?: { id: string; text: string; status: string; origin_seq: number; updated_seq: number }[];
  snapshot_sha?: string;
  // M7: streaming-preview payloads. partial marks the transient preview
  // (never journaled); intent/call_id come from tool_execution_start.
  partial?: boolean;
  intent?: string;
  call_id?: string;
  // /panel and /vision payloads: mark the event as panel/vision output and
  // carry per-model results for /panel.
  panel?: boolean;
  vision?: boolean;
  models?: { model: string; text: string; error?: string }[];
}

// M7 live streaming: the transient in-flight block preview returned by
// poll_events while a run streams. Never journaled — replaced wholesale on
// every poll and dropped when the block completes.
export interface PreviewEvent {
  type: EventType;
  payload: EventPayload;
}

export interface OdoEvent {
  id: number;
  conversation_id: number;
  seq: number;
  type: EventType;
  payload: EventPayload;
  created_at: string;
}

export type DiffStatus = "pending" | "accepted" | "rejected" | (string & {});

export interface Diff {
  id: number;
  status: DiffStatus;
  path: string;
  content: string;
}

export interface BootstrapResponse {
  ok: boolean;
  error?: string;
  project?: Project;
  workstream?: Workstream;
  conversation?: Conversation;
  events?: OdoEvent[];
  agent_running?: boolean;
  diff?: Diff | null;
}

// Type alias (not interface) so it is assignable to Tauri's InvokeArgs.
export type SendMessageRequest = {
  conversationId: number;
  text: string;
  attachments?: string[];
  // M1: steer journals the message for a running agent instead of starting
  // a new run; adapter selects the backend ("omp").
  steer?: boolean;
  adapter?: string;
  // M11 P1: routes to that project's daemon; null = bridge default.
  projectRoot?: string | null;
};

export interface SendMessageResponse {
  ok: boolean;
  error?: string;
  event?: OdoEvent;
}

// Belt A: cancel carries no payload; ok:false ("no active run") is the
// normal race against a run that finished just before the click.
export interface CancelResponse {
  ok: boolean;
  error?: string;
}

export interface PollEventsResponse {
  ok: boolean;
  error?: string;
  events?: OdoEvent[];
  agent_running?: boolean;
  // M7: transient in-flight block preview (partial:true), or null.
  preview?: PreviewEvent | null;
  streaming?: boolean;
  diff?: Diff | null;
  diffs?: Diff[];
}

export interface AcceptDiffResponse {
  ok: boolean;
  error?: string;
  diff_id?: number;
  applied?: boolean;
}

export interface RejectDiffResponse {
  ok: boolean;
  error?: string;
  diff_id?: number;
  applied?: boolean;
}

export interface ListWorkstreamsResponse {
  ok: boolean;
  error?: string;
  workstreams?: Workstream[];
}

export interface CreateWorkstreamResponse {
  ok: boolean;
  error?: string;
  workstream?: Workstream;
}

export interface DistillResponse {
  ok: boolean;
  error?: string;
  wiki_path?: string;
  // epoch is the NEW epoch after the increment (the distilled one is N-1).
  epoch?: number;
}

// ---------- M2: review panel, settings ----------

export type ReviewVerdict = "accept" | "reject" | "needs_fixes" | (string & {});

// One reviewer's verdict on a pending diff (daemon `review_diff`).
export interface ReviewResult {
  model: string;
  verdict: ReviewVerdict;
  comments: string;
}

export interface ReviewDiffResponse {
  ok: boolean;
  error?: string;
  reviews?: ReviewResult[];
  consensus?: string; // A4-lite+v2: deterministic verdict ("accept" | "reject" | "needs_fixes"); accept requires unanimity
}

// ---------- M15 (O-1 rung-0): autonomy streak snapshot ----------

// One diff-class row of the autonomy report (daemon `autonomy_status`).
export interface AutonomyClassReport {
  class: string; // "C0" | "C1" | "C2" | "C3" | "unclassified"
  description: string;
  accepted: number;
  rejected: number;
  streak: number;
  next_threshold: number; // 0 = none (terminal / non-rung class)
  eligible: string; // "" | "rung-1" | "rung-2" (eligibility math only — rung 0 applies nothing)
}

// Rung-0 observability snapshot. The auto_apply pref is displayed, never
// consumed — no auto-apply behavior exists.
export interface AutonomyReport {
  project_root: string;
  journal: string;
  workstreams_scanned: number;
  conversations_scanned: number;
  resolutions: number;
  unreadable_diffs: number;
  auto_apply: string;
  current_rung: number; // always 0 today
  rung_thresholds: Record<string, number>;
  revert_check: string;
  classes: AutonomyClassReport[];
}

export interface AutonomyStatusResponse {
  ok: boolean;
  error?: string;
  autonomy?: AutonomyReport;
}

// Daemon-managed project settings (daemon `get_settings` / `update_settings`).
export interface Settings {
  coding_model: string;
  coding_provider: string;
  orchestrator_model: string;
  orchestrator_provider: string;
  omp_timeout: string;
  review_models: string;
  // M12: default flipped to "on_idle"; the auto_curate_after_distill pref
  // is gone (auto-curate is daemon-conditional now).
  auto_distill: string;
  auto_distill_idle_seconds: string;
  max_concurrent_runs: string;
  // M15 (O-1 rung-0): off|branch|main|all — parsed and displayed only.
  auto_apply: string;
}

// P1-1: Known sudo-provider models (Hermes custom_providers). Hardcoded for
// MVP — no IPC model-discovery. Free-form values still round-trip via the
// datalist combobox; these only seed the picker suggestions.
export const SUDO_PROVIDER = "sudo";
export const SUDO_MODELS = [
  "t9s/glm-5.2",
  "t9s/kimi-k3",
  "t9s/gpt-5.6-sol",
  "t9s/deepseek-v4-flash",
  "t9s/kimi-k2.7-code",
] as const;

export interface GetSettingsResponse {
  ok: boolean;
  error?: string;
  settings?: Settings;
}

export interface UpdateSettingsResponse {
  ok: boolean;
  error?: string;
}

// Type alias (not interface) so it is assignable to Tauri's InvokeArgs.
export type UpdateSettingsRequest = {
  settings: Partial<Settings>;
};

// ---------- M3: wiki browser + visibility ----------

// One distilled wiki note, listed by the daemon's `list_wiki` command.
export interface WikiNoteInfo {
  path: string;
  name: string;
  epoch: number;
  modified_at: string;
}

export interface ListWikiResponse {
  ok: boolean;
  error?: string;
  wiki_notes?: WikiNoteInfo[];
}

export interface ReadWikiResponse {
  ok: boolean;
  error?: string;
  wiki_content?: string;
}

// M12 (D-auto): one scheduled (or blocked) auto-distill as reported by the
// daemon's pending_counts. eta_unix drives the composer countdown chip; a
// blocked_reason entry has eta 0 and means the fold is paused for honesty
// reasons (the window outgrew the distill prompt budget) until a manual
// distill lands.
export interface AutoDistillCountdown {
  conversation_id: number;
  eta_unix: number;
  trigger: string;
  blocked_reason?: string;
}

// Daemon `pending_counts` (spec §3c fallback): per-workstream pending-diff
// counts plus the workstreams with a live run. JSON object keys arrive as
// strings; the caller converts to number keys. M12: also the auto-distill
// countdowns and in-flight distill state — the GUI discloses daemon
// triggers, it never owns them.
export interface PendingCountsResponse {
  ok: boolean;
  error?: string;
  pending_counts?: Record<string, number>;
  running_workstreams?: number[];
  auto_distill?: AutoDistillCountdown[];
  distilling?: boolean;
  distilling_convs?: number[];
}

// Daemon `auto_distill_ctl` (M12): the chip's Cancel — disarms a scheduled
// (not yet fired) auto-distill. disarmed=false when nothing was armed.
export interface AutoDistillCtlResponse {
  ok: boolean;
  error?: string;
  disarmed?: boolean;
}

// ---------- M4: learning (memory.md / user.md proposals + apply) ----------

// The daemon tags every proposal with the file it targets.
export type MemoryTarget = "memory.md" | "user.md" | "skills";

// One learner-proposed rule from the pending memory_propose batch. evidence
// is optional — user-target proposals have no note evidence; projects is the
// daemon-verified recurrence set (display-only, never the LLM's own tags).
//
// M9: when target is "skills", rule holds the full composed SKILL.md content,
// name is the vetted kebab-case skill name, and reviews carries the tri-model
// gate verdicts.
export interface MemoryProposal {
  target: MemoryTarget;
  rule: string;
  name?: string;    // M9: vetted skill name (skills target only)
  evidence?: string;
  contradicts?: string;
  projects?: string[];
  reviews?: ReviewResult[]; // M9: tri-model gate verdicts (skills target only)
}

// One pending proposal batch (journal-only daemon storage: the
// memory_propose review_action whose epoch matches the latest distill).
export interface PendingMemoryBatch {
  epoch: number;
  seq: number;
  proposals: MemoryProposal[];
  reaffirm?: string[]; // daemon-internal, echoed for transparency
}

// memory_proposals: ok with no batch fields (epoch absent/0) = nothing
// pending; a new distill supersedes an older unconsumed batch.
export interface MemoryProposalsResponse {
  ok: boolean;
  error?: string;
  epoch?: number;
  seq?: number;
  proposals?: MemoryProposal[];
  reaffirm?: string[];
}

// Type alias (not interface) so it is assignable to Tauri's InvokeArgs.
// `index` addresses a proposal by its position in the batch's proposals
// array (across both targets), exactly as the daemon validates it.
export type ApplyMemoryRequest = {
  conversationId: number;
  epoch: number;
  accepted: { target: MemoryTarget; index: number }[];
};

// apply_memory is all-or-nothing: ok:true + applied means every target was
// written and journaled; ok:false leaves the batch pending for retry.
export interface ApplyMemoryResponse {
  ok: boolean;
  error?: string;
  applied?: boolean;
}

// read_memory: the daemon constructs the three canonical paths itself;
// missing files come back as "".
export interface ReadMemoryResponse {
  ok: boolean;
  error?: string;
  memory_content?: string;
  archive_content?: string;
  user_content?: string;
}

// ---------- M5: curation (topic pages + index.md + pins) ----------

// curate: the curator rewrites wiki/topics/*.md + wiki/index.md from the
// full epoch-note set (generation-2 rule). wiki_path is "wiki/index.md";
// memory_proposals is 0 (the sidebar topic count comes from list_topics).
export interface CurateResponse {
  ok: boolean;
  error?: string;
  wiki_path?: string;
  memory_proposals?: number;
}

// pin: store a verbatim pin in .odo/pins.md. ok:false (e.g. the overflow
// refusal, which names the pin text) means nothing was written.
export interface PinResponse {
  ok: boolean;
  error?: string;
  applied?: boolean;
}

// read_pins: .odo/pins.md content, "" when absent (same shape as
// read_memory).
export interface ReadPinsResponse {
  ok: boolean;
  error?: string;
  memory_content?: string;
}

// list_topics: one WikiNoteInfo per wiki/topics/<slug>.md — Name is the
// parsed topic title (first `# ` line, falling back to the slug) and Epoch
// is always 0 (topics are not per-epoch notes).
export interface ListTopicsResponse {
  ok: boolean;
  error?: string;
  wiki_notes?: WikiNoteInfo[];
}

// ---------- M6: precision + ledger ----------

// ledger: .odo/ledger.md content ("" when absent) — same shape as read_pins.
// The daemon is the only writer; the UI renders it read-only.
export interface LedgerResponse {
  ok: boolean;
  error?: string;
  memory_content?: string;
}

// contradictions: the conversation's note-retraction events
// (memory_update{layer:"note", cause:"retract"}) for the wiki browser's
// retracted badges.
export interface ContradictionsResponse {
  ok: boolean;
  error?: string;
  events?: OdoEvent[];
}

// E P2: cross-conversation search
export interface SearchResult {
  event: OdoEvent;
  workstream_id: number;
  workstream_name: string;
  conversation_id: number;
}

export interface SearchEventsResponse {
  ok: boolean;
  error?: string;
  search_results?: SearchResult[];
}

// M8 (Skills): skill metadata and CRUD responses.
export interface SkillInfo {
  name: string;
  description: string;
  keywords?: string[];
  path: string;
  origin: string; // "human" | "ported" | "agent-authored"
  scope: string;  // "global" | "project"
}

export interface ListSkillsResponse {
  ok: boolean;
  error?: string;
  skills?: SkillInfo[];
}

export interface ReadSkillResponse {
  ok: boolean;
  error?: string;
  skill_content?: string;
}

export interface UpdateSkillResponse {
  ok: boolean;
  error?: string;
}

// ---------- M12 (D-todo): durable plan layer ----------

// One journaled todo item as it appears in a todo_merge snapshot, plus the
// read-time flags the daemon derives from fold markers (swept/stale are
// never journaled — the GUI recomputes them with the same arithmetic).
export interface TodoViewItem {
  id: string;                     // daemon-assigned t<N>
  text: string;
  status: "open" | "done" | "struck";
  origin_seq: number;
  updated_seq: number;
  stale: boolean;                 // open, untouched through ≥3 folds (~ marker)
  swept: boolean;                 // done/struck with a fold boundary past updated_seq
}

// The op set the GUI may emit via todo_update (reopen is user-only — an
// agent's fenced block never carries it).
export type TodoUpdateAction = "add" | "done" | "strike" | "reopen" | "reword";

// Daemon `todo_update`: ok with the journaled todo_merge event; semantic
// rejects (unknown id, open cap, …) arrive INSIDE that event's payload as
// ops_rejected, not as an IPC error.
export interface TodoUpdateResponse {
  ok: boolean;
  error?: string;
  event?: OdoEvent;
}
