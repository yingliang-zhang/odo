// fork.go — turn-fork (P1 borrow #6 from the 2026-08-13 tri-model harness
// audit: codex durable reverts / grok worktree-fork UX). fork_conversation
// branch-copies a conversation's journal prefix into a NEW conversation on
// a NEW workstream lane, plus a fresh detached worktree at current HEAD.
//
// What turn-fork is:
//   - a COPY of journal state (seq 1..from_seq, verbatim payload/type/seq)
//     into a new conversation — receipts and context preserved exactly;
//   - a fresh workstream lane whose name derives from the source lane's
//     (Sidebar renders the forked_from provenance the store joins back);
//   - a fresh worktree checkout the new lane starts from: the repo HEAD
//     at fork time — i.e., the source conversation's last accepted diff
//     is in it (or plain main when nothing of it landed yet).
//
// What turn-fork is NOT:
//   - NOT a git revert (no history rewriting — both lanes continue
//     independently);
//   - NOT a checkpoint/restore (no state rollback — the original lane
//     keeps its full journal and its future);
//   - the source conversation is NEVER modified by the fork (append-only
//     invariant: fork copies, never edits).
package ipc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
	"github.com/yingliang-zhang/odo/internal/worktree"
)

// forkLaneNameMax bounds the unique-name ladder for fork lanes.
const forkLaneNameMax = 99

// handleForkConversation implements fork_conversation. s.mu is held for
// the whole handler (the handleSendMessage precedent): name-collision
// probing, store writes, and the worktree checkout stay one serial unit
// against concurrent forks/switches; git worktree add is IPC-fast (~ms)
// and adapter starts are absent from this path entirely.
func (s *Server) handleForkConversation(ctx context.Context, req Request) (Response, error) {
	if req.FromSeq < 1 {
		return Response{}, fmt.Errorf("fork_conversation: from_seq %d is below the journal floor", req.FromSeq)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := s.checkConversation(ctx, req.ConversationID)
	if err != nil {
		return Response{}, fmt.Errorf("fork_conversation: %w", err)
	}
	srcWs, err := s.store.GetWorkstream(ctx, src.WorkstreamID)
	if err != nil {
		return Response{}, fmt.Errorf("fork_conversation: %w", err)
	}
	// Mid-delete/deleted lane bar (forks would strand on a lane the GUI
	// stopped listing; same guard as send/bootstrap paths).
	if err := s.guardLiveWorkstreamLocked(srcWs); err != nil {
		return Response{}, fmt.Errorf("fork_conversation: %w", err)
	}

	// Past-end from_seq must refuse BEFORE the lane loop below runs:
	// the loop CREATES workstream rows, and a refusal after them would
	// strand an empty <src>-fork-N lane (the store op's identical check
	// is transactional — its conversation insert rolls back, but the
	// earlier lane row would not). Under s.mu the journal is stable, so
	// this precheck and the store op's agree exactly.
	maxSeq := 0
	if evs, lerr := s.store.ListEvents(ctx, src.ID, 0); lerr != nil {
		return Response{}, fmt.Errorf("fork_conversation: read source journal: %w", lerr)
	} else if len(evs) > 0 {
		maxSeq = evs[len(evs)-1].Seq
	}
	if req.FromSeq > maxSeq {
		return Response{}, fmt.Errorf("fork_conversation: source journal ends at seq %d — cannot fork from %d", maxSeq, req.FromSeq)
	}

	// Fresh lane: <src>-fork-N, first free N. CreateOrGetWorkstream's
	// get-hit branch means "name raced/taken" → bump; a fresh row wins.
	lane := store.Workstream{}
	for n := 1; n <= forkLaneNameMax; n++ {
		name := sanitizeBranchName(fmt.Sprintf("%s-fork-%d", srcWs.Name, n))
		if name == "" {
			break
		}
		if _, err := s.store.GetWorkstreamByName(ctx, srcWs.ProjectID, name); err == nil {
			continue // taken
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Response{}, fmt.Errorf("fork_conversation: name probe: %w", err)
		}
		cand, err := s.store.CreateOrGetWorkstream(ctx, srcWs.ProjectID, name)
		if err != nil {
			return Response{}, fmt.Errorf("fork_conversation: create lane: %w", err)
		}
		if _, err := s.store.GetActiveConversation(ctx, cand.ID); err == nil {
			continue // a loser of the v4 name race: lane exists with a conversation
		}
		lane = cand
		break
	}
	if lane.ID == 0 {
		return Response{}, fmt.Errorf("fork_conversation: no free fork lane name under %s (%d tried)", srcWs.Name, forkLaneNameMax)
	}

	// The fork's base is the repo HEAD at fork time (accepted diffs land
	// main forward; a no-diff lane starts at plain main HEAD). ErrNoHead-
	// class failures degrade to NULL like any other conversation.
	baseSHA := ""
	if sha, gerr := git.CurrentSHA(s.projectRoot); gerr == nil {
		baseSHA = sha
	} else {
		log.Printf("fork_conversation: read HEAD sha: %v (fork base stays NULL)", gerr)
	}

	newConv, copied, err := s.store.ForkConversation(ctx, src.ID, req.FromSeq, lane.ID, baseSHA)
	if err != nil {
		return Response{}, fmt.Errorf("fork_conversation: %w", err)
	}

	// Fresh worktree for the forked lane (the spec's branch-point
	// checkout): detached at HEAD. Per the run lifecycle the lane's first
	// run will carve its own run worktree at send time — this one is the
	// forensic/branch point and the sweeper owns its retirement.
	runDirID := worktree.NewRunID()
	wtPath, err := s.mgr.Create(runDirID)
	if err != nil {
		return Response{}, fmt.Errorf("fork_conversation: create worktree: %w", err)
	}

	// Fork receipt in the NEW lane (the source is never written): the
	// lane's first agent run replays this row and KNOWS its provenance.
	if _, err := s.store.AppendEvent(ctx, newConv.ID, store.EventReviewAction, mustJSON(map[string]interface{}{
		"action":              "conversation_forked",
		"actor":               "human",
		"src_conversation_id": src.ID,
		"src_workstream":      srcWs.Name,
		"from_seq":            req.FromSeq,
		"copied":              copied,
		"worktree_path":       wtPath,
	})); err != nil {
		return Response{}, fmt.Errorf("fork_conversation: journal receipt: %w", err)
	}
	return Response{Workstream: &lane, Conversation: &newConv, Path: wtPath}, nil
}
