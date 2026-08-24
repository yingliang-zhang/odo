# Test Environment & Verification Discipline

- AutoLand pipeline tests require both `t.Setenv("HOME")` isolation and `review:` arming (convention documented at autoland_test.go L352); verify runs with a scratch HOME since P1#11, so an unarmed test silently passes only on machines with a review configured — an environment-dependent lie (main-epoch-25)
- #39's `verify_failed` block traced to `TestAutoLandStartedRowsAbsentBeforeSpend`, the sole case among 24 missing isolation + arming; the +6-line fix added both with zero semantic change and the entire TestAutoLand group went green under scratch HOME (main-epoch-25)
- Faithful-reproduction recipe for verify failures: byte-identical worktree + scratch HOME + filtered env — pre-fix it isolated exactly one FAIL across the full suite, proving the block was a latent main defect and not #39's changes (main-epoch-25)
- Worktrees lack `gui/node_modules`, so `npx tsc` verify fails in seconds; `runVerifyGate` auto-provisions the symlink from the main checkout at both call sites — diff 8's "stuck panel" was this environmental false positive, not a content failure (UI-epoch-4)
- A zombie vite dev server on :1420 plus `reuseExistingServer: true` silently runs stale builds producing false e2e passes; verify port ownership before running e2e (bug-fix-epoch-2)
- Coupled fixture expectations: #37's cap 3->2 needed five synchronized edits in ledger.spec.ts (row count 11->10, round naming, nth-index shift, badge counts) — a repo-wide audit is required when changing pipeline round topology (main-epoch-22)
- The `pipeline-chip:84` failure reported by the gate was a timing flake that passed in isolation and on rerun; not every gate red warrants a fix (main-epoch-22)
- Mock identity bug: passing fixture arrays by reference made React `Object.is` bail out and froze the UI; mocks must copy arrays at the response edge (gui-wave-epoch-1)
- Scroll-repin tests need unequal-length histories (12 vs 72 messages): equal lengths let browser scroll anchoring coincidentally land at bottom and mask the bug in a false pass (bug-fix-epoch-2)
