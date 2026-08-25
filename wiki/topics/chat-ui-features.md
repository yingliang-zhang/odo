# Chat UI Features

- The steer queue is a display layer over the pre-existing daemon queue (`queuedSteers` drained at turn boundaries): journal-only derivation (`user_message{steer:true}` minus `run_prompt.steer_seqs`/`steer_dropped`) survives restarts with zero new polling, and every journaled steer ends consumed or dropped — ledger closed (UI-epoch-4)
- Ghost queued steers after daemon restart are closed at boot by `recoverOpenSteers` (production evidence: three historical dangling steers closed at one binary swap, none after) (UI-epoch-5) (UI-epoch-6)
- SteerQueue Drop two-stage confirm state is preserved across poll-tick re-derivations (UI-epoch-5)
- Deliberate Hermes deviations: queue items are not editable (Drop + retype instead — no un-steer edit channel exists) and backing is journal-based rather than localStorage for cross-restart/multi-window consistency (UI-epoch-4)
- StatusBar popovers went fully transparent because twMerge treats any `bg-*` class as the background-color group — the inert e2e marker `bg-runs-menu` discarded the opaque base background; renamed to `runs-menu` with a comment warning against `bg-`-prefixed markers inside PopoverContent (UI-epoch-7)
- Tailwind preflight's `svg{display:block}` stacked icons above text inside non-flex chips; the `status-run` chip and path-copy button gained `inline-flex items-center gap`, with the text moved into a `min-w-0 truncate` child so ellipsis still works (UI-epoch-3)
- Context-pressure meter, per-turn stats strip, and panel chip were built with honest derivation (wall time + bytes; tok/s only when counts exist; verbatim receipt keys) and zero new IPC by reverse-scanning existing journaled events — nothing fabricated (gui-wave-epoch-2)
- Right-sidebar close X was replaced by a TopBar toggle mirroring the left-sidebar pattern, ⌘J unchanged, with the panel e2e rewritten as an open/close round-trip (UI-epoch-7)
