package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

// M6 (Precision + Ledger) §4: the distiller's contradiction pass. When a new
// epoch note contradicts an older one ("Auth switched from JWT to session
// cookies" vs "Authentication uses JWT"), the daemon retracts the stale note
// WITH A RECORD (ADR-0003 inv 3): a memory_update{layer:"note",
// cause:"retract"} journal event. The old note's file is never mutated (inv
// 2: epoch notes are append-only records); the recall path (recall.go)
// filters retracted notes out of the injection set. Detection is a
// daemon-side token heuristic — no LLM in the data path (inv 4).

const (
	// contradictionScanCap bounds the older notes scanned (newest-first,
	// same ordering as the curator's allEpochNotes). It exists to bound the
	// scan on a pathological note set; at normal scale it never trips.
	contradictionScanCap = 50

	// contradictionSnippetCap bounds the contradicting sentence quoted in
	// the journaled retraction detail.
	contradictionSnippetCap = 120
)

// contradictionSignals is a change/negation token set. A new-note sentence
// containing one of these as a token AND sharing ≥1 salient token with an
// older note's sentence flags a contradiction. ("not" is a stop-word for
// keyword recall but a signal here — the two paths never mix token sets.)
var contradictionSignals = map[string]bool{
	"not": true, "no": true, "longer": true, "switched": true,
	"replaced": true, "removed": true, "instead": true, "changed": true,
	"migrated": true, "deprecated": true,
}

// splitSentences splits text on ". " (keeping the period), "!"/"?" followed
// by a newline, and bare newlines. Returns trimmed, non-empty sentences.
func splitSentences(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		rest := line
		for {
			i := strings.Index(rest, ". ")
			if i < 0 {
				break
			}
			if s := strings.TrimSpace(rest[:i+1]); s != "" {
				out = append(out, s)
			}
			rest = rest[i+2:]
		}
		if s := strings.TrimSpace(rest); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ruleTokens splits a normalizeRule-normalized sentence on whitespace and
// trims punctuation edges so sentence-final tokens compare equal with their
// mid-sentence forms ("jwt." ≡ "jwt"). Empty-after-trim tokens are dropped.
func ruleTokens(sentence string) []string {
	var out []string
	for _, tok := range strings.Fields(normalizeRule(sentence)) {
		tok = strings.Trim(tok, `.,;:!?"'()[]{}`)
		if tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// salientTokens returns the non-stopword tokens of a sentence (same
// stopWords set as keyword recall): `jwt` survives; `the`/`to` do not.
func salientTokens(sentence string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range ruleTokens(sentence) {
		if !stopWords[tok] {
			out[tok] = true
		}
	}
	return out
}

// contradiction is one flagged (old note, new sentence) pair. The file of
// the old note is never touched — the report is journaled instead.
type contradiction struct {
	oldNote string // "<ws>-epoch-<N>"
	newNote string // "<ws>-epoch-<M>" (filled by runContradictionPass)
	snippet string // the contradicting sentence (truncated to 120 chars)
}

// detectContradictions compares the just-written note against older epoch
// notes (newest-first, capped at contradictionScanCap). A new-note sentence
// is a candidate when it carries a contradiction-signal token; it flags an
// old note when it shares ≥1 salient token with any sentence of that note.
// One contradiction per (old note, new sentence) pair; the first matching
// old sentence wins (keeps reports bounded). May return nil.
func detectContradictions(newNote string, oldNotes []epochNote) []contradiction {
	var out []contradiction
	for _, sent := range splitSentences(newNote) {
		toks := ruleTokens(sent)
		signaled := false
		for _, tok := range toks {
			if contradictionSignals[tok] {
				signaled = true
				break
			}
		}
		if !signaled {
			continue // affirmative additions never flag
		}
		salient := map[string]bool{}
		for _, tok := range toks {
			if !stopWords[tok] {
				salient[tok] = true
			}
		}
		if len(salient) == 0 {
			continue
		}
		snippet := sent
		if r := []rune(snippet); len(r) > contradictionSnippetCap {
			snippet = string(r[:contradictionSnippetCap])
		}
		for i, on := range oldNotes {
			if i >= contradictionScanCap {
				break
			}
			for _, oldSent := range splitSentences(on.content) {
				shared := false
				for tok := range salientTokens(oldSent) {
					if salient[tok] {
						shared = true
						break
					}
				}
				if shared {
					out = append(out, contradiction{oldNote: on.name, snippet: snippet})
					break // first matching old sentence wins
				}
			}
		}
	}
	return out
}

// runContradictionPass compares the just-written note against ALL older
// epoch notes of its workstream (the full note set via allEpochNotes — not
// the query-selected 12 KB recall window, which would miss the
// contradiction by construction). Each contradiction is journaled as
// memory_update{layer:"note", cause:"retract"} with before_sha == after_sha
// (the retraction is a journal record, not a file mutation). Returns the
// count (journaled on the distill review_action as "contradictions").
// Journaling failures are logged, never fatal — the distill succeeds.
func (s *Server) runContradictionPass(ctx context.Context, conversationID int64, noteName, noteContent string, epoch int) int {
	notes, err := allEpochNotes(s.projectRoot)
	if err != nil {
		log.Printf("contradiction pass: read notes: %v", err)
		return 0
	}
	ws := strings.TrimSuffix(noteName, fmt.Sprintf("-epoch-%d", epoch))
	var olds []epochNote
	for _, n := range notes {
		if n.name == noteName || n.workstream != ws {
			continue // the just-written note and other workstreams never flag
		}
		olds = append(olds, n)
	}
	found := detectContradictions(noteContent, olds)

	contents := make(map[string]string, len(olds))
	for _, n := range olds {
		contents[n.name] = n.content
	}
	for _, c := range found {
		sha := sha16([]byte(contents[c.oldNote]))
		if _, err := s.store.AppendEvent(ctx, conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":      "note",
			"cause":      "retract",
			"detail":     fmt.Sprintf("%s contradicted by %s: %s", c.oldNote, noteName, c.snippet),
			"before_sha": sha,
			"after_sha":  sha,
		})); err != nil {
			log.Printf("contradiction pass: journal retract %s: %v", c.oldNote, err)
		}
	}
	return len(found)
}

// lastRecallEvent scans the conversation's events newest-first for the last
// user_message that carries a recall key (steering messages journal no
// recall key, so they are skipped). nil when no such event exists.
func lastRecallEvent(events []store.Event) *store.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != store.EventUserMessage {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(events[i].Payload, &p); err != nil {
			continue
		}
		if _, ok := p["recall"].([]interface{}); ok {
			return &events[i]
		}
	}
	return nil
}

// lastRecallCount returns the length of the last user_message's recall
// array (M6 rows: item count; pre-M6 rows: path count). Used by the ledger
// writer for the "recall notes" metric. 0 when no recall-carrying
// user_message exists (first distill).
func lastRecallCount(events []store.Event) int {
	ev := lastRecallEvent(events)
	if ev == nil {
		return 0
	}
	var p map[string]interface{}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return 0
	}
	raw, _ := p["recall"].([]interface{})
	return len(raw)
}
