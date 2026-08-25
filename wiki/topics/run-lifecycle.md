# Run & Workstream Lifecycle

- Accept paths refuse patch-own dirty paths via git.DirtyPaths guards at both the accept site and the stale-base refresh site; the refusal is NOT wrapped as errBaseStale because that would trigger the auto-revise loop and burn ~8.5 min of verify per round on a refusal that needs human triage (main-epoch-28)
- ProbeAlreadyLanded runs before the dirty-path refusal so unstaged identical edits land via bookkeeping rather than being killed by the dirty check (main-epoch-28)
- Staged-only divergent edits are detected by git.IndexEditsBeyondHEAD (real index vs HEAD stage-0 compare) and refused before adjudication — previously git add silently clobbered a divergent index (main-epoch-32)
- alreadyLanded builds its post-image via a temp index and byte-compares against worktree hash-object, so stray edits are never swept into accept commits (main-epoch-30)
- retireRun retires the diff's own run by matching worktreePath against s.runs; the byConv binding is only a fallback — previously reviewing an older diff closed a newer run's worktree and could kill an in-flight auto-land verify (main-epoch-28)
- handleDeleteWorkstream checks daemon in-memory active state (run/distill/slash/panel/loop/autoPending) then journal-derived loop activity before the SQL delete; the residual sub-second check-to-delete race is accepted as user self-inflicted with the store pending-diff check as backstop (main-epoch-38)
- Run-start duplication is closed by an atomic registration guard (struct field + helpers) wired at every start site: handleSendMessage tail, slash routes, auto fire/arm, preview slash slot, distill entry, loop start (main-epoch-39)
- Large-file preview streams via os.Open + io.LimitReader instead of whole-file reads; a 4 GiB sparse-file pin proves the bounded read (main-epoch-28)
- Orphaned in-flight requests are closed at boot (first in the NewServer recovery sequence) with agent_error{cause:daemon_restart}; steer/park/loop control rows are skipped by field-based recognition, and the sweep is self-excluding/idempotent (bug-fix-epoch-4)
