Working...
# 设计审查：将 f/g 移动至 Canary 层 (D9 锁修订)

## 结论：接受修改 (ACCEPT_WITH_FIXES)

方案 1 的方向是合理的——f/g 是行为效能指标，需要实时注入流量，而在冻结回放（frozen replay）中，新生候选者在结构上无法达到这些要求。然而，本次修订需要三项具体的修正（关于 h 检查、状态终结器和计数器事实连接），详见下文。

## 问题回答

### Q1 — 确切的新归宿：Canary 阶段门控 vs. 提升验证 (Promotion Verdict)

**答案：在 `learningPromotionVerdict` (learning_measure.go:442) 内部作为附加条件落地，而不是一个单独的 Canary 阶段门控。**

证据与推理：
- 提升验证已经是一个干净的 §5.3 门控——它仅使用带日志的度量结构（`learning_measure.go:442` 文档字符串：“仅使用带日志的度量，绝非原始证据”）。在其内部添加 f′/g′ 分支意味着零新增证据读取器，零新增日志结构。f/g 分支可以在每次时钟周期免费获得，因为 `learningCanaryMeasure` (learning_stages.go:453) 已经折叠了度量并对其进行了日志记录。
- 如果设计一个单独的 Canary 阶段门控（类似于回放），要么需要读取原始证据（违反了 §5.3 的结构分离——度量折叠是“提供学习门控的唯一被许可的证据读取器”，见 learning_measure.go 头部），要么也需要使用相同的度量结构——在这种情况下，它只是一个拆分为两行的验证，没有获得任何好处，但增加了第二个门控行系列。
- 提升失败的语义已经正确：“” = 保持度量（统计缺失时从不丢弃，证据会老化——见 `learning_measure.go:442` 文档字符串以及 `learning_stages.go` 中默认分支仅记录停滞的行为）。这适用于 f′/g′ 的失败：统计缺失 = 保持，而不是丢弃。

**但此归宿选择引发了一个必须解决的问题**：永久空洞的候选者可能会占用唯一的 Canary 槽位。当前的停滞咨询 (learning_stages.go, `learningCanaryMeasure` 默认分支) 会在“年龄 > 12 且 Canary 结果 < 10”时触发——它仅提供可见性，绝不自动丢弃。在单一槽位和确定性交错分配的情况下，一个空洞的占用者会通过 `shadow_queued` (learning_stages.go, `slotOccupied` 分支) 使整个影子队列处于饥饿状态。

**修复方案**：为空洞添加一个确定性的终结器，这与“统计缺失时保持”的规则不同：
- 丢弃条件：当度量窗口 ≥ `learningPromotionMinOutcomes` (10 个 Canary 结果，即确实存在实际流量) 时，若所有 Canary 注入上的规则注入均为 0，则丢弃。从未触发且在衡量了实际流量的情况下，该规则在结构上无法防止伤害——这类似于原先认为的“出生时的空洞是时间上的必然性，而非缺陷信号”。
- 终端原因：`vacuous_never_fired`，并附带明确的锁定说明：这不会触及 R2 冻结集 (见 Q4)。
- 零流量情况（结果 < 10，规则从未触发）将保留为 "" + 停滞咨询——流量不足确实是不确定的，且人类丢弃 CLI (W6) 是升级处理的途径。

### Q2 — g 的 Canary 重构

**基于候选者语法的实时对比，提出精确的整数不等式：**

- **f′ (Canary 反空洞)**：`preventedLive ≥ 1`，其中 `preventedLive` = LIVE 队列结果在发送满足规则 `when:` 谓词且结果为拒绝类（人类拒绝或弱拒绝）的次数——即候选者本应拦截的伤害。这直接替代了回放时代基于反事实的 f：不再是“冻结切片中反事实队列的预防伤害”，而是“Canary 观察窗口期间 LIVE 流量上相同谓词的预防伤害”。
- **g′ (摩擦差分)**：`2·ruleRejC + ruleWeakC ≤ 3 × preventedLive`，其中 `ruleRejC`/`ruleWeakC` 是来自 Canary 队列注入的按规则拒绝/弱拒绝计数（`computeLearningMeasure` 中已有的 `ruleTally` 累加器，learning_measure.go:155）。纯整数——无除法，无浮点数。
- **零分母保护**：如果 `preventedLive == 0 && frictionC > 0` → g′ 失败（规则导致摩擦但明显未防止任何伤害）。`preventedLive == 0 && frictionC == 0` 意味着规则从未产生可区分的影响 → 走 Q1 的空洞路径，而不是 g′。
- 浮点陷阱先例 (rules_audit.go:553-556: `(2*a.rej+a.weak)*bInj >= rulesFlagRateFactor*(2*bRej+bWeak)*a.inj` ——“float64 中的 2x0.15 是 0.30000000000000004”) 已通过使用原始整数乘法得到遵守；不等式中没有引入比率。

**实现成本（诚实记录）**：`computeLearningMeasure` 目前仅计算 Canary 块中存在的规则的规则归因计数（`containsNormalized(ruleSet, n)`，learning_measure.go:~250）。计算 LIVE 腿上的 `preventedLive` 需要扩展折叠逻辑以在 LIVE 队列发送上评估候选者的 `when:` 谓词——这是一种新的连接。它必须重用与回放的归因连接相同的资格判定谓词（参见 Q6 引脚 2）。

### Q3 — 影子进入标准

**现有的影子→Canary 标准就足够了；无需修改。**

来自 learning_stages.go:170 (`learningShadowCheckpoints`) 的证据：
- 每个主车道蒸馏尾部的重跑冻结回放；失败 ⇒ `shadow_failed` 丢弃。
- `aged = mainEpoch - learningCandidateMainEpoch(...) >= learningShadowAgingEpochs (3)`。
- `learningFrozenHits` R2 阶段中断。
- 单槽检查（`slotOccupied`，第 189 行）→ 否则为 `shadow_queued`。

随着 f/g 从回放中移除，一个 a-e 绿色的新生候选者会立即通过检查点，并在 3 个主车道年龄时进入 Canary——这正是预期的行为，因为效能验证现在发生在 Canary 中。交错 seam (learning_canary.go:76, M = round(1/f) ≥ 2, 序数 % M == 0，运行前记录分配) 与此无关，不受影响。没有缺口。

### Q4 — 回滚交互

**无停机更改；一个明确的保护要求。**

- `learningRollbackTargets` (learning_measure.go:491) 读取 `m.Rules[].Harmful` —— 精确的 rules-audit 有害元组 (rulesFlagMinInjections/MinRejects/MinRejectConversations/RateFactor, rules_audit.go:94-97)。f/g 从未被回滚语义引用。R1 的两层机制 (learning_rollback.go) 和每 epoch 的重新度量节奏保持不变。
- **新模式（来自 Q1 的空洞终端）**：空洞丢弃不能将文本添加到 R2 冻结集中。冻结集仅来自 `learning_rollback` 行（learning_rollback.go: "R2 冻结集读取此行"）。空洞丢弃使用不同的操作/原因，并且锁必须说明空洞 ≠ 有害：一个从未触发的无害规则想法应该可以自由重新提出，这符合 R2 自身拒绝“一次失误永久杀死一个规则想法”的精神。如果空洞丢弃触碰到了冻结集，就会为良性想法创建一个永久性的文本黑名单。

### Q5 — 锁文本修订草案

**§2.3 (回放通过标准) — 替换 f/g 条目：**

> 删除 f 和 g 作为回放通过标准。回放保留检查 a, b, c, d, e, h (h — loosened == 0 — 是确定性的日志历史卫生检查，而不是行为效能检查，因此根据 2026-09-01 的裁定其作用域为“仅限 a-e 卫生”，它保留在回放中)。`learningReplayReport` 中的 `prevented_harm`/`friction`/`loosened` 计数器字段仍然被记录，但 f/g 在回放中不再是门控条件。

**§3 (Canary 提升) — 添加：**

> Canary→project_active 的提升在现有的成对队列阶段 (canary ≤ live×5/4; taint ≤ live+5pp; 两个队列均 ≥10) 之外，还增加了两个效能阶段：
> - **f′ (反空洞)**：候选者添加规则的实时队列预防伤害 ≥ 1 (LIVE 队列结果在 LIVE 发送满足规则 `when:` 谓词且结果为拒绝类的情况下，按度量窗口计算)。
> - **g′ (摩擦差分)**：`2·(Canary 按规则拒绝) + (Canary 按规则弱拒绝) ≤ 3 × preventedLive`。所有整数，无除法。
> - **空洞终端**：如果窗口包含 ≥10 个 Canary 结果且所有 Canary 注入中的规则注入为 0，则 Canary→以原因 `vacuous_never_fired` 丢弃。此转换永远不会将文本添加到 R2 冻结集中。
> - f′/g′ 统计失败 = 保持度量 (stat-miss semantics)；只有空洞终端和有害元组才会丢弃。

**§3.2/§5 润色**：一条 §5 的句子 — “新 Canary f′ 腿中的实时预防伤害连接重用了与冻结回放的归因连接相同的资格判定谓词（一个共享谓词，从不在两处实现）。”

### Q6 — 失败模式锁定

12 个锁定的修改与新增：

1. **已修改 — 空洞候选者锁定**：旧锁定“空洞候选者 → GLM 防止伤害要求”被重定位：f′ 在 Canary 层强制执行它，并且空洞终端防止了无限期槽位占用。**仅靠 W5 停滞咨询是不够的** — 它 (a) 每 (hash, stage) 记录一次且不再重复，(b) 从不释放唯一的 Canary 槽位，因此队列停滞是不可见的，直到队列堆积。终端是必须的，而不仅仅是咨询。
2. **新锁定 — 谓词单源**：回放的资格判定连接和度量新的实时预防伤害连接共享一个谓词实现；通过将双重执行字节相同的固定装置扩展为覆盖度量折叠来固定（扩展 Sol 不同的回放锁定）。
3. **新锁定 — 空洞丢弃从不冻结**：`vacuous_never_fired` 丢弃将零条目写入 R2 冻结集（空洞 ≠ 有害）。

## 最尖锐的失败模式

1. **Canary 槽位占用作为修订的副作用**。在不改变丢弃语义的情况下，将 f/g 移动到 Canary 层，会创建一类使提升验证返回 "" 的候选者，直到时间尽头（例如一个从未在其 0.25 槽位上触发的规则——每个统计阶段都轻松通过，但 f′ 无法满足）。结合 R3 的单槽规则和仅一次的停滞咨询，一个占用者会静默地使整个影子管道处于饥饿状态。空洞终端（真实的流量，零触发 → 丢弃，无冻结）是唯一的干净释放；没有它，这项修订就是用一个确定性的死胡同换取了另一个。

2. **无法触发的规则使拒绝率阶段变得毫无意义**。在 Canary 中，`learningPromotionVerdict` 的拒绝率差分在 0 注入时轻松通过（cr=0 在任何 lr 下都不可能超过 live×5/4）。空洞保护必须是促进前的明确独立阶段 —— 将其合并到比率差分中会重新打开原来 f 检查所关闭的确切空洞性漏洞。

3. **防伤害连接的双重实现漂移**。f′ 需要在度量折叠内评估 `when:` 谓词，而回放现在不再执行此操作。如果 Canary 防伤害连接与回放的归因连接分叉（不同的标准化、不同的窗口规则），候选者可能会通过 Canary 验证但在稍后的回放重新检查中失败，反之亦然 —— 这正是 Sol 回放发散锁定所禁止的阶级不安全。缓解措施：一个共享的谓词，包含在双重执行固定装置中。
