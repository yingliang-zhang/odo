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
	text, err := c.Query(context.Background(), "test-model", "You are a reviewer.", "Review this diff.")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if text != "ACCEPT\n\nLooks good." {
		t.Errorf("text = %q, want ACCEPT...", text)
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
	text, err := c.Query(context.Background(), "test", "", "test")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if text != "Part 1.\n\nPart 2." {
		t.Errorf("text = %q, want 'Part 1.\\n\\nPart 2.'", text)
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
	text, audits, err := c.QueryWithTools(context.Background(), "test", "", "prompt", tools, exec, 0)
	if err != nil {
		t.Fatalf("QueryWithTools: %v", err)
	}
	if text != "Done." {
		t.Errorf("text = %q, want Done.", text)
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
