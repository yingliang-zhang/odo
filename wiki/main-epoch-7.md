# Odo 会话摘要 — 2026-08-09

## 概览

围绕四条主线：**repo 改名的执行与回退 → "输出消失"诊断（epoch 折叠）→ /panel 4096 截断修复 → epoch-fold 根治方案评审**。核心结论：panel 三家收敛——根治方案为「schema 显式记折叠边界 + 流内常驻可展开 chip」，且终审确认这是架构级根治而非 patch（附条件）。

## 关键决策

| # | 决策 | 依据/证据 |
|---|---|---|
| 1 | 执行 GitHub rename `odo`→`odo-agent` 后**完整回退** | 用户此前已放弃改名；`gh repo rename` 双向 + `git revert --no-edit cb7bde4`（`753d553`），**never rewrite history**，推送至 `a39825a` |
| 2 | Tauri 标识符保持 `com.yingliangzhang.odo` 不变 | bundle ID 是 macOS 应用身份 ≠ 仓库名；改动重置权限与存储，零收益 |
| 3 | 本地目录 `~/Projects/odo` 不改名 | 7 个活跃 worktree + running daemon（PID 30215）引用该路径，纯装饰性改动 |
| 4 | 回退后给 wiki note 盖 **SUPERSEDED/ROLLED BACK** 章；`memory/log.md` append-only | 防止未来 recall 复活过期状态（`main-epoch-1.md` 已盖章） |
| 5 | `journal.sqlite*` 重置**推迟到 Odo 完全退出后手动执行** | live daemon 持有 SQLite WAL，运行中删除 = 损坏 |
| 6 | **`defaultMaxTok` 4096 → 16384**（`internal/moa/client.go`） | thinking 模型（kimi-k3、deepseek-v4-flash）的 reasoning trace 烧 output 预算；实测三模型输出 7325/8076/8550 tokens，4096 必然截断。已 accept 为 `1de583c`（**未 push**） |
| 7 | Go run-loop 测试补 `t.Setenv("HOME", t.TempDir())`，t.Run 场景**逐 subtest** | `readUserMemory()` 注入真实 `~/.odo/user.md` 导致 `TestVisibleLoopAcceptRejectRestore` 断言失败；审计出 15 个同类测试 |
| 8 | /panel 提问必须 **grounded**（把代码事实贴进 prompt） | panel 模型无工具/文件访问，首轮 glm-5.2 只回了通用框架 |
| 9 | epoch-fold UX：放弃 A/B/C 三选一，采纳 panel 升级版 | A=SKIP（仅文档化应急开关）；B=NOW（空态二分）；C=NOW 但降级为伴生——**核心是折叠点常驻 chip「已折叠 N 条→epoch-K｜展开｜打开 note」+ schema 显式记 `lastSeq/note_path`** |
| 10 | 终审判定：chip+schema 方案是**根治不是 patch** | 把"信任有损函数"重构为"有损函数+可证伪账本+可逆操作"；**分水岭判据：rehydrate 必须是模型侧一等工具，不能只在 UI 层**（kimi-k3 判据 3） |

## 代码变更

| 文件 | 内容 | 状态 |
|---|---|---|
| `internal/moa/client.go` | `defaultMaxTok` 4096→16384 + 注释说明 thinking 模型预算 | 已 accept → main `1de583c`，**未 push** |
| 15 个 ipc 测试文件 | 补 HOME 隔离（server/concurrent/streaming/m6 等 run-loop 测试） | 已落地（`7559f7d`/`ac8bed8` 一带） |
| `memory/log.md` | 改名 + 回退条目（append-only） | `80bd148`、`a39825a` 已 push |
| `wiki/main-epoch-1.md` | SUPERSEDED 横幅 + ROLLED BACK 标注 | 已写（wiki 目录，untracked） |
| 批量 sed 事故 | 首轮 sed 越界扫入 `.odo/worktrees/*`，损坏 7 个 worktree + brief `.md` 第 47 行；逆向替换恢复并逐目录验证 `git status` 干净 | 已修复；蒸馏为 skill `scoped-bulk-text-replacement` |

## Panel 对 auto-distill / auto-curate 的审查结论

- **Q2 触发器**：Hermes 75% 上下文占用阈值**不适用**（Odo 是 OMP one-shot，无连续上下文窗口）；保留 idle，但需加倒计时/可取消、composer 锁、启动补偿（run 结束时 app 已关则永久漏触发）。
- **Q3 auto-curate 链式触发**：**不合理**——成本翻倍/O(N²)、整层重写无 human gate、劣质 distill 被放大、失败无回滚 → 改条件触发（新 notes ≥N 或时间间隔）+ 质量门。
- **Q4 最小防线**：**provenance 回链**——epoch note 每条结论强制附 journal seq 区间 + 便宜机械校验；retract 只认"被推翻的"，认不了"从未发生的"。
- **Q5 schema**：NOW = distill event 显式记 `firstSeq/lastSeq/note_path/note_sha`（现状靠 UI 反推 `lastDistillSeq`，是隐式契约）；curate `notes_read` 带 SHA；epoch 命名不变。
- **推荐实施顺序（Q7）**：① schema 记 lastSeq（零 UI 地基）→ ② 折叠 chip + 空态二分（止血）→ ③ 边沿硬化 + 倒计时 → ④ toast 带 path（廉价伴生）→ ⑤ 进度 pill → ⑥ 回链校验 → ⑦ curate 条件化。

## 开放问题

1. **16384 修复未生效于 live daemon**——最近两次 /panel 中 deepseek-v4-flash 仍报 `4096 max_tokens` 截断：daemon 跑的仍是旧二进制。需 rebuild + 重启 daemon（注意不能在本会话运行中杀 daemon）。
2. **main 有未 push 提交** `1de583c`；主 checkout 还有 10 个未提交的 wiki curator 重构文件，待用户决定提交或丢弃。
3. **epoch-fold 根治方案未实施**：schema `lastSeq` 记录、ChatSurface 常驻折叠 chip、empty-state 二分——待开工。
4. **auto-curate 改条件触发**未定案未实施；distill provenance 回链校验未实施。
5. **`journal.sqlite*` 重置**仍待 Odo 完全退出后手动 `rm .odo/journal.sqlite*`（bootstrap 自动重建）。
6. **browser 实机对比 Hermes 未做**——本轮用的是粘贴已提取源码事实（`context_engine.py` 阈值、四层 memory、memory-boundary-design 原则）替代。
7. 遗留：`odo/main` 落后 main 6-7 个提交（accept 时 `AdvanceBranch` 会快进，刻意未动）；M7 GUI webview E2E（cua-driver）仍 outstanding。