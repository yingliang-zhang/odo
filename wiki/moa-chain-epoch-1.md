# Conversation Summary

Two landed work items from the odo harness resilience plan (2026-08-14 audit docs): **R-W1** (moa client resilience) and **R-W3** (learner/curator OMP→moa migration).

## R-W1 — moa resilience (`internal/moa/client.go`)

**Code changes**
- **Bounded retry**: `post` split into a retry loop (holds derived deadline across the whole attempt chain) + `attempt` (single round trip, returns typed `*Error`). Retryable: 429, ≥5xx, network errors, timeouts. Client errors (4xx, request-build failures) never retry — bad keys stay fail-loud.
- **Backoff**: max 3 attempts; 200ms × 2ⁿ with ±50% jitter via `math/rand/v2` (legacy `math/rand` import caught and fixed per project rule). `Retry-After` header honored at original value, capped at 30s (`retryAfterCap`).
- **Cancellation**: no retry on caller cancel/timeout; `fmt.Errorf("...: %w", ctx.Err())` preserves `errors.Is(err, context.Canceled)`.
- **Typed errors**: `Error{Status, Class, Message, RetryAfter}` with five `Class` constants (`rate_limit | server_error | network | client_error | timeout`). Repo-wide grep confirmed zero callers depended on the old `"moa: API returned %d"` string — format change declared safe.
- **Result ledger**: added `InputTokens / StopReason / WallSeconds / TokPerSec`; `finalUsage` helper populates in both `oneShot` and `QueryWithTools`. Purely additive, backward compatible. Token fields record the final request; wall time covers the whole logical call (retries + escalations). Tool-loop `Result` still deliberately drops `Thinking` (preserves /panel behavior).

**Verification**
- 21/21 hermetic pins pass (new: 500→200 retry, 429 Retry-After honored and capped, network-error exhaustion, context-cancellation no-retry; extended: 401 client_error single-attempt, usage metadata on 200). All use a `sleepRetry` seam — recorded backoff table, zero wall time.
- `go build ./...`, `go vet`, and full-repo `go test -count=1` green. ipc suite (332s) unaffected by retries on its 500-stub tests (~1s real backoff each).

**Anti-scope honored**: Anthropic protocol shape, base URL, auth headers unchanged; zero new deps; no SSE; no strict-truncation knob (that is R-W4 #4).

## R-W3 — learner/curator → `moa.Query` (behind prefs flags)

**Key decisions**
1. Flags `learner_via` / `curator_via`; absent or `omp` keeps the wrapper byte-identical. `resolveDistillVia`/`distillVia*` generalized into one shared `resolveVia(task, prefKey)` + `viaOMP/viaMoa` — single routing convention for all three routes (distill from R-W2, learner, curator).
2. Receipts use R-W1.5's `Result.RequestSHA16/RequestBytes` (recorded client-side at the marshal point), not R-W2's `prompt_sha16`. New `moaReceipt` struct with `journal(map, prefix)`; distill's existing bare keys kept untouched (landed, tested contract).
3. Shared `runMoaOneShot(ctx, task, prompt)`: direct `moa.Query`, truncation fail-closed, deadline = `moa.TimeoutForModel`.
4. Learner parse failure still returns the receipt (request already on the wire — auditable); transport failure carries none (R-W1.5 convention). Learner failure never fails distill (degrade semantics preserved).
5. Curator truncation/errors fail-closed **before** any page rewrite; manual and auto curate share the branch. Fold marker uses `learner_*` prefixed keys to avoid colliding with distill's bare receipt keys.
6. **ADR-0003 inv7 reworded** with DSF: cadence stays daemon-owned; direct-moa transport does not create a model write path (daemon owns all memory writes); inv1 untouched.
7. **GUI timeouts**: `DISTILL_READ_TIMEOUT` 2100→3300s, `CURATE_READ_TIMEOUT` 660→1560s (derived from the 1446s worst-case moa chain found in R-W1).

**Verification**
- Shared test stub `startPassMoaStub` captures the raw body; receipt sha16/bytes recomputed independently by tests (wire-exact discipline). 6/6 new pins green (learner moa/omp routes, curator moa route, truncation-fail-closed->escalation, omp-route triptwires).
- `go build ./...`, `go vet ./...` clean; `go test ./internal/ipc/ -count=1` green (365s, incl. R-W2 `TestDistillViaMoa` regression); `cargo check` (gui/src-tauri) green. Plan doc row 8 flipped to landed ✅.

## Open loops

- R-W1 review action `auto_land_blocked` (seq 103, reason `base_stale`) — the accept was recorded but the land was blocked on a stale base; no rebase/land event appears in this conversation.
- R-W3 was implemented and verified but no review/land event is recorded here — land status after the auto-panel is unconfirmed.
- R-W4 #4 (strict-truncation knob) explicitly deferred — cited as out of scope in both tasks.
- No user decision recorded on whether to ever flip `learner_via`/`curator_via` defaults from `omp` to `moa`; both ship opt-in only.