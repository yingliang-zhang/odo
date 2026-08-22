package ipc

// Slash-command context tests (batch 1, items A + D): /panel and /vision
// used to send raw text plus a generic system line. These tests pin the
// shared context block's layer contract, order, exclusions, privacy scope
// gate, and the journaled receipts (path→sha16, context_scope,
// total_prompt_bytes).

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// capturedMoaRequest is the slice of the gateway call these tests assert
// on: the assembled SYSTEM prompt and the routed model.
type capturedMoaRequest struct {
	System string `json:"system"`
	Model  string `json:"model"`
}

// moaImageBlock is one image content block as it went on the wire, decoded
// back to its raw byte length so tests can prove the gateway received
// exactly the journaled image_bytes.
type moaImageBlock struct {
	Bytes     int
	MediaType string
}

// moaRecorder collects gateway requests across the fan-out goroutines.
type moaRecorder struct {
	mu       sync.Mutex
	requests []capturedMoaRequest
	images   [][]moaImageBlock // parallel to requests; nil for text-only calls
}

func (r *moaRecorder) add(req capturedMoaRequest, images []moaImageBlock) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	r.images = append(r.images, images)
}

func (r *moaRecorder) all() []capturedMoaRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]capturedMoaRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

func (r *moaRecorder) allImages() [][]moaImageBlock {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]moaImageBlock, len(r.images))
	copy(out, r.images)
	return out
}

// probeImages decodes any image content blocks in the gateway request body
// to their media type + RAW byte length (undoing the base64). Text-only
// requests probe to nil: their content is a bare string, so the lenient
// unmarshal simply yields no blocks.
func probeImages(body []byte) []moaImageBlock {
	var req struct {
		Messages []struct {
			Content []struct {
				Type   string `json:"type"`
				Source struct {
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	var out []moaImageBlock
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Type != "image" {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(b.Source.Data)
			if err != nil {
				continue
			}
			out = append(out, moaImageBlock{Bytes: len(raw), MediaType: b.Source.MediaType})
		}
	}
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
		rec.add(req, probeImages(body))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPanelLegOuterDeadline (P1 #9): a hung panel leg dies at the leg's
// outer deadline as a typed error, and the consult still answers on the
// surviving legs — the pre-fix shape held the RPC for hours (16 tool
// rounds × one worst-case attempt chain).
func TestPanelLegOuterDeadline(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	writePrefs(t, home, "review: pm1@test, pm2@test\n")
	// pm1 answers instantly; pm2's bounded hang (2s) far outlives the
	// client's 50ms leg deadline — the dead-line leg must error while the
	// alive leg completes, and the bound keeps srv.Close deterministic.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		text := "alive-leg-answer"
		if req.Model == "pm2" {
			time.Sleep(2 * time.Second)
			text = "too-late-answer"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": "end_turn",
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	rig.server.legTimeoutForTest = 50 * time.Millisecond
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	start := time.Now()
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel deadline probe"})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("/panel held %v — the hung leg must die at the leg deadline, not hold the consult", elapsed)
	}

	answeredDone := false
	for _, ev := range rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events {
		switch ev.Type {
		case store.EventAgentDone:
			var p struct {
				Panel bool `json:"panel"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err == nil && p.Panel {
				answeredDone = true
			}
		case store.EventAgentText:
			var p struct {
				Panel  bool `json:"panel"`
				Models []struct {
					Model string `json:"model"`
					Text  string `json:"text"`
					Error string `json:"error"`
				} `json:"models"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil || !p.Panel {
				continue
			}
			for _, m := range p.Models {
				switch m.Model {
				case "pm1@test":
					if m.Text != "alive-leg-answer" {
						t.Errorf("alive leg text = %q, want the survivor's answer", m.Text)
					}
				case "pm2@test":
					if !strings.Contains(m.Error, "deadline") {
						t.Errorf("hung leg error = %q, want a typed deadline error", m.Error)
					}
				}
			}
		}
	}
	if !answeredDone {
		t.Error("agent_done{panel:true} missing — the consult must complete on the surviving legs")
	}
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
	resetSharedMoa(t, rig.server) // the shared client (P1 #10) consumed the alpha URL
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
	resetSharedMoa(t, rig.server) // the shared client (P1 #10) consumed the alpha URL
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

// TestVisionContextScopeProjectOnly mirrors TestPanelContextScopeProjectOnly
// for the vision path: under panel_context_scope: project-only the vision
// block and its journaled receipt exclude ~/.odo/user.md (no phantom
// entry), while the project layers stay.
func TestVisionContextScopeProjectOnly(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	writePrefs(t, home, "panel_context_scope: project-only\n")
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "vision-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision scoped question"})
	got := rec.all()
	if len(got) == 0 {
		t.Fatal("/vision made no gateway call")
	}
	system := got[len(got)-1].System
	if strings.Contains(system, "USERPRINCIPLE") || strings.Contains(system, "## User memory") {
		t.Error("project-only scope must exclude user.md from the vision block")
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
}

// TestPanelIncludesCrossWsBlock pins the /panel contract for M12 Batch 3a
// (D-cross): the panel advises on the project as a whole, so matched
// topic pages and the newest matched sibling epoch note ride its system
// block — after the recalled home-workstream notes, before the
// conversation tail — with labeled provenance headers and journaled
// receipts.
func TestPanelIncludesCrossWsBlock(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	writePrefs(t, home, "review: pm1@test\n")
	// Cross-workstream fixture: a topic page and a sibling-workstream epoch
	// note, both matching the CJK panel query. The fixture's OWN
	// main-epoch-1 note matches too but is the home layer's — never a
	// "sibling".
	writeTopicPage(t, root, "sidebar-topic", "# Sidebar\n\n侧边栏 决策记录。 (ui-epoch-2)\n")
	writeEpochNote(t, root, "ui-epoch-3", "# UI 3\n\n侧边栏 width decision.\n")
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "panel-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	// One genuine exchange so the conversation tail section exists.
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "crossws-marker please"})
	rig.pollUntilDone(t, convID)

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel 侧边栏怎么看"})
	system := rec.all()[0].System
	for _, want := range []string{
		"## Cross-workstream context (project topic pages — other workstreams)",
		"### topics/sidebar-topic.md [matched: 侧边, 边栏] · sources: ui-epoch-2",
		"### ui-epoch-3.md [from workstream \"ui\"] [matched: 侧边, 边栏]",
		"侧边栏 决策记录。 (ui-epoch-2)",
		"侧边栏 width decision.",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("panel cross-workstream block missing %q", want)
		}
	}
	// Sibling provenance never points at the HOME workstream: the fixture's
	// matching main-epoch-1 note stays in the home recall section only.
	if strings.Contains(system, "[from workstream \"main\"]") {
		t.Error("sibling header pointed at the current workstream — home notes are not siblings")
	}
	// Position: after the recalled notes, before the conversation tail.
	iNotes := strings.Index(system, "## Prior notes (recalled)")
	iCross := strings.Index(system, "## Cross-workstream context")
	iConv := strings.Index(system, "## Conversation so far")
	if !(iNotes >= 0 && iCross > iNotes && iConv > iCross) {
		t.Errorf("cross block out of order: notes=%d cross=%d conv=%d", iNotes, iCross, iConv)
	}
	// Journaled receipts: both real wiki paths → sha16.
	var p struct {
		Receipt map[string]string `json:"receipt"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	var topicRcpt, sibRcpt bool
	for path, sha := range p.Receipt {
		switch {
		case strings.HasSuffix(path, filepath.Join("topics", "sidebar-topic.md")):
			topicRcpt = len(sha) == 16
		case strings.HasSuffix(path, "ui-epoch-3.md"):
			sibRcpt = len(sha) == 16
		}
	}
	if !topicRcpt || !sibRcpt {
		t.Errorf("panel receipt missing cross sources (topic=%v sibling=%v): %v", topicRcpt, sibRcpt, p.Receipt)
	}
}

// TestVisionExcludesCrossWsBlock pins the /vision lean contract: with the
// same matched cross-workstream fixture on disk, the vision block carries
// no cross-workstream layer and journals no receipts for it.
func TestVisionExcludesCrossWsBlock(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	writeTopicPage(t, root, "sidebar-topic", "# Sidebar\n\n侧边栏 决策记录。 (ui-epoch-2)\n")
	writeEpochNote(t, root, "ui-epoch-3", "# UI 3\n\n侧边栏 width decision.\n")
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "vision-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision 侧边栏 describe"})
	system := rec.all()[len(rec.all())-1].System
	for _, absent := range []string{
		"## Cross-workstream context",
		"### topics/sidebar-topic.md",
		"[from workstream",
		"侧边栏 width decision.",
	} {
		if strings.Contains(system, absent) {
			t.Errorf("vision block must exclude cross-workstream content, found %q", absent)
		}
	}
	var p struct {
		Receipt map[string]string `json:"receipt"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	for path := range p.Receipt {
		if strings.Contains(path, string(filepath.Separator)+"topics"+string(filepath.Separator)) || strings.HasSuffix(path, "ui-epoch-3.md") {
			t.Errorf("vision receipt lists cross-workstream content it never injected: %q", path)
		}
	}
}

// TestSlashTailClampsTurnCap pins the slash tail's per-turn cap: a
// replay_turn_kb setting above slashConvCap must not let the newest turn
// blow past the 4KB block cap through the newest-turn anti-starvation
// exception (renderConvBlock always keeps the first — newest — line). With
// replay_turn_kb: 16 the tail still truncates its turn at 4KB.
func TestSlashTailClampsTurnCap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "replay_turn_kb: 16\n")
	events := []store.Event{
		chatEvent(1, store.EventUserMessage, strings.Repeat("x", 3*slashConvCap)),
	}
	block, _, _, _ := slashConversation(events, slashModePanel)
	if !strings.Contains(block, "[truncated at 4KB]") {
		t.Errorf("slash tail must truncate the newest turn at the 4KB block cap, got:\n%.200s", block)
	}
	// Total stays at slashConvCap plus one turn's fixed overhead (header,
	// blurb, role/seq prefix, truncation marker ≈ 160B) — not the 16KB
	// per-turn prefs cap.
	if len(block) > slashConvCap+256 {
		t.Errorf("slash tail = %d bytes, want ≤ %d (cap + one turn's overhead)", len(block), slashConvCap+256)
	}
}

// TestSlashDroppedSeqsReceipt pins the slash/send symmetry: when the slash
// conversation tail overflows slashConvCap, the journaled slash
// user_message carries the SAME replay receipt the send path writes
// (covered window + boundary + bytes + the omitted [first,last] seq
// window), and the journaled window matches the omission marker inside
// the block the models actually saw (journal ↔ injection coherence).
func TestSlashDroppedSeqsReceipt(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	writePrefs(t, home, "review: pm1@test\n")
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "panel-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	// Overflow the 4KB tail: six ~1.5KB user turns (plus stub replies)
	// force the newest-first accumulation to drop the older ones.
	for i := range 6 {
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: fmt.Sprintf("filler-%d %s", i, strings.Repeat("x", 1500))})
		rig.pollUntilDone(t, convID)
	}

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel dropcheck"})
	var p struct {
		Replay *struct {
			AfterSeq    int   `json:"after_seq"`
			FirstSeq    int   `json:"first_seq"`
			LastSeq     int   `json:"last_seq"`
			Bytes       int   `json:"bytes"`
			DroppedSeqs []int `json:"dropped_seqs"`
		} `json:"replay"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	if p.Replay == nil {
		t.Fatal("slash user_message missing the replay receipt (want symmetry with the send path)")
	}
	if len(p.Replay.DroppedSeqs) != 2 {
		t.Fatalf("dropped_seqs = %v, want [first last] (tail overflowed)", p.Replay.DroppedSeqs)
	}
	if !(p.Replay.DroppedSeqs[0] <= p.Replay.DroppedSeqs[1] && p.Replay.DroppedSeqs[1] < p.Replay.FirstSeq) {
		t.Errorf("window order broken: dropped=%v included=%d–%d", p.Replay.DroppedSeqs, p.Replay.FirstSeq, p.Replay.LastSeq)
	}
	if p.Replay.Bytes <= 0 {
		t.Errorf("replay.bytes = %d, want > 0", p.Replay.Bytes)
	}
	// Journal ↔ injection coherence: the block the models actually saw
	// names the same omitted window.
	marker := fmt.Sprintf("seq %d–%d) omitted", p.Replay.DroppedSeqs[0], p.Replay.DroppedSeqs[1])
	for _, r := range rec.all() {
		if !strings.Contains(r.System, marker) {
			t.Errorf("panel block missing omission marker %q", marker)
		}
	}
}

// TestVisionTailDropReceipt: vision's fixed last-visionConvTurns slice
// omits older turns WITHOUT a block marker (no cap overflow — the header
// omission text is the byte cap's), so the journaled replay receipt is the
// only record of the omission. Assert its [first,last] names exactly the
// dropped turns' seqs.
func TestVisionTailDropReceipt(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "vision-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	for _, msg := range []string{"vd-one", "vd-two", "vd-three"} {
		rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: msg})
		rig.pollUntilDone(t, convID)
	}

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision vd-describe"})
	var p struct {
		Replay *struct {
			DroppedSeqs []int `json:"dropped_seqs"`
		} `json:"replay"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	if p.Replay == nil {
		t.Fatal("/vision user_message missing the replay receipt")
	}
	// Ground truth from the journal: three exchanges = six replayable
	// turns; vision keeps the last visionConvTurns, so the dropped window
	// runs from the first turn to the turn just before the kept ones. The
	// assembly saw the events strictly BEFORE the /vision user_message
	// (the handler journals it after assembling), so cut the tail there.
	evs := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	var turnSeqs []int
	for _, ev := range evs {
		if sent.Event != nil && ev.Seq >= sent.Event.Seq {
			break
		}
		if ev.Type == store.EventUserMessage || ev.Type == store.EventAgentText {
			turnSeqs = append(turnSeqs, ev.Seq)
		}
	}
	if len(turnSeqs) < 6 {
		t.Fatalf("expected ≥6 replayable turns before /vision, got %v", turnSeqs)
	}
	want := []int{turnSeqs[0], turnSeqs[len(turnSeqs)-visionConvTurns-1]}
	if len(p.Replay.DroppedSeqs) != 2 || p.Replay.DroppedSeqs[0] != want[0] || p.Replay.DroppedSeqs[1] != want[1] {
		t.Errorf("vision dropped_seqs = %v, want %v", p.Replay.DroppedSeqs, want)
	}
}

// TestVisionImageBytesReceipt pins the exact-injection receipt for /vision
// attachments: the journaled user_message lists the attachment paths (like
// the send path) plus image_bytes, and the bytes on the WIRE (base64
// blocks the mock gateway received) decode back to exactly that total.
func TestVisionImageBytesReceipt(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "vision-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	dir := t.TempDir()
	imgA := filepath.Join(dir, "shotA.png")
	imgB := filepath.Join(dir, "photoB.jpg")
	if err := os.WriteFile(imgA, make([]byte, 763), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imgB, make([]byte, 1026), 0o644); err != nil {
		t.Fatal(err)
	}

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision what is this", Attachments: []string{imgA, imgB}})
	var p struct {
		Attachments []string `json:"attachments"`
		ImageBytes  int      `json:"image_bytes"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	if len(p.Attachments) != 2 || p.Attachments[0] != imgA || p.Attachments[1] != imgB {
		t.Errorf("attachments = %v, want [%s %s]", p.Attachments, imgA, imgB)
	}
	if want := 763 + 1026; p.ImageBytes != want {
		t.Errorf("image_bytes = %d, want %d (raw bytes read)", p.ImageBytes, want)
	}
	// Wire end: one gateway call, two image blocks decoding back to the
	// same byte counts with the extension-derived media types.
	gotReqs, gotImgs := rec.all(), rec.allImages()
	if len(gotReqs) != 1 || len(gotImgs) != 1 {
		t.Fatalf("gateway calls = %d, want 1", len(gotReqs))
	}
	blocks := gotImgs[0]
	if len(blocks) != 2 || blocks[0] != (moaImageBlock{763, "image/png"}) || blocks[1] != (moaImageBlock{1026, "image/jpeg"}) {
		t.Errorf("wire image blocks = %+v, want [{763 image/png} {1026 image/jpeg}]", blocks)
	}
}

// TestVisionImageShaReceipts (M18 W2 item 4): the /vision user_message
// carries image_sha16 — per-image content hashes ALIGNED with the
// attachments order (distinct bytes per file prove the per-image mapping).
// A read failure leaves that index (and every later, never-attempted one)
// empty while the slice stays aligned — the receipt never claims bytes it
// did not read.
func TestVisionImageShaReceipts(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "vision-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	dir := t.TempDir()
	imgA := filepath.Join(dir, "shotA.png")
	imgB := filepath.Join(dir, "photoB.jpg")
	if err := os.WriteFile(imgA, []byte(strings.Repeat("a", 763)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imgB, []byte(strings.Repeat("b", 1026)), 0o644); err != nil {
		t.Fatal(err)
	}
	bytesOf := func(path string) []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	decode := func(ev *store.Event) []string {
		var p struct {
			ImageSha16 []string `json:"image_sha16"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("user_message payload: %v", err)
		}
		return p.ImageSha16
	}

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision compare these", Attachments: []string{imgA, imgB}})
	got := decode(sent.Event)
	want := []string{sha16(bytesOf(imgA)), sha16(bytesOf(imgB))}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("image_sha16 = %v, want %v (sha16 of each file's bytes, attachments order)", got, want)
	}

	// Missing middle attachment: its entry and every later one are absent
	// (""), index 0 keeps its receipt — the slice stays aligned.
	missing := filepath.Join(dir, "gone.png")
	sent2 := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision and this", Attachments: []string{imgA, missing, imgB}})
	got2 := decode(sent2.Event)
	if len(got2) != 3 || got2[0] != want[0] || got2[1] != "" || got2[2] != "" {
		t.Errorf("image_sha16 on read failure = %v, want [%s \"\" \"\"] (absent entries, aligned indexes)", got2, want[0])
	}
}

// TestVisionImageReadErrorNoByteReceipt: a missing attachment still
// journals the user_message (with the paths, for the audit trail) but no
// image_bytes — a receipt must never claim bytes that were not read
// (ADR-0003 inv 5) — and the gateway is never called.
func TestVisionImageReadErrorNoByteReceipt(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "vision-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID

	missing := filepath.Join(t.TempDir(), "gone.png")
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision broken", Attachments: []string{missing}})
	var p struct {
		Attachments []string `json:"attachments"`
		ImageBytes  *int     `json:"image_bytes"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	if len(p.Attachments) != 1 || p.Attachments[0] != missing {
		t.Errorf("attachments = %v, want [%s]", p.Attachments, missing)
	}
	if p.ImageBytes != nil {
		t.Errorf("image_bytes = %d, want absent (image read failed)", *p.ImageBytes)
	}
	if got := len(rec.all()); got != 0 {
		t.Errorf("gateway calls = %d, want 0 (read failed before the API call)", got)
	}
	evs := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	last := evs[len(evs)-2] // agent_text before agent_done
	var a struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(last.Payload, &a); err != nil {
		t.Fatalf("agent_text payload: %v", err)
	}
	if !strings.Contains(a.Text, "vision error") || !strings.Contains(a.Text, "gone.png") {
		t.Errorf("vision error text = %q, want the read failure naming the path", a.Text)
	}
}

// TestSlashSnapshotCoverage pins both slash modes to the W2 rule-file
// materialization: a /panel over non-empty memory.md/pins.md/user.md
// journals one memory_update{cause:"snapshot"} row per layer BEFORE the
// slash user_message it serves, each sha pairing with that message's
// receipt entry for the same source (seq-N receipt == newest snapshot at
// seq ≤ N); an unchanged /vision journals no new rows while its receipt
// still pairs with the existing snapshots; a hand-edit journals exactly one
// fresh row for the changed layer.
func TestSlashSnapshotCoverage(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	seedSlashFixture(t, root, home)
	writePrefs(t, home, "review: pm1@test\n")
	rec := &moaRecorder{}
	t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "panel-answer", rec).URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	sources := []string{"~/.odo/user.md", ".odo/memory.md", ".odo/pins.md"}

	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel coverage question"})
	if sent.Event == nil {
		t.Fatal("/panel: missing user_message event")
	}
	events := rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	snaps := ruleSnapshotRows(t, events, "snapshot")
	if len(snaps) != 3 {
		t.Fatalf("/panel snapshot rows = %d, want 3 (memory/pins/user): %v", len(snaps), snaps)
	}
	receipt := receiptFromEvent(t, sent.Event)
	bySource := map[string]ruleSnapshotRow{}
	for _, row := range snaps {
		src, _ := row.payload["source"].(string)
		bySource[src] = row
		if row.seq >= sent.Event.Seq {
			t.Errorf("%s snapshot seq %d ≥ /panel user_message seq %d — the row must precede the message it serves", src, row.seq, sent.Event.Seq)
		}
		if _, capped := row.payload["capped"]; capped {
			t.Errorf("%s: capped key on an untruncated read", src)
		}
	}
	for _, src := range sources {
		row, ok := bySource[src]
		if !ok {
			t.Errorf("missing snapshot row for %s", src)
			continue
		}
		if receipt[src] == "" {
			t.Errorf("receipt missing %s", src)
			continue
		}
		if receipt[src] != row.payload["sha"] {
			t.Errorf("receipt[%s] = %q, want the snapshot sha %v", src, receipt[src], row.payload["sha"])
		}
	}

	// /vision (mode vision, fixture unchanged): no new rows; its receipt
	// still pairs with the pre-existing ≤-seq snapshots.
	vision := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/vision what do you see"})
	if vision.Event == nil {
		t.Fatal("/vision: missing user_message event")
	}
	events = rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	if snaps = ruleSnapshotRows(t, events, "snapshot"); len(snaps) != 3 {
		t.Fatalf("snapshot rows after unchanged /vision = %d, want 3: %v", len(snaps), snaps)
	}
	for _, src := range sources {
		if got := receiptFromEvent(t, vision.Event)[src]; got != bySource[src].payload["sha"] {
			t.Errorf("/vision receipt[%s] = %q, want the existing snapshot sha %v", src, got, bySource[src].payload["sha"])
		}
	}

	// Hand-edit pins.md: exactly one fresh row, for the changed layer only.
	if err := os.WriteFile(filepath.Join(root, ".odo", "pins.md"), []byte("- SLASHPINMARKER: edited pin.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "/panel after pin edit"})
	events = rig.call(t, Request{Cmd: CmdPollEvents, ConversationID: convID, AfterSeq: 0}).Events
	snaps = ruleSnapshotRows(t, events, "snapshot")
	if len(snaps) != 4 {
		t.Fatalf("snapshot rows after pin edit = %d, want 4: %v", len(snaps), snaps)
	}
	if got := snaps[3].payload["source"]; got != ".odo/pins.md" {
		t.Errorf("fresh row source = %v, want .odo/pins.md", got)
	}
	if got := snaps[3].payload["content"]; got != "- SLASHPINMARKER: edited pin.\n" {
		t.Errorf("fresh row content = %q, want the edited pin", got)
	}
}

// TestSlashReceiptAssertionFailClosed drills the M18 W2 item-4 slash gate
// (TestSendFailsClosedOnReceiptBreach's slash sibling): the test seam drops
// the wiki/index.md receipt entry between assembly and the gate — a receipt
// diverging from the injected block — and BOTH slash modes must journal the
// attempt user_message, refuse with a paired agent_error naming the breach,
// and make ZERO gateway calls (fail-closed, evidence-first).
func TestSlashReceiptAssertionFailClosed(t *testing.T) {
	for _, mode := range []struct{ name, cmd string }{
		{"panel", "/panel"},
		{"vision", "/vision"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			root := initRepo(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
			seedSlashFixture(t, root, home)
			writePrefs(t, home, "review: pm1@test\n")
			rec := &moaRecorder{}
			t.Setenv("MOA_BASE_URL", recordingMoaServer(t, "must-never-arrive", rec).URL)
			t.Setenv("SUDO_CODING_KEY", "test-key")

			rig := startRig(t, root)
			defer rig.stop(t)
			boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
			convID := boot.Conversation.ID

			// The seam drops exactly the index entry between receipt
			// assembly and the gate (the send-path drill's move).
			rig.server.slashReceiptBreachForTest = func(receipt map[string]string) {
				delete(receipt, "wiki/index.md")
			}

			resp := rig.callExpectErr(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: mode.cmd + " breach drill"})
			if !strings.Contains(resp.Error, "slash receipt assertion failed") {
				t.Errorf("error = %q, want the assertion refusal", resp.Error)
			}
			if got := rec.all(); len(got) != 0 {
				t.Errorf("gateway calls = %d, want 0 (refusal must precede the moa call)", len(got))
			}
			// Journal-first: the slash user_message attempt, then the paired
			// agent_error naming the breach right after it — both on record,
			// no agent answer follows.
			types := rig.allEventTypes(t, convID)
			if n := len(types); n < 2 || types[n-2] != "user_message" || types[n-1] != "agent_error" {
				t.Errorf("event tail = %v, want [… user_message agent_error]", types)
			}
			for _, ty := range types {
				if ty == "agent_text" || ty == "agent_done" {
					t.Errorf("events = %v: an agent answer journaled despite the refusal", types)
				}
			}
			var errText string
			for _, ev := range mustListEvents(t, rig.store, convID) {
				if ev.Type == store.EventAgentError {
					var p struct {
						Error string `json:"error"`
					}
					_ = json.Unmarshal(ev.Payload, &p)
					errText = p.Error
				}
			}
			if !strings.Contains(errText, "missing entry") {
				t.Errorf("agent_error = %q, want it to name the missing receipt entry", errText)
			}
		})
	}
}
