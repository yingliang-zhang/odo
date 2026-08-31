# parseVerdict markdown-tolerance fix — Tier-1 memory gate unblocked (2026-08-31)

## Problem
`parseVerdict(model, text)` in `internal/ipc/server.go` (pre-fix, ~line 4705) matched only bare line-initial verdict tokens (`up == "ACCEPT"`, `strings.HasPrefix(up, "ACCEPT ")`, plus the NEEDS_FIXES/REJECT equivalents). All three panel models emit markdown-decorated verdict lines:

- K3: `**Verdict: ACCEPT**`
- GLM: `## Verdict: ACCEPT`
- DSF: `## Verdict: **ACCEPT**` / `**Verdict: NEEDS_FIXES**`

None matched → `verdict == ""` → fail-closed `needs_fixes`. Since `panelAccepts` (`skills_gate.go:81`) requires unanimous accept, no proposal could ever pass the Tier-1 memory gate (P1, structurally dead).

Journal-verified footprint: 300 proposal-review legs — 223 wrote text-verdict ACCEPT, only 39 parsed as accept (61% of accept-grade legs misfiled as needs_fixes); 4 `memory_apply` events ever, 0 accepted. The learner.go "28 automatic runs, zero applied rules" comment that motivated disabling the auto-learner was very likely this bug, not genuine zero-yield.

## Code changes (2 files, +77/−1, staged, not committed)
- **`internal/ipc/server.go`** — added `normalizeVerdictLine`, applied per line inside `parseVerdict` before the existing bare-token match: uppercase → iteratively strip leading `#*_> ` decoration → strip one optional `VERDICT:` label → strip leading decoration again → strip trailing `*_ `, to a fixpoint. Handles all three models' forms including stacked decoration.
- Cutset contains only decoration characters, no letters — prose like `ACCEPTANCE CRITERIA:` can never collapse to a bare token, so fail-closed semantics are unchanged by construction (no new exclusion rules).
- **`internal/ipc/server_test.go`** — new `TestParseVerdictMarkdown`, 12 table-driven cases following the existing `TestParseVerdict` convention (line 3031): the three models' real decorated forms; `Verdict: NEEDS_FIXES`; `**Verdict: REJECT**`; bare `ACCEPT` / `ACCEPT with minor nits` / `NEEDS FIXES` regression pins; verdict-less fail-closed; `ACCEPTANCE CRITERIA:` decoy; decorated last-verdict-wins (ACCEPT then later REJECT → reject); GLM real-world sample (`## Verdict: ACCEPT` mid-text after analysis paragraphs).
- Explicitly untouched: `panelAccepts`, `reviewVerdict` truncation semantics, fail-closed default on no verdict, last-verdict-wins precedence, comments fallback, settle/grounded paths.
- Process note: the first edit spliced the helper stub into the middle of `parseVerdict`; caught by re-read and repaired — final state verified clean.

## Gates — all green
| Gate | Result |
|---|---|
| `go build ./...` + `go vet ./internal/...` + gofmt | EXIT 0, clean |
| `go test ./internal/ipc/ -run 'ParseVerdict\|Verdict' -count=1` | ok 5.980s — new 12/12, existing 10/10, truncation/consensus pins PASS |
| Full suite `go test ./... -count=1` (nohup) | 7/7 packages ok, 0 FAIL; `internal/ipc` 567.1s (task-cited baseline ~507s, 20m timeout); log `/tmp/parseverdict-full-suite.log` |
| Worktree | Only the 2 files staged, left dirty per task instruction |

## Key decisions
- Minimal scope: normalization inside `parseVerdict` only — no gate-semantics changes; prefix-match and last-verdict-wins precedence preserved exactly.
- `server.go` verified NOT a protected path (`isProtectedPath` covers autoland/autonomy/learner/review/settle/ledger/risk/contradiction/design_moa/skills_gate; precedent: the `odo rules audit` IPC handler) → normal auto-land expected, no special review flow needed.
- Fix targets the parser, not the models: all three panel output formats are now tolerated rather than prompting any model/prompt change.

## Session / review events
- Memory recall pulled wiki notes `bug-fix-epoch-39.md`, `bug-fix-epoch-40.md`, and epoch-42's open loops as prior context for this gate work.
- Pre-task memory pipeline: note-layer contradiction candidate → memory proposal → `memory_apply` (auto_panel) → learning episode → wiki-layer commit; one curator-layer update failed (seq 18561).
- Post-task: `run_usage` update; completion review `accept` (auto_panel, seq 18629); daemon `gate_policy_check` (seq 18630) — outcome not shown in the journal.

## Open loops
- Auto-learner re-enable decision pending: evidence indicates its "zero applied rules" disable-rationale was this parsing bug, but nothing was re-enabled in this session.
- Auto-land outcome unverified: the 2 staged files (`server.go`, `server_test.go`) were left dirty in the worktree; the actual land/commit is not shown in this session.
- Real-world effect unconfirmed: no post-fix panel review has yet demonstrated a proposal passing the Tier-1 memory gate (first-ever accepted `memory_apply`).
- Curator-layer memory update failed (seq 18561) — cause and retry unaddressed.