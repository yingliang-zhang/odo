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
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yingliang-zhang/odo/internal/modelspec"
)

// Default endpoints and protocol constants.
const (
	defaultBaseURL = "https://coding.sudoai.cc/anthropic"
	defaultKeyEnv  = "SUDO_CODING_KEY"
	defaultModel   = "t9s/glm-5.2"
	apiVersion     = "2023-06-01"

	// Output-budget policy. The initial budget comes from the per-model
	// modelspec table; on stop_reason=max_tokens the request is re-issued
	// whole at double the budget, bounded by the per-model hard cap and
	// maxEscalations. Re-paying the small /panel input is cheaper and more
	// reliable than continuation-style appends: truncated thinking blocks
	// can't be replayed byte-complete (kimi signatures are placeholder
	// values) and upstream behavior on half-written turns diverges.
	maxEscalations = 3

	// Per-request deadline = baseRequestTimeout + budget/genTokPerSecFloor.
	// The 900s floor covers a max-effort review leg whose server-side
	// thinking runs long before the first output token; the generation
	// headroom above it is unchanged — 64K output tokens at the gateway's
	// measured pace (deepseek-v4-flash ≥170 tok/s on 2026-08-09) needs ~6.5
	// minutes, so the worst single request is 900 + 65536/120 ≈ 1446s.
	// Raising the base moves the FLOOR, not the ceiling: budget/120
	// headroom always stacks on top.
	baseRequestTimeout = 900 * time.Second
	genTokPerSecFloor  = 120 // conservative floor under the ≥170 tok/s measured on the slowest model

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
		// No client-wide Timeout: post() derives a per-request deadline from
		// the output budget (a fixed 300s cap would cut off the very
		// large-budget requests the escalation policy creates).
		HTTP: &http.Client{},
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
// (assistant -> us) and tool_result (us -> assistant), plus thinking blocks
// (decode-only: the tool loop replays them byte-verbatim via rawContent, and
// the Thinking field carries the reasoning trace for the review journal,
// M18 batch B). Fields are omitted when empty, so one struct marshals all
// shapes.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
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

// thinking concatenates the thinking-type blocks (reasoning traces the
// model emitted alongside its answer). This is REAL returned data, not a
// fabricated channel: the gateway relays thinking models' reasoning blocks
// in-band. The review journal (M18 batch B) caps and stores it for
// non-accept verdicts; the text() method keeps dropping it from visible
// output.
func (r *messageResponse) thinking() string {
	var traces []string
	for _, block := range r.Content {
		if block.Type == "thinking" && block.Thinking != "" {
			traces = append(traces, block.Thinking)
		}
	}
	return strings.Join(traces, "\n\n")
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

// requestTimeout derives one request's deadline from its output budget:
// the base latency the gateway needs even for tiny answers, plus generation
// time at a conservative tok/s floor (the measured slowest model ran ≥170
// tok/s while burning 21K output tokens). Callers' ctx deadlines still win
// when earlier — WithTimeout never widens a parent deadline.
func requestTimeout(maxTok int) time.Duration {
	return baseRequestTimeout + time.Duration(maxTok/genTokPerSecFloor)*time.Second
}

// post sends one request body and returns the parsed response. The shared
// transport for Query, QueryWithImages, and the tool loop. maxTok is the
// request's max_tokens and sets the per-request deadline.
func (c *Client) post(ctx context.Context, reqBody interface{}, maxTok int) (*messageResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout(maxTok))
	defer cancel()
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

// stopClass buckets a stop_reason into the caller's handling path.
type stopClass int

const (
	stopOK        stopClass = iota // end_turn / tool_use / stop_sequence — answer is complete
	stopTruncated                  // max_tokens — the budget policy decides (escalate, partial, or error)
	stopTerminal                   // refusal / context overflow — surface as an error
)

// classifyStop whitelists the stop reasons the gateway is known to emit
// (2026-08-09 sweep: {end_turn, tool_use, max_tokens}) and fails LOUD on
// anything else — a silent fallthrough is a failure class no alert can see.
// pause_turn and unknown reasons are treated as end_turn (the text arrives
// whole; odo has no continuation channel to resume a paused turn), but both
// are logged.
func (r *messageResponse) classifyStop() stopClass {
	switch r.StopReason {
	case "", "end_turn", "tool_use", "stop_sequence":
		return stopOK
	case "max_tokens":
		return stopTruncated
	case "refusal", "model_context_window_exceeded":
		return stopTerminal
	case "pause_turn":
		log.Printf("moa: stop_reason=pause_turn treated as end_turn (no continuation channel)")
		return stopOK
	default:
		log.Printf("moa: unknown stop_reason %q treated as end_turn", r.StopReason)
		return stopOK
	}
}

// Escalation records one output-budget bump: the request truncated at From,
// consumed OutputTokens there, and was re-issued at To. Journaled with the
// result so the budget policy is a falsifiable ledger, not a silent retry.
type Escalation struct {
	From         int `json:"from"`
	To           int `json:"to"`
	OutputTokens int `json:"output_tokens"`
}

// Result is one completion's visible outcome plus its budget ledger.
type Result struct {
	Text string `json:"text"`
	// Thinking is the model's reasoning trace (thinking-type content
	// blocks), empty for models/gateways that emit none. Journal-only
	// (M18 batch B review thinking_md); never display content.
	Thinking string `json:"thinking,omitempty"`
	// Truncated marks a partial answer: the model hit its hard output cap
	// (Budget, after every escalation) with stop_reason=max_tokens. A
	// /panel or /vision answer is display content with no downstream
	// consumer, so the partial ships flagged instead of erroring out.
	Truncated bool `json:"truncated,omitempty"`
	// Budget is the final request's max_tokens (the cap when Truncated).
	Budget       int          `json:"budget,omitempty"`
	OutputTokens int          `json:"output_tokens,omitempty"`
	Escalations  []Escalation `json:"escalations,omitempty"`
}

// outputBudget tracks one logical completion's max_tokens across retries:
// it doubles on truncation up to the per-model hard cap, never exceeding
// maxEscalations bumps, and logs every step (daemon.log is the audit sink).
type outputBudget struct {
	now, cap int
	esc      []Escalation
}

// escalate doubles the budget after a max_tokens stop. Returns false at the
// hard cap or the bump limit — the caller then decides: partial+flagged for
// display paths, error for the tool loop.
func (b *outputBudget) escalate(model string, outTok int) bool {
	if b.now >= b.cap || len(b.esc) >= maxEscalations {
		return false
	}
	next := b.now * 2
	if next > b.cap {
		next = b.cap
	}
	b.esc = append(b.esc, Escalation{From: b.now, To: next, OutputTokens: outTok})
	log.Printf("moa: %s truncated at %d output tokens; escalating max_tokens %d → %d", model, outTok, b.now, next)
	b.now = next
	return true
}

// oneShot runs one logical completion (Query / QueryWithImages): issue the
// request at the model's initial budget, escalate-and-re-issue on max_tokens,
// return the partial flagged once the hard cap is exhausted. mkBody builds
// the request body for a given budget; callers keep body shape private.
func (c *Client) oneShot(ctx context.Context, model string, mkBody func(model string, maxTok int) interface{}) (Result, error) {
	if c.APIKey == "" {
		return Result{}, fmt.Errorf("moa: API key is empty (check env var %s)", defaultKeyEnv)
	}
	if model == "" {
		model = defaultModel
	}
	spec := modelspec.Lookup(model)
	bud := outputBudget{now: spec.MaxTokens, cap: spec.MaxOutput}
	for {
		msg, err := c.post(ctx, mkBody(model, bud.now), bud.now)
		if err != nil {
			return Result{}, err
		}
		res := Result{
			Text:         msg.text(),
			Thinking:     msg.thinking(),
			Budget:       bud.now,
			OutputTokens: msg.Usage.OutputTokens,
			Escalations:  bud.esc,
		}
		switch msg.classifyStop() {
		case stopTruncated:
			if bud.escalate(model, msg.Usage.OutputTokens) {
				continue // re-issue the whole turn at the bigger budget
			}
			// Hard cap exhausted: ship the partial if it carries visible
			// text; a thinking-model response can also be 100% reasoning
			// trace with zero text, which displays as nothing — error.
			if res.Text == "" {
				return Result{}, fmt.Errorf("moa: %s truncated with no visible text (stop_reason=max_tokens at the %d-token cap after %d escalations)", model, bud.now, len(bud.esc))
			}
			res.Truncated = true
			log.Printf("moa: %s truncated at the %d-token cap after %d escalation(s); returning partial", model, bud.now, len(bud.esc))
			return res, nil
		case stopTerminal:
			return Result{}, fmt.Errorf("moa: %s refused or overflowed (stop_reason=%s, %d output tokens)", model, msg.StopReason, msg.Usage.OutputTokens)
		default:
			if res.Text == "" {
				return Result{}, fmt.Errorf("moa: response had no text content blocks (stop_reason=%s)", msg.StopReason)
			}
			return res, nil
		}
	}
}

// Query sends a single text completion request to the Anthropic Messages API.
// If system is non-empty it becomes the top-level system prompt
// (Anthropic-native, not in-band). Truncation policy: escalate max_tokens ×2
// up to the model's hard cap; a still-truncated answer returns the partial
// text with Result.Truncated set instead of an error.
func (c *Client) Query(ctx context.Context, model, system, prompt string) (Result, error) {
	return c.oneShot(ctx, model, func(model string, maxTok int) interface{} {
		return messageRequest{
			Model:     model,
			MaxTokens: maxTok,
			System:    system,
			Messages: []messageEntry{
				{Role: "user", Content: prompt},
			},
		}
	})
}

// QueryWithTools runs the read-only tool loop (E1): the request advertises
// tools; every response's tool_use blocks are executed (daemon-side, scoped
// by the executor) and echoed back as tool_result; the loop ends when the
// model stops calling tools (end_turn) or the round cap trips. The final
// text and the audit of every executed call are returned.
//
// With no tools (or a nil executor) the call degrades to Query — a model or
// gateway without tool support simply answers in one round as before.
// Truncation policy differs from the one-shot paths on purpose and splits by
// block type at the hard cap: a max_tokens tool_use carries a half-written
// JSON input, so the truncated attempt is discarded and re-issued WHOLE at
// double the budget (never executed, and an error if the cap is exhausted —
// placeholder-style continuation would ask the model to finish a broken turn
// from half a tool_use). A max_tokens final answer (no tool_use blocks) is
// display content like the one-shot paths: at the cap the partial ships
// flagged instead of erroring out after minutes of tool rounds.
func (c *Client) QueryWithTools(ctx context.Context, model, system, prompt string, tools []Tool, exec ToolExecutor, maxRounds int) (Result, []ToolAudit, error) {
	if len(tools) == 0 || exec == nil {
		res, err := c.Query(ctx, model, system, prompt)
		return res, nil, err
	}
	if c.APIKey == "" {
		return Result{}, nil, fmt.Errorf("moa: API key is empty (check env var %s)", defaultKeyEnv)
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

	spec := modelspec.Lookup(model)
	bud := outputBudget{now: spec.MaxTokens, cap: spec.MaxOutput}
	result := func(text string) Result {
		return Result{Text: text, Budget: bud.now, Escalations: bud.esc}
	}
	messages := []messageEntry{{Role: "user", Content: prompt}}
	var audits []ToolAudit
	for round := 1; ; round++ {
		msg, err := c.post(ctx, messageRequest{
			Model:     model,
			MaxTokens: bud.now,
			System:    system,
			Messages:  messages,
			Tools:     tools,
		}, bud.now)
		if err != nil {
			return Result{}, audits, err
		}
		switch msg.classifyStop() {
		case stopTruncated:
			if bud.escalate(model, msg.Usage.OutputTokens) {
				round-- // the discarded attempt consumed no round
				continue
			}
			if len(msg.toolUses()) > 0 {
				return Result{}, audits, fmt.Errorf("moa: %s tool loop truncated at the %d-token cap after %d escalation(s) (not executing a half-written tool_use)", model, bud.now, len(bud.esc))
			}
			res := result(msg.text())
			if res.Text == "" {
				return Result{}, audits, fmt.Errorf("moa: %s truncated with no visible text (stop_reason=max_tokens at the %d-token cap after %d escalations)", model, bud.now, len(bud.esc))
			}
			res.Truncated = true
			res.OutputTokens = msg.Usage.OutputTokens
			log.Printf("moa: %s tool-loop final answer truncated at the %d-token cap; returning flagged partial", model, bud.now)
			return res, audits, nil
		case stopTerminal:
			return Result{}, audits, fmt.Errorf("moa: %s refused or overflowed (stop_reason=%s, %d output tokens)", model, msg.StopReason, msg.Usage.OutputTokens)
		}
		uses := msg.toolUses()
		if len(uses) == 0 {
			text := msg.text()
			if text == "" {
				return Result{}, audits, fmt.Errorf("moa: response had no text content blocks (stop_reason=%s)", msg.StopReason)
			}
			res := result(text)
			res.OutputTokens = msg.Usage.OutputTokens
			return res, audits, nil
		}
		if round >= maxRounds {
			return Result{}, audits, fmt.Errorf("moa: tool loop exceeded %d rounds (model kept requesting tools)", maxRounds)
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
func (c *Client) QueryWithImages(ctx context.Context, model, system, prompt string, images []VisionImage) (Result, error) {
	// Build content blocks: images first, then text. Same truncation policy
	// as Query (escalate, then flagged partial).
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
	return c.oneShot(ctx, model, func(model string, maxTok int) interface{} {
		reqBody := map[string]interface{}{
			"model":      model,
			"max_tokens": maxTok,
			"messages": []map[string]interface{}{
				{"role": "user", "content": contentBlocks},
			},
		}
		if system != "" {
			reqBody["system"] = system
		}
		return reqBody
	})
}
