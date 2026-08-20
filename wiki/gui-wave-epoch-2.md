> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# GUI Wave B — Conversation Summary

**Task:** Implement three GUI-only features (no daemon changes): context-pressure meter, per-turn stats strip, MoA panel picker. Sources: harness-gui-tri-model-audit §3 items #5/#8/#9 and audits-summary row #11.

## Key decisions

### Prompt data-source corrections (grep-verified; prompt was stale on 3 counts)

| Prompt claimed | Reality | Resolution |
|---|---|---|
| `total_prompt_bytes` in pending_counts / bootstrap | Only journaled on `user_message` and `review_action{action:"run_prompt"}` (`internal/ipc/server.go:997`) | Reverse-scan existing GUI events for latest closure — zero new IPC |
| `agent_done` carries usage | Payload is only `{summary}`; OMP usage dropped at adapter seam (`internal/adapter/omp.go:702`) | Honest derivation: wall time + in/out bytes; defensive `input/output_tokens` so GUI auto-upgrades to tok/s once daemon journals usage |
| Per-layer byte breakdown | Receipt has per-layer **sha16** only | Popover lists verbatim receipt keys + replay sub-receipts + held_back — raw journal values, nothing fabricated |

### Design choices
- SVG ring, thresholds 50/80 (ok→warn→err), click expands composition popover — modeled on dsh ContextMeter.
- Stats strip never fabricates rate: byte branch `in 1.1 MB · out 175 B`; token branch `7.0 tok/s` only when counts exist.
- Panel chip read-only; model changes stay in SettingsPanel.
- Existing CSS tokens only; conv 1 fixtures untouched (Wave A collision lesson), new turns in conv 3.

## Code changes (8 files, GUI-only)

- `gui/src/stats.ts` (new): window mirror table, 4 B/tok heuristic, `deriveLastPrompt` / `deriveTurnStats` / `parseReviewModels`
- `types.ts`: payload keys for receipt/replay + defensive usage tokens
- `StatusBar.tsx`: `ContextMeter`, `PanelChip`, shared `useCloseOnClickAway`
- `App.tsx`: `getSettings` fetch, `lastPrompt` memo, wiring
- `ChatSurface.tsx`: RunHeader stats strip
- `app.css`, `fixtures.ts`, `e2e/wave-b.spec.ts` (5 specs)

## Verification

- `tsc --noEmit` clean (fixed lucide `Settings` × types `Settings` collision via alias)
- Playwright **74/74** (one mid-run failure was a wrong assertion, corrected to honest semantics; a full-suite wipe was dead-shell-cwd breaking vite — environmental)
- Browser screenshots: ring red ~86%, meter popover, Panel ×3 popover, both strip branches

## Anomaly flagged

Stray `_write` to an unrelated Dropbox path (MODE_BRIEFING.md, "fast_embed harness") executed mid-stream; repo untouched — flagged, not ignored.

## Open loops

- Billed usage needs a daemon wave: adapter parses OMP usage at `message_end` and journals into `agent_done` (GUI already ready).
- Per-layer **byte** breakdown, if wanted, requires daemon to attach byte counts to receipt values.
- Three features uncommitted — commit decision left to the user; file slices map cleanly onto three independent commits.