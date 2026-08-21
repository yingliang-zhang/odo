package ipc

import (
	"context"

	"github.com/yingliang-zhang/odo/internal/git"
	"github.com/yingliang-zhang/odo/internal/store"
)

// wikiDirName is the daemon-owned, git-tracked surface distill and curate
// write (epoch notes, topic pages, index). Agents are protected out of it,
// so a directory-scoped commit never sweeps user work.
const wikiDirName = "wiki"

// commitWiki stages and commits wiki/ after a pass that wrote it (distill
// note, curator rewrite). The memory pipeline's wiki output used to wait
// on manual commits; durability is now the pipeline's own job — the
// journal is the source of truth, but on-disk wiki is the read surface
// and an uncommitted tree is one `git checkout` away from a hole. Never
// fails the caller: git problems (mid-rebase, index lock, missing
// identity) journal commit_failed and the files stay for the next pass.
func (s *Server) commitWiki(ctx context.Context, conversationID int64, subject string) {
	journal := func(cause, detail string) {
		_, _ = s.store.AppendEvent(context.WithoutCancel(ctx), conversationID, store.EventMemoryUpdate, mustJSON(map[string]interface{}{
			"layer":  "wiki",
			"cause":  cause,
			"detail": detail,
		}))
	}
	changed, err := git.HasPathChanges(s.projectRoot, []string{wikiDirName})
	if err != nil {
		journal("commit_failed", "status: "+err.Error())
		return
	}
	if !changed {
		return // nothing to commit — silence, no empty churn
	}
	// Stage first: CommitPaths alone never picks up untracked files, and
	// wiki gains files constantly (epoch notes). Scoped to wiki/, other
	// staged work is untouched.
	if err := git.StagePaths(s.projectRoot, []string{wikiDirName}); err != nil {
		journal("commit_failed", "stage: "+err.Error())
		return
	}
	msg := "docs(wiki): " + subject
	if err := git.CommitPaths(s.projectRoot, msg, []string{wikiDirName}); err != nil {
		journal("commit_failed", "commit: "+err.Error())
		return
	}
	journal("commit", msg)
}
