package ipc

// CJK recall tokenizer tests (batch 1, item C): pure-Chinese queries used
// to tokenize to ZERO terms, silently degrading keyword recall to
// newest-first. The latin path is pinned byte-for-byte; the CJK path adds
// overlapping bigrams.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestTokenizeQueryLatinUnchanged pins the pre-CJK behavior: same tokens,
// same order, same dedup for ASCII input.
func TestTokenizeQueryLatinUnchanged(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"how does the auth work", []string{"auth", "work"}},
		{"Fix Sidebar width!", []string{"fix", "sidebar", "width"}},
		{"fix fix fix", []string{"fix"}},
		{"http://example.com/ax?b=c", []string{"http", "example", "com", "ax", "b", "c"}},
		{"JWT auth jwt", []string{"jwt", "auth"}},
		{"", nil},
	}
	for _, tc := range cases {
		if got := tokenizeQuery(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("tokenizeQuery(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestTokenizeQueryCJK: pure-Chinese queries yield overlapping bigrams; an
// isolated char degrades to a unigram; kana and hangul tokenize too.
func TestTokenizeQueryCJK(t *testing.T) {
	got := tokenizeQuery("改一下侧边栏宽度")
	want := []string{"改一", "一下", "下侧", "侧边", "边栏", "栏宽", "宽度"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tokenizeQuery(改一下侧边栏宽度) = %v, want %v", got, want)
	}
	if got := tokenizeQuery("改"); !reflect.DeepEqual(got, []string{"改"}) {
		t.Errorf("isolated CJK char = %v, want [改] (unigram)", got)
	}
	for _, in := range []string{"テスト", "한국어"} {
		if terms := tokenizeQuery(in); len(terms) == 0 {
			t.Errorf("tokenizeQuery(%q) yielded no terms, want bigrams", in)
		}
	}
	// Repeated text de-duplicates against the same seen set.
	if got := tokenizeQuery("侧边侧边"); !reflect.DeepEqual(got, []string{"侧边", "边侧"}) {
		t.Errorf("dedup = %v, want [侧边 边侧]", got)
	}
}

// TestTokenizeQueryMixed: latin tokens keep their exact pre-CJK order, with
// the CJK bigrams appended after them in reading order.
func TestTokenizeQueryMixed(t *testing.T) {
	got := tokenizeQuery("fix 侧边栏 bug")
	want := []string{"fix", "bug", "侧边", "边栏"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mixed query = %v, want %v", got, want)
	}
}

// TestMatchSkillsCJK: matchSkills inherits the tokenizer (same function),
// so Chinese queries rank skills with Chinese descriptions — there was no
// skills.go change to test around.
func TestMatchSkillsCJK(t *testing.T) {
	entries := []skillEntry{
		{info: SkillInfo{Name: "ui-layout", Description: "调整侧边栏样式", Keywords: []string{"ui"}}, body: "steps"},
		{info: SkillInfo{Name: "schema-migrations", Description: "database work", Keywords: []string{"db"}}, body: "steps"},
	}
	matched := matchSkills("改一下侧边栏宽度", entries)
	if len(matched) != 1 || matched[0].info.Name != "ui-layout" {
		t.Errorf("matchSkills(改一下侧边栏宽度) = %+v, want only ui-layout", matched)
	}
}

// TestRecallCJKMatchedTermsJournaled: the journaled user_message recall
// payload carries non-empty matched_terms for a Chinese query — the
// receipt proves recall matched by bigram instead of falling back to
// silent newest-first.
func TestRecallCJKMatchedTermsJournaled(t *testing.T) {
	root := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODO_OMP_WRAPPER", writeStub(t, stubWrapper))
	writeEpochNote(t, root, "main-epoch-1", "# Epoch 1\n\n调整了侧边栏宽度并记录了原因。\n")
	rig := startRig(t, root)
	defer rig.stop(t)

	boot := rig.call(t, Request{Cmd: CmdBootstrap, ProjectRoot: root})
	convID := boot.Conversation.ID
	sent := rig.call(t, Request{Cmd: CmdSendMessage, ConversationID: convID, Text: "改一下侧边栏宽度"})
	rig.pollUntilDone(t, convID)

	var p struct {
		Recall []struct {
			Path         string   `json:"path"`
			MatchedTerms []string `json:"matched_terms"`
		} `json:"recall"`
	}
	if err := json.Unmarshal(sent.Event.Payload, &p); err != nil {
		t.Fatalf("user_message payload: %v", err)
	}
	var terms []string
	for _, item := range p.Recall {
		if strings.HasSuffix(item.Path, "main-epoch-1.md") {
			terms = item.MatchedTerms
		}
	}
	if len(terms) == 0 {
		t.Errorf("CJK query: recall matched_terms empty (degraded to newest-first): %+v", p.Recall)
	}
}
