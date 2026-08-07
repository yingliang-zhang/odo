package moa

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
