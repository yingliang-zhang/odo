# GUI Stats, Context Meter & Panel Picker

- The prompt's data-source claims were stale on three counts and were corrected by grep before building: total_prompt_bytes is only journaled on user_message and review_action{action:run_prompt}, agent_done carries only {summary}, and receipts hold per-layer sha16 only — no byte breakdown (gui-wave-epoch-2)
- Context-pressure meter is an SVG ring with thresholds 50/80 that reverse-scans existing GUI events for the latest closure — zero new IPC; its popover lists verbatim receipt keys plus replay sub-receipts with nothing fabricated (gui-wave-epoch-2)
- The per-turn stats strip never fabricates a rate: it shows the byte branch ('in 1.1 MB · out 175 B') normally and the token branch ('7.0 tok/s') only when counts exist, with defensive input/output_tokens so the GUI auto-upgrades once the daemon journals usage (gui-wave-epoch-2)
- OMP usage is dropped at the adapter seam (adapter/omp.go), so billed usage requires a future daemon wave that parses usage at message_end and journals it into agent_done; the GUI is already ready for it (gui-wave-epoch-2)
- The MoA panel chip is read-only — model changes stay in SettingsPanel — and Wave B used existing CSS tokens only with new fixture turns placed in conv 3 per the Wave A collision lesson (gui-wave-epoch-2)
- A stray _write to an unrelated Dropbox path (MODE_BRIEFING.md) executed mid-stream; the repo was untouched and the anomaly was flagged rather than ignored (gui-wave-epoch-2)
