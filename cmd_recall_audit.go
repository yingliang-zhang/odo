package main

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

	"github.com/yingliang-zhang/odo/internal/ipc"
	"github.com/yingliang-zhang/odo/internal/store"
)

// M12 Batch 3a (D-semantic step 0): `odo recall audit` sizes the keyword
// recall miss rate before any engine (FTS5/embedding) is built — if the
// miss class is near zero, the spike's answer is already "don't build it".
// It scans user_message events across ALL conversations of the bound
// project and measures, from each event's journaled recall payload:
// messages, recall items per message, the miss class (zero matched notes
// despite ≥3 extracted query terms), the matched_terms distribution, the
// top matched vs never-matched note names, and the newest miss-class
// queries — the labeled pool for the embedding spike. Slash-command
// messages (/panel, /vision) journal no recall key — they carry only
// receipt+context_scope+total_prompt_bytes — so they are excluded from
// the miss class and reported as their own bucket; the miss rate counts
// only evidence-bearing runs.
//
// Like `odo journal` / `odo todo`, the transport is the agent's own shell
// — no daemon, no socket; the journal is opened READ-ONLY (query_only) so
// a live daemon's ownership is never disturbed. No LLM: pure SQL + journal
// parsing.
//
//	odo recall audit [--last N] [--json]
//
// Output: a compact human report (stdout); --json emits the same data as
// one machine-readable object.

const recallAuditUsage = `usage: odo recall audit [--last N] [--json]
  audit         recall miss-rate report over user_message events across ALL
                conversations of the bound project (read-only, no daemon)
  --last N      only the newest N user messages per conversation (default: all)
  --json        machine-readable report (one JSON object)`

// missAuditTermFloor is the extracted-query-term floor for the miss class:
// below 3 terms a query never gave keyword recall enough substance to
// blame it for finding nothing. The audit counts terms from the journaled
// message text alone, while production recall queries union that text with
// the last 3 current-epoch turns (recallQuery/recallCtxTurns) — so this
// floor is a conservative lower bound on the term count production saw.
const missAuditTermFloor = 3

// auditSlashCommands mirrors the slash commands handleSendMessage routes
// in internal/ipc/server.go (/panel, /vision, /preview) — keep the two in
// sync. Slash user_messages journal no recall key, so text-tokenized
// recall evidence does not exist for them; without this gate every slash
// message with ≥ missAuditTermFloor terms would classify as a miss.
var auditSlashCommands = []string{"/panel", "/vision", "/preview"}

// isSlashMessage reports whether a journaled user_message text is a slash
// payload, mirroring the daemon's routing rule: the trimmed text is
// "<cmd>" or "<cmd> <args>" for a routed command.
func isSlashMessage(text string) bool {
	t := strings.TrimSpace(text)
	for _, cmd := range auditSlashCommands {
		if t == cmd || strings.HasPrefix(t, cmd+" ") {
			return true
		}
	}
	return false
}

// missPoolSize bounds the labeled query pool printed for the spike.
const missPoolSize = 10

// noteTallySize bounds the top matched / never-matched note name lists.
const noteTallySize = 10

// recallAuditNote tallies one note name across user messages.
type recallAuditNote struct {
	Name     string `json:"name"`
	Messages int    `json:"messages"`
}

// recallAuditMiss is the miss-class summary: messages with ≥ missAuditTermFloor
// extracted query terms whose recall carried zero matched notes.
type recallAuditMiss struct {
	Count int     `json:"count"`
	Rate  float64 `json:"rate"` // count / evidence-bearing messages (user − excluded slash; 0 when none)
}

// recallAuditMissQuery is one labeled miss-class query (journal
// coordinates + truncated text) — the spike's input pool.
type recallAuditMissQuery struct {
	Workstream     string `json:"workstream"`
	ConversationID int64  `json:"conversation_id"`
	Seq            int    `json:"seq"`
	CreatedAt      string `json:"created_at"`
	Query          string `json:"query"` // rune-truncated to 80 chars
}

// recallAuditReport is the --json shape (field names frozen — the spike
// tooling consumes them).
type recallAuditReport struct {
	ProjectRoot            string                 `json:"project_root"`
	Journal                string                 `json:"journal"`
	WorkstreamsScanned     int                    `json:"workstreams_scanned"`
	ConversationsScanned   int                    `json:"conversations_scanned"`
	LastPerConversation    int                    `json:"last_per_conversation"` // 0 = all
	UserMessages           int                    `json:"user_messages"`
	ItemsPerMessage        map[string]int         `json:"items_per_message"`         // buckets: 0, 1, 2, 3-5, 6+
	MeanItemsPerMessage    float64                `json:"mean_items_per_message"`    // recall items, fixed layers included
	MatchedTermsPerMessage map[string]int         `json:"matched_terms_per_message"` // buckets: 0, 1, 2, 3, 4, 5+
	Miss                   recallAuditMiss        `json:"miss"`                      // ≥3 query terms, zero matched notes
	ExcludedSlashMessages  int                    `json:"excluded_slash_messages"`   // slash payloads journal no recall key — no miss evidence
	TopMatchedNotes        []recallAuditNote      `json:"top_matched_notes"`
	NeverMatchedNotes      []recallAuditNote      `json:"never_matched_notes"`
	MissQueries            []recallAuditMissQuery `json:"miss_queries,omitempty"`
}

// epochNoteNameRe spots an epoch-note recall item by basename
// (<ws>-epoch-<N>.md). Unexported in ipc; the audit keeps its own copy —
// it must ALSO classify journals written by older daemons, so it cannot
// lean on the current origin field being present.
var epochNoteNameRe = regexp.MustCompile(`-epoch-\d+\.md$`)

// auditItem is one parsed recall payload entry.
type auditItem struct {
	path    string
	matched []string
	origin  string
}

// noteName returns the tally name when the item is a wiki note (epoch
// note, topic page, or an origin-labeled cross source) and ok=false for
// fixed layers (user.md, memory.md, pins, skills, index).
func (it auditItem) noteName() (string, bool) {
	if it.origin == "topic" || it.origin == "sibling" {
		return filepath.Base(it.path), true
	}
	base := filepath.Base(it.path)
	if epochNoteNameRe.MatchString(base) {
		return base, true
	}
	if strings.Contains(filepath.ToSlash(it.path), "/wiki/topics/") {
		return base, true
	}
	return "", false
}

// auditMsg is one scanned user_message.
type auditMsg struct {
	ws        string
	convID    int64
	seq       int
	createdAt string
	text      string
	slash     bool // routed slash payload: journals no recall key, excluded from the miss class
	terms     int  // extracted query terms (exactly the recall tokenization)
	items     []auditItem
}

// matchedItems counts items that carried ≥1 matched term.
func (m auditMsg) matchedItems() (n int) {
	for _, it := range m.items {
		if len(it.matched) > 0 {
			n++
		}
	}
	return n
}

// matchedTermTotal sums matched terms across items.
func (m auditMsg) matchedTermTotal() (n int) {
	for _, it := range m.items {
		n += len(it.matched)
	}
	return n
}

// isMiss classifies the miss: enough query substance to blame recall, but
// recall found nothing.
func (m auditMsg) isMiss() bool {
	return m.terms >= missAuditTermFloor && m.matchedItems() == 0
}

// parseRecallMsg decodes a user_message payload into an auditMsg (terms
// filled by the caller from the exported recall tokenizer). Defensive on
// shape: pre-M6 journals stored recall as []string — non-object entries
// parse to an empty item (no path, no terms) and never crash the audit.
func parseRecallMsg(payload json.RawMessage) (text string, items []auditItem) {
	var p struct {
		Text   string            `json:"text"`
		Recall []json.RawMessage `json:"recall"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return "", nil
	}
	for _, raw := range p.Recall {
		var entry struct {
			Path   string   `json:"path"`
			Terms  []string `json:"matched_terms"`
			Origin string   `json:"origin"`
		}
		if json.Unmarshal(raw, &entry) != nil || entry.Path == "" {
			continue // legacy string entries: no match signal, skip
		}
		items = append(items, auditItem{path: entry.Path, matched: entry.Terms, origin: entry.Origin})
	}
	return p.Text, items
}

// collectRecallAudit scans every active workstream's active conversation
// of the bound project. lastN > 0 keeps only the newest lastN user
// messages per conversation.
func collectRecallAudit(ctx context.Context, jp journalProj, lastN int) (msgs []auditMsg, report recallAuditReport, err error) {
	report.ProjectRoot = jp.project.RootPath
	report.Journal = filepath.Join(jp.project.RootPath, ".odo", "journal.sqlite")
	report.LastPerConversation = lastN
	report.ItemsPerMessage = map[string]int{"0": 0, "1": 0, "2": 0, "3-5": 0, "6+": 0}
	report.MatchedTermsPerMessage = map[string]int{"0": 0, "1": 0, "2": 0, "3": 0, "4": 0, "5+": 0}

	streams, err := jp.store.ListWorkstreams(ctx, jp.project.ID)
	if err != nil {
		return nil, report, err
	}
	report.WorkstreamsScanned = len(streams)
	for _, w := range streams {
		c, cerr := jp.store.GetActiveConversation(ctx, w.ID)
		if cerr != nil {
			continue // workstreams without an active conversation contribute nothing
		}
		report.ConversationsScanned++
		events, lerr := jp.store.ListEvents(ctx, c.ID, 0)
		if lerr != nil {
			continue // a half-readable conversation must not sink the whole audit
		}
		var convMsgs []auditMsg
		for _, ev := range events {
			if ev.Type != store.EventUserMessage {
				continue
			}
			text, items := parseRecallMsg(ev.Payload)
			convMsgs = append(convMsgs, auditMsg{
				ws: w.Name, convID: c.ID, seq: ev.Seq, createdAt: ev.CreatedAt,
				text: text, slash: isSlashMessage(text), terms: len(ipc.TokenizeQuery(text)), items: items,
			})
		}
		if lastN > 0 && len(convMsgs) > lastN {
			convMsgs = convMsgs[len(convMsgs)-lastN:]
		}
		msgs = append(msgs, convMsgs...)
	}
	return msgs, report, nil
}

// tallyInto folds one auditMsg into the report aggregates.
func tallyInto(report *recallAuditReport, m auditMsg, matched, never map[string]int) {
	report.UserMessages++
	n := len(m.items)
	report.MeanItemsPerMessage += float64(n)
	switch {
	case n == 1:
		report.ItemsPerMessage["1"]++
	case n == 2:
		report.ItemsPerMessage["2"]++
	case n >= 3 && n <= 5:
		report.ItemsPerMessage["3-5"]++
	case n >= 6:
		report.ItemsPerMessage["6+"]++
	default:
		report.ItemsPerMessage["0"]++
	}
	tt := m.matchedTermTotal()
	switch {
	case tt == 1:
		report.MatchedTermsPerMessage["1"]++
	case tt == 2:
		report.MatchedTermsPerMessage["2"]++
	case tt == 3:
		report.MatchedTermsPerMessage["3"]++
	case tt == 4:
		report.MatchedTermsPerMessage["4"]++
	case tt >= 5:
		report.MatchedTermsPerMessage["5+"]++
	default:
		report.MatchedTermsPerMessage["0"]++
	}
	for _, it := range m.items {
		name, ok := it.noteName()
		if !ok {
			continue
		}
		if len(it.matched) > 0 {
			matched[name]++
		} else {
			never[name]++
		}
	}
	// Slash messages journal no recall key — excluded from the miss class
	// and counted in their own bucket so the rate sees only
	// evidence-bearing runs.
	if m.slash {
		report.ExcludedSlashMessages++
	} else if m.isMiss() {
		report.Miss.Count++
	}
}

// finalizeReport completes derived fields: mean, miss rate, top-N tallies,
// and the labeled miss query pool.
func finalizeReport(report *recallAuditReport, msgs []auditMsg, matched, never map[string]int) {
	if report.UserMessages > 0 {
		report.MeanItemsPerMessage /= float64(report.UserMessages)
	}
	// The miss rate counts only evidence-bearing runs: slash messages
	// journal no recall key, so they can neither hit nor miss.
	if evidence := report.UserMessages - report.ExcludedSlashMessages; evidence > 0 {
		report.Miss.Rate = float64(report.Miss.Count) / float64(evidence)
	}
	report.TopMatchedNotes = topNotes(matched, noteTallySize)
	report.NeverMatchedNotes = topNotes(never, noteTallySize)
	var misses []recallAuditMissQuery
	for _, m := range msgs {
		if m.slash || !m.isMiss() {
			continue
		}
		misses = append(misses, recallAuditMissQuery{
			Workstream: m.ws, ConversationID: m.convID, Seq: m.seq,
			CreatedAt: m.createdAt, Query: truncateRunes(m.text, 80),
		})
	}
	// Newest first (created_at is ISO-ordered text), deterministic on ties.
	sort.SliceStable(misses, func(i, j int) bool {
		if misses[i].CreatedAt != misses[j].CreatedAt {
			return misses[i].CreatedAt > misses[j].CreatedAt
		}
		if misses[i].ConversationID != misses[j].ConversationID {
			return misses[i].ConversationID > misses[j].ConversationID
		}
		return misses[i].Seq > misses[j].Seq
	})
	if len(misses) > missPoolSize {
		misses = misses[:missPoolSize]
	}
	report.MissQueries = misses
}

// topNotes renders the top-n tally (count DESC, name ASC on ties) as a
// stable slice — never nil so --json omits nothing.
func topNotes(tally map[string]int, n int) []recallAuditNote {
	out := make([]recallAuditNote, 0, len(tally))
	for name, count := range tally {
		out = append(out, recallAuditNote{Name: name, Messages: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Messages != out[j].Messages {
			return out[i].Messages > out[j].Messages
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// truncateRunes clips s to n runes, adding an ellipsis when clipped
// (rune-safe: CJK queries never corrupt).
func truncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

// bucketLine renders an ordered bucket map as "k:v k:v …" — order fixed by
// the caller's key list (maps don't iterate in order).
func bucketLine(m map[string]int, keys []string) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+":"+strconv.Itoa(m[k]))
	}
	return strings.Join(parts, " ")
}

// renderAuditHuman prints the compact report (stdout).
func renderAuditHuman(r recallAuditReport) {
	fmt.Printf("odo recall audit — %s\n", r.ProjectRoot)
	last := "all"
	if r.LastPerConversation > 0 {
		last = strconv.Itoa(r.LastPerConversation)
	}
	fmt.Printf("journal: %s · %d workstream(s) · %d conversation(s) scanned · last per conversation: %s\n",
		r.Journal, r.WorkstreamsScanned, r.ConversationsScanned, last)
	if r.UserMessages == 0 {
		fmt.Println("no data: 0 user_message events found — nothing to audit yet")
		return
	}
	fmt.Printf("user messages: %d\n", r.UserMessages)
	fmt.Printf("recall items/message: mean %.2f · buckets %s\n",
		r.MeanItemsPerMessage, bucketLine(r.ItemsPerMessage, []string{"0", "1", "2", "3-5", "6+"}))
	fmt.Printf("matched terms/message: buckets %s\n",
		bucketLine(r.MatchedTermsPerMessage, []string{"0", "1", "2", "3", "4", "5+"}))
	evidence := r.UserMessages - r.ExcludedSlashMessages
	fmt.Printf("miss class (≥%d query terms, zero matched notes): %d/%d (%.1f%%)\n",
		missAuditTermFloor, r.Miss.Count, evidence, r.Miss.Rate*100)
	if r.ExcludedSlashMessages > 0 {
		fmt.Printf("excluded slash (no recall evidence journaled): %d\n", r.ExcludedSlashMessages)
	}

	if len(r.TopMatchedNotes) > 0 {
		fmt.Println("\ntop matched notes (messages):")
		for _, n := range r.TopMatchedNotes {
			fmt.Printf("  %-40s %d\n", n.Name, n.Messages)
		}
	}
	if len(r.NeverMatchedNotes) > 0 {
		fmt.Println("\nnever-matched notes (fallback inclusions):")
		for _, n := range r.NeverMatchedNotes {
			fmt.Printf("  %-40s %d\n", n.Name, n.Messages)
		}
	}
	if len(r.MissQueries) > 0 {
		fmt.Printf("\nmiss query pool (%d most recent, journal coordinates + ≤80-char query):\n", len(r.MissQueries))
		for _, q := range r.MissQueries {
			fmt.Printf("  [%s conv %d seq %d] %s · %s\n",
				q.Workstream, q.ConversationID, q.Seq, q.CreatedAt, q.Query)
		}
	}
}

// runRecallCLI dispatches `odo recall <sub>`. Only `audit` exists (M12
// Batch 3a); later subcommands (FTS5, spike) land beside it.
func runRecallCLI(args []string) int {
	lastN := 0
	jsonOut := false
	var positional []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--last" && i+1 < len(args):
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "odo recall: --last must be a positive integer, got %q\n", args[i+1])
				return 2
			}
			lastN = n
			i++
		case strings.HasPrefix(args[i], "--last="):
			n, err := strconv.Atoi(strings.TrimPrefix(args[i], "--last="))
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "odo recall: --last must be a positive integer, got %q\n", args[i])
				return 2
			}
			lastN = n
		case args[i] == "--json":
			jsonOut = true
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) != 1 || positional[0] != "audit" {
		fmt.Fprintln(os.Stderr, recallAuditUsage)
		return 2
	}

	ctx := context.Background()
	jp, closeStore, err := journalStore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo recall: %v\n", err)
		return 1
	}
	defer closeStore()

	msgs, report, err := collectRecallAudit(ctx, jp, lastN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "odo recall audit: %v\n", err)
		return 1
	}
	matched := map[string]int{}
	never := map[string]int{}
	for _, m := range msgs {
		tallyInto(&report, m, matched, never)
	}
	finalizeReport(&report, msgs, matched, never)

	if jsonOut {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "odo recall audit: marshal: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}
	renderAuditHuman(report)
	return 0
}
