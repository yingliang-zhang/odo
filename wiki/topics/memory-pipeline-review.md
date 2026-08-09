# Auto-Distill and Auto-Curate Pipeline Review

- Hermes 75% context-occupancy trigger deemed inapplicable — Odo is an OMP one-shot with no continuous context window; keep the idle trigger but add countdown/cancel affordance, composer lock, and startup compensation (run ending after app close permanently misses the trigger) (epoch-7)
- Chained auto-curate-after-distill judged unreasonable: cost doubles / O(N²), whole-layer rewrite has no human gate, poor distills get amplified, failures have no rollback → switch to conditional trigger (≥N new notes or time interval) plus a quality gate (epoch-7)
- Minimum defense line: provenance back-links — every epoch-note conclusion must cite its journal seq range with cheap mechanical validation; retraction can only catch "overturned" claims, never "never happened" ones (epoch-7)
- Schema decision: distill events explicitly record firstSeq/lastSeq/note_path/note_sha (current state relies on UI-derived lastDistillSeq, an implicit contract); curate notes_read carries SHA; epoch naming unchanged (epoch-7)
