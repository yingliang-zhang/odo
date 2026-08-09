package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo-agent/internal/store"
)

// M5 (Curation): the curator pass reads the FULL set of epoch notes across
// all workstreams and rewrites wiki/topics/*.md + wiki/index.md from scratch
// (generation-2 rule: never from the previous topic page — each pass is
// generation-1 from source notes, preventing confabulation drift). index.md
// is always-injected (≤2 KB); topic pages are curator-owned derived
// artifacts (ADR-0003 inv 2: rebuildable from the journal). Pins
// (.odo/pins.md) live in pins.go; the curator never touches them.

const (
	// curatorTimeout bounds the one-shot orchestrator curator run. The
	// curator reads all epoch notes and rewrites all topic pages, so it
	// needs the full distill-level budget (generation-2 rule: the curator
	// never reads previous topic pages, only source notes).
	curatorTimeout = 10 * time.Minute

	// curatorNoteCap bounds the number of epoch notes the curator reads.
	// Notes are newest-first across all workstreams. This bounds input cost
	// as the project grows; oldest notes beyond the cap remain in the
	// journal (M6 pull-based recall can retrieve them on demand).
	curatorNoteCap = 50

	// indexCap bounds wiki/index.md at 2 KB (ADR-0003: always-injected,
	// ≤2 KB). The cap is enforced at write time with a line-boundary cut
	// from the end (never drops a topic entirely, truncates the list).
	indexCap = 2 * 1024

	// pinsCap bounds .odo/pins.md at 2 KB at read time (line-boundary cut).
	// Pins are human-owned; the daemon writes them only via the pin IPC
	// command and never truncates at write time (refuse-on-overflow, like
	// user.md).
	pinsCap = 2 * 1024

	// topicFileCap bounds each wiki/topics/<slug>.md at 8 KB. Topic pages
	// are derived artifacts; a whole-bullet cut at the cap keeps pages
	// scannable without half-bullets.
	topicFileCap = 8 * 1024
)

// epochNote is one distilled wiki note staged for the curator prompt.
type epochNote struct {
	name       string // e.g. "main-epoch-3"
	workstream string
	epoch      int
	content    string
}

// allEpochNotes reads ALL wiki/<ws>-epoch-*.md files across ALL workstreams
// (not just the active one — curation is project-wide), sorted newest-epoch
// first with mtime as the tie-breaker for same-epoch notes across
// workstreams, and caps the set at curatorNoteCap notes.
func allEpochNotes(projectRoot string) ([]epochNote, error) {
	matches, err := filepath.Glob(filepath.Join(projectRoot, "wiki", "*-epoch-*.md"))
	if err != nil {
		return nil, err
	}
	type cand struct {
		note  epochNote
		mtime time.Time
	}
	var cands []cand
	for _, m := range matches {
		epoch, ok := wikiNoteEpoch(m)
		if !ok {
			continue // skip unparseable names defensively
		}
		fi, err := os.Stat(m)
		if err != nil {
			continue // vanished between glob and stat
		}
		base := filepath.Base(m)
		cands = append(cands, cand{
			note: epochNote{
				name:       strings.TrimSuffix(base, ".md"),
				workstream: strings.TrimSuffix(base, wikiEpochRe.FindString(base)),
				epoch:      epoch,
			},
			mtime: fi.ModTime(),
		})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].note.epoch != cands[j].note.epoch {
			return cands[i].note.epoch > cands[j].note.epoch
		}
		return cands[i].mtime.After(cands[j].mtime)
	})
	var notes []epochNote
	for _, cn := range cands {
		if len(notes) >= curatorNoteCap {
			break
		}
		content, err := os.ReadFile(filepath.Join(projectRoot, "wiki", cn.note.name+".md"))
		if err != nil {
			continue // vanished between stat and read: skip it
		}
		cn.note.content = string(content)
		notes = append(notes, cn.note)
	}
	return notes, nil
}

// curatorPrompt renders the curator one-shot prompt: the instruction plus
// every epoch note under a `--- <name> (workstream: <ws>, epoch: <N>) ---`
// header, newest-first.
func curatorPrompt(notes []epochNote) string {
	var b strings.Builder
	b.WriteString(`You are running odo's memory curator pass. Synthesize the epoch notes below into topic pages — one per topic area (e.g., "authentication", "build-system", "testing").

Output JSON ONLY (no prose, no markdown fence), exactly this shape:
{"topics":[{"title":"<Topic Title>","slug":"<topic-slug>","bullets":["- <statement> (epoch-N)","- <statement> (epoch-N)"]}]}

Rules:
- Each topic groups related decisions across workstreams and epochs.
- Every bullet MUST end with a "(epoch-N)" citation naming the source epoch note.
- A bullet without a citation is allowed but will be flagged in the UI as "uncited."
- The slug is lowercase, hyphenated, no spaces (e.g., "authentication").
- Do NOT copy previous topic pages — write from the source notes only (generation-1).
- 3-10 topics is typical; fewer for small projects.

=== EPOCH NOTES (newest-first) ===
`)
	for _, n := range notes {
		fmt.Fprintf(&b, "--- %s (workstream: %s, epoch: %d) ---\n%s\n\n",
			n.name, n.workstream, n.epoch, n.content)
	}
	return b.String()
}

// topic is one curator topic page as the one-shot emits it.
type topic struct {
	Title   string   `json:"title"`
	Slug    string   `json:"slug"`
	Bullets []string `json:"bullets"`
}

// curatorResult is the JSON object the curator one-shot must emit.
type curatorResult struct {
	Topics []topic `json:"topics"`
}

// validTopics filters the curator's topic list to the pages that actually
// get written: non-empty slugs resolving inside wiki/topics/, deduplicated
// by slug with the first occurrence winning — a repeat slug would
// otherwise silently overwrite the earlier page and double-list the index.
func validTopics(projectRoot string, res *curatorResult) []topic {
	dir := filepath.Join(projectRoot, "wiki", "topics")
	seen := make(map[string]struct{}, len(res.Topics))
	out := make([]topic, 0, len(res.Topics))
	for _, t := range res.Topics {
		if t.Slug == "" {
			continue
		}
		if filepath.Dir(filepath.Join(dir, t.Slug+".md")) != dir {
			continue // defensive: the slug must name a file inside wiki/topics/
		}
		if _, dup := seen[t.Slug]; dup {
			continue
		}
		seen[t.Slug] = struct{}{}
		out = append(out, t)
	}
	return out
}

// parseCuratorOutput decodes the curator one-shot's raw text — the same
// fence-tolerant extraction as parseLearnerOutput: parse from the first '{'
// to the last '}' rather than rejecting a markdown-fenced answer.
func parseCuratorOutput(raw string) (*curatorResult, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in curator output")
	}
	var res curatorResult
	if err := json.Unmarshal([]byte(raw[start:end+1]), &res); err != nil {
		return nil, fmt.Errorf("curator output JSON: %w", err)
	}
	return &res, nil
}

// renderTopicPage renders one topic page: `# <Title>` + one bullet per
// line, LF-joined with a single trailing newline. The page is capped at
// topicFileCap with a whole-bullet cut (never a half-bullet).
func renderTopicPage(title string, bullets []string) string {
	content := "# " + title + "\n\n" + strings.Join(bullets, "\n") + "\n"
	if len(content) <= topicFileCap {
		return content
	}
	if cut := capAtLineBoundary(content, topicFileCap); cut != "" {
		return cut + "\n"
	}
	return "# " + title + "\n" // no complete bullet fits under the cap
}

// writeTopicPages clears wiki/topics/*.md (the curator rewrites from scratch
// — generation-2 rule) and writes one page per valid topic, returning the
// written paths. Removing a single stale file is best-effort (logged, not
// fatal); a write failure is fatal so the on-disk set never mixes
// generations.
func writeTopicPages(projectRoot string, topics []topic) ([]string, error) {
	dir := filepath.Join(projectRoot, "wiki", "topics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create topics dir: %w", err)
	}
	stale, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob topics dir: %w", err)
	}
	for _, m := range stale {
		if err := os.Remove(m); err != nil {
			log.Printf("curate: remove stale topic %s: %v", m, err)
		}
	}
	var paths []string
	for _, t := range topics {
		path := filepath.Join(dir, t.Slug+".md")
		if err := writeFileAtomic(path, renderTopicPage(t.Title, t.Bullets), 0o644); err != nil {
			return paths, fmt.Errorf("write topic %s: %w", t.Slug, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// writeIndex regenerates wiki/index.md: one line per WRITTEN topic (the
// same filtered list writeTopicPages consumed — duplicates and invalid
// slugs never reach the index). The list is capped at indexCap (2 KB) with
// a line-boundary cut from the end — a topic that falls off the index
// still has its page on disk. The content is written atomically (0644) and
// returned for the injection receipt.
func writeIndex(projectRoot string, topics []topic) (string, error) {
	var b strings.Builder
	b.WriteString("# Project Wiki Index\n\n## Topics\n")
	for _, t := range topics {
		fmt.Fprintf(&b, "- %s → topics/%s.md\n", t.Title, t.Slug)
	}
	content := b.String()
	if len(content) > indexCap {
		if cut := capAtLineBoundary(content, indexCap); cut != "" {
			content = cut + "\n"
		}
	}
	if err := writeFileAtomic(filepath.Join(projectRoot, "wiki", "index.md"), content, 0o644); err != nil {
		return "", fmt.Errorf("write index: %w", err)
	}
	return content, nil
}

// readIndex reads <projectRoot>/wiki/index.md capped at indexCap with a
// line-boundary cut. "" when absent/empty. M5: always-injected (ADR-0003).
func readIndex(projectRoot string) string {
	b, err := os.ReadFile(filepath.Join(projectRoot, "wiki", "index.md"))
	if err != nil {
		return ""
	}
	return capAtLineBoundary(string(b), indexCap)
}

// handleCurate runs the curator pass: read the full epoch-note set, one
// orchestrator one-shot, rewrite topic pages + index.md from scratch.
// Unlike the learner (which degrades silently inside distill), curate is a
// standalone command: a failure journals memory_update{layer:curator,
// cause:failed} AND returns the error to the caller (spec §1).
func (s *Server) handleCurate(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("curate: %w", err)
	}
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}
	// One curate at a time (M11 P0). The slot is reserved under the mutex and
	// the multi-minute orchestrator pass below runs unlocked, so other
	// connections stay responsive.
	s.mu.Lock()
	if s.curating {
		s.mu.Unlock()
		return Response{}, fmt.Errorf("curate: already in progress")
	}
	s.curating = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.curating = false
		s.mu.Unlock()
	}()
	notes, err := allEpochNotes(s.projectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("curate: %w", err)
	}
	if len(notes) == 0 {
		return Response{}, fmt.Errorf("curate: no epoch notes to curate — distill first")
	}

	// fail journals memory_update{layer:curator, cause:failed} before
	// returning the error. The detail is explicit: run/parse failures pass
	// the error text; write failures pass "write error: …" (any write
	// failure after the stale-clear must land in the journal — the same
	// asymmetry the parse path never had) and the empty-topics refusal
	// passes "empty topics".
	fail := func(err error, detail string) (Response, error) {
		_, _ = s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "curator",
			"cause":  "failed",
			"detail": detail,
		}))
		return Response{}, err
	}

	ad := s.distillAdapter
	if ad == nil {
		ad = s.adapters[""] // same fallback as runDistillAgent
	}
	raw, err := runOneShot(ctx, ad, curatorPrompt(notes), curatorTimeout)
	if err != nil {
		werr := fmt.Errorf("curate: curator run: %w", err)
		return fail(werr, werr.Error())
	}
	res, err := parseCuratorOutput(raw)
	if err != nil {
		return fail(err, err.Error())
	}

	// Refuse BEFORE the stale-clear when the curator gave us nothing
	// writable: an empty (or entirely invalid) topic list must not erase
	// the existing topic pages.
	topics := validTopics(s.projectRoot, res)
	if len(topics) == 0 {
		return fail(fmt.Errorf("curate: curator returned 0 topics — nothing to write"), "empty topics")
	}

	// before_sha covers the pre-curate index ("" when absent) — the M4
	// before/after convention for injected layers.
	oldIndex := readFileFull(filepath.Join(s.projectRoot, "wiki", "index.md"))
	if _, err := writeTopicPages(s.projectRoot, topics); err != nil {
		werr := fmt.Errorf("curate: %w", err)
		return fail(werr, "write error: "+err.Error())
	}
	indexContent, err := writeIndex(s.projectRoot, topics)
	if err != nil {
		werr := fmt.Errorf("curate: %w", err)
		return fail(werr, "write error: "+err.Error())
	}

	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":     "curate",
		"topics":     len(topics),
		"notes_read": len(notes),
	})); err != nil {
		return Response{}, err
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":      "index",
		"cause":      "curate",
		"before_sha": sha16([]byte(oldIndex)),
		"after_sha":  sha16([]byte(indexContent)),
		"detail":     fmt.Sprintf("rewrote %d topics + index", len(topics)),
	})); err != nil {
		return Response{}, err
	}
	// MemoryProposals stays 0: the field names PENDING learner proposals and
	// a curate proposes none; the sidebar topic count comes from list_topics.
	return Response{WikiPath: "wiki/index.md"}, nil
}

// handleListTopics lists the curator's topic pages under wiki/topics/ for
// the browser's Topics tab (Epoch=0 — topics are not per-epoch notes; Name
// carries the page title parsed from the first `# ` line, falling back to
// the slug). Read-only: no journal writes.
func (s *Server) handleListTopics(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("list_topics: %w", err)
	}
	matches, err := filepath.Glob(filepath.Join(s.projectRoot, "wiki", "topics", "*.md"))
	if err != nil {
		return Response{}, fmt.Errorf("list_topics: %w", err)
	}
	var topics []WikiNoteInfo
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue // vanished between glob and stat
		}
		topics = append(topics, WikiNoteInfo{
			Path:       m,
			Name:       topicTitle(m, strings.TrimSuffix(filepath.Base(m), ".md")),
			Epoch:      0,
			ModifiedAt: fi.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(topics, func(i, j int) bool { return topics[i].Name < topics[j].Name })
	return Response{WikiNotes: topics}, nil
}

// topicTitle reads the first "# "-prefixed line of a topic page, falling
// back to the slug when the page has no title line.
func topicTitle(path, slug string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return slug
	}
	for line := range strings.Lines(string(b)) {
		if title, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(title)
		}
	}
	return slug
}
