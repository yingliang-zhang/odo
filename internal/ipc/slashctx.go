package ipc

// Slash-command context (batch 1, item A): /panel and /vision used to send
// raw user text plus a generic system prompt — no user.md, memory.md, pins,
// wiki, or conversation, so the advisory models answered blind. This file
// assembles the shared context block both slash modes inject into the
// SYSTEM prompt (system injection keeps provider prompt caches warm).
// Layers mirror buildPrompt's stable order and reuse the same readers,
// caps, line-boundary cuts, and ADR-0003 receipt semantics (sha16 of
// exactly the injected bytes). Skills, the R2 memory map, and the R4
// resume card are skipped on purpose: the panel advises and carries its
// own read-only FS tools, and it is not a cold-start continuation.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
)

// slashContextMode selects the /panel vs /vision block contract.
type slashContextMode int

const (
	slashModePanel slashContextMode = iota
	slashModeVision
)

const (
	// slashRecallCap bounds the panel's recalled-notes section (the send
	// path's recall layer allows 12KB; the advisor block buys a tighter
	// slice of the same machinery).
	slashRecallCap = 6 * 1024
	// slashConvCap bounds the injected conversation tail for both modes.
	slashConvCap = 4 * 1024
	// visionConvTurns is the vision block's shorter tail: image questions
	// are about the latest exchange, not the whole thread.
	visionConvTurns = 2
)

// panel_context_scope prefs values.
const (
	panelScopeFull        = "full"
	panelScopeProjectOnly = "project-only"
)

// resolvePanelContextScope reads the panel_context_scope prefs key:
// "project-only" keeps ~/.odo/user.md out of both slash-mode blocks;
// missing or unparseable values resolve to "full" (fail to default, not to
// silence — the resolveMaxConcurrent pattern).
func resolvePanelContextScope() string {
	if adapter.LoadPrefsRaw("panel_context_scope") == panelScopeProjectOnly {
		return panelScopeProjectOnly
	}
	return panelScopeFull
}

// slashContextBlock renders the mode's context block plus the injection
// receipt entries (path → sha16 of exactly the injected bytes), mirroring
// buildPrompt's stable order: user (scope-gated), project, pins, index,
// recalled notes (panel only), then the conversation tail. The caller
// assembles BEFORE journaling the slash user_message (mirroring the send
// path's ordering at runMemoryLayers), so the block never contains the
// slash question itself. query is the recall query the caller already
// built (slash text UNION the last current-epoch turns, as recallQuery
// does); vision skips the recall section entirely. The caller resolves the
// context scope and fetches the events ONCE (it needs both for the
// journal receipt and, on the panel path, the recall query), so one slash
// call costs one prefs read and one events read like the send path.
func (s *Server) slashContextBlock(ctx context.Context, wsName string, convID int64, query string, events []store.Event, scope string, mode slashContextMode) (block string, receipt map[string]string, conv *slashConvReceipt) {
	receipt = map[string]string{}
	var sections []string
	section := func(header, body string) {
		sections = append(sections, header+"\n\n"+body)
	}

	if scope == panelScopeFull {
		if user := readUserMemory(); user != "" {
			receipt["~/.odo/user.md"] = sha16([]byte(user))
			section("## User memory", user)
		}
	}
	if project := readProjectMemory(s.projectRoot); project != "" {
		receipt[".odo/memory.md"] = sha16([]byte(project))
		section("## Project memory", project)
	}
	if pins := readPins(s.projectRoot); pins != "" {
		receipt[".odo/pins.md"] = sha16([]byte(pins))
		section("## Pins", pins)
	}
	if index := readIndex(s.projectRoot); index != "" {
		receipt["wiki/index.md"] = sha16([]byte(index))
		section("## Wiki index", index)
	}
	if mode == slashModePanel {
		mem, items, noteBytes := recallWikiNotesCapped(s.projectRoot, wsName, query, s.retractedNotes(ctx, convID), slashRecallCap)
		if mem != "" {
			for i, it := range items {
				receipt[it.path] = sha16(noteBytes[i])
			}
			section("## Prior notes (recalled)", mem)
		}
		// M12 Batch 3a (D-cross): the panel advises on the project as a
		// whole, so it buys the same matched-only cross-workstream layer as
		// the send path. Topic pages are project-scoped by definition, so
		// panel_context_scope: project-only does NOT exclude this layer
		// (the scope gate is about ~/.odo/user.md only). /vision stays
		// excluded — lean contract.
		if block, sources := crossWsBlock(ctx, s.store, s.projectRoot, wsName, query); block != "" {
			for _, src := range sources {
				receipt[src.path] = src.sha
			}
			sections = append(sections, block) // carries its own "##" header
		}
	}
	if block, first, last, dropped := slashConversation(events, mode); block != "" {
		sections = append(sections, block) // carries its own header + receipt range
		conv = &slashConvReceipt{
			after:   foldBoundary(events),
			first:   first,
			last:    last,
			bytes:   len(block),
			dropped: dropped,
		}
	}
	return strings.Join(sections, "\n\n---\n\n"), receipt, conv
}

// slashConvReceipt mirrors the send path's replay receipt for the slash
// conversation tail (server.go's msgPayload["replay"]): the covered seq
// window, the boundary it follows, the block's byte size, and — when the
// block omits older turns — the omitted [first,last] seq window. The
// omission marker already renders INSIDE the block; this is the journaled
// half of the same receipt, so "not visible" shows up in the user_message
// payload exactly like a capped main replay does.
type slashConvReceipt struct {
	after   int   // fold boundary the window starts after
	first   int   // first included seq
	last    int   // last included seq
	bytes   int   // rendered block size
	dropped []int // [first,last] omitted seq window (nil without drops)
}

// slashConversation renders the "## Conversation so far" section from the
// caller-fetched events: the current-epoch turn tail under slashConvCap
// with the same receipt header shape as the main replay. Vision keeps only
// the last visionConvTurns turns; panel takes the newest-first-capped
// tail. The per-turn cap clamps to slashConvCap: replay_turn_kb above 4KB
// would otherwise let the newest turn sail past the block cap through the
// anti-starvation exception (the newest line is always kept). The returned
// dropped window covers BOTH omission kinds — vision's fixed-turn slice
// and the byte cap's newest-first overflow — as one [first,last] span.
func slashConversation(events []store.Event, mode slashContextMode) (block string, first, last int, dropped []int) {
	turns := collectSlashTurns(events, foldBoundary(events))
	if mode == slashModeVision && len(turns) > visionConvTurns {
		cut := len(turns) - visionConvTurns
		dropped = []int{turns[0].seq, turns[cut-1].seq}
		turns = turns[cut:]
	}
	if len(turns) == 0 {
		return "", 0, 0, nil
	}
	var capDropped []int
	block, first, last, capDropped = renderSlashConversation(turns, slashConvCap, min(resolveReplayCaps().turn, slashConvCap))
	// The two drop kinds meet at the slice boundary: vision's slice dropped
	// turns[0..cut), a cap overflow then drops older KEPT turns — the
	// journaled window is the contiguous span from the oldest omitted turn
	// to the newest omitted turn.
	if len(capDropped) == 2 {
		if len(dropped) == 2 {
			dropped[1] = capDropped[1]
		} else {
			dropped = capDropped
		}
	}
	return block, first, last, dropped
}

// collectSlashTurns is collectReplayTurns minus prior /panel and /vision
// agent answers (their journaled `panel` / `vision` payload flags): a
// 30KB panel answer in every later block would starve the real
// conversation, and the panel has FS tools when it needs the details.
// Slash USER messages are regular conversation and stay.
func collectSlashTurns(events []store.Event, boundary int) []replayTurn {
	var turns []replayTurn
	for _, ev := range events {
		if ev.Seq <= boundary {
			continue
		}
		if ev.Type != store.EventUserMessage && ev.Type != store.EventAgentText {
			continue
		}
		var p struct {
			Text   string `json:"text"`
			Panel  bool   `json:"panel"`
			Vision bool   `json:"vision"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if ev.Type == store.EventAgentText && (p.Panel || p.Vision) {
			continue
		}
		text := strings.TrimSpace(p.Text)
		if text == "" {
			continue
		}
		role := "user"
		if ev.Type == store.EventAgentText {
			role = "agent"
		}
		turns = append(turns, replayTurn{seq: ev.Seq, role: role, text: text})
	}
	return turns
}

// renderSlashConversation renders the slash-mode conversation tail with a
// seq-range receipt header like the main replay (including the actionable
// omission marker when the cap drops older turns); the omitted window is
// returned for the journaled receipt, not just the header text.
func renderSlashConversation(turns []replayTurn, totalCap, turnCap int) (block string, firstSeq, lastSeq int, dropped []int) {
	if len(turns) == 0 {
		return "", 0, 0, nil
	}
	return renderConvBlock(
		"## Conversation so far (current epoch",
		"These turns are replayed verbatim from the journal (no summarization).",
		turns, totalCap, turnCap)
}

// slashUserMessagePayload builds the journaled payload for a slash
// user_message event: the "/cmd text" text (existing shape), the injection
// receipt (same path→sha16 map the send path journals), the effective
// context scope, the assembled prompt size (item D), and — when the
// conversation tail rendered — the same replay sub-receipt the send path
// writes (covered window + boundary + bytes + dropped_seqs on omission).
// Optional fields only (ADR-0002 preserved).
func slashUserMessagePayload(cmd, text string, receipt map[string]string, scope string, totalBytes int, conv *slashConvReceipt) map[string]interface{} {
	p := map[string]interface{}{
		"text":               cmd + " " + text,
		"context_scope":      scope,
		"total_prompt_bytes": totalBytes,
	}
	if len(receipt) > 0 {
		p["receipt"] = receipt
	}
	if conv != nil {
		rp := map[string]interface{}{
			"after_seq": conv.after,
			"first_seq": conv.first,
			"last_seq":  conv.last,
			"bytes":     conv.bytes,
		}
		if len(conv.dropped) == 2 {
			rp["dropped_seqs"] = conv.dropped
		}
		p["replay"] = rp
	}
	return p
}
