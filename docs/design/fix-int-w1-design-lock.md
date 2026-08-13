# DESIGN LOCK — fix-INT Wave 1 (accept pipeline correctness)

Locked by orchestrator from tri-model proposals (K3/GLM/DSF, 2026-08-13).
You implement this EXACTLY — no design freedom. If a locked step proves
wrong against the code, STOP and report; do not improvise.

## Locked decisions

- **D1 (3/3):** Atomic base-freshness check inside `handleDiffAction` under
  `acceptMu`, in the accept branch, AFTER the unmerged-index check and
  BEFORE `CapturePatchBaseline`. One spot covers both actors. Check-to-apply
  window: zero for daemon writers (CommitPaths runs under the same mutex).
- **D2 (3/3):** autoLand's entry check (autoland.go:227–239) STAYS as a cheap
  pre-spend filter, reason `base_stale` unchanged; detail string gains the
  marker ` — at pipeline entry (before verify/panel spend)` (2/3; DSF's
  keep-unchanged dissent recorded). Header comments updated: entry = filter,
  final = authoritative check in handleDiffAction.
- **D3 (K3 placement, merges all):** Freshness refusal surface =
  sentinel error. `var errBaseStale = errors.New("base stale")` in
  server.go; `handleDiffAction` wraps it (fmt.Errorf ... %w) and does NOT
  journal for either actor. The auto caller (`autoLand`, at the
  handleDiffAction call site ~autoland.go:350) matches via
  `errors.Is(err, errBaseStale)` and journals
  `journalAutoLandBlocked(ctx, d, "base_stale_at_land", err.Error()+
  " — the verify and panel attested the pre-drift tree; the diff stays
  pending for the human", reviews, cv)` — the completed panel rides the
  blocked row as advisory evidence (visual-gate precedent, autoland.go:307).
  (GLM/DSF's in-handler journaling placement REJECTED: handleDiffAction has
  no reviews in scope — the panel evidence would be lost.)
  Reason name `base_stale_at_land` (2/3 over K3's `base_stale_drift`).
  CONTRACT CHANGE: new auto_land_blocked reason.
- **D4 (3/3):** `d.BaseSHA == nil || *d.BaseSHA == ""` → SKIP the check
  (grandfather pre-v2 journal rows; the auto path already fail-closes nil
  base as `base_unresolvable`). `git.CurrentSHA` error → fail closed.
  Human refusal error names BOTH shas + remediation; NO journal row for the
  human refusal (2/3 K3+GLM — unmerged-index refusal precedent; DSF's
  agent_error-mirroring dissent recorded). No force/override flag this wave.
- **D5 (1/3+ adopted, DSF unique):** accept/reject review_action payload
  gains additive keys `base_sha` (diff's stored base, "" if nil) and
  `head_sha` (HEAD the action operated on; read where the accept path
  already knows it — reuse the freshness head for accept; reject reads it
  or records ""). CONTRACT CHANGE (additive): consumers ignore unknown keys
  (verified ComputeAutonomy/ledger/audit iterate generically).
- **D6 (item 3, DSF+GLM merge):** TWO constants change:
  - `internal/moa/client.go:52` `baseRequestTimeout` 300s → 900s, comment
    rewritten: floor moves, maxTok/120 headroom unchanged
    (900 + 65536/120 = 1446s worst single request).
  - `gui/src-tauri/src/lib.rs:45` `REVIEW_READ_TIMEOUT` 330s → 900s — the
    manual review_diff bridge ceiling (the item's literal "330s"; K3/GLM
    both missed it — do not skip this).
  NO prefs line this wave (K3's scoped TimeoutFloor + `review_timeout:`
  dissent deferred — recorded). No distill change (separate adapter path).

## Exact edits

### internal/ipc/server.go
1. Near `handleDiffAction`: add sentinel + helper with substantial comments
   (match the file's reason-heavy voice):
   - `errBaseStale` sentinel (comment: wrapped so the auto-land caller
     errors.Is-distinguishes drift from apply failure).
   - `func (s *Server) checkBaseFresh(d store.Diff) error`: nil/empty
     BaseSHA → nil; CurrentSHA err → wrap; head != base →
     `fmt.Errorf("accept_diff: main HEAD %s drifted from diff base %s — this diff was judged (and auto-land verified/panel-reviewed) against a tree that no longer exists; re-run the task on current HEAD or reject the diff, which stays pending: %w", head, *d.BaseSHA, errBaseStale)`; else nil.
2. `handleDiffAction` accept branch: call `checkBaseFresh(d)` after the
   unmerged-index check (~line 1481), before baseline capture. Refusal:
   `return Response{}, err` — diff stays pending, no journal, no status
   change.
3. review_action payloads (accept AND reject paths): add `base_sha`
   (diff's or "") and `head_sha` (the head read in step 2 for accept;
   for reject, read head via git.CurrentSHA best-effort, "" on error).
4. `autoLandMu`/`acceptMu` nesting comment if present (~server.go:111):
   note nesting `autoLandMu → acceptMu`.

### internal/ipc/autoland.go
5. Entry check detail (line ~237): append
   ` — at pipeline entry (before verify/panel spend)`. Reason stays
   `base_stale`. Header comment: freshness gate entry/final split.
6. Land call site (~line 350): after the review_action evidence row:
   ```go
   if _, err := s.handleDiffAction(ctx, d.ID, "accept", autoActor); err != nil {
       if errors.Is(err, errBaseStale) {
           s.journalAutoLandBlocked(ctx, d, "base_stale_at_land",
               err.Error()+" — the verify and panel attested the pre-drift tree; the diff stays pending for the human", reviews, cv)
       }
       log.Printf("auto-land: accept diff %d: %v", d.ID, err)
   }
   ```
   (Check the surrounding existing code — it already has a variant of this;
   replace/extend to add the sentinel branch. Import "errors" if absent.)

### internal/moa/client.go
7. Line 52: `baseRequestTimeout = 900 * time.Second`; rewrite the comment
   (900s floor for max-effort review legs; deadline = floor + maxTok/120;
   1446s worst request; raising the base moves the floor, not the ceiling).

### gui/src-tauri/src/lib.rs
8. Line 45: `REVIEW_READ_TIMEOUT: Duration = Duration::from_secs(900);`
   — comment: matches the daemon's 900s review-leg floor (the bridge must
   not cut a leg the daemon would still serve).

### docs/milestones/m16-auto-land.md
9. One paragraph: entry `base_stale` (pre-spend filter) vs final
   `base_stale_at_land` (authoritative check in handleDiffAction).

## Tests (exact names — new files NOT allowed; extend existing)

- internal/ipc/server_test.go:
  `TestAcceptBlocksStaleBase` (drift then accept → error names both SHAs,
  still pending, no accept/conflict row),
  `TestAcceptFreshBaseProceeds`,
  `TestAcceptNilBaseGrandfathered` (accept ok, payload carries base_sha:""
  + head_sha),
  `TestRejectIgnoresStaleBase` (reject succeeds on stale),
  `TestStackedPendingDiffsSharedBaseSecondBlocks` (accept #1 lands,
  #2 blocked stale).
- internal/ipc/autoland_test.go:
  `TestAutoLandBaseStaleAtLand` (panel stub commits a drift file INSIDE its
  handler then replies ACCEPT for every leg: assert exactly one blocked row
  reason `base_stale_at_land` with reviews attached + consensus present,
  moa_review row exists, zero accept rows, diff pending, main tree shows
  only the drift file);
  extend the existing entry-stale subtest: reason stays `base_stale`,
  detail contains `at pipeline entry`, and panel stub counter is 0;
  `TestHandleDiffActionStaleRefusalIsSentinel` (direct call with autoActor:
  errors.Is errBaseStale; NO auto_land_blocked row from the handler itself).
- internal/moa/client_test.go:
  update `TestRequestTimeout` pins (0→900s; 65536→1446s);
  new `TestRequestTimeoutFloor` (≥900s for budgets {0,4096,16384,32768,65536}).

## Verification (must all pass before you report done)

```
cd ~/Projects/odo
go build ./... && go vet ./internal/...
go test ./internal/moa/ -count=1
go test ./internal/ipc/ -count=1          # full suite ~300s; do NOT shorten -run
cd gui/src-tauri && cargo check 2>&1 | tail -3
```

## Hard rules

- NO `git add` / commit — the orchestrator commits after tri-model review.
- Touch ONLY the files listed above. Do NOT touch memory/, wiki/, gui/src/
  (frontend), m17/m18 docs, or the working tree's uncommitted user files.
- No new deps, no new files. Keep edits minimal and in-voice.
- Existing suite must stay green — if a locked step breaks an existing
  test, the lock is wrong: STOP and report the conflict.
