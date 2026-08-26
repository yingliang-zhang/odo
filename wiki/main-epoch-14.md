> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# /panel Core-Design Review and P0 Fix Batch

## Context

A 4-leg `/panel` review audited odo's core design — journal, workstream, memory layers, skills, orchestrator, panel, and loop subsystems — against the actual code. Verdict: spine healthy (pure-fold, journal-first evidence, daemon-exclusive writes, connect-vs-read retry split all verified), but four flagship defects found where documented design contracts disagreed with code, plus a learner gate-theater loop. User approved implementation ("开工吧"); all four P0 fixes shipped in one batch via parallel subagent slices.

## Key decisions

- **Gate-source diffs never land via majority valve.** Prompt/docs promised unanimity, but `settle.go`'s valve (`accepts*3 >= 2*len`) plus `panelVerdictAttestsDiff` accepting `majority_accept` allowed a 2/3 panel to land its own gate weakening. Fixed: gate diffs route to `ladder_suspended` (human accept fallback); `majority_accept` attestation path deleted.
- **Daemon-side liveness drain closes the C11 falsehood.** "GUI-closed loops continue" was false for agent-run phases (`drainRun` only called from `pollLocked`). Fixed: `runLivenessDrain` mounted in `NewServer` (2s default interval, atomic toggle, follows `startRig` dark-launch precedent).
- **Contradiction auto-retract demoted to advisory-only.** The 2-token-overlap heuristic mass-retracted notes (28 distills → 25 retracts; zero `unretract` uses in production). Fixed: pass now emits `memory_update{cause:"contradiction_candidate"}`; candidates no longer filter recall; retraction only via curator supersede.
- **New `odo retract <note> [reason…]` CLI** added because advisory-only left no retract emitter at all (curator supersede only marks file banners). Mirrors `odo unretract`: same-name validation, idempotent, byte-bound sha pair, reason in detail.
- **Learner instrumentation before gate changes.** Memory rule layer never closed in production (30 batches proposed, 0 applied; silent batch supersede; daemon binary 22h stale). Fixed before touching thresholds: journal `batch_superseded` rows, ledger `memory apply: accepted/rejected` lines, deploy staleness WARNING.
- **/panel consolidator: keep status quo, close open item.** Divergence is the product; inline consolidator adds latency and a second truncation contract, and a "complete-looking synthesis" deceives more than raw leg output. Only acceptable future form is lazy merge (single orchestrator pass over journaled answers, additive `agent_text{panel_merge}`), never in fan-out path.
- Explicit non-goals recorded: embedding recall, per-entry review queue, WAL checkpoint timers, fold memory cache, moa SSE streaming, infra-leg fallback models, verify-env denylist revert.

## Code changes

| Fix | Landing points | Tests |
|---|---|---|
| Gate valve | `settle.go:475-490` (`git.PatchPaths`→`gateSourceHit`, fail-closed on path-parse failure), `server.go:4565+` attest whitelist (`consensus:"accept"` only) | `TestSettleMajorityValveExcludesGateSource` (valve had zero prior coverage), `TestHumanAcceptGateSourceAllowed` majority-rejection leg |
| C11 liveness | `server.go` Server fields, `NewServer` mount, `Wait()` ordering (stopLiveness before drain), `drainRun` neighbor | `liveness_test.go` (no-poll progress + disable switch); `-race` clean; TestSteer/TestLoop/TestParked regressions green |
| Contradiction advisory | `contradiction.go` (detector/scanner/dedup untouched), `server.go:4224-4226` et al. (6 files); new `flaggedNoteSet` fold; fold attribution annotation (else distill halts on unowned growth) | 3 rewritten tests: candidates don't filter recall, zero retracts, cross-pass dedup |
| Learner instrumentation | `memory_autogate.go` (idempotent `batch_superseded` after distill marker), `ledger.go` (per-epoch accepted/rejected/actor rows; `pending`/`none` when absent), `server.go` + `internal/git/git.go` (`git.HeadCommitTime` deploy witness: binary mtime >5min older than HEAD → WARNING, log-only) | 5 tests: supersede idempotence, ledger row/absence/cross-epoch, staleness boundary |
| `odo retract` | new `cmd_retract.go` + dispatch registration | roundtrip test with `odo unretract` rollback |

Docs synced: `auto-land-zero-manual-lock.md` evidence-gate section (was still claiming `majority_accept` attests), `m6-precision-ledger.md` first-line revision note.

Verification: `go build ./...` + `go vet` green; `go test ./... -count=1` all green (internal/ipc 463.8s, 6 other packages ok); merge seams from 4 workers concurrently editing `server.go` manually spot-checked — all anchors in place, no conflicts.

## Open loops

- P1 items #5–#9 unstarted: SQLite DSN hardening (`mmap_size(0)` + `synchronous(FULL)`), C10 single-loop admission atomicity, `runMemoryLayers` swallowing journal read failures, N=1 unanimity single-judge degradation, `/panel` leg missing outer deadline — awaiting user go-ahead.
- GUI surface for contradiction candidates still absent; candidates live only in journal (`odo journal search contradiction_candidate`); wiki badge shows real retracts only — needs a separate GUI task.
- `wiki/topics/auto-land-pipeline.md:4` "never auto-land" wording drifted twice (2026-08-20 and this session); wiki is protected/daemon-owned so agent cannot fix — left for distill/curate cycle.
- Historical `majority_accept` rows still journaled for non-gate diffs (evidence value retained); only their gate-attestation capability was stripped — intentional, confirm acceptable.