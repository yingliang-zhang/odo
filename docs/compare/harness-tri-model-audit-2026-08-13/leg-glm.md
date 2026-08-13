Working...
All three scouts delivered comprehensive findings. I now have complete evidence across all axes. Let me compile the final structured report.

## A. Executive summary

Three coding-agent harnesses, all Apache/MIT-licensed, all **closed to external code contributions** — a pattern: "source-available" is the new "open source." `deepseek-harness` (created today, MIT) is a genuinely sophisticated TypeScript Cordis-plugin-architecture harness with real multi-provider neutrality — the "no description, created today" framing is a fresh public unveil of months of internal work, not a dump. `grok-build` (Apache-2.0) is a polished Rust TUI/agent synced from SpaceXAI's private monorepo by a bot; multi-provider BYOK, but system prompts are XOR-obfuscated and contributions are forbidden. `codex` (Apache-2.0, 105k stars) is the most mature and actively developed, with a Guardian LLM-safety reviewer and two-phase memory pipeline — but its wire protocol is now **Responses-API-only** (Chat Completions removed), making it the most vendor-funneled despite nominally supporting Ollama/Bedrock. The most architecturally consequential divergences: deepseek-harness's "everything is a plugin" Cordis composition vs grok-build's leader-follower IPC vs codex's multi-process daemon architecture. For Odo, the highest-value borrows are: codex's Guardian risk-taxonomy review, grok-build's doom-loop detection, codex's two-phase memory pipeline, and codex's sandbox-escalation-on-denial retry — all map cleanly to Odo's existing daemon/adapter/memory layers at S–M cost.

## B. Q1 — Technical comparison

### Comparison table

| Axis | deepseek-harness (`dsh`) | grok-build (`grok`) | codex |
|---|---|---|---|
| **Language** | TypeScript ESM monorepo (pnpm, Node ≥22.19) | Rust workspace (~90 crates) | Rust monorepo (~100 crates, Bazel+Cargo) |
| **Process model** | Web server + React UI (`dsh web`, :3080); one-shot headless (`dsh --profile headless`); ACP server; JSON-RPC SDK server | **Leader-follower IPC**: single leader per machine over Unix socket; TUI/headless/IDE connect as followers; separate workspace daemon | Multi-process: ratatui TUI, headless `codex exec`, JSON-RPC `app-server` (daemon-capable), `exec-server` (remote PTY over Noise relay) |
| **TUI/GUI** | Browser-based React/Vite Web UI (localhost:3080) | Fullscreen ratatui TUI, mouse-interactive, custom forks | ratatui TUI; VS Code extension via app-server; desktop app (`codex app`) |
| **Headless** | `dsh --profile headless "task"` — submits task, prints last assistant text, exits 0/1 | `grok -p "prompt"` — plain/json/streaming-json/streaming-messages-json output | `codex exec --json` JSONL; `--output-schema` (JSON Schema); `--output-last-message` |
| **Structured output** | Snapshot tests (replay); tool catalog generated on boot | `--json-schema` flag; four output formats incl. Messages-API `streaming-messages-json` | `--output-schema` JSON Schema; SDK `outputSchema`; app-server `thread/turn/item` notifications |
| **Agent loop** | Turn = 0+ steps; step = one model request + tools; ReactLoopAgent over Cordis fibers; typed waterfalls (agent/pre-step, tools/pre-execute, etc.); parallel tool calls capped at 10/step | Actor-based sampler (3-layer: client→stream→handle); doom-loop detection; `completionRequirement` orchestration; parallel tool_result blocks | `run_turn` loop: Responses-API sampling → function calls → tool dispatch → assistant message ends turn; parallel tool calls (`FuturesOrdered`); steering (mid-turn input injection) |
| **Tool protocol** | Cordis capability seams (Service Definition/Provider/Consumer); tool registry with pre/guard/around/post pipeline | Tool protocol abstraction (`xai-tool-protocol`); MCP meta-tools `use_tool`/`search_tool` | `ToolRegistry` (namespaced tools); orchestrator: approval→sandbox→attempt→escalated retry; `CoreToolRuntime` trait |
| **File edit** | `write` (full-rewrite) + `str_replace_editor` (unique literal replace/line insert); read-before-edit enforced | `search_replace` (string-replace, primary) + in-tree ports of codex `apply_patch` and opencode `edit`; `grok_build_hashline` (anchored edit) | `apply_patch` (diff/hunk format, NOT full-rewrite); `seek_sequence`; `PreserveLineEndings`/`NormalizeToLf` modes |
| **Shell** | Fresh `bash -c`/`pwsh -Command` per call (no state carryover); persistent PTY `terminal_*` tools | Persistent PTY (`pty_session.rs`); background tasks; per-segment read-only command splitting; 203KB bash tool | `exec.rs` sandboxed spawn (tokio); PTY; `unified_exec` for persistent/background; output cap, timeout (10s default), process-group kill |
| **Sandbox** | Linux bwrap→Landlock (native addon), macOS Seatbelt, Windows ACL restricted-token; E2B remote sandbox POC; fail-closed | Landlock/Seatbelt via `nono`; bubblewrap deny-globs; seccomp child-net (Linux only); **applied once, irreversible**; profiles: off/workspace/devbox/read-only/strict + custom | macOS Seatbelt (.sbpl profiles); Linux Landlock+Bubblewrap (bundled); Windows restricted-token; managed network proxy; **escalated-sandbox retry on denial** |
| **Permission** | `ApprovalPolicy` ask\|never; permission presets (workspace-write+ask, danger-full-access+never) pinned at session creation; no allow-always (deferred) | 6 modes (default/acceptEdits/plan/auto/dontAsk/bypassPermissions); `deny`>`ask`>`allow` rules; remembered grants; per-segment read-only auto-approve list; org enforcement via `requirements.toml` | 3 built-in profiles (`:read-only`/`:workspace-write`/`:danger-full-access`) + custom; `approval_policy` (Ask/Constrained); **Guardian reviewer**; hooks influence permissions; subagents forced to Never |
| **Context/memory** | Compaction as optional capability (model-summarized + model-free tool-result pruner); AGENTS.md/CLAUDE.md chain loader (touch-driven, deduped, byte-budgeted); no cross-session memory store | Auto-compact at 80% context_window; AGENTS.md discovery (cwd→git root); **cross-session memory**: markdown + sqlite-vec vector index, MMR, query expansion, `/dream` consolidation; experimental, off by default | ContextManager (incremental, no rewrite, bounded, hard cap, no item >10K tokens); local + **remote (provider-backed) compaction**; AGENTS.md hierarchy (root→cwd) + `AGENTS.override.md`; **two-phase memory pipeline** (rollout extraction → consolidation), state-DB persisted, injected as dev instructions |
| **Session** | Append-only SessionEvent log; JSONL + SQLite (WAL, monotonic schema version); fork via `ctx.sessions.fork()`; resume with crash-recovery | `~/.grok/sessions/` per-session dirs (summary.json, updates.jsonl, chat_history.jsonl, plan.json, rewind_points); resume/fork/rewind; worktree sessions; Claude session import; `/dashboard` multi-session | `$CODEX_HOME/sessions` + SQLite state DB (migrations: thread_history, queue, goals, logs, memory); resume/fork (app-server `thread/resume`/`thread/fork`); cursor pagination; `rollout-trace` |
| **Extensibility** | **"Everything is a plugin"**: profiles+bundles patch-layer composition (`--dump-config`); MCP client (stdio+HTTP); skills (catalog+filesystem+loader); hook bridges for Claude Code AND Codex; self-modification tools (vm-sandboxed `cordis_define/inspect/run`); human commands | Plugin **marketplace** (git/path sources, trust, auto-update); hooks (shell/HTTP, examples); MCP (rmcp stdio+HTTP); skills; agent/persona/role definition files; slash commands; Claude Code compat | MCP (codex-mcp, rmcp-client, tools/resources/elicitation/OAuth); **typed lifecycle hooks** (JSON-schema contracts); plugins/marketplace; skills (SKILL.md); SDKs (TS/Python); exec policy (`.rules` files); huge layered config (169KB JSON schema) |
| **Model neutrality** | DeepSeek default + pi-ai adapter (OpenAI/Anthropic/gateways/self-hosted, BYOK via apiKeyEnv); can drive Claude Code AND Codex as subagents; attribution headers | Default grok-4.5; 3 API backends (chat_completions/responses/messages); Anthropic/OpenAI/Ollama/Together/any OpenAI-compatible; patched async-openai fork | OpenAI/Bedrock/Ollama/LM Studio + BYOK; **BUT Responses-API only** (Chat Completions removed); default ChatGPT sign-in; OpenAI-protocol-centric |

### Most consequential architectural divergences

**1. Plugin composition vs process architecture.** deepseek-harness treats *everything* as a replaceable Cordis plugin — the agent loop, tool registry, sandbox, approval, even session persistence are swappable via `cordis.yml` patch layers (`docs/architecture.md`, `AGENTS.md`). grok-build and codex both use fixed process architectures (leader-follower vs multi-process daemon). This means dsh's extensibility model is fundamentally more open at the architecture level, while grok-build/codex are more performant and self-contained.

**2. Wire protocol lock-in.** codex removed Chat Completions API support entirely (`CHAT_WIRE_API_REMOVED_ERROR`; `model-provider-info/src/lib.rs`), forcing all providers through OpenAI's Responses API. grok-build supports three backends (chat_completions/responses/messages — `11-custom-models.md`), and deepseek-harness uses separate adapters per provider. This makes codex the most protocol-funneled despite having the most providers listed.

**3. Safety review architecture.** codex has a **Guardian** — an LLM-based auto-reviewer with a detailed risk taxonomy (`core/src/guardian/policy.md`: data exfiltration, credential probing, persistent security weakening, destructive actions) that gates tool actions. grok-build has doom-loop detection (`xai-grok-sampler/src/doom_loop.rs`) but no safety reviewer. deepseek-harness relies on its approval/sandbox pipeline. Odo's MoA fan-out is the most sophisticated review system among all four, but codex's Guardian taxonomy is a borrowable risk-classification scheme.

## C. Q2 — Openness verdicts

### deepseek-harness — **Open-ish**

| Signal | Evidence |
|---|---|
| License | MIT (`LICENSE`) |
| External PRs | Not accepted: "we cannot accept external pull requests at the moment" (`CONTRIBUTING.md:9`) |
| CLA | Absent (no CLA file found) |
| Governance docs | Absent (no GOVERNANCE.md, no RFC process) |
| Commit authors | All DeepSeek employees: `cc.yu@deepseek.com`, `turtle1999@deepseek.com`, `creatixchu@deepseek.com`, `jxiang@deepseek.com` (GitHub API commits). Top contributor: `tianyicui` (5235 contributions). "very small team" (`CONTRIBUTING.md:12`) |
| Created today | Repo created 2026-08-13T11:56:32Z; PR numbers reach #2521 — repo was developed internally then publicly unveiled today. Not a dump: Agent Notes span 2026-06-13→2026-08-12, ~80 package groups, generated catalogs, bilingual docs, Wine Windows gates |
| Plugin ecosystem | Encouraged: `dsh-plugin` GitHub topic for discoverability; "packages created by the community" treated as first-class (`CONTRIBUTING.md:19`) |
| Model openness | Genuinely multi-provider: DeepSeek default + pi-ai adapter (OpenAI/Anthropic/gateways/self-hosted, BYOK). Can even drive Claude Code and Codex as subagent children. Not protocol-locked |
| Discussion | GitHub Discussions enabled; Discord community |

**Verdict:** MIT + plugin-ecosystem encouragement + genuine model neutrality = the most open of the three. But closed contributions, no compat promises, pre-release instability.

### grok-build — **Open-ish**

| Signal | Evidence |
|---|---|
| License | Apache-2.0 (`LICENSE`) |
| External PRs | Explicitly forbidden: "This repository does **not** accept external pull requests or unsolicited patches" (`CONTRIBUTING.md:3-4`) |
| CLA | "No contributor license agreement is offered because external contributions are not accepted" (`CONTRIBUTING.md:18-19`) |
| Governance | Absent; all 28 commits by `grokkybara[bot]` with message "Synced from monorepo" — a one-way sync from SpaceXAI's private monorepo. `SOURCE_REV` file records monorepo SHA. No human commit authors visible in public tree |
| SpaceXAI authenticity | [INFERENCE] Branding consistent: README logo from `media.x.ai`, install from `x.ai/cli`, docs at `docs.x.ai/build`. `xai-org` GitHub org. Cannot verify commit authors directly (shallow clone, bot-authored syncs) — but branding and org membership strongly indicate official first-party release |
| Security | HackerOne (`hackerone.com/x`, `SECURITY.md`) — no public issues |
| Model openness | Multi-provider BYOK: 3 API backends (chat_completions/responses/messages); documented providers: Anthropic, OpenAI, Ollama, Together, any OpenAI-compatible (`11-custom-models.md`). Not protocol-locked |
| Red flag | System prompt **XOR-encrypted at rest** (`prompt_encrypted.rs`, `obfstr` crate) — deliberate obfuscation of the base prompt |

**Verdict:** Source-available, multi-provider, but zero contribution path, monorepo sync, and prompt obfuscation make this "transparency theater" — you can read and build the source, but you cannot influence it or even see the real development process.

### codex — **Funnel**

| Signal | Evidence |
|---|---|
| License | Apache-2.0 (`LICENSE`) |
| External PRs | "External contributions are by invitation only" (`docs/contributing.md:3`); "Pull requests that have not been explicitly invited by a member of the Codex team will be closed without review" (`docs/contributing.md:17`) |
| CLA | Present: `docs/CLA.md` (Individual CLA v1.0, based on ASF CLA v2.2; grants OpenAI perpetual license). CLA-Assistant bot signature required |
| Governance | `.github/CODEOWNERS`: single team `@openai/codex-core-agent-team` owns 8 core paths. No governance/roadmap docs in-repo (all point to `developers.openai.com/codex`). Discussions enabled. 12,477 open issues |
| Commit authors | All OpenAI employees: `tamird@openai.com`, `felipe.coury@openai.com`, `jif@openai.com`, `charliemarsh@openai.com`, `cconger@openai.com`, `owen@openai.com`, `kyleb@openai.com`, etc. (GitHub API). Top contributor: `jif-oai` (1405). PRs merged by `copyberry[bot]` (automated). Active development — multiple commits daily |
| Model openness | Multi-provider listed (OpenAI, Bedrock, Ollama, LM Studio + BYOK via `[model_providers]`), BUT: **wire protocol is Responses-API only** — `WireApi` has only `Responses`; Chat Completions removed (`CHAT_WIRE_API_REMOVED_ERROR`). All providers must speak OpenAI's protocol. Default auth: ChatGPT sign-in. README: "We recommend signing into your ChatGPT account" |
| Build friction | Bazel-on-Cargo: `MODULE.bazel` (18.9KB), `MODULE.bazel.lock` (1.5MB) — high contributor friction even for invited contributors |

**Verdict:** The most mature and active project, but the Responses-API-only wire protocol is a structural funnel to OpenAI's protocol ecosystem. Even Ollama and LM Studio must emulate the Responses API. Combined with ChatGPT-as-default-auth and invitation-only contributions, this is the most vendor-coupled of the three despite having the broadest provider list on paper.

## D. Q3 — Features worth borrowing for Odo (ranked)

### 1. Guardian risk-taxonomy safety review

| Field | Value |
|---|---|
| **Feature** | Guardian LLM-based safety review with risk taxonomy |
| **What it does** | An LLM auto-reviewer evaluates each planned action against a structured risk taxonomy (data exfiltration, credential probing, persistent security weakening, destructive actions), assigning risk levels and allow/deny decisions before execution |
| **Evidence** | `codex:codex-rs/core/src/guardian/policy.md` (full taxonomy); `codex:codex-rs/core/src/guardian/review_session.rs`; `codex:codex-rs/core/src/guardian/approval_request.rs`; PR #38368 "Add the Guardian V2 Luna sampler" (2026-08-13) |
| **Which repo(s)** | codex only. grok-build has doom-loop detection but no safety reviewer; deepseek-harness relies on approval/sandbox pipeline |
| **Map to Odo** | Go daemon orchestrator layer (`internal/ipc/server.go`, `internal/ipc/review.go`). Odo's MoA review already runs N parallel models on diffs; the Guardian taxonomy would give Odo's review panel a structured risk-classification rubric to score against, making the settlement ladder more principled. Maps to the `auto_land_blocked` gate in M16 |
| **Adoption cost** | **M** (1–3 days) — taxonomy is a markdown prompt template; integration needs a daemon-side reviewer call + journal event, but the risk classes map cleanly to Odo's existing protected-path/supply-chain/test-weakening gates |
| **Fit risk** | Low. Odo already does MoA review; this adds a risk-classification layer, not a new system. Risk: the Guardian is a single-reviewer gate, while Odo's philosophy is multi-model consensus — adopting the taxonomy without the single-reviewer bottleneck is the right adaptation |
| **Priority & confidence** | **P0, High** |

### 2. Doom-loop detection with recovery budget

| Field | Value |
|---|---|
| **Feature** | Doom-loop signal detection and mid-stream abort |
| **What it does** | Detects when the model is stuck in a repetitive loop (server-reported doom-loop signals), allocates a recovery budget, and aborts mid-stream once the budget is spent — disarming the abort when recovery is detected |
| **Evidence** | `grok-build:crates/codegen/xai-grok-sampler/src/doom_loop.rs` — "server-reported doom-loop signals with a recovery budget and mid-stream abort" |
| **Which repo(s)** | grok-build only |
| **Map to Odo** | Go daemon adapter layer (`internal/adapter/omp.go`). Odo's adapter tails the OMP JSONL stream; doom-loop detection would sit in the `Events()` tail loop, watching for repeated tool-call patterns. Maps to Odo's long-autonomy pillar — a stuck agent is the #1 threat to unattended runs |
| **Adoption cost** | **S** (<1 day) — pattern detection on the existing event stream + a configurable budget counter + abort signal to the OMP process |
| **Fit risk** | Low. Pure additive safety net; no philosophy conflict. Risk: false positives on legitimate retry loops (test-fix cycles) — needs a tunable threshold |
| **Priority & confidence** | **P1, High** |

### 3. Two-phase memory pipeline (rollout extraction → consolidation)

| Field | Value |
|---|---|
| **Feature** | Two-phase memory: extract structured facts from session rollouts, then consolidate globally |
| **What it does** | At session startup, Phase 1 extracts structured `raw_memory` + `rollout_summary` from past sessions; Phase 2 consolidates across sessions into a global memory store, injected as developer instructions in future sessions |
| **Evidence** | `codex:codex-rs/memories/README.md` — "two-phase pipeline at root-session startup — Phase 1 rollout extraction, Phase 2 global consolidation; stored in the state DB, injected as developer instructions" |
| **Which repo(s)** | codex. grok-build has `/dream` consolidation but it's manual + simpler; deepseek-harness has compaction but no cross-session memory |
| **Map to Odo** | Go daemon memory distiller (`internal/ipc/server.go` memory distiller, M4). Odo already has epoch→wiki→topic promotion, but the two-phase split (extraction vs consolidation as separate passes) and "injected as developer instructions" (not just recall chips) is a different injection surface. Could augment Odo's `memory.md` layer with a structured extraction pass |
| **Adoption cost** | **M** (1–3 days) — Odo already has the distiller; the two-phase split and dev-instruction injection surface are the new parts |
| **Fit risk** | Medium. Odo's memory philosophy is "agents never write memory layers — the distiller harvests from the journal." Codex's pipeline is daemon-driven, which aligns. But "injected as developer instructions" is a different injection surface than Odo's recall chip — need to decide if this replaces or augments the recall chip |
| **Priority & confidence** | **P1, Med** |

### 4. Sandbox-escalation-on-denial retry

| Field | Value |
|---|---|
| **Feature** | Automatic sandbox escalation on tool denial |
| **What it does** | When a tool call is denied by the sandbox, the orchestrator retries with an escalated sandbox profile (e.g., read-only → workspace-write), without re-asking for approval (cached decision) |
| **Evidence** | `codex:codex-rs/core/src/tools/orchestrator.rs` — "approval → select sandbox → attempt → retry with escalated sandbox on denial (no re-approval, cached)" |
| **Which repo(s)** | codex only |
| **Map to Odo** | Odo deliberately has no sandbox ("No sandbox containment" — `README.md:238`). This feature is NOT a fit for Odo's current architecture, but the *pattern* — graceful degradation with cached decisions — maps to Odo's auto-land gate escalation (M16's settlement ladder: protected-path → supply-chain → new-topdir → test-weakening). The "retry with escalation, no re-approval" pattern could streamline the ladder |
| **Adoption cost** | **L** (>3 days) — requires a sandbox layer Odo doesn't have; the escalation *pattern* is borrowable but the sandbox mechanism is not |
| **Fit risk** | High. Odo explicitly defers sandbox containment; adding one contradicts the "agents run in user's environment" design invariant. The escalation pattern is useful but the sandbox prerequisite is a philosophy conflict |
| **Priority & confidence** | **P2, Low** |

### 5. Typed lifecycle hooks with JSON schemas

| Field | Value |
|---|---|
| **Feature** | Lifecycle hooks with typed JSON-schema contracts |
| **What it does** | User-configurable hooks fire on agent lifecycle events (pre/post-tool-use, session-start/end, user-prompt-submit, stop, subagent-start/stop), with typed JSON schemas for payloads and responses. Hooks can block, inject context, rewrite tool input, and make permission decisions |
| **Evidence** | `codex:codex-rs/core/src/hook_runtime.rs`; `codex:codex-rs/hooks/schema/generated/` (typed schemas); `codex:AGENTS.md` mentions hook events |
| **Which repo(s)** | codex (most structured). grok-build has hooks (`xai-grok-hooks`, shell/HTTP, examples in `examples/hooks/`). deepseek-harness has hook bridges for Claude Code/Codex (`packages/hooks/`) |
| **Map to Odo** | Odo's skills layer (`internal/ipc/skills.go`) + adapter layer. Odo already has keyword-matched skill injection; typed hooks would let users add pre/post-tool interception points. The JSON-schema contracts are the borrowable part — they make hooks debuggable and versionable |
| **Adoption cost** | **M** (1–3 days) — define event types, JSON schemas, hook registration path, and execution pipeline in the daemon |
| **Fit risk** | Medium. Odo's philosophy is "the journal is the bedrock" — hooks that inject context or block tools need to journal their effects (Odo already receipts all injections). The risk is hook complexity vs Odo's lightweight ethos. A minimal subset (pre-tool-use block + post-tool-use context) would fit |
| **Priority & confidence** | **P2, Med** |

### 6. Capability seams (Service Definition / Provider / Consumer)

| Field | Value |
|---|---|
| **Feature** | Three-role capability seam pattern for swappable capabilities |
| **What it does** | Every capability (filesystem, shell, LLM, subagent, sandbox) is defined as a seam with three roles: Service Definition (interface), Service Provider (implementation), Consumer (model-facing tool). Swapping one provider changes the whole product without forks |
| **Evidence** | `deepseek-harness:docs/architecture.md` — "A seam is a swappable capability with three roles… one role alone is not a seam"; `docs/capability-seams.md` (generated graph) |
| **Which repo(s)** | deepseek-harness only. grok-build and codex have provider abstractions but not the explicit three-role formalism |
| **Map to Odo** | Odo's adapter layer (`internal/adapter/`) already has a provider abstraction (OMP adapter). The three-role formalism would make Odo's adapter more explicit: define the LLM seam (Service Definition), implement the OMP provider, and let the MoA fan-out be a Consumer. This is an architectural clarification, not a new feature |
| **Adoption cost** | **L** (>3 days) — retrofitting the formalism across Odo's existing adapter/skills/memory layers is a refactor, not an addition |
| **Fit risk** | Medium. Odo's "one durable Go authority" design invariant means the daemon already owns all state; the three-role pattern adds type ceremony. Risk: over-abstraction for a single-user app. But the *concept* — making the adapter seam explicit — is sound |
| **Priority & confidence** | **P2, Med** |

### 7. Cross-session vector memory with MMR ranking

| Field | Value |
|---|---|
| **Feature** | Hybrid vector + full-text memory search with MMR diversity ranking |
| **What it does** | Memory is stored as markdown + SQLite with FTS5 (keyword) and vec0 (semantic) indexes. Search uses weighted hybrid scoring (0.7 vector + 0.3 BM25) with MMR (Maximal Marginal Relevance) for diversity and query expansion. `/dream` consolidates scattered fragments into organized topics |
| **Evidence** | `grok-build:crates/codegen/xai-grok-memory/src/lib.rs` — "sqlite-vec vector index, embeddings, MMR, query expansion"; `13-memory.md` — hybrid scoring weights, source weights, `/dream` consolidation |
| **Which repo(s)** | grok-build. codex has two-phase memory but no vector search; deepseek-harness has no cross-session memory |
| **Map to Odo** | Odo's recall system (`cmd_recall_audit.go`, `internal/ipc/server.go` recall chip). Odo currently uses keyword-matched recall; adding a vector index (sqlite-vec or similar) with MMR would improve recall quality for large journals. Maps to the memory pillar |
| **Adoption cost** | **M** (1–3 days) — add an embedding column to the journal SQLite, compute embeddings on epoch notes, add MMR to the recall query. Odo already uses SQLite |
| **Fit risk** | Medium. Odo's philosophy is "no single growing memory.md" — a vector index over the journal is fine (it's derived, not authoritative). Risk: embedding model dependency adds a provider coupling Odo doesn't currently have. Could use a local embedding model to stay provider-neutral |
| **Priority & confidence** | **P1, Med** |

### 8. "Model-visible ⟺ logged" invariant with deriveMessages()

| Field | Value |
|---|---|
| **Feature** | Runtime invariant: anything reaching a model request must be reconstructable from the session log |
| **What it does** | A runtime assertion that every model-visible input is logged as a session event; `deriveMessages()` projects model history from the append-only log. A new model-visible input requires a new session event, enforced by the type system |
| **Evidence** | `deepseek-harness:docs/architecture.md` — "Model-visible means logged. Anything that reaches a model request must be reconstructable from the log, and a runtime invariant asserts it." `AGENTS.md:107` |
| **Which repo(s)** | deepseek-harness only. Odo has per-layer sha16 receipts but not a runtime assertion |
| **Map to Odo** | Odo's journal (`internal/ipc/server.go` journal, `internal/store/`). Odo already receipts all injected prompt layers with sha16 + byte counts; adding a runtime assertion that every model-visible input is journaled would close the audit loop. Maps to the journal pillar |
| **Adoption cost** | **S** (<1 day) — add a daemon-side assertion that checks every outgoing prompt against journaled events before sending. Odo already has the receipt infrastructure |
| **Fit risk** | Low. This is a strict strengthening of Odo's existing receipt system. No philosophy conflict — it makes the "if it's not in the log, it didn't happen" principle *enforced* rather than aspirational |
| **Priority & confidence** | **P0, High** |

### 9. Permission presets bundling sandbox + approval

| Field | Value |
|---|---|
| **Feature** | One-click permission preset bundling sandbox mode + approval policy |
| **What it does** | A named preset bundles a sandbox mode and an approval policy into a single user-facing selector, pinned at session creation (e.g., "workspace-write + ask" or "danger-full-access + never") |
| **Evidence** | `deepseek-harness:packages/interaction/permission-presets/README.md`; `grok-build:22-permissions-and-safety.md` (6 modes); `codex:codex-rs/core/src/config/permissions.rs` (3 built-in profiles) |
| **Which repo(s)** | All three, with different granularities. deepseek-harness has the cleanest preset model; grok-build has the most modes; codex has the simplest |
| **Map to Odo** | Odo's settings panel (`gui/` settings). Odo currently has per-run model/adapter selection but no permission preset. A preset like "research (read-only, no auto-land)" vs "autonomous (workspace-write, auto-land: main)" would make Odo's autonomy levels a single selector. Maps to the M16 auto-land pref-gating |
| **Adoption cost** | **S** (<1 day) — define 2–3 presets in `prefs.md`, wire to the existing auto-land + MoA prefs |
| **Fit risk** | Low. Odo already has `auto_apply: main` as a pref; bundling it with MoA review on/off and read-only mode into a named preset is pure UX improvement |
| **Priority & confidence** | **P1, High** |

### 10. Subagent personas with I/O contracts

| Field | Value |
|---|---|
| **Feature** | Personas declaring expected inputs and outputs for subagent chaining |
| **What it does** | A persona (behavioral overlay for a subagent) can declare typed input/output contracts (name, io_type, required, description). The parent agent reads these to know what context to supply and what artifacts to expect, enabling persona chaining (one persona's output file → next persona's input) |
| **Evidence** | `grok-build:crates/codegen/xai-grok-pager/docs/user-guide/16-subagents.md` — persona I/O contract fields + chaining example |
| **Which repo(s)** | grok-build only |
| **Map to Odo** | Odo's planned "Design-MoA for nontrivial tasks" (README.md:55: "3 blind design proposals → consolidator → journaled DESIGN LOCK → single implementer → existing MoA review"). Persona I/O contracts would formalize the handoff between the design-proposal agents and the implementer. Maps to the orchestrator pillar |
| **Adoption cost** | **M** (1–3 days) — define persona schema with I/O fields in `prefs.md`/skills, wire to the MoA fan-out to declare what each leg produces/consumes |
| **Fit risk** | Medium. Odo's MoA is a diff-review system, not a multi-step pipeline — the I/O contract pattern is more useful for the planned Design-MoA flow than for the current MoA review. Risk: premature formalism for a flow that's still on the roadmap |
| **Priority & confidence** | **P2, Med** |

## E. Surprises / red flags

| # | Finding | Evidence |
|---|---|---|
| 1 | **All three are closed to external code contributions.** Despite MIT/Apache licenses, none accept unsolicited PRs. This is a pattern: "source-available" has replaced "open source" for vendor-backed agent harnesses. deepseek-harness: "cannot accept external pull requests at the moment"; grok-build: "does not accept external pull requests or unsolicited patches"; codex: "External contributions are by invitation only" | `CONTRIBUTING.md` / `docs/contributing.md` in each repo |
| 2 | **deepseek-harness created TODAY is real.** The "no description, created 2026-08-13" framing initially suggests a dump. But: 2500+ PR numbers, Agent Notes spanning Jun–Aug 2026, ~80 package groups, generated catalogs, Wine Windows CI gates, bilingual EN/ZH docs, snapshot replay tests. It's a fresh public unveil of months of internal work — the single shallow-commit clone is just the squash point | GitHub API (created_at, PR numbers), `.agents/notes/` dates, AGENTS.md pre-release banner |
| 3 | **grok-build XOR-encrypts its system prompt at rest.** `prompt_encrypted.rs` uses position-dependent XOR key + `obfstr` crate. This is obfuscation, not security — it prevents users from reading the base prompt that shapes agent behavior. A transparency-inhibiting choice for an "open source" project | `grok-build:crates/codegen/xai-grok-agent/src/prompt/prompt_encrypted.rs` |
| 4 | **codex removed Chat Completions API entirely.** `WireApi` has only `Responses`; `CHAT_WIRE_API_REMOVED_ERROR` is the error for the old path. Even Ollama/LM Studio must speak the Responses API. This is a structural funnel masquerading as multi-provider support | `codex:codex-rs/model-provider-info/src/lib.rs`; `codex:codex-rs/core/src/client.rs` |
| 5 | **grok-build's entire public commit history is a bot.** All 28 commits by `grokkybara[bot]` with message "Synced from monorepo." No human author, no PR review process visible. Development happens in a private monorepo; the public tree is a periodic dump. This is "source transparency" without "development transparency" | GitHub API contributors (single bot, 28 contributions); `SOURCE_REV` file |
| 6 | **deepseek-harness can drive codex AND Claude Code as subagents.** The `packages/subagent/` family includes `subagent-codex` and `subagent-claude-code` providers — the harness wraps its competitors' agents as child workers. This is a striking interoperability play for a DeepSeek-branded product | `deepseek-harness:packages/subagent/subagent-codex/`, `subagent-claude-code/`; `docs/subsystems/subagent.md` |
| 7 | **codex's `docs/` directory is all stubs.** Every markdown file (sandbox.md, exec.md, config.md, etc.) is 1–3 lines redirecting to `developers.openai.com/codex/...`. The real documentation lives behind OpenAI's developer portal, not in the repo. Combined with Bazel-on-Cargo build infra (1.5MB lockfile), this raises contributor friction deliberately | `codex:docs/sandbox.md` (150B), `codex:docs/exec.md` (146B), etc. |
| 8 | **grok-build ports code from both codex and opencode.** `THIRD-PARTY-NOTICES` (744KB) credits "in-tree source ports (including openai/codex and sst/opencode tool implementations)" — grok-build literally incorporated its competitors' tool code | `grok-build:README.md:135-136`; `grok-build:THIRD-PARTY-NOTICES` |

## F. What Odo already does BETTER than all three

| # | What Odo does better | Evidence |
|---|---|---|
| 1 | **MoA fan-out with N parallel models on diffs.** None of the three run multiple models in parallel for review. codex's Guardian is a single-reviewer safety gate; grok-build has no review system; deepseek-harness has no MoA. Odo's `review_action` event journals one MoA run as a single review event, with Accept/Reject diff lanes — this is the most sophisticated review architecture among all four projects | Odo `README.md:80`: "MoA review: run a diff through N parallel models, results journal as one review_action event" |
| 2 | **Three-tier skill gating with MoA review.** deepseek-harness has a skill catalog + loader but no gating; codex has SKILL.md skills with scopes but no auto-discard/human-gate/auto-accept tiers; grok-build has skills but no gating. Odo's auto-discard / human-gate / auto-accept with MoA review is unique | Odo `README.md:79`: "Skill distillation: learner proposes skills… three-tier gating (auto-discard / human-gate / auto-accept) with MoA review" |
| 3 | **Settlement ladder / earned-autonomy ratchet.** None of the three have a graduated autonomy system. codex's Guardian is binary (allow/deny); grok-build has permission modes but no autonomy progression; deepseek-harness has presets but no streak-based escalation. Odo's `odo autonomy audit` (per-class accept streaks, rung-0 instrumentation) and M16 auto-land (unanimous-panel landing with mechanical gates) are the only earned-autonomy system among all four | Odo `README.md:98`: "Orchestrator: verdicts → autonomy; earned-autonomy instrumentation at rung 0"; `cmd_autonomy_audit.go` |
| 4 | **Receipt-anchored, attributable prompt injection.** Odo journals per-layer sha16 receipts + `total_prompt_bytes` + `dropped_seqs` for every sent prompt, with cap boundaries as actionable markers. deepseek-harness has "model-visible ⟺ logged" (close, but no per-layer byte receipts); codex has ContextManager with bounded items but no receipt system; grok-build has no prompt-attribution system. Odo's audit trail for "what did run N see?" is the most complete | Odo `README.md:272-274`: "Every sent prompt carries per-layer sha16 receipts plus total_prompt_bytes and dropped_seqs, with cap boundaries rendered as actionable markers" |
| 5 | **6-layer memory architecture with one-way promotion.** Odo's journal → epoch notes → topic pages → memory.md → user.md → ledger.md with one-way promotion and contradiction detection + retraction is more structured than any of the three. grok-build has flat MEMORY.md + session logs + `/dream`; codex has two-phase extraction but flat storage; deepseek-harness has no cross-session memory. Odo's "agents never write any memory layer — the distiller harvests from the journal" is a stricter invariant than any competitor's | Odo `README.md:258-270`: 6-layer table + "agents never write any memory layer"; `ADR-0003` |
