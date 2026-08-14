Working...
I have a thorough, evidence-cited picture of all four client layers. Writing the report now.

## A. Verdict

**Shore-up-first.** The client is a well-scoped, correctly-quirky 627-line thinking-task client — *but it has zero transport-layer resilience*: a single transient 429/5xx/connection error from the Sudo gateway kills the whole review leg, and the planned fan-out (distill/curator, a 3-leg Design-MoA consolidator, repo-root-scoped blind proposal legs) multiplies exactly that failure mode. The single strongest reason: `moa/client.go:post` returns on the first non-nil `HTTP.Do`/non-200 error (`client.go:262-281`) with no retry, no jitter, no `Retry-After` honoring, and a coarse one-line error that lets no caller branch AUTH vs RATE_LIMIT vs SERVER — while every peer's client layer treats transient retry as table stakes (`xai-grok-sampler/src/retry.rs:8-42`, `dsh packages/llm/llm/src/retry-policy.ts:14-24`).

Wave definitions used below (inferred from the brief's planned expansions; stated so the mapping is grounded): **W1** = pre-fan-out hardening (retry/error/observability). **W2** = distill/learner/curator onto `moa.Query`. **W3** = Design-MoA consolidator (one `Query` synthesizing 3 blind proposals). **W4** = Design-MoA blind proposal legs on `QueryWithTools` scoped to a repo root.

---

## B. moa/client.go quality assessment

**Strengths (evidence-cited)**

1. **Mission discipline.** Package doc declares it is *deliberately not* an agent runtime — no file edits, shell, sessions, streaming — that is OMP's job (`client.go:1-13`). 627 lines, zero external deps. The scope is the right size for the job.
2. **Thinking-block verbatim replay — the subtlest quirk, correctly absorbed.** The tool loop re-echoes the assistant turn from `rawContent` (a second JSON pass on the raw `content` array), not the cooked `contentBlock` struct, because thinking blocks carry fields the struct doesn't model (`thinking`, `signature`) and the gateway rejects round 2 with `400 "thinking.thinking: Field required"` (kimi-k3). Pinned by `TestQueryWithToolsReplaysAssistantVerbatim` (`client.go:179-194`, `:536-542`; `client_test.go:117-171`).
3. **Fail-loud stop classification.** `classifyStop` whitelists the known stop_reasons and logs+defaults unknown ones to `end_turn` rather than silently doing something else; `refusal`/`model_context_window_exceeded` are terminal (`client.go:307-328`). A silent fallthrough is a failure class no alert can see — the comment says so explicitly.
4. **Falsifiable budget ledger.** `outputBudget.escalate` doubles `max_tokens` on `max_tokens` stop up to the per-model `MaxOutput` cap, ≤3 bumps, and every bump lands in `Result.Escalations` (`client.go:333-380`, `:386-430`). The policy is an audited ledger, not a silent retry.
5. **Block-type-aware truncation split in the tool loop.** A truncated `tool_use` (half-written JSON) is discarded and the turn re-issued whole — never executed; a truncated *final answer* ships flagged (the `/panel` display contract). Pinned by `TestQueryWithToolsHardCapSplit` (`client.go:458-465`, `:503-518`; `client_test.go:354-387`).
6. **Budget-derived per-request deadline, floor-not-ceiling.** `requestTimeout = 900s + maxTok/120`; the 900s base covers a max-effort thinking leg's server-side latency before the first output token, and the generation headroom stacks on top at a conservative 120 tok/s floor (`client.go:39-57`, `:234-241`).
7. **Tool audit journaling.** Every executed call yields a `ToolAudit{name, input≤256B, result_bytes, error}` — capped input, full bytes live only in the request (`client.go:127-138`, `:544-556`).
8. **Credentials hygiene.** Env-only (`SUDO_CODING_KEY` or named var), empty-key guard, key never logged (`client.go:387-389`).
9. **Test posture is behavioral and deterministic.** ~16 `httptest` tests pin observable contracts — verbatim replay, escalation ladder, hard-cap split, stop classes, timeout math, the 16-round cap — not source text (`client_test.go` throughout).

**Weaknesses (evidence-cited)**

1. **No transport retry/backoff/jitter.** `post` returns on the first `HTTP.Do` error or non-200 (`client.go:262-281`). The *only* re-issue is on `max_tokens` (budget), never on a transient transport failure. Not verified whether `reviewWithModel`/`/panel` wrap the client in retry — the client layer itself has none.
2. **No 429 / `Retry-After` awareness.** A 429 is surfaced as `moa: API returned 429: <body tail>`; the `Retry-After` header is never read (`client.go:268-281` reads body only).
3. **Coarse error taxonomy.** One `fmt.Errorf("moa: API returned %d: %s", …)` for *all* non-200 (`client.go:272-281`); no stable error code, so callers cannot branch AUTH vs RATE_LIMIT vs SERVER vs TRANSPORT.
4. **Fully buffered (`io.ReadAll`).** No partial progress; a 900s thinking leg produces nothing until the whole body lands (`client.go:268`). Acceptable for whole-answer thinking tasks, but it forecloses early-cancel-on-garbage and any `/panel` incremental render.
5. **`maxToolRounds == maxToolRounds` ceiling == default (16).** A caller cannot ask for more than 16 rounds — the clamp makes the ceiling the only value (`client.go:63-65`, `:480-482`). Fine for `/panel`; a hard limit for repo-root-scoped design legs (W4).
6. **No "error-on-final-truncation" mode.** The one-shot/tool paths always ship a flagged partial at the hard cap (`client.go:414-419`, `:511-518`). Correct for display content; wrong for a consolidator whose half-synthesized verdict must not look complete.
7. **Untyped image path.** `QueryWithImages` builds `map[string]interface{}` blocks while the text path uses the typed `messageRequest` struct — a type-safety asymmetry (`client.go:599-626` vs `:148-154`).
8. **No per-request latency / tok-s in `Result`.** `OutputTokens` is captured but not wall-time or effective tok/s (`client.go:400-406`).

---

## C. Comparison matrix

| Axis | **odo-moa** (`client.go`, 627 LoC) | **codex** (`core/src/client.rs`, 2497 LoC) | **grok** (`xai-grok-sampler`: `client.rs` 3169 + `retry.rs` 1055 + `provider_error.rs` 458) | **dsh** (`llm` core + `llm-deepseek` + `llm-pi-ai`, ~2700 LoC) |
|---|---|---|---|---|
| Request construction | Structs + `json.Marshal`; owns bytes directly; one shape per path (`client.go:148-154`, `:599`) | `build_responses_request` + routing/attestation/compression/service_tier headers (`client.rs:1481-1497`) | Per-provider `serializeRequest`; tolerant `ProviderError` body parser | `serializeRequest` + **immutable snapshot frozen before first `await`** so URL+key never cross config gens (`llm-pi-ai/adapter.ts:1-19`) |
| Retry/backoff/timeout | **None.** Deadline `=900s+maxTok/120`, no jitter; only `max_tokens` re-issues (`:234-241`) | 401 auth-refresh→retry; WS→HTTP session-scoped fallback; prewarm; provider retry budget (`client.rs:1523-1556`, `:1906-1917`) | **14 retries (15 fatal)**, exp backoff capped 30s + jitter, `Retry-After` honored for 429 (threshold 2), `x-should-retry` header, doom-loop backoff, strip-images-on-413 (`retry.rs:8-66`) | Exp backoff + symmetric jitter (0.1), `maxRetries` 2, `retryableCodes` {EMPTY,RATE_LIMIT,SERVER,TIMEOUT,TRANSPORT}; **but adapters set `maxRetries:0`** — agent layer owns visible attempts (`retry-policy.ts:14-24`, `llm-pi-ai/adapter.ts:97`) |
| Streaming | **None** — `io.ReadAll`, all-or-nothing (`:268`) | SSE `eventsource_stream` + WebSocket streaming (`client.rs:94-95`, `:1511`) | SSE via `events.rs` (526 LoC) + `stream/` | Async-generator `StreamChunk`s (block-start/delta/end, reasoning-delta, tool-call-delta) + idle watchdog + abort (`llm-pi-ai/stream.ts:124-207`) |
| Error taxonomy | Coarse: one `Errorf` for all non-200; 3 stop classes (`:272-281`, `:299-305`) | `ApiError`/`TransportError`, `StatusCode` branching, `map_api_error`, unauthorized recovery | `ProviderError` tolerant parser across 7+ provider shapes (openai/anthropic/google/azure/vertex/bedrock/openrouter), slug extraction, double-encoding unwrap, markup detection (`provider_error.rs:138-228`) | Stable `LlmError` codes: AUTH, RATE_LIMIT, INVALID_REQUEST, SERVER, TIMEOUT, TRANSPORT, CONTEXT_WINDOW_EXCEEDED, QUOTA, EMPTY_RESPONSE, NO_ADAPTER, MISSING_CREDENTIAL, … (`llm-deepseek/adapter.ts:138-149`, `error.ts`) |
| Thinking/reasoning | Decode-only; `rawContent` verbatim replay for signatures; `thinking()` for journal (`:179-221`) | `Reasoning`/`ReasoningEffort`/`ReasoningSummary` config + service_tier (`client.rs:46-47`) | Reasoning in sampler stream | Levels off/high/max, `thinking_start/delta/end` blocks, `thinkingBudgets`, `cacheRetention` (`llm-pi-ai/adapter.ts:81-99`, `stream.ts:145-153`) |
| Tools protocol | Read-only loop, 16-round cap, daemon executor, `ToolAudit` journal, block-type truncation split (`:466-567`) | Full Responses-API tools; `create_tools_json` | `toolcall_start/delta/end` stream blocks | `toolUse` finish reason; raw-JSON arguments (`stream.ts:154-187`) |
| Output budget | Per-model `modelspec` MaxTokens/MaxOutput; ×2 escalate ≤3; re-issue whole; flagged partial at cap (`:360-430`) | `model_info` + service_tier + effort; compaction | Per-model catalog `maxTokens`; retry budget env override | `configuredMaxTokens` per model, `defaultMaxTokens`, `contextWindow` (`llm-pi-ai/adapter.ts:251-274`) |
| 429 / rate limit | **None** — plain non-200 error | 429 in transport-error handling | `Retry-After` honored (capped 120s parse), threshold 2, `edge_client` policy | `RATE_LIMIT` code, retryable |
| Observability | `log.Printf` + `Result{Escalations,OutputTokens}` + `ToolAudit`; daemon journal persists | `SessionTelemetry`/`RequestTelemetry`/`SseTelemetry`/W3C trace/inference traces/feedback tags/otel | `metrics.rs` + mixpanel + OTLP crates | `StatsLine` (billed, cache-hit %, TTFT, tok/s, wall); telemetry default-off + anon user id |
| Credentials | Env-only, empty-key guard, never logged (`:387-389`) | `AuthManager`/OAuth refresh/attestation | `auth_provider`/`credential_provider`/secrets crate | `CredentialRef` (name, not literal), per-request resolver from snapshot, fail-loud `MISSING_CREDENTIAL` |
| Test posture (pinned) | ~16 `httptest` behavioral tests: replay, escalation, hard-cap split, stop classes, timeout math, round cap | `client_tests.rs` + transport/auth suites | `provider_error` corpus (10+ provider shapes), retry suites, sampling-client tests | `catalog.spec`/`adapter.spec`/`dynamic-config.spec` |
| Quirk management | Inline comments: gateway stop_reason sweep, kimi signature quirk, tok/s floor measurement — absorbed in code | `model_provider_info`/`wire_api` handling | `provider_error` absorbs 7+ provider error shapes | `classifyPiAiError` text-matches upstream's flattened error messages (`llm-pi-ai/stream.ts:39-62`) |
| Size vs mission | 627 LoC, zero-dep, single file — **fits** | 2497+ LoC, agent-runtime scale — overkill | 4.7K LoC sampler — overkill | ~2.7K LoC plugin scale — overkill |

---

## D. Gaps worth fixing in moa/client.go (ranked)

| # | Gap | Evidence from peers | Fix sketch | Cost | Wave | Pri | Conf. |
|---|---|---|---|---|---|---|---|
| D1 | **No transport retry/backoff/jitter** — one transient error kills the leg | grok `retry.rs:8-66` (14-retry budget, jittered backoff, `Retry-After`); dsh `retry-policy.ts:14-24` (exp backoff+jitter, retryable codes) | Bounded loop in `post`: retry 429/5xx/net-errors with exp backoff+jitter, ≤3 attempts, 30s cap, honor `Retry-After` if present; keep `max_tokens` re-issue separate (budget, not transport) | S–M | W1 | **P0** | high |
| D2 | **Coarse error taxonomy** — callers can't branch on failure class | dsh stable `LlmError` codes (`AUTH/RATE_LIMIT/SERVER/TRANSPORT/…`, `adapter.ts:138-149`); grok `ProviderError.slug` | Small `Error{Status int; Code string; Body string}` + `classify()`: 401/403→AUTH, 429→RATE_LIMIT, 5xx→SERVER, `net.OpError`→TRANSPORT; keep `max_tokens`/`refusal` stop classes as-is | S | W1 | **P0** | high |
| D3 | **No `Retry-After` / server-hint honoring** | grok parses+caps `Retry-After` (`retry.rs:18-21,60-66`); dsh `RATE_LIMIT` retryable | In `post`, on 429 read `Retry-After` (seconds or HTTP-date), feed as the backoff floor (capped 120s) | S | W1 | **P0** | high |
| D4 | **No "error-on-truncation" mode for non-display paths** — consolidator would ship a half-synthesized verdict as a flagged partial | n/a (moa-internal); peers don't conflate display vs verdict paths | Add a `Strict bool` (or a `Policy`) on `Query`/oneShot so a final-answer `max_tokens` at the cap returns an error instead of a flagged partial; keep `/panel`/`/vision` on the lenient default | S | W3 | **P0** | high |
| D5 | **`maxToolRounds` ceiling == default (16)** — design legs (W4) have no headroom | grok retries are env/config overridable (`GROK_MAX_RETRIES`, `retry.rs:80-84`) | Decouple ceiling from default: raise `maxToolRounds` (e.g. 24–32) and let W4 callers request it; keep a sane default for `/panel` | S | W4 | **P1** | high |
| D6 | **No per-request latency / tok-s in `Result`** | dsh `StatsLine` (TTFT, tok/s, wall) | Add `Wall time.Duration` + derive `TokPerSec` from `OutputTokens` to `Result`; cheap, already have token counts | S | W2 | P1 | high |
| D7 | **No structured/schema-constrained output** — consolidator/legs can't be forced to JSON | codex `--output-schema` (audit doc §5 #5) | Optional `response_format`/schema passthrough in `messageRequest` if the Sudo gateway honors Anthropic tool-choice/schema; schema-in-prompt + strict validator as fallback | S–M | W3 | P1 | medium (gateway support unverified) |
| D8 | **Untyped image content blocks** | moa's own text path uses typed structs | Typed `imageContentBlock` struct mirroring `contentBlock`; remove the `map[string]interface{}` in `QueryWithImages` | S | W4 | P2 | high |
| D9 | **Tool-loop context grows unbounded across rounds** (no compaction) — repo-root scan could blow context | moa delegates compaction to OMP by design (`:1-13`), but `QueryWithTools` runs without it | Per-round context-size estimate; error early (not silently truncate) when approaching `modelspec.ContextWindow`; fstools already caps per-result at 64KB (`fstools.go:45-54`) | S | W4 | P1 | medium |
| D10 | **Buffered response forecloses early-cancel/partial** | all peers stream | Do **not** add SSE for the mission (see E); instead ensure `ctx` cancel is checked (it is, via `NewRequestWithContext`) and document that long legs are all-or-nothing by design | — | — | P2 | medium |

---

## E. What the peers do that moa must NOT copy

| Peer feature | Why not for moa |
|---|---|
| **codex WebSocket + HTTP transport fallback / prewarm / OAuth token refresh / attestation** (`client.rs:11-24,1523-1556`) | Agent-runtime scale. moa is a one-shot, single-gateway, env-key client; WS prewarm and token refresh solve multi-turn session continuity and ChatGPT auth moa doesn't have. |
| **grok 14-retry budget + doom-loop detection + 7-provider `ProviderError` parser** (`retry.rs`, `provider_error.rs`) | moa speaks ONE gateway (Sudo) over ONE protocol (Anthropic Messages). The provider-error zoo (openai/google/azure/vertex/bedrock/openrouter) is dead weight; doom-loop detection belongs to an agent loop, not a one-shot. |
| **dsh plugin/adapter registry + immutable config snapshot + `CredentialRef` indirection** (`llm-pi-ai/adapter.ts:1-19`) | Solves multi-tenant, multi-provider, live-reconfig problems. moa is single-user, single-gateway, env-key; the snapshot/credential-ref machinery is abstraction against a problem moa doesn't have. |
| **SSE streaming for its own sake** | The verbatim-replay invariant (`rawContent`) is *harder* to preserve over a stream — reassembling thinking blocks + signatures token-by-token reintroduces the kimi 400 risk. For whole-answer thinking tasks the buffered approach is the right call; streaming only earns its cost on `/panel` incremental UX, which is not the mission. |
| **dsh `always`-retry / unbounded mode** (`retry-policy.ts:49-54`) | For a review leg, an infinite retry loop *hides* a broken gateway. moa's fail-loud posture is the mission; bounded retry (D1) is the right ceiling, not unbounded. |

---

## F. Risks the planned expansions put on this client + mitigations

| Expansion | Risk on this client | Mitigation |
|---|---|---|
| **W2 — distill/learner/curator onto `moa.Query`** (from OMP one-shots) | These migrate off an OMP path that had transport retry/streaming/compaction. moa has none of the first two → a transient gateway blip now drops a distill run where OMP would have retried. Also: budget escalation re-issues the *whole* prompt at 2× (`client.go:39-45`) — fine for small `/panel` input, potentially expensive for a large distill corpus. | Ship **D1+D2+D3 (W1)** before any migration. Verify `max_tokens` stop is rare for distill inputs; if frequent, the re-issue-whole tradeoff (vs continuation) deserves a measurement, not the existing assumption. |
| **W3 — Design-MoA consolidator** (one `Query` synthesizing 3 blind proposals) | The consolidator's input is 3× a single proposal; its output must synthesize a verdict. At the hard cap the current policy ships a **flagged partial of a synthesis** (`client.go:414-419`) — a half-synthesized verdict that *looks complete* is worse than a display partial. The consolidator is not display content. | **D4 (error-on-truncation mode, P0)** for the consolidator path. Raise the consolidator's `MaxOutput` in `modelspec` if warranted. **D7 (structured output)** so the synthesis is JSON-constrained and a partial is detectably malformed. |
| **W4 — Design-MoA blind proposal legs on `QueryWithTools` scoped to a repo root** | (1) The 16-round ceiling == default (`client.go:63-65,480-482`) — a repo-root scan routinely exceeds 16 rounds (the comment already notes 8 cut off a legit chain). (2) A transient 429 *mid-chain* kills the whole leg after minutes of tool rounds — **D1 bites hardest here.** (3) No compaction in the tool loop → a 16+ round repo scan can blow `ContextWindow`. | **D5** (raise/decouple the round ceiling, P1). **D1** (transport retry, P0 — non-negotiable for long legs). **D9** (per-round context-size guard that errors early rather than silently truncating, P1). Confirm `fstools` per-result caps (64KB read, 32MB grep scan — `fstools.go:45-54`) are adequate for repo-root scale. |

**Net:** the client's design choices (verbatim replay, fail-loud stops, budget ledger, block-type truncation split) are sound and earned. The gap is *resilience*, and it is small and cheap to close (D1–D3 are all S-cost, W1). Close W1 before any fan-out; add D4 before the consolidator; add D5+D9 before the design legs.
