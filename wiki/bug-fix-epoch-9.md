# Auto-Distill daily_cap Suspension Fix (epoch: cap feedback loop)

## Defect

When the auto-distill daily cap was reached, every subsequent activity re-ran the same cycle: urgent trigger → journal `memory_update{cause:"scheduled"}` → fire discovers daily cap → journal `memory_update{cause:"skipped", reason=daily_cap}` → window never folded → next activity re-fires. The bookkeeping rows themselves grew the eligible window they were measuring. Production journal evidence: after the cap was hit, 3786→3819 events / 497KB→565KB of pure scheduler noise accumulated while no distill could run — self-amplifying bookkeeping.

## Design decisions (owner-approved)

1. **Suspend once per day, not per event.** On the FIRST cap hit, journal exactly one `memory_update{cause:"cap_suspended_until", detail:"<RFC3339 earliest quota release>"}`; journal nothing further (no repeated scheduled/skipped rows) until the horizon passes.
2. **Single timer.** At most one pending timer, aimed at the earliest quota-release time (oldest counted distill exiting the 24h window). New activity while suspended does not reschedule.
3. **Bookkeeping excluded from the eligible window.** Scheduler-internal rows (`scheduled`, `skipped`, `cap_suspended_until`) no longer count toward the fold query's event/byte eligibility; the window measures agent/user activity only.
4. **GUI surface.** While suspended, the Memory/auto-distill status shows `今日额度已用完 · 预计恢复 <time>`; recovery time comes from the `cap_suspended_until` row, falling back to computed (oldest counted distill + 24h) when the row is absent (upgrade path).
5. **Resume without storm.** When the timer fires and quota exists, ONE normal scheduled row appears and the cycle resumes — no catch-up, no backfill of skipped triggers.
6. **Cap numbers unchanged** — trigger behavior only. Exclusions honored: no landWG/replayer code, no docs/panel-evidence or attestation files.

## Code changes

- **Store layer (`internal/...`)**: fold/eligible-window queries audited — scheduler-internal `memory_update` causes excluded from event/byte eligibility.
- **`internal/daemon/auto.go`**: quiescence helpers, suspension gate on the urgent-trigger path, single-timer scheduling aimed at quota release, resume path emitting exactly one scheduled row, updated docs.
- **`internal/daemon/server.go`**: `pending_counts` handler extended with a cap-suspension wire field (resume timestamp) for the GUI; fixed a splice-introduced `go vet` unreachable-code failure at server.go:6747.
- **Cache redesign mid-implementation**: the initial 30s-TTL negative peek cache masked the cap transition (drills 7/8, would also misbehave in production). Replaced with a cache of capped **positives only**, self-expiring at `resumeUnix`; below-cap derives run uncached (cheap — same handler already does a journal-wide fold).
- **GUI (`gui/src/...`)**: types for the new wire field, App wiring, suspended-state chip in the Memory tab, mock/fixture updates, new Playwright e2e specs covering the chip flow and the upgrade fallback.

## Verification

- `go build ./...`, `go vet ./internal/...`, `gofmt -l internal/` — clean.
- Drills (4 new + updated frequency-caps test): cap → N urgent activities journal ZERO extra scheduled/skipped rows, exactly one `cap_suspended_until` per day, single pending timer; window stops growing under bookkeeping but grows on real events; timer past horizon → exactly one scheduled row, distill runs, suspension cleared; old-journal upgrade path → GUI falls back to computed recovery time, no crash. Initially 7/8 (cache bug above), green after redesign.
- GUI: `npx tsc --noEmit` clean; vitest 166/166; Playwright full gate **125 passed (6.1m)**, including both new auto-cap specs.
- First full-gate run was killed by the 300s shell deadline mid-run; re-run with proper timeouts succeeded.

## Open loops

- Final full `go test ./internal/...` re-run result not captured in the transcript (was in flight at session end; build/vet and all drills were green beforehand).
- Post-fix production confirmation pending: journal self-growth (the 3786→3819 / 497KB→565KB scheduler-noise pattern) has not yet been re-observed as stopped on a live instance.