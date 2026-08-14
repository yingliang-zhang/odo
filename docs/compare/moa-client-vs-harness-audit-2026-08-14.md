# Tri-Model Audit (2026-08-14): Odo's moa/client.go vs harness direct-LLM clients

> Fourth tri-model run of the 2026-08-13/14 harness session. Question: how
> good is `internal/moa/client.go` (627 lines, zero-dep, Anthropic-Messages
> client to the Sudo gateway), measured against the direct-LLM client
> layers of deepseek-harness, grok-build, and openai/codex?
> Consolidated from a blind-sealed tri-model MoA run — all three legs
> complete, no recovery needed. Raw legs + frozen brief:
> `./moa-client-vs-harness-audit-2026-08-14/`.
>
> **Wave labels normalized:** each leg invented its own wave numbering;
> this consolidation maps everything onto the canonical R-waves from
> `router-vs-omp-eval-2026-08-14.md` (R-W1 resilience → R-W2 distill →
> R-W3 learner/curator → R-W4 Design-MoA).

## 0. Verdict (3/3 convergent)

**Shore-up-first.** One convergent reason: `Client.post` is a single-shot
request with **zero transport resilience** — no retry, no backoff, no
429/`Retry-After` awareness — while every peer treats bounded, jittered,
`Retry-After`-honoring retry of 429/5xx/transport failures as table stakes
(codex `codex-client/src/retry.rs`, grok `xai-grok-sampler/src/retry.rs`,
dsh `packages/llm/llm/src/retry-policy.ts`). Inert under today's one-shot
traffic; exactly the failure class the planned expansions multiply
(unattended distill, a single-point consolidator, 3×16-round parallel
design legs). **Everything else is at or above peer standard for the
mission and must not grow.**

## 1. Where moa/client.go already beats the peers (3/3)

| Strength | Evidence | Peer comparison |
|---|---|---|
| Thinking-block verbatim replay | `rawContent` second unmarshal pass preserves the assistant turn byte-verbatim (observed kimi-k3 400 `thinking.thinking: Field required`); pinned by `TestQueryWithToolsReplaysAssistantVerbatim` | No peer pins a regression this precisely; grok merely accumulates thinking+signature in its stream layer |
| Most mature truncation semantics | truncated `tool_use` discarded whole and re-issued (never executed); truncated final answers ship flagged; two tests pin the split | grok explicitly never retries `MaxTokensTruncation` (odo is ahead of at least one peer); codex defers to provider config |
| Output budget = falsifiable ledger | ×2 escalation, ≤3 bumps to a per-model hard cap, every bump journaled as `Escalation{from,to,output_tokens}` | dsh defaults `DEFAULT_MAX_TOKENS` 256000 — odo's measured modelspec table (16–64K) correctly avoids that trap; grok just fails |
| Fail-loud discipline | `classifyStop` stop-reason whitelist ("a silent fallthrough is a failure class no alert can see"); budget-derived deadline (900s floor + `maxTok/120`, floor-not-ceiling); `ToolAudit` capped-input journaling; env-only credentials with URL scrubbing | codex has an equivalent stop taxonomy; none of the three journal a budget ledger into a durable store — they fleet-telemetry instead |

**Size/fit:** 627 LOC, zero deps, one file — vs codex client layer 2,497
LOC + 4 crates, grok sampler ~4.7K LOC / 14 files, dsh ~2.7K LOC plugin
stack. All three legs ruled the peer bulk solves problems odo doesn't have
(multi-tenant BYOK, OAuth, Cloudflare edge casework, 7-provider error
zoos, WS session stickiness).

## 2. The resilience gap (3/3) and the shore-up list

| # | Item | Conv. | Cost | Wave | Pri |
|---|---|---|---|---|---|
| 1 | **Bounded transport retry**: ≤3 attempts, 500ms×2ⁿ ±20% jitter, retry only 429/5xx/transport/timeout; honor `Retry-After` (≤30s); NEVER retry 4xx auth/422/525/526 (bad key stays fail-loud) | 3/3 | S | **R-W1** | P0 |
| 2 | **Typed error taxonomy**: `Error{Status, Class, Body}` + `errors.As`; classes Auth/RateLimit/Server/Transport/Invalid/Terminal — replaces today's message-string matching that forces `Infra:true` | 3/3 | S | R-W1 | P0 |
| 3 | **Surface `InputTokens` + wall time + derived tok/s in `Result`** — escalation re-pays whole prompts (worst 4× input at the 65536 cap); today that cost is invisible | 3/3 (K3+GLM+DSF elements) | S | R-W1/R-W2 | P1 |
| 4 | **Strict truncation mode**: non-display paths (consolidator) get an error at the hard cap, not a flagged partial — a half-synthesized verdict is worse than none | 2/3 (GLM's knob; K3's caller-branches-`Truncated` dissent recorded — compatible: the knob reuses the flag internally) | S | R-W4 | P1 |
| 5 | **Decouple the 16-round ceiling from the default** (design legs need explicit headroom; default stays 16) + `RoundCapError` returning accumulated partial work | 2/3 (GLM+DSF; K3 partial on round-cap discard) | S | R-W4 | P1 |
| 6 | **Per-round context accounting in `QueryWithTools`**: approaching ~70% of `modelspec.ContextWindow` → fail early / force a summarize-and-answer, instead of the whole leg dying at `model_context_window_exceeded` | 2/3 (GLM+DSF) | M | R-W4 | P2 |
| 7 | Small S items: hoist `QueryWithImages` base64 re-encode out of per-attempt `mkBody` (escalation re-encodes up to 3×460s — DSF unique); `apiVersion` as Client field (DSF unique); optional `x-odo-client` attribution header (K3 unique; gateway support unverified) | 1/3+ each | S | ride-along | P2 |

**distill-migration precondition (DSF unique, adopted):** every model used
for distill needs an explicit `modelspec` entry — unknown models fall back
to 16384/32768 budgets and a large distill window would silently truncate.
Additive map entries; no client change.

Fully hermetic: items 1–3 extend `client_test.go`'s httptest pinning
posture (same test style as the 16 existing behavioral pins).

## 3. What the peers do that moa must NOT copy (3/3)

- **codex**: WebSocket Responses-v2 + prewarm + sticky `x-codex-turn-state`
  sessions; OTel/SessionTelemetry fleet stack; OAuth login/keyring refresh.
- **grok**: 14-retry budget + HTTP/1.1 pool-escape + `GROK_POOL_*` knobs;
  CF-edge 52x casework + `x-should-retry`; 7-provider `ProviderError`
  parser; 3-wire multiplexer; doom-loop resampling; 413 image-strip.
  (Its never-retry-`max_tokens` stance is a deliberate product choice
  opposite to moa's escalation — moa's is right for its mission.)
- **dsh**: plugin registry / immutable config snapshot / `CredentialRef` /
  BYOK settings namespaces / `discoverModels` / zod `always`-retry mode /
  256K default budget.
- **SSE streaming itself (all three)**: buffered whole-body is correct for
  journaled one-shots; reassembling thinking blocks over SSE would
  reintroduce the kimi-400 risk (GLM's point). Streaming earns cost only
  where a GUI renders incrementally — not this client's mission.

## 4. Expansion risks + mitigations (3/3 merged)

| Expansion | Risk on this client | Mitigation |
|---|---|---|
| distill/learner/curator (R-W2/R-W3) | one transient 429/5xx silently kills a cycle — worse than today (degraded verdict → missing memory update) | #1–#3 land FIRST (sequencing rule); modelspec entries for distill models; caller-side escalation budget policy |
| Design-MoA consolidator (R-W4) | single point of failure by construction; a `Truncated` partial synthesis is worse than none | #4 strict mode; conservative per-model budgets; surface `Escalations` so the caller pre-compacts if bumps >1 |
| blind legs on `QueryWithTools` (R-W4) | 3 parallel legs × ≤16 rounds triple instantaneous 429 pressure on one gateway key; context-window exhaustion mid-loop kills the leg wholesale; executor scoping must be repo-root explicit (fstools is HOME-scoped via `moa_fs_root`) | #1 (+Retry-After-aware staggering), #5, #6; extend `fsToolExecutor` with a root parameter — do NOT create a second scope convention beside `moa_fs_root`; verified existing caps (64KB read / 2000 lines / 500 matches) stay |

## 5. Impact on the router-boundary plan

Inserts a **new first code wave R-W1 (moa resilience, S cost)** ahead of
the router evaluation's original W1: items #1–#3 in one landing, before
any migration wave. Rationale (3/3): an unattended task's tolerance for a
single 429 decides the order — the memory pipeline must not ride a
zero-retry client.

## 6. Honesty notes

- All three legs complete; K3's output had a duplicated tail section
  (ignored in consolidation).
- codex retry specifics read from `model-provider-info` defaults +
  `core/responses_retry.rs` citation (K3's "cited, not deep-read" flag
  carried over); dsh runtime retry loop body read at policy level only;
  dsh BYOK 3-wire verified via `llm-pi-ai` catalog specs.
- Peers' extra surface is real engineering for real problems — the
  not-copy verdicts are about mission fit, not quality.
