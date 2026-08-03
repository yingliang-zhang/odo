package ipc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yingliang-zhang/odo/internal/store"
)

// M5 (Curation): pins are human-owned verbatim statements hoovered into
// .odo/pins.md (`- <text>` per line) — no LLM processing, no curation, no
// metadata. A pin differs from a memory.md rule: a rule is a
// daemon-formatted behavior contract (`- <rule> — cites: <note>; reaffirmed:
// <epoch>`); a pin is a raw user statement injected exactly as written.
// Both are always-injected; the curator never touches pins.

// pinsPath returns the pins file location (next to memory.md).
func pinsPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".odo", "pins.md")
}

// readPins reads <projectRoot>/.odo/pins.md capped at pinsCap with a
// line-boundary cut (mirrors readUserMemory). "" when absent/empty. Pins are
// human-owned: the daemon writes them only via the pin IPC command and never
// truncates at write time.
func readPins(projectRoot string) string {
	b, err := os.ReadFile(pinsPath(projectRoot))
	if err != nil {
		return ""
	}
	return capAtLineBoundary(string(b), pinsCap)
}

// handlePin appends req.Text verbatim to .odo/pins.md as `- <text>` — the
// user-initiated hoover ("remember: X"), the single daemon write path for
// the file. The cap check reads the file in FULL (refuse-on-overflow, never
// truncate a user file — mirrors planUserApply) and the refusal names the
// pin text with nothing written.
func (s *Server) handlePin(ctx context.Context, req Request) (Response, error) {
	if req.Text == "" {
		return Response{}, fmt.Errorf("pin: text is required")
	}
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("pin: %w", err)
	}
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}

	old := readFileFull(pinsPath(s.projectRoot))
	out := strings.TrimRight(old, "\n")
	join := ""
	if out != "" {
		join = "\n"
	}
	content := out + join + "- " + req.Text + "\n"
	if len(content) > pinsCap {
		return Response{}, fmt.Errorf("pins.md would exceed %d bytes: pin %q", pinsCap, req.Text)
	}
	if err := writeFileAtomic(pinsPath(s.projectRoot), content, 0o644); err != nil {
		return Response{}, fmt.Errorf("pin: write pins.md: %w", err)
	}
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":  "pins",
		"cause":  "pin",
		"detail": req.Text,
	})); err != nil {
		return Response{}, err
	}
	return Response{Applied: true}, nil
}

// handleReadPins returns the full .odo/pins.md content as memory_content
// ("" when absent) for the review panel's reader tab — the daemon
// constructs the path itself; the same resolveProject guard as
// handleReadMemory applies. Read-only: no journal writes.
func (s *Server) handleReadPins(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("read_pins: %w", err)
	}
	return Response{MemoryContent: readFileFull(pinsPath(s.projectRoot))}, nil
}
