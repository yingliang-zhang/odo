// Thin type-safe wrappers over the Tauri commands. The Rust layer forwards
// each call to the Go daemon's Unix socket; these functions never contain
// business logic — the daemon is the single source of truth (Invariant 1).

import { invoke } from "@tauri-apps/api/core";
import type {
  AcceptDiffResponse,
  ApplyMemoryRequest,
  ApplyMemoryResponse,
  BootstrapResponse,
  CreateWorkstreamResponse,
  DistillResponse,
  FanoutSendRequest,
  FanoutSendResponse,
  GetSettingsResponse,
  ListWikiResponse,
  ListWorkstreamsResponse,
  MemoryProposalsResponse,
  PendingCountsResponse,
  PollEventsResponse,
  ReadMemoryResponse,
  ReadWikiResponse,
  RejectDiffResponse,
  ReviewDiffResponse,
  SendMessageRequest,
  SendMessageResponse,
  Settings,
  UpdateSettingsRequest,
  UpdateSettingsResponse,
} from "./types";

// Tauri v2 maps JS camelCase args onto the Rust snake_case parameters.

export function bootstrap(projectRoot?: string, workstreamId?: number): Promise<BootstrapResponse> {
  return invoke<BootstrapResponse>("bootstrap", {
    projectRoot: projectRoot ?? null,
    workstreamId: workstreamId ?? null,
  });
}

export interface SendOptions {
  // steer: journal the message for the running agent (no new run started).
  steer?: boolean;
  // adapter: backend to run with ("omp" | "pi"); ignored for steering.
  adapter?: string;
}

export function sendMessage(
  conversationId: number,
  text: string,
  attachments?: string[],
  opts?: SendOptions,
): Promise<SendMessageResponse> {
  const req: SendMessageRequest = { conversationId, text };
  const paths = (attachments ?? []).filter((a) => a.trim() !== "");
  if (paths.length > 0) {
    req.attachments = paths;
  }
  if (opts?.steer) {
    req.steer = true;
  }
  if (opts?.adapter) {
    req.adapter = opts.adapter;
  }
  return invoke<SendMessageResponse>("send_message", req);
}

export function listWorkstreams(projectRoot: string): Promise<ListWorkstreamsResponse> {
  return invoke<ListWorkstreamsResponse>("list_workstreams", { projectRoot });
}

export function createWorkstream(
  projectRoot: string,
  name: string,
): Promise<CreateWorkstreamResponse> {
  return invoke<CreateWorkstreamResponse>("create_workstream", { projectRoot, name });
}

// distill blocks daemon-side until the summary agent finishes (up to 10
// minutes); the Rust bridge uses a matching read timeout for this command.
export function distill(conversationId: number): Promise<DistillResponse> {
  return invoke<DistillResponse>("distill", { conversationId });
}

export function pollEvents(conversationId: number, afterSeq: number): Promise<PollEventsResponse> {
  return invoke<PollEventsResponse>("poll_events", { conversationId, afterSeq });
}

export function acceptDiff(diffId: number): Promise<AcceptDiffResponse> {
  return invoke<AcceptDiffResponse>("accept_diff", { diffId });
}

export function rejectDiff(diffId: number): Promise<RejectDiffResponse> {
  return invoke<RejectDiffResponse>("reject_diff", { diffId });
}

// M2: review_diff blocks daemon-side while every configured review model
// grades the diff; the Rust bridge uses a matching long read timeout.
export function reviewDiff(diffId: number): Promise<ReviewDiffResponse> {
  return invoke<ReviewDiffResponse>("review_diff", { diffId });
}

// M2 settings: the daemon resolves the project root when none is passed.
export function getSettings(): Promise<GetSettingsResponse> {
  return invoke<GetSettingsResponse>("get_settings", { projectRoot: null });
}

export function updateSettings(settings: Partial<Settings>): Promise<UpdateSettingsResponse> {
  const req: UpdateSettingsRequest = { settings };
  return invoke<UpdateSettingsResponse>("update_settings", { projectRoot: null, ...req });
}

// M2 fan-out: run the same prompt through N parallel agent runs.
export function fanoutSend(conversationId: number, text: string, n: number): Promise<FanoutSendResponse> {
  const req: FanoutSendRequest = { conversationId, text, n };
  return invoke<FanoutSendResponse>("fanout_send", req);
}

// M3 wiki browser: read-only, served from the daemon's project root.
export function listWiki(conversationId: number): Promise<ListWikiResponse> {
  return invoke<ListWikiResponse>("list_wiki", { conversationId });
}

export function readWiki(path: string): Promise<ReadWikiResponse> {
  return invoke<ReadWikiResponse>("read_wiki", { path });
}

// M3 visibility (spec §3c): the sidebar's only view into OTHER workstreams'
// runs and pending diffs — poll_events is per-conversation.
export function pendingCounts(projectRoot: string): Promise<PendingCountsResponse> {
  return invoke<PendingCountsResponse>("pending_counts", { projectRoot });
}

// M4 learning: read the three canonical memory files (project memory.md,
// memory-archive.md, global user.md) through the daemon; a project root is
// never sent — the daemon uses its bound root.
export async function readMemory(): Promise<ReadMemoryResponse> {
  return unwrap(await invoke<ReadMemoryResponse>("read_memory", { projectRoot: null }));
}

// M4 learning: the conversation's pending proposal batch. ok:true with no
// epoch means nothing is pending (no batch, or the latest distill emitted
// none); unwrap turns daemon failures into thrown Errors.
export async function memoryProposals(conversationId: number): Promise<MemoryProposalsResponse> {
  return unwrap(await invoke<MemoryProposalsResponse>("memory_proposals", { conversationId }));
}

// M4 learning: apply the accepted subset of the pending batch
// (all-or-nothing daemon-side). A refusal (e.g. user.md overflow) throws via
// unwrap and leaves the batch pending for retry.
export async function applyMemory(req: ApplyMemoryRequest): Promise<ApplyMemoryResponse> {
  return unwrap(
    await invoke<ApplyMemoryResponse>("apply_memory", {
      conversationId: req.conversationId,
      epoch: req.epoch,
      accepted: req.accepted,
    }),
  );
}

// Daemon-level failures arrive with ok:false; transport failures (invoke
// rejects) arrive as thrown strings. Normalize both into Error.
export function unwrap<T extends { ok: boolean; error?: string }>(resp: T): T {
  if (!resp.ok) {
    throw new Error(resp.error ?? "daemon returned an unknown error");
  }
  return resp;
}

export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message;
  if (typeof e === "string") return e;
  return String(e);
}
