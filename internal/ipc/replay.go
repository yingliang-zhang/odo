package ipc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
)

// R1 (journal mechanical replay): the biggest cross-dialogue memory gap was
// that the agent never saw the conversation itself — memory layers are
// distilled summaries, but the turns of the CURRENT epoch (everything after
// the last fold boundary, R3) were injected nowhere. The replay is
// mechanical: user_message.text and agent_text.text events straight from
// the journal, newest-first accumulation under a byte cap, turn-boundary
// truncation, and a receipt (the rendered block header + the user_message
// payload both record the covered seq range). No LLM is involved, so the
// "everything reconstructs from the journal" invariant holds.

const (
	// replayTotalCapDefault bounds the whole replay block (user 4KB /
	// project 4KB / recall 12KB scale) when prefs.md sets nothing. Newest
	// turns win; the cut drops OLD turns (they had the best chance of being
	// distilled/recalled already).
	replayTotalCapDefault = 8 * 1024
	// replayTurnCapDefault bounds one turn so a single monster reply (a
	// /panel answer can be >30KB) cannot starve every other turn.
	replayTurnCapDefault = 4 * 1024

	// Clamp ranges for the prefs-configurable caps: below 4KB total the
	// replay block carries almost nothing; above 64KB it crowds out the
	// memory layers it follows.
	replayTotalKBMin, replayTotalKBMax = 4, 64
	replayTurnKBMin, replayTurnKBMax   = 1, 16
)

// replayCaps carries the effective replay byte caps for one assembly.
// Resolved per call (prefs.md re-read, resolveMaxConcurrent pattern) —
// never package globals, so a prefs edit takes effect on the next send.
type replayCaps struct {
	total int // whole replay block
	turn  int // per-turn truncation
}

// resolveReplayCaps reads replay_total_kb / replay_turn_kb from prefs.md.
// Missing or unparseable values fail closed to the defaults (today's
// behavior); parseable out-of-range values clamp into [min,max] KB.
func resolveReplayCaps() replayCaps {
	caps := replayCaps{total: replayTotalCapDefault, turn: replayTurnCapDefault}
	if v := adapter.LoadPrefsRaw("replay_total_kb"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			caps.total = clampKB(n, replayTotalKBMin, replayTotalKBMax) * 1024
		}
	}
	if v := adapter.LoadPrefsRaw("replay_turn_kb"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			caps.turn = clampKB(n, replayTurnKBMin, replayTurnKBMax) * 1024
		}
	}
	return caps
}

func clampKB(n, min, max int) int {
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// replayTurn is one chat turn eligible for replay, with its journal seq.
type replayTurn struct {
	seq  int
	role string // "user" | "agent"
	text string
}

// collectReplayTurns extracts chat turns with seq > boundary from the
// seq-ascending events list. user_message and agent_text carry the
// conversation; tool calls/results, thinking traces, and bookkeeping
// (review_action, memory_update, agent_done/error) are noise a summary
// layer already owns — replayed chat stays chat.
func collectReplayTurns(events []store.Event, boundary int) []replayTurn {
	// W6 first pass: a WAITING parked goal (user_message{park:true} not yet
	// consumed by run_prompt{goal_seqs} or parked_goal_dropped{goal_seq})
	// must NOT replay into intervening runs' prompts — the M18 repair-
	// prompt hazard. It is a future ask, not history: its own activation
	// prompt carries the text verbatim at the end (send-path shape), and
	// consumed parks replay normally afterward (the goal ran; the text is
	// honest history).
	waitingParks := map[int]bool{}
	for _, g := range deriveParkedGoals(events) {
		waitingParks[g.seq] = true
	}
	var turns []replayTurn
	for _, ev := range events {
		if ev.Seq <= boundary {
			continue
		}
		if waitingParks[ev.Seq] {
			continue
		}
		if ev.Type != store.EventUserMessage && ev.Type != store.EventAgentText {
			continue
		}
		// M18: an auto-revise repair prompt is machine-generated chain
		// evidence, not a user turn — distillRender tombstones it and
		// originGoal skips it; replaying it as "user" would confuse the
		// NEXT repair run's prompt (and smuggle the demotion directive in
		// as a lower-authority past turn, P0 review GLM). M19: loop
		// fix/implement prompts are the same shape (loop_fix marker).
		if _, marked := parseAutoReviseMarker(ev.Payload); marked {
			continue
		}
		if _, marked := parseLoopFixMarker(ev.Payload); marked {
			continue
		}
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
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

// renderReplay builds the prompt block for the current epoch's turns under
// the effective caps. Returns "", 0, 0, nil when nothing falls in the
// window (fresh epoch, or everything already folded). droppedSeqs is the
// omitted window's [first,last] seq pair for the replay receipt (nil when
// nothing was dropped).
func renderReplay(turns []replayTurn, caps replayCaps) (block string, firstSeq, lastSeq int, droppedSeqs []int) {
	if len(turns) == 0 {
		return "", 0, 0, nil
	}
	return renderConvBlock(
		"## Recent conversation (journal replay: current epoch",
		"These turns are replayed verbatim from the journal (no summarization). The user's current message follows at the end.",
		turns, caps.total, caps.turn)
}

// renderConvBlock accumulates turns newest-first under totalCap (per-turn
// truncation at turnCap), then reverses back to chronological order so the
// block reads like a conversation. The header appends the covered seq
// range — the visible receipt — and, when older turns were dropped, the
// omission marker: the exact dropped seq window and the journal pull
// command, so "not visible" always carries its retrieval path.
func renderConvBlock(header, blurb string, turns []replayTurn, totalCap, turnCap int) (block string, firstSeq, lastSeq int, droppedSeqs []int) {
	var lines []string
	used := 0
	dropped := 0
	for i := len(turns) - 1; i >= 0; i-- {
		line := formatReplayTurn(turns[i], turnCap)
		if used+len(line) > totalCap && len(lines) > 0 {
			dropped = i + 1 // remaining older turns (0..i inclusive)
			break
		}
		lines = append(lines, line)
		used += len(line)
	}
	// Reverse to chronological order.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	included := turns[len(turns)-len(lines):]
	firstSeq, lastSeq = included[0].seq, included[len(included)-1].seq

	var b strings.Builder
	fmt.Fprintf(&b, "%s, seq %d–%d)", header, firstSeq, lastSeq)
	if dropped > 0 {
		a, z := turns[0].seq, turns[dropped-1].seq
		droppedSeqs = []int{a, z}
		fmt.Fprintf(&b, " — %d older turn(s) (seq %d–%d) omitted by the %dKB cap; pull with `odo journal range %d %d` or browse the tail via `odo journal tail 200`",
			dropped, a, z, totalCap/1024, a, z)
	}
	b.WriteString("\n\n" + blurb + "\n\n")
	b.WriteString(strings.Join(lines, "\n\n"))
	return b.String(), firstSeq, lastSeq, droppedSeqs
}

// formatReplayTurn renders one turn, truncating the text at turnCap so no
// single turn dominates the block (marker keeps the receipt honest).
func formatReplayTurn(t replayTurn, turnCap int) string {
	text := t.text
	if len(text) > turnCap {
		text = strings.TrimRight(runeSafeCut(text, turnCap), " \t\r\n") +
			fmt.Sprintf(" … [truncated at %dKB]", turnCap/1024)
	}
	return fmt.Sprintf("**%s** (seq %d): %s", t.role, t.seq, text)
}

// runeSafeCut trims text to at most maxBytes bytes without splitting a
// multi-byte rune: a raw byte cut (text[:maxBytes]) can land mid-rune and
// leave invalid UTF-8, and CJK text (3 bytes/rune) makes that the common
// case, not the edge. A truncated rune is at most 3 trailing bytes, so
// trimming back one byte at a time terminates immediately. maxBytes must
// be >= 0.
func runeSafeCut(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := text[:maxBytes]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// buildReplay is the one-call helper for send paths: fold boundary (R3,
// legacy fallback for pre-schema distills) -> turns -> capped block. The
// returned boundary and dropped seq window are journaled as replay receipt
// context.
func buildReplay(events []store.Event) (block string, firstSeq, lastSeq, boundary int, droppedSeqs []int) {
	boundary = foldBoundary(events)
	turns := collectReplayTurns(events, boundary)
	block, firstSeq, lastSeq, droppedSeqs = renderReplay(turns, resolveReplayCaps())
	return block, firstSeq, lastSeq, boundary, droppedSeqs
}

// recallCtxTurns bounds how many current-epoch turns seed the recall query
// alongside the user's message (M6.1 token union). A lone short message —
// CJK text tokenizes to almost nothing under the ASCII split — rarely
// carries enough terms to hit the notes the thread is about; the last few
// turns ARE the working topic.
const recallCtxTurns = 3

// recallQuery builds the M6 keyword-recall query (M6.1): the user's message
// UNION the last recallCtxTurns replayable turn texts of the current epoch.
// No events -> the message alone, so first-send behavior and receipts are
// unchanged. Stop-words/duplicates are filtered by tokenizeQuery
// downstream; seeds are per-turn truncated to the effective turn cap so a
// monster reply (a /panel answer can be >30KB) cannot flood the term set.
func recallQuery(text string, events []store.Event) string {
	if len(events) == 0 {
		return text
	}
	turns := collectReplayTurns(events, foldBoundary(events))
	if n := len(turns); n > recallCtxTurns {
		turns = turns[n-recallCtxTurns:]
	}
	turnCap := resolveReplayCaps().turn
	var b strings.Builder
	b.WriteString(text)
	for _, t := range turns {
		seed := t.text
		if len(seed) > turnCap {
			// Rune-safe like formatReplayTurn: a raw byte cut can split a
			// CJK rune mid-sequence, and the invalid tail then silently
			// costs the last bigram(s) in tokenizeQuery's range-over-runes
			// (replacer eats them) on the CJK-primary query path.
			seed = runeSafeCut(seed, turnCap)
		}
		b.WriteString("\n\n")
		b.WriteString(seed)
	}
	return b.String()
}

// resumeCardCap bounds the injected open-loops section so a runaway section
// cannot starve the replay and recall blocks (the /panel review's guard).
const resumeCardCap = 2 * 1024

// buildResumeCard renders the cold-start handoff block (R4): right after a
// distill folds the epoch, the replay window is empty and the agent would
// start over from lossy summaries alone. The newest distilled note's
// `## Open loops` section is the minimal anchor for continuing prior work.
// The caller gates on an empty replay, so the card fires for the first run
// after a fold and self-limits as soon as the journal carries visible turns
// again. Gated on a real fold in THIS conversation (boundary > 0): seq
// numbering is per-conversation, so a "folded through seq N" stamp is
// meaningless on a conversation that never distilled. Retraction is ignored
// deliberately — retraction targets older notes contradicted by newer ones,
// so the newest epoch note is never the contradicted side; notes older than
// the newest are never consulted (their loops may already be resolved).
// Returns the block and the source note's path (for the injection receipt),
// or "", "" when there is nothing honest to hand off (no fold, no note, no
// section, or the explicit None form).
func buildResumeCard(projectRoot, wsName string, events []store.Event) (block, notePath string) {
	boundary := foldBoundary(events)
	if boundary == 0 {
		return "", ""
	}
	matches, err := filepath.Glob(filepath.Join(projectRoot, "wiki", wsName+"-epoch-*.md"))
	if err != nil {
		return "", ""
	}
	newest, maxEpoch := "", -1
	for _, m := range matches {
		if ep, ok := wikiNoteEpoch(m); ok && ep > maxEpoch {
			newest, maxEpoch = m, ep
		}
	}
	if newest == "" {
		return "", ""
	}
	raw, err := os.ReadFile(newest)
	if err != nil {
		return "", ""
	}
	loops := capAtLineBoundary(openLoopsSection(string(raw)), resumeCardCap)
	if loops == "" {
		return "", ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Resume context (cold start: open loops from %s, folded through seq %d)\n\n",
		filepath.Base(newest), boundary)
	b.WriteString(loops)
	fmt.Fprintf(&b, "\n\n> Distilled summary of folded events — details may be lossy, and anything after seq %d is not covered. The journal is authoritative: verify with `odo journal search <terms>` (no seq known), `odo journal tail N`, or `odo journal range A B` before relying on specifics.", boundary)
	return b.String(), newest
}

// openLoopsSection extracts the body of the note's `## Open loops` H2
// section (up to the next H2 or EOF; H3+ stays in the body). The heading
// match is case-insensitive; the written convention stays `## Open loops`.
// "" when the section is absent, empty, or the explicit None form — a cold
// start with nothing open gets no card.
func openLoopsSection(note string) string {
	var body []string
	inSection := false
	for _, line := range strings.Split(note, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			if inSection {
				break
			}
			inSection = strings.EqualFold(strings.TrimSpace(trimmed[len("## "):]), "open loops")
			continue
		}
		if inSection {
			body = append(body, line)
		}
	}
	out := strings.TrimSpace(strings.Join(body, "\n"))
	if out == "" {
		return ""
	}
	if norm := strings.TrimSuffix(strings.TrimPrefix(out, "- "), "."); strings.EqualFold(norm, "none") {
		return ""
	}
	return out
}
