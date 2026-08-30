# Bug-fix epoch 29 — P2 panel-reject revise rounds and the settle diff digest

## Epoch arc

1. **Evidence round**: confirmed the P2 bundle (preview 3-tier + Runs tab + failure overlay + LRU park + 4 e2e root-cause fixes, 32 files, all `gui/src/**` + `gui/e2e/**`) in worktree `6a924c6f-93f4858ca262` — tsc PASS, vitest 376/376 (30 files), full Playwright 150/150 (8.1m, `PW_EXIT:0`, log `/tmp/p2-revise-full-pw.log`). No code changes; worktree left dirty, uncommitted (P2 auto-land had been blocked at seq 13516 by `verify_failed`).
2. **Port**: moved the uncommitted P2 diff into a fresh worktree via `git diff HEAD` (covers staged+unstaged) → `/tmp/p2-fixed.patch` (32 files, +3239/−56) → `git apply --3way`, all hunks clean. HEADs differed (dest `84a914e` is a direct descendant of source `169ca02`, one model-spec commit apart, no `gui/**` overlap) — verified safe before applying.
3. **Panel minority reject**: diff #105 got K3 grounded REJECT / GLM-5.3 accept / DSF accept → `panel_minority_reject` (D7 policy: one dissent does not end the chain). User answered with a revise round instead of accept/reject.
4. **P2 revise round 2**: fixed the panel's 3 findings in a fresh worktree (`6a92b7f9…`, after re-porting from `6a92aab7…`).
5. **Auto-land blocks**: `panel_infra` ×2, `repair_prompt_too_large` ×2, one bare `reject` (seq 14179) — the prompt-size blocker motivated Repair A.
6. **Repair A**: needs-based diff digest in the settle revise prompt (`internal/ipc/settle.go`), 3 files, +583/−51, left dirty for the pipeline.

## Key decisions

- **Stop-don't-improvise on port**: any `--3way` hunk failure would have halted the port for reporting; none failed.
- **F1 (P1) — reload escape hatch**: post-diff, classified failures (socket_closed / heartbeat_timeout) rendered only the FailureOverlay (Reconnect / Copy diagnostics / Open journal / dismiss) with no reload affordance at any poll count, killing the "harder escape hatch" the diff's comment claimed. Decision: the overlay offers a reload affordance once `pollFailures >= POLL_FAIL_RESTART_THRESHOLD` (20), for classified failures too; comment corrected to match reality.
- **F2 (P1) — focusSeq contract**: the stated "App clears the request once the jump lands" was never implemented; `focusSeq` survived forever and across workstream switches (ChatSurface is not keyed per conversation), pinning arbitrary groups into the render window on seq collision. Decision: clear `focusSeq` on jump land (scroll settled / target bubble visible) and on conversation/workstream switch; both pinned in vitest.
- **F3 (P2) — dismissal by class**: key the dismissal by failure class (dismiss A; a different class B arriving re-arms A) and clear it in `handleFailureReconnect` (previously Reconnect-then-recurrence surfaced no failure UI at all).
- **`slots.test.ts` exact-equality pin relaxed to `toMatchObject`**: accepted, left as-is (new subset+uniqueness tests cover it).
- **Repair A — digest instead of cap-chasing**: the cap was already raised 64K→128K→256K in one day as bundles grew ~95KB→194KB. Above a 128KB trigger (`settleCommentsCapBytes * 8`), the revise prompt now carries a needs-based digest: per-file stat block + full hunks of only the files non-accept feedback names + an explicit elision trailer pointing at `diff.PathOnDisk`. The 256KB hard cap stays as last resort on the *digested* input. `patchSHA`/`commentsSHA` are still computed from the FULL diff/feedback — chain identity unaffected.
- **Never guess files**: the digest's named set = `git.PatchPathsText`-validated paths ∩ feedback path-tokens (boundary `[A-Za-z0-9._/-]`, incl. `a/`/`b/` quoted forms); files absent from the diff are never included (anti-smuggle, pinned).
- **Byte-pin-before-refactor**: `TestSettleRevisePromptBytes` was written and green BEFORE the prompt-builder refactor, so the verbatim paths are provably byte-identical.
- **Pre-existing-failure attribution**: stashing the Repair A diff and rerunning on bare `9eab2eb` reproduced `TestLoopAuditSubjectCapAdmits200KB`'s failure — debt of the cap-raise commit, not of this diff.
- **Risk posture of the digest**: worst case of a bad named-file selection is over-elision (conservative direction), never silent truncation.

## Code changes

### P2 revise round 2 (GUI worktree, on top of the 32-file port)
- `FailureOverlay` + slots + CSS: threshold-gated reload affordance; class names independent so the existing dismiss e2e is unaffected.
- `App.tsx`: wiring for F1 threshold, F2 focusSeq clearing (land + switch), F3 class-keyed dismissal + reconnect reset.
- New tests: overlay component pin + an App-level pin file (7 new tests; suite grew 30→31 files, 376→383 tests).
- Mid-round regression found and fixed: the F3 catch-arm evaluated `cls === null && dismissed === null` as `null !== null` (always false), so unclassified failures could never arm the legacy banner.

### Repair A — settle diff digest (`internal/ipc`, 3 files, +583/−51)
- `settle.go`: `settleDiffDigestTriggerBytes` (128KB); `settleDiffDigest` (stat block via `git.PatchStats(d.PathOnDisk)`, same byte source as patch_sha16; single-pass `splitPatchSections` mirroring `diffPathsText` rules; verbatim trailer `"%d files elided from this digest; the full diff is on disk at %s — read it with your file tools if you need more."`); `settleDiffInput{text, digest}`; `startReviseRun` signature changed `prevDiff string` → `diff settleDiffInput` with a trigger×digest four-way builder (digest variants swap the fence header — no false "verbatim" claim); `auto_revise_round` gains `digest: {elided_files, named_files, digest_bytes}` (omitted when nil). `needs_fixes` and `base_stale` share one digest rule.
- `settle_test.go`: Pin 1 `TestSettleRevisePromptBytes` (byte pin); Pins 2/3 `TestSettleReviseDiffDigest` (named=2/elided=1; stat-only digest still spawns — the agent reads the repo; `base_stale` arm); Pin 4 rewritten over-cap arm (feedback naming a 307KB file → digest degenerates > 256KB → still blocks `repair_prompt_too_large`); `assertDigestRow` receipt assertions; `TestFeedbackNamesPath` boundary table.
- `autoland_test.go:2629`: one-line call-site sync for the new `startReviseRun` signature — the only file outside the task's stated scope, compile-required, verbatim semantics unchanged.

## Verification

| Gate | Result |
|---|---|
| P2 round 2 tsc | PASS (`TSC_OK`) |
| P2 round 2 vitest | 31 files / 383 tests PASS |
| P2 round 2 full Playwright | 1 failure (banner regression) → fixed → re-run; final tail not quoted in journal (see loops) |
| Repair A `go build ./...` | PASS (`BUILD_VET_OK`, vet + gofmt clean) |
| Repair A focused (`TestSettleRepairPrompt\|TestSettle`) | `ok github.com/yingliang-zhang/odo/internal/ipc 50.554s`, zero regressions in ReviseLands/RoundCap/MajorityValve/BaseStale |
| Repair A full `go test ./...` (background, 559.7s) | 6 packages ok; 1 FAIL — `TestLoopAuditSubjectCapAdmits200KB` (`subject_bytes = 199027, want (262144, 262144]`), proven pre-existing on bare `9eab2eb` |

Recurring harness gotcha (hit three times): GUI gates must run with cwd = `gui/`. From the repo root, vitest collects `gui/e2e/*.spec.ts` as vitest files (63 files / 48 fail) and `npx` misses `gui/node_modules`.

## Open loops

- Repair A diff (3 files, +583/−51, dirty) awaits the auto-land pipeline / panel verdict — no review action recorded after its report (journal ends at seq 14409).
- P2 bundle (diff #105 lineage, now with the 3 panel fixes) is still un-landed; last blocks were `panel_infra` ×2, `repair_prompt_too_large` ×2, and a bare `reject` (seq 14179). Re-run the settle ladder once Repair A lands.
- Final full-Playwright result for P2 round 2 after the unclassified-banner regression fix was never quoted — confirm before treating the round as green.
- Pre-existing `TestLoopAuditSubjectCapAdmits200KB` failure (debt of `9eab2eb`, which raised `settleDiffCapBytes` to equal `loopAuditSubjectCapBytes` and emptied the test's `(lower, upper]` window): proposed minimal fix is `loop_test.go:775` lower bound → `settleDiffDigestTriggerBytes` (128KB) so the 199KB fixture lands in-window; needs its own diff.
- `draftTabsRef.current` is still written inside the `setDraftTabs` updater (side effect in a pure updater) — panel note said fix if cheap; no evidence it was moved.