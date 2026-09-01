Working...
评审完成。以下是独立评审意见。

---

## Q1 — 哪些标签页移出，移到哪里

| 标签页 | 评审结论 | 证据 |
|---|---|---|
| **ledger** | **移出 → TopBar-overflow 启动的模态对话框** | LedgerPanel 是一个只读的守护进程写入文件视图，onClick 为 0，事件约为 0，且无徽章（contrib.ts:85 缺少徽章函数）。它已经有一个通过 `onOpenLedger`（TopBar.tsx:268-276）实现的第二个入口点，目前仅仅是将面板切换到该标签页——纯粹的成本，没有额外的交互功能。240px 的侧边栏对于指标表来说太拥挤了；设置（Settings）适用于配置，不适用于观察。模态对话框使文件视图保持全宽，且无需常驻的条带成本。 |
| **learning** | **保留** — 试用期内不可移动 | onClick 为 0，但它是自我提升支柱（D9-W3，contrib.ts:91-93）的唯一表面；LEA-1/SKL-1 目前处于阴影试用状态。移除它会中断人类观察回路。试用结束后重新评估。 |
| **skills** | **保留** | onClick 为 11 — 条带中交互性最高的主体。无需讨论。 |
| **preview** | **保留** | onClick 为 2，但它是推送式暂存区（代理推送预览事件）；P2.1 将其定为一等表面。它是工具包，而非菜单。 |
| **runs** | **保留**（不要合并到 jobs 中） | 255 个对话级的历史折叠与 UX-2 的实时作业是不同的数据；StatusBar 的 `bgruns` 弹出窗口（statusbar.test / OVERFLOW_RANK bgruns，StatusBar.tsx:171-182）仅涵盖*运行中*的跨工作流运行——没有历史记录。合并会强制对 A2-5 的 2 个部分进行修订。 |
| **wiki** | **保留标签页，丢弃冗余的 TopBar 溢出项** | WikiBrowser 具有搜索 + 6 个 onClick；TopBar.tsx:256-267 的 "Wiki" 项与该标签页重复。在对话框中全宽渲染浏览器效果较差。每个表面保留一个入口点。 |
| tasks/changes/review/memory | 保留 | Todos 通过 TodoList 处理；changes/review 是 IPC 动词表面（DiffViewer/ReviewInbox）；memory 有 7 个 onClick + 提案 UI + 徽章（contrib.ts:83）。 |

## Q2 — 左侧边栏底部作为容器？

**弱容器 — 拒绝对任何当前标签页使用。** 布局事实：侧边栏是 `flex-col`（Sidebar.tsx:688-694）；活动项目的工作流列表是 `flex-1 overflow-y-auto`（Sidebar.tsx:633），而“+ 新建工作流”是该列表的*最后一行*（Sidebar.tsx:663-680）——并不是一个全局底部栏。可以使用包装项目容器的 `<section>` 来创建底部区域，但存在两个硬约束：

1. **240px 密度：** 仅能容纳键:值行或紧凑的单行列。对于 Ledger 的指标表或 Learning 的情节/候选折叠来说太窄了——两者都会退化为滚动时展示不了的摘要。
2. **折叠轨道 = 48px：** 任何底部区域的内容都必须降级为图标（侧边栏已有针对折叠状态的侧轨处理，Sidebar.tsx:703）。这会使一个完整的表面变成一个带有图标的按钮——此时它就成了一个启动器，即溢出菜单/对话框，而不是一个容器。

诚实的结论：左侧边栏底部适用于*环境型、项目级状态*（守护进程健康，Ledger 单行摘要 → 点击打开模态框）。它不能容纳任何实际的面板主体。

## Q3 — 设置对话框分类法

**设置仅用于配置。** 其 3 个类别（常规/模型/知识）都是配置形式的；将观察结果埋入其中会将其置于无人查看的位置，并将其渲染与配置保存生命周期耦合。诚实分类法：

- **配置** → 设置
- **高频交互** → 面板标签页
- **低频查看 / 深度探讨** → 模态对话框（从 TopBar 溢出菜单或命令面板 CommandPalette 启动）
- **环境状态** → StatusBar 标签页 / 侧边栏圆点

Ledger 和 Learning 都是观察结果 → 都不是设置材料。Ledger 符合模态框定义；Learning 属于高频（试用观察），因此属于面板。

## Q4 — 带有 jobs 的最终条带；条带计算

宽度检查（11px 字体 ≈ 5.5px/字符，图标 12px，px-[5px]×2，gap-px；ContextPanel.tsx:262-264）：tasks≈53, changes≈64, review≈58, wiki≈47, memory≈61, skills≈57, ledger≈57, runs≈50, preview≈64, learning≈68, jobs≈49 → 10 个标签页 ≈ 578px；11 个 ≈ 627px。720px 最大值下的客户端：720 − 16 (px-2 头部内边距) = 704px，仅在溢出时显示箭头 (ContextPanel.tsx:233-247)。**11 个标签页可以放入 720px 最大值但无法放入默认值** — 默认值 420 → 客户端 ≈404，所以无论哪种情况，箭头都是 420px 下的常态（Chromium 发现 8 的测量结果：6 个标签页 = 419–457px）。减少一个标签页可以将最大宽度下的箭头完全去除，并将默认值下的滚动距离缩短约 60px。

**目标条带（顺序）：**
```
tasks · changes · review · jobs · preview · memory · skills · wiki · runs · learning   (10)
```
操作优先（tasks/changes/review/jobs），然后是暂存区（preview），知识（memory/skills/wiki），历史/观察（runs/learning）。

## Q5 — Ledger 迁移的迁移清单

1. **contrib.ts:85** — 移除条目；`PanelTab` 联合类型收缩 → 所有 `ledger` 引用在编译时报错（按设计：contrib.ts:113-115）。
2. **App.tsx** — 移除 LedgerPanel 的挂载子项 + `onOpenLedger` 重新连接：将 TopBar 溢出处理程序从“打开面板标签页”更改为“打开对话框”。
3. **localStorage `odo-panel-tab`** — **良性**：App.tsx:183-189 回退到 `"tasks"`，当存储的 ID 不在 `PANEL_TAB_IDS` 中时；无需迁移。Keep-alive LRU park 处理卸载（ContextPanel.tsx:51-54）。
4. **测试：** contrib.test.tsx:23（精确的 10-ID 列表断言），contextpanel.test.tsx，app_failure_focus.test.tsx:226，app_keepalive.test.tsx:77（不受影响 — 使用了 `wiki`/`runs`），e2e/ledger.spec.ts（重写以适配新界面），e2e/boot.spec.ts（TopBar 溢出包含一个 Ledger 项），e2e/lru-park.spec.ts，e2e/context-panel-tabs.spec.ts。
5. 在开始之前 `grep -rn "ledger" gui/src gui/e2e` — TopBar 属性、字符串、任何状态栏/快捷键引用。

## Q6 — P0/P1/P2

- **P0：** 在 LEA-1/SKL-1 试用结束前，不要移除 Learning — 这是本次提议中唯一的不可逆损害。将此减少操作与 UX-2 jobs 的落地合并（一个变更，条带 11→10）。
- **P1：** Ledger 从“标签页”降级为“从 TopBar 溢出菜单启动的模态框”，并同步进行 e2e 重写；不要只是隐藏它。
- **P2：** 通过删除 TopBar 溢出的 "Wiki" 项（而不是标签页）来去重入口点；将左侧边栏底部区域推迟到未来用于环境摘要，它无法容纳任何当前标签页。

---

## 结论：**ACCEPT_WITH_CHANGES**

**最终条带：** `tasks · changes · review · jobs · preview · memory · skills · wiki · runs · learning` (10；ledger 通过 TopBar 溢出菜单移至模态框；jobs 根据 A2 加入)。

**最重要的一点：** 这次减少操作的实际收获是 Ledger — 一个零交互、只读的视图，它重复了现有的溢出入口点。其他所有内容都以交互性、试用可见性或明确的用户使用为由保留；中途移除 Learning 是此提案中唯一可能错误的决定。
