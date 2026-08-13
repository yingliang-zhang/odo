# Frozen Brief — Tri-Model GUI/Interaction Audit: deepseek-harness vs grok-build vs openai/codex (for Odo)

You are analyst **__LEG_ID__**, one of three independent analysts comparing the
GUI and interaction design of three open-source coding-agent harnesses. Your
output will be consolidated with two other models' analyses by an orchestrator.
Be evidence-grounded: every claim must cite a file path (plus a symbol —
component/type/function name — wherever possible) that you actually read.

## 1. Context

**The three repos (existence + metadata verified via GitHub API on 2026-08-13):**

| Repo | Lang | License | Stars | Notes |
|---|---|---|---|---|
| `deepseek-ai/deepseek-harness` ("dsh") | TypeScript (pnpm monorepo) | MIT | ~19k | Published 2026-08-13; Web UI, **no TUI** |
| `xai-org/grok-build` | Rust workspace | Apache-2.0 | ~25k | Fullscreen mouse-interactive ratatui TUI + leader daemon |
| `openai/codex` | Rust workspace | Apache-2.0 | ~105k | ratatui TUI + app-server JSON-RPC daemon + IDE/desktop |

**For whom:** Odo (`~/Projects/odo`), a personal Research Coding OS —
Tauri 2 WebView + React GUI + a single Go daemon. The GUI lives in
`~/Projects/odo/gui/src/` with these components already built:
ChatSurface, MessageBubble, ToolTicker, DiffViewer (Accept/Reject lanes),
CommandPalette, ContextPanel, LedgerPanel, MemoryPanel, PlanChip,
SettingsPanel, Sidebar, SkillsPanel, StatusBar, TopBar, WikiBrowser.
Odo's vision: long uninterrupted autonomy — a gating ladder replaces human
diff review, asynchronous "parked" reviews replace synchronous gates, and a
goal/workstream model lets several runs proceed concurrently.

## 2. Scope rules (read carefully)

- **GUI and interaction ONLY.** Do NOT audit: governance/openness, licenses,
  telemetry, sandbox internals, wire protocols, memory/recall subsystems,
  edit grammars, compaction, agent-loop internals — except where they surface
  *in the UI/interaction* (e.g. an approval prompt, a compaction indicator).
- **Read-only everywhere.** Do NOT modify anything under `~/Projects/odo`.
  Do NOT run the three repos' installers, builds, or agents — static source
  audit only. You may run `ls`, `grep`, `cat` on the clones and Odo's `gui/`.
- **Blindness:** do NOT read anything under `~/Projects/odo/docs/compare/` —
  prior audit material lives there and would contaminate this leg.
- Do NOT re-audit Odo deeply. Skim `gui/src/App.tsx` + component filenames
  only to make Q3 mappings concrete (e.g. "maps to DiffViewer +
  daemon ipc review call").
- If a surface is absent (e.g. dsh has no TUI), say "absent" — never infer.

## 3. Repos to clone (shallow, unique scratch)

`git clone --depth 1` each into `/tmp/harness-src-__LEG_ID__/`:

- https://github.com/deepseek-ai/deepseek-harness
- https://github.com/xai-org/grok-build
- https://github.com/openai/codex

Navigation hints (verify before citing; these are entry points, not conclusions):

- dsh: `packages/bundle/web-app/` (React/Vite web UI), `docs/subsystems/*.md`
- grok: `crates/codegen/xai-grok-pager/` (TUI), `xai-grok-pager/docs/user-guide/*.md`
- codex: `codex-rs/tui/`, `codex-rs/app-server*/`, `codex-rs/core/`

## 4. The questions

### Q1 — Frontend architecture (table + ≤5 lines prose)
Per repo: UI technology & framework; how the UI process talks to the agent
(stdin/stdout? IPC socket? JSON-RPC? same process?); which surfaces exist
(TUI/web/desktop/IDE) and which share one session authority; headless↔UI
parity (what the headless mode can do that the UI can't, and vice versa);
how much UI state is derived from a durable log vs held ephemerally.

### Q2 — Interaction-pattern inventory (the core of this audit)
For each repo, inventory **with evidence** the mechanisms below. Mark each
present/absent/partial with `repo:path:Symbol`:

1. Streaming rendering (tokens, tool calls, thinking/reasoning display)
2. Approval/permission UX (how a risky action is presented, answered, remembered)
3. Interrupt & steering UX (esc/cancel, queued input, mid-turn injection, "park this")
4. Input editor (multiline, paste, file/image attach, history, vim mode)
5. Command discovery (slash commands, palette, fuzzy find, help surfaces)
6. Session management UX (resume/fork/rewind, multi-session view/dashboard, switching)
7. Background work visibility (tasks list, progress, completion notifications)
8. Plan & todo display (plan mode, checklists, progress)
9. Change/diff presentation (per-file diffs, review-actions, undo)
10. Model & effort selection UX (picker, per-run switch, cost/token display)
11. Status & context display (context usage, tokens, cwd, git branch, errors)
12. Mouse/keyboard support (keybindings, scrollback, copy-out, accessibility/i18n)

Then name the **3 most consequential interaction divergences** across the three.

### Q3 — GUI/interaction features worth borrowing into Odo (ranked, 6–12 total)
For each candidate:
1. Feature name; 2. What it does (1–2 sentences);
3. Evidence `repo:path:Symbol`; 4. Which repo(s), and who does it best;
5. Odo mapping — name the concrete Odo component (from §1 list) or daemon
   touchpoint; 6. Adoption cost S (<1d) / M (1–3d) / L (>3d) + one-line why;
7. Fit risk — adversarial: why it might NOT fit Odo (single researcher,
   desktop app, long autonomy, MoA default-on); 8. Priority P0/P1/P2 +
   confidence High/Med/Low.

### Q4 — Anti-patterns + honest strengths
- Which interaction patterns should Odo explicitly NOT copy, and why
  (cite evidence; e.g. patterns that fight "long unattended autonomy"
  or desktop-GUI ergonomics)?
- What does Odo's GUI already do BETTER than all three (2–4 honest items,
  anchored to the §1 component list)?

## 5. Output format (required structure)

```
## A. Executive summary (≤10 lines)
## B. Q1 frontend-architecture table + key divergences
## C. Q2 interaction-pattern inventory (12 numbered groups, per-repo cells)
## D. Q3 ranked GUI feature-borrow list (8 fields per item)
## E. Anti-patterns to avoid + what Odo already does better
## F. Surprises / unexpected finds
```

Keep prose tight; use tables over walls of text.

## FINAL-MESSAGE CONTRACT

Your reply may take many tool calls — that is expected. But the session's FINAL
assistant message must contain the COMPLETE structured deliverable (sections
A–F). Do NOT stop after dumping intermediate evidence notes; if you finish
gathering, keep writing inside the same turn until the full report is in the
final message. The final message must start with "## A. Executive summary".
