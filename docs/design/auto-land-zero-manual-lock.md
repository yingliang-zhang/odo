# DESIGN LOCK: Auto-Land Pipeline — Zero Manual Accept

> Tri-model MoA (K3/GLM/DSF, --thinking max, 900s, blind-sealed). 3/3 converged on root causes + fix proposals.

## Root Causes (3/3 converged)

| # | Pattern | Root Cause | Type |
|---|---|---|---|
| 1 | credential_probe false positive | Panel reads diff body, sees `os.Getenv("AWS_SECRET_ACCESS_KEY")` in fixtures.ts, votes needs_fixes. Risk classifier is observational only — it does NOT gate. The panel is the actual blocker. | Missing feature: no fixture-path awareness |
| 2 | Revise diffs pile up | Each round creates a new diff; old diffs stay pending. "Never auto-reject" invariant prevents cleanup. | Design choice + missing feature |
| 3 | GUI diffs run go test | `.odo-verify` has one command for all diffs; GUI-only diffs still run `go test ./...` (6-10min) | Missing feature: no path-scoped verify |
| 4 | Panel can't converge on complex diffs | Unanimous consensus required (3/3 accept). After 3 rounds of revise, ladder suspends → manual. | Design choice: unanimity rule |

## Fixes (3/3 converged, priority order)

### Fix 1: 2/3 Majority Accept at Round Cap (fixes Pattern 4 + most Pattern 1)

**Rule**: After 3 rounds of revise, if ≥2/3 models say "accept" AND zero rejects AND zero infra legs → auto-land with `consensus_verdict:"majority_accept"`.

**Code path**: `settle.go:settleRevise`, at `len(st.rounds) >= settleMaxReviseRounds`, before the suspension journal:
```
accepts := count(reviews, Verdict=="accept")
if accepts > 0 && accepts * 3 >= 2 * len(reviews) && count(reviews, Verdict=="reject") == 0:
    journal moa_review{consensus_verdict:"majority_accept"}
    handleDiffAction(d.ID, "accept", autoActor)
    return
// else: existing suspension
```

**Safety guards (3/3)**:
- Zero rejects required (reject = direction is wrong; needs_fixes = direction is right, not done)
- Zero infra legs required (truncated/transport failure can't be outvoted)
- Truncated-dissent veto: a truncated leg degrades to needs_fixes but wasn't fully pronounced — must not be outvoted (K3 finding)
- `majority_accept` journaled on moa_review + accept rows for audit
- Does NOT un-suspend: only fires at the cap, before suspension; post-suspension diffs still need human accept
- Threshold: `accepts * 3 >= 2 * len(reviews)` — degrades correctly at N=1 (never fires), N=3 (≥2), N=4 (≥3)

**Files**: `internal/ipc/settle.go`, `internal/ipc/settle_test.go`, `internal/ipc/autonomy.go` (add MajorityAccept tally)

### Fix 2: Supersede Old Chain Diffs on Land (fixes Pattern 2)

**Rule**: When a diff in a revise chain lands (auto or human), mark all older pending diffs in the same chain as `superseded`.

**New status**: `DiffSuperseded` in `store.go`. NOT `rejected` — it's housekeeping. Excluded from `ListPendingDiffs` automatically (status filter is `= DiffPending`).

**Chain derivation**: Join via `auto_revise_round` rows (`origin_diff_id` links chain members).

**Journal**: `review_action{action:"superseded", actor:"auto_panel", superseded_by: <landing_diff_id>}` — evidence before action.

**Invariant amendment**: "never auto-reject" → "never auto-delete; superseding is transparent chain cleanup with full journal evidence". Update `settle_test.go:584-585` pin.

**Files**: `internal/store/store.go` (constant), `internal/store/diffs.go` (UpdateDiffStatus), `internal/ipc/settle.go` or `autoland.go` (mark on land), `internal/ipc/settle_test.go`

### Fix 3: Path-Scoped Verify (fixes Pattern 3)

**Rule**: `.odo-verify` supports optional `glob: command` lines. If ALL diff paths match a glob, use that command; else use the bare fallback line.

**Format**:
```
gui/**: cd gui && npx tsc --noEmit && npx playwright test --reporter=line
go build ./... && go vet ./... && go test ./...
```

**Selection**: `git.PatchPaths` (already computed in `autoLandCheck`) → if all paths start with `gui/`, use the GUI command.

**Supply-chain safety**: `.odo-verify` is already in `autoLandSupplyChainFiles` — a diff can't modify its own verify gate.

**Files**: `internal/ipc/autoland.go` (verifyCommand + caller), `.odo-verify` (add gui line)

### Fix 4: Fixture-Path Exemption in Risk Classifier (fixes Pattern 1 receipt)

**Rule**: Skip content rules (`credential_probe`, `data_exfil`, `security_weakening`) for test/fixture paths. Keep `supply_chain` and `destructive` unexempted (path-independent hazards).

**Path predicate**: `gui/src/dev/`, `**/fixtures*`, `*_test.go`, `*.spec.ts`, `*.test.ts(x)`, `gui/e2e/**`

**Note**: This fixes the audit receipt. The actual revise loop is fixed by Fix 1 (majority accept) — the panel's dissent is outvoted after 3 rounds.

**Files**: `internal/ipc/risk.go` (scanLine, checkAddedContent), `internal/ipc/risk_test.go`

## Hard Rules

1. Majority accept only fires at round cap (≥3 rounds attempted), never earlier
2. Zero rejects required (reject = direction wrong; needs_fixes = direction right)
3. Zero infra/truncated legs required (can't outvote an incomplete review)
4. `majority_accept` journaled for audit; `ComputeAutonomy` tallies it separately
5. Superseded ≠ rejected — diffs stay on disk, status is housekeeping
6. Verify path selection is default-deny: mixed Go+GUI diffs use the Go gate
7. `.odo-verify` self-modification blocked by supply-chain gate
8. Risk classifier exemption is observational only; supply_chain + destructive stay

## Verification

```bash
go build ./... && go vet ./...
go test ./internal/ipc/ -run 'Settle|Majority|Supercede|Verify|Risk' -count=1 -v
go test ./internal/ipc/ -count=1 -timeout 600s
cd gui && npx tsc --noEmit
```

## Revision 2026-08-20: protected-path doctrine — review everything automatically

User directive: manual diff review is redundant; every review must be automatic. Supersedes the 2026-08-15 tri-model rule "a diff that weakens a gate must never auto-land" and the corresponding `protected_path` mechanical block.

**New split in `autoLandCheck`** (hard block ⇔ panel-weighed annotation):

| Class | Routing | Why |
|---|---|---|
| memory paths (`.odo/`, `wiki/`) | hard block `protected_path` (unchanged) | executor refuses them for EVERY actor; a panel would attest bytes that can never land |
| supply-chain files (manifests, lockfiles, `.odo-verify`) | hard block `supply_chain_path` (unchanged) | single-line RCE vectors diff review structurally cannot audit; a self-modified `.odo-verify` would be the verify oracle attesting itself |
| gate source files (`protectedGateFiles` — autoland/review/settle/risk/…) | **risk annotation → verify+panel → auto-land** | Go source is exactly what a panel can audit; the diff carries the same facts block plus "score any gate weakening as REJECT" |
| new top-level directory | risk annotation (was hard block) | placement intent is a content judgment |
| net `*_test.go` assertion loss | risk annotation (was hard block) | may be legitimate removal; panel weighs oracle-weakening as REJECT evidence |
| `unparseable_diff` / `base_unresolvable` / run-verdict taint | hard block (unchanged) | integrity preconditions — there is nothing valid to review |

**Executor evidence gate** (`server.go: panelVerdictAttestsDiff`): a non-human accept of gate source files requires a journaled `moa_review{actor:auto_panel, consensus_verdict: accept|majority_accept}` row whose `patch_sha16` matches the **exact on-disk patch bytes** being landed. The sha binding prevents a verdict for an earlier generation of the diff from unlocking land. All failures are fail-closed. The human Accept click stays the unconditional escape; memory paths stay refused for every actor.

**Residual risk (accepted by user)**: the panel is now the sole guard against the pipeline weakening its own judge (previously the mechanical gate short-circuited before any model saw the diff). Mitigations retained: unanimity across heterogeneous models, the diff body fenced as data, the gate-source annotation priming REJECT for gate weakening, byte-bound verdict evidence, and every outcome journaled (`auto_land_blocked` / `moa_review` / `accept`).

**Loop pipeline unchanged**: `/loop` fix rounds land with `loopActor` and no panel, so their protected-path suspension stays — `panelVerdictAttestsDiff` structurally refuses a loopActor gate-source land (no auto_panel verdict rows exist for it).

Tests: `TestAutoLandCheck` (annotation vs hard-block matrix), `TestHumanAcceptGateSourceAllowed` (three legs: human escape, no-evidence refusal, stale-sha refusal, evidence-bound auto-land), `TestBuildReviewPrompt` (risk annotations render in the facts block).
