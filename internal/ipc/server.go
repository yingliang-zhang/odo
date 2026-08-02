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
}

// Server dispatches IPC commands against the store, adapters, and worktree
// manager for one project.
type Server struct {
	store       *store.Store
	projectRoot string
	adapters    map[string]adapter.Adapter // "" and "omp" = default adapter
	mgr         *worktree.Manager

	runs   map[string]*runMeta // adapter runID -> meta
	byConv map[int64]string    // conversationID -> adapter runID (active run)
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
		resp, err = s.handleReviewDiff(ctx, req.DiffID, "accept")
	case CmdRejectDiff:
		resp, err = s.handleReviewDiff(ctx, req.DiffID, "reject")
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

	// Journal the user message with attachments (spec item 5).
	msgPayload := map[string]interface{}{"text": req.Text}
	if len(req.Attachments) > 0 {
		msgPayload["attachments"] = req.Attachments
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}

	// Build the prompt for the agent. If attachments are present, inject
	// the file paths so the agent reads them before proceeding.
	prompt := req.Text
	if len(req.Attachments) > 0 {
		prompt = fmt.Sprintf("Attached files: %s. Read them before proceeding.\n\n%s",
			strings.Join(req.Attachments, ", "), req.Text)
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
		_, _ = s.store.AppendEvent(ctx, c.ID, store.EventAgentError, mustJSON(map[string]interface{}{
			"error": "Steering not supported by current adapter.",
		}))
	}
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

// runDistillAgent runs the summary prompt through the default adapter in a
// temp directory and returns the concatenated agent_text output (the wiki
// note body). It blocks until the run's terminal event or distillTimeout.
func (s *Server) runDistillAgent(ctx context.Context, events []store.Event) (string, error) {
	ad := s.adapters[""]
	tmpDir, err := os.MkdirTemp("", "odo-distill-")
	if err != nil {
		return "", fmt.Errorf("distill dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	runID, err := ad.Start(ctx, tmpDir, distillPrompt(events))
	if err != nil {
		return "", fmt.Errorf("start distill run: %w", err)
	}
	defer ad.Close(ctx, runID)

	deadline := time.Now().Add(distillTimeout)
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
			return "", fmt.Errorf("distill run timed out")
		}
		time.Sleep(200 * time.Millisecond)
	}
	note := strings.Join(texts, "\n\n")
	if note == "" {
		if runErr != "" {
			return "", errors.New(runErr)
		}
		return "", fmt.Errorf("distill run produced no summary")
	}
	return note, nil
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
