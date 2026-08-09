# Epoch Folding and Auto-Distill UX

- "Missing output" diagnosed as auto-distill epoch fold, not data loss: distill completing at seq 278 made ChatSurface.tsx:456-466 filter seq > lastDistillSeq, hiding the rollback Q&A; data intact in journal seq 179–275 (epoch-6)
- Config confirmed in ~/.odo/prefs.md: auto_distill on_idle / 60s / auto_curate_after_distill true, matching fold timestamps 18:19:49 and 18:25:32; folded twice in one day, first masked by a message 4s later — systematic silent-fold failure mode (epoch-5)
- Adopted panel's upgraded root fix over A/B/C options: schema explicitly records fold boundary (firstSeq/lastSeq/note_path/note_sha) plus a persistent, expandable chip "已折叠 N 条→epoch-K｜展开｜打开 note" at the fold point (epoch-7)
- Final review judged chip+schema a cure, not a patch — reframes "trust the lossy function" as "lossy function + falsifiable ledger + reversible operation"; watermark criterion: rehydrate must be a first-class model-side tool, not only UI layer (epoch-7)
- Implementation order recommended by panel: ① schema records lastSeq (zero-UI foundation) ② fold chip + empty-state binary ③ edge hardening + countdown ④ toast with path ⑤ progress pill ⑥ back-link validation ⑦ conditional curate (epoch-7)
- Root fix still unimplemented as of latest note: schema lastSeq recording, ChatSurface persistent chip, and empty-state distinguishing "new" vs "folded to wiki" all pending (epoch-7)
