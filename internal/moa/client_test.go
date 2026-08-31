package moa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestQuerySuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s, want /v1/messages", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %s, want test-key", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %s", r.Header.Get("anthropic-version"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"content": [
				{"type": "thinking", "text": "reasoning here"},
				{"type": "text", "text": "ACCEPT\n\nLooks good."}
			],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 100, "output_tokens": 50}
		}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	res, err := c.Query(context.Background(), "test-model", "You are a reviewer.", "Review this diff.")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Text != "ACCEPT\n\nLooks good." {
		t.Errorf("text = %q, want ACCEPT...", res.Text)
	}
	// R-W1 usage ledger: input tokens, wall time, derived rate, stop reason.
	if res.InputTokens != 100 || res.OutputTokens != 50 {
		t.Errorf("usage = %d in / %d out, want 100/50", res.InputTokens, res.OutputTokens)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", res.StopReason)
	}
	if res.WallSeconds <= 0 {
		t.Errorf("wall_seconds = %v, want > 0", res.WallSeconds)
	}
	if res.TokPerSec <= 0 {
		t.Errorf("tok_per_sec = %v, want > 0 (derived from output/wall)", res.TokPerSec)
	}
}

func TestQueryThinkingBlocksDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"content": [
				{"type": "thinking", "text": "internal reasoning"},
				{"type": "text", "text": "Part 1."},
				{"type": "thinking", "text": "more thinking"},
				{"type": "text", "text": "Part 2."}
			],
			"stop_reason": "end_turn"
		}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	res, err := c.Query(context.Background(), "test", "", "test")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Text != "Part 1.\n\nPart 2." {
		t.Errorf("text = %q, want 'Part 1.\\n\\nPart 2.'", res.Text)
	}
}

func TestQueryError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid key"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad-key")
	_, err := c.Query(context.Background(), "test", "", "test")
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	// R-W1: a typed client_error, exactly one request — 4xx NEVER retries
	// (a bad key is fail-loud signal, not a flaky network).
	var mErr *Error
	if !errors.As(err, &mErr) {
		t.Fatalf("err = %v (%T), want a typed *Error", err, err)
	}
	if mErr.Class != ClassClientError || mErr.Status != 401 {
		t.Errorf("class/status = %q/%d, want client_error/401", mErr.Class, mErr.Status)
	}
	if calls != 1 {
		t.Errorf("requests = %d, want 1 — client errors never retry", calls)
	}
}

func TestQueryEmptyKey(t *testing.T) {
	c := NewClient("https://example.com", "")
	_, err := c.Query(context.Background(), "test", "", "test")
	if err == nil {
		t.Fatal("expected error for empty key, got nil")
	}
}

func TestQueryDefaultModel(t *testing.T) {
	var receivedModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req messageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			receivedModel = req.Model
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	_, _ = c.Query(context.Background(), "", "", "test")
	if receivedModel != defaultModel {
		t.Errorf("model = %q, want %q", receivedModel, defaultModel)
	}
}

// The tool loop must echo assistant turns byte-identically: thinking blocks
// carry fields contentBlock doesn't model ("thinking", "signature") and the
// gateway replays-validates them — re-marshaling the cooked struct used to
// 400 with "thinking.thinking: Field required" (kimi-k3).
func TestQueryWithToolsReplaysAssistantVerbatim(t *testing.T) {
	const round1Content = `[{"type":"thinking","thinking":"deep reasoning","signature":"sig-abc123"},{"type":"text","text":"Let me check."},{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"a.go"}}]`
	var echoes []json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if len(req.Messages) == 1 {
			fmt.Fprintf(w, `{"content":%s,"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`, round1Content)
			return
		}
		echoes = append(echoes, req.Messages[1].Content)
		w.Write([]byte(`{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	tools := []Tool{{Name: "read_file", Description: "read", InputSchema: map[string]interface{}{"type": "object"}}}
	exec := func(ctx context.Context, call ToolCall) (string, error) {
		if call.Name != "read_file" || call.ID != "toolu_1" {
			t.Errorf("unexpected tool call: %+v", call)
		}
		return "file body", nil
	}
	res, audits, err := c.QueryWithTools(context.Background(), "test", "", "prompt", tools, exec, 0)
	if err != nil {
		t.Fatalf("QueryWithTools: %v", err)
	}
	if res.Text != "Done." {
		t.Errorf("text = %q, want Done.", res.Text)
	}
	if len(audits) != 1 || audits[0].Name != "read_file" || audits[0].ResultBytes != len("file body") {
		t.Errorf("audits = %+v, want one read_file call", audits)
	}
	if len(echoes) != 1 {
		t.Fatalf("assistant echoes = %d, want 1", len(echoes))
	}
	var want, got interface{}
	if err := json.Unmarshal([]byte(round1Content), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(echoes[0], &got); err != nil {
		t.Fatalf("assistant echo is not valid JSON: %v (%s)", err, echoes[0])
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("assistant echo = %s, want verbatim %s", echoes[0], round1Content)
	}
}

// recordedRequest captures the max_tokens each gateway call asked for.
type recordedRequest struct {
	MaxTokens int `json:"max_tokens"`
}

// TestQueryPerModelInitialBudget pins the modelspec wiring: thinking models
// start at 32768, glm at 16384, unknown models at the fallback 16384.
func TestQueryPerModelInitialBudget(t *testing.T) {
	var got []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req recordedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			got = append(got, req.MaxTokens)
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	for _, model := range []string{"t9s/kimi-k3", "t9s/deepseek-v4-flash", "glm-5.2", "acme/pi-9"} {
		if _, err := c.Query(context.Background(), model, "", "q"); err != nil {
			t.Fatalf("%s: %v", model, err)
		}
	}
	want := []int{32768, 32768, 16384, 16384}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("initial budgets = %v, want %v", got, want)
	}
}

// TestQueryEscalatesOnMaxTokens: a max_tokens stop re-issues the whole turn
// at double the budget and the bump lands in the escalation ledger.
func TestQueryEscalatesOnMaxTokens(t *testing.T) {
	var budgets []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req recordedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			budgets = append(budgets, req.MaxTokens)
		}
		if len(budgets) == 1 {
			w.Write([]byte(`{"content":[{"type":"text","text":"half"}],"stop_reason":"max_tokens","usage":{"output_tokens":16384}}`))
			return
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"complete"}],"stop_reason":"end_turn","usage":{"output_tokens":5000}}`))
	}))
	defer srv.Close()

	// Unknown model: 16384 initial, 32768 hard cap.
	c := NewClient(srv.URL, "test-key")
	res, err := c.Query(context.Background(), "acme/pi-9", "", "q")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Text != "complete" || res.Truncated {
		t.Errorf("res = %+v, want complete untruncated", res)
	}
	if !reflect.DeepEqual(budgets, []int{16384, 32768}) {
		t.Errorf("request budgets = %v, want [16384 32768]", budgets)
	}
	wantEsc := []Escalation{{From: 16384, To: 32768, OutputTokens: 16384}}
	if !reflect.DeepEqual(res.Escalations, wantEsc) {
		t.Errorf("escalations = %+v, want %+v", res.Escalations, wantEsc)
	}
	if res.Budget != 32768 || res.OutputTokens != 5000 {
		t.Errorf("budget/output = %d/%d, want 32768/5000", res.Budget, res.OutputTokens)
	}
}

// TestQueryHardCapReturnsFlaggedPartial: truncation surviving the hard cap
// returns the partial text flagged — display content must not black-screen.
func TestQueryHardCapReturnsFlaggedPartial(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"content":[{"type":"text","text":"partial answer"}],"stop_reason":"max_tokens"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	res, err := c.Query(context.Background(), "acme/pi-9", "", "q")
	if err != nil {
		t.Fatalf("Query: %v (partial must not error)", err)
	}
	if !res.Truncated || res.Text != "partial answer" || res.Budget != 32768 {
		t.Errorf("res = %+v, want flagged partial at cap 32768", res)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (one escalation to the cap)", calls)
	}
}

// TestQueryHardCapEmptyPartialErrors: a thinking model can burn the entire
// budget on reasoning and ship zero text — there is nothing displayable, so
// this one stays an error.
func TestQueryHardCapEmptyPartialErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":[{"type":"thinking","thinking":"…"}],"stop_reason":"max_tokens"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	_, err := c.Query(context.Background(), "acme/pi-9", "", "q")
	if err == nil || !strings.Contains(err.Error(), "no visible text") {
		t.Fatalf("err = %v, want no-visible-text truncation error", err)
	}
}

// TestQueryStopReasonClasses: refusal is terminal; pause_turn and unknown
// reasons are treated as end_turn instead of silently doing something else.
func TestQueryStopReasonClasses(t *testing.T) {
	terminal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":[{"type":"text","text":"no"}],"stop_reason":"refusal"}`))
	}))
	defer terminal.Close()
	if _, err := NewClient(terminal.URL, "test-key").Query(context.Background(), "m", "", "q"); err == nil ||
		!strings.Contains(err.Error(), "refusal") {
		t.Fatalf("refusal should error, got %v", err)
	}

	for _, reason := range []string{"pause_turn", "some_future_reason"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"content":[{"type":"text","text":"fine"}],"stop_reason":"` + reason + `"}`))
		}))
		res, err := NewClient(srv.URL, "test-key").Query(context.Background(), "m", "", "q")
		srv.Close()
		if err != nil || res.Text != "fine" {
			t.Errorf("stop_reason=%s: got (%q, %v), want (fine, nil)", reason, res.Text, err)
		}
	}
}

// TestQueryWithToolsTruncationReissuesWholeRound: a truncated tool turn is
// discarded — the half-written tool_use is never executed — and re-issued
// whole at double the budget without consuming a round.
func TestQueryWithToolsTruncationReissuesWholeRound(t *testing.T) {
	var budgets []int
	var execCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req recordedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			budgets = append(budgets, req.MaxTokens)
		}
		switch len(budgets) {
		case 1: // truncated mid-tool_use at the initial budget
			w.Write([]byte(`{"content":[{"type":"tool_use","id":"t1","name":"read_file","input":{"p":"x"}}],"stop_reason":"max_tokens"}`))
		case 2: // intact tool turn at the doubled budget
			w.Write([]byte(`{"content":[{"type":"tool_use","id":"t1","name":"read_file","input":{"p":"x"}}],"stop_reason":"tool_use"}`))
		default:
			w.Write([]byte(`{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn"}`))
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	tools := []Tool{{Name: "read_file", Description: "read", InputSchema: map[string]interface{}{"type": "object"}}}
	exec := func(ctx context.Context, call ToolCall) (string, error) {
		execCalls++
		return "file body", nil
	}
	res, audits, err := c.QueryWithTools(context.Background(), "acme/pi-9", "", "prompt", tools, exec, 0)
	if err != nil {
		t.Fatalf("QueryWithTools: %v", err)
	}
	if res.Text != "Done." {
		t.Errorf("text = %q, want Done.", res.Text)
	}
	// Budgets climb 16384 (truncated) → 32768 (intact tool turn) → 32768.
	if !reflect.DeepEqual(budgets, []int{16384, 32768, 32768}) {
		t.Errorf("request budgets = %v, want [16384 32768 32768]", budgets)
	}
	if execCalls != 1 || len(audits) != 1 {
		t.Errorf("exec = %d, audits = %d, want exactly 1 (truncated turn discarded)", execCalls, len(audits))
	}
	if len(res.Escalations) != 1 || res.Escalations[0].To != 32768 {
		t.Errorf("escalations = %+v, want one bump to 32768", res.Escalations)
	}
}

// TestQueryWithToolsHardCapSplit pins the block-type split once escalation
// is exhausted: truncated tool_use errors (never execute half JSON), a
// truncated final answer ships flagged (the /panel display contract).
func TestQueryWithToolsHardCapSplit(t *testing.T) {
	tools := []Tool{{Name: "read_file", Description: "read", InputSchema: map[string]interface{}{"type": "object"}}}
	exec := func(ctx context.Context, call ToolCall) (string, error) { return "ok", nil }

	t.Run("truncated tool_use errors at cap", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"content":[{"type":"tool_use","id":"t1","name":"read_file","input":{}}],"stop_reason":"max_tokens"}`))
		}))
		defer srv.Close()
		c := NewClient(srv.URL, "test-key")
		_, audits, err := c.QueryWithTools(context.Background(), "acme/pi-9", "", "p", tools, exec, 0)
		if err == nil || !strings.Contains(err.Error(), "half-written tool_use") {
			t.Fatalf("err = %v, want half-written tool_use error", err)
		}
		if len(audits) != 0 {
			t.Errorf("audits = %v, want none (truncated call never executed)", audits)
		}
	})

	t.Run("truncated final answer ships flagged", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"content":[{"type":"text","text":"half answer"}],"stop_reason":"max_tokens"}`))
		}))
		defer srv.Close()
		c := NewClient(srv.URL, "test-key")
		res, _, err := c.QueryWithTools(context.Background(), "acme/pi-9", "", "p", tools, exec, 0)
		if err != nil {
			t.Fatalf("QueryWithTools: %v", err)
		}
		if !res.Truncated || res.Text != "half answer" || res.Budget != 32768 {
			t.Errorf("res = %+v, want flagged partial at cap 32768", res)
		}
	})
}

// TestRequestTimeout pins the deadline math: base latency plus generation
// time at the conservative tok/s floor, scaling with the output budget.
func TestRequestTimeout(t *testing.T) {
	if got := requestTimeout(0); got != 900*time.Second {
		t.Errorf("requestTimeout(0) = %v, want 900s", got)
	}
	if got := requestTimeout(65536); got != 900*time.Second+546*time.Second {
		t.Errorf("requestTimeout(65536) = %v, want 1446s", got)
	}
}

// TestRequestTimeoutFloor pins the 900s base as a FLOOR (fix-INT): a
// max-effort review leg's server-side thinking fits inside the base at
// every budget — the budget/120 headroom only ever stacks on top.
func TestRequestTimeoutFloor(t *testing.T) {
	for _, budget := range []int{0, 4096, 16384, 32768, 65536} {
		if got := requestTimeout(budget); got < 900*time.Second {
			t.Errorf("requestTimeout(%d) = %v, want >= 900s floor", budget, got)
		}
	}
}

// D9-C intentionally RETIRED the default == ceiling invariant: the
// grounded review/audit legs opt into the 40-round ceiling while the
// design legs (design_moa.go) and the /panel consult (server.go) keep
// passing 0 → the unchanged 16 default. Pin the split: default(16) <
// ceiling(40), and maxRounds=0 still means 16 posts.
func TestQueryWithToolsDefaultRoundCap(t *testing.T) {
	if defaultToolRounds != 16 || maxToolRounds != 40 {
		t.Errorf("round budget = default %d / ceiling %d, want 16/40 (the D9-C budget split)", defaultToolRounds, maxToolRounds)
	}
	if defaultToolRounds >= maxToolRounds {
		t.Errorf("defaultToolRounds (%d) >= maxToolRounds (%d) — the D9-C budget split is default < ceiling", defaultToolRounds, maxToolRounds)
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"content":[{"type":"tool_use","id":"t1","name":"read_file","input":{}}],"stop_reason":"tool_use"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	tools := []Tool{{Name: "read_file", Description: "read", InputSchema: map[string]interface{}{"type": "object"}}}
	exec := func(ctx context.Context, call ToolCall) (string, error) { return "ok", nil }
	_, _, err := c.QueryWithTools(context.Background(), "test", "", "prompt", tools, exec, 0)
	if err == nil || !strings.Contains(err.Error(), "16 rounds") {
		t.Fatalf("err = %v, want 16-round cap error", err)
	}
	if calls != defaultToolRounds {
		t.Errorf("requests = %d, want %d (callers passing 0 — designLeg, the /panel consult — keep the 16 default)", calls, defaultToolRounds)
	}
}

// TestQueryWithToolsRoundCapClamp pins the ceiling arithmetic (D9-C): any
// caller-supplied cap resolves inside [default, ceiling] — 0→16 (the no-op
// path every ungrounded caller rides), 16→16, 40→40 (the grounded legs'
// new full budget), 50→40 (above-ceiling reads as the ceiling).
func TestQueryWithToolsRoundCapClamp(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 16}, {16, 16}, {40, 40}, {50, 40}} {
		t.Run(fmt.Sprintf("maxRounds=%d→%d", tc.in, tc.want), func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Write([]byte(`{"content":[{"type":"tool_use","id":"t1","name":"read_file","input":{}}],"stop_reason":"tool_use"}`))
			}))
			defer srv.Close()

			c := NewClient(srv.URL, "test-key")
			tools := []Tool{{Name: "read_file", Description: "read", InputSchema: map[string]interface{}{"type": "object"}}}
			exec := func(ctx context.Context, call ToolCall) (string, error) { return "ok", nil }
			_, _, err := c.QueryWithTools(context.Background(), "test", "", "prompt", tools, exec, tc.in)
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeded %d rounds", tc.want)) {
				t.Fatalf("err = %v, want the %d-round cap error", err, tc.want)
			}
			if calls != tc.want {
				t.Errorf("requests = %d, want %d", calls, tc.want)
			}
		})
	}
}

// R-W1 transport resilience pins (moa audit 2026-08-14 §2#1–3). The retry
// loop is the only transport guarantee an unattended distill cycle has:
// one transient 429/5xx must not silently kill a one-shot.

// recorderSleep stubs the backoff wait seam and captures the retry
// schedule, keeping these pins wall-time free.
func recorderSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	got := new([]time.Duration)
	old := sleepRetry
	sleepRetry = func(ctx context.Context, d time.Duration) error {
		*got = append(*got, d)
		return nil
	}
	t.Cleanup(func() { sleepRetry = old })
	return got
}

// TestRetryServerErrorThenSuccess: a 5xx is retried after jittered backoff
// (200ms ±50% on the first retry) and the retry can succeed.
func TestRetryServerErrorThenSuccess(t *testing.T) {
	sleeps := recorderSleep(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"error":"gateway boom"}`)
			return
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"recovered"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	res, err := c.Query(context.Background(), "m", "", "q")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.Text != "recovered" || res.InputTokens != 10 {
		t.Errorf("res = %+v, want recovered with 10 input tokens", res)
	}
	if calls != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", calls)
	}
	if len(*sleeps) != 1 {
		t.Fatalf("backoff sleeps = %v, want exactly one", *sleeps)
	}
	if got := (*sleeps)[0]; got < 100*time.Millisecond || got > 300*time.Millisecond {
		t.Errorf("first backoff = %v, want %v ±50%%", got, retryBaseDelay)
	}
}

// TestRetryRateLimitHonorsRetryAfter: the server's Retry-After hint (in
// delta-seconds) is honored verbatim instead of the exponential backoff.
func TestRetryRateLimitHonorsRetryAfter(t *testing.T) {
	sleeps := recorderSleep(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	if _, err := c.Query(context.Background(), "m", "", "q"); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if calls != 2 {
		t.Errorf("requests = %d, want 2", calls)
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != 2*time.Second {
		t.Errorf("sleeps = %v, want exactly [2s] — the hint overrides backoff", *sleeps)
	}
}

// TestRateLimitExhaustedTypedError: a persistent 429 exhausts the budget
// and surfaces a typed rate_limit error carrying the raw (uncapped)
// Retry-After hint; the waits themselves honor the 30s cap.
func TestRateLimitExhaustedTypedError(t *testing.T) {
	sleeps := recorderSleep(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	_, err := c.Query(context.Background(), "m", "", "q")
	var mErr *Error
	if !errors.As(err, &mErr) {
		t.Fatalf("err = %v (%T), want a typed *Error", err, err)
	}
	if mErr.Class != ClassRateLimit || mErr.Status != 429 {
		t.Errorf("class/status = %q/%d, want rate_limit/429", mErr.Class, mErr.Status)
	}
	if mErr.RetryAfter == nil || *mErr.RetryAfter != 60 {
		t.Errorf("RetryAfter = %v, want 60 (the raw hint reaches callers uncapped)", mErr.RetryAfter)
	}
	if calls != maxAttempts {
		t.Errorf("requests = %d, want %d (budget exhausted)", calls, maxAttempts)
	}
	want := []time.Duration{retryAfterCap * time.Second, retryAfterCap * time.Second}
	if !reflect.DeepEqual(*sleeps, want) {
		t.Errorf("sleeps = %v, want %v (hint capped at %ds)", *sleeps, want, retryAfterCap)
	}
}

// TestRetryNetworkErrorExhausted: a dead endpoint retries the full budget
// (2 waits = 3 attempts) with growing jittered backoff, then surfaces a
// typed network error with no HTTP status.
func TestRetryNetworkErrorExhausted(t *testing.T) {
	sleeps := recorderSleep(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // connection refused on every attempt

	c := NewClient(srv.URL, "test-key")
	_, err := c.Query(context.Background(), "m", "", "q")
	var mErr *Error
	if !errors.As(err, &mErr) {
		t.Fatalf("err = %v (%T), want a typed *Error", err, err)
	}
	if mErr.Class != ClassNetwork || mErr.Status != 0 {
		t.Errorf("class/status = %q/%d, want network/0", mErr.Class, mErr.Status)
	}
	if len(*sleeps) != maxAttempts-1 {
		t.Fatalf("sleeps = %v, want %d waits (%d attempts)", *sleeps, maxAttempts-1, maxAttempts)
	}
	for i, s := range *sleeps {
		base := retryBaseDelay << i
		if s < base/2 || s > base*3/2 {
			t.Errorf("sleep %d = %v, want %v ±50%%", i, s, base)
		}
	}
}

// TestRetryContextCancellation: cancellation never retries. The caller
// cancels while the client would be waiting out backoff after a
// retryable 500 — the request chain ends with the context error intact
// (errors.Is keeps working) and exactly one request on the wire.
func TestRetryContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		cancel() // caller cancels after the retryable failure lands
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	_, err := c.Query(ctx, "m", "", "q")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in the chain", err)
	}
	var mErr *Error
	if errors.As(err, &mErr) {
		t.Errorf("err = %v, want the context error, not a typed transport Error", err)
	}
	if calls != 1 {
		t.Errorf("requests = %d, want 1 — cancellation never retries", calls)
	}
}

// TestRequestReceiptWireExact pins R-W1.5: Result.RequestSHA16/RequestBytes
// attest the EXACT bytes the stub server observed on the wire — the single
// body of a one-shot, the shared body of a retry chain, and the FINAL body
// of an escalation re-issue or tool loop.
func TestRequestReceiptWireExact(t *testing.T) {
	okResp := `{"content":[{"type":"text","text":"ACCEPT"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`

	assertReceipt := func(t *testing.T, res Result, want []byte) {
		t.Helper()
		if res.RequestSHA16 != sha16(want) || res.RequestBytes != len(want) {
			t.Errorf("receipt = %q/%d bytes, want sha16+len of the final wire body (=%q/%d)",
				res.RequestSHA16, res.RequestBytes, sha16(want), len(want))
		}
	}

	t.Run("one-shot attests its single body", func(t *testing.T) {
		var bodies [][]byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, b)
			w.Write([]byte(okResp))
		}))
		defer srv.Close()
		res, err := NewClient(srv.URL, "test-key").Query(context.Background(), "test-model", "sys", "prompt")
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(bodies) != 1 {
			t.Fatalf("requests = %d, want 1", len(bodies))
		}
		assertReceipt(t, res, bodies[0])
	})

	t.Run("retry chain re-sends identical bytes — one receipt covers it", func(t *testing.T) {
		recorderSleep(t)
		var bodies [][]byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, b)
			if len(bodies) == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Write([]byte(okResp))
		}))
		defer srv.Close()
		res, err := NewClient(srv.URL, "test-key").Query(context.Background(), "test-model", "", "prompt")
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(bodies) != 2 {
			t.Fatalf("requests = %d, want 2 (one retry)", len(bodies))
		}
		if !bytes.Equal(bodies[0], bodies[1]) {
			t.Error("retry attempt carried different bytes — the receipt could not cover the chain")
		}
		assertReceipt(t, res, bodies[1])
	})

	t.Run("escalation re-issue attests the final body", func(t *testing.T) {
		var bodies [][]byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, b)
			if len(bodies) == 1 {
				w.Write([]byte(`{"content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens","usage":{"input_tokens":10,"output_tokens":50}}`))
				return
			}
			w.Write([]byte(okResp))
		}))
		defer srv.Close()
		res, err := NewClient(srv.URL, "test-key").Query(context.Background(), "acme/pi-9", "", "prompt")
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(bodies) != 2 {
			t.Fatalf("requests = %d, want 2 (one escalation)", len(bodies))
		}
		if bytes.Equal(bodies[0], bodies[1]) {
			t.Fatalf("escalated bodies identical; want doubled max_tokens: %s", bodies[0])
		}
		// The receipt describes the final request (the usage ledger's
		// convention) — the first body is visible only via Escalations.
		assertReceipt(t, res, bodies[1])
	})

	t.Run("tool loop attests the final round's body", func(t *testing.T) {
		var bodies [][]byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, b)
			if len(bodies) == 1 {
				w.Write([]byte(`{"content":[{"type":"tool_use","id":"t1","name":"read_file","input":{}}],"stop_reason":"tool_use"}`))
				return
			}
			w.Write([]byte(okResp))
		}))
		defer srv.Close()
		tools := []Tool{{Name: "read_file", Description: "read", InputSchema: map[string]interface{}{"type": "object"}}}
		exec := func(ctx context.Context, call ToolCall) (string, error) { return "ok", nil }
		res, _, err := NewClient(srv.URL, "test-key").QueryWithTools(context.Background(), "test-model", "", "prompt", tools, exec, 0)
		if err != nil {
			t.Fatalf("QueryWithTools: %v", err)
		}
		if len(bodies) != 2 {
			t.Fatalf("requests = %d, want 2 (one tool round)", len(bodies))
		}
		if bytes.Equal(bodies[0], bodies[1]) {
			t.Error("later rounds must carry grown message lists — identical bodies break the final-round receipt")
		}
		assertReceipt(t, res, bodies[1])
	})

	t.Run("error return carries no receipt", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		res, err := NewClient(srv.URL, "bad-key").Query(context.Background(), "test-model", "", "prompt")
		if err == nil {
			t.Fatal("want error for 401")
		}
		if res.RequestSHA16 != "" || res.RequestBytes != 0 {
			t.Errorf("receipt = %q/%d, want empty on error (nothing shipped to attest)", res.RequestSHA16, res.RequestBytes)
		}
	})
}
