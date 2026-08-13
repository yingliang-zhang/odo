# DESIGN LOCK — fix-INT Wave 2 (journal/memory semantics)

Locked by orchestrator from tri-model reconstruct-then-design (K3/GLM/DSF,
2026-08-13). All four items verified against HEAD `86b2351`. Two waves of K3
implementation dispatches (A: items 1+3, B: items 2+4), then one 3-way blind
review of the unified diff, then one commit.

Dissent recorded (verbatim positions kept for the audit trail):
- GLM Item 1: "exclude only {auto_revise_round, auto_land_blocked} as
  mechanics; keep moa_review/accept actor-agnostic — actor split belongs to
  the ledger, not the note." — overruled 2/3 (K3+DSF: panel evidence rows are
  transcript noise; blocked-with-reason IS the open-loop the note must carry).
- GLM Item 4: slash paths exempt (already self-receipted, fold later) —
  overruled 2/3 (K3+DSF: same assertion applies, shared verifier).

## Contract-change ledger (all ADDITIVE, optional-when-absent, no history
   rewrite, live-journal compatible; consumer-safety verified by ≥2 legs)

- `review_action{action:"distill"}` marker: + `omitted_count`,
  `omitted_first_seq`, `omitted_last_seq` — present ONLY when the cap dropped.
  `first_seq`/`last_seq`/`window_events`/`window_bytes` keep their full-window
  meaning (documented in a comment).
- `review_action`: new action value `"run_prompt"` (continuation/retry receipt
  anchor) with origin `"continuation"|"retry"` + the unified receipt payload
  map. Fold render EXCLUDES it (Item 1 exclusion list).
- `user_message`: + `prompt_sha16`, `recall_held_back` (optional int); the
  auto-revise `user_message` gains the unified receipt payload (marker
  `auto_revise` untouched).
- `memory_update`: new cause `"snapshot"` (layers `"memory"`, `"pins"`,
  `"user"` — reuse of `"pins"`, new usage for the other two), keys `content`,
  `sha`, `source`, optional `capped:true`; new cause `"snapshot_failed"`
  (fail-open hole marker).
- Receipt namespace: new synthetic key `odo#memory-map` (journal#todo
  precedent).
- `review_action{moa_review}` (ALL sites: manual review_diff, auto-land gate,
  skill gate): + `patch_sha16` (DSF compensation for the review-leg exemption).
- `/vision` user_message payload: + `image_sha16` []string aligned with
  attachments.

---

## ITEM 1 — fold whitelist actor:"auto_panel" (dispatch A)

Render-only, zero contract change.

1. `distillRender` (server.go, EventReviewAction case): parse `actor` and
   `reason` additionally (`Actor string `json:"actor"`, Reason string
   `json:"reason"`).
2. New predicate beside `isAdvisoryAgentText`:
   `foldExcludedReviewAction(action, actor string) bool` — true when
   action ∈ {"moa_review", "auto_revise_round", "run_prompt"} AND
   actor == autoActor. Excluded rows render "".
3. Kept rows render the one-liner with `"actor"` appended when non-empty and
   `"reason"` appended when the action is `auto_land_blocked`:
   `{"action":"accept","actor":"auto_panel"}`,
   `{"action":"auto_land_blocked","actor":"auto_panel","reason":"panel_mixed"}`.
   Human rows (actor == "") render byte-identical to today (no actor key).
4. `distillRenderSize`: unchanged structurally — it already measures
   `len(distillRender(ev))` for these kinds (M17 F1 byte-agreement holds by
   construction). Add a test pinning it.
5. memory_update rows: unchanged (ladder_suspended/resumed keep rendering).

Tests:
- `TestDistillRenderAutoPanelWhitelist` (learner_test.go): auto_revise_round
  & moa_review{auto_panel} → ""; accept{auto_panel} → one-liner with actor;
  auto_land_blocked{auto_panel} → one-liner with actor+reason; no-actor rows
  byte-identical (regression pin).
- `TestDistillRenderSizeWhitelistAgreement`: render "" ⇔ size 0 across the
  table.
- `TestDistillPromptExcludesPanelChurn`: journaled auto chain → prompt lacks
  moa_review/auto_revise_round, carries one accept line with auto_panel.

## ITEM 3 — memory.md / pins.md / user.md materialization (dispatch A)

Snapshot-on-change-detect, fail-open.

1. New helper `func (s *Server) journalRuleSnapshots(ctx context.Context,
   convID int64, events []store.Event)` in learner.go (beside sha16):
   - Targets table: {path `.odo/memory.md`, layer "memory"}, {`.odo/pins.md`,
     "pins"}, {`~/.odo/user.md`, "user"}.
   - Observed bytes = the EXACT injected bytes (same read the receipt hashes
     — capped read; set `capped:true` when the read truncated).
   - Last snapshot = newest-first scan of caller-fetched `events` for
     memory_update{layer:L, cause:"snapshot"} (TodoStateFromEvents precedent;
     no new store query, no cache).
   - First sight with non-empty content → journal. Changed sha → journal.
     Payload: {layer, cause:"snapshot", source:path, content, sha:
     sha16(content), capped?} (no before key — derivable).
   - AppendEvent failure → best-effort second append {cause:"snapshot_failed",
     layer, detail}; the send proceeds (appendLedger precedent).
2. Call sites: `runMemoryLayers` (covers send/continuation/retry/revise —
   events already fetched there) and `slashContextBlock` (both slash modes).
   Ordering: snapshots append BEFORE the user_message they serve (the seq-N
   receipt hash == latest snapshot.sha ≤ N).
3. Consume-sha in tests only: assert snapshot.sha == the receipt entry on the
   paired user_message for that source.

Tests: `TestRuleSnapshotOnChange` (memory/pins/user; first-sight, unchanged
no-op, hand-edit → new row), `TestRuleSnapshotReconstruction` (A→B→C,
content-at-seq), `TestRuleSnapshotFailOpen` (AppendEvent failure still lets
the send return; snapshot_failed attempted), `TestSlashSnapshotCoverage`.

## ITEM 2 — distill cap-drop fact (dispatch B)

1. `distillPrompt(events) (string, omission)` where
   `omission{count, firstSeq, lastSeq int}` zero when none, computed from the
   SAME capEvents call that cuts the tail (threaded, never recomputed).
   Prompt's existing omission declaration line stays.
2. `distillCore` marker payload: when count>0 add `omitted_count`,
   `omitted_first_seq`, `omitted_last_seq`. Comment: full-window first/last_seq
   keep epoch-window meaning; omitted_* name the held-back prefix.

Tests: `TestDistillPromptOmission` (struct equals the prompt line's numbers;
zero under budget), `TestDistillMarkerJournalsOmittedSeqs` (>256 KiB window
→ three keys; control → keys absent).

## ITEM 4 — model-visible ⟺ logged assertion (dispatch B, with Item 2)

1. Close the production gap: `memoryLayers` adds
   `ml.receipt["odo#memory-map"] = sha16([]byte(ml.memoryMap))` when non-empty.
2. Unified receipt payload builder
   `promptReceiptPayload(ml, prompt) map[string]interface{}` → {receipt,
   recall, replay sub-receipt (incl dropped_seqs), total_prompt_bytes,
   prompt_sha16}. handleSendMessage journals it; the revise user_message
   gains it (marker preserved); slash keeps slashUserMessagePayload (already
   mirrors).
3. `assertPromptReceipts(ml, prompt) error` (new, fail-closed):
   - every non-empty layer field has its receipt entry; recomputable hashes
     match (user/memory/pins/index: content-hash convention; wiki items +
     `#open-loops` + `journal#todo`: block-hash; cross-chunks and skill
     blocks: presence-only, documented as the honest local bound);
   - replay exempt by structural sub-receipt (documented);
   - `len(prompt) == total_prompt_bytes`, `prompt_sha16 == sha16(prompt)`;
   - exemption ledger in the doc comment: distill (pure f(journal)+Item 2
     keys), learner (Item 3 snapshots), curator (journaled artifacts), review
     legs (per-row patch_sha16 attestation), panel tool results (derived).
4. Fail posture per path — journal-first, assert, refuse BEFORE `ad.Start`:
   - send: user_message journaled, then assert, refusal via existing failRun
     agent_error (attempt + breach both on record);
   - continuation/retry (startFollowupRunLocked): assert; on failure journal
     agent_error{prompt receipt assertion failed} — no silent drop;
   - revise (startReviseRun): assert after build; failure →
     dropReason="receipt_assert_failed" → existing revise_spawn_failed ledger
     row (evidence journaled before any adapter start).
5. New assembler `assembleRunPrompt(ctx, wsName, convID, text) (prompt,
   receiptPayload, err)` = runMemoryLayers + buildPrompt + assert.
   - handleSendMessage: uses it; journals user_message with unified payload.
   - startReviseRun: uses it; extends its user_message payload.
   - startFollowupRunLocked: uses it; journals
     `review_action{action:"run_prompt", origin:"continuation"|"retry",
     <unified payload>}` — NO user_message duplicate (chat surface discipline).
   - slash (slashctx): shared verifier over the receipted blocks before the
     moa call; failure → refuse query + agent_error paired with the already
     journaled user_message.
6. `recallWikiNotesCapped` returns the held-back count (computed today,
   discarded); user_message payload `recall_held_back` when >0.
7. `patch_sha16` on every moa_review append (manual review path, auto-land's
   fanout journal, gateSkillProposals) — sha16 of the diff bytes at hand.
8. `/vision` payload `image_sha16` aligned with attachments order.
9. Reflection test: enumerate memoryLayers fields; each must be classified
   (receipted-key + hash convention, or exempt-with-reason) in the assertion's
   classification table; assert table exhaustiveness.

Tests: `TestMemoryLayersReceiptCoverageReflect`,
`TestAssertPromptReceiptsDetectsGap` (missing entry/hash mismatch/total
mismatch), `TestSendJournalsPromptReceiptClosure`,
`TestSendFailsClosedOnReceiptBreach` (stub adapter records zero starts),
`TestContinuationJournalsRunPrompt`, `TestReviseUserMessageCarriesReceipt`,
`TestAssertionFailClosedSend`, `TestVisionImageShaReceipts`,
`TestRecallHeldBackJournaled`, patch_sha16 present on all three moa_review
sites.

## docs

- `docs/milestones/m18-settlement-ladder.md`: one short appendix listing the
  new journal keys/actions/causes (point to this lock) — keep it 10 lines.

## Hard rules

- No git add/commit. Touch ONLY: internal/ipc/{server.go, settle.go,
  recall.go, learner.go, slashctx.go, autoland.go, review.go, skills_gate.go}
  + their *_test.go peers + docs/milestones/m18-settlement-ladder.md (+ new
  file receiptassert.go OPTIONAL — prefer co-locating beside buildPrompt).
- Do NOT touch memory/, wiki/, gui/, m16/m17 docs. No new deps.
- If a locked step contradicts the code, STOP and report (W1 precedent).
- Implement dispatch A fully first, verify (build/vet/focused tests), then
  dispatch B, verify full suite:
  `go build ./... && go vet ./internal/... && go test ./internal/ipc/ -count=1`
