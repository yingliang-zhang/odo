package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

// subScriptAdapter scripts the OMP side of a spawn: Start captures
// workdir+prompt (and can simulate the child's byte writes via onStart),
// Events replays queued batches per start order, Close records retires.
type subScriptAdapter struct {
	mu      sync.Mutex
	starts  []subStart
	onStart func(dir string)
	closed  []string
	batches [][]adapter.AgentEvent
}

type subStart struct{ dir, prompt string }

func (a *subScriptAdapter) Start(_ context.Context, workdir, prompt string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starts = append(a.starts, subStart{workdir, prompt})
	if a.onStart != nil {
		a.onStart(workdir)
	}
	return fmt.Sprintf("subrun-%d", len(a.starts)), nil
}
func (a *subScriptAdapter) Send(context.Context, string, string) error { return nil }
func (a *subScriptAdapter) Events(_ context.Context, runID string, afterSeq int) ([]adapter.AgentEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var idx int
	if _, err := fmt.Sscanf(runID, "subrun-%d", &idx); err != nil || idx < 1 || idx > len(a.batches) {
		return nil, nil
	}
	batch := a.batches[idx-1]
	if afterSeq >= len(batch) {
		return nil, nil
	}
	return append([]adapter.AgentEvent(nil), batch[afterSeq:]...), nil
}
func (a *subScriptAdapter) Cancel(context.Context, string) error { return nil }
func (a *subScriptAdapter) Close(_ context.Context, runID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = append(a.closed, runID)
	return nil
}

// subServer is the direct-call fork of startRig for subagent tests:
// scripted default adapter, auto/liveness dark-launched, deterministic
// teardown mirroring rig.stop's join order.
func subServer(t *testing.T, root string, ad *subScriptAdapter) (*Server, *store.Store) {
	t.Helper()
	if os.Getenv("ODO_REGISTRY_PATH") == "" {
		t.Setenv("ODO_REGISTRY_PATH", filepath.Join(t.TempDir(), "projects.json"))
	}
	mgr := worktree.NewManager(root)
	if err := mgr.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	st, err := store.Open(filepath.Join(mgr.StateDir(), "journal.sqlite"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv := NewServer(st, root, ad, mgr)
	srv.autoDisabled = true
	srv.livenessDisabled.Store(true)
	t.Cleanup(func() {
		srv.stopLiveness()
		srv.stopAutoDistill()
		srv.distillWG.Wait()
		srv.recoverWG.Wait()
		srv.sealLandAndReleasePins()
		srv.landWG.Wait()
		srv.curateWG.Wait()
		_ = ad.Close(context.Background(), "drain")
		if err := st.Close(); err != nil {
			t.Fatalf("store close: %v", err)
		}
	})
	return srv, st
}

func seedConv(t *testing.T, st *store.Store, root string) store.Conversation {
	t.Helper()
	ctx := context.Background()
	p, err := st.CreateOrGetProject(ctx, root, filepath.Base(root))
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	w, err := st.CreateOrGetWorkstream(ctx, p.ID, "main")
	if err != nil {
		t.Fatalf("workstream: %v", err)
	}
	c, err := st.CreateConversation(ctx, w.ID, "")
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if _, err := st.AppendEvent(ctx, c.ID, store.EventUserMessage, `{"text":"parent goal"}`); err != nil {
		t.Fatalf("append: %v", err)
	}
	return c
}

func doneEvent(text string) adapter.AgentEvent {
	return adapter.AgentEvent{Type: store.EventAgentDone, Payload: map[string]interface{}{"summary": text}}
}

// TestSpawnSubagentIsolatesAndReports is the borrow's core contract: an
// isolated "sub-" worktree, the full report journaled into the PARENT
// conversation with the subagent_id marker, the isolated-side bytes never
// in the parent's checkout, and the extracted diff registered as a
// subagent PROPOSAL (never an auto-land candidate).
func TestSpawnSubagentIsolatesAndReports(t *testing.T) {
	root := initRepo(t)
	ad := &subScriptAdapter{
		onStart: func(dir string) {
			if err := os.WriteFile(filepath.Join(dir, "sub-only.txt"), []byte("sub grew this\n"), 0o644); err != nil {
				t.Errorf("child write: %v", err)
			}
		},
		batches: [][]adapter.AgentEvent{{
			{Type: store.EventAgentText, Payload: map[string]interface{}{"text": "report: wrote sub-only.txt"}},
			doneEvent("report: wrote sub-only.txt"),
		}},
	}
	srv, st := subServer(t, root, ad)
	ctx := context.Background()
	c := seedConv(t, st, root)

	resp, err := srv.handleSpawnSubagent(ctx, Request{ConversationID: c.ID, Goal: "grow a file", Context: "the parent needs this fact"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if resp.Subagent == nil {
		t.Fatal("response carries no subagent row")
	}
	subID := resp.Subagent.SubagentID
	if !strings.HasPrefix(subID, "sub-") {
		t.Errorf("subagent id = %q, want sub- prefix", subID)
	}

	// Prompt assembly: Context section > Goal > isolation contract —
	// nothing of the parent's durable memory layers.
	if len(ad.starts) != 1 {
		t.Fatalf("adapter starts = %d", len(ad.starts))
	}
	prompt := ad.starts[0].prompt
	if !strings.Contains(prompt, "## Context\n\nthe parent needs this fact") {
		t.Error("prompt misses the prepended Context section")
	}
	if !strings.Contains(prompt, "## Goal\n\ngrow a file") {
		t.Error("prompt misses the Goal section")
	}
	if !strings.Contains(prompt, "SUBAGENT") || !strings.Contains(prompt, "never landed automatically") {
		t.Error("prompt misses the isolation contract")
	}

	// Journaled spawn row: goal/context/worktree identity.
	spawned := findSubRow(t, st, c.ID, store.EventSubagentSpawned)
	var sp struct {
		ID     string `json:"subagent_id"`
		Goal   string `json:"goal"`
		Ctx    string `json:"context"`
		WtPath string `json:"worktree_path"`
		RunDir string `json:"run_dir_id"`
	}
	if err := json.Unmarshal(spawned.Payload, &sp); err != nil {
		t.Fatalf("spawn payload: %v", err)
	}
	if sp.ID != subID || sp.Goal != "grow a file" || sp.Ctx != "the parent needs this fact" || sp.WtPath == "" || sp.RunDir != subID {
		t.Errorf("spawn payload = %+v", sp)
	}
	// The recursion marker landed in the child's git dir (never in the diff).
	if _, err := os.Stat(filepath.Join(root, ".git", "worktrees", subID, "odo_subagent")); err != nil {
		t.Errorf("recursion marker missing: %v", err)
	}

	// Drain → report rows in the PARENT journal, tag-propagated agent text.
	if err := srv.drainSubAgentsLocked(ctx, 0); err != nil {
		t.Fatalf("drain: %v", err)
	}
	done := findSubRow(t, st, c.ID, store.EventSubagentDone)
	var dp struct {
		ID       string `json:"subagent_id"`
		ExitCode int    `json:"exit_code"`
		Summary  string `json:"summary"`
		DiffID   int64  `json:"diff_id"`
		DiffPath string `json:"diff_path"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(done.Payload, &dp); err != nil {
		t.Fatalf("done payload: %v", err)
	}
	if dp.ID != subID || dp.ExitCode != 0 || dp.Summary != "report: wrote sub-only.txt" || dp.DiffID == 0 || dp.DiffPath == "" || dp.Error != "" {
		t.Errorf("done payload = %+v", dp)
	}
	// The child's text row rides the tag.
	texts := subTaggedEvents(t, st, c.ID, store.EventAgentText)
	if len(texts) != 1 || texts[0].tag != subID {
		t.Errorf("tagged agent_text rows = %+v", texts)
	}
	// The diff is a PENDING subagent row (the human/agent decides).
	d, err := st.GetDiff(ctx, dp.DiffID)
	if err != nil {
		t.Fatalf("get diff: %v", err)
	}
	if d.Status != store.DiffPending || d.SubagentID == nil || *d.SubagentID != subID {
		t.Errorf("diff = status %q marker %v", d.Status, d.SubagentID)
	}
	if d.WorktreePath == nil || *d.WorktreePath == "" {
		t.Error("diff lost its worktree binding — accept/reject cannot retire it")
	}
	// Isolation contract: the child's bytes exist ONLY in its own
	// worktree, never in the parent's checkout.
	if _, err := os.Stat(filepath.Join(root, "sub-only.txt")); !os.IsNotExist(err) {
		t.Error("subagent bytes leaked into the parent checkout")
	}
	if _, err := os.Stat(filepath.Join(sp.WtPath, "sub-only.txt")); err != nil {
		t.Error("child's write missing from its isolated worktree")
	}
	// Settle retired the adapter state and freed the registry.
	if len(ad.closed) != 1 {
		t.Errorf("adapter closes = %v, want the run retired once", ad.closed)
	}
	if len(srv.subagents) != 0 {
		t.Errorf("registry holds %d subs after settle", len(srv.subagents))
	}
}

// TestSpawnSubagentNoDiffRetiresWorktree: a report-only child (no bytes
// written) finishes with a summary, no diff, and its worktree is retired
// immediately (drainRun's no-diff retire precedent).
func TestSpawnSubagentNoDiffRetiresWorktree(t *testing.T) {
	root := initRepo(t)
	ad := &subScriptAdapter{
		batches: [][]adapter.AgentEvent{{doneEvent("inspection complete")}},
	}
	srv, st := subServer(t, root, ad)
	ctx := context.Background()
	c := seedConv(t, st, root)

	resp, err := srv.handleSpawnSubagent(ctx, Request{ConversationID: c.ID, Goal: "inspect"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := srv.drainSubAgentsLocked(ctx, 0); err != nil {
		t.Fatalf("drain: %v", err)
	}
	done := findSubRow(t, st, c.ID, store.EventSubagentDone)
	var dp struct {
		DiffID int64 `json:"diff_id"`
	}
	if err := json.Unmarshal(done.Payload, &dp); err != nil {
		t.Fatalf("done payload: %v", err)
	}
	if dp.DiffID != 0 {
		t.Errorf("report-only child registered diff %d", dp.DiffID)
	}
	if _, err := os.Stat(resp.Subagent.WorktreePath); !os.IsNotExist(err) {
		t.Error("empty child worktree survived — the no-diff retire never ran")
	}
	pending, err := st.ListPendingDiffs(ctx, c.ID)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending diffs = %v, want none", pending)
	}
}

// TestSpawnSubagentErroredRunStillOffersPartial: agent_error journals
// exit_code 1 yet the partial change set extracts and registers —
// ADR-0001 posture (partial changes stay reviewable).
func TestSpawnSubagentErroredRunStillOffersPartial(t *testing.T) {
	root := initRepo(t)
	ad := &subScriptAdapter{
		onStart: func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, "partial.txt"), []byte("half work\n"), 0o644)
		},
		batches: [][]adapter.AgentEvent{{
			{Type: store.EventAgentError, Payload: map[string]interface{}{"error": "boom"}},
		}},
	}
	srv, st := subServer(t, root, ad)
	ctx := context.Background()
	c := seedConv(t, st, root)

	if _, err := srv.handleSpawnSubagent(ctx, Request{ConversationID: c.ID, Goal: "fail mid-way"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := srv.drainSubAgentsLocked(ctx, 0); err != nil {
		t.Fatalf("drain: %v", err)
	}
	done := findSubRow(t, st, c.ID, store.EventSubagentDone)
	var dp struct {
		ExitCode int    `json:"exit_code"`
		DiffID   int64  `json:"diff_id"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(done.Payload, &dp); err != nil {
		t.Fatalf("done payload: %v", err)
	}
	if dp.ExitCode != 1 || dp.DiffID == 0 || dp.Error != "" {
		t.Errorf("errored settle = %+v — want exit 1 with the partial diff still offered", dp)
	}
}

// TestSpawnSubagentRefusals: the admission gates — empty goal, the
// one-level recursion marker, a missing conversation, and the
// simultaneous-sub cap.
func TestSpawnSubagentRefusals(t *testing.T) {
	root := initRepo(t)
	ad := &subScriptAdapter{}
	srv, st := subServer(t, root, ad)
	ctx := context.Background()
	c := seedConv(t, st, root)

	if _, err := srv.handleSpawnSubagent(ctx, Request{ConversationID: c.ID, Goal: "  "}); err == nil {
		t.Error("empty goal admitted")
	}
	if _, err := srv.handleSpawnSubagent(ctx, Request{ConversationID: c.ID, Goal: "x", SubagentID: "sub-parent"}); err == nil ||
		!strings.Contains(err.Error(), "one level of isolation") {
		t.Errorf("recursion marker admitted: %v", err)
	}
	if _, err := srv.handleSpawnSubagent(ctx, Request{ConversationID: 9999, Goal: "x"}); err == nil {
		t.Error("unknown conversation admitted")
	}
	if _, err := srv.handleSpawnSubagent(ctx, Request{Goal: "x", Path: "/no/such/worktree"}); err == nil ||
		!strings.Contains(err.Error(), "conversation_id") {
		t.Errorf("stale worktree path admitted: %v", err)
	}
	// Cap: the 9th live child refuses; nothing about the registry leaks.
	for range subAgentMaxActive {
		if _, err := srv.handleSpawnSubagent(ctx, Request{ConversationID: c.ID, Goal: "queued child"}); err != nil {
			t.Fatalf("cap fill: %v", err)
		}
	}
	if _, err := srv.handleSpawnSubagent(ctx, Request{ConversationID: c.ID, Goal: "one too many"}); err == nil ||
		!strings.Contains(err.Error(), "cap") {
		t.Errorf("%d+1 active admitted: %v", subAgentMaxActive, err)
	}
}

// TestSpawnSubagentMemoryPathRefused mirrors drainRun's registration
// fail-fast: a child diff touching daemon-owned paths never becomes a
// pending proposal — the refusal IS the done row, patch kept as salvage.
func TestSpawnSubagentMemoryPathRefused(t *testing.T) {
	root := initRepo(t)
	ad := &subScriptAdapter{
		onStart: func(dir string) {
			_ = os.MkdirAll(filepath.Join(dir, "wiki"), 0o755)
			_ = os.WriteFile(filepath.Join(dir, "wiki", "note.md"), []byte("agent wiki write\n"), 0o644)
		},
		batches: [][]adapter.AgentEvent{{doneEvent("wrote memory")}},
	}
	srv, st := subServer(t, root, ad)
	ctx := context.Background()
	c := seedConv(t, st, root)

	resp, err := srv.handleSpawnSubagent(ctx, Request{ConversationID: c.ID, Goal: "poison the wiki"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := srv.drainSubAgentsLocked(ctx, 0); err != nil {
		t.Fatalf("drain: %v", err)
	}
	done := findSubRow(t, st, c.ID, store.EventSubagentDone)
	var dp struct {
		DiffID   int64  `json:"diff_id"`
		DiffPath string `json:"diff_path"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(done.Payload, &dp); err != nil {
		t.Fatalf("done payload: %v", err)
	}
	if dp.DiffID != 0 || !strings.Contains(dp.Error, "protected") {
		t.Errorf("memory-path settle = %+v — want a refusal naming protected paths, no diff id", dp)
	}
	pending, _ := st.ListPendingDiffs(ctx, c.ID)
	if len(pending) != 0 {
		t.Errorf("protected-path diff registered: %v", pending)
	}
	if _, err := os.Stat(resp.Subagent.WorktreePath); !os.IsNotExist(err) {
		t.Error("refused child's worktree survived")
	}
}

// TestRecoverOrphanedSubAgents: a spawned child with no done row at boot
// (the daemon died mid-run) gets ONE synthetic close; the next boot adds
// nothing (the fold's own idempotence).
func TestRecoverOrphanedSubAgents(t *testing.T) {
	root := initRepo(t)
	if os.Getenv("ODO_REGISTRY_PATH") == "" {
		t.Setenv("ODO_REGISTRY_PATH", filepath.Join(t.TempDir(), "projects.json"))
	}
	mgr := worktree.NewManager(root)
	if err := mgr.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(mgr.StateDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := seedConv(t, st, root)
	if _, err := st.AppendEvent(ctx, c.ID, store.EventSubagentSpawned, `{"subagent_id":"sub-orphan","goal":"died mid-run","worktree_path":"/tmp/x"}`); err != nil {
		t.Fatalf("seed spawn row: %v", err)
	}
	stop := func(srv *Server) {
		srv.stopLiveness()
		srv.stopAutoDistill()
		srv.distillWG.Wait()
		srv.recoverWG.Wait()
		srv.sealLandAndReleasePins()
		srv.landWG.Wait()
		srv.curateWG.Wait()
	}
	// Boot 1: the synthetic close lands.
	ad := &subScriptAdapter{}
	srv := NewServer(st, root, ad, mgr)
	srv.autoDisabled = true
	srv.livenessDisabled.Store(true)
	stop(srv)
	done := findSubRow(t, st, c.ID, store.EventSubagentDone)
	var dp struct {
		ID       string `json:"subagent_id"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(done.Payload, &dp); err != nil {
		t.Fatalf("orphan close payload: %v", err)
	}
	if dp.ID != "sub-orphan" || dp.ExitCode != 1 || !strings.Contains(dp.Error, "daemon restarted") {
		t.Errorf("orphan close = %+v", dp)
	}
	// Boot 2: idempotent — the recovered id is already done.
	srv2 := NewServer(st, root, ad, mgr)
	srv2.autoDisabled = true
	srv2.livenessDisabled.Store(true)
	stop(srv2)
	evs, err := st.ListEvents(ctx, c.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == store.EventSubagentDone {
			n++
		}
	}
	if n != 1 {
		t.Errorf("subagent_done rows after two boots = %d, want exactly one", n)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestNonSubagentRows covers the recover-pending filter's boundary: the
// subagent marker (non-nil, non-empty) drops a row every other shape keeps.
func TestNonSubagentRows(t *testing.T) {
	mk := func(id int64, sid *string) store.PendingDiffRow {
		r := store.PendingDiffRow{}
		r.ID = id
		r.SubagentID = sid
		return r
	}
	sub := "sub-x"
	empty := ""
	rows := []store.PendingDiffRow{mk(1, nil), mk(2, &sub), mk(3, &empty)}
	kept, skipped := nonSubagentRows(rows)
	if skipped != 1 || len(kept) != 2 || kept[0].ID != 1 || kept[1].ID != 3 {
		t.Errorf("filter = kept %d skipped %d (%v) — want 1 dropped, 2 kept in order", len(kept), skipped, kept)
	}
	if got, s := nonSubagentRows(nil); got != nil || s != 0 {
		t.Errorf("nil input = %v/%d", got, s)
	}
}

// TestSubAgentSummaryCapped: the done row's summary clips to the spec's
// 2KB (the OMP final text plus ellipsis), never unbounded journal growth.
func TestSubAgentSummaryCapped(t *testing.T) {
	root := initRepo(t)
	long := strings.Repeat("x", subAgentSummaryCap+500)
	ad := &subScriptAdapter{batches: [][]adapter.AgentEvent{{doneEvent(long)}}}
	srv, st := subServer(t, root, ad)
	ctx := context.Background()
	c := seedConv(t, st, root)
	if _, err := srv.handleSpawnSubagent(ctx, Request{ConversationID: c.ID, Goal: "chatty"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := srv.drainSubAgentsLocked(ctx, 0); err != nil {
		t.Fatalf("drain: %v", err)
	}
	done := findSubRow(t, st, c.ID, store.EventSubagentDone)
	var dp struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(done.Payload, &dp); err != nil {
		t.Fatalf("done payload: %v", err)
	}
	if len([]rune(dp.Summary)) > subAgentSummaryCap+8 {
		t.Errorf("summary = %d runes, want capped at %d", len([]rune(dp.Summary)), subAgentSummaryCap)
	}
	if !strings.HasSuffix(dp.Summary, "…") {
		t.Error("capped summary loses its ellipsis marker")
	}
}

// --- tiny fold helpers (subagent tests only) ---

type taggedEvent struct {
	seq int
	tag string
}

// findSubRow locates the single row of an event type naming subagent
// payloads, failing hard when the row isn't there (the journal is the
// contract under test — ambiguity must surface).
func findSubRow(t *testing.T, st *store.Store, convID int64, evType string) store.Event {
	t.Helper()
	evs, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var found []store.Event
	for _, ev := range evs {
		if ev.Type == evType {
			found = append(found, ev)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s rows = %d, want exactly 1 (journal: %v)", evType, len(found), evs)
	}
	return found[0]
}

// subTaggedEvents collects events of a type and their subagent_id tags.
func subTaggedEvents(t *testing.T, st *store.Store, convID int64, evType string) []taggedEvent {
	t.Helper()
	evs, err := st.ListEvents(context.Background(), convID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var out []taggedEvent
	for _, ev := range evs {
		if ev.Type != evType {
			continue
		}
		var p struct {
			Tag string `json:"subagent_id"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		out = append(out, taggedEvent{seq: ev.Seq, tag: p.Tag})
	}
	return out
}
