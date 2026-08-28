> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# D2 — Repo-Grounded Reviewer Leg (Control-Plane Hardening)

Implementation of **D2 [P1] Repo-grounded reviewer leg** from `docs/design/control-plane-hardening-lock.md` (binding spec) in the `odo` repo (Go; `internal/moa`, `internal/ipc`). Prior legs D1/D3/D5/D7 landed (914a82f, d0f0a5c, 8adc8d9, fdd282a) and were not reworked; `gatepolicy.go` / `gate_manifest.json` / `.odo-verify` untouched. Work done in an assigned worktree.

## Key decisions

- **Exactly one grounded leg per fan-out** (review panels and loop audits). Its verdict weighs identically to every other leg — no extra authority.
- **Model-visible ⟺ journaled, including refusals**: every read attempt (allowed or denied) appears in `tool_calls`; the executor appends each `ToolAudit` **before** the tool output returns to the model, so citing-without-calling is test-detectable.
- **Scope = one-hop import neighborhood**, computed daemon-side, LLM-free, hashed as `scope_sha16`: touched paths (`git.PatchPaths`) ∪ same-directory Go siblings ∪ repo files importing a touched package (existing capped grep engine: 32MB scan / 200 matches) ∪ repo-internal packages imported by touched files. Non-Go paths degrade to touched + same-dir. Computation failure ⇒ touched-only scope + journaled `scope_truncated: true` (fail-visible).
- **Gate-source grounding is fail-closed**: with `grounded_review_required: gate_sources` (default), a degraded grounded leg on a gate-source diff (D1's `isGateSourcePath`) = `Infra`, and the round fails closed via `panelInfraLeg`.
- **Budget exhaustion still owes a verdict**: journaled `tool_budget_exhausted: true`; no verdict token ⇒ existing degradation (review forces `needs_fixes`; audit leg ⇒ `parse_error`).
- **Empty scope is legal, not an init failure**: an empty diff (base==HEAD) yields an empty allowlist; the leg runs with all tool reads refused rather than degrading to infra.
- **Ungrounded legs keep byte-identical prompts**; only the grounded leg gets the additive framing sentence/tool notice (and, for audits, the "Do not review what the diff does not touch." clause is replaced by the scoped-repo-reads clause).
- **Docs**: no per-key prefs table exists in README (only a `prefs.md` pointer); convention is code header comments — `grounded.go`'s header documents the two new keys. No doc file added.

## Code changes

- **New `grounded.go`** (core): prefs resolution (`grounded_reviewer:` = `model@provider`; exact match against the fan-out's model line, else the line's FIRST entry; journaled `resolved_by: prefs|first`), `grounded_review_required: always|gate_sources|never` parsing, allowlist computation, `scopedToolExecutor{inner, allow}` allowlist decorator around `newFSToolExecutorRooted(s.projectRoot)`, total-bytes accounting.
- **`internal/moa/client.go` (~734)**: `QueryWithTools` plumbing wired with the decorator as `ToolExecutor`; grounded leg `maxRounds = 8` (client ceiling 16).
- **`design_moa.go` (~165–183)**: grounded design-leg plumbing reused for the reviewer leg.
- **`loop_audit.go` (~235–241)**: `auditSystem` prompt clause swap for the grounded leg only; `buildReviewPrompt` gains `grounded bool`.
- **Receipts (additive, ADR-0002-safe)**: `ReviewResult` and `auditLegResult` gain `grounded`, `tool_calls []moa.ToolAudit` (cap 64 + `tool_calls_truncated`), `read_bytes`, `scope_sha16`, `scope_files`, `tool_budget_exhausted`; journaled on `moa_review` and `loop_audit_round.legs[]` rows.
- **Caps**: per-read stays `fsReadBytesCap` 64KB; new `groundedTotalBytes = 256KB` enforced inside the decorator; wall deadline unchanged (`s.legTimeout`).
- **New test file** with all five mandated tests: `TestGroundedScopeOneHop`, `TestGroundedReceiptMirror`, `TestGroundedBudget` (9th round refused, total-bytes cap trips, verdict still parsed), `TestGateDiffRequiresGroundedLeg`, `TestAuditLegGroundedPrompt` (grounded prompt gains notice/drops notouch clause; ungrounded byte-unchanged).

## Bugs found and fixed during testing

1. **Symlinked-root mismatch (production-relevant)**: `planGrounded` computed scope against the unresolved `root` parameter while the executor used the `EvalSymlinks`-resolved root, so allowlist paths never matched. Fixed by resolving the root in `planGrounded` and using `p.root` consistently.
2. **`allows()` directory-target miss**: the ancestor walk started from the parent, so a directory target itself was never checked against the allowlist. Fixed.
3. **Test-semantics correction**: refusing to grep `b/pkg2` (a directory not itself in the allowlist — only `y.go` is) is correct behavior; the assertion was changed to grep allowed files.
4. **Empty-diff false-infra**: loop round 1 with base==HEAD produced an empty touched set, which the code mis-classified as grounded-leg init failure and forced `required` ⇒ infra. Branch removed; temporary `waitLoop` debug dumps reverted.

## Verification status

- `gofmt` clean; `go build ./...` clean; `go vet ./internal/ipc/ ./internal/moa/` clean.
- All focused D2 tests pass; adjacent review/loop suites re-run green.

## Open loops

- Full `internal/ipc` suite (`go test ./internal/ipc/ -timeout=700s -count=1`, ~10 min) was running in the background when the transcript ends — final result not yet captured.
- Final verbatim changed-files list + test results report (required by the task briefing) not yet delivered.
- D2 commit hash not yet recorded in the transcript.