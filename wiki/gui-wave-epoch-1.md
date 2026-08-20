> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Conversation Summary

Two sequential GUI tasks delivered in the odo worktree, both guided by `docs/compare/harness-gui-tri-model-audit-2026-08-13.md` and `docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md`. Neither committed.

## Task 1 — A-P0 #1: Guardian risk taxonomy in LedgerPanel (accepted)

**Decision:** render `review_action` journal rows as receipt cells above `ledger.md`; data comes from the existing `events` state (bootstrap replay + poll) — zero new IPC.

**Changes (6 files, +372):**
- `gui/src/types.ts` — `EventPayload` += `risk_class/risk_evidence/risk_classifier/consensus_verdict/reason/round/outcome/phase/timed_out`; comment mirrors `risk.go` contract (`"none"` = rated-clean, absent = pre-W5 unrated).
- `LedgerPanel.tsx` — new "review actions" section: `ReviewRow` = action badge + actor (`auto_panel`→Auto, `''`→Human) + risk badges + timed-out chip + detail (blocked reason / refresh phase), newest-first; bookkeeping actions (distill/curate/todo_merge) stay on the old surface.
- `App.tsx` — passes `events={events}` into LedgerPanel.
- `app.css` — severity ramp: red `--err` (critical) → amber `#d19a4a` (high, light-theme override so it doesn't collide with `--warn`) → yellow `--warn` (medium) → blue `--link` (low) → gray. `risk-unrated` is dashed-neutral — never masquerades as clean. New `badge-blocked/badge-refresh/badge-actor-*`.
- `fixtures.ts` — conv 3 gets 7 receipt rows covering every render branch (full auto-land loop, reject, mixed/timed-out, pre-W5 unrated).
- `e2e/ledger.spec.ts` — 4 specs (ordering/actor counts, severity colors + evidence tooltip, outcome details, ledger.md intact).

**Class→level map:** credential_probe=critical; data_exfil, destructive=high; security_weakening=medium; supply_chain=low; none=clean; unknown class → forward-compat neutral.

**Incident (root-caused, not flaky):** fixtures initially placed in conv 1 broke 3 assertions in diff.spec/review-inbox.spec — MessageBubble renders those rows as chat bubbles and `.badge-accept` collided with `getByText("Diff #2")`. Fix: moved fixture rows to conv 3 (fix-daemon-binary); ledger spec switches workstream in `beforeEach`.

**Verification:** `tsc --noEmit` clean; Playwright 65/65 (4 new).

## Task 2 — GUI Wave A: "still running" visibility + attention-ordered Sidebar (accepted)

**Decision:** no daemon task registry (separate daemon wave); derive everything from `pending_counts` + `running_workstreams`.

**Changes (6 files):**
- `App.tsx` — `bgNotice` watch effect diffs the **raw** `runningWorkstreams` set (not the view-filtered one), first observation only seeds the baseline, current ws excluded, 4s TTL; `fgRunLabel` memo reverse-scans events for the latest `agent_tool_call`.
- `StatusBar.tsx` — chip → multi-target dropdown (TopBar overflow precedent: click-away, Escape, `aria-haspopup/expanded`); rows show name + "still running", click jumps; `.bg-flash-done` completion chip (`role="status"`, `--ok` tint); `.bg-flash-new` start tint; menu auto-closes when empty.
- `Sidebar.tsx` — active-project rows sorted Needs-input (pending>0) → Working → Idle, stable sort preserving daemon created_at order; remote projects and tree untouched; `.ws-activity-line`: fg rows show "Running: \<tool\>", bg rows fixed "still running".
- `app.css` — `.bg-runs-menu` (opens upward, reuses panel-float/shadow tokens), `.bg-run-*`, flash chips, `.ws-item-body/.ws-item-line/.ws-activity-line` two-line row body.
- `mock-invoke.ts` — **bug fix:** mock passed the fixture array by reference as `running_workstreams`; e2e in-place mutation made it `Object.is`-equal to React state → bailout, permanently stale UI. Mock now copies arrays at the response edge (same fix at the `auto_distill` site).
- `e2e/background-runs.spec.ts` — 6 specs (dropdown + jump, click-away, completion flash on dual drain, start tint, three-tier ordering + stable ties, fg tool label + line removal on stop).

**Boundary decisions:**
- "Done" tier deliberately not implemented — no done-observable exists in `pending_counts` + `running_workstreams`; Idle is the floor until the daemon wave lands.
- bg rows can only honestly show "still running" (non-focused conversations are never polled; anything finer would be fabricated).
- raw-set watch makes jumps immune: jumping to a bg run removes it from the filtered view but not the raw set → no false "finished".

**Verification:** `tsc --noEmit` clean; Playwright 69/69 (6 new). One mid-run failure (completion flash) root-caused to the mock identity bug above before full-suite pass.

## Cross-cutting facts
- Persistent-shell `cwd` is unreliable in this worktree; all commands after discovery used absolute paths.
- `node_modules` was missing; `npm ci` installed 135 packages in `gui/`.
- Component style rules applied mid-task: static string sets as `Record<K, true>`; one-expression helpers inlined.

## Open loops
- Daemon task registry (audit §3 #1, daemon wave) — prerequisite for real bg activity lines ("Running: go test 12m") and for the Sidebar "Done" sort tier.
- `timed_out` rendering is defensive only: the daemon does not journal that field yet (grep-confirmed); GUI shows the chip when present.
- `npx vitest run` is pre-existingly broken in this worktree (no vitest config; collects `e2e/*.spec.ts` → double-registration); declared out of scope, still open if the project wants a unit-test entry point.
- Both changesets are uncommitted/unpushed — user decision on commit.