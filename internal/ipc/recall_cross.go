package ipc

// M12 Batch 3a (D-cross): matched-only cross-workstream context push.
// Content is injected only when EARNED by a keyword match — there is
// deliberately no newest-first fallback: an unmatched cross-workstream
// layer is a junk drawer of alien narratives (the failure ADR-0003
// §Context attributes to cross-project memory dumps, one level down).
// Full sibling history stays pull-only (`odo wiki read`,
// `odo journal search`); push only ever buys the matched top slice.
//
// Two bounded sources, combined into one optional layer:
//   - topic pages (wiki/topics/*.md): project-scoped, cross-workstream by
//     construction — top 2 matched pages under a 3KB page-boundary cap.
//   - sibling epoch notes (wiki/*-epoch-*.md from OTHER workstreams): the
//     single newest matched note under a 2KB line-boundary cap. A note
//     retracted in its own workstream's active conversation is never
//     pushed (the siblingRetractionGate below): retraction is gated at
//     the candidate's OWN workstream — the current conversation's
//     retraction set only covers home-workstream notes and cannot see
//     alien ones.
//
// Injection: the send path (after "## Prior notes (recalled)", before the
// memory map) and the /panel slash block (it advises on the project as a
// whole); /vision keeps its lean contract and stays excluded. Every source
// lands in the injection receipt (real path → sha16 of its rendered
// section chunk) and in the journaled recall payload with origin +
// matched_terms (optional fields, ADR-0002 preserved).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yingliang-zhang/odo/internal/adapter"
	"github.com/yingliang-zhang/odo/internal/store"
)

// Budgets for the cross-workstream layer (mirrored in budgets.go — the
// bounded Σ test enforces the registry).
const (
	// crossTopicsCap bounds the injected topic-page slice across the top
	// crossTopicsMax matched pages (page-boundary cut, headers counted).
	crossTopicsCap = 3 * 1024
	// crossTopicsMax bounds how many matched topic pages one push buys.
	crossTopicsMax = 2
	// crossSiblingCap bounds the newest matched sibling note's section
	// (header + line-boundary-cut content).
	crossSiblingCap = 2 * 1024
)

// cross_ws_recall prefs values (fail-to-default on missing/unknown).
const (
	crossWsOff     = "off"
	crossWsTopics  = "topics"
	crossWsSibling = "sibling"
	crossWsBoth    = "both"
)

// resolveCrossWsRecall reads the cross_ws_recall prefs key: which sources
// the cross-workstream layer may draw from. Missing or unknown values
// resolve to "both" (fail to default, not to silence — the
// resolvePanelContextScope pattern). Read per use so a prefs edit takes
// effect on the next prompt.
func resolveCrossWsRecall() string {
	switch v := adapter.LoadPrefsRaw("cross_ws_recall"); v {
	case crossWsOff, crossWsTopics, crossWsSibling, crossWsBoth:
		return v
	}
	return crossWsBoth
}

// crossSource is one injected cross-workstream section for journaling:
// its real wiki path, the provenance origin, the query terms that earned
// the injection, and the sha16 of exactly the rendered section chunk
// (ADR-0003 inv 5 — the receipt hashes what the prompt carried).
type crossSource struct {
	path         string
	origin       string // "topic" | "sibling"
	matchedTerms []string
	sha          string
}

// recallTopicPages selects matched topic pages (wiki/topics/*.md, index.md
// skipped) and renders each under a labeled header: the page name, the
// matched query terms, and the workstream-qualified citations parsed from
// the page ("sources:" tells the agent WHOSE knowledge this is). Ranking
// is match-count DESC, name ASC (deterministic under ties); the top
// crossTopicsMax pages share crossTopicsCap with a page-boundary cut — no
// page is half-included. Held-back matched pages are named by a count
// marker per cause (M6.1 visibility): past the top-crossTopicsMax limit,
// or cut by the cap. Zero matched pages ⇒ "" — matched-only by principle.
func recallTopicPages(projectRoot, query string) (body string, items []recallItem, chunks [][]byte) {
	matches, err := filepath.Glob(filepath.Join(projectRoot, "wiki", "topics", "*.md"))
	if err != nil {
		return "", nil, nil
	}
	// (2026-08-24 tri-review P0) a planted symlink inside the committable
	// topics/ tree must never inject bytes from outside it: the guard
	// treats an escaping link exactly like a vanished page.
	topicsDir := filepath.Join(projectRoot, "wiki", "topics")
	type page struct {
		path    string
		name    string // topics/<basename>
		content string
		matched []string
	}
	terms := tokenizeQuery(query)
	pages := make([]page, 0, len(matches))
	for _, m := range matches {
		base := filepath.Base(m)
		if base == "index.md" {
			continue // wiki/index.md rides its own always-injected layer
		}
		content, err := readWithinDir(projectRoot, topicsDir, m)
		if err != nil {
			continue // page vanished between glob and read — or a planted symlink escaping wiki/topics (2026-08-24 tri-review P0): skip it
		}
		matched := noteMatches(string(content), base, terms)
		if len(matched) == 0 {
			continue
		}
		pages = append(pages, page{path: m, name: "topics/" + base, content: string(content), matched: matched})
	}
	if len(pages) == 0 {
		return "", nil, nil
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if len(pages[i].matched) != len(pages[j].matched) {
			return len(pages[i].matched) > len(pages[j].matched)
		}
		return pages[i].name < pages[j].name
	})
	// Two distinct hold-back classes, each named by its own marker: pages
	// past the top-crossTopicsMax limit never enter the budget at all,
	// while pages inside it are cut on a page boundary when the cap runs
	// out. Conflating them misreports WHY a page was held back.
	heldByLimit := 0
	if len(pages) > crossTopicsMax {
		heldByLimit = len(pages) - crossTopicsMax
		pages = pages[:crossTopicsMax]
	}

	const sep = "\n\n---\n\n"
	var b strings.Builder
	heldByCap := 0
	for i, pg := range pages {
		header := fmt.Sprintf("### %s [matched: %s]", pg.name, strings.Join(pg.matched, ", "))
		if sources := topicCitations(pg.content); len(sources) > 0 {
			header += " · sources: " + strings.Join(sources, ", ")
		}
		chunk := header + "\n\n" + pg.content
		add := len(chunk)
		if b.Len() > 0 {
			add += len(sep)
		}
		if b.Len()+add > crossTopicsCap {
			heldByCap = len(pages) - i // cut on a page boundary: no page is half-included
			break
		}
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(chunk)
		items = append(items, recallItem{path: pg.path, matchedTerms: pg.matched})
		chunks = append(chunks, []byte(chunk))
	}
	if heldByLimit > 0 {
		fmt.Fprintf(&b, "\n\n_%d more matched topic page(s) held back by the top-%d limit — pull them from `%s`._\n",
			heldByLimit, crossTopicsMax, filepath.Join(projectRoot, "wiki", "topics"))
	}
	if heldByCap > 0 {
		fmt.Fprintf(&b, "\n\n_%d more matched topic page(s) held back by the %dKB cross-workstream cap — pull them from `%s`._\n",
			heldByCap, crossTopicsCap/1024, filepath.Join(projectRoot, "wiki", "topics"))
	}
	if len(items) == 0 {
		return "", nil, nil
	}
	return b.String(), items, chunks
}

// topicCitations extracts the workstream-qualified "(<ws>-epoch-N)"
// citations from a topic page (first-seen order, de-duplicated) — the
// provenance labels Batch 2's curator emits. Legacy bare "(epoch-N)"
// citations are not ws-qualified and are left out of the sources list.
func topicCitations(content string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range wsEpochCiteRe.FindAllStringSubmatch(content, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// siblingRetractionGate answers whether a sibling candidate note is
// retracted in its OWN workstream's active conversation. The current
// conversation's retraction set cannot see alien notes — retraction rows
// are journaled conversation-scoped against home-workstream notes — so
// gating per candidate is the only correct reading of the retraction
// record. Sets are cached
// per workstream per call: one send costs at most one events scan per
// distinct sibling workstream. Every store-level failure (unregistered
// project, deleted workstream, no active conversation) fails OPEN — a
// missing retraction record must never break recall. A nil store means
// no journal is in play and disables the gate.
type siblingRetractionGate struct {
	ctx         context.Context
	st          *store.Store
	projectRoot string
	resolved    bool             // wsByID resolution attempted (fail-open outcome cached too)
	wsByID      map[string]int64 // ws name → id for the bound project
	sets        map[string]map[string]bool
}

// retracted reports whether <wsName>-epoch note name sits in that
// workstream's active-conversation retraction set, loading and caching the
// set on first use per workstream.
func (g *siblingRetractionGate) retracted(wsName, name string) bool {
	if g.st == nil {
		return false
	}
	if g.sets == nil {
		g.sets = map[string]map[string]bool{}
	}
	set, cached := g.sets[wsName]
	if !cached {
		if wsID, ok := g.workstreamID(wsName); ok {
			if conv, err := g.st.GetActiveConversation(g.ctx, wsID); err == nil {
				set = retractedNoteSet(g.ctx, g.st, conv.ID)
			}
		}
		g.sets[wsName] = set
	}
	return set[name]
}

// workstreamID resolves the project's workstream name → id map once;
// resolution failures fail open (unknown id) and are cached as such.
func (g *siblingRetractionGate) workstreamID(wsName string) (int64, bool) {
	if !g.resolved {
		g.resolved = true
		if p, err := g.st.GetProjectByRoot(g.ctx, g.projectRoot); err == nil {
			if streams, err := g.st.ListWorkstreams(g.ctx, p.ID); err == nil {
				g.wsByID = map[string]int64{}
				for _, w := range streams {
					g.wsByID[w.Name] = w.ID
				}
			}
		}
	}
	id, ok := g.wsByID[wsName]
	return id, ok
}

// recallSiblingNote selects the single newest sibling epoch note the query
// earned: wiki/*-epoch-*.md MINUS the current workstream's own notes
// (already recalled by the home-workstream layer), only notes with ≥1
// matched term, newest by epoch number (mtime tie-break, name as final
// deterministic order). A candidate retracted in its own workstream's
// active conversation is skipped — the newest-first fallback then lands on
// the next-newest un-retracted candidate. The section is the labeled
// header plus the note content line-cut so header+content stay under
// crossSiblingCap; a header that alone exceeds the cap yields nothing (a
// negative content budget would slice-panic). No matched sibling ⇒
// ok=false — another workstream's narrative is pushed only when the query
// earned it.
func recallSiblingNote(ctx context.Context, st *store.Store, projectRoot, currentWsName, query string) (chunk string, item recallItem, ok bool) {
	matches, err := filepath.Glob(filepath.Join(projectRoot, "wiki", "*-epoch-*.md"))
	if err != nil {
		return "", recallItem{}, false
	}
	// (2026-08-24 tri-review P0) the glob root is the committable wiki/
	// tree — a planted symlink escaping it is skipped like a vanished note
	// so external bytes never ride the sibling push into a prompt.
	wikiDir := filepath.Join(projectRoot, "wiki")
	terms := tokenizeQuery(query)
	ownPrefix := currentWsName + "-epoch-"
	gate := siblingRetractionGate{ctx: ctx, st: st, projectRoot: projectRoot}
	type cand struct {
		path    string
		name    string // <ws>-epoch-<N> (basename without .md)
		ws      string
		epoch   int
		mtimeNs int64
		matched []string
		content string
	}
	var best *cand
	for _, m := range matches {
		base := filepath.Base(m)
		if strings.HasPrefix(base, ownPrefix) {
			continue // own workstream: the home recall layer already covers it
		}
		epoch, okEpoch := wikiNoteEpoch(m)
		if !okEpoch {
			continue
		}
		content, err := readWithinDir(projectRoot, wikiDir, m)
		if err != nil {
			continue // note vanished between glob and read — or a planted symlink escaping wiki/ (2026-08-24 tri-review P0): skip it
		}
		if strings.HasPrefix(string(content), supersededBanner) {
			continue // merged into topics: the topic-layer push already covers its facts
		}
		matched := noteMatches(string(content), base, terms)
		if len(matched) == 0 {
			continue
		}
		name := strings.TrimSuffix(base, ".md")
		var mtime int64
		if finfo, err := os.Stat(m); err == nil {
			mtime = finfo.ModTime().UnixNano()
		}
		c := cand{
			path:    m,
			name:    name,
			ws:      strings.TrimSuffix(name, fmt.Sprintf("-epoch-%d", epoch)),
			epoch:   epoch,
			mtimeNs: mtime,
			matched: matched,
			content: string(content),
		}
		if gate.retracted(c.ws, c.name) {
			continue // retracted in its OWN workstream: never push what its home conversation disowned
		}
		switch {
		case best == nil,
			c.epoch > best.epoch,
			c.epoch == best.epoch && c.mtimeNs > best.mtimeNs,
			c.epoch == best.epoch && c.mtimeNs == best.mtimeNs && c.name < best.name:
			best = &c
		}
	}
	if best == nil {
		return "", recallItem{}, false
	}
	header := fmt.Sprintf("### %s.md [from workstream %q] [matched: %s]",
		best.name, best.ws, strings.Join(best.matched, ", "))
	content := best.content
	budget := crossSiblingCap - len(header) - len("\n\n")
	if budget < 0 {
		return "", recallItem{}, false // header alone exceeds the cap — never let the cut slice negative
	}
	if len(content) > budget {
		content = capAtLineBoundary(content, budget)
		if content == "" {
			return "", recallItem{}, false // no complete line fits: say nothing rather than a naked header
		}
	}
	chunk = header + "\n\n" + content
	return chunk, recallItem{path: best.path, matchedTerms: best.matched}, true
}

// crossWsBlock combines the topic-page and sibling-note slices into one
// optional layer (gated by the cross_ws_recall pref) and returns the
// rendered block plus one receipt/journal source per injected section
// (real path → sha16 of its rendered chunk). "" and no sources when every
// enabled source came back unmatched — the block declares itself with its
// own "##" header, so callers render-or-skip on the empty string. The
// header names the contents honestly: "project topic pages" only when a
// topic slice is actually present; a sibling-only block gets the neutral
// "other workstreams" header. The store threads through to the sibling
// retraction gate (nil store: no journal in play, gate disabled).
func crossWsBlock(ctx context.Context, st *store.Store, projectRoot, currentWsName, query string) (string, []crossSource) {
	mode := resolveCrossWsRecall()
	const sep = "\n\n---\n\n"
	var sections []string
	var sources []crossSource
	header := "## Cross-workstream context (other workstreams)"
	if mode == crossWsTopics || mode == crossWsBoth {
		if body, items, chunks := recallTopicPages(projectRoot, query); len(items) > 0 {
			for i, it := range items {
				sources = append(sources, crossSource{path: it.path, origin: "topic", matchedTerms: it.matchedTerms, sha: sha16(chunks[i])})
			}
			header = "## Cross-workstream context (project topic pages — other workstreams)"
			sections = append(sections, body)
		}
	}
	if mode == crossWsSibling || mode == crossWsBoth {
		if chunk, item, ok := recallSiblingNote(ctx, st, projectRoot, currentWsName, query); ok {
			sources = append(sources, crossSource{path: item.path, origin: "sibling", matchedTerms: item.matchedTerms, sha: sha16([]byte(chunk))})
			sections = append(sections, chunk)
		}
	}
	if len(sections) == 0 {
		return "", nil
	}
	return header + "\n\n" + strings.Join(sections, sep), sources
}
