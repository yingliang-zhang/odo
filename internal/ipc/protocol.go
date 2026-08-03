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
	Path           string         `json:"path,omitempty"` // read_wiki: wiki note path
	Epoch          int            `json:"epoch,omitempty"`
	Accepted       []MemoryAccept `json:"accepted,omitempty"` // apply_memory: accepted proposals
}

// MemoryAccept references one proposal out of a pending memory_propose batch:
// the proposal's target plus its index in the batch's proposals array.
type MemoryAccept struct {
	Target string `json:"target"` // "memory.md" | "user.md"
	Index  int    `json:"index"`
}

// MemoryProposal is one learner-proposed behavior rule after daemon-side
// evidence vetting. Projects carries the daemon-verified project names whose
// staged inputs contained the rule (user.md target only) — never the LLM's
// self-tagged list.
type MemoryProposal struct {
	Target      string   `json:"target"` // "memory.md" | "user.md"
	Rule        string   `json:"rule"`
	Evidence    string   `json:"evidence,omitempty"`
	Contradicts string   `json:"contradicts,omitempty"`
	Projects    []string `json:"projects,omitempty"`
}

// ReviewResult is one model's verdict on a diff (MoA review fan-out).
type ReviewResult struct {
	Model    string `json:"model"`
	Verdict  string `json:"verdict"` // "accept" | "reject" | "needs_fixes"
	Comments string `json:"comments"`
}

// RunInfo reports the status of one fan-out run.
type RunInfo struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"` // "running" | "done" | "error"
}

// DiffInfo carries a diff record plus its file content to the client.
type DiffInfo struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	Path    string `json:"path"`
	Content string `json:"content"`
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
	Diff         *DiffInfo           `json:"diff,omitempty"`
	DiffID       int64               `json:"diff_id,omitempty"`
	Applied      bool                `json:"applied,omitempty"`
	WikiPath     string              `json:"wiki_path,omitempty"`
	Epoch        int                 `json:"epoch,omitempty"`
	Reviews      []ReviewResult      `json:"reviews,omitempty"`
	Runs         []RunInfo           `json:"runs,omitempty"`
	Settings     *Settings           `json:"settings,omitempty"`
	WikiNotes    []WikiNoteInfo      `json:"wiki_notes,omitempty"`
	WikiContent  string              `json:"wiki_content,omitempty"`
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
}
