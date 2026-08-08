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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/moa"
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

	// mu (M11 P0) guards every piece of in-memory run bookkeeping below it:
	// runs, byConv, distilling, curating, and each runMeta's
	// consumed/previewEvent/finished/errored fields. Handlers doing only
	// store/filesystem work don't take it; distill and curate explicitly
	// drop it around their multi-minute agent runs. wg tracks handleConn
	// goroutines for graceful shutdown (Wait).
	mu         sync.Mutex
	runs       map[string]*runMeta // adapter runID -> meta
	byConv     map[int64]string    // conversationID -> adapter runID (active run)

	distilling map[int64]struct{} // conversations with an in-flight distill (M11 P0)
	curating   bool               // a curate pass is in flight (M11 P0)
	wg         sync.WaitGroup     // active handleConn goroutines (M11 P0)
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
		distilling:   make(map[int64]struct{}),
	}
	s.adapters[""] = ad
	s.adapters["omp"] = ad
	ensureProjectRegistered(projectRoot)
	return s
}

// RegisterAdapter makes ad selectable via the send_message "adapter" field
// under the given name (e.g. "omp-alt" in tests).
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

// Serve accepts connections and handles each on its own goroutine (M11 P0;
// M0 was one connection at a time). Shared run bookkeeping is guarded by
// s.mu. Serve returns when the listener is closed (net.ErrClosed) or on a
// fatal accept error; in-flight handler goroutines keep running — call Wait
// to drain them during shutdown.
func (s *Server) Serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

// Wait blocks until every accepted connection's handler goroutine has
// returned. Call after Serve returns (the listener is closed) to drain
// in-flight requests — e.g. a distill still inside its 10-minute agent run —
// before shutdown cleanup kills agents and closes the journal.
func (s *Server) Wait() {
	s.wg.Wait()
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
	case CmdRenameWorkstream:
		resp, err = s.handleRenameWorkstream(ctx, req)
	case CmdDeleteWorkstream:
		resp, err = s.handleDeleteWorkstream(ctx, req)
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
	case CmdListSkills:
		resp, err = s.handleListSkills(ctx, req)
	case CmdReadSkill:
		resp, err = s.handleReadSkill(ctx, req)
	case CmdUpdateSkill:
		resp, err = s.handleUpdateSkill(ctx, req)
	case CmdDeleteSkill:
		resp, err = s.handleDeleteSkill(ctx, req)
	case CmdLedger:
		resp, err = s.handleLedger(ctx, req)
	case CmdContradictions:
		resp, err = s.handleContradictions(ctx, req)
	case CmdSearchEvents:
		resp, err = s.handleSearchEvents(ctx, req)
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
	// D8: Generate AGENTS.md so OMP reads Odo's project rules as its system
	// prompt. Odo owns the prompt prefix (memory/pins/wiki/skills); AGENTS.md
	// is the bridge that tells OMP to treat Odo's injection as authoritative.
	s.generateAgentsMD()
	return Response{
		Project:      &p,
		Workstream:   &w,
		Conversation: &c,
		Events:       events,
		AgentRunning: new(false),
		Diff:         s.latestDiffInfo(ctx, c.ID),
	}, nil
}

// generateAgentsMD writes an AGENTS.md file in .odo/ so OMP reads
// Odo's project rules as its system prompt. The content is derived from
// .odo/memory.md (project behavior rules) and .odo/pins.md (user-authored
// verbatim statements). If neither file exists, a minimal default is written.
// AGENTS.md is overwritten on every bootstrap so it never drifts.
func (s *Server) generateAgentsMD() {
	var b strings.Builder
	b.WriteString("# AGENTS.md\n\n")
	b.WriteString("This file is auto-generated by the Odo daemon on every bootstrap.\n")
	b.WriteString("Do not edit manually — edit .odo/memory.md and .odo/pins.md instead.\n\n")
	b.WriteString("## Project Rules\n\n")
	b.WriteString("Odo injects current user memory, project rules, pins, wiki, and recalled\n")
	b.WriteString("notes in the prompt prefix. Treat them as authoritative. If OMP's\n")
	b.WriteString("hindsight memory conflicts with the prompt, follow the prompt.\n\n")
	// Append project memory if it exists.
	if data, err := os.ReadFile(filepath.Join(s.projectRoot, ".odo", "memory.md")); err == nil {
		b.WriteString("## Memory\n\n")
		b.Write(data)
		b.WriteString("\n\n")
	}
	// Append pins if they exist.
	if data, err := os.ReadFile(filepath.Join(s.projectRoot, ".odo", "pins.md")); err == nil {
		b.WriteString("## Pins\n\n")
		b.Write(data)
		b.WriteString("\n\n")
	}
	agentsPath := filepath.Join(s.projectRoot, ".odo", "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(b.String()), 0o644); err != nil {
		log.Printf("ipc: generate AGENTS.md: %v", err)
	}
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

// handleRenameWorkstream renames a workstream. The new name is sanitized
// to a git-safe branch name. Returns the updated workstream.
func (s *Server) handleRenameWorkstream(ctx context.Context, req Request) (Response, error) {
	name := sanitizeBranchName(req.Name)
	if name == "" {
		return Response{}, fmt.Errorf("rename_workstream: a usable name is required")
	}
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("rename_workstream: %w", err)
	}
	if err := s.store.RenameWorkstream(ctx, req.WorkstreamID, name); err != nil {
		return Response{}, fmt.Errorf("rename_workstream: %w", err)
	}
	w, err := s.store.GetWorkstream(ctx, req.WorkstreamID)
	if err != nil {
		return Response{}, fmt.Errorf("rename_workstream: refetch: %w", err)
	}
	return Response{Workstream: &w}, nil
}

// handleDeleteWorkstream soft-deletes a workstream. Refuses if the
// workstream has pending diffs. Returns the updated workstream list.
func (s *Server) handleDeleteWorkstream(ctx context.Context, req Request) (Response, error) {
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("delete_workstream: %w", err)
	}
	if err := s.store.DeleteWorkstream(ctx, req.WorkstreamID); err != nil {
		return Response{}, fmt.Errorf("delete_workstream: %w", err)
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

// workstreamGitBranch maps a workstream's stored branch name to the git
// branch its runs check out (M11c). The row stores the bare name (e.g.
// "main"); every git consumer prefixes it with "odo/". "" means detached
// worktrees — legacy workstreams with no branch binding.
func workstreamGitBranch(w store.Workstream) string {
	if w.Branch == nil || *w.Branch == "" {
		return ""
	}
	return "odo/" + *w.Branch
}

// handleSendMessage journals the user message, creates a run worktree, and
// starts the agent in it.
func (s *Server) handleSendMessage(ctx context.Context, req Request) (Response, error) {
	if req.Text == "" {
		return Response{}, fmt.Errorf("send_message: text is required")
	}
	// /panel slash command: route to MoA thinking (3 models via direct API).
	// Must be outside s.mu — the fan-out blocks for up to N×HTTP_TIMEOUT.
	if rest := strings.TrimPrefix(strings.TrimSpace(req.Text), "/panel"); rest != strings.TrimSpace(req.Text) && (strings.HasPrefix(rest, " ") || rest == "") {
		c, err := s.checkConversation(ctx, req.ConversationID)
		if err != nil {
			return Response{}, err
		}
		// Reject /panel while an agent run is active (same invariant as send).
		s.mu.Lock()
		if runID, ok := s.byConv[c.ID]; ok {
			if meta := s.runs[runID]; meta != nil && !meta.finished {
				s.mu.Unlock()
				return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
			}
		}
		if _, ok := s.distilling[c.ID]; ok {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("send_message: distill in progress for conversation %d", c.ID)
		}
		s.mu.Unlock()
		return s.handlePanelQuery(ctx, &c, strings.TrimSpace(rest))
	}
	// /vision slash command: route to K3 (vision-capable) via direct API.
	// Same routing as /panel but single model (K3 only) for image analysis.
	if rest := strings.TrimPrefix(strings.TrimSpace(req.Text), "/vision"); rest != strings.TrimSpace(req.Text) && (strings.HasPrefix(rest, " ") || rest == "") {
		c, err := s.checkConversation(ctx, req.ConversationID)
		if err != nil {
			return Response{}, err
		}
		s.mu.Lock()
		if runID, ok := s.byConv[c.ID]; ok {
			if meta := s.runs[runID]; meta != nil && !meta.finished {
				s.mu.Unlock()
				return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
			}
		}
		if _, ok := s.distilling[c.ID]; ok {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("send_message: distill in progress for conversation %d", c.ID)
		}
		s.mu.Unlock()
		return s.handleVisionQuery(ctx, &c, strings.TrimSpace(rest), req.Attachments)
	}
	// Held for the entire handler (M11 P0): the byConv check and
	// the run-table insert must be one critical section, and adapter.Start is
	// non-blocking so the hold stays short (~200ms).
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	// M8: steering is handled by handleSteering when req.Steer is set.
	if req.Steer {
		return s.handleSteering(ctx, c, req)
	}
	adName := req.Adapter
	if adName == "" {
		adName = "omp"
	}
	ad, ok := s.adapters[adName]
	if !ok {
		if req.Adapter != "" {
			return Response{}, fmt.Errorf("send_message: unknown adapter %q", req.Adapter)
		}
		// Should not happen — "omp" is always registered — but fall back
		// to the default adapter for safety.
		adName, ad = "", s.adapters[""]
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
		}
	}
	// M11 P0 (DS review): reject sends during distill — the distill's
	// unlocked 10-min window would let a new run journal events into
	// the epoch the distill is about to roll.
	if _, ok := s.distilling[c.ID]; ok {
		return Response{}, fmt.Errorf("send_message: distill in progress for conversation %d", c.ID)
	}
	// M11 P3: parallelism cap — reject when too many concurrent runs.
	if cap := resolveMaxConcurrent(); s.activeRunCount() >= cap {
		return Response{}, fmt.Errorf("send_message: %d concurrent runs (cap %d)", s.activeRunCount(), cap)
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

	prompt := buildPrompt(req.Text, req.Attachments, ml.user, ml.project, ml.pins, ml.index, ml.wiki, ml.skills)

	// Setup failures after this point revoke the run with a journaled
	// agent_error so the chat history stays truthful.
	runDirID := worktree.NewRunID()
	wtPath, err := s.mgr.Create(runDirID, workstreamGitBranch(w))
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
	user          string             // ~/.odo/user.md (global principles)
	project       string             // .odo/memory.md (project behavior rules)
	pins          string             // .odo/pins.md (M5: user-authored, verbatim)
	skills        string             // M8: matched skill procedures (keyword-selected)
	skillReceipts []skillReceiptItem // M8: per-skill path + block hash for receipt
	index         string             // wiki/index.md (M5: always-injected)
	wiki          string             // recalled epoch notes block
	recall        []recallItem       // M6: was []string, now per-note with matched terms
	receipt       map[string]string
}

// memoryLayers reads the current memory layers for the workstream and builds
// the recall items plus the sha16 receipt for every non-empty layer
// (per-note hashes cover the exact injected block, header and separator
// included). The query is the user's message text (M6 keyword recall);
// retracted notes (the journal's note-layer retraction set) are excluded.
// Layers absent/empty appear in neither.
func (s *Server) memoryLayers(ctx context.Context, wsName string, conversationID int64, query string) memoryLayers {
	pins := readPins(s.projectRoot)
	sk, skReceipts := loadSkillsForPrompt(s.projectRoot, query)
	ml := memoryLayers{
		user:          readUserMemory(),
		project:       readProjectMemory(s.projectRoot),
		pins:          pins,
		skills:        sk,
		skillReceipts: skReceipts,
		index:         readIndex(s.projectRoot),
		receipt:       map[string]string{},
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
	// M8: per-skill receipt entries (ADR-0003 inv 5).
	for _, sr := range ml.skillReceipts {
		ml.receipt[sr.path] = sr.blockHash
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
	// M8: skill paths injected between pins and wiki index (matching buildPrompt order).
	for _, sr := range ml.skillReceipts {
		add(sr.path)
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
func buildPrompt(text string, attachments []string, userMem, projectMem, pins, index, memory, skills string) string {
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
	if skills != "" {
		b.WriteString("## Relevant skills (procedures)\n\n")
		b.WriteString(skills)
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

// handleSteering journals a steering message for a conversation without
// starting a new run. An active run receives the text via the owning
// adapter's Send; adapters without steering support get a journaled
// agent_error so the user sees why the message stops at the chat. With no
// active run the message is simply journaled. Caller holds s.mu (called from
// handleSendMessage).
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
	if ok && meta != nil && !meta.finished {
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
	// No active run: the steering message is journaled only.
	return Response{Event: &ev}, nil
}

// handleCancel SIGKILLs the conversation's active run through its adapter
// and journals agent_error{cancelled by user} so the chat history records
// the user's stop. The run is deliberately left unfinished: the normal
// drain path observes the dead process on the next poll, journals the
// adapter's own terminal event, and extracts whatever partial diff exists
// (ADR-0001: partial changes stay reviewable).
func (s *Server) handleCancel(ctx context.Context, req Request) (Response, error) {
	// Held for the entire handler (M11 P0): run-table reads, adapter.Cancel,
	// and the journal write stay consistent against concurrent drains.
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	// Primary single run.
	runID, ok := s.byConv[c.ID]
	meta := s.runs[runID]
	if ok && meta != nil && !meta.finished {
		if err := s.adapterFor(meta.adapter).Cancel(ctx, runID); err != nil {
			return Response{}, fmt.Errorf("cancel: %w", err)
		}
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentError,
			mustJSON(map[string]interface{}{"error": "cancelled by user"})); err != nil {
			return Response{}, err
		}
		return Response{}, nil
	}
	return Response{}, fmt.Errorf("cancel: no active run for conversation %d", c.ID)
}

// handlePollEvents drains finished-run adapter events into the journal,
// extracts each run's diff once, then returns journal events after afterSeq.
func (s *Server) handlePollEvents(ctx context.Context, req Request) (Response, error) {
	// Held for the entire handler (M11 P0): drainRun advances the consumed
	// cursor and sets finished, so concurrent pollers of the same run must
	// serialize — without this two polls journal the same adapter events.
	s.mu.Lock()
	defer s.mu.Unlock()
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

	events, err := s.store.ListEvents(ctx, c.ID, req.AfterSeq)
	if err != nil {
		return Response{}, err
	}
	agentRunning := false
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
		Diffs:        s.pendingDiffInfos(ctx, c.ID),
	}, nil
}

// activeRunCount returns the number of non-finished runs across all
// conversations — the daemon-wide concurrency level used by the cap.
// Caller must hold s.mu.
func (s *Server) activeRunCount() int {
	n := 0
	for _, meta := range s.runs {
		if !meta.finished {
			n++
		}
	}
	return n
}

// maxConcurrentDefault is used when prefs.md has no max_concurrent_runs line.
const maxConcurrentDefault = 4

// resolveMaxConcurrent reads the cap from prefs.md, falling back to the
// default when absent or unparseable.
func resolveMaxConcurrent() int {
	v := adapter.LoadPrefsRaw("max_concurrent_runs")
	if v == "" {
		return maxConcurrentDefault
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return maxConcurrentDefault
	}
	return n
}

// drainRun pulls new adapter events into the journal once. When the terminal
// event arrives it extracts the worktree diff exactly once and records it.
// Caller holds s.mu (called from handlePollEvents): consumed/finished must
// not advance concurrently or the same events journal twice.
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
		log.Printf("ipc: drainRun: InsertDiff failed: %v", err)
		s.store.AppendEvent(ctx, meta.conversationID, store.EventAgentError, mustJSON(map[string]interface{}{
			"error": "diff save failed: " + err.Error(),
		}))
		meta.finished = true
		return nil
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

	// Retire the reviewed run under the mutex (M11 P0): map deletes,
	// adapter.Close, and worktree removal must not interleave with
	// concurrent poll drains of the same run.
	s.mu.Lock()
	s.retireRunForDiff(ctx, d)
	if action == "accept" {
		// M11c: the run's worktree is retired, so no live worktree holds the
		// workstream branch — advance it past the accept commit.
		s.advanceWorkstreamBranch(ctx, d)
	}
	s.mu.Unlock()

	return Response{DiffID: diffID, Applied: applied}, nil
}

// advanceWorkstreamBranch points the workstream's odo/<name> branch at the
// main HEAD that now includes the accepted diff, so the branch accumulates
// changes across runs. Caller holds s.mu (handleDiffAction's locked
// section): the retire calls above must free the branch first — git refuses
// `branch -f` while the branch is checked out in a live worktree. Failures
// are non-fatal: a concurrent run on another conversation of the same
// workstream can still hold the branch, and the next run's
// `git worktree add -B` resets the ref forward regardless.
func (s *Server) advanceWorkstreamBranch(ctx context.Context, d store.Diff) {
	c, err := s.store.GetConversation(ctx, d.ConversationID)
	if err != nil {
		log.Printf("accept_diff: workstream branch advance: %v", err)
		return
	}
	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		log.Printf("accept_diff: workstream branch advance: %v", err)
		return
	}
	if branch := workstreamGitBranch(w); branch != "" {
		if err := git.AdvanceBranch(s.projectRoot, branch); err != nil {
			log.Printf("accept_diff: workstream branch advance (non-fatal): %v", err)
		}
	}
}

// retireRunForDiff releases resources after a diff review. Caller holds
// s.mu (handleDiffAction's locked section).
func (s *Server) retireRunForDiff(ctx context.Context, d store.Diff) {
	s.retireRun(ctx, d.ConversationID)
}

// retireRun closes the adapter run and removes the worktree for a concluded
// review. After a restart there is no in-memory run; the workstream's bound
// worktree path is the fallback. Removal failures are logged, not fatal — the
// review already happened and worktrees are reaped by `git worktree prune`.
// Caller holds s.mu (via retireRunForDiff).
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

// reviewModel is one parsed entry of the prefs.md `review:` line.
type reviewModel struct {
	model    string
	provider string
}

// handleReviewDiff implements review_diff: the diff is sent to every
// model on the prefs.md `review:` line via direct HTTP API (moa.Query),
// in parallel. The call blocks until all finish — like distill, it
// blocks the single-connection daemon.
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
			reviews[i] = s.reviewWithModel(ctx, m, reviewPrompt(string(content)))
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

// reviewWithModel runs a review prompt through one model via the direct
// HTTP API client (moa.Query) and parses the verdict. The prompt is
// pre-built by the caller (handleReviewDiff wraps with reviewPrompt;
// gateSkillProposals wraps with skillReviewPrompt). A failed run
// degrades to needs_fixes with the error as comments: a review that
// never happened must not read as an accept.
func (s *Server) reviewWithModel(ctx context.Context, m reviewModel, prompt string) ReviewResult {
	label := m.model + "@" + m.provider
	client := moa.NewClientFromEnv("", "")
	system := "You are a code reviewer. Review the following diff and provide your verdict."
	text, err := client.Query(ctx, m.model, system, prompt)
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

// handlePanelQuery routes a /panel prompt to N MoA models via the direct API
// client. It journals the user message and all model replies as events, then
// returns the combined replies. No worktree, no diff — read-only thinking.
func (s *Server) handlePanelQuery(ctx context.Context, c *store.Conversation, text string) (Response, error) {
	if text == "" {
		return Response{}, fmt.Errorf("/panel: prompt text is required after /panel")
	}
	models := parseReviewModels(adapter.LoadPrefsRaw("review"))
	if len(models) == 0 {
		return Response{}, errors.New("No review models configured for /panel. Set the 'review:' line in prefs.md.")
	}

	// Journal the user message.
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(map[string]interface{}{
		"text": "/panel " + text,
	})); err != nil {
		return Response{}, err
	}

	// Fan out to N models via direct API (parallel, no OMP process).
	client := moa.NewClientFromEnv("", "")
	results := make([]PanelResult, len(models))
	var wg sync.WaitGroup
	for i, m := range models {
		wg.Add(1)
		go func() {
			defer wg.Done()
			label := m.model + "@" + m.provider
			resp, err := client.Query(ctx, m.model, "You are an expert advisor. Provide a thorough, independent analysis.", text)
			if err != nil {
				results[i] = PanelResult{Model: label, Error: err.Error()}
				return
			}
			results[i] = PanelResult{Model: label, Text: resp}
		}()
	}
	wg.Wait()

	// Journal the panel responses as an agent_text event.
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentText, mustJSON(map[string]interface{}{
		"text":   formatPanelResults(results),
		"panel":  true,
		"models": results,
	})); err != nil {
		return Response{}, err
	}
	// Journal agent_done so the GUI knows the run is complete.
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentDone, mustJSON(map[string]interface{}{
		"panel": true,
	})); err != nil {
		return Response{}, err
	}

	return Response{OK: true}, nil
}

// PanelResult is one model's response from a /panel query.
type PanelResult struct {
	Model string `json:"model"`
	Text  string `json:"text"`
	Error string `json:"error,omitempty"`
}

// formatPanelResults renders the N model responses as readable text for the
// journal's agent_text event.
func formatPanelResults(results []PanelResult) string {
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString("## ")
		b.WriteString(r.Model)
		b.WriteString("\n\n")
		if r.Error != "" {
			b.WriteString("(error: ")
			b.WriteString(r.Error)
			b.WriteString(")")
		} else {
			b.WriteString(r.Text)
		}
	}
	return b.String()
}

// parseVerdict extracts the verdict (the first line that IS or STARTS WITH
// ACCEPT, REJECT, or NEEDS_FIXES) and the comments (everything after that line).
// handleVisionQuery routes a /vision prompt to K3 (the only vision-capable
// model on the gateway) via direct API. Unlike /panel which fans out to N
// models, /vision uses a single model because GLM/DS lack vision capability.
// The prompt text is sent to K3 via direct HTTP API. Image content blocks
// are sent as Anthropic image content blocks when attachments are provided.
func (s *Server) handleVisionQuery(ctx context.Context, c *store.Conversation, text string, attachments []string) (Response, error) {
	if text == "" {
		return Response{}, fmt.Errorf("/vision: prompt text is required after /vision")
	}
	// K3 is the only vision-capable model (confirmed in ~/.omp/agent/models.yml).
	const visionModel = "t9s/kimi-k3"

	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(map[string]interface{}{
		"text": "/vision " + text,
	})); err != nil {
		return Response{}, err
	}

	client := moa.NewClientFromEnv("", "")
	system := "You are a vision-capable coding assistant. Analyze the image or screenshot described in the prompt. Identify visual issues, layout problems, or design suggestions."
	var resp string
	var err error
	if len(attachments) > 0 {
		resp, err = client.QueryWithImages(ctx, visionModel, system, text, attachments)
	} else {
		resp, err = client.Query(ctx, visionModel, system, text)
	}

	var resultText string
	if err != nil {
		resultText = "(vision error: " + err.Error() + ")"
	} else {
		resultText = "## " + visionModel + "\n\n" + resp
	}

	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentText, mustJSON(map[string]interface{}{
		"text":   resultText,
		"vision": true,
	})); err != nil {
		return Response{}, err
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentDone, mustJSON(map[string]interface{}{
		"vision": true,
	})); err != nil {
		return Response{}, err
	}

	return Response{OK: true}, nil
}

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
// default adapter in a throwaway directory — it blocks only its own
// connection (M11 P0) and never touches the user's working tree. Old events
// stay in the append-only journal; only the epoch counter moves (ADR-0002).
func (s *Server) handleDistill(ctx context.Context, req Request) (Response, error) {
	start := time.Now() // M6: distill duration metric (ledger)
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	// Reserve this conversation's distill slot under the mutex (M11 P0), then
	// drop the lock for the 10-minute agent run so other connections
	// (poll_events, cancel, …) stay responsive throughout the distill.
	s.mu.Lock()
	if _, ok := s.distilling[c.ID]; ok {
		s.mu.Unlock()
		return Response{}, fmt.Errorf("distill: already in progress for conversation %d", c.ID)
	}
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("distill: agent still running for conversation %d", c.ID)
		}
	}
	s.distilling[c.ID] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.distilling, c.ID)
		s.mu.Unlock()
	}()
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

	// Learner pass (M4 §2 + M9): propose behavior rules and skill procedures
	// from the note just written. runLearner no longer journals —
	// handleDistill journals after gating the skill proposals. A learner
	// failure degrades to a journaled memory_update and never fails the
	// distill.
	proposals, reaffirm, stats, _ := s.runLearner(ctx, c.ID, noteName, note, c.Epoch)

	// M9: gate skill proposals through tri-model review. Non-skill proposals
	// (memory.md, user.md) go straight to the batch; skills are partitioned
	// by the gate into auto_discard (dropped + journaled) and human_gate
	// (kept with reviews, included in the batch).
	nonSkills, skillProposals := splitSkillProposals(proposals)
	var humanGateSkills []MemoryProposal
	if len(skillProposals) > 0 {
		models := parseReviewModels(adapter.LoadPrefsRaw("review"))
		gateResults := s.gateSkillProposals(ctx, skillProposals, models, note)
		for _, gr := range gateResults {
			if gr.Tier == "auto_discard" {
				// Journaled as skill_gate event (auditable, never in the batch).
				if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
					"action":  "skill_gate",
					"epoch":   c.Epoch,
					"name":    gr.Proposal.Name,
					"tier":    "auto_discard",
					"reviews": gr.Reviews,
				})); err != nil {
					// Fallback: journal a memory_update so the discard is never
					// invisible (the skill_gate event exists for auditability).
					_, _ = s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
						"layer":  "skills",
						"cause":  "gate_journal_failed",
						"detail": fmt.Sprintf("auto_discard %s: %s", gr.Proposal.Name, err.Error()),
					}))
				}
			} else {
				// human_gate: attach reviews, include in the batch.
				p := gr.Proposal
				p.Reviews = gr.Reviews
				humanGateSkills = append(humanGateSkills, p)
			}
		}
	}

	// Journal memory_propose with non-skills + human_gate skills.
	// If zero surviving proposals total, skip (same as today's len==0 check).
	batchProposals := append(nonSkills, humanGateSkills...)
	if len(batchProposals) > 0 {
		payload := map[string]interface{}{
			"action":    "memory_propose",
			"epoch":     c.Epoch,
			"proposals": batchProposals,
			"reaffirm":  reaffirm,
			"stats":     stats,
		}
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(payload)); err != nil {
			_, _ = s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
				"layer":  "learner",
				"cause":  "failed",
				"detail": fmt.Sprintf("journal memory_propose: %s", err.Error()),
			}))
		}
	}

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
	return Response{WikiPath: wikiPath, Epoch: newEpoch, MemoryProposals: len(batchProposals)}, nil
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
	s.mu.Lock() // M11 P0: the in-memory run table is shared
	for _, meta := range s.runs {
		if !meta.finished {
			running = append(running, meta.workstreamID)
		}
	}
	s.mu.Unlock()
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

// handleSearchEvents searches event payloads across all active workstreams
// in the project for the given query. Returns matches ordered newest-first.
func (s *Server) handleSearchEvents(ctx context.Context, req Request) (Response, error) {
	if req.Text == "" {
		return Response{}, fmt.Errorf("search_events: query is required")
	}
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("search_events: %w", err)
	}
	results, err := s.store.SearchEvents(ctx, p.ID, req.Text, 100)
	if err != nil {
		return Response{}, fmt.Errorf("search_events: %w", err)
	}
	return Response{SearchResults: results}, nil
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
	var skillWrites []skillWrite // M9: pre-computed skill file writes
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
		case "skills":
			// M9: skill proposals write to .odo/skills/<name>.md. Use the
			// vetted p.Name directly (NOT re-parsed frontmatter — TOCTOU risk).
			if p.Name == "" {
				return Response{}, fmt.Errorf("apply_memory: skill proposal %d has empty name", a.Index)
			}
			fname := filepath.Base(p.Name)
			if !strings.HasSuffix(fname, ".md") {
				fname += ".md"
			}
			if fname == "" || strings.Contains(fname, "..") {
				return Response{}, fmt.Errorf("apply_memory: invalid skill name: %s", p.Name)
			}
			target := filepath.Join(s.projectRoot, ".odo", "skills", fname)
			skillWrites = append(skillWrites, skillWrite{path: target, content: p.Rule})
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
	// M9: write skill files before memory.md (memory.md is still last for
	// convergence). Skill writes are idempotent by overwrite (atomic rename,
	// same content = no-op).
	for _, sw := range skillWrites {
		if err := writeFileAtomic(sw.path, sw.content, 0o644); err != nil {
			return Response{}, fmt.Errorf("apply_memory: write skill %s: %w", sw.path, err)
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
	// M9: journal one memory_update per skill write.
	for _, sw := range skillWrites {
		if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "skills",
			"cause":  "applied",
			"detail": fmt.Sprintf("wrote %s", filepath.Base(sw.path)),
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
// agent_text output. Distill uses it (review migrated to moa.Query in D5).
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

// pendingDiffInfos returns all pending diffs for the conversation with
// their content. Newest-first ordering matches the review flow.
func (s *Server) pendingDiffInfos(ctx context.Context, conversationID int64) []DiffInfo {
	diffs, err := s.store.ListPendingDiffs(ctx, conversationID)
	if err != nil || len(diffs) == 0 {
		return nil
	}
	out := make([]DiffInfo, 0, len(diffs))
	for _, d := range diffs {
		info := DiffInfo{ID: d.ID, Status: d.Status, Path: d.PathOnDisk}
		if b, err := os.ReadFile(d.PathOnDisk); err == nil {
			info.Content = string(b)
		}
		out = append(out, info)
	}
	return out
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

// M8 (Skills): handleListSkills returns metadata for all discovered skills
// (global ~/.odo/skills/*.md + project .odo/skills/*.md). Read-only.
func (s *Server) handleListSkills(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("list_skills: %w", err)
	}
	entries := scanSkills(s.projectRoot)
	var infos []SkillInfo
	for _, e := range entries {
		infos = append(infos, e.info)
	}
	return Response{OK: true, Skills: infos}, nil
}

// handleReadSkill returns the full markdown body of one skill file.
func (s *Server) handleReadSkill(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("read_skill: %w", err)
	}
	if req.Path == "" {
		return Response{}, fmt.Errorf("read_skill: path is required")
	}
	// Before building candidates, clean the path and reject traversal.
	// The GUI sends bare filenames, never absolute paths.
	name := filepath.Clean(req.Path)
	if strings.Contains(name, "..") || filepath.IsAbs(name) {
		return Response{}, fmt.Errorf("read_skill: invalid path: %s", req.Path)
	}
	// Resolve the path: try project skills dir first, then global.
	home, _ := os.UserHomeDir()
	base := filepath.Base(name)
	candidates := []string{
		filepath.Join(s.projectRoot, ".odo", "skills", base),
		filepath.Join(home, ".odo", "skills", base),
	}
	for _, c := range candidates {
		b, err := os.ReadFile(c)
		if err == nil {
			return Response{OK: true, SkillContent: string(b)}, nil
		}
	}
	return Response{}, fmt.Errorf("read_skill: skill file not found: %s", req.Path)
}

// handleUpdateSkill writes (creates or overwrites) a skill file. The scope
// ("global" or "project") is passed explicitly on the wire — the daemon
// never infers scope from path prefix (K3 H3 fix). This is the
// human-in-the-loop write path — the daemon never auto-writes skills
// (ADR-0003 invariant).
func (s *Server) handleUpdateSkill(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("update_skill: %w", err)
	}
	if req.Name == "" {
		return Response{}, fmt.Errorf("update_skill: name is required")
	}
	if req.Text == "" {
		return Response{}, fmt.Errorf("update_skill: content is required")
	}
	// Determine target dir by explicit scope (not path inference).
	var dir string
	if req.Scope == "global" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".odo", "skills")
	} else {
		dir = filepath.Join(s.projectRoot, ".odo", "skills")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Response{}, fmt.Errorf("update_skill: mkdir: %w", err)
	}
	// Sanitize: strip directory components from name to prevent path traversal.
	fname := filepath.Base(req.Name)
	if !strings.HasSuffix(fname, ".md") {
		fname += ".md"
	}
	if strings.Contains(fname, "..") {
		return Response{}, fmt.Errorf("update_skill: invalid name: %s", req.Name)
	}
	target := filepath.Join(dir, fname)
	if err := writeFileAtomic(target, req.Text, 0o644); err != nil {
		return Response{}, fmt.Errorf("update_skill: write: %w", err)
	}
	return Response{OK: true}, nil
}

// handleDeleteSkill removes a skill file. The scope ("global" or "project")
// is passed explicitly on the wire. The filename is sanitized via
// filepath.Base to prevent path traversal. Only known skills directories
// are targeted.
func (s *Server) handleDeleteSkill(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("delete_skill: %w", err)
	}
	if req.Name == "" {
		return Response{}, fmt.Errorf("delete_skill: name is required")
	}
	var dir string
	if req.Scope == "global" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".odo", "skills")
	} else {
		dir = filepath.Join(s.projectRoot, ".odo", "skills")
	}
	fname := filepath.Base(req.Name)
	if !strings.HasSuffix(fname, ".md") {
		fname += ".md"
	}
	if strings.Contains(fname, "..") {
		return Response{}, fmt.Errorf("delete_skill: invalid name: %s", req.Name)
	}
	target := filepath.Join(dir, fname)
	if err := os.Remove(target); err != nil {
		return Response{}, fmt.Errorf("delete_skill: %w", err)
	}
	return Response{OK: true}, nil
}
