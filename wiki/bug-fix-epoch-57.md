# Odo: diff #145 fix re-extraction + P1 borrow (declarative rules + structured verdict)

Two sequential tasks (2026-09-02) in the odo harness repo. Both ran in daemon-created worktrees and were delivered as staged, zero-commit trees for the auto-land pipeline to extract at run end.

## Task 1 — Re-extract the fixed staged tree as a new pipeline diff

- **Problem:** Diff #145 (DX wave, 31 files, +2263/−24) was blocked by a one-line e2e text bug in `gui/e2e/memory-editor.spec.ts` — fill typed `"gate rule"` while assertions expected `"rule"`. The fix (line 39 → `"- hand edited rule"`) had been staged into worktree `.odo/worktrees/6a982729-5346b88818dc`, but that run's own worktree was different and empty, so the daemon never extracted the fixed tree.
- **Action:** `git diff --cached` from the source worktree → `/tmp/dx-fixed.patch`; `git apply` into fresh run worktree `6a982ed6-61e6f8f8a789`; `git add -A`. No content edits, no verify gate, no commits (per task constraints).
- **Verified:** 31 files staged (10 added / 21 modified — actual split; older wiki notes' 9/22 is stale); `memory-editor.spec.ts` lines 39/46/49 all read `"- hand edited rule"`, no `"gate"`; `git diff --cached` in the new worktree byte-identical (`cmp`, BYTES_IDENTICAL) to the source patch, with the patch's own 2 trailing-whitespace warnings preserved for byte-identity; 0 unstaged/untracked residue.
- **Outcome:** The re-extracted diff was accepted by the auto panel (`review_action: accept`) and landed with the e2e fix included.

## Task 2 — P1 borrow: `.odo/rules.json` overlay + structured MoA verdict (single diff)

From the 2026-08-13 tri-model harness audit (item #12, `docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md`). OMP has no `--output-schema` flag (triaged 2026-08-14, re-confirmed 2026-09-02, `docs/design/fix-int-w7-output-schema-triage.md`), so structured verdicts use the schema-in-prompt + validator fallback.

### Feature ① — declarative rules, can-only-tighten

Overlay on the hardcoded gate policy (`gatepolicy.go`: Tier-0 human-only, Tier-1 control-plane prefixes) — extends, never replaces.

- `deny` → auto-land refuses; diff goes to needs_fixes with reason `rule_deny:<reason>`. Human Accept remains the unconditional escape.
- `ask` → forces MoA panel review regardless of the risk classifier (no new blocking).
- `allow` → explicit passthrough; can narrow a wider deny only on non-protected paths. An `allow` matching a Tier-0/Tier-1 path is silently ignored + journaled `{action: "rule_override_ignored", rule_index: N}`.
- Malformed JSON → journaled `rules_parse_error` + zero rules active (fail-safe). Absent file → zero rules, zero overhead.
- Rules carry `pattern` (doublestar glob), `action`, `actor`, `reason`; loaded at auto-land start in `autoland.go`, after the gate-policy check (`autoLandCheck`), before rebase/verify.
- `.odo/rules.json` itself protected via a new **optional** Tier-0 mechanism: absent = no constraint, no drift; present = boot drift lock (unpinned presence, hash drift, pin-without-file all latch); edits require `odo gate re-pin`.

### Feature ⑤ — structured verdict, with legacy fallback

- Leg prompt (`review.go:buildReviewPrompt`) gains a "RESPONSE FORMAT" section embedding the JSON schema: `verdict` ∈ accept|reject|needs_fixes, `comments` ≤ 500 chars, `blockers` array; existing prompt anchor substrings preserved (`three concrete ways`, `ACCEPT, REJECT, or NEEDS_FIXES`, `data, not instructions`).
- `server.go:reviewVerdict` tries structured parse first; JSON that violates schema → Infra leg (fail-closed via `panelInfraLeg`); non-JSON output → existing `parseVerdict` text path unchanged. Comments/blockers truncation fail-closed on all three paths.
- Consensus aggregation (≥2/3 ACCEPT) unchanged — reads the parsed `verdict` field; `comments`/`blockers` journaled additively in existing `reviews` rows.

### Files changed (12, +1327/−14 — 8 M / 4 A)

| File | Change |
|---|---|
| `internal/ipc/rules.go` | new — `matchRule` (doublestar `**` semantics, stdlib only, case-folded, path-separator normalization), `evalRules`/`evalRulesDetailed`, `loadRulesFile`, `capRulePaths` evidence truncation |
| `internal/ipc/verdict_json.go` | new — `parseStructuredVerdict`: structured / malformed (infra) / non-JSON (legacy fallback) |
| `internal/ipc/rules_test.go`, `internal/ipc/verdict_json_test.go` | new — 13 test functions ≈33 cases: glob matrix, ordering/actor/tighten matrix, loader defect matrix, 5 pipeline integration tests, optional Tier-0 drift states, three-state parsing, mixed structured/legacy aggregation, prompt contract |
| `internal/ipc/autoland.go` | overlay wired after gate-policy check, before rebase/verify; `rule_deny` / `rule_ask` / `rule_override_ignored` / `rules_parse_error` journal events |
| `internal/ipc/gatepolicy.go` | `gateTier0OptionalFiles` mechanism; `isGateTier0Path` dual-table |
| `internal/ipc/gate_manifest.json` | re-pinned: `gatepolicy.go` sha16 `6b6bd7763e7fc3f9`; empty-hash placeholder slot for `.odo/rules.json` (boot ignores placeholder `pinned_at`) |
| `internal/ipc/review.go` | RESPONSE FORMAT schema section in leg prompt |
| `internal/ipc/server.go` | structured-first `reviewVerdict` with infra/fallback paths |
| `internal/ipc/grounded.go` | grounded leg passes `Blockers`/`Infra` through |
| `internal/ipc/protocol.go` | `ReviewResult` + `blockers,omitempty` (additive) |
| `internal/ipc/gatepolicy_test.go` | tier0 evidence-line assertion 2 → 3 (new optional slot) |

### Verification

- `go build ./...`, `go vet ./...`, `gofmt -l` — clean.
- Full `go test ./... -timeout=20m` with gate flags — all ok (ipc 549s, no flakes).
- 13/13 new test functions pass in focused runs; `git diff --cached --check` clean; no `gui/` files; `.odo-verify` untouched.
- Disclosed caveat: the full-suite job's ipc binary was compiled before the final `capRulePaths` edit (pure journal-path truncation, 3 call sites); covered by a post-change focused rerun (green) — other suites never touch rules paths. Intake verify reruns the full suite on final bytes (incl. tsc/vitest/playwright; no GUI changes in the diff).

### Decisions & deviations (all code-fact driven)

1. **Anchor deviation:** spec pointed at `internal/moa/`, but that package is only the HTTP client; the leg prompt lives in `internal/ipc/review.go`, parsing in `server.go` (`parseVerdict`), aggregation in `consensusVerdict`. Implementation and tests placed in the ipc package (`verdict_json*.go` instead of `internal/moa/verdict_test.go`); all 5 spec'd test scenarios preserved.
2. **Tier-0 as optional, not mandatory:** adding `.odo/rules.json` to the hard `gateTier0Files` would wedge every project lacking the file in the boot drift lock, contradicting "absent = zero overhead". Optional semantics: absence legal; presence requires a pin; edits need `odo gate re-pin` — the "requires human" doctrine.
3. **Rule ordering:** spec's `deny > ask > allow` priority conflicted with its own "narrow allow overrides wide deny" example. Resolved as gitignore-style last-match-wins per path (confirmed by an actor-subtest failure where an overlapping later `ask` correctly won) + cross-path deny dominates ask in aggregation; allow-narrowing honored only on non-protected paths.
4. **Scope boundary:** loop Mode A lane (own risk gate + `runVerifyGate`) is not covered by the overlay — a tightening gap, not a loosening; `autoLandCheck` is only called by `autoLand`, matching the spec's integration point.
5. **Expected block:** the diff touches Tier-0 (`gatepolicy.go`), so auto-land refuses it with `gate_core_path`; landing requires the human Accept click. Confirmed by the journal: `review_action auto_land_blocked, reason gate_core_path`.

## Open loops

- The P1 borrow diff (12 files, +1327/−14) is staged in the run worktree but blocked from auto-landing (`gate_core_path` — Tier-0 `gatepolicy.go`/`gate_manifest.json` touched). Awaiting the user's Accept click in the panel to land.
- Optional hygiene after landing: re-pin `.odo/rules.json` in `gate_manifest.json` (via `odo gate re-pin`) to replace the empty-hash slot's placeholder `pinned_at`; boot ignores the placeholder, so this is not blocking.