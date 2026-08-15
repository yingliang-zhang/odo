# Self-Improving Agent: First-Principles Design (Tri-Model Consolidation)

> Tri-model MoA analysis (K3/GLM/DSF, --thinking max, 900s, blind-sealed). 3/3 converged on core.

## A. First Principles: What IS Self-Improving? (3/3 convergence)

**定义**：Self-improving = 一个闭环控制过程 `measure → identify → propose → review → land → re-measure`，其中 agent 自己的执行轨迹改变它未来的行为。关键属性是**闭环**，不是模态。

**3/3 一致**的分解：

| 轴 | 含义 | Odo 现状 | 缺什么 |
|---|---|---|---|
| (a) 任务能力 | 知识/技能积累 | ✅ 部分 — learner 提议 memory rules，curator 组织 wiki | 无效果追踪：rule 被接受后永远注入，不检查是否有效 |
| (b) 审查能力 | panel 准确度提升 | ❌ 零 — MoA panel 是固定陪审团，无 verdict-vs-outcome 校准 | 完全缺失 |
| (c) 编排能力 | workflow 优化 | ⚠ 部分 — autonomy ladder 有 streak 测量但只升不降 | 无 ground truth 可比（调度没有反事实） |
| (d) 自我修改 | 改自己的代码 | ✅ 已有 — auto-land pipeline | 成功率不反馈 |

**3/3 关键判断**：
- **(d) 不应作为 improvement 机制本身** — 它是最危险的子集（GLM: "self-improving ≠ self-modifying"）
- **(c) MVP 不应尝试** — 无 ground truth，无反事实（DSF）
- **核心缺口不是能力，是闭环** — 每个碎片都在 measure 或 propose，但没有 re-measure（K3）

## B. Memory Connection (3/3 convergence)

**3/3 一致**：Self-improving 不是新 memory 层，而是**在现有层之上的新机制**。

```
                    ┌─────────────────────────────────┐
                    │     journal (唯一真相源)          │
                    │  review_action, memory_propose,  │
                    │  memory_update, distill, ...     │
                    └──────────┬──────────────────────┘
                               │
                    ┌──────────▼──────────┐
                    │   Measure (新)       │ ← deterministic, LLM-free (inv 4)
                    │   join receipt ×     │
                    │   outcome = 效果数据  │
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │   ledger.md (现有)    │ ← daemon-only writer (inv 4)
                    │   效果指标行          │
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │   Identify (新)      │ ← deterministic flagging
                    │   rule X reject-rate  │
                    │   ≥ 2× baseline       │
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │   Propose (现有+扩展) │ ← learner at distill (inv 7)
                    │   flagged rules →     │
                    │   learnerPrompt as    │
                    │   DATA (not instruction)│
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │   Review (现有)       │ ← human gate (memory_propose)
                    │   或 MoA panel        │   或 unanimous panel (code diff)
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │   Land (现有)         │ ← apply_memory / auto-land
                    └──────────┬──────────┘
                               │
                    ┌──────────▼──────────┐
                    │   Re-measure (新)     │ ← next epoch's audit
                    │   before/after delta  │
                    │   oscillation guard   │
                    └─────────────────────┘
```

**Memory 联动方式（3/3）**：
- **读**：audit 从 journal 的 injection receipt + review_action 事件 join 出效果数据
- **写**：通过现有 `memory_propose` → human `apply_memory` 路径，不创建新写路径
- **存储**：效果指标进 `ledger.md`（daemon-only, pull-based, inv 4）；flagged rules 进 journal `review_action{action:"memory_audit_flag"}`
- **不违反 ADR-0003**：agents 仍然不写 memory；daemon 是唯一 writer；distill 仍是唯一 cadence

## C. Harness Lessons (3/3 convergence)

| Harness | 教训 |
|---|---|
| **dsh** | 无 cross-session memory — 如果加了 self-improving，会是 "event-sourced log → pattern detection → behavioral change"。Odo 已有更好的 memory stack。 |
| **grok** | 实验性 FTS5+vec0 检索（默认关闭）+ doom-loop 检测。Doom-loop detection 的 epoch-scope 变体 = oscillation guard（规则在 N 个 epoch 内反复 toggle ≥2 次就冻结）。 |
| **codex** | Guardian 单法官审查 = self-improving 的最大风险。**绝不能让 panel 调优自己的审查标准** — judge 必须在 judged 之外。 |
| **三者都不 self-improve** | 因为它们是 vendor 产品，人类改进它们。Odo 是 self-developing 的 — self-improving 是自然延伸，但需要 bounds。 |

## D. Failure Modes & Mitigations (3/3 convergence)

| 失败模式 | 风险 | Odo 架构如何缓解 |
|---|---|---|
| **退化反馈环** | panel 收敛到永远 accept | judge 不在 judged 内；panel 标准不在 loop 的 target space（K3/GLM/DSF 3/3） |
| **能力退化** | "改进"让 agent 在某方面变差但指标不捕获 | before/after delta 测量 + oscillation guard |
| **Memory 腐败** | 坏规则写入 memory，steer 所有未来 run | human gate on every rule write (memory_propose → apply_memory)；auto-land 自己的 outcome 不 grade loop |
| **失控自修改** | agent 改自己的审查标准 | **Protected-path 扩展（GLM 独有发现，3/3 采纳）**：将 `autoland.go`, `autonomy.go`, `learner.go`, `review.go`, `settle.go` 等安全机制源文件加入 C0 never-auto-land 集合。今天只有 `.odo/` 和 `wiki/` 被 protected — gate 代码不在其中，一个小的 in-scope 编辑可以弱化 gate 然后 auto-land。 |
| **过拟合当前项目** | agent 为自己优化但不泛化 | 跨项目 promotion gate（≥2 projects 才进 user.md）；单项目信号只 retract 该项目的 memory.md |

## E. MVP Design: Outcome-Linked Rule Retraction (3/3 convergence)

**3/3 一致**：MVP = **关闭现有的开放循环** — 不是加新能力，而是让已有的 measure/propose 碎片形成闭环。

### 核心循环：Rule Effectiveness Audit

1. **Measure**（确定性，LLM-free，新 CLI `odo rules audit`）
   - 从 journal 的 injection receipt × human outcome join 出效果数据
   - 复用 `cmd_skills_audit.go` 的 attribution 模型（send → terminal → diff → outcome）
   - 排除 `auto_panel` actor（只用 human verdict 做 ground truth）
   - Block-level attribution 先行（receipt 是 per-path block hash）；per-rule attribution 后续（需 replay `memory_apply` 事件）

2. **Identify**（确定性 flagging，无 action）
   - 规则被 flag "harmful" 当 ALL 满足：injections ≥10, human rejects ≥3, rejects 跨 ≥3 conversations, reject-rate ≥ 2× baseline
   - 规则被 flag "effective" 当 accept-rate ≥ 2× baseline
   - Flag 结果写 `ledger.md`（daemon-only, inv 4）+ journal `review_action{action:"memory_audit_flag"}`

3. **Propose**（learner at distill boundary, inv 7）
   - Flagged rules 作为 DATA（不是 instruction）注入 learnerPrompt
   - Learner 可以提议 retract/demote flagged rule（通过现有 `contradicts` 字段）
   - Daemon vetting 要求 flag row 存在才能 propose retraction

4. **Review**（human gate, 现有路径）
   - Retraction proposal 通过现有 `memory_propose` → human `apply_memory`
   - **无 auto-retraction** — flag 只提议，human 决定
   - 涉及代码修改的 → 走现有 auto-land pipeline + MoA panel

5. **Land**（现有路径）
   - `apply_memory` → `memory.md` 移除 → `memory-archive.md`（append-only, inv 3）
   - `memory_update` journaled with `cause:retracted`

6. **Re-measure**（next epoch）
   - Rule 不再注入 → receipt 变化 → baseline shift
   - Before/after delta = 改进效果
   - **Oscillation guard**（grok doom-loop 原理）：规则在 N epoch 内 toggle ≥2 次就冻结 + surface to user

### Bounds（3/3）

- Per-epoch proposal cap（现有 `procedureMaxCount=3` 先例）
- Human gate on every rule write
- Panel 审查标准不在 target space（不能调优自己的判断标准）
- Autonomy/rung thresholds 不在 target space
- Protected-path 扩展：gate 源文件加入 C0 never-auto-land
- Measurement 是 LLM-free（inv 4）；只有 propose 步骤用 LLM（learner, inv 7）
- 所有 actuation 通过现有 chokepoint（apply_memory + auto-land）

## F. Scoping (3/3 convergence)

| 关注点 | Scope | 位置 |
|---|---|---|
| 测量 | **per-project** | journal (project-scoped) + `ledger.md` (daemon rows) |
| 规则提议 | **per-project** → `memory.md` | 现有路径 |
| 全局规则 | **cross-project recurrence** → `~/.odo/user.md` | 现有 ≥2-projects promotion gate |
| 效果指标 | **无新层** | `ledger.md` (daemon-only, pull-only) |
| Flagged rules | **journal only** | `review_action{action:"memory_audit_flag"}` (不是 memory 层) |
| Per-workstream rules | **永远不** | workstream 是 branch；epoch notes 可以，always-injected rules 不行 |
| Gate 源文件 | **C0 protected** | `autoland.go`, `autonomy.go`, `learner.go`, `review.go`, `settle.go`, `ledger.go` |

### 不在 MVP 范围内

- Panel 自动调优自己的审查标准（最高风险）
- Orchestration self-improvement（无 ground truth）
- Auto-retraction（flag → human gate → land，不自动）
- Auto-accept for rule proposals（保持 deferred）
- Vector search for pattern mining

### 提议的新 ADR-0003 修正

> **Invariant 8 (proposed): Self-improving loops measure with human ground truth and actuate through reviewed rules, never through metrics.** The measurement half (audit → ledger) is pull-only and never injected; the actuation half (retraction proposal → apply_memory) is a human-reviewed rule, never an auto-applied metric. Auto-land's own outcomes never grade the loop.

> **Protected-path extension (proposed):** Add safety-mechanism source files to the C0 never-auto-land set. Today only `.odo/` and `wiki/` are protected; the gate code is not, so a small in-scope edit weakening a gate could auto-land. The loop may improve agent behavior but cannot auto-land changes to its own gates.

### 实施排序（each independently reversible）

| Wave | 内容 | 风险 |
|---|---|---|
| 1 | `odo rules audit` CLI (read-only measure) + ledger rows | 零（只读） |
| 2 | `odo autonomy audit` 扩展：panel-agreement 统计 | 零（observability only） |
| 3 | Flagged rules → learnerPrompt as DATA + daemon vet | 低（human gate 保留） |
| 4 | GUI: MemoryPanel 显示 flagged rules + 效果指标 | 零（display only） |
