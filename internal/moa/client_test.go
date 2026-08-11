package moa

import (
	"context"
	"encoding/json"
	"fmt"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid key"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad-key")
	_, err := c.Query(context.Background(), "test", "", "test")
	if err == nil {
		t.Fatal("expected error for 401, got nil")
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
	if got := requestTimeout(0); got != 300*time.Second {
		t.Errorf("requestTimeout(0) = %v, want 300s", got)
	}
	if got := requestTimeout(65536); got != 300*time.Second+546*time.Second {
		t.Errorf("requestTimeout(65536) = %v, want 846s", got)
	}
}

// An endlessly-calling model must trip the default cap, now the ceiling:
// defaultToolRounds == maxToolRounds == 16, so maxRounds=0 means 16 posts.
func TestQueryWithToolsDefaultRoundCap(t *testing.T) {
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
	if calls != maxToolRounds {
		t.Errorf("requests = %d, want %d (default == ceiling)", calls, maxToolRounds)
	}
}
