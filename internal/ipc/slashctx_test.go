package ipc

// Slash-command context tests (batch 1, items A + D): /panel and /vision
// used to send raw text plus a generic system line. These tests pin the
// shared context block's layer contract, order, exclusions, privacy scope
// gate, and the journaled receipts (path→sha16, context_scope,
// total_prompt_bytes).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// capturedMoaRequest is the slice of the gateway call these tests assert
// on: the assembled SYSTEM prompt and the routed model.
type capturedMoaRequest struct {
	System string `json:"system"`
	Model  string `json:"model"`
}

// moaRecorder collects gateway requests across the fan-out goroutines.
type moaRecorder struct {
	mu       sync.Mutex
	requests []capturedMoaRequest
}

func (r *moaRecorder) add(req capturedMoaRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
}

func (r *moaRecorder) all() []capturedMoaRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]capturedMoaRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

// recordingMoaServer mocks the MoA gateway, returning a fixed answer while
// recording each request's system prompt + model. Callers set MOA_BASE_URL
// and SUDO_CODING_KEY.
func recordingMoaServer(t *testing.T, text string, rec *moaRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req capturedMoaRequest
		_ = json.Unmarshal(body, &req)
		rec.add(req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedSlashFixture seeds every memory layer the slash block must carry:
// user.md, memory.md, pins.md, wiki/index.md, an epoch note (with CJK
// content so the recall section is exercised by CJK queries), and a
// sidebar skill — the skill must NOT leak into slash context (skip
// contract): its keyword matches the panel queries on purpose.
func seedSlashFixture(t *testing.T, root, home string) {
	t.Helper()
	writeUserMD(t, home, "USERPRINCIPLE: always verify before claiming done.\n")
	if err := os.MkdirAll(filepath.Join(root, ".odo", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".odo", "memory.md"), []byte("SLASHPROJECTRULE: Go only in this repo.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".odo", "pins.md"), []byte("- SLASHPINMARKER: never push without asking.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeEpochNote(t, root, "main-epoch-1", "# Epoch 1\n\nSLASHNOTEMARKER: 调整了侧边栏宽度并记录原因。\n")
	if err := os.WriteFile(filepath.Join(root, "wiki", "index.md"), []byte("# Wiki index\n\n- SLASHINDEXMARKER topic list\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: sidebar-fix\ndescription: 侧边栏修复步骤\nkeywords: [sidebar]\n---\n\nSKILLBODYMARKER: do the thing.\n"
	if err := os.WriteFile(filepath.Join(root, ".odo", "skills", "sidebar-fix.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPanelContextBlock pins the /panel system-prompt contract: role line
// first, then user/project/pins/index/recalled-notes/conversation in
// buildPrompt order, skills skipped, the panel question absent from its
// own block, and the full journaled receipt. The second panel proves
// flagged prior panel ANSWERS stay out of the conversation tail while
// genuine turns (and prior slash user messages) stay in.
func TestPanelContextBlock(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	writePrefs(t, home, "review: pm1@test\n")
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "panel-answer-alpha", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// One genuine exchange so the conversation tail has real turns.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "usermarker-one please"})
	rig.pollUntilDone(t, convID)

	const panelText = "ui-sidebar-question 侧边栏怎么看"
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel " + panelText})
	got := rec.all()
	if len(got) == 0 {
		t.Fatal("/panel made no gateway call")
	}
	system := got[0].System
	if !strings.HasPrefix(system, "You are an expert advisor.") {
		t.Error("system prompt must keep the existing role line FIRST")
	}
	for _, want := range []string{
		"## User memory", "USERPRINCIPLE",
		"## Project memory", "SLASHPROJECTRULE",
		"## Pins", "SLASHPINMARKER",
		"## Wiki index", "SLASHINDEXMARKER",
		"## Prior notes (recalled)", "SLASHNOTEMARKER",
		"## Conversation so far", "usermarker-one",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("panel system block missing %q", want)
		}
	}
	// Layer order must mirror buildPrompt's stable order.
	order := []string{"## User memory", "## Project memory", "## Pins", "## Wiki index", "## Prior notes (recalled)", "## Conversation so far"}
	prev := -1
	for _, h := range order {
		i := strings.Index(system, h)
		if i < 0 || i < prev {
			t.Fatalf("layer %q out of buildPrompt order (at %d, prev %d)", h, i, prev)
		}
		prev = i
	}
	// Skip contract: no skills, no memory map, no resume card.
	if strings.Contains(system, "SKILLBODYMARKER") || strings.Contains(system, "## Relevant skills") {
		t.Error("slash block must skip skills even when the query matches one")
	}
	// The block is assembled before journaling: the panel question itself
	// must not appear anywhere in its own context block.
	if strings.Contains(system, "怎么看") {
		t.Error("context block leaked the panel question it was built for")
	}

	// Journaled receipt: path→sha16, scope, assembled size.
	var p struct {
		Receipt    map[string]string `json:"receipt"`
		Scope      string            `json:"context_scope"`
		TotalBytes int               `json:"total_prompt_bytes"`
	}
	if sent.Event == nil {
		t.Fatal("/panel response missing the journaled user_message event")
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	for _, path := range []string{"~/.odo/user.md", ".odo/memory.md", ".odo/pins.md", "wiki/index.md"} {
		if len(p.Receipt[path]) != 16 {
			t.Errorf("receipt[%q] = %q, want a sha16 entry", path, p.Receipt[path])
		}
	}
	var noteReceipted bool
	for path := range p.Receipt {
		if strings.HasSuffix(path, "main-epoch-1.md") {
			noteReceipted = true
		}
	}
	if !noteReceipted {
		t.Error("receipt missing the recalled note's path entry")
	}
	if p.Scope != "full" {
		t.Errorf("context_scope = %q, want full (default without prefs)", p.Scope)
	}
	if want := len(system) + len(panelText); p.TotalBytes != want {
		t.Errorf("total_prompt_bytes = %d, want %d (system block + user text)", p.TotalBytes, want)
	}

	// Second panel: the first panel's flagged ANSWER stays out; the
	// genuine user turn and the prior slash USER message stay in.
	rec2 := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "panel-answer-beta", rec2).URL)
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel second question"})
	system = rec2.all()[0].System
	if strings.Contains(system, "panel-answer-alpha") {
		t.Error("conversation tail leaked a prior /panel agent answer (panel-flagged turns must be excluded)")
	}
	if !strings.Contains(system, "usermarker-one") {
		t.Error("conversation tail lost the genuine user turn")
	}
	if !strings.Contains(system, "/panel "+panelText) {
		t.Error("conversation tail must keep prior slash user messages (only flagged agent turns are excluded)")
	}
}

// TestPanelContextScopeProjectOnly pins the privacy gate: panel prompts
// carry no user.md under project-only (receipt and block agree), while the
// project layers stay. Unknown values fail to the default ("full"), not to
// silence.
func TestPanelContextScopeProjectOnly(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	writePrefs(t, home, "review: pm1@test\npanel_context_scope: project-only\n")
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "panel-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel scoped question"})
	system := rec.all()[0].System
	if strings.Contains(system, "USERPRINCIPLE") || strings.Contains(system, "## User memory") {
		t.Error("project-only scope must exclude user.md from the panel block")
	}
	if !strings.Contains(system, "SLASHPROJECTRULE") {
		t.Error("project-only scope still carries the project memory layer")
	}
	var p struct {
		Receipt map[string]string `json:"receipt"`
		Scope   string            `json:"context_scope"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	if p.Scope != "project-only" {
		t.Errorf("context_scope = %q, want project-only", p.Scope)
	}
	if _, has := p.Receipt["~/.odo/user.md"]; has {
		t.Error("receipt must not list user.md it never injected (ADR-0003 inv 5)")
	}

	// Unknown scope values resolve to the default "full".
	writePrefs(t, home, "review: pm1@test\npanel_context_scope: bogus\n")
	rec2 := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "panel-answer", rec2).URL)
	sent2 := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel scoped again"})
	if system = rec2.all()[0].System; !strings.Contains(system, "USERPRINCIPLE") {
		t.Error("unknown panel_context_scope must fail to default (full), not to silence")
	}
	var p2 struct {
		Scope string `json:"context_scope"`
	}
	if err := json.Unmarshal(sent2.Event.Payload, &p2); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	if p2.Scope != "full" {
		t.Errorf("bogus scope journaled as %q, want full", p2.Scope)
	}
}

// TestVisionContextBlock pins the /vision contract: vision role line
// first, user/project/pins/index layers then a LAST-TWO-TURN conversation
// tail with the same exclusions (flagged slash agent answers stay out),
// recalled notes skipped, receipt journaled.
func TestVisionContextBlock(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "vision-answer-one", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// A flagged slash answer early in the history — must stay out of the
	// tail even when the window reaches it.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision earlier screenshot"})
	// More than visionConvTurns of history so the shorter tail is visible.
	for _, msg := range []string{"vm-one", "vm-two", "vm-three"} {
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: msg})
		rig.pollUntilDone(t, convID)
	}

	const visionText = "vq-marker describe this"
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision " + visionText})
	got := rec.all()
	if len(got) == 0 {
		t.Fatal("/vision made no gateway call")
	}
	last := got[len(got)-1]
	if last.Model != "t9s/kimi-k3" {
		t.Errorf("/vision routed to %q, want t9s/kimi-k3", last.Model)
	}
	system := last.System
	if !strings.HasPrefix(system, "You are a vision-capable coding assistant.") {
		t.Error("system prompt must keep the existing vision line FIRST")
	}
	for _, want := range []string{
		"## User memory", "USERPRINCIPLE",
		"## Project memory", "SLASHPROJECTRULE",
		"## Pins", "SLASHPINMARKER",
		"## Wiki index", "SLASHINDEXMARKER",
		"## Conversation so far",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("vision system block missing %q", want)
		}
	}
	if strings.Contains(system, "## Prior notes") || strings.Contains(system, "SLASHNOTEMARKER") {
		t.Error("vision must skip the recalled-notes section")
	}
	// Last ~2 turns only: vm-three (and its agent reply) in, vm-one/two out.
	if !strings.Contains(system, "vm-three") {
		t.Error("vision tail must contain the latest exchange")
	}
	if strings.Contains(system, "vm-two") || strings.Contains(system, "vm-one") {
		t.Error("vision tail must be only the last ~2 turns")
	}
	if strings.Contains(system, "vision-answer-one") {
		t.Error("conversation tail leaked a flagged /vision agent answer")
	}
	if strings.Contains(system, visionText) {
		t.Error("context block leaked the vision question it was built for")
	}

	var p struct {
		Receipt    map[string]string `json:"receipt"`
		Scope      string            `json:"context_scope"`
		TotalBytes int               `json:"total_prompt_bytes"`
	}
	if sent.Event == nil {
		t.Fatal("/vision response missing the journaled user_message event")
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	for path := range p.Receipt {
		if strings.HasSuffix(path, ".md") && strings.Contains(path, "-epoch-") {
			t.Errorf("vision receipt lists a recalled note it never injected: %q", path)
		}
	}
	if p.Scope != "full" {
		t.Errorf("context_scope = %q, want full (default)", p.Scope)
	}
	if want := len(system) + len(visionText); p.TotalBytes != want {
		t.Errorf("total_prompt_bytes = %d, want %d (system block + user text)", p.TotalBytes, want)
	}
}
