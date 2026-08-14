# 三路盲审 Consolidation: 并行 Workstream 开发评估

> Tri-model MoA (K3/GLM/DSF, --thinking max, 540s, blind-sealed). Orchestrator pure-consolidator.

## A. Verdict (3/3 收敛)

**Needs-work — daemon 基座已就绪，GUI 是瓶颈。**

三路独立确认：Odo daemon 今天就能并行跑 4 个 workstream（cap 4、per-run worktree 隔离、W6 park-and-switch 已落地）。但 GUI 是单会话视角，无法同时监控 4 条流、跨 workstream 审查 diff、或从 GUI 发起 park。

| 路线 | Verdict | 一句话 |
|---|---|---|
| K3 | needs-work | base-staleness 级联 + 无 GUI park = 硬阻塞 |
| GLM | needs-work | GUI 是单 pane cockpit，diff 按会话隔离无法聚合 |
| DSF | needs-work | 差一层 GUI 间距：跨 workstream 审查 inbox + park dock |

## B. Daemon 就绪度 (3/3 收敛)

### 今天就能工作的

| 能力 | 证据 | 状态 |
|---|---|---|
| 4 并发 agent run | `maxConcurrentDefault=4` (server.go:1450), send 路径 cap 检查 (server.go:663) | ✅ |
| Per-run worktree 隔离 | `worktree.Manager.Create` (worktree.go:76), 每个 run 独立 detached worktree | ✅ |
| W6 park-and-switch | parked.go 全链路：journal FIFO cap 8, 3-site auto-dequeue, recoverParkedGoals | ✅ |
| Per-ws 状态上报 | `pending_counts` 返回 PendingCounts + RunningWorkstreams + ParkedGoals (server.go:3066) | ✅ |
| Diff 按 ID accept/reject | `handleDiffAction` 按 diffID 查找，不绑会话 (server.go:1735) | ✅ |

### 瓶颈（吞吐量限制，非正确性问题）

| 瓶颈 | 证据 | 影响 |
|---|---|---|
| **base-staleness 级联** (K3 独有发现) | `checkBaseFresh` 拒绝 HEAD 漂移后的 accept (server.go:1722)；第一个 land 使其他 3 个 diff 不可 land | **硬阻塞**：4 路并行的第一个 diff land 后，其余 3 个必须 rebase/重跑 |
| **autoLandMu 串行** (3/3) | server.go:122, autoland.go:197 | 4 个 pipeline 串行执行 verify+panel，最差 ~40-50 min |
| **acceptMu 串行** (3/3) | server.go:112, handleDiffAction:1748 | 设计如此（单 main checkout），非 bug |
| **curate daemon-wide** (3/3) | server.go:102, curator.go:493 | 第二个 curate 被拒；epoch 级别操作，低频 |
| **memory/wiki 共享** (3/3) | `.odo/memory.md`, `wiki/` 是 project 级 (server.go:761) | 跨 ws 交叉注入是设计的 D-cross；distill 并发写 wiki 需验证序列化 |
| **无跨 ws diff 列表 IPC** (GLM 独有) | `ListPendingDiffs(ctx, conversationID)` — 按会话查 (server.go:3872) | GUI 无法一次拉取全部 ws 的 pending diff 内容 |

## C. GUI 缺口 (3/3 收敛)

| 排序 | 缺口 | 收敛 | 证据 |
|---|---|---|---|
| **C1** | **无跨 workstream 审查队列** | 3/3 | `pendingDiffInfos(ctx, c.ID)` 只返回活跃会话的 diff (server.go:3872)；App.tsx diff 状态是单数的；切换 ws = bootstrap 全量替换 (App.tsx:374-401) |
| **C2** | **background-run chip 太简陋** | 3/3 | StatusBar "N background runs" 跳到第一个 (StatusBar.tsx:67-79)；无 per-ws 选择器、无 ETA、无完成通知 |
| **C3** | **无 GUI park 操作** | 3/3 | `Request.Park` 存在 (protocol.go:94) 但 GUI 无 park toggle；W6 ADR 明确 "GUI dock = future GUI wave" |
| **C4** | **无 per-ws streaming preview** | GLM+K3 | preview 只给活跃会话 (App.tsx:470)；background ws 只有一个紫点 |
| **C5** | **切换丢失上下文** | GLM+K3 | bootstrap 重置 panel 状态 (App.tsx:386-390)；scroll 位置、draft text 丢失 |
| **C6** | **Sidebar 未按注意力排序** | DSF+K3 | 按创建顺序排，非 Needs-input → Working → Idle |

## D. 前置条件排序 (2/3 收敛 K3+GLM，DSF 补充)

| 优先级 | 功能 | 填补缺口 | 成本 | 依赖 | 可并行 |
|---|---|---|---|---|---|
| **P0a** | **Stale-diff rebase/refresh 机制** | base-staleness 级联 (K3 独有 P0) | M | 无 | 与 P0b 并行 |
| **P0b** | **GUI QueueDock** — park/drop/resume + per-conv 队列视图 | C3 park 操作 + cap 耗尽可视化 | M GUI | daemon W6 已落地 | 与 P0a 并行 |
| **P1a** | **跨 ws pending-review inbox** — `ListAllPendingDiffs` IPC + GUI 聚合面板 | C1 审查 4 个 diff 不切换 | M | 无 | 是，GUI 独立 |
| **P1b** | **Background-run 选择器** — chip → multi-target dropdown + 完成闪光 | C2 监控 4 个 run | S | 无 | 是，GUI 独立 |
| **P1c** | **autoLand pipeline 并行化** — verify+panel 并行，仅 accept 串行 | autoLandMu 串行瓶颈 | S-M | P0a | 之后 |
| **P2** | **Sidebar 注意力排序** + task registry (GUI Wave A) | C6 + 长自治监控 | M | P1a schema | 是 |

**最小可行并行工作流** (2/3 K3+GLM)：P0a + P0b + P1a。三者让用户能 park goals、看跨 ws 审查队列、rebase stale diff。

## E. 风险评估 (3/3 收敛)

| 风险 | 机制 | 严重度 | 缓解 |
|---|---|---|---|
| **base-staleness 级联** | 第一个 land 使其余 diff base 过期 (server.go:1728) | **最高·确定** | P0a rebase 机制；按 RRR 顺序 accept；按文件不重叠拆 ws |
| **memory 交叉污染** | 共享 memory.md + D-cross 注入 (server.go:739) | **中** | pins verbatim、protected paths、accept memory proposals 时人工把关 |
| **wiki/epoch 碰撞** | distill 写 project 级 wiki/ (server.go:2581) | **低-中** | epoch note 文件名含 workstream 名，机械碰撞低；概念重叠可能 |
| **审查疲劳** | 4 diff 同时到达 | **中** | auto_apply=main 自动 land 干净 diff；human_gate_visual 强制人工审 GUI 改动 |
| **autoLand 串行** | 4 pipeline 排队 ~40-50 min | **高·吞吐** | P1c：verify+panel 并行，仅 accept 串行 |
| **cap 耗尽** | 4 槽满 + park goal → log-only 拒绝 | **低·设计如此** | P0b GUI 可视化队列深度；调 max_concurrent_runs=6-8 |

## F. 推荐分组 (3/3 收敛：3 workstream + 1 备用)

| WS | 任务链 | 类型 | 文件域 | 冲突面 |
|---|---|---|---|---|
| **WS-A "moa-chain"** | #2 (R-W1 moa resilience) → #7 (distill→moa) → #8 (learner/curator→moa) → #9 (Design-MoA consolidator) | daemon | `internal/moa/client.go`, `internal/ipc/{auto,distill,curator}.go` | moa/client.go 单一拥有者 |
| **WS-B "GUI wave"** | #4 (Guardian taxonomy GUI) → #10 (GUI Wave A) → #11 (GUI Wave B) | GUI | `gui/src/**` | **零冲突** — 完全隔离 |
| **WS-C "daemon-misc"** | #5 (visible⟺logged 残余检查) → #3 (receipts fill) | daemon | `internal/ipc/server.go` (send path), `internal/ipc/protocol.go` | 与 WS-A 的 server.go 编辑需错开 |
| **备用槽** | park dock GUI + 审查 inbox (P0b/P1a) 或 idle 吸收 rebase 冲突 | GUI/混合 | `gui/src/components/ChatSurface.tsx` | 与 WS-B 同域，时序错开 |

**文件冲突矩阵**：

| | WS-A (moa) | WS-B (GUI) | WS-C (ipc misc) |
|---|---|---|---|
| WS-A | — | 无 | `server.go` (#7/8 callsites vs #3/5)，低重叠 |
| WS-B | 无 | — | 无 |
| WS-C | 低 (server.go) | 无 | — |

**关键约束**：
- WS-B 可以 **t=0 立即开始**，零文件冲突（GUI vs daemon）
- WS-A 内部必须串行（#2→#7→#8→#9 是依赖链）
- WS-C 的 #5 是 5 分钟检查 → 先做，可能直接关闭
- base-staleness 级联 → **先 land 小 diff**（WS-C 的 S 级任务），再 land 大的
- 并行实操策略：每个 WS 先发 head task；完成后 RRR 顺序 accept（最小的先 land）；再 park 下一个 goal

## 结论

| 维度 | 状态 |
|---|---|
| Daemon 并行能力 | ✅ 今天就能跑 |
| GUI 并行体验 | ❌ 需要建 P0a/P0b/P1a |
| 最大阻塞 | base-staleness 级联（第一个 land 杀死其余 3 个） |
| 最小可行 | P0a (rebase) + P0b (park dock) + P1a (review inbox) |
| 推荐分组 | 3 WS + 1 备用，WS-B (GUI) 可立即零冲突并行 |
