# odo — landWG Lifecycle Landing (diff #74) & Boot Journal Replayer (diff #75 lineage)

Scope note: covers journal seq 4430–6439 only; earlier rounds of the same efforts are referenced where the visible window cites them.

## Part B — landWG accept-tail lifecycle hardening: LANDED

**Outcome:** auto-panel accepted diff #74 (seq 5542). Landed as `7d17431` on main. Base: `92dc74d`.

**Key decisions**

- **Split strategy:** the repeatedly rejected mega-diff (21 files/132 KB) was split A/B. This line pursued Part B only — the accept-tail lifecycle fix — with all replay/journal-outbox content hard-excluded.
- **Structural invariant over comment invariants:** every run pre-adds a lifetime pin (`landWG.Add(1)`, `landPinned=true`, map registration) inside one `s.mu` hold in `bindRunLocked`, so any drain-tail Add provably precedes `landWG.Wait`.
- **Seal for late admissions:** `releaseUnfinishedRunPins()` became `sealLandAndReleasePins()` — closing admissions (`s.landSealed`) and sweeping pins in a single critical section; post-seal `bindRunLocked` refuses (bool return), unwinding pipelines like agent-start failure while leaving diffs pending for boot recovery. A late bind degrades to a refusal, never a hang.
- **Pin-release ordering:** `retireRunCore(…, releaseLandPin bool)` / `retireRunInDrain` split; drain-internal retires skip release — pin release is owned solely by drainRun's top defer, after all tail registrations.
- **Proof via test seams:** production-nil `drainTailGate` and `diffActionGate` replace timing-window tests; mutation experiments (delete pin Add; mid-branch pin release) verified both fail the drills.
- **Teardown idempotence:** `stopflight sync.Once` + `stopOnce(t)` helper; all pre-existing crash drills converted (gap found at `loop_test.go:1173`); post-migration census shows zero bare mid-test `rig.stop(t)`.

**Final landed shape:** exactly 11 files under `internal/ipc/` (+854/−34, 59,879 bytes) — autoland, autoland_test, design_moa_test, loop_run, loop_test, parked, parked_test, protocol, server, server_test, settle. No attestation doc. Frozen: `TestRecoverReFireAnchorsStoredGoal` assertions (join-infrastructure `defer` insertions only); protocol.go one-line comment re-anchor (panel-mandated).

**Why it took 7 rounds:** the code was K3-accepted from the start; rejections were proof-packaging failures — the panel reads only the diff itself. An evidence doc (`docs/panel-evidence/…`) was added, corrected (census truthfulness; two citation line numbers, including a 5368-vs-5369 tree discrepancy where the applied tree and panel's tree disagreed), and still failed — root cause was finally identified as a **goal/packet scope mismatch** (goal described a one-line doc fix; packet carried ~1250 lines of code). A mechanical repackaging run (zero code changes, doc excluded, corrected goal) passed immediately.

## Boot-time journal replayer for stranded memory/pins intents (IN REVIEW)

**Defect:** per-lane heals folded one conversation's events with predicate `sha == after → landed; sha != before → return`. A crashed-then-superseded receipt hits the silent-return branch — the intent is journal-consumed but absent from the projection forever, untraced.

**Locked design (panel-corrected):** once-per-boot, project-wide replay at `NewServer` (after orphaned-request recovery, before parked goals/serving). Per layer, only the **newest receipt** decides: disk==after → landed no-op (boot idempotence); disk==before → crashed mid-write → restore from receipt body; true-foreign → entry-merge add-only receipts (journal `heal_merged`), non-mergeable/retraction/whole-file → journal `heal_conflict` with embedded `stranded_body`. Exposed via `pending_counts.stranded_memory_ops` (conflict minus resolved, full-project fold), `resolve_heal_conflict` IPC (Resolve = overwrite with stranded body / Dismiss), GUI Memory-tab banner + Resolve/Dismiss buttons. Per-lane heals removed from the runtime path; old `cause:"recover"` rows stay invisible to the fold. Retraction-as-newest stays authoritative — no resurrection.

**REGENERATE fixes (cap-blocked auto-revise, consolidated):**
1. Legacy pin receipt (`AfterSHA==""`) = terminal landed boundary (`memReceipt.legacy` tombstone competing by eventID); new `TestMemoryReplayLegacyPinTerminalBoundary`.
2. Resolve routes by `Request.StrandedConversation` against the project-wide heal ledger (key `${conv}:${layer}:${seq}`), not the request conversation; `TestResolveHealConflictTwoLaneCollision`.
3. GUI `strandedOps` memo depends on `[events]`; lint suppression removed.
4. Two-lane coverage drill (`TestMemoryReplayTwoLaneNewestPerLayer`): retraction-as-newest, no resurrection, correct conflict attribution.
5. `memoryMergePlan` merged rules emit `reaffirmed: 1` (epoch is lane-local; don't fabricate recency).
6. Dev-mock resolve pairing, NewServer W6 comment repair, whitespace cleanup.

**Flake root-caused, not waved:** numbered banner text depends on `pending_counts` (refreshed only every 4th poll tick), vs 5s default expect timeout; switched to the 12 s `POLL` window → 15/15 repeat runs, then full suite 0 flakes.

**Gates (green at end of window):** `go build`, `go vet`, `go test ./internal/...` (ipc 517.6 s), `tsc --noEmit`, playwright 123 passed. 15 files staged.

## Open loops

- **Replayer packet await panel verdict:** the staged 15-file REGENERATE diff (#75 + FIX 1–6) had not yet been re-snapshotted/reviewed when the window closed.
- **Pre-existing boundary, deliberately out of scope:** a ladder-tail `fireLoopTick` can still `loopWG.Add` unjoined after seal (refused at its own bind; loop resumes from journal fold next boot) — recorded in completion notes, no tracking decision made.