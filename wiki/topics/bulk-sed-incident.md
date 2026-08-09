# Bulk Sed Worktree Incident and Distilled Skills

- Self-caused incident (repaired same session): the first bulk sed for the module rename traversed .odo/worktrees/*, rewriting all 7 worktree checkouts; the reverse-substitution pass also corrupted line 47 of the brief .md in each worktree (epoch-3)
- All 7 worktrees restored and verified clean via per-directory git status (epoch-2)
- Lesson distilled into skill scoped-bulk-text-replacement: scope replacements via git ls-files, never raw directory traversal (epoch-4)
- Related distilled skills pending acceptance: rollback-pushed-change, diagnose-folded-epoch, reset-odo-journal-safely, rename-github-repo-and-go-module; MoA reviews mostly ACCEPT, two needed wording fixes where the verification step contradicted commit-later ordering (epoch-6)
