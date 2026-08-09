package ipc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// replayTotalCap bounds the whole replay block (user 4KB / project 4KB /
	// recall 12KB scale). Newest turns win; the cut drops OLD turns (they
	// had the best chance of being distilled/recalled already).
	replayTotalCap = 8 * 1024
	// replayTurnCap bounds one turn so a single monster reply (a /panel
	// answer can be >30KB) cannot starve every other turn.
	replayTurnCap = 4 * 1024
)

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
	var turns []replayTurn
	for _, ev := range events {
		if ev.Seq <= boundary {
			continue
		}
		if ev.Type != store.EventUserMessage && ev.Type != store.EventAgentText {
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

// renderReplay builds the prompt block for the current epoch's turns. Turns
// accumulate newest-first until the total cap, then reverse back to
// chronological order so the prompt reads like a conversation. The header
// names the covered seq range — the visible receipt — and flags when older
// turns were dropped by the cap. Returns "", 0, 0 when nothing falls in the
// window (fresh epoch, or everything already folded).
func renderReplay(turns []replayTurn) (block string, firstSeq, lastSeq int) {
	if len(turns) == 0 {
		return "", 0, 0
	}
	var lines []string
	used := 0
	dropped := 0
	for i := len(turns) - 1; i >= 0; i-- {
		line := formatReplayTurn(turns[i])
		if used+len(line) > replayTotalCap && len(lines) > 0 {
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
	fmt.Fprintf(&b, "## Recent conversation (journal replay: current epoch, seq %d–%d)", firstSeq, lastSeq)
	if dropped > 0 {
		fmt.Fprintf(&b, " — %d older turn(s) beyond the %dKB cap omitted; they remain in the journal", dropped, replayTotalCap/1024)
	}
	b.WriteString("\n\nThese turns are replayed verbatim from the journal (no summarization). The user's current message follows at the end.\n\n")
	b.WriteString(strings.Join(lines, "\n\n"))
	return b.String(), firstSeq, lastSeq
}

// formatReplayTurn renders one turn, truncating the text at replayTurnCap
// so no single turn dominates the block (marker keeps the receipt honest).
func formatReplayTurn(t replayTurn) string {
	text := t.text
	if len(text) > replayTurnCap {
		text = strings.TrimRight(text[:replayTurnCap], " \t\r\n") +
			fmt.Sprintf(" … [truncated at %dKB]", replayTurnCap/1024)
	}
	return fmt.Sprintf("**%s** (seq %d): %s", t.role, t.seq, text)
}

// buildReplay is the one-call helper for send paths: fold boundary (R3,
// legacy fallback for pre-schema distills) -> turns -> capped block. The
// returned boundary is journaled as replay receipt context.
func buildReplay(events []store.Event) (block string, firstSeq, lastSeq, boundary int) {
	boundary = foldBoundary(events)
	turns := collectReplayTurns(events, boundary)
	block, firstSeq, lastSeq = renderReplay(turns)
	return block, firstSeq, lastSeq, boundary
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
// downstream; seeds are per-turn truncated to replayTurnCap so a monster
// reply (a /panel answer can be >30KB) cannot flood the term set.
func recallQuery(text string, events []store.Event) string {
	if len(events) == 0 {
		return text
	}
	turns := collectReplayTurns(events, foldBoundary(events))
	if n := len(turns); n > recallCtxTurns {
		turns = turns[n-recallCtxTurns:]
	}
	var b strings.Builder
	b.WriteString(text)
	for _, t := range turns {
		seed := t.text
		if len(seed) > replayTurnCap {
			seed = seed[:replayTurnCap]
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
	fmt.Fprintf(&b, "\n\n> Distilled summary of folded events — details may be lossy, and anything after seq %d is not covered. The journal is authoritative: verify with `odo journal tail N` or `odo journal range A B` before relying on specifics.", boundary)
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
