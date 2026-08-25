# Security Hardening

- P0 symlink prompt-injection containment: project-side reads go through `readWithinDir`; daemon-owned file writes add Lstat symlink refusal; containment violations degrade to absent/vanished semantics, never new error faces (main-epoch-32) (main-epoch-30)
- Containment scope excludes global `~/.odo` files (user.md/pins) — restricting them would break legitimate dotfiles symlinks and they sit outside the threat model; `wiki/` is git-committable so it is in scope (main-epoch-30)
- The P0 vector was `generateAgentsMD` reading project `memory.md`/`pins.md` directly at server.go:740 (main-epoch-32)
- /preview redirect bypass closed in two layers: per-hop redirect validation + final-URL capture with a documented v1 boundary (JS/meta-refresh unblocked), then an in-process loopback-only filtering proxy env-injected into the Playwright child denying off-loopback requests before dial (main-epoch-30) (main-epoch-32)
- Staged-only edits are protected by `git.IndexEditsBeyondHEAD` (real index vs HEAD stage-0 compare) with unified refusal before adjudication, so `git add` cannot clobber a divergent index; the diff stays pending (main-epoch-32)
- Accept rollback no longer destroys uncommitted work: `git.DirtyPaths` guards refuse patch-own dirty paths on both apply sites and keep the diff pending; the refusal is NOT wrapped as `errBaseStale` because user dirt requires human triage, not an auto-revise cycle (main-epoch-28)
- `ProbeAlreadyLanded` identical-content rescue is ordered before the dirty-path refusal so unstaged identical edits still land via bookkeeping (main-epoch-28)
- `retireRun` selects its target by matching worktreePath against `s.runs` — reviewing an older diff no longer closes a newer run's in-progress worktree or kills its auto-land verify (main-epoch-28)
- Panel claim triage precedent: verify every reviewer charge against source — of three charges on diff #46 only the `sameAutoDistillList` asymmetric-membership hole was valid (fixed with consume semantics mirroring `sameIdList` plus 3 duplicate-id tests); the memo-defeat and intermediate-symlink charges were rejected with evidence (main-epoch-33)
- Large-file preview streams via `os.Open` + `io.LimitReader(cap+1)` instead of reading whole files into memory (main-epoch-28)
