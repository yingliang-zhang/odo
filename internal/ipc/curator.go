package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
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

// oldestUnretractedNoteMtime returns the oldest mtime across
// wiki/*-epoch-*.md files NOT in retracted — the M17 F4 age source for a
// never-curated project's auto_age leg. Epoch notes are the curation
// input (the source of truth; topic pages are derived artifacts that the
// curator regenerates), so NOTE age — not page age — measures curation
// staleness. Retracted notes are dead knowledge: they must not drag the
// clock. ok=false when no unretracted note exists.
func oldestUnretractedNoteMtime(projectRoot string, retracted map[string]bool) (time.Time, bool) {
	matches, err := filepath.Glob(filepath.Join(projectRoot, "wiki", "*-epoch-*.md"))
	if err != nil {
		return time.Time{}, false
	}
	var oldest time.Time
	found := false
	for _, m := range matches {
		if _, ok := wikiNoteEpoch(m); !ok {
			continue // defensive: same name rule as allEpochNotes
		}
		name := strings.TrimSuffix(filepath.Base(m), ".md")
		if retracted[name] {
			continue
		}
		fi, err := os.Stat(m)
		if err != nil {
			continue // vanished between glob and stat
		}
		if !found || fi.ModTime().Before(oldest) {
			oldest = fi.ModTime()
			found = true
		}
	}
	return oldest, found
}

// curatorPrompt renders the curator one-shot prompt: the instruction plus
// every epoch note under a `--- <name> (workstream: <ws>, epoch: <N>) ---`
// header, newest-first. M12: citations are workstream-qualified
// "(main-epoch-3)" — the bare "(epoch-N)" form collides across workstreams
// (epoch numbering restarts per workstream), and the daemon validates +
// repairs citations before any page is rewritten.
func curatorPrompt(notes []epochNote) string {
	var b strings.Builder
	b.WriteString(`You are running odo's memory curator pass. Synthesize the epoch notes below into topic pages — one per topic area (e.g., "authentication", "build-system", "testing").

Output JSON ONLY (no prose, no markdown fence), exactly this shape:
{"topics":[{"title":"<Topic Title>","slug":"<topic-slug>","bullets":["- <statement> (main-epoch-3)","- <statement> (ui-epoch-2)"]}],"superseded":["<note-name>"]}

Rules:
- Each topic groups related decisions across workstreams and epochs.
- Every bullet MUST end with a workstream-qualified citation naming its source note: "(<workstream>-epoch-N)", e.g. "(main-epoch-3)". The note headers below give each note's exact name — copy it verbatim.
- A bullet without a citation is allowed but will be flagged in the UI as "uncited."
- The slug is lowercase, hyphenated, no spaces (e.g., "authentication").
- Do NOT copy previous topic pages — write from the source notes only (generation-1).
- 3-10 topics is typical; fewer for small projects.
- "superseded" (optional): note names whose durable content is now FULLY represented in the topic bullets you wrote. List a note ONLY when at least one bullet cites it — the daemon verifies this and stamps those notes out of active recall (they stay on disk for citation liveness and pull-based reads). Prefer consolidating many small old notes over leaving them half-merged. Omit the field entirely when nothing qualifies.

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
	// Superseded names epoch notes the curator asserts are fully merged
	// into this pass's topics. The daemon re-verifies every claim against
	// the WRITTEN bullets before stamping (a note named but never cited
	// drops out silently — asserted merge coverage is falsifiable).
	Superseded []string `json:"superseded"`
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

// Citation shapes accepted from the curator (M12). Qualified citations are
// the only emitted form; the bare epoch form is repaired when it resolves
// unambiguously and fails the gate otherwise — unqualified "(epoch-N)"
// collides across workstreams (epoch numbering restarts per workstream),
// so guessing would attach a claim to the wrong note.
var (
	// wsEpochCiteRe matches "(<ws>-epoch-N)" including workstreams that
	// themselves contain dashes (greedy up to the final -epoch-N segment).
	wsEpochCiteRe = regexp.MustCompile(`\(([A-Za-z0-9][A-Za-z0-9._-]*-epoch-\d+)\)`)
	// bareEpochCiteRe matches the legacy unqualified "(epoch-N)" form.
	bareEpochCiteRe = regexp.MustCompile(`\(epoch-(\d+)\)`)
)

// deadCitation is one citation that failed the liveness pre-check.
type deadCitation struct {
	slug  string // topic the bullet belongs to
	token string // the citation text as emitted, e.g. "(ui-epoch-9)" or "(epoch-3)"
}

// citationEpochRe extracts the epoch number of a citation's note name
// ("ui-epoch-9" → 9) or the bare legacy form ("epoch-9" → 9) for the
// ghost check — a ghost is form-agnostic (the confabulated epoch number
// never existed either way).
var citationEpochRe = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9._-]*-)?epoch-(\d+)$`)

// isGhostCitation reports whether a citation — already known to name no
// on-disk note — is a GHOST: the epoch number never existed anywhere (no
// note file of that epoch in ANY workstream, and markerForEpoch says no
// distill marker for it in the journal). Ghosts are repairable by
// stripping: the claim cited nothing that ever was. A real dangling
// reference (the epoch number DID exist — another workstream's note file,
// or a marker whose note file vanished) stays a gate failure:
// confabulated repair would attach the claim to the wrong source.
// markerForEpoch == nil means the journal was not consulted — nothing is
// provably a ghost, so the conservative old behavior (abort) stands.
func isGhostCitation(projectRoot string, markerForEpoch func(epoch int) bool, token string) bool {
	m := citationEpochRe.FindStringSubmatch(strings.TrimSuffix(strings.TrimPrefix(token, "("), ")"))
	if m == nil || markerForEpoch == nil {
		return false
	}
	if matches, _ := filepath.Glob(filepath.Join(projectRoot, "wiki", "*-epoch-"+m[1]+".md")); len(matches) > 0 {
		return false // the epoch number exists on disk elsewhere: real dangling
	}
	epoch, err := strconv.Atoi(m[1])
	if err != nil {
		return false
	}
	return !markerForEpoch(epoch)
}

// checkTopicCitations enforces citation liveness BEFORE any topic page is
// rewritten: qualified citations must name an on-disk wiki note; bare
// "(epoch-N)" citations are repaired in place when exactly one note of
// that epoch exists project-wide and fail the gate when none or several
// do (a collision can never resolve silently to the wrong workstream).
// M17 F4: a line whose only dead citations are ghosts (epochs that never
// existed) is STRIPPED instead of aborting the curate — the stripped
// tokens come back separately for journaling. Returns the repaired topics
// (ghost-stripped, possibly with fewer bullets), every real dead
// citation, and every ghost token stripped.
func checkTopicCitations(projectRoot string, topics []topic, markerForEpoch func(epoch int) bool) (repaired []topic, dead, stripped []deadCitation) {
	repaired = make([]topic, 0, len(topics))
	for _, t := range topics {
		t.Bullets = repairBullets(projectRoot, t.Slug, t.Bullets, markerForEpoch, &dead, &stripped)
		if len(t.Bullets) > 0 {
			// A topic whose every bullet cited ghosts is pure
			// confabulation — drop the page, never write an empty shell.
			repaired = append(repaired, t)
		}
	}
	return repaired, dead, stripped
}

// repairBullets rewrites one topic's bullet list, qualifying bare
// citations, stripping ghost-cited lines, and collecting real dead
// citations plus the stripped ghost tokens (for the curate marker).
// A line mixes outcomes conservatively: any real dead citation on it
// keeps the line (the gate aborts the whole curate on it anyway);
// ghost-only lines are dropped whole.
func repairBullets(projectRoot, slug string, bullets []string, markerForEpoch func(epoch int) bool, dead, stripped *[]deadCitation) []string {
	var out []string
	for _, bullet := range bullets {
		line, strip := repairCitations(projectRoot, slug, bullet, markerForEpoch, dead, stripped)
		if strip {
			continue // ghost-cited line: dropped through the repair machinery
		}
		out = append(out, line)
	}
	return out
}

// repairCitations rewrites the citations of ONE bullet and classifies its
// fate: strip=false returns the (possibly repaired) bullet; strip=true
// means every problem on the line is a ghost and the caller drops the
// line, with its ghost tokens recorded in stripped. Real dead citations
// are appended to dead regardless of the fate — stripping never rescues
// a real dangling reference.
func repairCitations(projectRoot, slug, bullet string, markerForEpoch func(epoch int) bool, dead, stripped *[]deadCitation) (line string, strip bool) {
	var lineDead, lineGhosts []deadCitation
	var lineLive int
	out := wsEpochCiteRe.ReplaceAllStringFunc(bullet, func(tok string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(tok, "("), ")")
		if noteExists(projectRoot, name) {
			lineLive++
			return tok
		}
		if isGhostCitation(projectRoot, markerForEpoch, tok) {
			lineGhosts = append(lineGhosts, deadCitation{slug: slug, token: tok})
			return tok
		}
		lineDead = append(lineDead, deadCitation{slug: slug, token: tok})
		return tok
	})
	out = bareEpochCiteRe.ReplaceAllStringFunc(out, func(tok string) string {
		m := bareEpochCiteRe.FindStringSubmatch(tok)
		matches, _ := filepath.Glob(filepath.Join(projectRoot, "wiki", "*-epoch-"+m[1]+".md"))
		switch len(matches) {
		case 0:
			if isGhostCitation(projectRoot, markerForEpoch, tok) {
				lineGhosts = append(lineGhosts, deadCitation{slug: slug, token: tok})
			} else {
				lineDead = append(lineDead, deadCitation{slug: slug, token: tok})
			}
		case 1:
			lineLive++
			return "(" + strings.TrimSuffix(filepath.Base(matches[0]), ".md") + ")" // repair: qualify
		default:
			lineDead = append(lineDead, deadCitation{slug: slug, token: tok})
		}
		return tok
	})
	*dead = append(*dead, lineDead...)
	if len(lineGhosts) == 0 || len(lineDead) > 0 {
		return out, false
	}
	// Every problem is a ghost (an epoch that never existed).
	*stripped = append(*stripped, lineGhosts...)
	if lineLive == 0 {
		// No live citation anchors the line: its prose is unprovenanced
		// confabulation by construction — strip the whole line.
		return "", true
	}
	// A live citation anchors the line's fact: the content stays, only the
	// phantom tokens are scrubbed in-line — whole-line stripping destroyed
	// live facts that merely shared a line with a ghost (P0 review DSF).
	cleaned := bullet
	for _, g := range lineGhosts {
		cleaned = strings.Replace(cleaned, g.token, "", 1)
	}
	return strings.TrimSpace(cleaned), false
}

// noteExists reports whether wiki/<name>.md is a readable note file —
// the citation-liveness pre-check's source of truth (on disk, not prompt).
func noteExists(projectRoot, name string) bool {
	fi, err := os.Stat(filepath.Join(projectRoot, "wiki", name+".md"))
	return err == nil && !fi.IsDir()
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

// handleCurate runs the manual curator pass (trigger "manual"): read the
// full epoch-note set, one orchestrator one-shot, rewrite topic pages +
// index.md from scratch. Unlike the learner (which degrades silently
// inside distill), curate is a standalone command: a failure journals
// memory_update{layer:curator, cause:failed} AND returns the error to the
// caller (spec §1). M12: the trigger, citation liveness, and marker
// provenance live in curateCore, shared with the daemon's conditional
// auto-curate.
func (s *Server) handleCurate(ctx context.Context, req Request) (Response, error) {
	p, err := s.resolveProject(ctx, req.ProjectRoot)
	if err != nil {
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
	notesSince := 0
	if n, _, err := s.store.AutoCurateState(ctx, p.ID); err == nil {
		notesSince = n
	}
	if err := s.curateCore(ctx, p.ID, c.ID, distillTriggerManual, notesSince); err != nil {
		return Response{}, err
	}
	// MemoryProposals stays 0: the field names PENDING learner proposals and
	// a curate proposes none; the sidebar topic count comes from list_topics.
	return Response{WikiPath: "wiki/index.md"}, nil
}

// curateCore is the manual + M12 auto-curate shared pipeline. trigger is
// "manual" | "auto_notes" | "auto_age" (journaled); notesSince is the
// caller's count of distill markers since the latest passing curate (the
// auto trigger's own evidence). Failures journal
// memory_update{layer:curator, cause:failed|gate_failed} and return the
// error — the manual caller relays it to the GUI, the auto caller logs it.
//
// M17 F4: retracted notes never feed the curator (they are out of recall
// already; a curate built on them would revive retracted claims — the
// retraction set is conversation-scoped, so the ACTIVE conversation's set
// filters the project-wide enumeration).
func (s *Server) curateCore(ctx context.Context, projectID, convID int64, trigger string, notesSince int) error {
	notes, err := allEpochNotes(s.projectRoot)
	if err != nil {
		return fmt.Errorf("curate: %w", err)
	}
	if len(notes) == 0 {
		return fmt.Errorf("curate: no epoch notes to curate — distill first")
	}
	retracted := s.retractedNotes(ctx, convID)
	unretracted := make([]epochNote, 0, len(notes))
	for _, n := range notes {
		if !retracted[n.name] {
			unretracted = append(unretracted, n)
		}
	}
	notes = unretracted
	if len(notes) == 0 {
		return fmt.Errorf("curate: every epoch note stands retracted — unretract a false positive or distill fresh notes first")
	}
	// notesRead is the marker's input provenance: every note the curator
	// was shown, with its content hash (the pass is falsifiable against
	// the notes on disk).
	notesRead := make([]map[string]string, 0, len(notes))
	for _, n := range notes {
		notesRead = append(notesRead, map[string]string{"name": n.name, "sha16": sha16([]byte(n.content))})
	}

	// fail journals memory_update{layer:curator, cause:failed} before
	// returning the error. The detail is explicit: run/parse failures pass
	// the error text; write failures pass "write error: …" (any write
	// failure after the stale-clear must land in the journal — the same
	// asymmetry the parse path never had) and the empty-topics refusal
	// passes "empty topics".
	fail := func(err error, detail string) error {
		_, _ = s.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "curator",
			"cause":  "failed",
			"detail": detail,
		}))
		return err
	}

	// R-W3: the prefs `curator_via:` switch picks the completion route
	// (absent/"omp" → the historical OMP one-shot; "moa" → one direct
	// moa.Query, its receipts landing on the pass marker below). Truncation
	// fails closed inside runMoaOneShot — before citation gating, before
	// ANY page rewrite; the daemon remains the sole writer of every memory
	// layer (ADR-0003 inv 7 amendment): curator JSON is parsed text, gates
	// act, writes are daemon calls.
	prompt := curatorPrompt(notes)
	raw := ""
	var rec *moaReceipt
	if resolveVia("curator", "curator_via") == viaMoa {
		raw, rec, err = runMoaOneShot(ctx, "curator", prompt)
	} else {
		ad := s.distillAdapter
		if ad == nil {
			ad = s.adapterFor("") // same fallback as runDistillAgent
		}
		raw, err = runOneShot(ctx, ad, prompt, curatorTimeout)
	}
	if err != nil {
		return fail(fmt.Errorf("curate: curator run: %w", err), err.Error())
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

	// M12 citation-liveness gate: every parsed citation must resolve to an
	// on-disk note BEFORE any topic page is rewritten (bare "(epoch-N)"
	// forms are repaired when unambiguous). A REAL dead citation (the epoch
	// existed — mis-scope or vanished file) skips the WHOLE curate: the old
	// generation survives on disk, the gate_failed marker + memory_update
	// journal what refused to land. M17 F4: a bullet whose only dead
	// citations are GHOSTS (an epoch number that never existed — no note
	// file of it in any workstream, no distill marker in the journal) is
	// stripped through the same repair machinery instead of aborting the
	// curate; stripped tokens land on the pass marker for audit.
	markerForEpoch := func(epoch int) bool {
		ok, err := s.store.DistillMarkerExistsForEpoch(ctx, projectID, epoch)
		if err != nil {
			// Unknown journal state must never strip: treat the epoch as
			// real (the abort branch — fail toward human attention).
			log.Printf("curate: epoch marker check: %v", err)
			return true
		}
		return ok
	}
	topics, dead, stripped := checkTopicCitations(s.projectRoot, topics, markerForEpoch)
	if len(topics) == 0 {
		return fail(fmt.Errorf("curate: every topic cited only ghost epochs — nothing to write"), "empty topics")
	}
	if len(dead) > 0 {
		tokens := make([]string, 0, len(dead))
		for _, d := range dead {
			tokens = append(tokens, fmt.Sprintf("%s (topic %s)", d.token, d.slug))
		}
		// Audit trail on the abort path too: the ghosts that WOULD have
		// been stripped (and the surviving lines they were scrubbed from)
		// vanish from the record if only the pass marker reports them
		// (P0 review GLM). Nothing was written, but the next curate
		// reprocesses the same ghosts — the journal should show them now.
		ghosts := make([]string, 0, len(stripped))
		for _, g := range stripped {
			ghosts = append(ghosts, fmt.Sprintf("%s (topic %s)", g.token, g.slug))
		}
		if _, err := s.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(map[string]interface{}{
			"action":             "curate",
			"topics":             0,
			"notes_read":         notesRead,
			"trigger":            trigger,
			"notes_since_last":   notesSince,
			"gate":               "failed",
			"dead_citations":     tokens,
			"stripped_citations": ghosts,
		})); err != nil {
			return err
		}
		_, _ = s.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "curator",
			"cause":  "gate_failed",
			"detail": fmt.Sprintf("dead citation(s): %s", strings.Join(tokens, ", ")),
		}))
		return fmt.Errorf("curate: citation gate: dead citation(s): %s", strings.Join(tokens, ", "))
	}

	// before_sha covers the pre-curate index ("" when absent) — the M4
	// before/after convention for injected layers.
	oldIndex := readFileFull(filepath.Join(s.projectRoot, "wiki", "index.md"))
	if _, err := writeTopicPages(s.projectRoot, topics); err != nil {
		return fail(fmt.Errorf("curate: %w", err), "write error: "+err.Error())
	}
	indexContent, err := writeIndex(s.projectRoot, topics)
	if err != nil {
		return fail(fmt.Errorf("curate: %w", err), "write error: "+err.Error())
	}

	// Epoch→topic merge (P3): pages landed — now stamp the notes the
	// curator declared fully merged. Stamping runs AFTER the writes so a
	// failed pass never half-retires its sources, and BEFORE the marker so
	// the journal records exactly which notes left active recall.
	stamped := stampSupersededNotes(s.projectRoot, topics, res.Superseded, notesRead)

	marker := map[string]interface{}{
		"action":           "curate",
		"topics":           len(topics),
		"notes_read":       notesRead, // M12: input provenance [{name,sha16}]
		"trigger":          trigger,
		"notes_since_last": notesSince,
		"gate":             "pass",
	}
	if len(stamped) > 0 {
		marker["superseded"] = stamped
	}
	detail := fmt.Sprintf("rewrote %d topics + index", len(topics))
	if len(stripped) > 0 {
		tokens := make([]string, 0, len(stripped))
		for _, d := range stripped {
			tokens = append(tokens, fmt.Sprintf("%s (topic %s)", d.token, d.slug))
		}
		// M17 F4: ghost-cited lines were stripped instead of aborting —
		// the audit trail names exactly what was dropped.
		marker["stripped_citations"] = tokens
		detail += fmt.Sprintf(" (stripped %d ghost-cited line(s): %s)", len(stripped), strings.Join(tokens, ", "))
	}
	// R-W3: the moa route's wire-request receipt (absent on the OMP route,
	// whose attestation stays the exemption-ledger's). Bare keys — the
	// curate marker carries no distill receipt to collide with.
	if rec != nil {
		rec.journal(marker, "")
	}
	curateEv, err := s.store.AppendEvent(ctx, convID, store.EventReviewAction, mustJSON(marker))
	if err != nil {
		return err
	}
	// Ledger: curate section (best-effort, never blocks the pipeline).
	s.journalCurateLedger(ctx, convID, curateEv)
	if _, err := s.store.AppendEvent(ctx, convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":      "index",
		"cause":      "curate",
		"before_sha": sha16([]byte(oldIndex)),
		"after_sha":  sha16([]byte(indexContent)),
		"detail":     detail,
	})); err != nil {
		return err
	}
	return nil
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

// supersededBanner marks an epoch note as fully merged into topic pages.
// The banner is a plain first-line blockquote so the note stays a legal
// citation target (the M12 liveness gate resolves names on disk, never
// content) while recall stops INJECTING it — its facts now live in the
// topic layer. `odo wiki read <name>` still pulls the full text (drop
// lines, not bytes).
const supersededBanner = "> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.\n\n"

// stampSupersededNotes writes the superseded banner onto epoch notes the
// curator claimed as fully merged, returning the names ACTUALLY stamped.
// A claim is honored only when both hold (falsifiable merge assert):
//
//  1. the named note was READ in this pass (a supersede claim for a note
//     outside the input window is unverifiable), and
//  2. the note is cited at least once in the WRITTEN (post-repair) topic
//     bullets — if nothing made it into a page, the note is by definition
//     not merged, and stamping it would silently drop its facts from
//     every future prompt.
//
// Notes already carrying the banner are idempotently counted as stamped.
// Write failures drop the name (the note just keeps being injected).
func stampSupersededNotes(projectRoot string, topics []topic, claims []string, notesRead []map[string]string) []string {
	cited := map[string]bool{}
	for _, t := range topics {
		for _, b := range t.Bullets {
			for _, m := range wsEpochCiteRe.FindAllStringSubmatch(b, -1) {
				cited[m[1]] = true
			}
		}
	}
	read := map[string]bool{}
	for _, n := range notesRead {
		read[n["name"]] = true
	}
	var stamped []string
	seen := map[string]bool{}
	for _, name := range claims {
		if seen[name] || !read[name] || !cited[name] {
			continue
		}
		seen[name] = true
		path := filepath.Join(projectRoot, "wiki", name+".md")
		content, err := os.ReadFile(path)
		if err != nil {
			continue // vanished between read and stamp: keeps being injected
		}
		if strings.HasPrefix(string(content), supersededBanner) {
			stamped = append(stamped, name) // already stamped: reaffirm
			continue
		}
		if err := writeFileAtomic(path, supersededBanner+string(content), 0o644); err != nil {
			log.Printf("curate: stamp superseded %s: %v", name, err)
			continue
		}
		stamped = append(stamped, name)
	}
	return stamped
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
