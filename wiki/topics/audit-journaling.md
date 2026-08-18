# Audit & Journaling Coverage

- The A-P0 #2 visible⟺logged audit found no residual: every moa.Query/QueryWithTools/QueryWithImages call site in internal/ipc (panel, vision, manual review_diff, auto-land gate, skill gate) journals a receipt, verified by grep with file:line evidence (daemon-misc-epoch-1)
- The auto-land gate is fail-closed on journaling: journal failure at autoland.go means the diff is NOT landed (daemon-misc-epoch-1)
- Distill inputs are journal-derived, curator markers pin notes_read[{name,sha16}], and the learner is anchored by distill's note_sha (daemon-misc-epoch-1)
- Deferred lanes were explicit non-gaps: byte-exact receipts went to R-W1.5, learner memory-file input hashing stays scheduled under R-W8, and vision was excluded from R-W1.5 because image_sha16 already anchors the bulk bytes (daemon-misc-epoch-1)
- The GUI renders timed_out defensively only — the daemon does not journal that field yet (grep-confirmed), and the chip appears only when present (gui-wave-epoch-1)
