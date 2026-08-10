package ipc

// M12 Batch 3a (D-cross) tests: matched-only cross-workstream recall.
// These pin the junk-drawer defense (zero match ⇒ NOTHING injected — no
// newest-first fallback), the top-2 topic ranking, both caps' boundary
// cuts, sibling workstream scoping, the labeled provenance headers, the
// cross_ws_recall pref matrix, and the journaled receipts (origin +
// matched_terms).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yingliang-zhang/odo/internal/store"
)

// writeTopicPage seeds one wiki topic page under root/wiki/topics.
func writeTopicPage(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, "wiki", "topics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCrossTopicRecallCJKENParity: the topic matcher is the same
// tokenizeQuery/noteMatches union as home-workstream recall — a pure-CJK
// query matches a CJK topic page via bigrams, an EN query matches an EN
// page the same way, and unmatched pages stay out both ways.
func TestCrossTopicRecallCJKENParity(t *testing.T) {
	root := t.TempDir()
	writeTopicPage(t, root, "epoch-folding", "# Folding\n\n折叠边界由 marker 显式记录。 (main-epoch-3)\n")
	writeTopicPage(t, root, "recall-tuning", "# Recall\n\nTokenizer bigrams matter for recall. (ui-epoch-2)\n")

	// CJK query: bigrams 折叠/边界 match only the CJK page.
	body, items, _ := recallTopicPages(root, "折叠边界为什么重要")
	if len(items) != 1 || !strings.HasSuffix(items[0].path, "epoch-folding.md") {
		t.Fatalf("CJK query items = %+v, want only epoch-folding.md", items)
	}
	// [折叠 叠边 边界]: the union tokenizer also emits 叠边 — the overlap
	// bigram spanning 折叠|边界 (same union path as home recall).
	if fmt.Sprint(items[0].matchedTerms) != "[折叠 叠边 边界]" {
		t.Errorf("CJK matched terms = %v, want [折叠 叠边 边界]", items[0].matchedTerms)
	}
	if !strings.Contains(body, "折叠边界由 marker") {
		t.Errorf("CJK topic body missing page content: %q", body)
	}

	// EN query: both terms match only the EN page.
	_, items, _ = recallTopicPages(root, "tokenizer bigrams")
	if len(items) != 1 || !strings.HasSuffix(items[0].path, "recall-tuning.md") {
		t.Fatalf("EN query items = %+v, want only recall-tuning.md", items)
	}
	if fmt.Sprint(items[0].matchedTerms) != "[tokenizer bigrams]" {
		t.Errorf("EN matched terms = %v, want [tokenizer bigrams]", items[0].matchedTerms)
	}
}

// TestCrossZeroMatchNoFallback pins the defining contract: with topic
// pages AND sibling notes on disk, a query matching NOTHING yields no
// block at all. There is no newest-first fallback tier — an unmatched
// cross-workstream push is a junk drawer of alien narratives.
func TestCrossZeroMatchNoFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no prefs ⇒ cross_ws_recall defaults to "both"
	root := t.TempDir()
	writeTopicPage(t, root, "epoch-folding", "# Folding\n\nEpoch folds explained. (main-epoch-3)\n")
	writeEpochNote(t, root, "ui-epoch-2", "# UI epoch 2\n\nSidebar rework notes.\n")

	// "zzzqqq wxyz uvw": no token is a substring of either fixture file
	// (mind: short tokens like "no" substring-match "notes" — the query
	// must be genuinely absent, not merely odd).
	const noMatchQuery = "zzzqqq wxyz uvw"
	if block, sources := crossWsBlock(context.Background(), nil, root, "main", noMatchQuery); block != "" {
		t.Errorf("zero-match block = %q (sources %v), want \"\" — matched-only, NO fallback", block, sources)
	}
	// The same pin at the component level.
	if body, items, _ := recallTopicPages(root, noMatchQuery); body != "" || items != nil {
		t.Errorf("zero-match topic recall = %q %+v, want empty", body, items)
	}
	if chunk, _, ok := recallSiblingNote(context.Background(), nil, root, "main", noMatchQuery); ok || chunk != "" {
		t.Errorf("zero-match sibling recall = %q ok=%v (newest sibling exists! fallback must NOT fire)", chunk, ok)
	}
}

// TestCrossTopicTop2RankingTie: top-2 selection under a match-count
// collision — the 2-term page wins, the rank-2 slot among three equal
// 1-term pages breaks deterministically by name ASC.
func TestCrossTopicTop2RankingTie(t *testing.T) {
	root := t.TempDir()
	writeTopicPage(t, root, "b-page", "gamma only here\n")
	writeTopicPage(t, root, "top", "alpha beta both here\n")
	writeTopicPage(t, root, "a-page", "gamma mentioned too\n")
	writeTopicPage(t, root, "c-page", "gamma again\n")

	_, items, _ := recallTopicPages(root, "alpha beta gamma")
	if len(items) != 2 {
		t.Fatalf("items = %d, want top-2", len(items))
	}
	if !strings.HasSuffix(items[0].path, "top.md") {
		t.Errorf("rank 1 = %s, want top.md (2 matched terms)", items[0].path)
	}
	if !strings.HasSuffix(items[1].path, "a-page.md") {
		t.Errorf("rank 2 = %s, want a-page.md (tie on 1 term ⇒ name ASC)", items[1].path)
	}
}

// TestCrossTopicPageBoundaryCap: the 3KB cap cuts on a PAGE boundary — a
// page that would overflow is left whole-out and named by the omission
// marker; an oversized first page yields nothing at all.
func TestCrossTopicPageBoundaryCap(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("alpha content line padding for size.\n", 60) // ~2.1KB
	writeTopicPage(t, root, "one", big)
	writeTopicPage(t, root, "two", big)

	body, items, chunks := recallTopicPages(root, "alpha")
	if len(items) != 1 {
		t.Fatalf("cap items = %d, want 1 (second page whole-out at the page boundary)", len(items))
	}
	if len(chunks) != 1 || len(chunks[0]) > crossTopicsCap {
		t.Errorf("chunk bytes = %v, want one page ≤ %d cap", len(chunks[0]), crossTopicsCap)
	}
	if !strings.Contains(body, "1 more matched topic page(s) held back by the 3KB cross-workstream cap") {
		t.Errorf("cap omission marker missing: %q", body[len(body)-200:])
	}

	// Oversize first page: nothing fits on a page boundary ⇒ empty.
	oversize := t.TempDir()
	writeTopicPage(t, oversize, "huge", strings.Repeat("alpha x", crossTopicsCap/4)) // >3KB after one page
	if body, items, _ := recallTopicPages(oversize, "alpha"); body != "" || len(items) != 0 {
		t.Errorf("oversize page recall = %d items, want 0 (never half-include a page)", len(items))
	}
}

// TestCrossSiblingExcludesCurrentWs: the current workstream's own notes
// are never "siblings" — they're the home recall layer's job. Among
// matched siblings the newest epoch wins; equal epochs break on mtime.
func TestCrossSiblingExcludesCurrentWs(t *testing.T) {
	root := t.TempDir()
	writeEpochNote(t, root, "main-epoch-5", "zeta in the current workstream\n")
	writeEpochNote(t, root, "ui-epoch-2", "zeta from the ui sibling\n")

	chunk, item, ok := recallSiblingNote(context.Background(), nil, root, "main", "zeta")
	if !ok {
		t.Fatal("sibling recall ok=false, want ui-epoch-2")
	}
	if !strings.HasSuffix(item.path, "ui-epoch-2.md") {
		t.Errorf("sibling = %s, want ui-epoch-2.md — main-epoch-5 must be EXCLUDED (same ws)", item.path)
	}
	if !strings.Contains(chunk, "zeta from the ui sibling") {
		t.Errorf("chunk missing sibling content: %q", chunk)
	}

	// Newest epoch wins.
	writeEpochNote(t, root, "ui-epoch-3", "newer zeta\n")
	_, item, _ = recallSiblingNote(context.Background(), nil, root, "main", "zeta")
	if !strings.HasSuffix(item.path, "ui-epoch-3.md") {
		t.Errorf("sibling = %s, want ui-epoch-3.md (newest epoch)", item.path)
	}

	// Equal epochs across workstreams: newest mtime wins.
	writeEpochNote(t, root, "exp-epoch-3", "zeta in exp\n")
	old := time.Now().Add(-time.Hour)
	recent := time.Now()
	for name, mt := range map[string]time.Time{"exp-epoch-3.md": old, "ui-epoch-3.md": recent} {
		if err := os.Chtimes(filepath.Join(root, "wiki", name), mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	_, item, _ = recallSiblingNote(context.Background(), nil, root, "main", "zeta")
	if !strings.HasSuffix(item.path, "ui-epoch-3.md") {
		t.Errorf("sibling = %s, want ui-epoch-3.md (epoch tie ⇒ mtime wins)", item.path)
	}
}

// TestCrossSiblingLineBoundaryCap: header+content stay under 2KB and the
// content cut lands on a whole line.
func TestCrossSiblingLineBoundaryCap(t *testing.T) {
	root := t.TempDir()
	line := "zeta-padding-line\n"
	writeEpochNote(t, root, "ui-epoch-2", strings.Repeat(line, 200))

	chunk, _, ok := recallSiblingNote(context.Background(), nil, root, "main", "zeta")
	if !ok {
		t.Fatal("sibling recall ok=false")
	}
	if len(chunk) > crossSiblingCap {
		t.Errorf("chunk = %d bytes, want ≤ %d", len(chunk), crossSiblingCap)
	}
	header := fmt.Sprintf("### ui-epoch-2.md [from workstream \"ui\"] [matched: zeta]")
	kept := strings.TrimPrefix(chunk, header+"\n\n")
	if !strings.HasPrefix(strings.Repeat(line, 200), kept+"\n") {
		t.Errorf("cut not on a line boundary: kept content does not extend to a whole line (kept %d bytes)", len(kept))
	}
}

// TestCrossLabeledHeaders: both section headers carry their provenance —
// the topic page's matched terms plus its ws-qualified citations parsed
// from the content (first-seen, de-duplicated), the sibling note's
// workstream name.
func TestCrossLabeledHeaders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	// Citations: ui-epoch-2 cited twice (dedupe), main-epoch-3 once; a bare
	// legacy "(epoch-9)" is not ws-qualified and stays out.
	writeTopicPage(t, root, "folding", "- alpha decision (ui-epoch-2)\n- alpha followup (main-epoch-3)\n- more alpha (ui-epoch-2)\n- legacy (epoch-9)\n")
	writeEpochNote(t, root, "ui-epoch-2", "alpha in ui\n")

	block, _ := crossWsBlock(context.Background(), nil, root, "main", "alpha")
	if !strings.HasPrefix(block, "## Cross-workstream context (project topic pages — other workstreams)\n\n") {
		t.Errorf("block missing the layer header: %q", block[:min(80, len(block))])
	}
	wantTopic := "### topics/folding.md [matched: alpha] · sources: ui-epoch-2, main-epoch-3"
	if !strings.Contains(block, wantTopic) {
		t.Errorf("topic header missing %q: %q", wantTopic, block)
	}
	wantSibling := "### ui-epoch-2.md [from workstream \"ui\"] [matched: alpha]"
	if !strings.Contains(block, wantSibling) {
		t.Errorf("sibling header missing %q: %q", wantSibling, block)
	}
}

// TestCrossCitationlessTopicHeader: a page without ws-qualified citations
// renders without a sources segment rather than a fabricated one.
func TestCrossCitationlessTopicHeader(t *testing.T) {
	root := t.TempDir()
	writeTopicPage(t, root, "uncited", "- alpha with no citations at all\n")
	body, _, _ := recallTopicPages(root, "alpha")
	if !strings.Contains(body, "### topics/uncited.md [matched: alpha]\n") {
		t.Errorf("citationless header wrong: %q", body)
	}
	if strings.Contains(body, "sources:") {
		t.Errorf("citationless page must not render a sources segment: %q", body)
	}
}

// TestCrossWsRecallPrefMatrix: off ⇒ no layer; topics ⇒ pages only;
// sibling ⇒ sibling note only; both ⇒ both; unknown ⇒ fails to the
// default "both" (not to silence).
func TestCrossWsRecallPrefMatrix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	writeTopicPage(t, root, "alpha-topic", "alpha topic content (ui-epoch-1)\n")
	writeEpochNote(t, root, "ui-epoch-1", "alpha sibling content\n")

	cases := []struct {
		prefs      string
		wantTopic  bool
		wantSib    bool
		wantOrigin []string
	}{
		{"", true, true, []string{"topic", "sibling"}},   // missing ⇒ both (default)
		{"cross_ws_recall: both\n", true, true, nil},     // explicit both
		{"cross_ws_recall: off\n", false, false, nil},    // off ⇒ nothing
		{"cross_ws_recall: topics\n", true, false, nil},  // pages only
		{"cross_ws_recall: sibling\n", false, true, nil}, // sibling only
		{"cross_ws_recall: bogus\n", true, true, nil},    // unknown ⇒ fail to default (both)
	}
	for _, tc := range cases {
		writePrefs(t, home, tc.prefs)
		block, sources := crossWsBlock(context.Background(), nil, root, "main", "alpha")
		gotTopic := strings.Contains(block, "### topics/alpha-topic.md")
		gotSib := strings.Contains(block, "### ui-epoch-1.md")
		if gotTopic != tc.wantTopic || gotSib != tc.wantSib {
			t.Errorf("prefs %q: topic=%v sibling=%v, want %v/%v (block %q)",
				strings.TrimSpace(tc.prefs), gotTopic, gotSib, tc.wantTopic, tc.wantSib, block)
		}
		if tc.prefs == "" && block == "" {
			t.Errorf("no prefs: block empty, want default both")
		}
		if len(tc.wantOrigin) > 0 {
			origins := map[string]bool{}
			for _, s := range sources {
				origins[s.origin] = true
				if len(s.sha) != 16 {
					t.Errorf("source %s sha = %q, want sha16", s.path, s.sha)
				}
			}
			for _, want := range tc.wantOrigin {
				if !origins[want] {
					t.Errorf("sources missing origin %q: %+v", want, sources)
				}
			}
		}
	}
}

// TestCrossWsPromptOrder: the send-path block sits after the recalled
// notes and before the memory map (injection order pinned).
func TestCrossWsPromptOrder(t *testing.T) {
	ml := memoryLayers{wiki: "WIKI-LAYER", cross: "## Cross-workstream context\n\nCROSS-LAYER", memoryMap: "## Memory read-back\n\nMAP-LAYER"}
	p := buildPrompt("msg", nil, ml)
	iW := strings.Index(p, "WIKI-LAYER")
	iC := strings.Index(p, "CROSS-LAYER")
	iM := strings.Index(p, "MAP-LAYER")
	if iW < 0 || iC < 0 || iM < 0 || !(iW < iC && iC < iM) {
		t.Errorf("order wiki=%d cross=%d map=%d, want wiki < cross < memoryMap", iW, iC, iM)
	}
}

// TestCrossWsSendPathReceipts: the journaled user_message carries the
// cross sources as recall entries with origin + matched_terms, and the
// receipt maps their real wiki paths to the chunk sha16s.
func TestCrossWsSendPathReceipts(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	writeTopicPage(t, root, "zeta-topic", "zeta odds and ends (ui-epoch-2)\n")
	writeEpochNote(t, root, "ui-epoch-2", "zeta from the ui sibling\n")

	rig := startRig(t, root)
	defer rig.stop(t)
	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "zeta"})

	var p struct {
		Recall  []map[string]interface{} `json:"recall"`
		Receipt map[string]string        `json:"receipt"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	byOrigin := map[string]map[string]interface{}{}
	for _, item := range p.Recall {
		if o, _ := item["origin"].(string); o != "" {
			byOrigin[o] = item
		}
	}
	topic, ok := byOrigin["topic"]
	if !ok {
		t.Fatalf("recall payload missing an origin:topic entry: %v", p.Recall)
	}
	if path, _ := topic["path"].(string); !strings.HasSuffix(path, filepath.Join("wiki", "topics", "zeta-topic.md")) {
		t.Errorf("topic origin path = %v, want the real wiki path", topic["path"])
	}
	if fmt.Sprint(topic["matched_terms"]) != "[zeta]" {
		t.Errorf("topic matched_terms = %v, want [zeta]", topic["matched_terms"])
	}
	sibling, ok := byOrigin["sibling"]
	if !ok {
		t.Fatalf("recall payload missing an origin:sibling entry: %v", p.Recall)
	}
	if path, _ := sibling["path"].(string); !strings.HasSuffix(path, "ui-epoch-2.md") {
		t.Errorf("sibling origin path = %v, want ui-epoch-2.md", sibling["path"])
	}
	if fmt.Sprint(sibling["matched_terms"]) != "[zeta]" {
		t.Errorf("sibling matched_terms = %v, want [zeta]", sibling["matched_terms"])
	}
	// Home-workstream notes stay origin-free (optional-field style).
	for _, item := range p.Recall {
		o, _ := item["origin"].(string)
		if o == "" {
			continue
		}
		if o != "topic" && o != "sibling" {
			t.Errorf("unknown origin %q in %v", o, item)
		}
	}
	// Receipts: both real wiki paths map to sha16s of their chunks.
	var topicRcpt, sibRcpt bool
	for path, sha := range p.Receipt {
		switch {
		case strings.HasSuffix(path, "zeta-topic.md"):
			topicRcpt = len(sha) == 16
		case strings.HasSuffix(path, "ui-epoch-2.md"):
			sibRcpt = len(sha) == 16
		}
	}
	if !topicRcpt || !sibRcpt {
		t.Errorf("receipt missing cross sources (topic=%v sibling=%v): %v", topicRcpt, sibRcpt, p.Receipt)
	}
	rig.pollUntilDone(t, convID)
}

// seedCrossStore builds a journal at root with one active conversation per
// named workstream and returns the store + per-workstream conversation IDs
// (F2 fixtures journal retractions into the candidate's OWN workstream).
func seedCrossStore(t *testing.T, root string, workstreams ...string) (*store.Store, map[string]int64) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(root, ".odo", "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateOrGetProject(ctx, root, "p")
	if err != nil {
		t.Fatal(err)
	}
	convs := map[string]int64{}
	for _, name := range workstreams {
		w, err := st.CreateOrGetWorkstream(ctx, p.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		c, err := st.CreateConversation(ctx, w.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		convs[name] = c.ID
	}
	return st, convs
}

// retractNote journals one memory_update{layer:"note", cause:"retract"}
// event (same parsing contract as retractedNoteSet).
func retractNote(t *testing.T, st *store.Store, convID int64, note, by string) {
	t.Helper()
	if _, err := st.AppendEvent(context.Background(), convID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":  "note",
		"cause":  "retract",
		"detail": note + " contradicted by " + by + ": fixture",
	})); err != nil {
		t.Fatal(err)
	}
}

// TestCrossSiblingRetractionOwnWorkstream (F2): the sibling push honors
// retractions journaled in the CANDIDATE'S OWN workstream — the current
// conversation's retraction set cannot see alien notes. Newest matched
// but retracted ⇒ the next-newest un-retracted wins; all retracted ⇒
// nothing is pushed; an un-retracted newer note still wins while the
// gate is active.
func TestCrossSiblingRetractionOwnWorkstream(t *testing.T) {
	root := t.TempDir()
	writeEpochNote(t, root, "ui-epoch-2", "zeta in ui epoch two\n")
	writeEpochNote(t, root, "ui-epoch-3", "newer zeta in ui epoch three\n")
	st, convs := seedCrossStore(t, root, "main", "ui")
	defer st.Close()
	ctx := context.Background()

	// Gate active, nothing retracted: the newer ui-epoch-3 still wins.
	_, item, ok := recallSiblingNote(ctx, st, root, "main", "zeta")
	if !ok || !strings.HasSuffix(item.path, "ui-epoch-3.md") {
		t.Fatalf("unretracted: item = %+v ok=%v, want ui-epoch-3.md (gate must not disturb normal selection)", item, ok)
	}

	// Retract ui-epoch-3 in UI's OWN conversation: the newest matched
	// candidate is retracted ⇒ fallback picks ui-epoch-2.
	retractNote(t, st, convs["ui"], "ui-epoch-3", "ui-epoch-4")
	_, item, ok = recallSiblingNote(ctx, st, root, "main", "zeta")
	if !ok || !strings.HasSuffix(item.path, "ui-epoch-2.md") {
		t.Fatalf("fallback: item = %+v ok=%v, want ui-epoch-2.md (newest retracted ⇒ next-newest)", item, ok)
	}

	// Retract ui-epoch-2 as well: every matched sibling is retracted in
	// its own workstream ⇒ nothing is pushed into main.
	retractNote(t, st, convs["ui"], "ui-epoch-2", "ui-epoch-3")
	if chunk, _, ok := recallSiblingNote(ctx, st, root, "main", "zeta"); ok || chunk != "" {
		t.Errorf("all retracted: chunk = %q ok=%v, want nothing pushed", chunk, ok)
	}
}

// TestCrossSiblingRetractionScopedToOwnWs (F2): a retraction journaled in
// the CURRENT conversation names home-workstream notes only — it must
// not leak onto a sibling candidate.
func TestCrossSiblingRetractionScopedToOwnWs(t *testing.T) {
	root := t.TempDir()
	writeEpochNote(t, root, "main-epoch-1", "zeta in the current workstream\n")
	writeEpochNote(t, root, "ui-epoch-2", "zeta from the ui sibling\n")
	st, convs := seedCrossStore(t, root, "main", "ui")
	defer st.Close()

	// main's conversation retracts main-epoch-1: home-scoped, irrelevant
	// to the sibling push (which never offers main's own notes anyway).
	retractNote(t, st, convs["main"], "main-epoch-1", "main-epoch-2")
	_, item, ok := recallSiblingNote(context.Background(), st, root, "main", "zeta")
	if !ok || !strings.HasSuffix(item.path, "ui-epoch-2.md") {
		t.Errorf("item = %+v ok=%v, want ui-epoch-2.md — a conversation's retraction set is home-scoped", item, ok)
	}
}

// TestCrossSiblingOversizeHeaderNoPanic (P2): a matched-terms list long
// enough to push the section header past the 2KB cap yields nothing —
// before the budget guard, the negative content allowance slice-panicked
// inside capAtLineBoundary.
func TestCrossSiblingOversizeHeaderNoPanic(t *testing.T) {
	root := t.TempDir()
	terms := make([]string, 0, 90)
	for i := 0; i < 90; i++ {
		terms = append(terms, fmt.Sprintf("term%02daaaaaaaaaaaaaaaaaaa", i))
	}
	query := strings.Join(terms, " ")
	writeEpochNote(t, root, "ui-epoch-1", query+" body\n")
	chunk, _, ok := recallSiblingNote(context.Background(), nil, root, "main", query)
	if ok || chunk != "" {
		t.Errorf("oversize header: ok=%v chunk=%d bytes, want nothing (header alone exceeds the cap)", ok, len(chunk))
	}
}

// TestCrossSiblingOnlyHeader (P2): with no matched topic page on disk the
// block header must not claim "topic pages" — it renders the neutral
// other-workstreams header around the sibling slice.
func TestCrossSiblingOnlyHeader(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeEpochNote(t, root, "ui-epoch-2", "alpha in ui without topics\n")
	block, sources := crossWsBlock(context.Background(), nil, root, "main", "alpha")
	if !strings.HasPrefix(block, "## Cross-workstream context (other workstreams)\n\n") {
		t.Errorf("sibling-only header wrong: %q", block[:min(80, len(block))])
	}
	if strings.Contains(block, "topic pages") {
		t.Errorf("sibling-only block must not claim topic pages: %q", block[:min(120, len(block))])
	}
	if len(sources) != 1 || sources[0].origin != "sibling" {
		t.Errorf("sources = %+v, want exactly one sibling source", sources)
	}
}

// TestCrossTopicLimitMarker (P2): matched pages past the top-2 limit are
// held back with the LIMIT marker — distinct from the 3KB cap marker
// pinned by TestCrossTopicPageBoundaryCap.
func TestCrossTopicLimitMarker(t *testing.T) {
	root := t.TempDir()
	writeTopicPage(t, root, "a-gamma", "gamma small page a\n")
	writeTopicPage(t, root, "b-gamma", "gamma small page b\n")
	writeTopicPage(t, root, "c-gamma", "gamma small page c\n")
	writeTopicPage(t, root, "d-gamma", "gamma small page d\n")

	body, items, _ := recallTopicPages(root, "gamma")
	if len(items) != 2 {
		t.Fatalf("items = %d, want top-2", len(items))
	}
	if !strings.Contains(body, "2 more matched topic page(s) held back by the top-2 limit") {
		t.Errorf("limit marker missing: %q", body)
	}
	if strings.Contains(body, "held back by the 3KB") {
		t.Errorf("cap marker must not appear without a cap cut: %q", body)
	}
}
