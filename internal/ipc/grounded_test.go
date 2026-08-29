package ipc

// D2 (control-plane hardening lock, docs/design/control-plane-hardening-lock.md):
// the repo-grounded reviewer leg — one-hop scope computation, the
// model-visible ⟺ logged receipt mirror, budget and round caps with the
// verdict still owed, the gate-source fail-closed posture, and grounded
// vs ungrounded prompt byte discipline.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yingliang-zhang/odo/internal/moa"
	"github.com/yingliang-zhang/odo/internal/store"
)

// writeGroundedFile writes one fixture file under root, mkdir-ing its
// parents.
func writeGroundedFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// oneHopRepo builds the D2 scope fixture: touched a/pkg1/x.go is imported
// by b/pkg2/y.go, imports internal/dep itself, and has a same-dir sibling
// sib.go; c/pkg3/z.go is outside the neighborhood.
func oneHopRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeGroundedFile(t, root, "go.mod", "module example.com/m\n\ngo 1.24\n")
	writeGroundedFile(t, root, "a/pkg1/x.go", "package pkg1\n\nimport \"example.com/m/internal/dep\"\n\nvar X = dep.Y\n")
	writeGroundedFile(t, root, "a/pkg1/sib.go", "package pkg1\n\nconst S = 1\n")
	writeGroundedFile(t, root, "b/pkg2/y.go", "package pkg2\n\nimport \"example.com/m/a/pkg1\"\n\nvar _ = pkg1.X\n")
	writeGroundedFile(t, root, "c/pkg3/z.go", "package pkg3\n\nconst Z = 1\n")
	writeGroundedFile(t, root, "internal/dep/dep.go", "package dep\n\nconst Y = 1\n")
	return root
}

// TestGroundedScopeOneHop pins the allowlist: touched path ∪ same-dir
// siblings ∪ repo files importing the touched package ∪ repo-internal
// packages the touched file imports — and pkg3 stays OUT (the lock's
// fixture shape).
func TestGroundedScopeOneHop(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // scope computation is prefs-independent
	root := oneHopRepo(t)
	exec := newFSToolExecutorRooted(root)
	at := func(rel string) string { return filepath.Join(exec.root, filepath.FromSlash(rel)) }

	scope := computeGroundedScope(exec.root, []string{"a/pkg1/x.go"})
	if scope.truncated {
		t.Error("scope.truncated = true on a clean fixture (the import neighborhood resolved)")
	}
	for _, rel := range []string{"a/pkg1/x.go", "a/pkg1/sib.go", "b/pkg2/y.go", "internal/dep/dep.go"} {
		if !scope.allows(at(rel)) {
			t.Errorf("scope refuses %s — must admit the touched file, its same-dir sibling, its importer, and its import", rel)
		}
	}
	if !scope.dirs[filepath.Dir(at("a/pkg1/x.go"))] {
		t.Error("same-dir entry (touched package siblings) missing from the allowlist")
	}
	if !scope.files[at("b/pkg2/y.go")] {
		t.Error("importer b/pkg2/y.go missing — the bounded import grep did not find it")
	}
	if !scope.dirs[at("internal/dep")] {
		t.Error("imported package internal/dep missing from the allowlist")
	}
	if scope.allows(at("c/pkg3/z.go")) {
		t.Error("scope admits c/pkg3/z.go — a file outside the one-hop neighborhood")
	}
	if scope.count() == 0 || scope.sha() == "" {
		t.Errorf("scope identity missing: count=%d sha=%q", scope.count(), scope.sha())
	}

	// Executor behavior: an in-scope read succeeds; out-of-scope reads
	// (file and dir) are refused model-visibly.
	scoped := &scopedToolExecutor{inner: exec, scope: scope}
	out, err := scoped.Execute(context.Background(), moaToolCall("read_file", `{"path":"b/pkg2/y.go"}`))
	if err != nil {
		t.Fatalf("in-scope read refused: %v", err)
	}
	if !strings.Contains(out, "package pkg2") {
		t.Errorf("in-scope read returned %q, want the importer's content", out)
	}
	if _, err := scoped.Execute(context.Background(), moaToolCall("read_file", `{"path":"c/pkg3/z.go"}`)); err == nil || !strings.Contains(err.Error(), "outside the grounded review scope") {
		t.Errorf("out-of-scope file read: err = %v, want a grounded-scope refusal", err)
	}
	if _, err := scoped.Execute(context.Background(), moaToolCall("grep", `{"pattern":"Z","path":"c/pkg3"}`)); err == nil || !strings.Contains(err.Error(), "outside the grounded review scope") {
		t.Errorf("out-of-scope dir grep: err = %v, want a grounded-scope refusal", err)
	}
	if _, err := scoped.Execute(context.Background(), moaToolCall("grep", `{"pattern":"pkg1","path":"b/pkg2"}`)); err == nil || !strings.Contains(err.Error(), "outside the grounded review scope") {
		// The importer's DIRECTORY is not in scope — only the matched
		// FILE is (a dir grep would expose its non-scope neighbors).
		t.Errorf("grep over a scope-file's parent dir: err = %v, want refusal", err)
	}
	if out, err := scoped.Execute(context.Background(), moaToolCall("grep", `{"pattern":"pkg1","path":"b/pkg2/y.go"}`)); err != nil || !strings.Contains(out, "pkg1") {
		t.Errorf("in-scope file grep: out=%q err=%v, want the match served", out, err)
	}
	if _, err := scoped.Execute(context.Background(), moaToolCall("grep", `{"pattern":"pkg1|pkg2","path":"a/pkg1"}`)); err != nil {
		t.Errorf("in-scope dir grep (touched package dir) refused: %v", err)
	}
}

// moaToolCall builds one tool call as the client would hand it to the
// executor.
func moaToolCall(name, input string) moa.ToolCall {
	return moa.ToolCall{ID: "call-" + name, Name: name, Input: json.RawMessage(input)}
}

// groundedStub serves a scripted Anthropic Messages endpoint: script
// receives the 1-based post ordinal and returns the response's content
// blocks and stop_reason. bodies() replays the captured request bodies in
// order for receipt-mirror assertions.
func groundedStub(t *testing.T, script func(post int) (blocks []map[string]interface{}, stop string)) func() [][]byte {
	t.Helper()
	var mu sync.Mutex
	var bodies [][]byte
	var posts int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		n := atomic.AddInt64(&posts, 1)
		mu.Lock()
		bodies = append(bodies, raw)
		mu.Unlock()
		blocks, stop := script(int(n))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"content": blocks, "stop_reason": stop})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MOA_BASE_URL", srv.URL)
	t.Setenv("SUDO_CODING_KEY", "test-key")
	return func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), bodies...)
	}
}

func readUse(id, path string) map[string]interface{} {
	return map[string]interface{}{
		"type":  "tool_use",
		"id":    id,
		"name":  "read_file",
		"input": map[string]interface{}{"path": path},
	}
}

func textBlock(text string) []map[string]interface{} {
	return []map[string]interface{}{{"type": "text", "text": text}}
}

// groundedPlanFor resolves a plan against a one-model line (the prefs-free
// default: idx 0, resolved_by "first").
func groundedPlanFor(t *testing.T, s *Server, root string, touched []string) groundedPlan {
	t.Helper()
	plan := s.planGrounded([]reviewModel{{model: "rmG", provider: "test"}}, root, touched, nil)
	if !plan.ok {
		t.Fatalf("planGrounded: init failure on a clean fixture: %s", plan.detail)
	}
	if plan.idx != 0 || plan.resolvedBy != "first" {
		t.Errorf("resolution = idx %d by %q, want idx 0 by first (no grounded_reviewer: pref)", plan.idx, plan.resolvedBy)
	}
	return plan
}

// TestGroundedReceiptMirror pins the structural invariant: every read the
// model was SHOWN appears in the journaled tool_calls and vice versa —
// refusal rows included (a cited read that never executed, or an executed
// read that never journaled, both break the mirror).
func TestGroundedReceiptMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeGroundedFile(t, root, "sub/a.go", "package sub\n\nconst A = 1\n")
	writeGroundedFile(t, root, "z.go", "package main\n")
	bodies := groundedStub(t, func(post int) ([]map[string]interface{}, string) {
		if post == 1 {
			// One allowed read, one out-of-scope read — same round.
			return []map[string]interface{}{readUse("call-1", "sub/a.go"), readUse("call-2", "z.go")}, "tool_use"
		}
		return textBlock("ACCEPT\n\ngrounded verdict."), "end_turn"
	})

	s := &Server{projectRoot: root}
	plan := groundedPlanFor(t, s, root, []string{"sub/a.go"})
	rr := s.reviewWithModelGrounded(context.Background(), reviewModel{model: "rmG", provider: "test"}, "review this diff", plan)

	if rr.Verdict != "accept" {
		t.Errorf("verdict = %q (%s), want accept", rr.Verdict, rr.Comments)
	}
	if !rr.Grounded || rr.ResolvedBy != "first" || rr.ScopeSHA16 == "" || rr.ScopeFiles == 0 {
		t.Errorf("grounded receipts = %+v, want grounded/resolved_by/scope set", rr)
	}
	if len(rr.ToolCalls) != 2 {
		t.Fatalf("tool_calls = %d, want 2 (the allowed read and the journaled refusal)", len(rr.ToolCalls))
	}
	if rr.ToolCalls[0].Error != "" || rr.ToolCalls[0].ResultBytes == 0 {
		t.Errorf("tool_calls[0] = %+v, want the served read", rr.ToolCalls[0])
	}
	if rr.ToolCalls[1].Error == "" || !strings.Contains(rr.ToolCalls[1].Error, "outside the grounded review scope") {
		t.Errorf("tool_calls[1] = %+v, want the journaled scope refusal (refusals are logged)", rr.ToolCalls[1])
	}

	// Mirror: collect the tool_result blocks the model was SHOWN from the
	// second post's messages, then check 1:1 against the audits — served
	// content byte-count ⟷ ResultBytes, refusal text ⟷ Error.
	if got := len(bodies()); got != 2 {
		t.Fatalf("posts = %d, want 2 (tool round + answer)", got)
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodies()[1], &req); err != nil {
		t.Fatalf("decode second post: %v", err)
	}
	type wireBlock struct {
		Type    string `json:"type"`
		Content string `json:"content"`
		IsError bool   `json:"is_error"`
	}
	var results []wireBlock
	for _, m := range req.Messages {
		if len(m.Content) == 0 || m.Content[0] != '[' {
			continue // plain string content (the initial prompt)
		}
		var blocks []wireBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_result" {
				results = append(results, b)
			}
		}
	}
	if len(results) != len(rr.ToolCalls) {
		t.Fatalf("mirror broken: %d tool_results shown vs %d audits journaled", len(results), len(rr.ToolCalls))
	}
	for i, audit := range rr.ToolCalls {
		shown := results[i]
		if shown.IsError != (audit.Error != "") {
			t.Errorf("call %d: shown is_error=%v but audit error=%q — the mirror diverged", i, shown.IsError, audit.Error)
		}
		if audit.Error == "" {
			if len(shown.Content) != audit.ResultBytes {
				t.Errorf("call %d: shown %d bytes, audit claims %d — citing-without-calling would pass", i, len(shown.Content), audit.ResultBytes)
			}
		} else if shown.Content != "error: "+audit.Error {
			t.Errorf("call %d: shown refusal %q ≠ journaled error %q", i, shown.Content, audit.Error)
		}
	}
}

// TestGroundedBudget pins both caps: the 256KB total-read budget trips
// (the crossing read is served, the next call refused model-visibly) and
// the leg STILL owes a verdict — flagged tool_budget_exhausted, verdict
// parsed; and the 8-round wall refuses a stubborn tool loop — the
// existing fail-closed degradation (needs_fixes) with every executed
// call still journaled.
func TestGroundedBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// 400 lines × 100 chars ≈ 42.3KB per read under the 64KB per-read
	// cap: 6 reads ≈ 254KB fit, the 7th crosses 256KB and trips the
	// budget, the 8th is refused.
	big := strings.Repeat(strings.Repeat("x", 100)+"\n", 400)

	t.Run("byte budget trips and the verdict still parses", func(t *testing.T) {
		root := t.TempDir()
		writeGroundedFile(t, root, "sub/a.go", "package sub\n\nconst A = 1\n")
		writeGroundedFile(t, root, "sub/big.txt", big)
		bodies := groundedStub(t, func(post int) ([]map[string]interface{}, string) {
			if post <= 4 {
				// Two reads per round — 8 calls across 4 rounds (well
				// under the round wall, isolating the BYTE cap).
				return []map[string]interface{}{readUse(fmt.Sprintf("call-%d-a", post), "sub/big.txt"), readUse(fmt.Sprintf("call-%d-b", post), "sub/big.txt")}, "tool_use"
			}
			return textBlock("ACCEPT\n\nbudget verdict."), "end_turn"
		})

		s := &Server{projectRoot: root}
		plan := groundedPlanFor(t, s, root, []string{"sub/a.go"})
		rr := s.reviewWithModelGrounded(context.Background(), reviewModel{model: "rmG", provider: "test"}, "review this diff", plan)

		if rr.Verdict != "accept" {
			t.Errorf("verdict = %q (%s), want accept — budget exhaustion must NOT cost the leg its verdict", rr.Verdict, rr.Comments)
		}
		if !rr.ToolBudgetExhausted {
			t.Error("tool_budget_exhausted = false, want true (journaled on the exhausted leg)")
		}
		if got := len(rr.ToolCalls); got != 8 {
			t.Fatalf("tool_calls = %d, want 8 (7 served + 1 refusal)", got)
		}
		for i := 0; i < 7; i++ {
			if rr.ToolCalls[i].Error != "" {
				t.Errorf("tool_calls[%d] errored: %v — the crossing read is SERVED before the budget trips", i, rr.ToolCalls[i].Error)
			}
		}
		if !strings.Contains(rr.ToolCalls[7].Error, "budget exhausted") {
			t.Errorf("tool_calls[7].Error = %q, want the budget-exhausted refusal (model-visible ⟺ logged)", rr.ToolCalls[7].Error)
		}
		if rr.ReadBytes <= groundedTotalBytes {
			t.Errorf("read_bytes = %d, want > %d (the crossing read is what trips the budget)", rr.ReadBytes, groundedTotalBytes)
		}
		if got := len(bodies()); got != 5 {
			t.Errorf("posts = %d, want 5 (4 tool rounds + answer)", got)
		}
	})

	t.Run("the round wall refuses a stubborn tool loop fail-closed", func(t *testing.T) {
		root := t.TempDir()
		writeGroundedFile(t, root, "sub/a.go", "package sub\n\nconst A = 1\n")
		groundedStub(t, func(post int) ([]map[string]interface{}, string) {
			return []map[string]interface{}{readUse(fmt.Sprintf("call-%d", post), "sub/a.go")}, "tool_use" // never stops asking
		})

		s := &Server{projectRoot: root}
		plan := groundedPlanFor(t, s, root, []string{"sub/a.go"})
		rr := s.reviewWithModelGrounded(context.Background(), reviewModel{model: "rmG", provider: "test"}, "review this diff", plan)

		// maxRounds = 8 executes 7 tool rounds and refuses the 8th (the
		// post that would START the 9th API turn never leaves); with no
		// verdict token the existing fail-closed degradation applies.
		if rr.Verdict != "needs_fixes" || !strings.Contains(rr.Comments, "exceeded 8 rounds") {
			t.Errorf("degraded leg = %q (%s), want needs_fixes naming the 8-round wall", rr.Verdict, rr.Comments)
		}
		// 2026-08-29 (P1 diff #101 lesson): tool-loop exhaustion IS infra
		// regardless of posture — the leg's reasoning machinery failed
		// before it could judge, so its verdict is not direction evidence.
		if !rr.Infra {
			t.Error("Infra = false on loop exhaustion — a burned-out tool loop is not a judgment")
		}
		if got := len(rr.ToolCalls); got != 7 {
			t.Errorf("tool_calls = %d, want 7 (every executed round journaled; the refused 8th round executed nothing)", got)
		}
		if rr.ToolBudgetExhausted {
			t.Error("tool_budget_exhausted = true with tiny reads — only the byte cap may set it")
		}
		if !rr.Grounded || rr.ScopeSHA16 == "" {
			t.Error("degraded row missing grounded receipts — refusals and audits ride every outcome")
		}
	})
}

// TestGateDiffRequiresGroundedLeg (the lock's Sol clause): on a
// gate-source diff the grounded leg is REQUIRED by default
// (grounded_review_required = gate_sources) — an init failure degrades
// it to Infra, so the round fails closed as panel_infra and the blocked
// row journals grounding:"degraded".
func TestGateDiffRequiresGroundedLeg(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePrefs(t, home, "review: rm1@test, rm2@test\n")
	calls := startPanelStub(t, func(call int64, model string) (int, string) {
		return 200, "ACCEPT\nlooks correct"
	})
	f := newAutonomyFixture(t)
	root, _ := autolandRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "internal", "ipc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "ipc", "core.go"), []byte("package ipc\n\nconst V = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".odo-verify"), []byte("echo PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "gate source + verify")

	s := &Server{store: f.st, projectRoot: root, groundedInitFailForTest: "test-forced scope failure"}
	d := baseBoundDiff(t, f, root, "p.diff", realPatch(t, root, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "internal", "ipc", "core.go"), []byte("package ipc\n\nconst V = 2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}))
	s.autoLand(context.Background(), d, root, "goal", false, "")

	sc := scanSettle(t, f.st, f.c.ID)
	if got := sc.blockedReasons(); len(got) != 1 || got[0] != "panel_infra" {
		t.Fatalf("blocked reasons = %v, want [panel_infra] (a degraded required grounding fails the round closed)", got)
	}
	if got := sc.blocked[0]["grounding"]; got != "degraded" {
		t.Errorf("blocked grounding = %v, want \"degraded\"", got)
	}
	if len(sc.accepts) != 0 {
		t.Errorf("accepts = %v, want none (the round never validly completed)", sc.accepts)
	}
	reviews, ok := sc.blocked[0]["reviews"].([]interface{})
	if !ok || len(reviews) != 2 {
		t.Fatalf("blocked reviews = %v, want the two legs attached", sc.blocked[0]["reviews"])
	}
	leg0, _ := reviews[0].(map[string]interface{})
	if leg0["grounded"] != true || leg0["infra"] != true {
		t.Errorf("grounded leg = %v, want grounded:true + infra:true (the degraded grounded leg IS the infra leg)", leg0)
	}
	if leg0["resolved_by"] != "first" {
		t.Errorf("resolved_by = %v, want first (no grounded_reviewer: pref)", leg0["resolved_by"])
	}
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Errorf("panel calls = %d, want 1 (only the ungrounded leg spent; the init-failed leg never queried)", n)
	}
	got, err := f.st.GetDiff(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.DiffPending {
		t.Errorf("diff status = %q, want pending (fail-closed)", got.Status)
	}
}

// TestAuditLegGroundedPrompt pins the prompt discipline: the grounded
// auditor's system drops "Do not review what the diff does not touch."
// for the scoped-repo-reads clause while the UNGROUNDED contract (and the
// ungrounded review prompt) stay byte-identical.
func TestAuditLegGroundedPrompt(t *testing.T) {
	grounded := auditSystemGrounded()
	if grounded == auditSystem {
		t.Fatal("grounded audit system == auditSystem — the clause swap no-op'd")
	}
	if strings.Contains(grounded, auditNoTouchClause) {
		t.Error("grounded system still carries the no-touch clause")
	}
	for _, token := range []string{"read-only tools over the repository", "one-hop import neighborhood", "every read is journaled"} {
		if !strings.Contains(grounded, token) {
			t.Errorf("grounded system missing %q from the scoped-repo-reads clause", token)
		}
	}
	if !strings.Contains(auditSystem, auditNoTouchClause) {
		t.Error("auditSystem lost the no-touch clause — ungrounded legs must stay byte-identical")
	}
	// The grounded variant differs from auditSystem by EXACTLY the swap.
	if back := strings.Replace(grounded, auditGroundedClause, auditNoTouchClause, 1); back != auditSystem {
		t.Errorf("grounded system diverges beyond the clause swap:\n%q", grounded)
	}

	// Panel prompt: grounded gains the notice; ungrounded is unchanged.
	bad := filepath.Join(t.TempDir(), "missing.diff")
	in := reviewPromptInput{mode: reviewPromptAdvisory, diffPath: bad, diffText: "junk", verifyNote: "not run"}
	base := buildReviewPrompt(in)
	if strings.Contains(base, groundedReviewNotice) {
		t.Error("ungrounded prompt carries the grounded notice — ungrounded legs must stay byte-identical")
	}
	in.grounded = true
	g := buildReviewPrompt(in)
	if !strings.Contains(g, groundedReviewNotice) {
		t.Error("grounded prompt missing the tool notice")
	}
	// The notice is a pure insertion: the grounded prompt still ends in
	// the ungrounded prompt's tail.
	if !strings.HasSuffix(g, base[len(base)-60:]) {
		t.Error("grounded prompt is not the ungrounded prompt plus an additive notice")
	}
}
