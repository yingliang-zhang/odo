# GUI Visibility & Workstream Surfaces

- LedgerPanel renders review_action journal rows as receipt cells above ledger.md using the existing events state (bootstrap replay + poll) with zero new IPC; bookkeeping actions (distill/curate/todo_merge) stay on the old surface (gui-wave-epoch-1)
- Risk severity ramp maps classes to colors red→amber→yellow→blue→gray, and risk-unrated is dashed-neutral so pre-W5 unrated rows never masquerade as clean; unknown classes fall back to forward-compat neutral (gui-wave-epoch-1)
- Background-run visibility derives everything from pending_counts + running_workstreams with no daemon task registry; the 'Done' sort tier was deliberately not implemented because no done-observable exists in those fields (gui-wave-epoch-1)
- The bg-notice watch diffs the RAW runningWorkstreams set rather than the view-filtered one, so jumping to a background run cannot produce a false 'finished' flash (gui-wave-epoch-1)
- Sidebar active-project rows sort Needs-input → Working → Idle with a stable sort preserving daemon created_at order; bg rows honestly show only 'still running' because non-focused conversations are never polled (gui-wave-epoch-1)
- mock-invoke bug root-caused: fixture arrays passed by reference made in-place e2e mutation Object.is-equal to React state causing bailout and permanently stale UI; fixed by copying arrays at the response edge (gui-wave-epoch-1)
- Real bg activity lines ('Running: go test 12m') and the Done tier require the deferred daemon task registry wave (audit §3 #1) (gui-wave-epoch-1)
