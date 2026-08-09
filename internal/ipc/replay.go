package ipc

import (
	"encoding/json"
	"fmt"
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
