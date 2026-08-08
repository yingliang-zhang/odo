# A2 Queue-Continuation + A4 Consensus Verdict — Tri-Model Review Brief

## 1. Task

Review commit `4f67d77` ("feat: A2-lite queue-continuation + A4-lite consensus verdict").

Two changes in one commit (7 files, +306/-40):

### A2-lite: Queue-continuation (fixes steering trust bug)

**Problem**: `steering.txt` was a dead path — `Adapter.Send` wrote to it but the OMP wrapper never reads it. UI showed steering messages as "delivered" but the agent never saw them.

**Fix**: Steering messages are now queued in `runMeta.queuedSteers`. When the run completes (`drainRun` sets `meta.finished = true`), if `queuedSteers` is non-empty and the run didn't error, a goroutine starts a continuation run with the queued texts joined as the prompt (verbatim, never LLM-summarized). The continuation gets fresh memory-layer injection and a new worktree.

Files:
- `internal/ipc/server.go`: `runMeta.queuedSteers` field, `handleSteering` rewritten (queue instead of dead `adapter.Send`), `drainRun` continuation trigger, `startContinuationRun` method
- `internal/ipc/server_test.go`: `TestSteering` updated to verify queue + `steer:true` payload (was checking for dead `steering.txt` file)

### A4-lite: Deterministic 2/3 consensus verdict

**Fix**: After `handleReviewDiff` collects N review results, `consensusVerdict()` computes a deterministic tally: ≥2/3 ACCEPT → "accept"; any REJECT → "reject"; otherwise "needs_fixes". No 4th model call. Journaled as `consensus_verdict` in the `review_action` event, returned as `Response.Consensus`, displayed as a badge in DiffViewer.

Files:
- `internal/ipc/protocol.go`: `ConsensusResult` type + `Response.Consensus` field
- `internal/ipc/server.go`: `consensusVerdict()` function + `handleReviewDiff` returns it
- `gui/src/types.ts`: `ReviewDiffResponse.consensus` field
- `gui/src/components/DiffViewer.tsx`: consensus badge in review-results section
- `gui/src/styles/app.css`: `.review-consensus` + `.review-consensus-label` styles

## 2. Review Criteria

**RC1: Steering queue correctness**
- `handleSteering` appends to `meta.queuedSteers` instead of calling `adapter.Send`
- Journal payload includes `steer: true` tag
- No active run: message journaled only (unchanged behavior)
- Verify: read `handleSteering` and confirm queue semantics

**RC2: Continuation trigger in drainRun**
- After `meta.finished = true`, checks `len(meta.queuedSteers) > 0 && !meta.errored`
- Copies queue to local var, clears `meta.queuedSteers` before spawning goroutine
- Only triggers on successful runs (not errored)
- Verify: read the continuation trigger block at end of `drainRun`

**RC3: startContinuationRun safety**
- Takes `s.mu.Lock()` (own lock — drainRun already released by this point since goroutine)
- Re-checks no active run for the conversation (race with user sending normal message)
- Respects concurrency cap (`resolveMaxConcurrent`)
- Creates fresh worktree, reads fresh memory layers, starts new OMP run
- On any failure: logs error, cleans up worktree, returns silently (no user-facing error)
- Verify: read `startContinuationRun` and check all error paths

**RC4: consensusVerdict logic**
- `threshold = ceil(2N/3)` computed as `(len(reviews)*2 + 2) / 3`
- Any REJECT → "reject" (short-circuits)
- ACCEPTs >= threshold → "accept"
- Otherwise → "needs_fixes"
- Empty reviews → "needs_fixes"
- Verify: trace the logic with N=3 (threshold=2), N=2 (threshold=2), N=1 (threshold=1)

**RC5: Protocol additions**
- `ConsensusResult` struct with `Verdict string`
- `Response.Consensus string` with `json:"consensus,omitempty"`
- `consensus_verdict` field in the journaled `review_action` event
- Verify: read protocol.go and the journal event construction

**RC6: DiffViewer consensus badge**
- `consensus` state initialized to null
- Set from `resp.consensus` in `runReview`
- Rendered as `verdict-badge verdict-${consensus}` + label showing reviewer count
- Only shown when `reviews.length > 0 && consensus` is truthy
- Verify: read the JSX in the review-results section

**RC7: Test correctness**
- `TestSteering/active_run_queues_the_steer_text` verifies:
  - Steer event is journaled as user_message seq 2
  - agent_done appears in event types
  - Steer payload has `steer: true`
- `TestSteering/no_active_run_journals_only` unchanged
- All other Go tests still pass
- 43/43 E2E pass
- Verify: read the test and confirm it validates the queue behavior

**RC8: Dead code removal**
- `adapter.Send` is no longer called from `handleSteering`
- `steering.txt` write path in `omp.go` still exists (dead but not removed — removing it would change the Adapter interface)
- `strings` import in server.go still needed (used elsewhere)
- Verify: check if any dead imports or unused functions were introduced

## 3. Instructions

1. Read the changed files in the live repo.
2. For each RC1–RC8, independently verify.
3. Check for race conditions in the continuation goroutine.
4. Verify the consensus threshold math.
5. Confirm no regressions in existing behavior.
6. Give ACCEPT or REJECT per criterion, then an overall verdict.

Write your complete analysis as text. Do NOT write files to the repository.
