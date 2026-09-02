package ipc

import (
	"context"
	"fmt"
	"path/filepath"
)

// Odo DX wave — the Memory tab's direct-edit write path (docs/design lock:
// memory.md/pins.md GUI editor). write_memory replaces the FULL body of a
// PROJECT memory layer from the GUI: .odo/memory.md or .odo/pins.md ONLY;
// user.md is cross-project and stays owned by direct ~/.odo edits, and the
// clock/archive layers stay daemon-owned. This is a shortcut around the
// proposal/auto-apply flow, not a replacement — that flow is untouched.
//
// Crash-recovery posture, deliberately different from pin/apply: pin and
// apply are multi-step ops (read-modify-write with derived content), so
// they journal marker-first receipts for the replay engine. write_memory
// is ONE atomic rename of caller-supplied bytes — it lands whole or not
// at all, so there is no stranded intermediate state to recover and the
// journal stays quiet.

// handleWriteMemory overwrites <project>/.odo/<file> with req.Content.
// Validation is cheapest-first (argv shape before any I/O): the layer name
// must be one of the two GUI-editable files; the body must fit the layer's
// injection cap (refuse-on-overflow — the same never-truncate-a-user-file
// posture as pin: a 4KB-capped textarea that silently clipped would inject
// a truncated rule set forever after). The write itself is
// writeFileWithin (symlink guard + tmp+rename), serialized with pin/apply
// under memMu so a concurrent batch apply can't interleave a derived body
// with a human overwrite mid-write.
func (s *Server) handleWriteMemory(ctx context.Context, req Request) (Response, error) {
	if _, err := s.resolveProject(ctx, req.ProjectRoot); err != nil {
		return Response{}, fmt.Errorf("write_memory: %w", err)
	}
	var cap int
	switch req.File {
	case memoryFileName:
		cap = memoryCap
	case "pins.md":
		cap = pinsCap
	default:
		return Response{}, fmt.Errorf("write_memory: refused %q — only memory.md and pins.md are GUI-editable", req.File)
	}
	if len(req.Content) > cap {
		return Response{}, fmt.Errorf("write_memory: %s would exceed %d bytes (%d written) — refusing to truncate a user file", req.File, cap, len(req.Content))
	}
	s.memMu.Lock()
	defer s.memMu.Unlock()
	if err := writeFileWithin(s.projectRoot, filepath.Join(s.projectRoot, ".odo", req.File), req.Content, 0o644); err != nil {
		return Response{}, fmt.Errorf("write_memory: write %s: %w", req.File, err)
	}
	return Response{Applied: true}, nil
}
