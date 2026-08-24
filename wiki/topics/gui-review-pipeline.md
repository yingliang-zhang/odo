# GUI Review Pipeline Surfaces

- `pipelineHumanLocked` truth table: `in_flight` and `landing` lock human review buttons; `queued`, `revise`, `blocked`, `suspended` stay usable because those are states where human action is the escape hatch; scope is active-conversation-only (UI-epoch-4)
- Permanent GUI lock root cause: `derivePipelineStates.latestChainRow` treated `auto_revise_product` bookkeeping rows as chain state -> `in_flight` -> lock, and the row is never superseded on the origin; fix skips those rows so the origin falls back to its round row and stays human-decidable (UI-epoch-10)
- The `auto_revise_product` skip is pinned by a regression test using the real 21->22 chain shape (pipeline.ts:126), with `product_diff_id` typed on the event payload (UI-epoch-11)
- LedgerPanel renders guardian `review_action` receipts (actor auto/human, risk severity ramp, timed-out chip, blocked reasons) from the existing events state with zero new IPC; bookkeeping actions (distill/curate/todo_merge) stay on the old surface (gui-wave-epoch-1)
- Human reject via the daemon socket always passes — `handleDiffAction` has no pipeline lock, only acceptMu + status checks; used to reject #21 when the GUI was unclickable due to the very bug being fixed (UI-epoch-10)
