# Daemon Resilience & Crash Recovery

- modernc.org/sqlite WAL recovery mmap SIGBUS crashes recur (multiple daemon.log signatures); recommended first remediation is _pragma mmap_size=0 in the connection string, upgrading the dependency only if that fails (UI-epoch-10) (bug-fix-epoch-4)
- SIGQUIT immunity chosen over graceful-TERM-on-QUIT: srv.Wait would hang shutdown on in-flight panels up to ~24min losing advisories anyway; signal.Notify logs and discards each SIGQUIT, SIGABRT retained for deliberate dumps (bug-fix-epoch-4)
- Orphan sweep placed first in NewServer recovery: every unanswered non-control user_message (steer/park//loop control rows excluded field-wise) gets agent_error{cause:daemon_restart} so the GUI renders a failure bubble instead of hanging; recognition is pure field-based and self-excluding on next fold, hence idempotent (bug-fix-epoch-4)
- recover-pending-diffs dedup is self-evidencing in the boot log ("N/N pending diffs already adjudicated — skipping their re-fire") (UI-epoch-10)
- "Resend" chat bubbles are orphan markers stamped on in-flight requests when the daemon dies; they persist in history and do not indicate current failure (UI-epoch-10)
- recoverOpenSteers closes historically dangling queued steers at restart by journaling steer_dropped{cause:daemon_restart}, eliminating undeletable GUI ghost rows (UI-epoch-6)
- TERM exit logging "remove socket: no such file" is a pre-existing harmless dup between Go unix-listener auto-unlink and explicit os.Remove, deliberately left untouched (bug-fix-epoch-4)
- Store-hardening pack: CommitFold does epoch bump + marker in one transaction; SearchEvents gained LIKE ESCAPE and id-DESC ordering (created_at ties); markers LIKE queries use colon-space dual-form OR; read paths narrow to post-fold windows with no-pin markers falling back to full history to keep legacy sweep rescue working (main-epoch-23)
