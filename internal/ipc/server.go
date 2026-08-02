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
	"strings"
	"sync"
	"time"
	"unicode"

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
	adapter        string // adapter name that owns the run ("" = default)
	conversationID int64
	workstreamID   int64
	worktreePath   string
	consumed       int  // adapter events already journaled
	finished       bool // terminal adapter event (done/error) journaled
	errored        bool // terminal adapter event was agent_error
}

// Server dispatches IPC commands against the store, adapters, and worktree
// manager for one project.
type Server struct {
	store          *store.Store
	projectRoot    string
	adapters       map[string]adapter.Adapter // "" and "omp" = default adapter
	distillAdapter adapter.Adapter            // uses orchestrator model from prefs.md
	mgr            *worktree.Manager

	runs       map[string]*runMeta // adapter runID -> meta
	byConv     map[int64]string    // conversationID -> adapter runID (active run)
	fanoutRuns map[int64][]string  // conversationID -> fan-out adapter runIDs (kept until their diffs are reviewed)
}

// NewServer builds a Server bound to one project root. ad becomes the default
// adapter ("omp").
func NewServer(st *store.Store, projectRoot string, ad adapter.Adapter, mgr *worktree.Manager) *Server {
	s := &Server{
		store:       st,
		projectRoot: projectRoot,
		adapters:    make(map[string]adapter.Adapter),
		mgr:         mgr,
		runs:        make(map[string]*runMeta),
		byConv:      make(map[int64]string),
		fanoutRuns:  make(map[int64][]string),
	}
	s.adapters[""] = ad
	s.adapters["omp"] = ad
	return s
}

// RegisterAdapter makes ad selectable via the send_message "adapter" field
// under the given name (e.g. "pi").
func (s *Server) RegisterAdapter(name string, ad adapter.Adapter) {
	s.adapters[name] = ad
}

// SetDistillAdapter sets the adapter used for distill runs (uses the
// orchestrator model from prefs.md instead of the coding model).
func (s *Server) SetDistillAdapter(ad adapter.Adapter) {
	s.distillAdapter = ad
}

// adapterFor resolves a run/request adapter name to its Adapter. Unknown
// names fall back to the default adapter.
func (s *Server) adapterFor(name string) adapter.Adapter {
	if ad, ok := s.adapters[name]; ok {
		return ad
	}
	return s.adapters[""]
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
	case CmdCreateWorkstream:
		resp, err = s.handleCreateWorkstream(ctx, req)
	case CmdListWorkstreams:
		resp, err = s.handleListWorkstreams(ctx, req)
	case CmdSendMessage:
		resp, err = s.handleSendMessage(ctx, req)
	case CmdPollEvents:
		resp, err = s.handlePollEvents(ctx, req)
	case CmdAcceptDiff:
		resp, err = s.handleDiffAction(ctx, req.DiffID, "accept")
	case CmdRejectDiff:
		resp, err = s.handleDiffAction(ctx, req.DiffID, "reject")
	case CmdReviewDiff:
		resp, err = s.handleReviewDiff(ctx, req)
	case CmdGetSettings:
		resp, err = s.handleGetSettings(ctx, req)
	case CmdUpdateSettings:
		resp, err = s.handleUpdateSettings(ctx, req)
	case CmdFanoutSend:
		resp, err = s.handleFanoutSend(ctx, req)
	case CmdDistill:
		resp, err = s.handleDistill(ctx, req)
	default:
		err = fmt.Errorf("unknown command %q", req.Cmd)
	}
	if err != nil {
		return Response{OK: false, Error: err.Error()}
	}
	resp.OK = true
	return resp
}

// resolveProject resolves (creating as needed) the project row for a
// request's project root, defaulting to the daemon's bound root and rejecting
// any other path. reqRoot may be empty.
func (s *Server) resolveProject(ctx context.Context, reqRoot string) (store.Project, error) {
	root := reqRoot
	if root == "" {
		root = s.projectRoot
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return store.Project{}, fmt.Errorf("resolve project root: %w", err)
	}
	if abs != s.projectRoot {
		return store.Project{}, fmt.Errorf("daemon is bound to %s, not %s", s.projectRoot, abs)
	}
	return s.store.CreateOrGetProject(ctx, abs, filepath.Base(abs))
}

// handleBootstrap resolves (creating as needed) project + workstream +
// active conversation, and returns their IDs plus full event history and the
// latest diff — everything a client needs to restore a session. Without a
// workstream_id it targets the default "main" workstream (creating it); with
// one it targets that workstream, which must belong to the project.
func (s *Server) handleBootstrap(ctx context.Context, req Request) (Response, error) {
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("bootstrap: %w", err)
	}
	var w store.Workstream
	if req.WorkstreamID != 0 {
		w, err = s.store.GetWorkstream(ctx, req.WorkstreamID)
		if err != nil {
			return Response{}, fmt.Errorf("bootstrap: %w", err)
		}
		if w.ProjectID != p.ID {
			return Response{}, fmt.Errorf("bootstrap: workstream %d belongs to another project", req.WorkstreamID)
		}
	} else {
		w, err = s.store.CreateOrGetWorkstream(ctx, p.ID, "main")
		if err != nil {
			return Response{}, err
		}
	}
	c, err := s.store.GetActiveConversation(ctx, w.ID)
	if errors.Is(err, sql.ErrNoRows) {
		// Base SHA anchors stale-diff detection later; a repo with zero
		// commits simply stores NULL.
		baseSHA, _ := git.CurrentSHA(s.projectRoot)
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

// handleCreateWorkstream creates (or returns) the named workstream for the
// project. The name is sanitized into a git-safe branch name; the sanitized
// form is also the workstream's stored name. An empty name is an error.
func (s *Server) handleCreateWorkstream(ctx context.Context, req Request) (Response, error) {
	name := sanitizeBranchName(req.Name)
	if name == "" {
		return Response{}, fmt.Errorf("create_workstream: a usable name is required")
	}
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("create_workstream: %w", err)
	}
	w, err := s.store.CreateOrGetWorkstream(ctx, p.ID, name)
	if err != nil {
		return Response{}, err
	}
	return Response{Project: &p, Workstream: &w}, nil
}

// handleListWorkstreams returns every workstream for the project, oldest
// first.
func (s *Server) handleListWorkstreams(ctx context.Context, req Request) (Response, error) {
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("list_workstreams: %w", err)
	}
	ws, err := s.store.ListWorkstreams(ctx, p.ID)
	if err != nil {
		return Response{}, err
	}
	return Response{Project: &p, Workstreams: ws}, nil
}

// sanitizeBranchName maps a user-typed workstream name to a git-safe branch
// name: letters, digits, and ._- pass through, everything else becomes "-",
// runs of dashes collapse, and leading/trailing edge characters are trimmed.
// It returns "" when nothing usable remains.
func sanitizeBranchName(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), ".-")
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
	if req.Steer {
		return s.handleSteering(ctx, c, req)
	}
	ad, ok := s.adapters[req.Adapter]
	if req.Adapter != "" && !ok {
		return Response{}, fmt.Errorf("send_message: unknown adapter %q", req.Adapter)
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
		}
	}
	if s.fanoutActive(c.ID) {
		return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
	}

	// Journal the user message with attachments (spec item 5).
	msgPayload := map[string]interface{}{"text": req.Text}
	if len(req.Attachments) > 0 {
		msgPayload["attachments"] = req.Attachments
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}

	prompt := buildPrompt(req.Text, req.Attachments)

	// Setup failures after this point revoke the run with a journaled
	// agent_error so the chat history stays truthful.
	runDirID := worktree.NewRunID()
	wtPath, err := s.mgr.Create(runDirID)
	if err != nil {
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("create worktree: %w", err))
	}
	if err := s.store.UpdateWorkstreamWorktree(ctx, c.WorkstreamID, &wtPath); err != nil {
		_ = s.mgr.Remove(wtPath) // don't orphan the worktree we just created
		return Response{}, fmt.Errorf("bind worktree: %w", err)
	}

	runID, err := ad.Start(ctx, wtPath, prompt)
	if err != nil {
		_ = s.mgr.Remove(wtPath) // nothing to review; don't orphan a worktree
		_ = s.store.UpdateWorkstreamWorktree(ctx, c.WorkstreamID, nil)
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("start agent: %w", err))
	}

	meta := &runMeta{
		runID:          runID,
		runDirID:       runDirID,
		adapter:        req.Adapter,
		conversationID: c.ID,
		workstreamID:   c.WorkstreamID,
		worktreePath:   wtPath,
	}
	s.runs[runID] = meta
	s.byConv[c.ID] = runID
	return Response{Event: &ev}, nil
}

// buildPrompt renders the agent prompt for a user message. If attachments
// are present, the file paths are injected so the agent reads them before
// proceeding.
func buildPrompt(text string, attachments []string) string {
	if len(attachments) == 0 {
		return text
	}
	return fmt.Sprintf("Attached files: %s. Read them before proceeding.\n\n%s",
		strings.Join(attachments, ", "), text)
}

// handleFanoutSend journals the user message once and starts N parallel
// agent runs sharing the conversation, each in its own worktree. Polling
// drains and reviews each run independently, producing one diff per run.
func (s *Server) handleFanoutSend(ctx context.Context, req Request) (Response, error) {
	if req.Text == "" {
		return Response{}, fmt.Errorf("fanout_send: text is required")
	}
	n := req.N
	if n < 1 {
		n = 2 // default fan-out width when the client sends no count
	}
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	ad, ok := s.adapters[req.Adapter]
	if req.Adapter != "" && !ok {
		return Response{}, fmt.Errorf("fanout_send: unknown adapter %q", req.Adapter)
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return Response{}, fmt.Errorf("fanout_send: agent already running for conversation %d", c.ID)
		}
	}
	if s.fanoutActive(c.ID) {
		return Response{}, fmt.Errorf("fanout_send: agent already running for conversation %d", c.ID)
	}

	msgPayload := map[string]interface{}{"text": req.Text}
	if len(req.Attachments) > 0 {
		msgPayload["attachments"] = req.Attachments
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}

	prompt := buildPrompt(req.Text, req.Attachments)

	// All-or-nothing: a failed setup cancels every run started so far so no
	// orphan agent process or worktree is left behind.
	runs := make([]RunInfo, 0, n)
	started := make([]*runMeta, 0, n)
	for range n {
		runDirID := worktree.NewRunID()
		wtPath, err := s.mgr.Create(runDirID)
		if err != nil {
			s.cancelFanout(ctx, ad, started)
			return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("fanout_send: create worktree: %w", err))
		}
		runID, err := ad.Start(ctx, wtPath, prompt)
		if err != nil {
			_ = s.mgr.Remove(wtPath) // nothing to review; don't orphan a worktree
			s.cancelFanout(ctx, ad, started)
			return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("fanout_send: start agent: %w", err))
		}
		meta := &runMeta{
			runID:          runID,
			runDirID:       runDirID,
			adapter:        req.Adapter,
			conversationID: c.ID,
			workstreamID:   c.WorkstreamID,
			worktreePath:   wtPath,
		}
		s.runs[runID] = meta
		s.fanoutRuns[c.ID] = append(s.fanoutRuns[c.ID], runID)
		started = append(started, meta)
		runs = append(runs, RunInfo{RunID: runID, Status: "running"})
	}
	return Response{Event: &ev, Runs: runs}, nil
}

// cancelFanout unwinds the fan-out runs that already started after a sibling
// run's setup failed.
func (s *Server) cancelFanout(ctx context.Context, ad adapter.Adapter, started []*runMeta) {
	for _, meta := range started {
		_ = ad.Cancel(ctx, meta.runID)
		_ = ad.Close(ctx, meta.runID)
		delete(s.runs, meta.runID)
		ids := s.fanoutRuns[meta.conversationID]
		for i, id := range ids {
			if id == meta.runID {
				ids = append(ids[:i], ids[i+1:]...)
				break
			}
		}
		if len(ids) == 0 {
			delete(s.fanoutRuns, meta.conversationID)
		} else {
			s.fanoutRuns[meta.conversationID] = ids
		}
		if err := s.mgr.Remove(meta.worktreePath); err != nil {
			log.Printf("fanout_send: remove worktree %s: %v", meta.worktreePath, err)
		}
	}
}

// handleSteering journals a steering message for a conversation without
// starting a new run. An active run receives the text via the owning
// adapter's Send; adapters without steering support get a journaled
// agent_error so the user sees why the message stops at the chat. With no
// active run the message is simply journaled.
func (s *Server) handleSteering(ctx context.Context, c store.Conversation, req Request) (Response, error) {
	msgPayload := map[string]interface{}{"text": req.Text}
	if len(req.Attachments) > 0 {
		msgPayload["attachments"] = req.Attachments
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}
	runID, ok := s.byConv[c.ID]
	meta := s.runs[runID]
	if !ok || meta == nil || meta.finished {
		return Response{Event: &ev}, nil // no active run: journaled only
	}
	if err := s.adapterFor(meta.adapter).Send(ctx, runID, req.Text); err != nil {
		log.Printf("steering: send to run %s: %v", runID, err)
		msg := err.Error()
		if strings.Contains(msg, "not supported") {
			msg = "Steering not supported by current adapter."
		}
		_, _ = s.store.AppendEvent(ctx, c.ID, store.EventAgentError, mustJSON(map[string]interface{}{
			"error": msg,
		}))
	}
	return Response{Event: &ev}, nil
}

// handlePollEvents drains finished-run adapter events into the journal,
// extracts each run's diff once, then returns journal events after afterSeq.
// Fan-out conversations drain every tracked run and report all of them in
// the runs field.
func (s *Server) handlePollEvents(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}

	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			if err := s.drainRun(ctx, meta); err != nil {
				return Response{}, err
			}
		}
	}
	for _, id := range s.fanoutRuns[c.ID] {
		if meta := s.runs[id]; meta != nil && !meta.finished {
			if err := s.drainRun(ctx, meta); err != nil {
				return Response{}, err
			}
		}
	}

	events, err := s.store.ListEvents(ctx, c.ID, req.AfterSeq)
	if err != nil {
		return Response{}, err
	}
	agentRunning := s.fanoutActive(c.ID)
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			agentRunning = true
		}
	}
	return Response{
		Events:       events,
		AgentRunning: new(agentRunning),
		Diff:         s.latestDiffInfo(ctx, c.ID),
		Runs:         s.runInfos(c.ID),
	}, nil
}

// fanoutActive reports whether any fan-out run for the conversation is still
// unfinished.
func (s *Server) fanoutActive(conversationID int64) bool {
	for _, id := range s.fanoutRuns[conversationID] {
		if meta := s.runs[id]; meta != nil && !meta.finished {
			return true
		}
	}
	return false
}

// runInfos snapshots all tracked fan-out runs for the conversation — running,
// done, and errored alike. A run leaves the list only when its diff is
// reviewed (retireRunForDiff drops it).
func (s *Server) runInfos(conversationID int64) []RunInfo {
	ids := s.fanoutRuns[conversationID]
	if len(ids) == 0 {
		return nil
	}
	infos := make([]RunInfo, 0, len(ids))
	for _, id := range ids {
		status := "done"
		if meta := s.runs[id]; meta != nil {
			switch {
			case !meta.finished:
				status = "running"
			case meta.errored:
				status = "error"
			}
		}
		infos = append(infos, RunInfo{RunID: id, Status: status})
	}
	return infos
}

// drainRun pulls new adapter events into the journal once. When the terminal
// event arrives it extracts the worktree diff exactly once and records it.
func (s *Server) drainRun(ctx context.Context, meta *runMeta) error {
	evs, err := s.adapterFor(meta.adapter).Events(ctx, meta.runID, meta.consumed)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		if _, err := s.store.AppendEvent(ctx, meta.conversationID, ev.Type, mustJSON(ev.Payload)); err != nil {
			return err
		}
		meta.consumed++ // advance per successfully journaled event
	}
	if len(evs) == 0 {
		return nil // still running
	}
	if t := evs[len(evs)-1].Type; t != store.EventAgentDone && t != store.EventAgentError {
		return nil // more events to come (not reached in M0: terminal batch is atomic)
	}
	meta.errored = evs[len(evs)-1].Type == store.EventAgentError

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
		meta.finished = true // mark finished so polling stops even on diff failure
		return nil
	}
	if diffPath == "" {
		meta.finished = true // agent changed nothing; run is complete
		return nil
	}
	if _, err := s.store.InsertDiff(ctx, meta.conversationID, diffPath, baseSHA); err != nil {
		return err
	}
	meta.finished = true // mark finished only after the diff row exists
	return nil
}

// handleDiffAction implements accept_diff and reject_diff. Accept applies the
// diff to the user's working tree with git apply (the visible loop closes
// here). Both journal a review_action and retire the run's worktree.
func (s *Server) handleDiffAction(ctx context.Context, diffID int64, action string) (Response, error) {
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
		// Commit the applied diff so the next worktree (created from HEAD)
		// includes all previously accepted files. Without this, files applied
		// via `git apply` but never committed don't appear in new worktrees,
		// and the agent can't modify them in isolation.
		if err := git.CommitAll(s.projectRoot, fmt.Sprintf("odo: accept diff #%d", diffID)); err != nil {
			// Non-fatal: the file is already applied to the working tree.
			// The commit just ensures worktree freshness for future runs.
			log.Printf("accept_diff: auto-commit failed (non-fatal): %v", err)
		}
		applied = true
	}

	status := store.DiffAccepted
	if action == "reject" {
		status = store.DiffRejected
	}
	// Update diff status first: if the event journal fails, the diff is at
	// least correctly marked and a retry returns "already accepted/rejected"
	// instead of re-running git apply on an already-applied patch.
	if err := s.store.UpdateDiffStatus(ctx, diffID, status); err != nil {
		return Response{}, err
	}
	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":  action,
		"diff_id": d.ID,
	})); err != nil {
		return Response{}, err
	}

	s.retireRunForDiff(ctx, d)
	return Response{DiffID: diffID, Applied: applied}, nil
}

// retireRunForDiff releases resources after a diff review. A fan-out run is
// retired individually — only the run whose diff was reviewed is closed and
// its worktree removed, leaving sibling runs reviewable. Everything else
// takes the whole-conversation retirement. (Fan-out run tracking is
// in-memory only: after a daemon restart a reviewed fan-out diff leaves its
// worktree to `git worktree prune`, same as any crash-orphaned worktree.)
func (s *Server) retireRunForDiff(ctx context.Context, d store.Diff) {
	// The diff file basename is the run's runDirID (see worktree.DiffPath).
	runDirID := strings.TrimSuffix(filepath.Base(d.PathOnDisk), ".diff")
	if ids, ok := s.fanoutRuns[d.ConversationID]; ok {
		for i, id := range ids {
			meta := s.runs[id]
			if meta == nil || meta.runDirID != runDirID {
				continue
			}
			_ = s.adapterFor(meta.adapter).Close(ctx, id)
			delete(s.runs, id)
			s.fanoutRuns[d.ConversationID] = append(ids[:i], ids[i+1:]...)
			if len(s.fanoutRuns[d.ConversationID]) == 0 {
				delete(s.fanoutRuns, d.ConversationID)
			}
			if err := s.mgr.Remove(meta.worktreePath); err != nil {
				log.Printf("ipc: retire fanout run: remove worktree %s: %v", meta.worktreePath, err)
			}
			return
		}
	}
	s.retireRun(ctx, d.ConversationID)
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
			_ = s.adapterFor(meta.adapter).Close(ctx, runID)
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

// moaReviewTimeout bounds each parallel review run. The adapter wrapper
// applies its own timeout (default 600s); a skew between the two only
// changes which error message lands in the review comments.
const moaReviewTimeout = 5 * time.Minute

// reviewModel is one parsed entry of the prefs.md `review:` line.
type reviewModel struct {
	model    string
	provider string
}

// handleReviewDiff implements review_diff: the diff is fanned out to every
// model on the prefs.md `review:` line, each as a one-shot OMP run in a temp
// directory (the distill pattern). Reviews run in parallel; the call blocks
// until all finish — like distill, it blocks the single-connection daemon.
// Results are journaled as a review_action event with action "moa_review".
func (s *Server) handleReviewDiff(ctx context.Context, req Request) (Response, error) {
	if req.DiffID == 0 {
		return Response{}, fmt.Errorf("review_diff: diff_id is required")
	}
	d, err := s.store.GetDiff(ctx, req.DiffID)
	if err != nil {
		return Response{}, err
	}
	content, err := os.ReadFile(d.PathOnDisk)
	if err != nil {
		return Response{}, fmt.Errorf("review_diff: read diff: %w", err)
	}
	models := parseReviewModels(adapter.LoadPrefsRaw("review"))
	if len(models) == 0 {
		return Response{}, errors.New("No review models configured.")
	}

	reviews := make([]ReviewResult, len(models))
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reviews[i] = s.reviewWithModel(ctx, m, string(content))
		}()
	}
	wg.Wait()

	if _, err := s.store.AppendEvent(ctx, d.ConversationID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":  "moa_review",
		"diff_id": d.ID,
		"reviews": reviews,
	})); err != nil {
		return Response{}, err
	}
	return Response{Reviews: reviews}, nil
}

// reviewWithModel runs the review prompt through one explicit-model OMP
// adapter and parses the verdict. A failed run degrades to needs_fixes with
// the error as comments: a review that never happened must not read as an
// accept.
func (s *Server) reviewWithModel(ctx context.Context, m reviewModel, diffContent string) ReviewResult {
	label := m.model + "@" + m.provider
	ad := adapter.NewOMPExplicit(s.mgr.StateDir(), m.model, m.provider)
	text, err := runOneShot(ctx, ad, reviewPrompt(diffContent), moaReviewTimeout)
	if err != nil {
		return ReviewResult{Model: label, Verdict: "needs_fixes", Comments: "review failed: " + err.Error()}
	}
	return parseVerdict(label, text)
}

// parseReviewModels parses the comma-separated `review:` prefs value into
// model/provider pairs, skipping blank and malformed entries.
func parseReviewModels(raw string) []reviewModel {
	var out []reviewModel
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		model, provider := adapter.ParseModelProvider(entry)
		if model == "" {
			continue
		}
		out = append(out, reviewModel{model: model, provider: provider})
	}
	return out
}

// reviewPrompt wraps the diff content with the MoA review instruction.
func reviewPrompt(diffContent string) string {
	return "Review the following diff. Provide your verdict and comments.\n\n" +
		"Verdict must be one of: ACCEPT, REJECT, NEEDS_FIXES\n\n" + diffContent
}

// parseVerdict extracts the verdict (the first line containing ACCEPT,
// REJECT, or NEEDS_FIXES) and the comments (everything after that line).
// Output with no recognizable verdict degrades to needs_fixes with the full
// text as comments — an unparseable review must never read as an accept.
func parseVerdict(model, text string) ReviewResult {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		up := strings.ToUpper(line)
		verdict := ""
		switch {
		case strings.Contains(up, "NEEDS_FIXES"):
			verdict = "needs_fixes"
		case strings.Contains(up, "REJECT"):
			verdict = "reject"
		case strings.Contains(up, "ACCEPT"):
			verdict = "accept"
		}
		if verdict != "" {
			return ReviewResult{
				Model:    model,
				Verdict:  verdict,
				Comments: strings.TrimSpace(strings.Join(lines[i+1:], "\n")),
			}
		}
	}
	return ReviewResult{Model: model, Verdict: "needs_fixes", Comments: strings.TrimSpace(text)}
}

// handleGetSettings returns the effective daemon settings: prefs.md values
// where present, compiled-in adapter defaults elsewhere.
func (s *Server) handleGetSettings(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("get_settings: %w", err)
	}
	st := adapter.ReadSettings()
	return Response{Settings: &st}, nil
}

// handleUpdateSettings writes the request's non-empty fields to prefs.md and
// returns the resulting effective settings. The daemon is not restarted —
// adapters re-read prefs on every run, so changes take effect on next run.
func (s *Server) handleUpdateSettings(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("update_settings: %w", err)
	}
	if req.Settings == nil {
		return Response{}, fmt.Errorf("update_settings: settings object is required")
	}
	if err := adapter.UpdateSettings(*req.Settings); err != nil {
		return Response{}, err
	}
	st := adapter.ReadSettings()
	return Response{Settings: &st}, nil
}

// distillTimeout bounds the blocking distill agent run. The adapter wrapper
// applies its own timeout on a similar scale; a skew between the two only
// changes which error message the user sees.
const distillTimeout = 10 * time.Minute

// handleDistill summarizes the conversation's journaled events into a wiki
// note at <project>/wiki/<workstream>-epoch-<N>.md (N = the distilled epoch)
// and starts a new epoch. The summary comes from a one-shot run of the
// default adapter in a throwaway directory — it blocks the single-connection
// daemon but never touches the user's working tree. Old events stay in the
// append-only journal; only the epoch counter moves (ADR-0002).
func (s *Server) handleDistill(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return Response{}, fmt.Errorf("distill: agent still running for conversation %d", c.ID)
		}
	}
	if s.fanoutActive(c.ID) {
		return Response{}, fmt.Errorf("distill: agent still running for conversation %d", c.ID)
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return Response{}, err
	}

	note, err := s.runDistillAgent(ctx, events)
	if err != nil {
		return Response{}, fmt.Errorf("distill: %w", err)
	}

	wikiDir := filepath.Join(s.projectRoot, "wiki")
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		return Response{}, fmt.Errorf("distill: create wiki dir: %w", err)
	}
	wikiPath := filepath.Join(wikiDir, fmt.Sprintf("%s-epoch-%d.md", w.Name, c.Epoch))
	if err := os.WriteFile(wikiPath, []byte(note), 0o644); err != nil {
		return Response{}, fmt.Errorf("distill: write wiki note: %w", err)
	}

	newEpoch, err := s.store.IncrementEpoch(ctx, c.ID)
	if err != nil {
		return Response{}, err
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":    "distill",
		"epoch":     newEpoch,
		"wiki_path": wikiPath,
	})); err != nil {
		return Response{}, err
	}
	return Response{WikiPath: wikiPath, Epoch: newEpoch}, nil
}

// runDistillAgent runs the summary prompt through the orchestrator adapter
// as a one-shot run and returns the wiki note body.
func (s *Server) runDistillAgent(ctx context.Context, events []store.Event) (string, error) {
	ad := s.distillAdapter
	if ad == nil {
		ad = s.adapters[""] // fallback to default if distill adapter not configured
	}
	return runOneShot(ctx, ad, distillPrompt(events), distillTimeout)
}

// runOneShot runs prompt through ad in a throwaway directory, blocking until
// the run's terminal event or timeout, and returns the concatenated
// agent_text output. Distill and the MoA review fan-out both use it.
func runOneShot(ctx context.Context, ad adapter.Adapter, prompt string, timeout time.Duration) (string, error) {
	tmpDir, err := os.MkdirTemp("", "odo-oneshot-")
	if err != nil {
		return "", fmt.Errorf("oneshot dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	runID, err := ad.Start(ctx, tmpDir, prompt)
	if err != nil {
		return "", fmt.Errorf("start run: %w", err)
	}
	defer ad.Close(ctx, runID)

	deadline := time.Now().Add(timeout)
	consumed := 0
	var texts []string
	var runErr string
	for {
		evs, err := ad.Events(ctx, runID, consumed)
		if err != nil {
			return "", err
		}
		consumed += len(evs)
		for _, ev := range evs {
			switch ev.Type {
			case store.EventAgentText:
				if t, ok := ev.Payload["text"].(string); ok && t != "" {
					texts = append(texts, t)
				}
			case store.EventAgentError:
				if e, ok := ev.Payload["error"].(string); ok {
					runErr = e
				}
			}
		}
		if n := len(evs); n > 0 {
			if t := evs[n-1].Type; t == store.EventAgentDone || t == store.EventAgentError {
				break // terminal adapter event
			}
		}
		if time.Now().After(deadline) {
			_ = ad.Cancel(ctx, runID)
			return "", fmt.Errorf("run timed out")
		}
		time.Sleep(200 * time.Millisecond)
	}
	out := strings.Join(texts, "\n\n")
	if out == "" {
		if runErr != "" {
			return "", errors.New(runErr)
		}
		return "", fmt.Errorf("run produced no output")
	}
	return out, nil
}

// distillPrompt renders journaled events into the summary prompt: the M1
// spec's instruction line plus each event as raw payload JSON.
func distillPrompt(events []store.Event) string {
	var b strings.Builder
	b.WriteString("Summarize the key decisions, code changes, and open questions from this conversation. Format as markdown.\n\n")
	for _, ev := range events {
		fmt.Fprintf(&b, "### %s (seq %d)\n%s\n\n", ev.Type, ev.Seq, ev.Payload)
	}
	return b.String()
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
