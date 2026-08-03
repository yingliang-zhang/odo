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
//   memory_update     { layer, cause, before_sha?, after_sha?, detail? }
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
  // M3: memory recall — user_message journals the paths injected into the
  // prompt (~/.odo/user.md first when present, then wiki note paths).
  recall?: string[];
  // M4: injection receipt — content hashes (sha256[:16]) of the exact
  // blocks injected, keyed by the same path strings used in `recall`.
  receipt?: Record<string, string>;
  // memory_update payload fields (M4): which layer changed, why
  // (apply | rotate | retract | failed), and a human-readable summary.
  layer?: string;
  cause?: string;
  detail?: string;
  // review_action when action == "distill" (M1 memory distiller).
  epoch?: number;
  wiki_path?: string;
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
  // a new run; adapter selects the backend ("omp" | "pi").
  steer?: boolean;
  adapter?: string;
};

export interface SendMessageResponse {
  ok: boolean;
  error?: string;
  event?: OdoEvent;
}

export interface PollEventsResponse {
  ok: boolean;
  error?: string;
  events?: OdoEvent[];
  agent_running?: boolean;
  diff?: Diff | null;
  runs?: RunInfo[];
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

// ---------- M2: review panel, settings, fan-out ----------

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
}

// Daemon-managed project settings (daemon `get_settings` / `update_settings`).
export interface Settings {
  coding_model: string;
  coding_provider: string;
  orchestrator_model: string;
  orchestrator_provider: string;
  omp_timeout: string;
  default_adapter: string;
  review_models: string;
}

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

// One parallel run started by `fanout_send`.
export interface RunInfo {
  run_id: string;
  status: "running" | "done" | "error" | (string & {});
}

// Type alias (not interface) so it is assignable to Tauri's InvokeArgs.
export type FanoutSendRequest = {
  conversationId: number;
  text: string;
  n: number;
};

export interface FanoutSendResponse {
  ok: boolean;
  error?: string;
  runs?: RunInfo[];
}

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

// Daemon `pending_counts` (spec §3c fallback): per-workstream pending-diff
// counts plus the workstreams with a live run. JSON object keys arrive as
// strings; the caller converts to number keys.
export interface PendingCountsResponse {
  ok: boolean;
  error?: string;
  pending_counts?: Record<string, number>;
  running_workstreams?: number[];
}

// ---------- M4: learning (memory.md / user.md proposals + apply) ----------

// The daemon tags every proposal with the file it targets.
export type MemoryTarget = "memory.md" | "user.md";

// One learner-proposed rule from the pending memory_propose batch. evidence
// is optional — user-target proposals have no note evidence; projects is the
// daemon-verified recurrence set (display-only, never the LLM's own tags).
export interface MemoryProposal {
  target: MemoryTarget;
  rule: string;
  evidence?: string;
  contradicts?: string;
  projects?: string[];
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
