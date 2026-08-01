package ipc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

// runMeta tracks one in-flight (or recently finished) agent run in memory.
// Run bookkeeping is intentionally NOT journaled: after a daemon restart no
// agent process is alive, and pending diffs are reviewed from the journal +
// diff file alone.
type runMeta struct {
	runID          string // adapter-assigned ID
	runDirID       string // daemon-generated ID naming worktree + diff file
	conversationID int64
	workstreamID   int64
	worktreePath   string
	consumed       int  // adapter events already journaled
	finished       bool // terminal adapter event (done/error) journaled
}

// Server dispatches IPC commands against the store, adapter, and worktree
// manager for one project.
type Server struct {
	store       *store.Store
	projectRoot string
	adapter     adapter.Adapter
	mgr         *worktree.Manager

	runs   map[string]*runMeta // adapter runID -> meta
	byConv map[int64]string    // conversationID -> adapter runID (active run)
}

// NewServer builds a Server bound to one project root.
func NewServer(st *store.Store, projectRoot string, ad adapter.Adapter, mgr *worktree.Manager) *Server {
	return &Server{
		store:       st,
		projectRoot: projectRoot,
		adapter:     ad,
		mgr:         mgr,
		runs:        make(map[string]*runMeta),
		byConv:      make(map[int64]string),
	}
}

// Serve accepts connections and handles them one at a time (M0). It returns
// when the listener is closed (net.ErrClosed) or on a fatal accept error.
func (s *Server) Serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.handleConn(conn)
	}
}

// handleConn processes requests on a connection until EOF. Requests and
// responses are line-delimited JSON via json.Decoder/Encoder.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		var req Request
		err := dec.Decode(&req)
		if err != nil {
			if err != io.EOF {
				log.Printf("ipc: decode: %v", err)
			}
			return
		}
		resp := s.dispatch(context.Background(), req)
		if err := enc.Encode(resp); err != nil {
			log.Printf("ipc: encode: %v", err)
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req Request) Response {
	var resp Response
	var err error
	switch req.Cmd {
	case CmdBootstrap:
		resp, err = s.handleBootstrap(ctx, req)
	case CmdSendMessage:
		resp, err = s.handleSendMessage(ctx, req)
	case CmdPollEvents:
		resp, err = s.handlePollEvents(ctx, req)
	case CmdAcceptDiff:
		resp, err = s.handleReviewDiff(ctx, req.DiffID, "accept")
	case CmdRejectDiff:
		resp, err = s.handleReviewDiff(ctx, req.DiffID, "reject")
	default:
		err = fmt.Errorf("unknown command %q", req.Cmd)
	}
	if err != nil {
		return Response{OK: false, Error: err.Error()}
	}
	resp.OK = true
	return resp
}

// handleBootstrap resolves (creating as needed) project + main workstream +
// active conversation, and returns their IDs plus full event history and the
// latest diff — everything a client needs to restore a session.
func (s *Server) handleBootstrap(ctx context.Context, req Request) (Response, error) {
	root := req.ProjectRoot
	if root == "" {
		root = s.projectRoot
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Response{}, fmt.Errorf("bootstrap: resolve project root: %w", err)
	}
	if abs != s.projectRoot {
		return Response{}, fmt.Errorf("bootstrap: daemon is bound to %s, not %s", s.projectRoot, abs)
	}

	p, err := s.store.CreateOrGetProject(ctx, abs, filepath.Base(abs))
	if err != nil {
		return Response{}, err
	}
	w, err := s.store.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		return Response{}, err
	}
	c, err := s.store.GetActiveConversation(ctx, w.ID)
	if errors.Is(err, sql.ErrNoRows) {
		// Base SHA anchors stale-diff detection later; a repo with zero
		// commits simply stores NULL.
		baseSHA, _ := git.CurrentSHA(abs)
		c, err = s.store.CreateConversation(ctx, w.ID, baseSHA)
	}
	if err != nil {
		return Response{}, err
	}
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return Response{}, err
	}
	return Response{
		Project:      &p,
		Workstream:   &w,
		Conversation: &c,
		Events:       events,
		AgentRunning: new(false),
		Diff:         s.latestDiffInfo(ctx, c.ID),
	}, nil
}

// handleSendMessage journals the user message, creates a run worktree, and
// starts the agent in it.
func (s *Server) handleSendMessage(ctx context.Context, req Request) (Response, error) {
	if req.Text == "" {
		return Response{}, fmt.Errorf("send_message: text is required")
	}
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
		}
	}

	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(map[string]interface{}{"text": req.Text}))
	if err != nil {
		return Response{}, err
	}

	// Setup failures after this point revoke the run with a journaled
	// agent_error so the chat history stays truthful.
	runDirID := worktree.NewRunID()
	wtPath, err := s.mgr.Create(runDirID)
	if err != nil {
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("create worktree: %w", err))
	}
	if err := s.store.UpdateWorkstreamWorktree(ctx, c.WorkstreamID, &wtPath); err != nil {
		return Response{}, fmt.Errorf("bind worktree: %w", err)
	}

	runID, err := s.adapter.Start(ctx, wtPath, req.Text)
	if err != nil {
		_ = s.mgr.Remove(wtPath) // nothing to review; don't orphan a worktree
		_ = s.store.UpdateWorkstreamWorktree(ctx, c.WorkstreamID, nil)
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("start agent: %w", err))
	}

	meta := &runMeta{
		runID:          runID,
		runDirID:       runDirID,
		conversationID: c.ID,
		workstreamID:   c.WorkstreamID,
		worktreePath:   wtPath,
	}
	s.runs[runID] = meta
	s.byConv[c.ID] = runID
	return Response{Event: &ev}, nil
}

// handlePollEvents drains finished-run adapter events into the journal,
// extracts the run's diff once, then returns journal events after afterSeq.
func (s *Server) handlePollEvents(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}

	agentRunning := false
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			if err := s.drainRun(ctx, meta); err != nil {
				return Response{}, err
			}
		}
	}

	events, err := s.store.ListEvents(ctx, c.ID, req.AfterSeq)
	if err != nil {
		return Response{}, err
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			agentRunning = true
		}
	}
	return Response{
		Events:       events,
		AgentRunning: new(agentRunning),
		Diff:         s.latestDiffInfo(ctx, c.ID),
	}, nil
}

// drainRun pulls new adapter events into the journal once. When the terminal
// event arrives it extracts the worktree diff exactly once and records it.
func (s *Server) drainRun(ctx context.Context, meta *runMeta) error {
	evs, err := s.adapter.Events(ctx, meta.runID, meta.consumed)
	if err != nil {
		return err
	}
	meta.consumed += len(evs)
	for _, ev := range evs {
		if _, err := s.store.AppendEvent(ctx, meta.conversationID, ev.Type, mustJSON(ev.Payload)); err != nil {
			return err
		}
	}
	if len(evs) == 0 {
		return nil // still running
	}
	if t := evs[len(evs)-1].Type; t != store.EventAgentDone && t != store.EventAgentError {
		return nil // more events to come (not reached in M0: terminal batch is atomic)
	}
	meta.finished = true

	// The diff is extracted whether the run succeeded or failed: partial
	// changes are reviewable, and the human decides. (ADR-0001.)
	baseSHA := ""
	if c, err := s.store.GetConversation(ctx, meta.conversationID); err == nil && c.BaseCommitSHA != nil {
		baseSHA = *c.BaseCommitSHA
	}
	diffPath, err := s.mgr.ExtractDiff(meta.worktreePath, meta.runDirID)
	if err != nil {
		_, _ = s.store.AppendEvent(ctx, meta.conversationID, store.EventAgentError,
			mustJSON(map[string]interface{}{"error": fmt.Sprintf("extract diff: %v", err)}))
		return nil
	}
	if diffPath == "" {
		return nil // agent changed nothing; no diff to review
	}
	if _, err := s.store.InsertDiff(ctx, meta.conversationID, diffPath, baseSHA); err != nil {
		return err
	}
	return nil
}

// handleReviewDiff implements accept_diff and reject_diff. Accept applies the
// diff to the user's working tree with git apply (the visible loop closes
// here). Both journal a review_action and retire the run's worktree.
func (s *Server) handleReviewDiff(ctx context.Context, diffID int64, action string) (Response, error) {
	if diffID == 0 {
		return Response{}, fmt.Errorf("%s_diff: diff_id is required", action)
	}
	d, err := s.store.GetDiff(ctx, diffID)
	if err != nil {
		return Response{}, err
	}
	if d.Status != store.DiffPending {
		return Response{}, fmt.Errorf("%s_diff: diff %d already %s", action, diffID, d.Status)
	}

	applied := false
	if action == "accept" {
		if err := git.ApplyDiff(s.projectRoot, d.PathOnDisk); err != nil {
			// Stay pending: review didn't conclude. The git error text says why.
			return Response{}, fmt.Errorf("accept_diff: apply: %w", err)
		}
		applied = true
	}

	status := store.DiffAccepted
	if action == "reject" {
		status = store.DiffRejected
	}
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":  action,
		"diff_id": d.ID,
	})); err != nil {
		return Response{}, err
	}
	if err := s.store.UpdateDiffStatus(ctx, diffID, status); err != nil {
		return Response{}, err
	}

	s.retireRun(ctx, d.ConversationID)
	return Response{DiffID: diffID, Applied: applied}, nil
}

// retireRun closes the adapter run and removes the worktree for a concluded
// review. After a restart there is no in-memory run; the workstream's bound
// worktree path is the fallback. Removal failures are logged, not fatal — the
// review already happened and worktrees are reaped by `git worktree prune`.
func (s *Server) retireRun(ctx context.Context, conversationID int64) {
	var wtPath string
	if runID, ok := s.byConv[conversationID]; ok {
		if meta := s.runs[runID]; meta != nil {
			wtPath = meta.worktreePath
			_ = s.adapter.Close(ctx, runID)
			delete(s.runs, runID)
		}
		delete(s.byConv, conversationID)
	}

	c, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return
	}
	if wtPath == "" {
		if w, err := s.store.GetWorkstream(ctx, c.WorkstreamID); err == nil && w.WorktreePath != nil {
			wtPath = *w.WorktreePath
		}
	}
	if wtPath != "" {
		if err := s.mgr.Remove(wtPath); err != nil {
			log.Printf("ipc: retire run: remove worktree %s: %v", wtPath, err)
		}
	}
	if err := s.store.UpdateWorkstreamWorktree(ctx, c.WorkstreamID, nil); err != nil {
		log.Printf("ipc: retire run: unbind worktree: %v", err)
	}
}

// latestDiffInfo returns the latest diff for a conversation with its content,
// or nil when the conversation has no diffs.
func (s *Server) latestDiffInfo(ctx context.Context, conversationID int64) *DiffInfo {
	d, err := s.store.LatestDiff(ctx, conversationID)
	if err != nil {
		return nil
	}
	info := &DiffInfo{ID: d.ID, Status: d.Status, Path: d.PathOnDisk}
	if b, err := os.ReadFile(d.PathOnDisk); err == nil {
		info.Content = string(b)
	}
	return info
}

// checkConversation validates that the conversation exists and belongs to
// this daemon's project.
func (s *Server) checkConversation(ctx context.Context, conversationID int64) (store.Conversation, error) {
	if conversationID == 0 {
		return store.Conversation{}, fmt.Errorf("conversation_id is required")
	}
	c, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return store.Conversation{}, err
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return store.Conversation{}, err
	}
	p, err := s.store.GetProject(ctx, w.ProjectID)
	if err != nil {
		return store.Conversation{}, err
	}
	if p.RootPath != s.projectRoot {
		return store.Conversation{}, fmt.Errorf("conversation %d belongs to project %s, not %s",
			conversationID, p.RootPath, s.projectRoot)
	}
	return c, nil
}

// failRun journals an agent_error for a run that could not start and returns
// the error for the response.
func (s *Server) failRun(ctx context.Context, conversationID int64, cause error) error {
	_, _ = s.store.AppendEvent(ctx, conversationID, store.EventAgentError,
		mustJSON(map[string]interface{}{"error": cause.Error()}))
	return cause
}

// mustJSON marshals a payload map; adapter payloads are always marshal-safe.
func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"payload marshal failed"}`
	}
	return string(b)
}
