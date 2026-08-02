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
)

// Request is one command line on the socket.
type Request struct {
	Cmd            string    `json:"cmd"`
	ProjectRoot    string    `json:"project_root,omitempty"`
	ConversationID int64     `json:"conversation_id,omitempty"`
	WorkstreamID   int64     `json:"workstream_id,omitempty"`
	Name           string    `json:"name,omitempty"`
	Text           string    `json:"text,omitempty"`
	Attachments    []string  `json:"attachments,omitempty"`
	AfterSeq       int       `json:"after_seq,omitempty"`
	DiffID         int64     `json:"diff_id,omitempty"`
	Steer          bool      `json:"steer,omitempty"`
	Adapter        string    `json:"adapter,omitempty"`
	N              int       `json:"n,omitempty"`
	Settings       *Settings `json:"settings,omitempty"`
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
}
