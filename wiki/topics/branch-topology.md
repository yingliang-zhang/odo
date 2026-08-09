# Branch Topology and odo/main Work Branch

- No merge of `odo/main` into `main` was ever needed: `odo/main` is fully contained in `main` (0 ahead, 5 then 7 behind); merge-base equals the `odo/main` tip (epoch-3)
- The "two mains" are explained by M11c design: workstreams store bare name `main`, git consumers prefix `odo/` (server.go:453), run worktrees check out `odo/main` via CreateWorktreeOnBranch (`git worktree add -B odo/main`, git.go:41) (epoch-2)
- Accept flow lands on `main`, then AdvanceBranch fast-forwards `odo/main` (server.go:1147); the 7-commit lag (SUDO_CODING_KEY, IME, PATH fixes + rename/revert pair) self-corrects on the next accept (epoch-3)
- A work session ran from worktree `.odo/worktrees/6a7852d9-*` on branch `odo/main` (epoch-3)
