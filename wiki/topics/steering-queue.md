# Steering Queue

- Daemon was already a queue (`handleSteering` appends to `meta.queuedSteers`, drained at run end into continuation run); the change was visibility layer + fail-closed fixes, zero scheduling change (UI-epoch-4)
- Journal-only queue derivation: `user_message{steer:true}` minus `run_prompt.steer_seqs`/`steer_dropped` closures — survives restarts and switches, no new polling (UI-epoch-4)
- Fail-closed daemon fixes: orphan steers rejected (no active/finished run), `queuedSteers` carry journal seqs, `run_prompt` receipts gain `steer_seqs`, `drop_queued_steer` IPC, batched `steer_dropped{cause}` on every abandon path — every journaled steer ends consumed or dropped (UI-epoch-4)
- Diff #9 panel-rejected on 3 real defects: ghost steers after daemon restart (undeletable), Drop two-stage confirm reset by poll heartbeat, mixed go+gui diff ran only go verify; fix round landed as diff #11 — lesson: do not force-accept panel-flagged diffs (UI-epoch-5, UI-epoch-4, UI-epoch-6)
- `recoverOpenSteers` restores queuedSteers from journal on startup and closes historically dangling steers — production evidence: seq 2641 closed steers 405/653/659 at binary-swap restart (UI-epoch-5, UI-epoch-6)
- SteerQueue Drop-confirm/disarm state survives poll-tick re-derivation (e2e pinned); journal-invisible aspect still needs manual GUI confirmation — open loop (UI-epoch-5, UI-epoch-6)
- Hermes alignment vs deviations: not editable (Drop+retype instead — no edit IPC), journal-backed instead of localStorage for cross-restart/multi-window consistency (UI-epoch-4)
- Steering end-to-end proven: enqueue during active run → agent_done → drainRun → `run_prompt{origin:"continuation", steer_seqs:[…]}` — ledger closes exactly (UI-epoch-6)
