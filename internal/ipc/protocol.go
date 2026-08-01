// Package ipc implements the daemon's Unix-socket API: line-delimited JSON
// requests and responses. One connection at a time (M0).
package ipc

import (
	"github.com/yingliang-zhang/odo/internal/store"
)

// Commands.
const (
	CmdBootstrap   = "bootstrap"
	CmdSendMessage = "send_message"
	CmdPollEvents  = "poll_events"
	CmdAcceptDiff  = "accept_diff"
	CmdRejectDiff  = "reject_diff"
)

// Request is one command line on the socket.
type Request struct {
	Cmd            string   `json:"cmd"`
	ProjectRoot    string   `json:"project_root,omitempty"`
	ConversationID int64    `json:"conversation_id,omitempty"`
	Text           string   `json:"text,omitempty"`
	Attachments    []string `json:"attachments,omitempty"`
	AfterSeq       int      `json:"after_seq,omitempty"`
	DiffID         int64    `json:"diff_id,omitempty"`
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
	Conversation *store.Conversation `json:"conversation,omitempty"`
	Event        *store.Event        `json:"event,omitempty"`
	Events       []store.Event       `json:"events,omitempty"`
	AgentRunning *bool               `json:"agent_running,omitempty"`
	Diff         *DiffInfo           `json:"diff,omitempty"`
	DiffID       int64               `json:"diff_id,omitempty"`
	Applied      bool                `json:"applied,omitempty"`
}
