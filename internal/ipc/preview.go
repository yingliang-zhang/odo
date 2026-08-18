package ipc

// /preview slash command (tri-model DESIGN LOCK): a headless-chromium
// screenshot of a localhost URL, analyzed by the SAME /vision pipeline
// (K3 direct API with ADR-0003 receipts — image_sha16/image_bytes on the
// slash user_message, exact-injection preimage). v1 locked boundaries:
//   - localhost-only URL allowlist; external hosts are refused pre-journal
//     (nothing spawns);
//   - per-shot browser lifecycle (spawn → navigate → capture → exit under a
//     45s cap) — no persistent browser, no profile reuse beyond the npx
//     cache;
//   - no auto-anything: no auto-retry, no auto screenshot after errors or
//     after auto-land.
// Explicit non-goals: agent tool channel (MCP server), any visible browser
// pane, CDP attach to the user's browser, a Process Dock.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yingliang-zhang/odo/internal/moa"
	"github.com/yingliang-zhang/odo/internal/store"
)

const (
	// previewViewport / previewSettleMs are the npx playwright screenshot
	// arguments: a desktop-size viewport and a 3s settle for client-side
	// rendering before capture.
	previewViewport = "1440,900"
	previewSettleMs = "3000"

	// previewDefaultPrompt is the analysis prompt when the user gives none.
	previewDefaultPrompt = "Analyze this screenshot as a UI reviewer: list layout issues, style inconsistencies, overflow/misalignment. Be specific with locations."

	// previewInstallHint is the exact first-run prerequisite command shown
	// on a missing-browser failure.
	previewInstallHint = "PATH=~/.hermes/node/bin:$PATH npx playwright install chromium"
)

// previewChildTimeout bounds the whole per-shot subprocess (npx resolution,
// browser launch, navigation, capture). A var (not const) so the timeout
// test can shrink it — never touched in production paths.
var previewChildTimeout = 45 * time.Second

// previewAllowedHosts is the v1 URL allowlist (url.Hostname() values,
// compared lower-cased): loopback only.
var previewAllowedHosts = map[string]bool{
	"localhost": true,
	"127.0.0.1": true,
	"::1":       true,
}

// parsePreviewArgs splits the /preview argument text: the first token is
// the URL (required), the rest is the optional analysis prompt.
func parsePreviewArgs(text string) (rawURL, prompt string, err error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", "", fmt.Errorf("/preview: URL is required (usage: /preview <url> [prompt])")
	}
	return fields[0], strings.Join(fields[1:], " "), nil
}

// validatePreviewURL enforces the v1 locked boundary: scheme http/https
// and the hostname exactly one of the loopback allowlist (localhost.
// subdomains, trailing dots, and external hosts all refuse).
func validatePreviewURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("preview: invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("preview: URL scheme must be http or https, got %q", u.Scheme)
	}
	if !previewAllowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("preview is restricted to localhost URLs (external URLs intentionally blocked in v1)")
	}
	return nil
}

// redactPreviewURL returns the URL for journaling: a BASIC-auth password in
// userinfo is masked (url.Redacted). The raw form still spawns the capture —
// redaction only applies to the journaled record, never the wire args.
func redactPreviewURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Redacted()
}

// attachmentDir returns <root>/.odo/attachments, creating it — the
// clipboard (save_attachment) and /preview captures share one store so
// chips, vision receipts, and audits resolve identically. The caller wraps
// the error with its own command prefix (save_attachment's wording predates
// the extraction and stays).
func attachmentDir(root string) (string, error) {
	dir := filepath.Join(root, ".odo", "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// hermesNodeBin is the project's pinned Node location — prepended FIRST to
// the screenshot child's PATH so playwright's npx shim (#!/usr/bin/env
// node) runs on hermes's Node and never on a newer system Node (the known
// Node 26 breakage); a .app-launched daemon's minimal PATH stays
// insufficient-only when hermes itself is absent.
func hermesNodeBin() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".hermes", "node", "bin")
}

// resolvePreviewNpx prefers the hermes-pinned npx, else falls back to
// LookPath (dev shells with node on PATH). Keeping resolution here (not
// cmd.Env) matters: exec.Command resolves the binary via the PARENT's
// PATH at construction, so extending only the child env would leave a
// .app-launched daemon unable to find npx at all.
func resolvePreviewNpx() (string, error) {
	if bin := hermesNodeBin(); bin != "" {
		cand := filepath.Join(bin, "npx")
		if info, err := os.Stat(cand); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	p, err := exec.LookPath("npx")
	if err != nil {
		return "", fmt.Errorf("preview needs node/npx — not found on PATH or at ~/.hermes/node/bin (install Node.js first)")
	}
	return p, nil
}

// previewChildEnv is os.Environ() with ~/.hermes/node/bin FIRST on PATH.
func previewChildEnv() []string {
	env := os.Environ()
	bin := hermesNodeBin()
	if bin == "" {
		return env
	}
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + bin + string(filepath.ListSeparator) + e[len("PATH="):]
			return env
		}
	}
	return append(env, "PATH="+bin)
}

// previewShot is one captured screenshot and its receipt facts: the bytes
// the gateway request will carry (read ONCE — the user_message then
// journals sha16/bytes of exactly this preimage, ADR-0003) plus the
// preview_captured event's full-sha256 and capture wall time.
type previewShot struct {
	Path   string
	Data   []byte
	Sha16  string
	Sha256 string // full hex — the capture event's audit hash
	WaitMs int64
}

// runPreviewScreenshot performs one per-shot capture: spawn
// `npx -y playwright@^1 screenshot` → navigate → capture → exit, under
// previewChildTimeout. Failures map to readable chat errors (missing
// node/npx, missing chromium with the install command, navigation timeout,
// refused host).
func runPreviewScreenshot(ctx context.Context, rawURL, out string) (previewShot, error) {
	npx, err := resolvePreviewNpx()
	if err != nil {
		return previewShot{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, previewChildTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, npx,
		"-y", "playwright@^1", "screenshot",
		"--viewport-size="+previewViewport,
		"--wait-for-timeout="+previewSettleMs,
		rawURL, out)
	// Per-shot lifecycle guarantee (lock item 2): on the deadline, kill the
	// whole process GROUP, not just npx — npx spawns node, which spawns the
	// headless chromium; killing only the top PID orphans the subtree
	// (review finding D3). Same pattern as internal/adapter/omp.go.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Env = previewChildEnv()
	start := time.Now()
	output, err := cmd.CombinedOutput()
	waitMs := time.Since(start).Milliseconds()
	if err != nil {
		return previewShot{}, classifyPreviewFailure(cctx, err, string(output))
	}
	data, rerr := os.ReadFile(out)
	if rerr != nil {
		return previewShot{}, fmt.Errorf("preview: read captured screenshot: %w", rerr)
	}
	sum := sha256.Sum256(data)
	return previewShot{
		Path:   out,
		Data:   data,
		Sha16:  sha16(data),
		Sha256: hex.EncodeToString(sum[:]),
		WaitMs: waitMs,
	}, nil
}

// classifyPreviewFailure maps a playwright child failure to a readable
// chat error, ordered most-actionable first. output is the child's
// combined stdout+stderr.
func classifyPreviewFailure(ctx context.Context, err error, output string) error {
	tail := lastLines(output, 3)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("preview screenshot timed out after %ds (navigation timeout, hung page, or the first run still downloading playwright — a warm-cache retry usually succeeds) — is the dev server serving and responsive?", int(previewChildTimeout/time.Second))
	case strings.Contains(output, "Executable doesn't exist") || strings.Contains(output, "browserType.launch"):
		return fmt.Errorf("preview's headless chromium is not installed — run: %s", previewInstallHint)
	case strings.Contains(output, "net::ERR_CONNECTION_REFUSED"):
		return fmt.Errorf("preview could not reach the URL (connection refused) — is the dev server running?")
	case strings.Contains(output, "net::ERR_NAME_NOT_RESOLVED"):
		return fmt.Errorf("preview could not resolve the URL host")
	case exitCode(err) == 127:
		return fmt.Errorf("preview needs node/npx — the child failed to launch (exit 127): %s", tail)
	default:
		return fmt.Errorf("preview screenshot failed: %s", tail)
	}
}

// exitCode extracts an *exec.ExitError's code (-1 when the error carries
// none: a signal kill or the deadline's Kill).
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// lastLines keeps the last n non-empty lines of child output for a chat
// error (playwright's banner is long; the cause is at the end).
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return "(no output)"
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// handlePreviewQuery routes /preview through one headless capture and then
// the /vision pipeline. Mirrors handleVisionQuery's structure — gates and
// slash-slot registration (M12, one critical section), the slashModeVision
// context block BEFORE the user_message journals, the fail-closed receipt
// closure, then the K3 direct-API call — with the loopback allowlist as a
// pre-journal refusal and the capture facts journaled as preview_captured
// right after its user_message (so the badge lands inside the run it
// belongs to).
func (s *Server) handlePreviewQuery(ctx context.Context, c *store.Conversation, text string) (Response, error) {
	rawURL, prompt, err := parsePreviewArgs(text)
	if err != nil {
		return Response{}, err
	}
	// Locked boundary 1: pre-journal refusal — nothing is spawned, nothing
	// is journaled; the IPC error renders as the chat error surface.
	if err := validatePreviewURL(rawURL); err != nil {
		return Response{}, err
	}
	if prompt == "" {
		prompt = previewDefaultPrompt
	}

	// M12: same gates + slash-slot registration as /panel and /vision (one
	// critical section — see handlePanelQuery).
	s.mu.Lock()
	if runID, ok := s.byConv[c.ID]; ok {
		if meta := s.runs[runID]; meta != nil && !meta.finished {
			s.mu.Unlock()
			return Response{}, fmt.Errorf("send_message: agent already running for conversation %d", c.ID)
		}
	}
	if err := s.gateAutoDistillForSendLocked(ctx, c.ID); err != nil {
		s.mu.Unlock()
		return Response{}, err
	}
	s.slashing[c.ID]++
	s.mu.Unlock()
	defer s.releaseSlashSlot(ctx, c.ID)

	w, err := s.store.GetWorkstream(ctx, c.WorkstreamID)
	if err != nil {
		return Response{}, err
	}

	// Vision's lean contract (slashModeVision): the block is assembled
	// before the /preview user_message journals, so it never contains the
	// preview question itself. One events fetch feeds the conversation tail.
	var events []store.Event
	if evs, lerr := s.store.ListEvents(ctx, c.ID, 0); lerr == nil {
		events = evs
	}
	scope := resolvePanelContextScope()
	system := "You are a vision-capable coding assistant. Analyze the image or screenshot described in the prompt. Identify visual issues, layout problems, or design suggestions."
	block, receipt, convBlock, conv := s.slashContextBlock(ctx, w.Name, c.ID, prompt, events, scope, slashModeVision)
	if block != "" {
		system += "\n\n---\n\n" + block
	}

	// Capture BEFORE journaling the user_message (the /vision pre-read
	// ordering): the receipt then covers exactly the PNG bytes the gateway
	// request carries. A capture failure still journals the attempt — with
	// the paths absent (no attachment was ever produced; ADR-0003 inv 5: a
	// receipt never claims bytes that were not read) — and refuses with a
	// paired agent_error BEFORE any moa call (the send path's
	// evidence-first ordering; failRun precedent at the receipt breach).
	attachDir, err := attachmentDir(s.projectRoot)
	if err != nil {
		return Response{}, fmt.Errorf("preview: mkdir attachments: %w", err)
	}
	urlSum := sha256.Sum256([]byte(rawURL))
	out := filepath.Join(attachDir, fmt.Sprintf("preview-%d-%s.png", time.Now().Unix(), hex.EncodeToString(urlSum[:4])))
	shot, shotErr := runPreviewScreenshot(ctx, rawURL, out)

	msgPayload := slashUserMessagePayload("/preview", text, receipt, scope, len(system)+len(prompt), conv)
	if shotErr == nil {
		msgPayload["attachments"] = []string{shot.Path}
		msgPayload["image_sha16"] = []string{shot.Sha16}
		msgPayload["image_bytes"] = len(shot.Data)
	}
	ev, err := s.store.AppendEvent(ctx, c.ID, store.EventUserMessage, mustJSON(msgPayload))
	if err != nil {
		return Response{}, err
	}
	if shotErr != nil {
		return Response{}, s.failRun(ctx, c.ID, shotErr)
	}

	// The capture receipt: full-sha256 (audit-grade, the user_message's
	// sha16 covers the wire bytes), size, and the per-shot wall time. The
	// URL is journaled redacted — a loopback BASIC-auth password must not
	// persist verbatim (review finding: userinfo leaks into the journal).
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventPreviewCaptured, mustJSON(map[string]interface{}{
		"url":     redactPreviewURL(rawURL),
		"bytes":   len(shot.Data),
		"sha256":  shot.Sha256,
		"wait_ms": shot.WaitMs,
	})); err != nil {
		return Response{}, err
	}

	// W2 item 4: fail closed BEFORE any moa call — the attempt (above) and
	// the breach (agent_error) both stay on record.
	if aerr := s.assertSlashReceipts(block, receipt, convBlock); aerr != nil {
		return Response{}, s.failRun(ctx, c.ID, fmt.Errorf("slash receipt assertion failed: %w", aerr))
	}

	client := moa.NewClientFromEnv("", "")
	res, qerr := client.QueryWithImages(ctx, slashVisionModel, system, prompt, []moa.VisionImage{
		{Path: shot.Path, MediaType: moa.ImageMediaType(shot.Path), Data: shot.Data},
	})

	var resultText string
	if qerr != nil {
		resultText = "(vision error: " + qerr.Error() + ")"
	} else {
		resultText = "## " + slashVisionModel + "\n\n" + res.Text
		if res.Truncated {
			resultText += truncationMarker(res.Budget, len(res.Escalations))
		}
	}

	// vision:true (plus preview:true provenance) keeps the fold-eligibility
	// and slash-turn exclusions the vision flag already buys (auto.go's
	// measureWindow, collectSlashTurns); the GUI renders it with the
	// existing vision event rendering.
	agentPayload := map[string]interface{}{
		"text":    resultText,
		"vision":  true,
		"preview": true,
	}
	if qerr == nil && res.Truncated {
		agentPayload["truncated"] = true
		agentPayload["output_tokens"] = res.OutputTokens
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentText, mustJSON(agentPayload)); err != nil {
		return Response{}, err
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventAgentDone, mustJSON(map[string]interface{}{
		"vision":  true,
		"preview": true,
	})); err != nil {
		return Response{}, err
	}

	return Response{Event: &ev}, nil
}
