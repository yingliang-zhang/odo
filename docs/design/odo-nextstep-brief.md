# Odo Next-Step Priority Evaluation — Tri-Model Brief

## 1. Project Context

Odo is a Tauri 2 + React 18 + Go daemon desktop app (macOS only) for AI-assisted coding with memory. All milestones M0–M11 are complete (147 commits, ~27K lines). The GUI has been polished (PR1-3: CSS systemBlue/SF Pro, Settings inspector, TopBar declutter). 43/43 E2E tests pass.

**Stack**: Go daemon (SQLite journal, OMP adapter, worktree plumbing) → Tauri IPC → React 18 + Vite frontend. Single CSS file (`app.css` ~3900 lines). OMP CLI as the model transport (no direct LLM API calls). MoA = proposer-only fan-out (no aggregator layer — OMP is a black box).

**Architecture invariants** (from README):
- Every commit traces to a pain point
- Close the loop before hardening it
- Invisible work is presumptive scope creep
- Implementation does not review its own output

## 2. Current State (verified)

| Area | Status |
|---|---|
| Core loop (send → agent → diff → accept/reject) | ✅ Complete (M1) |
| Memory (6-layer: journal → epoch notes → topics → memory.md → user.md → ledger.md) | ✅ Complete (M3-M6) |
| Skill distillation + three-tier gating | ✅ Complete (M8-M9) |
| Auto-distill (idle timer, client-side) | ✅ Complete (M10) |
| Multi-project | ✅ Complete (M11) |
| GUI polish (CSS, Settings inspector, TopBar) | ✅ Complete (PR1-3) |
| E2E (Playwright, 43 tests) | ✅ All pass |
| Go tests | ✅ All pass |
| Experiment ledger | ❌ Not created |
| README | ❌ Stuck at M0-M7 (M8/M9/M10/M11 + GUI audit + PR1-3 not documented) |

## 3. Known Planned Items (from README "Planned" section)

1. **Cross-examiner** — one-shot mid-discussion second opinion at decision points (config in `prefs.md` mentioned, handler not yet wired)
2. **P2 polish** — WCAG AA contrast, aria-busy spinners, in-app confirm dialogs, command palette combobox wiring, fuzzy search
3. **Experiment ledger** — `docs/experiment-ledger.md` for cross-session experiment tracking

## 4. Additional Candidate Items (from prior audits and session memory)

4. **README update** — M8/M9/M10/M11 + GUI audit + PR1-3 not documented; README still says "M0-M7 complete"
5. **Dead code cleanup** — prior audit found dead Go code (NewOMPExplicit, QueryModel, moaReviewTimeout) and 6 stale comments
6. **Auto-distill real integration** — idle detection logic needs wiring in App.tsx (M10 added settings UI but idle timer may not be fully connected)
7. **AGENTS.md generation** — DESIGN LOCK D8: daemon generates .odo/AGENTS.md from memory.md + pins.md, defines precedence (Odo prompt > OMP hindsight). Not yet implemented.
8. **/panel + /vision route separation** — these routes share the 120s send_message budget; should be separated for long-running panel/vision operations
9. **git diff per-run lock** — current diff uses s.mu (global mutex); should use per-run diff-lock
10. **Diff line numbers** — hunk header parsing for old/new line numbers in diff viewer
11. **Diff split view comments** — 💬 comment button currently only in Inline view, not Split view
12. **Vision support** — route image prompts to vision-capable model lanes (K3-based vision proxy + browser-screenshot-after as finishing layer)

## 5. Questions

For each candidate item, evaluate:
- **Value**: What user pain does it solve? How visible is it?
- **Cost**: Estimated complexity (lines, files, time)
- **Risk**: What can go wrong? Does it touch core paths?
- **Priority**: NOW (do next) / DEFER (do later) / WONTFIX (delete from roadmap)

**Q1**: Which items should be done NOW vs DEFER vs WONTFIX?
**Q2**: Are there items not on this list that should be?
**Q3**: Is the experiment ledger still relevant, or should it be WONTFIX given Odo is a single-user app?
**Q4**: Should AGENTS.md generation (D8) be prioritized given it bridges Odo-OMP memory precedence?
**Q5**: Is vision support worth building now, or is it premature for a text-first coding assistant?

## 6. Constraints

- macOS only (Tauri webview)
- OMP CLI is the only model transport (no direct API)
- Single CSS file architecture
- No new dependencies unless justified
- Apple HIG design language
- Every feature must trace to a user pain point

Write your complete analysis as text in your response. Do NOT write files to the repository.
