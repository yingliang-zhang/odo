# Repo Rename and Rollback

- GitHub rename odo→odo-agent executed as Op1 of rename-install-cleanup-brief.md (go.mod + 15 Go files rewritten, 26 lines, commit cb7bde4), then fully rolled back via `gh repo rename odo` + `git revert --no-edit cb7bde4` → 753d553; never rewrite pushed history (epoch-4)
- Rollback log entries kept append-only: 80bd148 (rename+cleanup log) and a39825a (rollback log) both pushed; final module path is github.com/yingliang-zhang/odo (epoch-3)
- Tauri bundle identifier kept com.yingliangzhang.odo across rename — bundle ID is macOS app identity, changing it resets permissions and Tauri stores for zero value (epoch-2)
- Local directory ~/Projects/odo deliberately not renamed — 7 live worktrees plus running daemon (PID 30215) reference the path; purely cosmetic (epoch-2)
- Historical docs/logs never rewritten; rolled-back decisions stamped SUPERSEDED/ROLLED BACK on wiki/main-epoch-1.md to prevent future recall from resurrecting stale state (epoch-4)
- Gates (go build/vet/test, ipc suite ~123s) re-run green after both the rename and the revert (epoch-4)
