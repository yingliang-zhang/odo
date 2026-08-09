package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

// recallMemoryCap bounds the total recalled memory injected into a prompt.
// Wiki notes are distill summaries (small by design); the cap keeps a
// long-running project from overwhelming the agent's context window. Notes
// are included most-recent-first; the cut happens on a note boundary so no
// note is half-included.
const recallMemoryCap = 12 * 1024 // 12 KB ≈ 3k tokens

// foldBoundary returns the journal position of the latest epoch fold (R3):
// the last_seq recorded on the newest review_action{action:"distill"}
// payload, falling back to that event's OWN seq for pre-schema distills
// (the legacy implicit contract ChatSurface also implements). Events are
// seq-ascending; the boundary is the max over all distill markers.
func foldBoundary(events []store.Event) int {
	boundary := 0
	for _, ev := range events {
		if ev.Type != store.EventReviewAction {
			continue
		}
		var p struct {
			Action  string `json:"action"`
			LastSeq int    `json:"last_seq"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || p.Action != "distill" {
			continue
		}
		b := p.LastSeq
		if b <= 0 {
			b = ev.Seq // pre-R3 distill: its own seq is the fold position
		}
		if b > boundary {
			boundary = b
		}
	}
	return boundary
}

// wikiEpochRe parses the epoch number out of a wiki note name
// (<workstream>-epoch-<N>.md).
var wikiEpochRe = regexp.MustCompile(`-epoch-(\d+)\.md$`)

// wikiNoteEpoch extracts the epoch from a wiki note path. Callers skip
// unparseable names defensively.
func wikiNoteEpoch(path string) (int, bool) {
	m := wikiEpochRe.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// recallItem is one recalled note with the query terms that matched it (M6).
// matchedTerms is empty for notes included purely by newest-first fallback
// (the unmatched tier). The journal's user_message recall payload serializes
// this as {"path":"…","matched_terms":[…]} (matched_terms omitted when empty).
type recallItem struct {
	path         string
	matchedTerms []string
}

// stopWords are filtered from the query — they appear in nearly every note
// and carry no topical signal. The list is small and fixed (no i18n, no
// config) to keep recall deterministic and dependency-free.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "been": true, "being": true, "have": true,
	"has": true, "had": true, "do": true, "does": true, "did": true,
	"will": true, "would": true, "should": true, "could": true, "can": true,
	"this": true, "that": true, "these": true, "those": true, "it": true,
	"its": true, "in": true, "on": true, "at": true, "to": true, "for": true,
	"of": true, "with": true, "and": true, "or": true, "but": true, "not": true,
	"how": true, "what": true, "why": true, "when": true, "who": true,
	"i": true, "you": true, "we": true, "they": true, "me": true, "my": true,
}

// queryTokenRe splits the lowercased query on non-alphanumeric runs.
var queryTokenRe = regexp.MustCompile(`[^a-z0-9]+`)

// tokenizeQuery lowercases text, splits it on non-alphanumeric runs, drops
// stop-words, and de-duplicates preserving first-seen order.
func tokenizeQuery(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range queryTokenRe.Split(strings.ToLower(text), -1) {
		if tok == "" || stopWords[tok] || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// noteMatches returns the subset of terms found as case-insensitive
// substrings in the note's filename plus content (de-duplicated,
// order-stable). Empty when nothing matched.
func noteMatches(content, name string, terms []string) []string {
	if len(terms) == 0 {
		return nil
	}
	haystack := strings.ToLower(name + " " + content)
	var out []string
	for _, term := range terms {
		if strings.Contains(haystack, term) {
			out = append(out, term)
		}
	}
	return out
}

// recallWikiNotes reads all wiki/<workstreamName>-epoch-*.md files for the
// workstream and selects the injection set by keyword tiering (M6): notes
// matching ≥1 query token rank first (match-count DESC, then epoch DESC),
// unmatched notes follow (epoch DESC). The result is concatenated under
// headers and truncated to recallMemoryCap on a note boundary — the cap and
// the boundary cut are unchanged; only the ORDER changed. Notes held back
// by the cap are counted in a trailing marker line naming the pull path.
// Notes named in
// retracted (the journal's `<ws>-epoch-<N>` retraction set) are skipped.
// Returns the memory block ("" when no notes exist), the included items
// (paths + matched terms, for journaling), and noteBytes — the exact block
// string injected per note (`## <basename>\n\n<content>\n\n---\n\n`) so the
// injection receipt can hash precisely what the prompt carried. An empty
// query degrades to pure newest-first (the pre-M6 behavior).
func recallWikiNotes(projectRoot, workstreamName, query string, retracted map[string]bool) (memory string, items []recallItem, noteBytes [][]byte) {
	matches, err := filepath.Glob(filepath.Join(projectRoot, "wiki", workstreamName+"-epoch-*.md"))
	if err != nil {
		return "", nil, nil
	}
	type note struct {
		path    string
		name    string // <ws>-epoch-<N> (basename without .md)
		epoch   int
		content string
		matched []string
	}
	terms := tokenizeQuery(query)
	notes := make([]note, 0, len(matches))
	for _, m := range matches {
		epoch, ok := wikiNoteEpoch(m)
		if !ok {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(m), ".md")
		if retracted[name] {
			continue // retracted by the contradiction pass: record stays, injection stops
		}
		content, err := os.ReadFile(m)
		if err != nil {
			continue // note vanished between glob and read: skip it
		}
		notes = append(notes, note{
			path: m, name: name, epoch: epoch,
			content: string(content),
			matched: noteMatches(string(content), name, terms),
		})
	}
	// Two tiers: matched (match-count DESC, epoch DESC) then unmatched
	// (epoch DESC). sort.SliceStable keeps the comparator total.
	sort.SliceStable(notes, func(i, j int) bool {
		mi, mj := len(notes[i].matched) > 0, len(notes[j].matched) > 0
		if mi != mj {
			return mi // matched tier first
		}
		if mi && len(notes[i].matched) != len(notes[j].matched) {
			return len(notes[i].matched) > len(notes[j].matched)
		}
		return notes[i].epoch > notes[j].epoch
	})

	var b strings.Builder
	omitted := 0
	for i, n := range notes {
		block := "## " + filepath.Base(n.path) + "\n\n" + n.content + "\n\n---\n\n"
		if b.Len()+len(block) > recallMemoryCap {
			omitted = len(notes) - i // cut on a note boundary: no note is half-included
			break
		}
		b.WriteString(block)
		items = append(items, recallItem{path: n.path, matchedTerms: n.matched})
		noteBytes = append(noteBytes, []byte(block))
	}
	if omitted > 0 {
		// M6.1 visibility signal: the cap's silent drop was the recall
		// layer's accounting gap — name what is held back and where to pull
		// it so "not recalled" never reads as "does not exist".
		fmt.Fprintf(&b, "_%d more note(s) held back by the %dKB recall cap — pull them from `%s` (e.g. `odo wiki read main-epoch-3`)._\n",
			omitted, recallMemoryCap/1024, filepath.Join(projectRoot, "wiki"))
	}
	if b.Len() == 0 {
		return "", nil, nil
	}
	return b.String(), items, noteBytes
}

// retractedNotes reads the conversation's memory_update{layer:"note"} events
// and returns the set of `<ws>-epoch-<N>` note names currently retracted.
// The detail format is `<oldNote> contradicted by <newNote>: <snippet>` — the
// first token is the retracted note name. Events apply in journal order: a
// retract adds, an unretract removes (forward-compatible; M6 never emits
// unretract, so a retraction stands for the milestone).
func (s *Server) retractedNotes(ctx context.Context, conversationID int64) map[string]bool {
	out := map[string]bool{}
	events, err := s.store.ListEvents(ctx, conversationID, 0)
	if err != nil {
		return out
	}
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
		case "retract":
			out[name] = true
		case "unretract":
			delete(out, name)
		}
	}
	return out
}

// userMemoryCap bounds the global user memory injected into every prompt.
// Durable principles are few by nature; the cap keeps steering small by
// design (ADR-0003).
const userMemoryCap = 4 * 1024 // 4 KB ≈ 1k tokens

// readUserMemory reads ~/.odo/user.md (global, user-maintained durable
// principles and preferences). Returns "" when the file is absent or empty.
// Content is capped at userMemoryCap with a line-boundary cut. M3 only
// reads this file; M4 adds the learner that writes it.
func readUserMemory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".odo", "user.md"))
	if err != nil {
		return ""
	}
	return capAtLineBoundary(string(b), userMemoryCap)
}

// capAtLineBoundary trims s to cap bytes, cutting at the last newline so no
// line is half-kept; returns "" when the content is blank or no complete
// line fits under the cap.
func capAtLineBoundary(s string, cap int) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	if len(s) > cap {
		cut := strings.LastIndex(s[:cap], "\n")
		if cut < 0 {
			return "" // no complete line fits under the cap
		}
		s = strings.TrimRight(s[:cut], " \t\r\n")
	}
	return s
}
