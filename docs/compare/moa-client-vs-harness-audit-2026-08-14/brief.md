# Frozen Brief — Tri-Model Audit: Odo's moa/client.go vs harness direct-LLM clients

You are analyst **__LEG_ID__**, one of three independent analysts. An
orchestrator consolidates your output with two other models' analyses.
Cite `path:Symbol` for every load-bearing claim; "not verified" > guess.

## HARD TIME DISCIPLINE

- Total budget ~35 min. Gather ≤15 min, then WRITE the report no matter
  what. No subagents, no builds, no test runs. greps/reads/`omp --help` only.
- Final assistant message must START with "## A. Verdict" and contain the
  complete A–F deliverable.

## The object under audit

`~/Projects/odo/internal/moa/client.go` (627 lines) — a single-file, zero-dep
Go client speaking Anthropic Messages to the Sudo gateway
(`https://coding.sudoai.cc/anthropic`, key env `SUDO_CODING_KEY` — never
print secrets, cite env-var NAMES only). Mission: "thinking tasks" (review,
audit, design, research) — one-shot completions + `QueryWithTools` read-only
loop (16-round cap). It is deliberately NOT an agent runtime (no file edits,
no shell, no sessions, no streaming) — that is OMP's job via
`internal/adapter/omp.go`. Read also: `internal/moa/client_test.go` (test
posture), `internal/modelspec/*.go` (per-model budgets), and the caller
shapes in `internal/ipc/server.go` (`reviewWithModel`, `/panel`, `/vision`)
and `internal/ipc/fstools.go` (tool executor). READ-ONLY: another session
has uncommitted work in this repo; never modify anything.

Planned expansions this client must carry (from today's tri-model
evaluation): distill/learner/curator migrating from OMP one-shots onto
`moa.Query`; a Design-MoA consolidator (single `Query` synthesizing 3 blind
proposals); Design-MoA blind proposal legs on `QueryWithTools` scoped to a
repo root.

## The comparators (frozen clones may still exist on disk — reuse if present)

- `/tmp/harness-src-k3/{dsh,grok,codex}` (k3's clones) OR
  `/tmp/harness-src-__LEG_ID__/*` (your own) — else `git clone --depth 1`
  into `/tmp/harness-src-__LEG_ID__/`: deepseek-ai/deepseek-harness,
  xai-org/grok-build, openai/codex.

Find each harness's **direct-LLM client layer** (not its agent loop):
discovery hints (verify, don't trust blindly):
- codex: `codex-rs/core/src/client*.rs`, `model_provider_info*.rs`,
  `error*.rs` — the Responses-API client.
- grok: `ls crates/codegen | grep -i 'model\|provider\|client\|llm'` — find
  the LLM protocol client crate(s).
- dsh: `ls packages/host`, `grep -rln "messages.create\|/v1/messages\|chat/completions\|responses" packages/host` — the BYOK 3-wire-protocol layer.
For each, assess ONLY the client layer: request construction, auth, retry/
timeout, streaming, error taxonomy, thinking/reasoning handling, tools
protocol, budgets/rate limits, telemetry.

You may read `~/Projects/odo/docs/compare/harness-tri-model-audit-2026-08-13.md`
and `harness-gui-tri-model-audit-2026-08-13.md` as shared context (wire-protocol
facts already established there); re-verify before citing specifics.

## Axes (all required, per codebase)

Request-construction exactness (who owns bytes on the wire); retry/backoff/
timeout policy (deadlines, jitter, which errors retry); streaming (SSE?
buffered? how partial output is handled); error taxonomy (status classes,
fail-loud vs silent); thinking/reasoning-block handling (replay quirks,
signatures); tools protocol (execution loop, round caps, auditability);
output-budget/token policy (max_tokens selection, escalation, truncation
handling); rate-limit/429 awareness; observability (journaling, receipts,
usage capture, metrics); credentials hygiene; test posture (what's pinned);
quirk management (how provider quirks are absorbed); size/simplicity vs fit
for a ~630-line mission.

## Deliverable structure

```
## A. Verdict  (ship-as-is / shore-up-first — with the single strongest reason)
## B. moa/client.go quality assessment  (strengths + weaknesses, evidence-cited)
## C. Comparison matrix  (odo-moa × codex × grok × dsh, across the axes)
## D. Gaps worth fixing in moa/client.go  (ranked; per item: gap / evidence from peers / fix sketch / cost S-M-L / maps to which wave W1-W4 / priority P0-P2 / confidence)
## E. What the peers do that moa must NOT copy  (over-engineering against its mission — with reason)
## F. Risks the planned expansions (distill, consolidator, design legs) put on this client + mitigations
```

## Constraints

READ-ONLY everywhere. Analysis only — no edits, no live API calls. Terse
prose, tables over walls.
