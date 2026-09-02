# Diff #150 repair: restore weakened test assertions, then panel-revision doc fixes

## Context

Diff #150 (turn-fork + subagent feature, patching #149) passed verify but was suspended by the auto-land panel (`panel_minority_reject` — one REJECT from reviewer GLM; D7 policy keeps the chain open on a single dissent). Disqualifying finding: in `internal/ipc/fork_test.go` `TestForkConversationRefusals`, the below-floor case's exact error assertion was deleted and all three refusal cases were genericized to `!resp.OK && resp.Error != ""` — an oracle weakening, which scores REJECT under the standing gate rule (pipeline-source diff: any weakening of the verify oracle). Task constraint: restore, don't weaken; handler code is correct (confirmed by both accept-side reviewers) and must not change.

## Key decisions

- **Restore only what was lost; invent nothing.** Checked #149's archived patch (`.odo/diffs/6a98435c-….diff`): the original refusal table's three cases all asserted only `!resp.OK && resp.Error != ""` — past-end and missing-conversation never had content assertions, so they stay generic. The below-floor case (from_seq 0) got its content assertion back per instruction, grounded in the handler's deterministic refusal string at `fork.go:46` (`from_seq %d is below the journal floor`).
- **Handler code untouched across both rounds** (floor-check-first, deterministic).
- **Both of GLM's optional minor findings fixed** (each mechanical and safe): receiver shadowing renamed; fail-closed guard added.
- **Fail-closed guard rationale:** when `PatchPaths` fails to parse, `accept_diff`'s parse gate would inevitably reject the patch — registering it via `InsertSubagentDiff` would leave a permanently stuck proposal row; so set `done["error"]` and skip registration, symmetric with the adjacent memory-path rejection.
- **Round-2 fixes are doc-only accuracy fixes** — comments aligned to actual behavior, zero behavioral change.
- **`ListWorkstreams` fork provenance deferred:** it binds only the active conversation, so provenance disappears after an epoch flip; reviewer assessed it UX-cosmetic and non-blocking; a fix needs join-semantics changes — out of scope, recorded as follow-up.
- **Stage-and-stop both rounds:** full gate deliberately not run; the staged tree is handed to panel re-review / auto-land.

## Code changes

Final staged state: 18 files (6 added, 12 modified), +2220/−34, zero commits, no unstaged residue. Worktree base HEAD `7f4fc61`. Round 1 applied #150's archived patch (18 files, incl. its round-2 fixes); round 2 re-applied the reviewed diff (journal diff #151, `.odo/diffs/6a9851b4-….diff` = #150 full content + round-1 repairs) via `git apply --3way`, verifying `sum` rename, `patch unparseable` guard, and below-floor assertion were present first.

- `internal/ipc/fork_test.go`
  - `TestForkConversationRefusals`: table gains a `wantErr` field; the below-floor row asserts `strings.Contains(resp.Error, "below the journal floor")`; the other two refusal rows keep the generic check (matching #149's original).
  - Test doc (~lines 122–124) rewritten to describe actual coverage — three refusal cases, below-floor content assertion, no lane created on refusal. It had claimed a "live-run fork admitted" case the test body never covered.
- `internal/ipc/subagent.go`
  - ~279: local `s` in `if s, ok := payload["summary"]` renamed to `sum` — was shadowing the Server receiver `s`.
  - ~333: fail-closed guard — unparseable `PatchPaths` sets `done["error"]` and skips `InsertSubagentDiff`.
  - ~233: `drainSubAgentsLocked` doc corrected from `""` to `(0 = all conversations, …)` — both call sites (pollLocked, liveness tick) pass the int64 `0` sentinel; the body tests `conversationID != 0`.

## Verification

- `gofmt -l internal/` clean; `go build ./... && go vet ./...` pass (both rounds).
- `go test ./internal/ipc/ -run 'TestFork|TestSpawnSubagent' -count=1`: 8/8 PASS (round 1), green (round 2) — includes the restored-assertion `TestForkConversationRefusals`.
- `git diff --cached --check` clean; pure staged state both rounds.
- Full gate not run, per the stage-and-stop instruction.

## Incidents (both closed)

- Round 1: an edit mangled the imports-block indentation in `fork_test.go`; fixed immediately.
- Round 2: an edit with the wrong separator wrote a malformed one-line doc at `subagent.go:233`; fixed by byte-level replacement and re-read; gofmt/compile pass confirms closure.

## Open loops

- Auto-land chain for #150: the staged 18-file round-2 revision awaits panel re-review / inbox triage (accept or reject); the full gate has not been run. Last logged panel action after delivery was `auto_revise_product`; its verdict is not visible in this transcript.
- Follow-up: `ListWorkstreams` fork provenance disappears after an epoch flip (active-conversation-only binding); needs a join-semantics change.