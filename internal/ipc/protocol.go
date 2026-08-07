// Package ipc implements the daemon's Unix-socket API: line-delimited JSON
// requests and responses. One connection at a time (M0).
package ipc

import (
	"github.com/yingliang-zhang/odo/internal/adapter"
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
	CmdFanoutSend       = "fanout_send"
	CmdPendingCounts    = "pending_counts"
	CmdListWiki         = "list_wiki"
	CmdReadWiki         = "read_wiki"
	CmdReadMemory       = "read_memory"
	CmdMemoryProposals  = "memory_proposals"
	CmdApplyMemory      = "apply_memory"
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
	CmdSearchEvents    = "search_events"
	// M8 (Skills): skills are plain markdown files in ~/.odo/skills/ and
	// .odo/skills/. list_skills returns metadata; read_skill returns the
	// full body; update_skill writes (creates or overwrites) a skill file.
	CmdListSkills  = "list_skills"
	CmdReadSkill   = "read_skill"
	CmdUpdateSkill = "update_skill"
	CmdDeleteSkill = "delete_skill"
)

// Request is one command line on the socket.
type Request struct {
	Cmd            string         `json:"cmd"`
	ProjectRoot    string         `json:"project_root,omitempty"`
	ConversationID int64          `json:"conversation_id,omitempty"`
	WorkstreamID   int64          `json:"workstream_id,omitempty"`
	Name           string         `json:"name,omitempty"`
	Text           string         `json:"text,omitempty"`
	Attachments    []string       `json:"attachments,omitempty"`
	AfterSeq       int            `json:"after_seq,omitempty"`
	DiffID         int64          `json:"diff_id,omitempty"`
	Steer          bool           `json:"steer,omitempty"`
	Adapter        string         `json:"adapter,omitempty"`
	N              int            `json:"n,omitempty"`
	Settings       *Settings      `json:"settings,omitempty"`
	Path           string         `json:"path,omitempty"` // read_wiki: wiki note path; read_skill/update_skill: skill filename
	Scope          string         `json:"scope,omitempty"` // update_skill: "global" | "project" (M8)
	Epoch          int            `json:"epoch,omitempty"`
	Accepted       []MemoryAccept `json:"accepted,omitempty"` // apply_memory: accepted proposals
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
	Target      string         `json:"target"`           // "memory.md" | "user.md" | "skills"
	Rule        string         `json:"rule"`             // imperative rule OR full SKILL.md content (skills target)
	Name        string         `json:"name,omitempty"`   // M9: vetted skill name (skills target only)
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
}

// RunInfo reports the status of one fan-out run. RunID is the daemon's
// runDirID (not the adapter's internal ID), so it joins events → worktree →
// diff. Index is the 0-based batch ordinal for display ("Run 1", "Run 2").
// DiffID is non-nil when the run has produced a pending diff. Preview carries
// the run's M7 streaming preview (partial:true, never journaled).
type RunInfo struct {
	RunID   string              `json:"run_id"`
	Status  string              `json:"status"` // "running" | "done" | "error"
	Index   int                 `json:"index"`
	DiffID  *int64              `json:"diff_id,omitempty"`
	Preview *adapter.AgentEvent `json:"preview,omitempty"`
}

// DiffInfo carries a diff record plus its file content to the client.
// RunID/RunIndex are set when the diff was produced by a fan-out run,
// allowing the frontend to associate a diff card with its lane. RunIndex
// is a pointer (not int) so that index 0 ("Run 1") is not dropped by
// omitempty — the most common lane.
type DiffInfo struct {
	ID       int64  `json:"id"`
	Status   string `json:"status"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	RunID    string `json:"run_id,omitempty"`
	RunIndex *int   `json:"run_index,omitempty"`
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
	Runs        []RunInfo           `json:"runs,omitempty"`
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
	PendingCounts      map[int64]int             `json:"pending_counts,omitempty"`
	RunningWorkstreams []int64                   `json:"running_workstreams,omitempty"`
	// search_events: cross-conversation search results.
	SearchResults []store.SearchResult           `json:"search_results,omitempty"`
	// M8 (Skills): list_skills returns all discovered skill metadata;
	// read_skill returns the full markdown body of one skill.
	Skills       []SkillInfo `json:"skills,omitempty"`
	SkillContent string      `json:"skill_content,omitempty"`
}
