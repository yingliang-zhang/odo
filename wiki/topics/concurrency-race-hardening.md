# Concurrency & Race Hardening

- All auto-distill spawns, timer callbacks, and the recoverPendingDiffs fan-out are wrapped into the Server waitgroup with rig.stop draining in-flight work (measured 3.7–3.9s); previously they wrote wiki/journal after TempDir cleanup and store close (main-epoch-42)
- delete_workstream vs bootstrap atomicity: the deletingWs flag is hoisted unconditionally before the conversation lookup and bootstrap creation runs under the same lock with the guard, closing the conversationless-lane bootstrap-after-delete race (main-epoch-42)
- handleDeleteWorkstream checks daemon in-memory active state (run/distill/slash/panel/loop/autoPending) then journal-derived loop activity before the SQL delete; a residual sub-second guard-to-delete race is classified user self-inflicted with the store pending-diff check as backstop (main-epoch-38)
- Concurrent panels no longer corrupt progress: panelProg is a per-consult batch-group slice, defer removes only its own batch, and the poll snapshot merges — ending Done > Total and mixed legs (main-epoch-38)
- Workstream fossil duplicates are deduped at migrateV4 (winner keeps the name, loser gets -dup-<id>) plus a partial unique index on status='active'; CreateOrGet re-reads the winner row on constraint conflict (main-epoch-38)
- Loop admission is single-winner atomic: state fold and loop_started journal append happen in one critical section (the early fold survives only as a fast-reject hint), pinned by a 4-goroutine single-winner race test (main-epoch-15)
- Fold commit atomicity: the epoch bump and fold marker commit in a single store transaction via CommitFold (main-epoch-23)
- Store v4 dedupe is collision-free with a dedicated collision test (main-epoch-39)
- Run-start duplication is closed by an atomic registration guard wired at every start site: send tail, slash routes, auto fire/arm, slash slot, distill entry, and loop start; handleSendMessage holds s.mu for its whole body (main-epoch-39)
- Panel legs carry an outer deadline via WithTimeout(legTimeout with a test seam); the same unbounded defect was found and fixed in the shared reviewWithModel funnel and the /vision and /preview legs (main-epoch-15)
- Failpoint seams on the Server struct enable crash-injection tests of the memory recovery protocol (main-epoch-39)
