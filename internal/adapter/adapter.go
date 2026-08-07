// Package adapter defines the 5-verb agent adapter contract. The OMP
// implementation is the production adapter; the interface exists so test
// stubs drop in without touching the daemon.
package adapter

import "context"

// AgentEvent is one event emitted by a running or finished agent.
// Type is one of the store's agent event types (agent_text, agent_tool_call,
// agent_tool_result, agent_done, agent_error).
type AgentEvent struct {
	Type    string                 `json:"type"`
	Payload map[string]interface{} `json:"payload"`
}

// Adapter is the contract between the daemon and a coding agent runner.
type Adapter interface {
	// Start launches an agent run in workdir with the given prompt and
	// returns an opaque run ID. It must not block for the run's completion.
	// ctx covers spawn only; the run's lifetime is managed via Cancel/Close.
	Start(ctx context.Context, workdir string, prompt string) (runID string, err error)

	// Send steers a running agent with a follow-up message. (M1+; M0
	// implementations may return an error.)
	Send(ctx context.Context, runID string, message string) error

	// Events returns agent events after the adapter-local index afterSeq
	// (0-based count of events already consumed). While the run is in
	// progress it returns an empty slice.
	Events(ctx context.Context, runID string, afterSeq int) ([]AgentEvent, error)

	// Cancel kills the run's process group. Safe to call on a finished run.
	Cancel(ctx context.Context, runID string) error

	// Close releases all run state. It does NOT delete the worktree — that
	// persists until the user accepts or rejects the diff.
	Close(ctx context.Context, runID string) error
}
