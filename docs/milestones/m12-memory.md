# M12 Memory — DESIGN LOCK (2026-08-10)

Tri-model design round (kimi-k3 / glm-5.2 / deepseek-v4-flash, blind, 900s) consolidated by the
orchestrator; user confirmed every decision below. Source specs: three legs archived in
/tmp/odo-m12/ (design-spec-kimi.md, m12-design-spec.md, spec.md). Preceding research:
five-system comparative audit (Odo/Hermes/OpenCode/Multica/Orca) also completed 2026-08-10.

## Scope

Batch 2 (this milestone): D-budget registry, D-auto (daemon-side automatic distill + curate),
D-todo (durable plan layer). Batch 3 (separate milestone): D-cross (cross-workstream recall push,
default `both`), D-semantic (miss-audit → FTS5 arm → embedding spike, pre-registered thresholds).

## D-budget — prompt budget registry (ships first)

- `internal/ipc/budgets.go`: static registry of every injection cap (name, owning constant,
  default bytes, clamp-max bytes, layer), incl. slash-path rows and memoryMap measured allowance.
- Bound test asserts BOTH `Σdefaults ≤ 48KB` (soft) AND `Σclamp-max ≤ 128KB` (hard) — a
  defaults-only test would bless 43KB while a prefs edit ships ~119KB (replay_total_kb=64).

## D-auto — daemon-side automatic distill + curate

GUI timer (App.tsx ~906-959) deleted. GUI becomes read-only disclosure (countdown chip + Cancel).
All triggers and every evaluation outcome journaled — nothing skipped silently.

| Trigger | Condition | Notes |
|---|---|---|
| T1 run-finished + idle | drainRun finished, no live run, eligible window, quiet 120s | replaces GUI timer; idle configurable, daemon floor 15s |
| T2 startup compensation | at boot, every active conversation's un-folded window recomputed from journal; eligible + last event older than idle → schedule (0-60s jitter) | kills the "run ended after app close" missed-fold hole |
| T3 urgent size | rendered window ≥ 128KB (½ of the 256KB backstop) → fire without idle | makes the silent `capEvents` drop-oldest path unreachable |
| T4 manual | existing IPC | unchanged; exempt from caps; keeps let-finish semantics |

- Eligibility: ≥6 events AND ≥16KB rendered (long /panel answers do NOT count toward the byte
  threshold — a 30KB panel reply must not trigger a fold by itself).
- Frequency/budget: ≤2/hour/conversation, ≤12/day/project (manual exempt). Cap-hit → journaled skip.
- Failure backoff: 5m → 30m → 2h → disabled until next user event; driven by journaled outcomes
  (survives restart). Failed distills MUST journal (today they journal nothing).
- **Cancel-before-note**: an auto-distill in flight carries a context cancel. A user send arriving
  mid-auto-distill cancels it (journals `cancelled_by_send`) and proceeds normally — NO refusal.
  Clean because the note write happens only after the one-shot returns; cancel-before-note leaves
  zero artifacts and the trigger re-arms. Manual distill keeps let-finish + refusal.
- **Slash gate (live-bug fix)**: /panel and /vision handlers now check `distilling` exactly like
  send_message — today a slash answer arriving mid-distill is folded into `last_seq` unseen.
- **Coverage-honesty rule (live-bug fix)**: if `capEvents` would omit events, an AUTO distill is
  refused and journaled `skipped{reason:"window_exceeds_prompt_budget"}` (surfaced via
  pending_counts); the fold marker never claims coverage it did not see. Manual distill behavior
  unchanged.
- Auto-curate: conditional, NEVER chained. Fire when ≥4 new distill markers since the latest curate
  marker OR newest curate older than 7 days; mechanical quality gate = existing curator validation
  + citation-liveness checked BEFORE rewriting topic pages (dead cite → skip, old generation kept)
  + curate marker gains `notes_read:[{name,sha16}]`. The `auto_curate_after_distill` pref + GUI
  chain is removed (journaled migration note). Curator citations become workstream-qualified
  (`(ui-epoch-2)`) — unqualified `(epoch-N)` collides across workstreams.
- Journal deltas: distill marker += `{trigger:"manual|idle|startup|urgent", window_events,
  window_bytes}`; auto attempts: `memory_update{layer:"auto_distill", cause:"scheduled|fired|
  skipped|cancelled_by_send|failed", detail}`. Spend per trigger-class becomes a SQL query.
- Prefs (fail-to-default): `auto_distill:"on_idle"` — DEFAULT FLIPPED, applies to existing installs
  (explicit `never` is preserved), `auto_distill_idle_seconds:120`, `auto_distill_min_events:6`,
  `auto_distill_min_kb:16`, `auto_distill_urgent_kb:128`, `auto_distill_max_per_hour:2`,
  `auto_distill_daily_cap:12`, `auto_curate_min_notes:4`, `auto_curate_max_age_days:7`.
- GUI: `pending_counts` extends with `auto_distill:[{conversation_id, eta_unix, trigger}]` +
  `distilling`; composer chip countdown + Cancel (`auto_distill_ctl{action:"disarm"}`, journaled;
  any send also disarms); composer lock during MANUAL distill only.

## D-todo — durable plan layer

- **Write path (ADR-0003 inv 1 scoped amendment)**: the agent emits a fenced ```odo-todo``` JSON
  op block inside its normal agent_text; the daemon parses it mechanically (fixed schema, no
  content evaluation) at drainRun ingest and merges it into the journaled todo state. The daemon
  remains the sole writer of every layer. Todo content is a RECORD (plan state), never a RULE.
- **Storage**: journal-only. `review_action{action:"todo_merge", ops_applied, ops_rejected:
  [{op,id,reason}], snapshot:[{id,text,status,origin_seq,updated_seq}], snapshot_sha}` — full
  snapshot per merge; boot re-materializes via one journal scan. NO table, NO derived file
  (injection + `odo todo` CLI render on demand; user chose K3's no-file variant).
- Ops: `add{text}` (single line ≤240B), `done{id}`, `strike{id}` (never deletion), `reword{id,text}`;
  daemon-assigned `t<N>` ids; unknown/non-open id → rejected + journaled; duplicate-normalized add
  → reaffirm bumping `updated_seq`; open cap 30/conversation. Malformed block → zero-apply +
  journaled parse_error; agent_text itself never modified.
- Lifecycle: open items SURVIVE folds (the point); done/stricken visible through the current epoch,
  swept at fold (journal snapshots retain them); open ≥3 folds untouched → `stale:true` marker,
  never auto-struck. Surviving open items are appended to the distill prompt as labeled
  authoritative state → Open loops seeded from truth.
- Injection: `## Current plan (todo — journal-backed, durable across folds)` between resume card
  and replay; cap 1.5KB, whole-item cut, omission marker; receipt `journal#todo` → sha16. Empty ⇒
  no block, no receipt. Send path only (slash contract stays lean).
- GUI: composer chip "Plan · N open" → popover (done checkbox, strike, stale dim, + add) via
  `todo_update` IPC, journaled. No new tabs.
- ADR-0003 amendment texts (inv 1 scoped todo exception; inv 7: distill remains the only LLM write
  cadence, todo merge is mechanical bookkeeping; layers table += todo 1.5KB) are recorded in the
  kimi leg spec §D-todo.7 and will be copied into docs/adr/0003 with the Batch-2 landing commit.

## Out of scope (do-NOT, locked)

Chained auto-curate; GUI-owned triggers; send refusal during auto-distill; sibling-note newest-first
fallback; per-todo human gate; todo table/file as truth; LLM in merge/gating/staleness; vectors
before the spike's pre-registered gate; memory layers inside the distill prompt (observe-only);
splitting oversized epochs across two distills; Hindsight as Odo's recall substrate (revisit only
if the spike proves vectors AND the corpus outgrows a local index).

## Verification

go build/vet/test full suite; new tests cover: all four triggers, startup compensation, eligibility
(panel exclusion), cancel-before-note send flow, slash gate, refusal honesty, curate gate
(liveness, SHA schema), todo parser fuzz/merge/rejects/sweep/stale/injection/dedupe, budget
Σ-tests. Dogfooding acceptance (2 weeks): zero send refusals caused by auto-distill; 100% of auto
evaluations journaled; no coverage-lie marker (replay distill prompt on every marker window);
kill-app restart fires exactly one startup trigger; layer bytes before replay stable across sends.
