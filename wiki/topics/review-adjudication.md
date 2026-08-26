# Review Adjudication Methodology

- Every third-party/panel claim is verified against source before acting: triage tables (claim → verdict → disposition) with 'valid → fixed', 'rejected → evidence' — e.g. deepseek's comparator hole fixed, memo-defeat and Lstat-leaf charges rejected with citations (main-epoch-33)
- Blocked-diff adjudication runs on bytes, not vibes: archive sha16 recompute, payload path attribution, same-bytes reproduction; block + identical-bytes-green ⇒ manual accept; block + real failure ⇒ fix and repack (main-epoch-28)
- Patch regeneration before accept follows drain semantics (`git add -A && git diff --cached HEAD`); safe only while the panel never attested (no `patch_sha16` binding broken) — a new sha invalidates stale blocks bound to the old one (main-epoch-22)
- Ledger divergence is reconciled by journal `ledger_correction` appends with `corrects_seq` — never silent store mutation; terminal statuses are left as-is per convention (main-epoch-27)
- Instrumentation probes (App.tsx logging, probe specs) added for diagnosis are fully reverted; scratch artifacts never enter the final diff and worktree cleanliness is proven before packaging (main-epoch-28)
- Parallel execution owns files by lane with conflict serialization (one agent owned server.go + internal/git); merge seams from concurrent editing are manually spot-checked and contract hunks diff-stat verified (main-epoch-30)
- Panel rejection analysis distinguishes mechanism from content: unanimous attestation bypass for gate files, split-verdict repair-ladder routing, and fabrication risk (e.g. glm's factually refuted reasons) all legible from journal seqs (main-epoch-27)
- Review-fix ordering by expected value: racey failures with stable reproducers first, heaviest semantic changes (project outbox) last (main-epoch-42)
- Reject-before-accept ordering when a superseded pair exists: close the older diff's recovery race window first so a daemon restart can't re-fire it; archived patches make rejection lossless (main-epoch-41)
