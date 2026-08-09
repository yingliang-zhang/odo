# Branch Topology and Accept Flow

- odo/main is Odo's internal work branch with a literal '/' (M11c design): workstreams store bare main, git consumers prefix odo/ (server.go:453); run worktrees check out odo/main via CreateWorktreeOnBranch; accept lands on main, then AdvanceBranch fast-forwards odo/main (epoch-2)
- No merge needed: odo/main was fully contained in main (merge-base = ac8bed8 = odo/main tip); main was strictly ahead with IME/PATH/SUDO_CODING_KEY fixes (epoch-3)
- odo/main left deliberately 6–7 commits behind main — AdvanceBranch fast-forwards it automatically on the next accept (epoch-7)
- memory/log.md HEAD marker was observed stale (logged 6ecbac0 vs actual ac8bed8+) (epoch-5)
