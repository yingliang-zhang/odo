# Workstream Switching: Performance & Scroll

- Switch stall had four stacked causes: full journal replay per bootstrap, daemon global lock held over SQL+file I/O, unconditional `generateAgentsMD` write, zero GUI caching (UI-epoch-8)
- Fix: stale-while-revalidate per-conversation LRU cache + incremental bootstrap (`afterSeq`+`conversationId`) — seq-merge is lossless because the journal is append-only; fallback full replay on cold cache (UI-epoch-8)
- Lock restructure: snapshot under `s.mu`, SQL/file reads outside lock (`latestDiffInfo`/`pendingDiffInfos` verified lock-free-safe); `generateAgentsMD` skips write when content identical (UI-epoch-8)
- Optimistic flip with real rollback: restores workstream+name, `rootFlipped` computed at flip time, in-flight `handleSend` responses guarded by captured (cid, root) (UI-epoch-8)
- Scroll repin bug: ChatSurface does not remount on workstream switch, so `stickRef=false` leaked across sessions; fix resets stick/pill and pins bottom on conversationId change (bug-fix-epoch-2)
- Test design critical: two sessions must have unequal-length histories (12 vs 72) — equal lengths false-pass via scroll anchoring (bug-fix-epoch-2)
- Auto-follow: un-stick ONLY on explicit user gestures (wheel-up, touch pull-down, scrollbar drag), never scroll events — content-visibility resolution moves scrollTop passively (UI-epoch-2)
- Open loop: rollback-on-failure e2e failed at session end blocking auto-land; no before/after latency numbers captured (UI-epoch-8)
