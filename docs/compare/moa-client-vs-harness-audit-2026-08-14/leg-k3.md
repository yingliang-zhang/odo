# Tri-Model## A. Verdict

**Shore-up-first.** Single strongest reason: `post` (`internal/moa/client.go:Client.post`) is a **single-shot request with zero transport/HTTP retry and zero 429 awareness** — every peer (codex `codex-client/src/retry.rs:backoff`, grok `xai-grok-sampler/src/retry.rs:DEFAULT_MAX_RETRIES`, dsh `packages/llm/llm/src/retry-policy.ts:ResolvedRetryPolicy`) treats bounded, jittered/`Retry-After`-honoring retry of 429/5xx/transport failures as the minimum viable client behavior, and the planned expansions (unattended distill; a single-`Query` consolidator that fails wholesale; 3× parallel design legs) all multiply exposure to exactly that transient class. Everything else in the file is at or above peer standard for its mission and should not grow.

## B. moa/client.go quality assessment

**Strengths (evidence-cited):**

- **Request-construction exactness.** Hand-typed `messageRequest`/`contentBlock` (`client.go:messageRequest`), `omitempty` everywhere, system prompt top-level. Only the vision path uses `map[string]interface{}` (`Client.QueryWithImages`) — the loosest spot, still boring. Bytes on the wire are fully owned: zero deps, one marshaling site (`Client.post`).
- **Thinking/reasoning replay correctness — the file's crown jewel.** `rawContent` keeps the assistant turn byte-verbatim for tool-loop echo (`client.go:messageResponse`, comment citing the kimi-k3 `thinking.thinking: Field required` 400); pinned by `TestQueryWithToolsReplaysAssistantVerbatim`. None of the peers has a regression pin this precise; grok merely accumulates thinking+signature in its stream layer (`stream/messages.rs:BlockState`).
- **Output-budget policy is a falsifiable ledger.** Per-model initial budget + hard cap from `internal/modelspec:Lookup` (measured values documented); ×2 escalation, ≤3 bumps, `Escalation` structs journaled (`client.go:outputBudget.escalate`); pinned by `TestQueryEscalatesOnMaxTokens`, `TestQueryHardCapReturnsFlaggedPartial`, `TestQueryPerModelInitialBudget`.
- **Truncation split by block type.** `max_tokens` tool_use discarded whole, never executed, re-issued without consuming a round; truncated final answer ships flagged (`QueryWithTools`; pinned by `TestQueryWithToolsTruncationReissuesWholeRound`, `TestQueryWithToolsHardCapSplit`).
- **Fail-loud stop taxonomy.** `classifyStop` whitelists known reasons, logs unknowns, degrades to end_turn; pinned by `TestQueryStopReasonClasses`.
- **Deadline math derived from budget.** 900s floor + `maxTok/120` headroom (`client.go:requestTimeout`), parent ctx still wins; pinned by `TestRequestTimeout*`; the `NewClient` comment explains why no fixed client Timeout.
- **Tool loop auditability.** Round cap 16 with measured justification (`defaultToolRounds` comment); executor errors become `is_error` tool_results; per-call `ToolAudit` with 256-char input cap; pinned by `TestQueryWithToolsDefaultRoundCap`.
- **Credentials hygiene.** Key only from env (`NewClientFromEnv`); errors name the env var, never the value (`oneShot` empty-key branch); caller-side `scrubBaseURL` pinned against userinfo leakage (`internal/ipc/review_test.go:336-338`).
- **Test posture.** 431 lines hermetic `httptest`: headers, path, budgets, escalation ledger, verbatim replay, round cap, deadline math — behavior contracts, not plumbing.

**Weaknesses:**

1. **No retry, ever** (`Client.post` — one `c.HTTP.Do`). A 429/502/reset mid `review_diff` degrades the leg to `needs_fixes` (`internal/ipc/server.go:reviewWithModel`).
2. **No 429/`Retry-After` awareness** — `post` flattens every non-200 into a string with a 512-char tail (`errBodyTail`); status not preserved as data.
3. **Untyped error taxonomy** — all failures `fmt.Errorf` strings; the caller's `Infra` marker is the string-matching workaround.
4. **Input tokens parsed but dropped** — `messageResponse.Usage.InputTokens` decoded, never surfaced in `Result`.
5. **No request correlation** — no attribution header sent, no gateway request-id captured; all three peers ship both (codex `requests/headers.rs`, dsh `attributionHeaders()` + `requestId`).
6. **Buffered whole-body read, no mid-read liveness check** (`io.ReadAll`) — bounded by the ctx deadline, so minor; listed for completeness.

## C. Comparison matrix

| Axis | odo-moa | codex | grok (xai-grok-sampler) | dsh (llm-deepseek + runtime) |
|---|---|---|---|---|
| Request construction | Hand-typed structs, one marshal site; Anthropic only | `codex-api` endpoint/responses + headers (originator, session, timing); Responses + Chat-Completions `WireApi` | 3 protocol serializers + stream transforms | transport-only adapter; BYOK 3-protocol via `llm-pi-ai` catalog routes |
| Auth | `x-api-key` + `anthropic-version` from env | `codex-login` AuthManager, refresh, keyring | `xai-grok-auth`/`secrets` | per-request credential resolver frozen with endpoint snapshot — key+URL never straddle config generations |
| Retry/timeout | **None**; deadline 900s + budget/120 | 4 attempts, 200ms exp, 5xx+transport (`codex-client/retry.rs`); 429 higher up (`core/responses_retry.rs`); stream idle 300s | 14 retries, 2s→30s, ±20% jitter; `Retry-After` capped except 429 (full, ≤2); 525/526 fatal; `x-should-retry` hint; pool-escape attempt | 2 retries, codes `[EMPTY_RESPONSE,RATE_LIMIT,SERVER,TIMEOUT,TRANSPORT]`, 500ms→10s jitter; `Retry-After` honored |
| Streaming | None; buffered; stop_reason classified post-body | SSE first-class + WS v2 prewarm, HTTP fallback per session | SSE, idle timeout, ping-vs-progress discrimination | SSE always; idle watchdog 300s → `TIMEOUT` |
| Error taxonomy | Status-flattened string; typed stop_reason only | `ApiError` enum (Transport/ContextWindow/Quota/Retryable{delay}/RateLimit/…) | `SamplingError` classes + CF 52x taxonomy | `LlmError` codes; quota-vs-429 sniffing |
| Thinking/reasoning | verbatim replay via `rawContent`; journal-only; quirk pinned | effort mapping (`Ultra→Max` at wire); per-turn summary config | per-index accumulation incl. thinking+signature | thinking toggle + effort off/high/max |
| Tools protocol | 16-round cap (measured), is_error results, audit ledger | tools JSON builders, session-wide | delta re-accumulation; doom-loop resampling | handled above adapter, in runtime |
| Output budget | modelspec init+cap; ×2 ≤3; flagged partial | per-provider config; stream-surfaced | `MaxTokensTruncation` fatal by design | profile default 256000, catalog override |
| 429/rate limit | None | `rate_limits.rs`, `Retryable{delay}`, quota distinction | full `Retry-After`, 2-attempt threshold | `RATE_LIMIT` code + `providerRetryAfterMs` |
| Observability | log.Printf + Escalation/ToolAudit journals; output tokens only | OTel/SessionTelemetry/rollout-trace/feedback tags | metrics.rs latency stats, sampling_log, request ids | attribution/user-id/session headers, captured requestId |
| Credentials hygiene | env-name-only errors; URL scrub pinned | keyring, OAuth | env registry | literal keys banned from config |
| Quirk management | comments + tests (kimi replay; glm round cap) | per-provider `ModelProviderInfo` knobs | CF edge handling, pool-escape, should-retry | per-route protocol repointing, auth retained |
| Size/simplicity | **627 LOC, zero deps, one file** | core wrapper 2497 LOC + codex-api/client/login/otel crates | sampler ~150KB / 14 files + http crate | adapter 346 LOC + runtime retry/service + zod schemas |
| Test posture | 431 LOC hermetic; contracts pinned | client SSE/WS e2e, retry tables | retry classification, CF edge, kill switch | scriptable SSE mock server; adapter/header/retry specs |

## D. Gaps worth fixing in moa/client.go

| # | Gap | Peer evidence | Fix sketch | Cost | Wave | Pri | Conf. |
|---|---|---|---|---|---|---|---|
| 1 | No retry on transient failures (429/5xx/transport) | codex `codex-client/src/retry.rs` (4, 200ms exp); grok `retry.rs` (14, jitter, Retry-After); dsh `retry-policy.ts` (2, code allowlist) | Bounded loop around `post`: ≤3 attempts, 500ms×2ⁿ ±20% jitter, retry only 429/5xx/transport/timeouts; honor `Retry-After` ≤30s. POST is a fresh completion each issue — safe to re-issue | S | W1 | P0 | High |
| 2 | No 429 status/`Retry-After` surfaced | dsh `providerRetryAfterMs`; codex `ApiError::Retryable{delay}` | Typed error carrying `Status int`, `RetryAfter`; fanout callers can stagger legs | S | W1/W3 | P1 | High |
| 3 | Untyped error taxonomy; callers string-match | codex `ApiError` enum; dsh `LlmError` codes | `type Error struct{ Kind; Status; Body string }` + `errors.As`; kinds Transport/Timeout/Auth/RateLimit/Server/Invalid; keep current message text | S-M | W1 | P1 | High |
| 4 | Input tokens parsed but dropped; ledger output-only | dsh usage assertions (`inputTokens/outputTokens`); codex `UsageNotIncluded` | Add `InputTokens` to `Result`; journal per leg (cost accounting) | S | W2 | P1 | High |
| 5 | No request-id capture / attribution header | codex originator/session headers; dsh `attributionHeaders()` + requestId | Send `x-odo-client: odo/<ver>`; log gateway request-id if returned | S | any | P2 | Med (gateway support unverified) |
| 6 | Round-cap error discards accumulated tool work (audits returned, no partial text) | none directly — moa's own flagged-partial is the pattern to extend | On cap, return `Result{Text: lastAssistantText, …}` + audits + typed `RoundCapError` | S-M | W3 | P2 | Med |

Not gaps (deliberate, verified): no streaming (journaled one-shots; buffered is correct); escalation re-pays input instead of continuation (kimi signature placeholders make truncated replay invalid — documented at `maxEscalations`); map-built vision body (works, zero-dep). *Wave labels assumed: W1 distill/learner/curator, W2 consolidator, W3 blind legs, W4 hardening.*

## E. What the peers do that moa must NOT copy

| Peer feature | Why it exists there | Why moa must refuse |
|---|---|---|
| codex WS Responses v2 + prewarm + sticky `x-codex-turn-state` (`ModelClientSession`) | Long interactive sessions; turn-scoped server stickiness | moa has no sessions — one POST per completion; a cached sticky token would replay into the wrong turn (their own comment calls that a contract violation), zero payoff through a translating gateway |
| codex OTel/SessionTelemetry/rollout-trace/feedback stack | Fleet-scale product telemetry | odo's contract is the daemon journal + Escalation/ToolAudit ledgers; a metrics pipeline duplicates M18 journaling at 10× the code |
| codex login/keyring/OAuth refresh | ChatGPT subscription auth | static env-var key by design; refresh flows add credential surface for nothing |
| grok 14-retry + HTTP/1.1 pool-escape + `GROK_POOL_*` knobs | hostile CF edge paths inside long streams; poisoned-pool recovery | 3 bounded retries cover moa's transient class; per-request fresh ctx already resets state; pool-escape is a streaming-session remedy |
| grok CF-edge taxonomy, `x-should-retry`, 413 image-strip | xAI's CCP/Cloudflare deployment reality | absorb the *policy shape* (status classes, Retry-After), not vendor 52x/525-526 casework; image-size policy belongs to the caller that read the bytes (ipc journals byte receipts, ADR-0003) |
| grok 3-protocol abstraction (messages/cc/responses) | one sampler serving every upstream | the gateway normalizes upstreams; a protocol layer inside odo re-creates the gateway |
| dsh zod retry-policy schemas, `mode:'always'`, per-request config re-resolution thunks | BYOK plugin host, user-tunable hot config | odo's table is deliberately not prefs (modelspec comment); infinite retry in a daemon hangs user actions; env is re-read per call already |
| dsh `DEFAULT_MAX_TOKENS` 256000 | DeepSeek catalog reality | contradicts measured modelspec budgets (16–64K): would reintroduce the truncations the table was measured to avoid |
| SSE streaming itself (all three) | token-by-token agent UX | moa outputs are journaled, rendered whole; partial-block accumulator machinery for no consumer |

## F. Risks the planned expansions put on this client + mitigations

| Expansion | Load it adds | Client-level risk | Mitigation |
|---|---|---|---|
| Distill/learner/curator on `moa.Query` (off `runOneShot`, `server.go:3502`) | unattended, scheduled, larger prompts; no human retry button | one transient 429/5xx silently kills a cycle (today: degraded verdict; tomorrow: missing memory update) | D1 retry; D3 typed errors — AUTH pages the user, RATE_LIMIT backs off the next cycle |
| ↑ | recurring cost | escalation re-issues whole prompts: worst case 4× input at the 65536 cap; cost invisible because input tokens are dropped | D4 surface `InputTokens`; consider per-call escalation ceiling for distill (`maxEscalations=3` is compile-time) |
| Design-MoA consolidator (single `Query` over 3 proposals) | single point of failure *by construction* | transient error loses the whole synthesis; a `Truncated` partial merge worse than none | D1 retry; caller MUST branch on `Result.Truncated` (client already flags — reuse, don't re-detect); D4 usage ledger |
| Blind legs on `QueryWithTools` scoped to repo root | 3 parallel legs × ≤16 rounds; executor scope moves home→repo | parallel legs triple 429 pressure on one gateway key; `fstools.go` executor is HOME-scoped via `moa_fs_root` — a second scope convention beside it would drift (extend executor with a root parameter, keep the deny-list merge); a 16-round-cap hit returns only error, accumulated tool work lost to the consolidator | D2 Retry-After-aware leg staggering; D6 partial-on-cap; reuse `fsToolExecutor` with root arg |
| All three | aggregate QPS on one gateway + one key | no rate-limit signal at all (D2); no correlation id for gateway-side support (D5) | D2+D5; keep per-action request count in journal (already: Escalations/ToolAudit) |
| Consolidator + legs | deep prompt + big output on one request | 900s + 65536/120 ≈ 1446s worst-case silent wait — acceptable (buffered by design) but must be journaled as elapsed time by callers | none in client; measure real latency once at rollout |

**Confidence notes.** moa claims verified by full read of `client.go`, tests, modelspec, fstools, and call sites. codex retry defaults from `model-provider-info` (DEFAULT_REQUEST_MAX_RETRIES=4, base 200ms, `retry_429:false` at transport layer; 429 handled in `core/responses_retry.rs` — cited, not deep-read). grok retry semantics from `retry.rs`'s behavioral doc header + constants. dsh runtime retry loop body not read; policy defaults and `providerRetryAfterMs` plumbing verified in `retry-policy.ts`/specs; BYOK 3-wire routing verified via `llm-pi-ai` `catalog.spec` rather than adapter source.

Report also written to `out-k3.md`.
izes (ipc already journals byte receipts, ADR-0003) |
| grok 3-protocol abstraction (messages/chat-completions/responses streams) | One sampler serving every upstream | moa's entire edge is that the gateway normalizes upstreams; a protocol abstraction layer inside odo re-creates the gateway |
| dsh zod-validated provider retry-policy schemas, `mode:'always'` infinite retry, per-request config re-resolution thunks | BYOK plugin host; user-tunable settings; hot config reload | odo's policy table is deliberately not user prefs (modelspec doc comment); 'always' retry in a daemon hangs user actions forever; moa reads env per call already — hot reload is free |
| dsh `DEFAULT_MAX_TOKENS` 256000 | DeepSeek's catalog reality | contradicts odo's measured modelspec budgets (16–64K); copying it would reintroduce the truncations the table was measured to avoid |
| SSE streaming itself (all three peers) | Interactive token-by-token UX in agent harnesses | moa outputs are journaled and rendered whole; streaming adds partial-block machinery (grok accumulators) for no consumer |

## F. Risks the planned expansions put on this client + mitigations

| Expansion | Load it adds | Client-level risk | Mitigation (item in D where applicable) |
|---|---|---|---|
| Distill/learner/curator on `moa.Query` (migrating off `runOneShot`, `server.go:3502`) | Unattended, scheduled, larger prompts; no human retry button | A single transient 429/5xx silently kills a distill cycle (today: degraded review verdict; tomorrow: missing memory update) | D1 bounded retry; D3 typed errors so the caller can log-vs-alert (AUTH = page user; RATE_LIMIT = backoff next cycle) |
| ↑ same | Recurring cost | Escalation re-issues whole prompts: worst case 4× input cost on a 65536-cap model (3 escalations); input tokens dropped from ledger so the cost is invisible | D4 surface `InputTokens`; journal per cycle; consider caller-side escalation cap for distill (fixed `maxEscalations=3` is compile-time — a per-call ceiling knob may be needed, keep default) |
| Design-MoA consolidator (single `Query` over 3 proposals) | Single point of failure *by construction* — one POST synthesizes everything | Transient error loses the whole synthesis, not one leg; a `Truncated` partial synthesis is worse than none for a structured merge | D1 retry (covers transient); consolidator caller MUST branch on `Result.Truncated` (client already flags it — reuse, don't re-detect); D4 usage ledger for the synthesis step |
| Design-MoA blind legs on `QueryWithTools` scoped to a repo root | 3 parallel legs × up to 16 rounds of tool traffic; executor scoping moves from home-root to repo-root | Parallel legs triple instantaneous 429 pressure on the shared gateway key — today /panel fans out similarly but interactively; repo-root scoping is executor-side (`internal/ipc/fstools.go` is HOME-scoped with prefs override — a second scope convention beside `moa_fs_root` would drift; extend the executor with a root parameter instead); a leg hitting the 16-round cap returns only an error — the accumulated tool work is lost to the consolidator | D2 Retry-After-aware staggering of legs; D6 partial-result-on-cap; reuse `fsToolExecutor` with a root argument (boring, matches existing deny-list merge) |
| All three | Aggregate QPS against one gateway + one key | No rate-limit headroom signal at all (D2); no correlation id for gateway-side support (D5) | D2+D5; keep total per-action request count visible in the journal (already: `Escalations`, `ToolAudit`) |
| Consolidator + legs together | Deep prompt + big output on one request | 900s floor + 65536/120 headroom ≈ 1446s worst-case silent wait; acceptable but must be journaled as elapsed time by callers (client can't help — buffered by design) | None in client; verifier: measure real consolidator latency once and pin in the wave's rollout notes |

**Confidence notes.** "moa has no retry / no 429 handling / no input-token surfacing": verified
directly against `client.go` (read in full). codex retry defaults: `model-provider-info`
`DEFAULT_REQUEST_MAX_RETRIES=4`, 200ms base, `retry_429:false` at transport layer (429 handled
in `core/responses_retry.rs` — cited, not read in depth). grok retry semantics: read from
`retry.rs` doc header + constants (authoritative comments with behavioral detail). dsh runtime
retry loop body not read; policy defaults and `providerRetryAfterMs` plumbing verified in
`retry-policy.ts`/specs. dsh 3-wire BYOK protocol layer verified via `llm-pi-ai` tests
(`catalog.spec` protocol repointing) rather than its adapter source.
