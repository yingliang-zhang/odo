# D9 lock-doc coherence: Amendment A1 cross-reference banners (diff #130)

## Context

The D9 primary lock doc (`docs/design/d9-learning-control-plane-lock.md`) still carried the original replay pass-criteria text (checks a–h, incl. f/g) in its "Frozen design" section, while the authoritative amendment lives in `docs/design/d9-lock-amendment-a1-fg-canary.md` (commit `b649ee0`, quad-blind 4/4, user ruling Option 1, implemented in `a8410af`). A reader of the primary lock alone would implement superseded semantics. The session was triggered by a memory-layer contradiction candidate and a recorded learning episode.

## Key decisions

- **Lock doc is a historical record — additive banners only.** The original a–h criteria text is untouched (zero bytes changed); cross-referencing is done solely via new banner lines pointing to the amendment file as authoritative.
- **No literal "§2.3" heading exists in the lock doc.** The a–h pass-criteria list actually lives inside the GLM-5.3 bullet of the "Frozen design" section; the second banner was placed there as an indented blockquote continuation.
- **Backlog Status line is immutable history.** The first attempt rewrote `Status: OPEN — needs design-lock amendment…` to RESOLVED (and mangled the sentence); reviewers flagged this as overstepping the 2026-08-31 audit record. The final patch conveys resolution only via a new header line; the Status line stays byte-identical.
- **No inline markup splices.** The first attempt appended an inline `**[AMENDED …]**` marker mid-paragraph, producing unbalanced `**` and a literal `**]`; the revision replaced it with a standalone blockquote matching the Frozen-design banner style.
- **Auto-land blocked by a known load flake, not the diff.** The verify run failed on `TestAutoLandVerifyNoEvidence` (pitfall #42 TempDir-teardown flake, same class as diff #109; passes 3/3 isolated in ~1s; the diff is a pure two-`.md` docs change that cannot affect Go tests). Auto panel: `auto_land_blocked` (`verify_failed`) → `reject`; the patch was archived as **diff #130** (`6a963ab0-c9d789d11696.diff`).
- **Final delivery is a verbatim re-apply, no edits.** `git apply --3way` of the archived patch (single action per instructions); fast gates only, no full suites.

## Changes (diff #130, +10/−0, 2 files)

- `docs/design/d9-learning-control-plane-lock.md`
  - 3-line blockquote banner at the top of the "Frozen design" section (~L57–59): "**AMENDED 2026-09-01** — replay checks f/g are DELETED; efficacy moves to the canary layer. See `docs/design/d9-lock-amendment-a1-fg-canary.md` (authoritative)."
  - Same 3-line blockquote at the replay pass-criteria passage, inside the GLM-5.3 bullet (~L73–75, 2-space indented continuation, blank-line separated); historical bullet text unchanged.
- `docs/design/d9-replay-stage-order-backlog.md`
  - One new line under the title (~L3): `**RESOLVED 2026-09-01 by Amendment A1 (b649ee0, implemented a8410af).**` — body and Status line left as original bytes.

## Verification

- `go build ./...` → exit 0 (5.84s final re-apply run; 5.77s/6.01s in the earlier rounds).
- `git diff --cached --check` → clean in the final run (`git diff --check` clean in prior rounds).
- `git status --short` → exactly the two modified `.md` files; staged via `git add -A`; no commits; worktree left dirty by instruction (pitfall #36: stage only in own worktree).

## Session bookkeeping

Memory/review events: note-layer contradiction candidate, learning episode recorded, wiki layer updated, curator skipped; auto-land path: `auto_revise_product` (round 1) → `auto_land_blocked` (`verify_failed`) → `reject`; `run_usage` updates and a final `auto_distill` fired.

## Open loops

- Diff #130 is staged but uncommitted in the fresh worktree (intentionally left dirty, no commits) — the actual landing of the doc-coherence patch is still pending.
- The `TestAutoLandVerifyNoEvidence` flake (pitfall #42 TempDir-teardown class, same as diff #109) remains unfixed and can block future auto-lands, including this patch's.