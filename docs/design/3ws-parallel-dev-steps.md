# 3-WS 并行开发：步骤与 Prompt

## 前置条件（已全部满足 ✅）

- P0a stale-diff rebase → 并行 land 不互相杀死
- P0b QueueDock → park goal 排队
- P1a Review inbox → 一个面板审查所有 ws 的 diff
- cap=4, 3 个 ws 同时跑留 1 个备用槽

---

## Step 1: 在 Odo GUI 创建 3 个 workstream

在 Sidebar 点 `+` 创建：

| Workstream 名称 | 用途 |
|---|---|
| `moa-chain` | WS-A: moa 客户端韧性 + distill/learner 迁移 |
| `gui-wave` | WS-B: GUI 波次（零 daemon 冲突） |
| `daemon-misc` | WS-C: 残余检查 + receipts fill |

---

## Step 2: 向每个 workstream 发送第一个任务

切换到每个 workstream，粘贴对应 prompt 后点 Send。

### WS-A "moa-chain" — 第一个任务: R-W1 moa resilience

```
Implement R-W1 moa resilience in internal/moa/client.go.

Read these design docs first:
- docs/compare/moa-client-vs-harness-audit-2026-08-14.md (§D gaps #1-3)
- docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md (row #2)

Add three things to internal/moa/client.go:

1. Bounded retry: retry on 5xx and network errors only (never 4xx).
   Max 3 attempts with exponential backoff + jitter (200ms base, ±50% jitter).
   Honor Retry-After header when present. No retry on context cancellation.

2. Typed errors: replace bare error returns with a typed Error struct:
   type Error struct {
       Status  int       // HTTP status code (0 for network errors)
       Class   string    // "rate_limit" | "server_error" | "network" | "client_error" | "timeout"
       Message  string   // human-readable detail
       RetryAfter *int   // seconds, from Retry-After header (nil if absent)
   }
   Existing callers check errors.Is/As — keep the error chain working.

3. Result struct: return usage metadata from Query/QueryWithTools:
   type Result struct {
       InputTokens   int
       OutputTokens  int
       WallSeconds   float64
       TokPerSec     float64
       StopReason    string
   }
   Callers that ignore Result continue to work (backward compatible).

4. Hermetic tests: add internal/moa/client_test.go with httptest.Server
   pins for: clean 200, 429 with Retry-After, 500 then 200 (retry works),
   network error (retry exhausted), context cancellation (no retry).

Do NOT change the Anthropic Messages protocol shape, the base URL, or the
auth header. Do NOT add new deps. Verify: go build ./... && go test ./internal/moa/ -v
```

### WS-B "gui-wave" — 第一个任务: Guardian taxonomy GUI rendering

```
Implement A-P0 #1: Guardian risk taxonomy GUI rendering in LedgerPanel.

Read these design docs first:
- docs/compare/harness-gui-tri-model-audit-2026-08-13.md (§3 item #3)
- docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md (row #4)

The daemon already journals review_action rows with risk_class, actor,
and outcome (W5, commit eb81f5b). The GUI needs to render these in
LedgerPanel (gui/src/components/LedgerPanel.tsx).

1. Read gui/src/components/LedgerPanel.tsx to understand the current
   ledger rendering. Read internal/ipc/risk.go for the risk class
   taxonomy (5 classes). Read internal/ipc/autonomy.go for how
   review_action payloads are structured.

2. Add columns/fields to LedgerPanel rows:
   - Risk class badge (color-coded: e.g. red=critical, amber=high,
     yellow=medium, gray=low, blue=info) — match existing CSS token patterns
   - Actor label: "auto_panel" → "Auto", "" → "Human"
   - Outcome: accept/reject/auto_land_blocked/refresh_attempted —
     each with a distinct badge
   - TimedOut indicator when present in the payload

3. The data source is the existing poll_events review_action rows.
   LedgerPanel already receives events — extend the row renderer.
   Do NOT add new IPC commands; the data is already in the journal.

4. Add CSS in gui/src/styles/app.css using existing tokens. No Tailwind.
   Match the existing badge/pill patterns (ws-pending-pill, status-badge).

5. Verify: cd gui && npx tsc --noEmit. If E2E test infrastructure is
   available, add a test to gui/e2e/ verifying ledger rows show risk badges.
```

### WS-C "daemon-misc" — 第一个任务: visible⟺logged 残余检查

```
Task: Verify the visible⟺logged assertion residual (A-P0 #2).

Read:
- docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md (row #5)
- internal/ipc/server.go — the send_message path (handleSendMessage,
  ~line 568) and the send-closure assertion added in fix-INT W2
  (commit f17da7b)

W2 added a fail-closed assertion: every prompt sent to the model MUST
have a corresponding journal receipt (model-visible ⟺ logged). The
residual question: is there any code path that constructs a model
request (via moa.Query or moa.QueryWithTools) WITHOUT journaling a
receipt?

Steps:
1. grep for all call sites of moa.Query, moa.QueryWithTools,
   moa.QueryWithImages in internal/ipc/
2. For each call site, verify there is a journal AppendEvent
   (EventReviewAction or similar) that records the request BEFORE
   or AFTER the model call
3. Check the /panel path, the /vision path, the distill path, the
   curator/learner path, and the review_diff path
4. Report: is there any gap? If yes, describe it with file:line
   references and a fix. If no gaps, say "no residual — assertion
   coverage is complete" and list every verified call site.

This is a read-only audit. Do NOT modify any files unless you find
a real gap. If you find a gap, implement the minimal fix (add the
missing journal receipt) and verify with go build ./... && go vet ./...
```

---

## Step 3: 监控与审查

3 个 workstream 同时跑后：

1. **StatusBar** 显示 "N background runs" — 点 chip 跳转
2. **Sidebar** 每行有状态点（蓝色=前台，紫色=后台，琥珀=待审查）
3. **Review tab**（ContextPanel 新 tab）聚合显示所有 ws 的 pending diff

当一个 workstream 的 agent 跑完：
- diff 出现在 Review inbox tab
- 在 Review inbox 里直接点 Accept/Reject（不用切换 workstream）
- 如果 diff 有冲突或质量问题，切换到对应 workstream 查看完整对话

---

## Step 4: tri-model review（每个 task 完成后）

每个 workstream 产出的 diff，我会（Hermes orchestrator）：
1. 提取 `git diff`
2. 派 3 路盲审（K3/GLM/DSF, 540s, --thinking max）
3. Consolidate 三路输出
4. ≥2/3 ACCEPT → 通知你 accept
5. <2/3 → 通知你问题 + 建议修复

---

## Step 5: 完成后发送下一个任务

| WS | 第一个完成 → | 第二个任务 prompt |
|---|---|---|
| WS-A | #2 moa resilience → | #7 distill→moa migration（依赖 #2 落地） |
| WS-B | #4 Guardian GUI → | #10 GUI Wave A: task registry + StatusBar |
| WS-C | #5 检查完成 → | #3 R-W1.5 receipts fill（request_sha16） |

WS-A 的第二个任务等 #2 被 accept 后再发（依赖链）。
WS-B 和 WS-C 的第二个任务可以 park（用 QueueDock 的 park toggle 排队）。

### WS-A 第二个任务（#2 accept 后）: distill→moa migration

```
Implement R-W2: migrate distill from OMP one-shot to moa.Query.

Read:
- docs/compare/router-vs-omp-eval-2026-08-14.md (verdict B)
- docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md (row #7)
- internal/ipc/server.go — the distill path (search for "distill")
- internal/moa/client.go — Query (now with retry + typed errors from #2)

Behind a prefs flag `distill_via: omp` (default: keep OMP for now).
When the flag is absent or "omp", behavior is unchanged. When set to
"moa", distill calls moa.Query instead of spawning an OMP process.

The migration is mechanical: the distill prompt construction already
builds the system+user messages. Route them through moa.Query instead
of the OMP adapter. Journal the same receipts (the moa client now
returns Result with token counts).

Verify: go build ./... && go test ./internal/ipc/ -count=1
```

### WS-B 第二个任务（#4 accept 后）: GUI Wave A

```
Implement GUI Wave A: background-task registry + StatusBar "still running" + attention-ordered Sidebar.

Read:
- docs/compare/harness-gui-tri-model-audit-2026-08-13.md (§3 items #1, #2)
- docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md (row #10)

1. StatusBar: replace the single "N background runs" chip with a
   multi-target dropdown — clicking shows a list of all running
   workstreams with their name + a "still running" label. Click any
   to jump. Add a completion flash when a background run finishes.

2. Sidebar: sort workstream rows by attention: Needs-input (pending
   diffs) → Working (running) → Idle → Done. Keep the existing status
   dots; add a per-row current-activity line when running.

3. Do NOT add a daemon task registry (that's a separate daemon wave).
   Use existing pending_counts + running_workstreams data.

Verify: cd gui && npx tsc --noEmit. Add E2E test if possible.
```

### WS-C 第二个任务（#5 完成后）: receipts fill

```
Implement R-W1.5: add request_sha16 + request_bytes to panel/review payloads.

Read:
- docs/design/odo-harness-audits-summary-and-plan-2026-08-14.md (row #3)
- internal/ipc/server.go — the review_diff path and panel assembly
- internal/ipc/autoland.go — where panel payloads are built

Add two additive fields to every moa review request and panel payload:
- request_sha16: sha16 of the exact request bytes sent to the model
- request_bytes: byte count of the request body

These ride the existing review_action / moa_review journal rows as
additive JSON keys (ADR-0002 immune). No new event types, no schema
migration. The fields go into the payload JSON, not the DB schema.

Verify: go build ./... && go test ./internal/ipc/ -count=1
```

---

## 操作总结

| 步骤 | 操作 | 位置 |
|---|---|---|
| 1 | 创建 3 个 workstream | Sidebar + 按钮 |
| 2 | 切换到每个 ws，粘贴对应 prompt，Send | ChatSurface |
| 3 | 3 个 agent 同时跑，监控 StatusBar/Sidebar | StatusBar + Sidebar |
| 4 | diff 到达 → Review tab 审查 → Accept/Reject | ContextPanel Review tab |
| 5 | 通知我做 tri-model review | 回复我 |
| 6 | review 通过 → accept → 发下一个任务（或 park） | ChatSurface / QueueDock |
