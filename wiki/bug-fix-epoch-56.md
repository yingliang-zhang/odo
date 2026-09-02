# DX wave diff #144 → #146: byte-exact re-apply, then one-line e2e fix

## Background

Diff #144 (DX wave, patch_sha16 `1d738a1dd2d50b63`, 31 files, +2263/−24) was auto-land blocked (`verify_failed`): 6 vitest "Test timed out in 5000ms" failures under the full-suite `--maxWorkers=2` run — 5 in `src/app_keepalive.test.tsx`, 1 in `src/components/settingspanel_k8s.test.tsx`. Flake protocol (same worktree, same node_modules, isolated runs) showed both files fully green in isolation (7/7 in 2.57s; 4/4 in 0.96s) → load-induced timeouts, not a code fault.

## Turn 1 — byte-exact re-apply of #144 (became diff #145)

**Decisions**

- Use the daemon-prepared fresh worktree `.odo/worktrees/6a982729-5346b88818dc` (clean @ HEAD `c6e0cf8d` = main; not #144's original tree `6a981ada`).
- Apply the archived patch `.odo/diffs/6a981ada-7dd9ae782f27.diff` (journal: base `1db418f`, status `pending`, goal DX Wave) with a single `git apply --3way`. 31/31 files applied cleanly, zero conflicts (only one wiki commit between base and HEAD).
- Strictly read-only toward content: no file edits, no commits, verify gate not run (the daemon owns it).

**Evidence**

- `git diff --cached` vs archived patch: `cmp` byte-identical (BYTES_IDENTICAL); 31 files, +2263/−24.
- `git status --short`: exactly 31 entries (9 A / 22 M); nothing unstaged, no extra untracked files.

**Outcome**: registered as diff #145; blocked again (`verify_failed`) — but this time a deterministic bug in the patch's own new e2e test, not the flake.

## Turn 2 — one-line fix in #145's new e2e test (→ expected diff #146)

**Root cause**: `gui/e2e/memory-editor.spec.ts`, test "memory.md: draft → save → refreshed section + saved toast…" — the fill on line 39 used `"- hand edited gate rule\n"` while the assertions on lines 46/49 expected `"- hand edited rule"`; `getByText` could never match. Rest of #145 verified green: tsc ✓, vitest 558/558 ✓, playwright 172/173 (only this test red).

**Change** (single line, `gui/e2e/memory-editor.spec.ts:39`, nothing else touched):

```diff
-  await area.fill("- hand edited gate rule\n");
+  await area.fill("- hand edited rule\n");
```

Verification: `grep -n "hand edited"` now shows lines 39/46/49 all reading the same string.

**Staging**

- Resumed in place in worktree `6a982729-5346b88818dc` (no new worktree, no reset).
- Staged diff vs archived #145 patch (`6a982729-5346b88818dc.diff`): exactly 2 lines differ (blob index hash + the fixed line); the other 2826 lines byte-identical. Still exactly 31 files (9 A / 22 M), zero commits, no unstaged residue.
- `git apply --check` reports only the 2 pre-existing trailing-whitespace warnings carried by the original patch (`preview-panel.spec.ts:126`, `runs.spec.ts:164`) — deliberately preserved for byte-identity.
- Gate not re-run per instruction; daemon verifies at intake. Expected next journal id: **#146** (daemon registration authoritative).

## Open loops

- Vitest full-suite `--maxWorkers=2` 5000ms timeout flake (`app_keepalive.test.tsx` ×5, `settingspanel_k8s.test.tsx` ×1) remains unfixed — absent from #145's gate run but may re-block any future verify. Open decision: keep handling via the flake protocol (isolated re-run + attribution disclosure) or structurally fix (per-test timeout budget / worker-count tuning).
- Diff #146 (staged #145 content plus the one-line fix) awaits daemon intake and its verify gate; auto-land result pending and could be re-blocked by the flake above.