> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Control-Plane Hardening D1 — Implementation Summary

## Scope

Implement only **D1 [P0]** of the binding spec `docs/design/control-plane-hardening-lock.md`: replace the 10-file hand-written `protectedGateFiles` map (`internal/ipc/server.go:5528-5539`) with a structural, fail-closed gate-source policy. D2–D7 explicitly out of scope. Worked in an assigned worktree, clean at lock HEAD.

## Key decisions

- **Two-tier gate boundary** (new `internal/ipc/gatepolicy.go`):
  - Tier-0 (human-only, editing IS an exemption grant): `internal/ipc/gatepolicy.go`, `internal/ipc/gate_manifest.json`.
  - Tier-1 prefixes: `internal/ipc/`, `internal/store/`, `internal/git/`, `internal/moa/`, `internal/adapter/`; root `main.go` Tier-1 per ruling ①. `internal/modelspec`, `gui/` not protected; `cmd_*.go` stays Tier-1 via the ipc prefix.
  - `isGateSourcePath(p)`: case-folded prefix match over both tiers; `isGateTier0Path(p)`: Tier-0 membership.
- **Tracked manifest** (`internal/ipc/gate_manifest.json`): `{version, protected_prefixes, tier0_files, tier0_sha16, pinned_at, pinned_by:"human"}`; manifest's own hash slot is the empty string (self-reference excluded; Tier-0 status compiled in). Generated via the new re-pin CLI.
- **Startup drift latch** (`NewServer`, before serving): recompute sha16 of each Tier-0 file; mismatch/missing ⇒ journal `memory_update{layer:"gate_policy", cause:"gate_source_drift", detail, expected_sha16, actual_sha16}` + `s.gateDrift=true`. While latched, autoLand / loopFixPipeline / settleDraft majority valve refuse with `auto_land_blocked{reason:"gate_policy_drift"}` (fail-closed).
- **Human-only CLI `odo gate re-pin`**: recomputes hashes, rewrites manifest, prints commit instructions; never commits.
- **Accept-time guard** (`handleDiffAction`, before attestation branch): Tier-0 hit + `actor != ""` ⇒ hard error naming the file, diff stays pending; human (`actor == ""`) unaffected. Tier-1 hit + `!panelVerdictAttestsDiff` ⇒ existing block, predicate repointed to `isGateSourcePath`.
- **autoLandCheck** (`autoland.go:770`): pre-panel hard block on Tier-0 ⇒ `auto_land_blocked{reason:"gate_core_path"}`.
- **Mode A reroute** (`loop_run.go` loopFixPipeline): classification order memory prefix → Tier-0 (suspend `loop_suspended{cause:"risk:gate_core"}`) → supply-chain → Tier-1 via `s.autoLand(ctx, d, wtPath, goal, false, "")` verbatim (C8 inherit-never-fork). Deleted the verify-only landing path (`runVerifyGate` + `handleDiffAction(loopActor)`) for loop fixes.
- **Fold attribution** (`loop_journal.go` deriveLoopStates review branch): accept/blocked rows attribute to the open Mode A fix phase iff actor ∈ {loopActor, autoActor} AND a `loop_diff_bound{round}` row names the diff; no binding ⇒ no attribution (fail-closed).
- **Migration**: `protectedGateFiles` deleted; `isProtectedPath` = memory prefixes OR `isGateSourcePath`. `review_action{action:"gate_policy_check",...}` journaled once per start.
- **C0 purity guard**: `autonomy.go classifyDiff` stays memory-prefix-ONLY; gate-source hits NOT folded into C0 (would pollute the autonomy ladder). Pinned by dedicated test.
- **Renames**: `git.PatchPaths` includes pre-image paths (both-sides discipline, like `rejectMemoryPaths` at server.go:5550-5556) so a rename out of a protected prefix is gate-source on both sides.
- No `settle.go` edits needed.

## Debug findings during verification

- Case-fold pin actually lives in `m6_test.go`, not `server_test.go:1090-1127` as the task stated; updated there, stale comment deleted. `internal/ipc/server.go` flips to protected=true.
- Loop e2e rigs dark-launch the liveness drain ⇒ the fix run needs manual `pollDone` polling; initial stall was test-harness, not product.
- `waitLoop` calls Fatalf on timeout, so journal dumps after it never ran; SIGQUIT mid-stall gave the real goroutine stacks (no pipeline goroutine alive at 8s ⇒ pipeline finished without journaling).
- **Panel stub verdict format**: `"VERDICT: ACCEPT"` does not parse; canonical stub is `"ACCEPT\nlooks correct"`. Bad format spawned the revise ladder instead. Fixed all stubs; the fixpoint test's `"review"` case had been dead code under the old pipeline.
- `loopStallCheck` hard-coded `actor == loopActor`; post-reroute lands are `auto_panel`. Comparator re-gated on the same `loop_diff_bound` binding as the fold.

## Tests (all from lock, added/passing)

- TestIsGateSourcePathStructural — ipc/*.go ⇒ true; modelspec, gui/src, cmd_*.go ⇒ false; `INTERNAL/IPC/x.go` ⇒ true (case-fold).
- TestIsProtectedPathCaseFold — updated (location: m6_test.go).
- TestGateCoreRefusedForActors — actors refused on Tier-0, human lands.
- TestGatePolicyDrift — corrupted Tier-0 file ⇒ startup drift row + block; re-pin restores.
- TestLoopFixRoutesGateSourceThroughPanel — moa_review row BEFORE accept; no verdict ⇒ never landed.
- TestLoopFixSuspendTier0 — `loop_suspended{cause:"risk:gate_core"}`.
- TestLoopFoldAttributesPanelLandedFix — attribution with binding; none without.
- TestClassifyDiffC0Purity — gate-source hit does NOT make C0.

Also updated: loop fixpoint/stall/risk e2e fixtures (panel stub format, pollDone, stall comparator) and GUI fixtures under `gui/`.

## Verification status

- gofmt clean; `go build ./...` green.
- Gate unit tests: green (all eight above).
- Loop fixpoint + risk + stall subtests: green.
- GUI: typecheck + 172 vitest green.

## Open loops

- Full Go suite (`go test ./internal/ipc/ ./internal/...`) was launched in background; final green not yet confirmed in-trace.
- Final report of changed files + verbatim test results still owed to the user.
- `internal/ipc/gate_manifest.json` generated via `odo gate re-pin`; the re-pin + all D1 changes still need committing in the worktree.