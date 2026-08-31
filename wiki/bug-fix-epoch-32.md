> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# D9-W4 — Learning Control Plane lifecycle core (candidate / lint / frozen replay / stages / canary seam)

Wave W4 of the D9 design lock (`docs/design/d9-learning-control-plane-lock.md`), built on the landed W3 layer (`learning_episode` / `learning_store` / `learning_status`). Scope: candidate lifecycle, LLM-free lint and frozen replay gates, journaled stage machine, canary injection seam, and the never-score-own-changes predicate. Constraints honored: Go `internal/ipc/**` only, GUI untouched, no Tier-0/supply-chain files, `settle.go` untouched, zero LLM in any gate, `planMemoryApply` reused not duplicated.

## Key decisions

- **Candidate origin & divert**: candidates arise only from accepted learner-batch proposals on the existing `autoApplyProposals` path when `learning_stages:` pref is ON (default ON; OFF = legacy W3 behavior with zero candidates). Accepted proposals are diverted into a candidate row instead of writing memory.md directly. Retract deltas stay human-originated only (via `odo learning from-retracts`).
- **Hash & idempotence**: `artifact_hash` = sha256 over {version, scope, base_sha16, base_source_seq, delta, content} with content from `planMemoryApply` (same pure function replay uses). Dedupe semantics split in two and tested separately: crash-replay with the same `base_source_seq` is an idempotent no-op; a new distill head legitimately produces a new candidate row.
- **Write ordering**: journal row is written before the jsonl row — backfilling `CreatedSeq` after the jsonl append would have broken append-only dedupe.
- **Lint gate** (LLM-free): memoryCap bounds, `memoryLineRe` format, dup-free, retract targets exist in current memory.md, evidence note exists under `wiki/`, R2 freeze-set (texts rolled back within 3 main-lane epochs rejected). Per-line journaled rejects.
- **Frozen replay** (LLM-free): freeze manifest `learning_freeze{bounds, input_sha256}`; head = newest distill marker, tail = marker 8 epochs back. Missing inputs ⇒ verdict `unverifiable` = FAIL (fail-closed). Criteria (a)–(h) all deterministic, including double-execution byte-identical (e), ≥1 prevented-harm anti-vacuity (f), friction ≤ 3× prevented-harm (g), `loosened == 0` (h). `unverifiable` with `deterministic=true` is correct semantics — determinism is independent of the verdict grade.
- **Stage machine**: journaled transitions only (`candidate → shadow` on lint+replay all-pass; `candidate → dropped` on any fail with per-check evidence; `shadow → canary` gated on ≥3 main epochs + re-pass on grown slice + free single canary slot, with `shadow_queued` so candidates never stick; `any → frozen` on R2). Stage status is journal-fold only, never stored on the jsonl row. Shadow checkpoint re-runs replay at each main-lane distill against the grown slice.
- **Canary seam**: `learning_canary_fraction` pref (default 0.25, ceiling 0.5, 0 = off); deterministic interleave `ordinal % M == 0` per lane, zero RNG. Assignment journaled before the run; steer-continuations inherit the chain's cohort. Injection substitutes candidate content inside `runMemoryLayers` — the existing `.odo/memory.md` receipt key cohorts the run, zero new receipt keys. Canary outcomes excluded from live rows and baseline (reported as `canary_outcomes`). With W5 not landed, no candidate can reach canary in this wave; the seam is exercised via test fixtures that force-stage a candidate.
- **Never-score-own-changes**: one shared `excludedFromScoring` predicate (gate-source ∪ memory-path ∪ learning-plane paths) mirrored into the rules-audit baseline/cohort resolution. Mid-implementation design correction: **live** rules-audit keeps the legacy truth posture for unreadable patch files (switching live to fail-closed would have zeroed all existing fixtures/old journals); fail-closed unknown⇒excluded applies **only** to frozen replay, matching the lock's "over a frozen slice" wording.
- **Hot path**: send returns at zero cost when `candidates.jsonl` is absent/empty.

## Code changes

New (six core files in `internal/ipc/):
- `learning_candidate.go` — candidate creation + divert hook from `autoApplyProposals`.
- `learning_lint.go` — lint + security checks + R2 freeze fold.
- `learning_replay.go` — frozen replay engine (rewritten once to fix duplicate scan, convID fidelity — harmful tuple needs ≥3 conversations — d-check logic, full provenance resolution, file-level slice type).
- `learning_stages.go` — stage fold, journal helpers, shadow checkpoint.
- Canary seam file — fraction pref, deterministic interleave, cohort journaling, injection substitution.
- Shared scoring-predicate file — `excludedFromScoring`.

Modified:
- `memory_autogate.go` — `applyResolvedBatch` gained a `diverted` parameter (candidate path bypasses memory.md write).
- `server.go` — `runMemoryLayers` / `memoryLayers` wired to the canary substitution.
- W3 status reporting switched to the shared stage fold (global id ordering across lanes).
- `rules_audit.go` — excludedFromScoring mirror in cohort/baseline resolution, with the legacy live-audit posture correction.
- Distill tail wiring + whitelist for the new `learning_*` journal event family.
- Send hot-path early return.

Tests (four new test files):
- Lint table (each reject reason), security, freeze fail-closed, bounds.
- Replay fixtures for each criterion (a)–(h), freeze fail-closed on missing input, double-exec determinism, hash idempotence.
- Lifecycle: divert e2e, stage transitions, shadow checkpoint, whitelist, legacy-pref-off `armedDistillRig`.
- Canary seam: interleave determinism, chain inheritance, stage-flip mid-chain, audit isolation.

Fixture fixes along the way: empty-content snapshot lines must not be written; marker seq must avoid colliding with journal extras; audit's current-memory read requires memory.md present on disk.

## Verification

- Focus gate `go test ./internal/ipc/ -run 'Learning' -count=1` run repeatedly while iterating fixtures; final tails not quoted in this record.
- Full `internal/ipc` suite launched in background (~9 min) — first attempt died with a session hangout, relaunched under `setsid`; result not captured before the session ended.
- Worktree intentionally left dirty per task instructions; per-item report with gate tails and a one-line risk note not yet delivered.

## Open loops

- Confirm the background full `go test ./internal/ipc/` suite (setsid relaunch) passed; capture tails.
- Run the broader `go test ./... -timeout=14m` nohup gate (task requires it after the ipc suite; not shown as run).
- Deliver the owed final report: per-item status + gate tails + one-line risk note.
- W5 (promotion shadow → canary → project_active) is unlanded; until then the canary seam can only be exercised via forced-stage fixtures — schedule the W5 wave.
- No GUI gates were run (`gui/**` believed untouched); quick tsc+vitest pass only if any gui file turns out to have moved.