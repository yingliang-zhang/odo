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
  | "preview_captured"
  | "loop_event"
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
//   preview_captured  { url, bytes, sha256, wait_ms } — /preview's
//                       headless-capture receipt, journaled right after
//                       its slash user_message. sha256 is the full hex of
//                       the PNG; the user_message's image_sha16 covers
//                       the same bytes as the wire preimage.
//   loop_event        M19 (/loop) — ONE discriminated type per design
//                       lock V1: kind ∈ loop_started | loop_design_lock |
//                       loop_task_spawn | loop_task_done |
//                       loop_audit_round | loop_verdict | loop_fix_spawn |
//                       loop_suspended | loop_completed | loop_stopped |
//                       loop_budget_exceeded | loop_recovered |
//                       loop_resumed | loop_notified. Common keys: kind,
//                       loop_id (seq of loop_started — the started row
//                       itself carries loop_id 0 and IS the id), mode
//                       (audit|tasks|tasks_final), actor "auto_loop" (or
//                       origin "loop_ctl" on GUI rows), spent_tokens
//                       (cumulative). Per-kind extras ride the daemon's
//                       journal contract (internal/ipc/loop_journal.go);
//                       the fold below consumes: max_rounds,
//                       budget_tokens, base, tasks, task, status,
//                       verdict, cause, terminal_kind, round,
//                       subject_sha16.
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
  // W6 (goal queue): user_message carries park:true when the user queued
  // the goal for later (derives the durable parked-goal FIFO);
  // review_action rows consume queued seqs — run_prompt{goal_seqs} marks
  // dequeuing runs (origin "parked_goal", actor set for automatic
  // dequeues, absent for a human resume), parked_goal_dropped{goal_seq}
  // journals a human drop.
  park?: boolean;
  goal_seqs?: number[];
  goal_seq?: number;
  actor?: string;
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
  // M18 W2 (prompt receipt closure): run-starting paths journal the
  // assembled prompt's byte total + sha16, plus the recall cap's held-back
  // count when >0, and the replay structural sub-receipt when a replay was
  // injected. Rides user_message on send/slash and
  // review_action{action:"run_prompt"} on continuations — the GUI's
  // context-pressure meter reads the latest of these.
  total_prompt_bytes?: number;
  prompt_sha16?: string;
  recall_held_back?: number;
  replay?: {
    after_seq: number;
    first_seq: number;
    last_seq: number;
    bytes: number;
    dropped_seqs?: number[];
  };
  // GUI Wave B (defensive): billed usage on agent_done. NOTHING writes
  // these yet — the OMP adapter drops the stream's usage at the seam, so
  // the stats strip derives sizes from journaled bytes and only upgrades
  // to billed tokens when a payload carries them (timed_out precedent).
  input_tokens?: number;
  output_tokens?: number;
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
  // review_action when action == "auto_land_started" (auto-land indicator
  // lock Phase 2): which silent stage the pipeline just entered —
  // "verify" (the .odo-verify gate) or "panel" (the review fan-out). The
  // pipeline chip derives its "running" label from this.
  stage?: string;
  // fix-INT W5 (Guardian risk taxonomy, internal/ipc/risk.go): receipt-
  // eligible review_action rows (accept/reject/auto_land_blocked/moa_review/
  // auto_revise_round) carry a PURE-MECHANICAL risk receipt —
  // risk_class is severity-ranked multi-label (["none"] = explicitly rated
  // clean); the key is ABSENT on pre-W5 rows (unrated, never a false clean)
  // and when the patch was unreadable. risk_evidence (one trigger artifact
  // per class) is omitted on clean rows; risk_classifier is the provenance
  // constant ("mechanical" this wave).
  risk_class?: string[];
  risk_evidence?: Record<string, string>;
  risk_classifier?: string;
  // Review-decision result fields ridden by the auto-land pipeline (M16 +
  // settle ladder): consensus_verdict on moa_review; reason on
  // auto_land_blocked; round on auto_revise_round; outcome
  // (clean|conflict|error) + phase (pre_spend_probe|accept_apply) on
  // refresh_attempted (P0a — no risk receipt, it's a rebase, not a verdict).
  consensus_verdict?: string;
  reason?: string;
  round?: number;
  outcome?: string;
  phase?: string;
  // M19 (/loop): loop_event discriminated-type keys (V1 — the single
  // consumer touch-point). `loop_id` is the loop_started row's seq on
  // every later row; the started row itself carries 0. Fix/implement
  // lands and verify/land failures do NOT ride this type — they stay
  // review_action{accept | auto_land_blocked{reason:"loop_*"},
  // actor:"auto_loop"} rows, attributing to the newest non-terminal loop
  // (the Go fold's rule, mirrored in loop.ts).
  kind?: string;
  loop_id?: number;
  mode?: string; // audit | tasks | tasks_final
  spent_tokens?: number; // cumulative ledger at row time
  max_rounds?: number;
  budget_tokens?: number;
  base?: string;
  tasks?: string[]; // loop_started's Mode B task list
  task?: number; // loop_design_lock / loop_task_spawn / loop_task_done
  status?: string; // loop_task_done: landed|settle_blocked|vetoed|design_infra|skipped
  verdict?: string; // loop_verdict: clean|fix|audit_infra|stall|round_cap
  subject_sha16?: string; // loop_audit_round's audited diff subject
  terminal_kind?: string; // loop_notified's receipt key
  rounds?: number; // loop_completed's total audited rounds
  fixes_landed?: number; // loop_completed summary
  findings_count?: number; // loop_audit_round / loop_fix_spawn actionable findings
  budget?: number; // loop_resumed's optional budget raise
  prompt_tokens_est?: number; // spawn-time prompt estimate (chars/4)
  // Read-only run/verify log (tri-model right sidebar gap): moa_review rows
  // from the auto-land pipeline carry the verify that attested the landing —
  // the command and its capped output tail (previously prompt-ephemeral
  // only). Blocked rows already carry theirs inside `detail`.
  verify_cmd?: string;
  verify_tail?: string;
  // auto_revise_round chains the row to the diff being repaired
  // (settle.go:598): round 1 carries diff_id == origin_diff_id (chain
  // start), later rounds carry the just-evaluated product's id with the
  // SAME origin — the GUI resolves chain membership from this pair.
  origin_diff_id?: number;
  // Timed-out review marker (codex ReviewDecision.TimedOut parity):
  // defensive — rendered when the payload carries it, nothing writes it yet.
  timed_out?: boolean;
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
  // carry per-model results for /panel; /preview answers ride the same
  // vision shape (plus preview:true provenance).
  panel?: boolean;
  vision?: boolean;
  preview?: boolean;
  models?: { model: string; text: string; error?: string }[];
  // preview_captured payload fields: the captured URL, PNG byte size,
  // full-sha256 audit hash, and the per-shot wall time.
  url?: string;
  bytes?: number;
  sha256?: string;
  wait_ms?: number;
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

// P1a (review inbox): one inbox row — the daemon's DiffInfoEx embeds
// DiffInfo, and Go flattens the embedded struct, so the wire shape is a
// single object carrying both the diff fields and the workstream label.
export interface DiffInfoEx extends Diff {
  workstream_name: string;
  conversation_id: number;
  workstream_id: number;
}

export interface ListAllPendingDiffsResponse {
  ok: boolean;
  error?: string;
  all_pending_diffs?: DiffInfoEx[];
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
  // W6 (goal queue): park queues the message as a parked goal instead of
  // sending/steering (the daemon refuses steer+park, so the composer
  // passes at most one).
  park?: boolean;
  // M11 P1: routes to that project's daemon; null = bridge default.
  projectRoot?: string | null;
};

export interface SendMessageResponse {
  ok: boolean;
  error?: string;
  event?: OdoEvent;
  // W6: the conversation's parked-goal queue depth after a park.
  parked?: number;
}

// W6 (goal queue): one queued goal, derived from the journal
// (gui/src/parked.ts mirrors the daemon's deriveParkedGoals): the seq of
// its user_message{park:true} row plus the verbatim text.
export interface ParkedGoal {
  seq: number;
  text: string;
}

// Daemon `resume_parked_goal` / `drop_parked_goal` response. ok:false
// (e.g. "no parked goal with seq N") is a benign reconcile — an
// auto-dequeue raced the click, and the next poll reflects it.
export interface ParkedGoalResponse {
  ok: boolean;
  error?: string;
  parked?: number;
}

// Belt A: cancel carries no payload; ok:false ("no active run") is the
// normal race against a run that finished just before the click.
export interface CancelResponse {
  ok: boolean;
  error?: string;
}

// M19 (/loop): daemon `loop_ctl` — design gate + chip stop/resume +
// notification receipts. Stop/resume journal their row and carry it back;
// notified is idempotent (a journaled receipt short-circuits to ok with
// no event). ok:false names the fold-level refusal ("no active loop",
// "not suspended").
export interface LoopCtlResponse {
  ok: boolean;
  error?: string;
  event?: OdoEvent;
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
  // M19 (V11): daemon's loop_notify_on_complete pref, resolved
  // fail-to-default ON (an explicit off-shape pref value reports false).
  // Read-only over IPC — hand-edited in prefs.md. Optional because a
  // pre-M19 daemon never sends it; absent reads as ON by the watcher.
  loop_notify_on_complete?: boolean;
}

// P1-1: Known sudo-provider models (Hermes custom_providers). Hardcoded for
// MVP — no IPC model-discovery. Free-form values still round-trip via the
// datalist combobox; these only seed the picker suggestions.
export const SUDO_PROVIDER = "sudo";
export const SUDO_MODELS = [
  "sudo/t9s/glm-5.2",
  "sudo/t9s/kimi-k3",
  "sudo/t9s/gpt-5",
  "sudo/t9s/gpt-5.6-sol",
  "sudo/t9s/deepseek-v4-flash",
  "sudo/t9s/claude-sonnet-4",
  "sudo/t9s/kimi-k2.7-code",
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
  // W6 (goal queue): per-workstream parked-goal queue depth, keyed like
  // pending_counts. The sidebar's parked pill is sourced from this —
  // the daemon's count is the authoritative depth.
  parked_goals?: Record<string, number>;
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

// ---------- P2 (OMP stats): provider usage + grievances ----------

// One usage limit entry from `omp usage --json`. The daemon passes the
// raw omp JSON through; these interfaces describe the shape the GUI
// renders. Unknown/extra fields are ignored (omp may add new ones).
export interface OmpUsageWindow {
  id: string;
  label: string;
  durationMs: number;
  resetsAt: number;
}

export interface OmpUsageAmount {
  used: number;
  limit: number;
  remaining: number;
  usedFraction: number;
  remainingFraction: number;
  unit: string;
}

export interface OmpUsageLimit {
  id: string;
  label: string;
  window: OmpUsageWindow;
  amount: OmpUsageAmount;
  status: string;
}

export interface OmpUsageReport {
  provider: string;
  fetchedAt: number;
  limits: OmpUsageLimit[];
}

export interface OmpUsageData {
  generatedAt?: number;
  reports?: OmpUsageReport[];
}

// Grievances: omp grievances --json returns an array of issue objects.
// The exact shape varies; the GUI only needs the count and optionally
// the first few for display. We type it as unknown[] for safety.
export type OmpGrievance = Record<string, unknown>;

// Merged response from the daemon's omp_usage handler:
//   { usage: <omp usage JSON>, grievances: <array>,
//     errors?: string[] }
// Fields are omitempty on the Go side — absent (undefined) when the
// corresponding omp command failed, present when it succeeded.
export interface OmpUsageMerged {
  usage?: OmpUsageData | null;
  grievances?: OmpGrievance[] | null;
  errors?: string[];
}

export interface OmpUsageResponse {
  ok: boolean;
  error?: string;
  omp_usage?: OmpUsageMerged;
}
