# 会话总结 — Odo 状态盘点 → 改名回退 → Epoch 折叠诊断

## 关键决策

| # | 决策 | 依据/结果 |
|---|---|---|
| 1 | **无需 merge**:`odo/main` 完全包含于 `main`(0 领先/当时 5 落后） | merge-base = `odo/main` tip;main 多出的为 IME/PATH/`SUDO_CODING_KEY` 修复 |
| 2 | **执行 GitHub rename `odo` → `odo-agent`**（后被回退） | 对照 `rename-install-cleanup-brief.md` Op1；用户事后声明早已放弃此改名 |
| 3 | **Tauri identifier 保持 `com.yingliangzhang.odo`** | bundle ID 是 macOS 应用身份，改动会重置权限与存储，零收益 |
| 4 | **本地目录 `~/Projects/odo` 不改名** | 7 个活跃 worktree + 运行中 daemon 引用该路径 |
| 5 | **历史文档/日志一律不改**(append-only) | 只更新功能性引用 |
| 6 | **journal.sqlite 重置推迟到 Odo 完全退出后** | 活跃 daemon(PID 30215）持有 SQLite WAL，强删即损坏 |
| 7 | **回退用 `git revert --no-edit`，不改写历史** | `cb7bde4` → revert `753d553`;`80bd148` 作为历史保留 |
| 8 | **回退后在 wiki 笔记顶部标 SUPERSEDED / ROLLED BACK** | 防止未来召回复活过期结论（`wiki/main-epoch-1.md`) |
| 9 | **"没看到输出" = epoch 折叠，非数据丢失** | auto-distill 完成后 `ChatSurface` 只显示 `seq > lastDistillSeq`；输出完整在 journal |

## 代码/仓库变更

**主线提交（均已推送）:**

| Commit | 内容 | 状态 |
|---|---|---|
| `cb7bde4` | Go module path `odo` → `odo-agent`(go.mod + 15 Go 文件，共 26 行） | ❌ 已被 revert |
| `80bd148` | memory/log.md 记录 rename + Op3 清理 | ✅ 保留（append-only) |
| `753d553` | `git revert cb7bde4`，模块路径恢复 `github.com/yingliang-zhang/odo` | ✅ |
| `a39825a` | log.md 追加回退条目 | ✅ |

**GitHub 侧：** repo `odo` → `odo-agent` → 回滚回 `odo`（两次 `gh repo rename`，旧 URL 均自动重定向）;origin URL 同步两次。

**Op3 清理（非提交）:** 删 6 个过期 session+prompt 对（保留活跃会话）、`gui/dist`、`gui/test-results`；截断 `daemon.log`;wiki/ 与 ledger.md 本已不存在。

**门禁：** rename 与 revert 后各跑一轮 `go build` + `go vet` + `go test ./...`，全绿（ipc 套件 ~123s)。

## 事故与修复

1. **bulk sed 越界**（自造成，已修复）：首轮替换 travers 了 `.odo/worktrees/*`，改写全部 7 个 worktree 副本；逆向替换又损坏每个 worktree 中 brief `.md` 第 47 行。全部恢复，逐目录验证 `git status` 干净 → 已蒸馏为 skill(scoped replacement / 用 `git ls-files` 圈定范围）。
2. **早前异常**（日午间，未追查）:`agent_error` exit 127(`omp: command not found`)；一次 `agent_text` 输出为原始 session JSON;401 认证失败。

## 诊断结论（Epoch 折叠）

- 触发链：run 结束（18:23:46)→ +60s auto-distill(18:24:46)→ 完成（18:25:32)→ `review_action(distill)` seq 278 → GUI 隐藏 ≤278 全部事件（含回退问答 seq 179–275)。
- 配置证实：`~/.odo/prefs.md` = `auto_distill: on_idle` / `60s` / `auto_curate_after_distill: true`，与两次折叠时间戳（18:19:49、18:25:32）精确吻合。
- 机制代码：`ChatSurface.tsx:456-466`;auto-distill arm:`App.tsx:906-947`。
- 当天折叠两次：第一次用户 4 秒后发新消息未察觉；第二次离场发生 → 本次疑问。

## 开放问题

| 项 | 状态 | 备注 |
|---|---|---|
| Epoch 折叠 UX | ⏳ 等用户拍板 | 已给选项：A. `auto_distill: never`;B. 空态文案区分"全新 vs 已折叠到 wiki（可点击）";C. distill toast 注明已折叠 |
| `journal.sqlite` 重置 | ⏳ 需手动 | 退出 Odo 后 `rm .odo/journal.sqlite*`,bootstrap 自动重建 |
| Op2 /Applications 安装 | 先前推迟 | 但 18:01 出现新构建的 `/Applications/Odo.app` 并在运行 `[INFERENCE: 用户自行完成]` |
| `odo/main` 落后 `main` 7 个提交 | 未处理 | 下次 accept 时 `AdvanceBranch` 自动快进 |
| M7 GUI webview E2E(cua-driver) | 仍 outstanding | log 遗留项 |
| `steering.txt` 死代码 | 未清理 | A2 brief RC8 注明，Adapter 接口未动 |
| Hermes 对照评审（GUI / auto distill / auto curate / schema) | ⚠️ 不完整 | `/panel`:2/3 模型（kimi-k3、deepseek-v4-flash）在 4096 max_tokens 截断；glm-5.2 未访问到 Hermes 本地文件，只给了通用框架 —— 需要贴文件/截图后做 grounded 复审 |