# Testing & Build Environment

- Zombie vite dev server on :1420 makes e2e silently run a stale build (reuseExistingServer:true) — verify port ownership before e2e; switching to `false` recommended but left for user decision (bug-fix-epoch-2, UI-epoch-2, UI-epoch-5)
- Verify gate false positive: worktrees lack `gui/node_modules` → 6s tsc 'not the tsc command' verify_failed; `runVerifyGate` now auto-provisions a symlink from the main checkout at both call sites (autoLand + loop) (UI-epoch-4, UI-epoch-5)
- Mixed go+gui diffs now run ALL applicable `.odo-verify` matches, not just the first (UI-epoch-5)
- Main's red tsc fixed by tsconfig lib ES2020→ES2024 (`Promise.withResolvers` in mock-invoke.ts); adjacent executor unified to withResolvers per project convention (UI-epoch-11)
- Shell `cwd` parameter intermittently ignored mid-session and agent cwd drifts into worktrees — use absolute paths, explicit `cd`, or `Bun.spawn({cwd})`; false '22 files failed' vitest reading traced to this (reported via xd://report_issue) (UI-epoch-11, UI-epoch-6, UI-epoch-2)
- Mock-invoke must copy arrays at the response edge — passing fixture arrays by reference makes in-place e2e mutation `Object.is`-equal to React state → permanent bailout/stale UI (gui-wave-epoch-1)
- Fixture placement: receipt rows in conv 1 broke diff/review-inbox assertions (MessageBubble renders them as chat bubbles); conv 3 is the receipt-fixture home (gui-wave-epoch-1)
- Shell `kill` builtin may refuse a PID (session process-tree ancestor); external `/bin/kill` bypasses (UI-epoch-6)
- Known gofmt debt in `loop_audit.go`/`loop_journal.go` (/server.go/server_test.go) retained by prior decision, left untouched (main-epoch-20, UI-epoch-10, UI-epoch-4)
- Full-suite green runs are the acceptance currency: `go test ./...` ~400–510s, vitest, tsc, playwright e2e counts tracked per session (main-epoch-20, UI-epoch-11, UI-epoch-8)
