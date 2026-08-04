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
	"sort"
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
	// M7: the run's transient streaming preview (adapter event with
	// partial:true), rebuilt by each drainRun while the run is live. Never
	// journaled; handlePollEvents passes it through verbatim.
	previewEvent *adapter.AgentEvent
}

// Server dispatches IPC commands against the store, adapters, and worktree
// manager for one project.
type Server struct {
	store          *store.Store
	projectRoot    string
	resolvedRoot   string                     // projectRoot after EvalSymlinks (registry exclusion compares resolved forms)
	adapters       map[string]adapter.Adapter // "" and "omp" = default adapter
	distillAdapter adapter.Adapter            // uses orchestrator model from prefs.md
	mgr            *worktree.Manager

	runs       map[string]*runMeta // adapter runID -> meta
	byConv     map[int64]string    // conversationID -> adapter runID (active run)
	fanoutRuns map[int64][]string  // conversationID -> fan-out adapter runIDs (kept until their diffs are reviewed)
}

// NewServer builds a Server bound to one project root. ad becomes the default
// adapter ("omp"). Binding a project also registers it in the global
// ~/.odo/projects.json registry (best-effort) so the learner can find sibling
// projects for user.md recurrence checks (M4 §1).
func NewServer(st *store.Store, projectRoot string, ad adapter.Adapter, mgr *worktree.Manager) *Server {
	resolved, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		resolved = projectRoot
	}
	s := &Server{
		store:        st,
		projectRoot:  projectRoot,
		resolvedRoot: resolved,
		adapters:     make(map[string]adapter.Adapter),
		mgr:          mgr,
		runs:         make(map[string]*runMeta),
		byConv:       make(map[int64]string),
		fanoutRuns:   make(map[int64][]string),
	}
	s.adapters[""] = ad
	s.adapters["omp"] = ad
	ensureProjectRegistered(projectRoot)
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
	case CmdCancel:
		resp, err = s.handleCancel(ctx, req)
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
	case CmdListWiki:
		resp, err = s.handleListWiki(ctx, req)
	case CmdPendingCounts:
		resp, err = s.handlePendingCounts(ctx, req)
	case CmdReadWiki:
		resp, err = s.handleReadWiki(ctx, req)
	case CmdReadMemory:
		resp, err = s.handleReadMemory(ctx, req)
	case CmdMemoryProposals:
		resp, err = s.handleMemoryProposals(ctx, req)
	case CmdApplyMemory:
		resp, err = s.handleApplyMemory(ctx, req)
	case CmdCurate:
		resp, err = s.handleCurate(ctx, req)
	case CmdPin:
		resp, err = s.handlePin(ctx, req)
	case CmdReadPins:
		resp, err = s.handleReadPins(ctx, req)
	case CmdListTopics:
		resp, err = s.handleListTopics(ctx, req)
	case CmdLedger:
		resp, err = s.handleLedger(ctx, req)
	case CmdContradictions:
		resp, err = s.handleContradictions(ctx, req)
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
	adName := adapter.ResolveAdapter(req.Adapter)
	ad, ok := s.adapters[adName]
	if !ok {
		if req.Adapter != "" {
			return Response{}, fmt.Errorf("send_message: unknown adapter %q", req.Adapter)
		}
		// The name came from prefs resolution; a prefs typo must never wedge
		// the daemon, so fall back to the compiled-in default.
		adName, ad = "", s.adapters[""]
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
		}
	}
	if s.fanoutActive(c.ID) {
		return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
	}

	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}
	ml := s.memoryLayers(ctx, w.Name, c.ID, req.Text)

	// Journal the user message with attachments (spec item 5).
	msgPayload := map[string]interface{}{"text": req.Text}
	if len(req.Attachments) > 0 {
		msgPayload["attachments"] = req.Attachments
	}
	if jr := ml.journalRecall(); len(jr) > 0 {
		msgPayload["recall"] = jr
	}
	if len(ml.receipt) > 0 {
		msgPayload["receipt"] = ml.receipt
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}

	prompt := buildPrompt(req.Text, req.Attachments, ml.user, ml.project, ml.pins, ml.index, ml.wiki)

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
		adapter:        adName,
		conversationID: c.ID,
		workstreamID:   c.WorkstreamID,
		worktreePath:   wtPath,
	}
	s.runs[runID] = meta
	s.byConv[c.ID] = runID
	return Response{Event: &ev}, nil
}

// memoryLayers bundles everything a prompt-building send path needs: the
// injected layer bodies, the recall path list, and the injection receipt
// (ADR-0003 inv 5: content hashes of exactly what was injected).
type memoryLayers struct {
	user    string       // ~/.odo/user.md (global principles)
	project string       // .odo/memory.md (project behavior rules)
	pins    string       // .odo/pins.md (M5: user-authored, verbatim)
	index   string       // wiki/index.md (M5: always-injected)
	wiki    string       // recalled epoch notes block
	recall  []recallItem // M6: was []string, now per-note with matched terms
	receipt map[string]string
}

// memoryLayers reads the current memory layers for the workstream and builds
// the recall items plus the sha16 receipt for every non-empty layer
// (per-note hashes cover the exact injected block, header and separator
// included). The query is the user's message text (M6 keyword recall);
// retracted notes (the journal's note-layer retraction set) are excluded.
// Layers absent/empty appear in neither.
func (s *Server) memoryLayers(ctx context.Context, wsName string, conversationID int64, query string) memoryLayers {
	ml := memoryLayers{
		user:    readUserMemory(),
		project: readProjectMemory(s.projectRoot),
		pins:    readPins(s.projectRoot),
		index:   readIndex(s.projectRoot),
		receipt: map[string]string{},
	}
	retracted := s.retractedNotes(ctx, conversationID)
	m, items, noteBytes := recallWikiNotes(s.projectRoot, wsName, query, retracted)
	ml.wiki = m
	if ml.user != "" {
		ml.receipt["~/.odo/user.md"] = sha16([]byte(ml.user))
	}
	if ml.project != "" {
		ml.receipt[".odo/memory.md"] = sha16([]byte(ml.project))
	}
	if ml.pins != "" {
		ml.receipt[".odo/pins.md"] = sha16([]byte(ml.pins))
	}
	if ml.index != "" {
		ml.receipt["wiki/index.md"] = sha16([]byte(ml.index))
	}
	for i, it := range items {
		ml.receipt[it.path] = sha16(noteBytes[i])
	}
	ml.recall = items
	return ml
}

// journalRecall serializes the recall payload for the user_message event
// (M6): fixed-marker layers first in daemon order as {"path": …} objects
// (matched_terms omitted — they are always-injected, not keyword-selected),
// then the recalled notes with optional matched_terms. M6 shape change:
// []string → []object (payload-key extension, ADR-0002 preserved).
func (ml *memoryLayers) journalRecall() []interface{} {
	var out []interface{}
	add := func(path string) {
		out = append(out, map[string]interface{}{"path": path})
	}
	if ml.user != "" {
		add("~/.odo/user.md")
	}
	if ml.project != "" {
		add(".odo/memory.md")
	}
	if ml.pins != "" {
		add(".odo/pins.md")
	}
	if ml.index != "" {
		add("wiki/index.md")
	}
	for _, it := range ml.recall {
		item := map[string]interface{}{"path": it.path}
		if len(it.matchedTerms) > 0 {
			item["matched_terms"] = it.matchedTerms
		}
		out = append(out, item)
	}
	return out
}

// buildPrompt renders the agent prompt. Layers inject in ADR-0003's stable
// order (inv 6 extended, M5): userMem (global, durable user principles),
// projectMem (.odo/memory.md behavior rules), pins (.odo/pins.md, verbatim),
// index (wiki/index.md, always-injected), then recalled wiki notes,
// attachment hints, and the user's text last (cache-friendly stable prefix).
func buildPrompt(text string, attachments []string, userMem, projectMem, pins, index, memory string) string {
	var b strings.Builder
	if userMem != "" {
		b.WriteString("## User memory (durable cross-project principles)\n\n")
		b.WriteString(userMem)
		b.WriteString("\n\n---\n\n")
	}
	if projectMem != "" {
		b.WriteString("## Project memory (behavior rules)\n\n")
		b.WriteString(projectMem)
		b.WriteString("\n\n---\n\n")
	}
	if pins != "" {
		b.WriteString("## Pins (user-authored, verbatim)\n\n")
		b.WriteString(pins)
		b.WriteString("\n\n---\n\n")
	}
	if index != "" {
		b.WriteString("## Wiki index\n\n")
		b.WriteString(index)
		b.WriteString("\n\n---\n\n")
	}
	if memory != "" {
		b.WriteString("## Prior notes (recalled)\n\n")
		b.WriteString(memory)
		b.WriteString("\n\n---\n\n")
	}
	if len(attachments) > 0 {
		fmt.Fprintf(&b, "Attached files: %s. Read them before proceeding.\n\n",
			strings.Join(attachments, ", "))
	}
	b.WriteString(text)
	return b.String()
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
	if n > 8 {
		return Response{}, fmt.Errorf("fanout_send: n must be ≤ 8, got %d", n)
	}
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	adName := adapter.ResolveAdapter(req.Adapter)
	ad, ok := s.adapters[adName]
	if !ok {
		if req.Adapter != "" {
			return Response{}, fmt.Errorf("fanout_send: unknown adapter %q", req.Adapter)
		}
		// The name came from prefs resolution; a prefs typo must never wedge
		// the daemon, so fall back to the compiled-in default.
		adName, ad = "", s.adapters[""]
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return Response{}, fmt.Errorf("fanout_send: agent already running for conversation %d", c.ID)
		}
	}
	if s.fanoutActive(c.ID) {
		return Response{}, fmt.Errorf("fanout_send: agent already running for conversation %d", c.ID)
	}

	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}
	ml := s.memoryLayers(ctx, w.Name, c.ID, req.Text)

	msgPayload := map[string]interface{}{"text": req.Text}
	if len(req.Attachments) > 0 {
		msgPayload["attachments"] = req.Attachments
	}
	if jr := ml.journalRecall(); len(jr) > 0 {
		msgPayload["recall"] = jr
	}
	if len(ml.receipt) > 0 {
		msgPayload["receipt"] = ml.receipt
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}

	prompt := buildPrompt(req.Text, req.Attachments, ml.user, ml.project, ml.pins, ml.index, ml.wiki)

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
			adapter:        adName,
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

// handleCancel SIGKILLs the conversation's active run through its adapter
// and journals agent_error{cancelled by user} so the chat history records
// the user's stop. The run is deliberately left unfinished: the normal
// drain path observes the dead process on the next poll, journals the
// adapter's own terminal event, and extracts whatever partial diff exists
// (ADR-0001: partial changes stay reviewable).
func (s *Server) handleCancel(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	runID, ok := s.byConv[c.ID]
	meta := s.runs[runID]
	if !ok || meta == nil || meta.finished {
		return Response{}, fmt.Errorf("cancel: no active run for conversation %d", c.ID)
	}
	if err := s.adapterFor(meta.adapter).Cancel(ctx, runID); err != nil {
		return Response{}, fmt.Errorf("cancel: %w", err)
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentError,
		mustJSON(map[string]interface{}{"error": "cancelled by user"})); err != nil {
		return Response{}, err
	}
	return Response{}, nil
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
	var preview *adapter.AgentEvent
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			preview = meta.previewEvent
		}
	}
	return Response{
		Events:       events,
		AgentRunning: new(agentRunning),
		Preview:      preview,
		Streaming:    preview != nil,
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
	// M7: a trailing partial event is the adapter's transient preview —
	// strip it before journaling and stash it for this poll's response. It
	// never advances consumed: the next Events call re-sends the completed
	// block it was previewing.
	meta.previewEvent = nil
	if n := len(evs); n > 0 && evs[n-1].Payload["partial"] == true {
		preview := evs[n-1]
		meta.previewEvent = &preview
		evs = evs[:n-1]
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
		// M6 (§8b): explicit guarded-path check — gitignore is not the
		// enforcement point (wiki/ is NOT gitignored, so this daemon-side
		// guard is the sole protection for daemon-owned content). The diff
		// stays pending on a violation; the user can still reject it.
		if paths, gerr := diffTargetPaths(d.PathOnDisk); gerr == nil {
			if perr := rejectProtectedPaths(paths); perr != nil {
				_, _ = s.store.AppendEvent(ctx, d.ConversationID, store.EventAgentError,
					mustJSON(map[string]interface{}{"error": "accept_diff: " + perr.Error()}))
				return Response{}, perr
			}
		}
		// An unreadable patch file falls through to git apply, the authority
		// on the patch format (its error names the file problem).
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

	// Fan-out auto-reject: when one diff from a fan-out is accepted,
	// auto-reject all sibling pending diffs from the same fan-out batch.
	// This prevents worktree leaks and unreviewable sibling diffs.
	if action == "accept" {
		s.autoRejectFanoutSiblings(ctx, d)
	}

	return Response{DiffID: diffID, Applied: applied}, nil
}

// retireRunForDiff releases resources after a diff review. A fan-out run is
// retired individually — only the run whose diff was reviewed is closed and
// its worktree removed, leaving sibling runs reviewable. Everything else
// takes the whole-conversation retirement. (Fan-out run tracking is
// in-memory only: after a daemon restart a reviewed fan-out diff leaves its
// worktree to `git worktree prune`, same as any crash-orphaned worktree.)

// autoRejectFanoutSiblings rejects all other pending diffs from the same
// fan-out batch when one diff is accepted. This prevents worktree leaks and
// ensures the user isn't stuck with unreviewable sibling diffs.
func (s *Server) autoRejectFanoutSiblings(ctx context.Context, accepted store.Diff) {
	ids, ok := s.fanoutRuns[accepted.ConversationID]
	if !ok {
		return // not a fan-out run, nothing to clean up
	}
	acceptedRunDir := strings.TrimSuffix(filepath.Base(accepted.PathOnDisk), ".diff")
	// Get all pending diffs for this conversation.
	pendingDiffs, err := s.store.ListPendingDiffs(ctx, accepted.ConversationID)
	if err != nil {
		log.Printf("auto-reject: list pending diffs: %v", err)
		return
	}
	for _, sd := range pendingDiffs {
		if sd.ID == accepted.ID {
			continue // skip the accepted diff itself
		}
		// Reject the sibling diff.
		if err := s.store.UpdateDiffStatus(ctx, sd.ID, store.DiffRejected); err != nil {
			log.Printf("auto-reject: update diff %d status: %v", sd.ID, err)
			continue
		}
		_, _ = s.store.AppendEvent(ctx, accepted.ConversationID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action":  "reject",
			"diff_id": sd.ID,
		}))
	}
	// Close all fan-out runs and remove worktrees (except the accepted one
	// which was already retired by retireRunForDiff).
	for _, id := range ids {
		meta := s.runs[id]
		if meta == nil {
			continue
		}
		if meta.runDirID == acceptedRunDir {
			continue // already retired
		}
		_ = s.adapterFor(meta.adapter).Close(ctx, id)
		delete(s.runs, id)
		if err := s.mgr.Remove(meta.worktreePath); err != nil {
			log.Printf("auto-reject: remove worktree %s: %v", meta.worktreePath, err)
		}
	}
	// Clear the fan-out tracking for this conversation.
	delete(s.fanoutRuns, accepted.ConversationID)
}

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

// parseVerdict extracts the verdict (the first line that IS or STARTS WITH
// ACCEPT, REJECT, or NEEDS_FIXES) and the comments (everything after that line).
// A line like "I cannot accept this" must NOT match — only a verdict token
// on its own or as the first word of the line counts. Unparseable output
// degrades to needs_fixes — a review must never silently read as an accept.
func parseVerdict(model, text string) ReviewResult {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		up := strings.ToUpper(strings.TrimSpace(line))
		verdict := ""
		switch {
		case up == "NEEDS_FIXES" || strings.HasPrefix(up, "NEEDS_FIXES ") || strings.HasPrefix(up, "NEEDS FIXES"):
			verdict = "needs_fixes"
		case up == "REJECT" || strings.HasPrefix(up, "REJECT "):
			verdict = "reject"
		case up == "ACCEPT" || strings.HasPrefix(up, "ACCEPT "):
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
	start := time.Now() // M6: distill duration metric (ledger)
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

	// M6: contradiction pass (daemon-side, no LLM). Runs between the note
	// write and the learner, before the epoch moves.
	noteName := fmt.Sprintf("%s-epoch-%d", w.Name, c.Epoch)
	contradictions := s.runContradictionPass(ctx, c.ID, noteName, note, c.Epoch)

	// Learner pass (M4 §2): propose behavior rules from the note just
	// written, journaled before the epoch moves so the propose event's epoch
	// is the distilled note's epoch (c.Epoch pre-increment, not newEpoch).
	// A learner failure degrades to a journaled memory_update and never
	// fails the distill.
	proposals := s.runLearner(ctx, c.ID, noteName, note, c.Epoch)

	newEpoch, err := s.store.IncrementEpoch(ctx, c.ID)
	if err != nil {
		return Response{}, err
	}
	distillEv, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":         "distill",
		"epoch":          newEpoch,
		"wiki_path":      wikiPath,
		"duration_ms":    time.Since(start).Milliseconds(), // M6: ledger metric
		"contradictions": contradictions,                   // M6: contradiction report count
	}))
	if err != nil {
		return Response{}, err
	}

	// M6: ledger append (best-effort, after the distill event so its seq is
	// citable). Section header uses c.Epoch — the distilled note's epoch,
	// not newEpoch (the counter after increment).
	s.journalDistillLedger(ctx, c.ID, c.Epoch, distillEv)
	return Response{WikiPath: wikiPath, Epoch: newEpoch, MemoryProposals: proposals}, nil
}

// journalDistillLedger appends the distill's section to .odo/ledger.md from
// a fresh events scan. Best-effort: a failed ledger write journals
// memory_update{layer:"ledger", cause:"write_failed"} and a failed
// metrics scan journals the same with the underlying list error —
// silently dropping the section would leave an unaccountable hole in the
// ledger (inv 3). The gap journal goes out on a cancel-free copy of ctx:
// when the request's ctx is what failed (client dropped mid-distill), the
// original ctx could never carry the record.
func (s *Server) journalDistillLedger(ctx context.Context, conversationID int64, epoch int, distillEv store.Event) {
	events, lerr := s.store.ListEvents(ctx, conversationID, 0)
	if lerr != nil {
		_, _ = s.store.AppendEvent(context.WithoutCancel(ctx), conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "ledger",
			"cause":  "write_failed",
			"detail": "list_events: " + lerr.Error(),
		}))
		return
	}
	if err := appendLedger(s.projectRoot, fmt.Sprintf("epoch %d", epoch),
		distillLedgerMetrics(events, distillEv, lastRecallCount(events), epoch)); err != nil {
		_, _ = s.store.AppendEvent(ctx, conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "ledger",
			"cause":  "write_failed",
			"detail": err.Error(),
		}))
	}
}

// handlePendingCounts reports, per workstream, the number of pending diffs
// plus which workstreams have a live agent run (M3 §3c sidebar badges).
// Read-only: SQL over diffs + a scan of the in-memory run table.
func (s *Server) handlePendingCounts(ctx context.Context, req Request) (Response, error) {
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("pending_counts: %w", err)
	}
	counts, err := s.store.PendingDiffCountsByWorkstream(ctx, p.ID)
	if err != nil {
		return Response{}, err
	}
	var running []int64
	for _, meta := range s.runs {
		if !meta.finished {
			running = append(running, meta.workstreamID)
		}
	}
	return Response{PendingCounts: counts, RunningWorkstreams: running}, nil
}

// handleLedger returns the .odo/ledger.md content as memory_content (same
// shape as read_pins; "" when the file is absent). The ledger is read-only
// in the UI — the daemon is the only writer. Read-only: no journal writes.
func (s *Server) handleLedger(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("ledger: %w", err)
	}
	return Response{MemoryContent: readFileFull(ledgerPath(s.projectRoot))}, nil
}

// handleContradictions returns the conversation's note-retraction events
// (memory_update{layer:"note", cause:"retract"}) for the wiki browser's
// "⚠ retracted" badges. Read-only: no journal writes.
func (s *Server) handleContradictions(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return Response{}, err
	}
	var out []store.Event
	for _, ev := range events {
		if ev.Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.Layer == "note" && p.Cause == "retract" {
			out = append(out, ev)
		}
	}
	return Response{Events: out}, nil
}

// diffTargetPaths reads the unified diff at pathOnDisk and returns the
// target (b-side) path of each file header: from "+++ b/<path>" lines, or
// the b-side of "diff --git a/<x> b/<y>" when +++ is absent (mode-only
// changes). Malformed headers are skipped — git apply is the authority on
// the patch format; this is an overlay check, not a parser.
func diffTargetPaths(pathOnDisk string) ([]string, error) {
	b, err := os.ReadFile(pathOnDisk)
	if err != nil {
		return nil, err
	}
	var paths []string
	pendingB := "" // b-side of a diff --git header with no +++ line yet
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			if pendingB != "" {
				paths = append(paths, pendingB)
			}
			pendingB = ""
			// `diff --git a/<x> b/<y>` — the b-side follows the last " b/".
			rest := strings.TrimPrefix(line, "diff --git ")
			if i := strings.LastIndex(rest, " b/"); i >= 0 {
				pendingB = rest[i+len(" b/"):]
			}
		case strings.HasPrefix(line, "+++ "):
			pendingB = "" // resolved by this +++ line (even /dev/null)
			target := strings.TrimPrefix(line, "+++ ")
			if i := strings.IndexByte(target, '\t'); i >= 0 {
				target = target[:i] // strip an optional trailing timestamp
			}
			target = strings.TrimSpace(target)
			target = strings.Trim(target, "\"") // git C-quotes paths with non-ASCII bytes (+++ "b/<path>")
			if strings.HasPrefix(target, "b/") {
				paths = append(paths, strings.TrimPrefix(target, "b/"))
			}
		}
	}
	if pendingB != "" {
		paths = append(paths, pendingB)
	}
	return paths, nil
}

// rejectProtectedPaths errs when any target path lives under a protected
// prefix (ADR-0003 invariant 1: agents never write memory). Protected:
// .odo/ (memory.md, memory-archive.md, pins.md, ledger.md, journal.sqlite,
// worktrees) and wiki/ (epoch notes, topics, index.md — derived artifacts
// owned by the daemon, not the agent).
func rejectProtectedPaths(paths []string) error {
	for _, f := range paths {
		if strings.HasPrefix(f, ".odo/") || strings.HasPrefix(f, "wiki/") {
			return fmt.Errorf("diff touches protected path %q (invariant 1: agents never write memory)", f)
		}
	}
	return nil
}

// handleListWiki lists the distilled wiki notes for the conversation's
// workstream, newest epoch first. Read-only: no journal writes.
func (s *Server) handleListWiki(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}
	matches, err := filepath.Glob(filepath.Join(s.projectRoot, "wiki", w.Name+"-epoch-*.md"))
	if err != nil {
		return Response{}, fmt.Errorf("list_wiki: %w", err)
	}
	var notes []WikiNoteInfo
	for _, m := range matches {
		epoch, ok := wikiNoteEpoch(m)
		if !ok {
			continue // skip unparseable names defensively
		}
		fi, err := os.Stat(m)
		if err != nil {
			continue // vanished between glob and stat
		}
		notes = append(notes, WikiNoteInfo{
			Path:       m,
			Name:       strings.TrimSuffix(filepath.Base(m), ".md"),
			Epoch:      epoch,
			ModifiedAt: fi.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Epoch > notes[j].Epoch })
	return Response{WikiNotes: notes}, nil
}

// handleReadWiki returns the content of one memory file: a note under
// <projectRoot>/wiki/ or the global ~/.odo/user.md — anything else is
// rejected (path-traversal guard). A missing wiki note is an error; a
// missing user.md is OK with empty content so the frontend can render a
// create-hint. Read-only: no journal writes.
func (s *Server) handleReadWiki(_ context.Context, req Request) (Response, error) {
	// Class 2: exactly ~/.odo/user.md (the one global file), accepted as the
	// expanded absolute path or as the literal "~/.odo/user.md".
	if home, err := os.UserHomeDir(); err == nil {
		userMemPath := filepath.Join(home, ".odo", "user.md")
		expanded := req.Path
		if strings.HasPrefix(expanded, "~/") {
			expanded = filepath.Join(home, expanded[len("~/"):])
		}
		if filepath.Clean(expanded) == userMemPath {
			b, err := os.ReadFile(userMemPath)
			switch {
			case err == nil:
				return Response{WikiContent: string(b)}, nil
			case os.IsNotExist(err):
				return Response{WikiContent: ""}, nil // frontend renders a create-hint
			default:
				return Response{}, fmt.Errorf("read_wiki: %w", err)
			}
		}
	}

	// Class 1: a file under <projectRoot>/wiki/, no escaping the project.
	clean := filepath.Clean(req.Path)
	rel, relErr := filepath.Rel(s.projectRoot, clean)
	if relErr == nil && !strings.HasPrefix(rel, "..") &&
		(rel == "wiki" || strings.HasPrefix(rel, "wiki"+string(filepath.Separator))) {
		b, err := os.ReadFile(clean)
		if err != nil {
			return Response{}, fmt.Errorf("read_wiki: %s: %w", clean, err)
		}
		return Response{WikiContent: string(b)}, nil
	}
	return Response{}, fmt.Errorf("read_wiki: only files under wiki/ or ~/.odo/user.md are readable, got %q", req.Path)
}

// handleReadMemory returns the contents of the three canonical memory files
// (project memory.md, append-only archive, global user.md) as
// memory_content/archive_content/user_content. The daemon constructs the
// paths itself; req.ProjectRoot must be the bound root (same guard as
// resolveProject). Missing files come back "" — the archive is returned
// uncapped (append-only, never injected). Read-only: no journal writes.
func (s *Server) handleReadMemory(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("read_memory: %w", err)
	}
	resp := Response{
		MemoryContent:  readFileFull(filepath.Join(s.projectRoot, ".odo", memoryFileName)),
		ArchiveContent: readArchive(s.projectRoot),
	}
	if home, err := os.UserHomeDir(); err == nil {
		resp.UserContent = readFileFull(filepath.Join(home, ".odo", "user.md"))
	}
	return resp, nil
}

// handleMemoryProposals returns the pending propose batch for review: the
// memory_propose journaled at the latest distill, unless already consumed by
// a memory_apply (spec §5). Nothing pending → empty response (epoch 0).
func (s *Server) handleMemoryProposals(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return Response{}, err
	}
	batch := findPendingBatch(events)
	if !batch.exists || batch.consumed {
		return Response{}, nil
	}
	return Response{
		Epoch:     batch.epoch,
		Seq:       batch.seq,
		Proposals: batch.proposals,
		Reaffirm:  batch.reaffirm,
	}, nil
}

// handleApplyMemory consumes the pending batch all-or-nothing (spec §5):
// every target is pre-computed in memory before anything hits disk or the
// journal; a user.md overflow refusal writes nothing and leaves the batch
// pending for retry. Per changed layer a memory_update event is journaled
// (rotation and successful retraction are their own causes, spec §6), then
// the review_action apply marker; a second apply errors "already
// applied".
func (s *Server) handleApplyMemory(ctx context.Context, req Request) (Response, error) {
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	events, err := s.store.ListEvents(ctx, c.ID, 0)
	if err != nil {
		return Response{}, err
	}
	batch := findPendingBatch(events)
	if !batch.exists {
		return Response{}, errors.New("apply_memory: no pending batch")
	}
	if batch.consumed {
		return Response{}, fmt.Errorf("apply_memory: epoch %d already applied", req.Epoch)
	}
	if req.Epoch != batch.epoch {
		return Response{}, fmt.Errorf("apply_memory: no pending batch for epoch %d (pending epoch is %d)", req.Epoch, batch.epoch)
	}

	// Resolve + validate every accepted ref against the batch proposals.
	accepted := make([]bool, len(batch.proposals))
	var memAccepted []acceptedRule
	var userAccepted []acceptedUserRule
	for _, a := range req.Accepted {
		if a.Index < 0 || a.Index >= len(batch.proposals) {
			return Response{}, fmt.Errorf("apply_memory: proposal index %d out of range (%d proposals)", a.Index, len(batch.proposals))
		}
		p := batch.proposals[a.Index]
		if a.Target != p.Target {
			return Response{}, fmt.Errorf("apply_memory: proposal %d is target %q, not %q", a.Index, p.Target, a.Target)
		}
		if accepted[a.Index] {
			return Response{}, fmt.Errorf("apply_memory: proposal %d accepted twice", a.Index)
		}
		accepted[a.Index] = true
		switch p.Target {
		case "memory.md":
			memAccepted = append(memAccepted, acceptedRule{
				rule: p.Rule, evidence: p.Evidence, contradicts: p.Contradicts,
			})
		case "user.md":
			// Projects on the proposal are the daemon-verified recurrence set
			// (the LLM's self-tagged list was replaced at vet time).
			userAccepted = append(userAccepted, acceptedUserRule{
				rule: p.Rule, projects: p.Projects,
			})
		default:
			return Response{}, fmt.Errorf("apply_memory: unknown proposal target %q", p.Target)
		}
	}
	// Rejected refs are daemon-computed (every proposal not accepted).
	var rejected []MemoryAccept
	for i, ok := range accepted {
		if !ok {
			rejected = append(rejected, MemoryAccept{Target: batch.proposals[i].Target, Index: i})
		}
	}

	// Pre-compute EVERY target before any write (all-or-nothing).
	memPath := filepath.Join(s.projectRoot, ".odo", memoryFileName)
	oldMem := readFileFull(memPath) // FULL uncapped: the write basis (inv 3)
	memPlan := memoryApplyPlan{content: oldMem}
	memChanged := false
	if len(memAccepted) > 0 || len(batch.reaffirm) > 0 {
		memPlan = planMemoryApply(oldMem, memAccepted, batch.reaffirm, batch.epoch)
		memChanged = memPlan.content != oldMem || memPlan.archiveAppend != ""
	}

	var userPath, oldUser, newUser string
	userChanged := false
	if len(userAccepted) > 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return Response{}, fmt.Errorf("apply_memory: resolve home: %w", err)
		}
		userPath = filepath.Join(home, ".odo", "user.md")
		oldUser = readFileFull(userPath)
		newUser, err = planUserApply(oldUser, userAccepted)
		if err != nil {
			// Refused: nothing written, nothing journaled, the batch
			// stays pending (a retry recomputes from the same proposes).
			return Response{}, fmt.Errorf("apply_memory: %w", err)
		}
		userChanged = newUser != oldUser
	}

	// Writes: archive first, then user.md, memory.md LAST. A mid-sequence
	// failure then leaves the previous memory.md intact, so a retry replans
	// against the ORIGINAL rules — no duplicate appends and no
	// evicted-but-unarchived loss (archive append re-running on retry is a
	// harmless duplicate append-only line, never a loss).
	if memChanged && memPlan.archiveAppend != "" {
		arcPath := filepath.Join(s.projectRoot, ".odo", archiveFileName)
		if err := writeFileAtomic(arcPath, readArchive(s.projectRoot)+memPlan.archiveAppend, 0o644); err != nil {
			return Response{}, fmt.Errorf("apply_memory: append archive: %w", err)
		}
	}
	if userChanged {
		if err := writeFileAtomic(userPath, newUser, 0o600); err != nil {
			return Response{}, fmt.Errorf("apply_memory: write user.md: %w", err)
		}
	}
	if memChanged {
		if err := writeFileAtomic(memPath, memPlan.content, 0o644); err != nil {
			return Response{}, fmt.Errorf("apply_memory: write memory.md: %w", err)
		}
	}

	// Journal per changed layer. Rotation and successful retraction are
	// DISTINCT memory_update causes (spec §6: the UI switch is exhaustive),
	// not clauses folded into the apply detail.
	if memChanged {
		detail := fmt.Sprintf("accepted %d rule(s)", len(memAccepted))
		if memPlan.reaffirmed > 0 {
			detail += fmt.Sprintf("; reaffirmed %d", memPlan.reaffirmed)
		}
		beforeSHA, afterSHA := sha16([]byte(oldMem)), sha16([]byte(memPlan.content))
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":      "memory",
			"cause":      "apply",
			"before_sha": beforeSHA,
			"after_sha":  afterSHA,
			"detail":     detail,
		})); err != nil {
			return Response{}, err
		}
		if len(memPlan.rotated) > 0 {
			if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
				"layer":      "memory",
				"cause":      "rotate",
				"before_sha": beforeSHA,
				"after_sha":  afterSHA,
				"detail": fmt.Sprintf("rotated %d to memory-archive.md (overflow): %s",
					len(memPlan.rotated), strings.Join(memPlan.rotated, " | ")),
			})); err != nil {
				return Response{}, err
			}
		}
		if len(memPlan.retracted) > 0 {
			if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
				"layer":      "memory",
				"cause":      "retract",
				"before_sha": beforeSHA,
				"after_sha":  afterSHA,
				"detail": fmt.Sprintf("retracted %d (conflict): %s",
					len(memPlan.retracted), strings.Join(memPlan.retracted, " | ")),
			})); err != nil {
				return Response{}, err
			}
		}
	}
	for _, unmatched := range memPlan.unmatchedContradicts {
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "memory",
			"cause":  "retract",
			"detail": fmt.Sprintf("no match for contradicts: %q", unmatched),
		})); err != nil {
			return Response{}, err
		}
	}
	if userChanged {
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":      "user",
			"cause":      "apply",
			"before_sha": sha16([]byte(oldUser)),
			"after_sha":  sha16([]byte(newUser)),
			"detail":     fmt.Sprintf("accepted %d rule(s)", len(userAccepted)),
		})); err != nil {
			return Response{}, err
		}
	}

	// Batch-consumed marker (daemon-computed counts, ADR inv 4) — captured
	// so the M6 ledger row can cite its seq.
	applyEv, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":   "memory_apply",
		"epoch":    batch.epoch,
		"accepted": req.Accepted,
		"rejected": rejected,
		"metrics": map[string]int{
			"accepted": len(req.Accepted),
			"rejected": len(rejected),
		},
	}))
	if err != nil {
		return Response{}, err
	}

	// M6: ledger append (best-effort). Separate "(apply)" section: the file
	// is append-only and a later epoch's distill section may already follow
	// the epoch this apply belongs to.
	if err := appendLedger(s.projectRoot, fmt.Sprintf("epoch %d (apply)", batch.epoch), applyLedgerMetrics(applyEv)); err != nil {
		_, _ = s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "ledger",
			"cause":  "write_failed",
			"detail": err.Error(),
		}))
	}
	return Response{Applied: true}, nil
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
		// M7: a trailing partial event is the transient streaming preview —
		// not journaled, not counted, not part of the concatenated output.
		if n := len(evs); n > 0 && evs[n-1].Payload["partial"] == true {
			evs = evs[:n-1]
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
	if runErr != "" {
		return "", errors.New(runErr)
	}
	if out == "" {
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
