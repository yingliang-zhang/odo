package ipc

import (
	"context"
	"fmt"
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
// line-boundary cut (mirrors readUserMemory). "" when absent/empty — or
// when the file is a planted symlink escaping the project .odo tree
// (2026-08-24 tri-review P0): pins ride the always-injected layer, so the
// read is contained to .odo. Pins are human-owned: the daemon writes them
// only via the pin IPC command and never truncates at write time.
func readPins(projectRoot string) string {
	b, err := readWithinDir(projectRoot, filepath.Join(projectRoot, ".odo"), pinsPath(projectRoot))
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
	// M5-hardening: pins are single-line statements — the file is
	// one `- <text>` line per pin, so whitespace is trimmed, empty-after-
	// trim is refused, and a newline (which would break the one-line
	// format) is refused before anything is written.
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return Response{}, fmt.Errorf("pin text is empty")
	}
	if strings.ContainsAny(text, "\r\n") {
		return Response{}, fmt.Errorf("pin text must be single-line")
	}
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("pin: %w", err)
	}
	c, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, err
	}

	s.memMu.Lock() // single-writer (2026-08-25 audit P1): pin read-modify-write races batch applies cross-workstream
	defer s.memMu.Unlock()
	// Recovery FIRST (2026-08-25 review follow-up P1, replay-engine form
	// since 2026-08-26): the receipt now journals BEFORE the file write
	// (marker-first, the apply protocol's shape), so a crash in between is
	// repaired from the journal — the old file-first order could hand the
	// model a pin no receipt covered, and its crash window had no path
	// back. The replay also protects THIS pin's read-modify-write basis:
	// it must include the crashed pin's line. Same engine as the boot
	// replayer, pins-only scope — never an independent scan.
	if events, lerr := s.store.ListEvents(ctx, c.ID, 0); lerr == nil {
		s.replayLaneMemReceipts(ctx, c.ID, events, replayPin)
	}
	old := readFileWithin(s.projectRoot, filepath.Join(s.projectRoot, ".odo"), pinsPath(s.projectRoot)) // contained: the write basis feeds the file itself (2026-08-24 tri-review P0)
	out := strings.TrimRight(old, "\n")
	join := ""
	if out != "" {
		join = "\n"
	}
	content := out + join + "- " + text + "\n"
	if len(content) > pinsCap {
		return Response{}, fmt.Errorf("pins.md would exceed %d bytes: pin %q", pinsCap, text)
	}
	// Journal-first with the recovery fields (before/after sha + body —
	// bounded by pinsCap): consumption is journaled intent, so the pin can
	// never land journal-less, and a crash after this append is repaired by
	// the replay engine (boot replayer / the next pin's basis pass).
	if _, err := s.store.AppendEvent(ctx, c.ID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
		"layer":      "pins",
		"cause":      "pin",
		"detail":     text,
		"before_sha": sha16([]byte(old)),
		"after_sha":  sha16([]byte(content)),
		"body":       content,
	})); err != nil {
		return Response{}, err
	}
	// Test-only crash drill (2026-08-25 review follow-up): return as if the
	// daemon died here — receipt journaled, file unwritten. Production
	// never sets the seam.
	if s.failPinAfterReceipt != nil {
		return Response{}, s.failPinAfterReceipt
	}
	if err := writeFileWithin(s.projectRoot, pinsPath(s.projectRoot), content, 0o644); err != nil {
		return Response{}, fmt.Errorf("pin: write pins.md: %w", err)
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
	return Response{MemoryContent: readFileWithin(s.projectRoot, filepath.Join(s.projectRoot, ".odo"), pinsPath(s.projectRoot))}, nil // contained to project .odo (2026-08-24 tri-review P0)
}
