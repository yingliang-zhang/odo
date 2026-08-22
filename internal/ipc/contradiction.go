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
// cookies" vs "Authentication uses JWT"), the daemon journals an ADVISORY
// record (ADR-0003 inv 3): memory_update{layer:"note",
// cause:"contradiction_candidate"}. Detection is advisory-only
// (2026-08-22): the 2-token overlap heuristic mass-false-retracted in
// production (25 of 28 distills journaled a retract; a whole epoch series
// was wiped out of recall before a human could disagree), so the daemon's
// automatic behavior stops at the candidate row — only the curated/human
// paths ever journal cause:"retract", and only cause:"retract" filters the
// recall injection set (recall.go) and the auto_age clock. A candidate row
// therefore never hides a note; it is the flag a curator or human resolves.
// The old note's file is never mutated (inv 2: epoch notes are append-only
// records) and before_sha == after_sha on every row this pass writes.
// Detection is a daemon-side token heuristic — no LLM in the data path
// (inv 4).

const (
	// contradictionScanCap bounds the older notes scanned (newest-first,
	// same ordering as the curator's allEpochNotes). It exists to bound the
	// scan on a pathological note set; at normal scale it never trips.
	contradictionScanCap = 50

	// contradictionSnippetCap bounds the contradicting sentence quoted in
	// the journaled candidate detail.
	contradictionSnippetCap = 120

	// contradictionOverlapMin is the count of shared NON-SIGNAL salient
	// tokens a candidate sentence must have with an old-note sentence to
	// flag a contradiction. 1 was the M6 barn door — any single shared
	// keyword ("journal", "window") plus a negation token flagged; 2
	// requires topical coincidence before a note is declared suspect.
	contradictionOverlapMin = 2
)

// contradictionSignals is a change/negation token set. A new-note sentence
// containing one of these as a token AND sharing ≥2 NON-SIGNAL salient
// tokens with an older note's sentence flags a contradiction. ("not" is a
// stop-word for keyword recall but a signal here — the two paths never mix
// token sets.)
//
// M17 F2: signal tokens are a candidate GATE ONLY — they never join the
// salient-overlap set. Production false positive (journal seqs 5144–5149):
// a post-reset note's scope disclaimer ("seq 1–4907 was omitted … is not
// covered") candidated on the signal "not" and the pre-M17 ≥1-overlap barn
// door then matched ANY older sentence sharing a single content-free
// salient token (e.g. "no"-shaped negation prose — "no" is a signal, not a
// stopword — or one generic keyword). Six valid notes retracted on six
// single-token coincidences. ("not" itself is in recall's stopWords, so it
// never joined EITHER salient set — its only role was gating candidacy;
// m6_test.go states the mechanism accurately.) query-time stopWords stays
// untouched: negations remain salient for recall scoring, just never for
// candidate overlap.
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
// old note when it shares ≥2 NON-SIGNAL salient tokens with any sentence of
// that note. One contradiction per (old note, new sentence) pair; the first
// matching old sentence wins (keeps reports bounded). May return nil.
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
		// Salient-overlap set: non-stopword AND non-signal. Signals gate the
		// candidate; letting them also overlap made any negated boilerplate
		// ("is not covered") retract any sentence carrying a negation.
		salient := map[string]bool{}
		for _, tok := range toks {
			if !stopWords[tok] && !contradictionSignals[tok] {
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
				overlap := 0
				for tok := range salientTokens(oldSent) {
					if salient[tok] && !contradictionSignals[tok] {
						overlap++
					}
				}
				if overlap >= contradictionOverlapMin {
					out = append(out, contradiction{oldNote: on.name, snippet: snippet})
					break // first matching old sentence wins
				}
			}
		}
	}
	return out
}

// flaggedNoteSet is the contradiction pass's dedup view of the journal:
// the note names already ON THE RECORD — retracted (cause:"retract",
// journaled only by curated/human paths) or flagged contradiction-candidate
// by an earlier pass. Same detail contract as RetractionSetFromEvents
// (first token = `<ws>-epoch-<N>` note name; a retract/candidate adds, an
// unretract removes: the repair clears the note's flag for future passes).
// It gates ONLY candidate re-journaling — recall/age gating keeps honoring
// cause:"retract" alone, so an advisory row never filters the injection
// set. RetractionSetFromEvents' exported semantics are deliberately
// unchanged: this is the sibling that counts candidates as flagged.
func flaggedNoteSet(events []store.Event) map[string]bool {
	out := map[string]bool{}
	for _, ev := range events {
		if ev.Type != store.EventMemoryUpdate {
			continue
		}
		var p struct {
			Layer  string `json:"layer"`
			Cause  string `json:"cause"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Layer != "note" {
			continue
		}
		name, _, _ := strings.Cut(p.Detail, " ")
		if name == "" {
			continue
		}
		switch p.Cause {
		case "retract", "contradiction_candidate":
			out[name] = true
		case "unretract":
			delete(out, name)
		}
	}
	return out
}

// runContradictionPass compares the just-written note against ALL older
// epoch notes of its workstream (the full note set via allEpochNotes — not
// the query-selected 12 KB recall window, which would miss the
// contradiction by construction). Each flagged note is journaled as an
// ADVISORY memory_update{layer:"note", cause:"contradiction_candidate"}
// with before_sha == after_sha (the candidate is a journal record, not a
// file mutation — and it never filters recall; see the file header for the
// 2026-08-22 advisory-only contract and the production false-positive
// evidence behind it). A note already on the journal's flagged set — by a
// curated/human cause:"retract" or by an earlier candidate row — is not
// re-journaled, and repeated flags against one note inside a single pass
// journal once (no duplicate records). Returns the journaled count
// (recorded on the distill review_action as "contradictions").
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
	// Cross-pass dedup: a note already flagged (candidate row from an
	// earlier pass, or a curated/human retract) is out of the advisory
	// surface; re-journaling it would only duplicate the record. The scan
	// fails open (empty set) like the recall path's own fold — a missed
	// dedup costs one duplicate advisory, never a lost note.
	flagged := map[string]bool{}
	if events, err := s.store.ListEvents(ctx, conversationID, 0); err != nil {
		log.Printf("contradiction pass: list events: %v", err)
	} else {
		flagged = flaggedNoteSet(events)
	}
	journaled := 0
	for _, c := range found {
		if flagged[c.oldNote] {
			continue // already on the record: don't re-journal
		}
		sha := sha16([]byte(contents[c.oldNote]))
		if _, err := s.store.AppendEvent(ctx, conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":      "note",
			"cause":      "contradiction_candidate",
			"detail":     fmt.Sprintf("%s contradicted by %s: %s", c.oldNote, noteName, c.snippet),
			"before_sha": sha,
			"after_sha":  sha,
		})); err != nil {
			log.Printf("contradiction pass: journal candidate %s: %v", c.oldNote, err)
			continue
		}
		journaled++
		// Two new-note sentences can flag the same old note; one candidate
		// record per note suffices.
		flagged[c.oldNote] = true
	}
	return journaled
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
