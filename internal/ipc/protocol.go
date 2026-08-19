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
	// read_file: inline file preview (tri-model right sidebar gap). Reads a
	// project-contained text file with the same containment rule as the
	// GUI's open_path (canonicalize-then-prefix-check); binary files and
	// escape attempts are rejected. Capped at readFileMaxBytes.
	CmdReadFile = "read_file"
	// M19 (/loop): loop_ctl is the GUI-only control surface — Mode B
	// design gate (approve_design | amend_design with Request.Text |
	// veto_design), chip buttons (stop | resume with Request.LoopBudget),
	// and the notification receipt (notified with Request.LoopID +
	// Request.Text = terminal kind).
	CmdLoopCtl = "loop_ctl"
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
	Park     bool      `json:"park,omitempty"`
	GoalSeq  int       `json:"goal_seq,omitempty"`
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
// (frontmatter + body), Name is the vetted kebab-case skill name, and Reviews
// carries the tri-model gate verdicts (nil for auto_discard proposals, which
// are never included in the memory_propose batch).
type MemoryProposal struct {
	Target      string         `json:"target"`         // "memory.md" | "user.md" | "skills"
	Rule        string         `json:"rule"`           // imperative rule OR full SKILL.md content (skills target)
	Name        string         `json:"name,omitempty"` // M9: vetted skill name (skills target only)
	Evidence    string         `json:"evidence,omitempty"`
	Contradicts string         `json:"contradicts,omitempty"` // memory: contradicts existing rule; skills: "overwrites existing skill: <name>"
	Projects    []string       `json:"projects,omitempty"`
	Reviews     []ReviewResult `json:"reviews,omitempty"` // M9: tri-model gate verdicts (skills target only)
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
	Preview     *adapter.AgentEvent `json:"preview,omitempty"`
	Streaming   bool                `json:"streaming,omitempty"`
	Diff        *DiffInfo           `json:"diff,omitempty"`
	Diffs       []DiffInfo          `json:"diffs,omitempty"`
	DiffID      int64               `json:"diff_id,omitempty"`
	Applied     bool                `json:"applied,omitempty"`
	WikiPath    string              `json:"wiki_path,omitempty"`
	Epoch       int                 `json:"epoch,omitempty"`
	Reviews     []ReviewResult      `json:"reviews,omitempty"`
	Consensus   string              `json:"consensus,omitempty"` // A4-lite+v2: deterministic tally — accept requires unanimity
	Settings    *Settings           `json:"settings,omitempty"`
	WikiNotes   []WikiNoteInfo      `json:"wiki_notes,omitempty"`
	WikiContent string              `json:"wiki_content,omitempty"`
	// read_memory: contents of the daemon-constructed canonical files
	// (missing files come back as "").
	MemoryContent  string `json:"memory_content,omitempty"`
	ArchiveContent string `json:"archive_content,omitempty"`
	UserContent    string `json:"user_content,omitempty"`
	// memory_proposals: the pending batch (absent/epoch 0 = nothing pending).
	Seq       int              `json:"seq,omitempty"`
	Proposals []MemoryProposal `json:"proposals,omitempty"`
	Reaffirm  []string         `json:"reaffirm,omitempty"`
	// distill: count of pending memory+user proposals in the new batch.
	MemoryProposals int `json:"memory_proposals,omitempty"`
	// pending_counts: Go map keys serialize as JSON strings — that is the
	// contract the frontend implements.
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
	// R-W4 (design_moa): the consolidated DESIGN LOCK document plus every
	// leg's proposal/metadata ("proposals" is already the memory_proposals
	// wire key, hence design_proposals).
	DesignLock      string           `json:"design_lock,omitempty"`
	DesignProposals []DesignProposal `json:"design_proposals,omitempty"`
	// P2 (OMP stats): merged omp usage + grievances JSON for the
	// StatusBar's read-only stats chip. Raw JSON passthrough — the
	// daemon does not parse or journal this data.
	OmpUsage json.RawMessage `json:"omp_usage,omitempty"`
	// read_file: inline file preview (tri-model right sidebar gap). Content
	// capped at readFileMaxBytes; truncated=true when the cap was hit.
	// Binary files return an error; req.Path/journal are untouched (the
	// same containment rule as open_path: canonicalize-then-prefix-check).
	FileContent   string `json:"file_content,omitempty"`
	FileResolved  string `json:"file_resolved,omitempty"`
	FileTruncated bool   `json:"file_truncated,omitempty"`
}
