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
	"time"

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
	t.Setenv(groundedRoundsEnv, "") // no ambient hatch: this test pins the D9-C default budget
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
		// D9-C: tool_rounds_used rides EVERY grounded row (not just cap
		// deaths), and fail-soft byte exhaustion stays visually distinct
		// from the fail-hard round-cap death marker.
		if rr.ToolRoundsUsed != 8 {
			t.Errorf("tool_rounds_used = %d, want 8 (journaled on every grounded row, not just cap deaths)", rr.ToolRoundsUsed)
		}
		if strings.Contains(rr.Comments, "round-cap death") {
			t.Errorf("comments = %q — byte-budget exhaustion must never read as a round-cap death", rr.Comments)
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

		// maxRounds = 40 (D9-C; was 16) executes 39 tool rounds and
		// refuses the 40th; with no verdict token the existing fail-closed
		// degradation applies, and the row's marker keeps the fail-hard
		// round-cap death visually distinct from fail-soft byte-budget
		// exhaustion.
		if rr.Verdict != "needs_fixes" || !strings.Contains(rr.Comments, "exceeded 40 rounds") {
			t.Errorf("degraded leg = %q (%s), want needs_fixes naming the 40-round wall", rr.Verdict, rr.Comments)
		}
		if !strings.Contains(rr.Comments, "round-cap death") {
			t.Errorf("comments = %q, want the fail-hard round-cap-death marker (never blurred with byte exhaustion)", rr.Comments)
		}
		// 2026-08-29 (P1 diff #101 lesson): tool-loop exhaustion IS infra
		// regardless of posture — the leg's reasoning machinery failed
		// before it could judge, so its verdict is not direction evidence.
		if !rr.Infra {
			t.Error("Infra = false on loop exhaustion — a burned-out tool loop is not a judgment")
		}
		if got := len(rr.ToolCalls); got != 39 {
			t.Errorf("tool_calls = %d, want 39 (every executed round journaled; the refused 40th round executed nothing)", got)
		}
		// D9-C: the death row journals the call names/args (linear
		// progress vs degenerate re-reads) and the pre-truncation spend.
		if rr.ToolRoundsUsed != 39 {
			t.Errorf("tool_rounds_used = %d, want 39 (the pre-capToolAudits count rides every grounded row)", rr.ToolRoundsUsed)
		}
		if rr.ToolCalls[0].Name != "read_file" || !strings.Contains(rr.ToolCalls[0].Input, "sub/a.go") {
			t.Errorf("tool_calls[0] = %+v, want the call's name/args journaled on the death path", rr.ToolCalls[0])
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
	t.Parallel()
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

// --- D9-C: the parameterized round cap -------------------------------

// TestGroundedToolRoundsResolution pins the D9-C resolution order: the
// default ships ACTIVE (40 — a default of 16 ships the #118/#120 incident
// unfixed), the env/prefs line is the escape hatch (never the activation
// mechanism), and an explicit Server field (the test seam) wins.
// Above-ceiling values read as the ceiling; garbage reads as the default.
func TestGroundedToolRoundsResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ambient prefs.md
	if got := (&Server{}).groundedToolRoundsCap(); got != 40 {
		t.Errorf("default = %d, want 40 (the D9-C fix ships ACTIVE — a default of 16 ships the incident unfixed)", got)
	}

	t.Run("env hatch round-trips", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv(groundedRoundsEnv, "12")
		if got := (&Server{}).groundedToolRoundsCap(); got != 12 {
			t.Errorf("env-set = %d, want 12", got)
		}
		t.Setenv(groundedRoundsEnv, "") // cleared reads as absent
		if got := (&Server{}).groundedToolRoundsCap(); got != 40 {
			t.Errorf("env-cleared = %d, want the 40 default back", got)
		}
	})

	t.Run("prefs hatch", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv(groundedRoundsEnv, "")
		writePrefs(t, home, "grounded_tool_rounds: 18\n")
		if got := (&Server{}).groundedToolRoundsCap(); got != 18 {
			t.Errorf("prefs-set = %d, want 18", got)
		}
	})

	t.Run("explicit field wins over the hatch", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv(groundedRoundsEnv, "12")
		writePrefs(t, home, "grounded_tool_rounds: 18\n")
		if got := (&Server{groundedToolRounds: 16}).groundedToolRoundsCap(); got != 16 {
			t.Errorf("field = %d, want 16 (the explicit field wins over env and prefs)", got)
		}
	})

	t.Run("above-ceiling and garbage read sanely", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv(groundedRoundsEnv, "99")
		if got := (&Server{}).groundedToolRoundsCap(); got != 40 {
			t.Errorf("above-ceiling env = %d, want the 40 ceiling (QueryWithTools clamps there regardless)", got)
		}
		t.Setenv(groundedRoundsEnv, "many")
		if got := (&Server{}).groundedToolRoundsCap(); got != 40 {
			t.Errorf("garbage env = %d, want the 40 default", got)
		}
	})
}

// TestGroundedLegDeadlineInterlock pins the D9-C wall-clock interlock
// (DSF option b): a round cap above the 16-round baseline scales the
// leg's outer deadline by rounds/16 — a legitimate long chain dies a
// typed round-capacity death, never a misleading wall-clock timeout.
func TestGroundedLegDeadlineInterlock(t *testing.T) {
	t.Parallel()
	base := 100 * time.Second
	for _, tc := range []struct {
		rounds int
		want   time.Duration
	}{
		{8, base},               // below the baseline: unchanged
		{16, base},              // at the baseline: unchanged
		{32, 200 * time.Second}, // ×32/16
		{40, 250 * time.Second}, // the D9-C default: ×40/16
	} {
		if got := groundedLegDeadline(base, tc.rounds); got != tc.want {
			t.Errorf("groundedLegDeadline(base, %d) = %v, want %v", tc.rounds, got, tc.want)
		}
	}
}

// TestGroundedRoundCapIncident is the D9-C incident-shaped regression
// (#118 ×2, #120 ×3): K3's grounded leg runs a legitimate ~25-round
// glob→grep→read chain — it COMPLETES and issues a verdict under the
// default-40 cap, and DIES a fail-hard round-cap death under the old 16
// (the behavior being fixed).
func TestGroundedRoundCapIncident(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	chain := func(post int) ([]map[string]interface{}, string) {
		if post <= 25 {
			// The legitimate chain: one scoped read per round for 25
			// rounds (K3 reads the import neighborhood serially), then
			// the verdict.
			return []map[string]interface{}{readUse(fmt.Sprintf("call-%d", post), "sub/a.go")}, "tool_use"
		}
		return textBlock("ACCEPT\n\nchain verified."), "end_turn"
	}

	t.Run("completes and issues a verdict under the default 40", func(t *testing.T) {
		t.Setenv(groundedRoundsEnv, "")
		root := t.TempDir()
		writeGroundedFile(t, root, "sub/a.go", "package sub\n\nconst A = 1\n")
		bodies := groundedStub(t, chain)

		s := &Server{projectRoot: root}
		plan := groundedPlanFor(t, s, root, []string{"sub/a.go"})
		rr := s.reviewWithModelGrounded(context.Background(), reviewModel{model: "rmG", provider: "test"}, "review this diff", plan)

		if rr.Verdict != "accept" {
			t.Errorf("verdict = %q (%s), want accept — a legitimate >16-round chain must now complete", rr.Verdict, rr.Comments)
		}
		if rr.Infra {
			t.Error("Infra = true on a chain the 40-round budget serves")
		}
		if rr.ToolRoundsUsed != 25 || len(rr.ToolCalls) != 25 {
			t.Errorf("tool_rounds_used = %d, len(tool_calls) = %d, want 25/25", rr.ToolRoundsUsed, len(rr.ToolCalls))
		}
		if got := len(bodies()); got != 26 {
			t.Errorf("posts = %d, want 26 (25 tool rounds + the verdict)", got)
		}
		raw, _ := json.Marshal(rr)
		if !strings.Contains(string(raw), `"tool_rounds_used":25`) {
			t.Errorf("journaled row missing tool_rounds_used:25 — got %s", raw)
		}
	})

	t.Run("dies a fail-hard round-cap death under the old 16", func(t *testing.T) {
		t.Setenv(groundedRoundsEnv, "")
		root := t.TempDir()
		writeGroundedFile(t, root, "sub/a.go", "package sub\n\nconst A = 1\n")
		groundedStub(t, chain)

		s := &Server{projectRoot: root, groundedToolRounds: 16} // the pre-D9-C budget, via the field seam
		plan := groundedPlanFor(t, s, root, []string{"sub/a.go"})
		rr := s.reviewWithModelGrounded(context.Background(), reviewModel{model: "rmG", provider: "test"}, "review this diff", plan)

		if rr.Verdict != "needs_fixes" || !rr.Infra {
			t.Errorf("leg = %q infra=%v, want needs_fixes + infra (the 16-round death the incident shipped)", rr.Verdict, rr.Infra)
		}
		if !strings.Contains(rr.Comments, "round-cap death") || !strings.Contains(rr.Comments, "exceeded 16 rounds") {
			t.Errorf("comments = %q, want the fail-hard marker naming the 16-round wall", rr.Comments)
		}
		if rr.ToolRoundsUsed != 15 || len(rr.ToolCalls) != 15 {
			t.Errorf("tool_rounds_used = %d, len = %d, want 15/15 (cut mid-chain, names/args journaled)", rr.ToolRoundsUsed, len(rr.ToolCalls))
		}
	})
}

// TestGroundedByteBudgetGraceful pins lock item 6 at its literal edge: a
// loop running to the very END of the 40-round budget — 39 executed
// rounds, the verdict on the 40th and last admitted post — that trips
// the 256KB byte budget mid-loop degrades GRACEFULLY: refusals steer the
// model, the verdict still ships, and the row stays visually distinct
// (tool_budget_exhausted + verdict ≠ round-cap death + infra). Pre-fix
// this exact shape died at round 16 as an infra error. (Re-review
// strengthening: the v2 submission exercised only 17 rounds; the lock
// names a 40-round loop — this runs the full budget.)
func TestGroundedByteBudgetGraceful(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(groundedRoundsEnv, "")
	// 400 lines × 100 chars ≈ 42.3KB per read under the 64KB per-read
	// cap: 6 reads fit, the 7th crosses 256KB (served, then the budget
	// trips), every later call is refused "budget exhausted — issue
	// your verdict now".
	big := strings.Repeat(strings.Repeat("x", 100)+"\n", 400)

	root := t.TempDir()
	writeGroundedFile(t, root, "sub/a.go", "package sub\n\nconst A = 1\n")
	writeGroundedFile(t, root, "sub/big.txt", big)
	bodies := groundedStub(t, func(post int) ([]map[string]interface{}, string) {
		if post <= 39 {
			return []map[string]interface{}{readUse(fmt.Sprintf("call-%d", post), "sub/big.txt")}, "tool_use"
		}
		return textBlock("ACCEPT\n\nissuing the verdict with what I have."), "end_turn"
	})

	s := &Server{projectRoot: root}
	plan := groundedPlanFor(t, s, root, []string{"sub/a.go"})
	rr := s.reviewWithModelGrounded(context.Background(), reviewModel{model: "rmG", provider: "test"}, "review this diff", plan)

	if rr.Verdict != "accept" {
		t.Errorf("verdict = %q (%s), want accept — byte exhaustion is fail-soft and must not cost the verdict", rr.Verdict, rr.Comments)
	}
	if rr.Infra {
		t.Error("Infra = true — byte-budget exhaustion is NOT an infra death (that class belongs to the round cap)")
	}
	if !rr.ToolBudgetExhausted {
		t.Error("tool_budget_exhausted = false, want true (the full-budget loop DID trip the 256KB budget)")
	}
	if rr.ReadBytes <= groundedTotalBytes {
		t.Errorf("read_bytes = %d, want > %d (the crossing read trips the budget mid-loop)", rr.ReadBytes, groundedTotalBytes)
	}
	if strings.Contains(rr.Comments, "round-cap death") || strings.Contains(rr.Comments, "exceeded") {
		t.Errorf("comments = %q — fail-soft byte exhaustion must stay visually distinct from a round-cap death", rr.Comments)
	}
	// The maximal loop the 40-round cap admits: 39 executed rounds, the
	// verdict on the 40th post — one more tool request would die.
	if rr.ToolRoundsUsed != 39 || len(rr.ToolCalls) != 39 {
		t.Errorf("tool_rounds_used = %d, len(tool_calls) = %d, want 39/39", rr.ToolRoundsUsed, len(rr.ToolCalls))
	}
	if rr.ToolCallsTruncated {
		t.Error("tool_calls_truncated = true at 39 calls — the 96-entry journal cap must hold a full 40-round loop")
	}
	refusals := 0
	for _, c := range rr.ToolCalls {
		if c.Error != "" {
			refusals++
		}
	}
	if refusals != 32 {
		t.Errorf("budget refusals = %d, want 32 (calls 8–39 refused after the served crossing read)", refusals)
	}
	if !strings.Contains(rr.ToolCalls[7].Error, "budget exhausted") {
		t.Errorf("tool_calls[7].Error = %q, want the budget-exhausted refusal on the first refused call", rr.ToolCalls[7].Error)
	}
	if got := len(bodies()); got != 40 {
		t.Errorf("posts = %d, want 40 (39 tool rounds + the verdict — the full 40-round budget)", got)
	}
}

// TestAuditLegGroundedRoundsUsed pins the D9-C receipts on the audit
// side: the grounded audit leg consumes the plan's resolved round cap
// and journals tool_rounds_used (pre-truncation) on every row.
func TestAuditLegGroundedRoundsUsed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(groundedRoundsEnv, "")
	root := t.TempDir()
	writeGroundedFile(t, root, "sub/a.go", "package sub\n\nconst A = 1\n")
	groundedStub(t, func(post int) ([]map[string]interface{}, string) {
		if post <= 3 {
			return []map[string]interface{}{readUse(fmt.Sprintf("call-%d", post), "sub/a.go")}, "tool_use"
		}
		return textBlock("```findings\n```"), "end_turn"
	})

	s := &Server{projectRoot: root}
	plan := groundedPlanFor(t, s, root, []string{"sub/a.go"})
	if plan.rounds != 40 {
		t.Errorf("plan.rounds = %d, want the resolved D9-C default 40", plan.rounds)
	}
	client := moa.NewClient(os.Getenv("MOA_BASE_URL"), "test-key")
	res := auditLegGrounded(context.Background(), client, reviewModel{model: "rmG", provider: "test"}, auditSystemGrounded(), "audit this diff", plan)

	if res.Verdict != "complete" {
		t.Errorf("verdict = %q, want complete (the empty findings block parses)", res.Verdict)
	}
	if res.ToolRoundsUsed != 3 || len(res.ToolCalls) != 3 {
		t.Errorf("tool_rounds_used = %d, len(tool_calls) = %d, want 3/3 (pre-truncation count on every grounded row)", res.ToolRoundsUsed, len(res.ToolCalls))
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), `"tool_rounds_used":3`) {
		t.Errorf("journaled audit-leg row missing tool_rounds_used:3 — got %s", raw)
	}
}
