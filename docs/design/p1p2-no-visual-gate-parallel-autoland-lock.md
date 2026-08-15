# DESIGN LOCK: P1+P2 — Remove human_gate_visual + Parallelize autoLandMu

> Tri-model MoA consolidation (K3/GLM/DSF, --thinking max, 540s, blind-sealed). 3/3 converged.

## P1: Remove human_gate_visual (3/3 convergence)

**Hard delete** the visual gate and its supporting plumbing. No pref flag.

### Changes

1. **`internal/ipc/autoland.go:381-387`**: Delete the `if visual := diffVisualPaths(diffText)` block. After `panelInfraLeg` check, flow proceeds directly to `settlementClass` switch.

2. **`internal/ipc/autoland.go:68-76`**: Rewrite the "visual class" header section to one line: GUI diffs land through the same unanimous-panel pipeline as daemon diffs.

3. **`internal/ipc/autoland.go:435-437`**: Reword "(human_gate_visual precedent)" to "(the blocked-row-as-evidence precedent)".

4. **`internal/ipc/review.go:232-245`**: Delete `visualPathPrefix` const + `diffVisualPaths` func. Grep confirms sole production caller is the deleted gate.

5. **`internal/ipc/review.go:22-26`**: Delete the `diffVisualPaths` doc field from the `reviewPromptInput` comment block.

6. **`internal/ipc/review_test.go:~195-210`**: Delete the `diffVisualPaths` table test.

7. **Keep** `autonomy.go:125-126` (`VisualGateBlocks`) + `autonomy.go:412-414` (`case "human_gate_visual"`): these classify **historical** journal rows. Retaining preserves audit correctness. Historical tests stay green untouched.

8. **`internal/ipc/autoland_test.go:353-430`**: Delete `TestAutoLandVisualGate` (asserts `human_gate_visual`). Replace with `TestAutoLandVisualDiffUnanimousAcceptLands` (GUI diff + unanimous panel → lands, zero blocked rows).

## P2: Remove autoLandMu (3/3 convergence)

**Delete `autoLandMu` entirely.** The final accept already has `acceptMu`. Verify+panel+entry-probe are all parallel-safe (isolated worktrees, stateless HTTP). Add a narrow `ladderMu` for the settle-revise chain-fork invariant.

### Changes

1. **`internal/ipc/autoland.go:210-211`**: Delete `s.autoLandMu.Lock()` / `defer s.autoLandMu.Unlock()`. Keep the `autoLandDone` defer block.

2. **`internal/ipc/autoland.go:207-209`**: Replace the "One auto-land pipeline at a time" comment with: pipelines run concurrently; the final `handleDiffAction` accept is serialized by `acceptMu`.

3. **`internal/ipc/server.go:122`**: Remove `autoLandMu sync.Mutex` field. Add `ladderMu sync.Mutex` field near `acceptMu` with comment referencing the settle.go chain-fork invariant.

4. **`internal/ipc/server.go:114-123`**: Rewrite the lock-ordering comment to state `acceptMu → mu` and `ladderMu → mu` ordering. Verify/panel run concurrently; only the accept critical section and the ladder decision are serialized.

5. **`internal/ipc/settle.go:362-364`**: Update comment from "Called from autoLand with autoLandMu held" to "Called from autoLand with ladderMu held — one ladder decision at a time daemon-wide, so the rounds chain cannot fork."

6. **`internal/ipc/autoland.go` `settleRevise` call site** (~line 407): Wrap with `s.ladderMu.Lock(); s.settleRevise(...); s.ladderMu.Unlock()`. Put the lock at the call site, not inside `settleRevise`.

### Concurrency safety (3/3 verified)

- **Entry probe**: `ProbeApplyClean` uses throwaway temp worktree, never touches main, never takes `acceptMu`. Parallel-safe.
- **Verify**: `runVerify` runs in the run's own detached worktree. Distinct directories, distinct git index files. Parallel-safe.
- **Panel**: `reviewFanout` → `moa.Query` (HTTP POST, stateless client). Parallel-safe (already parallel within one pipeline).
- **Land**: `handleDiffAction` takes `acceptMu` for the entire apply/commit/rollback. Sole serialization point. Unchanged.
- **Ladder**: `ladderMu` prevents two concurrent `settleRevise` calls from the same conversation forking the rounds chain. Narrow lock (ms duration).

### Settlement race (3/3 confirmed correct)

Two unanimous pipelines A, B. A lands first (acceptMu). B's `handleDiffAction` finds HEAD moved → `checkAndRefreshBase` → clean refresh or `base_stale_at_land`. Same behavior as today's human-race case, now routine instead of rare.

### autoLandDone (3/3 confirmed safe)

`select { case s.autoLandDone <- struct{}{}: default: }` non-blocking send is safe under parallelism. First pipeline fills the cap-1 buffer, later sends drop. Only consumer is `runverdict_test.go` which drives one pipeline.

## Hard rules

1. **No pref flag** for visual gate — remove outright.
2. **No narrowing autoLandMu** to accept section — that duplicates `acceptMu`. Remove entirely.
3. **Keep** `VisualGateBlocks` in autonomy.go — classifies historical journal rows.
4. **Keep** `autoLandDone` test signal — non-blocking send is parallel-safe.
5. **`ladderMu` at call site**, not inside `settleRevise` — mirrors repo's `Locked` suffix convention.
6. **No git add/commit.** Touch only files listed above.

## Test names

- `TestAutoLandVisualDiffUnanimousAcceptLands` — GUI diff + unanimous panel → lands, zero blocked rows, no `human_gate_visual`
- `TestAutoLandVisualDiffNeedsFixesEntersLadder` — GUI diff + needs_fixes → enters revise ladder (same as daemon diff)
- `TestAutoLandParallelPipelines` — two diffs finish simultaneously; both pipelines run; first lands, second refreshes or base_stale_at_land
- `TestAutoLandLadderNoFork` — two needs_fixes diffs from same conversation; only one revise round spawns (ladderMu prevents fork)
- Delete: `TestAutoLandVisualGate` (asserts human_gate_visual)
- Delete: `diffVisualPaths` table test in review_test.go

## Verification

```bash
go build ./... && go vet ./...
go test ./internal/ipc/ -run 'AutoLand|Visual|Ladder|Settle' -count=1 -v
go test ./internal/ipc/ -count=1 -timeout 600s
```
