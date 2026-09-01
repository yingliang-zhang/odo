Working...
# 盲审：Odo GUI UX 批次

所有证据均来自今日本人的第一手阅读；文件:行号见文末。

---

## Q1 — R1 修复：`margin-left:16px → auto`，而不是 `space-between`

**根本原因已确认。** `.app-topbar` (app.css:375-387) 是 `display:flex`，没有设置 justify-content，`padding:0 14px`。`.topbar-actions` (app.css:432-437) 设置了 `margin-left:16px` — 固定间距，因此动作集群在品牌字符串后向左偏移。已验证 TopBar.tsx:179-336 中的 JSX 顺序：`[sidebar-toggle, brand, ·, project, ·, workstream, actions]`。

**关于修复方案的结论：** 仅使用 `margin-left:auto`。`justify-content:space-between` 在这里是错的 — flex 容器有 7 个子元素，而不仅仅是 2 组；使用 space-between 会把 `brand/·/project/·/workstream` 互相分散拉开。Auto-margin 会将右侧的剩余空间折叠到一个间隙中，完全符合“集群固定在右边缘”的布局意图。

**补丁 (1 行)：**
```css
/* app.css:436 */
- margin-left: 16px;
+ margin-left: auto;   /* 行内注释: 将动作集群推至最右侧；容器间距保持 8px 最小间距 */
```

**后续影响检查（全部已核实，均安全）：**
- Pin popover: `.topbar-overflow` 设置了 `position:relative` (app.css:486-488)，`.topbar-pin-popover` 是绑定在 wrapper 上 `top:calc(100%+6px)` 的绝对定位 (app.css:508-511) — 它会随集群移动，无需重新布局。
- Radix `DropdownMenuContent align="end"` (TopBar.tsx:233) 渲染到 portal 中；跟随触发器。
- e2e: boot.spec.ts:47-60 仅断言 `.topbar-action` 的可见性/文本 — 没有几何断言。sidebar.spec.ts 仅对 `.app-topbar` 执行 `toContainText`。
- 窄宽度下的风险：auto-margin 吸收了所有剩余空间；`gap:8px` (app.css:378) 仍保证最小 8px 间距，且 `.topbar-project`/`.topbar-workstream` 在 200px 处有省略号截断 (app.css:415-429)。无重叠风险。

## Q2 — Tasks 标签页：选项 (a)，注册表条目如下

**读取路径：**(a) 镜像 `PlanChip` 的路径。`todo.ts:1-8` 是设计契约：journal 是唯一的真相来源，`deriveTodoState` 与守护进程的 `TodoStateFromEvents` (internal/ipc/todo.go) 一致，读取时零新增 IPC。新的 IPC 会制造守护进程已经修复为 SSOT 的第二个真相来源 — 这违反了锁机制。漂移不可能因为两个界面调用**同一个导出函数**而出现。

**注册表条目 (contrib.ts):**
```ts
import { ListTodo } from "lucide-react";   // 添加到导入块 (contrib.ts:23-33)
// 在 CONTRIBUTIONS 中，位置 0:
{ id: "tasks", title: "Tasks", icon: ListTodo,
  badge: (i: PanelBadgeInput) => positive(i.openTodos) },
```
扩展 `PanelBadgeInput` (contrib.ts:38-47) 增加 `openTodos: number` — 这是 App 已经为 `PlanChip` 计算的 open-and-not-swept 计数；将其传入 `ContextPanel` 的 badge 输入与 `pendingDiffs` 的线程处理方式完全一致。App 中不需要新的推导。

**主体：** 通过 App 的 keep-alive/LRU 挂载块 (contrib.ts:10-17 的衔接点) 挂载 `TasksPanel.tsx`。渲染 `visibleTodoItems(deriveTodoState(events))` 并重用 `PlanChip` 的行 UI；写入操作重用现有的 `todo_update` IPC, `origin:"user"` (PlanChip.tsx:64-86) — 无新增内容。

**位置：** 索引 0，在 Changes 之前 — 用户说“优先显示”，且注册表顺序即是条状顺序。保持 Changes 作为第 2 个并保留其 badge（badge 机制免费携带了信息）；不要删除它 — diff 界面服务的是 `DiffViewer` 流水线，不仅仅服务于该用户。

**第 10 个标签页的风险：** context-panel-tabs.spec.ts:100-114 是**针对恰好 9 个标签 (~616px) 与 720px 面板 (659px 客户端) 校准的**。第 10 个标签 (~+68px) 破坏了“每个标签都适配 / 滚动控件数量为 0”的断言。必须重新校准该 spec (更宽的视口或更宽的面板宽度) — 这与 W3 的债务是同一个 bug 类型。此外，localStorage `odo-panel-tab` 的白名单自动派生自 `PANEL_TAB_IDS` (contrib.ts:107-109)，因此新 ID 在那里是安全的；lru-park.spec.ts 的标签定位器使用名称前缀正则表达式 — "Tasks" 是唯一的，没有冲突。

## Q3 — 轮询足够；不要构建推送通道

App.tsx:79-81: `POLL_INTERVAL_RUNNING_MS = 350`, `POLL_INTERVAL_IDLE_MS = 1500`, 自调度 setTimeout，带有抖动退避 (:1081-1100)。`todo_merge` 快照在每次 tick 时追加到 `events` 中；`PlanChip` 在轮询到达时会重新渲染。

任务进度仅在 turn 运行**时**发生变化 — 恰好是在 350ms 的节奏下。1.5s 的空闲节奏仅在没有发生任何变化时才生效。这已经是实时了。SSE/websocket 将是针对 350ms 轮询在 4 倍内收敛的数据进行新基础设施的投入，外加守护进程端的推送通道，而这目前并不存在。**结论：保留轮询。在守护进程有推送通道的独立需求之前，不要修改。**

## Q4 — 后台任务：MVP = 扩展现有的弹出框；暂不新增标签页，暂不新增事件类型

范围，基于证据：
- **(a) 其他工作流中的智能体运行 — 已交付。** `bgNotice` 闪烁 + 4s 超时 (App.tsx:711-728)，带有 `bg-run-row` 行的 bgruns 芯片弹出框 + 开始闪烁着色 (StatusBar.tsx:1140-1155, :942-952)，以及 RunsPanel 的日志折叠历史记录 (runs.ts:1-13, 状态 running/ok/error + 时长)。
- **(b) 守护进程执行的命令 — 目前不存在。** internal/adapter 仅运行 omp 进程；没有通用的执行日志记录。诚实的 MVP 需要一个新的事件类型对 (`exec_started`/`exec_finished`) 以及守护进程中目前不存在的生命周期连接。
- **(c) Hermes 端 OMP 包装进程 — 超出守护进程的权限范围。** 守护进程无法看到它们；仅凭 GUI 也无法可靠地看到它们。不要承诺。

**MVP 建议：** 发布 (a) 的润色 — 在 `StatusBar` bgruns 弹出框中，使用已日志记录的数据添加每行的运行时长和最后一个事件行（`events` prop 已在 StatusBar.tsx:789-790 中）。这涵盖了 R3 的“显示我后台运行的东西”，且零新增基础设施。仅当用户确认他们真正的需求是任意 shell 命令 (b) 时，再安排 — 那是一个真正的守护进程功能。不要为 (a) 开设一个新标签页；那会重复 RunsPanel。注意 Q5 的 K8s 标签页可以说能更好地覆盖用户实际的工作流（“我提交的作业”），这进一步降低了 (b) 的优先级。

## Q5 — K8s：新标签页 + 按需 IPC 查询，日志记录延期

已确认没有现有的 k8s 代码 (GT5)。分阶段进行：

**阶段 MVP-1 (M)：一次性查询，直接 IPC，新标签页。**
- **数据路径：直接 IPC 查询，而不是日志事件。** K8s 状态是外部且高变更的；将 30s 轮询记入日志会淹没作为对话 SSOT 的事件流。代码库中已有此准则的先例：OMP 使用情况是“只读（从不记入日志）”，在弹出框打开时惰性轮询 (StatusBar.tsx:598-602, :656-665, `OMP_POLL_INTERVAL = 60_000`)。完全采用该模式。
- **守护进程：** 新的 IPC 处理程序 `k8s_status`。守护进程使用 `exec.Command` argv 风格（无 shell）生成 `kubectl get pods,jobs -n lab -o json`。安全性：命名空间在服务器端固定，动词白名单为只读（get/describe/logs），**绝不透传用户提供的参数**，kubeconfig/context 固定在守护进程环境中，失败时返回软错误 `{"available:false, reason}`。
- **GUI：** 注册表条目 `{ id: "k8s", title: "K8s", icon: Container }` (lucide `Container`) + App 挂载，第 11 个标签页。仅当 K8s 标签页处于活动状态时，每 30-60s 轮询一次。表格：名称/状态/开始时间/时长/重启次数，作业 → pods 扩展。日志查看 (`kubectl logs --tail`) 作为行操作，在 Phase 1.5 阶段，同样使用白名单。
- **MVP 范围：** `-n lab` 按标签选择器过滤（用户的作业 — 例如提交时已知的所有者/名称前缀标签，符合预先存在的 K8s 作业清单/预检约定），并带有一个“显示所有 lab pods”的切换开关。默认 = 我的作业。

**阶段 2 (L)：近实时生命周期。** 作业 succeeded/failed 的通知需要一个守护进程端的观察者 (`kubectl get jobs -w` 或轮询差异) 加上推送/通知渠道，以及一个新的事件类型 (`k8s_event`) 和一个 StatusBar 芯片。这是一个真正的守护进程功能 — 在 MVP-1 验证了用户确实会打开该标签页后，再安排它。不要在 Phase 1 中构建 `-w` 流：目前不存在长连接基础设施 (Q3 的结论)，且带有惰性刷新的 30s 轮询对于数小时 GPU 作业的监控来说已经足够了。

## Q6 — 顺序

| # | 项 | 规模 | 依赖 |
|---|---|---|---|
| 1 | R1 CSS 修复 | S | 无。发布。 |
| 2 | Tasks 标签页 (Q2) | M | 无。 |
| 3 | K8s MVP-1 (Q5) | M-L | **受益于 2 的优先级排序**：相同的衔接点 (注册表条目 + keep-alive 挂载 + 惰性轮询模式)，并且第 10 个标签页的 e2e 重校准只需做一次 — 执行 2 可以低成本验证它，然后 3 复用该经验。 |
| 4 | StatusBar bgruns 润色 (Q4) | S | 无；可随时插入。 |
| 5 | K8s watch/通知 (Q5 Phase 2) | L | 需要推送通道决策 — 首先与守护进程路线图协调。 |
| 6 | 守护进程执行事件 (Q4b) | M | 首先需要用户确认；在 5 之前没有任何阻塞。 |

1/2/4 是相互独立的 — 可以并行运行。3 应该排在 2 之后，尽管它在数据路径上是独立的（直接 IPC，无日志）。

---

## 结论：**接受但需修正**

方向是合理的；四个修正点：
1. Q1：`margin-left:auto`，明确**不**使用 `justify-content:space-between`。
2. Q2/Q5：各领域的数据路径准则 — Tasks 从日志中读取（现有的 `deriveTodoState`，零 IPC）；K8s 是直接 IPC，**从不记入日志**。混合这些是这里最常见的失败模式。
3. 第 10 个标签页的溢出：`context-panel-tabs.spec.ts:100-114` 在合并任何新标签页之前必须重新校准 — 这是已知的 W3 债务类型，现在处理很廉价，但以后处理会很痛苦。
4. Q4：发布 (a) 的润色；不要在用户确认之前构建 (b)，不要尝试 (c)。

**P0**
- 无。(R1 修复本身是一个安全的一行代码。)

**P1**
- 第 10 个标签页条状溢出：在 Tasks 标签页发布之前必须重新校准 `context-panel-tabs.spec.ts` (相同的 bug 类型为 W3 的 616-vs-579px)。
- K8s 安全姿态必须从一开始就保持只读 + argv 风格 + 固定的命名空间/上下文 — `kubectl` 是一个强大的二进制文件，守护进程生成它扩大了影响范围。

**P2**
- 不要为 K8s MVP 添加 SSE/websockets 或任何推送基础设施 (App.tsx:80 处的 350ms 轮询使得 Q3 没有必要；30s 的惰性轮询使得 K8s MVP 没有必要)。
- 不要在 MVP 中为 k8s 轮询添加日志事件；如果 Phase 2 需要通知，稍后再重新审视。

**最重要的一点：** 保持单个真相来源准则的精准 — journal 是智能体状态的 SSOT (Tasks 从中读取，不添加任何内容)，而外部世界状态 (K8s) 绝不进入 journal。每一个“双源漂移” bug 和每一个被淹没的事件流都源于打破这两半中的一半。
