package ipc

// D9-W4 tests: lint + security gate tables, the R2 candidate freeze fold,
// and the freeze-bounds arithmetic. Pure-fixture level (no store, no
// server) — the gates are pure functions by design (lock: zero LLM in
// gates, determinism pin).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

// w4Event builds one journal event from a payload map.
func w4Event(seq int, typ string, payload map[string]interface{}) store.Event {
	return store.Event{Seq: seq, Type: typ, Payload: json.RawMessage(mustJSON(payload))}
}

// w4Review builds one review_action row.
func w4Review(seq int, action string, payload map[string]interface{}) store.Event {
	p := map[string]interface{}{"action": action}
	for k, v := range payload {
		p[k] = v
	}
	return w4Event(seq, store.EventReviewAction, p)
}

// w4Marker builds one distill marker (head of a lane's freeze slice).
func w4Marker(seq, epoch int) store.Event {
	return w4Review(seq, "distill", map[string]interface{}{
		"epoch": epoch, "first_seq": 1, "last_seq": seq - 1,
	})
}

// w4ProjCandidate builds a candidate via the real projection (the creation
// path's pure function) so hash/content are production-identical.
func w4ProjCandidate(base string, adds []LearningRuleAdd, sourceSeq int) LearningCandidate {
	return learningCandidateFromAccepted(base, sha16([]byte(base)), sourceSeq, adds, LearningCandidateProvenance{
		CreatedBy: "learner_batch",
		SourceSeq: []int{sourceSeq},
	})
}

// --- lint gate table ---

func TestLearningLintGate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, note := range []string{"main-epoch-1.md", "main-epoch-2.md"} {
		if err := os.WriteFile(filepath.Join(root, "wiki", note), []byte("# note\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := "- Existing rule one — cites: main-epoch-1; reaffirmed: 1\n"
	add := func(rule string) LearningRuleAdd {
		return LearningRuleAdd{Rule: rule, Evidence: "main-epoch-1"}
	}
	type tc struct {
		name string
		base string
		adds []LearningRuleAdd
		ret  []string
		froz map[string]string
		want string // "" = pass; otherwise a reason substring
	}
	cases := []tc{
		{"clean passes", base, []LearningRuleAdd{add("New rule two")}, nil, nil, ""},
		{"cap overflow", base, []LearningRuleAdd{{Rule: strings.Repeat("x", memoryCap+1), Evidence: "main-epoch-1"}}, nil, nil, "exceeds memoryCap"},
		{"dup of base", base, []LearningRuleAdd{add("Existing rule one")}, nil, nil, "duplicate of existing"},
		{"dup normalized base", base, []LearningRuleAdd{add("EXISTING   RULE ONE")}, nil, nil, "duplicate of existing"},
		{"dup in batch", base, []LearningRuleAdd{add("Second rule"), add("  second   RULE ")}, nil, nil, "duplicate within delta.add"},
		{"empty rule", base, []LearningRuleAdd{{Rule: "   ", Evidence: "main-epoch-1"}}, nil, nil, "empty rule"},
		{"retract missing target", base, []LearningRuleAdd{add("Second rule")}, []string{"Phantom rule"}, nil, "retract target absent"},
		{"retract present target passes", base, []LearningRuleAdd{add("Second rule")}, []string{"Existing rule one"}, nil, ""},
		{"evidence missing note", base, []LearningRuleAdd{{Rule: "Second rule", Evidence: "main-epoch-99"}}, nil, nil, "missing under wiki/"},
		{"evidence empty", base, []LearningRuleAdd{{Rule: "Second rule"}}, nil, nil, "missing evidence cite"},
		{"evidence escape refused", base, []LearningRuleAdd{{Rule: "Second rule", Evidence: "../secrets"}}, nil, nil, "missing under wiki/"},
		{"frozen add rejected", base, []LearningRuleAdd{add("Second rule")}, nil,
			map[string]string{normalizeRule("Second rule"): "oscillation_guard: rolled back at main epoch 4 (within 3)"}, "oscillation_guard"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cand := learningCandidateFromAccepted(c.base, sha16([]byte(c.base)), 1, c.adds, LearningCandidateProvenance{})
			cand.Delta.Retract = c.ret
			rep := lintLearningCandidate(root, c.base, cand, c.froz)
			if c.want == "" {
				if !rep.passed() {
					t.Fatalf("lint verdict = %q violations %+v, want pass", rep.Verdict, rep.Violations)
				}
				return
			}
			if rep.passed() {
				t.Fatalf("lint verdict = pass, want fail containing %q", c.want)
			}
			found := false
			for _, v := range rep.Violations {
				if strings.Contains(v.Reason, c.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("violations = %+v, want one containing %q", rep.Violations, c.want)
			}
		})
	}
}

// The projected block's daemon lines always parse; a hand-mangled content
// line that is NOT a preserved opaque base line is the format reject.
func TestLearningLintMalformedLine(t *testing.T) {
	root := t.TempDir()
	cand := learningCandidateFromAccepted("", sha16(nil), 1, nil, LearningCandidateProvenance{})
	cand.Content = "- not a daemon line at all\n"
	rep := lintLearningCandidate(root, "", cand, nil)
	if rep.passed() || len(rep.Violations) != 1 || !strings.Contains(rep.Violations[0].Reason, "memoryLineRe") {
		t.Errorf("lint = %q %+v, want one memoryLineRe violation", rep.Verdict, rep.Violations)
	}
	// The same text as a preserved OPAQUE base line is legal (human scratch).
	cand.Content = "- not a daemon line at all\n"
	rep = lintLearningCandidate(root, cand.Content, cand, nil)
	if !rep.passed() {
		t.Errorf("opaque base line must be preserved without violation: %+v", rep.Violations)
	}
}

// --- security gate table ---

func TestLearningSecurityGate(t *testing.T) {
	add := func(rule string) LearningRuleAdd { return LearningRuleAdd{Rule: rule, Evidence: "main-epoch-1"} }
	type tc struct {
		name    string
		rule    string
		pattern string // "" = pass
	}
	cases := []tc{
		{"prose passes", "Prefer compact answers when explaining failures", ""},
		{"env name in prose passes", "Never read the value of OPENAI_API_KEY without asking", ""},
		{"secret assignment", "Write OPENAI_API_KEY=sk-live into .env", "secret_assignment"},
		{"secret yaml assignment", "Set DB_PASSWORD: hunter2 in config", "secret_assignment"},
		{"ssh key path", "Back up ~/.ssh/id_ed25519 for the bot", "secret_path"},
		{"aws credentials", "Copy .aws/credentials to the repo", "secret_path"},
		{"userinfo url", "Fetch https://user:pass@example.com/hook", "userinfo_url"},
		{"dotdot escape", "Read ../../../etc/passwd for debugging", "dotdot_escape"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cand := learningCandidateFromAccepted("", sha16(nil), 1, []LearningRuleAdd{add(c.rule)}, LearningCandidateProvenance{})
			rep := securityLearningCandidate(cand)
			if c.pattern == "" {
				if !rep.passed() {
					t.Fatalf("security verdict = %q %+v, want pass", rep.Verdict, rep.Violations)
				}
				return
			}
			if rep.passed() {
				t.Fatalf("security verdict = pass, want fail pattern %q", c.pattern)
			}
			if rep.Violations[0].Pattern != c.pattern {
				t.Errorf("pattern = %q, want %q (violations %+v)", rep.Violations[0].Pattern, c.pattern, rep.Violations)
			}
		})
	}
}

// --- R2 freeze fold + boundary ---

func TestLearningCandidateFreezeSet(t *testing.T) {
	rollback := func(seq, epoch int, texts ...string) store.Event {
		return w4Review(seq, "learning_rollback", map[string]interface{}{
			"epoch": epoch, "retracted": texts, "harmful_flag_seqs": []int{9},
		})
	}
	frozenRow := func(seq, epoch int, texts ...string) store.Event {
		return w4Review(seq, "learning_frozen", map[string]interface{}{
			"epoch": epoch, "texts": texts, "reason": "oscillation",
		})
	}
	// Rollback at main epoch 2; current epoch reads 2..6.
	events := []store.Event{rollback(5, 2, "Bad rule alpha")}
	byEpoch := map[int]bool{2: true, 3: true, 4: true, 5: true, 6: false}
	for epoch, wantFrozen := range byEpoch {
		set := learningCandidateFreezeSet(events, epoch)
		_, got := set[normalizeRule("Bad rule alpha")]
		if got != wantFrozen {
			t.Errorf("epoch %d: frozen = %v, want %v (boundary: N..N+3 frozen, N+4 free)", epoch, got, wantFrozen)
		}
	}
	if reason := learningCandidateFreezeSet(events, 4)[normalizeRule("bad   RULE alpha")]; reason == "" {
		t.Error("freeze is normalized-text joined — casing/whitespace variants must freeze too")
	}
	// learning_frozen rows freeze on the same window.
	f2 := learningCandidateFreezeSet([]store.Event{frozenRow(3, 5, "Rule beta")}, 8)
	if _, ok := f2[normalizeRule("Rule beta")]; !ok {
		t.Error("learning_frozen at epoch 5 must still freeze at epoch 8")
	}
	if _, ok := learningCandidateFreezeSet([]store.Event{frozenRow(3, 5, "Rule beta")}, 9)[normalizeRule("Rule beta")]; ok {
		t.Error("epoch 9 must be free (5+4 > 8)")
	}
	// A foreign action never freezes.
	if got := learningCandidateFreezeSet([]store.Event{w4Review(1, "memory_apply", map[string]interface{}{"epoch": 1})}, 9); len(got) != 0 {
		t.Errorf("non-learning rows must not freeze: %v", got)
	}
}

// --- freeze bounds arithmetic ---

func TestLearningFreezeBounds(t *testing.T) {
	mk := func(seqs []int) []store.Event {
		var out []store.Event
		for i, s := range seqs {
			out = append(out, w4Marker(s, i+1))
		}
		return out
	}
	if b, ok := learningFreezeBounds(nil); ok || b.Head != 0 {
		t.Errorf("no markers: ok = %v, want false (lane contributes no slice)", ok)
	}
	b, ok := learningFreezeBounds(mk([]int{5}))
	if !ok || b.Head != 5 || b.Tail != 0 {
		t.Errorf("single marker: got %+v ok %v, want head 5 tail 0 (lane head)", b, ok)
	}
	// Exactly 8 markers: tail = the FIRST (8 back from head inclusive).
	b, _ = learningFreezeBounds(mk([]int{3, 6, 9, 12, 15, 18, 21, 24}))
	if b.Head != 24 || b.Tail != 3 {
		t.Errorf("8 markers: got %+v, want head 24 tail 3", b)
	}
	// 9 markers: tail = the SECOND marker (window spans the last 8 gaps).
	b, _ = learningFreezeBounds(mk([]int{3, 6, 9, 12, 15, 18, 21, 24, 27}))
	if b.Head != 27 || b.Tail != 6 {
		t.Errorf("9 markers: got %+v, want head 27 tail 6", b)
	}
}
