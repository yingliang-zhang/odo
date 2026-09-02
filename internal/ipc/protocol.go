// Package ipc implements the daemon's Unix-socket API: line-delimited JSON
// requests and responses. M11 P0: goroutine-per-connection serving; one
// connection processes its requests sequentially, but connections never
// block each other.
package ipc

import (
	"encoding/json"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/moa"
	"github.com/yingliang-zhang/odo/internal/store"
)

// Settings aliases the adapter package's settings shape so IPC payloads and
// prefs.md handling share one definition.
type Settings = adapter.Settings

// Commands.
const (
	CmdBootstrap        = "bootstrap"
	CmdSendMessage      = "send_message"
	CmdCancel           = "cancel"
	CmdPollEvents       = "poll_events"
	CmdAcceptDiff       = "accept_diff"
	CmdRejectDiff       = "reject_diff"
	CmdCreateWorkstream = "create_workstream"
	CmdListWorkstreams  = "list_workstreams"
	CmdRenameWorkstream = "rename_workstream"
	CmdDeleteWorkstream = "delete_workstream"
	CmdDistill          = "distill"
	CmdReviewDiff       = "review_diff"
	CmdGetSettings      = "get_settings"
	CmdUpdateSettings   = "update_settings"
	CmdPendingCounts    = "pending_counts"
	// P1a (review inbox): list_all_pending_diffs returns every pending diff
	// across all active workstreams of the project, labeled with the owning
	// workstream — the aggregate review tab's data source.
	CmdListAllPendingDiffs = "list_all_pending_diffs"
	CmdListWiki            = "list_wiki"
	CmdReadWiki            = "read_wiki"
	CmdReadMemory          = "read_memory"
	CmdMemoryProposals     = "memory_proposals"
	CmdApplyMemory         = "apply_memory"
	// M5 (Curation): curate rewrites topic pages + wiki/index.md from the full
	// epoch-note set; pin/read_pins manage the human-owned .odo/pins.md;
	// list_topics lists wiki/topics/*.md for the browser's Topics tab.
	CmdCurate     = "curate"
	CmdPin        = "pin"
	CmdReadPins   = "read_pins"
	CmdListTopics = "list_topics"
	// M6 (Precision + Ledger): ledger reads .odo/ledger.md (same shape as
	// read_pins); contradictions returns the conversation's note-retraction
	// events for the wiki browser's retracted badges.
	CmdLedger         = "ledger"
	CmdContradictions = "contradictions"
	CmdSearchEvents   = "search_events"
	// M8 (Skills): skills are plain markdown files in ~/.odo/skills/ and
	// .odo/skills/. list_skills returns metadata; read_skill returns the
	// full body; update_skill writes (creates or overwrites) a skill file.
	CmdListSkills     = "list_skills"
	CmdReadSkill      = "read_skill"
	CmdUpdateSkill    = "update_skill"
	CmdDeleteSkill    = "delete_skill"
	CmdSaveAttachment = "save_attachment"
	// M12 (D-auto): auto_distill_ctl disarms a scheduled (not yet fired)
	// auto-distill for one conversation — the composer countdown chip's
	// Cancel. The disarm is journaled.
	CmdAutoDistillCtl = "auto_distill_ctl"
	// M12 (D-todo): todo_update applies one user op (add/done/strike/
	// reopen/reword) from the composer "Plan" popover; the merge journals
	// with origin:"user" exactly like an agent-emitted odo-todo block.
	CmdTodoUpdate = "todo_update"
	// M15 (O-1 rung-0): autonomy_status returns the rung-0 streak snapshot
	// for the DiffViewer header (same journal reads as odo autonomy audit).
	CmdAutonomyStatus = "autonomy_status"
	// W6 (goal queue, ADR-0005): the manual override pair for the durable
	// per-conversation parked-goal FIFO — resume activates the queue head
	// (or Request.GoalSeq's entry), drop journals parked_goal_dropped and
	// removes the entry ("clean the junk drawer").
	CmdResumeParkedGoal = "resume_parked_goal"
	CmdDropParkedGoal   = "drop_parked_goal"
	// drop_queued_steer (Hermes steer queue): the manual drop twin of
	// drop_parked_goal for the transient steer queue — removes one
	// still-queued steer from the conversation's active run and journals
	// review_action{action:"steer_dropped", steer_seq}. Already-consumed
	// or dropped seqs refuse cleanly (a benign reconcile, journal-neutral).
	CmdDropQueuedSteer = "drop_queued_steer"
	// R-W4 (Design-MoA): design_moa fans a goal out to blind-sealed
	// proposal legs (the prefs review: models over read-only repo tools),
	// then consolidates them into one DESIGN LOCK via a single
	// orchestrator-model moa.Query, journaled as review_action
	// {action:"design_lock"}. Opt-in: requires prefs `design_via: moa`.
	CmdDesignMoa = "design_moa"
	// P2 (OMP stats): omp_usage returns provider usage limits and
	// grievances from `omp usage --json` + `omp grievances --json`,
	// merged into one JSON blob. Read-only display — the data is
	// never journaled as facts.
	CmdOmpUsage = "omp_usage"
	// UX-2 (D5 Stage 0 / A2-1): k8s_status is the one-shot k8s snapshot —
	// kubectl get jobs,pods -n <ns> -o json, read-only get ONLY, argv-only
	// exec (the runOmpJSON posture), NEVER journaled (cluster state never
	// enters the journal). Degradation contract: off-by-config answers
	// available:false reason:"off" with no exec; a configured broken sensor
	// answers available:false WITH a cause class — never silently.
	CmdK8sStatus = "k8s_status"
	// D5b (A2-4): k8s_batch_status is the batch progress bridge —
	// local-mount-first status.json reader with a pod-cat fallback. Same
	// containment as k8s_status: read-only, one-shot per invoke, NEVER
	// journaled. See docs/design/d5b-batch-status.md for the schema.
	CmdK8sBatchStatus = "k8s_batch_status"
	// read_file: inline file preview (tri-model right sidebar gap). Reads a
	// project-contained text file with the same containment rule as the
	// GUI's open_path (canonicalize-then-prefix-check); binary files and
	// escape attempts are rejected. Capped at readFileMaxBytes.
	CmdReadFile = "read_file"
	// Odo DX wave (Memory tab direct edit): write_memory replaces the
	// full body of a PROJECT memory layer — .odo/memory.md or
	// .odo/pins.md ONLY (Request.File argv-validated; user.md stays
	// owned by ~/.odo edits). Atomic (writeFileWithin tmp+rename), held
	// to the layer's cap (memoryCap/pinsCap, refuse-on-overflow), and
	// journaled nowhere: a rename lands whole or not at all, so there
	// is no multi-step state to recover. The proposal/auto-apply flow
	// is untouched.
	CmdWriteMemory = "write_memory"
	// Odo DX wave (Run/Test hub): run_command executes one named
	// command from the project's .odo/commands.json via sh -c at the
	// project root (user-authored config — same trust class as
	// .odo-verify's verify line, runVerify's posture) with EnrichedEnv,
	// bounded output tails, and a clamped per-command timeout. The
	// outcome JOURNALS as command_result — a run artifact,
	// deliberately unlike the k8s pollers, which journal nothing.
	CmdRunCommand = "run_command"
	// M19 (/loop): loop_ctl is the GUI-only control surface — Mode B
	// design gate (approve_design | amend_design with Request.Text |
	// veto_design), chip buttons (stop | resume with Request.LoopBudget),
	// and the notification receipt (notified with Request.LoopID +
	// Request.Text = terminal kind).
	CmdLoopCtl = "loop_ctl"
	// resolve_heal_conflict closes one journaled heal_conflict (a stranded
	// memory/pins crash-recovery): Resolve overwrites the layer file with
	// the stranded body; Dismiss records the decision without touching
	// files. Both journal heal_resolved (2026-08-26 memory-replay doctrine).
	CmdResolveHealConflict = "resolve_heal_conflict"
	// D9-W3: learning_status returns the daemon's single learning fold —
	// learning_episode rows, memory_audit_flag rows (the first flag
	// surface), and the candidate stage list — for the Memory panel's
	// Learning tab. The GUI renders this payload and never re-folds.
	CmdLearningStatus = "learning_status"
	// D9-W6: learning_action is the daemon's exposure of the human
	// learning actions — Request.Action drop | apply | promote_global
	// with Request.Hash naming the candidate (full hash or unique
	// prefix). The handler rides the same exported cores as
	// `odo learning drop|apply|promote --global` (learning_actions.go):
	// one actuation path, journaled with actor:"human".
	CmdLearningAction = "learning_action"
)

// Request is one command line on the socket.
type Request struct {
	Cmd            string   `json:"cmd"`
	ProjectRoot    string   `json:"project_root,omitempty"`
	ConversationID int64    `json:"conversation_id,omitempty"`
	WorkstreamID   int64    `json:"workstream_id,omitempty"`
	Name           string   `json:"name,omitempty"`
	Text           string   `json:"text,omitempty"`
	Attachments    []string `json:"attachments,omitempty"`
	AfterSeq       int      `json:"after_seq,omitempty"`
	DiffID         int64    `json:"diff_id,omitempty"`
	// Tri-model right sidebar gap: optional custom commit message for
	// accept_diff. When non-empty, overrides the daemon's default
	// "odo: accept diff #N" message so the user can edit before landing.
	CommitMessage string `json:"commit_message,omitempty"`
	Steer         bool   `json:"steer,omitempty"`
	// W6 (ADR-0005): Park journals user_message{park:true} — the durable
	// queued goal — instead of starting a run; steer and park are mutually
	// exclusive (refused pre-journal). GoalSeq selects one parked goal for
	// resume_parked_goal / drop_parked_goal (0 = the queue head).
	Park    bool `json:"park,omitempty"`
	GoalSeq int  `json:"goal_seq,omitempty"`
	// SteerSeq selects one queued steer for drop_queued_steer — the seq of
	// its user_message{steer:true} journal row (the parked GoalSeq shape;
	// int64 because journal seqs leave the process as int64 elsewhere).
	SteerSeq int64     `json:"steer_seq,omitempty"`
	Adapter  string    `json:"adapter,omitempty"`
	Settings *Settings `json:"settings,omitempty"`
	Path     string    `json:"path,omitempty"`  // read_wiki: wiki note path; read_skill/update_skill: skill filename
	Scope    string    `json:"scope,omitempty"` // update_skill: "global" | "project" (M8)
	Epoch    int       `json:"epoch,omitempty"`
	// A1: save_attachment writes a base64-encoded file to .odo/attachments/.
	Data     string         `json:"data,omitempty"`     // save_attachment: base64-encoded file content
	Accepted []MemoryAccept `json:"accepted,omitempty"` // apply_memory: accepted proposals
	// M12: auto_distill_ctl's verb (only "disarm" today); todo_update's op
	// name (add/done/strike/reopen/reword).
	Action string `json:"action,omitempty"`
	// M12 (D-todo): todo_update's item id (daemon-assigned t<N>) for
	// done/strike/reopen/reword.
	TodoID string `json:"todo_id,omitempty"`
	// R-W4 (design_moa): Goal is the design question; ContextFiles are
	// repo-root-relative paths inlined into every leg's prompt (capped).
	Goal         string   `json:"goal,omitempty"`
	ContextFiles []string `json:"context_files,omitempty"`
	// M19 (loop_ctl): LoopID selects the loop for the notified receipt;
	// LoopBudget carries resume's optional budget raise.
	LoopID     int64 `json:"loop_id,omitempty"`
	LoopBudget int64 `json:"loop_budget,omitempty"`
	// resolve_heal_conflict: the journaled heal_conflict's layer and its
	// stranded receipt's per-conversation journal seq; Dismissed selects the
	// resolve-without-write decision. StrandedConversation is the row's
	// identity half — the receipt's owning conversation: heal rows may ride
	// a different carrier after active-conversation rotation, so lookup and
	// routing run on it, never on the request's conversation.
	Layer                string `json:"layer,omitempty"`
	ReceiptSeq           int    `json:"receipt_seq,omitempty"`
	Dismissed            bool   `json:"dismissed,omitempty"`
	StrandedConversation int64  `json:"stranded_conversation,omitempty"`
	// learning_action (D9-W6): the candidate artifact hash or its unique
	// prefix.
	Hash string `json:"hash,omitempty"`
	// write_memory (Odo DX wave): File is the daemon-owned layer name
	// ("memory.md" | "pins.md" — nothing else passes validation);
	// Content is the full replacement body.
	File    string `json:"file,omitempty"`
	Content string `json:"content,omitempty"`
}

// AutoDistillInfo is one scheduled auto-distill for the pending_counts
// response: the countdown chip renders EtaUnix against the client clock.
// (Pre-M17 this also carried coverage-honesty blocks; over-cap windows now
// fold with a declared omission instead of blocking — F1.)
type AutoDistillInfo struct {
	ConversationID int64  `json:"conversation_id"`
	EtaUnix        int64  `json:"eta_unix"`
	Trigger        string `json:"trigger"`
}

// MemoryAccept references one proposal out of a pending memory_propose batch:
// the proposal's target plus its index in the batch's proposals array.
type MemoryAccept struct {
	Target string `json:"target"` // "memory.md" | "user.md" | "skills"
	Index  int    `json:"index"`
}

// MemoryProposal is one learner-proposed behavior rule after daemon-side
// evidence vetting. Projects carries the daemon-verified project names whose
// staged inputs contained the rule (user.md target only) — never the LLM's
// self-tagged list.
//
// M9: when Target is "skills", Rule holds the full composed SKILL.md content
// (frontmatter + body) and Name is the vetted kebab-case skill name.
//
// Panel-gated apply: Reviews carries the panel verdicts for every gated
// proposal (all targets when the prefs `review:` panel is configured; nil
// when the gate is inert, and nil for auto_discard skills, which never
// reach the memory_propose batch).
type MemoryProposal struct {
	Target      string         `json:"target"`         // "memory.md" | "user.md" | "skills"
	Rule        string         `json:"rule"`           // imperative rule OR full SKILL.md content (skills target)
	Name        string         `json:"name,omitempty"` // M9: vetted skill name (skills target only)
	Evidence    string         `json:"evidence,omitempty"`
	Contradicts string         `json:"contradicts,omitempty"` // memory: contradicts existing rule; skills: "overwrites existing skill: <name>"
	Projects    []string       `json:"projects,omitempty"`
	Reviews     []ReviewResult `json:"reviews,omitempty"` // panel gate verdicts
	// D4 (2026-08-28, ruling ④ Sol hybrid): Intent "retract" marks a
	// deletion-class proposal the learner built from a rules-audit flag:
	// Rule is the daemon-filled flagged rule text (journal truth, never
	// the LLM's citation), FlagSeq the memory_audit_flag row it cites.
	// Accepted retract intents are NEVER applied — the apply core journals
	// memory_update{layer:"memory", cause:"retract_candidate"} instead and
	// a human resolves (apply_memory contradicts / `odo rules retract`).
	// Additive keys: legacy batches carry neither.
	Intent  string `json:"intent,omitempty"`   // "" | "retract"
	FlagSeq int    `json:"flag_seq,omitempty"` // memory_audit_flag seq (retract intent)
}

// ReviewResult is one model's verdict on a diff (MoA review fan-out).
type ReviewResult struct {
	Model    string `json:"model"`
	Verdict  string `json:"verdict"` // "accept" | "reject" | "needs_fixes"
	Comments string `json:"comments"`
	// Infra marks a leg that failed on transport/auth/timeout (M18): the
	// model never issued a verdict. consensusVerdict still reads the
	// Verdict field (unchanged semantics); the settlement ladder consults
	// Infra to fail the round closed as panel_infra instead of mistaking
	// an error string for dissent.
	Infra bool `json:"infra,omitempty"`
	// Truncated marks a leg whose response was cut at the output token
	// cap (reviewVerdict degrades it to needs_fixes). The majority-accept
	// valve (Fix 1) vetoes truncated legs: a truncated review could have
	// ended in reject — outvoting it is fail-open.
	Truncated bool `json:"truncated,omitempty"`
	// ThinkingMD (M18 batch B) journals the leg's reasoning text, capped
	// at 4KB, ONLY for non-accept verdicts (accept legs stay unjournaled —
	// journal noise discipline). Real data: the moa client's thinking
	// blocks when present, otherwise the leg's full response text (the
	// documented approximation — the direct API exposes no separate
	// reasoning channel for those models).
	ThinkingMD string `json:"thinking_md,omitempty"`
	// BaseURL (M18 batch B, provider honesty) records the endpoint the
	// leg actually hit, userinfo-scrubbed. Prefs declare model@provider
	// labels, but the direct-API path routes every leg through the one
	// moa gateway — the journal must say where the leg truly went, not
	// what the label implies.
	BaseURL string `json:"base_url,omitempty"`
	// RequestSHA16 / RequestBytes (R-W1.5) are the moa client's wire
	// receipt: sha16 of the exact request body whose verdict shipped, and
	// its length. Absent on infra legs — no answer shipped, nothing to
	// attest (patch_sha16 covers the judged content; this pair covers the
	// assembled request).
	RequestSHA16 string `json:"request_sha16,omitempty"`
	RequestBytes int    `json:"request_bytes,omitempty"`

	// --- D2 grounded-leg receipts (additive; all absent on ungrounded
	// legs) ---
	// Grounded marks the fan-out's one grounded leg: the leg got
	// read-only repo tools scoped to the diff and its one-hop import
	// neighborhood (grounded.go). Its verdict weighs exactly like every
	// other leg this wave — D2 grants no extra authority.
	Grounded bool `json:"grounded,omitempty"`
	// ResolvedBy records the grounded-model resolution: "prefs" (the
	// grounded_reviewer: line named a model on the fan-out's line) or
	// "first" (absent/unmatched ⇒ the line's first entry).
	ResolvedBy string `json:"resolved_by,omitempty"`
	// ToolCalls is the executed tool audit (cap groundedToolCallsCap,
	// truncated flag set when more executed). Model-visible ⟺ logged
	// holds for refusals too: an out-of-scope read rides here with its
	// Error set.
	ToolCalls          []moa.ToolAudit `json:"tool_calls,omitempty"`
	ToolCallsTruncated bool            `json:"tool_calls_truncated,omitempty"`
	// ToolRoundsUsed (D9-C) is the executed tool-call count BEFORE the
	// journal cap truncated ToolCalls — journaled on every grounded row,
	// not just round-cap deaths, so a post-mortem reads the loop's true
	// spend and distinguishes linear progress from degenerate re-reads.
	ToolRoundsUsed int `json:"tool_rounds_used,omitempty"`
	// ReadBytes is the total tool-result bytes the leg was served (the
	// groundedTotalBytes budget's spend).
	ReadBytes int `json:"read_bytes,omitempty"`
	// ScopeSHA16/ScopeFiles identify the computed allowlist (sorted
	// file+dir entries); ScopeTruncated marks a scope computation that
	// degraded to touched-only-plus-same-dir (a skipped import
	// neighborhood is fail-visible, never silent).
	ScopeSHA16     string `json:"scope_sha16,omitempty"`
	ScopeFiles     int    `json:"scope_files,omitempty"`
	ScopeTruncated bool   `json:"scope_truncated,omitempty"`
	// ToolBudgetExhausted marks a leg whose grounded read budget tripped
	// (the leg still owed a verdict; a missing verdict token degrades
	// fail-closed as usual).
	ToolBudgetExhausted bool `json:"tool_budget_exhausted,omitempty"`
}

// DesignProposal is one blind-sealed leg's outcome (R-W4 design_moa).
// Shape mirrors PanelResult (E1 audit: every executed tool call rides the
// row) plus the R-W1.5 wire receipt. A failed leg — transport/auth/timeout
// or a truncated final answer (strict truncation: the partial never feeds
// the consolidator) — keeps its receipts, drops its text, and marks Error
// (+ Truncated when the cause was the output cap).
type DesignProposal struct {
	Model        string           `json:"model"`
	Text         string           `json:"text,omitempty"`
	Error        string           `json:"error,omitempty"`
	Truncated    bool             `json:"truncated,omitempty"`
	ToolCalls    []moa.ToolAudit  `json:"tool_calls,omitempty"`
	Budget       int              `json:"budget,omitempty"`
	OutputTokens int              `json:"output_tokens,omitempty"`
	Escalations  []moa.Escalation `json:"escalations,omitempty"`
	// R-W1.5: sha16 + length of the exact request body whose answer
	// shipped (the final tool-loop round). Present even on failed legs
	// when a body shipped.
	RequestSHA16 string `json:"request_sha16,omitempty"`
	RequestBytes int    `json:"request_bytes,omitempty"`
	// D6 (design-MoA diversity gate): the endpoint the leg truly hit
	// (scrubBaseURL — credential material stripped; when every leg shares
	// one gateway the journal must say so) and the model's vendor family
	// (modelspec.Family — label diversity is NOT model diversity: the
	// same model under two provider labels is ONE opinion).
	Endpoint    string `json:"endpoint,omitempty"`
	ModelFamily string `json:"model_family,omitempty"`
}

// DiffInfo carries a diff record plus its file content to the client.
type DiffInfo struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// DiffInfoEx extends DiffInfo with the owning workstream label for the
// cross-workstream review inbox (P1a). JSON flattens the embedding, so the
// wire shape is {id, status, path, content, conversation_id, workstream_id,
// workstream_name}.
type DiffInfoEx struct {
	DiffInfo
	ConversationID int64  `json:"conversation_id"`
	WorkstreamID   int64  `json:"workstream_id"`
	WorkstreamName string `json:"workstream_name"`
}

// WikiNoteInfo describes one distilled wiki note for the browser list.
type WikiNoteInfo struct {
	Path       string `json:"path"`
	Name       string `json:"name"` // e.g. "main-epoch-1"
	Epoch      int    `json:"epoch"`
	ModifiedAt string `json:"modified_at"`
}

// PanelProgress is the live fan-out tally of an in-flight /panel consult:
// Done model legs answered of the current Total batch. The daemon keeps it
// in memory only (the previewEvent precedent — never journaled) and hands
// a COPY to every poll_events response so the GUI's spinner row can show
// progress during multi-minute consults; absent when no panel is in flight
// for the polled conversation.
type PanelProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
	// Per-leg detail registered at fan-out (one row per model, in prefs
	// order): the GUI's status card shows who is still out, who is back,
	// and who errored instead of a bare N/M tally. Append-only for the
	// entry's lifetime; concurrent panels on one conversation append to
	// the shared entry.
	Legs []PanelLeg `json:"legs,omitempty"`
}

// PanelLeg is one model's slot in a /panel fan-out. Done flips when the
// leg answers — answer or error (Error distinguishes those); Total/Done
// on PanelProgress stay the summary of the same transitions.
type PanelLeg struct {
	Model string `json:"model"`
	Done  bool   `json:"done"`
	Error bool   `json:"error,omitempty"`
}

// Response is one result line on the socket. Fields are present only when
// relevant to the command (see docs/milestones for the shapes).
type Response struct {
	OK           bool                `json:"ok"`
	Error        string              `json:"error,omitempty"`
	Project      *store.Project      `json:"project,omitempty"`
	Workstream   *store.Workstream   `json:"workstream,omitempty"`
	Workstreams  []store.Workstream  `json:"workstreams,omitempty"`
	Conversation *store.Conversation `json:"conversation,omitempty"`
	Event        *store.Event        `json:"event,omitempty"`
	Events       []store.Event       `json:"events,omitempty"`
	AgentRunning *bool               `json:"agent_running,omitempty"`
	// M7 live streaming: the active run's transient in-flight block preview
	// (partial:true; never journaled — rebuilt on every poll). Streaming is
	// true while a preview is present.
	Preview   *adapter.AgentEvent `json:"preview,omitempty"`
	Streaming bool                `json:"streaming,omitempty"`
	// poll_events: live /panel leg tally for the polled conversation (a
	// copy of the daemon's in-memory progress — never journaled). Drives
	// the GUI spinner row's N/M counter during multi-minute consults.
	PanelProgress *PanelProgress `json:"panel_progress,omitempty"`
	Diff          *DiffInfo      `json:"diff,omitempty"`
	Diffs         []DiffInfo     `json:"diffs,omitempty"`
	DiffID        int64          `json:"diff_id,omitempty"`
	Applied       bool           `json:"applied,omitempty"`
	WikiPath      string         `json:"wiki_path,omitempty"`
	Epoch         int            `json:"epoch,omitempty"`
	Reviews       []ReviewResult `json:"reviews,omitempty"`
	Consensus     string         `json:"consensus,omitempty"` // A4-lite+v2: deterministic tally — accept requires unanimity
	Settings      *Settings      `json:"settings,omitempty"`
	WikiNotes     []WikiNoteInfo `json:"wiki_notes,omitempty"`
	WikiContent   string         `json:"wiki_content,omitempty"`
	// read_memory: contents of the daemon-constructed canonical files
	// (missing files come back as "").
	MemoryContent  string `json:"memory_content,omitempty"`
	ArchiveContent string `json:"archive_content,omitempty"`
	UserContent    string `json:"user_content,omitempty"`
	// memory_proposals: the latest epoch's batch (absent/epoch 0 = no batch
	// at all). Consumed false = pending and actionable; true = decided —
	// ApplyActor names who decided ("auto_panel" or a human apply, absent
	// on pre-panel rows), Accepted/Rejected carry the daemon-computed refs.
	Seq        int              `json:"seq,omitempty"`
	Proposals  []MemoryProposal `json:"proposals,omitempty"`
	Reaffirm   []string         `json:"reaffirm,omitempty"`
	Consumed   bool             `json:"consumed,omitempty"`
	ApplyActor string           `json:"apply_actor,omitempty"`
	Accepted   []MemoryAccept   `json:"accepted,omitempty"`
	Rejected   []MemoryAccept   `json:"rejected,omitempty"`
	// distill: count of pending memory+user proposals in the new batch.
	MemoryProposals int `json:"memory_proposals,omitempty"`
	// pending_counts: Go map keys serialize as JSON strings — the key encoding is the frontend contract.
	PendingCounts      map[int64]int `json:"pending_counts,omitempty"`
	RunningWorkstreams []int64       `json:"running_workstreams,omitempty"`
	// P1a (review inbox): list_all_pending_diffs payload — pending diffs
	// across all active workstreams with workstream labels.
	AllPendingDiffs []DiffInfoEx `json:"all_pending_diffs,omitempty"`
	// W6 (goal queue): Parked is the conversation's parked-goal queue depth
	// after a park/resume/drop command; ParkedGoals rides pending_counts
	// with the per-workstream queue depth, keyed like PendingCounts.
	Parked      int           `json:"parked,omitempty"`
	ParkedGoals map[int64]int `json:"parked_goals,omitempty"`
	// M12 (D-auto): pending_counts also reports scheduled auto-distills
	// (countdown chip) and in-flight distills (manual or auto — the GUI
	// locks the composer for manual, badges for auto).
	AutoDistill     []AutoDistillInfo `json:"auto_distill,omitempty"`
	Distilling      bool              `json:"distilling,omitempty"`
	DistillingConvs []int64           `json:"distilling_convs,omitempty"`
	// pending_counts: unresolved heal_conflict rows across the whole project
	// (heal_conflict minus heal_resolved, folded by content key) — the
	// Memory tab's "N stranded crash-recoveries" banner count.
	StrandedMemoryOps int `json:"stranded_memory_ops,omitempty"`
	// pending_counts: the counted rows themselves, project-wide (round-3
	// FIX F) — the count is project-wide, so the actionable rows must be
	// too: a conflict riding a rotated/foreign lane rendered "N stranded"
	// with zero rows to act on. The GUI folds THIS list, and routes each
	// resolve by the row's owning conversation.
	StrandedOps []StrandedOp `json:"stranded_ops,omitempty"`
	// pending_counts: the auto-distill daily-cap suspension disclosure —
	// the Memory tab's "今日额度已用完 · 预计恢复" chip. Nil while the cap
	// is un-hit, the horizon has passed, or auto-distill is disabled (the
	// chip never outlives either). Project-scoped like the cap itself.
	AutoDistillCapResume *AutoCapResumeInfo `json:"auto_distill_cap_resume,omitempty"`
	// auto_distill_ctl: whether a scheduled auto-distill was disarmed.
	Disarmed bool `json:"disarmed,omitempty"`
	// search_events: cross-conversation search results.
	SearchResults []store.SearchResult `json:"search_results,omitempty"`
	// M8 (Skills): list_skills returns all discovered skill metadata;
	// read_skill returns the full markdown body of one skill.
	Skills       []SkillInfo `json:"skills,omitempty"`
	SkillContent string      `json:"skill_content,omitempty"`
	// A1: save_attachment returns the absolute path of the written file.
	Path string `json:"path,omitempty"`
	// autonomy_status: the rung-0 observability snapshot (M15 O-1).
	Autonomy *AutonomyReport `json:"autonomy,omitempty"`
	// learning_status: the D9-W3 learning fold (episodes + audit flags +
	// candidate stages).
	Learning *LearningStatusReport `json:"learning,omitempty"`
	// learning_action: the D9-W6 human-action outcome (drop / apply /
	// promote_global).
	LearningAction *LearningActionResult `json:"learning_action,omitempty"`
	// R-W4 (design_moa): the consolidated DESIGN LOCK document plus every
	// leg's proposal/metadata ("proposals" is already the memory_proposals
	// wire key, hence design_proposals).
	DesignLock      string           `json:"design_lock,omitempty"`
	DesignProposals []DesignProposal `json:"design_proposals,omitempty"`
	// P2 (OMP stats): merged omp usage + grievances JSON for the
	// StatusBar's read-only stats chip. Raw JSON passthrough — the
	// daemon does not parse or journal this data.
	OmpUsage json.RawMessage `json:"omp_usage,omitempty"`
	// UX-2 (D5 Stage 0): the StatusBar Jobs chip's k8s snapshot. Jobs/Pods
	// are the kubectl `get jobs,pods -o json` item slices (swap-friendly
	// passthrough — the daemon splits kinds and caps, never interprets).
	K8sStatus *K8sStatus `json:"k8s_status,omitempty"`
	// D5b (A2-4): the batch progress bridge — status.json snapshots read
	// local-first (CPFS mount glob), kubectl exec cat fallback. NEVER
	// journaled, same containment as k8s_status.
	K8sBatchStatus *K8sBatchStatus `json:"k8s_batch_status,omitempty"`
	// read_file: inline file preview (tri-model right sidebar gap). Content
	// capped at readFileMaxBytes; truncated=true when the cap was hit.
	// Binary files return an error; req.Path/journal are untouched (the
	// same containment rule as open_path: canonicalize-then-prefix-check).
	FileContent   string `json:"file_content,omitempty"`
	FileResolved  string `json:"file_resolved,omitempty"`
	FileTruncated bool   `json:"file_truncated,omitempty"`
	// run_command (Odo DX wave): the executed command's outcome — the
	// same fields as the journaled command_result payload, so the Runs
	// tab badge flips on the invoke response without waiting on a poll.
	CommandResult *CommandResult `json:"command_result,omitempty"`
}

// CommandResult is run_command's outcome (Odo DX wave): exit code, the
// bounded output tails (commandTailCap each, bounded at capture), wall
// time, and the timeout flag — a timed-out exec reports exit_code -1
// with TimedOut true; the badge reds on either.
type CommandResult struct {
	Name       string `json:"name"`
	ExitCode   int    `json:"exit_code"`
	StdoutTail string `json:"stdout_tail,omitempty"`
	StderrTail string `json:"stderr_tail,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out,omitempty"`
}

// K8sNsStatus is one configured namespace's outcome row (A4 D3): the
// popover renders one row per CONFIGURED namespace in configured order.
// A failed namespace degrades HERE — OK stays false and Reason/Detail
// carry the cause (timeout/auth/unreachable classes, same k8sClassify
// vocabulary as the whole-response Reason); the whole chip never gains a
// third "partial" state (partial availability = healthy chip + degraded
// per-ns rows).
type K8sNsStatus struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	// Detail is THIS leg's capped stderr tail (k8sStderrCap at capture).
	Detail string `json:"detail,omitempty"`
	// JobCount counts this namespace's jobs AFTER the row cap — the
	// "ns · N jobs" popover header reads it without re-deriving.
	JobCount int `json:"job_count"`
}

// K8sStatus is the k8s_status payload (UX-2 / A2-1 + A4 multi-ns).
// Reason carries the cause class when Available is false: "off" (feature
// disabled), "ENOENT" (kubectl missing), "timeout", "auth",
// "unreachable", "bad_namespace" — the last one covers BOTH a rejected
// namespace element and an over-cap list (N > k8sMaxNamespaces); the
// Detail names the offending element(s) or the cap. Data may be absent,
// the reason may never be absent (A2-1, verbatim).
type K8sStatus struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Detail is the diagnosis behind a non-off Reason — kubectl's capped
	// stderr tail for exec-shaped failures, the parse diagnostic for
	// pref-shaped ones (bad_namespace). Bounded at capture (k8sStderrCap
	// via a LimitReader pipe); absent pre-exec (off/ENOENT exec nothing).
	Detail string `json:"detail,omitempty"`
	// Raw kubectl list-item slices (kind Job/Pod) FLAT-MERGED across the
	// answering namespaces (kubectl items carry metadata.namespace — the
	// GUI groups by it). Truncated marks the 50-row PER-NS job cap
	// tripping on ANY answering namespace (OR'd; only reachable with an
	// empty k8s_job_selector pref).
	Jobs      json.RawMessage `json:"jobs,omitempty"`
	Pods      json.RawMessage `json:"pods,omitempty"`
	Truncated bool            `json:"truncated"`
	// Namespaces carries one row per CONFIGURED namespace, in configured
	// order — present whenever the pref parses to a non-empty list,
	// including on total failure (Available:false), so the popover can
	// render every namespace's state without guessing.
	Namespaces  []K8sNsStatus `json:"namespaces,omitempty"`
	FetchedUnix int64         `json:"fetched_unix"`
}

// K8sBatchStatus is the k8s_batch_status payload (D5b / A2-4). The read
// is LOCAL-FIRST: every *.json directly under k8s_batch_dir (the CPFS
// mount on the Mac). Only when the dir read fails AND k8s is configured
// does the kubectl exec fallback fire — resolving the pod per-read via
// the k8s_job_selector labels (never a stored pod name) and `cat`-ing the
// canonical status.json under the dir (the ONLY whitelisted exec verb).
// Reason classes: "off" (k8s_batch_dir empty), "ENOENT" (kubectl missing
// for the fallback), "local_missing" (configured dir unreadable and no
// fallback source available). Rows carry their own Reason for per-file
// degradations ("schema_mismatch", "pod_not_found", "ambiguous_pod",
// "no_pod_selector") — the degradation contract at file granularity: a
// row may be missing its data, its reason may never be absent.
type K8sBatchStatus struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
	// Batches sorts newest-first by UpdatedUnix, capped at
	// k8sBatchRowCap (25 — a runaway glob must not flood the GUI);
	// Truncated declares the cap was hit, never a silent drop.
	Batches   []K8sBatch `json:"batches,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// K8sBatch is one status.json row — the schema pinned in
// docs/design/d5b-batch-status.md. The daemon validates schema_version==1
// and computes Stale from the heartbeat (updated_unix older than
// k8sBatchStaleSeconds), shipping BOTH the raw stamp and the derived flag
// so the GUI can render "stale 2m" without re-deriving the threshold.
// Reason (only while set) marks rows the daemon could fill with NO data.
type K8sBatch struct {
	Batch       string  `json:"batch"`
	Stage       string  `json:"stage,omitempty"`
	Total       int     `json:"total"`
	Done        int     `json:"done"`
	Errs        int     `json:"errs"`
	RatePerMin  float64 `json:"rate_per_min"`
	UpdatedUnix int64   `json:"updated_unix"`
	Status      string  `json:"status"` // running | done | failed
	Stale       bool    `json:"stale"`
	Reason      string  `json:"reason,omitempty"`
}

// AutoCapResumeInfo discloses an auto-distill daily-cap suspension through
// pending_counts (the Memory tab's chip). ResumeAtUnix is the earliest
// quota release, rendered against the client clock; Computed marks the
// upgrade fallback — a journal that predates cap_suspended_until rows gets
// oldest-counted-distill + 24h instead of the row's hardened horizon.
type AutoCapResumeInfo struct {
	ResumeAtUnix int64 `json:"resume_at_unix"`
	Computed     bool  `json:"computed,omitempty"`
}

// StrandedOp is one OPEN heal_conflict row as pending_counts exposes it
// project-wide (2026-08-26 memory-replay doctrine, round-3 FIX F). The
// identity fields are the resolve request's addressing halves:
// StrandedConversation is the receipt's owning conversation (routing AND
// content-key half — heal rows may ride a rotated carrier).
type StrandedOp struct {
	StrandedConversation int64  `json:"conversation_id"`
	Layer                string `json:"layer"`
	ReceiptSeq           int    `json:"receipt_seq"`
	Detail               string `json:"detail,omitempty"`
}
