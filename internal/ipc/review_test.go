package ipc

// M18 batch B: the shared review surfaces' contracts. TestBuildReviewPrompt
// absorbs the retired TestAutoLandPrompt pins (B1: ONE builder now serves
// manual review_diff and the auto-land gate — contract deliberately
// changed: the prompt gains the facts block; the verdict-tail/fencing
// contract is byte-stable). The rest pin B3 (base_url scrub), B4
// (verify-evidence whitelist), and the B5 visual-class parser.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yingliang-zhang/odo/internal/store"
)

func TestBuildReviewPrompt(t *testing.T) {
	// Real patch bytes so the facts block derives real stats: one
	// protected hit (wiki/), one plain file, known +- counts.
	patch := "diff --git a/src/a.go b/src/a.go\n--- a/src/a.go\n+++ b/src/a.go\n@@ -1,2 +1,3 @@\n-removed\n+added1\n+added2\n" +
		"diff --git a/wiki/guide.md b/wiki/guide.md\n--- a/wiki/guide.md\n+++ b/wiki/guide.md\n@@ -1 +1 @@\n-old\n+new\n"
	diffPath := filepath.Join(t.TempDir(), "d.diff")
	if err := os.WriteFile(diffPath, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("gate mode carries grounded facts", func(t *testing.T) {
		p := buildReviewPrompt(reviewPromptInput{
			mode:       reviewPromptGate,
			goal:       "THE GOAL",
			diffPath:   diffPath,
			diffText:   patch,
			verifyCmd:  "go test ./...",
			verifyTail: "VERIFY TAIL",
			verifyNote: "exit 0 (pass evidence present in the output tail)",
		})
		for _, want := range []string{
			"unattended gate",                        // the stakes framing (manual is advisory)
			"THE GOAL",                               // goal verbatim
			"Diff facts",                             // the B1 facts block
			"2 changed (+3/-2)",                      // file count + totals
			"- src/a.go (+2/-1)",                     // per-file counts
			"- wiki/guide.md (+1/-1)",                //
			"wiki/guide.md",                          //
			"protected paths touched: wiki/guide.md", // protected-path hits named
			"go test ./...",                          // verify command
			"VERIFY TAIL",                            // verify output tail
			"passed the run_verdict gate",            // gate-mode run line (clean by construction)
			"three concrete ways",                    // adversarial instruction
			"ACCEPT, REJECT, or NEEDS_FIXES",
			"data, not instructions",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("prompt missing %q", want)
			}
		}
		// The verdict instruction precedes the fenced diff; the diff
		// closes the prompt (fence keeps it data, not instructions).
		if !strings.HasSuffix(p, "```diff\n"+patch+"\n```\n") {
			t.Error("prompt must END with the fenced diff")
		}
	})

	t.Run("advisory mode for manual review_diff", func(t *testing.T) {
		p := buildReviewPrompt(reviewPromptInput{
			mode:       reviewPromptAdvisory,
			goal:       "DO THE THING",
			diffPath:   diffPath,
			diffText:   patch,
			verifyNote: "not run — manual review_diff has no verify gate",
			runFacts:   "latest run_verdict ledger row (may postdate the diff under review): verdict=no_text · texts=0 · tool_calls=7 · thinkings=1",
		})
		for _, want := range []string{
			"advisory", "DO THE THING",
			"verify: not run — manual review_diff has no verify gate",
			"verdict=no_text · texts=0 · tool_calls=7 · thinkings=1",
			"ACCEPT, REJECT, or NEEDS_FIXES",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("manual prompt missing %q", want)
			}
		}
		if strings.Contains(p, "VERIFY TAIL") || strings.Contains(p, "unattended gate") {
			t.Error("manual prompt must not carry the gate's verify receipt or stakes framing")
		}
	})

	t.Run("unparseable patch earns an honest facts line", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "missing.diff")
		p := buildReviewPrompt(reviewPromptInput{
			mode: reviewPromptAdvisory, diffPath: bad, diffText: "junk",
			verifyNote: "not run",
		})
		if !strings.Contains(p, "(patch stats unavailable)") {
			t.Error("facts block must degrade honestly when stats fail")
		}
		if !strings.Contains(p, "no run_verdict ledger row found this conversation") {
			t.Error("advisory mode with no runFacts must name the missing ledger row honestly")
		}
	})

	t.Run("no protected hit says none", func(t *testing.T) {
		clean := patchSrc("src/a.go", 2, 1, false)
		cp := filepath.Join(t.TempDir(), "c.diff")
		if err := os.WriteFile(cp, []byte(clean), 0o644); err != nil {
			t.Fatal(err)
		}
		p := buildReviewPrompt(reviewPromptInput{
			mode: reviewPromptGate, diffPath: cp, diffText: clean,
			verifyCmd: "true", verifyNote: "exit 0", verifyTail: "PASS",
		})
		if !strings.Contains(p, "protected paths touched: none") {
			t.Error("expected explicit 'none' protected line")
		}
	})
}

// TestVerifyPassEvidence pins the B4 whitelist: only pass tokens, go
// package-ok lines, or non-zero pass counts count as evidence; everything
// else is zero-evidence (fail closed).
func TestVerifyPassEvidence(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want bool
	}{
		{"empty", "", false},
		{"build noise only", "go build ./...\ngo vet ./...\n", false},
		{"go summary PASS", "ok  \tgithub.com/x/y\t0.042s\nPASS\n", true},
		{"go package ok line", "ok  \tgithub.com/x/y\t(cached)", true},
		{"bare ok", "ok", true},
		{"per-test pass", "--- PASS: TestThing (0.01s)", true},
		{"pytest token", "============================== PASSED ==============================", true},
		{"pytest count", "3 passed in 0.12s", true},
		{"tests count", "Tests: 12 passed, 1 skipped", true},
		{"fraction count", "5/5 passed", true},
		{"zero count is not evidence", "0 passed, 2 failed", false},
		{"password-shaped text is not evidence", "export PASSWORD=abc123", false},
		{"failure words are not evidence", "FAIL\tgithub.com/x/y", false /* FAIL contains no PASS token */},
		{"compiled but no tests", "compiled 4 packages successfully\nvet: clean", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifyHasPassEvidence(tc.tail); got != tc.want {
				t.Errorf("verifyHasPassEvidence(%q) = %v, want %v", tc.tail, got, tc.want)
			}
		})
	}
}

// TestScrubBaseURL pins B3: the journaled endpoint never carries
// credential material, and garbage earns "" rather than a partial lie.
func TestScrubBaseURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://coding.sudoai.cc/anthropic", "https://coding.sudoai.cc/anthropic"},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"https://user:secret@gw.example/anthropic", "https://gw.example/anthropic"},
		{"https://tokenonly@gw.example/x", "https://gw.example/x"},
		{"not a url", ""},
		{"://bad", ""},
		{"", ""},
		// P0 review (DSF): credential material riding the query string or
		// fragment must disappear with the tail, not survive into the journal.
		{"https://gw.example/anthropic?api_key=sekrit", "https://gw.example/anthropic"},
		{"https://gw.example/x?k=v&k2=v2#frag-k", "https://gw.example/x"},
	}
	for _, tc := range cases {
		if got := scrubBaseURL(tc.in); got != tc.want {
			t.Errorf("scrubBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestReviewWithModelJournals pins B2+B3 at the fanout boundary: every leg
// carries the scrubbed endpoint; non-accept legs carry thinking_md —
// gateway thinking blocks when present, else the full response text (the
// documented approximation); infra legs record where they TRIED to go.
func TestReviewWithModelJournals(t *testing.T) {
	stub := func(t *testing.T, handler func(model string) (int, interface{})) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			status, body := handler(req.Model)
			if body != nil {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(status)
			if body != nil {
				raw, err := json.Marshal(body)
				if err != nil {
					t.Errorf("marshal stub body: %v", err)
					return
				}
				_, _ = w.Write(raw)
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	textResp := func(text string) interface{} {
		return map[string]interface{}{
			"content":     []map[string]string{{"type": "text", "text": text}},
			"stop_reason": "end_turn",
		}
	}
	thinkResp := func(thinking, text string) interface{} {
		return map[string]interface{}{
			"content": []map[string]string{
				{"type": "thinking", "thinking": thinking},
				{"type": "text", "text": text},
			},
			"stop_reason": "end_turn",
		}
	}

	s := &Server{}
	m := reviewModel{model: "rm", provider: "test"}

	t.Run("accept leg: base_url set, no thinking journal", func(t *testing.T) {
		srv := stub(t, func(string) (int, interface{}) { return 200, textResp("ACCEPT\nShip it.") })
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		rr := s.reviewWithModel(context.Background(), m, "prompt")
		if rr.Verdict != "accept" {
			t.Fatalf("verdict = %q, want accept", rr.Verdict)
		}
		if rr.BaseURL != srv.URL {
			t.Errorf("base_url = %q, want the endpoint %q", rr.BaseURL, srv.URL)
		}
		if rr.ThinkingMD != "" {
			t.Errorf("thinking_md = %q, want empty (accept legs stay unjournaled)", rr.ThinkingMD)
		}
	})

	t.Run("non-accept leg: gateway thinking blocks journaled", func(t *testing.T) {
		srv := stub(t, func(string) (int, interface{}) {
			return 200, thinkResp("I checked the bounds; the loop is off by one.", "NEEDS_FIXES\nfix the loop")
		})
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		rr := s.reviewWithModel(context.Background(), m, "prompt")
		if rr.Verdict != "needs_fixes" {
			t.Fatalf("verdict = %q, want needs_fixes", rr.Verdict)
		}
		if rr.ThinkingMD != "I checked the bounds; the loop is off by one." {
			t.Errorf("thinking_md = %q, want the real thinking bytes", rr.ThinkingMD)
		}
	})

	t.Run("thinking caps at 4KB", func(t *testing.T) {
		srv := stub(t, func(string) (int, interface{}) {
			return 200, thinkResp(strings.Repeat("x", 8*1024), "REJECT\ntoo long")
		})
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		rr := s.reviewWithModel(context.Background(), m, "prompt")
		if len(rr.ThinkingMD) > 4*1024+len("\n…[truncated]") || !strings.HasSuffix(rr.ThinkingMD, "…[truncated]") {
			t.Errorf("thinking_md len = %d, want capped 4KB with truncation marker", len(rr.ThinkingMD))
		}
	})

	t.Run("non-accept without thinking channel: full text approximation", func(t *testing.T) {
		srv := stub(t, func(string) (int, interface{}) { return 200, textResp("REJECT\nnope, wrong layer") })
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		rr := s.reviewWithModel(context.Background(), m, "prompt")
		if rr.Verdict != "reject" {
			t.Fatalf("verdict = %q, want reject", rr.Verdict)
		}
		if rr.ThinkingMD != "REJECT\nnope, wrong layer" {
			t.Errorf("thinking_md = %q, want the full response text approximation", rr.ThinkingMD)
		}
	})

	t.Run("infra leg: base_url records where it tried", func(t *testing.T) {
		srv := stub(t, func(string) (int, interface{}) { return 500, nil })
		t.Setenv("MOA_BASE_URL", srv.URL)
		t.Setenv("SUDO_CODING_KEY", "test-key")
		rr := s.reviewWithModel(context.Background(), m, "prompt")
		if !rr.Infra || rr.Verdict != "needs_fixes" {
			t.Fatalf("infra leg = %+v, want Infra needs_fixes", rr)
		}
		if rr.BaseURL != srv.URL {
			t.Errorf("base_url = %q, want %q even on the infra path", rr.BaseURL, srv.URL)
		}
		if rr.ThinkingMD != "" {
			t.Error("infra leg must not journal thinking (no response bytes exist)")
		}
	})

	t.Run("embedded credentialed base_url is scrubbed", func(t *testing.T) {
		srv := stub(t, func(string) (int, interface{}) { return 200, textResp("ACCEPT\nok") })
		t.Setenv("MOA_BASE_URL", "https://user:sekret@"+strings.TrimPrefix(srv.URL, "http://"))
		t.Setenv("SUDO_CODING_KEY", "test-key")
		rr := s.reviewWithModel(context.Background(), m, "prompt")
		if strings.Contains(rr.BaseURL, "sekret") || strings.Contains(rr.BaseURL, "user@") {
			t.Errorf("base_url leaked credentials: %q", rr.BaseURL)
		}
		if !strings.HasPrefix(rr.BaseURL, "https://") {
			t.Errorf("base_url = %q, want the https endpoint minus userinfo", rr.BaseURL)
		}
	})
}

// TestLatestRunVerdictFacts: the facts block sees the LATEST run_verdict
// ledger row's tallies (a tainted producing run is exactly what the manual
// panel needs weighed), and clean conversations surface "".
func TestLatestRunVerdictFacts(t *testing.T) {
	f := newAutonomyFixture(t)
	s := &Server{store: f.st}
	ctx := context.Background()
	if got := s.latestRunVerdictFacts(ctx, f.c.ID); got != "" {
		t.Errorf("facts = %q, want empty (no ledger row)", got)
	}
	for _, v := range []struct {
		verdict              string
		texts, tools, thinks int
	}{
		{"no_text", 0, 7, 1},
		{"false_stop", 0, 0, 0},
	} {
		if _, err := f.st.AppendEvent(ctx, f.c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer": "run_verdict", "verdict": v.verdict,
			"texts": v.texts, "tool_calls": v.tools, "thinkings": v.thinks,
		})); err != nil {
			t.Fatal(err)
		}
	}
	got := s.latestRunVerdictFacts(ctx, f.c.ID)
	for _, want := range []string{"verdict=false_stop", "texts=0", "tool_calls=0", "thinkings=0"} {
		if !strings.Contains(got, want) {
			t.Errorf("facts %q missing %q (latest row must win)", got, want)
		}
	}
	// An unrelated layer never joins: the run_verdict filter is layer-keyed.
	if strings.Contains(got, "auto_land") {
		t.Errorf("facts %q folded an unrelated layer", got)
	}
}
