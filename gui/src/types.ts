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
  | (string & {});

// Payload keys by event type (M0):
//   user_message      { text }
//   agent_text        { text }
//   agent_tool_call   { name, args }      (M1+; the M0 adapter never emits it)
//   agent_tool_result { name, result }    (M1+; the M0 adapter never emits it)
//   agent_done        { summary }
//   agent_error       { error }
//   review_action     { action: "accept" | "reject", diff_id }
export interface EventPayload {
  text?: string;
  summary?: string;
  error?: string;
  name?: string;
  args?: unknown;
  result?: unknown;
  action?: string;
  diff_id?: number;
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
