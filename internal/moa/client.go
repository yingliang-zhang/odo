// Package moa provides a direct LLM API client for "thinking" tasks
// (review, audit, design, research) using the Anthropic Messages protocol.
// It bypasses the OMP agent process to avoid the overhead of spawning a full
// agent runtime for a single completion.
//
// Read-only tool use (E1): QueryWithTools exposes Anthropic-native tools —
// the model emits tool_use blocks, the daemon-side executor answers them
// under a scoped root, and the loop continues until end_turn or a round
// cap. The daemon journals every executed call; no writes, no shell.
//
// The client talks to the Sudo gateway (https://coding.sudoai.cc/anthropic)
// which translates /v1/messages to each upstream model's native protocol.
package moa

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Default endpoints and protocol constants.
const (
	defaultBaseURL = "https://coding.sudoai.cc/anthropic"
	defaultKeyEnv  = "SUDO_CODING_KEY"
	defaultModel   = "t9s/glm-5.2"
	apiVersion     = "2023-06-01"
	// 4096 truncated thinking models (kimi-k3, deepseek-v4-flash): their
	// reasoning trace burns the same output budget, so /panel replies came
	// back with stop_reason=max_tokens. 16384 leaves room for reasoning +
	// answer across the sudo gateway's upstreams.
	defaultMaxTok = 16384

	// defaultToolRounds bounds QueryWithTools' execute-and-continue loop.
	// 8 cut glm-5.2 off mid-chain (observed: a legitimate glob→grep→read
	// chain filled 15 calls across 8 rounds before it could write the
	// answer), so the default is the ceiling.
	defaultToolRounds = 16
	// maxToolRounds is the hard ceiling a caller can raise the cap to.
	maxToolRounds = 16
	// errBodyTail caps how much of a non-200 response body the error shows
	// (gateway diagnostics; the old "check server logs" text hid 400s that
	// name the exact problem, e.g. an unsupported tools field).
	errBodyTail = 512
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
// default; apiKey comes from the named env variable (moa_key_env or SUDO_CODING_KEY).
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

// Tool describes one callable tool in the Anthropic tools protocol.
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// ToolCall is one tool_use block the model emitted.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolAudit records one executed call for daemon-side journaling: what was
// asked, how much came back, and whether it failed. Audit input is capped —
// the full bytes live only in the request, not the journal.
type ToolAudit struct {
	Name        string `json:"name"`
	Input       string `json:"input"`
	ResultBytes int    `json:"result_bytes"`
	Error       string `json:"error,omitempty"`
}

// auditInputCap bounds the echoed input inside a ToolAudit.
const auditInputCap = 256

// ToolExecutor runs one tool call and returns the result text handed back
// to the model as a tool_result. An error is sent as an is_error
// tool_result (the model sees the failure and can adapt) — it does not
// abort the loop.
type ToolExecutor func(ctx context.Context, call ToolCall) (string, error)

// messageRequest is the Anthropic Messages API request body. Content is
// either a plain string (text-only calls) or []contentBlock (tool loop).
type messageRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    string         `json:"system,omitempty"`
	Messages  []messageEntry `json:"messages"`
	Tools     []Tool         `json:"tools,omitempty"`
}

type messageEntry struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// contentBlock covers every block shape the loop moves: text, tool_use
// (assistant -> us) and tool_result (us -> assistant). Fields are omitted
// when empty, so one struct marshals all three shapes.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// messageResponse is the Anthropic Messages API response body. rawContent
// keeps the content array verbatim: thinking models emit blocks with fields
// contentBlock doesn't model ("thinking", "signature"), and the protocol
// requires those blocks be replayed untouched in the tool loop. Re-marshaling
// the cooked struct drops them — the sudo gateway then rejects round 2 with
// 400 "thinking.thinking: Field required" (observed with kimi-k3).
type messageResponse struct {
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	rawContent json.RawMessage
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// text concatenates the text-type blocks (thinking blocks are dropped —
// they carry reasoning traces, not visible output).
func (r *messageResponse) text() string {
	var texts []string
	for _, block := range r.Content {
		if block.Type == "text" && block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// toolUses returns the tool_use blocks (none when the model is done).
func (r *messageResponse) toolUses() []contentBlock {
	var out []contentBlock
	for _, block := range r.Content {
		if block.Type == "tool_use" {
			out = append(out, block)
		}
	}
	return out
}

// post sends one request body and returns the parsed response. The shared
// transport for Query, QueryWithImages, and the tool loop.
func (c *Client) post(ctx context.Context, reqBody interface{}) (*messageResponse, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("moa: marshal request: %w", err)
	}
	url := c.BaseURL + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("moa: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", apiVersion)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("moa: http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("moa: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		tail := strings.TrimSpace(string(raw))
		if len(tail) > errBodyTail {
			tail = tail[:errBodyTail] + "…"
		}
		if tail == "" {
			tail = "(empty body)"
		}
		return nil, fmt.Errorf("moa: API returned %d: %s", resp.StatusCode, tail)
	}
	var msg messageResponse
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("moa: parse response: %w", err)
	}
	// Second pass captures the content array as raw bytes for verbatim
	// assistant echo in the tool loop (see rawContent above).
	var envelope struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("moa: parse response content: %w", err)
	}
	msg.rawContent = envelope.Content
	return &msg, nil
}

// validate guards the response's visible output: a max_tokens stop is an
// error, never a partial answer or a truncated tool input.
func (r *messageResponse) validate() error {
	if r.StopReason == "max_tokens" {
		return fmt.Errorf("moa: response truncated (stop_reason=max_tokens, %d output tokens used)", r.Usage.OutputTokens)
	}
	return nil
}

// Query sends a single text completion request to the Anthropic Messages API
// and returns the concatenated text from all text-type content blocks.
// If system is non-empty it becomes the top-level system prompt
// (Anthropic-native, not in-band).
func (c *Client) Query(ctx context.Context, model, system, prompt string) (string, error) {
	if c.APIKey == "" {
		return "", fmt.Errorf("moa: API key is empty (check env var %s)", defaultKeyEnv)
	}
	if model == "" {
		model = defaultModel
	}
	msg, err := c.post(ctx, messageRequest{
		Model:     model,
		MaxTokens: defaultMaxTok,
		System:    system,
		Messages: []messageEntry{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return "", err
	}
	if err := msg.validate(); err != nil {
		return "", err
	}
	text := msg.text()
	if text == "" {
		return "", fmt.Errorf("moa: response had no text content blocks (stop_reason=%s)", msg.StopReason)
	}
	return text, nil
}

// QueryWithTools runs the read-only tool loop (E1): the request advertises
// tools; every response's tool_use blocks are executed (daemon-side, scoped
// by the executor) and echoed back as tool_result; the loop ends when the
// model stops calling tools (end_turn) or the round cap trips. The final
// text and the audit of every executed call are returned.
//
// With no tools (or a nil executor) the call degrades to Query — a model or
// gateway without tool support simply answers in one round as before.
func (c *Client) QueryWithTools(ctx context.Context, model, system, prompt string, tools []Tool, exec ToolExecutor, maxRounds int) (string, []ToolAudit, error) {
	if len(tools) == 0 || exec == nil {
		text, err := c.Query(ctx, model, system, prompt)
		return text, nil, err
	}
	if c.APIKey == "" {
		return "", nil, fmt.Errorf("moa: API key is empty (check env var %s)", defaultKeyEnv)
	}
	if model == "" {
		model = defaultModel
	}
	if maxRounds <= 0 {
		maxRounds = defaultToolRounds
	}
	if maxRounds > maxToolRounds {
		maxRounds = maxToolRounds
	}

	messages := []messageEntry{{Role: "user", Content: prompt}}
	var audits []ToolAudit
	for round := 1; ; round++ {
		msg, err := c.post(ctx, messageRequest{
			Model:     model,
			MaxTokens: defaultMaxTok,
			System:    system,
			Messages:  messages,
			Tools:     tools,
		})
		if err != nil {
			return "", audits, err
		}
		// Never act on a truncated turn: a max_tokens tool_use carries a
		// half-written JSON input, so executing it is unsafe.
		if err := msg.validate(); err != nil {
			return "", audits, err
		}
		uses := msg.toolUses()
		if len(uses) == 0 {
			text := msg.text()
			if text == "" {
				return "", audits, fmt.Errorf("moa: response had no text content blocks (stop_reason=%s)", msg.StopReason)
			}
			return text, audits, nil
		}
		if round >= maxRounds {
			return "", audits, fmt.Errorf("moa: tool loop exceeded %d rounds (model kept requesting tools)", maxRounds)
		}

		// Echo the assistant turn verbatim (rawContent), then answer each call
		// as a tool_result in one user turn. The cooked struct can't round-trip
		// thinking blocks — the tool loop requires them byte-identical.
		var assistantContent interface{} = msg.Content
		if len(msg.rawContent) > 0 {
			assistantContent = msg.rawContent
		}
		messages = append(messages, messageEntry{Role: "assistant", Content: assistantContent})
		results := make([]contentBlock, 0, len(uses))
		for _, tu := range uses {
			call := ToolCall{ID: tu.ID, Name: tu.Name, Input: tu.Input}
			out, execErr := exec(ctx, call)
			input := string(tu.Input)
			if len(input) > auditInputCap {
				input = input[:auditInputCap] + "…"
			}
			audits = append(audits, ToolAudit{
				Name:        tu.Name,
				Input:       input,
				ResultBytes: len(out),
			})
			block := contentBlock{Type: "tool_result", ToolUseID: tu.ID, Content: out}
			if execErr != nil {
				audits[len(audits)-1].Error = execErr.Error()
				block.IsError = true
				block.Content = "error: " + execErr.Error()
			}
			results = append(results, block)
		}
		messages = append(messages, messageEntry{Role: "user", Content: results})
	}
}

// VisionImage is one pre-read image for QueryWithImages. The CALLER reads
// the bytes (the daemon handler journals the byte receipt before the
// gateway call, ADR-0003 exact-injection); reading here too would double
// the IO and let the receipt and the wire disagree.
type VisionImage struct {
	Path      string // audit/display only — never re-read
	MediaType string // ImageMediaType(path)
	Data      []byte
}

// ImageMediaType maps an image path's extension to the Anthropic media
// type; anything unrecognized stays PNG (the attachment store's format).
func ImageMediaType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return "image/png"
}

// QueryWithImages sends a request with both text and image content blocks.
// images arrive pre-read (see VisionImage) and are base64-encoded and sent
// as Anthropic image content blocks before the text prompt.
func (c *Client) QueryWithImages(ctx context.Context, model, system, prompt string, images []VisionImage) (string, error) {
	if c.APIKey == "" {
		return "", fmt.Errorf("moa: API key is empty (check env var %s)", defaultKeyEnv)
	}
	if model == "" {
		model = defaultModel
	}
	// Build content blocks: images first, then text.
	var contentBlocks []map[string]interface{}
	for _, img := range images {
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type": "image",
			"source": map[string]interface{}{
				"type":       "base64",
				"media_type": img.MediaType,
				"data":       base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	contentBlocks = append(contentBlocks, map[string]interface{}{
		"type": "text",
		"text": prompt,
	})
	reqBody := map[string]interface{}{
		"model":      model,
		"max_tokens": defaultMaxTok,
		"messages": []map[string]interface{}{
			{"role": "user", "content": contentBlocks},
		},
	}
	if system != "" {
		reqBody["system"] = system
	}
	msg, err := c.post(ctx, reqBody)
	if err != nil {
		return "", err
	}
	if err := msg.validate(); err != nil {
		return "", err
	}
	text := msg.text()
	if text == "" {
		return "", fmt.Errorf("moa: response had no text content blocks (stop_reason=%s)", msg.StopReason)
	}
	return text, nil
}
