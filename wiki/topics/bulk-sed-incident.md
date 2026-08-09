# Incident: Bulk sed Scope Violation

- Self-caused incident: the bulk `sed` for the module rename traversed `.odo/worktrees/*`, rewriting all 7 worktree checkouts; the reverse-substitution then corrupted line 47 of the brief .md in each worktree (epoch-2)
- All worktrees were restored and each verified `git status` clean (epoch-1)
- Lesson distilled into a proposed skill `scoped-bulk-text-replacement`: use `git ls-files` to bound replacement scope instead of broad traversal (epoch-4)
