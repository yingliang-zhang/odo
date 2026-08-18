# R-W1.5: Thread request receipts onto panel/review payloads

## Key decisions

- **Premise correction (load-bearing):** The task claimed `RequestSHA16`/`RequestBytes` already existed on the moa client's `Result` from R-W1 (commit `e2f8b61`). `git show` + repo-wide grep disproved this — R-W1 added only retries, typed errors, and the usage ledger (`InputTokens`/`WallSeconds`/`TokPerSec`). Client-side wiring was therefore built in full, not merely journaled through.
- **Receipt semantics = final-request convention** (matches existing usage-ledger/`Budget` convention):
  - Receipt computed at `post()`'s marshal point — the only location that sees wire bytes.
  - Retry chain: same body resent across attempts → one receipt covers the whole chain (pin proves byte-identity).
  - Budget escalation: body rebuilt → receipt points to the **final** request; superseded bodies visible via `Escalations` ledger.
  - Error returns: no receipt (no answer to attest) — consistent with the `patch_sha16` absence precedent.
- **sha16 dual implementation:** moa cannot import ipc (import cycle); 4-line helper duplicated with a "convention is the contract" comment, same sha256-prefix scheme.
- **Additive JSON keys only** (`omitempty`): ADR-0002 immune, no new event types, no schema migration, no gui/types changes.

## Code changes

| File | Change |
|---|---|
| `internal/moa/client.go` | `Result` += `RequestSHA16`/`RequestBytes`; `requestReceipt` type threaded through `post()` signature; `sha16` helper; `oneShot` records directly; `QueryWithTools` records final round via `lastRcpt` |
| `internal/ipc/protocol.go` | `ReviewResult` += two fields — all three journal sites (manual `moa_review`, auto-land, skill gate) serialize the struct directly, so fields propagate to every row |
| `internal/ipc/server.go` | `reviewWithModel` fills `rr`; `PanelResult` += two fields, `handlePanelQuery` fanout populates (absent on error leg); `agent_text` `models[]` carries automatically |
| `internal/ipc/panel_live_test.go` | Mirror fanout construction synced |

## Verification (all green)

- **New wire-exact pins** — stubs capture the real HTTP body, independently compute `sha16`, compare against journaled receipt:
  - `TestRequestReceiptWireExact` (moa, 5 subtests): single-shot / retry-chain byte-identity / escalation→final body / tool-loop→final round / no receipt on error.
  - `TestReviewWithModelJournals`: +accept-leg wire-exact subtest; infra subtest asserts receipt absence.
  - `TestReviewDiff`: mutex-guarded per-model body capture; Response equality incl. receipts; journaled `moa_review` row reviews[] per-leg `sha16(body)` match.
  - `TestPanelTruncationFlagged`: journaled panel receipt == post-escalation final body, ≠ first (truncated) body.
- Full suite: moa 0.7s, ipc 360s — green. `go build ./...` + `go vet` clean.
- Design doc row 3 flipped ✅ (referral `6a802429`; row notes the premise correction).

## Open loops

None.