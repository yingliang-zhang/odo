// Thin type-safe wrappers over the Tauri commands. The Rust layer forwards
// each call to the Go daemon's Unix socket; these functions never contain
// business logic — the daemon is the single source of truth (Invariant 1).
//
// M11 P1: every daemon-bound function takes an optional projectRoot; when
// absent the bridge resolves its default root (identical to pre-M11
// behavior). list_projects is the only command that never touches a
// daemon — it reads the global ~/.odo/projects.json registry directly.

import { invoke as tauriInvoke } from "@tauri-apps/api/core";
import { mockInvoke } from "./dev/mock-invoke";

// E P2: browser dev mode — when not in the Tauri webview, route all
// invoke calls to the mock adapter (fixture data). Same `invoke<T>(cmd, args)`
// signature, so the rest of api.ts is unchanged. Add ?nomock=1 to force
// the real invoke even in a browser.
const useMock = typeof window !== "undefined" &&
  !("__TAURI_INTERNALS__" in window) &&
  !new URLSearchParams(location.search).has("nomock");

// Generic wrapper — mirrors Tauri v2's invoke<T>(cmd, args) signature.
function invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  if (useMock) {
    return mockInvoke(cmd, args) as Promise<T>;
  }
  return tauriInvoke<T>(cmd, args);
}
import type {
  AcceptDiffResponse,
  ApplyMemoryRequest,
  ApplyMemoryResponse,
  BootstrapResponse,
  CancelResponse,
  ContradictionsResponse,
  CreateWorkstreamResponse,
  CurateResponse,
  DistillResponse,
  FanoutSendRequest,
  FanoutSendResponse,
  GetSettingsResponse,
  LedgerResponse,
  ListTopicsResponse,
  ListWikiResponse,
  ListWorkstreamsResponse,
  MemoryProposalsResponse,
  PendingCountsResponse,
  PinResponse,
  PollEventsResponse,
  ProjectEntry,
  ReadMemoryResponse,
  ReadPinsResponse,
  ReadWikiResponse,
  RejectDiffResponse,
  ReviewDiffResponse,
  SendMessageRequest,
  SendMessageResponse,
  SearchEventsResponse,
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

// M11 P1: the daemon-owned global project registry (~/.odo/projects.json),
// read straight from disk by the bridge — no daemon is contacted, so a
// project whose daemon won't start still appears in the switcher.
export function listProjects(): Promise<ProjectEntry[]> {
  return invoke<ProjectEntry[]>("list_projects");
}

// M11 F1: open a native folder picker; if the user selects a folder, the
// bridge ensures the daemon is running (auto-registers the project) and
// returns the new entry. Returns null when the user cancels.
export async function addProject(): Promise<ProjectEntry | null> {
  const entry = await invoke<ProjectEntry | null>("add_project");
  return entry ?? null;
}

export interface SendOptions {
  // steer: journal the message for the running agent (no new run started).
  steer?: boolean;
  // adapter: backend to run with ("omp" | "pi"); ignored for steering.
  adapter?: string;
  // M11 P1: route to that project's daemon; null keeps the bridge default.
  projectRoot?: string;
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
  req.projectRoot = opts?.projectRoot ?? null;
  return invoke<SendMessageResponse>("send_message", req);
}

// Belt A: abort the conversation's active run (adapter SIGKILL). The
// daemon journals agent_error{cancelled by user}; ok:false means the run
// ended before the cancel landed — a benign race, so callers may ignore it.
export function cancel(conversationId: number, projectRoot?: string): Promise<CancelResponse> {
  return invoke<CancelResponse>("cancel", { conversationId, projectRoot: projectRoot ?? null });
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

// M11 F7: rename + delete workstream
export function renameWorkstream(
  projectRoot: string,
  workstreamId: number,
  name: string,
): Promise<BootstrapResponse> {
  return invoke<BootstrapResponse>("rename_workstream", { projectRoot, workstreamId, name });
}

export async function deleteWorkstream(
  projectRoot: string,
  workstreamId: number,
): Promise<ListWorkstreamsResponse> {
  return invoke<ListWorkstreamsResponse>("delete_workstream", { projectRoot, workstreamId });
}

// distill blocks daemon-side until the summary agent finishes (up to 10
// minutes); the Rust bridge uses a matching read timeout for this command.
export function distill(conversationId: number, projectRoot?: string): Promise<DistillResponse> {
  return invoke<DistillResponse>("distill", { conversationId, projectRoot: projectRoot ?? null });
}

export function pollEvents(
  conversationId: number,
  afterSeq: number,
  projectRoot?: string,
): Promise<PollEventsResponse> {
  return invoke<PollEventsResponse>("poll_events", {
    conversationId,
    afterSeq,
    projectRoot: projectRoot ?? null,
  });
}

export function acceptDiff(diffId: number, projectRoot?: string): Promise<AcceptDiffResponse> {
  return invoke<AcceptDiffResponse>("accept_diff", { diffId, projectRoot: projectRoot ?? null });
}

export function rejectDiff(diffId: number, projectRoot?: string): Promise<RejectDiffResponse> {
  return invoke<RejectDiffResponse>("reject_diff", { diffId, projectRoot: projectRoot ?? null });
}

// M2: review_diff blocks daemon-side while every configured review model
// grades the diff; the Rust bridge uses a matching long read timeout.
export function reviewDiff(diffId: number, projectRoot?: string): Promise<ReviewDiffResponse> {
  return invoke<ReviewDiffResponse>("review_diff", { diffId, projectRoot: projectRoot ?? null });
}

// M2 settings: the bridge resolves its default project root when none is
// passed.
export function getSettings(projectRoot?: string): Promise<GetSettingsResponse> {
  return invoke<GetSettingsResponse>("get_settings", { projectRoot: projectRoot ?? null });
}

export function updateSettings(
  settings: Partial<Settings>,
  projectRoot?: string,
): Promise<UpdateSettingsResponse> {
  const req: UpdateSettingsRequest = { settings };
  return invoke<UpdateSettingsResponse>("update_settings", {
    projectRoot: projectRoot ?? null,
    ...req,
  });
}

// M2 fan-out: run the same prompt through N parallel agent runs.
export function fanoutSend(
  conversationId: number,
  text: string,
  n: number,
  projectRoot?: string,
): Promise<FanoutSendResponse> {
  const req: FanoutSendRequest = { conversationId, text, n };
  return invoke<FanoutSendResponse>("fanout_send", { ...req, projectRoot: projectRoot ?? null });
}

// M3 wiki browser: read-only, served from the project's daemon.
export function listWiki(conversationId: number, projectRoot?: string): Promise<ListWikiResponse> {
  return invoke<ListWikiResponse>("list_wiki", {
    conversationId,
    projectRoot: projectRoot ?? null,
  });
}

export function readWiki(path: string, projectRoot?: string): Promise<ReadWikiResponse> {
  return invoke<ReadWikiResponse>("read_wiki", { path, projectRoot: projectRoot ?? null });
}

// M3 visibility (spec §3c): the sidebar's only view into OTHER workstreams'
// runs and pending diffs — poll_events is per-conversation.
export function pendingCounts(projectRoot: string): Promise<PendingCountsResponse> {
  return invoke<PendingCountsResponse>("pending_counts", { projectRoot });
}

// M4 learning: read the three canonical memory files (project memory.md,
// memory-archive.md, global user.md) through the project's daemon; when no
// root is given the bridge default daemon answers (today's behavior).
export async function readMemory(projectRoot?: string): Promise<ReadMemoryResponse> {
  return unwrap(await invoke<ReadMemoryResponse>("read_memory", {
    projectRoot: projectRoot ?? null,
  }));
}

// M4 learning: the conversation's pending proposal batch. ok:true with no
// epoch means nothing is pending (no batch, or the latest distill emitted
// none); unwrap turns daemon failures into thrown Errors.
export async function memoryProposals(
  conversationId: number,
  projectRoot?: string,
): Promise<MemoryProposalsResponse> {
  return unwrap(await invoke<MemoryProposalsResponse>("memory_proposals", {
    conversationId,
    projectRoot: projectRoot ?? null,
  }));
}

// M4 learning: apply the accepted subset of the pending batch
// (all-or-nothing daemon-side). A refusal (e.g. user.md overflow) throws via
// unwrap and leaves the batch pending for retry.
export async function applyMemory(
  req: ApplyMemoryRequest,
  projectRoot?: string,
): Promise<ApplyMemoryResponse> {
  return unwrap(
    await invoke<ApplyMemoryResponse>("apply_memory", {
      conversationId: req.conversationId,
      epoch: req.epoch,
      accepted: req.accepted,
      projectRoot: projectRoot ?? null,
    }),
  );
}

// M5 curation: curate blocks daemon-side up to 10 minutes while the
// curator reads the full epoch-note set and rewrites every topic page +
// index.md; the Rust bridge uses CURATE_READ_TIMEOUT for this command.
export function curate(conversationId: number, projectRoot?: string): Promise<CurateResponse> {
  return invoke<CurateResponse>("curate", { conversationId, projectRoot: projectRoot ?? null });
}

// M5 curation: store a verbatim pin in .odo/pins.md. A refusal (empty
// text, or the file would overflow its 2 KB cap) arrives as ok:false
// naming the pin text; nothing is written.
export function pin(conversationId: number, text: string, projectRoot?: string): Promise<PinResponse> {
  return invoke<PinResponse>("pin", { conversationId, text, projectRoot: projectRoot ?? null });
}

// M5 curation: .odo/pins.md content for the review panel reader; same
// shape and unwrap semantics as readMemory.
export async function readPins(projectRoot?: string): Promise<ReadPinsResponse> {
  return unwrap(await invoke<ReadPinsResponse>("read_pins", { projectRoot: projectRoot ?? null }));
}

// M5 curation: topic pages for the wiki browser's Topics tab; the
// project's daemon lists them (the bridge default answers when no root is
// given).
export async function listTopics(projectRoot?: string): Promise<ListTopicsResponse> {
  return unwrap(await invoke<ListTopicsResponse>("list_topics", {
    projectRoot: projectRoot ?? null,
  }));
}

// M6 precision+ledger: .odo/ledger.md content for the review panel's Ledger
// tab; same shape and unwrap semantics as readMemory/readPins. The daemon
// is the only writer; the file is never injected into prompts (pull-only).
export async function ledger(projectRoot?: string): Promise<LedgerResponse> {
  return unwrap(await invoke<LedgerResponse>("ledger", { projectRoot: projectRoot ?? null }));
}

// M6 precision+ledger: the conversation's note-retraction events for the
// wiki browser's "⚠ retracted" badges and the retraction toast.
export async function contradictions(
  conversationId: number,
  projectRoot?: string,
): Promise<ContradictionsResponse> {
  return unwrap(await invoke<ContradictionsResponse>("contradictions", {
    conversationId,
    projectRoot: projectRoot ?? null,
  }));
}

// E P2: cross-conversation search
export async function searchEvents(
  text: string,
  projectRoot?: string,
): Promise<SearchEventsResponse> {
  return unwrap(await invoke<SearchEventsResponse>("search_events", {
    text,
    projectRoot: projectRoot ?? null,
  }));
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
