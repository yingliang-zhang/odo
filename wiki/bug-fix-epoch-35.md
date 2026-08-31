# D9-W5 Learning Control Plane: rollback, freeze, stall, never-score (plus W4.5 re-snapshot and rescue)

## Context
Three sequential tasks: (1) zero-change gate re-snapshot of the staged W4.5 IPC-suite-acceleration diff (ODO_STUB_SCALE seam, TestMain 0.15 scale, oneShotPollNs seam, `t.Parallel` marks, WINDOW-class pin); (2) single-action rescue re-stage of that diff after its source worktree vanished; (3) implementation of D9 wave W5 per `docs/design/d9-learning-control-plane-lock.md` (K3 spec `docs/design/learning-control-plane-d9.md` §4–§6), building on W3 (`bafb61d`), W4, and W4.5 (`acf96f9`).

## Key decisions
- **Evidence reuse over re-execution.** Full IPC suite cited from `/tmp/w45-full-suite.log` (`ok internal/ipc 507.844s`, EXIT=0; 840–865s → 507s) per instruction; only quick gates re-run.
- **Discovery over task framing (dispatch mismatch).** Task 1 claimed the diff was staged in the assigned worktree; staged count was 0 (clean at `6a94fb18` = main `d0cb320`). The 42-file changeset actually lived in sibling worktree `6a94ebb6` (HEAD `8979516` = diff #115). Gates ran on the tree holding the diff; zero files modified anywhere.
- **Byte-equivalent archive rescue.** The sibling worktree had been deleted before task 2; used `~/Projects/odo/.odo/diffs/6a94ebb6-a514894c5359-rescue.diff` (90,899 B = #116 archive + 386 B) as source. Verified post-stage: 42 files, +263/−11, staged-bytes sha256 prefix `81851fb9`, byte-identical to the rescue diff. Own worktree clean pre-patch (`c836f78`, wiki commits atop `8979516`; code tree equal to sibling base).
- **W4 baseline restored from archive, not main.** #115 had landed only 4 of 22 W4 files (259 of ~6,700 lines — test fragments); the full W4 implementation survived only in `6a942826-ebd55941a53d.diff` (170 KB; bulk authored in a parallel worktree). The staged W4.5 diff was byte-identical to #117 (already landed at `acf96f9`), so it was removed from the index (file bytes unchanged) to reach a clean baseline; the W4 archive was then 3-way applied — 22/22 clean, build EXIT=0, W4 Learning tests verified.
- **R1 rollback semantics (user ruling, binding).** Candidate layer only: instant stage demotion to `rolled_back`, fold-derived, zero memory.md writes. If the candidate had landed in memory.md via the receipted project_active path, the fold additionally emits `memory_update{layer:"memory", cause:"retract_candidate", rule, flag_seq, candidate, epoch}` (existing D4 receipt shape); the human resolves via apply_memory / `odo rules retract`. The daemon never deletes memory.md lines. Restore bounded to the candidate's own delta.add — opaque/human lines and other candidates' lines never touched; journaled marker-first with the full evidence tuple.
- **Two-phase frozen-set recomputation.** Inside the per-epoch measure tick, rollback is evaluated before canary promotion, so the frozen set is recomputed in segments: a freshly rolled-back candidate's text must freeze other candidates carrying the same text in the same tick. Canary stall advisory guarded by `epoch > 0`.
- **Cross-lane window arithmetic** relies on `Event.CreatedAt` from SQLite `datetime('now')` (UTC, `YYYY-MM-DD HH:MM:SS`) — lexicographically sortable, so no extra parsing.
- **W4 tests migrated, not duplicated.** W4 `shadow_queued` expectations superseded by W5 shadow→canary actuation; existing tests updated to the W5 contract.
- **Boring stdlib.** Custom `itoa`/`ageText` helpers deleted in favor of `strconv.Itoa`.
- **Full-suite execution via `async: true`.** The nohup background run died when the tool session reaped its process group (log stopped at 3 packages, no FAIL lines); re-launched async and waited via hub.
- **Suite failures fixed at contract level.** Full run EXIT=1; two failing tests tied to auto-apply landing in memory.md under the default preference were fixed (×2, focus gate green), then a same-class audit ran for other tests asserting that behavior — consistent with the design lock that autoApplyProposals edits route human-Accept-preferred.

## Code changes (Go `internal/ipc/**` only; settle.go untouched; GUI deferred)
- **`learning_measure.go` (new):** per-epoch measure tick — harmful-tuple rollback on a candidate's own delta.add, `rolled_back` demotion, two-layer retract receipt, bounded restore, promotion cohort check (paired cohorts both ≥10 human outcomes; `excludedFromScoring` extended from baseline to promotion), stall advisory (shadow > 12 epochs without promotion-worthy evidence; never auto-promotes, never auto-drops).
- **`learning_stages.go`:** shadow→canary actuation (aged/queued branches of `learningShadowCheckpoints` rewritten); frozen stage-interrupt with journaled `learning_frozen` event (boundary fixture: N+1 rejected via oscillation_guard, N+4 free); measure tick + promote apply + stall appended; `sort` import; helper cleanup.
- **`server.go`:** measure tick wired into the distill tail, where the rules_audit join already runs.
- **`learning_measure_test.go` (new, unit):** rollback happy path + two-layer R1 receipt; restore over-reach refused; freeze boundary integers; never-score exclusion pin; promotion cohort pairing; greppable evidence→measure→gate signature pin (no single evidence row moves a stage).
- **`learning_rollback_test.go` (new, e2e):** end-to-end rollback, stage-interrupt freeze, stall advisory behavior.
- **W4 test updates** to the W5 contract + **2 full-suite test fixes** for auto-apply/memory.md default-pref assertions.
- **Final staged state:** 27 files, +6,036/−65 (22 W4-restored + 5 new W5). Worktree left dirty per instruction.

## Verification
- Task 1: `go build ./... && go vet ./internal/...` → BUILD_VET_OK; focus `ok github.com/yingliang-zhang/odo/internal/ipc 22.587s` EXIT=0 (`-count=3`, fresh processes); staged bytes identical pre/post gates in both worktrees; zero modifications.
- Task 2: staged 42 files, +263/−11, sha256 prefix `81851fb9`, byte-identical to rescue.diff; nothing else touched.
- Task 3: build + vet green; `go test ./internal/ipc/ -run 'Learning' -count=1` green after W4-test migration; full `go test ./... -timeout=20m` first run EXIT=1 → 2 fixes → focus green → re-run in flight at session end.

## Open loops
- Full-suite re-run (`go test ./... -timeout=20m`, async) result pending; the required final W5 report — per-item status, gate tails, one-line risk note — is undelivered until it lands.
- Commit/land decision for the staged 27-file changeset (+6,036/−65 = W4 restoration 22 files + W5 5 files); worktree intentionally left dirty.
- Root cause of the dispatch/attachment logic that assigned sessions to a clean worktree while the changeset sat in a sibling (which was later deleted) — observed twice, never investigated.
- W4 provenance governance: main held only 259 of ~6,700 W4 lines before this restoration; confirm the 22-file restoration actually lands, and correct epoch/wiki notes claiming W4 fully landed at `8979516`.
- Outcome of the same-class audit (other tests asserting auto-apply → memory.md writes under default prefs) not yet recorded — confirm no stragglers survive the re-run.
- W6 GUI surface deferred by design: LearningPanel stage feed, stall-advisory rendering, and human actions (accept_diff / retract valve) remain daemon-side only this wave.