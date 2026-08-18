# Conversation Summary

## Task 1: Audit — visible⟺logged residual (A-P0 #2)

**Type:** Read-only audit. **Outcome:** `no residual — assertion coverage is complete`; zero files changed.

**Verified call sites (all `moa.Query/QueryWithTools/QueryWithImages` in `internal/ipc/`):**

| Path | Model call | Journal receipt | Timing |
|---|---|---|---|
| `/panel` (`handlePanelQuery`) | `server.go:2364` | user_message receipt + prompt bytes | Before call + `assertSlashReceipts` fail-closed |
| `/vision` (`handleVisionQuery`) | `server.go:2591/2593` | `image_sha16[]` + `image_bytes` receipt | Before call + assertion |
| Manual `review_diff` | `server.go:2215` | `EventReviewAction{moa_review, diff_id, patch_sha16, risk}` | After call, before consumption |
| Auto-land gate | `autoland.go:367` | `moa_review{actor:auto_panel, patch_sha16}`; journal failure → NOT landing | After call, before landing |
| Skill gate | `skills_gate.go:94` | reviews via `skill_gate`/`memory_propose` rows | After call, before fold |

Also verified: send/steer/revise/parked all routed through `assembleRunPrompt` → `assertPromptReceipts` (W2 assertion); distill inputs are journal-derived; curator markers pin `notes_read[{name,sha16}]`; learner anchored by distill `note_sha`. Cross-checked `NewClientFromEnv` (3 production sites, all covered).

**Deferred (not gaps):** byte-exact receipts → R-W1.5 territory; learner memory-file hashing → R-W8.

## Task 2: Implement R-W1.5 — `request_sha16` + `request_bytes`

**Semantics:** receipt recorded by the moa client at `post()` marshal point — only the client holds actual wire bytes. SHA-256 folded across all request bodies in order; bytes = total count. Escalation re-sends and tool-loop rounds fold into the same pair. Error returns carry no receipt.

**Code changes:**
- `internal/moa/client.go` — `Result` += `RequestSHA16`/`RequestBytes` (omitempty); `requestLedger` through `post()`; stamped in `oneShot` and `QueryWithTools`. Dead-weight cleanup (removed helper field/structural interface, stdlib `hash.Hash`).
- `internal/ipc/protocol.go` — `ReviewResult` += both fields (one point covers all three review lanes).
- `internal/ipc/server.go` — `reviewWithModel` stamps legs; `PanelResult` += fields, filled in `/panel` fanout; infra/error legs omitted by convention.

HTTP**: additive JSON keys only, ADR-0002 immune, no schema migration.

**Verification:**
- `go build ./... && go vet ./...` clean.
- New `TestRequestReceiptWireExact` (moa): single post, escalation fold, tool-loop fold, error leg — expected digest recomputed independently from server-captured bytes.
- `TestReviewDiff` upgraded (Response + journaled row byte-exact vs stub wire body); `TestPanelRouting` extended via `moaMockServerCapturing` (existing callers untouched).
- Full `go test ./internal/ipc/ -count=1` green (330s).
- Design doc row 3 flipped to landed (worktree `6a7f98f2`).

**Boundary decisions (deliberate non-changes):** vision excluded (row 3 scope; `image_sha16` already anchors bulk bytes); `gui/src/types.ts` untouched (additive keys transparent to TS).

## Session events

- Post-implementation, `review_action: conflict` and `auto_land_blocked` events fired — the change appears blocked from landing.

## Open loops

- **Landing blocked:** `conflict` + `auto_land_blocked` review actions on the R-W1.5 worktree (`6a7f98f2`) after tests passed — the change is verified but not landed; needs conflict resolution or user decision.
- **Vision receipts optional lane:** `request_sha16`/`request_bytes` trivially available for the vision path if wanted later (excluded from R-W1.5 scope by design).
- **R-W8:** learner memory-file input hashing remains scheduled under R-W8 (learner/curator→moa migration), untouched.