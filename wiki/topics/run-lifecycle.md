# Run, Workstream & Loop Lifecycle

- delete-vs-bootstrap atomicity: bootstrap's create section moved under the same lock with the `deletingWs` guard hoisted unconditionally before conversation lookup — closes the conversationless-lane bootstrap-under-deleted-workstream race (main-epoch-42)
- `handleDeleteWorkstream` checks daemon in-memory active state (run/distill/distillKind/slash/panel/loop/autoPending) then journal-derived loop activity before SQL delete; the residual sub-second guard→delete race is classified user self-inflicted with the pending-diff check as backstop (main-epoch-38)
- Atomic run-start registration guard: new struct field + helpers beside `checkConversation`, wired at every start site (send tail, slash routes, auto fire/arm, preview slash slot, distill entry, loop start); `handleDeleteWorkstream` restructured around the atomic flag (main-epoch-39)
- Daemon-side liveness drain: `runLivenessDrain` mounted in `NewServer` (2s interval, atomic toggle) makes "GUI-closed loops continue" true for agent-run phases — previously a documented-design falsehood (C11) because `drainRun` only ran from `pollLocked` (main-epoch-14)
- C10 loop-admission atomicity: state fold + `loop_started` journal append happen in one critical section in both start paths; the early fold survives only as a fast-reject hint with existing error precedence preserved (main-epoch-15)
- `retireRun` selects the diff's own run by `worktreePath` match (`byConv` binding used only when no match) — previously reviewing an older diff could close a newer run's worktree or kill an in-flight auto-land verify (main-epoch-28)
- Loop fold efficiency: `loopRowPayload` performs one JSON decode per row instead of 2–4 per-key decodes (main-epoch-23)
- Archived-lane actionability: `checkConversation`'s chain carries no status predicate and delete is a status flip, so stranded rows on archived workstreams remain resolvable via IPC with `actor:"human"` (bug-fix-epoch-8)
- Workstream-switch state isolation: per-conversation expansion state and captured `(cid, root)` guards on in-flight responses prevent cross-workstream leaks (UI-epoch-12) (UI-epoch-8)
