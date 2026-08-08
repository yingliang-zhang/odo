// Package moa provides a direct LLM API client for text-only "thinking" tasks
// (review, audit, design, research) using the Anthropic Messages protocol.
// It bypasses the OMP agent process to avoid the overhead of spawning a full
// agent runtime for a single text completion.
//
// The client talks to the Sudo gateway (https://coding.sudoai.cc/anthropic)
// which translates /v1/messages to each upstream model's native protocol.
package moa

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Default endpoints and protocol constants.
const (
	defaultBaseURL = "https://coding.sudoai.cc/anthropic"
	defaultKeyEnv  = "SUDO_CODING_KEY"
	defaultModel   = "t9s/glm-5.2"
	apiVersion     = "2023-06-01"
	defaultMaxTok  = 4096
)

// Client is a minimal Anthropic Messages API client.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewClient creates a client from explicit parameters.
func NewClient(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

// NewClientFromEnv creates a client using the standard env/config defaults.
// baseURL comes from the MOA_BASE_URL env var (if set) or the hardcoded
// default; apiKey comes from the named env var (moa_key_env or SUDO_CODING_KEY).
func NewClientFromEnv(prefsBaseURL, prefsKeyEnv string) *Client {
	baseURL := prefsBaseURL
	if baseURL == "" {
		baseURL = os.Getenv("MOA_BASE_URL")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	keyEnv := prefsKeyEnv
	if keyEnv == "" {
		keyEnv = defaultKeyEnv
	}
	apiKey := os.Getenv(keyEnv)
	return NewClient(baseURL, apiKey)
}

// messageRequest is the Anthropic Messages API request body.
type messageRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    string         `json:"system,omitempty"`
	Messages  []messageEntry `json:"messages"`
}

type messageEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// messageResponse is the Anthropic Messages API response body.
type messageResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Query sends a single text completion request to the Anthropic Messages API
// and returns the concatenated text from all text-type content blocks.
// Thinking blocks are silently dropped (they carry reasoning traces, not
// visible output). If system is non-empty it becomes the top-level system
// prompt (Anthropic-native, not in-band).
func (c *Client) Query(ctx context.Context, model, system, prompt string) (string, error) {
	if c.APIKey == "" {
		return "", fmt.Errorf("moa: API key is empty (check env var %s)", defaultKeyEnv)
	}
	if model == "" {
		model = defaultModel
	}
	reqBody := messageRequest{
		Model:     model,
		MaxTokens: defaultMaxTok,
		System:    system,
		Messages: []messageEntry{
			{Role: "user", Content: prompt},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("moa: marshal request: %w", err)
	}
	url := c.BaseURL + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("moa: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", apiVersion)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("moa: http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("moa: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("moa: API returned %d (check server logs for details)", resp.StatusCode)
	}
	var msg messageResponse
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", fmt.Errorf("moa: parse response: %w", err)
	}
	var texts []string
	for _, block := range msg.Content {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	if msg.StopReason == "max_tokens" {
		return "", fmt.Errorf("moa: response truncated (stop_reason=max_tokens, %d output tokens used)", msg.Usage.OutputTokens)
	}
	if len(texts) == 0 {
		return "", fmt.Errorf("moa: response had no text content blocks (stop_reason=%s)", msg.StopReason)
	}
	return strings.Join(texts, "\n\n"), nil
}
