> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# D9-C v3 revision (auto-revise round 1): grounded tool-round cap 16→40, budget tests at lock-literal 40 rounds

## Context

Auto-revise round 1 applying the reviewed D9-C v3 diff (grounded tool-round budget) to the canonical worktree. Pre-image was verified to match the diff's old side before any edit; the full changeset was then applied with revisions addressing panel findings. Preceding the round, memory-layer maintenance events fired (contradiction candidate, learning episode, wiki commit, curation) — bookkeeping only, no code impact.

## Key decisions

| Finding | Disposition | Rationale |
|---|---|---|
| GLM: byte-budget test ran only 17 rounds, not the lock item 6 literal "40-round loop" | **Fixed** — the only substantive change this round | Lock literal takes precedence; test now exercises the full 40-round budget |
| GLM: `omitempty` omits `tool_rounds_used` on zero-call rows | **Declined** | Review itself scored it below threshold; dropping `omitempty` pollutes every ungrounded row; a pointer-based optional field introduces a second convention; `grounded:true` already disambiguates |
| K3: `capToolAudits` / review-leg success path not re-read (blind spot) | **Closed by direct source verification**, no code change | `capToolAudits` (grounded.go:553) is truncation-only; success path sets `ToolRoundsUsed` unconditionally in the struct literal — both paths journaled |
| Panel overall NEEDS_FIXES (no directional rejection) | **Assessed as infra-degraded aggregation** | Both visible reviewers ACCEPT; K3 self-reported byte-budget exhaustion; other legs likely died on the old 16-round cap — the exact accident this diff fixes. No code change can affect that round's aggregation |

Scope discipline held: no changes to consensus math, verdict semantics, Infra classification, revise ladder, or 256KB fail-soft behavior; `rr.Infra = plan.required || loopExhausted` kept byte-identical to the old version.

## Code changes

Changeset: **8 files, +490/−60** (reviewed diff was +477/−60; the +13 delta is exactly the strengthened graceful test; −60 production removals identical). Staged in the canonical worktree — **uncommitted, dirty**. Files confirmed by name in session: `server.go`, `protocol.go`, `grounded.go`, `loop_audit.go`, `grounded_test.go`, `client_test.go`, plus round-cap option wiring in the `QueryWithTools` path.

Production (lock items 1–5, 7):
- `maxToolRounds` 16→40; `defaultToolRounds` stays 16. Callers passing 0 (`design_moa.go:289`, `server.go` /panel, `panel_live_test`) keep prior semantics via the default — pinned by `TestQueryWithToolsDefaultRoundCap` + `TestQueryWithToolsRoundCapClamp` (0→16, 16→16, 40→40, 50→40).
- `groundedMaxRounds` constant → Server field `groundedToolRounds`, default 40 ACTIVE. Resolution order: field > env `ODO_GROUNDED_TOOL_ROUNDS` > prefs `grounded_tool_rounds:` > 40. `planGrounded` resolves once; review and audit legs share it. Header comment links the three budgets and the 32K→256K ladder.
- `groundedLegDeadline`: rounds > 16 scales leg deadline ×rounds/16; wired into both legs (review via `s.legTimeout`, audit via `moa.TimeoutForModel`), preserving the original base asymmetry.
- `ToolRoundsUsed` journaled on every row from pre-truncation `len(calls)`; round-cap death rows carry call names/args plus a `tool round-cap death (fail-hard)` marker, kept bidirectionally distinct from fail-soft byte-budget rows.
- `groundedToolCallsCap` 64→96.
- `server.go` /panel consult comment corrected to "user-facing, unscoped home-root executor"; behavior untouched.

Tests (lock item 6 — six groups in place): DefaultRoundCap/Clamp, Resolution, LegDeadlineInterlock, RoundCapIncident (includes 16-round death comparison), ByteBudgetGraceful, AuditLegGroundedRoundsUsed. Round-wall subtest rewritten 16→40 in `client_test.go`.

This round's strengthening of `TestGroundedByteBudgetGraceful`:
- 39 tool rounds executed + 40th post delivers the verdict — the maximum loop the 40-cap permits; one more tool request would die.
- First 7 reads served (6 × 242.4KB + 7th crossing to 282.8KB); calls 8–39 = 32 budget refusals; `read_bytes > 256KB` asserted so the budget provably tripped.
- Assertions distinguish both directions: `tool_budget_exhausted` + verdict present, no round-cap death/"exceeded"; `ToolCallsTruncated=false` (39 < 96 journal cap); posts == 40. Pre-fix, this shape died at round 16 (infra).

## Verification

- `gofmt -l` clean (an initial exit-code misread — `gofmt -l` exits 0 even when dirty — was corrected by re-checking output; `loop_audit.go` alignment normalized to match the reviewed diff byte-for-byte), `go build ./...` exit 0, `go vet ./...` exit 0.
- Focused gates: ipc `Grounded|LoopAudit|ToolRounds|QueryWithTools` → ok 3.901s; moa `ToolRounds|QueryWithTools` → ok 0.435s, Clamp 4/4 subtests PASS.
- New-test verbose run: TestGroundedBudget / Resolution / LegDeadlineInterlock / RoundCapIncident / ByteBudgetGraceful / AuditLegGroundedRoundsUsed — all PASS.
- Full suite `go test ./... -timeout=20m -count=1`: 7 packages all ok, 0 FAIL; `internal/ipc` 529.101s (baseline-conformant); log `/tmp/d9c-v3-full-suite.log`.
- Same-site audit: repo-wide zero `groundedMaxRounds` residue; `groundedPlan{` has a single construction site (`roundsCap()` backfills the zero value); all other `QueryWithTools` callers pass 0 — semantics unchanged; "16 rounds" mentions in history docs/wiki left untouched as historical record.

## Risk & rollback

Default 40 ACTIVE raises the grounded legs' wall-clock ceiling by ×40/16 (leg bases `s.legTimeout` / `moa.TimeoutForModel`); review throughput / panel_infra dwell needs post-landing observation. Rollback is config-only: env `ODO_GROUNDED_TOOL_ROUNDS` or prefs `grounded_tool_rounds:` — verdict and gate semantics have zero drift. `grounded.go` / `loop_audit.go` are gate files and are expected to enter the manual Accept queue.

## Open loops

- Staged changeset is uncommitted in the canonical worktree — commit awaits manual accept (gate files `grounded.go`/`loop_audit.go` route to the human Accept queue).
- Post-landing observation of the ×40/16 grounded-leg wall-clock ceiling on panel_infra dwell / review throughput; rollback via `ODO_GROUNDED_TOOL_ROUNDS` / prefs if it bites.
- Whether the revised diff clears the panel: this round's NEEDS_FIXES aggregation was infra-degraded (both visible reviewers ACCEPT); the re-review/re-aggregation outcome is unresolved.
- GLM's `omitempty` finding on `tool_rounds_used` was intentionally declined — revisit only if re-raised above the review threshold.