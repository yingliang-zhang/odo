> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Odo replay series — settle digest repair, P2 GUI bundle, P3 quality infra, D9-W3 learning observability

## Context
Four sequential replay/build tasks in `/Users/yingliangzhang/Projects/odo`, each re-applying archived diffs or building new packages on advancing HEADs (811f9ec → 2afaa32 → b644a4a → post-P3). Every wave passed its gates; all worktrees were left intentionally dirty for diff extraction, with landing routed through the auto-panel/human Accept pipeline. The final task (D9-W3) was still mid-verification when the session ended.

## Key decisions

- **Digest cap/trigger coexistence (repair-A re-apply)**: expected conflicts in the cap-constant region never materialized — the patch preimage already contained HEAD's 256K text, so `--3way` landed clean. Result: `settleDiffCapBytes = 256*1024` (HEAD) kept, `settleDiffDigestTriggerBytes = 128K` (patch) added; `loop_test.go` untouched.
- **Panel findings on the digest diff — pin, don't reimplement**: F1 (C-quoted header selection misses → no pin) and F2 (128–256K band behavior change, no migration anchor) were closed with new test pins, not code changes; conservative over-elision was assessed as the sanctioned direction (elision trailer always points to full source). F3 (signature ripple) got no action — Go signature changes are compile-backed, full green suite rules out unmigrated callers.
- **Flake policy (diff #109)**: `TestAutoLandVerifyNoEvidence/zero_evidence_blocks` TempDir teardown failure was classified as a test-harness flake, not logic failure — re-verify, never modify the test. Flake did not recur on re-apply.
- **P2 environment remediation**: worktree lacked `node_modules`; symlink-to-main-checkout passed tsc/vitest but broke Playwright (dual-instance `beforeEach` exceptions). Resolution: `npm ci` real install (3s, lockfile untouched), then full re-verification.
- **P3 delegation split**: perf gate + theme (no file overlap) dispatched to background agents; Esc registry + contribution registry (both touch `App.tsx`) done serially in the main session to avoid same-file edit races.
- **P3 contribution registry deviation**: the `component` field from the task spec was deliberately omitted — panel body props are heterogeneous and wrapped by App keep-alive/LRU, so a `component` field would be inert metadata; body seam documented in `contrib.ts` header. Dead `ledgerBadge` convention deleted; 5 per-tab badge props consolidated into `badgeInput`.
- **P3 perf gate sizing**: limit = ceil(measured p95 × 3), rounded up to a 10ms floor — catches order-of-magnitude regressions, not noise. Gate FAILs (not warns); `PERF_UPDATE=1` regenerates baseline.
- **P3 theme cascade**: three-layer `:root` (63 `--p-*` primitives → semantic tokens re-pointed → `[data-theme="light"]` pure `var()` overrides); equivalence proven by 42-point (21 tokens × 2 themes) CSSOM var-hop resolution, 0 mismatches.
- **D9-W3 run_usage semantics**: chose K3 "ALL runs" — journal a `run_usage` row even when usage data is unavailable (option A), absorbing the TestDistill/TestDistillViaMoa pin updates this required.
- **D9-W3 perf noise**: a 3–10× degradation across all 4 perf surfaces during gating was diagnosed as CPU contention from parallel background suites (full Go + Playwright) — environmental noise, not regression; baseline untouched, quiet re-run planned.
- **`.odo-verify` parsing quirk discovered**: only the first fallback command is parsed — failing verify script had to be single-line.

## Code changes

### Settle digest (repair-A / diff #109, `internal/ipc/**`)
- `settle.go`: `settleDiffDigest()` — over-threshold (>128K) revise prompts become file-level stat block + dissent-named files' full hunks + elision trailer (points to `d.PathOnDisk`); >256K cap backstop; journal receipt `auto_revise_round.digest = {elided_files, named_files, digest_bytes}`. Under-threshold path byte-identical (pinned by `TestSettleRevisePromptBytes`).
- `settle_test.go` (+114/−7, revise round): `spawnPatch` parameterized base (old subtests verbatim); pin 6 = integration subtest "quoted-header path elides conservatively" (0 section riding, stat block lists decoded files, trailer reports full elision, receipt `named:0/elided:2`) + `TestSettleSplitPatchSectionsQuoted`; pin 7 = "under-trigger round rides verbatim with no digest receipt" (byte-identical riding, no digest key). `TestFeedbackNamesPath` gained `("", path)` guard row.
- `autoland_test.go`: one-line signature sync.

### P2 bundle (33 files, `gui/src/**` + `gui/e2e/**` only)
Preview 3-tier, Runs tab, failure overlay, LRU park, plus the three grounded-review fixes: F1 `POLL_FAIL_RESTART_THRESHOLD = 20` (`App.tsx:94`) with reload affordance into `FailureOverlay` (`App.tsx:2236-2240`); F2 `focusSeq` cleared on land (`handleFocusSeqLanded`) and on conversation switch; F3 class-keyed `dismissedFailureRef` with reset on reconnect.

### P3 quality infrastructure (16 files, `gui/src/**` + `gui/vitest*.config.ts`)
- `gui/src/contrib.ts`: `PANEL_CONTRIBUTIONS` (8 panels, id/title/icon/badge derivation), `PanelTab` union and `PANEL_TAB_IDS` derived from the table (single source); ContextPanel renders from registry. 8 tests.
- `gui/src/esc-registry.ts`: priority registry (overlay > menu > panel > global-cancel), `registerEscLayer` returns identity-based disposer, `useEscLayer` stable-slot hook, `__resetEscLayersForTests` seam. App global querySelector chain replaced by 4 built-in layers + `dispatchEscape()`; 3 ContextMenus and ChatSurface at/slash menus migrated; React `stopPropagation` discipline and Radix capture layers untouched. 9 tests.
- Perf: `gui/vitest.perf.config.ts` (separate config), `gui/src/perf/render.perf.test.tsx`, `gui/src/perf/baseline.json`, main config excludes `src/perf/**`. Limits: ChatSurface 200 events 130ms (p95 42.7), ContextPanel tab-switch 20ms (3.9), RunsPanel 50 runs 30ms (7.8), MessageBubble 5-file diff 10ms (2.3). Violation proof: limit 0.1ms → FAIL.
- Theme: `styles/app.css` +155/−71 refactor; 4 component-level light overrides + `mark` migrated to primitive refs; `theme-cascade.test.ts` (6 tests, grep-able zero-literal assertion).

### D9-W3 learning control plane (zero behavior change)
- `internal/ipc/learning_episode.go`: pure fold at distill tail — one `review_action{action:"learning_episode"}` row per lane per distill over the marker-pinned `[first_seq, last_seq]` window; outcome classes per lock §1.1; panel_infra recorded as context count only (not a verdict); `unownedFoldGrowth` whitelisted. Fixture test covers each outcome + non-outcome cases.
- `internal/ipc/learning_store.go`: append-only `.odo/learning/candidates.jsonl` writer, `artifact_hash` sha256 over {version, scope, base_sha16, base_source_seq, delta, content}, creation-time provenance only. 7 tests (roundtrip + hash stability). No candidates written yet in W3 (writer is the deliverable).
- Additive keys: `verify_ms` on verify outcomes (fail-soft); `journalRunUsage` rows via `adapter.SessionUsage` for all non-loop runs, at the existing drain seam.
- `internal/ipc/learning_status.go` + protocol/dispatch + `odo learning status` CLI: single daemon-side fold (episodes + candidate stages + flags) as one JSON payload; CLI prints compact table; smoke-tested on the real binary.
- GUI: MemoryPanel Learning sub-tab (9th panel) via the contrib registry — first flag surface (episodes, rules_audit flags, empty candidate state). `learning_stages:` pref deliberately not introduced (later wave).

## Verification
- Digest waves: `go build ./...`, focused `-run "TestSettleRepairPrompt|TestSettle"`, full `internal/ipc` suite (~534–560s) via nohup background pattern — all PASS; diff #109 full `go test ./...` 7/7 packages PASS, flake not reproduced.
- P2: tsc PASS (99 files covered), vitest 384/384, Playwright 150/150 (7.9m).
- P3: tsc PASS, vitest 34 files / 407 tests (+23 new), Playwright 150/150 (7.8m), perf gate 4/4.
- D9-W3: focus `Learning|Distill` tests green, learning_status tests green, CLI smoke green. Two pre-fixed precise-line table pins (cancel class). Full `go test ./... -timeout=14m` and full Playwright were launched in background and still running at session end; perf gate quiet re-run pending.

## Open loops
- D9-W3 final report never delivered: background full Go suite and Playwright suite results unconfirmed; gate tails unquoted.
- Perf gate quiet re-run outstanding — contention-inflated 3–10× readings must not be regenerated into `gui/src/perf/baseline.json`.
- Playwright `context-panel-tabs` retry observed after the 9th (Learning) tab changed the panel strip — mock-fixture investigation in flight; possible e2e selector/mock fix needed.
- All waves sit in dirty, uncommitted worktrees awaiting pipeline landing; `settle.go` is a protected gate file expected to route to the human Accept queue.
- Contribution registry `component`-field omission flagged as a possible revise-round follow-up (body seam documented in `contrib.ts`).
- Seven earlier review rows were marked `superseded` by the auto panel after P3 acceptance — final disposition of those diffs unconfirmed in-session.