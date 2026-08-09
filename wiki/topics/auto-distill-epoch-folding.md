# Auto-Distill and Epoch Folding UX

- "No output this time" was diagnosed as epoch folding, not data loss: after a run ends, 60s idle triggers auto-distill, and ChatSurface then shows only events with seq > lastDistillSeq, hiding the prior Q&A which remains intact in the journal (epoch-4)
- Concrete trigger chain: run ended 18:23:46 → auto-distill at 18:24:46 → completed 18:25:32 → review_action(distill) seq 278 → GUI hid all events ≤278 including rollback Q&A seq 179–275 (epoch-4)
- Machine settings confirmed at ~/.odo/prefs.md:45-47: `auto_distill: on_idle`, `auto_distill_idle_seconds: 60`, `auto_curate_after_distill: true`, matching both fold timestamps 18:19:49 and 18:25:32 (epoch-3)
- Mechanism code: epoch filter in ChatSurface.tsx:456-466; auto-distill armed in App.tsx:906-947 (epoch-4)
- The day saw two folds: the first went unnoticed because the user sent a new message 4 seconds later; the second happened while away, causing the confusion (epoch-4)
- Open UX decision awaiting user: A. set `auto_distill: never`; B. empty-state copy distinguishing "new conversation" vs "folded to wiki/xxx.md (click to open)"; C. distill toast noting the chat was folded to wiki (epoch-4)
