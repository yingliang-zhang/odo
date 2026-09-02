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
  AutonomyStatusResponse,
  AcceptDiffResponse,
  ApplyMemoryRequest,
  ApplyMemoryResponse,
  AutoDistillCtlResponse,
  BootstrapResponse,
  TodoUpdateAction,
  TodoUpdateResponse,
  CancelResponse,
  ContradictionsResponse,
  CreateWorkstreamResponse,
  CurateResponse,
  DistillResponse,
  ForkConversationResponse,
  GetSettingsResponse,
  LearningStatusResponse,
  ListAllPendingDiffsResponse,
  ListTopicsResponse,
  ListWikiResponse,
  ListWorkstreamsResponse,
  LoopCtlResponse,
  MemoryProposalsResponse,
  ParkedGoalResponse,
  PendingCountsResponse,
  PinResponse,
  PollEventsResponse,
  ProjectEntry,
  QueuedSteerResponse,
  ReadMemoryResponse,
  ReadPinsResponse,
  ReadWikiResponse,
  RejectDiffResponse,
  ResolveHealConflictResponse,
  ReviewDiffResponse,
  SendMessageRequest,
  SearchEventsResponse,
  WriteMemoryResponse,
  RunCommandResponse,
  SendMessageResponse,
  Settings,
  UpdateSettingsRequest,
  UpdateSettingsResponse,
  ListSkillsResponse,
  OmpUsageResponse,
  K8sStatusResponse,
  K8sBatchStatusResponse,
  ReadSkillResponse,
  UpdateSkillResponse,
} from "./types";

// Tauri v2 maps JS camelCase args onto the Rust snake_case parameters.

// `cached` is the switch-cache hint (conversation id + full-journal seq
// high-water); when it still resolves to the active conversation the
// daemon replays only the tail instead of the whole journal.
export function bootstrap(
  projectRoot?: string,
  workstreamId?: number,
  cached?: { conversationId: number; afterSeq: number },
): Promise<BootstrapResponse> {
  return invoke<BootstrapResponse>("bootstrap", {
    projectRoot: projectRoot ?? null,
    workstreamId: workstreamId ?? null,
    conversationId: cached?.conversationId ?? null,
    afterSeq: cached?.afterSeq ?? null,
  });
}

// M11 P1: the daemon-owned global project registry (~/.odo/projects.json),
// read straight from disk by the bridge — no daemon is contacted, so a
// project whose daemon won't start still appears in the switcher.
export function listProjects(): Promise<ProjectEntry[]> {
  return invoke<ProjectEntry[]>("list_projects");
}

// Open file / reveal in folder — pure OS command, daemon-free.
// Resolves relative paths against projectRoot. Canonicalizes + validates
// containment (projectRoot or ~/.odo) on the Rust side.
export async function openPath(
  path: string,
  reveal: boolean,
  projectRoot?: string | null,
): Promise<string> {
  return invoke<string>("open_path", {
    path,
    reveal,
    projectRoot: projectRoot ?? null,
  });
}

// Inline file preview — daemon-side read with the same containment rule as
// open_path (canonicalize-then-prefix-check). Binary files and escapes are
// rejected daemon-side; content is capped at 512 KiB (FileTruncated).
export interface ReadFileResponse {
  file_content?: string;
  file_resolved?: string;
  file_truncated?: boolean;
  // P2.1 (forward-compatible, absent on today's daemon): when a later
  // read_file serves an image it returns raw bytes base64-encoded plus a
  // MIME hint; the GUI renders `data:<mime>;base64,…` capped at 2MB.
  file_data_base64?: string;
  file_mime?: string;
}
export async function readFile(
  path: string,
  projectRoot?: string | null,
): Promise<ReadFileResponse> {
  return invoke<ReadFileResponse>("read_file", {
    path,
    projectRoot: projectRoot ?? null,
  });
}

// M11 F1: open a native folder picker; if the user selects a folder, the
// bridge ensures the daemon is running (auto-registers the project) and
// returns the new entry. Returns null when the user cancels.
export async function addProject(): Promise<ProjectEntry | null> {
  const entry = await invoke<ProjectEntry | null>("add_project");
  return entry ?? null;
}

// M11 F8: drop a project from the global registry (the phantom-project
// escape hatch). Daemon-free like list_projects — it works even when the
// entry's daemon is dead, which is exactly the state stale rows are in.
// Returns the updated registry so callers can setState without a re-read.
// Project files and any running daemon are untouched.
export function removeProject(root: string): Promise<ProjectEntry[]> {
  return invoke<ProjectEntry[]>("remove_project", { root });
}

export interface SendOptions {
  // steer: journal the message for the running agent (no new run started).
  steer?: boolean;
  // W6 (goal queue): park queues the message as a parked goal. Mutually
  // exclusive with steer — the composer passes at most one (the daemon
  // refuses a steer+park combination).
  park?: boolean;
  // adapter: backend to run with ("omp"); ignored for steering.
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
  if (opts?.park) {
    req.park = true;
  }
  if (opts?.adapter) {
    req.adapter = opts.adapter;
  }
  req.projectRoot = opts?.projectRoot ?? null;
  return invoke<SendMessageResponse>("send_message", req);
}

// A1: save_attachment — writes base64-encoded clipboard image to .odo/attachments/
// and returns the absolute path for use as an attachment in /vision queries.
export interface SaveAttachmentResponse {
  ok: boolean;
  error?: string;
  path?: string;
}

export function saveAttachment(
  name: string,
  data: string,
  projectRoot?: string,
): Promise<SaveAttachmentResponse> {
  return invoke<SaveAttachmentResponse>("save_attachment", {
    name,
    data,
    projectRoot: projectRoot ?? null,
  });
}

// Belt A: abort the conversation's active run (adapter SIGKILL). The
// daemon journals agent_error{cancelled by user}; ok:false means the run
// ended before the cancel landed — a benign race, so callers may ignore it.
export function cancel(conversationId: number, projectRoot?: string): Promise<CancelResponse> {
  return invoke<CancelResponse>("cancel", { conversationId, projectRoot: projectRoot ?? null });
}
// P1 borrow #6 (turn-fork, quad-audit follow-up): branch-copy the
// conversation's journal prefix (everything up to and INCLUDING fromSeq)
// into a fresh lane + worktree, then switch over — matches the
// GitFork affordance on user_message bubbles. Refusals (from_seq below
// the journal floor / past its end) come back IPC-side as errors.
export function forkConversation(
  conversationId: number,
  fromSeq: number,
  projectRoot?: string,
): Promise<ForkConversationResponse> {
  return invoke<ForkConversationResponse>("fork_conversation", {
    conversationId,
    fromSeq,
    projectRoot: projectRoot ?? null,
  });
}

// M19 (/loop): GUI chip buttons + design-gate verbs + notification
// receipts (design lock: GUI-only IPC). stop/resume resolve the active
// loop daemon-side — the chip passes no loop_id; the design-gate verbs
// (approve/veto, amend needs the amended design in `text`) likewise
// resolve the pending design lock daemon-side (loopDesignCtl: the first
// not-done task holding a lock — at most one can be open). notified
// carries loop_id + terminal kind in `text` (the daemon dedups per
// terminal kind: a journaled receipt makes re-fires impossible).
export function loopCtl(
  conversationId: number,
  action: "stop" | "resume" | "notified" | "approve_design" | "amend_design" | "veto_design",
  opts?: { loopId?: number; text?: string; loopBudget?: number; projectRoot?: string },
): Promise<LoopCtlResponse> {
  return invoke<LoopCtlResponse>("loop_ctl", {
    conversationId,
    action,
    loopId: opts?.loopId ?? null,
    text: opts?.text ?? null,
    loopBudget: opts?.loopBudget ?? null,
    projectRoot: opts?.projectRoot ?? null,
  });
}

// W6 (goal queue): activate one parked goal now. The daemon refuses with
// ok:false when a run is active, the concurrency cap is reached, or a
// distill is in progress — the goal stays queued, so the caller treats a
// refusal as a benign reconcile, not an error.
export function resumeParkedGoal(conversationId: number, goalSeq: number, projectRoot?: string): Promise<ParkedGoalResponse> {
  return invoke<ParkedGoalResponse>("resume_parked_goal", { conversationId, goalSeq, projectRoot: projectRoot ?? null });
}

// W6 (goal queue): journal a human drop for one parked goal (the "clean
// the junk drawer" path). Always available, even while a run is active.
export function dropParkedGoal(conversationId: number, goalSeq: number, projectRoot?: string): Promise<ParkedGoalResponse> {
  return invoke<ParkedGoalResponse>("drop_parked_goal", { conversationId, goalSeq, projectRoot: projectRoot ?? null });
}

// Steer queue: journal a human drop for one queued steer. Only the run
// that owns the steer can close it, so the caller treats an ok:false
// refusal as a benign reconcile, not an error.
export function dropQueuedSteer(conversationId: number, steerSeq: number, projectRoot?: string): Promise<QueuedSteerResponse> {
  return invoke<QueuedSteerResponse>("drop_queued_steer", { conversationId, steerSeq, projectRoot: projectRoot ?? null });
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

export function acceptDiff(diffId: number, projectRoot?: string, commitMessage?: string): Promise<AcceptDiffResponse> {
  return invoke<AcceptDiffResponse>("accept_diff", {
    diffId,
    projectRoot: projectRoot ?? null,
    // Tri-model right sidebar gap: user-editable commit message on Accept.
    // Empty string → daemon default "odo: accept diff #N".
    commitMessage: commitMessage ?? null,
  });
}

export function rejectDiff(diffId: number, projectRoot?: string): Promise<RejectDiffResponse> {
  return invoke<RejectDiffResponse>("reject_diff", { diffId, projectRoot: projectRoot ?? null });
}

// M2: review_diff blocks daemon-side while every configured review model
// grades the diff; the Rust bridge uses a matching long read timeout.
export function reviewDiff(diffId: number, projectRoot?: string): Promise<ReviewDiffResponse> {
  return invoke<ReviewDiffResponse>("review_diff", { diffId, projectRoot: projectRoot ?? null });
}

// M15 (O-1 rung-0): the autonomy streak snapshot the DiffViewer header
// shows on open; a read-only journal computation daemon-side.
export function autonomyStatus(projectRoot?: string): Promise<AutonomyStatusResponse> {
  return invoke<AutonomyStatusResponse>("autonomy_status", { projectRoot: projectRoot ?? null });
}
// D9-W3 (learning control plane, pure observability): the flagged-rules +
// episode/candidate fold the Learning panel renders. Read-only — no
// decision path consumes it.
export function learningStatus(projectRoot?: string): Promise<LearningStatusResponse> {
  return invoke<LearningStatusResponse>("learning_status", { projectRoot: projectRoot ?? null });
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

// P1a (review inbox): every pending diff across the project's active
// workstreams with content + workstream labels — the Review tab's single
// fetch. Gated on tab visibility at the call site; never poll blindly.
export function listAllPendingDiffs(projectRoot?: string): Promise<ListAllPendingDiffsResponse> {
  return invoke<ListAllPendingDiffsResponse>("list_all_pending_diffs", { projectRoot: projectRoot ?? null });
}

// M12 (D-auto): the composer countdown chip's Cancel — disarm a scheduled
// auto-distill. The daemon journals the disarm; in-flight distills are not
// touched (a send cancels those).
export function autoDistillCtl(conversationId: number, action: "disarm", projectRoot?: string): Promise<AutoDistillCtlResponse> {
  return invoke<AutoDistillCtlResponse>("auto_distill_ctl", { conversationId, action, projectRoot: projectRoot ?? null });
}

// M12 (D-todo): one user op from the composer "Plan" popover. The daemon
// journals the merge with origin:"user"; semantic rejects (unknown id, cap)
// land inside the journaled event as ops_rejected, not as an IPC error.
export function todoUpdate(
  conversationId: number,
  action: TodoUpdateAction,
  opts?: { todoId?: string; text?: string; projectRoot?: string },
): Promise<TodoUpdateResponse> {
  return invoke<TodoUpdateResponse>("todo_update", {
    conversationId,
    action,
    todoId: opts?.todoId ?? null,
    text: opts?.text ?? null,
    projectRoot: opts?.projectRoot ?? null,
  });
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
// resolve_heal_conflict (2026-08-26 memory-replay doctrine): close one
// journaled heal_conflict — Resolve restores the stranded body, Dismiss
// records the decision without a write. applied=false (or a thrown
// error string) means files and the ledger stayed untouched.
export function resolveHealConflict(
  args: { conversationId: number; layer: string; receiptSeq: number; strandedConversation: number; dismissed?: boolean },
  projectRoot?: string,
): Promise<ResolveHealConflictResponse> {
  return invoke<ResolveHealConflictResponse>("resolve_heal_conflict", {
    conversationId: args.conversationId,
    layer: args.layer,
    receiptSeq: args.receiptSeq,
    strandedConversation: args.strandedConversation,
    dismissed: args.dismissed ?? false,
    projectRoot: projectRoot ?? null,
  });
}

// M5 curation: .odo/pins.md content for the review panel reader; same
// shape and unwrap semantics as readMemory.
export async function readPins(projectRoot?: string): Promise<ReadPinsResponse> {
  return unwrap(await invoke<ReadPinsResponse>("read_pins", { projectRoot: projectRoot ?? null }));
}

// Odo DX wave: the Memory tab's direct-edit shortcut — full-body replace
// of memory.md / pins.md through the same daemon containment + layer caps
// (user.md stays cross-project-owned). A refusal (unknown layer, over the
// layer cap) throws via unwrap; nothing is written.
export async function writeMemory(
  file: "memory.md" | "pins.md",
  content: string,
  projectRoot?: string,
): Promise<WriteMemoryResponse> {
  return unwrap(await invoke<WriteMemoryResponse>("write_memory", {
    file,
    content,
    projectRoot: projectRoot ?? null,
  }));
}

// Odo DX wave (Run/Test hub): execute one named .odo/commands.json
// command. Validation failures (missing/malformed config, unknown name)
// throw via unwrap; an EXECUTED command answers its outcome — exit 0 or
// not — and the daemon journals the same row as command_result.
export async function runCommand(
  conversationId: number,
  name: string,
  projectRoot?: string,
): Promise<RunCommandResponse> {
  return unwrap(await invoke<RunCommandResponse>("run_command", {
    conversationId,
    name,
    projectRoot: projectRoot ?? null,
  }));
}

// M5 curation: topic pages for the wiki browser's Topics tab; the
// project's daemon lists them (the bridge default answers when no root is
// given).
export async function listTopics(projectRoot?: string): Promise<ListTopicsResponse> {
  return unwrap(await invoke<ListTopicsResponse>("list_topics", {
    projectRoot: projectRoot ?? null,
  }));
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

// E P2 / P1.1: read-only journal search across the project's active
// workstreams; the daemon owns the index (journal stays the only one).
export function searchEvents(text: string, projectRoot?: string): Promise<SearchEventsResponse> {
  return invoke<SearchEventsResponse>("search_events", { text, projectRoot: projectRoot ?? null });
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

// M8 (Skills): list/read/update skill files via the daemon.
export async function listSkills(projectRoot?: string): Promise<ListSkillsResponse> {
  return unwrap(await invoke<ListSkillsResponse>("list_skills", {
    projectRoot: projectRoot ?? null,
  }));
}

export async function readSkill(path: string, projectRoot?: string): Promise<ReadSkillResponse> {
  return unwrap(await invoke<ReadSkillResponse>("read_skill", {
    path,
    projectRoot: projectRoot ?? null,
  }));
}

export async function updateSkill(
  name: string,
  text: string,
  scope?: string,
  path?: string,
  projectRoot?: string,
): Promise<UpdateSkillResponse> {
  return unwrap(await invoke<UpdateSkillResponse>("update_skill", {
    name,
    text,
    scope: scope ?? "project",
    path: path ?? "",
    projectRoot: projectRoot ?? null,
  }));
}

export async function deleteSkill(
  name: string,
  scope?: string,
  projectRoot?: string,
): Promise<UpdateSkillResponse> {
  return unwrap(await invoke<UpdateSkillResponse>("delete_skill", {
    name,
    scope: scope ?? "project",
    projectRoot: projectRoot ?? null,
  }));
}

// P2 (OMP stats): merged provider usage + grievances for the StatusBar's
// read-only stats chip. The daemon shells out to omp with a 10s timeout.
export function ompUsage(projectRoot?: string): Promise<OmpUsageResponse> {
  return invoke<OmpUsageResponse>("omp_usage", {
    projectRoot: projectRoot ?? null,
  });
}

// UX-2 (D5 Stage 0 / A2-1): the StatusBar Jobs chip's read-only k8s
// snapshot. Degradation rides the payload (available:false + reason
// class) — a configured broken sensor never surfaces as an IPC error.
export function k8sStatus(projectRoot?: string): Promise<K8sStatusResponse> {
  return invoke<K8sStatusResponse>("k8s_status", {
    projectRoot: projectRoot ?? null,
  });
}

// D5b (A2-4): the batch progress bridge — status.json rows (local CPFS
// read first, kubectl exec cat fallback). Same containment: degradation
// rides the payload, never the envelope.
export function k8sBatchStatus(projectRoot?: string): Promise<K8sBatchStatusResponse> {
  return invoke<K8sBatchStatusResponse>("k8s_batch_status", {
    projectRoot: projectRoot ?? null,
  });
}
