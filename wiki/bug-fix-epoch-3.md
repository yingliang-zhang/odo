> SUPERSEDED by curator: fully merged into topic pages — retained for citation liveness, excluded from recall injection.

# Advisory slash commands (`/panel`) freeze the composer — root cause and GUI fix

## Problem

Sending `/panel` in the main stream made the GUI feel deadlocked: every workstream's composer input became uneditable for up to minutes, with the draft left sitting in the box as if the prompt was never sent.

## Root cause chain

1. `/panel` runs **synchronously inside the daemon `send_message` RPC** (`handlePanelQuery`): it persists the user message, fans out to N models in parallel with multi-turn tool loops, and returns only after `wg.Wait()`. The RPC can hang for minutes.
2. `ChatSurface.submitDraft` held `sending=true` across `await onSend(...)`; the textarea is `disabled={sending…}` and the draft was cleared only after the await resolved.
3. `sending` lives on the single global ChatSurface instance → **all** workstream composers locked. The screenshot showed exactly this: residual draft plus a WKWebView-stuck IME marked-text artifact.
4. The only feedback was one easy-to-miss "Panel consulting models…" spinner line in the chat area.

## Decision

Fix purely in the GUI; the daemon RPC contract is unchanged. Advisory slash submits are **detached from the composer await**: the box clears and unlocks immediately while the background promise still drives the spinner, and the draft is restored if the daemon rejects instantly (busy gates).

This creates one accepted semantic change: users can now send normal messages while a panel consult is in flight (previously physically impossible). Judged safe — panel is read-only, the `slashing` slot only rejects distill, advisory answers carry the `panel` flag (excluded from fold/replay), and the same concurrency was already reachable from multiple clients.

## Code changes

| File | Change |
|---|---|
| `gui/src/slash.ts` (new) | `isAdvisorySlash()` — advisory detection matching daemon routing exactly (`/panel`, `/vision`, `/preview` followed by space or end) |
| `gui/src/components/ChatSurface.tsx` | Advisory submit bypasses the `sending` lock: clears/unlocks immediately via extracted `clearComposer` (includes WKWebView IME force-clear); restores draft on instant daemon rejection |
| `gui/src/App.tsx` | Uses shared `isAdvisorySlash`; fixed sibling bug — advisory sends no longer set `agentRunning` (which briefly flipped the composer into steer mode + Stop button until the next poll) |
| `gui/src/dev/fixtures.ts`, `gui/src/dev/mock-invoke.ts` | `advisorySend` test latch: mock now mirrors real daemon timing — persist question (poll-visible) → hold RPC → persist answer + done |
| `gui/e2e/advisory-slash.spec.ts` (new) | 2 cases: (1) composer clears/stays usable during consult, question lands via poll, answer arrives on release; (2) daemon rejection restores draft + shows error banner |

## Verification

- Control group: with the ChatSurface fix stashed, case (1) fails (19.4 s lock timeout); with the fix, it passes.
- Full suites green: e2e 108/108 (incl. steer-composition IME regression), vitest 80/80, `tsc --noEmit` clean.
- Changes left uncommitted in the worktree for the normal diff flow.

## Open loops

- Fix is uncommitted in the worktree — awaiting the user's normal review/diff-and-land decision.