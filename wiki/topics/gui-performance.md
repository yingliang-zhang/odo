# GUI Performance & Switching

- Keep-alive panels: six panels moved off conditional-mount to `export default memo(...)` (ChatSurface pattern) plus an App aggregate setState prev-bail comparator reusing the diff_stable family — quiet tick now produces zero re-renders (main-epoch-30) (main-epoch-32)
- `hidden` skips layout/paint but not reconciliation, and `App.tsx` periodically wrote fresh aggregate objects, so all visited panels re-rendered on every tick before the prev-bail fix (main-epoch-32)
- MemoryPanel deep-linking under keep-alive is handled with a nonce scheme that preserves `initialTab` semantics (main-epoch-30)
- Switch-cache: a per-conversation LRU journal cache renders instantly on click (stale-while-revalidate) while the bootstrap merges by seq — lossless because the journal is append-only; the daemon honors `afterSeq`+`conversationId` for incremental replay (UI-epoch-8)
- Daemon lock restructure: `handlePollEvents`/`handlePendingCounts` snapshot under `s.mu` then run SQL/file I/O outside the lock; verified the touched helpers were already lock-free store+file operations (UI-epoch-8)
- Optimistic workstream flip carries a full rollback (restore `workstream`/`workstreamNameRef` via a `rootFlipped` boolean computed at flip time) and guards in-flight responses by captured (cid, root) (UI-epoch-8)
- Auto-follow un-sticks only on explicit user gestures (wheel-up/touch pull-down/scrollbar drag) with 250ms suppression around programmatic writes — scroll anchoring and content-visibility size resolution move scrollTop passively and were misread as user intent (UI-epoch-2)
- Switching conversations re-pins to the bottom via a conversationId-change effect resetting `stickRef` — ChatSurface does not remount on switch, so stick state previously leaked across sessions (bug-fix-epoch-2)
- The resize grip at `z-0` was painted under header/body, pinning width at 380px; raised to `z-20` with the hit area extended 4px right, preserving the `px-2` header contract (main-epoch-32)
- `generateAgentsMD` skips its write when content is identical — it previously rewrote the file on every bootstrap (UI-epoch-8)
- Human review buttons lock only on `in_flight`/`landing` phases via one shared truth table (`pipelineHumanLocked`); queued/revise/blocked/suspended stay usable because human action is the escape hatch, and the derivation must skip `auto_revise_product` bookkeeping rows or the lock becomes permanent (UI-epoch-4) (UI-epoch-10)
